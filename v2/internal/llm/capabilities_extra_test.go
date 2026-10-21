package llm

import (
	"errors"
	"strings"
	"testing"
)

// TestNegotiateAccumulatesEveryReason proves a route failing on several axes
// at once (missing capability, low usage quality, unsupported schema
// keyword, and both ceilings) reports all of them in one error rather than
// stopping at the first mismatch.
func TestNegotiateAccumulatesEveryReason(t *testing.T) {
	caps := ProviderCapabilities{
		Provider:              "negotiate-test-vendor",
		Model:                 "negotiate-test-model",
		Supports:              map[Capability]bool{CapNativeJSONSchema: true},
		SchemaKeywords:        []string{"required"},
		UsageReportingQuality: UsageEstimated,
		ContextWindow:         1000,
		MaxOutputTokens:       100,
	}
	req := Requirement{
		Capabilities:       []Capability{CapNativeJSONSchema, CapStreaming},
		SchemaKeywords:     []string{"required", "additionalProperties"},
		MinUsageQuality:    UsageExact,
		MinContextWindow:   2000,
		MinMaxOutputTokens: 200,
	}

	err := Negotiate(caps, true, req)
	if err == nil {
		t.Fatal("Negotiate accepted a route that fails every axis")
	}
	if !errors.Is(err, ErrCapabilityUnmet) {
		t.Errorf("error does not wrap ErrCapabilityUnmet: %v", err)
	}
	for _, want := range []string{
		"missing capabilities: streaming",
		"usage reporting quality",
		"unsupported schema keywords: additionalProperties",
		"context window 1000 below required 2000",
		"max output tokens 100 below required 200",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Negotiate error missing %q: %v", want, err)
		}
	}
}

// TestNegotiateUnknownRoute proves an unknown route is refused with
// ErrCapabilityUnknown, distinct from ErrCapabilityUnmet, and never reads
// caps at all beyond naming it in the message.
func TestNegotiateUnknownRoute(t *testing.T) {
	err := Negotiate(ProviderCapabilities{Provider: "p", Model: "m"}, false, Requirement{})
	if err == nil {
		t.Fatal("Negotiate accepted an unknown route")
	}
	if !errors.Is(err, ErrCapabilityUnknown) {
		t.Errorf("error does not wrap ErrCapabilityUnknown: %v", err)
	}
}

// TestNegotiateEmptyRequirementAlwaysSucceeds proves a Requirement asking for
// nothing is satisfied by any known route, including a bare zero-value one.
func TestNegotiateEmptyRequirementAlwaysSucceeds(t *testing.T) {
	if err := Negotiate(ProviderCapabilities{Provider: "p", Model: "m"}, true, Requirement{}); err != nil {
		t.Errorf("Negotiate(empty requirement) = %v, want nil", err)
	}
}

// TestMissingSchemaKeywordsSkippedWithoutNativeSchemaRequirement proves
// SchemaKeywords is only meaningful when the request also asks for
// CapNativeJSONSchema -- asking for a keyword without asking for native
// schema support at all must not manufacture a spurious mismatch.
func TestMissingSchemaKeywordsSkippedWithoutNativeSchemaRequirement(t *testing.T) {
	caps := ProviderCapabilities{SchemaKeywords: []string{}}
	req := Requirement{
		Capabilities:   []Capability{CapJSONMode}, // not CapNativeJSONSchema
		SchemaKeywords: []string{"required"},
	}
	if missing := missingSchemaKeywords(caps, req); missing != nil {
		t.Errorf("missingSchemaKeywords = %v, want nil when native schema was never requested", missing)
	}
}

// TestMissingSchemaKeywordsNoKeywordsRequested proves the empty-request
// short circuit: no SchemaKeywords named means nothing to check regardless
// of what caps declares.
func TestMissingSchemaKeywordsNoKeywordsRequested(t *testing.T) {
	req := Requirement{Capabilities: []Capability{CapNativeJSONSchema}}
	if missing := missingSchemaKeywords(ProviderCapabilities{}, req); missing != nil {
		t.Errorf("missingSchemaKeywords = %v, want nil when no keywords were requested", missing)
	}
}

// TestCapabilitiesForFamilyMatching proves the family lookup picks the exact
// entry first, then the LONGEST matching prefix among overlapping families,
// scoped to the right provider, and returns unknown when nothing matches.
func TestCapabilitiesForFamilyMatching(t *testing.T) {
	defer ResetCapabilityRegistryForTest()
	ResetCapabilityRegistryForTest()

	RegisterCapabilityFamily("openai", "gpt-5", ProviderCapabilities{
		Supports: map[Capability]bool{CapStreaming: true},
	})
	RegisterCapabilityFamily("openai", "gpt-5.6", ProviderCapabilities{
		Supports: map[Capability]bool{CapStreaming: true, CapToolCalling: true},
	})
	RegisterCapabilityFamily("", "shared-prefix", ProviderCapabilities{
		Supports: map[Capability]bool{CapSeed: true},
	})
	RegisterCapabilities(ProviderCapabilities{
		Provider: "openai", Model: "gpt-5.6-luna",
		Supports: map[Capability]bool{CapNativeJSONSchema: true},
	})

	t.Run("exact entry wins over any family", func(t *testing.T) {
		caps, known := CapabilitiesFor("openai", "gpt-5.6-luna")
		if !known {
			t.Fatal("exact entry not found")
		}
		if !caps.Has(CapNativeJSONSchema) {
			t.Error("exact entry's own capability is missing")
		}
		if caps.Has(CapToolCalling) {
			t.Error("the exact entry must win outright, not merge with the family")
		}
	})

	t.Run("longest matching family prefix wins", func(t *testing.T) {
		caps, known := CapabilitiesFor("openai", "gpt-5.6-sol")
		if !known {
			t.Fatal("no family matched gpt-5.6-sol")
		}
		if !caps.Has(CapToolCalling) {
			t.Error("the longer, more specific gpt-5.6 family should have won over the gpt-5 family")
		}
		if caps.Provider != "openai" || caps.Model != "gpt-5.6-sol" {
			t.Errorf("Provider/Model = %q/%q, want them stamped with the queried route", caps.Provider, caps.Model)
		}
	})

	t.Run("shorter family still matches a model outside the longer one", func(t *testing.T) {
		caps, known := CapabilitiesFor("openai", "gpt-5.4-mini")
		if !known {
			t.Fatal("the gpt-5 family did not match gpt-5.4-mini")
		}
		if !caps.Has(CapStreaming) || caps.Has(CapToolCalling) {
			t.Errorf("caps = %+v, want only the gpt-5 family's declaration", caps)
		}
	})

	t.Run("a provider-scoped family does not leak to another provider", func(t *testing.T) {
		if _, known := CapabilitiesFor("anthropic", "gpt-5.6-luna"); known {
			t.Error("an openai-scoped family matched an anthropic lookup")
		}
	})

	t.Run("a provider-agnostic family matches regardless of provider", func(t *testing.T) {
		caps, known := CapabilitiesFor("anyvendor", "shared-prefix-model")
		if !known || !caps.Has(CapSeed) {
			t.Errorf("caps, known = %+v, %v; want the provider-agnostic family to match", caps, known)
		}
	})

	t.Run("nothing matches at all", func(t *testing.T) {
		if _, known := CapabilitiesFor("openai", "totally-unrelated-model"); known {
			t.Error("CapabilitiesFor claimed to know an unregistered model")
		}
	})
}
