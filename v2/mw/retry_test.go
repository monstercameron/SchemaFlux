package mw

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/monstercameron/schemaflux/telemetry"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// bubble runs a test body in a synctest bubble, where every timer reads a fake
// clock that advances when all goroutines are blocked: a wait takes exactly as
// long as it was asked to and no real time.
func bubble(t *testing.T, name string, body func(t *testing.T)) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		synctest.Test(t, body)
	})
}

// retrier is a provider that states its own retry settings.
type retrier struct {
	*schemafluxtest.Provider
	retries int
	backoff time.Duration
}

func (r retrier) RetryPolicy() (int, time.Duration) { return r.retries, r.backoff }

func TestRetry(t *testing.T) {
	t.Parallel()

	bubble(t, "a request that failed is sent again and the answer is returned", func(t *testing.T) {
		var p schemafluxtest.Provider
		p.FailThen(1, unavailable(), "answer")
		resp, err := Retry()(&p).Complete(t.Context(), ask("q"))
		if err != nil || resp.Content != "answer" {
			t.Fatalf("Complete = %q, %v", resp.Content, err)
		}
		if p.Calls() != 2 {
			t.Errorf("the provider was asked %d times, want 2", p.Calls())
		}
	})

	bubble(t, "no retries are made when the first request succeeds", func(t *testing.T) {
		var p schemafluxtest.Provider
		p.Reply("answer")
		if _, err := Retry()(&p).Complete(t.Context(), ask("q")); err != nil {
			t.Fatal(err)
		}
		if p.Calls() != 1 {
			t.Errorf("the provider was asked %d times, want 1", p.Calls())
		}
	})

	bubble(t, "the default is two retries, three attempts in all", func(t *testing.T) {
		var p schemafluxtest.Provider
		p.Fail(unavailable())
		_, err := Retry()(&p).Complete(t.Context(), ask("q"))
		wantKind(t, err, schemaflux.ErrUnavailable)
		if p.Calls() != 3 {
			t.Errorf("the provider was asked %d times, want 3", p.Calls())
		}
	})

	for _, tc := range []struct {
		retries      int
		wantAttempts int
	}{
		{1, 2}, {2, 3}, {4, 5}, {7, 8},
	} {
		bubble(t, "Retries of "+string(rune('0'+tc.retries))+" repeats that many times after the first attempt", func(t *testing.T) {
			var p schemafluxtest.Provider
			p.Fail(unavailable())
			_, err := Retry(RetryOptions{Retries: tc.retries})(&p).Complete(t.Context(), ask("q"))
			wantKind(t, err, schemaflux.ErrUnavailable)
			if p.Calls() != tc.wantAttempts {
				t.Errorf("the provider was asked %d times, want %d", p.Calls(), tc.wantAttempts)
			}
		})
	}

	bubble(t, "a negative Retries is none: the request is sent once and its failure returned", func(t *testing.T) {
		var p schemafluxtest.Provider
		p.Fail(unavailable())
		_, err := Retry(RetryOptions{Retries: -1})(&p).Complete(t.Context(), ask("q"))
		wantKind(t, err, schemaflux.ErrUnavailable)
		if p.Calls() != 1 {
			t.Errorf("the provider was asked %d times, want 1", p.Calls())
		}
	})

	bubble(t, "a provider that states its own retries decides when Retries is zero", func(t *testing.T) {
		inner := new(schemafluxtest.Provider)
		inner.Fail(unavailable())
		_, err := Retry()(retrier{Provider: inner, retries: 4, backoff: time.Millisecond}).Complete(t.Context(), ask("q"))
		wantKind(t, err, schemaflux.ErrUnavailable)
		if inner.Calls() != 5 {
			t.Errorf("the provider was asked %d times, want 5: its own 4 retries", inner.Calls())
		}
	})

	bubble(t, "Retries beats the provider's own setting", func(t *testing.T) {
		inner := new(schemafluxtest.Provider)
		inner.Fail(unavailable())
		_, err := Retry(RetryOptions{Retries: 1})(retrier{Provider: inner, retries: 4}).Complete(t.Context(), ask("q"))
		wantKind(t, err, schemaflux.ErrUnavailable)
		if inner.Calls() != 2 {
			t.Errorf("the provider was asked %d times, want 2", inner.Calls())
		}
	})

	for _, tc := range []struct {
		name   string
		err    error
		repeat bool
	}{
		{"unavailable", &schemaflux.Error{Kind: schemaflux.ErrUnavailable}, true},
		{"rate limited", &schemaflux.Error{Kind: schemaflux.ErrRateLimited}, true},
		{"timeout", &schemaflux.Error{Kind: schemaflux.ErrTimeout}, true},
		{"busy", &schemaflux.Error{Kind: schemaflux.ErrBusy}, false},
		{"unauthorized", &schemaflux.Error{Kind: schemaflux.ErrUnauthorized}, false},
		{"an invalid request", &schemaflux.Error{Kind: schemaflux.ErrInvalidRequest}, false},
		{"a spent budget", &schemaflux.Error{Kind: schemaflux.ErrBudgetExceeded}, false},
		{"a policy refusal", &schemaflux.Error{Kind: schemaflux.ErrPolicy}, false},
		{"a configuration error", &schemaflux.Error{Kind: schemaflux.ErrConfiguration}, false},
		{"canceled", &schemaflux.Error{Kind: schemaflux.ErrCanceled}, false},
	} {
		bubble(t, "a failure that is "+tc.name+" is repeated or returned by its kind", func(t *testing.T) {
			var p schemafluxtest.Provider
			p.Fail(tc.err)
			_, err := Retry(RetryOptions{Retries: 2})(&p).Complete(t.Context(), ask("q"))
			if _, ok := kindOf(err); !ok {
				t.Fatalf("err = %v, want an *Error", err)
			}
			want := 1
			if tc.repeat {
				want = 3
			}
			if p.Calls() != want {
				t.Errorf("the provider was asked %d times, want %d", p.Calls(), want)
			}
		})
	}

	bubble(t, "a wait the error states is the wait, whatever the base delay", func(t *testing.T) {
		var p schemafluxtest.Provider
		p.FailThen(1, rateLimited(7*time.Second), "answer")
		start := time.Now()
		if _, err := Retry(RetryOptions{BaseDelay: time.Millisecond})(&p).Complete(t.Context(), ask("q")); err != nil {
			t.Fatal(err)
		}
		if waited := time.Since(start); waited != 7*time.Second {
			t.Errorf("waited %v, want the stated 7s", waited)
		}
	})

	bubble(t, "a stated wait is capped at two minutes", func(t *testing.T) {
		var p schemafluxtest.Provider
		p.FailThen(1, rateLimited(time.Hour), "answer")
		start := time.Now()
		if _, err := Retry()(&p).Complete(t.Context(), ask("q")); err != nil {
			t.Fatal(err)
		}
		if waited := time.Since(start); waited != 2*time.Minute {
			t.Errorf("waited %v, want 2m0s", waited)
		}
	})

	bubble(t, "BaseDelay and MaxDelay bound the computed waits", func(t *testing.T) {
		var p schemafluxtest.Provider
		p.Fail(unavailable())
		start := time.Now()
		_, err := Retry(RetryOptions{Retries: 2, BaseDelay: 3 * time.Second, MaxDelay: 3 * time.Second})(&p).Complete(t.Context(), ask("q"))
		wantKind(t, err, schemaflux.ErrUnavailable)
		if waited := time.Since(start); waited != 6*time.Second {
			t.Errorf("waited %v for two waits of 3s", waited)
		}
	})

	bubble(t, "a context that ends during the wait ends the request with the context's error", func(t *testing.T) {
		var p schemafluxtest.Provider
		p.Fail(unavailable())
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		_, err := Retry(RetryOptions{BaseDelay: time.Minute, MaxDelay: time.Minute})(&p).Complete(ctx, ask("q"))
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want the deadline", err)
		}
		wantKind(t, err, schemaflux.ErrTimeout)
		if p.Calls() != 1 {
			t.Errorf("the provider was asked %d times, want 1", p.Calls())
		}
	})

	bubble(t, "a negative delay is a configuration error and nothing is sent", func(t *testing.T) {
		for _, o := range []RetryOptions{{BaseDelay: -1}, {MaxDelay: -time.Second}} {
			var p schemafluxtest.Provider
			_, err := Retry(o)(&p).Complete(t.Context(), ask("q"))
			e := wantKind(t, err, schemaflux.ErrConfiguration)
			if e.Op != "mw.retry" {
				t.Errorf("Op = %q", e.Op)
			}
			if p.Calls() != 0 {
				t.Errorf("%+v: the provider was asked %d times, want 0", o, p.Calls())
			}
		}
	})

	bubble(t, "option values merge left to right with a later non-zero field winning", func(t *testing.T) {
		var p schemafluxtest.Provider
		p.Fail(unavailable())
		_, err := Retry(RetryOptions{Retries: 5}, RetryOptions{}, RetryOptions{Retries: 1})(&p).Complete(t.Context(), ask("q"))
		wantKind(t, err, schemaflux.ErrUnavailable)
		if p.Calls() != 2 {
			t.Errorf("the provider was asked %d times, want 2: the last Retries", p.Calls())
		}
	})

	bubble(t, "a request is sent unchanged each time it is repeated", func(t *testing.T) {
		var p schemafluxtest.Provider
		p.FailThen(2, unavailable(), "answer")
		want := schemaflux.Request{Model: "m", System: "s", User: "u", MaxTokens: 9, Format: "json"}
		if _, err := Retry()(&p).Complete(t.Context(), want); err != nil {
			t.Fatal(err)
		}
		for i, got := range p.Requests() {
			if got.Model != want.Model || got.System != want.System || got.User != want.User || got.MaxTokens != want.MaxTokens || got.Format != want.Format {
				t.Errorf("request %d = %+v, want %+v", i+1, got, want)
			}
		}
	})
}

