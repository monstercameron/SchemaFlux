package mw

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
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

// With no kinds configured a failure that stops everything does not send the
// same request to another route; a failure of the route does.
func TestFallbackWithNoKindsConfiguredStopsOnATerminalFailure(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		fallover bool
	}{
		{"a refused budget", types.Newf(types.KindBudgetExceeded, "mw.Budget", "over"), false},
		{"a refused data policy", types.Newf(types.KindPolicyViolation, "x", "no"), false},
		{"a shutdown", types.Newf(types.KindShutdown, "x", "closing"), false},
		{"a cancellation", types.Newf(types.KindCanceled, "x", "stop"), false},
		{"a configuration error", types.Newf(types.KindConfiguration, "x", "missing"), false},
		{"an authentication error", types.Newf(types.KindAuthentication, "x", "key"), false},
		{"a permission error", types.Newf(types.KindPermission, "x", "no"), false},
		{"a request the provider rejected", types.Newf(types.KindInvalidRequest, "x", "bad"), false},
		{"a 401", llm.NewAPIError("p", "m", 401, ""), false},
		{"a 400", llm.NewAPIError("p", "m", 400, ""), false},
		{"a 503", llm.NewAPIError("p", "m", 503, ""), true},
		{"a 500", llm.NewAPIError("p", "m", 500, ""), true},
		{"a 429", llm.NewAPIError("p", "m", 429, ""), true},
		{"a provider timeout", types.Newf(types.KindTimeout, "x", "slow"), true},
		{"an open circuit", types.Newf(types.KindCircuitOpen, "x", "open"), true},
		{"an error nobody classified", errors.New("boom"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			primary := &fallbackFakeProvider{name: "primary", err: tc.err}
			alt := &fallbackFakeProvider{name: "alt", resp: llm.CompletionResponse{Content: "from the alternate"}}
			wrapped := Fallback([]FallbackRoute{{Handler: alt}})(primary)

			resp, err := wrapped.Complete(context.Background(), llm.CompletionRequest{})
			if tc.fallover {
				if err != nil || resp.Content != "from the alternate" || alt.calls != 1 {
					t.Fatalf("resp %q, err %v, alternate calls %d; want the alternate's answer", resp.Content, err, alt.calls)
				}
				return
			}
			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want the primary's own failure", err)
			}
			if alt.calls != 0 {
				t.Fatalf("the alternate was called %d times after a failure that stops everything", alt.calls)
			}
		})
	}
}

// A kind named in WithFallbackKinds triggers a fallover even when it is
// terminal: naming it is the opt-in.
func TestFallbackWithKindsConfiguredHonoursTheCallersChoice(t *testing.T) {
	primary := &fallbackFakeProvider{name: "primary", err: types.Newf(types.KindAuthentication, "x", "key")}
	alt := &fallbackFakeProvider{name: "alt", resp: llm.CompletionResponse{Content: "ok"}}
	wrapped := Fallback([]FallbackRoute{{Handler: alt}}, WithFallbackKinds(types.KindAuthentication))(primary)

	if resp, err := wrapped.Complete(context.Background(), llm.CompletionRequest{}); err != nil || resp.Content != "ok" {
		t.Fatalf("resp %q, err %v; want the alternate's answer for a kind the caller listed", resp.Content, err)
	}
}

