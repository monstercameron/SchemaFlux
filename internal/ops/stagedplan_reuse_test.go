package ops

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// The verify line of a staged plan: "a two-stage extract-then-verify plan sends the
// source once; a case where the deterministic check suffices makes ONE
// provider call, not two." This file is that proof, plus the rest of the
// task's reuse and concurrency clauses -- everything stagedplan_test.go's
// fixtures (extractThenVerifyPlan, stagedFakeProvider, respondExtractThenVerify)
// exist to make checkable without a second, parallel set of test plumbing.

// alwaysVerified is a DeterministicCheck that always finds the contract
// already established, at a level strong enough to satisfy
// declaredContractLevel(newVerifyStageOp().Contract) --
// ContractSchemaAndInvariantChecked, since that Op declares both a schema
// name and an invariant.
func alwaysVerified(d testDatum) (testDatum, types.ContractLevel, bool) {
	d.Verified = true
	return d, types.ContractSchemaAndInvariantChecked, true
}

// insufficientCheck runs (ok=true) but reports a level below what the verify
// stage requires -- the "check ran and the answer just isn't good enough
// yet" case, not "the check could not decide". Op must still run.
func insufficientCheck(d testDatum) (testDatum, types.ContractLevel, bool) {
	return d, types.ContractJSONWellFormed, true
}

// inapplicableCheck reports it could not decide at all (ok=false). Op must
// run exactly as if no check were registered.
func inapplicableCheck(d testDatum) (testDatum, types.ContractLevel, bool) {
	return testDatum{}, types.ContractPromptOnly, false
}

// 1. The verify line itself: the deterministic check suffices, so the plan
// makes ONE provider call (extract's), not two. This is the case a plan
// that always calls its model has not implemented -- the value alone cannot
// distinguish "skipped" from "called and happened to agree", which is why
// the assertion is on provider.callCount(), not on result.Value.
func TestRunStagedPlan_DeterministicCheckSkipsSecondProviderCall(t *testing.T) {
	provider := &stagedFakeProvider{respond: respondExtractThenVerify}
	ctx := WithProvider(context.Background(), provider)

	plan := extractThenVerifyPlan(alwaysVerified)
	result, err := RunStagedPlan(ctx, plan, testDatum{Source: "raw source text"}, types.OpOptions{Model: "test-model"})
	if err != nil {
		t.Fatalf("RunStagedPlan: %v", err)
	}

	if got := provider.callCount(); got != 1 {
		t.Fatalf("provider called %d times, want exactly 1 (extract only; verify's deterministic check should have skipped its model call)", got)
	}
	if !result.Stages[1].Skipped {
		t.Fatal("verify stage's StageOutcome.Skipped = false, want true")
	}
	if result.Value.Name != "Alice" || !result.Value.Verified {
		t.Fatalf("result.Value = %+v, want Name=Alice Verified=true", result.Value)
	}
}

// 2. Without a check, the same plan makes TWO provider calls -- the
// necessary contrast case: it proves test 1's "one call" is because the
// check fired, not because the plan only ever calls extract by accident of
// how the fixture is wired.
func TestRunStagedPlan_NoDeterministicCheckCallsBothStages(t *testing.T) {
	provider := &stagedFakeProvider{respond: respondExtractThenVerify}
	ctx := WithProvider(context.Background(), provider)

	plan := extractThenVerifyPlan(nil)
	result, err := RunStagedPlan(ctx, plan, testDatum{Source: "raw source text"}, types.OpOptions{Model: "test-model"})
	if err != nil {
		t.Fatalf("RunStagedPlan: %v", err)
	}

	if got := provider.callCount(); got != 2 {
		t.Fatalf("provider called %d times, want exactly 2 (no check registered, so verify must call its model)", got)
	}
	if result.Stages[1].Skipped {
		t.Fatal("verify stage's StageOutcome.Skipped = true with no DeterministicCheck registered")
	}
}

// 3. A check that runs but reports a level below what the stage requires is
// not a veto -- Op still runs, and the plan still makes two calls. This is
// what tells "the check decided the answer isn't good enough" apart from
// "the check could not decide at all" (test 4): both must fall through to
// Op, but for different reasons, and a caller reading Skipped needs both
// paths to behave identically even though the check's internal reasoning
// differed.
func TestRunStagedPlan_InsufficientCheckLevelFallsThroughToModel(t *testing.T) {
	provider := &stagedFakeProvider{respond: respondExtractThenVerify}
	ctx := WithProvider(context.Background(), provider)

	plan := extractThenVerifyPlan(insufficientCheck)
	result, err := RunStagedPlan(ctx, plan, testDatum{Source: "raw source text"}, types.OpOptions{Model: "test-model"})
	if err != nil {
		t.Fatalf("RunStagedPlan: %v", err)
	}

	if got := provider.callCount(); got != 2 {
		t.Fatalf("provider called %d times, want exactly 2 (check level too low to skip)", got)
	}
	if result.Stages[1].Skipped {
		t.Fatal("Skipped = true despite the check reporting a level below RequiredContract")
	}
}

