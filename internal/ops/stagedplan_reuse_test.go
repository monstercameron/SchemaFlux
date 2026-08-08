package ops

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// PL-010's own verify line: "a two-stage extract-then-verify plan sends the
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
	if !result.Steps[1][0].Skipped {
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
	if result.Steps[1][0].Skipped {
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
	if result.Steps[1][0].Skipped {
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
	if result.Steps[1][0].Skipped {
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

	verify := result.Steps[1][0]
	if verify.Meta.Attempts != 0 {
		t.Fatalf("skipped stage Meta.Attempts = %d, want 0", verify.Meta.Attempts)
	}
	if verify.Meta.Cost.TotalCost != 0 {
		t.Fatalf("skipped stage Meta.Cost.TotalCost = %v, want 0", verify.Meta.Cost.TotalCost)
	}
	if verify.Provenance.ResultID == "" {
		t.Fatal("skipped stage produced no ResultID")
	}
	extractID := result.Steps[0][0].Provenance.ResultID
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
		Steps: []PlanStep[testDatum]{
			{Stages: []Stage[testDatum]{{Op: extractOp}}},
			{Stages: []Stage[testDatum]{{Op: verifyOp}}},
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

// 7. Concurrent stages in a group, submitted through the SAME *Scheduler,
// are bounded by ONE shared admission budget -- MaxConcurrent: 1 serializes
// them deterministically (Scheduler.Submit only admits the second stage
// after the first's run releases its slot; there is no timing involved).
func TestRunStagedPlan_SchedulerBoundsGroupConcurrencyUnderOneBudget(t *testing.T) {
	checkA := Op[testDatum, testDatum]{
		ID:          types.OperationID{Name: "check-a"},
		Contract:    OutputContract[testDatum]{SchemaName: "testDatum", Decode: decodeTestDatum},
		BuildPrompt: func(input testDatum, opt types.OpOptions) (string, string) { return "a", input.Name },
	}
	checkB := Op[testDatum, testDatum]{
		ID:          types.OperationID{Name: "check-b"},
		Contract:    OutputContract[testDatum]{SchemaName: "testDatum", Decode: decodeTestDatum},
		BuildPrompt: func(input testDatum, opt types.OpOptions) (string, string) { return "b", input.Name },
	}

	provider := &stagedFakeProvider{
		sleep:   20 * time.Millisecond,
		respond: func(call int, req llm.CompletionRequest) string { return `{"name":"bounded"}` },
	}
	ctx := WithProvider(context.Background(), provider)

	sched := NewScheduler(SchedulerLimits{MaxConcurrent: 1})
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = sched.Close(closeCtx)
	})

	plan := StagedPlan[testDatum]{
		ID:        types.OperationID{Name: "bounded-group"},
		Scheduler: sched,
		Steps: []PlanStep[testDatum]{
			{Stages: []Stage[testDatum]{{Op: checkA}, {Op: checkB}}},
		},
	}

	if _, err := RunStagedPlan(ctx, plan, testDatum{Name: "seed"}, types.OpOptions{Model: "test-model"}); err != nil {
		t.Fatalf("RunStagedPlan: %v", err)
	}

	provider.mu.Lock()
	maxConc := provider.maxConc
	calls := provider.calls
	provider.mu.Unlock()

	if calls != 2 {
		t.Fatalf("provider called %d times, want 2 (both stages must still run)", calls)
	}
	if maxConc != 1 {
		t.Fatalf("observed max concurrency %d, want 1: one Scheduler with MaxConcurrent:1 must serialize the whole group under one budget", maxConc)
	}
}

// 8. Without a Scheduler, the same group's two stages genuinely run at the
// same time -- proved deterministically with a barrier that only releases
// once BOTH stages' Complete calls have started, rather than by timing.
// This is the contrast case for test 7: it shows the plan's default
// behavior really is concurrent, so test 7's MaxConcurrent:1 result is the
// Scheduler doing something, not the plan being sequential all along.
func TestRunStagedPlan_GroupIsConcurrentWithoutAScheduler(t *testing.T) {
	var barrier sync.WaitGroup
	barrier.Add(2)

	checkA := Op[testDatum, testDatum]{
		ID:          types.OperationID{Name: "check-a"},
		Contract:    OutputContract[testDatum]{SchemaName: "testDatum", Decode: decodeTestDatum},
		BuildPrompt: func(input testDatum, opt types.OpOptions) (string, string) { return "a", input.Name },
	}
	checkB := Op[testDatum, testDatum]{
		ID:          types.OperationID{Name: "check-b"},
		Contract:    OutputContract[testDatum]{SchemaName: "testDatum", Decode: decodeTestDatum},
		BuildPrompt: func(input testDatum, opt types.OpOptions) (string, string) { return "b", input.Name },
	}

	provider := &stagedFakeProvider{
		barrier: &barrier,
		respond: func(call int, req llm.CompletionRequest) string { return `{"name":"concurrent"}` },
	}
	ctx := WithProvider(context.Background(), provider)

	plan := StagedPlan[testDatum]{
		ID: types.OperationID{Name: "unbounded-group"}, // no Scheduler
		Steps: []PlanStep[testDatum]{
			{Stages: []Stage[testDatum]{{Op: checkA}, {Op: checkB}}},
		},
	}

	done := make(chan error, 1)
	go func() {
		_, err := RunStagedPlan(ctx, plan, testDatum{Name: "seed"}, types.OpOptions{Model: "test-model"})
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunStagedPlan: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("group did not run concurrently: the barrier (which needs both stages in flight at once) never released")
	}
}