func TestFallbackTriggers(t *testing.T) {
	panicked := &types.OperationError{Kind: types.KindInvariantViolation, Op: "hedge", Message: "a request panicked", Narrow: types.NarrowPanic}
	cases := []struct {
		name  string
		kinds []types.ErrorKind
		err   error
		want  bool
	}{
		{"nil never triggers", nil, nil, false},
		{"nil never triggers with kinds", []types.ErrorKind{types.KindTimeout}, nil, false},
		{"an unclassified error triggers by default", nil, errors.New("x"), true},
		{"a 503 triggers by default", nil, llm.NewAPIError("p", "m", 503, ""), true},
		{"a rate limit triggers by default", nil, types.Newf(types.KindRateLimited, "x", "y"), true},
		{"a budget refusal does not trigger by default", nil, types.Newf(types.KindBudgetExceeded, "x", "y"), false},
		{"a policy refusal does not trigger by default", nil, types.Newf(types.KindPolicyViolation, "x", "y"), false},
		{"a cancellation does not trigger by default", nil, context.Canceled, false},
		{"a shutdown does not trigger by default", nil, types.Newf(types.KindShutdown, "x", "y"), false},
		{"a full bulkhead does not trigger by default", nil, types.Newf(types.KindAdmissionRejected, "x", "y"), false},
		{"a panic does not trigger by default", nil, panicked, false},
		{"a panic wrapped does not trigger by default", nil, fmt.Errorf("wrapped: %w", panicked), false},
		{"a wrong answer of the same kind as a panic does", nil, types.Newf(types.KindInvariantViolation, "x", "y"), true},
		{"a malformed answer triggers by default", nil, types.Newf(types.KindMalformedOutput, "x", "y"), true},
		{"an unavailable provider triggers by default", nil, types.Newf(types.KindProviderUnavailable, "x", "y"), true},
		{"an unsupported capability triggers by default", nil, types.Newf(types.KindUnsupportedCapability, "x", "y"), true},
		{"a rejected key does not trigger by default", nil, types.Newf(types.KindAuthentication, "x", "y"), false},
		{"a full bulkhead triggers when it is listed", []types.ErrorKind{types.KindAdmissionRejected}, types.Newf(types.KindAdmissionRejected, "x", "y"), true},
		{"a panic triggers when its kind is listed", []types.ErrorKind{types.KindInvariantViolation}, panicked, true},
		{"a listed kind triggers", []types.ErrorKind{types.KindTimeout}, types.Newf(types.KindTimeout, "x", "y"), true},
		{"an unlisted kind does not", []types.ErrorKind{types.KindTimeout}, types.Newf(types.KindRateLimited, "x", "y"), false},
		{"a listed terminal kind triggers", []types.ErrorKind{types.KindBudgetExceeded}, types.Newf(types.KindBudgetExceeded, "x", "y"), true},
		{"an unclassified error with kinds does not", []types.ErrorKind{types.KindTimeout}, errors.New("x"), false},
		{"an empty list triggers nothing", []types.ErrorKind{}, types.Newf(types.KindTimeout, "x", "y"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var opts []FallbackOption
			if tc.kinds != nil {
				opts = append(opts, WithFallbackKinds(tc.kinds...))
			}
			provider := Fallback(nil, opts...)(&fallbackFakeProvider{name: "p"}).(*fallbackProvider)
			if got := provider.triggers(tc.err); got != tc.want {
				t.Fatalf("triggers(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// A route with no Handler is left out when Fallback is built, where it used to
// panic on the first failure of the primary.
func TestFallbackLeavesOutARouteWithNoHandler(t *testing.T) {
	primary := &fallbackFakeProvider{name: "primary", err: unavailableErr()}
	alt := &fallbackFakeProvider{name: "alt", resp: llm.CompletionResponse{Content: "alt-ok"}}

	cases := []struct {
		name   string
		routes []FallbackRoute
		want   string
	}{
		{"only a nil route", []FallbackRoute{{}}, ""},
		{"a nil route first", []FallbackRoute{{}, {Handler: alt}}, "alt-ok"},
		{"a nil route last", []FallbackRoute{{Handler: alt}, {}}, "alt-ok"},
		{"nil routes between", []FallbackRoute{{}, {Handler: alt}, {}}, "alt-ok"},
		{"a nil route that declares a policy", []FallbackRoute{{DataPolicy: FallbackDataPolicy{Classification: "x"}}, {Handler: alt}}, "alt-ok"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			primary.calls, alt.calls = 0, 0
			wrapped := Fallback(tc.routes)(primary)
			resp, err := wrapped.Complete(context.Background(), llm.CompletionRequest{})
			if tc.want == "" {
				if err == nil {
					t.Fatal("want the primary's failure")
				}
				return
			}
			if err != nil || resp.Content != tc.want {
				t.Fatalf("resp %q, err %v; want %q", resp.Content, err, tc.want)
			}
		})
	}
}

// An error carries no response: what the last route returned beside its error
// is not passed on.
func TestFallbackReturnsAnEmptyResponseBesideAnError(t *testing.T) {
	leaky := llm.CompletionResponse{Content: "partial text from a failing provider"}
	primary := &fallbackFakeProvider{name: "primary", resp: leaky, err: unavailableErr()}
	alt := &fallbackFakeProvider{name: "alt", resp: leaky, err: unavailableErr()}

	for name, wrapped := range map[string]Handler{
		"every route failed": Fallback([]FallbackRoute{{Handler: alt}})(primary),
		"no alternates":      Fallback(nil)(primary),
		"terminal failure": Fallback([]FallbackRoute{{Handler: alt}})(&fallbackFakeProvider{
			name: "p", resp: leaky, err: types.Newf(types.KindBudgetExceeded, "x", "y"),
		}),
	} {
		resp, err := wrapped.Complete(context.Background(), llm.CompletionRequest{})
		if err == nil || resp.Content != "" {
			t.Errorf("%s: resp %q, err %v; want an empty response beside the error", name, resp.Content, err)
		}
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

// Every alternate is held to the client's data policy before it is used, by the
// same check and the same rules as the primary. A policy checked only against
// the provider the request was handed to would let a failure of the primary
// send the content on to an alternate the policy does not allow.
func TestFallbackHoldsEveryAlternateToTheClientsDataPolicy(t *testing.T) {
	policy := func(p types.DataPolicy) context.Context {
		return ops.WithExecConfig(context.Background(), &ops.ExecConfig{DataPolicy: p, DataPolicyDeclared: true})
	}
	openaiOnly := types.DataPolicy{AllowedProviders: []string{"openai"}, AllowedRegions: []string{"eu"}}
	both := types.DataPolicy{AllowedProviders: []string{"openai", "anthropic"}, AllowedRegions: []string{"eu"}}
	route := func(provider string, regions []string, retention int) llm.ProviderCapabilities {
		return llm.ProviderCapabilities{Provider: provider, Model: "m", Regions: regions, RetentionDays: retention}
	}
	eu := []string{"eu"}

	cases := []struct {
		name     string
		ctx      context.Context
		routes   []llm.ProviderCapabilities
		altCalls int
		succeeds bool
	}{
		{"no client policy", context.Background(), nil, 1, true},
		{"a client that declared no policy", ops.WithExecConfig(context.Background(), &ops.ExecConfig{}), nil, 1, true},
		{"the alternate's provider is not allowed", policy(openaiOnly), []llm.ProviderCapabilities{route("openai", eu, 0), route("anthropic", eu, 0)}, 0, false},
		{"the alternate is allowed and declared", policy(both), []llm.ProviderCapabilities{route("openai", eu, 0), route("anthropic", eu, 0)}, 1, true},
		{"the alternate is allowed and has declared nothing", policy(both), []llm.ProviderCapabilities{route("openai", eu, 0)}, 0, false},
		{"the alternate runs in a region that is not allowed", policy(both), []llm.ProviderCapabilities{route("openai", eu, 0), route("anthropic", []string{"us"}, 0)}, 0, false},
		{"the alternate may also run somewhere else", policy(both), []llm.ProviderCapabilities{route("openai", eu, 0), route("anthropic", []string{"eu", "us"}, 0)}, 0, false},
		{"the alternate keeps content", policy(both), []llm.ProviderCapabilities{route("openai", eu, 0), route("anthropic", eu, 30)}, 0, false},
		{"the alternate declares no region", policy(both), []llm.ProviderCapabilities{route("openai", eu, 0), route("anthropic", nil, 0)}, 0, false},
		{"an empty policy", policy(types.DataPolicy{}), []llm.ProviderCapabilities{route("openai", eu, 0), route("anthropic", eu, 0)}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			llm.ResetCapabilityRegistryForTest()
			t.Cleanup(llm.ResetCapabilityRegistryForTest)
			for _, declared := range tc.routes {
				llm.RegisterCapabilities(declared)
			}

			failure := errors.New("the primary is down")
			primary := &fakeProvider{name: "openai", responses: []fakeResponse{{err: failure}}}
			alternate := &fakeProvider{name: "anthropic", responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "from the alternate", Provider: "anthropic"}}}}
			wrapped := Fallback([]FallbackRoute{{Handler: alternate}})(primary)

			resp, err := wrapped.Complete(tc.ctx, llm.CompletionRequest{Model: "m", UserPrompt: "the customer's record"})
			if alternate.calls != tc.altCalls {
				t.Fatalf("the alternate was called %d times, want %d", alternate.calls, tc.altCalls)
			}
			if tc.succeeds {
				if err != nil || resp.Content != "from the alternate" {
					t.Fatalf("resp %q, err %v; want the alternate's answer", resp.Content, err)
				}
				return
			}
			// What comes back is the primary's own failure: the alternate was
			// never an option.
			if !errors.Is(err, failure) {
				t.Errorf("err = %v, want the primary's failure", err)
			}
			if resp.Content != "" {
				t.Errorf("content %q came back with the failure", resp.Content)
			}
		})
	}

	// With two alternates, the one the policy forbids is passed over and the
	// one it allows is used.
	t.Run("a forbidden alternate is passed over for an allowed one", func(t *testing.T) {
		llm.ResetCapabilityRegistryForTest()
		t.Cleanup(llm.ResetCapabilityRegistryForTest)
		llm.RegisterCapabilities(route("openai", eu, 0))
		llm.RegisterCapabilities(route("azure", eu, 0))

		primary := &fakeProvider{name: "openai", responses: []fakeResponse{{err: errors.New("down")}}}
		forbidden := &fakeProvider{name: "anthropic", responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "no"}}}}
		allowed := &fakeProvider{name: "azure", responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "yes"}}}}
		wrapped := Fallback([]FallbackRoute{{Handler: forbidden}, {Handler: allowed}})(primary)

		ctx := policy(types.DataPolicy{AllowedProviders: []string{"openai", "azure"}, AllowedRegions: eu})
		resp, err := wrapped.Complete(ctx, llm.CompletionRequest{Model: "m"})
		if err != nil || resp.Content != "yes" || forbidden.calls != 0 || allowed.calls != 1 {
			t.Fatalf("resp %q, err %v, forbidden called %d, allowed called %d", resp.Content, err, forbidden.calls, allowed.calls)
		}
	})
}

