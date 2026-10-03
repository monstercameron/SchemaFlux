package mw

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/telemetry"
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

// The context ends while the first attempt is running: the failure that comes
// back is retryable, so what stops the retry is the wait between attempts.
func TestRetryContextCancellationDuringWaitAborts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	provider := completeFunc(func(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
		calls++
		if calls == 1 {
			cancel()
			return llm.CompletionResponse{Content: "partial text from a failing provider", Model: "m"}, llm.NewAPIError("fake", "m", 503, "")
		}
		return llm.CompletionResponse{Content: "ok"}, nil
	})
	fc := newFakeClock(time.Unix(0, 0))
	rp := newTestRetryProvider(provider, retryConfig{maxAttempts: 3, baseDelay: time.Millisecond}, fc, nil)

	resp, err := rp.Complete(ctx, llm.CompletionRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Complete() error = %v, want context.Canceled", err)
	}
	if resp.Content != "" || resp.Model != "" {
		t.Fatalf("resp = %+v, want an empty response beside the failure", resp)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (the wait after the first failure must have aborted)", calls)
	}
}

// A wait the error states is honoured up to llm.MaxRetryAfter and no further,
// whichever layer produced it.
func TestRetryClampsAStatedWaitToTheCeiling(t *testing.T) {
	rateLimit := func(wait time.Duration) error {
		return &llm.RateLimitError{APIError: llm.NewAPIError("fake", "m", 429, ""), RetryAfter: wait}
	}
	operation := func(wait time.Duration) error {
		return &types.OperationError{Kind: types.KindRateLimited, RetryAfter: wait}
	}
	cases := []struct {
		name string
		err  error
		want time.Duration
	}{
		{"an operation error of an hour", operation(time.Hour), llm.MaxRetryAfter},
		{"an operation error of a day", operation(24 * time.Hour), llm.MaxRetryAfter},
		{"an operation error just over the ceiling", operation(llm.MaxRetryAfter + time.Nanosecond), llm.MaxRetryAfter},
		{"an operation error at the ceiling", operation(llm.MaxRetryAfter), llm.MaxRetryAfter},
		{"an operation error under the ceiling", operation(7 * time.Second), 7 * time.Second},
		{"an operation error of a nanosecond", operation(time.Nanosecond), time.Nanosecond},
		{"a rate limit error of an hour", rateLimit(time.Hour), llm.MaxRetryAfter},
		{"a rate limit error of a day", rateLimit(24 * time.Hour), llm.MaxRetryAfter},
		{"a rate limit error under the ceiling", rateLimit(30 * time.Second), 30 * time.Second},
		{"a rate limit error at the ceiling", rateLimit(llm.MaxRetryAfter), llm.MaxRetryAfter},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeProvider{responses: []fakeResponse{{err: tc.err}, {resp: llm.CompletionResponse{Content: "ok"}}}}
			fc := newFakeClock(time.Unix(0, 0))
			rp := newTestRetryProvider(fake, retryConfig{maxAttempts: 3, baseDelay: time.Millisecond}, fc, nil)

			if _, err := rp.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if waits := fc.waits(); len(waits) != 1 || waits[0] != tc.want {
				t.Fatalf("waits = %v, want [%v]", waits, tc.want)
			}
		})
	}
}

func TestStatedRetryAfter(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		want   time.Duration
		stated bool
	}{
		{"nothing stated", errors.New("x"), 0, false},
		{"an operation error with no wait", &types.OperationError{Kind: types.KindRateLimited}, 0, false},
		{"a negative wait", &types.OperationError{Kind: types.KindRateLimited, RetryAfter: -time.Second}, 0, false},
		{"an operation error", &types.OperationError{Kind: types.KindRateLimited, RetryAfter: 3 * time.Second}, 3 * time.Second, true},
		{"an operation error over the ceiling", &types.OperationError{Kind: types.KindRateLimited, RetryAfter: time.Hour}, llm.MaxRetryAfter, true},
		{"a rate limit error", &llm.RateLimitError{APIError: llm.NewAPIError("p", "m", 429, ""), RetryAfter: 9 * time.Second}, 9 * time.Second, true},
		{"a rate limit error with no wait", &llm.RateLimitError{APIError: llm.NewAPIError("p", "m", 429, "")}, 0, false},
		{"a rate limit error over the ceiling", &llm.RateLimitError{APIError: llm.NewAPIError("p", "m", 429, ""), RetryAfter: 10 * time.Hour}, llm.MaxRetryAfter, true},
		{"a wrapped operation error", fmt.Errorf("wrapped: %w", &types.OperationError{Kind: types.KindRateLimited, RetryAfter: time.Second}), time.Second, true},
		{"a wrapped rate limit error", fmt.Errorf("wrapped: %w", &llm.RateLimitError{APIError: llm.NewAPIError("p", "m", 429, ""), RetryAfter: 2 * time.Second}), 2 * time.Second, true},
		{"an api error that is not a rate limit", llm.NewAPIError("p", "m", 503, ""), 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, stated := statedRetryAfter(tc.err)
			if got != tc.want || stated != tc.stated {
				t.Fatalf("statedRetryAfter = (%v, %v), want (%v, %v)", got, stated, tc.want, tc.stated)
			}
		})
	}
}

