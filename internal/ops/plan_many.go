package ops

import (
	"context"
	"fmt"
	"maps"
	"sync"
	"sync/atomic"

	"github.com/monstercameron/schemaflux/internal/types"
)

// RunOpMany is the generic engine a plural API wires an Op[In, Out] onto, with
// batching, ordering, and a call ceiling as stated semantics rather than a
// caller's own loop reinterpreting a singular function. The operation's
// BuildPrompt must render a per-item prompt from In, which a genuine batch of
// independent inputs needs.
//
// PreflightFor decides the shape, for the client the context carries, and the
// shape decides how items are dispatched. The chunks are cut for that client's
// provider and model, the ones the calls will go to; a context with no client
// is planned as Preflight plans.
// The atomic shape runs through MapReduce's bounded worker pool; the batched
// shapes send their chunks, and any oversized items one call each, through a
// bounded pool of their own, so independent chunks wait on the provider
// together and a batch of five chunks takes about one call's time. How many are
// in flight at once is DefaultConcurrency, or, when the context carries a
// client's scheduler that sets a limit, that limit (at most sixty-four): every
// call is submitted through it, as a single call is, so a batch cannot exceed
// the client's limits or crowd out its other callers. The result order always
// matches the input order regardless of which chunk or item finished first.
//
// A client's budget is checked as each call starts. Calls that overlap are
// started before the first of them has been paid for, so when the budget runs
// out the batch has already sent up to one call fewer than its bound and pays
// for them: it can overshoot the budget by that many calls, and by none when
// the bound is one.
//
// The chunks are cut by Preflight before any call is made and none depends on
// another's answer, which is what lets them overlap. Adaptive chunk sizing
// (AdaptiveChunkState) is not part of this path; RunOpManyRecover is the entry
// point that uses it, and it stays serial because the size of each chunk is
// decided by how the one before it was answered.
//
// A single item's or chunk's failure fails the whole call, as MapReduce's
// default does everywhere else in this package. The first failure to happen
// stops the batch: the calls in flight are cancelled, nothing new starts, and
// that failure is the error returned. With the chunks overlapping, "first" is
// first in time, not first by position. A caller that needs the items
// that worked uses RunOpManyPartial (partial.go), which is a separate entry
// point so that this function's "any failure fails the call" stays what its
// callers rely on.
//
// Metadata is per result. For an item sent alone, Meta is that call's. For the
// items of a batched chunk, one call served them all, so the chunk's usage,
// cost, attempts, repairs and timing are reported once, on the chunk's first
// item, and are zero on the others: a caller who sums Meta.Cost over the
// results gets what was spent, not the chunk's cost once per item. A zero cost
// on such an item means "counted on the chunk's first item", not "free"; the
// descriptive fields (operation, provider, model, schema, contract, checks)
// are the chunk's on every item.
func RunOpMany[In, Out any](ctx context.Context, op Op[In, Out], items []In, req types.PlanRequest) (_ []types.Result[Out], _ types.Plan, runErr error) {
	// The client the batch runs on is fixed here, so the plan and every call
	// that carries it out read the same one.
	ctx, _ = PinExec(ctx)
	// The run is one call to an observer, and the requests of every item and chunk
	// are its children: the same items are one operation from every entry.
	ctx, observed := observeOperation(ctx, op.ID.Name)
	defer observed.end(&runErr)
	plan, err := PreflightFor(ctx, op, items, req)
	if err != nil {
		return nil, plan, err
	}
	if !plan.Eligible {
		// The plan says why it was refused, and what kind of refusal that is: an
		// over-budget plan is a budget error and not a configuration one, and an
		// item that cannot be encoded is an invalid request. A plan that was
		// refused with no kind named is still a refusal, and a configuration
		// error is the one that says the call cannot be made as asked.
		kind := plan.IneligibleKind
		if kind == types.KindUnknown {
			kind = types.KindConfiguration
		}
		return nil, plan, types.Classified(kind, fmt.Errorf("op %s: plan is not eligible: %s", op.ID, plan.IneligibleReason))
	}
	if len(items) == 0 {
		return nil, plan, nil
	}

	// Any item's failure fails the call and returns no result, so an observer is
	// told every item failed with it.
	switch plan.Shape {
	case types.ShapeAtomic:
		results, plan, err := runManyAtomic(ctx, op, items, req.Options, plan)
		observed.recordAllOrNothing(len(items), err)
		return results, plan, err

	case types.ShapeMDSP, types.ShapeGlobal:
		results, plan, err := runManyBatched(ctx, op, items, req.Options, plan)
		observed.recordAllOrNothing(len(items), err)
		return results, plan, err

	default:
		return nil, plan, types.Classified(types.KindConfiguration, fmt.Errorf("op %s: shape %s has no execution path in this delivery", op.ID, plan.Shape))
	}
}

