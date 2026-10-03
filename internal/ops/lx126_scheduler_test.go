package ops

import (
	"context"
	"errors"
	"iter"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// gateProvider counts how many of its calls are in flight at once. A call
// stays in flight for hold, or until release is closed when one is set.
type gateProvider struct {
	name    string
	hold    time.Duration
	release chan struct{}
	retries int
	backoff time.Duration
	// failFirst makes the first attempt of every call fail with an error the
	// retry loop retries.
	failFirst bool

	inFlight atomic.Int64
	peak     atomic.Int64
	calls    atomic.Int64

	mu     sync.Mutex
	seen   map[string]int
	events []string
}

func (p *gateProvider) enter() {
	now := p.inFlight.Add(1)
	for {
		peak := p.peak.Load()
		if now <= peak || p.peak.CompareAndSwap(peak, now) {
			return
		}
	}
}

func (p *gateProvider) wait(ctx context.Context) error {
	if p.release != nil {
		select {
		case <-p.release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	select {
	case <-time.After(p.hold):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *gateProvider) note(event string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, event)
}

func (p *gateProvider) noted() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.events...)
}

func (p *gateProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.enter()
	defer p.inFlight.Add(-1)
	p.calls.Add(1)

	p.mu.Lock()
	if p.seen == nil {
		p.seen = map[string]int{}
	}
	p.seen[req.UserPrompt]++
	attempt := p.seen[req.UserPrompt]
	p.mu.Unlock()

	p.note("start " + req.UserPrompt)
	defer p.note("end " + req.UserPrompt)

	if err := p.wait(ctx); err != nil {
		return llm.CompletionResponse{}, err
	}
	if p.failFirst && attempt == 1 {
		return llm.CompletionResponse{}, errors.New("upstream hiccup")
	}
	return llm.CompletionResponse{Content: `{"ok":true}`, FinishReason: "stop", Model: "gpt-6-luna", Provider: p.providerName()}, nil
}

func (p *gateProvider) CompleteStream(ctx context.Context, _ llm.CompletionRequest) iter.Seq2[llm.StreamChunk, error] {
	return func(yield func(llm.StreamChunk, error) bool) {
		p.enter()
		defer p.inFlight.Add(-1)
		p.calls.Add(1)
		if !yield(llm.StreamChunk{Delta: "first "}, nil) {
			return
		}
		if err := p.wait(ctx); err != nil {
			yield(llm.StreamChunk{}, err)
			return
		}
		yield(llm.StreamChunk{Done: true, Response: llm.CompletionResponse{
			Content: "first ", FinishReason: "stop", Model: "gpt-6-luna", Provider: p.providerName(),
		}}, nil)
	}
}

func (p *gateProvider) providerName() string {
	if p.name == "" {
		return "openai"
	}
	return p.name
}
func (p *gateProvider) Name() string                               { return p.providerName() }
func (p *gateProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *gateProvider) RetryPolicy() (int, time.Duration)          { return p.retries, p.backoff }

func scheduledContext(t *testing.T, limits SchedulerLimits) (context.Context, *Scheduler) {
	t.Helper()
	scheduler := NewScheduler(limits)
	t.Cleanup(func() { _ = scheduler.Close(context.Background()) })
	return WithExecConfig(context.Background(), &ExecConfig{Scheduler: scheduler}), scheduler
}

var lx126Options = types.OpOptions{Model: "gpt-6-luna", ResponseFormat: "json"}

// LX-126. A client's scheduler bounds how many of its calls are with the
// provider at once. It was stored on the context and read by nothing, so a
// client limited to two calls in flight had as many as it had callers.
func TestAClientsSchedulerBoundsTheCallsInFlight(t *testing.T) {
	const callers = 12

	cases := []struct {
		name   string
		limits SchedulerLimits
		ceil   int64
	}{
		{"one at a time", SchedulerLimits{MaxConcurrent: 1, MaxQueued: 100}, 1},
		{"two at a time", SchedulerLimits{MaxConcurrent: 2, MaxQueued: 100}, 2},
		{"three at a time", SchedulerLimits{MaxConcurrent: 3, MaxQueued: 100}, 3},
		{"five at a time", SchedulerLimits{MaxConcurrent: 5, MaxQueued: 100}, 5},
		{"a limit above the callers", SchedulerLimits{MaxConcurrent: 50, MaxQueued: 100}, callers},
		{"a limit per provider", SchedulerLimits{PerProviderConcurrency: 2}, 2},
		{"the tighter of two limits", SchedulerLimits{MaxConcurrent: 4, PerProviderConcurrency: 1, MaxQueued: 100}, 1},
		{"no limit set", SchedulerLimits{}, callers},
	}
	for _, tc := range cases {
		for _, path := range []string{"buffered", "streamed"} {
			t.Run(tc.name+", "+path, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					provider := &gateProvider{hold: 15 * time.Millisecond}
					ctx, scheduler := scheduledContext(t, tc.limits)

					var wg sync.WaitGroup
					failures := make(chan error, callers)
					for range callers {
						wg.Add(1)
						go func() {
							defer wg.Done()
							if path == "buffered" {
								if _, err := CallLLM(ctx, provider, "system", "user", lx126Options); err != nil {
									failures <- err
								}
								return
							}
							opts := lx126Options
							opts.ResponseFormat = "text"
							stream, err := streamWith(ctx, provider, opts)
							if err != nil {
								failures <- err
								return
							}
							if _, err := stream.Text(); err != nil {
								failures <- err
							}
						}()
					}
					wg.Wait()
					close(failures)
					for err := range failures {
						t.Errorf("a call failed: %v", err)
					}

					if got := provider.calls.Load(); got != callers {
						t.Errorf("the provider was called %d times, want %d", got, callers)
					}
					// Every caller asks before any call finishes, so the scheduler
					// admits as many as its limit lets in, and no more.
					if peak := provider.peak.Load(); peak != tc.ceil {
						t.Errorf("%d calls were in flight at once, want %d, the limit", peak, tc.ceil)
					}
					waitForIdle(t, scheduler)
				})
			})
		}
	}
}

