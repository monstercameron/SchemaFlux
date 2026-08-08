package ops

import (
	"context"
	"fmt"
	"sync"

	"github.com/monstercameron/schemaflux/internal/types"
)

// PL-008: partial success and failure policies, as an item-level engine
// built on RunOpResult -- the same per-item unit runManyAtomic (plan_many.go)
// already dispatches for ShapeAtomic, reused here rather than a second call
// path this package would have to keep in sync with RunOp by hand.
//
// This delivers the item-level half of PL-008: RunOpManyPartial always
// dispatches one call per item, which is exactly ShapeAtomic. What it does
// not deliver is MDSP-chunk-level partial success -- a chunk that answered
// most of its offered ids and dropped a few is PL-009's progressive
// recovery cascade (isolate the unresolved ids, retry a smaller MDSP, fall
// back to atomic), which is a different task and is not built here. A
// caller running MDSP batches through RunOpMany (plan_many.go) today still
// gets PL-007's all-or-nothing behaviour for a chunk; RunOpManyPartial is
// the new, additional entry point for a caller who wants PL-008's
// per-item accounting and is willing to pay for it in calls (one per item,
// not one per chunk).

// PartialConfig configures RunOpManyPartial.
type PartialConfig struct {
	// Policy selects PL-008's behaviour. PolicyUnspecified resolves through
	// types.DefaultPolicyFor(len(items)); the resolved policy and the reason
	// are recorded on the returned BatchResult.Summary.
	Policy types.BatchPolicy

	// Concurrency bounds items in flight. Zero means DefaultConcurrency.
	Concurrency int

	// MaxRetries bounds PolicyRetryFailedItems / PolicyRetryThenCollect's
	// retry pass: how many additional attempts an item still failed after
	// the first pass gets, isolated from every other item. Zero means
	// DefaultMaxRetries.
	MaxRetries int
}

// DefaultMaxRetries is PartialConfig.MaxRetries' default -- bounded, not
// unbounded, so an item that fails for a reason retrying cannot fix (a
// malformed input, not a transient provider hiccup) cannot spend the whole
// call's budget retrying something that will never come back.
const DefaultMaxRetries = 2

// itemOutcome is one item's state while RunOpManyPartial is still running,
// before it is rendered into a types.ItemResult.
type itemOutcome[Out any] struct {
	status   types.ItemStatus
	value    Out
	attempts int
	repairs  int
	cost     types.CostInfo
	err      error
}

// outcomeFrom turns one RunOpResult call's return into an itemOutcome,
// which is where an item's Attempts/Repairs/Cost accounting (PL-013) is
// read from the same envelope every other operation in this package
// already reports through -- no second accounting pass invented for this
// engine alone.
func outcomeFrom[Out any](res types.Result[Out], err error) itemOutcome[Out] {
	if err != nil {
		return itemOutcome[Out]{
			status:   types.ItemFailed,
			attempts: res.Meta.Attempts,
			repairs:  res.Meta.Repairs,
			cost:     res.Meta.Cost,
			err:      err,
		}
	}
	return itemOutcome[Out]{
		status:   types.ItemSucceeded,
		value:    res.Value,
		attempts: res.Meta.Attempts,
		repairs:  res.Meta.Repairs,
		cost:     res.Meta.Cost,
	}
}

// RunOpManyPartial runs op over items, one call per item, applying
// cfg.Policy, and returns a types.BatchResult naming what happened to every
// item -- the PL-008 contract a bare ([]Out, error) cannot express.
//
// The returned error is non-nil exactly when cfg.Policy's own contract says
// a partial result should fail the call (PolicyFailFast, PolicyRequireAll,
// and PolicyRetryFailedItems when the retry pass did not resolve every
// failure); it is always nil for PolicyCollectFailures and
// PolicyRetryThenCollect, whose contract is "read BatchResult.Complete(),
// not the error return". The BatchResult is returned either way -- unlike
// MapReduce's default behaviour elsewhere in this package, which nils the
// whole result on the failure that stops the run, an error here never hides
// which items got through.
func RunOpManyPartial[In, Out any](ctx context.Context, op Op[In, Out], items []In, opt types.OpOptions, cfg PartialConfig) (types.BatchResult[Out], error) {
	var result types.BatchResult[Out]
	result.Summary.Total = len(items)

	policy := cfg.Policy
	reason := ""
	if policy == types.PolicyUnspecified {
		policy, reason = types.DefaultPolicyFor(len(items))
	}
	result.Summary.Policy = policy
	result.Summary.PolicyReason = reason

	if len(items) == 0 {
		return result, nil
	}

	if op.BuildPrompt == nil {
		return result, fmt.Errorf("op %s: no BuildPrompt to render a request from", op.ID)
	}
	if op.Contract.Decode == nil {
		return result, fmt.Errorf("op %s: no Decode to turn a response into a value", op.ID)
	}

	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = DefaultConcurrency
	}
	maxRetries := cfg.MaxRetries
	if maxRetries <= 0 {
		maxRetries = DefaultMaxRetries
	}

	outcomes := make([]itemOutcome[Out], len(items))

	switch policy {
	case types.PolicyFailFast:
		runFailFast(ctx, op, items, opt, concurrency, outcomes)

	case types.PolicyCollectFailures, types.PolicyRequireAll:
		runCollect(ctx, op, items, opt, concurrency, outcomes)

	case types.PolicyRetryFailedItems, types.PolicyRetryThenCollect:
		runCollect(ctx, op, items, opt, concurrency, outcomes)
		runRetryPass(ctx, op, items, opt, concurrency, maxRetries, outcomes)

	default:
		return result, fmt.Errorf("op %s: unknown batch policy %s", op.ID, policy)
	}

	result.Items = make([]types.ItemResult[Out], len(items))
	for i, o := range outcomes {
		result.Items[i] = types.ItemResult[Out]{
			Index:    i,
			Status:   o.status,
			Value:    o.value,
			Attempts: o.attempts,
			Repairs:  o.repairs,
			Cost:     o.cost,
			Mode:     types.ShapeAtomic.String(),
			Err:      o.err,
		}
		switch o.status {
		case types.ItemSucceeded:
			result.Summary.Succeeded++
		case types.ItemFailed:
			result.Summary.Failed++
		case types.ItemCancelled:
			result.Summary.Cancelled++
		}
	}

	switch policy {
	case types.PolicyFailFast, types.PolicyRequireAll, types.PolicyRetryFailedItems:
		if result.Summary.Failed > 0 || result.Summary.Cancelled > 0 {
			return result, fmt.Errorf("op %s: %s", op.ID, result.String())
		}
	}

	return result, nil
}