// runManyAtomic runs one RunOpResult per item, bounded to manyConcurrency in
// flight, via MapReduce with ChunkSize 1 -- one goroutine pool, reused, rather
// than a second one this function starts by hand.
func runManyAtomic[In, Out any](ctx context.Context, op Op[In, Out], items []In, opt types.OpOptions, plan types.Plan) ([]types.Result[Out], types.Plan, error) {
	results, _, err := MapReduce(ctx, items,
		MapReduceOptions{ChunkSize: 1, Concurrency: manyConcurrency(ctx)},
		func(ctx context.Context, chunk []In) (types.Result[Out], error) {
			return RunOpResult(ctx, op, chunk[0], opt)
		})
	if err != nil {
		return nil, plan, err
	}
	return results, plan, nil
}

// runManyBatched runs RunOpBatch once per planned chunk, using the chunk
// sizes Preflight already computed rather than recomputing them: an execution
// that used a different chunk size than the plan it claims to be running is
// drift a caller comparing plan.Chunks against the run would not forgive.
//
// Oversized items (types.Plan.OversizedItems) run atomically, one call
// each, in the same pool as the chunks and after them in the order calls are
// started, so they overlap the chunks and one another. The plan's chunks have to
// account for every item that is not oversized; a plan that does not is refused
// before any call is made, rather than leaving the rest with empty results.
//
// Each chunk and each oversized item writes only the result slots of its own
// items, so no two calls write the same place.
func runManyBatched[In, Out any](ctx context.Context, op Op[In, Out], items []In, opt types.OpOptions, plan types.Plan) ([]types.Result[Out], types.Plan, error) {
	ctx = flowContext(ctx)
	results := make([]types.Result[Out], len(items))

	oversizedIndex := make(map[int]bool, len(plan.OversizedItems))
	for _, o := range plan.OversizedItems {
		oversizedIndex[o.Index] = true
	}

	// Chunked items, in the order Preflight assigned them: every index not
	// in oversizedIndex, grouped by plan.Chunks' sizes.
	var chunkedIndexes []int
	for i := range items {
		if !oversizedIndex[i] {
			chunkedIndexes = append(chunkedIndexes, i)
		}
	}

	// The whole plan is checked against the items before the first call. The
	// chunks used to be sent one at a time and the mismatch found when the loop
	// reached it or ended, after the earlier chunks had been paid for.
	var chunks [][]int
	offset := 0
	for _, chunk := range plan.Chunks {
		if chunk.ItemCount <= 0 {
			continue
		}
		end := offset + chunk.ItemCount
		if end > len(chunkedIndexes) {
			return nil, plan, types.Newf(types.KindInvariantViolation, op.ID.Name,
				"the plan's chunk boundaries (%d items) do not match the %d non-oversized items to run", offset+chunk.ItemCount, len(chunkedIndexes))
		}
		chunks = append(chunks, chunkedIndexes[offset:end])
		offset = end
	}
	if offset != len(chunkedIndexes) {
		return nil, plan, types.Newf(types.KindInvariantViolation, op.ID.Name,
			"the plan's chunks cover %d of the %d non-oversized items", offset, len(chunkedIndexes))
	}

	run := func(runCtx context.Context, task int) error {
		if task >= len(chunks) {
			index := plan.OversizedItems[task-len(chunks)].Index
			res, err := RunOpResult(runCtx, op, items[index], opt)
			if err != nil {
				return err
			}
			results[index] = res
			return nil
		}

		indexes := chunks[task]
		chunkItems := make([]In, len(indexes))
		for j, idx := range indexes {
			chunkItems[j] = items[idx]
		}
		result, err := RunOpBatchResult(runCtx, op, chunkItems, opt)
		if err != nil {
			return err
		}
		if len(result.Value) != len(indexes) {
			return types.Newf(types.KindInvariantViolation, op.ID.Name,
				"the batch returned %d outputs for %d items", len(result.Value), len(indexes))
		}
		for j, idx := range indexes {
			results[idx] = types.Result[Out]{Value: result.Value[j], Meta: itemMetaFromChunk(result.Meta, j == 0)}
		}
		return nil
	}

	tasks := len(chunks) + len(plan.OversizedItems)
	ran := runBounded(ctx, "runmany", tasks, boundedOptions{workers: manyConcurrency(ctx), stopOnFailure: true}, run)
	if ran.first != nil {
		return nil, plan, ran.first
	}
	for _, started := range ran.started {
		if !started {
			// Nothing failed, so the caller's context ended and the rest were
			// never sent. The classified cancellation is what a call that
			// reached the provider with that context would have returned.
			return nil, plan, fmt.Errorf("op %s: the batch was stopped before every call was sent: %w", op.ID, stoppedBy(ctx))
		}
	}

	return results, plan, nil
}

