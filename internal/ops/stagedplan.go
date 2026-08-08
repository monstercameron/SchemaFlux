package ops

import (
	"context"
	"fmt"
	"sync"

	"github.com/monstercameron/schemaflux/internal/types"
)

// PL-010. SDMP staged plans over one datum, with reuse.
//
// The task names four things a staged plan has to do that a caller writing
// "call extract, then call verify" by hand does not get for free:
//
//   - pass the STRUCTURED output of one stage into the next, rather than
//     resending the original source -- Stage[T] fixes In and Out to the same
//     T ("one datum"), so a stage's input is literally the previous stage's
//     Value, never anything reconstructed from the source.
//   - reuse deterministic preprocessing and schema artifacts across stages --
//     RunStagedPlan takes ONE types.OpOptions and passes it, unmodified except
//     for ParentResultIDs, to every stage. A caller who set SchemaID,
//     CacheIdentity, or JSONSchema once does not rebuild them per stage, and
//     every stage's provider request shares the same cache-key material
//     (llm_helper.go's promptCacheKeyFor reads exactly these fields).
//   - run independent checks concurrently under one budget -- a PlanStep with
//     more than one Stage runs them concurrently, and StagedPlan.Scheduler,
//     when set, is the SAME *Scheduler for every stage in the plan, so
//     MaxConcurrent/MaxInFlightCost/MaxInFlightTokens bound the whole plan's
//     provider calls together, not one ceiling per group.
//   - skip the model stage entirely when a deterministic check already
//     establishes the required contract -- Stage.DeterministicCheck, below.
//     This is the clause a plan that always calls its Op has not implemented,
//     which is why stagedplan_reuse_test.go proves it by provider CALL COUNT,
//     not by inspecting the returned value: a call that happened to agree
//     with the deterministic answer is indistinguishable from a skip by any
//     other observation.
//
// What this does not do: choose among several stages' outputs to build a new
// datum (see PlanStep's doc comment), fuse or reorder stages (that is
// PL-012), or recover a failed stage by retrying with a smaller unit of work
// (that is PL-009, over a different execution path -- RunOpManyRecover -- this
// file does not touch). A stage that fails ends the plan; there is no partial
// staged-plan result.

// Stage is one step of a StagedPlan over datum type T.
//
// ID names the stage itself, distinct from Op.ID: Op.ID identifies the model
// contract a stage enforces when it actually calls the model, and two stages
// in the same plan can share one Op (the same extraction contract applied
// twice to two different slices of a pipeline) while still needing to be
// told apart in the run's lineage. An unset ID defaults to Op.ID (see
// runStage) -- the ordinary case, where the stage IS the operation and there
// is nothing else to name it.
type Stage[T any] struct {
	ID types.OperationID
	Op Op[T, T]

	// DeterministicCheck is a pure Go function that decides, without calling
	// a model, whether T already satisfies this stage's required contract.
	// It returns the value to use (normally input itself, unmodified, but a
	// check that also normalizes -- trims, canonicalizes -- may return an
	// adjusted value), the types.ContractLevel the check can actually attest
	// to having verified, and whether it was able to decide at all.
	//
	// ok=false means the check could not determine the answer (not "the
	// answer is bad" -- that is level being too low with ok=true) and Op
	// runs as if no check existed. ok=true with level below RequiredContract
	// means the check ran and found the contract not yet established, so Op
	// still runs -- the check is not a veto, it is a shortcut when one
	// applies. Only ok=true with level >= RequiredContract skips Op.
	//
	// A nil DeterministicCheck means the stage always calls Op -- correct
	// for a stage this library has no deterministic way to verify, which is
	// most operations most of the time. AGENTS.md's "decide locally what can
	// be decided locally" is the rule this field exists to let a caller
	// follow at the stage level rather than only inside one Op's Invariants.
	DeterministicCheck func(T) (T, types.ContractLevel, bool)

	// RequiredContract is the level DeterministicCheck must attest to reach
	// before Op is skipped. The zero value, types.ContractPromptOnly, means
	// "unstated" and is read as declaredContractLevel(Op.Contract) --
	// whatever level Op itself would deliver on a call -- rather than as a
	// literal floor of "the model was asked nicely", which every check would
	// trivially clear and defeat the point of stating a requirement at all.
	// A caller wanting the literal floor sets it explicitly; RunStagedPlan
	// cannot tell that apart from "unstated" and does not try to.
	RequiredContract types.ContractLevel
}

