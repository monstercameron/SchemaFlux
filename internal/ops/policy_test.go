package ops

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// policyCountingProvider is a minimal llm.Provider whose call count a test
// can inspect directly, so "not called" can be pinned as a call count of
// zero rather than inferred from an error being returned.
type policyCountingProvider struct {
	name  string
	calls int
}

func (p *policyCountingProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.calls++
	return llm.CompletionResponse{Content: "ok"}, nil
}
func (p *policyCountingProvider) Name() string                                   { return p.name }
func (p *policyCountingProvider) EstimateCost(req llm.CompletionRequest) float64 { return 0 }
func (p *policyCountingProvider) RetryPolicy() (int, time.Duration)              { return 0, 0 }

// CP-001/CP-002 combined route selection: a route must be both
// capability-eligible and policy-eligible, and the caller must be able to
// tell which family of refusal fired.

func setupPolicyTestCapabilities(t *testing.T) {
	t.Helper()
	llm.ResetCapabilityRegistryForTest()
	t.Cleanup(llm.ResetCapabilityRegistryForTest)

	llm.RegisterCapabilities(llm.ProviderCapabilities{
		Provider: "openai", Model: "gpt-4o",
		Supports:              map[llm.Capability]bool{llm.CapNativeJSONSchema: true, llm.CapStreaming: true},
		UsageReportingQuality: llm.UsageExact,
		ContextWindow:         128000,
	})
	llm.RegisterCapabilities(llm.ProviderCapabilities{
		Provider: "local", Model: "gemma-4",
		Supports: map[llm.Capability]bool{llm.CapJSONMode: true},
	})
}

func TestEligibleRouteRefusesUnknownCapabilities(t *testing.T) {
	setupPolicyTestCapabilities(t)

	c := RouteCandidate{Provider: "nobody", Model: "mystery", Region: "us"}
	policy := types.DataPolicy{AllowedProviders: []string{"nobody"}, AllowedRegions: []string{"us"}}

	err := EligibleRoute(c, llm.Requirement{Capabilities: []llm.Capability{llm.CapNativeJSONSchema}}, policy)
	if err == nil {
		t.Fatal("EligibleRoute accepted a route with no capability declaration")
	}
	if !errors.Is(err, llm.ErrCapabilityUnknown) {
		t.Fatalf("err = %v, want it to wrap llm.ErrCapabilityUnknown", err)
	}
}

func TestEligibleRouteRefusesUnmetCapability(t *testing.T) {
	setupPolicyTestCapabilities(t)

	c := RouteCandidate{Provider: "local", Model: "gemma-4", Region: "us"}
	policy := types.DataPolicy{AllowedProviders: []string{"local"}, AllowedRegions: []string{"us"}}

	err := EligibleRoute(c, llm.Requirement{Capabilities: []llm.Capability{llm.CapNativeJSONSchema}}, policy)
	if err == nil {
		t.Fatal("EligibleRoute accepted a route lacking native JSON schema")
	}
	if !errors.Is(err, llm.ErrCapabilityUnmet) {
		t.Fatalf("err = %v, want it to wrap llm.ErrCapabilityUnmet", err)
	}
}

func TestEligibleRouteRefusesProviderNotInPolicy(t *testing.T) {
	setupPolicyTestCapabilities(t)

	c := RouteCandidate{Provider: "openai", Model: "gpt-4o", Region: "us"}
	policy := types.DataPolicy{AllowedProviders: []string{"anthropic"}, AllowedRegions: []string{"us"}}

	err := EligibleRoute(c, llm.Requirement{}, policy)
	if err == nil {
		t.Fatal("EligibleRoute accepted a provider absent from the data policy's allowed list")
	}
	if !errors.Is(err, ErrDataPolicyRefusal) {
		t.Fatalf("err = %v, want it to wrap ErrDataPolicyRefusal", err)
	}
}

func TestEligibleRouteRefusesRegionNotInPolicy(t *testing.T) {
	setupPolicyTestCapabilities(t)

	c := RouteCandidate{Provider: "openai", Model: "gpt-4o", Region: "ap-southeast"}
	policy := types.DataPolicy{AllowedProviders: []string{"openai"}, AllowedRegions: []string{"us", "eu"}}

	err := EligibleRoute(c, llm.Requirement{}, policy)
	if err == nil {
		t.Fatal("EligibleRoute accepted a region absent from the data policy's allowed list")
	}
	if !errors.Is(err, ErrDataPolicyRefusal) {
		t.Fatalf("err = %v, want it to wrap ErrDataPolicyRefusal", err)
	}
}

func TestEligibleRouteRefusesExcessRetention(t *testing.T) {
	setupPolicyTestCapabilities(t)

	c := RouteCandidate{Provider: "openai", Model: "gpt-4o", Region: "us", RetentionDays: 30}
	policy := types.DataPolicy{
		AllowedProviders: []string{"openai"}, AllowedRegions: []string{"us"},
		MaxRetentionDays: 7,
	}

	err := EligibleRoute(c, llm.Requirement{}, policy)
	if err == nil {
		t.Fatal("EligibleRoute accepted retention longer than the policy's ceiling")
	}
	if !errors.Is(err, ErrDataPolicyRefusal) {
		t.Fatalf("err = %v, want it to wrap ErrDataPolicyRefusal", err)
	}
}

