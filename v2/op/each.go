package op

import (
	"context"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Item is the outcome of one item passed to RunEach. It contains either an
// answer or an error, so a failed item cannot also expose a value.
type Item[Out any] struct {
	value Out
	err   error
}

// Value returns the answer, or the zero value and the error explaining why no
// answer exists. Check the error before using the value.
func (i Item[Out]) Value() (Out, error) {
	if i.err != nil {
		var zero Out
		return zero, i.err
	}
	return i.value, nil
}

// RunEach runs a descriptor on many items and returns one outcome per input, in
// input order. A caller must inspect the items: a nil run error means the work
// was collected, not that every item succeeded. Use these three lines:
//
//	items, err := op.RunEach(ctx, d, inputs)
//	if err != nil { return err }
//	for i, item := range items { value, itemErr := item.Value(); _ = i; _, _ = value, itemErr }
//
// The plan chooses one request per item or the descriptor's id batch protocol.
// Failed items have errors; successful items keep their values. Cancellation
// returns completed items and a nil error, with cancellation on unfinished items.
// A nil context, invalid descriptor or options, or an ineligible plan is an
// error before requests are sent. Each call adds one Record named for the
// descriptor or "op.runeach", containing the summed request cost.
func RunEach[In, Out any](ctx context.Context, d Descriptor[In, Out], items []In, opts ...PlanOptions) ([]Item[Out], error) {
	const operation = "op.runeach"
	if ctx == nil {
		return nil, bind.Refuse(operation, types.KindInvalidRequest, "the context is nil")
	}
	call, err := bind.Begin(ctx, name(d.ID, operation))
	if err != nil {
		return nil, err
	}
	if err := checkDescriptor(d, operation); err != nil {
		return nil, call.End(nil, err)
	}
	o := mergePlanOptions(opts)
	common, err := checkPlanOptions(o, operation)
	if err != nil {
		return nil, call.End(nil, err)
	}
	if o.Force == ShapeGlobal || o.Force == ShapeSplit {
		return nil, call.End(nil, bind.Refuse(operation, types.KindConfiguration, "the forced shape cannot run items independently"))
	}
	t := new(trap)
	internal := lowered(d, t, name(d.ID, operation))
	callCtx := call.Context()
	type runResult struct {
		result types.BatchResult[Out]
		plan   types.Plan
	}
	run, runErr := guarded(t, operation, func() (runResult, error) {
		req := planRequest(requestOptions(callCtx, o.Options, common), o)
		plan, err := ops.PreflightFor(callCtx, internal, items, req)
		if err != nil {
			return runResult{}, err
		}
		if !plan.Eligible {
			// The planner renders prompts to choose a shape. If that caller-owned
			// prompt function panics, the atomic runner can isolate the panic to
			// its item; refusing the whole planned run would discard good siblings.
			if t.tripped() {
				result, err := ops.RunOpManyPartial(callCtx, internal, items, req.Options, ops.PartialConfig{Policy: types.PolicyCollectFailures})
				plan.Shape = types.ShapeAtomic
				return runResult{result: result, plan: plan}, err
			}
			return runResult{plan: plan}, types.Newf(plan.IneligibleKind, operation, "the run plan is ineligible")
		}
		var result types.BatchResult[Out]
		if plan.Shape == types.ShapeAtomic {
			result, err = ops.RunOpManyPartial(callCtx, internal, items, req.Options, ops.PartialConfig{Policy: types.PolicyCollectFailures})
		} else if plan.Shape == types.ShapeMDSP && d.Batch.Item != nil {
			ceiling := o.MaxItemsPerCall
			if ceiling <= 0 {
				for _, c := range plan.Chunks {
					if c.ItemCount > ceiling {
						ceiling = c.ItemCount
					}
				}
			}
			result, err = ops.RunOpManyRecover(callCtx, internal, items, ops.BatchItemCodec[In, Out]{DecodeItem: d.Batch.Item}, req.Options, ops.RecoverConfig{MaxItemsPerCall: ceiling})
		} else {
			return runResult{plan: plan}, bind.Refuse(operation, types.KindConfiguration, "the planned batch does not declare an item decoder")
		}
		return runResult{result: result, plan: plan}, err
	})
	meta := run.result.Total
	meta.Operation = name(d.ID, operation)
	if run.plan.Shape != types.ShapeUnspecified {
		meta.Strategy = shapeOf(run.plan.Shape).String()
	}
	if runErr != nil {
		return nil, call.End(&meta, runErr)
	}
	out := make([]Item[Out], len(run.result.Items))
	for i, item := range run.result.Items {
		// An item's error is converted as the call's own are, so that errors.As
		// finds the root's *Error and errors.Is the root's Kind in it.
		out[i] = Item[Out]{value: item.Value, err: bind.ConvertError(meta.Operation, item.Err)}
	}
	return out, call.End(&meta, nil)
}
