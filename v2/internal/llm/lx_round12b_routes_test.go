package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// Review round 12, routes and the registry.

// Which model names are one route. An allowed route was refused after it had
// been paid for whenever a provider answered under a name that was not the
// asked name with a version tag on the end: a "-latest" alias answered by the
// snapshot it resolved to, a pinned snapshot answered by its alias, a gateway's
// "vendor/model" answered by the bare model. The table's second half is the
// property that has to keep holding: no model passes as a version of another.
func TestSameModelRouteAcceptsTheNamesAProviderAnswersUnder(t *testing.T) {
	cases := []struct {
		name            string
		asked, answered string
		same            bool
	}{
		// A "-latest" alias answered by a snapshot, or by its bare name.
		{"a latest alias answered by a dated snapshot", "claude-3-5-sonnet-latest", "claude-3-5-sonnet-20241022", true},
		{"a latest alias answered by a numbered version", "gemini-1.5-pro-latest", "gemini-1.5-pro-002", true},
		{"a latest alias answered by an ISO-dated snapshot", "gpt-4o-latest", "gpt-4o-2024-08-06", true},
		{"a latest alias answered by its bare name", "claude-3-5-sonnet-latest", "claude-3-5-sonnet", true},
		{"a latest alias in other capitals", "Claude-3-5-Sonnet-LATEST", "claude-3-5-sonnet-20241022", true},
		{"a latest alias answered by itself", "gemini-1.5-pro-latest", "gemini-1.5-pro-latest", true},
		// An asked snapshot answered by its base alias.
		{"a snapshot answered by its alias", "gpt-4o-2024-08-06", "gpt-4o", true},
		{"a Claude snapshot answered by its alias", "claude-sonnet-4-5-20250929", "claude-sonnet-4-5", true},
		{"a numbered version answered by its alias", "gemini-1.5-pro-002", "gemini-1.5-pro", true},
		{"a mini snapshot answered by the mini alias", "gpt-4o-mini-2024-07-18", "gpt-4o-mini", true},
		// A gateway-prefixed request answered bare.
		{"a prefixed request answered by a dated snapshot", "openai/gpt-4o", "gpt-4o-2024-08-06", true},
		{"a prefixed request answered by the bare name", "openai/gpt-4o", "gpt-4o", true},
		{"a prefixed request answered with the prefix and a tag", "openai/gpt-4o", "openai/gpt-4o-2024-08-06", true},
		{"a prefixed latest alias answered by a snapshot", "anthropic/claude-3-5-sonnet-latest", "claude-3-5-sonnet-20241022", true},
		{"a prefixed snapshot answered by its bare alias", "openai/gpt-4o-2024-08-06", "gpt-4o", true},
		{"a prefix of two parts", "accounts/fireworks/llama-v3", "llama-v3", true},
		{"a prefixed request in other capitals", "OpenAI/GPT-4o", "gpt-4o-2024-08-06", true},
		// What was already the same route.
		{"the same name", "gpt-4o", "gpt-4o", true},
		{"a version tag on the answer", "gpt-4o", "gpt-4o-2024-08-06", true},

		// The names a tag rule must leave alone: other models.
		{"gpt-4 is not gpt-4-32k", "gpt-4", "gpt-4-32k", false},
		{"gpt-4 is not a preview of itself", "gpt-4", "gpt-4-1106-preview", false},
		{"a preview asked is not the alias answered", "gpt-4-1106-preview", "gpt-4", false},
		{"llama-3.1-70b is not llama-3.1", "llama-3.1", "llama-3.1-70b", false},
		{"a free variant is not the model", "model", "model:free", false},
		{"a fine-tune is not its base", "gpt-4o", "ft:gpt-4o:org::id", false},
		{"a fine-tune asked is not its base answered", "ft:gpt-4o:org::id", "gpt-4o", false},
		{"gpt-4o is not gpt-4o-mini with a date", "gpt-4o", "gpt-4o-mini-2024-07-18", false},
		{"gpt-4o-mini is not gpt-4o", "gpt-4o-mini", "gpt-4o", false},
		{"a mini snapshot is not the full model", "gpt-4o-mini-2024-07-18", "gpt-4o", false},
		{"a latest alias is not another model's snapshot", "claude-3-5-sonnet-latest", "claude-3-5-haiku-20241022", false},
		{"a latest alias is not a longer name", "gpt-4o-latest", "gpt-4o-mini", false},
		{"a latest alias is not a variant with a date", "gpt-4o-latest", "gpt-4o-mini-2024-07-18", false},
		{"nothing is left of a bare latest", "-latest", "-001", false},
		{"a prefix does not make another model the same", "openai/gpt-4o", "gpt-4o-mini", false},
		{"a prefix on its own is nothing", "openai/", "openai", false},
		{"a prefixed model is not another's bare name", "openai/gpt-4o", "gpt-4", false},

		// Recorded limits, both refused on purpose: nothing in the names says
		// these are one model.
		{"a preview rename", "gpt-4-turbo-preview", "gpt-4-0125-preview", false},
		{"an Azure deployment name", "my-gpt4-deployment", "gpt-4-0613", false},
		{"a pinned snapshot answered by another snapshot", "gpt-4o-2024-08-06", "gpt-4o-2024-11-20", false},

		// Neither name can be empty.
		{"no asked name", "", "gpt-4o", false},
		{"no answered name", "gpt-4o", "", false},
		{"neither", "", "", false},
		{"only blanks", "  ", "  ", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SameModelRoute(tc.asked, tc.answered); got != tc.same {
				t.Errorf("SameModelRoute(%q, %q) = %v, want %v", tc.asked, tc.answered, got, tc.same)
			}
		})
	}
}

