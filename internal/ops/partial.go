package ops

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/telemetry"
)

// Partial success and failure policies, as an item-level engine built on
// RunOpResult -- the same per-item unit runManyAtomic (plan_many.go) dispatches
// for ShapeAtomic, so there is one call path to keep in step with RunOp.
//
// RunOpManyPartial always dispatches one call per item, which is exactly
// ShapeAtomic. It does not recover inside a batched chunk: a chunk that
// answered most of its offered ids and dropped a few is RunOpMany's
// all-or-nothing case (plan_many.go). RunOpManyPartial is the entry point for
// a caller who wants per-item accounting and pays for it in calls, one per
// item rather than one per chunk.

// PartialConfig configures RunOpManyPartial.
type PartialConfig struct {
	// Policy selects the behaviour. PolicyUnspecified is PolicyCollectFailures,
	// whatever the number of items: a policy that changed with the length of the
	// input would return an error at 20 items and a result with failures in it at
	// 21. The resolved policy and the reason are recorded on the returned
	// BatchResult.Summary.
	Policy types.BatchPolicy

	// Concurrency bounds items in flight. Zero means the bound every engine
	// that sends many requests shares (manyConcurrency): DefaultConcurrency, or
	// the limit of the context's client scheduler when it sets one (at most 64).
	Concurrency int

	// MaxRetries bounds PolicyRetryFailedItems / PolicyRetryThenCollect's
	// retry pass: how many retries, after the first pass, an item that failed
	// gets, isolated from every other item. Only a failure that
	// isRetryableLLMError allows is retried, and no round starts once ctx has
	// ended. Zero or less means DefaultMaxRetries, and a value above
	// MaxRetriesLimit is MaxRetriesLimit.
	//
	// A retry multiplies what the item costs: an item can make at most
	// (1+MaxRetries) x (RepairAttempts+1) x (Client retries+1) provider requests,
	// because each of those layers repeats what the one inside it already repeated.
	MaxRetries int
}

// DefaultMaxRetries is PartialConfig.MaxRetries' default -- bounded, not
// unbounded, so an item that keeps failing cannot spend the whole call's budget
// on retries.
const DefaultMaxRetries = 2

// MaxRetriesLimit is the most retry rounds a run makes whatever PartialConfig
// asks for: the product above grows with it, and a caller who needs more than
// five rounds for an item has a provider that is down.
const MaxRetriesLimit = 5

// itemOutcome is one item's state while RunOpManyPartial is still running,
// before it is rendered into a types.ItemResult.
type itemOutcome[Out any] struct {
	status    types.ItemStatus
	value     Out
	attempts  int
	repairs   int
	cost      types.CostInfo
	delivered types.ContractLevel
	err       error
}

// outcomeFrom turns one RunOpResult call's return into an itemOutcome, which is
// where an item's attempts, repairs and cost are read from the same envelope
// every other operation in this package already reports through -- no second
// accounting pass invented for this engine alone.
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
		status:    types.ItemSucceeded,
		value:     res.Value,
		attempts:  res.Meta.Attempts,
		repairs:   res.Meta.Repairs,
		cost:      res.Meta.Cost,
		delivered: res.Meta.DeliveredContract,
	}
}