// modelRecorder is a provider that remembers the model it was last asked for.
type modelRecorder struct {
	fakeProvider
	asked string
}

func (p *modelRecorder) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.asked = req.Model
	return p.fakeProvider.Complete(ctx, req)
}

// An alternate at another provider runs the request on a model of its own, and
// it is that route the client's data policy has to allow. Checked under the
// request's model, which is the primary's, an alternate whose provider is
// declared in the EU but whose actual model is declared in the US and keeps
// content would be allowed, and the content would go to the forbidden route.
func TestFallbackChecksAnAlternateUnderTheModelItRuns(t *testing.T) {
	eu := []string{"eu"}
	policy := ops.WithExecConfig(context.Background(), &ops.ExecConfig{
		DataPolicyDeclared: true,
		DataPolicy:         types.DataPolicy{AllowedProviders: []string{"openai", "anthropic"}, AllowedRegions: eu},
	})

	cases := []struct {
		name     string
		ctx      context.Context
		altModel string
		declare  func()
		altCalls int
		sentAs   string // the model the alternate was asked for
	}{
		{"no policy, no model of its own", context.Background(), "", func() {}, 1, "gpt-x"},
		{"no policy, a model of its own", context.Background(), "claude-x", func() {}, 1, "claude-x"},
		{"its own model, declared in the EU", policy, "claude-x", func() {
			llm.DeclareRoute("anthropic", "claude-x", eu, 0)
		}, 1, "claude-x"},
		{"its own model, declared in the US", policy, "claude-x", func() {
			llm.DeclareRoute("anthropic", "claude-x", []string{"us"}, 0)
		}, 0, ""},
		{"its own model keeps content", policy, "claude-x", func() {
			llm.DeclareRoute("anthropic", "claude-x", eu, 30)
		}, 0, ""},
		{"its own model has declared nothing", policy, "claude-x", func() {}, 0, ""},
		// The provider as a whole is declared in the
		// EU and this model of it is declared in the US.
		{"the family in the EU and this model in the US", policy, "claude-x", func() {
			llm.RegisterCapabilityFamily("anthropic", "", llm.ProviderCapabilities{Regions: eu})
			llm.DeclareRoute("anthropic", "claude-x", []string{"us"}, 30)
		}, 0, ""},
		{"only the primary's model is declared for it", policy, "claude-x", func() {
			llm.DeclareRoute("anthropic", "gpt-x", eu, 0)
		}, 0, ""},
		{"no model of its own, and the request's is declared for it", policy, "", func() {
			llm.DeclareRoute("anthropic", "gpt-x", eu, 0)
		}, 1, "gpt-x"},
		{"no model of its own, and the request's is not declared for it", policy, "", func() {
			llm.DeclareRoute("anthropic", "claude-x", eu, 0)
		}, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			llm.ResetCapabilityRegistryForTest()
			t.Cleanup(llm.ResetCapabilityRegistryForTest)
			llm.DeclareRoute("openai", "gpt-x", eu, 0)
			tc.declare()

			down := errors.New("the primary is down")
			primary := &fakeProvider{name: "openai", responses: []fakeResponse{{err: down}}}
			alternate := &modelRecorder{fakeProvider: fakeProvider{name: "anthropic", responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "from the alternate"}}}}}
			wrapped := Fallback([]FallbackRoute{{Handler: alternate, Model: tc.altModel}})(primary)

			resp, err := wrapped.Complete(tc.ctx, llm.CompletionRequest{Model: "gpt-x"})
			if alternate.calls != tc.altCalls {
				t.Fatalf("the alternate was called %d times, want %d", alternate.calls, tc.altCalls)
			}
			if tc.altCalls == 0 {
				if !errors.Is(err, down) {
					t.Errorf("err = %v, want the primary's failure", err)
				}
				return
			}
			if err != nil || resp.Content != "from the alternate" {
				t.Fatalf("resp %q, err %v", resp.Content, err)
			}
			if alternate.asked != tc.sentAs {
				t.Errorf("the alternate was asked for model %q, want %q", alternate.asked, tc.sentAs)
			}
		})
	}
}