// The tag rules are unchanged by the new shapes: the names that were never
// stripped to another model still are not.
func TestWithoutVersionTagLeavesOtherModelsAlone(t *testing.T) {
	cases := []struct{ name, want string }{
		{"gpt-4-32k", "gpt-4-32k"},
		{"gpt-4-1106-preview", "gpt-4-1106-preview"},
		{"gpt-4-turbo-preview", "gpt-4-turbo-preview"},
		{"llama-3.1-70b", "llama-3.1-70b"},
		{"model:free", "model:free"},
		{"ft:gpt-4o:org::id", "ft:gpt-4o:org::id"},
		{"gpt-4o-mini-2024-07-18", "gpt-4o-mini"},
		{"o3-mini-2025-01-31", "o3-mini"},
		{"claude-3-5-sonnet-20241022", "claude-3-5-sonnet"},
		{"gemini-1.5-pro-002", "gemini-1.5-pro"},
		{"deployment@001", "deployment"},
		{"gemini-1.5-pro-latest", "gemini-1.5-pro-latest"},
		{"-123", "-123"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := WithoutVersionTag(tc.name); got != tc.want {
			t.Errorf("WithoutVersionTag(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Registering a family that is already registered replaces it. The first one
// stood and the later one was ignored without a word, so a family re-registered
// to tighten its regions or its retention left the looser one in force, and the
// data policy read the loose one.
func TestRegisteringAFamilyAgainReplacesIt(t *testing.T) {
	cases := []struct {
		name      string
		register  func()
		model     string
		regions   []string
		retention int
	}{
		{"the same family twice, tightened", func() {
			RegisterCapabilityFamily("openai", "gpt-", ProviderCapabilities{Regions: []string{"eu", "us"}, RetentionDays: 30})
			RegisterCapabilityFamily("openai", "gpt-", ProviderCapabilities{Regions: []string{"eu"}, RetentionDays: 0})
		}, "gpt-x", []string{"eu"}, 0},
		{"the same family twice, loosened", func() {
			RegisterCapabilityFamily("openai", "gpt-", ProviderCapabilities{Regions: []string{"eu"}})
			RegisterCapabilityFamily("openai", "gpt-", ProviderCapabilities{Regions: []string{"eu", "us"}, RetentionDays: 7})
		}, "gpt-x", []string{"eu", "us"}, 7},
		{"three times, the last stands", func() {
			RegisterCapabilityFamily("openai", "gpt-", ProviderCapabilities{Regions: []string{"a"}})
			RegisterCapabilityFamily("openai", "gpt-", ProviderCapabilities{Regions: []string{"b"}})
			RegisterCapabilityFamily("openai", "gpt-", ProviderCapabilities{Regions: []string{"c"}})
		}, "gpt-x", []string{"c"}, 0},
		{"the provider in other capitals", func() {
			RegisterCapabilityFamily("openai", "gpt-", ProviderCapabilities{Regions: []string{"us"}})
			RegisterCapabilityFamily(" OpenAI ", "gpt-", ProviderCapabilities{Regions: []string{"eu"}})
		}, "gpt-x", []string{"eu"}, 0},
		{"the prefix in other capitals", func() {
			RegisterCapabilityFamily("openai", "gpt-", ProviderCapabilities{Regions: []string{"us"}})
			RegisterCapabilityFamily("openai", "GPT-", ProviderCapabilities{Regions: []string{"eu"}})
		}, "gpt-x", []string{"eu"}, 0},
		{"a replacement that declares nothing clears what was declared", func() {
			RegisterCapabilityFamily("openai", "gpt-", ProviderCapabilities{Regions: []string{"eu"}, RetentionDays: 9})
			RegisterCapabilityFamily("openai", "gpt-", ProviderCapabilities{})
		}, "gpt-x", nil, 0},
		{"another prefix is another family", func() {
			RegisterCapabilityFamily("openai", "gpt-", ProviderCapabilities{Regions: []string{"us"}})
			RegisterCapabilityFamily("openai", "gpt-x", ProviderCapabilities{Regions: []string{"eu"}})
		}, "gpt-y", []string{"us"}, 0},
		{"another provider's family is another family", func() {
			RegisterCapabilityFamily("openai", "gpt-", ProviderCapabilities{Regions: []string{"us"}})
			RegisterCapabilityFamily("azure", "gpt-", ProviderCapabilities{Regions: []string{"eu"}})
		}, "gpt-y", []string{"us"}, 0},
		{"a family for every provider is another family", func() {
			RegisterCapabilityFamily("", "gpt-", ProviderCapabilities{Regions: []string{"us"}})
			RegisterCapabilityFamily("openai", "gpt-", ProviderCapabilities{Regions: []string{"eu"}})
		}, "gpt-y", []string{"eu"}, 0},
		{"the provider-specific family wins at equal length, registered first", func() {
			RegisterCapabilityFamily("openai", "gpt-", ProviderCapabilities{Regions: []string{"eu"}})
			RegisterCapabilityFamily("", "gpt-", ProviderCapabilities{Regions: []string{"us"}})
		}, "gpt-y", []string{"eu"}, 0},
		{"the provider-specific family wins at equal length, registered last", func() {
			RegisterCapabilityFamily("", "gpt-", ProviderCapabilities{Regions: []string{"us"}})
			RegisterCapabilityFamily("openai", "gpt-", ProviderCapabilities{Regions: []string{"eu"}})
		}, "gpt-y", []string{"eu"}, 0},
		{"a longer prefix still wins over a provider-specific shorter one", func() {
			RegisterCapabilityFamily("openai", "gpt-", ProviderCapabilities{Regions: []string{"eu"}})
			RegisterCapabilityFamily("", "gpt-y", ProviderCapabilities{Regions: []string{"us"}})
		}, "gpt-y", []string{"us"}, 0},
		{"a route declared before the family is replaced follows the new one", func() {
			RegisterCapabilityFamily("openai", "gpt-", ProviderCapabilities{Regions: []string{"us"}, RetentionDays: 30})
			DeclareRoute("openai", "gpt-y", []string{"eu"}, 1)
			RegisterCapabilityFamily("openai", "gpt-", ProviderCapabilities{Regions: []string{"ap"}, RetentionDays: 2})
		}, "gpt-x", []string{"ap"}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ResetCapabilityRegistryForTest()
			t.Cleanup(ResetCapabilityRegistryForTest)
			tc.register()
			caps, known := CapabilitiesFor("openai", tc.model)
			if !known {
				t.Fatal("the model is not known")
			}
			if !reflect.DeepEqual(caps.Regions, tc.regions) || caps.RetentionDays != tc.retention {
				t.Errorf("regions %v and retention %d, want %v and %d", caps.Regions, caps.RetentionDays, tc.regions, tc.retention)
			}
		})
	}
}

// An operator's declaration of a route is an overlay on its family: the fields
// it sets win, and every other field is resolved from the family when the route
// is looked up. The family's entry was copied into the route when it was
// declared or published, and kept: a longer family registered afterwards never
// reached the route, a family changed afterwards did not either, and a
// published conformance run lost the family's schema keywords, usage quality
// and token ceilings, so Negotiate on a family-covered route failed after it.
func TestADeclaredRouteIsAnOverlayOnItsFamily(t *testing.T) {
	family := func(regions []string, retention int) ProviderCapabilities {
		return ProviderCapabilities{
			Supports:              map[Capability]bool{CapNativeJSONSchema: true, CapToolCalling: true},
			SchemaKeywords:        []string{"enum", "pattern"},
			UsageReportingQuality: UsageExact,
			Regions:               regions,
			RetentionDays:         retention,
			ContextWindow:         128000,
			MaxOutputTokens:       16000,
		}
	}
	cases := []struct {
		name  string
		setup func()
		check func(t *testing.T, caps ProviderCapabilities)
	}{
		{"publishing over a family keeps its other fields", func() {
			RegisterCapabilityFamily("openai", "gpt-", family([]string{"eu"}, 3))
			publishObserved(ProviderCapabilities{Provider: "openai", Model: "gpt-x", Supports: map[Capability]bool{CapStreaming: true}})
		}, func(t *testing.T, caps ProviderCapabilities) {
			if err := Negotiate(caps, true, Requirement{
				Capabilities: []Capability{CapNativeJSONSchema, CapStreaming}, MinUsageQuality: UsageExact,
				SchemaKeywords: []string{"enum"}, MinContextWindow: 100000, MinMaxOutputTokens: 8000,
			}); err != nil {
				t.Errorf("Negotiate after a Publish: %v", err)
			}
			if !reflect.DeepEqual(caps.Regions, []string{"eu"}) || caps.RetentionDays != 3 {
				t.Errorf("placement %v / %d, want the family's", caps.Regions, caps.RetentionDays)
			}
		}},
		{"what the run observed false stays false beside the family's true", func() {
			RegisterCapabilityFamily("openai", "gpt-", family(nil, 0))
			publishObserved(ProviderCapabilities{Provider: "openai", Model: "gpt-x", Supports: map[Capability]bool{CapToolCalling: false}})
		}, func(t *testing.T, caps ProviderCapabilities) {
			if caps.Has(CapToolCalling) || !caps.Has(CapNativeJSONSchema) {
				t.Errorf("Supports = %v", caps.Supports)
			}
		}},
		{"what the run observed wins over the family's field", func() {
			RegisterCapabilityFamily("openai", "gpt-", family(nil, 0))
			publishObserved(ProviderCapabilities{Provider: "openai", Model: "gpt-x", Supports: map[Capability]bool{CapStreaming: true},
				ContextWindow: 1000, UsageReportingQuality: UsageEstimated, SchemaKeywords: []string{"format"}})
		}, func(t *testing.T, caps ProviderCapabilities) {
			if caps.ContextWindow != 1000 || caps.UsageReportingQuality != UsageEstimated || !reflect.DeepEqual(caps.SchemaKeywords, []string{"format"}) {
				t.Errorf("the observed fields did not win: %+v", caps)
			}
			if caps.MaxOutputTokens != 16000 {
				t.Errorf("an unobserved ceiling was not resolved from the family: %d", caps.MaxOutputTokens)
			}
		}},
		{"a family tightened after a Publish reaches the route", func() {
			RegisterCapabilityFamily("openai", "gpt-", family([]string{"eu", "us"}, 30))
			publishObserved(ProviderCapabilities{Provider: "openai", Model: "gpt-x", Supports: map[Capability]bool{CapStreaming: true}})
			RegisterCapabilityFamily("openai", "gpt-", family([]string{"eu"}, 0))
		}, func(t *testing.T, caps ProviderCapabilities) {
			if !reflect.DeepEqual(caps.Regions, []string{"eu"}) || caps.RetentionDays != 0 {
				t.Errorf("placement %v / %d, want the tightened family's", caps.Regions, caps.RetentionDays)
			}
		}},
		{"a family tightened after a declaration reaches the other fields", func() {
			RegisterCapabilityFamily("openai", "gpt-", family([]string{"eu"}, 0))
			DeclareRoute("openai", "gpt-x", []string{"eu"}, 0)
			RegisterCapabilityFamily("openai", "gpt-", ProviderCapabilities{Supports: map[Capability]bool{CapSeed: true}, ContextWindow: 5})
		}, func(t *testing.T, caps ProviderCapabilities) {
			if !caps.Has(CapSeed) || caps.Has(CapToolCalling) || caps.ContextWindow != 5 {
				t.Errorf("the route did not follow its family: %+v", caps)
			}
		}},
		{"a longer family registered after a declaration reaches the route", func() {
			RegisterCapabilityFamily("openai", "gpt-", ProviderCapabilities{Supports: map[Capability]bool{CapStreaming: true}})
			DeclareRoute("openai", "gpt-x", []string{"eu"}, 0)
			RegisterCapabilityFamily("openai", "gpt-x", ProviderCapabilities{Supports: map[Capability]bool{CapToolCalling: true}})
		}, func(t *testing.T, caps ProviderCapabilities) {
			if !caps.Has(CapToolCalling) || caps.Has(CapStreaming) {
				t.Errorf("the longer family did not reach the route: %v", caps.Supports)
			}
			if !reflect.DeepEqual(caps.Regions, []string{"eu"}) {
				t.Errorf("the declared region was lost: %v", caps.Regions)
			}
		}},
		{"a declared region wins over a tightened family's", func() {
			DeclareRoute("openai", "gpt-x", []string{"us"}, 5)
			RegisterCapabilityFamily("openai", "gpt-", family([]string{"eu"}, 0))
		}, func(t *testing.T, caps ProviderCapabilities) {
			if !reflect.DeepEqual(caps.Regions, []string{"us"}) || caps.RetentionDays != 5 {
				t.Errorf("placement %v / %d, want what was declared", caps.Regions, caps.RetentionDays)
			}
		}},
		{"a declared retention of zero wins over a family's thirty", func() {
			RegisterCapabilityFamily("openai", "gpt-", family([]string{"eu"}, 30))
			DeclareRoute("openai", "gpt-x", []string{"eu"}, 0)
		}, func(t *testing.T, caps ProviderCapabilities) {
			if caps.RetentionDays != 0 {
				t.Errorf("retention = %d, want the declared zero", caps.RetentionDays)
			}
		}},
		{"a declaration of no region wins over a family's regions", func() {
			RegisterCapabilityFamily("openai", "gpt-", family([]string{"eu"}, 0))
			DeclareRoute("openai", "gpt-x", nil, 0)
		}, func(t *testing.T, caps ProviderCapabilities) {
			if len(caps.Regions) != 0 {
				t.Errorf("Regions = %v, want none", caps.Regions)
			}
		}},
		{"a declaration over a registered route keeps its capabilities and ignores the family", func() {
			RegisterCapabilityFamily("openai", "gpt-", family([]string{"us"}, 30))
			RegisterCapabilities(ProviderCapabilities{Provider: "openai", Model: "gpt-x", Supports: map[Capability]bool{CapSeed: true}})
			DeclareRoute("openai", "gpt-x", []string{"eu"}, 1)
		}, func(t *testing.T, caps ProviderCapabilities) {
			if !caps.Has(CapSeed) || caps.Has(CapToolCalling) || caps.ContextWindow != 0 {
				t.Errorf("the family reached a route that was registered whole: %+v", caps)
			}
			if !reflect.DeepEqual(caps.Regions, []string{"eu"}) || caps.RetentionDays != 1 {
				t.Errorf("placement %v / %d", caps.Regions, caps.RetentionDays)
			}
		}},
		{"registering a route again replaces what was declared about it", func() {
			DeclareRoute("openai", "gpt-x", []string{"eu"}, 1)
			RegisterCapabilities(ProviderCapabilities{Provider: "openai", Model: "gpt-x", Supports: map[Capability]bool{CapSeed: true}})
		}, func(t *testing.T, caps ProviderCapabilities) {
			if len(caps.Regions) != 0 || caps.RetentionDays != 0 || !caps.Has(CapSeed) {
				t.Errorf("the earlier declaration survived: %+v", caps)
			}
		}},
		{"the observed regions win over the family's and over an earlier declaration's", func() {
			RegisterCapabilityFamily("openai", "gpt-", family([]string{"us"}, 30))
			DeclareRoute("openai", "gpt-x", []string{"ap"}, 2)
			publishObserved(ProviderCapabilities{Provider: "openai", Model: "gpt-x", Supports: map[Capability]bool{CapStreaming: true},
				Regions: []string{"eu"}, RetentionDays: 1})
		}, func(t *testing.T, caps ProviderCapabilities) {
			if !reflect.DeepEqual(caps.Regions, []string{"eu"}) || caps.RetentionDays != 1 {
				t.Errorf("placement %v / %d, want what the run observed", caps.Regions, caps.RetentionDays)
			}
		}},
		{"a registered route that is published keeps its placement", func() {
			RegisterCapabilities(ProviderCapabilities{Provider: "openai", Model: "gpt-x", Regions: []string{"eu"}, RetentionDays: 4})
			publishObserved(ProviderCapabilities{Provider: "openai", Model: "gpt-x", Supports: map[Capability]bool{CapStreaming: true}})
		}, func(t *testing.T, caps ProviderCapabilities) {
			if !reflect.DeepEqual(caps.Regions, []string{"eu"}) || caps.RetentionDays != 4 || !caps.Has(CapStreaming) {
				t.Errorf("%+v", caps)
			}
		}},
		{"a route stays stamped with the names it was declared under", func() {
			DeclareRoute("OpenAI", "GPT-X", []string{"eu"}, 0)
		}, func(t *testing.T, caps ProviderCapabilities) {
			if caps.Provider != "OpenAI" || caps.Model != "GPT-X" {
				t.Errorf("stamped %q/%q", caps.Provider, caps.Model)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ResetCapabilityRegistryForTest()
			t.Cleanup(ResetCapabilityRegistryForTest)
			tc.setup()
			caps, known := CapabilitiesFor("openai", "gpt-x")
			if !known {
				t.Fatal("the route is not known")
			}
			tc.check(t, caps)
		})
	}
}

// DeclareRoute keeps an empty list of regions empty, as every other
// declaration does, and a nil one nil: "declared, and nowhere" is not "not
// declared". It turned an empty list into nil.
func TestDeclareRouteKeepsAnEmptyListEmpty(t *testing.T) {
	cases := []struct {
		name    string
		regions []string
		nonNil  bool
	}{
		{"nil", nil, false},
		{"empty", []string{}, true},
		{"made empty", make([]string, 0, 4), true},
		{"sliced empty", []string{"eu"}[:0], true},
		{"one", []string{"eu"}, true},
		{"two", []string{"eu", "us"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ResetCapabilityRegistryForTest()
			t.Cleanup(ResetCapabilityRegistryForTest)
			DeclareRoute("openai", "gpt-x", tc.regions, 0)
			caps, known := CapabilitiesFor("openai", "gpt-x")
			if !known {
				t.Fatal("the route is not known")
			}
			if (caps.Regions != nil) != tc.nonNil {
				t.Errorf("Regions = %#v, non-nil should be %v", caps.Regions, tc.nonNil)
			}
			if len(caps.Regions) != len(tc.regions) {
				t.Errorf("Regions = %v, want %v", caps.Regions, tc.regions)
			}
			regions, _, _ := RoutePlacementFor("openai", "gpt-x")
			if (regions != nil) != tc.nonNil {
				t.Errorf("RoutePlacementFor regions = %#v, non-nil should be %v", regions, tc.nonNil)
			}
		})
	}
}

// RoutePlacementFor answers what CapabilitiesFor answers for the two fields a
// data policy reads, without the copies.
func TestRoutePlacementForResolvesAsCapabilitiesForDoes(t *testing.T) {
	cases := []struct {
		name      string
		setup     func()
		provider  string
		model     string
		known     bool
		regions   []string
		retention int
	}{
		{"nothing registered", func() {}, "openai", "gpt-x", false, nil, 0},
		{"an exact route", func() {
			RegisterCapabilities(ProviderCapabilities{Provider: "openai", Model: "gpt-x", Regions: []string{"eu"}, RetentionDays: 2})
		},
			"openai", "gpt-x", true, []string{"eu"}, 2},
		{"an exact route in other capitals", func() { DeclareRoute("OpenAI", "GPT-X", []string{"eu"}, 0) }, " openai ", "gpt-x", true, []string{"eu"}, 0},
		{"a declared route", func() { DeclareRoute("openai", "gpt-x", []string{"eu", "us"}, 30) }, "openai", "gpt-x", true, []string{"eu", "us"}, 30},
		{"another route is not known", func() { DeclareRoute("openai", "gpt-y", []string{"eu"}, 0) }, "openai", "gpt-x", false, nil, 0},
		{"another provider is not known", func() { DeclareRoute("azure", "gpt-x", []string{"eu"}, 0) }, "openai", "gpt-x", false, nil, 0},
		{"a family", func() {
			RegisterCapabilityFamily("openai", "gpt-", ProviderCapabilities{Regions: []string{"us"}, RetentionDays: 7})
		},
			"openai", "gpt-x", true, []string{"us"}, 7},
		{"a family with no placement is known and has none", func() { RegisterCapabilityFamily("openai", "gpt-", ProviderCapabilities{}) },
			"openai", "gpt-x", true, nil, 0},
		{"the longest family", func() {
			RegisterCapabilityFamily("openai", "gpt-", ProviderCapabilities{Regions: []string{"us"}})
			RegisterCapabilityFamily("openai", "gpt-x", ProviderCapabilities{Regions: []string{"eu"}})
		}, "openai", "gpt-x1", true, []string{"eu"}, 0},
		{"a declared route over a family, placement of its own", func() {
			RegisterCapabilityFamily("openai", "gpt-", ProviderCapabilities{Regions: []string{"us"}, RetentionDays: 7})
			DeclareRoute("openai", "gpt-x", []string{"eu"}, 0)
		}, "openai", "gpt-x", true, []string{"eu"}, 0},
		{"a published route over a family keeps the family's placement", func() {
			RegisterCapabilityFamily("openai", "gpt-", ProviderCapabilities{Regions: []string{"us"}, RetentionDays: 7})
			publishObserved(ProviderCapabilities{Provider: "openai", Model: "gpt-x", Supports: map[Capability]bool{CapStreaming: true}})
		}, "openai", "gpt-x", true, []string{"us"}, 7},
		{"a route registered whole ignores the family's placement", func() {
			RegisterCapabilityFamily("openai", "gpt-", ProviderCapabilities{Regions: []string{"us"}, RetentionDays: 7})
			RegisterCapabilities(ProviderCapabilities{Provider: "openai", Model: "gpt-x"})
		}, "openai", "gpt-x", true, nil, 0},
		{"a retention below zero is passed on, not corrected", func() { DeclareRoute("openai", "gpt-x", []string{"eu"}, -1) }, "openai", "gpt-x", true, []string{"eu"}, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ResetCapabilityRegistryForTest()
			t.Cleanup(ResetCapabilityRegistryForTest)
			tc.setup()

			regions, retention, known := RoutePlacementFor(tc.provider, tc.model)
			if known != tc.known || retention != tc.retention || !reflect.DeepEqual(regions, tc.regions) {
				t.Errorf("RoutePlacementFor = %v, %d, %v; want %v, %d, %v", regions, retention, known, tc.regions, tc.retention, tc.known)
			}
			caps, capsKnown := CapabilitiesFor(tc.provider, tc.model)
			if capsKnown != known || caps.RetentionDays != retention || !reflect.DeepEqual(caps.Regions, regions) {
				t.Errorf("CapabilitiesFor disagrees: %v, %d, %v", caps.Regions, caps.RetentionDays, capsKnown)
			}
		})
	}
}

// The registry hands out copies and keeps copies, on every way a route gets
// into it, including the ones that are resolved from a family when they are
// looked up.
func TestTheRegistryKeepsAndGivesCopiesOnEveryRoute(t *testing.T) {
	ways := []struct {
		name     string
		register func(caps ProviderCapabilities)
	}{
		{"an exact route", func(caps ProviderCapabilities) {
			caps.Provider, caps.Model = "openai", "gpt-x"
			RegisterCapabilities(caps)
		}},
		{"an exact route registered twice", func(caps ProviderCapabilities) {
			caps.Provider, caps.Model = "openai", "gpt-x"
			RegisterCapabilities(ProviderCapabilities{Provider: "openai", Model: "gpt-x"})
			RegisterCapabilities(caps)
		}},
		{"a family", func(caps ProviderCapabilities) { RegisterCapabilityFamily("openai", "gpt-", caps) }},
		{"a family registered twice", func(caps ProviderCapabilities) {
			RegisterCapabilityFamily("openai", "gpt-", ProviderCapabilities{})
			RegisterCapabilityFamily("openai", "gpt-", caps)
		}},
		{"a family for the whole provider", func(caps ProviderCapabilities) { RegisterCapabilityFamily("openai", "", caps) }},
		{"a family for every provider", func(caps ProviderCapabilities) { RegisterCapabilityFamily("", "gpt-", caps) }},
		{"a family, a route declared over it", func(caps ProviderCapabilities) {
			RegisterCapabilityFamily("openai", "gpt-", caps)
			DeclareRoute("openai", "gpt-x", []string{"eu"}, 0)
		}},
		{"a family, a route declared over it, the family tightened", func(caps ProviderCapabilities) {
			RegisterCapabilityFamily("openai", "gpt-", ProviderCapabilities{Regions: []string{"us"}})
			DeclareRoute("openai", "gpt-x", []string{"eu"}, 0)
			RegisterCapabilityFamily("openai", "gpt-", caps)
		}},
		{"a family, a run published over it", func(caps ProviderCapabilities) {
			RegisterCapabilityFamily("openai", "gpt-", caps)
			publishObserved(ProviderCapabilities{Provider: "openai", Model: "gpt-x", Supports: map[Capability]bool{CapStreaming: true}})
		}},
		{"a run published, and the family registered after", func(caps ProviderCapabilities) {
			publishObserved(ProviderCapabilities{Provider: "openai", Model: "gpt-x", Supports: map[Capability]bool{CapStreaming: true}})
			RegisterCapabilityFamily("openai", "gpt-", caps)
		}},
	}
	for _, way := range ways {
		t.Run(way.name, func(t *testing.T) {
			ResetCapabilityRegistryForTest()
			t.Cleanup(ResetCapabilityRegistryForTest)

			given := ProviderCapabilities{
				Supports:       map[Capability]bool{CapToolCalling: true},
				SchemaKeywords: []string{"pattern"},
				Regions:        []string{"eu"},
			}
			way.register(given)
			given.Supports[CapToolCalling] = false
			given.SchemaKeywords[0] = "changed"
			given.Regions[0] = "us"

			first, known := CapabilitiesFor("openai", "gpt-x")
			if !known {
				t.Fatal("the route is not known")
			}
			if !first.Supports[CapToolCalling] || first.SchemaKeywords[0] != "pattern" || (len(first.Regions) > 0 && first.Regions[0] != "eu") {
				t.Fatalf("changing what was registered changed the declaration: %+v", first)
			}
			first.Supports[CapToolCalling] = false
			first.SchemaKeywords[0] = "changed"
			if len(first.Regions) > 0 {
				first.Regions[0] = "us"
			}
			second, _ := CapabilitiesFor("openai", "gpt-x")
			if !second.Supports[CapToolCalling] || second.SchemaKeywords[0] != "pattern" || (len(second.Regions) > 0 && second.Regions[0] != "eu") {
				t.Errorf("changing what was looked up changed the declaration: %+v", second)
			}
		})
	}
}

// ModelSentFor names the model the Anthropic provider sends, so a data policy
// can check the route that is used and not the one the request names. The
// provider replaced an empty model and any "gpt" one with its default, and the
// policy was left checking "anthropic/<the gpt name>".
func TestTheAnthropicProviderSaysWhichModelItSends(t *testing.T) {
	provider := newTestAnthropicProvider(t, "http://127.0.0.1:1")
	cases := []struct {
		requested string
		sent      string
	}{
		{"", "claude-3-5-sonnet-20240620"},
		{"gpt-4o", "claude-3-5-sonnet-20240620"},
		{"gpt-6-luna", "claude-3-5-sonnet-20240620"},
		{"gpt", "claude-3-5-sonnet-20240620"},
		{"gpt-anything", "claude-3-5-sonnet-20240620"},
		{"claude-3-5-haiku-20241022", "claude-3-5-haiku-20241022"},
		{"claude-sonnet-4-5", "claude-sonnet-4-5"},
		{"claude-3-5-sonnet-20240620", "claude-3-5-sonnet-20240620"},
		{"gemini-1.5-pro", "gemini-1.5-pro"},
		{"o3-mini", "o3-mini"},
		{"mygpt-model", "mygpt-model"},
		// The name is read as it is spelt: case and surrounding space do not make
		// a gpt model one that Anthropic can serve.
		{"GPT-4o", "claude-3-5-sonnet-20240620"},
		{" gpt-4o", "claude-3-5-sonnet-20240620"},
	}
	for _, tc := range cases {
		if got := provider.ModelSentFor(tc.requested); got != tc.sent {
			t.Errorf("ModelSentFor(%q) = %q, want %q", tc.requested, got, tc.sent)
		}
		if got := ModelSentBy(provider, tc.requested); got != tc.sent {
			t.Errorf("ModelSentBy(%q) = %q, want %q", tc.requested, got, tc.sent)
		}
	}
}

// An empty name and a gpt name are sent as the default, however they are spelt:
// the test was case-sensitive and did not trim, so " GPT-4o" went to Anthropic as
// itself and was answered with a 404.
func TestAnthropicModelSentForReadsTheNameAsItIsSpelt(t *testing.T) {
	provider := newTestAnthropicProvider(t, "http://example.invalid")
	cases := []struct {
		requested string
		want      string
	}{
		{"", anthropicDefaultModel},
		{"   ", anthropicDefaultModel},
		{"\t\n", anthropicDefaultModel},
		{"gpt-4o", anthropicDefaultModel},
		{"GPT-4o", anthropicDefaultModel},
		{" gpt-6-luna ", anthropicDefaultModel},
		{"Gpt-5", anthropicDefaultModel},
		{"claude-sonnet-4-5", "claude-sonnet-4-5"},
		{" claude-sonnet-4-5 ", "claude-sonnet-4-5"},
		{"Claude-Sonnet-4-5", "Claude-Sonnet-4-5"},
		{"other-model", "other-model"},
		{"my-gpt-model", "my-gpt-model"},
	}
	for _, tc := range cases {
		if got := provider.ModelSentFor(tc.requested); got != tc.want {
			t.Errorf("ModelSentFor(%q) = %q, want %q", tc.requested, got, tc.want)
		}
	}
}

// What ModelSentFor says is what Complete sends: the two cannot drift apart.
func TestTheAnthropicProviderSendsWhatItSaysItSends(t *testing.T) {
	for _, requested := range []string{"", "gpt-4o", "gpt-6-luna", "claude-3-5-haiku-20241022", "claude-sonnet-4-5", "other-model"} {
		t.Run("requested "+requested, func(t *testing.T) {
			var sent string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model string `json:"model"`
				}
				raw, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(raw, &body)
				sent = body.Model
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"stop_reason": "end_turn", "content":[{"type":"text","text":"ok"}],"model":"` + body.Model + `"}`))
			}))
			defer server.Close()

			provider := newTestAnthropicProvider(t, server.URL)
			if _, err := provider.Complete(context.Background(), CompletionRequest{Model: requested, UserPrompt: "x"}); err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if want := provider.ModelSentFor(requested); sent != want {
				t.Errorf("the request carried %q, ModelSentFor says %q", sent, want)
			}
		})
	}
}

// A provider that sends what it is asked for says so by saying nothing.
func TestModelSentByLeavesAProviderThatSendsWhatItIsAskedForAlone(t *testing.T) {
	local, _ := NewLocalProvider(ProviderConfig{})
	for _, requested := range []string{"", "gpt-4o", "claude-x", "GPT-4o", " m "} {
		if got := ModelSentBy(local, requested); got != requested {
			t.Errorf("ModelSentBy(local, %q) = %q", requested, got)
		}
	}
	if got := ModelSentBy(nil, "gpt-4o"); got != "gpt-4o" {
		t.Errorf("ModelSentBy(nil, ...) = %q", got)
	}
}

// A gateway that says which provider served the request is read. The decode
// never looked at the field, so for the repo's own router the answer-route check
// could not see where the request went.
func TestAChatCompletionSaysWhichProviderServedIt(t *testing.T) {
	const choices = `"choices":[{"finish_reason":"stop","message":{"content":"{}"}}]`
	cases := []struct {
		name     string
		body     string
		upstream string
	}{
		{"a provider named", `{"model":"openai/gpt-4o","provider":"OpenAI",` + choices + `}`, "OpenAI"},
		{"a provider named with spaces", `{"model":"m","provider":"  Together  ",` + choices + `}`, "Together"},
		{"a provider with a space in its name", `{"model":"m","provider":"Google AI Studio",` + choices + `}`, "Google AI Studio"},
		{"no provider", `{"model":"m",` + choices + `}`, ""},
		{"a null provider", `{"model":"m","provider":null,` + choices + `}`, ""},
		{"an empty provider", `{"model":"m","provider":"",` + choices + `}`, ""},
		{"a blank provider", `{"model":"m","provider":"   ",` + choices + `}`, ""},
		{"a provider that is an object", `{"model":"m","provider":{"name":"OpenAI"},` + choices + `}`, ""},
		{"a provider that is a number", `{"model":"m","provider":7,` + choices + `}`, ""},
		{"a provider that is a list", `{"model":"m","provider":["a","b"],` + choices + `}`, ""},
		{"a provider that is a boolean", `{"model":"m","provider":true,` + choices + `}`, ""},
		{"the provider after the choices", `{"model":"m",` + choices + `,"provider":"Fireworks"}`, "Fireworks"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			provider, err := NewOpenRouterProvider(ProviderConfig{APIKey: "k", BaseURL: server.URL})
			if err != nil {
				t.Fatalf("NewOpenRouterProvider: %v", err)
			}
			resp, err := provider.Complete(context.Background(), CompletionRequest{Model: "openai/gpt-4o", UserPrompt: "x"})
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if resp.UpstreamProvider != tc.upstream {
				t.Errorf("UpstreamProvider = %q, want %q", resp.UpstreamProvider, tc.upstream)
			}
			// Provider stays the provider the library handed the request to:
			// pricing, call records and the answered route are keyed by it.
			if resp.Provider != "openrouter" {
				t.Errorf("Provider = %q, want the router's own name", resp.Provider)
			}
		})
	}
}

// A provider the library does not know, behind the same decode, is read the
// same way.
func TestAnotherChatDialectProviderSaysWhichProviderServedIt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"model":"m","provider":"Cerebras","choices":[{"finish_reason":"stop","message":{"content":"{}"}}]}`))
	}))
	defer server.Close()
	provider, err := NewDeepSeekProvider(ProviderConfig{APIKey: "k", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}
	resp, err := provider.Complete(context.Background(), CompletionRequest{Model: "m", UserPrompt: "x"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.UpstreamProvider != "Cerebras" || resp.Provider != provider.Name() {
		t.Errorf("answered by %q behind %q", resp.Provider, resp.UpstreamProvider)
	}
}

// LocalProvider.Complete: what it answers, under what name, with what usage.
func TestLocalProviderComplete(t *testing.T) {
	boom := errors.New("the handler failed")
	cases := []struct {
		name         string
		handler      func(context.Context, CompletionRequest) (string, error)
		req          CompletionRequest
		want         string // exact content, when set
		wantContains string
		wantErr      error
		wantModel    string
	}{
		{"a handler answers", func(context.Context, CompletionRequest) (string, error) { return "the answer", nil },
			CompletionRequest{Model: "m", UserPrompt: "q"}, "the answer", "", nil, "m"},
		{"a handler that fails", func(context.Context, CompletionRequest) (string, error) { return "", boom },
			CompletionRequest{Model: "m", UserPrompt: "q"}, "", "", boom, ""},
		{"a handler that fails after producing text", func(context.Context, CompletionRequest) (string, error) { return "partial", boom },
			CompletionRequest{Model: "m", UserPrompt: "q"}, "", "", boom, ""},
		{"a handler that answers nothing", func(context.Context, CompletionRequest) (string, error) { return "", nil },
			CompletionRequest{Model: "m", UserPrompt: "q"}, "", "", nil, "m"},
		{"no handler, no model", nil, CompletionRequest{UserPrompt: "hello"}, "", unscriptedAnswerText, nil, "local-model"},
		{"no handler, a model", nil, CompletionRequest{Model: "gpt-6-luna", UserPrompt: "hello"}, "", unscriptedAnswerText, nil, "gpt-6-luna"},
		{"no handler, an extraction prompt is not answered with an extraction", nil, CompletionRequest{UserPrompt: "please extract the fields"}, "", unscriptedAnswerText, nil, "local-model"},
		{"no handler, an extraction as JSON is not answered with a person", nil, CompletionRequest{UserPrompt: "extract", ResponseFormat: "json"}, "", `"unscripted_local_provider_answer"`, nil, "local-model"},
		{"no handler, a validation prompt is not answered valid", nil, CompletionRequest{UserPrompt: "validate this"}, "", unscriptedAnswerText, nil, "local-model"},
		{"no handler, a transformation prompt is not answered transformed", nil, CompletionRequest{UserPrompt: "transform this"}, "", unscriptedAnswerText, nil, "local-model"},
		{"no handler, JSON asked for", nil, CompletionRequest{UserPrompt: "anything", ResponseFormat: "json"}, "", `"unscripted_local_provider_answer"`, nil, "local-model"},
		{"no handler, a schema", nil, CompletionRequest{UserPrompt: "x", ResponseFormat: "json", SchemaName: "s",
			JSONSchema: map[string]any{"type": "object", "properties": map[string]any{"n": map[string]any{"type": "integer"}}, "required": []any{"n"}}}, "", `"n"`, nil, "local-model"},
		{"a model name with a date is echoed as asked", func(context.Context, CompletionRequest) (string, error) { return "x", nil },
			CompletionRequest{Model: "a-model-2026-01-01", UserPrompt: "q"}, "x", "", nil, "a-model-2026-01-01"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider, err := NewLocalProvider(ProviderConfig{})
			if err != nil {
				t.Fatalf("NewLocalProvider: %v", err)
			}
			if tc.handler != nil {
				provider.WithHandler(tc.handler)
			}
			resp, err := provider.Complete(context.Background(), tc.req)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				// An error comes with an empty result.
				if resp.Content != "" || resp.Model != "" || resp.Provider != "" || resp.Usage.TotalTokens != 0 {
					t.Errorf("a response came back beside the error: %+v", resp)
				}
				return
			}
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if tc.handler != nil && resp.Content != tc.want {
				t.Errorf("Content = %q, want %q", resp.Content, tc.want)
			}
			if tc.wantContains != "" && !strings.Contains(resp.Content, tc.wantContains) {
				t.Errorf("Content = %q, want it to contain %q", resp.Content, tc.wantContains)
			}
			if resp.Model != tc.wantModel {
				t.Errorf("Model = %q, want %q", resp.Model, tc.wantModel)
			}
			if resp.Provider != "local" || resp.FinishReason != "stop" {
				t.Errorf("Provider %q, FinishReason %q", resp.Provider, resp.FinishReason)
			}
			if tc.req.Model != "" && !SameModelRoute(tc.req.Model, resp.Model) {
				t.Errorf("the answer to %q names %q, which is not the route asked", tc.req.Model, resp.Model)
			}
		})
	}
}

// The local provider hands its handler the request it was given, and reports
// usage from the sizes of what went in and came out.
func TestLocalProviderPassesTheRequestAndReportsUsage(t *testing.T) {
	provider, _ := NewLocalProvider(ProviderConfig{})
	var seen CompletionRequest
	type key struct{}
	provider.WithHandler(func(ctx context.Context, req CompletionRequest) (string, error) {
		seen = req
		if ctx.Value(key{}) != "carried" {
			t.Error("the handler did not get the caller's context")
		}
		return strings.Repeat("a", 40), nil
	})
	ctx := context.WithValue(context.Background(), key{}, "carried")
	resp, err := provider.Complete(ctx, CompletionRequest{Model: "m", SystemPrompt: strings.Repeat("s", 8), UserPrompt: strings.Repeat("u", 12)})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if seen.Model != "m" || len(seen.SystemPrompt) != 8 || len(seen.UserPrompt) != 12 {
		t.Errorf("the handler saw %+v", seen)
	}
	if resp.Usage.PromptTokens != 5 || resp.Usage.CompletionTokens != 10 || resp.Usage.TotalTokens != 15 {
		t.Errorf("usage = %+v, want 5, 10 and 15", resp.Usage)
	}
}
