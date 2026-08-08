package ops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// scriptedRetryProvider fails its first failures calls with err, then answers
// with body. It records how many times it was called.
type scriptedRetryProvider struct {
	calls      int
	failures   int
	err        error
	body       string
	maxRetries int
	backoff    time.Duration
}

func (p *scriptedRetryProvider) Complete(ctx context.Context, _ llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.calls++
	if p.calls <= p.failures {
		return llm.CompletionResponse{}, p.err
	}
	return llm.CompletionResponse{Content: p.body, FinishReason: "stop"}, nil
}
func (p *scriptedRetryProvider) Name() string                               { return "scripted-retry" }
func (p *scriptedRetryProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *scriptedRetryProvider) RetryPolicy() (int, time.Duration)          { return p.maxRetries, p.backoff }

// A provider with a name the library does not know has no default model, which
// is the point of I-03's fix. A real caller in that position sets the model
// explicitly; so does the test.
func withExplicitModel(t *testing.T) {
	t.Helper()
	t.Setenv("SCHEMAFLUX_MODEL", "test-model")
}

// The retry budget is the caller's to set, and each setting has to be honoured
// exactly: a budget of N means at most N+1 attempts, and zero means one.
func TestRetryBudgetIsHonouredExactly(t *testing.T) {
	withExplicitModel(t)

	cases := []struct {
		name        string
		budget      int
		failures    int
		wantCalls   int
		wantSuccess bool
	}{
		{"zero_budget_succeeds_first_try", 0, 0, 1, true},
		{"zero_budget_does_not_retry", 0, 5, 1, false},
		{"one_retry_recovers", 1, 1, 2, true},
		{"one_retry_exhausted", 1, 5, 2, false},
		{"two_retries_recover", 2, 2, 3, true},
		{"two_retries_exhausted", 2, 5, 3, false},
		{"three_retries_recover", 3, 3, 4, true},
		{"budget_unused_when_first_attempt_works", 3, 0, 1, true},
		{"five_retries_exhausted", 5, 99, 6, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &scriptedRetryProvider{
				failures:   tc.failures,
				err:        errors.New("rate limit exceeded, try again later"),
				body:       "recovered",
				maxRetries: tc.budget,
				backoff:    time.Millisecond,
			}

			_, err := CallLLM(context.Background(), provider, "system", "user", types.OpOptions{})

			if provider.calls != tc.wantCalls {
				t.Errorf("calls = %d, want %d (budget %d)", provider.calls, tc.wantCalls, tc.budget)
			}
			if succeeded := err == nil; succeeded != tc.wantSuccess {
				t.Errorf("succeeded = %v, want %v (err: %v)", succeeded, tc.wantSuccess, err)
			}
		})
	}
}

// Retrying a failure that cannot change is waste: it spends the caller's
// latency budget to receive the same answer.
func TestDeterministicFailuresAreNotRetriedAcrossKinds(t *testing.T) {
	withExplicitModel(t)

	cases := []struct {
		name      string
		err       error
		wantCalls int
	}{
		{"no_message_output", llm.ErrNoMessageOutput, 1},
		{"context_cancelled", context.Canceled, 1},
		{"wrapped_no_message_output", errors.Join(errors.New("call failed"), llm.ErrNoMessageOutput), 1},
		{"rate_limit_is_retried", errors.New("rate limit exceeded"), 4},
		{"connection_reset_is_retried", errors.New("connection reset by peer"), 4},
		{"timeout_is_retried", errors.New("request timeout"), 4},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &scriptedRetryProvider{
				failures:   99,
				err:        tc.err,
				maxRetries: 3,
				backoff:    time.Millisecond,
			}

			if _, err := CallLLM(context.Background(), provider, "system", "user", types.OpOptions{}); err == nil {
				t.Fatal("an always-failing provider must produce an error")
			}
			if provider.calls != tc.wantCalls {
				t.Errorf("calls = %d, want %d for %v", provider.calls, tc.wantCalls, tc.err)
			}
		})
	}
}

// A cancelled context stops the loop rather than spending the whole budget.
func TestRetryLoopStopsOnCancellation(t *testing.T) {
	withExplicitModel(t)

	ctx, cancel := context.WithCancel(context.Background())

	provider := &cancellingProvider{cancel: cancel, maxRetries: 5, backoff: 50 * time.Millisecond}

	start := time.Now()
	if _, err := CallLLM(ctx, provider, "system", "user", types.OpOptions{}); err == nil {
		t.Fatal("a cancelled retry loop must report an error")
	}
	if provider.calls > 2 {
		t.Errorf("calls = %d; the loop kept retrying after cancellation", provider.calls)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("cancellation took %v; the backoff is not cancellable", elapsed)
	}
}

type cancellingProvider struct {
	calls      int
	cancel     context.CancelFunc
	maxRetries int
	backoff    time.Duration
}

func (p *cancellingProvider) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.calls++
	if p.calls == 1 {
		p.cancel()
	}
	return llm.CompletionResponse{}, errors.New("rate limit exceeded")
}
func (p *cancellingProvider) Name() string                               { return "cancelling" }
func (p *cancellingProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *cancellingProvider) RetryPolicy() (int, time.Duration)          { return p.maxRetries, p.backoff }

// The error the caller sees must name the last failure, not just the fact that
// retries ran out.
func TestExhaustedRetriesReportTheUnderlyingFailure(t *testing.T) {
	withExplicitModel(t)

	provider := &scriptedRetryProvider{
		failures:   99,
		err:        errors.New("upstream said 503"),
		maxRetries: 1,
		backoff:    time.Millisecond,
	}

	_, err := CallLLM(context.Background(), provider, "system", "user", types.OpOptions{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("the error should carry the underlying failure, got %v", err)
	}
}

// A success on the final attempt is still a success.
func TestSuccessOnTheLastAttempt(t *testing.T) {
	withExplicitModel(t)

	provider := &scriptedRetryProvider{
		failures:   3,
		err:        errors.New("rate limit exceeded"),
		body:       "recovered",
		maxRetries: 3,
		backoff:    time.Millisecond,
	}

	body, err := CallLLM(context.Background(), provider, "system", "user", types.OpOptions{})
	if err != nil {
		t.Fatalf("the fourth attempt should have succeeded: %v", err)
	}
	if body != "recovered" {
		t.Errorf("body = %q", body)
	}
	if provider.calls != 4 {
		t.Errorf("calls = %d, want 4", provider.calls)
	}
}

// Backoff has to grow (or hold at its cap), or a retry storm hits the
// provider as fast as the first attempt did. nextRetryDelay's jitter makes a
// single call's delay non-deterministic, so this chains prevWait the way
// CallLLM's loop does and pins the growth at jitter's own ceiling (randFloat
// returning ~1 every time), which is deterministic and is also the case that
// would reveal a regression to "delay shrinks after growing" fastest.
func TestRetryDelayGrows(t *testing.T) {
	base := 100 * time.Millisecond
	maxDelay := 5 * time.Second

	prevWait := base
	previous := time.Duration(0)
	for attempt := 0; attempt < 5; attempt++ {
		delay := nextRetryDelay(nil, base, prevWait, maxDelay, oneRand)
		if delay <= 0 {
			t.Fatalf("attempt %d produced a non-positive delay %v", attempt, delay)
		}
		if delay < previous {
			t.Errorf("attempt %d delay %v is shorter than the previous %v", attempt, delay, previous)
		}
		previous = delay
		prevWait = delay
	}
}
