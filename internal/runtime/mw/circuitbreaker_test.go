package mw

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

type cbClock struct{ now time.Time }

func (c *cbClock) Now() time.Time { return c.now }

// TestCircuitBreakerMiddlewareOpensAfterFailuresAndRefusesWithoutCalling
// proves the middleware refuses further Complete calls once its threshold
// trips, and that the refusal never reaches the wrapped provider.
func TestCircuitBreakerMiddlewareOpensAfterFailuresAndRefusesWithoutCalling(t *testing.T) {
	clk := &cbClock{now: time.Unix(0, 0)}
	fake := &fakeProvider{
		name: "p",
		responses: []fakeResponse{
			{err: errors.New("boom")},
		},
	}
	wrapped := CircuitBreaker(ops.CircuitBreakerConfig{FailureThreshold: 2, Now: clk.Now})(fake)

	for i := 0; i < 2; i++ {
		if _, err := wrapped.Complete(context.Background(), llm.CompletionRequest{}); err == nil {
			t.Fatalf("call %d: expected the provider's own failure", i)
		}
	}
	if fake.calls != 2 {
		t.Fatalf("provider calls = %d, want 2", fake.calls)
	}

	_, err := wrapped.Complete(context.Background(), llm.CompletionRequest{})
	if err == nil {
		t.Fatal("expected the breaker to refuse the 3rd call")
	}
	var opErr *types.OperationError
	if !errors.As(err, &opErr) || opErr.Kind != types.KindCircuitOpen {
		t.Fatalf("expected KindCircuitOpen, got %v", err)
	}
	if fake.calls != 2 {
		t.Fatalf("provider calls after the breaker opened = %d, want still 2 -- the refusal must not reach the provider", fake.calls)
	}
}

// TestCircuitBreakerMiddlewareKeysByProviderName proves two different
// provider names never share a breaker state under the same middleware
// instance's default (non-model) keying.
func TestCircuitBreakerMiddlewareKeysByProviderName(t *testing.T) {
	clk := &cbClock{now: time.Unix(0, 0)}
	mwFn := CircuitBreaker(ops.CircuitBreakerConfig{FailureThreshold: 1, Now: clk.Now})

	failing := &fakeProvider{name: "a", responses: []fakeResponse{{err: errors.New("boom")}}}
	wrappedA := mwFn(failing)
	if _, err := wrappedA.Complete(context.Background(), llm.CompletionRequest{}); err == nil {
		t.Fatal("expected provider a's own failure")
	}
	if _, err := wrappedA.Complete(context.Background(), llm.CompletionRequest{}); err == nil {
		t.Fatal("expected provider a's breaker to be open now")
	}

	healthy := &fakeProvider{name: "b", responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "ok"}}}}
	wrappedB := mwFn(healthy)
	if _, err := wrappedB.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
		t.Fatalf("provider b should be unaffected by provider a's open breaker: %v", err)
	}
}

// TestCircuitBreakerMiddlewareKeyByModelSeparatesModels proves KeyByModel
// makes two models on the same provider trip independently.
func TestCircuitBreakerMiddlewareKeyByModelSeparatesModels(t *testing.T) {
	clk := &cbClock{now: time.Unix(0, 0)}
	fake := &fakeProvider{
		name: "p",
		responses: []fakeResponse{
			{err: errors.New("boom")},
			{resp: llm.CompletionResponse{Content: "ok"}},
		},
	}
	wrapped := CircuitBreaker(ops.CircuitBreakerConfig{FailureThreshold: 1, Now: clk.Now}, KeyByModel())(fake)

	if _, err := wrapped.Complete(context.Background(), llm.CompletionRequest{Model: "gpt-a"}); err == nil {
		t.Fatal("expected model gpt-a's own failure")
	}
	// gpt-a's breaker is now open; a different model must still be allowed
	// through to the (still-healthy, per this call's scripted response)
	// provider.
	if _, err := wrapped.Complete(context.Background(), llm.CompletionRequest{Model: "gpt-b"}); err != nil {
		t.Fatalf("model gpt-b should be unaffected by model gpt-a's open breaker: %v", err)
	}
}

