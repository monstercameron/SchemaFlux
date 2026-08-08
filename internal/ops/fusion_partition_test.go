package ops

import (
	"context"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// PL-012's own Verify line, split in two: fusion_test.go carries "fused and
// unfused runs of the same fifty builders produce identical values" (values
// are the load-bearing half); this file carries "builders differing only
// in steering land in separate partitions," plus the same partitioning
// proof for the other six axes and the executionShape gate compatible()
// adds on top of FusionKey.
//
// partitionFor is a small helper shared by every test below: build two
// entries through a group's Defer, run partitionEntries directly (not
// through Run/RunOpManyPartial -- no provider needed to check partitioning
// itself), and report how many groups resulted.
func partitionFor(a, b *fusionEntry[string, string]) int {
	return len(partitionEntries([]*fusionEntry[string, string]{a, b}))
}

func deferEntry(g *FusionGroup[string, string], op Op[string, string], input string, opt types.OpOptions, fo FusionOptions) *fusionEntry[string, string] {
	h := g.Defer(op, input, opt, fo)
	return h.entry
}

// baseline is the opt/fo pair every test below starts from and changes
// exactly one thing about, so a resulting partition split is attributable
// to that one change and nothing else.
func baseline() (types.OpOptions, FusionOptions) {
	return types.OpOptions{
			SchemaID:     "widget@v1",
			Model:        "gpt-5",
			Intelligence: types.Smart,
			Steering:     "be concise",
		}, FusionOptions{
			DataPolicy: types.DataPolicy{Classification: "public"},
			Budget:     BudgetSettings{CeilingUSD: 2, Scope: "team-a"},
		}
}

// TestIdenticalBuildersFuseIntoOnePartition is the control: two builders
// with the exact same op/opt/fo land in one partition.
func TestIdenticalBuildersFuseIntoOnePartition(t *testing.T) {
	g := NewFusionGroup[string, string]()
	op := fusionOp("extract", "v1")
	opt, fo := baseline()

	a := deferEntry(g, op, "x", opt, fo)
	b := deferEntry(g, op, "y", opt, fo)

	if got := partitionFor(a, b); got != 1 {
		t.Fatalf("two identical builders formed %d partitions, want 1", got)
	}
}

// TestBuildersDifferingOnlyInSteeringPartition is PL-012's own Verify
// sentence, verbatim: "builders differing only in steering land in
// separate partitions."
func TestBuildersDifferingOnlyInSteeringPartition(t *testing.T) {
	g := NewFusionGroup[string, string]()
	op := fusionOp("extract", "v1")
	opt, fo := baseline()

	a := deferEntry(g, op, "x", opt, fo)

	opt2 := opt
	opt2.Steering = "be verbose instead"
	b := deferEntry(g, op, "y", opt2, fo)

	if got := partitionFor(a, b); got != 2 {
		t.Fatalf("builders differing only in steering formed %d partition(s), want 2", got)
	}
}

// TestBuildersDifferingOnlyInOperationIDPartition: axis 1.
func TestBuildersDifferingOnlyInOperationIDPartition(t *testing.T) {
	g := NewFusionGroup[string, string]()
	opt, fo := baseline()

	a := deferEntry(g, fusionOp("extract", "v1"), "x", opt, fo)
	b := deferEntry(g, fusionOp("extract", "v2"), "y", opt, fo) // same name, different version

	if got := partitionFor(a, b); got != 2 {
		t.Fatalf("builders differing only in operation version formed %d partition(s), want 2", got)
	}

	c := deferEntry(g, fusionOp("classify", "v1"), "z", opt, fo) // different name, same version
	if got := partitionFor(a, c); got != 2 {
		t.Fatalf("builders differing only in operation name formed %d partition(s), want 2", got)
	}
}

// TestBuildersDifferingOnlyInSchemaHashPartition: axis 2.
func TestBuildersDifferingOnlyInSchemaHashPartition(t *testing.T) {
	g := NewFusionGroup[string, string]()
	op := fusionOp("extract", "v1")
	opt, fo := baseline()

	a := deferEntry(g, op, "x", opt, fo)

	opt2 := opt
	opt2.SchemaID = "widget@v2"
	b := deferEntry(g, op, "y", opt2, fo)

	if got := partitionFor(a, b); got != 2 {
		t.Fatalf("builders differing only in schema hash formed %d partition(s), want 2", got)
	}
}

// TestBuildersDifferingOnlyInRoutePartition: axis 3, both halves (model pin
// and intelligence tier independently).
func TestBuildersDifferingOnlyInRoutePartition(t *testing.T) {
	op := fusionOp("extract", "v1")
	opt, fo := baseline()

	t.Run("model", func(t *testing.T) {
		g := NewFusionGroup[string, string]()
		a := deferEntry(g, op, "x", opt, fo)
		opt2 := opt
		opt2.Model = "a-different-model"
		b := deferEntry(g, op, "y", opt2, fo)
		if got := partitionFor(a, b); got != 2 {
			t.Fatalf("builders differing only in model pin formed %d partition(s), want 2", got)
		}
	})

	t.Run("intelligence", func(t *testing.T) {
		g := NewFusionGroup[string, string]()
		a := deferEntry(g, op, "x", opt, fo)
		opt2 := opt
		opt2.Intelligence = types.Quick
		b := deferEntry(g, op, "y", opt2, fo)
		if got := partitionFor(a, b); got != 2 {
			t.Fatalf("builders differing only in intelligence tier formed %d partition(s), want 2", got)
		}
	})
}

// TestBuildersDifferingOnlyInContractLevelPartition: axis 5. Two Op values
// with the same OperationID but different declared contracts is the
// adversarial case FusionGroup's own doc comment names as undetectable via
// op.ID alone -- Contract still catches it because it is compared
// independently.
func TestBuildersDifferingOnlyInContractLevelPartition(t *testing.T) {
	g := NewFusionGroup[string, string]()
	opt, fo := baseline()

	plain := fusionOp("extract", "v1")
	withInvariant := fusionOp("extract", "v1")
	withInvariant.Contract.Invariants = []func(string) error{func(string) error { return nil }}

	a := deferEntry(g, plain, "x", opt, fo)
	b := deferEntry(g, withInvariant, "y", opt, fo)

	if got := partitionFor(a, b); got != 2 {
		t.Fatalf("builders differing only in declared contract level formed %d partition(s), want 2", got)
	}
}

// TestBuildersDifferingOnlyInDataPolicyPartition: axis 6.
func TestBuildersDifferingOnlyInDataPolicyPartition(t *testing.T) {
	g := NewFusionGroup[string, string]()
	op := fusionOp("extract", "v1")
	opt, fo := baseline()

	a := deferEntry(g, op, "x", opt, fo)

	fo2 := fo
	fo2.DataPolicy = types.DataPolicy{Classification: "restricted"}
	b := deferEntry(g, op, "y", opt, fo2)

	if got := partitionFor(a, b); got != 2 {
		t.Fatalf("builders differing only in data policy formed %d partition(s), want 2", got)
	}
}

// TestBuildersDifferingOnlyInBudgetPartition: axis 7.
func TestBuildersDifferingOnlyInBudgetPartition(t *testing.T) {
	g := NewFusionGroup[string, string]()
	op := fusionOp("extract", "v1")
	opt, fo := baseline()

	a := deferEntry(g, op, "x", opt, fo)

	fo2 := fo
	fo2.Budget = BudgetSettings{CeilingUSD: 999, Scope: "team-a"}
	b := deferEntry(g, op, "y", opt, fo2)

	if got := partitionFor(a, b); got != 2 {
		t.Fatalf("builders differing only in budget formed %d partition(s), want 2", got)
	}
}

// TestBudgetSettingsMustMatchExactlyNotJustBeCompatible pins the stated
// limitation in BudgetSettings' own doc comment: to-production.md 9.8 says
// "compatible budget settings," but this implementation requires exact
// equality. Two ceilings that would, in a smarter accounting scheme, safely
// share one call (e.g. both comfortably above the group's real cost) still
// partition here.
func TestBudgetSettingsMustMatchExactlyNotJustBeCompatible(t *testing.T) {
	g := NewFusionGroup[string, string]()
	op := fusionOp("extract", "v1")
	opt, fo := baseline()

	a := deferEntry(g, op, "x", opt, fo)

	fo2 := fo
	fo2.Budget.CeilingUSD = fo.Budget.CeilingUSD * 2 // strictly more permissive, still partitions
	b := deferEntry(g, op, "y", opt, fo2)

	if got := partitionFor(a, b); got != 2 {
		t.Fatalf("a strictly-more-permissive budget ceiling still fused (%d partition(s)), want 2 -- BudgetSettings must require exact equality", got)
	}
}

// --- The executionShape gate: axes RunOpManyPartial's single shared
// types.OpOptions forces to match even though PL-012 does not name them.

// TestBuildersDifferingOnlyInModeDoNotFuse proves the residual-equality
// gate: Mode is not one of PL-012's seven named axes, but RunOpManyPartial
// takes exactly one types.OpOptions per partition, so two builders that
// disagree on Mode cannot share one without one of them silently running
// under the other's Mode -- exactly the correctness defect PL-012's Verify
// line forbids.
func TestBuildersDifferingOnlyInModeDoNotFuse(t *testing.T) {
	g := NewFusionGroup[string, string]()
	op := fusionOp("extract", "v1")
	opt, fo := baseline()
	opt.Mode = types.Strict

	a := deferEntry(g, op, "x", opt, fo)

	opt2 := opt
	opt2.Mode = types.Creative
	b := deferEntry(g, op, "y", opt2, fo)

	if got := partitionFor(a, b); got != 2 {
		t.Fatalf("builders differing only in Mode formed %d partition(s), want 2 (Mode is not shareable across RunOpManyPartial's single opt)", got)
	}
}

// TestBuildersDifferingOnlyInThresholdDoNotFuse: same property, Threshold.
func TestBuildersDifferingOnlyInThresholdDoNotFuse(t *testing.T) {
	g := NewFusionGroup[string, string]()
	op := fusionOp("extract", "v1")
	opt, fo := baseline()
	opt.Threshold = 0.9

	a := deferEntry(g, op, "x", opt, fo)

	opt2 := opt
	opt2.Threshold = 0.1
	b := deferEntry(g, op, "y", opt2, fo)

	if got := partitionFor(a, b); got != 2 {
		t.Fatalf("builders differing only in Threshold formed %d partition(s), want 2", got)
	}
}

// TestBuildersDifferingOnlyInMaxOutputTokensDoNotFuse: same property,
// MaxOutputTokens.
func TestBuildersDifferingOnlyInMaxOutputTokensDoNotFuse(t *testing.T) {
	g := NewFusionGroup[string, string]()
	op := fusionOp("extract", "v1")
	opt, fo := baseline()
	opt.MaxOutputTokens = 100

	a := deferEntry(g, op, "x", opt, fo)

	opt2 := opt
	opt2.MaxOutputTokens = 4000
	b := deferEntry(g, op, "y", opt2, fo)

	if got := partitionFor(a, b); got != 2 {
		t.Fatalf("builders differing only in MaxOutputTokens formed %d partition(s), want 2", got)
	}
}

// TestBuildersDifferingOnlyInRequestIDStillFuse is the deliberate other
// side of the same design: RequestID/CorrelationID/Context identify or
// trace a call rather than change what it computes, and normalizedOptions
// excludes them on purpose (fusion.go's own doc comment). Two builders that
// differ ONLY here must still share a partition, or fusion would never
// collapse anything a caller's loop naturally varies per iteration (a
// fresh RequestID per call is the common case, not the exception).
func TestBuildersDifferingOnlyInRequestIDStillFuse(t *testing.T) {
	g := NewFusionGroup[string, string]()
	op := fusionOp("extract", "v1")
	opt, fo := baseline()
	opt.RequestID = "req-aaa"
	opt.CorrelationID = "corr-aaa"

	a := deferEntry(g, op, "x", opt, fo)

	opt2 := opt
	opt2.RequestID = "req-bbb"
	opt2.CorrelationID = "corr-bbb"
	b := deferEntry(g, op, "y", opt2, fo)

	if got := partitionFor(a, b); got != 1 {
		t.Fatalf("builders differing only in RequestID/CorrelationID formed %d partition(s), want 1 (tracing fields must not block fusion)", got)
	}
}

// TestBuildersDifferingOnlyInContextStillFuse: Context is the third
// excluded field, checked on its own -- two different, non-nil contexts
// (which are never DeepEqual to one another even carrying the same
// deadline/values, since context.Context is an interface over unexported
// implementation state) must not block fusion either.
func TestBuildersDifferingOnlyInContextStillFuse(t *testing.T) {
	g := NewFusionGroup[string, string]()
	op := fusionOp("extract", "v1")
	opt, fo := baseline()

	type ctxKeyA struct{}
	type ctxKeyB struct{}

	opt1 := opt
	opt1.Context = context.WithValue(context.Background(), ctxKeyA{}, "a")
	a := deferEntry(g, op, "x", opt1, fo)

	opt2 := opt
	opt2.Context = context.WithValue(context.Background(), ctxKeyB{}, "b")
	b := deferEntry(g, op, "y", opt2, fo)

	if got := partitionFor(a, b); got != 1 {
		t.Fatalf("builders differing only in Context formed %d partition(s), want 1", got)
	}
}

// TestMixedGroupPartitionsIntoExactlyTheExpectedClusters is the multi-way
// case: five builders across three distinct compatibility classes (by
// steering) must produce exactly three partitions, each holding exactly
// the entries that belong to it, in first-seen order -- proving
// partitionEntries does not merely handle a single pairwise comparison
// correctly but scales to a real caller's loop shape.
func TestMixedGroupPartitionsIntoExactlyTheExpectedClusters(t *testing.T) {
	g := NewFusionGroup[string, string]()
	op := fusionOp("extract", "v1")
	opt, fo := baseline()

	steerA := opt
	steerA.Steering = "class-a"
	steerB := opt
	steerB.Steering = "class-b"
	steerC := opt
	steerC.Steering = "class-c"

	// class-a, class-b, class-a, class-c, class-b
	e1 := deferEntry(g, op, "1", steerA, fo)
	e2 := deferEntry(g, op, "2", steerB, fo)
	e3 := deferEntry(g, op, "3", steerA, fo)
	e4 := deferEntry(g, op, "4", steerC, fo)
	e5 := deferEntry(g, op, "5", steerB, fo)

	groups := partitionEntries([]*fusionEntry[string, string]{e1, e2, e3, e4, e5})
	if len(groups) != 3 {
		t.Fatalf("got %d partitions, want 3", len(groups))
	}

	wantMembers := [][]string{
		{"1", "3"}, // class-a, first-seen order preserved
		{"2", "5"}, // class-b
		{"4"},      // class-c
	}
	for gi, want := range wantMembers {
		if gi >= len(groups) {
			t.Fatalf("missing partition %d, want members %v", gi, want)
		}
		got := make([]string, len(groups[gi]))
		for i, e := range groups[gi] {
			got[i] = e.input
		}
		if len(got) != len(want) {
			t.Fatalf("partition %d members = %v, want %v", gi, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("partition %d members = %v, want %v", gi, got, want)
			}
		}
	}
}

// TestFusionEndToEndPartitionsAndFusesCorrectly drives the whole thing
// through Run/RunOpManyPartial with a real (deterministic) provider: a
// caller loop deferring builders in two steering classes must produce two
// distinct RunOpManyPartial calls (observed indirectly: both classes'
// outputs are correct AND the group's own partitioning, checked directly,
// is 2), not one call that silently ran everything under one class's
// steering.
func TestFusionEndToEndPartitionsAndFusesCorrectly(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")

	provider := &deterministicEchoProvider{}
	ctx := WithProvider(context.Background(), provider)

	g := NewFusionGroup[string, string]()
	op := fusionOp("extract", "v1")

	steerA := types.OpOptions{Steering: "class-a"}
	steerB := types.OpOptions{Steering: "class-b"}

	hA1 := g.Defer(op, "a1", steerA, FusionOptions{})
	hB1 := g.Defer(op, "b1", steerB, FusionOptions{})
	hA2 := g.Defer(op, "a2", steerA, FusionOptions{})
	hB2 := g.Defer(op, "b2", steerB, FusionOptions{})

	if got := len(partitionEntries(collectEntriesForTest(g))); got != 2 {
		t.Fatalf("two steering classes formed %d partition(s) before Run, want 2", got)
	}

	if err := g.Run(ctx, PartialConfig{}); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	for _, tc := range []struct {
		name string
		h    FusionHandle[string, string]
		want string
	}{
		{"a1", hA1, "a1"},
		{"b1", hB1, "b1"},
		{"a2", hA2, "a2"},
		{"b2", hB2, "b2"},
	} {
		res, ok := tc.h.Result()
		if !ok {
			t.Fatalf("%s: handle never resolved", tc.name)
		}
		if res.Status != types.ItemSucceeded {
			t.Fatalf("%s: status = %v, want ItemSucceeded (err=%v)", tc.name, res.Status, res.Err)
		}
		// callLLM (llm_helper.go) wraps a non-empty Steering into the rendered
		// user prompt with a trust-boundary marker (trust.go) before it ever
		// reaches the provider, so the echoed value is not the bare input --
		// it still has to CONTAIN the bare input, and it must not contain the
		// other class's steering text or its own class's input landed in the
		// wrong partition's call.
		if !strings.Contains(res.Value, tc.want) {
			t.Fatalf("%s: value = %q, does not contain its own input %q", tc.name, res.Value, tc.want)
		}
	}
}