// A request is repeated by one loop. The client repeats a failed request itself
// unless the provider says it does not, and a chain that holds a Retry says so, so
// the number of requests is the middleware's alone and not the two counts
// multiplied.
func TestRetryIsTheOnlyLoopThatRepeatsARequestThroughAClient(t *testing.T) {
	t.Parallel()

	// requests is how many requests reach the primary provider for one Summarize
	// through a client with chain built over it, when every request fails with an
	// outage. The client's own settings are left at their defaults.
	requests := func(t *testing.T, build func(p *schemafluxtest.Provider) schemaflux.Provider) (n int) {
		t.Helper()
		var p schemafluxtest.Provider
		p.SetName("counted")
		p.Fail(unavailable())
		bubble(t, "", func(t *testing.T) {
			ctx := schemaflux.WithClient(t.Context(), schemaflux.Client{Provider: build(&p), Model: "test-model"})
			_, err := schemaflux.Summarize(ctx, "some text")
			wantKind(t, err, schemaflux.ErrUnavailable)
		})
		return p.Calls()
	}
	fast := func(retries int) RetryOptions {
		return RetryOptions{Retries: retries, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
	}
	alternate := func() Route {
		alt, _ := route("alternate", "unused")
		alt.Provider.(*schemafluxtest.Provider).Fail(unavailable())
		return alt
	}

	for _, tc := range []struct {
		name  string
		build func(p *schemafluxtest.Provider) schemaflux.Provider
		want  int
	}{
		{"a bare provider that states no retry settings is retried as the client does by default", func(p *schemafluxtest.Provider) schemaflux.Provider {
			return Func("counted", func(ctx context.Context, req schemaflux.Request) (schemaflux.Response, error) {
				return p.Complete(ctx, req)
			})
		}, 4},
		{"a bare provider that says it retries nothing is sent a request once", func(p *schemafluxtest.Provider) schemaflux.Provider { return p }, 1},
		{"Retry with no options sends a request three times", func(p *schemafluxtest.Provider) schemaflux.Provider {
			return Chain(p, Retry(RetryOptions{BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}))
		}, 3},
		{"Retry of one retry sends it twice", func(p *schemafluxtest.Provider) schemaflux.Provider { return Chain(p, Retry(fast(1))) }, 2},
		{"Retry of none sends it once, and the client sends it once as well", func(p *schemafluxtest.Provider) schemaflux.Provider { return Chain(p, Retry(fast(-1))) }, 1},
		{"Retry inside a Fallback", func(p *schemafluxtest.Provider) schemaflux.Provider {
			return Chain(p, Fallback(nil), Retry(fast(1)))
		}, 2},
		{"Retry outside a Fallback with an alternate that fails too", func(p *schemafluxtest.Provider) schemaflux.Provider {
			return Chain(p, Retry(fast(1)), Fallback([]Route{alternate()}))
		}, 2},
		{"a layer of another kind around a Retry does not bring the client's loop back", func(p *schemafluxtest.Provider) schemaflux.Provider {
			return Chain(p, Redact(), Cache(), Budget(1e9, BudgetOptions{Cost: priced(0)}), Retry(fast(1)), RateLimit(100, time.Second), Bulkhead(5), CircuitBreaker(BreakerOptions{Failures: 100}))
		}, 2},
		{"a Retry in the generating provider of a pair", func(p *schemafluxtest.Provider) schemaflux.Provider {
			return Decisions(Chain(p, Retry(fast(1))), schemafluxtest.NewDecider(), DecisionOptions{})
		}, 2},
		// Two Retry middleware in one chain do multiply, which the doc says: the outer
		// one repeats the whole of the inner one's attempts.
		{"two Retry middleware nested multiply", func(p *schemafluxtest.Provider) schemaflux.Provider {
			return Chain(p, Retry(fast(1)), Retry(fast(1)))
		}, 4},
		{"a Retry that wraps nothing but a Func provider is the only loop too", func(p *schemafluxtest.Provider) schemaflux.Provider {
			return Chain(Func("counted", func(ctx context.Context, req schemaflux.Request) (schemaflux.Response, error) {
				return p.Complete(ctx, req)
			}), Retry(fast(2)))
		}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := requests(t, tc.build); got != tc.want {
				t.Errorf("%d requests reached the provider, want %d", got, tc.want)
			}
		})
	}
}