// streamWith opens a text stream on provider through the same path the
// exported Stream operations use.
func streamWith(ctx context.Context, provider llm.Provider, opts types.OpOptions) (*TextStream, error) {
	opts.Context = WithProvider(ctx, provider)
	return streamLLM(opts.Context, "system", "user", opts)
}

// waitForIdle asserts that the scheduler holds nothing once every goroutine of
// the test's bubble has run as far as it can.
func waitForIdle(t *testing.T, scheduler *Scheduler) {
	t.Helper()
	synctest.Wait()
	if stats := scheduler.Stats(); stats.InFlight != 0 || stats.Queued != 0 {
		t.Errorf("the scheduler did not come back to idle: %+v", stats)
	}
}

// With no scheduler nothing is bounded and nothing changes.
func TestWithNoSchedulerCallsAreNotQueued(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		provider := &gateProvider{release: make(chan struct{})}

		var wg sync.WaitGroup
		for range 6 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := CallLLM(context.Background(), provider, "system", "user", lx126Options); err != nil {
					t.Errorf("a call failed: %v", err)
				}
			}()
		}
		synctest.Wait()
		if got := provider.inFlight.Load(); got != 6 {
			t.Errorf("%d of 6 calls reached the provider with no scheduler set", got)
		}
		close(provider.release)
		wg.Wait()
	})
}

