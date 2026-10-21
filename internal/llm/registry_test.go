package llm

import (
	"reflect"
	"strconv"
	"sync"
	"testing"
)

// Review round 9. The registry is read on every call a data policy governs,
// and the error for an undeclared route says to declare it, which a caller
// does while calls are running. Without a lock that is a map read during a map
// write, and Go ends the process for it.
func TestTheRouteRegistryIsReadAndWrittenAtOnce(t *testing.T) {
	ResetCapabilityRegistryForTest()
	t.Cleanup(ResetCapabilityRegistryForTest)

	const writers, readers, rounds = 4, 8, 400
	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range rounds {
				model := "model-" + strconv.Itoa((w*rounds+i)%50)
				switch i % 3 {
				case 0:
					DeclareRoute("openai", model, []string{"eu"}, 0)
				case 1:
					RegisterCapabilities(ProviderCapabilities{Provider: "openai", Model: model, Regions: []string{"eu"}})
				default:
					RegisterCapabilityFamily("openai", "model-", ProviderCapabilities{})
				}
			}
		}()
	}
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range rounds * 4 {
				caps, known := CapabilitiesFor("openai", "model-"+strconv.Itoa(i%50))
				if known && caps.Provider != "openai" {
					t.Errorf("a lookup came back for provider %q", caps.Provider)
					return
				}
			}
		}()
	}
	wg.Wait()

	// Every route that was declared is still declared.
	for i := range 50 {
		if _, known := CapabilitiesFor("openai", "model-"+strconv.Itoa(i)); !known {
			t.Fatalf("model-%d is not known after being declared", i)
		}
	}
}

// Publishing a conformance report keeps what the route had declared about
// where it runs. A report observes capabilities and knows nothing of regions,
// and it replaced the route's entry whole, so a route a client's data policy
// allowed stopped being allowed after a conformance run.
func TestPublishingAConformanceReportKeepsTheRoutesRegions(t *testing.T) {
	report := func() *ConformanceReport {
		return &ConformanceReport{
			ProviderName: "openai",
			Model:        "gpt-6-luna",
			checks:       []ConformanceCheck{{ID: "tools", Category: CategoryModel, Capability: CapToolCalling}},
			Results:      map[string]ConformanceOutcome{"tools": {Passed: true, Supported: true}},
		}
	}

	cases := []struct {
		name      string
		before    func()
		regions   []string
		retention int
	}{
		{"nothing declared before", func() {}, nil, 0},
		{"a region declared before", func() { DeclareRoute("openai", "gpt-6-luna", []string{"eu"}, 0) }, []string{"eu"}, 0},
		{"two regions and a retention", func() { DeclareRoute("openai", "gpt-6-luna", []string{"eu", "us"}, 30) }, []string{"eu", "us"}, 30},
		{"another model's declaration is not taken", func() { DeclareRoute("openai", "gpt-6-sol", []string{"eu"}, 30) }, nil, 0},
		{"another provider's declaration is not taken", func() { DeclareRoute("azure", "gpt-6-luna", []string{"eu"}, 30) }, nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ResetCapabilityRegistryForTest()
			t.Cleanup(ResetCapabilityRegistryForTest)
			tc.before()

			if !report().Publish() {
				t.Fatal("the report was not published")
			}
			caps, known := CapabilitiesFor("openai", "gpt-6-luna")
			if !known || !caps.Supports[CapToolCalling] {
				t.Fatalf("known = %v, caps = %+v; the observed capability was not published", known, caps)
			}
			if len(caps.Regions) != len(tc.regions) || (len(tc.regions) > 0 && !reflect.DeepEqual(caps.Regions, tc.regions)) {
				t.Errorf("Regions = %v, want %v", caps.Regions, tc.regions)
			}
			if caps.RetentionDays != tc.retention {
				t.Errorf("RetentionDays = %d, want %d", caps.RetentionDays, tc.retention)
			}
		})
	}
}