// noteCollector is a telemetry observer that keeps the notes it is told.
type noteCollector struct {
	mu    sync.Mutex
	notes []telemetry.Note
}

func (c *noteCollector) OperationStarted(ctx context.Context, _ telemetry.OperationEvent) context.Context {
	return ctx
}
func (c *noteCollector) OperationFinished(context.Context, telemetry.OperationResult) {}
func (c *noteCollector) OperationNoted(_ context.Context, note telemetry.Note) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notes = append(c.notes, note)
}

// The repeats the wrapper makes reach a tracing observer: one note for each failed
// attempt that another follows, and none when the first attempt answers.
func TestRetryTellsTheObserversOfEachRepeat(t *testing.T) {
	collector := &noteCollector{}
	t.Cleanup(telemetry.AddObserver(collector))
	start := func() context.Context {
		return telemetry.ObserveOperationStart(t.Context(), telemetry.OperationEvent{Operation: "test"})
	}

	var failing schemafluxtest.Provider
	failing.Fail(unavailable())
	Retry(RetryOptions{Retries: 2, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond})(&failing).Complete(start(), ask("q"))
	collector.mu.Lock()
	got := append([]telemetry.Note(nil), collector.notes...)
	collector.notes = nil
	collector.mu.Unlock()
	if len(got) != 2 || got[0].Attempt != 1 || got[1].Attempt != 2 || got[0].ErrorKind != "provider unavailable" || got[0].Name != telemetry.NoteRetry || got[0].Wait < time.Millisecond {
		t.Errorf("three attempts that failed gave the notes %+v, want two, for attempts 1 and 2", got)
	}

	p, _ := answering("ok")
	if _, err := Retry()(p).Complete(start(), ask("q")); err != nil {
		t.Fatal(err)
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	if len(collector.notes) != 0 {
		t.Errorf("a first attempt that answered gave the notes %+v, want none", collector.notes)
	}
}