// The answer a fallback returns says which route it came by. An alternate that
// leaves the provider or the model off its answer is spoken for by the
// fallback, which knows both: a client's data policy checks the route an answer
// names, and an answer with no name on it is taken for the primary's.
func TestFallbackSaysWhichRouteAnswered(t *testing.T) {
	down := errors.New("down")
	answer := func(provider, model string) []fakeResponse {
		return []fakeResponse{{resp: llm.CompletionResponse{Content: "x", Provider: provider, Model: model}}}
	}
	failing := []fakeResponse{{err: down}}

	cases := []struct {
		name         string
		primary      []fakeResponse
		first        []fakeResponse
		firstModel   string
		second       []fakeResponse
		secondModel  string
		wantProvider string
		wantModel    string
	}{
		{"the primary answers, naming nothing: it is left alone", answer("", ""), nil, "", nil, "", "", ""},
		{"the primary answers, naming itself", answer("openai", "gpt-x-2026-01-01"), nil, "", nil, "", "openai", "gpt-x-2026-01-01"},
		{"an alternate with a model of its own, naming nothing", failing, answer("", ""), "claude-x", nil, "", "anthropic", "claude-x"},
		{"an alternate with no model of its own, naming nothing", failing, answer("", ""), "", nil, "", "anthropic", "gpt-x"},
		{"an alternate naming its provider only", failing, answer("anthropic", ""), "claude-x", nil, "", "anthropic", "claude-x"},
		{"an alternate naming its model only", failing, answer("", "claude-x-20250929"), "claude-x", nil, "", "anthropic", "claude-x-20250929"},
		{"an alternate naming both", failing, answer("anthropic", "claude-x-20250929"), "claude-x", nil, "", "anthropic", "claude-x-20250929"},
		{"an alternate that sent it on, and says so", failing, answer("elsewhere", "other-model"), "claude-x", nil, "", "elsewhere", "other-model"},
		{"the second alternate, naming nothing", failing, failing, "claude-x", answer("", ""), "gemini-x", "google", "gemini-x"},
		{"the second alternate, with no model of its own", failing, failing, "claude-x", answer("", ""), "", "google", "gpt-x"},
		{"the second alternate, naming both", failing, failing, "claude-x", answer("google", "gemini-x-001"), "gemini-x", "google", "gemini-x-001"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			primary := &fakeProvider{name: "openai", responses: tc.primary}
			var routes []FallbackRoute
			if tc.first != nil {
				routes = append(routes, FallbackRoute{Handler: &fakeProvider{name: "anthropic", responses: tc.first}, Model: tc.firstModel})
			}
			if tc.second != nil {
				routes = append(routes, FallbackRoute{Handler: &fakeProvider{name: "google", responses: tc.second}, Model: tc.secondModel})
			}
			resp, err := Fallback(routes)(primary).Complete(context.Background(), llm.CompletionRequest{Model: "gpt-x"})
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if resp.Provider != tc.wantProvider || resp.Model != tc.wantModel {
				t.Errorf("the answer names %q/%q, want %q/%q", resp.Provider, resp.Model, tc.wantProvider, tc.wantModel)
			}
		})
	}

	// A failure names nobody: nothing answered.
	primary := &fakeProvider{name: "openai", responses: failing}
	resp, err := Fallback([]FallbackRoute{{Handler: &fakeProvider{name: "anthropic", responses: failing}, Model: "claude-x"}})(primary).
		Complete(context.Background(), llm.CompletionRequest{Model: "gpt-x"})
	if !errors.Is(err, down) || resp.Provider != "" || resp.Model != "" {
		t.Errorf("resp %+v, err %v", resp, err)
	}
}

// sendingProvider is an alternate that, as the Anthropic provider does, sends a model
// other than the one it is asked for.
type sendingProvider struct {
	modelRecorder
	sends map[string]string
}

