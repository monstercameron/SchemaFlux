package ops

import (
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// PL-014: pre-execution alternatives (types.Plan.Alternatives, populated by
// chooseShape in plan.go).
// "An explanation nobody can act on is decoration" is TODOS.md's own line
// for why Alternatives exists: Plan.Explain() already said what ran; these
// tests are about the other half -- what did NOT run, and why not.

// --- Pre-execution: rejected alternatives.

func TestPreflightRecordsAtomicAsRejectedWhenMDSPIsChosen(t *testing.T) {
	op := widgetOp() // BatchItemwise, MDSP legal
	items := widgets(5)

	plan, err := Preflight(op, items, types.PlanRequest{})
	if err != nil {
		t.Fatalf("Preflight failed: %v", err)
	}
	if plan.Shape != types.ShapeMDSP {
		t.Fatalf("expected ShapeMDSP for 5 items with a wired batch algebra, got %s", plan.Shape)
	}
	if len(plan.Alternatives) != 1 || plan.Alternatives[0].Shape != types.ShapeAtomic {
		t.Fatalf("expected exactly one rejected alternative (Atomic), got %+v", plan.Alternatives)
	}
	if plan.Alternatives[0].Reason == "" {
		t.Error("a rejected alternative with no reason tells a caller nothing actionable")
	}
}

func TestPreflightRecordsMDSPAsRejectedWhenOneItemChoosesAtomic(t *testing.T) {
	op := widgetOp()
	items := widgets(1)

	plan, err := Preflight(op, items, types.PlanRequest{})
	if err != nil {
		t.Fatalf("Preflight failed: %v", err)
	}
	if plan.Shape != types.ShapeAtomic {
		t.Fatalf("expected ShapeAtomic for a single item, got %s", plan.Shape)
	}
	if len(plan.Alternatives) != 1 || plan.Alternatives[0].Shape != types.ShapeMDSP {
		t.Fatalf("expected MDSP recorded as a rejected (but legal) alternative for one item, got %+v", plan.Alternatives)
	}
}

func TestPreflightRecordsNoAlternativeWhenNoneWasLegal(t *testing.T) {
	op := atomicOnlyOp() // BatchNone: MDSP is never legal
	items := []string{"a", "b", "c"}

	plan, err := Preflight(op, items, types.PlanRequest{})
	if err != nil {
		t.Fatalf("Preflight failed: %v", err)
	}
	if plan.Shape != types.ShapeAtomic {
		t.Fatalf("expected ShapeAtomic for a BatchNone op, got %s", plan.Shape)
	}
	if len(plan.Alternatives) != 0 {
		t.Errorf("a BatchNone op has no legal alternative to reject; got %+v", plan.Alternatives)
	}
}

func TestPreflightRecordsEveryOtherLegalShapeAsRejectedWhenForced(t *testing.T) {
	op := widgetOp()
	items := widgets(5)

	plan, err := Preflight(op, items, NewPlanBuilder(types.OpOptions{}).Atomic().WithReason("caller distrusts batching here").Build())
	if err != nil {
		t.Fatalf("Preflight failed: %v", err)
	}
	if !plan.ShapeForced || plan.Shape != types.ShapeAtomic {
		t.Fatalf("expected a forced Atomic shape, got forced=%v shape=%s", plan.ShapeForced, plan.Shape)
	}
	if len(plan.Alternatives) != 1 || plan.Alternatives[0].Shape != types.ShapeMDSP {
		t.Fatalf("expected MDSP recorded as rejected because Atomic was forced, got %+v", plan.Alternatives)
	}
	if !strings.Contains(plan.Alternatives[0].Reason, "forced") {
		t.Errorf("a forced-shape rejection should say it was forced, got %q", plan.Alternatives[0].Reason)
	}
}

func TestExplainRendersEveryRejectedAlternativeWithItsReason(t *testing.T) {
	op := widgetOp()
	items := widgets(5)

	plan, err := Preflight(op, items, types.PlanRequest{})
	if err != nil {
		t.Fatalf("Preflight failed: %v", err)
	}

	explanation := plan.Explain()
	if !strings.Contains(explanation, "Rejected: atomic") {
		t.Errorf("Explain() does not name the rejected alternative:\n%s", explanation)
	}
	if !strings.Contains(explanation, plan.Alternatives[0].Reason) {
		t.Errorf("Explain() does not carry the rejected alternative's reason:\n%s", explanation)
	}
}

func TestPreflightAlternativesAreDeterministic(t *testing.T) {
	// PL-001's byte-identical-Serialize property extends to Alternatives:
	// two Preflight calls over identical input must agree, or a plan cache
	// keyed on Plan.Digest() would treat two runs of the same decision as
	// different plans.
	op := widgetOp()
	items := widgets(5)

	plan1, err := Preflight(op, items, types.PlanRequest{})
	if err != nil {
		t.Fatalf("Preflight failed: %v", err)
	}
	plan2, err := Preflight(op, items, types.PlanRequest{})
	if err != nil {
		t.Fatalf("Preflight failed: %v", err)
	}
	if plan1.Digest() != plan2.Digest() {
		t.Error("two Preflight calls over identical input produced different plan digests")
	}
}