// What a call that is not admitted comes back with, and that the provider is
// never called for it.
func TestACallTheSchedulerDoesNotAdmitIsNotSent(t *testing.T) {
	t.Run("the queue is full", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			provider := &gateProvider{release: make(chan struct{})}
			ctx, _ := scheduledContext(t, SchedulerLimits{MaxConcurrent: 1, MaxQueued: 1})

			// One at a time: a call counts against the queue from the moment it
			// asks until it is admitted, so two asking together can find the
			// queue full between them.
			results := make(chan error, 2)
			call := func() {
				_, err := CallLLM(ctx, provider, "system", "user", lx126Options)
				results <- err
			}
			go call()
			waitFor(t, func() bool { return provider.inFlight.Load() == 1 })
			go call()
			// One is with the provider and one is waiting.
			waitFor(t, func() bool { return provider.inFlight.Load() == 1 && ExecScheduler(ctx).Stats().Queued == 1 })

			_, err := CallLLM(ctx, provider, "system", "user", lx126Options)
			if !errors.Is(err, types.ErrAdmissionRejected) {
				t.Errorf("err = %v, want an admission refusal", err)
			}
			if got := provider.calls.Load(); got != 1 {
				t.Errorf("the provider was called %d times, want 1", got)
			}

			close(provider.release)
			for range 2 {
				if err := <-results; err != nil {
					t.Errorf("a call that was admitted failed: %v", err)
				}
			}
		})
	})

	t.Run("a refusal is not retried", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			provider := &gateProvider{release: make(chan struct{}), retries: 5, backoff: time.Millisecond}
			ctx, _ := scheduledContext(t, SchedulerLimits{MaxConcurrent: 1, MaxQueued: 1})

			// One at a time: a call counts against the queue from the moment it
			// asks until it is admitted, so two asking together can find the
			// queue full between them.
			results := make(chan error, 2)
			call := func() {
				_, err := CallLLM(ctx, provider, "system", "user", lx126Options)
				results <- err
			}
			go call()
			waitFor(t, func() bool { return provider.inFlight.Load() == 1 })
			go call()
			waitFor(t, func() bool { return provider.inFlight.Load() == 1 && ExecScheduler(ctx).Stats().Queued == 1 })

			started := time.Now()
			_, err := CallLLM(ctx, provider, "system", "user", lx126Options)
			if !errors.Is(err, types.ErrAdmissionRejected) {
				t.Errorf("err = %v, want an admission refusal", err)
			}
			if waited := time.Since(started); waited != 0 {
				t.Errorf("the refusal took %v, it was retried", waited)
			}
			close(provider.release)
			<-results
			<-results
		})
	})

	t.Run("the caller gives up while waiting", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			provider := &gateProvider{release: make(chan struct{})}
			ctx, scheduler := scheduledContext(t, SchedulerLimits{MaxConcurrent: 1, MaxQueued: 10})

			first := make(chan error, 1)
			go func() {
				_, err := CallLLM(ctx, provider, "system", "user", lx126Options)
				first <- err
			}()
			waitFor(t, func() bool { return provider.inFlight.Load() == 1 })

			waiting, cancel := context.WithCancel(ctx)
			second := make(chan error, 1)
			go func() {
				_, err := CallLLM(waiting, provider, "system", "user", lx126Options)
				second <- err
			}()
			waitFor(t, func() bool { return scheduler.Stats().Queued == 1 })
			cancel()

			if err := <-second; !errors.Is(err, types.ErrCanceled) {
				t.Errorf("err = %v, want a cancellation", err)
			}
			if got := provider.calls.Load(); got != 1 {
				t.Errorf("the provider was called %d times, want 1", got)
			}
			close(provider.release)
			if err := <-first; err != nil {
				t.Errorf("the admitted call failed: %v", err)
			}
			waitForIdle(t, scheduler)
		})
	})

	t.Run("the deadline passes while waiting", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			provider := &gateProvider{release: make(chan struct{})}
			ctx, scheduler := scheduledContext(t, SchedulerLimits{MaxConcurrent: 1, MaxQueued: 10})

			first := make(chan error, 1)
			go func() {
				_, err := CallLLM(ctx, provider, "system", "user", lx126Options)
				first <- err
			}()
			waitFor(t, func() bool { return provider.inFlight.Load() == 1 })

			waiting, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
			defer cancel()
			_, err := CallLLM(waiting, provider, "system", "user", lx126Options)
			if !errors.Is(err, types.ErrTimeout) {
				t.Errorf("err = %v, want a timeout", err)
			}
			if got := provider.calls.Load(); got != 1 {
				t.Errorf("the provider was called %d times, want 1", got)
			}
			close(provider.release)
			<-first
			waitForIdle(t, scheduler)
		})
	})

	t.Run("the scheduler is closed", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			provider := &gateProvider{}
			ctx, scheduler := scheduledContext(t, SchedulerLimits{MaxConcurrent: 1})
			if err := scheduler.Close(context.Background()); err != nil {
				t.Fatalf("Close: %v", err)
			}

			_, err := CallLLM(ctx, provider, "system", "user", lx126Options)
			if !errors.Is(err, types.ErrShutdown) {
				t.Errorf("err = %v, want a shutdown refusal", err)
			}
			if got := provider.calls.Load(); got != 0 {
				t.Errorf("the provider was called %d times after the scheduler closed", got)
			}

			opts := lx126Options
			opts.ResponseFormat = "text"
			stream, err := streamWith(ctx, provider, opts)
			if stream != nil || !errors.Is(err, types.ErrShutdown) {
				t.Errorf("stream opened = %v, err = %v; want no stream and a shutdown refusal", stream != nil, err)
			}
		})
	})

	t.Run("a stream the queue has no room for", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			provider := &gateProvider{release: make(chan struct{})}
			ctx, scheduler := scheduledContext(t, SchedulerLimits{MaxConcurrent: 1, MaxQueued: 1})
			opts := lx126Options
			opts.ResponseFormat = "text"

			running, err := streamWith(ctx, provider, opts)
			if err != nil {
				t.Fatalf("the first stream: %v", err)
			}
			queued := make(chan error, 1)
			go func() {
				stream, err := streamWith(ctx, provider, opts)
				if err == nil {
					_, err = stream.Text()
				}
				queued <- err
			}()
			waitFor(t, func() bool { return scheduler.Stats().Queued == 1 })

			stream, err := streamWith(ctx, provider, opts)
			if stream != nil || !errors.Is(err, types.ErrAdmissionRejected) {
				t.Errorf("stream opened = %v, err = %v; want no stream and an admission refusal", stream != nil, err)
			}

			close(provider.release)
			if _, err := running.Text(); err != nil {
				t.Errorf("the running stream failed: %v", err)
			}
			if err := <-queued; err != nil {
				t.Errorf("the queued stream failed: %v", err)
			}
			waitForIdle(t, scheduler)
		})
	})
}

