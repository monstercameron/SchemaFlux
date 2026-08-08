package mw

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// newTestRetryProvider builds a *retryProvider directly (rather than through
// the public Retry constructor) so a test can install the fake clock and a
// deterministic random source before the first call.
func newTestRetryProvider(next Handler, cfg retryConfig, fc *fakeClock, randFloat func() float64) *retryProvider {
	if randFloat == nil {
		randFloat = func() float64 { return 0 }
	}
	return &retryProvider{next: next, cfg: cfg, sleep: fc.sleep, randFloat: randFloat}
}

func TestRetrySucceedsOnFirstAttemptWithoutSleeping(t *testing.T) {
	fake := &fakeProvider{responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "ok"}}}}
	fc := newFakeClock(time.Unix(0, 0))
	rp := newTestRetryProvider(fake, retryConfig{maxAttempts: 3, baseDelay: time.Millisecond}, fc, nil)

	resp, err := rp.Complete(context.Background(), llm.CompletionRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("Content = %q, want %q", resp.Content, "ok")
	}
	if fake.calls != 1 {
		t.Fatalf("calls = %d, want 1", fake.calls)
	}
	if len(fc.waits()) != 0 {
		t.Fatalf("a first-attempt success must not sleep, slept %v", fc.waits())
	}
}

func TestRetryRetriesOnRetryableStatusThenSucceeds(t *testing.T) {
	fake := &fakeProvider{
		responses: []fakeResponse{
			{err: llm.NewAPIError("fake", "m", 503, "")}, // provider unavailable: retryable
			{resp: llm.CompletionResponse{Content: "ok"}},
		},
	}
	fc := newFakeClock(time.Unix(0, 0))
	rp := newTestRetryProvider(fake, retryConfig{maxAttempts: 3, baseDelay: 10 * time.Millisecond}, fc, nil)

	resp, err := rp.Complete(context.Background(), llm.CompletionRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("Content = %q, want %q", resp.Content, "ok")
	}
	if fake.calls != 2 {
		t.Fatalf("calls = %d, want 2", fake.calls)
	}
	if len(fc.waits()) != 1 {
		t.Fatalf("expected exactly one wait between attempts, got %v", fc.waits())
	}
}

func TestRetryDoesNotRetryNonRetryableStatus(t *testing.T) {
	fake := &fakeProvider{
		responses: []fakeResponse{
			{err: llm.NewAPIError("fake", "m", 400, "")}, // invalid request: not retryable
		},
	}
	fc := newFakeClock(time.Unix(0, 0))
	rp := newTestRetryProvider(fake, retryConfig{maxAttempts: 5, baseDelay: 10 * time.Millisecond}, fc, nil)

	_, err := rp.Complete(context.Background(), llm.CompletionRequest{})
	if err == nil {
		t.Fatal("expected the 400 to be returned unretried")
	}
	if fake.calls != 1 {
		t.Fatalf("calls = %d, want 1 (a 400 must fail fast)", fake.calls)
	}
	if len(fc.waits()) != 0 {
		t.Fatalf("a non-retryable failure must not sleep, slept %v", fc.waits())
	}
}

func TestRetryStopsAtMaxAttemptsAndReturnsLastError(t *testing.T) {
	fake := &fakeProvider{
		responses: []fakeResponse{
			{err: llm.NewAPIError("fake", "m", 503, "")},
		},
	}
	fc := newFakeClock(time.Unix(0, 0))
	rp := newTestRetryProvider(fake, retryConfig{maxAttempts: 3, baseDelay: time.Millisecond}, fc, nil)

	_, err := rp.Complete(context.Background(), llm.CompletionRequest{})
	if err == nil {
		t.Fatal("expected an error after exhausting attempts")
	}
	if fake.calls != 3 {
		t.Fatalf("calls = %d, want 3 (maxAttempts)", fake.calls)
	}
	if len(fc.waits()) != 2 {
		t.Fatalf("expected 2 waits between 3 attempts, got %v", fc.waits())
	}
}

