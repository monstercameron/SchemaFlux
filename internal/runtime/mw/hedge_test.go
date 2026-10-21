package mw

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// slowThenFast answers its calls after the delays it is given, in the order
// the calls arrive, and records how each call ended.
type slowThenFast struct {
	delays []time.Duration
	fails  []error

	mu        sync.Mutex
	calls     int
	cancelled atomic.Int32
}

func (p *slowThenFast) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.mu.Lock()
	n := p.calls
	p.calls++
	p.mu.Unlock()

	delay := p.delays[min(n, len(p.delays)-1)]
	select {
	case <-time.After(delay):
	case <-ctx.Done():
		p.cancelled.Add(1)
		return llm.CompletionResponse{}, ctx.Err()
	}
	if n < len(p.fails) && p.fails[n] != nil {
		return llm.CompletionResponse{}, p.fails[n]
	}
	return llm.CompletionResponse{Content: "answer " + string(rune('1'+n)), Model: req.Model, Provider: "fake", FinishReason: "stop"}, nil
}
func (p *slowThenFast) Name() string                               { return "fake" }
func (p *slowThenFast) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *slowThenFast) RetryPolicy() (int, time.Duration)          { return 0, 0 }
func (p *slowThenFast) made() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// A request that has not answered within a set time is sent again, and
// whichever answers first is used. The second copy is only sent for the slow
// ones, so it costs the slow tail and not the whole.
//
// Each case runs in a synctest bubble, where the provider's delays are waits on
// a fake clock, so the time a call takes is known exactly: the copy that wins
// answers its delay after it was sent, and the second copy is sent after the
// hedge's delay.
func TestHedgeSendsASecondCopyOfASlowRequest(t *testing.T) {
	const after = 100 * time.Millisecond
	const slow, fast = 3 * time.Second, 5 * time.Millisecond
	down := errors.New("the provider failed")
	other := errors.New("the second copy failed")

	cases := []struct {
		name     string
		delays   []time.Duration
		fails    []error
		want     string // the answer, "" for an error
		wantErr  error
		calls    int
		hedged   bool
		took     time.Duration // how long the call takes, from the start of the request
		canceled int32
	}{
		{"a quick answer is sent once", []time.Duration{fast}, nil, "answer 1", nil, 1, false, fast, 0},
		{"a slow answer: the second copy wins", []time.Duration{slow, fast}, nil, "answer 2", nil, 2, true, after + fast, 1},
		{"both slow: the first, which started earlier, wins", []time.Duration{250 * time.Millisecond, slow}, nil, "answer 1", nil, 2, true, 250 * time.Millisecond, 1},
		{"a quick failure is returned and not hedged", []time.Duration{fast}, []error{down}, "", down, 1, false, fast, 0},
		{"the first fails after the second copy went out, and the second answers", []time.Duration{250 * time.Millisecond, 300 * time.Millisecond}, []error{down}, "answer 2", nil, 2, true, after + 300*time.Millisecond, 0},
		{"the second copy fails and the first answers", []time.Duration{500 * time.Millisecond, fast}, []error{nil, other}, "answer 1", nil, 2, true, 500 * time.Millisecond, 0},
		{"both fail: the first one's error", []time.Duration{250 * time.Millisecond, fast}, []error{down, other}, "", down, 2, true, 250 * time.Millisecond, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				provider := &slowThenFast{delays: tc.delays, fails: tc.fails}
				var events []HedgeEvent
				var eventsMu sync.Mutex
				hedged := Chain(provider, Hedge(after, WithHedgeObserver(func(event HedgeEvent) {
					eventsMu.Lock()
					events = append(events, event)
					eventsMu.Unlock()
				})))

				started := time.Now()
				resp, err := hedged.Complete(context.Background(), llm.CompletionRequest{Model: "m"})
				took := time.Since(started)

				if !errors.Is(err, tc.wantErr) || resp.Content != tc.want {
					t.Fatalf("resp %q, err %v; want %q, %v", resp.Content, err, tc.want, tc.wantErr)
				}
				if provider.made() != tc.calls {
					t.Errorf("%d calls were made, want %d", provider.made(), tc.calls)
				}
				if took != tc.took {
					t.Errorf("the call took %v, want %v", took, tc.took)
				}
				eventsMu.Lock()
				defer eventsMu.Unlock()
				if len(events) != 1 || events[0].Hedged != tc.hedged {
					t.Fatalf("events = %+v, want one with Hedged = %v", events, tc.hedged)
				}
				if tc.want == "answer 2" && !events[0].SecondWon {
					t.Error("the event does not say the second copy won")
				}
				if tc.want == "answer 1" && events[0].SecondWon {
					t.Error("the event says the second copy won, and the first did")
				}
				// The copy that lost is cancelled. Once every goroutine of the bubble
				// is blocked the copy has noticed, or never will.
				synctest.Wait()
				if got := provider.cancelled.Load(); got != tc.canceled {
					t.Errorf("%d copies were cancelled, want %d", got, tc.canceled)
				}
			})
		})
	}

	// An answer that is ready exactly as the delay passes: the provider's timer
	// and the hedge's own timer fire at the same fake instant, and which of them
	// the request sees first is the runtime's to choose. Both outcomes are right
	// and the test names them: either the request is answered by the first copy
	// alone, or the second copy was sent in the same instant and was cancelled
	// when the first copy's answer was taken. The first copy's answer is the
	// answer, and the request takes the delay, in both.
	t.Run("an answer exactly as the delay passes is still one answer", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			provider := &slowThenFast{delays: []time.Duration{after}}
			var events []HedgeEvent
			var eventsMu sync.Mutex
			hedged := Chain(provider, Hedge(after, WithHedgeObserver(func(event HedgeEvent) {
				eventsMu.Lock()
				events = append(events, event)
				eventsMu.Unlock()
			})))

			started := time.Now()
			resp, err := hedged.Complete(context.Background(), llm.CompletionRequest{Model: "m"})
			took := time.Since(started)
			synctest.Wait()

			if err != nil || resp.Content != "answer 1" {
				t.Fatalf("resp %q, err %v; want the first copy's answer", resp.Content, err)
			}
			if took != after {
				t.Errorf("the call took %v, want %v", took, after)
			}
			eventsMu.Lock()
			defer eventsMu.Unlock()
			if len(events) != 1 || events[0].SecondWon {
				t.Fatalf("events = %+v, want one that the second copy did not win", events)
			}
			calls, cancelled := provider.made(), provider.cancelled.Load()
			switch {
			case calls == 1 && !events[0].Hedged && cancelled == 0:
				// The request saw the first copy's answer first.
			case calls == 2 && events[0].Hedged && cancelled == 1:
				// The request saw its own timer first.
			default:
				t.Errorf("%d calls, %d cancelled, events %+v: want one call, not hedged, or two calls, hedged, the second cancelled", calls, cancelled, events)
			}
		})
	})
}