func waitFor(t *testing.T, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the condition waited for did not come about")
}

// A slot is held while a request is with the provider, and given back for the
// wait before a retry: a call sitting out its backoff is not using the
// provider, and holding its slot would stall every other caller behind it.
func TestASlotIsGivenBackForTheWaitBeforeARetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// The backoff is long enough that the second caller cannot be slow enough
		// to start after it. What is asserted is not how long the second caller
		// took, which a loaded machine stretches, but whether the first caller had
		// retried by the time the second finished: with the slot held through the
		// backoff it cannot have finished before the retry, and without it, it
		// finishes during the wait however late it started.
		provider := &gateProvider{hold: time.Millisecond, failFirst: true, retries: 2, backoff: 500 * time.Millisecond}
		ctx, scheduler := scheduledContext(t, SchedulerLimits{MaxConcurrent: 1, MaxQueued: 10})
		started := func(prompt string) int {
			count := 0
			for _, event := range provider.noted() {
				if event == "start "+prompt {
					count++
				}
			}
			return count
		}

		slow := make(chan error, 1)
		go func() {
			_, err := CallLLM(ctx, provider, "system", "retried", lx126Options)
			slow <- err
		}()
		// The first attempt has failed and the call is waiting to retry.
		waitFor(t, func() bool {
			for _, event := range provider.noted() {
				if event == "end retried" {
					return true
				}
			}
			return false
		})

		// Another caller gets the slot during that wait. Its own first attempt
		// fails too, which is fine: what matters is that it reached the provider.
		other := &gateProvider{hold: time.Millisecond}
		if _, err := CallLLM(ctx, other, "system", "other", lx126Options); err != nil {
			t.Fatalf("the second caller: %v", err)
		}
		if n := started("retried"); n != 1 {
			t.Errorf("the first caller had made %d attempts when the second finished, want 1: the slot was held through the first caller's backoff", n)
		}

		if err := <-slow; err != nil {
			t.Errorf("the retried call failed: %v", err)
		}
		if peak := provider.peak.Load(); peak > 1 {
			t.Errorf("%d attempts were in flight at once", peak)
		}
		waitForIdle(t, scheduler)
	})
}