// TestCircuitBreakerMiddlewareRecoversAfterOpenDuration proves the breaker
// allows a probe through, and success closes it, purely from the injected
// clock -- no sleep.
func TestCircuitBreakerMiddlewareRecoversAfterOpenDuration(t *testing.T) {
	clk := &cbClock{now: time.Unix(0, 0)}
	fake := &fakeProvider{
		name: "p",
		responses: []fakeResponse{
			{err: errors.New("boom")},
			{resp: llm.CompletionResponse{Content: "recovered"}},
		},
	}
	wrapped := CircuitBreaker(ops.CircuitBreakerConfig{FailureThreshold: 1, OpenDuration: time.Minute, Now: clk.Now})(fake)

	if _, err := wrapped.Complete(context.Background(), llm.CompletionRequest{}); err == nil {
		t.Fatal("expected the first call to fail")
	}
	if _, err := wrapped.Complete(context.Background(), llm.CompletionRequest{}); err == nil {
		t.Fatal("expected the breaker to be open immediately after tripping")
	}

	clk.now = clk.now.Add(2 * time.Minute)
	resp, err := wrapped.Complete(context.Background(), llm.CompletionRequest{})
	if err != nil {
		t.Fatalf("expected the post-recovery probe to succeed: %v", err)
	}
	if resp.Content != "recovered" {
		t.Fatalf("Content = %q, want %q", resp.Content, "recovered")
	}
}

// TestCircuitBreakerComposesWithRetryThroughChain proves a KindCircuitOpen
// refusal is retryable through mw.Retry exactly like a provider's own
// failure -- the same composition guarantee mw.RateLimit documents for its
// own Reject-mode refusal.
func TestCircuitBreakerComposesWithRetryThroughChain(t *testing.T) {
	clk := &cbClock{now: time.Unix(0, 0)}
	fake := &fakeProvider{
		name: "p",
		responses: []fakeResponse{
			// A classified, retryable failure -- a plain error would never
			// reach a second attempt at all, which would make this test
			// pass without ever exercising the breaker's refusal.
			{err: llm.NewAPIError("fake", "m", 503, "")},
			{resp: llm.CompletionResponse{Content: "should not be reached before recovery"}},
		},
	}

	breaker := CircuitBreaker(ops.CircuitBreakerConfig{FailureThreshold: 1, OpenDuration: time.Minute, Now: clk.Now})
	retrier := Retry(WithMaxAttempts(2), WithBaseDelay(time.Millisecond))

	wrappedBreaker := breaker(fake)
	retried := retrier(wrappedBreaker)
	rp := retried.(*retryProvider)
	fc := newFakeClock(clk.now)
	rp.sleep = fc.sleep
	rp.randFloat = func() float64 { return 0 }

	// First Complete: provider fails, breaker opens. Retry retries once more
	// inside the SAME Complete call, immediately hitting the now-open
	// breaker (the clock has not advanced), and returns the classified
	// circuit-open error rather than hanging or silently succeeding.
	_, err := retried.Complete(context.Background(), llm.CompletionRequest{})
	if err == nil {
		t.Fatal("expected a classified error: the provider failed once and the breaker was open for the retry")
	}
	var opErr *types.OperationError
	if !errors.As(err, &opErr) {
		t.Fatalf("expected *types.OperationError, got %T", err)
	}
	if opErr.Kind != types.KindCircuitOpen {
		t.Fatalf("kind = %v, want KindCircuitOpen (the retry's second attempt should have hit the now-open breaker)", opErr.Kind)
	}
}

// cbStatus is a provider failure carrying an HTTP status.
func cbStatus(status int) error { return llm.NewAPIError("p", "m", status, "") }