// RunOpManyPartial runs op over items, one call per item, applying
// cfg.Policy, and returns a types.BatchResult naming what happened to every
// item -- the contract a bare ([]Out, error) cannot express.
//
// A call that cannot be run is refused before anything is sent and before any
// function of the caller's is called, with the empty result and an error: a nil
// context (an invalid request), a context that is already over (canceled or
// timeout), a descriptor with no BuildPrompt or Decode or a policy that is none of
// the five (configuration), and a contract floor the descriptor cannot reach (a
// policy violation). Once requests have been sent the result is returned, with
// whatever error the policy gives.
//
// PolicyUnspecified is PolicyCollectFailures.
//
// Once requests have been sent, the returned error is non-nil exactly when
// cfg.Policy's own contract says a partial result should fail the call
// (PolicyFailFast, PolicyRequireAll, and PolicyRetryFailedItems when the retry
// pass did not resolve every failure); it is always nil for
// PolicyCollectFailures and PolicyRetryThenCollect, whose contract is "read
// BatchResult.Complete(), not the error return". The BatchResult is returned
// beside the error -- unlike MapReduce's default behaviour elsewhere in this
// package, which nils the whole result on the failure that stops the run, an error
// here never hides which items got through. That is the one place in the package
// where a value comes back beside an error, by design, and a caller that exposes
// this engine hides it. A run that collects its failures returns a nil error even
// when every item failed, so an observer is told what became of the items and not
// only that the call returned.
//
// The error of an item is classified, and its text is the library's own: the
// operation's name, the kind of the failure, and the position ("item 3 of 8"). What
// the failure was, a vendor's message or the text of the caller's own Decode or
// invariant error, is the error's cause, reached with errors.Is and errors.As, and
// is not copied into the text, which callers log.
//
// Under PolicyFailFast the error is classified as the failure that stopped the
// run, the first in time, and carries it as its cause. An item that was in
// flight when that failure cancelled the run, or that was never started because
// of it, is counted as cancelled, not as a second failure, and its error is of
// the kind canceled with the failure that stopped the run as its cause. An item
// that the run's context ended is cancelled under every policy: Summary.Failed
// counts what went wrong with an item, and a caller's own cancellation is not
// that.
//
// The retry rounds multiply what an item costs: at most (1+MaxRetries) x
// (RepairAttempts+1) x (Client retries+1) provider requests, with MaxRetries at
// most MaxRetriesLimit.
//
// The whole run is one call of the operation to an installed observer, and
// BatchResult.Total is what all of it cost: every request made, including one whose
// item then failed or whose caller's function panicked on its answer. A function
// of the caller's that ends its goroutine with runtime.Goexit leaves no account
// of the request it was answering; the item fails with an internal error and the
// goroutine of the pool is replaced.
//
// Items overlap, so a client's budget, which is checked as each call starts, can
// be overshot: when it runs out, up to Concurrency less one calls (or the default
// bound less one) have already started and are paid for. With a bound of one it
// cannot.
//
// A function of the caller's that blocks, and does not look at the context it
// cannot see, holds the call: nothing in Go can end it.
func RunOpManyPartial[In, Out any](ctx context.Context, op Op[In, Out], items []In, opt types.OpOptions, cfg PartialConfig) (_ types.BatchResult[Out], runErr error) {
	name := operationLabel(op.ID)
	// Nothing reads a nil context as Background: the run could not be
	// cancelled, and a caller who passed one has made a mistake worth
	// reporting.
	if ctx == nil {
		return types.BatchResult[Out]{}, types.Newf(types.KindInvalidRequest, name, "a nil context was given")
	}
	started := time.Now()

	// The client the whole run uses is fixed once, so that a default replaced
	// while the items run does not give one item the provider of one client and
	// the next the budget of another.
	ctx, _ = PinExec(ctx)
	// The run is one call to an observer, and the requests the items make are
	// its children.
	ctx, observed := observeOperation(ctx, op.ID.Name)
	defer observed.end(&runErr)

	// A context that is already over runs nothing: not an item, and not a function
	// of the caller's.
	if over := contextOver(ctx); over != nil {
		return types.BatchResult[Out]{}, failureOf(name, batchFailureKind(over), over, "the context had already ended, so nothing was run")
	}

	policy := cfg.Policy
	reason := ""
	if policy == types.PolicyUnspecified {
		policy, reason = types.PolicyCollectFailures, "no policy specified; every item runs and its failures are collected, so read BatchResult.Complete() and Failures()"
	}
	switch policy {
	case types.PolicyFailFast, types.PolicyCollectFailures, types.PolicyRetryFailedItems,
		types.PolicyRetryThenCollect, types.PolicyRequireAll:
	default:
		return types.BatchResult[Out]{}, types.Newf(types.KindConfiguration, name, "the batch policy is none of the five this run knows")
	}
	if op.BuildPrompt == nil {
		return types.BatchResult[Out]{}, types.Newf(types.KindConfiguration, name, "there is no BuildPrompt to render a request from")
	}
	if op.Contract.Decode == nil {
		return types.BatchResult[Out]{}, types.Newf(types.KindConfiguration, name, "there is no Decode to turn a response into a value")
	}

	// The floor of the caller's policy is asked before anything is sent. Each item
	// would refuse for itself, and a run that collects its failures would then
	// report every item as failed beside a nil error.
	planned := plannedContractLevel(ctx, declaredContractLevel(op.Contract), opt)
	if floorErr := checkContractFloor(ctx, planned); floorErr != nil {
		return types.BatchResult[Out]{}, floorErr
	}

	var result types.BatchResult[Out]
	result.Summary = types.BatchSummary{Total: len(items), Policy: policy, PolicyReason: reason}
	if len(items) == 0 {
		return result, nil
	}

	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = manyConcurrency(ctx)
	}
	maxRetries := cfg.MaxRetries
	switch {
	case maxRetries <= 0:
		maxRetries = DefaultMaxRetries
	case maxRetries > MaxRetriesLimit:
		maxRetries = MaxRetriesLimit
	}

	outcomes := make([]itemOutcome[Out], len(items))
	spent := &spend{}

	// stopped is the failure that stopped a fail-fast run: the first in time.
	var stopped error
	switch policy {
	case types.PolicyFailFast:
		stopped = runFailFast(ctx, op, items, opt, concurrency, outcomes, spent)

	case types.PolicyCollectFailures, types.PolicyRequireAll:
		runCollect(ctx, op, items, opt, concurrency, outcomes, spent)

	case types.PolicyRetryFailedItems, types.PolicyRetryThenCollect:
		runCollect(ctx, op, items, opt, concurrency, outcomes, spent)
		runRetryPass(ctx, op, items, opt, concurrency, maxRetries, outcomes, spent)
	}

	result.Items = make([]types.ItemResult[Out], len(items))
	delivered, anyDelivered := types.ContractLevel(0), false
	for i, o := range outcomes {
		var itemErr error
		switch o.status {
		case types.ItemFailed:
			itemErr = itemFailure(name, i, len(items), o.err)
		case types.ItemCancelled:
			itemErr = itemStopped(name, i, len(items), o.err)
		}
		result.Items[i] = types.ItemResult[Out]{
			Index:    i,
			Status:   o.status,
			Value:    o.value,
			Attempts: o.attempts,
			Repairs:  o.repairs,
			Cost:     o.cost,
			Mode:     types.ShapeAtomic.String(),
			Err:      itemErr,
		}
		switch o.status {
		case types.ItemSucceeded:
			result.Summary.Succeeded++
			if !anyDelivered || o.delivered < delivered {
				delivered, anyDelivered = o.delivered, true
			}
		case types.ItemFailed:
			result.Summary.Failed++
		case types.ItemCancelled:
			result.Summary.Cancelled++
		}
	}

	// What became of the items is what an observer is told of them: a run that
	// collects its failures returns no error, and the counts are the only trace
	// of its failed items.
	observed.recordItems(result.Summary.Total, result.Summary.Succeeded, result.Summary.Failed, result.Summary.Cancelled, firstItemFailure(result.Items))

	result.Total = spent.total(op.ID.String())
	result.Total.Elapsed = time.Since(started)
	result.Total.Strategy = types.ShapeAtomic.String()
	result.Total.RequestedContract = planned
	result.Total.DeliveredContract = delivered

	// The counters are emitted whether or not this policy goes on to fail the
	// call: a policy that returns an error still ran real provider calls that
	// cost real money, and a metric that only fired on success would hide
	// exactly the runs a caller most needs to see.
	emitBatchMetrics(op.ID.String(), "atomic_partial", result)

	switch policy {
	case types.PolicyFailFast, types.PolicyRequireAll, types.PolicyRetryFailedItems:
		if result.Summary.Failed > 0 || result.Summary.Cancelled > 0 {
			// The failure that ended the run is the error's cause and gives it its
			// kind: the one that stopped a fail-fast run, and otherwise the first
			// item that failed, by position, or when every item was cancelled the
			// first of those. Its text stays out of the message, which is the
			// summary.
			cause := stopped
			if cause == nil {
				cause = firstItemFailure(result.Items)
			}
			if cause == nil {
				cause = firstItemCancellation(result.Items)
			}
			return result, &types.OperationError{
				Kind:    batchFailureKind(cause),
				Op:      name,
				Message: result.String(),
				Cause:   cause,
			}
		}
	}

	return result, nil
}

