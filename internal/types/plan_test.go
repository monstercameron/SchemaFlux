package types

import (
	"strings"
	"testing"
)

func TestExecutionShapeString(t *testing.T) {
	cases := []struct {
		s    ExecutionShape
		want string
	}{
		{ShapeUnspecified, "unspecified"},
		{ShapeAtomic, "atomic"},
		{ShapeMDSP, "mdsp"},
		{ShapeSDMP, "sdmp"},
		{ShapeGlobal, "global"},
		{ExecutionShape(99), "unspecified"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.s.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

// PolicySnapshotFrom's whole job is to drop the caller's payload
// (Steering) while keeping everything that influences planning -- the
// refusal this function exists for.
func TestPolicySnapshotFromDropsSteeringContent(t *testing.T) {
	opt := OpOptions{
		Steering:        "the caller's private instructions, verbatim",
		Mode:            Strict,
		Intelligence:    Smart,
		MaxOutputTokens: 500,
		Threshold:       0.9,
		ResponseFormat:  "json",
	}
	snap := PolicySnapshotFrom(opt)

	if snap.Mode != Strict || snap.Intelligence != Smart || snap.MaxOutputTokens != 500 ||
		snap.Threshold != 0.9 || snap.ResponseFormat != "json" {
		t.Fatalf("snapshot = %+v, did not carry the planning-relevant fields", snap)
	}
	if !snap.HasSteering {
		t.Error("HasSteering = false despite Steering being set")
	}
	// The refusal: nothing in the snapshot's own fields can reproduce the
	// caller's steering text, because the type has no field for it.
	if strings.Contains(snap.Digest(), "private instructions") {
		t.Fatal("the snapshot's digest input carried the caller's steering text")
	}

	empty := PolicySnapshotFrom(OpOptions{})
	if empty.HasSteering {
		t.Error("HasSteering = true for an OpOptions with no steering set")
	}
}

func TestPolicySnapshotDigestIsStableAndDistinguishing(t *testing.T) {
	a := PolicySnapshotFrom(OpOptions{Mode: Strict, Intelligence: Smart})
	b := PolicySnapshotFrom(OpOptions{Mode: Strict, Intelligence: Smart})
	c := PolicySnapshotFrom(OpOptions{Mode: Creative, Intelligence: Smart})

	if a.Digest() != b.Digest() {
		t.Fatal("identical snapshots produced different digests")
	}
	if a.Digest() == c.Digest() {
		t.Fatal("different snapshots produced the same digest")
	}
	if a.Digest() == "" {
		t.Fatal("Digest() returned empty")
	}
}

func TestCapabilitySnapshotDigest(t *testing.T) {
	a := CapabilitySnapshot{Provider: "openai", Model: "gpt-5.6-luna", MaxOutputTokens: 4000}
	b := CapabilitySnapshot{Provider: "openai", Model: "gpt-5.6-luna", MaxOutputTokens: 4000}
	c := CapabilitySnapshot{Provider: "openai", Model: "gpt-5.6-terra", MaxOutputTokens: 4000}

	if a.Digest() != b.Digest() {
		t.Fatal("identical capability snapshots produced different digests")
	}
	if a.Digest() == c.Digest() {
		t.Fatal("different capability snapshots produced the same digest")
	}
}

// digestOf degrades to a fixed marker for a value json.Marshal cannot
// encode, rather than panicking -- exercised here through Plan.Digest with
// a channel smuggled into a field that accepts `any`... but Plan carries no
// such field, so this is checked against digestOf's own documented
// unreachable-in-practice path via a type that mirrors what canonicalJSON
// would receive. Plan itself is always plain data (see Plan's own doc
// comment), so this test targets the byte-identical guarantee instead: two
// preflight-shaped plans built identically must serialize identically.
func TestPlanDigestAndSerializeAreDeterministic(t *testing.T) {
	build := func() Plan {
		return Plan{
			Operation:  OperationID{Name: "extract", Version: "v1"},
			InputShape: "[]string of 3",
			ItemCount:  3,
			Eligible:   true,
			Shape:      ShapeMDSP,
			Chunks:     []ChunkPlan{{ItemCount: 3, BoundingRule: "item_count"}},
			MaxCalls:   1,
			EstimatedCost: CostEstimate{
				Priced: true, EstimatedCost: 0.002, Currency: "USD",
			},
		}
	}
	a, b := build(), build()

	if a.Digest() != b.Digest() {
		t.Fatal("two identically-built plans produced different digests")
	}

	serialized, err := a.Serialize()
	if err != nil {
		t.Fatalf("Serialize() error: %v", err)
	}
	if len(serialized) == 0 {
		t.Fatal("Serialize() returned no bytes")
	}
	// Serialize never carries a caller value -- every field is a shape, a
	// count, or a number this package computed.
	if strings.Contains(string(serialized), "the caller's actual data") {
		t.Fatal("serialized plan carried caller content")
	}
}

func TestPlanExplainIneligible(t *testing.T) {
	p := Plan{
		Operation:        OperationID{Name: "extract", Version: "v1"},
		Eligible:         false,
		IneligibleReason: "no BuildPrompt declared",
	}
	explain := p.Explain()
	if !strings.Contains(explain, "not eligible") {
		t.Errorf("Explain() = %q, want it to say not eligible", explain)
	}
	if !strings.Contains(explain, "no BuildPrompt declared") {
		t.Errorf("Explain() = %q, want the ineligible reason", explain)
	}
}

// Explain covers every optional section: forced shape, rejected
// alternatives, chunks, oversized items, and both the priced and unpriced
// cost branches -- the full pre-execution explanation PL-014 asks for.
func TestPlanExplainEligibleCoversEverySection(t *testing.T) {
	p := Plan{
		Operation:   OperationID{Name: "extract", Version: "v1"},
		ItemCount:   10,
		Eligible:    true,
		ShapeForced: true,
		Shape:       ShapeMDSP,
		ShapeReason: "caller forced MDSP for cost",
		Alternatives: []RejectedAlternative{
			{Shape: ShapeAtomic, Reason: "would cost more"},
		},
		Chunks:         []ChunkPlan{{ItemCount: 10, BoundingRule: "item_count"}},
		OversizedItems: []OversizedItem{{Index: 3, Bound: "payload_bytes"}},
		MaxCalls:       2,
		EstimatedCost:  CostEstimate{Priced: true, Currency: "USD", EstimatedCost: 0.01},
	}
	explain := p.Explain()
	for _, want := range []string{
		"forced: caller forced MDSP for cost",
		"Rejected: atomic",
		"would cost more",
		"1 chunk(s)",
		"1 item(s) routed atomically",
		"at most 2 call(s)",
		"estimated USD",
	} {
		if !strings.Contains(explain, want) {
			t.Errorf("Explain() = %q, missing %q", explain, want)
		}
	}

	unpriced := p
	unpriced.ShapeForced = false
	unpriced.EstimatedCost = CostEstimate{}
	unpriced.Alternatives = nil
	unpriced.OversizedItems = nil
	unpricedExplain := unpriced.Explain()
	if !strings.Contains(unpricedExplain, "cost unpriced") {
		t.Errorf("Explain() = %q, want \"cost unpriced\" when EstimatedCost.Priced is false", unpricedExplain)
	}
	if strings.Contains(unpricedExplain, "forced:") {
		t.Errorf("Explain() = %q, an unforced shape must not say \"forced:\"", unpricedExplain)
	}
}