func TestCircuitOutcomeOf(t *testing.T) {
	live := context.Background()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancelExpired()

	cases := []struct {
		name string
		ctx  context.Context
		err  error
		want circuitOutcome
	}{
		{"no error is a success", live, nil, circuitSuccess},
		{"a cancellation with the caller's context over", cancelled, context.Canceled, circuitNoVerdict},
		{"a deadline the caller set", expired, context.DeadlineExceeded, circuitNoVerdict},
		{"a classified timeout with the caller's context over", cancelled, types.Newf(types.KindTimeout, "x", "t"), circuitNoVerdict},
		{"a cancellation while the caller's context is live", live, context.Canceled, circuitNoVerdict},
		{"a deadline error while the caller's context is live is the provider's timeout", live, context.DeadlineExceeded, circuitFailure},
		{"400", live, cbStatus(400), circuitNoVerdict},
		{"401", live, cbStatus(401), circuitNoVerdict},
		{"403", live, cbStatus(403), circuitNoVerdict},
		{"404", live, cbStatus(404), circuitNoVerdict},
		{"422", live, cbStatus(422), circuitNoVerdict},
		{"413", live, cbStatus(413), circuitNoVerdict},
		{"429", live, cbStatus(429), circuitFailure},
		{"500", live, cbStatus(500), circuitFailure},
		{"502", live, cbStatus(502), circuitFailure},
		{"503", live, cbStatus(503), circuitFailure},
		{"504", live, cbStatus(504), circuitFailure},
		{"a policy refusal", live, types.Newf(types.KindPolicyViolation, "x", "p"), circuitNoVerdict},
		{"a budget refusal", live, types.Newf(types.KindBudgetExceeded, "x", "b"), circuitNoVerdict},
		{"a refusal by an inner breaker", live, types.Newf(types.KindCircuitOpen, "x", "o"), circuitNoVerdict},
		{"a configuration error", live, types.Newf(types.KindConfiguration, "x", "c"), circuitNoVerdict},
		{"a capability the provider does not have", live, types.Newf(types.KindUnsupportedCapability, "x", "u"), circuitNoVerdict},
		{"a refusal at admission", live, types.Newf(types.KindAdmissionRejected, "x", "a"), circuitNoVerdict},
		{"a shutdown", live, types.Newf(types.KindShutdown, "x", "s"), circuitNoVerdict},
		{"a network timeout", live, &net.DNSError{IsTimeout: true}, circuitFailure},
		{"a network failure", live, &net.OpError{Op: "dial", Err: errors.New("refused")}, circuitFailure},
		{"an error nobody classified", live, errors.New("boom"), circuitFailure},
		{"an answer cut off", live, types.Newf(types.KindOutputTruncated, "x", "t"), circuitNoVerdict},
		{"a malformed answer", live, types.Newf(types.KindMalformedOutput, "x", "m"), circuitNoVerdict},
		{"an answer of the wrong shape", live, types.Newf(types.KindSchemaViolation, "x", "s"), circuitNoVerdict},
		{"an answer that fails its checks", live, types.Newf(types.KindInvariantViolation, "x", "i"), circuitNoVerdict},
		{"an answer with a claim nothing supports", live, types.Newf(types.KindEvidenceViolation, "x", "e"), circuitNoVerdict},
		{"repairs that ran out", live, types.Newf(types.KindRepairExhausted, "x", "r"), circuitNoVerdict},
		{"an answer held for review", live, types.Newf(types.KindReviewRequired, "x", "r"), circuitNoVerdict},
		{"a request that panicked below", live, &types.OperationError{Kind: types.KindInvariantViolation, Op: "hedge", Message: "a request panicked", Narrow: types.NarrowPanic}, circuitFailure},
		{"a panic wrapped", live, fmt.Errorf("w: %w", &types.OperationError{Kind: types.KindInvariantViolation, Op: "hedge", Message: "p", Narrow: types.NarrowPanic}), circuitFailure},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := circuitOutcomeOf(tc.ctx, tc.err); got != tc.want {
				t.Fatalf("circuitOutcomeOf = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestCircuitBreakerIgnoresFailuresThatAreNotTheProviders(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	cases := []struct {
		name string
		ctx  context.Context
		err  error
	}{
		{"cancelled by the caller", cancelled, context.Canceled},
		{"400", context.Background(), cbStatus(400)},
		{"401", context.Background(), cbStatus(401)},
		{"403", context.Background(), cbStatus(403)},
		{"404", context.Background(), cbStatus(404)},
		{"422", context.Background(), cbStatus(422)},
		{"policy", context.Background(), types.Newf(types.KindPolicyViolation, "x", "p")},
		{"budget", context.Background(), types.Newf(types.KindBudgetExceeded, "x", "b")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clk := &cbClock{now: time.Unix(0, 0)}
			fake := &fakeProvider{name: "p", responses: []fakeResponse{{err: tc.err}}}
			wrapped := CircuitBreaker(ops.CircuitBreakerConfig{FailureThreshold: 2, Now: clk.Now})(fake)
			for i := 0; i < 6; i++ {
				_, err := wrapped.Complete(tc.ctx, llm.CompletionRequest{})
				if err == nil || types.KindOf(err) == types.KindCircuitOpen {
					t.Fatalf("call %d: err = %v, want the provider's own failure and never a circuit refusal", i, err)
				}
			}
			if fake.calls != 6 {
				t.Fatalf("provider calls = %d, want 6", fake.calls)
			}
		})
	}
}

func TestCircuitBreakerCountsFailuresThatAreTheProviders(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"429", cbStatus(429)},
		{"500", cbStatus(500)},
		{"503", cbStatus(503)},
		{"a provider timeout", types.Newf(types.KindTimeout, "x", "t")},
		{"a network error", &net.OpError{Op: "dial", Err: errors.New("refused")}},
		{"unclassified", errors.New("boom")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clk := &cbClock{now: time.Unix(0, 0)}
			fake := &fakeProvider{name: "p", responses: []fakeResponse{{err: tc.err}}}
			wrapped := CircuitBreaker(ops.CircuitBreakerConfig{FailureThreshold: 2, Now: clk.Now})(fake)
			for i := 0; i < 2; i++ {
				wrapped.Complete(context.Background(), llm.CompletionRequest{})
			}
			_, err := wrapped.Complete(context.Background(), llm.CompletionRequest{})
			if types.KindOf(err) != types.KindCircuitOpen {
				t.Fatalf("after two failures err = %v, want a circuit refusal", err)
			}
		})
	}
}

