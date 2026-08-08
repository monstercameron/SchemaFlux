package ops

import (
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// PL-014: pre-execution alternatives (types.Plan.Alternatives, populated by
// chooseShape in plan.go) and the post-execution decision ledger
// (types.BatchResult.Ledger, populated by RunOpManyRecover in recover.go).
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

// --- Post-execution: the decision ledger (RunOpManyRecover, recover.go).

func TestRunOpManyRecoverLedgerRecordsEveryTopLevelChunk(t *testing.T) {
	provider := newRecoverFakeProvider("fake-model")
	ctx := recoverCtx(t, provider)

	items := recoverWidgets(25)
	cfg := RecoverConfig{MaxItemsPerCall: 10, StartItemsPerCall: 10}

	result, err := RunOpManyRecover(ctx, recoverWidgetOp(), items, recoverCodec(), types.OpOptions{}, cfg)
	if err != nil {
		t.Fatalf("RunOpManyRecover failed: %v", err)
	}
	if !result.Complete() {
		t.Fatalf("expected every item to resolve on the compliant path, got %s", result.String())
	}

	// 25 items at chunk size 10 is 3 top-level chunks (10, 10, 5); the
	// ledger must have one entry per chunk, each with a non-empty reason.
	if len(result.Ledger.Entries) != 3 {
		t.Fatalf("expected 3 ledger entries (one per top-level chunk), got %d: %+v", len(result.Ledger.Entries), result.Ledger.Entries)
	}
	for _, e := range result.Ledger.Entries {
		if e.Reason == "" {
			t.Errorf("ledger entry %+v has no reason -- an adaptive decision that cannot explain itself is unauditable", e)
		}
	}
}

func TestRunOpManyRecoverLedgerRecordsAnAtomicFallbackEntry(t *testing.T) {
	provider := newRecoverFakeProvider("fake-model")
	provider.batchScript = []recoverBatchHandler{
		omitBatch(itemID(0)), // pass 1: omit item 0
		omitBatch(itemID(0)), // isolate pass: still omit item 0
	}
	ctx := recoverCtx(t, provider)

	items := recoverWidgets(3)
	result, err := RunOpManyRecover(ctx, recoverWidgetOp(), items, recoverCodec(), types.OpOptions{}, RecoverConfig{})
	if err != nil {
		t.Fatalf("RunOpManyRecover failed: %v", err)
	}
	if result.Summary.Succeeded != 3 {
		t.Fatalf("expected all 3 items to eventually resolve (2 via MDSP, 1 via atomic fallback), got %d succeeded", result.Summary.Succeeded)
	}

	found := false
	for _, e := range result.Ledger.Entries {
		if strings.Contains(e.Node, "atomic fallback") {
			found = true
			if e.Reason == "" {
				t.Error("the atomic fallback ledger entry has no reason")
			}
		}
	}
	if !found {
		t.Errorf("expected a ledger entry naming the atomic fallback stage, got %+v", result.Ledger.Entries)
	}
}

func TestRunOpManyPartialLeavesTheLedgerEmpty(t *testing.T) {
	// RunOpManyPartial (partial.go) makes no adaptive sizing decisions --
	// one call per item, unconditionally -- so its BatchResult.Ledger must
	// stay at its honest zero value rather than reporting fabricated
	// entries for decisions that were never made.
	provider := newRecoverFakeProvider("fake-model")
	ctx := recoverCtx(t, provider)

	items := recoverWidgets(3)
	result, err := RunOpManyPartial(ctx, recoverWidgetOp(), items, types.OpOptions{}, PartialConfig{Policy: types.PolicyCollectFailures})
	if err != nil {
		t.Fatalf("RunOpManyPartial failed: %v", err)
	}
	if len(result.Ledger.Entries) != 0 {
		t.Errorf("RunOpManyPartial should leave Ledger empty, got %+v", result.Ledger.Entries)
	}
	if result.Ledger.Explain() == "" {
		t.Error("an empty ledger's Explain() should still return a readable (non-empty) string")
	}
}

func TestDecisionLedgerExplainRendersEntriesInOrder(t *testing.T) {
	var ledger types.DecisionLedger
	ledger.Record("chunk 1", "chunk size held at 20", "compliant (1/3 before growing)")
	ledger.Record("chunk 2", "chunk size changed from 20 to 10", "halved from 20 to 10 after omission")

	explanation := ledger.Explain()
	firstIdx := strings.Index(explanation, "chunk 1")
	secondIdx := strings.Index(explanation, "chunk 2")
	if firstIdx == -1 || secondIdx == -1 || firstIdx > secondIdx {
		t.Errorf("ledger entries are not rendered in order:\n%s", explanation)
	}
	if !strings.Contains(explanation, "halved from 20 to 10 after omission") {
		t.Errorf("Explain() dropped an entry's reason:\n%s", explanation)
	}
}