// operationLabel names the operation in an error: the name its author gave it,
// and a plain word when there is none, never the "name@version" of an ID, which
// reads as a file name.
func operationLabel(id types.OperationID) string {
	if id.Name == "" {
		return "operation"
	}
	return id.Name
}

// failureOf is an error for the caller of an engine: the kind, the operation's
// name and a message that is the library's own, with what actually failed as its
// cause. errors.Is and errors.As reach the cause, so a caller's own error is still
// the caller's to match, and the text a caller logs carries none of what the cause
// says, which can be a vendor's message or the text of the caller's own Decode,
// and can hold their data.
//
// What the cause says about the failure that is not text is kept: a refusal or a
// panic is still marked as one, and the route, the request id and the wait the
// provider asked for are the ones the cause carried.
func failureOf(op string, kind types.ErrorKind, cause error, format string, args ...any) *types.OperationError {
	failure := &types.OperationError{Kind: kind, Op: op, Message: fmt.Sprintf(format, args...), Cause: cause}
	if cause == nil {
		return failure
	}
	failure.Narrow = types.NarrowingOf(cause)
	var inner *types.OperationError
	if errors.As(cause, &inner) && inner != nil {
		failure.Provider, failure.Model = inner.Provider, inner.Model
		failure.RequestID, failure.CorrelationID, failure.AttemptID = inner.RequestID, inner.CorrelationID, inner.AttemptID
		failure.RetryAfter, failure.Ambiguous = inner.RetryAfter, inner.Ambiguous
	}
	if provider, model, requestID, retryAfter, routed := types.CallOf(cause); routed {
		if failure.Provider == "" {
			failure.Provider = provider
		}
		if failure.Model == "" {
			failure.Model = model
		}
		if failure.RequestID == "" {
			failure.RequestID = requestID
		}
		if failure.RetryAfter == 0 {
			failure.RetryAfter = retryAfter
		}
	}
	return failure
}