// A route declared whole stays whole when a report is published for it: what the
// run observed is merged into the declaration and everything else it declared is
// kept. Publishing turned it into an overlay that carried only the observed
// capabilities, so its context window and usage reporting resolved from a family
// that did not exist and became zero, and a requirement the route was declared to
// meet was refused after the run.
func TestPublishingAConformanceReportKeepsAWholeDeclaration(t *testing.T) {
	ResetCapabilityRegistryForTest()
	t.Cleanup(ResetCapabilityRegistryForTest)

	RegisterCapabilities(ProviderCapabilities{
		Provider: "acme", Model: "m1",
		Supports:              map[Capability]bool{CapStreaming: true, CapNativeJSONSchema: true},
		SchemaKeywords:        []string{"enum", "required"},
		UsageReportingQuality: UsageExact,
		Regions:               []string{"eu"},
		RetentionDays:         7,
		ContextWindow:         128000,
		MaxOutputTokens:       4096,
	})
	requirement := Requirement{
		Capabilities:    []Capability{CapStreaming, CapNativeJSONSchema},
		MinUsageQuality: UsageExact, MinContextWindow: 100000, MinMaxOutputTokens: 4000,
		SchemaKeywords: []string{"enum"},
	}
	before, _ := CapabilitiesFor("acme", "m1")
	if err := Negotiate(before, true, requirement); err != nil {
		t.Fatalf("the declaration does not meet the requirement before publishing: %v", err)
	}

	report := &ConformanceReport{
		ProviderName: "acme", Model: "m1",
		checks: []ConformanceCheck{
			{ID: "tools", Category: CategoryModel, Capability: CapToolCalling},
			{ID: "schema", Category: CategoryModel, Capability: CapNativeJSONSchema},
		},
		Results: map[string]ConformanceOutcome{
			"tools":  {Passed: true, Supported: true},
			"schema": {Passed: true, Supported: false},
		},
	}
	if !report.Publish() {
		t.Fatal("the report was not published")
	}

	after, known := CapabilitiesFor("acme", "m1")
	if !known {
		t.Fatal("the route is unknown after publishing")
	}
	if !after.Supports[CapToolCalling] {
		t.Error("the observed capability was not published")
	}
	if after.Supports[CapNativeJSONSchema] {
		t.Error("a capability the run found unsupported is still declared supported")
	}
	if !after.Supports[CapStreaming] {
		t.Error("a capability the run did not observe was dropped")
	}
	if after.ContextWindow != 128000 || after.MaxOutputTokens != 4096 || after.UsageReportingQuality != UsageExact ||
		!reflect.DeepEqual(after.SchemaKeywords, []string{"enum", "required"}) ||
		!reflect.DeepEqual(after.Regions, []string{"eu"}) || after.RetentionDays != 7 {
		t.Errorf("what the route declared was not kept: %+v", after)
	}
	regions, retention, ok := RoutePlacementFor("acme", "m1")
	if !ok || !reflect.DeepEqual(regions, []string{"eu"}) || retention != 7 {
		t.Errorf("placement = %v, %d, %v", regions, retention, ok)
	}
}

