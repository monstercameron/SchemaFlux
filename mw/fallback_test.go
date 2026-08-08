package mw

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// fallbackFakeProvider is a Handler whose failure/success and call count a
// test controls directly.
type fallbackFakeProvider struct {
	name  string
	resp  llm.CompletionResponse
	err   error
	calls int
}

func (f *fallbackFakeProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	f.calls++
	return f.resp, f.err
}
func (f *fallbackFakeProvider) Name() string                                   { return f.name }
func (f *fallbackFakeProvider) EstimateCost(req llm.CompletionRequest) float64 { return 0 }
func (f *fallbackFakeProvider) RetryPolicy() (int, time.Duration)              { return 0, 0 }

func unavailableErr() error {
	return llm.NewAPIError("primary", "m", 503, "")
}

func TestFallbackReturnsPrimarySuccessWithoutTryingAlternates(t *testing.T) {
	primary := &fallbackFakeProvider{name: "primary", resp: llm.CompletionResponse{Content: "ok"}}
	alt := &fallbackFakeProvider{name: "alt", resp: llm.CompletionResponse{Content: "alt-ok"}}

	wrapped := Fallback([]FallbackRoute{{Handler: alt}})(primary)

	resp, err := wrapped.Complete(context.Background(), llm.CompletionRequest{})
	if err != nil {
		t.Fatalf("Complete returned an error: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("Content = %q, want %q", resp.Content, "ok")
	}
	if alt.calls != 0 {
		t.Fatalf("alt.calls = %d, want 0 -- a successful primary must never try an alternate", alt.calls)
	}
}

func TestFallbackTriesAlternateOnPrimaryFailure(t *testing.T) {
	primary := &fallbackFakeProvider{name: "primary", err: unavailableErr()}
	alt := &fallbackFakeProvider{name: "alt", resp: llm.CompletionResponse{Content: "alt-ok"}}

	wrapped := Fallback([]FallbackRoute{{Handler: alt}})(primary)

	resp, err := wrapped.Complete(context.Background(), llm.CompletionRequest{})
	if err != nil {
		t.Fatalf("Complete returned an error: %v", err)
	}
	if resp.Content != "alt-ok" {
		t.Fatalf("Content = %q, want %q", resp.Content, "alt-ok")
	}
	if alt.calls != 1 {
		t.Fatalf("alt.calls = %d, want 1", alt.calls)
	}
}

func TestFallbackTriesAlternatesInOrder(t *testing.T) {
	primary := &fallbackFakeProvider{name: "primary", err: unavailableErr()}
	first := &fallbackFakeProvider{name: "first", err: unavailableErr()}
	second := &fallbackFakeProvider{name: "second", resp: llm.CompletionResponse{Content: "second-ok"}}

	wrapped := Fallback([]FallbackRoute{{Handler: first}, {Handler: second}})(primary)

	resp, err := wrapped.Complete(context.Background(), llm.CompletionRequest{})
	if err != nil {
		t.Fatalf("Complete returned an error: %v", err)
	}
	if resp.Content != "second-ok" {
		t.Fatalf("Content = %q, want %q", resp.Content, "second-ok")
	}
	if first.calls != 1 || second.calls != 1 {
		t.Fatalf("first.calls=%d second.calls=%d, want 1 and 1", first.calls, second.calls)
	}
}

func TestFallbackReturnsLastFailureWhenEveryRouteFails(t *testing.T) {
	primary := &fallbackFakeProvider{name: "primary", err: unavailableErr()}
	alt := &fallbackFakeProvider{name: "alt", err: llm.NewAPIError("alt", "m", 500, "")}

	wrapped := Fallback([]FallbackRoute{{Handler: alt}})(primary)

	_, err := wrapped.Complete(context.Background(), llm.CompletionRequest{})
	if err == nil {
		t.Fatal("want an error when every route fails, got nil")
	}
	// The alternate's OWN failure is what comes back, not the primary's --
	// "classified on its own terms, not hidden behind the original."
	var apiErr *llm.APIError
	if !errors.As(err, &apiErr) || apiErr.Provider != "alt" {
		t.Fatalf("error does not carry the alternate's own identity: %v", err)
	}
}

// TestFallbackNeverCallsAnAlternateLackingRequiredSchemaSupport is the core
// safety property from the Revised line: a route that cannot deliver the
// requested contract is never substituted in silently. The alternate's
// Complete must not be invoked at all when it is ineligible -- not invoked
// and its answer discarded, never invoked.
func TestFallbackNeverCallsAnAlternateLackingRequiredSchemaSupport(t *testing.T) {
	primary := &fallbackFakeProvider{name: "primary", err: unavailableErr()}
	weakAlt := &fallbackFakeProvider{name: "weak", resp: llm.CompletionResponse{Content: "should not be seen"}}

	wrapped := Fallback(
		[]FallbackRoute{{Handler: weakAlt, Capabilities: FallbackCapabilities{NativeJSONSchema: false}}},
		WithPrimaryCapabilities(FallbackCapabilities{NativeJSONSchema: true}),
	)(primary)

	_, err := wrapped.Complete(context.Background(), llm.CompletionRequest{
		ResponseFormat: "json",
		JSONSchema:     map[string]any{"type": "object"},
	})
	if err == nil {
		t.Fatal("want the primary's error to survive when no alternate qualifies, got nil")
	}
	if weakAlt.calls != 0 {
		t.Fatalf("weakAlt.calls = %d, want 0 -- an alternate lacking native schema support must never be called for a schema request", weakAlt.calls)
	}
}

func TestFallbackUsesAWeakerAlternateWhenSchemaDegradationIsAllowed(t *testing.T) {
	primary := &fallbackFakeProvider{name: "primary", err: unavailableErr()}
	weakAlt := &fallbackFakeProvider{name: "weak", resp: llm.CompletionResponse{Content: "degraded-ok"}}

	wrapped := Fallback(
		[]FallbackRoute{{Handler: weakAlt, Capabilities: FallbackCapabilities{NativeJSONSchema: false}}},
		WithPrimaryCapabilities(FallbackCapabilities{NativeJSONSchema: true}),
		AllowSchemaDegradation(),
	)(primary)

	resp, err := wrapped.Complete(context.Background(), llm.CompletionRequest{
		ResponseFormat: "json",
		JSONSchema:     map[string]any{"type": "object"},
	})
	if err != nil {
		t.Fatalf("Complete returned an error with degradation explicitly allowed: %v", err)
	}
	if resp.Content != "degraded-ok" {
		t.Fatalf("Content = %q, want %q", resp.Content, "degraded-ok")
	}
	if weakAlt.calls != 1 {
		t.Fatalf("weakAlt.calls = %d, want 1", weakAlt.calls)
	}
}

func TestFallbackNeverCallsAnAlternateWithAMismatchedDataPolicy(t *testing.T) {
	primary := &fallbackFakeProvider{name: "primary", err: unavailableErr()}
	publicAlt := &fallbackFakeProvider{name: "public", resp: llm.CompletionResponse{Content: "should not be seen"}}

	wrapped := Fallback(
		[]FallbackRoute{{Handler: publicAlt, DataPolicy: FallbackDataPolicy{Classification: "public"}}},
		WithPrimaryDataPolicy(FallbackDataPolicy{Classification: "private"}),
	)(primary)

	_, err := wrapped.Complete(context.Background(), llm.CompletionRequest{})
	if err == nil {
		t.Fatal("want the primary's error to survive when no policy-compatible alternate exists, got nil")
	}
	if publicAlt.calls != 0 {
		t.Fatalf("publicAlt.calls = %d, want 0 -- a private route must never fail over to a public one", publicAlt.calls)
	}
}

func TestFallbackUsesAnAlternateWithAMatchingDataPolicy(t *testing.T) {
	primary := &fallbackFakeProvider{name: "primary", err: unavailableErr()}
	privateAlt := &fallbackFakeProvider{name: "private-alt", resp: llm.CompletionResponse{Content: "ok"}}

	wrapped := Fallback(
		[]FallbackRoute{{Handler: privateAlt, DataPolicy: FallbackDataPolicy{Classification: "private"}}},
		WithPrimaryDataPolicy(FallbackDataPolicy{Classification: "private"}),
	)(primary)

	resp, err := wrapped.Complete(context.Background(), llm.CompletionRequest{})
	if err != nil {
		t.Fatalf("Complete returned an error: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("Content = %q, want %q", resp.Content, "ok")
	}
}

func TestFallbackWithNoPrimaryPolicyDeclaredEnforcesNothing(t *testing.T) {
	// Documents the honesty boundary: an undeclared requirement is not
	// treated as "anything goes" being SAFE, it is simply not checked.
	primary := &fallbackFakeProvider{name: "primary", err: unavailableErr()}
	anyAlt := &fallbackFakeProvider{name: "any", resp: llm.CompletionResponse{Content: "ok"}}

	wrapped := Fallback([]FallbackRoute{{Handler: anyAlt, DataPolicy: FallbackDataPolicy{Classification: "public"}}})(primary)

	if _, err := wrapped.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
		t.Fatalf("Complete returned an error: %v", err)
	}
	if anyAlt.calls != 1 {
		t.Fatalf("anyAlt.calls = %d, want 1 -- with no primary policy declared, nothing is enforced", anyAlt.calls)
	}
}

func TestFallbackWithFallbackKindsRestrictsWhichFailuresTriggerFallover(t *testing.T) {
	// A malformed-output failure is not in the allowed set, so Fallback must
	// return it directly rather than trying the alternate.
	primary := &fallbackFakeProvider{name: "primary", err: &types.OperationError{Kind: types.KindMalformedOutput}}
	alt := &fallbackFakeProvider{name: "alt", resp: llm.CompletionResponse{Content: "should not be seen"}}

	wrapped := Fallback(
		[]FallbackRoute{{Handler: alt}},
		WithFallbackKinds(types.KindProviderUnavailable, types.KindTimeout),
	)(primary)

	_, err := wrapped.Complete(context.Background(), llm.CompletionRequest{})
	if err == nil {
		t.Fatal("want the primary's malformed-output error, got nil")
	}
	if types.KindOf(err) != types.KindMalformedOutput {
		t.Fatalf("Kind = %v, want KindMalformedOutput", types.KindOf(err))
	}
	if alt.calls != 0 {
		t.Fatalf("alt.calls = %d, want 0 -- malformed output is not a configured fallover trigger", alt.calls)
	}
}

func TestFallbackWithFallbackKindsAllowsAConfiguredKindThrough(t *testing.T) {
	primary := &fallbackFakeProvider{name: "primary", err: &types.OperationError{Kind: types.KindTimeout}}
	alt := &fallbackFakeProvider{name: "alt", resp: llm.CompletionResponse{Content: "ok"}}

	wrapped := Fallback(
		[]FallbackRoute{{Handler: alt}},
		WithFallbackKinds(types.KindTimeout),
	)(primary)

	resp, err := wrapped.Complete(context.Background(), llm.CompletionRequest{})
	if err != nil {
		t.Fatalf("Complete returned an error: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("Content = %q, want %q", resp.Content, "ok")
	}
}

func TestFallbackRespectsContextCancellationBetweenRoutes(t *testing.T) {
	primary := &fallbackFakeProvider{name: "primary", err: unavailableErr()}
	alt := &fallbackFakeProvider{name: "alt", resp: llm.CompletionResponse{Content: "should not be seen"}}

	wrapped := Fallback([]FallbackRoute{{Handler: alt}})(primary)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := wrapped.Complete(ctx, llm.CompletionRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if alt.calls != 0 {
		t.Fatalf("alt.calls = %d, want 0 -- a canceled context must stop the fallover loop", alt.calls)
	}
}

func TestFallbackDelegatesNameCostAndRetryPolicyToThePrimary(t *testing.T) {
	primary := &fakeProvider{name: "primary-fake", maxRetries: 3, backoff: 2 * time.Second}
	alt := &fallbackFakeProvider{name: "alt"}

	wrapped := Fallback([]FallbackRoute{{Handler: alt}})(primary)

	if wrapped.Name() != "primary-fake" {
		t.Fatalf("Name() = %q, want %q", wrapped.Name(), "primary-fake")
	}
	if got := wrapped.EstimateCost(llm.CompletionRequest{}); got != 0.01 {
		t.Fatalf("EstimateCost() = %v, want 0.01", got)
	}
	retries, backoff := wrapped.RetryPolicy()
	if retries != 3 || backoff != 2*time.Second {
		t.Fatalf("RetryPolicy() = (%d, %v), want (3, 2s)", retries, backoff)
	}
}

func TestFallbackWithNoAlternatesBehavesAsPassthrough(t *testing.T) {
	primary := &fallbackFakeProvider{name: "primary", err: unavailableErr()}
	wrapped := Fallback(nil)(primary)

	_, err := wrapped.Complete(context.Background(), llm.CompletionRequest{})
	if err == nil {
		t.Fatal("want the primary's error with no alternates configured, got nil")
	}
}

// TestFallbackThroughChainWithScriptedProvider is the composition case: the
// primary route wrapped by Retry first (so a transient failure is retried
// before ever falling over), then Fallback outermost, built through
// mw.Chain the way a caller actually wires it.
func TestFallbackThroughChainWithScriptedProvider(t *testing.T) {
	primary := &fakeProvider{
		responses: []fakeResponse{
			{err: llm.NewAPIError("primary", "m", 503, "")},
			{err: llm.NewAPIError("primary", "m", 503, "")},
			{err: llm.NewAPIError("primary", "m", 503, "")},
			{err: llm.NewAPIError("primary", "m", 503, "")},
		},
	}
	alt := &fallbackFakeProvider{name: "alt", resp: llm.CompletionResponse{Content: "alt-ok"}}

	fc := newFakeClock(time.Unix(0, 0))
	retrier := Retry(WithMaxAttempts(2), WithBaseDelay(time.Millisecond))
	retried := retrier(primary)
	rp := retried.(*retryProvider)
	rp.sleep = fc.sleep

	wrapped := Chain(retried, Fallback([]FallbackRoute{{Handler: alt}}))

	resp, err := wrapped.Complete(context.Background(), llm.CompletionRequest{})
	if err != nil {
		t.Fatalf("Complete returned an error: %v", err)
	}
	if resp.Content != "alt-ok" {
		t.Fatalf("Content = %q, want %q", resp.Content, "alt-ok")
	}
	// Retry exhausts its 2 attempts against the primary before Fallback ever
	// sees a failure to act on.
	if primary.calls != 2 {
		t.Fatalf("primary.calls = %d, want 2 (Retry's attempts exhausted first)", primary.calls)
	}
	if alt.calls != 1 {
		t.Fatalf("alt.calls = %d, want 1", alt.calls)
	}
}