// itemFailure is the error of an item that failed: its place among the items, and
// the failure as its cause.
func itemFailure(op string, index, total int, cause error) error {
	return failureOf(op, batchFailureKind(cause), cause, "item %d of %d failed", index+1, total)
}

// itemStopped is the error of an item that was cut off, or never started, because
// the run was stopped: by the context ending, or by the failure of another item.
// Its kind is the cancellation's own when the context ended, and canceled when
// another item's failure stopped the run, with that failure as the cause.
func itemStopped(op string, index, total int, cause error) error {
	kind := types.KindCanceled
	switch types.KindOf(cause) {
	case types.KindCanceled, types.KindTimeout:
		kind = types.KindOf(cause)
	}
	return failureOf(op, kind, cause, "item %d of %d was stopped before it finished", index+1, total)
}

// runOpItem runs op on one input as RunOpResult does, and keeps the account of
// what it cost when a function of the caller's panics on the answer.
//
// A request that was paid for is recorded on the context the call runs under, and
// RunOpResult builds its envelope from it; a panic in the caller's Decode, an
// invariant or Normalize unwinds through RunOpResult and the account goes with it.
// The caller's functions are therefore run under a guard that turns a panic into
// an error of kind invariant violation, marked as a panic, and cancels the call's
// context so that nothing more is sent for the item: RunOpResult returns normally,
// with the envelope of everything that was spent. The panic's value is not in the
// error, only that there was one, and the type of it is logged.
//
// A function that ends its goroutine with runtime.Goexit cannot be turned into
// anything here, and its call leaves no account.
func runOpItem[In, Out any](ctx context.Context, op Op[In, Out], input In, opt types.OpOptions) (types.Result[Out], error) {
	itemCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var panicked atomic.Bool
	trapped := func(function string, recovered any) {
		loggerFor(ctx).Error("A function of an operation panicked", "operation", op.ID.Name, "function", function, "panic_type", fmt.Sprintf("%T", recovered))
		panicked.Store(true)
		cancel()
	}
	panicFailure := func() error {
		failure := types.Newf(types.KindInvariantViolation, operationLabel(op.ID), "a function of the operation panicked")
		failure.Narrow = types.NarrowPanic
		return failure
	}

	guarded := op
	if op.BuildPrompt != nil {
		guarded.BuildPrompt = func(in In, o types.OpOptions) (system, user string) {
			defer func() {
				if recovered := recover(); recovered != nil {
					trapped("BuildPrompt", recovered)
				}
			}()
			return op.BuildPrompt(in, o)
		}
	}
	if op.Contract.Decode != nil {
		guarded.Contract.Decode = func(body string) (value Out, err error) {
			defer func() {
				if recovered := recover(); recovered != nil {
					trapped("Decode", recovered)
					var none Out
					value, err = none, panicFailure()
				}
			}()
			return op.Contract.Decode(body)
		}
	}
	if len(op.Contract.Invariants) > 0 {
		guarded.Contract.Invariants = make([]func(Out) error, len(op.Contract.Invariants))
		for i, invariant := range op.Contract.Invariants {
			guarded.Contract.Invariants[i] = func(value Out) (err error) {
				defer func() {
					if recovered := recover(); recovered != nil {
						trapped("invariant", recovered)
						err = panicFailure()
					}
				}()
				return invariant(value)
			}
		}
	}
	if op.Contract.Normalize != nil {
		guarded.Contract.Normalize = func(value Out) (out Out) {
			out = value
			defer func() {
				if recovered := recover(); recovered != nil {
					trapped("Normalize", recovered)
				}
			}()
			return op.Contract.Normalize(value)
		}
	}

	res, err := RunOpResult(itemCtx, guarded, input, opt)
	if panicked.Load() {
		var none Out
		res.Value = none
		return res, panicFailure()
	}
	return res, err
}