// What the caller's context does, and what a hedge is not given.
func TestHedgeFollowsTheCallersContext(t *testing.T) {
	t.Run("the caller cancels while both copies are out", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			provider := &slowThenFast{delays: []time.Duration{time.Hour}}
			ctx, cancel := context.WithCancel(context.Background())
			// The second copy is sent at 20 ms and the caller goes away at 30 ms, a
			// later instant of the bubble's clock, so both copies are out; that they
			// are is asserted as the caller goes.
			time.AfterFunc(30*time.Millisecond, func() {
				if made := provider.made(); made != 2 {
					t.Errorf("%d copies were out when the caller went away, want 2", made)
				}
				cancel()
			})
			started := time.Now()
			_, err := Chain(provider, Hedge(20*time.Millisecond)).Complete(ctx, llm.CompletionRequest{})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want the caller's cancellation", err)
			}
			if took := time.Since(started); took != 30*time.Millisecond {
				t.Errorf("the call took %v, want it to end as the caller went away, at 30ms", took)
			}
			synctest.Wait()
			if provider.cancelled.Load() != 2 {
				t.Errorf("%d of the two copies were cancelled", provider.cancelled.Load())
			}
		})
	})

	t.Run("a context that is already over sends nothing twice", func(t *testing.T) {
		provider := &slowThenFast{delays: []time.Duration{time.Hour}}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := Chain(provider, Hedge(time.Millisecond)).Complete(ctx, llm.CompletionRequest{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
		if provider.made() > 1 {
			t.Errorf("%d calls were made on a context that was over", provider.made())
		}
	})

	t.Run("no delay means no hedge", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			for _, after := range []time.Duration{0, -time.Second} {
				provider := &slowThenFast{delays: []time.Duration{80 * time.Millisecond}}
				started := time.Now()
				resp, err := Chain(provider, Hedge(after)).Complete(context.Background(), llm.CompletionRequest{})
				if err != nil || resp.Content != "answer 1" || provider.made() != 1 {
					t.Errorf("after %v: resp %q, err %v, %d calls", after, resp.Content, err, provider.made())
				}
				if took := time.Since(started); took != 80*time.Millisecond {
					t.Errorf("after %v: the call took %v, want the provider's 80ms", after, took)
				}
			}
		})
	})

	// Fifty requests that each answer in 5 ms, well inside the delay, are fifty
	// calls. On the bubble's clock none of them can be late, where on a real one
	// a request held up for a second by a busy machine was sent twice.
	t.Run("many at once", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			provider := &slowThenFast{delays: []time.Duration{5 * time.Millisecond}}
			hedged := Chain(provider, Hedge(time.Second))
			started := time.Now()
			var wait sync.WaitGroup
			for range 50 {
				wait.Add(1)
				go func() {
					defer wait.Done()
					if _, err := hedged.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
						t.Errorf("Complete: %v", err)
					}
				}()
			}
			wait.Wait()
			if provider.made() != 50 {
				t.Errorf("%d calls for fifty quick requests", provider.made())
			}
			if took := time.Since(started); took != 5*time.Millisecond {
				t.Errorf("fifty requests at once took %v, want the 5ms of one", took)
			}
		})
	})
}

// What Hedge does when a copy panics, when the observer does, when the
// caller's context ends, and where it stands in a chain.

// scriptedProvider is a provider whose calls each do what a function says, so
// that a test decides what the first and the second copy do, and sees when they
// started.
type scriptedProvider struct {
	steps []func(ctx context.Context) (llm.CompletionResponse, error)

	mu    sync.Mutex
	calls int
}

