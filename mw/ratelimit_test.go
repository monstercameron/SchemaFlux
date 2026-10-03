package mw

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// newTestBucket builds a tokenBucket wired to a fakeClock, so every test in
// this file runs without waiting on real time.
func newTestBucket(n int, interval time.Duration, capacity int, fc *fakeClock) *tokenBucket {
	b := newTokenBucket(n, interval, capacity, fc.Now)
	b.sleep = fc.sleep
	return b
}

func TestTokenBucketAllowsBurstUpToCapacity(t *testing.T) {
	fc := newFakeClock(time.Unix(0, 0))
	b := newTestBucket(1, time.Second, 3, fc)

	for i := 0; i < 3; i++ {
		if err := b.wait(context.Background(), false); err != nil {
			t.Fatalf("request %d: unexpected error: %v", i, err)
		}
	}
	if len(fc.waits()) != 0 {
		t.Fatalf("burst within capacity should not have waited, slept %v", fc.waits())
	}
}

func TestTokenBucketBlocksPastCapacity(t *testing.T) {
	fc := newFakeClock(time.Unix(0, 0))
	b := newTestBucket(1, time.Second, 1, fc)

	if err := b.wait(context.Background(), false); err != nil {
		t.Fatalf("first request: unexpected error: %v", err)
	}
	// The bucket held one token; the second request must wait roughly one
	// interval for the next token to refill.
	if err := b.wait(context.Background(), false); err != nil {
		t.Fatalf("second request: unexpected error: %v", err)
	}
	waits := fc.waits()
	if len(waits) != 1 {
		t.Fatalf("expected exactly one wait, got %v", waits)
	}
	if waits[0] < 900*time.Millisecond || waits[0] > 1100*time.Millisecond {
		t.Fatalf("wait = %v, want ~1s", waits[0])
	}
}

func TestTokenBucketRefillsOverTime(t *testing.T) {
	fc := newFakeClock(time.Unix(0, 0))
	b := newTestBucket(2, time.Second, 2, fc)

	// Drain the bucket.
	if err := b.wait(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if err := b.wait(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	// Advance the clock by a full second without going through wait/sleep
	// (simulating time passing between requests, not a wait inside one).
	fc.mu.Lock()
	fc.now = fc.now.Add(time.Second)
	fc.mu.Unlock()

	ok, wait := b.tryAcquire()
	if !ok {
		t.Fatalf("expected a token to be available after a full refill interval, wait = %v", wait)
	}
}

func TestTokenBucketRejectModeFailsWithoutWaiting(t *testing.T) {
	fc := newFakeClock(time.Unix(0, 0))
	b := newTestBucket(1, time.Second, 1, fc)

	if err := b.wait(context.Background(), true); err != nil {
		t.Fatalf("first request should be admitted: %v", err)
	}
	err := b.wait(context.Background(), true)
	if err == nil {
		t.Fatal("second request should have been rejected")
	}
	if len(fc.waits()) != 0 {
		t.Fatalf("reject mode must not sleep, slept %v", fc.waits())
	}

	var opErr *types.OperationError
	if !errors.As(err, &opErr) {
		t.Fatalf("error is not a *types.OperationError: %v", err)
	}
	if opErr.Kind != types.KindRateLimited {
		t.Fatalf("Kind = %v, want KindRateLimited", opErr.Kind)
	}
	if opErr.RetryAfter <= 0 {
		t.Fatalf("RetryAfter = %v, want > 0", opErr.RetryAfter)
	}
}

func TestTokenBucketRejectDoesNotConsumeAToken(t *testing.T) {
	// A refused request must not have spent a slot: refuse, then wait out
	// the refill, then confirm a full-capacity burst is still available.
	fc := newFakeClock(time.Unix(0, 0))
	b := newTestBucket(1, time.Second, 1, fc)

	if err := b.wait(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if err := b.wait(context.Background(), true); err == nil {
		t.Fatal("expected rejection while the bucket was empty")
	}

	fc.mu.Lock()
	fc.now = fc.now.Add(time.Second)
	fc.mu.Unlock()

	if ok, _ := b.tryAcquire(); !ok {
		t.Fatal("expected exactly one token to have refilled, not been eaten by the refusal")
	}
}

func TestTokenBucketHonoursContextDeadline(t *testing.T) {
	fc := newFakeClock(time.Unix(0, 0))
	b := newTestBucket(1, time.Hour, 1, fc) // refill so slow the wait will not finish

	if err := b.wait(context.Background(), false); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := b.wait(ctx, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("wait() = %v, want context.Canceled", err)
	}
}

func TestTokenBucketAlreadyCanceledContextFailsFast(t *testing.T) {
	fc := newFakeClock(time.Unix(0, 0))
	b := newTestBucket(1, time.Second, 5, fc)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := b.wait(ctx, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait() = %v, want context.Canceled", err)
	}
	if len(fc.waits()) != 0 {
		t.Fatal("an already-canceled context must not attempt to sleep")
	}
}

func TestRateLimitZeroOrNegativeInputsDefaultSafely(t *testing.T) {
	// n<=0 and interval<=0 must not panic (divide by zero) or produce a
	// limiter that admits nothing.
	mwFn := RateLimit(0, 0)
	fake := &fakeProvider{responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "ok"}}}}
	wrapped := mwFn(fake)

	if _, err := wrapped.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
		t.Fatalf("unexpected error with defaulted RateLimit(0, 0): %v", err)
	}
}