// emitBatchMetrics tells the metrics what a finished plural run measured: item
// outcomes, repairs, omissions, fallbacks and cost. The telemetry layer does not
// record them unless metrics are on, and the measurement, which walks every
// item, is not made when they are not.
func emitBatchMetrics[Out any](operation, shape string, result types.BatchResult[Out]) {
	if !telemetry.MetricsEnabled() {
		return
	}
	telemetry.RecordBatchMetrics(operation, shape, result.Metrics())
}

// batchFailureKind is the kind a call that failed because of its items has: the
// cause's own, and what the call path calls a failure it does not know, an
// unavailable provider, when nothing in the cause's chain says. No error leaves a
// run without one.
func batchFailureKind(cause error) types.ErrorKind {
	if kind := llm.Classify(cause); kind != types.KindUnknown {
		return kind
	}
	return types.KindProviderUnavailable
}

// firstItemFailure is the error of the first item, by position, that failed with
// one, or nil when none did. Unlike the failure that stops a fail-fast run, it is
// not first in time: a run that collects its failures has no such moment.
func firstItemFailure[Out any](items []types.ItemResult[Out]) error {
	for _, item := range items {
		if item.Status == types.ItemFailed && item.Err != nil {
			return item.Err
		}
	}
	return nil
}

// firstItemCancellation is the error of the first item, by position, that was
// cancelled with one: the reason a run in which nothing failed, and everything
// was stopped, ended.
func firstItemCancellation[Out any](items []types.ItemResult[Out]) error {
	for _, item := range items {
		if item.Status == types.ItemCancelled && item.Err != nil {
			return item.Err
		}
	}
	return nil
}

