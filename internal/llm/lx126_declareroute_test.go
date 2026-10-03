package llm

import (
	"reflect"
	"testing"
)

// LX-126. DeclareRoute says where a route runs and what it keeps, and leaves
// whatever else is known about the route as it was.
func TestDeclareRoute(t *testing.T) {
	type declared struct {
		provider, model string
		regions         []string
		retention       int
	}
	cases := []struct {
		name     string
		before   func()
		declare  []declared
		provider string
		model    string
		known    bool
		regions  []string
		keeps    int
		tools    bool // whether the route is still known to support tools
	}{
		{"nothing declared", nil, nil, "openai", "gpt-6-luna", false, nil, 0, false},
		{"one route", nil, []declared{{"openai", "gpt-6-luna", []string{"eu"}, 0}}, "openai", "gpt-6-luna", true, []string{"eu"}, 0, false},
		{"another model of the same provider is not declared", nil, []declared{{"openai", "gpt-6-luna", []string{"eu"}, 0}}, "openai", "gpt-6-sol", false, nil, 0, false},
		{"another provider's model of the same name is not declared", nil, []declared{{"openai", "gpt-6-luna", []string{"eu"}, 0}}, "azure", "gpt-6-luna", false, nil, 0, false},
		{"two regions", nil, []declared{{"openai", "gpt-6-luna", []string{"eu", "us"}, 0}}, "openai", "gpt-6-luna", true, []string{"eu", "us"}, 0, false},
		{"retention", nil, []declared{{"openai", "gpt-6-luna", []string{"eu"}, 30}}, "openai", "gpt-6-luna", true, []string{"eu"}, 30, false},
		{"declared twice, the later one stands", nil, []declared{
			{"openai", "gpt-6-luna", []string{"us"}, 30},
			{"openai", "gpt-6-luna", []string{"eu"}, 0},
		}, "openai", "gpt-6-luna", true, []string{"eu"}, 0, false},
		{"no region at all", nil, []declared{{"openai", "gpt-6-luna", nil, 0}}, "openai", "gpt-6-luna", true, nil, 0, false},
		{"looked up in other capitals", nil, []declared{{"OpenAI", "GPT-6-Luna", []string{"eu"}, 0}}, "openai", "gpt-6-luna", true, []string{"eu"}, 0, false},
		{"what was registered before is kept", func() {
			RegisterCapabilities(ProviderCapabilities{Provider: "openai", Model: "gpt-6-luna", Supports: map[Capability]bool{CapToolCalling: true}})
		}, []declared{{"openai", "gpt-6-luna", []string{"eu"}, 0}}, "openai", "gpt-6-luna", true, []string{"eu"}, 0, true},
		{"what the family declared is kept", func() {
			RegisterCapabilityFamily("openai", "gpt-6", ProviderCapabilities{Supports: map[Capability]bool{CapToolCalling: true}})
		}, []declared{{"openai", "gpt-6-luna", []string{"eu"}, 0}}, "openai", "gpt-6-luna", true, []string{"eu"}, 0, true},
		{"declaring one model does not declare its family", func() {
			RegisterCapabilityFamily("openai", "gpt-6", ProviderCapabilities{Supports: map[Capability]bool{CapToolCalling: true}})
		}, []declared{{"openai", "gpt-6-luna", []string{"eu"}, 0}}, "openai", "gpt-6-sol", true, nil, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ResetCapabilityRegistryForTest()
			t.Cleanup(ResetCapabilityRegistryForTest)
			if tc.before != nil {
				tc.before()
			}
			for _, d := range tc.declare {
				DeclareRoute(d.provider, d.model, d.regions, d.retention)
			}

			caps, known := CapabilitiesFor(tc.provider, tc.model)
			if known != tc.known {
				t.Fatalf("known = %v, want %v", known, tc.known)
			}
			if !known {
				return
			}
			if len(caps.Regions) != len(tc.regions) || (len(tc.regions) > 0 && !reflect.DeepEqual(caps.Regions, tc.regions)) {
				t.Errorf("Regions = %v, want %v", caps.Regions, tc.regions)
			}
			if caps.RetentionDays != tc.keeps {
				t.Errorf("RetentionDays = %d, want %d", caps.RetentionDays, tc.keeps)
			}
			if caps.Supports[CapToolCalling] != tc.tools {
				t.Errorf("tools supported = %v, want %v", caps.Supports[CapToolCalling], tc.tools)
			}
		})
	}

	// The regions are copied: changing the caller's slice afterwards does not
	// change what was declared.
	ResetCapabilityRegistryForTest()
	t.Cleanup(ResetCapabilityRegistryForTest)
	regions := []string{"eu"}
	DeclareRoute("openai", "gpt-6-luna", regions, 0)
	regions[0] = "us"
	if caps, _ := CapabilitiesFor("openai", "gpt-6-luna"); caps.Regions[0] != "eu" {
		t.Errorf("the declared region followed the caller's slice: %v", caps.Regions)
	}
}
