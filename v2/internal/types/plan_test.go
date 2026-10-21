package types

import (
	"encoding/json"
	"math"
	"reflect"
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
	// The refusal: the snapshot says whether steering was set and nothing of
	// what it said. Two options that differ only in the steering text give
	// byte-identical snapshots, so no text is in them: the digest is a hash and
	// cannot be searched for a phrase.
	other := opt
	other.Steering = "entirely different words"
	first, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(PolicySnapshotFrom(other))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) || snap.Digest() != PolicySnapshotFrom(other).Digest() {
		t.Fatalf("the snapshot depends on the steering text: %s versus %s", first, second)
	}
	if strings.Contains(string(first), "instructions") {
		t.Fatalf("the snapshot's JSON carries the steering text: %s", first)
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

// Two plans built identically serialize and digest identically.
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
}

// A Plan holds shapes, counts and names this library computed, and no value the
// caller passed in. That is a property of its fields: the types below are what
// is allowed to hold text, and a field of interface or map type, which could hold
// anything, is refused. Adding a field that can carry the caller's data fails
// here until it is looked at.
func TestAPlanHasNoFieldThatCouldHoldTheCallersData(t *testing.T) {
	allowedText := map[string]bool{
		"Plan.InputShape": true, "Plan.IneligibleReason": true, "Plan.ShapeReason": true,
		"Plan.ForceReason": true, "Plan.EstimatorVersion": true, "Plan.PolicyDigest": true,
		"Plan.CapabilityDigest": true, "OperationID.Name": true, "OperationID.Version": true,
		"RejectedAlternative.Reason": true, "ChunkPlan.BoundingRule": true, "OversizedItem.Bound": true,
		"CostEstimate.Currency": true, "CostEstimate.PricingSource": true, "PolicySnapshot.ResponseFormat": true,
		"CapabilitySnapshot.Provider": true, "CapabilitySnapshot.Model": true,
	}
	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type)
	walk = func(typ reflect.Type) {
		for typ.Kind() == reflect.Slice || typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Array {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || seen[typ] {
			return
		}
		seen[typ] = true
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			name := typ.Name() + "." + field.Name
			switch field.Type.Kind() {
			case reflect.Interface, reflect.Map, reflect.Func, reflect.Chan:
				t.Errorf("%s is of kind %s, which can hold anything the caller passed in", name, field.Type.Kind())
			case reflect.String:
				if !allowedText[name] {
					t.Errorf("%s is text that is not on the list of names and reasons this library computes", name)
				}
			}
			walk(field.Type)
		}
	}
	walk(reflect.TypeOf(Plan{}))
}

