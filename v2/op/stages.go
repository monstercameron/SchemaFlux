package op

import (
	"context"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// Stage is one operation in a sequential job. Its descriptor receives the
// value returned by the preceding stage, and all stages share the same type.
type Stage[T any] struct {
	// Name is the stage's Record and error name. Empty uses Descriptor.ID.Name.
	Name string
	// Descriptor is the operation performed at this stage.
	Descriptor Descriptor[T, T]
	// Check may establish the stage's result without a model request. False means
	// it could not decide; true skips only when level meets Require.
	Check func(datum T) (settled T, level schemaflux.ContractLevel, ok bool)
	// Require is the level Check must attest to before the request is skipped.
	// Zero uses the descriptor's declared contract level.
	Require schemaflux.ContractLevel
}

// RunStages passes datum through stages in order, using each answer as the next
// stage's input. Options are merged and reused for every stage. Each stage gets
// one Record named for its Name or descriptor. Failure stops the job and returns
// no value; completed and failed stage costs remain in those Records. A call
// refused before its first stage (no stages, a stage or option no call can use,
// a context that ended, a floor the stages cannot meet) sends nothing and adds
// one Record, named "op.runstages", with no requests.
func RunStages[T any](ctx context.Context, datum T, stages []Stage[T], opts ...Options) (T, error) {
	var zero T
	const operation = "op.runstages"
	call, err := bind.Begin(ctx, operation)
	if err != nil {
		return zero, err
	}
	if len(stages) == 0 {
		return zero, call.Refuse(types.KindInvalidRequest, "there are no stages")
	}
	o := mergeOptions(opts)
	common, err := checkOptions(o, operation)
	if err != nil {
		return zero, call.End(nil, err)
	}
	t := new(trap)
	internal := ops.StagedPlan[T]{ID: types.OperationID{Name: operation}, Stages: make([]ops.Stage[T], len(stages))}
	for i, stage := range stages {
		if err := checkDescriptor(stage.Descriptor, operation); err != nil {
			return zero, call.End(nil, err)
		}
		id := stage.Descriptor.ID.internal()
		if stage.Name != "" {
			id.Name = stage.Name
		}
		var check func(T) (T, types.ContractLevel, bool)
		if stage.Check != nil {
			check = func(v T) (T, types.ContractLevel, bool) {
				out, level, ok := stage.Check(v)
				return out, types.ContractLevel(level), ok
			}
		}
		internal.Stages[i] = ops.Stage[T]{ID: id, Op: lowered(stage.Descriptor, t, name(stage.Descriptor.ID, operation)), DeterministicCheck: check, RequiredContract: types.ContractLevel(stage.Require)}
	}
	callCtx := call.Context()
	opt := requestOptions(callCtx, o, common)
	type stagedRun struct {
		result ops.StagedPlanResult[T]
		err    error
	}
	run, runErr := guarded(t, operation, func() (stagedRun, error) {
		result, err := ops.RunStagedPlan(callCtx, internal, datum, opt)
		return stagedRun{result: result, err: err}, nil
	})
	if runErr == nil {
		runErr = run.err
	}
	var returned T
	if runErr == nil {
		returned = run.result.Value
	}
	for i, outcome := range run.result.Stages {
		if i >= len(stages) {
			break
		}
		stageName := stages[i].Name
		if stageName == "" {
			stageName = name(stages[i].Descriptor.ID, operation)
		}
		// A stage that ran before the context ended still has its Record.
		stageCall, beginErr := bind.Begin(context.WithoutCancel(callCtx), stageName)
		if beginErr == nil {
			stageCall.ScopeIsAnothers()
			_ = stageCall.End(&outcome.Meta, outcome.Err)
		}
	}
	if runErr != nil {
		if len(run.result.Stages) == 0 {
			// Nothing was sent: the call is refused (a context that ended, a floor
			// above what the stages establish) and no stage has a Record to hold
			// it, so the call adds the one Record of its own.
			return zero, call.End(nil, runErr)
		}
		return zero, bind.ConvertError(stageErrorOperation(stages, len(run.result.Stages)), runErr)
	}
	return returned, nil
}

func stageErrorOperation[T any](stages []Stage[T], n int) string {
	if n > 0 && n <= len(stages) {
		s := stages[n-1]
		if s.Name != "" {
			return s.Name
		}
		return name(s.Descriptor.ID, "op.runstages")
	}
	return "op.runstages"
}

// PreflightStages prices each stage without making provider requests. The second
// result lists the per-stage plans in order. Later
// stages use the initial datum as a stand-in for their unknown input, so their
// contribution is an upper bound, not a prediction of the eventual bill.
func PreflightStages[T any](ctx context.Context, datum T, stages []Stage[T], opts ...Options) (Plan, []Plan, error) {
	const operation = "op.preflightstages"
	if ctx == nil {
		return Plan{}, nil, bind.Refuse(operation, types.KindInvalidRequest, "the context is nil")
	}
	if len(stages) == 0 {
		return Plan{}, nil, bind.Refuse(operation, types.KindInvalidRequest, "there are no stages")
	}
	o := mergeOptions(opts)
	common, err := checkOptions(o, operation)
	if err != nil {
		return Plan{}, nil, bind.ConvertError(operation, err)
	}
	t := new(trap)
	internal := ops.StagedPlan[T]{ID: types.OperationID{Name: "op.runstages"}, Stages: make([]ops.Stage[T], len(stages))}
	for i, stage := range stages {
		if err := checkDescriptor(stage.Descriptor, operation); err != nil {
			return Plan{}, nil, bind.ConvertError(operation, err)
		}
		id := stage.Descriptor.ID.internal()
		if stage.Name != "" {
			id.Name = stage.Name
		}
		var check func(T) (T, types.ContractLevel, bool)
		if stage.Check != nil {
			check = func(v T) (T, types.ContractLevel, bool) {
				out, level, ok := stage.Check(v)
				return out, types.ContractLevel(level), ok
			}
		}
		internal.Stages[i] = ops.Stage[T]{ID: id, Op: lowered(stage.Descriptor, t, name(stage.Descriptor.ID, operation)), DeterministicCheck: check, RequiredContract: types.ContractLevel(stage.Require)}
	}
	staged, err := ops.PreflightStagedPlan(ctx, internal, datum, types.PlanRequest{Options: requestOptions(ctx, o, common)})
	if err != nil {
		return Plan{}, nil, bind.ConvertError(operation, err)
	}
	plan := planOf(staged.Plan)
	plan.Operation = ID{Name: "op.runstages"}
	plan.Shape = ShapeSplit
	if t.tripped() {
		plan.Eligible = false
		plan.IneligibleKind = schemaflux.ErrInternal
		plan.IneligibleReason = t.panicked(operation).Error()
	}
	perStage := make([]Plan, len(staged.Stages))
	for i, stage := range staged.Stages {
		perStage[i] = Plan{
			Operation:        ID{Name: stage.ID.Name, Version: stage.ID.Version},
			InputShape:       plan.InputShape,
			ItemCount:        1,
			Eligible:         stage.Eligible,
			IneligibleReason: stage.IneligibleReason,
			IneligibleKind:   schemaflux.Kind(bind.KindOf(stage.IneligibleKind, types.NarrowNone)),
			Shape:            ShapeAtomic,
			MaxCalls:         stage.Requests,
			EstimatedCost: CostEstimate{
				Priced: stage.Estimate.Priced, EstimatedCost: stage.Estimate.EstimatedCost,
				Currency: stage.Estimate.Currency, PricingSource: stage.Estimate.PricingSource,
				EstimatedPromptTokens:     stage.Estimate.EstimatedPromptTokens,
				EstimatedCompletionTokens: stage.Estimate.EstimatedCompletionTokens,
			},
			Policy: plan.Policy, Capability: plan.Capability, EstimatorVersion: plan.EstimatorVersion,
			PolicyDigest: plan.PolicyDigest, CapabilityDigest: plan.CapabilityDigest,
		}
		if stage.UpperBound {
			perStage[i].ShapeReason = "upper bound from the initial datum"
		}
	}
	return plan, perStage, nil
}