func newScript(steps ...func(ctx context.Context) (llm.CompletionResponse, error)) *scriptedProvider {
	return &scriptedProvider{steps: steps}
}

func (s *scriptedProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	s.mu.Lock()
	n := s.calls
	s.calls++
	s.mu.Unlock()
	return s.steps[min(n, len(s.steps)-1)](ctx)
}
func (s *scriptedProvider) Name() string                               { return "fake" }
func (s *scriptedProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (s *scriptedProvider) RetryPolicy() (int, time.Duration)          { return 0, 0 }
func (s *scriptedProvider) made() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// waitsFor waits until the provider has been called n times.
func waitsFor(t *testing.T, s *scriptedProvider, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for s.made() < n {
		if time.Now().After(deadline) {
			t.Fatalf("the provider was called %d times, want %d", s.made(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

func answersWith(content string) func(context.Context) (llm.CompletionResponse, error) {
	return func(context.Context) (llm.CompletionResponse, error) {
		return llm.CompletionResponse{Content: content}, nil
	}
}
func failsWith(err error) func(context.Context) (llm.CompletionResponse, error) {
	return func(context.Context) (llm.CompletionResponse, error) { return llm.CompletionResponse{}, err }
}
func panicsWith(value any) func(context.Context) (llm.CompletionResponse, error) {
	return func(context.Context) (llm.CompletionResponse, error) { panic(value) }
}
func hangs(ctx context.Context) (llm.CompletionResponse, error) {
	<-ctx.Done()
	return llm.CompletionResponse{}, ctx.Err()
}

// gate holds a step until the test lets it go.
func gate(release <-chan struct{}, then func(context.Context) (llm.CompletionResponse, error)) func(context.Context) (llm.CompletionResponse, error) {
	return func(ctx context.Context) (llm.CompletionResponse, error) {
		select {
		case <-release:
			return then(ctx)
		case <-ctx.Done():
			return llm.CompletionResponse{}, ctx.Err()
		}
	}
}

// A provider that panics is an error to the caller of a hedged request, as it
// would be to the caller of one that was not: the copy runs in a goroutine of
// the library's own, where nobody can recover it, and it took the process down.
func TestHedgeTurnsACopysPanicIntoAnError(t *testing.T) {
	secret := "the caller's data is in this panic"
	type steps = []func(context.Context) (llm.CompletionResponse, error)
	cases := []struct {
		name    string
		steps   func(release <-chan struct{}) steps
		want    string // the answer, "" for an error
		wantErr bool
	}{
		{"the first copy panics at once", func(<-chan struct{}) steps { return steps{panicsWith(secret)} }, "", true},
		{"the first copy panics with an error value", func(<-chan struct{}) steps { return steps{panicsWith(errors.New(secret))} }, "", true},
		{"the first copy panics with nil", func(<-chan struct{}) steps { return steps{panicsWith(nil)} }, "", true},
		{"the second copy panics and the first answers", func(release <-chan struct{}) steps {
			return steps{gate(release, answersWith("first")), panicsWith(secret)}
		}, "first", false},
		{"the first copy panics after the second went out, and the second answers", func(release <-chan struct{}) steps {
			return steps{gate(release, panicsWith(secret)), answersWith("second")}
		}, "second", false},
		{"both copies panic", func(release <-chan struct{}) steps {
			return steps{gate(release, panicsWith(secret)), panicsWith(secret)}
		}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				release := make(chan struct{})
				provider := newScript(tc.steps(release)...)
				hedged := Chain(provider, Hedge(20*time.Millisecond))
				done := make(chan struct{})
				var resp llm.CompletionResponse
				var err error
				go func() {
					defer close(done)
					resp, err = hedged.Complete(context.Background(), llm.CompletionRequest{})
				}()
				// The gated copy is let go once the second one is out. Closing the
				// channel lets it go whenever it gets to the gate.
				if len(tc.steps(release)) > 1 {
					waitsFor(t, provider, 2)
					close(release)
				}
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					t.Fatal("the call did not return")
				}
				if tc.wantErr {
					if err == nil {
						t.Fatalf("answer %q for a provider that panicked", resp.Content)
					}
					var op *types.OperationError
					if !errors.As(err, &op) || op.Kind != types.KindInvariantViolation {
						t.Errorf("the error is %T %v, want an OperationError that is an invariant violation, as a step that panicked is", err, err)
					}
					if strings.Contains(err.Error(), secret) {
						t.Errorf("the panic's value is in the error: %v", err)
					}
					if types.NarrowingOf(err) != types.NarrowPanic {
						t.Errorf("the error narrows to %v, want a panic", types.NarrowingOf(err))
					}
					if want := "hedge: invariant violation: a request panicked"; err.Error() != want {
						t.Errorf("the error reads %q, want %q", err.Error(), want)
					}
					return
				}
				if err != nil || resp.Content != tc.want {
					t.Errorf("resp %q, err %v; want %q", resp.Content, err, tc.want)
				}
			})
		})
	}
}

// An observer that panics, or blocks, is the caller's own affair and does not
// take the call's answer with it.
func TestHedgeSurvivesAnObserverThatPanics(t *testing.T) {
	observers := map[string]func(HedgeEvent){
		"a panic":            func(HedgeEvent) { panic("the observer failed") },
		"a panic with nil":   func(HedgeEvent) { panic(nil) },
		"a panic with error": func(HedgeEvent) { panic(errors.New("boom")) },
	}
	for name, observe := range observers {
		for _, hedge := range []bool{false, true} {
			label := name + ", quick answer"
			steps := []func(context.Context) (llm.CompletionResponse, error){answersWith("answer")}
			if hedge {
				label = name + ", hedged"
				steps = []func(context.Context) (llm.CompletionResponse, error){hangs, answersWith("answer")}
			}
			t.Run(label, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					resp, err := Chain(newScript(steps...), Hedge(20*time.Millisecond, WithHedgeObserver(observe))).
						Complete(context.Background(), llm.CompletionRequest{})
					if err != nil || resp.Content != "answer" {
						t.Errorf("resp %q, err %v: the observer's panic reached the call", resp.Content, err)
					}
				})
			})
		}
	}

	t.Run("a nil observer and a nil option are no observer", func(t *testing.T) {
		resp, err := Chain(newScript(answersWith("answer")), Hedge(20*time.Millisecond, WithHedgeObserver(nil), nil)).
			Complete(context.Background(), llm.CompletionRequest{})
		if err != nil || resp.Content != "answer" {
			t.Errorf("resp %q, err %v", resp.Content, err)
		}
	})

	t.Run("the observer is told once, and may be told about many requests at once", func(t *testing.T) {
		var told atomic.Int32
		hedged := Chain(newScript(answersWith("answer")), Hedge(time.Second, WithHedgeObserver(func(HedgeEvent) { told.Add(1) })))
		var wait sync.WaitGroup
		for range 20 {
			wait.Add(1)
			go func() {
				defer wait.Done()
				_, _ = hedged.Complete(context.Background(), llm.CompletionRequest{})
			}()
		}
		wait.Wait()
		if told.Load() != 20 {
			t.Errorf("the observer was told %d times for 20 requests", told.Load())
		}
	})
}

