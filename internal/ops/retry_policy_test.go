package ops

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// countingProvider records how many times it was called and always fails with a
// retryable error.
type countingProvider struct {
	calls      int
	maxRetries int
	backoff    time.Duration
}

func (p *countingProvider) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.calls++
	return llm.CompletionResponse{}, fmt.Errorf("rate limit exceeded, try again later")
}
func (p *countingProvider) Name() string                               { return "counting" }
func (p *countingProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *countingProvider) RetryPolicy() (int, time.Duration)          { return p.maxRetries, p.backoff }

// Client.WithRetries(0) is documented as the retry ceiling and explicitly
// clamps negatives to zero, but the dispatcher tested `maxRetries <= 0` and
// substituted the global default of three -- so retries could not be disabled,
// and a caller who asked for none waited out two backoffs anyway.
func TestRetriesCanBeDisabled(t *testing.T) {
	withExplicitModel(t)

	provider := &countingProvider{maxRetries: 0, backoff: time.Millisecond}

	_, err := CallLLM(context.Background(), provider, "system", "user", types.OpOptions{})
	if err == nil {
		t.Fatal("expected the provider error to surface")
	}
	if provider.calls != 1 {
		t.Errorf("provider called %d times, want exactly 1 when retries are disabled", provider.calls)
	}
}

// A configured retry budget is still honoured: attempts are retries + 1.
func TestConfiguredRetryBudgetIsHonoured(t *testing.T) {
	withExplicitModel(t)

	for _, retries := range []int{1, 2, 3, 5} {
		provider := &countingProvider{maxRetries: retries, backoff: time.Millisecond}

		if _, err := CallLLM(context.Background(), provider, "system", "user", types.OpOptions{}); err == nil {
			t.Fatal("expected the provider error to surface")
		}

		want := retries + 1
		if provider.calls != want {
			t.Errorf("retries=%d: provider called %d times, want %d", retries, provider.calls, want)
		}
	}
}

// A negative budget means "not configured" and falls back to the global default.
func TestNegativeRetryBudgetUsesTheGlobalDefault(t *testing.T) {
	withExplicitModel(t)

	provider := &countingProvider{maxRetries: -1, backoff: time.Millisecond}

	if _, err := CallLLM(context.Background(), provider, "system", "user", types.OpOptions{}); err == nil {
		t.Fatal("expected the provider error to surface")
	}
	if provider.calls < 2 {
		t.Errorf("provider called %d times; a negative budget should fall back to the default", provider.calls)
	}
}

// Retries must stop the moment the caller cancels, rather than sleeping out the
// remaining backoff.
func TestRetriesStopOnCancellation(t *testing.T) {
	withExplicitModel(t)

	provider := &countingProvider{maxRetries: 5, backoff: time.Hour}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	if _, err := CallLLM(ctx, provider, "system", "user", types.OpOptions{}); err == nil {
		t.Fatal("expected an error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("cancellation took %v; retries must abort promptly", elapsed)
	}
}

// A deterministic failure is not retried at all, whatever the budget.
func TestDeterministicFailuresAreNotRetried(t *testing.T) {
	withExplicitModel(t)

	provider := &deterministicFailureProvider{maxRetries: 5}

	if _, err := CallLLM(context.Background(), provider, "system", "user", types.OpOptions{}); err == nil {
		t.Fatal("expected an error")
	}
	if provider.calls != 1 {
		t.Errorf("provider called %d times, want 1: a response with no message output is identical on retry", provider.calls)
	}
}

type deterministicFailureProvider struct {
	calls      int
	maxRetries int
}

func (p *deterministicFailureProvider) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.calls++
	return llm.CompletionResponse{}, fmt.Errorf("openai: %w", llm.ErrNoMessageOutput)
}
func (p *deterministicFailureProvider) Name() string                               { return "deterministic" }
func (p *deterministicFailureProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *deterministicFailureProvider) RetryPolicy() (int, time.Duration) {
	return p.maxRetries, time.Millisecond
}

// Non-retryable API errors stop immediately too.
//
// A-008: these used to be plain fmt.Errorf strings ("status 400 bad
// request") that only stopped the retry loop because llm_helper.go carried a
// substring list matching "status 400" -- exactly the second opinion A-008
// removes. Built typed instead, via llm.NewAPIError, so the assertion is
// about the classifier reading the STATUS, which is what actually decides
// this now; a plain string carrying the same words no longer would.
func TestNonRetryableErrorsStopImmediately(t *testing.T) {
	withExplicitModel(t)

	for _, tc := range []struct {
		name   string
		status int
	}{
		{"unauthorized", 401},
		{"forbidden", 403},
		{"bad_request", 400},
		{"not_found", 404},
	} {
		provider := &statusErrorProvider{maxRetries: 5, status: tc.status}

		if _, err := CallLLM(context.Background(), provider, "system", "user", types.OpOptions{}); err == nil {
			t.Fatal("expected an error")
		}
		if provider.calls != 1 {
			t.Errorf("%s (status %d): provider called %d times, want 1", tc.name, tc.status, provider.calls)
		}
	}
}

type statusErrorProvider struct {
	calls      int
	maxRetries int
	status     int
}

func (p *statusErrorProvider) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.calls++
	return llm.CompletionResponse{}, llm.NewAPIError("message", "m", p.status, "")
}
func (p *statusErrorProvider) Name() string                               { return "message" }
func (p *statusErrorProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *statusErrorProvider) RetryPolicy() (int, time.Duration) {
	return p.maxRetries, time.Millisecond
}