func (p *sendingProvider) ModelSentFor(requested string) string {
	if sent, ok := p.sends[requested]; ok {
		return sent
	}
	return requested
}

// An alternate is held to the client's policy under the model it will send,
// which is not always the one it is asked for: the Anthropic provider sends its
// default for a "gpt" model. A check on the name in the request would allow or
// refuse the alternate on a route it does not use. An answer that names nothing
// is spoken for under the model that was sent.
func TestFallbackChecksAnAlternateUnderTheModelItSends(t *testing.T) {
	eu := []string{"eu"}
	policy := ops.WithExecConfig(context.Background(), &ops.ExecConfig{
		DataPolicyDeclared: true,
		DataPolicy:         types.DataPolicy{AllowedProviders: []string{"openai", "anthropic"}, AllowedRegions: eu},
	})
	sends := map[string]string{"gpt-x": "claude-default", "": "claude-default"}

	cases := []struct {
		name      string
		ctx       context.Context
		altModel  string
		declare   func()
		altCalls  int
		stampedAs string // the model an unnamed answer is spoken for under
	}{
		{"no policy", context.Background(), "", func() {}, 1, "claude-default"},
		{"the model sent is declared", policy, "", func() { llm.DeclareRoute("anthropic", "claude-default", eu, 0) }, 1, "claude-default"},
		{"only the name in the request is declared", policy, "", func() { llm.DeclareRoute("anthropic", "gpt-x", eu, 0) }, 0, ""},
		{"the model sent is declared in the US", policy, "", func() {
			llm.DeclareRoute("anthropic", "claude-default", []string{"us"}, 0)
			llm.DeclareRoute("anthropic", "gpt-x", eu, 0)
		}, 0, ""},
		{"the model sent keeps content", policy, "", func() {
			llm.DeclareRoute("anthropic", "claude-default", eu, 30)
			llm.DeclareRoute("anthropic", "gpt-x", eu, 0)
		}, 0, ""},
		{"both are declared", policy, "", func() {
			llm.DeclareRoute("anthropic", "claude-default", eu, 0)
			llm.DeclareRoute("anthropic", "gpt-x", eu, 0)
		}, 1, "claude-default"},
		{"nothing is declared", policy, "", func() {}, 0, ""},
		{"a model of its own that the provider sends as asked", policy, "claude-own", func() { llm.DeclareRoute("anthropic", "claude-own", eu, 0) }, 1, "claude-own"},
		{"a model of its own that is declared elsewhere", policy, "claude-own", func() { llm.DeclareRoute("anthropic", "claude-own", []string{"us"}, 0) }, 0, ""},
		{"a model of its own, and only the default declared", policy, "claude-own", func() { llm.DeclareRoute("anthropic", "claude-default", eu, 0) }, 0, ""},
		{"a family covers the model sent", policy, "", func() {
			llm.RegisterCapabilityFamily("anthropic", "claude-", llm.ProviderCapabilities{Regions: eu})
		}, 1, "claude-default"},
		{"a family covers only the name in the request", policy, "", func() {
			llm.RegisterCapabilityFamily("anthropic", "gpt-", llm.ProviderCapabilities{Regions: eu})
		}, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			llm.ResetCapabilityRegistryForTest()
			t.Cleanup(llm.ResetCapabilityRegistryForTest)
			llm.DeclareRoute("openai", "gpt-x", eu, 0)
			tc.declare()

			down := errors.New("the primary is down")
			primary := &fakeProvider{name: "openai", responses: []fakeResponse{{err: down}}}
			alternate := &sendingProvider{
				modelRecorder: modelRecorder{fakeProvider: fakeProvider{name: "anthropic", responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "from the alternate"}}}}},
				sends:         sends,
			}
			wrapped := Fallback([]FallbackRoute{{Handler: alternate, Model: tc.altModel}})(primary)

			resp, err := wrapped.Complete(tc.ctx, llm.CompletionRequest{Model: "gpt-x"})
			if alternate.calls != tc.altCalls {
				t.Fatalf("the alternate was called %d times, want %d", alternate.calls, tc.altCalls)
			}
			if tc.altCalls == 0 {
				if !errors.Is(err, down) {
					t.Errorf("err = %v, want the primary's failure", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if resp.Provider != "anthropic" || resp.Model != tc.stampedAs {
				t.Errorf("an answer that named nothing is spoken for as %s/%s, want anthropic/%s", resp.Provider, resp.Model, tc.stampedAs)
			}
		})
	}

	// An answer that names its own route is left as it names it.
	t.Run("an answer that names its own route is not overwritten", func(t *testing.T) {
		llm.ResetCapabilityRegistryForTest()
		t.Cleanup(llm.ResetCapabilityRegistryForTest)
		llm.DeclareRoute("anthropic", "claude-default", eu, 0)
		primary := &fakeProvider{name: "openai", responses: []fakeResponse{{err: errors.New("down")}}}
		alternate := &sendingProvider{
			modelRecorder: modelRecorder{fakeProvider: fakeProvider{name: "anthropic", responses: []fakeResponse{{resp: llm.CompletionResponse{
				Content: "x", Provider: "elsewhere", Model: "its-own-snapshot"}}}}},
			sends: sends,
		}
		resp, err := Fallback([]FallbackRoute{{Handler: alternate}})(primary).Complete(policy, llm.CompletionRequest{Model: "gpt-x"})
		if err != nil || resp.Provider != "elsewhere" || resp.Model != "its-own-snapshot" {
			t.Errorf("resp %s/%s, err %v", resp.Provider, resp.Model, err)
		}
	})
}