// A copy that has failed with a real error before the caller's context ends is
// the reason for the failure, and a bare cancellation hides it: a rate limit
// from the first copy and a deadline became a timeout, which is classified
// differently and retried differently.
func TestHedgeReturnsARealErrorWhenTheContextEndsAfterIt(t *testing.T) {
	limited := errors.New("the provider said too many requests")
	other := errors.New("the second copy failed")
	cases := []struct {
		name  string
		steps []func(context.Context) (llm.CompletionResponse, error)
		// The first copy's step is gated and let go by the test; the second hangs
		// or fails at once.
		want error
	}{
		{"the first copy failed and the second hangs", []func(context.Context) (llm.CompletionResponse, error){nil, hangs}, limited},
		{"the first copy failed and the second failed too: the first's", []func(context.Context) (llm.CompletionResponse, error){nil, failsWith(other)}, limited},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				release := make(chan struct{})
				returned := make(chan struct{})
				gated := gate(release, failsWith(limited))
				firstCopy := func(ctx context.Context) (llm.CompletionResponse, error) {
					resp, err := gated(ctx)
					close(returned)
					return resp, err
				}
				steps := append([]func(context.Context) (llm.CompletionResponse, error){firstCopy}, tc.steps[1:]...)
				provider := newScript(steps...)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan error, 1)
				go func() {
					_, err := Chain(provider, Hedge(10*time.Millisecond)).Complete(ctx, llm.CompletionRequest{})
					done <- err
				}()
				waitsFor(t, provider, 2)
				close(release)
				// The failure reaches the hedge's channel as the first copy's goroutine
				// returns from the step. When every other goroutine of the bubble is
				// blocked, the hedge has taken it and is waiting again, and only then
				// does the caller give up.
				waitOrFail(t, returned, "the first copy to fail")
				synctest.Wait()
				cancel()
				select {
				case err := <-done:
					if !errors.Is(err, tc.want) {
						t.Errorf("err = %v, want the copy's own failure", err)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("the call did not return")
				}
			})
		})
	}

	t.Run("no copy failed: the caller's own cancellation", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			provider := newScript(hangs)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				_, err := Chain(provider, Hedge(10*time.Millisecond)).Complete(ctx, llm.CompletionRequest{})
				done <- err
			}()
			waitsFor(t, provider, 2)
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Errorf("err = %v, want the cancellation", err)
			}
		})
	})

	t.Run("a deadline that passes before the delay", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			provider := newScript(hangs)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
			defer cancel()
			started := time.Now()
			_, err := Chain(provider, Hedge(10*time.Second)).Complete(ctx, llm.CompletionRequest{})
			if !errors.Is(err, context.DeadlineExceeded) || provider.made() != 1 {
				t.Errorf("err = %v after %d calls", err, provider.made())
			}
			if took := time.Since(started); took != 5*time.Millisecond {
				t.Errorf("the call took %v, want it to end with the deadline, at 5ms", took)
			}
		})
	})
}

