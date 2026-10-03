package llm

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// Review round 11.

func TestWithoutVersionTag(t *testing.T) {
	cases := []struct{ name, want string }{
		{"gpt-6-luna", "gpt-6-luna"},
		{"gpt-6-luna-2026-09-15", "gpt-6-luna"},
		{"gpt-6-luna-20260915", "gpt-6-luna"},
		{"claude-sonnet-4-5-20250929", "claude-sonnet-4-5"},
		{"gemini-2.0-flash-001", "gemini-2.0-flash"},
		{"gpt-4-0613", "gpt-4"},
		{"gpt-4-32k", "gpt-4-32k"},
		{"gpt-6-luna-mini", "gpt-6-luna-mini"},
		{"claude-3-5", "claude-3-5"},
		{"o1", "o1"},
		{"o1-2024-12-17", "o1"},
		{"model@20250101", "model"},
		{"model:20250101", "model"},
		{"model:latest", "model:latest"},
		{"20250929", "20250929"},
		{"-20250929", "-20250929"},
		{"", ""},
		{"gpt-4o-2024-08-06", "gpt-4o"},
		{"a-12", "a-12"},
		{"a-123", "a"},
	}
	for _, tc := range cases {
		if got := WithoutVersionTag(tc.name); got != tc.want {
			t.Errorf("WithoutVersionTag(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestSameModelRoute(t *testing.T) {
	cases := []struct {
		asked, answered string
		same            bool
	}{
		{"gpt-6-luna", "gpt-6-luna", true},
		{"gpt-6-luna", "GPT-6-Luna", true},
		{"gpt-6-luna", "  gpt-6-luna ", true},
		{"gpt-6-luna", "gpt-6-luna-2026-09-15", true},
		{"gpt-6-luna", "gpt-6-luna-20260915", true},
		{"gpt-6-luna", "gpt-6-luna-001", true},
		{"claude-sonnet-4-5", "claude-sonnet-4-5-20250929", true},
		{"gpt-6-luna-2026-09-15", "gpt-6-luna-2026-09-15", true},
		{"gpt-6-luna", "gpt-6-luna-mini", false},
		{"gpt-6-luna", "gpt-6-terra", false},
		{"gpt-6-luna", "gpt-6", false},
		{"gpt-6-luna", "", false},
		{"", "gpt-6-luna", false},
		// Asked for a snapshot, answered under its alias: the same model
		// (review round 12 found this refused after the call was paid for).
		{"gpt-6-luna-2026-09-15", "gpt-6-luna", true},
		{"gpt-4", "gpt-4-32k", false},
		{"gpt-6-luna", "gpt-6-luna-2026-09-15-preview", false},
	}
	for _, tc := range cases {
		if got := SameModelRoute(tc.asked, tc.answered); got != tc.same {
			t.Errorf("SameModelRoute(%q, %q) = %v, want %v", tc.asked, tc.answered, got, tc.same)
		}
	}
}

// Publishing what a conformance run observed keeps where the route runs and
// what it keeps, wherever that was declared. It looked only at the route's own
// entry: a route covered by its family's declaration came out of a Publish
// with an entry of its own and no regions, and was refused by every policy.
func TestPublishingKeepsWhatTheFamilyDeclared(t *testing.T) {
	cases := []struct {
		name          string
		declare       func()
		observed      ProviderCapabilities
		wantRegions   []string
		wantRetention int
	}{
		{"nothing declared", func() {}, ProviderCapabilities{}, nil, 0},
		{"the route's own declaration", func() { DeclareRoute("openai", "gpt-x", []string{"eu"}, 7) },
			ProviderCapabilities{}, []string{"eu"}, 7},
		{"the family's declaration", func() {
			RegisterCapabilityFamily("openai", "gpt-", ProviderCapabilities{Regions: []string{"eu"}, RetentionDays: 3})
		}, ProviderCapabilities{}, []string{"eu"}, 3},
		{"a family for every model of the provider", func() {
			RegisterCapabilityFamily("openai", "", ProviderCapabilities{Regions: []string{"eu", "us"}})
		}, ProviderCapabilities{}, []string{"eu", "us"}, 0},
		{"the route's own over the family's", func() {
			RegisterCapabilityFamily("openai", "gpt-", ProviderCapabilities{Regions: []string{"us"}, RetentionDays: 30})
			DeclareRoute("openai", "gpt-x", []string{"eu"}, 0)
		}, ProviderCapabilities{}, []string{"eu"}, 0},
		{"the longer family over the shorter", func() {
			RegisterCapabilityFamily("openai", "gpt-", ProviderCapabilities{Regions: []string{"us"}})
			RegisterCapabilityFamily("openai", "gpt-x", ProviderCapabilities{Regions: []string{"eu"}})
		}, ProviderCapabilities{}, []string{"eu"}, 0},
		{"another provider's family", func() {
			RegisterCapabilityFamily("anthropic", "gpt-", ProviderCapabilities{Regions: []string{"eu"}})
		}, ProviderCapabilities{}, nil, 0},
		{"a family that does not match", func() {
			RegisterCapabilityFamily("openai", "o1", ProviderCapabilities{Regions: []string{"eu"}})
		}, ProviderCapabilities{}, nil, 0},
		{"what the run observed is kept over the family", func() {
			RegisterCapabilityFamily("openai", "gpt-", ProviderCapabilities{Regions: []string{"us"}, RetentionDays: 30})
		}, ProviderCapabilities{Regions: []string{"eu"}, RetentionDays: 1}, []string{"eu"}, 1},
		{"twice over", func() {
			RegisterCapabilityFamily("openai", "gpt-", ProviderCapabilities{Regions: []string{"eu"}, RetentionDays: 3})
			publishObserved(ProviderCapabilities{Provider: "openai", Model: "gpt-x"})
		}, ProviderCapabilities{}, []string{"eu"}, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ResetCapabilityRegistryForTest()
			t.Cleanup(ResetCapabilityRegistryForTest)
			tc.declare()

			observed := tc.observed
			observed.Provider, observed.Model = "openai", "gpt-x"
			observed.Supports = map[Capability]bool{CapToolCalling: true}
			publishObserved(observed)

			got, known := CapabilitiesFor("openai", "gpt-x")
			if !known || !got.Supports[CapToolCalling] {
				t.Fatalf("what the run observed was not stored: %+v", got)
			}
			if !reflect.DeepEqual(got.Regions, tc.wantRegions) || got.RetentionDays != tc.wantRetention {
				t.Errorf("regions %v and retention %d, want %v and %d", got.Regions, got.RetentionDays, tc.wantRegions, tc.wantRetention)
			}
		})
	}
}

// The registry keeps its own copy of everything it is given and hands out
// copies. A family's declaration was stored as given and returned as stored,
// so a caller who changed a list it had registered, or one it had looked up,
// changed the declaration for everyone.
func TestTheRegistryKeepsAndGivesCopies(t *testing.T) {
	ways := []struct {
		name     string
		register func(caps ProviderCapabilities)
	}{
		{"an exact route", func(caps ProviderCapabilities) {
			caps.Provider, caps.Model = "openai", "gpt-x"
			RegisterCapabilities(caps)
		}},
		{"a family", func(caps ProviderCapabilities) { RegisterCapabilityFamily("openai", "gpt-", caps) }},
		{"a family for the whole provider", func(caps ProviderCapabilities) { RegisterCapabilityFamily("openai", "", caps) }},
		{"a family, then the route declared over it", func(caps ProviderCapabilities) {
			RegisterCapabilityFamily("openai", "gpt-", caps)
			DeclareRoute("openai", "gpt-x", []string{"eu"}, 0)
		}},
		{"a family, then a run published over it", func(caps ProviderCapabilities) {
			RegisterCapabilityFamily("openai", "gpt-", caps)
			publishObserved(ProviderCapabilities{Provider: "openai", Model: "gpt-x",
				Supports: map[Capability]bool{CapToolCalling: true}, SchemaKeywords: []string{"pattern"}})
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

			// The caller changes what it registered.
			given.Supports[CapToolCalling] = false
			given.SchemaKeywords[0] = "changed"
			given.Regions[0] = "us"

			first, known := CapabilitiesFor("openai", "gpt-x")
			if !known {
				t.Fatal("the route is not known")
			}
			if !first.Supports[CapToolCalling] || first.SchemaKeywords[0] != "pattern" || first.Regions[0] != "eu" {
				t.Fatalf("changing what was registered changed the declaration: %+v", first)
			}

			// The caller changes what it looked up.
			first.Supports[CapToolCalling] = false
			first.SchemaKeywords[0] = "changed"
			first.Regions[0] = "us"

			second, _ := CapabilitiesFor("openai", "gpt-x")
			if !second.Supports[CapToolCalling] || second.SchemaKeywords[0] != "pattern" || second.Regions[0] != "eu" {
				t.Errorf("changing what was looked up changed the declaration: %+v", second)
			}
		})
	}
}

func TestCopyCapabilities(t *testing.T) {
	cases := []struct {
		name string
		caps ProviderCapabilities
	}{
		{"empty", ProviderCapabilities{}},
		{"names only", ProviderCapabilities{Provider: "p", Model: "m"}},
		{"a map", ProviderCapabilities{Supports: map[Capability]bool{CapToolCalling: true}}},
		{"an empty map", ProviderCapabilities{Supports: map[Capability]bool{}}},
		{"keywords", ProviderCapabilities{SchemaKeywords: []string{"a", "b"}}},
		{"empty keywords", ProviderCapabilities{SchemaKeywords: []string{}}},
		{"regions", ProviderCapabilities{Regions: []string{"eu", "us"}}},
		{"empty regions", ProviderCapabilities{Regions: []string{}}},
		{"retention", ProviderCapabilities{RetentionDays: 30}},
		{"everything", ProviderCapabilities{Provider: "p", Model: "m", Supports: map[Capability]bool{CapToolCalling: true, CapStreaming: false},
			SchemaKeywords: []string{"a"}, Regions: []string{"eu"}, RetentionDays: 7}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			copied := copyCapabilities(tc.caps)
			if !reflect.DeepEqual(copied, tc.caps) {
				t.Fatalf("the copy differs: %+v, want %+v", copied, tc.caps)
			}
			// Nothing is shared: changing the copy leaves the original alone.
			for capability := range copied.Supports {
				copied.Supports[capability] = !copied.Supports[capability]
			}
			if copied.Supports != nil {
				copied.Supports["made-up"] = true
			}
			for i := range copied.SchemaKeywords {
				copied.SchemaKeywords[i] = "changed"
			}
			for i := range copied.Regions {
				copied.Regions[i] = "changed"
			}
			again := copyCapabilities(tc.caps)
			if len(tc.caps.Supports) > 0 && reflect.DeepEqual(copied.Supports, again.Supports) {
				t.Error("the map was not changed, so the test proves nothing")
			}
			for capability, supported := range tc.caps.Supports {
				if again.Supports[capability] != supported {
					t.Errorf("the original's map changed with the copy")
				}
			}
			if _, leaked := tc.caps.Supports["made-up"]; leaked {
				t.Error("the original's map gained an entry from the copy")
			}
			for i, keyword := range tc.caps.SchemaKeywords {
				if keyword == "changed" {
					t.Errorf("the original's keyword %d changed with the copy", i)
				}
			}
			for i, region := range tc.caps.Regions {
				if region == "changed" {
					t.Errorf("the original's region %d changed with the copy", i)
				}
			}
		})
	}
}

// Publishing reads a route and writes it back as one step. The test for that
// started a Publish and a DeclareRoute together and looked at what was left,
// which a version with the read and the write under two locks passes nearly
// every time. This one stands between the read and the write and has a
// declaration made there: it must not get in until the write is done, and it
// must be there afterwards.
func TestPublishingReadsAndWritesARouteAsOneStep(t *testing.T) {
	ResetCapabilityRegistryForTest()
	t.Cleanup(ResetCapabilityRegistryForTest)
	t.Cleanup(func() { betweenReadAndWriteForTest = nil })

	declared := make(chan struct{})
	gotIn := false
	betweenReadAndWriteForTest = func() {
		running := make(chan struct{})
		go func() {
			close(running)
			DeclareRoute("openai", "gpt-x", []string{"eu"}, 7)
			close(declared)
		}()
		// The window starts once the declaring goroutine is running. Started
		// from the hook, a loaded machine could spend the whole window before
		// that goroutine had tried the lock, and a version that let it in
		// would pass.
		<-running
		select {
		case <-declared:
			gotIn = true
		case <-time.After(200 * time.Millisecond):
		}
	}
	publishObserved(ProviderCapabilities{Provider: "openai", Model: "gpt-x", Supports: map[Capability]bool{CapToolCalling: true}})
	betweenReadAndWriteForTest = nil

	if gotIn {
		t.Error("a declaration was made between publishObserved's read and its write")
	}
	select {
	case <-declared:
	case <-time.After(2 * time.Second):
		t.Fatal("the declaration never got in")
	}
	caps, known := CapabilitiesFor("openai", "gpt-x")
	if !known || len(caps.Regions) != 1 || caps.Regions[0] != "eu" || caps.RetentionDays != 7 {
		t.Errorf("the declaration was lost: %+v", caps)
	}
	if !caps.Supports[CapToolCalling] {
		t.Errorf("what the run observed was lost: %+v", caps)
	}
}

// The local provider's answer names the model that was asked for. It named
// "local-model" whatever was asked, which a client's data policy reads as an
// answer from a route other than the one it allowed.
func TestTheLocalProviderAnswersAsTheModelAsked(t *testing.T) {
	provider, err := NewLocalProvider(ProviderConfig{})
	if err != nil {
		t.Fatalf("NewLocalProvider: %v", err)
	}
	provider.WithHandler(func(context.Context, CompletionRequest) (string, error) { return "{}", nil })
	for _, tc := range []struct{ asked, want string }{
		{"gpt-6-luna", "gpt-6-luna"},
		{"llama3", "llama3"},
		{"a-model-2026-01-01", "a-model-2026-01-01"},
		{"", "local-model"},
	} {
		resp, err := provider.Complete(context.Background(), CompletionRequest{Model: tc.asked, UserPrompt: "x"})
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if resp.Model != tc.want || resp.Provider != provider.Name() {
			t.Errorf("asked for %q, the answer names %q/%q", tc.asked, resp.Provider, resp.Model)
		}
		if tc.asked != "" && !SameModelRoute(tc.asked, resp.Model) {
			t.Errorf("the answer to %q does not name the route that was asked", tc.asked)
		}
	}
}