// A stream holds its slot until it ends, however it ends.
func TestAStreamHoldsItsSlotUntilItEnds(t *testing.T) {
	opts := lx126Options
	opts.ResponseFormat = "text"

	endings := []struct {
		name string
		end  func(t *testing.T, stream *TextStream, provider *gateProvider)
	}{
		{"read to the end", func(t *testing.T, stream *TextStream, provider *gateProvider) {
			close(provider.release)
			if _, err := stream.Text(); err != nil {
				t.Errorf("Text: %v", err)
			}
		}},
		{"cancelled by the caller", func(t *testing.T, stream *TextStream, _ *gateProvider) {
			stream.Cancel()
		}},
		{"abandoned partway", func(t *testing.T, stream *TextStream, _ *gateProvider) {
			for range stream.All() {
				break
			}
		}},
	}
	for _, tc := range endings {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				provider := &gateProvider{release: make(chan struct{})}
				ctx, scheduler := scheduledContext(t, SchedulerLimits{MaxConcurrent: 1, MaxQueued: 10})

				stream, err := streamWith(ctx, provider, opts)
				if err != nil {
					t.Fatalf("the stream: %v", err)
				}
				waitFor(t, func() bool { return provider.inFlight.Load() == 1 })
				if got := scheduler.Stats().InFlight; got != 1 {
					t.Fatalf("the scheduler counts %d in flight while a stream is open, want 1", got)
				}

				tc.end(t, stream, provider)
				waitForIdle(t, scheduler)
			})
		})
	}
}

// A request that can run now is not a request waiting. The queue bound counted
// a request from the moment it asked until the dispatcher got round to
// admitting it, so two calls arriving together at an idle scheduler with a
// queue of one could refuse the second for a full queue while every slot was
// free. A client with a small queue saw refusals under no load at all.
func TestABurstIntoAnIdleSchedulerIsNotRefusedForAFullQueue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const slots = 8
		for round := range 40 {
			scheduler := NewScheduler(SchedulerLimits{MaxConcurrent: slots, MaxQueued: 1})
			release := make(chan struct{})
			start := make(chan struct{})
			results := make(chan error, slots)
			var running atomic.Int64

			for range slots {
				go func() {
					<-start
					_, err := Submit(context.Background(), scheduler, SubmitRequest{}, func(context.Context) (struct{}, error) {
						running.Add(1)
						<-release
						return struct{}{}, nil
					})
					results <- err
				}()
			}
			close(start)
			waitFor(t, func() bool { return running.Load() == slots || len(results) > 0 })
			if refused := len(results); refused > 0 {
				t.Fatalf("round %d: a call was refused with slots free: %v", round, <-results)
			}

			// Now it is full. The next call waits, and the one after has no room.
			waiting := make(chan error, 1)
			go func() {
				_, err := Submit(context.Background(), scheduler, SubmitRequest{}, func(context.Context) (struct{}, error) {
					return struct{}{}, nil
				})
				waiting <- err
			}()
			waitFor(t, func() bool { return scheduler.Stats().Queued == 1 })
			_, err := Submit(context.Background(), scheduler, SubmitRequest{}, func(context.Context) (struct{}, error) {
				return struct{}{}, nil
			})
			if !errors.Is(err, types.ErrAdmissionRejected) {
				t.Fatalf("round %d: err = %v, want an admission refusal with the queue full", round, err)
			}

			close(release)
			for range slots {
				if err := <-results; err != nil {
					t.Fatalf("round %d: an admitted call failed: %v", round, err)
				}
			}
			if err := <-waiting; err != nil {
				t.Fatalf("round %d: the queued call failed: %v", round, err)
			}
			_ = scheduler.Close(context.Background())
		}
	})
}

