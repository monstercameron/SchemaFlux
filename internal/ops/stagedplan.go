package ops

import (
	"context"

	"github.com/monstercameron/schemaflux/internal/types"
)

// Staged plans over one datum, with reuse.
//
// A caller writing "call extract, then call verify" by hand does not get four
// things for free, and a staged plan does them:
//
//   - pass the STRUCTURED output of one stage into the next, rather than
//     resending the original source -- Stage[T] fixes In and Out to the same
//     T ("one datum"), so a stage's input is literally the previous stage's
//     Value, never anything reconstructed from the source.
//   - reuse deterministic preprocessing and schema artifacts across stages --
//     RunStagedPlan takes ONE types.OpOptions and passes it, unmodified except
//     for ParentResultIDs (the caller's own, then the previous stage's result),
//     to every stage. A caller who set SchemaID,
//     CacheIdentity, or JSONSchema once does not rebuild them per stage, and
//     every stage's provider request shares the same cache-key material
//     (llm_helper.go's promptCacheKeyFor reads exactly these fields).
//   - skip the model stage entirely when a deterministic check already
//     establishes the required contract -- Stage.DeterministicCheck, below. This
//     is the clause a plan that always calls its Op has not implemented, which is
//     why stagedplan_reuse_test.go proves it by provider CALL COUNT, not by
//     inspecting the returned value: a call that happened to agree with the
//     deterministic answer is indistinguishable from a skip by any other
//     observation.
//   - chain the lineage: each stage's result names the previous stage's result as
//     its parent.
//
// The stages run one after another on the caller's goroutine. What bounds the
// requests they make is the client's scheduler, once, in the call path: a plan
// has no scheduler of its own, because a stage that ran inside the plan's slot
// while the call it made asked the client's scheduler for another waited on
// itself whenever both were the same scheduler with one slot.
//
// What this does not do: fuse or reorder stages, or recover a failed stage by
// retrying with a smaller unit of work (that is the recovering run, over a
// different execution path, which this file does not touch). A stage that fails
// ends the plan, and no value is produced: what the stages that completed cost,
// and what the one that failed did, come back in the outcomes beside the error. A
// caller who wants several stages at once runs them with Gather over RunOpResult,
// which bounds them by the client's scheduler in the same way.

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
	// to having verified, and whether it was able to decide at all. The level
	// is what the check says: the library cannot verify a claim made by the
	// caller's own code.
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

// StagedPlan is an ordered sequence of stages that all operate on one datum.
//
// ID identifies the plan in error messages and names the plan when a stage has no
// name of its own. The plan has no scheduler of its own: the requests its stages
// make are bounded by the client's. It has no groups of stages either: what runs
// several operations at once is Gather over RunOpResult, which says what happens
// to the datum and to the first failure in the caller's own code and is bounded by
// the client's scheduler; the plan's own goroutines ran the caller's BuildPrompt,
// Decode and checks with no guard around them.
type StagedPlan[T any] struct {
	ID     types.OperationID
	Stages []Stage[T]
}

// StageOutcome is what one Stage produced.
type StageOutcome[T any] struct {
	StageID types.OperationID
	Value   T

	// Skipped is true when DeterministicCheck already established the
	// stage's required contract, so Op was never called. This is the fact a
	// provider call count proves from outside; Skipped is this package's own
	// record of the same fact, so a caller does not have to infer it indirectly
	// from Meta.Attempts being zero.
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
	// regardless: the lineage naming this stage's own operation identity
	// and the stage it followed. For a called stage this is a copy of
	// Meta.Provenance.
	Provenance types.Provenance

	// Err is why the stage failed, and nil for a stage that completed. Only the
	// last outcome of a run that failed has one, and its Value is the zero value.
	Err error
}

// StagedPlanResult is what running a StagedPlan produced.
type StagedPlanResult[T any] struct {
	// Value is what the last stage answered. It is the zero value for a run that
	// failed.
	Value T

	// Stages holds one entry per stage that ran, in the order the plan declared
	// them. For a run that failed it holds the stages that completed and then the
	// one that failed, with its Meta, so that what the run spent is not lost with
	// the value: the stages after the failed one were not run and have no entry.
	Stages []StageOutcome[T]
}