func TestFallbackEligible(t *testing.T) {
	schema := map[string]any{"type": "object"}
	jsonRequest := llm.CompletionRequest{JSONSchema: schema, ResponseFormat: "json"}
	native := FallbackCapabilities{NativeJSONSchema: true}
	cases := []struct {
		name        string
		req         llm.CompletionRequest
		primaryCaps FallbackCapabilities
		primaryPol  string
		alt         FallbackRoute
		degrade     bool
		want        bool
	}{
		{"nothing declared", llm.CompletionRequest{}, FallbackCapabilities{}, "", FallbackRoute{}, false, true},
		{"a schema request, nothing declared", jsonRequest, FallbackCapabilities{}, "", FallbackRoute{}, false, true},
		{"a schema request, the primary native and the alternate not", jsonRequest, native, "", FallbackRoute{}, false, false},
		{"a schema request, both native", jsonRequest, native, "", FallbackRoute{Capabilities: native}, false, true},
		{"a schema request, the alternate not native but degradation allowed", jsonRequest, native, "", FallbackRoute{}, true, true},
		{"no schema in the request, the alternate not native", llm.CompletionRequest{ResponseFormat: "json"}, native, "", FallbackRoute{}, false, true},
		{"a schema but the format is text", llm.CompletionRequest{JSONSchema: schema, ResponseFormat: "text"}, native, "", FallbackRoute{}, false, true},
		{"the primary not native, the alternate native", jsonRequest, FallbackCapabilities{}, "", FallbackRoute{Capabilities: native}, false, true},
		{"a policy declared and matched", llm.CompletionRequest{}, FallbackCapabilities{}, "eu", FallbackRoute{DataPolicy: FallbackDataPolicy{Classification: "eu"}}, false, true},
		{"a policy declared and not matched", llm.CompletionRequest{}, FallbackCapabilities{}, "eu", FallbackRoute{DataPolicy: FallbackDataPolicy{Classification: "us"}}, false, false},
		{"a policy declared and the alternate declares none", llm.CompletionRequest{}, FallbackCapabilities{}, "eu", FallbackRoute{}, false, false},
		{"no policy declared, the alternate declares one", llm.CompletionRequest{}, FallbackCapabilities{}, "", FallbackRoute{DataPolicy: FallbackDataPolicy{Classification: "us"}}, false, true},
		{"the capability matches and the policy does not", jsonRequest, native, "eu", FallbackRoute{Capabilities: native, DataPolicy: FallbackDataPolicy{Classification: "us"}}, false, false},
		{"the policy matches and the capability does not, degradation allowed", jsonRequest, native, "eu", FallbackRoute{DataPolicy: FallbackDataPolicy{Classification: "eu"}}, true, true},
		{"the policy matches and the capability does not", jsonRequest, native, "eu", FallbackRoute{DataPolicy: FallbackDataPolicy{Classification: "eu"}}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := []FallbackOption{WithPrimaryCapabilities(tc.primaryCaps), WithPrimaryDataPolicy(FallbackDataPolicy{Classification: tc.primaryPol})}
			if tc.degrade {
				opts = append(opts, AllowSchemaDegradation())
			}
			provider := Fallback(nil, opts...)(&fallbackFakeProvider{name: "p"}).(*fallbackProvider)
			if got := provider.eligible(tc.req, tc.alt); got != tc.want {
				t.Fatalf("eligible = %v, want %v", got, tc.want)
			}
		})
	}
}

// fallbackRecordingProvider is a fallbackFakeProvider that keeps the requests it
// was handed, so a test can compare what an alternate received with what the
// caller sent.
type fallbackRecordingProvider struct {
	fallbackFakeProvider
	seen []llm.CompletionRequest
}

func (f *fallbackRecordingProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	f.seen = append(f.seen, req)
	return f.fallbackFakeProvider.Complete(ctx, req)
}

func unsupportedErr(provider string) error {
	return &types.OperationError{
		Kind:     types.KindUnsupportedCapability,
		Op:       "llm.complete",
		Provider: provider,
		Message:  provider + " cannot send Tools, which the request sets",
	}
}

// toolsRequest is a request in the middle of a tool loop: tools declared, a user
// turn, a call the model made and the tool's answer to it.
func toolsRequest() llm.CompletionRequest {
	return llm.CompletionRequest{
		Model:        "m",
		SystemPrompt: "You answer questions about weather.",
		Tools:        []llm.Tool{{Name: "get_weather", Description: "d"}},
		Messages: []llm.Message{
			{Role: "user", Content: "How warm is Oslo?"},
			{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "call_1", Name: "get_weather", Arguments: []byte(`{"city":"Oslo"}`)}}},
			{Role: "tool", Content: "7C", ToolCallID: "call_1"},
		},
	}
}

