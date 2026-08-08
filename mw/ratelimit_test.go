package mw

import (
	"context"
	"errors"
	"testing"
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

func TestRateLimitMiddlewareDelegatesNameAndCost(t *testing.T) {
	fake := &fakeProvider{name: "fakeprovider"}
	wrapped := RateLimit(10, time.Second)(fake)

	if wrapped.Name() != "fakeprovider" {
		t.Fatalf("Name() = %q, want %q", wrapped.Name(), "fakeprovider")
	}
	if got := wrapped.EstimateCost(llm.CompletionRequest{}); got != 0.01 {
		t.Fatalf("EstimateCost() = %v, want 0.01", got)
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
