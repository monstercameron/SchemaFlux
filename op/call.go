package op

// This file is what the functions of this package share about a call: how a
// descriptor of the caller's is lowered onto the internal runners, how a panic in
// one of its functions is told from a panic of anything else, how the options of
// a call reach the internal options, and how the many requests of a run add up to
// one Record. The functions of run.go are written to the pattern of the root's
// operation functions (bind.Begin, the checks, the plain call or the twin,
// call.End) and use these helpers for what is particular to a descriptor.
//
//	name, lowered, guarded       the Record's and the error's name, the descriptor as an
//	                             internal operation, and the call that survives its panics
//	requestOptions               the options of a call as the internal runners take them
//	sumMetas                     the one envelope of a run made of many requests
//
// The helpers are written to be used by every function of the package that runs
// a descriptor, RunEach and RunStages included: lowered gives them the
// descriptor as the internal operation, guarded makes their call safe from the
// descriptor's own panics and sumMetas gives them the Record of a run of
// several requests.

import (
	"context"
	"fmt"
	"sync/atomic"

	schemaflux "github.com/monstercameron/schemaflux/v2"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// name is the Op of the Record a call adds and of the errors it returns: the
// descriptor's own name as its author wrote it, and fallback when it has none.
func name(id ID, fallback string) string {
	if id.Name != "" {
		return id.Name
	}
	return fallback
}

// The two functions of a descriptor that the planner calls, and so the two whose
// panic it turns into a refusal of the plan. A trap names the first of them that
// panicked in one call.
const (
	fnNone int32 = iota
	fnBuildPrompt
	fnEncode
)

var fnNames = [...]string{
	fnNone:        "",
	fnBuildPrompt: "BuildPrompt",
	fnEncode:      "Batch.Encode",
}

// A trap tells the planner's refusal of a descriptor that panicked from a plan
// that was refused for another reason. The internal planner recovers a panic in
// BuildPrompt or Encode and refuses the plan as an invariant violation, with no
// mark that says a function panicked, and a run that is stopped by that plan
// would be reported as an invalid answer. The two functions that adapt the call's
// options (lowered) therefore note themselves here when they panic, and panic
// again with a value that holds nothing of what the first one carried.
//
// Every other function of the descriptor is passed to the internal runners as it
// is, and a panic in one of them is recovered by guarded, or by the runner's own
// goroutine, which marks it. A trap is made for one call and is safe for the
// concurrent use of the goroutines a run makes.
type trap struct{ first atomic.Int32 }

// descriptorPanic is the value the two adapting functions panic with. It is a
// type of this package with no field, so that nothing a caller's function put in
// its panic value travels on, and a log that writes the type of a panic writes
// this.
type descriptorPanic struct{}

// guard is deferred by the adapting functions, as `defer t.guard(fn)`. It
// recovers a panic of the function fn, notes fn if it is the first, and panics
// again with a descriptorPanic.
func (t *trap) guard(fn int32) {
	if recover() == nil {
		return
	}
	t.first.CompareAndSwap(fnNone, fn)
	panic(descriptorPanic{})
}

// tripped reports whether BuildPrompt or Encode panicked.
func (t *trap) tripped() bool { return t.first.Load() != fnNone }

// panicked is the internal error of a call that a function of the descriptor
// panicked in: an invariant violation marked as a recovered panic, which the
// root reports as ErrInternal. It names the function when the trap knows it, and
// holds nothing the function panicked with.
func (t *trap) panicked(op string) error {
	message := "the call panicked in a function of the descriptor"
	if fn := t.first.Load(); fn != fnNone {
		message = fmt.Sprintf("the descriptor's %s panicked", fnNames[fn])
	}
	return &types.OperationError{
		Kind:    types.KindInvariantViolation,
		Narrow:  types.NarrowPanic,
		Op:      op,
		Message: message,
	}
}

// guarded runs run, which makes the internal call in the caller's goroutine, and
// returns what it returns, except that a panic of the call, which is a function of
// the descriptor panicking in the caller's goroutine, comes back as the error of
// t.panicked beside the zero value, and so does an error after BuildPrompt or
// Encode panicked inside the planner or a goroutine of the run. The goroutines of
// a run have their own recovery, which marks the panic and ends the run; the
// panic's value is never in the error.
//
// The call has no recovery of its own for a provider: a Provider's panic is
// already an error of kind ErrInternal by the time it gets here.
func guarded[T any](t *trap, op string, run func() (T, error)) (value T, err error) {
	defer func() {
		if recover() == nil {
			return
		}
		var zero T
		value, err = zero, t.panicked(op)
	}()
	value, err = run()
	if err != nil && t.tripped() {
		var zero T
		return zero, t.panicked(op)
	}
	return value, err
}

// lowered is the descriptor as the internal operation the runners take, named
// operation, which is the descriptor's own name or, when it has none, the name of
// the function that runs it, so that an observer is told one operation by that
// name. The two functions that are given the call's Options, BuildPrompt and
// Encode, are adapted: the internal runners hand them the internal options and the
// descriptor's own are in the types of this package. Every other function is the
// caller's own and is passed as it is, so that lowering a descriptor costs two
// closures and nothing for the rest. A function the descriptor leaves nil is left
// nil, which the internal runners refuse before sending anything.
func lowered[In, Out any](d Descriptor[In, Out], t *trap, operation string) ops.Op[In, Out] {
	iop := ops.Op[In, Out]{
		ID:        types.OperationID{Name: operation, Version: d.ID.Version},
		Semantics: d.Semantics.internal(),
		Contract: ops.OutputContract[Out]{
			SchemaName: d.Contract.SchemaName,
			Decode:     d.Contract.Decode,
			Invariants: d.Contract.Invariants,
			Normalize:  d.Contract.Normalize,
		},
		Batch:         loweredBatch(d.Batch, t),
		DefaultPolicy: types.DefaultPolicy{RepairAttempts: d.Policy.RepairAttempts},
	}
	if f := d.BuildPrompt; f != nil {
		iop.BuildPrompt = func(input In, opt types.OpOptions) (string, string) {
			defer t.guard(fnBuildPrompt)
			return f(input, optionsOf(opt))
		}
	}
	return iop
}

// loweredBatch is the batch of a descriptor as the internal algebra. A batch with
// Item is the library's id protocol, built by the internal constructor, whose
// batch level is the one that vouches for every id and every item.
func loweredBatch[In, Out any](b Batch[In, Out], t *trap) ops.BatchAlgebra[In, Out] {
	var algebra ops.BatchAlgebra[In, Out]
	if b.Item != nil {
		algebra = ops.NewIDBatchAlgebra(b.Class.internal(), b.Kind.internal(), b.Instructions, ops.BatchItemCodec[In, Out]{DecodeItem: b.Item})
	} else {
		algebra = ops.BatchAlgebra[In, Out]{Class: b.Class.internal(), Kind: b.Kind.internal(), Merge: b.Merge}
		if f := b.Encode; f != nil {
			algebra.Encode = func(items []In, opt types.OpOptions) (string, string, error) {
				defer t.guard(fnEncode)
				return f(items, optionsOf(opt))
			}
		}
	}
	algebra.GlobalValidate = b.GlobalValidate
	return algebra
}

// optionsOf is the Options of the internal options a call resolved, which is what
// the descriptor's own functions are given. The caller's parent ids and the
// internal settings the options of this package cannot set are not in it.
func optionsOf(opt types.OpOptions) Options {
	return Options{
		Options: schemaflux.Options{
			Model:           opt.Model,
			Tier:            tierOf(opt.Intelligence),
			Effort:          schemaflux.Effort(opt.Effort),
			Mode:            modeOf(opt.Mode),
			Steering:        opt.Steering,
			MaxOutputTokens: opt.MaxOutputTokens,
			WebSearch:       opt.WebSearch,
		},
		JSONSchema:     opt.JSONSchema,
		SchemaName:     opt.SchemaName,
		ResponseFormat: opt.ResponseFormat,
	}
}

// requestOptions is the internal options of a call on ctx: the common settings c,
// which a check has already passed, laid on an empty value where they are not
// their zero value, then the three settings that are this package's own where
// they are not theirs, and the parent ids the context carries (WithParents),
// which an answer's provenance names.
func requestOptions(ctx context.Context, o Options, c bind.Common) types.OpOptions {
	var opt types.OpOptions
	c.Apply(&opt)
	if o.JSONSchema != nil {
		opt.JSONSchema = o.JSONSchema
	}
	if o.SchemaName != "" {
		opt.SchemaName = o.SchemaName
	}
	if o.ResponseFormat != "" {
		opt.ResponseFormat = o.ResponseFormat
	}
	opt.ParentResultIDs = ops.ParentResultIDs(ctx)
	return opt
}

// sumMetas adds up the envelopes of the requests one run of many made, which the
// internal runner returns one to each item. A chunk of a batched run served all
// its items with one request, so its spend is on its first item and zero on the
// others, and a sum over the items is what was spent.
//
// Usage, attempts, retries, repairs, cache hits and timing are added. The cost is
// added over the requests that were priced and is priced only when every request
// that was made was, with the worst quality of its parts. What belongs to no
// single request is left out: the provider, the model, the requested model, the
// schema and the requested tier are kept only where every request that was made
// agrees on them. Elapsed is the caller's to set, since requests that ran
// together add up to more time than passed. The contract levels and the strategy
// describe the run, and so does Checks, which holds one entry for the repairs of
// the whole run, in a count and never in the text of what the model was shown.
func sumMetas[Out any](operation string, results []types.Result[Out]) types.Meta {
	total := types.Meta{Operation: operation}

	var made []types.Meta
	repaired := 0
	for _, result := range results {
		meta := result.Meta
		total.Attempts += meta.Attempts
		total.Repairs += meta.Repairs
		total.Retries += meta.Retries
		total.CacheHits += meta.CacheHits
		total.Hedged = total.Hedged || meta.Hedged
		if meta.ServiceTier != "" {
			total.ServiceTier = meta.ServiceTier
		}
		total.Usage.PromptTokens += meta.Usage.PromptTokens
		total.Usage.CompletionTokens += meta.Usage.CompletionTokens
		total.Usage.TotalTokens += meta.Usage.TotalTokens
		total.Usage.CachedTokens += meta.Usage.CachedTokens
		total.Usage.ReasoningTokens += meta.Usage.ReasoningTokens
		total.Timing = total.Timing.Add(meta.Timing)
		if meta.Attempts > 0 {
			made = append(made, meta)
			// Every item of a chunk carries the chunk's checks, and the item that
			// carries its spend is the one that counts them.
			for _, check := range meta.Checks {
				if !check.Passed {
					repaired++
				}
			}
		}
	}
	if repaired > 0 {
		total.Checks = []types.Check{{
			Name:   "repair",
			Passed: false,
			Detail: fmt.Sprintf("%d refused answers were sent back to the model", repaired),
		}}
	}
	if total.Usage.PromptTokens > 0 {
		total.CacheHitRatio = float64(total.Usage.CachedTokens) / float64(total.Usage.PromptTokens)
	}
	requested, delivered := weakestLevels(results)
	total.RequestedContract, total.DeliveredContract = requested, delivered
	if len(made) == 0 {
		return total
	}

	priced := true
	voters := 0
	quality := qualityTally{}
	for i := range made {
		cost := &made[i].Cost
		if !cost.Priced && made[i].Usage == (types.TokenUsage{}) {
			// A request that reported nothing about what it cost casts no vote either
			// way: what a failed request was billed is not known, and is not guessed.
			continue
		}
		voters++
		quality.add(cost.Quality)
		if !cost.Priced {
			priced = false
			continue
		}
		if voters == 1 || total.Cost.Currency == "" {
			total.Cost.Currency = cost.Currency
			total.Cost.PricingSource = cost.PricingSource
		}
		total.Cost.TotalCost += cost.TotalCost
		total.Cost.PromptCost += cost.PromptCost
		total.Cost.CompletionCost += cost.CompletionCost
		total.Cost.CachedCost += cost.CachedCost
		total.Cost.ReasoningCost += cost.ReasoningCost
	}
	total.Cost.Priced = priced && voters > 0
	if !total.Cost.Priced {
		total.Cost.PricingSource = ""
	}
	total.Cost.Quality = quality.result()

	total.Provider = agreed(made, func(m types.Meta) string { return m.Provider })
	total.Model = agreed(made, func(m types.Meta) string { return m.Model })
	total.RequestedModel = agreed(made, func(m types.Meta) string { return m.RequestedModel })
	total.SchemaID = agreed(made, func(m types.Meta) string { return m.SchemaID })
	if tier := made[0].RequestedTier; tier != types.TierUnset {
		same := true
		for _, meta := range made[1:] {
			same = same && meta.RequestedTier == tier
		}
		if same {
			total.RequestedTier = tier
		}
	}
	for _, meta := range made {
		total.ModelDrifted = total.ModelDrifted || meta.ModelDrifted
		total.ModelDriftUnknown = total.ModelDriftUnknown || meta.ModelDriftUnknown
	}
	return total
}

// weakestLevels is the weakest contract level any item of a run requested and the
// weakest it was delivered at: what the run as a whole can claim.
func weakestLevels[Out any](results []types.Result[Out]) (requested, delivered types.ContractLevel) {
	for i, result := range results {
		if i == 0 {
			requested, delivered = result.Meta.RequestedContract, result.Meta.DeliveredContract
			continue
		}
		requested = min(requested, result.Meta.RequestedContract)
		delivered = min(delivered, result.Meta.DeliveredContract)
	}
	return requested, delivered
}

// qualityTally combines the pricing quality of the requests of a run: unknown if
// any is unknown, else estimated if any is estimated, else exact if any was
// priced, else free. Free beside exact is exact, since a genuine zero adds
// nothing to a measured figure, and a run with no figure at all is unknown.
type qualityTally struct{ any, unknown, estimated, exact bool }

func (q *qualityTally) add(quality types.PricingQuality) {
	q.any = true
	switch quality {
	case types.PricingEstimated:
		q.estimated = true
	case types.PricingExact:
		q.exact = true
	case types.PricingFree:
	default:
		// The empty string is a request from a path that does not classify yet, and
		// reads as unknown: the alternative is to claim a figure exact on the
		// strength of a zero value.
		q.unknown = true
	}
}

func (q qualityTally) result() types.PricingQuality {
	switch {
	case !q.any, q.unknown:
		return types.PricingUnknown
	case q.estimated:
		return types.PricingEstimated
	case q.exact:
		return types.PricingExact
	}
	return types.PricingFree
}

// agreed is the value every meta has for a field, or "" when they differ or none
// has one.
func agreed(metas []types.Meta, field func(types.Meta) string) string {
	first := field(metas[0])
	for _, meta := range metas[1:] {
		if field(meta) != first {
			return ""
		}
	}
	return first
}
