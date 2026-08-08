package mw

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
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

// TestCircuitBreakerMiddlewareDelegatesNameCostAndRetryPolicy proves the
// wrapper is transparent about everything except Complete, matching every
// other middleware in this package.
func TestCircuitBreakerMiddlewareDelegatesNameCostAndRetryPolicy(t *testing.T) {
	fake := &fakeProvider{name: "delegate-test", maxRetries: 2, backoff: time.Second}
	wrapped := CircuitBreaker(ops.CircuitBreakerConfig{})(fake)

	if wrapped.Name() != "delegate-test" {
		t.Fatalf("Name() = %q, want %q", wrapped.Name(), "delegate-test")
	}
	if got := wrapped.EstimateCost(llm.CompletionRequest{}); got != 0.01 {
		t.Fatalf("EstimateCost() = %v, want 0.01", got)
	}
	retries, backoff := wrapped.RetryPolicy()
	if retries != 2 || backoff != time.Second {
		t.Fatalf("RetryPolicy() = (%d, %v), want (2, 1s)", retries, backoff)
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