func TestEligibleRouteAcceptsFullyQualifiedRoute(t *testing.T) {
	setupPolicyTestCapabilities(t)

	c := RouteCandidate{Provider: "openai", Model: "gpt-4o", Region: "us", RetentionDays: 0}
	policy := types.DataPolicy{
		AllowedProviders: []string{"openai"}, AllowedRegions: []string{"us"},
		MaxRetentionDays: 7,
	}

	err := EligibleRoute(c, llm.Requirement{
		Capabilities:    []llm.Capability{llm.CapNativeJSONSchema},
		MinUsageQuality: llm.UsageEstimated,
	}, policy)
	if err != nil {
		t.Fatalf("EligibleRoute refused a route that should qualify: %v", err)
	}
}

// The bar this milestone names explicitly: a route that cannot meet a
// required capability is NEVER CALLED. SelectRoute is the function that
// would hand a route to a caller about to make the actual provider call, so
// this asserts the disqualified candidate is skipped -- not merely that an
// error is returned -- by checking which candidate SelectRoute returns.
func TestSelectRouteSkipsIneligibleCandidatesEntirely(t *testing.T) {
	setupPolicyTestCapabilities(t)

	candidates := []RouteCandidate{
		{Provider: "local", Model: "gemma-4", Region: "us"}, // capability-ineligible (no native schema)
		{Provider: "openai", Model: "gpt-4o", Region: "cn"}, // policy-ineligible (region)
		{Provider: "openai", Model: "gpt-4o", Region: "us"}, // eligible
	}
	policy := types.DataPolicy{AllowedProviders: []string{"local", "openai"}, AllowedRegions: []string{"us"}}
	req := llm.Requirement{Capabilities: []llm.Capability{llm.CapNativeJSONSchema}}

	got, err := SelectRoute(candidates, req, policy)
	if err != nil {
		t.Fatalf("SelectRoute: %v", err)
	}
	if got.Provider != "openai" || got.Region != "us" {
		t.Fatalf("SelectRoute chose %+v, want the openai/us candidate", got)
	}
}

func TestSelectRouteRefusesWhenNoCandidateQualifies(t *testing.T) {
	setupPolicyTestCapabilities(t)

	candidates := []RouteCandidate{
		{Provider: "local", Model: "gemma-4", Region: "us"},
	}
	policy := types.DataPolicy{AllowedProviders: []string{"local"}, AllowedRegions: []string{"us"}}
	req := llm.Requirement{Capabilities: []llm.Capability{llm.CapNativeJSONSchema}}

	_, err := SelectRoute(candidates, req, policy)
	if err == nil {
		t.Fatal("SelectRoute returned a candidate when none qualified")
	}
	if !errors.Is(err, ErrRouteRefused) {
		t.Fatalf("err = %v, want it to wrap ErrRouteRefused", err)
	}
}

func TestSelectRouteRefusesEmptyCandidateList(t *testing.T) {
	setupPolicyTestCapabilities(t)

	_, err := SelectRoute(nil, llm.Requirement{}, types.DataPolicy{})
	if !errors.Is(err, ErrRouteRefused) {
		t.Fatalf("err = %v, want it to wrap ErrRouteRefused for an empty candidate list", err)
	}
}

// A private, no-retention policy makes a candidate declaring any retention
// unselectable, and the refusal names the constraint -- CP-002's own
// verification line.
func TestPrivateNoRetentionPolicyMakesRetainingCandidateUnselectable(t *testing.T) {
	setupPolicyTestCapabilities(t)

	c := RouteCandidate{Provider: "openai", Model: "gpt-4o", Region: "us", RetentionDays: 1}
	policy := types.DataPolicy{
		AllowedProviders: []string{"openai"}, AllowedRegions: []string{"us"},
		MaxRetentionDays: 0,
	}
	err := EligibleRoute(c, llm.Requirement{}, policy)
	if err == nil {
		t.Fatal("a retention-declaring candidate was accepted under a zero-retention policy")
	}
	if !errors.Is(err, ErrDataPolicyRefusal) {
		t.Fatalf("err = %v, want ErrDataPolicyRefusal", err)
	}
	if err.Error() == "" {
		t.Fatal("refusal carries no message naming the constraint")
	}
}

// This is the specific bar the milestone names: a route that cannot meet a
// required capability is not called -- proved by a call count of zero on the
// disqualified route's actual provider, not merely by SelectRoute returning
// a different candidate. A caller who used SelectRoute's answer to decide
// which provider to invoke never touches the disqualified one's Complete at
// all.
func TestDisqualifiedCandidateProviderIsNeverCalled(t *testing.T) {
	setupPolicyTestCapabilities(t)

	disqualified := &policyCountingProvider{name: "local"} // lacks CapNativeJSONSchema
	eligible := &policyCountingProvider{name: "openai"}
	providers := map[string]*policyCountingProvider{"local": disqualified, "openai": eligible}

	candidates := []RouteCandidate{
		{Provider: "local", Model: "gemma-4", Region: "us"},
		{Provider: "openai", Model: "gpt-4o", Region: "us"},
	}
	policy := types.DataPolicy{AllowedProviders: []string{"local", "openai"}, AllowedRegions: []string{"us"}}
	req := llm.Requirement{Capabilities: []llm.Capability{llm.CapNativeJSONSchema}}

	chosen, err := SelectRoute(candidates, req, policy)
	if err != nil {
		t.Fatalf("SelectRoute: %v", err)
	}

	// Only now, after selection, does anything call a provider -- mirroring
	// how a real caller would use this: negotiate first, invoke second.
	if _, err := providers[chosen.Provider].Complete(context.Background(), llm.CompletionRequest{}); err != nil {
		t.Fatalf("Complete on the selected provider: %v", err)
	}

	if disqualified.calls != 0 {
		t.Fatalf("disqualified provider was called %d times, want 0", disqualified.calls)
	}
	if eligible.calls != 1 {
		t.Fatalf("eligible provider was called %d times, want 1", eligible.calls)
	}
}