func TestRetryUnknownKindIsNotRetried(t *testing.T) {
	// An error the taxonomy has no opinion about must fail fast, the same
	// way a KindUnknown is neither guessed retryable nor guessed terminal
	// anywhere else in this module.
	fake := &fakeProvider{responses: []fakeResponse{{err: errors.New("boom")}}}
	fc := newFakeClock(time.Unix(0, 0))
	rp := newTestRetryProvider(fake, retryConfig{maxAttempts: 3, baseDelay: time.Millisecond}, fc, nil)

	_, err := rp.Complete(context.Background(), llm.CompletionRequest{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if fake.calls != 1 {
		t.Fatalf("calls = %d, want 1", fake.calls)
	}
}

func TestRetryHonoursServerStatedRetryAfter(t *testing.T) {
	fake := &fakeProvider{
		responses: []fakeResponse{
			{err: &llm.RateLimitError{
				APIError:   llm.NewAPIError("fake", "m", 429, ""),
				RetryAfter: 47 * time.Second,
			}},
			{resp: llm.CompletionResponse{Content: "ok"}},
		},
	}
	fc := newFakeClock(time.Unix(0, 0))
	// A base delay of 1ms would jitter to something tiny; the stated
	// Retry-After must win regardless.
	rp := newTestRetryProvider(fake, retryConfig{maxAttempts: 3, baseDelay: time.Millisecond}, fc, nil)

	if _, err := rp.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	waits := fc.waits()
	if len(waits) != 1 || waits[0] != 47*time.Second {
		t.Fatalf("waits = %v, want [47s]", waits)
	}
}

func TestRetryHonoursOperationErrorRetryAfter(t *testing.T) {
	// mw.RateLimit's own refusal carries RetryAfter on a *types.OperationError,
	// not an *llm.RateLimitError. Retry has to read both, or a client-side
	// throttle would fall back to a guessed jitter instead of the exact wait
	// the limiter already computed.
	fake := &fakeProvider{
		responses: []fakeResponse{
			{err: &types.OperationError{Kind: types.KindRateLimited, RetryAfter: 5 * time.Second}},
			{resp: llm.CompletionResponse{Content: "ok"}},
		},
	}
	fc := newFakeClock(time.Unix(0, 0))
	rp := newTestRetryProvider(fake, retryConfig{maxAttempts: 3, baseDelay: time.Millisecond}, fc, nil)

	if _, err := rp.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	waits := fc.waits()
	if len(waits) != 1 || waits[0] != 5*time.Second {
		t.Fatalf("waits = %v, want [5s]", waits)
	}
}

func TestRetryContextCancellationDuringWaitAborts(t *testing.T) {
	fake := &fakeProvider{
		responses: []fakeResponse{
			{err: llm.NewAPIError("fake", "m", 503, "")},
			{resp: llm.CompletionResponse{Content: "ok"}},
		},
	}
	fc := newFakeClock(time.Unix(0, 0))
	rp := newTestRetryProvider(fake, retryConfig{maxAttempts: 3, baseDelay: time.Millisecond}, fc, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := rp.Complete(ctx, llm.CompletionRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Complete() error = %v, want context.Canceled", err)
	}
	if fake.calls != 1 {
		t.Fatalf("calls = %d, want 1 (the wait after the first failure must have aborted)", fake.calls)
	}
}

func TestRetryUsesProviderRetryPolicyWhenNoOptionsGiven(t *testing.T) {
	fake := &fakeProvider{
		maxRetries: 2, // RetryPolicy() -> (2, backoff), so maxAttempts should resolve to 3
		backoff:    10 * time.Millisecond,
		responses: []fakeResponse{
			{err: llm.NewAPIError("fake", "m", 503, "")},
			{err: llm.NewAPIError("fake", "m", 503, "")},
			{resp: llm.CompletionResponse{Content: "ok"}},
		},
	}
	fc := newFakeClock(time.Unix(0, 0))
	// No maxAttempts/baseDelay set: must fall back to fake.RetryPolicy().
	rp := newTestRetryProvider(fake, retryConfig{}, fc, nil)

	resp, err := rp.Complete(context.Background(), llm.CompletionRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("Content = %q, want %q", resp.Content, "ok")
	}
	if fake.calls != 3 {
		t.Fatalf("calls = %d, want 3 (provider RetryPolicy allowed 2 retries)", fake.calls)
	}
}

func TestRetryFallsBackToSaneDefaultsWhenProviderGivesNothingUseful(t *testing.T) {
	fake := &fakeProvider{
		// RetryPolicy() -> (0, 0): a provider that has nothing to say.
		responses: []fakeResponse{
			{err: llm.NewAPIError("fake", "m", 503, "")},
			{resp: llm.CompletionResponse{Content: "ok"}},
		},
	}
	fc := newFakeClock(time.Unix(0, 0))
	rp := newTestRetryProvider(fake, retryConfig{}, fc, nil)

	if _, err := rp.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.calls != 2 {
		t.Fatalf("calls = %d, want 2 (default policy must allow at least one retry)", fake.calls)
	}
}

func TestWithMaxAttemptsZeroOrNegativeDefaultsToOne(t *testing.T) {
	cfg := retryConfig{}
	WithMaxAttempts(0)(&cfg)
	if cfg.maxAttempts != 1 {
		t.Fatalf("maxAttempts = %d, want 1", cfg.maxAttempts)
	}
	WithMaxAttempts(-5)(&cfg)
	if cfg.maxAttempts != 1 {
		t.Fatalf("maxAttempts = %d, want 1", cfg.maxAttempts)
	}
}

func TestDecorrelatedJitterStaysWithinBaseAndCap(t *testing.T) {
	base := 100 * time.Millisecond
	maxDelay := time.Second
	prev := base

	for i, r := range []float64{0, 0.25, 0.5, 0.75, 0.999} {
		wait := decorrelatedJitter(base, prev, maxDelay, func() float64 { return r })
		if wait < base {
			t.Fatalf("case %d: wait = %v, want >= base %v", i, wait, base)
		}
		if wait > maxDelay {
			t.Fatalf("case %d: wait = %v, want <= maxDelay %v", i, wait, maxDelay)
		}
		prev = wait
	}
}

func TestDecorrelatedJitterCapsAtMax(t *testing.T) {
	base := time.Second
	prev := 10 * time.Second // upper = prev*3 = 30s, far past the cap
	maxDelay := 2 * time.Second

	wait := decorrelatedJitter(base, prev, maxDelay, func() float64 { return 1 })
	if wait != maxDelay {
		t.Fatalf("wait = %v, want exactly maxDelay %v", wait, maxDelay)
	}
}

func TestDecorrelatedJitterNeverAlignsAcrossIndependentCallers(t *testing.T) {
	// Two "callers" with the same base and previous wait but different
	// random draws must not land on the same instant -- the entire point of
	// jitter over a fixed exponential schedule.
	base := 50 * time.Millisecond
	prev := 50 * time.Millisecond
	maxDelay := 5 * time.Second

	a := decorrelatedJitter(base, prev, maxDelay, func() float64 { return 0.1 })
	b := decorrelatedJitter(base, prev, maxDelay, func() float64 { return 0.9 })
	if a == b {
		t.Fatalf("expected different waits for different random draws, both = %v", a)
	}
}

func TestRetryProviderDelegatesRetryPolicy(t *testing.T) {
	fake := &fakeProvider{maxRetries: 7, backoff: 3 * time.Second}
	fc := newFakeClock(time.Unix(0, 0))
	rp := newTestRetryProvider(fake, retryConfig{}, fc, nil)

	retries, backoff := rp.RetryPolicy()
	if retries != 7 || backoff != 3*time.Second {
		t.Fatalf("RetryPolicy() = (%d, %v), want (7, 3s)", retries, backoff)
	}
}

func TestRetryMiddlewareIntegratesWithNextRetryPolicy(t *testing.T) {
	fake := &fakeProvider{
		name:       "prov",
		maxRetries: 1,
		backoff:    time.Millisecond,
		responses: []fakeResponse{
			{err: llm.NewAPIError("prov", "m", 500, "")},
			{resp: llm.CompletionResponse{Content: "ok"}},
		},
	}
	mwFn := Retry()
	wrapped := mwFn(fake)
	rp, ok := wrapped.(*retryProvider)
	if !ok {
		t.Fatal("Retry() did not return a *retryProvider")
	}
	fc := newFakeClock(time.Unix(0, 0))
	rp.sleep = fc.sleep

	resp, err := wrapped.Complete(context.Background(), llm.CompletionRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("Content = %q, want %q", resp.Content, "ok")
	}
	if wrapped.Name() != "prov" {
		t.Fatalf("Name() = %q, want %q", wrapped.Name(), "prov")
	}
}