func TestRateLimitWithBurstAllowsLargerInitialAllowance(t *testing.T) {
	fc := newFakeClock(time.Unix(0, 0))
	b := newTokenBucket(1, time.Second, 5, fc.Now) // rate 1/s, burst capacity 5
	b.sleep = fc.sleep

	for i := 0; i < 5; i++ {
		if err := b.wait(context.Background(), true); err != nil {
			t.Fatalf("request %d within burst capacity was rejected: %v", i, err)
		}
	}
	if err := b.wait(context.Background(), true); err == nil {
		t.Fatal("request beyond burst capacity should have been rejected")
	}
}

func TestRateLimitMiddlewareDelegatesToNextOnSuccess(t *testing.T) {
	fake := &fakeProvider{responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "hello"}}}}
	wrapped := RateLimit(10, time.Second)(fake)

	resp, err := wrapped.Complete(context.Background(), llm.CompletionRequest{UserPrompt: "hi"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "hello" {
		t.Fatalf("Content = %q, want %q", resp.Content, "hello")
	}
	if fake.calls != 1 {
		t.Fatalf("calls = %d, want 1", fake.calls)
	}
}

func TestRateLimitRejectErrorFlowsThroughClassify(t *testing.T) {
	// A caller building recovery logic on llm.Classify must see the same
	// kind for a client-side refusal as for a provider's own 429 -- that is
	// the entire reason Reject() builds a *types.OperationError instead of
	// a bespoke error type.
	fc := newFakeClock(time.Unix(0, 0))
	b := newTestBucket(1, time.Hour, 1, fc)
	if err := b.wait(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	err := b.wait(context.Background(), true)
	if err == nil {
		t.Fatal("expected rejection")
	}
	got := llm.Classify(err)
	if got != types.KindRateLimited {
		t.Fatalf("llm.Classify(reject error) = %v, want KindRateLimited", got)
	}
	if !(&types.OperationError{Kind: got}).Retryable() {
		t.Fatal("a client-side rate limit refusal should be classified retryable, same as a provider 429")
	}
}

func TestWithBurstSetsACapacityDistinctFromTheRefillRate(t *testing.T) {
	fc := newFakeClock(time.Unix(0, 0))
	bucket := newTestBucket(1, time.Second, 5, fc) // burst overridden via WithBurst below is exercised through RateLimit

	// newTestBucket bypasses WithBurst; exercise the option itself through
	// the public constructor and confirm the resulting bucket allows a burst
	// of 5 despite a refill rate of 1/sec.
	_ = bucket
	fake := &fakeProvider{name: "p", responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "ok"}}}}
	limiter := RateLimit(1, time.Second, WithBurst(5))(fake)

	for i := 0; i < 5; i++ {
		if _, err := limiter.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
			t.Fatalf("call %d: burst of 5 should be immediate, got %v", i+1, err)
		}
	}
}