// PlanStep groups the stages that run at one point in a StagedPlan.
//
// A step with exactly one Stage runs it against the plan's current datum,
// and its output becomes the new current datum for the next step -- the
// ordinary, sequential case.
//
// A step with more than one Stage is PL-010's "independent checks": every
// stage in the group receives the SAME current datum, runs concurrently
// (through StagedPlan.Scheduler when one is set), and every stage must
// succeed for the plan to continue. A group does not choose among its
// stages' outputs to build a new datum -- there is no general rule for
// merging N independently-computed values of an arbitrary T, and inventing
// one (first stage wins, last stage wins) would be a silent, undocumented
// tie-break exactly where this library elsewhere refuses to guess. The
// datum entering the step after a group is the SAME datum that entered the
// group. A plan needing to both check something concurrently and change the
// datum from what it learns is two steps: a group, then a single-stage step
// whose Op or DeterministicCheck reads what it needs from the datum the
// group already validated.
type PlanStep[T any] struct {
	Stages []Stage[T]
}

// StagedPlan is an ordered sequence of steps that all operate on one datum.
//
// ID identifies the plan for error messages and for SubmitRequest.Tenant
// when Scheduler is set (below) -- every stage in the same plan shares one
// tenant bucket, which is what makes Scheduler's per-tenant concurrency
// limit describe the whole plan rather than one stage of it.
type StagedPlan[T any] struct {
	ID    types.OperationID
	Steps []PlanStep[T]

	// Scheduler bounds every stage's provider call -- solo or grouped --
	// under one shared admission budget. Nil runs every stage (and every
	// stage within a concurrent group) with no such ceiling: the plan still
	// runs stages concurrently within a group, just without a bound on how
	// many, which is the correct behavior for a caller who wants the
	// concurrency but has not chosen to cap it. A caller who wants a real
	// ceiling constructs one *Scheduler and sets it here -- constructing a
	// fresh one per group would give each group its own budget, which is
	// the opposite of what "one budget" (the task's own words) asks for.
	Scheduler *Scheduler
}

// StageOutcome is what one Stage produced.
type StageOutcome[T any] struct {
	StageID types.OperationID
	Value   T

	// Skipped is true when DeterministicCheck already established the
	// stage's required contract, so Op was never called. This is the fact
	// PL-010's verify line asks a provider call count to prove; Skipped is
	// this package's own record of the same fact, so a caller does not have
	// to infer it indirectly from Meta.Attempts being zero.
	Skipped bool

	// DeliveredContract is the level actually established for this stage --
	// DeterministicCheck's attested level when Skipped, or
	// Meta.DeliveredContract (RunOpResult's own accounting) otherwise.
	DeliveredContract types.ContractLevel

	// Meta is the RunOpResult envelope for a stage that called Op --
	// attempts, usage, cost, contract, provenance. The zero value for a
	// skipped stage: nothing was spent establishing an answer that was
	// already known, and reporting a nonzero Attempts or Cost for a call
	// that never happened would be inventing a number AGENTS.md forbids.
	Meta types.Meta

	// Provenance is filled in for every stage, called or skipped, so a
	// caller reading outcomes does not have to reach into Meta (which is
	// zero for a skipped stage) to get the one thing every stage produces
	// regardless: TC-003 lineage naming this stage's own operation identity
	// and the parent stage(s) it followed. For a called stage this is a copy
	// of Meta.Provenance.
	Provenance types.Provenance
}

// StagedPlanResult is what running a StagedPlan produced.
type StagedPlanResult[T any] struct {
	Value T

	// Steps holds one entry per PlanStep, each holding one StageOutcome per
	// Stage in that step, in the order the plan declared them -- not the
	// order goroutines happened to finish in, for a concurrent group (see
	// runStepStages).
	Steps [][]StageOutcome[T]
}

// RunStagedPlan runs plan's steps in order against input, threading the
// datum and its lineage forward one step at a time.
//
// opt is resolved ONCE by the caller and reused for every stage's call --
// see StagedPlan's doc comment for why passing the same OpOptions to every
// stage, rather than letting each stage build its own, is the "reuse ...
// schema artifacts across stages" half of PL-010.
func RunStagedPlan[T any](ctx context.Context, plan StagedPlan[T], input T, opt types.OpOptions) (StagedPlanResult[T], error) {
	var zero StagedPlanResult[T]
	if len(plan.Steps) == 0 {
		return zero, fmt.Errorf("staged plan %s: no steps to run", plan.ID)
	}
	for i, step := range plan.Steps {
		if len(step.Stages) == 0 {
			return zero, fmt.Errorf("staged plan %s: step %d has no stages", plan.ID, i)
		}
	}

	current := input
	var parents []string
	result := StagedPlanResult[T]{Steps: make([][]StageOutcome[T], 0, len(plan.Steps))}

	for _, step := range plan.Steps {
		outcomes, err := runStepStages(ctx, plan, step, current, opt, parents)
		if err != nil {
			return zero, err
		}
		result.Steps = append(result.Steps, outcomes)

		// The next step's lineage names every stage THIS step actually ran --
		// TC-003's ParentResultIDs, one level up: a stage derived from several
		// concurrent siblings names all of them, not just the one whose value
		// happened to carry forward (which, for a group, is none of them --
		// see PlanStep's doc comment).
		parents = make([]string, 0, len(outcomes))
		for _, o := range outcomes {
			if o.Provenance.ResultID != "" {
				parents = append(parents, o.Provenance.ResultID)
			}
		}

		if len(step.Stages) == 1 {
			current = outcomes[0].Value
		}
	}

	result.Value = current
	return result, nil
}