// A cancelled call neither counts toward the threshold nor clears the count a
// provider failure started.
func TestCircuitBreakerCancellationNeitherCountsNorClearsFailures(t *testing.T) {
	clk := &cbClock{now: time.Unix(0, 0)}
	script := []error{cbStatus(503), context.Canceled, cbStatus(503)}
	calls := 0
	provider := completeFunc(func(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
		err := script[calls%len(script)]
		calls++
		return llm.CompletionResponse{}, err
	})
	wrapped := CircuitBreaker(ops.CircuitBreakerConfig{FailureThreshold: 2, Now: clk.Now})(provider)
	for i := 0; i < 3; i++ {
		wrapped.Complete(context.Background(), llm.CompletionRequest{})
	}
	if _, err := wrapped.Complete(context.Background(), llm.CompletionRequest{}); types.KindOf(err) != types.KindCircuitOpen {
		t.Fatalf("err = %v, want the two provider failures either side of a cancellation to have opened the breaker", err)
	}
}

// A probe the caller cancelled holds no verdict and must still give its slot
// back: before, nothing was reported, and every later call was refused as
// "already probing".
func TestCircuitBreakerCancelledProbeReleasesItsSlot(t *testing.T) {
	clk := &cbClock{now: time.Unix(0, 0)}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	outcomes := []error{cbStatus(503), context.Canceled, nil}
	calls := 0
	provider := completeFunc(func(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
		err := outcomes[calls]
		calls++
		return llm.CompletionResponse{}, err
	})
	wrapped := CircuitBreaker(ops.CircuitBreakerConfig{FailureThreshold: 1, SuccessThreshold: 2, OpenDuration: time.Minute, Now: clk.Now})(provider)

	wrapped.Complete(context.Background(), llm.CompletionRequest{})
	clk.now = clk.now.Add(2 * time.Minute)
	if _, err := wrapped.Complete(cancelled, llm.CompletionRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("probe: err = %v, want the cancellation", err)
	}
	if _, err := wrapped.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
		t.Fatalf("the call after a cancelled probe was refused: %v", err)
	}
	if calls != 3 {
		t.Fatalf("provider calls = %d, want 3", calls)
	}
}