// runCollect runs every item exactly once, bounded to concurrency in
// flight, with no cancellation on a failure -- PolicyCollectFailures' and
// PolicyRequireAll's shared scheduling, and the first pass
// PolicyRetryFailedItems/PolicyRetryThenCollect build their retry pass on
// top of.
//
// Every item is offered to RunOpResult even when ctx is over, which reports the
// cancellation as that item's own error. An item whose call panicked, or ended
// its goroutine, is a failed item with an error that says so.
func runCollect[In, Out any](ctx context.Context, op Op[In, Out], items []In, opt types.OpOptions, concurrency int, outcomes []itemOutcome[Out], spent *spend) {
	ran := runBounded(ctx, "partial", len(items), boundedOptions{workers: concurrency, runAfterCancel: true}, func(runCtx context.Context, i int) error {
		res, err := runOpItem(runCtx, op, items[i], opt)
		spent.add(res.Meta)
		outcomes[i] = outcomeFrom(res, err)
		if endedBy(runCtx, err) {
			outcomes[i].status = types.ItemCancelled
		}
		return nil
	})
	for i, err := range ran.errs {
		if err != nil {
			outcomes[i] = outcomeFrom(types.Result[Out]{}, err)
		}
	}
}

// runFailFast runs items bounded to concurrency in flight, stopping the
// dispatch of new items the instant one fails and marking every item that
// was never started ItemCancelled -- never ItemFailed, since nothing about
// an unstarted item's own input was found wrong. A call that was in flight when
// the run was stopped, and returned the cancellation, is the same: its input was
// not found wrong either, and counting it among the failures would report two
// where one item failed. Its error stays beside the status.
//
// It returns the failure that stopped the run, the first in time, or nil when
// nothing failed.
//
// It runs on runBounded, the pool MapReduce (mapreduce.go) uses too, which hands
// indices out in order. A semaphore racing goroutines dispatches in whatever
// order the scheduler wakes them, which makes "stop after item 3 fails"
// non-deterministic about which items after it actually ran. Handing the indices
// out in order keeps dispatch order fixed regardless of concurrency.
func runFailFast[In, Out any](ctx context.Context, op Op[In, Out], items []In, opt types.OpOptions, concurrency int, outcomes []itemOutcome[Out], spent *spend) error {
	ran := runBounded(ctx, "partial", len(items), boundedOptions{workers: concurrency, stopOnFailure: true}, func(runCtx context.Context, i int) error {
		res, err := runOpItem(runCtx, op, items[i], opt)
		spent.add(res.Meta)
		outcomes[i] = outcomeFrom(res, err)
		if endedBy(runCtx, err) {
			outcomes[i].status = types.ItemCancelled
		}
		return err
	})

	for i := range outcomes {
		switch {
		case ran.started[i] && outcomes[i].status == types.ItemUnspecified:
			// The call panicked or ended its goroutine, and wrote nothing.
			outcomes[i] = outcomeFrom(types.Result[Out]{}, ran.errs[i])
		case !ran.started[i]:
			// Never started: the caller's context ended, or another item failed first.
			// A bare context error says neither, and names the wrong cause when the
			// caller's context is alive.
			cause := stoppedBy(ctx)
			if ctx.Err() == nil && ran.first != nil {
				cause = stoppedBySibling(ran.first)
			}
			outcomes[i] = itemOutcome[Out]{status: types.ItemCancelled, err: cause}
		}
	}
	return ran.first
}

// stoppedBySibling is why an item a fail-fast run never started has no answer:
// another item failed first. The failure that stopped the run is its cause, and
// the text is the library's.
func stoppedBySibling(first error) error {
	return &types.OperationError{Kind: types.KindCanceled, Message: "another item failed first and stopped the run", Cause: first}
}