// runStepStages runs one PlanStep's stages against datum: sequentially for a
// single stage, concurrently (each against the same datum) for a group.
//
// A group's stages are launched together and joined together, but outcomes
// is written by index so the returned slice is in the plan's declared order
// regardless of which goroutine finished first -- a caller correlating
// Steps[i][j] back to plan.Steps[i].Stages[j] must get the stage it asked
// about, not whichever one happened to answer fastest.
func runStepStages[T any](ctx context.Context, plan StagedPlan[T], step PlanStep[T], datum T, opt types.OpOptions, parents []string) ([]StageOutcome[T], error) {
	n := len(step.Stages)
	outcomes := make([]StageOutcome[T], n)

	if n == 1 {
		outcome, err := runStage(ctx, plan, step.Stages[0], datum, opt, parents)
		if err != nil {
			return nil, err
		}
		outcomes[0] = outcome
		return outcomes, nil
	}

	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i, stage := range step.Stages {
		go func(i int, stage Stage[T]) {
			defer wg.Done()
			outcome, err := runStage(ctx, plan, stage, datum, opt, parents)
			outcomes[i] = outcome
			errs[i] = err
		}(i, stage)
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return outcomes, nil
}

// runStage runs one Stage: the deterministic check first, Op only when the
// check does not already establish RequiredContract.
func runStage[T any](ctx context.Context, plan StagedPlan[T], stage Stage[T], input T, opt types.OpOptions, parents []string) (StageOutcome[T], error) {
	id := stage.ID
	if id == (types.OperationID{}) {
		id = stage.Op.ID
	}

	parentCopy := append([]string(nil), parents...)

	if stage.DeterministicCheck != nil {
		if value, level, ok := stage.DeterministicCheck(input); ok {
			required := requiredContractFor(stage)
			if level >= required {
				return StageOutcome[T]{
					StageID:           id,
					Value:             value,
					Skipped:           true,
					DeliveredContract: level,
					Provenance: types.Provenance{
						ResultID:         types.NewResultID(),
						ParentResultIDs:  parentCopy,
						InputDigest:      types.DigestValue(input),
						OperationVersion: id.String(),
						PromptVersion:    id.Version,
						ItemRecoveryPath: "deterministic check, no provider call",
						LibraryVersion:   types.Version,
					},
				}, nil
			}
			// The check ran and reports a level below what this stage
			// requires -- not an error, a fact that a shortcut does not
			// apply this time. Op runs below exactly as if no check had
			// been registered.
		}
	}

	localOpt := opt
	localOpt.ParentResultIDs = parentCopy

	runFn := func(runCtx context.Context) (types.Result[T], error) {
		return RunOpResult(runCtx, stage.Op, input, localOpt)
	}

	var result types.Result[T]
	var err error
	if plan.Scheduler != nil {
		result, err = Submit(ctx, plan.Scheduler, SubmitRequest{Tenant: plan.ID.String()}, runFn)
	} else {
		result, err = runFn(ctx)
	}
	if err != nil {
		return StageOutcome[T]{}, fmt.Errorf("staged plan %s, stage %s: %w", plan.ID, id, err)
	}

	return StageOutcome[T]{
		StageID:           id,
		Value:             result.Value,
		DeliveredContract: result.Meta.DeliveredContract,
		Meta:              result.Meta,
		Provenance:        result.Meta.Provenance,
	}, nil
}

// requiredContractFor reads stage.RequiredContract, or -- when left at the
// zero value -- what stage.Op itself declares it would deliver on a call.
// See Stage.RequiredContract's doc comment for why the zero value is not
// read literally.
func requiredContractFor[T any](stage Stage[T]) types.ContractLevel {
	if stage.RequiredContract != types.ContractPromptOnly {
		return stage.RequiredContract
	}
	return declaredContractLevel(stage.Op.Contract)
}