// When the caller's context is over and the timer has fired too, a select picks
// at random, and the second copy went out on a context that was dead. The timer
// is set to fire before the select looks, so that both are ready every time.
func TestHedgeSendsNoSecondCopyOnAContextThatIsOver(t *testing.T) {
	for i := range 200 {
		provider := newScript(hangs)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := Chain(provider, Hedge(time.Nanosecond)).Complete(ctx, llm.CompletionRequest{})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("run %d: err = %v", i, err)
		}
		// A second copy, if one was sent, is started on a goroutine of its own.
		yield()
		if made := provider.made(); made > 1 {
			t.Fatalf("run %d: %d calls were made on a context that was over", i, made)
		}
	}
}

// Where Hedge goes in a chain. The first middleware listed is the outermost, so
// a cache listed before Hedge is outside it and a hit never reaches it, and a
// middleware listed after Hedge sees both copies of a request that was hedged.
func TestHedgeInAChain(t *testing.T) {
	// counter counts the requests that reach it.
	counter := func(n *atomic.Int32) Middleware {
		return func(next Handler) Handler { return &countingHandler{next: next, n: n} }
	}
	slowThenQuick := func() *scriptedProvider { return newScript(hangs, answersWith("answer")) }

	t.Run("a cache outside Hedge: the second identical request is a hit and is not hedged", func(t *testing.T) {
		provider := newScript(answersWith("answer"))
		var events atomic.Int32
		chain := Chain(provider, Cache(), Hedge(time.Hour, WithHedgeObserver(func(HedgeEvent) { events.Add(1) })))
		for range 3 {
			if _, err := chain.Complete(context.Background(), llm.CompletionRequest{Model: "m", UserPrompt: "u"}); err != nil {
				t.Fatalf("Complete: %v", err)
			}
		}
		if provider.made() != 1 || events.Load() != 1 {
			t.Errorf("%d provider calls and %d requests that reached Hedge for three identical requests", provider.made(), events.Load())
		}
	})

	t.Run("a counter after Hedge sees both copies of a hedged request", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var after, before atomic.Int32
			provider := slowThenQuick()
			chain := Chain(provider, counter(&before), Hedge(10*time.Millisecond), counter(&after))
			if resp, err := chain.Complete(context.Background(), llm.CompletionRequest{}); err != nil || resp.Content != "answer" {
				t.Fatalf("resp %q, err %v", resp.Content, err)
			}
			if before.Load() != 1 || after.Load() != 2 {
				t.Errorf("a counter before Hedge saw %d and one after saw %d, want 1 and 2", before.Load(), after.Load())
			}
		})
	})

	t.Run("a counter after Hedge sees one copy of a request that was quick", func(t *testing.T) {
		var after atomic.Int32
		chain := Chain(newScript(answersWith("answer")), Hedge(time.Hour), counter(&after))
		if _, err := chain.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if after.Load() != 1 {
			t.Errorf("a counter after Hedge saw %d", after.Load())
		}
	})

	t.Run("a retry outside Hedge hedges each attempt", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var after atomic.Int32
			boom := llm.NewAPIError("fake", "m", 503, "unavailable")
			provider := newScript(failsWithAfter(30*time.Millisecond, boom), failsWith(boom), hangs, answersWith("answer"))
			chain := Chain(provider, Retry(WithMaxAttempts(2), WithBaseDelay(time.Millisecond)), Hedge(10*time.Millisecond), counter(&after))
			_, err := chain.Complete(context.Background(), llm.CompletionRequest{})
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if after.Load() != 4 {
				t.Errorf("%d requests reached the provider for one retry of one hedged request, want 2 attempts x 2 copies", after.Load())
			}
		})
	})
}

type countingHandler struct {
	next Handler
	n    *atomic.Int32
}

func (c *countingHandler) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	c.n.Add(1)
	return c.next.Complete(ctx, req)
}
func (c *countingHandler) Name() string { return c.next.Name() }
func (c *countingHandler) EstimateCost(r llm.CompletionRequest) float64 {
	return c.next.EstimateCost(r)
}
func (c *countingHandler) RetryPolicy() (int, time.Duration) { return c.next.RetryPolicy() }
func (c *countingHandler) Unwrap() Handler                   { return c.next }

func failsWithAfter(wait time.Duration, err error) func(context.Context) (llm.CompletionResponse, error) {
	return func(ctx context.Context) (llm.CompletionResponse, error) {
		select {
		case <-time.After(wait):
			return llm.CompletionResponse{}, err
		case <-ctx.Done():
			return llm.CompletionResponse{}, ctx.Err()
		}
	}
}

// contextOver is a context that is over and whose Done channel does not say so.
//
// Hedge waits on three things at once: an answer, the caller's context, and
// the delay. When the context is over and the delay has passed too, which of
// the two it sees first is not its to choose, and having seen the delay it
// asks the context once more before it sends a second copy. A cancelled context
// with a short delay almost never reaches that check, since the select sees
// the context first. A context whose Done never fires is one for which the
// delay is always seen first, so the check is the only thing between it and a
// second copy.
type contextOver struct{ context.Context }

func (contextOver) Done() <-chan struct{} { return nil }
func (contextOver) Err() error            { return context.Canceled }