// manyConcurrency is how many calls of one batch are in flight at once when the
// caller did not choose a number. It is DefaultConcurrency, the bound
// single-item fan-out has always had, unless the context carries a scheduler
// that sets a limit. Then it is that limit, the smallest of the scheduler's
// global, per-provider and per-tenant limits, since every call goes through the
// scheduler and the scheduler decides how many of them run; and never more than
// eachAtOnce, so that a generous limit is still not a goroutine per chunk.
//
// A scheduler that sets no limit gives the same bound as no scheduler. The bound
// is also how far a batch can overshoot a client budget that refuses once a cost
// is recorded, and a scheduler built to queue or meter calls says nothing about
// wanting sixty-four of them started together.
func manyConcurrency(ctx context.Context) int {
	limit, _ := schedulerAdmitsAtOnce(ctx)
	switch {
	case limit <= 0:
		return DefaultConcurrency
	case limit > eachAtOnce:
		return eachAtOnce
	default:
		return limit
	}
}

// capToScheduler lowers a bound a caller chose to what the context's scheduler
// admits at once, so that a batch does not queue more calls than it can run: a
// scheduler with a short queue refuses the calls that do not fit, and a batch
// that was serial a moment ago would start getting refusals. A bound already
// below the scheduler's limit, or a context with no scheduler, is returned as it
// is.
func capToScheduler(ctx context.Context, bound int) int {
	if limit, scheduled := schedulerAdmitsAtOnce(ctx); scheduled && limit > 0 && limit < bound {
		return limit
	}
	return bound
}

// schedulerAdmitsAtOnce reports whether the context carries a client scheduler,
// and the most calls it lets run at once: the smallest of its limits that is
// set, or zero when it sets none. The limits are read from the scheduler as it
// was built; they do not change.
func schedulerAdmitsAtOnce(ctx context.Context) (limit int, scheduled bool) {
	scheduler := ExecScheduler(ctx)
	if scheduler == nil {
		return 0, false
	}
	for _, bound := range []int{scheduler.limits.MaxConcurrent, scheduler.limits.PerProviderConcurrency, scheduler.limits.PerTenantConcurrency} {
		if bound > 0 && (limit == 0 || bound < limit) {
			limit = bound
		}
	}
	return limit, true
}

// stoppedBy is the classified reason a context ended, as a call that found it
// over would report it. For a context that is not over it is a cancellation, which
// is still classified: a bare context error has no kind, and says the caller
// cancelled when it may have been another step's failure that did.
func stoppedBy(ctx context.Context) error {
	if err := contextOver(ctx); err != nil {
		return err
	}
	return types.Classified(types.KindCanceled, context.Canceled)
}