// A failure that is not retried comes back with an empty response, as every
// other failure in the library does.
func TestRetryReturnsAnEmptyResponseBesideANonRetryableError(t *testing.T) {
	leaky := llm.CompletionResponse{Content: "partial text from a failing provider", Model: "m"}
	for _, err := range []error{
		llm.NewAPIError("fake", "m", 400, ""),
		llm.NewAPIError("fake", "m", 401, ""),
		types.Newf(types.KindPolicyViolation, "x", "no"),
		types.Newf(types.KindBudgetExceeded, "x", "no"),
		errors.New("unclassified"),
	} {
		fake := &fakeProvider{responses: []fakeResponse{{resp: leaky, err: err}}}
		rp := newTestRetryProvider(fake, retryConfig{maxAttempts: 3, baseDelay: time.Millisecond}, newFakeClock(time.Unix(0, 0)), nil)
		resp, gotErr := rp.Complete(context.Background(), llm.CompletionRequest{})
		if gotErr == nil || resp.Content != "" || resp.Model != "" {
			t.Errorf("%v: resp %+v, err %v; want an empty response beside the error", err, resp, gotErr)
		}
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

func TestWithMaxDelayCapsTheComputedBackoff(t *testing.T) {
	fake := &fakeProvider{name: "p", maxRetries: 5, backoff: time.Second, responses: []fakeResponse{
		{err: &types.OperationError{Kind: types.KindRateLimited, Message: "429"}},
		{err: &types.OperationError{Kind: types.KindRateLimited, Message: "429"}},
		{resp: llm.CompletionResponse{Content: "ok"}},
	}}

	var waits []time.Duration
	rp := &retryProvider{
		next: fake,
		cfg:  retryConfig{maxAttempts: 5, baseDelay: time.Second, maxDelay: 2 * time.Millisecond},
		sleep: func(ctx context.Context, d time.Duration) error {
			waits = append(waits, d)
			return nil
		},
		randFloat: func() float64 { return 1.0 }, // pushes the uncapped jitter to its maximum
	}

	if _, err := rp.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	for i, wait := range waits {
		if wait > 2*time.Millisecond {
			t.Errorf("wait %d = %v, want capped at the 2ms WithMaxDelay ceiling", i, wait)
		}
	}
	if len(waits) == 0 {
		t.Fatal("expected at least one wait between the two failing attempts and the success")
	}
}

func TestWithMaxDelayOptionIgnoresANonPositiveValue(t *testing.T) {
	cfg := retryConfig{}
	WithMaxDelay(0)(&cfg)
	if cfg.maxDelay != 0 {
		t.Errorf("maxDelay = %v after WithMaxDelay(0), want unchanged (0)", cfg.maxDelay)
	}
	WithMaxDelay(-time.Second)(&cfg)
	if cfg.maxDelay != 0 {
		t.Errorf("maxDelay = %v after WithMaxDelay(negative), want unchanged (0)", cfg.maxDelay)
	}
	WithMaxDelay(5 * time.Second)(&cfg)
	if cfg.maxDelay != 5*time.Second {
		t.Errorf("maxDelay = %v, want 5s after a positive WithMaxDelay", cfg.maxDelay)
	}
}

// An open circuit is a refusal made without sending anything, by a breaker that
// stays open far longer than a backoff. Repeating it is the same refusal after a
// wait, and a Fallback outside the Retry would be kept from using the next route.
func TestRetryReturnsAnOpenCircuitAtOnce(t *testing.T) {
	fake := &fakeProvider{responses: []fakeResponse{{err: types.Newf(types.KindCircuitOpen, "circuit", "open")}}}
	fc := newFakeClock(time.Unix(0, 0))
	rp := newTestRetryProvider(fake, retryConfig{maxAttempts: 5, baseDelay: time.Second}, fc, nil)

	_, err := rp.Complete(context.Background(), llm.CompletionRequest{})
	if !errors.Is(err, types.ErrCircuitOpen) {
		t.Fatalf("error = %v, want the open circuit", err)
	}
	if fake.calls != 1 || len(fc.waits()) != 0 {
		t.Errorf("an open circuit was repeated: %d calls, waits %v", fake.calls, fc.waits())
	}
}

// With a Retry between a Fallback and a CircuitBreaker, as the package comment
// of the version 2 middleware orders them, a primary whose circuit is open is
// left for the alternate without a wait.
func TestFallbackLeavesAPrimaryWhoseCircuitIsOpenWithoutWaitingForARetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		primary := &fakeProvider{name: "primary", responses: []fakeResponse{{err: llm.NewAPIError("primary", "m", 503, "")}}}
		alternate := &fakeProvider{name: "alternate", responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "from the alternate"}}}}
		breaker := CircuitBreaker(ops.CircuitBreakerConfig{FailureThreshold: 1, OpenDuration: time.Hour})
		chain := Chain(primary,
			Fallback([]FallbackRoute{{Handler: alternate}}),
			Retry(WithMaxAttempts(3), WithBaseDelay(10*time.Second)),
			breaker)

		// The first request opens the circuit; the Retry repeats the failure into
		// the open circuit and the Fallback is reached after the one wait the
		// failure itself earned.
		if resp, err := chain.Complete(context.Background(), llm.CompletionRequest{Model: "m"}); err != nil || resp.Content != "from the alternate" {
			t.Fatalf("first request: %q, %v", resp.Content, err)
		}

		start := time.Now()
		resp, err := chain.Complete(context.Background(), llm.CompletionRequest{Model: "m"})
		if err != nil || resp.Content != "from the alternate" {
			t.Fatalf("second request: %q, %v", resp.Content, err)
		}
		if waited := time.Since(start); waited != 0 {
			t.Errorf("the request waited %v for a circuit that was known to be open", waited)
		}
		if primary.calls != 1 {
			t.Errorf("the primary was asked %d times, want once: the circuit stayed open", primary.calls)
		}
	})
}