// An unsupported capability is a property of the route, not of the request: a
// primary that cannot take tools says so before it sends anything, and an
// alternate that can is the route that serves the request. The request reaches
// the alternate whole.
func TestFallbackTriesTheAlternateWhenThePrimaryCannotTakeTheRequest(t *testing.T) {
	calls := []llm.ToolCall{{ID: "call_2", Name: "get_weather", Arguments: []byte(`{"city":"Oslo"}`)}}
	cases := []struct {
		name string
		req  llm.CompletionRequest
	}{
		{"tools and messages", toolsRequest()},
		{"tools only", llm.CompletionRequest{Model: "m", UserPrompt: "u", Tools: []llm.Tool{{Name: "t"}}}},
		{"messages only", llm.CompletionRequest{Model: "m", Messages: []llm.Message{{Role: "user", Content: "hi"}}}},
		{"a tool choice", llm.CompletionRequest{Model: "m", UserPrompt: "u", Tools: []llm.Tool{{Name: "t"}}, ToolChoice: "required"}},
		{"web search", llm.CompletionRequest{Model: "m", UserPrompt: "u", WebSearch: true}},
		{"a plain request", llm.CompletionRequest{Model: "m", UserPrompt: "u"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			primary := &fallbackRecordingProvider{fallbackFakeProvider: fallbackFakeProvider{name: "primary", err: unsupportedErr("primary")}}
			alt := &fallbackRecordingProvider{fallbackFakeProvider: fallbackFakeProvider{name: "alt",
				resp: llm.CompletionResponse{ToolCalls: calls, FinishReason: "tool_calls"}}}

			resp, err := Fallback([]FallbackRoute{{Handler: alt}})(primary).Complete(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if primary.calls != 1 || alt.calls != 1 {
				t.Fatalf("primary asked %d times and alternate %d, want 1 and 1", primary.calls, alt.calls)
			}
			if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "call_2" || resp.Content != "" || resp.FinishReason != "tool_calls" {
				t.Errorf("the alternate's reply did not come back as it was: %+v", resp)
			}
			if resp.Provider != "alt" {
				t.Errorf("the answer names %q, want the alternate that gave it", resp.Provider)
			}
			got := alt.seen[0]
			if len(got.Tools) != len(tc.req.Tools) || len(got.Messages) != len(tc.req.Messages) ||
				got.ToolChoice != tc.req.ToolChoice || got.WebSearch != tc.req.WebSearch || got.UserPrompt != tc.req.UserPrompt {
				t.Errorf("the alternate was handed a different request:\n got %+v\nwant %+v", got, tc.req)
			}
		})
	}
}

// When no route can take the request the failure that comes back is the last
// route's own refusal, each route was asked once, and nothing comes back beside
// it.
func TestFallbackReturnsTheLastRoutesRefusalWhenNoRouteCanTakeTheRequest(t *testing.T) {
	primary := &fallbackRecordingProvider{fallbackFakeProvider: fallbackFakeProvider{name: "primary", err: unsupportedErr("primary"),
		resp: llm.CompletionResponse{Content: "partial"}}}
	first := &fallbackRecordingProvider{fallbackFakeProvider: fallbackFakeProvider{name: "first", err: unsupportedErr("first")}}
	last := &fallbackRecordingProvider{fallbackFakeProvider: fallbackFakeProvider{name: "last", err: unsupportedErr("last")}}

	resp, err := Fallback([]FallbackRoute{{Handler: first}, {Handler: last}})(primary).Complete(context.Background(), toolsRequest())
	if err == nil {
		t.Fatalf("every route refused and the call answered: %+v", resp)
	}
	if types.KindOf(err) != types.KindUnsupportedCapability {
		t.Errorf("kind = %v, want unsupported capability", types.KindOf(err))
	}
	var failure *types.OperationError
	if !errors.As(err, &failure) || failure.Provider != "last" {
		t.Errorf("the error is not the last route's own: %v", err)
	}
	if primary.calls != 1 || first.calls != 1 || last.calls != 1 {
		t.Errorf("calls %d, %d, %d; want each route asked once", primary.calls, first.calls, last.calls)
	}
	if resp.Content != "" || len(resp.ToolCalls) != 0 {
		t.Errorf("a response came back beside the error: %+v", resp)
	}
}

// A refusal passes over every route that cannot take the request, and stops at
// the first that can, without asking the ones behind it.
func TestFallbackSkipsRoutesThatRefuseAndStopsAtTheFirstThatAnswers(t *testing.T) {
	primary := &fallbackFakeProvider{name: "primary", err: unsupportedErr("primary")}
	refusing := &fallbackFakeProvider{name: "refusing", err: unsupportedErr("refusing")}
	answering := &fallbackFakeProvider{name: "answering", resp: llm.CompletionResponse{Content: "ok"}}
	unreached := &fallbackFakeProvider{name: "unreached", resp: llm.CompletionResponse{Content: "never"}}

	resp, err := Fallback([]FallbackRoute{{Handler: refusing}, {Handler: answering}, {Handler: unreached}})(primary).
		Complete(context.Background(), toolsRequest())
	if err != nil || resp.Content != "ok" {
		t.Fatalf("resp %q, err %v; want the third route's answer", resp.Content, err)
	}
	if unreached.calls != 0 {
		t.Errorf("a route behind the one that answered was asked %d times", unreached.calls)
	}
}

// An alternate the declared capabilities, the data policy or the client's policy
// disqualify is not asked, refusal or not.
func TestFallbackAfterARefusalStillHoldsAnAlternateToItsRequirements(t *testing.T) {
	t.Run("a declared data policy", func(t *testing.T) {
		primary := &fallbackFakeProvider{name: "primary", err: unsupportedErr("primary")}
		publicAlt := &fallbackFakeProvider{name: "public", resp: llm.CompletionResponse{Content: "never"}}
		wrapped := Fallback([]FallbackRoute{{Handler: publicAlt, DataPolicy: FallbackDataPolicy{Classification: "public"}}},
			WithPrimaryDataPolicy(FallbackDataPolicy{Classification: "private"}))(primary)
		_, err := wrapped.Complete(context.Background(), toolsRequest())
		if types.KindOf(err) != types.KindUnsupportedCapability || publicAlt.calls != 0 {
			t.Fatalf("err %v, alternate calls %d; want the primary's refusal and no call", err, publicAlt.calls)
		}
	})
	t.Run("native schema support", func(t *testing.T) {
		primary := &fallbackFakeProvider{name: "primary", err: unsupportedErr("primary")}
		weak := &fallbackFakeProvider{name: "weak", resp: llm.CompletionResponse{Content: "never"}}
		wrapped := Fallback([]FallbackRoute{{Handler: weak}}, WithPrimaryCapabilities(FallbackCapabilities{NativeJSONSchema: true}))(primary)
		req := toolsRequest()
		req.ResponseFormat, req.JSONSchema = "json", map[string]any{"type": "object"}
		_, err := wrapped.Complete(context.Background(), req)
		if types.KindOf(err) != types.KindUnsupportedCapability || weak.calls != 0 {
			t.Fatalf("err %v, alternate calls %d; want the primary's refusal and no call", err, weak.calls)
		}
	})
	t.Run("the client's data policy", func(t *testing.T) {
		primary := &fallbackFakeProvider{name: "openai", err: unsupportedErr("openai")}
		other := &fallbackFakeProvider{name: "other", resp: llm.CompletionResponse{Content: "never"}}
		ctx := ops.WithExecConfig(context.Background(), &ops.ExecConfig{
			DataPolicy: types.DataPolicy{AllowedProviders: []string{"openai"}}, DataPolicyDeclared: true})
		_, err := Fallback([]FallbackRoute{{Handler: other}})(primary).Complete(ctx, toolsRequest())
		if types.KindOf(err) != types.KindUnsupportedCapability || other.calls != 0 {
			t.Fatalf("err %v, alternate calls %d; want the primary's refusal and no call", err, other.calls)
		}
	})
	t.Run("a cancelled context", func(t *testing.T) {
		primary := &fallbackFakeProvider{name: "primary", err: unsupportedErr("primary")}
		alt := &fallbackFakeProvider{name: "alt", resp: llm.CompletionResponse{Content: "never"}}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := Fallback([]FallbackRoute{{Handler: alt}})(primary).Complete(ctx, toolsRequest())
		if !errors.Is(err, context.Canceled) || alt.calls != 0 {
			t.Fatalf("err %v, alternate calls %d; want cancellation and no call", err, alt.calls)
		}
	})
}

// The built-in providers that cannot do tools refuse before anything is sent, and
// that refusal is what sends the request on to the alternate that can.
func TestFallbackRoutesAToolsRequestAwayFromProvidersThatCannotDoTools(t *testing.T) {
	local, err := llm.NewLocalProvider(llm.ProviderConfig{})
	if err != nil {
		t.Fatal(err)
	}
	alt := &fallbackFakeProvider{name: "alt", resp: llm.CompletionResponse{Content: "ok"}}

	resp, err := Fallback([]FallbackRoute{{Handler: alt}})(local).Complete(context.Background(), toolsRequest())
	if err != nil || resp.Content != "ok" || alt.calls != 1 {
		t.Fatalf("resp %q, err %v, alternate calls %d; want the alternate's answer", resp.Content, err, alt.calls)
	}
}

// Every kind, in both directions. With no kinds configured a kind that stops
// everything stays terminal, a full bulkhead stops too (it is the limit the
// program set), and every other kind goes to the alternate, an unsupported
// capability among them: it is the route's, not the request's. With
// the kind listed it goes to the alternate whatever it is; with another kind
// listed it does not.
func TestFallbackTreatsEveryKindOfFailureAsItShould(t *testing.T) {
	for kind := types.KindUnknown; kind <= types.KindReviewRequired; kind++ {
		stops := (kindIsTerminal(kind) && kind != types.KindUnsupportedCapability) || kind == types.KindAdmissionRejected
		for _, withTools := range []bool{false, true} {
			name := kind.String()
			req := llm.CompletionRequest{Model: "m", UserPrompt: "u"}
			if withTools {
				name += " with tools"
				req = toolsRequest()
			}
			t.Run(name, func(t *testing.T) {
				failure := &types.OperationError{Kind: kind, Op: "x", Message: "failed"}
				ask := func(opts ...FallbackOption) (llm.CompletionResponse, error, *fallbackFakeProvider) {
					primary := &fallbackFakeProvider{name: "primary", err: failure}
					alt := &fallbackFakeProvider{name: "alt", resp: llm.CompletionResponse{Content: "from the alternate"}}
					resp, err := Fallback([]FallbackRoute{{Handler: alt}}, opts...)(primary).Complete(context.Background(), req)
					return resp, err, alt
				}

				// No kinds configured.
				resp, err, alt := ask()
				if stops {
					if !errors.Is(err, failure) || alt.calls != 0 {
						t.Errorf("default: err %v, alternate calls %d; want the primary's own failure and no call", err, alt.calls)
					}
				} else if err != nil || resp.Content != "from the alternate" || alt.calls != 1 {
					t.Errorf("default: resp %q, err %v, alternate calls %d; want the alternate's answer", resp.Content, err, alt.calls)
				}

				// The kind itself listed: the caller asked for it.
				resp, err, alt = ask(WithFallbackKinds(kind))
				if err != nil || resp.Content != "from the alternate" || alt.calls != 1 {
					t.Errorf("listed: resp %q, err %v, alternate calls %d; want the alternate's answer", resp.Content, err, alt.calls)
				}

				// Another kind listed: the caller asked for something else.
				other := types.KindTimeout
				if kind == types.KindTimeout {
					other = types.KindRateLimited
				}
				_, err, alt = ask(WithFallbackKinds(other))
				if !errors.Is(err, failure) || alt.calls != 0 {
					t.Errorf("another kind listed: err %v, alternate calls %d; want the primary's own failure and no call", err, alt.calls)
				}
			})
		}
	}
}