// boundedOptions says how runBounded runs its steps.
type boundedOptions struct {
	// workers is how many steps may be in flight at once. Fewer than one is one.
	workers int

	// stopOnFailure makes the first step to return an error cancel the context
	// the others run under and keep any further step from starting. Without it
	// every step runs whatever the others do.
	stopOnFailure bool

	// runAfterCancel keeps starting steps once the context is over, so that
	// every step is offered the cancelled context and reports the cancellation
	// itself. Without it, no step starts once the context is over.
	runAfterCancel bool
}

// boundedRun is what runBounded reports.
type boundedRun struct {
	// started[i] is whether step i was started. A step that was not started was
	// stopped by a failure (with stopOnFailure) or by the context ending.
	started []bool

	// errs[i] is what step i returned, or the error that a step which panicked,
	// or ended its goroutine, is reported as.
	errs []error

	// first is, with stopOnFailure, the error that stopped the run: the first
	// failure in time, and never the cancellation it caused in the others.
	first error

	// firstStep is the index of the step that failed with first, or -1 when no
	// step did.
	firstStep int
}

// runBounded runs run(ctx, i) for i in [0, n) on at most opts.workers
// goroutines and returns when every goroutine it started has ended. Steps are
// handed out in index order, so a bound of one is sequential and a failure
// leaves the later steps unstarted rather than racing to be the ones that were.
//
// It is the pool the batch entry points share, in place of a goroutine per step
// held back by a semaphore: a thousand inputs are a thousand cheap steps and
// opts.workers goroutines. The step runs under runGuarded, so a panic in it is
// an error for that step and not the process's end, and a step that ends its
// goroutine without returning (runtime.Goexit, as t.FailNow does) is reported
// the same way, as flow.go's gather reports it, and its worker is replaced so
// the steps after it still run.
func runBounded(ctx context.Context, op string, n int, opts boundedOptions, run func(ctx context.Context, i int) error) boundedRun {
	ran := boundedRun{started: make([]bool, n), errs: make([]error, n), firstStep: -1}
	if n == 0 {
		return ran
	}
	ctx = flowContext(ctx)
	workers := opts.workers
	if workers > n {
		workers = n
	}
	if workers < 1 {
		workers = 1
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		next  atomic.Int64
		wait  sync.WaitGroup
		first sync.Once
	)
	failed := func(step int, err error) {
		if opts.stopOnFailure {
			first.Do(func() {
				ran.first = err
				ran.firstStep = step
				cancel()
			})
		}
	}

	var worker func()
	worker = func() {
		current := -1
		finished := false
		defer func() {
			if !finished {
				// The step ended this goroutine. Nothing after it runs, so the
				// step's error and its replacement are recorded here.
				ran.errs[current] = stepPanicked(op)
				failed(current, ran.errs[current])
				wait.Add(1)
				go worker()
			}
			wait.Done()
		}()
		for opts.runAfterCancel || runCtx.Err() == nil {
			i := int(next.Add(1)) - 1
			if i >= n {
				break
			}
			current = i
			ran.started[i] = true
			if err := runGuarded(runCtx, op, i, run); err != nil {
				ran.errs[i] = err
				failed(i, err)
			}
		}
		finished = true
	}

	wait.Add(workers)
	for w := 0; w < workers; w++ {
		go worker()
	}
	wait.Wait()
	return ran
}

// itemMetaFromChunk is the Meta of one item of a batched chunk. chunk is the
// record of the single call that served every item in it, so what that call
// spent belongs to the chunk and not to each item: the first item carries it
// and the others carry zero, which makes a sum over the results the chunk's
// total once. The slices and map are copied so the items do not share them.
func itemMetaFromChunk(chunk types.Meta, first bool) types.Meta {
	item := chunk
	item.Checks = append([]types.Check(nil), chunk.Checks...)
	item.ModelClaims = maps.Clone(chunk.ModelClaims)
	if first {
		return item
	}
	item.Usage = types.TokenUsage{}
	item.Cost = types.CostInfo{}
	item.CacheHitRatio = 0
	item.Attempts = 0
	item.Repairs = 0
	item.Elapsed = 0
	item.Timing = types.Timing{}
	return item
}