func TestRetryRepeats(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"rate limited", types.Newf(types.KindRateLimited, "x", "limited"), true},
		{"unavailable", types.Newf(types.KindProviderUnavailable, "x", "down"), true},
		{"timeout", types.Newf(types.KindTimeout, "x", "slow"), true},
		{"circuit open", types.Newf(types.KindCircuitOpen, "x", "open"), false},
		{"admission rejected", types.Newf(types.KindAdmissionRejected, "x", "full"), false},
		{"budget exceeded", types.Newf(types.KindBudgetExceeded, "x", "spent"), false},
		{"authentication", types.Newf(types.KindAuthentication, "x", "key"), false},
		{"invalid request", types.Newf(types.KindInvalidRequest, "x", "shape"), false},
		{"canceled", types.Newf(types.KindCanceled, "x", "gone"), false},
		{"a 429", llm.NewAPIError("p", "m", 429, ""), true},
		{"a 503", llm.NewAPIError("p", "m", 503, ""), true},
		{"a 400", llm.NewAPIError("p", "m", 400, ""), false},
		{"a 401", llm.NewAPIError("p", "m", 401, ""), false},
		{"an open circuit wrapped", fmt.Errorf("wrapped: %w", types.Newf(types.KindCircuitOpen, "x", "open")), false},
		{"unclassified", errors.New("something"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := retryRepeats(tc.err); got != tc.want {
				t.Errorf("retryRepeats(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// A request given up on while it waited out a failure is both: the caller's
// cancellation is the result, and the failure stays in the chain behind it.
func TestRetryEndedByTheContextWhileWaitingKeepsTheFailureItWaitedOut(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	limited := types.Newf(types.KindRateLimited, "provider", "limited")
	limited.RetryAfter = time.Hour
	provider := completeFunc(func(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
		return llm.CompletionResponse{}, limited
	})
	rp := newTestRetryProvider(provider, retryConfig{maxAttempts: 3, baseDelay: time.Millisecond}, newFakeClock(time.Unix(0, 0)), nil)
	rp.sleep = func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}

	_, err := rp.Complete(ctx, llm.CompletionRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want the cancellation", err)
	}
	if !errors.Is(err, types.ErrRateLimited) {
		t.Errorf("error = %v, want the rate limit it was waiting out in its chain", err)
	}
	if kind := llm.Classify(err); kind != types.KindCanceled {
		t.Errorf("the kind is %v, want the cancellation's: the request was given up on", kind)
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

// The repeats Retry makes are seen by a tracing observer: one note for each
// attempt that failed and was followed by another, with the attempt, the name of
// the kind and the wait, and nothing of the error's text.
func TestRetryTellsTheObserversOfEachRepeat(t *testing.T) {
	collector := &noteCollector{}
	t.Cleanup(telemetry.AddObserver(collector))
	const secret = "the secret text of the provider's error"

	run := func(attempts int, failures []error) []telemetry.Note {
		collector.mu.Lock()
		collector.notes = nil
		collector.mu.Unlock()
		fake := &fakeProvider{}
		for _, err := range failures {
			fake.responses = append(fake.responses, fakeResponse{err: err})
		}
		fake.responses = append(fake.responses, fakeResponse{resp: llm.CompletionResponse{Content: "ok"}})
		fc := newFakeClock(time.Unix(0, 0))
		rp := newTestRetryProvider(fake, retryConfig{maxAttempts: attempts, baseDelay: 7 * time.Millisecond}, fc, nil)
		ctx := telemetry.ObserveOperationStart(context.Background(), telemetry.OperationEvent{Operation: "test"})
		rp.Complete(ctx, llm.CompletionRequest{})
		collector.mu.Lock()
		defer collector.mu.Unlock()
		return append([]telemetry.Note(nil), collector.notes...)
	}
	down := llm.NewAPIError("p", "m", 503, secret)
	limited := types.Newf(types.KindRateLimited, "x", "%s", secret)
	limited.RetryAfter = 3 * time.Second

	cases := []struct {
		name     string
		attempts int
		failures []error
		want     []telemetry.Note
	}{
		{"a first attempt that succeeds says nothing", 3, nil, nil},
		{"one failure then success is one note", 3, []error{down}, []telemetry.Note{{Name: "retry", Attempt: 1, ErrorKind: "provider unavailable", Wait: 7 * time.Millisecond}}},
		{"two failures then success are two notes", 3, []error{down, down}, []telemetry.Note{
			{Name: "retry", Attempt: 1, ErrorKind: "provider unavailable", Wait: 7 * time.Millisecond},
			{Name: "retry", Attempt: 2, ErrorKind: "provider unavailable", Wait: 7 * time.Millisecond}}},
		{"N attempts that all fail are N-1 notes", 3, []error{down, down, down}, []telemetry.Note{
			{Name: "retry", Attempt: 1, ErrorKind: "provider unavailable", Wait: 7 * time.Millisecond},
			{Name: "retry", Attempt: 2, ErrorKind: "provider unavailable", Wait: 7 * time.Millisecond}}},
		{"a stated wait is the note's wait", 2, []error{limited}, []telemetry.Note{{Name: "retry", Attempt: 1, ErrorKind: "rate limited", Wait: 3 * time.Second}}},
		{"a failure that is not repeated says nothing", 3, []error{llm.NewAPIError("p", "m", 400, secret)}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := run(tc.attempts, tc.failures)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("notes = %+v, want %+v", got, tc.want)
			}
			for _, note := range got {
				if strings.Contains(note.ErrorKind, secret) {
					t.Errorf("the note carries the error's text: %+v", note)
				}
			}
		})
	}
}

// With no observer installed a retry costs the call nothing more.
func TestRetryNoteAllocatesNothingWithoutAnObserver(t *testing.T) {
	if telemetry.HasObserver() {
		t.Skip("another test has an observer installed")
	}
	err := llm.NewAPIError("p", "m", 503, "")
	ctx := context.Background()
	if n := testing.AllocsPerRun(100, func() { noteRetry(ctx, 1, err, time.Second) }); n != 0 {
		t.Errorf("noteRetry allocated %v times with no observer, want 0", n)
	}
}
