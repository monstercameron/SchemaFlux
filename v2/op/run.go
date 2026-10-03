package op

import (
	"context"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// Run runs a descriptor on one input: it renders the request with the
// descriptor's BuildPrompt, asks the model, reads the answer with Contract.Decode,
// holds it to the descriptor's Invariants and returns it after Normalize. An
// answer that is refused is shown back to the model with what was wrong and the
// model is asked again, up to Policy.RepairAttempts times, so one call can send
// more than one request, and each one is paid for. What is left is
// ErrInvalidAnswer.
//
//	invoice, err := op.Run(ctx, invoiceDescriptor, emailBody)
//
// Options may be given more than once and are merged left to right. The caller's
// JSON schema, schema name and response format are the settings this function
// takes that the built-in operations do not.
//
// The Record of the call is named for the descriptor, its ID.Name, and "op.run"
// when that is empty. Record.Checks has an entry for each answer that was refused
// and sent back, Record.Repairs counts them, and Record.Provenance is the answer's
// lineage: its id, the digests of its input and schema, the descriptor's version
// and the parents given with WithParents. The level the answer is delivered at is
// computed from the descriptor: ContractSchemaAndInvariantChecked when it has
// Invariants and the request sent a schema, ContractSchemaConstrained when it
// sent one and has none, and ContractJSONWellFormed otherwise, because a schema
// that was named and never sent was never enforced. A client's floor above that
// refuses the call with ErrPolicy before anything is sent.
//
// A descriptor with no BuildPrompt or no Contract.Decode, or an Invariants entry
// that is nil, is refused with ErrConfiguration before anything is sent. A
// function of the descriptor that panics ends the call with an error of kind
// ErrInternal. A nil ctx is ErrInvalidRequest.
func Run[In, Out any](ctx context.Context, d Descriptor[In, Out], input In, opts ...Options) (Out, error) {
	var zero Out
	op := name(d.ID, "op.run")
	call, err := bind.Begin(ctx, op)
	if err != nil {
		return zero, err
	}
	if err := checkDescriptor(d, op); err != nil {
		return zero, call.End(nil, err)
	}
	o := mergeOptions(opts)
	common, err := checkOptions(o, op)
	if err != nil {
		return zero, call.End(nil, err)
	}
	t := new(trap)
	internal := lowered(d, t, op)
	if call.Plain() {
		callCtx := call.Context()
		value, err := guarded(t, op, func() (Out, error) {
			out, _, err := ops.RunOp(callCtx, internal, input, requestOptions(callCtx, o, common))
			return out, err
		})
		return value, call.End(nil, err)
	}
	// The twin sends the same request and reports what the Record carries: the
	// repairs, the provenance and the level actually delivered.
	callCtx := call.Context()
	res, err := guarded(t, op, func() (types.Result[Out], error) {
		return ops.RunOpResult(callCtx, internal, input, requestOptions(callCtx, o, common))
	})
	meta := res.Meta
	return res.Value, call.End(&meta, err)
}

// RunBatch runs a descriptor over many items in one request: it renders the
// request with the descriptor's Batch, asks the model once, reads the answer back
// to the items, holds each output to the Invariants and runs Normalize on it, and
// then, for a batch of class BatchAggregate, makes the check Batch.GlobalValidate
// makes of the whole. It returns one output for each item, in the order of items.
//
// A batch whose Batch.Item is set is the library's id protocol: each item is sent
// with an id made for this request, the answer is read by those ids, and every id
// has to be answered exactly once before one item is decoded. What it cannot
// detect is a model that writes one item's answer under another item's id; a
// caller for whom that matters sends one request for each item (RunMany with
// Force ShapeAtomic). A batch written with Encode and Merge is as good as they
// are.
//
// It does not repair: a batch that fails fails whole, with an error that says
// which step failed, in counts and never in the items' values. For no items it
// sends nothing and returns an empty slice.
//
// The Record is named as Run's is, and "op.runbatch" when the descriptor has no
// name. Its Strategy is "batched". It is delivered at the level of the batch, which
// is that of the descriptor's contract for a batch built on Batch.Item and
// ContractPromptOnly for one with an Encode and a Merge of the caller's own: the
// library cannot see into them. A descriptor with no batch to run is refused with
// ErrConfiguration, and a client's floor above the level refuses the call with
// ErrPolicy, both before anything is sent.
func RunBatch[In, Out any](ctx context.Context, d Descriptor[In, Out], items []In, opts ...Options) ([]Out, error) {
	op := name(d.ID, "op.runbatch")
	call, err := bind.Begin(ctx, op)
	if err != nil {
		return nil, err
	}
	if err := checkDescriptor(d, op); err != nil {
		return nil, call.End(nil, err)
	}
	o := mergeOptions(opts)
	common, err := checkOptions(o, op)
	if err != nil {
		return nil, call.End(nil, err)
	}
	t := new(trap)
	internal := lowered(d, t, op)
	callCtx := call.Context()
	if call.Plain() {
		outputs, err := guarded(t, op, func() ([]Out, error) {
			return ops.RunOpBatch(callCtx, internal, items, requestOptions(callCtx, o, common))
		})
		return batchOutputs(outputs, err), call.End(nil, err)
	}
	res, err := guarded(t, op, func() (types.Result[[]Out], error) {
		return ops.RunOpBatchResult(callCtx, internal, items, requestOptions(callCtx, o, common))
	})
	meta := res.Meta
	if meta.Strategy == types.ShapeMDSP.String() {
		meta.Strategy = ShapeBatched.String()
	}
	return batchOutputs(res.Value, err), call.End(&meta, err)
}

// batchOutputs is the slice a batch returns: the outputs, an empty slice that is
// not nil for a batch that succeeded with none, and nothing beside an error.
func batchOutputs[Out any](outputs []Out, err error) []Out {
	switch {
	case err != nil:
		return nil
	case outputs == nil:
		return []Out{}
	}
	return outputs
}

// RunMany plans a run of a descriptor over many items and carries it out: one
// request for each item, or several items in a request, as the plan says. It
// returns one output for each item, in the order of items, and sends nothing for
// a run the plan refuses. The plan is the one Preflight returns for the same
// arguments, and a plan that is not eligible is the error of RunMany.
//
//	outputs, err := op.RunMany(ctx, ticketDescriptor, tickets, op.PlanOptions{MaxItemsPerCall: 20})
//
// A run that fails fails whole: the first request to fail stops the run, the ones
// in flight are cancelled, nothing new starts, and that failure is returned with
// no outputs. A bad item costs the run. Calls overlap: at most four are in flight
// at once, or the limit of the client's Scheduler when it sets one, and the
// client's Budget and Policy apply to each. A client's Budget is checked as each
// call starts, so a run can overshoot it by the calls that were already started.
//
// The Record is one for the whole call, named as Run's is, "op.runmany" when the
// descriptor has no name. It is the sum of what every request cost: tokens, cost,
// requests, retries, repairs, timing. Its Strategy is the plan's shape, "atomic"
// or "batched", and its Requested and Delivered are the weakest of the runs'.
// Record.Checks holds one entry, with a count, when answers were refused and sent
// back. A run that fails reports what its returned results cost, which is nothing:
// requests sent before the failure are in the client's ledger and not in the
// Record.
//
// A budget over PlanOptions.Budget is ErrBudgetExceeded and a shape the descriptor
// cannot run is ErrConfiguration, each before anything is sent. A client's floor
// above what the descriptor delivers is ErrPolicy. A function of the descriptor
// that panics ends the call with an error of kind ErrInternal.
func RunMany[In, Out any](ctx context.Context, d Descriptor[In, Out], items []In, opts ...PlanOptions) ([]Out, error) {
	op := name(d.ID, "op.runmany")
	call, err := bind.Begin(ctx, op)
	if err != nil {
		return nil, err
	}
	if err := checkDescriptor(d, op); err != nil {
		return nil, call.End(nil, err)
	}
	o := mergePlanOptions(opts)
	common, err := checkPlanOptions(o, op)
	if err != nil {
		return nil, call.End(nil, err)
	}
	t := new(trap)
	internal := lowered(d, t, op)
	callCtx := call.Context()
	type ran struct {
		results []types.Result[Out]
		plan    types.Plan
	}
	run, err := guarded(t, op, func() (ran, error) {
		results, plan, err := ops.RunOpMany(callCtx, internal, items, planRequest(requestOptions(callCtx, o.Options, common), o))
		return ran{results, plan}, err
	})
	var meta *types.Meta
	if !call.Plain() {
		// The runs inside shadow the call's own log, so the Record is made of the
		// Metas they returned.
		summed := sumMetas(op, run.results)
		if run.plan.Shape != types.ShapeUnspecified {
			summed.Strategy = shapeOf(run.plan.Shape).String()
		}
		meta = &summed
	}
	if err != nil {
		return nil, call.End(meta, err)
	}
	outputs := make([]Out, len(run.results))
	for i, result := range run.results {
		outputs[i] = result.Value
	}
	return outputs, call.End(meta, nil)
}

// Preflight returns the plan for running a descriptor over items with RunMany:
// the shape, the chunks, the most calls, what it will cost and, when it cannot be
// carried out, why. It sends nothing, and makes no call to a provider.
//
// The plan is made for the client on ctx: its provider, its model and its context
// window, and a call is cut into chunks that fit that model and not the process
// default's. The estimate is of what the descriptor renders: BuildPrompt for each
// item of an atomic plan and Batch.Encode for each chunk of a batched one, each
// called once for each item or chunk, as the call path will send them, with the
// reinforcement the library puts in front of the system prompt and the caller's
// Steering in front of the user prompt. They are the caller's functions and may do
// nothing but return the prompts. The completion is priced at the tier's output
// ceiling, so the estimate is an upper bound on what the answers cost.
//
// A plan that cannot be carried out is returned, not an error: Eligible is false
// and IneligibleKind and IneligibleReason say why (a shape the descriptor cannot
// run was forced, the estimate is over PlanOptions.Budget, an item cannot be
// encoded, a function of the descriptor panicked). The error is for a call that
// cannot be made at all: a nil ctx is ErrInvalidRequest, a descriptor that cannot
// be used or an option that holds a value it cannot take is ErrConfiguration.
//
// It adds no Record, and no floor applies to it. A function of the descriptor that
// panics makes the plan ineligible with IneligibleKind ErrInternal and never ends
// the process. Errors name the operation "op.preflight".
func Preflight[In, Out any](ctx context.Context, d Descriptor[In, Out], items []In, opts ...PlanOptions) (Plan, error) {
	const op = "op.preflight"
	if ctx == nil {
		return Plan{}, bind.Refuse(op, types.KindInvalidRequest, "the context is nil")
	}
	if err := checkDescriptor(d, op); err != nil {
		return Plan{}, bind.ConvertError(op, err)
	}
	o := mergePlanOptions(opts)
	common, err := checkPlanOptions(o, op)
	if err != nil {
		return Plan{}, bind.ConvertError(op, err)
	}
	t := new(trap)
	internal := lowered(d, t, d.ID.Name)
	plan, err := ops.PreflightFor(ctx, internal, items, planRequest(requestOptions(ctx, o.Options, common), o))
	if err != nil {
		return Plan{}, bind.ConvertError(op, err)
	}
	made := planOf(plan)
	if t.tripped() {
		// The planner refuses a plan it could not price because a function of the
		// descriptor panicked, as a recovered panic is refused everywhere else.
		made.Eligible = false
		made.IneligibleKind = schemaflux.ErrInternal
		if made.IneligibleReason == "" {
			made.IneligibleReason = t.panicked(op).Error()
		}
	}
	return made, nil
}

// WithParents returns a context whose calls name resultIDs as the parents of the
// answers they produce, in Record.Provenance.ParentResultIDs. It is how a
// pipeline of calls is traced: a stage's answer has an id in its Record's
// Provenance.ResultID, and the stage that reads it names that id here.
//
//	first, _ := op.Run(ctx, extract, text)  // its Record's Provenance.ResultID is "r-1"
//	ctx = op.WithParents(ctx, "r-1")
//	second, _ := op.Run(ctx, review, first)
//
// The ids replace those of the context it is given, and with none it says the
// calls have no parents. They are copied. Only a call that makes a Provenance
// reports them: Run does. A nil ctx panics, as context.WithValue does.
func WithParents(ctx context.Context, resultIDs ...string) context.Context {
	return ops.WithParentResultIDs(ctx, resultIDs...)
}
