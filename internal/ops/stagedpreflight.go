package ops

import (
	"context"
	"fmt"

	"github.com/monstercameron/schemaflux/internal/types"
)

// A staged plan can be planned before it is paid for. PreflightStagedPlan says,
// for each stage, what will be asked, in how many requests, and at what estimated
// cost, with no provider call, through the estimator Preflight uses (planOperation)
// and not a second one: a stage is priced as the operation it is, over the datum.

// StagePreflight is what one stage of a staged plan will ask, before anything is
// asked.
type StagePreflight struct {
	// ID names the stage as a run names it: its own ID, or its operation's when it
	// has none.
	ID types.OperationID

	// Eligible is whether the stage can be run and priced. When it cannot,
	// IneligibleReason says why in words that name nothing the caller's functions
	// produced, and IneligibleKind says what sort of refusal it is. A stage that
	// is not eligible has no requests and no estimate.
	Eligible         bool
	IneligibleReason string
	IneligibleKind   types.ErrorKind

	// Requests is the most requests the stage will make.
	Requests int

	// Estimate is what those requests are expected to cost, by the estimator
	// Preflight uses.
	Estimate types.CostEstimate

	// UpperBound is true when the stage's input does not exist yet, so Estimate is
	// from the datum the job started with and not from what the stage before it
	// will answer. Only the first stage is asked about the datum itself. The
	// estimate is also counted whether or not the stage is settled by a check, so
	// what a run spends on such a stage is at most this.
	UpperBound bool

	// MaySettleWithoutAsking is true for a stage with a deterministic check, which
	// may settle it without a request. Estimate counts the request regardless: the
	// check is not run by a plan, and what it will find is not known until the
	// stage before it has answered.
	MaySettleWithoutAsking bool
}

// StagedPreflight is the plan for a staged job, and what it holds for each stage,
// in the order a run takes them.
type StagedPreflight struct {
	Plan   types.Plan
	Stages []StagePreflight
}