func TestHedgeAsksTheContextBeforeItSendsASecondCopy(t *testing.T) {
	// The first copy does not answer; a second would, at once.
	provider := newScript(hangs, answersWith("the second copy's answer"))
	var events []HedgeEvent
	hedged := Chain(provider, Hedge(20*time.Millisecond, WithHedgeObserver(func(event HedgeEvent) {
		events = append(events, event)
	})))

	resp, err := hedged.Complete(contextOver{context.Background()}, llm.CompletionRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, resp %q: want the context's error", err, resp.Content)
	}
	if resp.Content != "" {
		t.Errorf("an answer came back beside the error: %q", resp.Content)
	}
	if made := provider.made(); made != 1 {
		t.Errorf("%d copies were sent on a context that was over", made)
	}
	if len(events) != 1 || events[0].Hedged {
		t.Errorf("the observer was told %+v, want one request that was not hedged", events)
	}
}

// yield lets goroutines that are ready to run, run. It stands in for waiting on
// an event no code of ours signals, such as a goroutine that is about to send on
// a buffered channel.
func yield() {
	for range 1000 {
		runtime.Gosched()
	}
}

// What a request returns once the caller's context is over, given the outcomes
// that had been delivered: an answer first, else the reason a copy failed, else
// the context's own error.
func TestHedgeDrain(t *testing.T) {
	limited := errors.New("the first copy was rate limited")
	other := errors.New("the second copy failed")
	earlier := errors.New("a failure already seen")
	first := func(err error) hedgeOutcome { return hedgeOutcome{err: err} }
	second := func(err error) hedgeOutcome { return hedgeOutcome{err: err, second: true} }
	answerOf := func(content string, secondCopy bool) hedgeOutcome {
		return hedgeOutcome{resp: llm.CompletionResponse{Content: content}, second: secondCopy}
	}

	cases := []struct {
		name         string
		known        hedgeFailures
		delivered    []hedgeOutcome
		wantAnswer   string
		wantSecond   bool
		wantErr      error
		wantCtxError bool
	}{
		{"nothing delivered: the context's own error", hedgeFailures{}, nil, "", false, nil, true},
		{"the first copy had failed", hedgeFailures{}, []hedgeOutcome{first(limited)}, "", false, limited, false},
		{"the second copy had failed", hedgeFailures{}, []hedgeOutcome{second(other)}, "", false, other, false},
		{"both had failed: the first's", hedgeFailures{}, []hedgeOutcome{second(other), first(limited)}, "", false, limited, false},
		{"both had failed, in the other order", hedgeFailures{}, []hedgeOutcome{first(limited), second(other)}, "", false, limited, false},
		{"the first copy had answered", hedgeFailures{}, []hedgeOutcome{answerOf("first", false)}, "first", false, nil, false},
		{"the second copy had answered", hedgeFailures{}, []hedgeOutcome{answerOf("second", true)}, "second", true, nil, false},
		{"an answer behind a failure", hedgeFailures{}, []hedgeOutcome{first(limited), answerOf("second", true)}, "second", true, nil, false},
		{"a failure behind an answer: the answer", hedgeFailures{}, []hedgeOutcome{answerOf("first", false), second(other)}, "first", false, nil, false},
		{"a failure seen before and nothing delivered now", hedgeFailures{first: earlier}, nil, "", false, earlier, false},
		{"a second copy's failure seen before and nothing now", hedgeFailures{other: earlier}, nil, "", false, earlier, false},
		{"a failure seen before and the first copy's now: the first's", hedgeFailures{other: earlier}, []hedgeOutcome{first(limited)}, "", false, limited, false},
		{"a failure seen before and an answer now", hedgeFailures{first: earlier}, []hedgeOutcome{answerOf("second", true)}, "second", true, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcomes := make(chan hedgeOutcome, 4)
			for _, outcome := range tc.delivered {
				outcomes <- outcome
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			failed := tc.known

			answer, answered, err := hedgeDrain(ctx, outcomes, &failed)
			if answered != (tc.wantAnswer != "") || answer.resp.Content != tc.wantAnswer || (answered && answer.second != tc.wantSecond) {
				t.Fatalf("answer %q (answered %v, second %v), want %q (second %v)", answer.resp.Content, answered, answer.second, tc.wantAnswer, tc.wantSecond)
			}
			switch {
			case tc.wantCtxError:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("err = %v, want the context's", err)
				}
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
			default:
				if err != nil {
					t.Fatalf("err = %v, want none beside an answer", err)
				}
			}
		})
	}
}

func TestHedgeFailuresReportTheFirstCopysErrorFirst(t *testing.T) {
	a, b, c := errors.New("a"), errors.New("b"), errors.New("c")
	cases := []struct {
		name    string
		outcome []hedgeOutcome
		want    error
	}{
		{"none", nil, nil},
		{"the first copy", []hedgeOutcome{{err: a}}, a},
		{"the second copy", []hedgeOutcome{{err: a, second: true}}, a},
		{"the second then the first", []hedgeOutcome{{err: a, second: true}, {err: b}}, b},
		{"the first then the second", []hedgeOutcome{{err: a}, {err: b, second: true}}, a},
		{"a later failure of the first copy replaces an earlier one", []hedgeOutcome{{err: a}, {err: b}}, b},
		{"a later failure of the second copy replaces an earlier one", []hedgeOutcome{{err: a, second: true}, {err: b, second: true}}, b},
		{"three outcomes", []hedgeOutcome{{err: a, second: true}, {err: b}, {err: c, second: true}}, b},
		{"a success is not a failure and is never noted", nil, nil},
		{"the first copy overrides any number of the second's", []hedgeOutcome{{err: a, second: true}, {err: b, second: true}, {err: c}}, c},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var failed hedgeFailures
			for _, outcome := range tc.outcome {
				failed.note(outcome)
			}
			if got := failed.err(); got != tc.want {
				t.Fatalf("err = %v, want %v", got, tc.want)
			}
		})
	}
}