func TestWithBurstIgnoresANonPositiveValue(t *testing.T) {
	fake := &fakeProvider{name: "p", responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "ok"}}}}
	// A non-positive burst must not zero out the bucket's capacity -- it
	// falls back to n, same as never having called WithBurst at all.
	limiter := RateLimit(3, time.Second, WithBurst(0))(fake)
	for i := 0; i < 3; i++ {
		if _, err := limiter.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
			t.Fatalf("call %d: WithBurst(0) should not shrink capacity below n=3, got %v", i+1, err)
		}
	}
}

// Reject, wired through the public RateLimit constructor rather than
// exercised only via the internal tokenBucket, fails a call immediately
// once the burst is spent instead of blocking for a slot.
func TestRejectOptionFailsImmediatelyThroughTheConstructor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := &fakeProvider{name: "p", responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "ok"}}}}
		limiter := RateLimit(1, time.Hour, Reject())(fake)

		if _, err := limiter.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
			t.Fatalf("first call should consume the single burst slot without error: %v", err)
		}

		start := time.Now()
		_, err := limiter.Complete(context.Background(), llm.CompletionRequest{})
		if err == nil {
			t.Fatal("second call within an hour-long interval should have been rejected, not served")
		}
		if elapsed := time.Since(start); elapsed != 0 {
			t.Errorf("Reject took %v on the bubble's clock; it must fail immediately rather than waiting", elapsed)
		}
		if types.KindOf(err) != types.KindRateLimited {
			t.Errorf("Kind = %v, want KindRateLimited", types.KindOf(err))
		}
	})
}

// End-to-end through the real constructor and the real sleep hook: a second
// call issued faster than the refill rate blocks for the refill interval and
// then proceeds, rather than being rejected or hanging. The wait is on the
// timers of a synctest bubble, so it is exactly the interval.
func TestRateLimitWithRealClockBlocksThenProceeds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := &fakeProvider{name: "p", responses: []fakeResponse{
			{resp: llm.CompletionResponse{Content: "first"}},
			{resp: llm.CompletionResponse{Content: "second"}},
		}}
		limiter := RateLimit(1, 30*time.Millisecond)(fake)

		ctx := context.Background()
		if _, err := limiter.Complete(ctx, llm.CompletionRequest{}); err != nil {
			t.Fatalf("first call: %v", err)
		}

		start := time.Now()
		resp, err := limiter.Complete(ctx, llm.CompletionRequest{})
		if err != nil {
			t.Fatalf("second call: %v", err)
		}
		if resp.Content != "second" {
			t.Errorf("Content = %q, want %q", resp.Content, "second")
		}
		// One token every 30 ms and the first spent: the second call waits the
		// 30 ms for the next one, on the bubble's clock.
		if elapsed := time.Since(start); elapsed != 30*time.Millisecond {
			t.Errorf("the second call waited %v, want 30ms for the bucket to refill", elapsed)
		}
	})
}