// 4. A check that cannot decide (ok=false) also falls through to Op, and
// the plan still makes two calls.
func TestRunStagedPlan_InapplicableCheckFallsThroughToModel(t *testing.T) {
	provider := &stagedFakeProvider{respond: respondExtractThenVerify}
	ctx := WithProvider(context.Background(), provider)

	plan := extractThenVerifyPlan(inapplicableCheck)
	result, err := RunStagedPlan(ctx, plan, testDatum{Source: "raw source text"}, types.OpOptions{Model: "test-model"})
	if err != nil {
		t.Fatalf("RunStagedPlan: %v", err)
	}

	if got := provider.callCount(); got != 2 {
		t.Fatalf("provider called %d times, want exactly 2 (check could not decide)", got)
	}
	if result.Stages[1].Skipped {
		t.Fatal("Skipped = true despite the check reporting ok=false")
	}
}

// 5. A skipped stage's Meta is the honest zero value -- no attempts, no
// cost, nothing invented for a call that never happened -- while its
// Provenance is real: a ResultID, and ParentResultIDs naming the stage(s)
// before it. AGENTS.md's "never invent numbers" applies to a skipped
// stage's envelope exactly as it does to a called one's.
func TestRunStagedPlan_SkippedStageHasZeroMetaButRealLineage(t *testing.T) {
	provider := &stagedFakeProvider{respond: respondExtractThenVerify}
	ctx := WithProvider(context.Background(), provider)

	plan := extractThenVerifyPlan(alwaysVerified)
	result, err := RunStagedPlan(ctx, plan, testDatum{Source: "raw source text"}, types.OpOptions{Model: "test-model"})
	if err != nil {
		t.Fatalf("RunStagedPlan: %v", err)
	}

	verify := result.Stages[1]
	if verify.Meta.Attempts != 0 {
		t.Fatalf("skipped stage Meta.Attempts = %d, want 0", verify.Meta.Attempts)
	}
	if verify.Meta.Cost.TotalCost != 0 {
		t.Fatalf("skipped stage Meta.Cost.TotalCost = %v, want 0", verify.Meta.Cost.TotalCost)
	}
	if verify.Provenance.ResultID == "" {
		t.Fatal("skipped stage produced no ResultID")
	}
	extractID := result.Stages[0].Provenance.ResultID
	if len(verify.Provenance.ParentResultIDs) != 1 || verify.Provenance.ParentResultIDs[0] != extractID {
		t.Fatalf("skipped stage ParentResultIDs = %v, want [%s]", verify.Provenance.ParentResultIDs, extractID)
	}
	if verify.DeliveredContract != types.ContractSchemaAndInvariantChecked {
		t.Fatalf("skipped stage DeliveredContract = %v, want %v (the check's attested level)",
			verify.DeliveredContract, types.ContractSchemaAndInvariantChecked)
	}
}

// 6. Every stage's call reuses the SAME OpOptions the caller supplied --
// "reuse ... schema artifacts across stages" -- rather than each stage
// rebuilding its own. Captured via BuildPrompt closures on both stages,
// which is the only point in the whole path where a stage's Op can observe
// the resolved opt.
func TestRunStagedPlan_ReusesSameOpOptionsAcrossStages(t *testing.T) {
	var extractSeen, verifySeen types.OpOptions

	extractOp := newExtractStageOp()
	extractOp.BuildPrompt = func(input testDatum, opt types.OpOptions) (string, string) {
		extractSeen = opt
		return "extract-system", input.Source
	}
	verifyOp := newVerifyStageOp()
	verifyOp.BuildPrompt = func(input testDatum, opt types.OpOptions) (string, string) {
		verifySeen = opt
		return "verify-system", input.Name
	}

	provider := &stagedFakeProvider{respond: respondExtractThenVerify}
	ctx := WithProvider(context.Background(), provider)

	opt := types.OpOptions{
		SchemaID:      "schema-v7",
		CacheIdentity: "cache-key-abc",
		Model:         "pinned-model",
	}
	plan := StagedPlan[testDatum]{
		ID: types.OperationID{Name: "reuse"},
		Stages: []Stage[testDatum]{
			{Op: extractOp},
			{Op: verifyOp},
		},
	}

	if _, err := RunStagedPlan(ctx, plan, testDatum{Source: "x"}, opt); err != nil {
		t.Fatalf("RunStagedPlan: %v", err)
	}

	// ParentResultIDs legitimately differs per stage (that is the lineage
	// this task also asks for); every OTHER field must be byte-identical to
	// what the caller passed in -- nothing rebuilt, nothing recomputed.
	extractSeen.ParentResultIDs = nil
	verifySeen.ParentResultIDs = nil
	if extractSeen.SchemaID != opt.SchemaID || extractSeen.CacheIdentity != opt.CacheIdentity || extractSeen.Model != opt.Model {
		t.Fatalf("extract stage saw a rebuilt OpOptions: %+v", extractSeen)
	}
	if verifySeen.SchemaID != opt.SchemaID || verifySeen.CacheIdentity != opt.CacheIdentity || verifySeen.Model != opt.Model {
		t.Fatalf("verify stage saw a rebuilt OpOptions: %+v", verifySeen)
	}
}