// A request that fails inside the delay returns the failure and nothing else,
// whatever the provider handed back beside it.
func TestHedgeReturnsAnEmptyResponseBesideAnError(t *testing.T) {
	boom := errors.New("boom")
	leaky := func(context.Context) (llm.CompletionResponse, error) {
		return llm.CompletionResponse{Content: "partial text"}, boom
	}
	resp, err := Chain(newScript(leaky), Hedge(time.Hour)).Complete(context.Background(), llm.CompletionRequest{})
	if !errors.Is(err, boom) || resp.Content != "" {
		t.Fatalf("resp %q, err %v; want an empty response beside the failure", resp.Content, err)
	}
}

// hedgeGoroutines returns the state of every goroutine that is inside the hedge
// middleware, by goroutine number, from a stack dump of the whole process.
func hedgeGoroutines() map[string]string {
	buffer := make([]byte, 1<<16)
	for {
		n := runtime.Stack(buffer, true)
		if n < len(buffer) {
			buffer = buffer[:n]
			break
		}
		buffer = make([]byte, 2*len(buffer))
	}
	found := map[string]string{}
	for _, stack := range strings.Split(string(buffer), "\n\n") {
		if !strings.Contains(stack, "mw.(*hedged).") {
			continue
		}
		header, _, _ := strings.Cut(stack, "\n") // goroutine 7 [chan send]:
		id, state, _ := strings.Cut(strings.TrimPrefix(header, "goroutine "), " ")
		found[id] = state
	}
	return found
}

// Both copies report their outcome to a channel with room for both, so the one
// that finishes after the caller has left is not left blocked on a send nobody
// will ever receive. A goroutine blocked in the middleware once the providers
// have returned is a copy that is stuck for good, and is told from one that is
// still on its way out by its state in a stack dump, not by a count.
func TestHedgeLeavesNoCopyBlockedWhenTheCallerGivesUpFirst(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		before := hedgeGoroutines()
		release := make(chan struct{})
		bothStarted := make(chan struct{})
		var started atomic.Int32
		provider := completeFunc(func(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
			if started.Add(1) == 2 {
				close(bothStarted)
			}
			<-release
			return llm.CompletionResponse{}, errors.New("answered after the caller left")
		})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := Hedge(time.Millisecond)(provider).Complete(ctx, llm.CompletionRequest{Model: "m", UserPrompt: "q"})
			done <- err
		}()
		waitOrFail(t, bothStarted, "the second copy to be sent")
		cancel()
		<-done
		close(release)

		// When every other goroutine of the bubble has finished or is blocked for
		// good, a copy that is still inside the middleware is one that is stuck:
		// there is no copy left that is only on its way out.
		synctest.Wait()
		stuck := 0
		for id := range hedgeGoroutines() {
			if _, earlier := before[id]; !earlier {
				stuck++
			}
		}
		if stuck != 0 {
			t.Errorf("%d copies are still inside the middleware after the caller left and their providers returned", stuck)
		}
	})
}

// An answer that is delivered as the caller's context ends, before the delay
// has passed, is the answer: the request does not return the context's error
// beside an answer it holds. Which of the two a select sees first is the
// runtime's to choose, so the test pins the scheduler to one processor, where
// the order is fixed: the first copy runs when the request waits, ends the
// context, and delivers its answer before the request runs again. The loop is a
// bound for a scheduler that orders it otherwise, not a part of the test.
func TestHedgeUsesAnAnswerThatArrivesAsTheContextEndsBeforeTheDelay(t *testing.T) {
	previous := runtime.GOMAXPROCS(1)
	t.Cleanup(func() { runtime.GOMAXPROCS(previous) })

	const attempts = 100
	for i := 0; i < attempts; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		provider := completeFunc(func(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
			cancel()
			return llm.CompletionResponse{Content: "answered"}, nil
		})
		resp, err := Hedge(time.Hour)(provider).Complete(ctx, llm.CompletionRequest{Model: "m", UserPrompt: "q"})
		cancel()
		if err == nil && resp.Content == "answered" {
			return
		}
	}
	t.Fatalf("in %d attempts the answer that arrived as the context ended was never returned", attempts)
}