// RunStagedPlan runs plan's stages in order against input, threading the datum
// and its lineage forward one stage at a time.
//
// opt is resolved ONCE by the caller and reused for every stage's call -- every
// stage gets the same options, so a schema or a cache identity set once is not
// rebuilt per stage. The one option that differs between stages is
// ParentResultIDs: every stage names the parents the caller gave in opt, and then
// the result of the stage before it.
//
// A call that cannot be run is refused before anything is sent and before any
// function of the caller's is called, with the empty result and an error: a nil
// context, a context that is already over and a plan with no stages (an invalid
// request, or canceled or timeout for the context); a stage whose operation has no
// BuildPrompt or Decode (configuration); and a contract floor that a stage without
// a deterministic check cannot reach (a policy violation). A stage with a check is
// not held to the floor up front, because the check may settle it without asking
// the model.
//
// A stage that fails ends the plan. The error is classified with the kind of the
// stage's own failure, names the stage by its position and the name of its
// operation, and carries nothing the caller supplied as data: not the datum, and
// not the text of the stage's failure, which stays in the cause. StageOutcome.Err
// of the failed stage is that same error. The result comes back beside it with the
// outcomes of the stages that ran, the failed one included
// (StagedPlanResult.Stages), and no value. That is the one place in the package
// where a value comes back beside an error, so that what the stages that completed
// cost can be reported, and a caller that exposes this function hides it.
//
// An engine that spends money does not lose the account of it. A panic in a stage's
// BuildPrompt, Decode, invariant, Normalize or DeterministicCheck does not escape:
// it is that stage's failure, of kind invariant violation and marked as a panic,
// with no panic value in the error (the type of it is logged), and the result holds
// the stages that completed and what the failed stage cost. A function that ends
// its goroutine with runtime.Goexit is caught the same way, because the stage's
// functions run on a goroutine of the library's, and the request it was answering
// leaves no account.
//
// A stage a deterministic check settles asks nothing of the model, and still has to
// pass the client's contract floor: a check whose level is below it is read as not
// having settled the stage, and the model is asked. A level that is not one of the
// contract levels is a configuration error. The check's level is what the check
// says; the library cannot verify it.
//
// The plan is one call of the operation to an installed observer, named for the
// plan. A function of the caller's that blocks and ignores the context it cannot
// see holds the call: nothing in Go can end it.
func RunStagedPlan[T any](ctx context.Context, plan StagedPlan[T], input T, opt types.OpOptions) (_ StagedPlanResult[T], runErr error) {
	var zero StagedPlanResult[T]
	label := plan.ID.Name
	if label == "" {
		label = "staged plan"
	}
	if ctx == nil {
		return zero, types.Newf(types.KindInvalidRequest, label, "a nil context was given")
	}
	// The plan is one call to an observer, named for the plan, and the operations
	// of its stages are its children: a stage used to be an operation of its own
	// beside the others, and nothing tied the stages of one run together.
	name := plan.ID.Name
	if name == "" {
		name = "staged_plan"
	}
	ctx, observed := observeOperation(ctx, name)
	defer observed.end(&runErr)
	if over := contextOver(ctx); over != nil {
		return zero, failureOf(label, batchFailureKind(over), over, "the context had already ended, so nothing was run")
	}
	if len(plan.Stages) == 0 {
		return zero, types.Newf(types.KindInvalidRequest, label, "the plan has no stages to run")
	}
	for i, stage := range plan.Stages {
		if stage.Op.BuildPrompt == nil || stage.Op.Contract.Decode == nil {
			return zero, types.Newf(types.KindConfiguration, label, "stage %d of %d has an operation with no BuildPrompt or no Decode", i+1, len(plan.Stages))
		}
	}

	// The client every stage runs on is fixed once, so that a default replaced
	// between two stages does not give one the provider of one client and the
	// next the budget of another.
	ctx, _ = PinExec(ctx)

	// A stage that will certainly ask the model is held to the floor before the
	// first request, so that a plan whose later stage cannot reach it does not pay
	// for the earlier ones first.
	for _, stage := range plan.Stages {
		if stage.DeterministicCheck != nil {
			continue
		}
		planned := plannedContractLevel(ctx, declaredContractLevel(stage.Op.Contract), opt)
		if floorErr := checkContractFloor(ctx, planned); floorErr != nil {
			return zero, floorErr
		}
	}

	current := input
	callerParents := append([]string(nil), opt.ParentResultIDs...)
	var previous string
	result := StagedPlanResult[T]{Stages: make([]StageOutcome[T], 0, len(plan.Stages))}

	for i, stage := range plan.Stages {
		// The stage's lineage names the parents the caller gave, and the stage that
		// ran before it.
		parents := append([]string(nil), callerParents...)
		if previous != "" {
			parents = append(parents, previous)
		}

		outcome, err := runStage(ctx, plan, i, stage, current, opt, parents)
		result.Stages = append(result.Stages, outcome)
		if err != nil {
			return result, err
		}

		previous = outcome.Provenance.ResultID
		current = outcome.Value
	}

	result.Value = current
	return result, nil
}