// An interval too long for its refill to be counted in a Duration used to give a
// negative wait: a rejected caller was told to retry in the past, and a waiting
// one did not sleep.
func TestTokenBucketWaitIsBoundedForAHugeInterval(t *testing.T) {
	cases := []struct {
		name     string
		n        int
		interval time.Duration
		want     time.Duration
	}{
		{"the longest interval a Duration holds", 1, time.Duration(1<<63 - 1), llm.MaxRetryAfter},
		{"a day", 1, 24 * time.Hour, llm.MaxRetryAfter},
		{"an hour", 1, time.Hour, llm.MaxRetryAfter},
		{"a refill under the bound is the refill", 1, time.Minute, time.Minute},
		{"a second", 1, time.Second, time.Second},
		{"two a minute", 2, time.Minute, 30 * time.Second},
		{"a hundred a second", 100, time.Second, 10 * time.Millisecond},
		{"exactly the bound", 1, llm.MaxRetryAfter, llm.MaxRetryAfter},
		{"a billion a second", 1_000_000_000, time.Second, time.Nanosecond},
		{"a huge count over a huge interval", 1 << 40, time.Duration(1<<63 - 1), 8388608 * time.Nanosecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := newFakeClock(time.Unix(0, 0))
			b := newTestBucket(tc.n, tc.interval, 1, fc)
			if ok, _ := b.tryAcquire(); !ok {
				t.Fatal("the first token is not there")
			}
			ok, wait := b.tryAcquire()
			if ok {
				t.Fatal("a second token was there")
			}
			if wait <= 0 || wait > llm.MaxRetryAfter {
				t.Errorf("wait = %v, want a positive wait of at most %v", wait, llm.MaxRetryAfter)
			}
			if wait != tc.want {
				t.Errorf("wait = %v, want %v", wait, tc.want)
			}
		})
	}

	t.Run("a rejected caller is told a wait that is ahead", func(t *testing.T) {
		fc := newFakeClock(time.Unix(0, 0))
		b := newTestBucket(1, time.Duration(1<<63-1), 1, fc)
		b.wait(context.Background(), true)
		err := b.wait(context.Background(), true)
		var refusal *types.OperationError
		if !errors.As(err, &refusal) || refusal.RetryAfter <= 0 {
			t.Errorf("err = %v, want a refusal that states a wait that is ahead", err)
		}
	})

	t.Run("a waiting caller sleeps and does not spin", func(t *testing.T) {
		fc := newFakeClock(time.Unix(0, 0))
		b := newTestBucket(1, time.Duration(1<<63-1), 1, fc)
		b.wait(context.Background(), false)
		ctx, cancel := context.WithCancel(context.Background())
		sleeps := 0
		b.sleep = func(ctx context.Context, d time.Duration) error {
			if d <= 0 {
				t.Errorf("asked to sleep %v", d)
			}
			if sleeps++; sleeps == 3 {
				cancel()
			}
			return fc.sleep(ctx, d)
		}
		if err := b.wait(ctx, false); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want the cancellation that ended the wait", err)
		}
		if sleeps != 3 {
			t.Errorf("slept %d times before the cancellation, want 3", sleeps)
		}
	})
}

// A wait longer than llm.MaxRetryAfter is served in sleeps of at most that, and
// the caller gets its token when it is due and not before. It must not spin when
// a sleep ends a nanosecond short of the refill.
func TestRateLimitWaitsOutAnIntervalLongerThanTheBoundInBoundedSleeps(t *testing.T) {
	for _, interval := range []time.Duration{time.Hour, 7*time.Minute + 13*time.Second + 7*time.Nanosecond, 3 * llm.MaxRetryAfter, llm.MaxRetryAfter + time.Nanosecond} {
		t.Run(interval.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var calls int
				provider := completeFunc(func(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
					calls++
					return llm.CompletionResponse{Content: "ok"}, nil
				})
				limited := RateLimit(1, interval)(provider)
				if _, err := limited.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
					t.Fatal(err)
				}
				start := time.Now()
				if _, err := limited.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
					t.Fatal(err)
				}
				if waited := time.Since(start); waited < interval || waited > interval+time.Microsecond {
					t.Errorf("the second request waited %v, want the interval %v", waited, interval)
				}
				if calls != 2 {
					t.Errorf("%d calls, want 2", calls)
				}
			})
		})
	}
}