// A response says on itself that a second copy of its request was sent, so the
// call record can say the call may have been billed twice. A request answered
// before the delay, or sent through a delay of zero, was sent once and is not
// marked.
func TestHedgeSaysOnTheResponseThatASecondCopyWasSent(t *testing.T) {
	const after = 100 * time.Millisecond
	cases := []struct {
		name   string
		after  time.Duration
		delays []time.Duration
		fails  []error
		hedged bool
	}{
		{"a quick answer is sent once", after, []time.Duration{5 * time.Millisecond}, nil, false},
		{"the second copy wins", after, []time.Duration{3 * time.Second, 5 * time.Millisecond}, nil, true},
		{"the first copy wins after the second went out", after, []time.Duration{250 * time.Millisecond, 3 * time.Second}, nil, true},
		{"the first fails after the second went out and the second answers", after, []time.Duration{250 * time.Millisecond, 300 * time.Millisecond}, []error{errors.New("down")}, true},
		{"the second fails and the first answers", after, []time.Duration{500 * time.Millisecond, 5 * time.Millisecond}, []error{nil, errors.New("down")}, true},
		{"an answer exactly at the delay is still one request", after, []time.Duration{50 * time.Millisecond}, nil, false},
		{"a delay of zero sends every request once", 0, []time.Duration{3 * time.Second}, nil, false},
		{"a negative delay sends every request once", -time.Second, []time.Duration{3 * time.Second}, nil, false},
		{"a slow single copy answered by the first after a late second is marked", after, []time.Duration{2 * time.Second, 4 * time.Second}, nil, true},
		{"a delay far above the answer sends one request", time.Hour, []time.Duration{time.Second}, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				provider := &slowThenFast{delays: tc.delays, fails: tc.fails}
				resp, err := Chain(provider, Hedge(tc.after)).Complete(context.Background(), llm.CompletionRequest{Model: "m"})
				if err != nil {
					t.Fatalf("Complete: %v", err)
				}
				if resp.Hedged != tc.hedged {
					t.Errorf("Hedged is %v, want %v", resp.Hedged, tc.hedged)
				}
			})
		})
	}

	// A caller whose context ends after the second copy went out, as an answer is
	// delivered, gets the answer marked or the context's error, and never an answer
	// that is not marked. Which of the two it is depends on whether the copy had
	// delivered its answer by the time the request looked: the provider here ends
	// the context just before it returns, so the two race, and the check holds for
	// either result.
	t.Run("an answer taken as the context ends after the second copy went out is marked", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			provider := completeFunc(func(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
				time.Sleep(time.Second)
				cancel()
				return llm.CompletionResponse{Content: "answered"}, nil
			})
			resp, err := Hedge(after)(provider).Complete(ctx, llm.CompletionRequest{Model: "m"})
			if err != nil {
				if !errors.Is(err, context.Canceled) || resp.Content != "" {
					t.Fatalf("Complete: %v, %q: want the answer marked or the context's error alone", err, resp.Content)
				}
			} else if !resp.Hedged {
				t.Errorf("an answer returned after the second copy went out is not marked")
			}
			// The copy that lost is still asleep in the bubble; let it end.
			time.Sleep(time.Minute)
		})
	})

	// A flag the provider already set is not cleared by the middleware.
	t.Run("a flag the answer already carries is kept", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			provider := completeFunc(func(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
				return llm.CompletionResponse{Content: "answered", Hedged: true}, nil
			})
			resp, err := Hedge(time.Hour)(provider).Complete(context.Background(), llm.CompletionRequest{Model: "m"})
			if err != nil || !resp.Hedged {
				t.Errorf("resp.Hedged = %v, err = %v; want the answer's own flag kept", resp.Hedged, err)
			}
		})
	})
}

// Hedge is the one middleware that returns when the caller's context ends while a
// copy is still running. A provider that ignores its context leaves the copies
// running until it lets them go, and that is documented; a provider that honours
// the cancellation Hedge gives its copies leaves none.
func TestHedgeCopiesOutliveTheCallOnlyWhenTheProviderIgnoresItsContext(t *testing.T) {
	for _, honours := range []bool{false, true} {
		name := "a provider that ignores its context"
		if honours {
			name = "a provider that honours its context"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				release := make(chan struct{})
				var running atomic.Int32
				provider := completeFunc(func(ctx context.Context, _ llm.CompletionRequest) (llm.CompletionResponse, error) {
					running.Add(1)
					defer running.Add(-1)
					if honours {
						select {
						case <-release:
						case <-ctx.Done():
							return llm.CompletionResponse{}, ctx.Err()
						}
						return llm.CompletionResponse{Content: "late"}, nil
					}
					<-release
					return llm.CompletionResponse{Content: "late"}, nil
				})
				ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
				defer cancel()

				start := time.Now()
				_, err := Hedge(10*time.Millisecond)(provider).Complete(ctx, llm.CompletionRequest{})
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Errorf("err = %v, want the deadline", err)
				}
				if took := time.Since(start); took != 100*time.Millisecond {
					t.Errorf("the call took %v, want the 100 ms it had: Hedge returns when the context ends", took)
				}
				synctest.Wait()
				stillRunning := running.Load()
				if honours && stillRunning != 0 {
					t.Errorf("%d copies are still running after the call, want 0: the provider honours its context", stillRunning)
				}
				if !honours && stillRunning != 2 {
					t.Errorf("%d copies are still running after the call, want the 2 that were sent: the provider ignores its context", stillRunning)
				}
				close(release)
				synctest.Wait()
				if left := running.Load(); left != 0 {
					t.Errorf("%d copies are running after the provider let go, want 0", left)
				}
			})
		})
	}
}