// runStage runs one Stage: the deterministic check first, Op only when the
// check does not already establish RequiredContract at a level the client's floor
// accepts. For a stage that fails the outcome it returns holds the failure and what
// the stage spent.
//
// The caller's functions run on a goroutine of the library's (onWorker), so that a
// panic or a runtime.Goexit in one is the stage's failure, and runOpItem keeps the
// account of what a request cost when a function panics on its answer.
func runStage[T any](ctx context.Context, plan StagedPlan[T], index int, stage Stage[T], input T, opt types.OpOptions, parents []string) (StageOutcome[T], error) {
	id := stage.ID
	if id == (types.OperationID{}) {
		id = stage.Op.ID
	}

	failed := func(meta types.Meta, cause error) (StageOutcome[T], error) {
		failure := stageFailure(plan, index, id, cause)
		return StageOutcome[T]{StageID: id, Meta: meta, Provenance: meta.Provenance, Err: failure}, failure
	}

	if stage.DeterministicCheck != nil {
		var value T
		var level types.ContractLevel
		var decided bool
		if failure := onWorker(ctx, "staged plan", func() error {
			value, level, decided = stage.DeterministicCheck(input)
			return nil
		}); failure != nil {
			return failed(types.Meta{}, failure)
		}
		if decided {
			if level > types.ContractFullyGoverned {
				return failed(types.Meta{}, types.Newf(types.KindConfiguration, "staged plan", "the check attested to a contract level that is not one"))
			}
			// The level the check claims is held to the client's floor as a model's
			// answer is: below it, the stage is not settled and the model is asked.
			if level >= requiredContractFor(stage) && enforceContractPolicy(ctx, level) == nil {
				return StageOutcome[T]{
					StageID:           id,
					Value:             value,
					Skipped:           true,
					DeliveredContract: level,
					Provenance: types.Provenance{
						ResultID:         types.NewResultID(),
						ParentResultIDs:  parents,
						InputDigest:      types.DigestValue(input),
						OperationVersion: id.String(),
						PromptVersion:    id.Version,
						ItemRecoveryPath: "deterministic check, no provider call",
						LibraryVersion:   types.Version,
					},
				}, nil
			}
			// The check ran and reports a level below what this stage requires, or
			// below what the client's contract policy accepts -- not an error, a fact
			// that a shortcut does not apply this time. Op runs below exactly as if no
			// check had been registered.
		}
	}

	localOpt := opt
	localOpt.ParentResultIDs = parents

	var result types.Result[T]
	var err error
	if failure := onWorker(ctx, "staged plan", func() error {
		result, err = runOpItem(ctx, stage.Op, input, localOpt)
		return nil
	}); failure != nil {
		// The function ended its goroutine: whatever the request cost is lost with it.
		return failed(types.Meta{}, failure)
	}
	if err != nil {
		return failed(result.Meta, err)
	}

	return StageOutcome[T]{
		StageID:           id,
		Value:             result.Value,
		DeliveredContract: result.Meta.DeliveredContract,
		Meta:              result.Meta,
		Provenance:        result.Meta.Provenance,
	}, nil
}

// stageFailure is the error a failed stage ends the plan with: the kind of the
// stage's own failure, the stage's position and the name of its operation, and
// the failure as the cause. The text of the cause stays out of the message, since
// what a decoder or a check of the caller's reports can hold the datum.
func stageFailure[T any](plan StagedPlan[T], index int, id types.OperationID, cause error) error {
	name := id.Name
	if name == "" {
		name = plan.ID.Name
	}
	if name == "" {
		name = "staged plan"
	}
	return failureOf(name, batchFailureKind(cause), cause, "stage %d of %d failed", index+1, len(plan.Stages))
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