// Publishing twice merges twice, and a route with no declaration of its own is
// still published as an overlay on its family.
func TestPublishingReportsInSuccessionMergesThem(t *testing.T) {
	ResetCapabilityRegistryForTest()
	t.Cleanup(ResetCapabilityRegistryForTest)
	RegisterCapabilities(ProviderCapabilities{Provider: "acme", Model: "m2", Supports: map[Capability]bool{CapSeed: true}, ContextWindow: 8000})

	publish := func(capability Capability, supported bool) {
		report := &ConformanceReport{
			ProviderName: "acme", Model: "m2",
			checks:  []ConformanceCheck{{ID: "c", Category: CategoryModel, Capability: capability}},
			Results: map[string]ConformanceOutcome{"c": {Passed: true, Supported: supported}},
		}
		if !report.Publish() {
			t.Fatal("not published")
		}
	}
	publish(CapStreaming, true)
	publish(CapToolCalling, false)

	caps, _ := CapabilitiesFor("acme", "m2")
	if !caps.Supports[CapSeed] || !caps.Supports[CapStreaming] || !caps.HasDeclared(CapToolCalling) || caps.Supports[CapToolCalling] {
		t.Errorf("Supports = %v", caps.Supports)
	}
	if caps.ContextWindow != 8000 {
		t.Errorf("ContextWindow = %d, want 8000", caps.ContextWindow)
	}

	RegisterCapabilityFamily("acme", "fam-", ProviderCapabilities{ContextWindow: 32000, Supports: map[Capability]bool{CapJSONMode: true}})
	report := &ConformanceReport{
		ProviderName: "acme", Model: "fam-1",
		checks:  []ConformanceCheck{{ID: "c", Category: CategoryModel, Capability: CapStreaming}},
		Results: map[string]ConformanceOutcome{"c": {Passed: true, Supported: true}},
	}
	report.Publish()
	overlay, _ := CapabilitiesFor("acme", "fam-1")
	if overlay.ContextWindow != 32000 || !overlay.Supports[CapJSONMode] || !overlay.Supports[CapStreaming] {
		t.Errorf("a route with only a family is not an overlay on it: %+v", overlay)
	}
}

// What the registry holds is its own copy. It kept the caller's slices and map
// as given, so changing them after registering changed the declaration: a
// region list edited later moved a route out from under a data policy that had
// allowed it, or into one that had not.
func TestRegisterCapabilitiesKeepsItsOwnCopy(t *testing.T) {
	ResetCapabilityRegistryForTest()
	t.Cleanup(ResetCapabilityRegistryForTest)

	regions := []string{"eu"}
	keywords := []string{"enum"}
	supports := map[Capability]bool{CapToolCalling: true}
	RegisterCapabilities(ProviderCapabilities{Provider: "openai", Model: "m", Regions: regions, SchemaKeywords: keywords, Supports: supports})

	regions[0] = "us"
	keywords[0] = "pattern"
	supports[CapToolCalling] = false
	supports[CapStreaming] = true

	caps, known := CapabilitiesFor("openai", "m")
	if !known {
		t.Fatal("the route is not known")
	}
	if caps.Regions[0] != "eu" || caps.SchemaKeywords[0] != "enum" || !caps.Supports[CapToolCalling] || caps.Supports[CapStreaming] {
		t.Errorf("the declaration followed the caller's values: %+v", caps)
	}

	// Nil stays nil: a route that declared no region has not declared an
	// empty list of them.
	RegisterCapabilities(ProviderCapabilities{Provider: "openai", Model: "bare"})
	if bare, _ := CapabilitiesFor("openai", "bare"); bare.Regions != nil || bare.Supports != nil || bare.SchemaKeywords != nil {
		t.Errorf("a bare declaration grew fields: %+v", bare)
	}
}

// A declaration made while a report is being published is not lost. Publish
// read the route, and wrote it back, under two separate locks, so a region
// declared in between was overwritten with the entry from before it.
func TestPublishingDoesNotLoseADeclarationMadeMeanwhile(t *testing.T) {
	for round := range 200 {
		ResetCapabilityRegistryForTest()
		report := &ConformanceReport{
			ProviderName: "openai",
			Model:        "gpt-6-luna",
			checks:       []ConformanceCheck{{ID: "tools", Category: CategoryModel, Capability: CapToolCalling}},
			Results:      map[string]ConformanceOutcome{"tools": {Passed: true, Supported: true}},
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); report.Publish() }()
		go func() { defer wg.Done(); DeclareRoute("openai", "gpt-6-luna", []string{"eu"}, 7) }()
		wg.Wait()

		caps, known := CapabilitiesFor("openai", "gpt-6-luna")
		if !known || len(caps.Regions) != 1 || caps.Regions[0] != "eu" || caps.RetentionDays != 7 {
			t.Fatalf("round %d: the declaration was lost: %+v", round, caps)
		}
	}
	ResetCapabilityRegistryForTest()
}