func TestCircuitBreakerPanicIsAFailureAndStillPanics(t *testing.T) {
	clk := &cbClock{now: time.Unix(0, 0)}
	provider := completeFunc(func(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
		panic("provider bug")
	})
	wrapped := CircuitBreaker(ops.CircuitBreakerConfig{FailureThreshold: 1, Now: clk.Now})(provider)

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the panic was swallowed")
			}
		}()
		wrapped.Complete(context.Background(), llm.CompletionRequest{})
	}()
	if _, err := wrapped.Complete(context.Background(), llm.CompletionRequest{}); types.KindOf(err) != types.KindCircuitOpen {
		t.Fatalf("err = %v, want the panic counted as a failure that opened the breaker", err)
	}
}

// A panic while half-open must give the probe slot back: it was kept for good,
// so no later call was ever admitted.
func TestCircuitBreakerPanickingProbeReleasesItsSlot(t *testing.T) {
	clk := &cbClock{now: time.Unix(0, 0)}
	step := 0
	provider := completeFunc(func(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
		step++
		switch step {
		case 1:
			return llm.CompletionResponse{}, cbStatus(503)
		case 2:
			panic("provider bug")
		}
		return llm.CompletionResponse{Content: "ok"}, nil
	})
	wrapped := CircuitBreaker(ops.CircuitBreakerConfig{FailureThreshold: 1, OpenDuration: time.Minute, Now: clk.Now})(provider)

	wrapped.Complete(context.Background(), llm.CompletionRequest{})
	clk.now = clk.now.Add(2 * time.Minute)
	func() {
		defer func() { recover() }()
		wrapped.Complete(context.Background(), llm.CompletionRequest{})
	}()
	clk.now = clk.now.Add(2 * time.Minute)
	if _, err := wrapped.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
		t.Fatalf("a probe after the panicking one was refused: %v", err)
	}
}

func TestCircuitBreakerBoundsTheKeysKeyByModelCreates(t *testing.T) {
	clk := &cbClock{now: time.Unix(0, 0)}
	provider := completeFunc(func(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
		if req.Model == "bad" {
			return llm.CompletionResponse{}, cbStatus(503)
		}
		return llm.CompletionResponse{Content: "ok"}, nil
	})
	wrapped := CircuitBreaker(ops.CircuitBreakerConfig{FailureThreshold: 1, Now: clk.Now}, KeyByModel())(provider)
	for i := 0; i < circuitBreakerMaxModelKeys+50; i++ {
		wrapped.Complete(context.Background(), llm.CompletionRequest{Model: fmt.Sprintf("m%d", i)})
	}
	if got := len(wrapped.(*circuitBreakerProvider).models.seen); got != circuitBreakerMaxModelKeys {
		t.Fatalf("model keys = %d, want %d", got, circuitBreakerMaxModelKeys)
	}
	// A model past the bound is tracked under its provider's key, which it opens
	// for every other model that was not given a key of its own.
	wrapped.Complete(context.Background(), llm.CompletionRequest{Model: "bad"})
	if _, err := wrapped.Complete(context.Background(), llm.CompletionRequest{Model: "another-late-model"}); types.KindOf(err) != types.KindCircuitOpen {
		t.Fatalf("err = %v, want the late models to share the provider's key", err)
	}
	if _, err := wrapped.Complete(context.Background(), llm.CompletionRequest{Model: "m0"}); err != nil {
		t.Fatalf("a model with a key of its own was refused: %v", err)
	}
}