// A request whose own estimate is over a ceiling on what may be in flight can
// never be admitted. It used to be queued, where it waited until its caller
// gave up; it is refused when it asks.
func TestARequestThatCanNeverFitIsRefusedAndNotQueued(t *testing.T) {
	cases := []struct {
		name    string
		limits  SchedulerLimits
		req     SubmitRequest
		refused bool
	}{
		{"tokens over the ceiling", SchedulerLimits{MaxInFlightTokens: 1000}, SubmitRequest{EstimatedTokens: 1001}, true},
		{"tokens at the ceiling", SchedulerLimits{MaxInFlightTokens: 1000}, SubmitRequest{EstimatedTokens: 1000}, false},
		{"tokens under the ceiling", SchedulerLimits{MaxInFlightTokens: 1000}, SubmitRequest{EstimatedTokens: 10}, false},
		{"no estimate of tokens", SchedulerLimits{MaxInFlightTokens: 1000}, SubmitRequest{}, false},
		{"no ceiling on tokens", SchedulerLimits{}, SubmitRequest{EstimatedTokens: 1 << 40}, false},
		{"cost over the ceiling", SchedulerLimits{MaxInFlightCost: 0.5}, SubmitRequest{EstimatedCost: 0.75}, true},
		{"cost at the ceiling", SchedulerLimits{MaxInFlightCost: 0.5}, SubmitRequest{EstimatedCost: 0.5}, false},
		{"cost under the ceiling", SchedulerLimits{MaxInFlightCost: 0.5}, SubmitRequest{EstimatedCost: 0.01}, false},
		{"no ceiling on cost", SchedulerLimits{}, SubmitRequest{EstimatedCost: 1e9}, false},
		{"tokens fit and cost does not", SchedulerLimits{MaxInFlightTokens: 1000, MaxInFlightCost: 0.5}, SubmitRequest{EstimatedTokens: 10, EstimatedCost: 2}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				scheduler := NewScheduler(tc.limits)
				defer scheduler.Close(context.Background())
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()

				ran := false
				_, err := Submit(ctx, scheduler, tc.req, func(context.Context) (struct{}, error) {
					ran = true
					return struct{}{}, nil
				})
				if tc.refused {
					if !errors.Is(err, types.ErrAdmissionRejected) || ran {
						t.Fatalf("err = %v, ran = %v; want an admission refusal and nothing run", err, ran)
					}
					return
				}
				if err != nil || !ran {
					t.Fatalf("err = %v, ran = %v; want it run", err, ran)
				}
			})
		})
	}
}

// What a client's scheduler is told about each request: the provider, a
// priority that is not the lowest by accident, and estimates of what the
// request holds while it runs.
func TestAClientsSchedulerIsToldWhatTheRequestHolds(t *testing.T) {
	provider := &gateProvider{hold: time.Millisecond}
	long := strings.Repeat("word ", 400) // 2,000 characters, about 500 tokens

	cases := []struct {
		name    string
		limits  SchedulerLimits
		prompt  string
		refused bool
	}{
		{"a small prompt under a token ceiling", SchedulerLimits{MaxInFlightTokens: 100000}, "short", false},
		{"a prompt whose estimate is over the token ceiling", SchedulerLimits{MaxInFlightTokens: 100}, long, true},
		{"no ceilings", SchedulerLimits{}, long, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, _ := scheduledContext(t, tc.limits)
				_, err := CallLLM(ctx, provider, "system", tc.prompt, lx126Options)
				if tc.refused != errors.Is(err, types.ErrAdmissionRejected) {
					t.Fatalf("err = %v, want a refusal: %v", err, tc.refused)
				}
			})
		})
	}
}