// runCollect runs every item exactly once, bounded to concurrency in
// flight, with no cancellation on a failure -- PolicyCollectFailures' and
// PolicyRequireAll's shared scheduling, and the first pass
// PolicyRetryFailedItems/PolicyRetryThenCollect build their retry pass on
// top of.
func runCollect[In, Out any](ctx context.Context, op Op[In, Out], items []In, opt types.OpOptions, concurrency int, outcomes []itemOutcome[Out]) {
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i := range items {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			res, err := RunOpResult(ctx, op, items[i], opt)
			outcomes[i] = outcomeFrom(res, err)
		}(i)
	}
	wg.Wait()
}

// runFailFast runs items bounded to concurrency in flight, stopping the
// dispatch of new items the instant one fails and marking every item that
// was never started ItemCancelled -- never ItemFailed, since nothing about
// an unstarted item's own input was found wrong.
//
// It is a bounded worker pool fed through a channel, the same shape
// MapReduce (mapreduce.go) uses for the identical reason: a semaphore
// racing goroutines dispatches in whatever order the scheduler wakes them,
// which makes "stop after item 3 fails" non-deterministic about which items
// after it actually ran. Feeding indices through a channel that a single
// dispatcher goroutine closes on cancellation keeps dispatch order fixed
// regardless of concurrency.
func runFailFast[In, Out any](ctx context.Context, op Op[In, Out], items []In, opt types.OpOptions, concurrency int, outcomes []itemOutcome[Out]) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	indices := make(chan int)
	go func() {
		defer close(indices)
		for i := range items {
			select {
			case indices <- i:
			case <-runCtx.Done():
				return
			}
		}
	}()

	workers := concurrency
	if workers > len(items) {
		workers = len(items)
	}
	if workers < 1 {
		workers = 1
	}

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range indices {
				if runCtx.Err() != nil {
					// Drained after cancellation without running: left
					// ItemUnspecified here, resolved to ItemCancelled below.
					continue
				}
				res, err := RunOpResult(runCtx, op, items[i], opt)
				if err != nil {
					outcomes[i] = outcomeFrom(res, err)
					cancel()
					continue
				}
				outcomes[i] = outcomeFrom(res, nil)
			}
		}()
	}
	wg.Wait()

	for i := range outcomes {
		if outcomes[i].status == types.ItemUnspecified {
			outcomes[i] = itemOutcome[Out]{status: types.ItemCancelled, err: context.Canceled}
		}
	}
}

// runRetryPass retries, isolated and bounded to concurrency in flight, only
// the items outcomes already marks ItemFailed -- PL-009's "a valid item is
// never replayed because a sibling failed" applied at item granularity: a
// succeeded item from runCollect's first pass is never touched here. It
// runs up to maxRetries additional rounds, stopping early once nothing is
// left to retry.
func runRetryPass[In, Out any](ctx context.Context, op Op[In, Out], items []In, opt types.OpOptions, concurrency, maxRetries int, outcomes []itemOutcome[Out]) {
	for attempt := 0; attempt < maxRetries; attempt++ {
		var pending []int
		for i, o := range outcomes {
			if o.status == types.ItemFailed {
				pending = append(pending, i)
			}
		}
		if len(pending) == 0 {
			return
		}

		sem := make(chan struct{}, concurrency)
		var wg sync.WaitGroup
		for _, i := range pending {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int) {
				defer wg.Done()
				defer func() { <-sem }()

				priorAttempts := outcomes[i].attempts
				priorRepairs := outcomes[i].repairs
				priorCost := outcomes[i].cost

				res, err := RunOpResult(ctx, op, items[i], opt)
				o := outcomeFrom(res, err)
				o.attempts += priorAttempts
				o.repairs += priorRepairs
				o.cost = addCost(priorCost, o.cost)
				outcomes[i] = o
			}(i)
		}
		wg.Wait()
	}
}

// addCost sums two CostInfo values' spend, carrying Priced/Currency from
// whichever side reports pricing -- so a retry pass's cost accounting never
// silently drops the first attempt's spend, which is money that was really
// paid even though that attempt did not produce a usable answer.
func addCost(a, b types.CostInfo) types.CostInfo {
	if !a.Priced && !b.Priced {
		return types.CostInfo{}
	}
	sum := types.CostInfo{
		Priced:         true,
		TotalCost:      a.TotalCost + b.TotalCost,
		PromptCost:     a.PromptCost + b.PromptCost,
		CompletionCost: a.CompletionCost + b.CompletionCost,
		CachedCost:     a.CachedCost + b.CachedCost,
		ReasoningCost:  a.ReasoningCost + b.ReasoningCost,
	}
	sum.Currency = a.Currency
	if sum.Currency == "" {
		sum.Currency = b.Currency
	}
	return sum
}