// PreflightStagedPlan plans taking datum through the stages of plan without
// asking a model anything: the plan's shape is ShapeSDMP, its call ceiling is the
// sum of the stages' requests, and its estimate is the sum of theirs.
//
// Each stage is planned as Preflight plans one operation over one item, the datum,
// for the client ctx carries (PinExec, and the route it names, as PreflightFor
// does): what BuildPrompt renders from it, and the model's ceiling for the answer.
// The first stage is asked about the datum itself. A later stage's input is the
// answer of the stage before it, which does not exist yet, so it is priced from
// the datum as an upper bound and says so (StagePreflight.UpperBound). No stage's
// check is run, and every stage with one is still counted as a request.
//
// req.Options are the options every stage is run with. req.Force may be the
// staged shape or nothing, and a positive req.Budget bounds the sum of the
// estimates, as it bounds a single operation's.
//
// A stage that cannot be rendered or run makes the plan ineligible, with the
// reason and the kind of the first such stage, and says which stage it is by its
// place and its name, never by anything its functions produced. Only a plan that
// cannot be asked about at all is an error: a nil context or no stages, which is
// ErrInvalidRequest, with nothing beside it.
//
// It emits no plan metrics: they count a plan's chunks, and a staged plan has
// none.
func PreflightStagedPlan[T any](ctx context.Context, plan StagedPlan[T], datum T, req types.PlanRequest) (StagedPreflight, error) {
	if ctx == nil {
		return StagedPreflight{}, types.Newf(types.KindInvalidRequest, "staged_plan", "a staged plan needs a context to be planned for, and none was given")
	}
	if len(plan.Stages) == 0 {
		return StagedPreflight{}, types.Newf(types.KindInvalidRequest, "staged_plan", "staged plan %s: no stages to plan", plan.ID)
	}
	total := len(plan.Stages)

	// The client is read once, so that every stage is planned for the same
	// provider and model whatever happens to the default meanwhile.
	ctx, _ = PinExec(ctx)

	staged := types.Plan{
		Operation:        plan.ID,
		ItemCount:        1,
		InputShape:       types.DescribeValue(datum),
		EstimatorVersion: estimatorVersion,
		Policy:           types.PolicySnapshotFrom(req.Options),
		Capability:       capabilitySnapshot(ctx, req.Options),
	}
	staged.PolicyDigest = staged.Policy.Digest()
	staged.CapabilityDigest = staged.Capability.Digest()

	if req.Force != types.ShapeUnspecified && req.Force != types.ShapeSDMP {
		refuse(&staged, &planRefusal{types.KindConfiguration, fmt.Sprintf(
			"forced shape %s is not legal for a staged plan: its shape is %s, one datum through several operations", req.Force, types.ShapeSDMP)})
		return StagedPreflight{Plan: staged}, nil
	}

	// A stage is planned as one operation over one item, with the options the run
	// gives it and none of the request's shape or budget: the budget is the job's
	// and is held to the sum below, and one item is never batched.
	stageRequest := types.PlanRequest{Options: req.Options}
	input := []T{datum}

	stages := make([]StagePreflight, 0, total)
	var refusal *planRefusal
	for i, stage := range plan.Stages {
		id := stage.ID
		if id == (types.OperationID{}) {
			id = stage.Op.ID
		}
		planned := planOperation(ctx, stage.Op, input, stageRequest, false)
		entry := StagePreflight{
			ID:                     id,
			Eligible:               planned.Eligible,
			IneligibleReason:       planned.IneligibleReason,
			IneligibleKind:         planned.IneligibleKind,
			Requests:               planned.MaxCalls,
			Estimate:               planned.EstimatedCost,
			UpperBound:             i > 0,
			MaySettleWithoutAsking: stage.DeterministicCheck != nil,
		}
		stages = append(stages, entry)
		if !entry.Eligible && refusal == nil {
			refusal = &planRefusal{entry.IneligibleKind, fmt.Sprintf("stage %d of %d (%s): %s", i+1, total, id, entry.IneligibleReason)}
		}
	}
	if refusal != nil {
		refuse(&staged, refusal)
		return StagedPreflight{Plan: staged, Stages: stages}, nil
	}

	staged.Shape = types.ShapeSDMP
	staged.ShapeForced = req.Force == types.ShapeSDMP
	switch {
	case !staged.ShapeForced:
		staged.ShapeReason = fmt.Sprintf("one datum through %d stage(s), each asked about the answer of the one before", total)
	case req.ForceReason != "":
		staged.ShapeReason = req.ForceReason
	default:
		staged.ShapeReason = "caller forced this shape"
	}
	staged.Eligible = true

	// The estimate of the job is the stages' summed. It is priced only when every
	// stage is: a sum with an unpriced part says nothing about cost, and is not a
	// cost of whatever the priced parts come to.
	estimate := types.CostEstimate{Priced: true}
	for _, stage := range stages {
		staged.MaxCalls += stage.Requests
		estimate.EstimatedPromptTokens += stage.Estimate.EstimatedPromptTokens
		estimate.EstimatedCompletionTokens += stage.Estimate.EstimatedCompletionTokens
		if !stage.Estimate.Priced {
			estimate.Priced = false
			continue
		}
		estimate.EstimatedCost += stage.Estimate.EstimatedCost
		estimate.Currency = stage.Estimate.Currency
		estimate.PricingSource = stage.Estimate.PricingSource
	}
	if !estimate.Priced {
		estimate = types.CostEstimate{
			EstimatedPromptTokens:     estimate.EstimatedPromptTokens,
			EstimatedCompletionTokens: estimate.EstimatedCompletionTokens,
		}
	}
	staged.EstimatedCost = estimate

	holdToBudget(&staged, req.Budget)
	return StagedPreflight{Plan: staged, Stages: stages}, nil
}
