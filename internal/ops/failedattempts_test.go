package ops

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// Only a successful call used to publish a record, so a request that exhausted
// its retries and failed outright contributed nothing to the envelope: a
// caller reading Meta.Attempts on a failure saw zero, as though the provider
// had never been called — while it had in fact been called, and billed, as
// many times as the retry budget allowed.
//
// That made "how many attempts did that take" answerable only for the requests
// that worked, which is the opposite of when anybody asks it. Found while
// building PL-008's per-item failure reporting, where a permanently failed
// item reported no attempts at all.

// countingFailProvider fails every call with a retryable error and counts how
// many times it was really asked.
type countingFailProvider struct {
	calls int
	err   error
}

func (p *countingFailProvider) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.calls++
	return llm.CompletionResponse{}, p.err
}

func (p *countingFailProvider) Name() string                               { return "openai" }
func (p *countingFailProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }

// Two retries beyond the first, with no backoff worth waiting for.
func (p *countingFailProvider) RetryPolicy() (int, time.Duration) { return 2, time.Millisecond }

func TestAFailedRequestStillReportsItsAttempts(t *testing.T) {
	provider := &countingFailProvider{
		err: &types.OperationError{Kind: types.KindProviderUnavailable, Message: "down"},
	}

	ctx, records := withCallRecording(context.Background())
	_, err := CallLLM(ctx, provider, "system", "user", types.OpOptions{Intelligence: types.Fast})
	if err == nil {
		t.Fatal("a provider that fails every call reported success")
	}

	meta := envelopeFrom(records.collect(), "extract")

	if provider.calls == 0 {
		t.Fatal("the provider was never called, so this test witnesses nothing")
	}
	if meta.Attempts != provider.calls {
		t.Errorf("Meta.Attempts = %d but the provider was called %d times; a failure that burned "+
			"the whole retry budget must not report as though nothing happened",
			meta.Attempts, provider.calls)
	}
}

// The identity of the failed call is recorded too, so a caller can tell which
// provider and model failed rather than reading an empty envelope.
func TestAFailedRequestRecordsWhichProviderFailed(t *testing.T) {
	provider := &countingFailProvider{err: errors.New("something unclassifiable")}

	ctx, records := withCallRecording(context.Background())
	_, _ = CallLLM(ctx, provider, "system", "user", types.OpOptions{Intelligence: types.Fast})

	meta := envelopeFrom(records.collect(), "extract")
	if meta.Provider != "openai" {
		t.Errorf("Meta.Provider = %q on a failed request", meta.Provider)
	}
	if meta.Elapsed <= 0 {
		t.Error("a failed request reported no elapsed time")
	}
}

// Usage and cost stay zero on a failure rather than being guessed. Most error
// paths report no token consumption at all, and inventing a figure here would
// be the same invented number PR-001 exists to prevent.
func TestAFailedRequestInventsNoUsageOrCost(t *testing.T) {
	provider := &countingFailProvider{
		err: &types.OperationError{Kind: types.KindProviderUnavailable},
	}

	ctx, records := withCallRecording(context.Background())
	_, _ = CallLLM(ctx, provider, "system", "user", types.OpOptions{Intelligence: types.Fast})

	meta := envelopeFrom(records.collect(), "extract")
	if meta.Usage.TotalTokens != 0 {
		t.Errorf("Usage.TotalTokens = %d on a failed request", meta.Usage.TotalTokens)
	}
	if meta.Cost.TotalCost != 0 {
		t.Errorf("Cost.TotalCost = %v on a failed request", meta.Cost.TotalCost)
	}
	// And it must not claim the zero is a measurement.
	if meta.Cost.Quality == types.PricingExact {
		t.Error("a failed request reported its cost as exact")
	}
}

// A terminal failure stops after one call, and reports one attempt — not the
// retry budget it never used.
func TestATerminalFailureReportsTheOneAttemptItMade(t *testing.T) {
	provider := &countingFailProvider{
		err: &types.OperationError{Kind: types.KindInvalidRequest, Message: "malformed"},
	}

	ctx, records := withCallRecording(context.Background())
	_, err := CallLLM(ctx, provider, "system", "user", types.OpOptions{Intelligence: types.Fast})
	if err == nil {
		t.Fatal("a terminal failure reported success")
	}

	meta := envelopeFrom(records.collect(), "extract")
	if provider.calls != 1 {
		t.Fatalf("the provider was called %d times for a terminal failure", provider.calls)
	}
	if meta.Attempts != 1 {
		t.Errorf("Meta.Attempts = %d, want 1", meta.Attempts)
	}
}

// A successful request is unaffected: it still publishes exactly one record
// with its real figures, so the fix did not double-count by publishing on both
// paths.
func TestASuccessfulRequestStillReportsOnce(t *testing.T) {
	provider := &countingFailProvider{err: nil}

	ctx, records := withCallRecording(context.Background())
	_, _ = CallLLM(ctx, provider, "system", "user", types.OpOptions{Intelligence: types.Fast})

	collected := records.collect()
	if len(collected) != 1 {
		t.Errorf("%d records published for one successful call", len(collected))
	}
}