// A Threshold that is NaN or infinite is a caller's value. JSON has no such
// number, so the plan used to fail to serialize and every such plan digested to
// the same fixed marker, making two different plans compare identical.
func TestANonFiniteThresholdKeepsPlansSerializableAndDistinct(t *testing.T) {
	for _, threshold := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		build := func(items int) Plan {
			return Plan{
				Operation: OperationID{Name: "extract", Version: "v1"},
				ItemCount: items,
				Eligible:  true,
				Shape:     ShapeMDSP,
				MaxCalls:  items,
				Policy:    PolicySnapshotFrom(OpOptions{Threshold: threshold}),
			}
		}
		a, b, c := build(3), build(3), build(4)

		if _, err := a.Serialize(); err != nil {
			t.Errorf("threshold %v: Serialize() = %v", threshold, err)
		}
		if a.Digest() == "undigestable" || a.Policy.Digest() == "undigestable" {
			t.Errorf("threshold %v: the digest is the fixed marker", threshold)
		}
		if a.Digest() != b.Digest() {
			t.Errorf("threshold %v: identical plans digest differently", threshold)
		}
		if a.Digest() == c.Digest() {
			t.Errorf("threshold %v: different plans digest the same", threshold)
		}
	}

	// And the finite ones are untouched: the snapshot is plain JSON.
	encoded, err := json.Marshal(PolicySnapshotFrom(OpOptions{Threshold: 0.75, ResponseFormat: "json"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"Threshold":0.75`) {
		t.Errorf("a finite threshold changed shape: %s", encoded)
	}
	encoded, _ = json.Marshal(PolicySnapshotFrom(OpOptions{Threshold: math.NaN()}))
	if !strings.Contains(string(encoded), `"Threshold":"NaN"`) {
		t.Errorf("a NaN threshold was written as %s", encoded)
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

// Explain writes each fact on its own line, and the lines are these.
func TestPlanExplainWritesEachFactOnItsOwnLine(t *testing.T) {
	full := Plan{
		Operation:   OperationID{Name: "extract", Version: "v1"},
		ItemCount:   10,
		Eligible:    true,
		ShapeForced: true,
		Shape:       ShapeMDSP,
		ShapeReason: "caller forced MDSP for cost",
		Alternatives: []RejectedAlternative{
			{Shape: ShapeAtomic, Reason: "would cost more"},
			{Shape: ShapeGlobal, Reason: "no global contract"},
		},
		Chunks:         []ChunkPlan{{ItemCount: 10, BoundingRule: "item_count"}},
		OversizedItems: []OversizedItem{{Index: 3, Bound: "payload_bytes"}},
		MaxCalls:       2,
		EstimatedCost:  CostEstimate{Priced: true, Currency: "USD", EstimatedCost: 0.01},
	}

	cases := []struct {
		name string
		plan func() Plan
		want string
	}{
		{"every section", func() Plan { return full }, strings.Join([]string{
			"extract@v1: 10 item(s), shape mdsp (forced: caller forced MDSP for cost)",
			"1 chunk(s)",
			"1 item(s) routed atomically (oversized)",
			"at most 2 call(s)",
			"estimated USD 0.010000",
			"Rejected: atomic -- would cost more",
			"Rejected: global -- no global contract",
		}, "\n")},
		{"unforced, unpriced, no alternatives", func() Plan {
			p := full
			p.ShapeForced, p.ShapeReason = false, "the only shape"
			p.EstimatedCost = CostEstimate{}
			p.Alternatives = nil
			p.OversizedItems = nil
			return p
		}, strings.Join([]string{
			"extract@v1: 10 item(s), shape mdsp (the only shape)",
			"1 chunk(s)",
			"at most 2 call(s)",
			"cost unpriced",
		}, "\n")},
		{"no chunks and one alternative", func() Plan {
			p := full
			p.Chunks, p.OversizedItems = nil, nil
			p.Alternatives = p.Alternatives[:1]
			p.MaxCalls = 10
			p.Shape = ShapeAtomic
			p.ShapeForced = false
			p.ShapeReason = "one call per item"
			return p
		}, strings.Join([]string{
			"extract@v1: 10 item(s), shape atomic (one call per item)",
			"at most 10 call(s)",
			"estimated USD 0.010000",
			"Rejected: atomic -- would cost more",
		}, "\n")},
		{"ineligible", func() Plan {
			return Plan{Operation: OperationID{Name: "extract", Version: "v1"}, IneligibleReason: "no prompt"}
		}, "extract@v1: not eligible -- no prompt"},
		{"an unnamed operation", func() Plan {
			p := full
			p.Operation = OperationID{}
			p.Alternatives, p.Chunks, p.OversizedItems = nil, nil, nil
			return p
		}, strings.Join([]string{
			"unnamed@: 10 item(s), shape mdsp (forced: caller forced MDSP for cost)",
			"at most 2 call(s)",
			"estimated USD 0.010000",
		}, "\n")},
		{"two chunks and two oversized items", func() Plan {
			p := full
			p.Chunks = []ChunkPlan{{ItemCount: 5}, {ItemCount: 5}}
			p.OversizedItems = []OversizedItem{{Index: 1}, {Index: 2}}
			p.Alternatives = nil
			return p
		}, strings.Join([]string{
			"extract@v1: 10 item(s), shape mdsp (forced: caller forced MDSP for cost)",
			"2 chunk(s)",
			"2 item(s) routed atomically (oversized)",
			"at most 2 call(s)",
			"estimated USD 0.010000",
		}, "\n")},
		{"a zero call ceiling", func() Plan {
			p := full
			p.MaxCalls = 0
			p.Alternatives, p.Chunks, p.OversizedItems = nil, nil, nil
			return p
		}, strings.Join([]string{
			"extract@v1: 10 item(s), shape mdsp (forced: caller forced MDSP for cost)",
			"at most 0 call(s)",
			"estimated USD 0.010000",
		}, "\n")},
		{"a priced estimate in another currency", func() Plan {
			p := full
			p.EstimatedCost = CostEstimate{Priced: true, Currency: "EUR", EstimatedCost: 1.5}
			p.Alternatives, p.Chunks, p.OversizedItems = nil, nil, nil
			return p
		}, strings.Join([]string{
			"extract@v1: 10 item(s), shape mdsp (forced: caller forced MDSP for cost)",
			"at most 2 call(s)",
			"estimated EUR 1.500000",
		}, "\n")},
		{"an alternative whose reason has a comma and a newline-free dash", func() Plan {
			p := full
			p.Alternatives = []RejectedAlternative{{Shape: ShapeSDMP, Reason: "needs a staged form, none declared -- refused"}}
			p.Chunks, p.OversizedItems = nil, nil
			return p
		}, strings.Join([]string{
			"extract@v1: 10 item(s), shape mdsp (forced: caller forced MDSP for cost)",
			"at most 2 call(s)",
			"estimated USD 0.010000",
			"Rejected: sdmp -- needs a staged form, none declared -- refused",
		}, "\n")},
		{"an eligible plan with no items", func() Plan {
			p := full
			p.ItemCount = 0
			p.ShapeForced, p.ShapeReason = false, "nothing to do"
			p.Alternatives, p.Chunks, p.OversizedItems, p.EstimatedCost = nil, nil, nil, CostEstimate{}
			p.MaxCalls = 0
			return p
		}, strings.Join([]string{
			"extract@v1: 0 item(s), shape mdsp (nothing to do)",
			"at most 0 call(s)",
			"cost unpriced",
		}, "\n")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.plan().Explain()
			if got != tc.want {
				t.Errorf("Explain() =\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}