// endedBy reports whether err is the reason ctx is over: a call that returns it
// was stopped by the context and failed of nothing of its own. A call that timed
// out under its own deadline while the context was cancelled for another reason
// is not.
func endedBy(ctx context.Context, err error) bool {
	reason := ctx.Err()
	return err != nil && reason != nil && errors.Is(err, reason)
}

// runRetryPass retries, isolated and bounded to concurrency in flight, only
// the items outcomes marks ItemFailed whose failure is worth sending again: a
// valid item is never replayed because a sibling failed, and a failure that
// repeats identically (a refusal, a malformed request, an exhausted budget) is
// not billed a second and third time. That decision is isRetryableLLMError's,
// the one the rest of the package uses. It runs up to maxRetries additional
// rounds, stopping early once nothing is left to retry or ctx has ended: a
// cancelled run would otherwise walk every pending item and fail each at once.
//
// A round that ctx ends part of the way through leaves every item it did not
// finish with the failure it had. The retry that was in flight returns the
// cancellation, which says the caller gave up and not what was wrong with the
// item, so it is not recorded in place of the failure that was; what the cut-off
// call used is still added to the item's attempts and cost.
func runRetryPass[In, Out any](ctx context.Context, op Op[In, Out], items []In, opt types.OpOptions, concurrency, maxRetries int, outcomes []itemOutcome[Out], spent *spend) {
	for attempt := 0; attempt < maxRetries; attempt++ {
		if ctx.Err() != nil {
			return
		}
		var pending []int
		for i, o := range outcomes {
			if o.status == types.ItemFailed && isRetryableLLMError(o.err) {
				pending = append(pending, i)
			}
		}
		if len(pending) == 0 {
			return
		}

		ran := runBounded(ctx, "partial", len(pending), boundedOptions{workers: concurrency}, func(runCtx context.Context, k int) error {
			i := pending[k]
			priorAttempts := outcomes[i].attempts
			priorRepairs := outcomes[i].repairs
			priorCost := outcomes[i].cost
			priorErr := outcomes[i].err

			res, err := runOpItem(runCtx, op, items[i], opt)
			spent.add(res.Meta)
			o := outcomeFrom(res, err)
			o.attempts += priorAttempts
			o.repairs += priorRepairs
			o.cost = addCost(priorCost, o.cost)
			if endedBy(runCtx, err) {
				o.err = priorErr
			}
			outcomes[i] = o
			return nil
		})
		// A retry that panicked or ended its goroutine left the item's earlier
		// failure in place; the failure it reports is the one that happened last.
		for k, err := range ran.errs {
			if err != nil {
				outcomes[pending[k]].err = err
			}
		}
	}
}

// addCost sums two CostInfo values' spend, carrying Priced/Currency/Quality/
// PricingSource from whichever side reports pricing -- so a retry pass's cost
// accounting never silently drops the first attempt's spend, which is money
// that was really paid even though that attempt did not produce a usable
// answer.
//
// Quality and PricingSource matter here: a retried item's summed CostInfo that
// kept Priced == true (correct) but reset Quality to its zero value would carry
// a quality that is not PricingUnknown, PricingExact, PricingEstimated, or
// PricingFree -- an invalid quality on a record a caller's Cost.Quality.Known()
// check would otherwise trust. A record that says "priced" while carrying a
// quality nothing recognizes is exactly the kind of number this library is not
// supposed to invent.
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
	// Prefer the priced side's Quality/PricingSource; when both sides are
	// priced (a retry that also had a priced prior attempt) they came from
	// the same op and model, so they already agree -- a favors the earlier
	// attempt only because there is no third value to prefer between equals.
	switch {
	case a.Priced:
		sum.Quality = a.Quality
		sum.PricingSource = a.PricingSource
	case b.Priced:
		sum.Quality = b.Quality
		sum.PricingSource = b.PricingSource
	}
	return sum
}
