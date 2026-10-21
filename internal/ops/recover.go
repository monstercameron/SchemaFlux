package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// The progressive recovery cascade.
//
// RunOpBatch (batchprotocol.go) runs one batched request and requires exact id
// coverage before decoding anything: a chunk that omits, duplicates, or invents
// even one id fails the WHOLE chunk, and the eighteen items that came back
// correctly are thrown away along with the two that did not. This file closes
// that gap: ask for the items in batches, keep the valid items, isolate only the
// unresolved ones, ask again in a smaller batch, and only then fall back to one
// request for one item.
//
// The honest part of this contract, and the reason it is not just "retry until it
// works": every item the caller offered ends up in the returned BatchResult
// exactly once, and ItemResult.Mode says whether that item's value came from a
// batched request or from a request of its own -- a batched answer and an atomic
// retry are different provenance, and a caller reconciling a bill or a quality
// regression needs to tell them apart. A valid item from an early pass is never
// replayed because a sibling in the same chunk failed; only the unresolved subset
// moves to the next pass.
//
// AdaptiveChunkState (adaptive.go) is fed the first-pass outcome of every
// top-level chunk (the isolate passes and the atomic fallback do not feed it --
// they are this chunk's own recovery, not a signal about the *next* chunk's
// size), and its Size() decides how large the next top-level chunk is. A run that
// keeps omitting items converges on a smaller top-level chunk size over its own
// lifetime: a provider that truncates above 20 items converges to a stable size
// and stays there.
//
// What this delivers: batched requests -> keep valid items -> isolate unresolved
// ids -> a smaller batched request (a bounded number of passes) -> one request
// per item.
//
// What it does not: escalating to a different model or provider before the atomic
// fallback, which is conditioned on the minimum contract and data policy
// surviving, a capability-negotiation question this path has no seam for, so
// every fallback call runs under the same options the caller supplied. And a
// review packet at budget exhaustion: an item that fails its own atomic call is
// reported ItemFailed with the error that produced it, which is the material a
// review packet would be built from, but nothing here assembles that packet.
// GlobalValidate (BatchAggregate operations such as a future Sort or Cluster) is
// also out of scope -- this cascade is for itemwise operations, where each item
// maps to exactly one independent output; an aggregate operation's whole-set
// validation cannot be checked against a partially recovered set at all, and such
// an operation is refused.

// RecoverConfig configures RunOpManyRecover.
type RecoverConfig struct {
	// MaxItemsPerCall bounds every batched request's chunk size, and so the
	// top-level chunk's ceiling and the ceiling AdaptiveChunkState may grow back
	// up to. Zero (the common case) means the planner's own default
	// (defaultMaxItemsPerCall, plan.go). The ceiling the run uses is the largest
	// chunk the planner (Preflight) cuts under this bound, which also leaves room in
	// the model's window for the system prompt and the answers.
	MaxItemsPerCall int

	// StartItemsPerCall is the size of the very first top-level chunk, and
	// AdaptiveChunkState's starting point. Zero means the ceiling -- start at the
	// ceiling, the same starting point a first-time caller with no history of this
	// provider's behaviour already gets from a plain plan.
	StartItemsPerCall int

	// MaxIsolatePasses bounds how many times an unresolved remainder is
	// retried at a smaller size, isolated from the items that already
	// resolved, before whatever is still unresolved falls back to one request
	// per item. Zero means DefaultMaxIsolatePasses, and a value above
	// MaxIsolatePassesLimit is MaxIsolatePassesLimit. There is no way to ask for
	// zero isolate passes (go straight from the first batched request to the
	// atomic one) through this field -- the same "zero means default"
	// convention MaxRetries (partial.go) already uses in this package.
	MaxIsolatePasses int

	// Concurrency bounds the atomic-fallback stage's items in flight. Zero means
	// the bound every engine that sends many requests shares (manyConcurrency):
	// DefaultConcurrency, or the limit of the context's client scheduler when it
	// sets one (at most 64).
	Concurrency int
}

// DefaultMaxIsolatePasses is RecoverConfig.MaxIsolatePasses' default: one
// retry at a smaller size before whatever is left falls back to one request per
// item. Not unbounded, for the same reason DefaultMaxRetries (partial.go) is not:
// an id that a provider keeps failing to answer for a reason isolation cannot
// fix (a malformed protocol on every call, not a size problem) must not spend the
// whole run's budget retrying something that will not come back.
const DefaultMaxIsolatePasses = 1

// MaxIsolatePassesLimit is the most isolate passes a chunk gets whatever
// RecoverConfig asks for. The number of requests a run can make grows with it
// (recoverRequestBound), and a pass that resolved nothing is never followed by
// another.
const MaxIsolatePassesLimit = 3

// outageRequestLimit is how many requests in a row have to fail in transport (a
// rate limit, an outage, a timeout, a scheduler that refuses) before the run
// stops and fails what is left with the last of those failures. One request that
// fails that way says nothing about the chunk, so it does not shrink it; a run of
// them says the provider is not answering, and sending each remaining item on its
// own to find that out again would make the most requests the design allows
// against a provider that is already refusing them.
const outageRequestLimit = 3

// recoverRequestBound is the most requests RunOpManyRecover sends for n items
// when it is allowed isolatePasses isolate passes, counting each request the
// library sends once (the client repeats a request that failed in transport up to
// Client.Retries times, and an atomic request may be repaired up to
// RepairAttempts times; each of those is a provider call inside one request).
//
// A chunk of c items sends at most 2*isolatePasses + c batched requests: the first
// request, the two halves each isolate pass can send when the one before it
// resolved nothing, and the requests of a split by refusal, which are fewer than
// the chunk's items. A chunk of one item sends none, and its item is sent alone.
// There are at most n/2 chunks of two or more items and their sizes add up to at
// most n, so the batched requests are at most 2*isolatePasses*(n/2) + n; each item
// is then sent alone at most once, which is n more.
func recoverRequestBound(n, isolatePasses int) int {
	if n <= 0 {
		return 0
	}
	return 2*n + 2*isolatePasses*(n/2)
}

// recoverRecord is one item's state while the cascade is still running,
// before it is rendered into a types.ItemResult. Distinct from partial.go's
// itemOutcome: this tracks resolution across several passes at possibly
// different chunk memberships, accumulating attempts and cost across all of
// them, where itemOutcome is written once per RunOpResult call.
type recoverRecord[Out any] struct {
	resolved bool
	// tried marks an item that a batched pass was made for, whether or not a request
	// was sent for it: an item the batched rung did not resolve and that is then sent
	// alone is a fallback, where one that was never part of a batch is not.
	tried bool
	// stopped marks an item the cascade gave up on because the failure will
	// repeat: no later rung is tried for it.
	stopped bool
	value   Out
	// delivered is the contract level the request that resolved the item
	// delivered.
	delivered types.ContractLevel
	attempts  int
	repairs   int
	cost      types.CostInfo
	mode      string
	err       error
}

// ModeMDSP is the ItemResult.Mode value for an item resolved from a batched
// response -- types.ExecutionShape's own "mdsp" string (types.ShapeMDSP.String()),
// which RunOpBatchResult (batchprotocol.go) writes into Meta.Strategy for the same
// reason. It is a constant: a package variable could be assigned by any caller,
// and every item the cascade reports would then carry the new word and no longer
// match what types.BatchMetrics compares it against. ModeAtomicFallback lives in
// internal/types (types.ModeAtomicFallback) rather than here, because
// types.BatchResult.Metrics() has to compare ItemResult.Mode against it and
// internal/types cannot import internal/ops.
const ModeMDSP = "mdsp"

// RunOpManyRecover runs op over items through the progressive recovery cascade:
// batched requests over top-level chunks sized by an AdaptiveChunkState that
// reacts to what actually came back, isolate-and-retry for any chunk that does
// not cover its own ids exactly, and an atomic fallback call for whatever
// isolation could not resolve.
//
// codec is the same BatchItemCodec op's batch algebra was built from with
// NewIDBatchAlgebra, and has to be given again here: op.Batch.Merge is an opaque
// closure with no way to ask it for a partial decode, and mergeIDBatchPartial
// needs codec.DecodeItem itself. Nothing checks that the two agree.
//
// What the cascade does with an answer. An answer is attributed to an item by the
// id its element carries, an id this request offered and no other request did, and
// by nothing else; it is never taken from a position. An element whose id was
// not offered in the request, or that answers an id the response already answered,
// is no answer to any item: it fills no other item, and an extra element that
// leaves every offered id answered once costs nothing. An item the response did
// not answer is exactly the item whose id is missing: a response that drops
// one answer leaves one item unresolved and does not move the rest. What cannot
// be detected is a model that writes one item's answer under another item's id:
// nothing in Go can compare an answer with its question, so run one request per
// item (RunOpManyPartial) when that matters.
//
// A call that cannot be run is refused before anything is sent, with the empty
// result and an error: a nil context (an invalid request); a context that is
// already over (canceled or timeout); an operation whose batch is not an itemwise
// batch built by NewIDBatchAlgebra, whose Encode is missing, or that has no
// BuildPrompt or Decode, or a codec with no DecodeItem, or that requires evidence,
// which a batched answer does not carry (configuration); and a contract floor the
// batch cannot reach (a policy violation).
//
// The planner decides the size of the batches: the ceiling, and the start, are
// the largest chunk Preflight cuts for the client's model, a chunk is never
// larger than the one the planner cut it from, and an item too large to share a
// request with any other is sent alone, without being asked in a batch first. A
// chunk of one item is sent alone as well.
//
// Once requests have been sent it returns a nil error whenever the cascade ran to
// completion, whatever the outcome -- the same "read BatchResult.Complete(), not
// the error return" contract types.PolicyCollectFailures documents, because a
// cascade IS the caller asking for whatever can be recovered rather than an
// all-or-nothing call. That includes a run that all of its items failed and one
// whose context ended: the items that finished are kept, and each other item is
// cancelled with an error of kind canceled or timeout that also matches
// context.Canceled or context.DeadlineExceeded. Summary.Policy is recorded as
// types.PolicyCollectFailures with a PolicyReason explaining it is this
// cascade's own terminal step, since the cascade is not a scheduling policy over
// atomic calls but a recovery ladder over batched ones. It never returns a result
// beside an error.
//
// A request that fails in a way that sending it again would repeat (a refusal by
// the client's data policy, a spent budget, a key the provider does not accept, a
// route that cannot take the request, or the caller's own context ending) stops the
// cascade: every item that was not yet resolved fails with it and nothing more is
// sent. So does a batched request answered by a route that delivered less than the
// caller's contract floor: nothing it decoded is accepted. So do outageRequestLimit
// requests in a row that fail in transport, which fail what is left with the last
// of those failures. One request's own timeout does not stop a run on its own: it
// says nothing about the chunk, and the cascade goes on.
//
// A request the provider's own content filter refused is about some item and not
// about the run: the chunk is split in halves, and so on down to the item that is
// refused alone, which fails with the kind policy violation, marked as a refusal,
// where the items around it are answered. The same is true of a request the
// client's policy refuses to send, which is about the client and repeats for every
// item, and stops the run as above.
//
// A request is never sent again as it was. An isolate pass asks only about the
// items that were not resolved and is sent only when that is fewer than the
// request before it; a request that resolved nothing is followed by two requests
// of half its items. A pass that resolved nothing after a split is not followed by
// another. The most requests the library sends for n items is recoverRequestBound
// (n, isolate passes): 2n + 2k*floor(n/2) for k isolate passes, 3n at the default of
// one. Each request may be repeated by the client (Client.Retries) and each atomic
// request repaired (RepairAttempts), so the provider calls are at most
// (Retries+1) x (batched + (RepairAttempts+1) x atomic).
//
// The error of an item is classified, and its text is the library's own (the
// operation's name, the kind, and "item 3 of 8"): what a vendor said, or the text of
// the caller's own Decode or invariant error, is the error's cause and is not in
// the text. Every ledger entry and summary field is counts and names.
//
// The whole run is one call of the operation to an installed observer, the
// client's deadline binds each batched request as it does a single call, and
// BatchResult.Total is what every request of the run cost, requests whose answer
// the caller's own function then panicked on included.
//
// The caller's functions run on a goroutine of the library's, one call each, so
// that a panic or a runtime.Goexit in Encode, DecodeItem, an invariant or
// Normalize fails that item or that chunk and not the caller's goroutine. A client's
// budget is checked as each request starts, and the atomic rung's requests overlap,
// so a budget that runs out can be overshot by up to the concurrency less one
// requests that had already started. A function of the caller's that blocks and does
// not look at the context it cannot see holds the call: nothing in Go can end it.
func RunOpManyRecover[In, Out any](ctx context.Context, op Op[In, Out], items []In, codec BatchItemCodec[In, Out], opt types.OpOptions, cfg RecoverConfig) (_ types.BatchResult[Out], runErr error) {
	name := operationLabel(op.ID)
	if ctx == nil {
		return types.BatchResult[Out]{}, types.Newf(types.KindInvalidRequest, name, "a nil context was given")
	}
	started := time.Now()

	// The client every call of the cascade runs on is fixed once, so that a
	// default replaced while the passes run does not give one pass the provider
	// of one client and the next the budget of another.
	ctx, _ = PinExec(ctx)
	// The run is one call to an observer, and every request of every pass is its
	// child.
	ctx, observed := observeOperation(ctx, op.ID.Name)
	defer observed.end(&runErr)

	// A context that is already over runs nothing: not a request, and not a
	// function of the caller's.
	if over := contextOver(ctx); over != nil {
		return types.BatchResult[Out]{}, failureOf(name, batchFailureKind(over), over, "the context had already ended, so nothing was run")
	}

	switch {
	case op.Batch.Encode == nil:
		return types.BatchResult[Out]{}, types.Newf(types.KindConfiguration, name, "there is no batch Encode to run the batched passes with")
	case !op.Batch.checksEachItem || op.Batch.Class != types.BatchItemwise:
		return types.BatchResult[Out]{}, types.Newf(types.KindConfiguration, name, "the batch is not an itemwise batch built by NewIDBatchAlgebra, and a recovering run can only read the answers of that protocol one item at a time")
	case op.BuildPrompt == nil:
		return types.BatchResult[Out]{}, types.Newf(types.KindConfiguration, name, "there is no BuildPrompt to run the requests of a single item with")
	case op.Contract.Decode == nil:
		return types.BatchResult[Out]{}, types.Newf(types.KindConfiguration, name, "there is no Decode to turn the answer to a single item into a value")
	case codec.DecodeItem == nil:
		return types.BatchResult[Out]{}, types.Newf(types.KindConfiguration, name, "the codec has no DecodeItem to read one item's answer with")
	case batchCannotCarryEvidence(op):
		return types.BatchResult[Out]{}, types.Newf(types.KindConfiguration, name, "the operation requires evidence, which the answer to a batch does not carry: it runs one request per item")
	}

	// The floor of the caller's policy is asked before anything is sent, of the
	// level a batched answer establishes on the route it is about to take. The
	// level each answer was actually delivered at is asked again after every
	// request, since the route that answers may not be the one that was planned.
	planned := plannedContractLevel(ctx, batchContractLevel(op), opt)
	if floorErr := checkContractFloor(ctx, planned); floorErr != nil {
		return types.BatchResult[Out]{Total: types.Meta{Operation: name, RequestedContract: planned}}, floorErr
	}

	var result types.BatchResult[Out]
	result.Summary.Total = len(items)
	result.Summary.Policy = types.PolicyCollectFailures
	result.Summary.PolicyReason = "every item runs through batched requests, isolation, and a request of its own before being reported; read BatchResult.Complete() and Failures(), not an error return"
	if len(items) == 0 {
		return result, nil
	}

	maxIsolate := cfg.MaxIsolatePasses
	switch {
	case maxIsolate <= 0:
		maxIsolate = DefaultMaxIsolatePasses
	case maxIsolate > MaxIsolatePassesLimit:
		maxIsolate = MaxIsolatePassesLimit
	}
	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = manyConcurrency(ctx)
	}

	// The planner says how large a batch can be for this model, and which items
	// cannot share one. A plan that cannot be made leaves the bounds of the
	// configuration and no oversized item: the passes meet whatever made it
	// unplannable themselves, and fall back to a request of their own.
	maxItems := cfg.MaxItemsPerCall
	if maxItems <= 0 {
		maxItems = defaultMaxItemsPerCall
	}
	oversized := make(map[int]bool)
	var eligible []int
	var plannedChunks []types.ChunkPlan
	var plan types.Plan
	if planFailure := onWorker(ctx, "batch", func() error {
		plan = planOperation(ctx, op, items, types.PlanRequest{
			Options: opt, Force: types.ShapeMDSP, ForceReason: "the recovering run asks in batched requests", MaxItemsPerCall: cfg.MaxItemsPerCall,
		}, false)
		return nil
	}); planFailure != nil {
		// A function of the caller's ended its goroutine while the plan was made.
		plan = types.Plan{}
	}
	if plan.Eligible {
		for _, o := range plan.OversizedItems {
			oversized[o.Index] = true
		}
		plannedChunks = plan.Chunks
		maxItems = 1
		for _, chunk := range plannedChunks {
			if chunk.ItemCount > maxItems {
				maxItems = chunk.ItemCount
			}
		}
	}
	for i := range items {
		if !oversized[i] {
			eligible = append(eligible, i)
		}
	}
	// limitAt[p] is where the planned chunk that holds the p-th eligible item ends,
	// so that no chunk the cascade sends is larger than the one the planner cut.
	limitAt := make([]int, len(eligible))
	for p := range limitAt {
		limitAt[p] = len(eligible)
	}
	if covered := 0; len(plannedChunks) > 0 {
		for _, chunk := range plannedChunks {
			for p := covered; p < covered+chunk.ItemCount && p < len(eligible); p++ {
				limitAt[p] = covered + chunk.ItemCount
			}
			covered += chunk.ItemCount
		}
		if covered != len(eligible) {
			for p := range limitAt {
				limitAt[p] = len(eligible)
			}
		}
	}
	start := cfg.StartItemsPerCall
	if start <= 0 {
		start = maxItems
	}

	run := &cascade[In, Out]{
		op:         op,
		items:      items,
		codec:      codec,
		opt:        opt,
		maxIsolate: maxIsolate,
		records:    make([]recoverRecord[Out], len(items)),
		ledger:     &result.Ledger,
	}
	for i := range run.records {
		run.records[i].mode = types.ShapeAtomic.String()
	}
	state := NewAdaptiveChunkState(start, maxItems)
	result.Ledger.Record("plan", fmt.Sprintf("%d item(s) in batches of at most %d, %d sent alone", len(eligible), state.Size(), len(oversized)),
		"the ceiling and the first size are the largest batch the planner cuts for this model; an item too large to share a request is sent alone")

	// The decision ledger is written after the outcome is known. sizeBefore is
	// read before state.Record so a "held at N" entry (no change) is
	// distinguishable from a "grew/halved" entry by comparing sizeBefore to
	// state.Size() after, rather than guessing from the outcome alone -- the
	// outcome classification and the size-change decision are two different
	// pieces of AdaptiveChunkState's own logic, and the entry is only honest if
	// it reports what that logic actually did.
	chunkNumber := 0
	for position := 0; position < len(eligible); {
		size := state.Size()
		if limit := limitAt[position] - position; size > limit {
			size = limit
		}
		if size < 1 {
			size = 1
		}
		chunkNumber++
		node := fmt.Sprintf("chunk %d (%d item(s))", chunkNumber, size)
		chunkIdx := append([]int(nil), eligible[position:position+size]...)
		position += size

		if size == 1 {
			// A batch of one item is that item's request with the protocol's
			// overhead: it is sent alone, and says nothing about the size.
			result.Ledger.Record(node, "sent alone", "a batch of one item gains nothing over a request of its own")
			continue
		}

		outcome, stop := run.chunk(ctx, chunkIdx, node)
		if stop != nil {
			// The failure will repeat: every item not yet resolved fails with
			// it, the ones in chunks that were never sent included, and
			// nothing more is sent.
			failure := classifiedFailure(stop)
			for i := range run.records {
				if !run.records[i].resolved {
					run.records[i].stopped = true
					run.records[i].err = failure
				}
			}
			result.Ledger.Record(node, "stopped the cascade", "a request failed in a way that sending again would repeat")
			break
		}

		if outcome == outcomeNoSignal {
			// The call failed in transport, or was refused for one item of it, or
			// nothing could be sent. That says nothing about whether the chunk
			// was too large, and the size is left as it is.
			result.Ledger.Record(node, fmt.Sprintf("chunk size held at %d", state.Size()),
				"the first request failed in transport or was refused, which says nothing about the chunk size")
			continue
		}

		sizeBefore := state.Size()
		state.Record(outcome)
		sizeAfter := state.Size()

		decision := fmt.Sprintf("chunk size held at %d", sizeBefore)
		if sizeAfter != sizeBefore {
			decision = fmt.Sprintf("chunk size changed from %d to %d", sizeBefore, sizeAfter)
		}
		result.Ledger.Record(node, decision, state.Reason())
	}

	var pendingAtomic []int
	for i, r := range run.records {
		if !r.resolved && !r.stopped {
			pendingAtomic = append(pendingAtomic, i)
		}
	}
	if len(pendingAtomic) > 0 {
		result.Ledger.Record(
			fmt.Sprintf("atomic rung (%d item(s))", len(pendingAtomic)),
			"one request per item",
			"sent alone, or unresolved after every batched pass and isolate retry this cascade allows",
		)
		run.atomic(ctx, concurrency, pendingAtomic)
	}

	result.Items = make([]types.ItemResult[Out], len(items))
	delivered, anyDelivered := types.ContractLevel(0), false
	for i, r := range run.records {
		status := types.ItemFailed
		var itemErr error
		switch {
		case r.resolved:
			status = types.ItemSucceeded
			if !anyDelivered || r.delivered < delivered {
				delivered, anyDelivered = r.delivered, true
			}
		case endedBy(ctx, r.err) || (ctx.Err() != nil && types.KindOf(r.err) == types.KindCanceled):
			// The run's context ending is not this item's failure.
			status = types.ItemCancelled
			itemErr = itemStopped(name, i, len(items), r.err)
		default:
			itemErr = itemFailure(name, i, len(items), r.err)
		}
		result.Items[i] = types.ItemResult[Out]{
			Index:    i,
			Status:   status,
			Value:    r.value,
			Attempts: r.attempts,
			Repairs:  r.repairs,
			Cost:     r.cost,
			Mode:     r.mode,
			Err:      itemErr,
		}
		switch status {
		case types.ItemSucceeded:
			result.Summary.Succeeded++
		case types.ItemFailed:
			result.Summary.Failed++
		case types.ItemCancelled:
			result.Summary.Cancelled++
		}
	}

	// What became of the items is what an observer is told of them, as in
	// RunOpManyPartial.
	observed.recordItems(result.Summary.Total, result.Summary.Succeeded, result.Summary.Failed, result.Summary.Cancelled, firstItemFailure(result.Items))

	result.Total = run.spent.total(op.ID.String())
	result.Total.Elapsed = time.Since(started)
	result.Total.Strategy = types.ShapeMDSP.String()
	result.Total.RequestedContract = planned
	result.Total.DeliveredContract = delivered

	emitBatchMetrics(op.ID.String(), "mdsp_recover", result)

	return result, nil
}

// cascade is what the passes of one run share: the operation, the items, what
// each item's state is so far, and what every request has cost.
type cascade[In, Out any] struct {
	op         Op[In, Out]
	items      []In
	codec      BatchItemCodec[In, Out]
	opt        types.OpOptions
	maxIsolate int
	records    []recoverRecord[Out]
	spent      spend

	// ledger is the run's decision ledger. Only the caller's goroutine writes it:
	// the batched rung is serial.
	ledger *types.DecisionLedger

	// failures counts the requests in a row that failed in transport, and halted
	// is the failure that ends the run, once there is one. The atomic rung's
	// requests overlap, so both are read and written under mu.
	mu       sync.Mutex
	failures int
	halted   error
}

// answered notes that a request got an answer, whatever the answer was: the
// provider is there.
func (c *cascade[In, Out]) answered() {
	c.mu.Lock()
	c.failures = 0
	c.mu.Unlock()
}

// failedInTransport notes a request that failed in transport and returns the
// failure that ends the run when outageRequestLimit requests in a row have, or
// nil while the run goes on.
func (c *cascade[In, Out]) failedInTransport(err error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failures++
	if c.failures >= outageRequestLimit && c.halted == nil {
		c.halted = err
	}
	return c.halted
}

// halt ends the run with err, unless it was already ended by something else.
func (c *cascade[In, Out]) halt(err error) {
	c.mu.Lock()
	if c.halted == nil {
		c.halted = err
	}
	c.mu.Unlock()
}

// haltedBy is the failure that ended the run, or nil while it goes on.
func (c *cascade[In, Out]) haltedBy() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.halted
}

// job is a set of items to ask about in one batched request.
type job struct {
	set []int

	// isolateLeft is how many isolate passes may still follow this request.
	isolateLeft int

	// afterNothing is true when the request this one was cut from resolved nothing,
	// so that this one resolving nothing too ends the isolating.
	afterNothing bool

	// mayBeOne is true for an isolate pass over what a request left unresolved when
	// it resolved something: a pass over one item is still a request that is smaller
	// than the one before it. A half of a request that resolved nothing is not one
	// when it is a single item, which is sent alone.
	mayBeOne bool

	// label names the request in the ledger.
	label string
}

// chunk runs one top-level chunk's own recovery ladder: a first batched request over
// chunkIdx, then, over whatever remained unresolved and isolated from the items that
// already resolved, up to maxIsolate further requests along each path, each over a
// strictly smaller set than the one it follows: the unresolved items when something
// resolved, and two halves of them when nothing did. It writes every resolved item
// into the records as it goes and returns the FIRST request's outcome -- the signal
// AdaptiveChunkState.Record reacts to, because that is the one that says whether this
// top-level chunk SIZE was too large; an isolate pass's own outcome is about an
// already-known-bad subset, not new information about sizing.
//
// A request the provider's filter refused is split in halves without using up an
// isolate pass, down to the item refused alone, which is left for the atomic rung.
// What is left unresolved is not sent again here: the atomic rung sends it, one
// request each. A chunk makes at most 2*maxIsolate + len(chunkIdx) batched requests.
//
// stop is the error of a call that failed in a way sending again would repeat
// (cascadeStops), or of the outageRequestLimit-th request in a row to fail in
// transport, and nil otherwise.
func (c *cascade[In, Out]) chunk(ctx context.Context, chunkIdx []int, node string) (AdaptiveOutcome, error) {
	firstOutcome := OutcomeCompliant
	haveFirst := false
	budget := 2*c.maxIsolate + len(chunkIdx)

	stack := []job{{set: chunkIdx, isolateLeft: c.maxIsolate, label: "first request"}}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if len(current.set) == 0 || (len(current.set) == 1 && !current.mayBeOne) || budget <= 0 {
			// One item that no request is left to ask about is sent alone, by the atomic
			// rung.
			continue
		}
		budget--

		res := c.pass(ctx, current.set)
		c.ledger.Record(node, fmt.Sprintf("%s: %d item(s) asked, %d resolved, %d left", current.label, len(current.set), res.resolved, len(res.unresolved)), res.outcome.String())
		if !haveFirst {
			firstOutcome = res.outcome
			haveFirst = true
		}
		if res.stop != nil {
			return firstOutcome, res.stop
		}
		if len(res.unresolved) == 0 || res.giveUp {
			continue
		}

		switch {
		case res.refused:
			// The provider's filter refused the request, which is about some item in
			// it and not about the run: ask about each half.
			stack = append(stack, halves(res.unresolved, current.isolateLeft, current.afterNothing, "split after a refusal")...)
		case current.isolateLeft == 0:
			continue
		case res.resolved > 0:
			stack = append(stack, job{set: res.unresolved, isolateLeft: current.isolateLeft - 1, label: "isolate pass", mayBeOne: true})
		case current.afterNothing:
			// Nothing resolved twice running: a smaller request does not cure it.
			continue
		default:
			stack = append(stack, halves(res.unresolved, current.isolateLeft-1, true, "isolate pass over half")...)
		}
	}

	return firstOutcome, nil
}

// halves cuts set in two, the first half larger when it is odd, as jobs in the order
// a stack pops them: the first half first.
func halves(set []int, isolateLeft int, afterNothing bool, label string) []job {
	middle := (len(set) + 1) / 2
	return []job{
		{set: set[middle:], isolateLeft: isolateLeft, afterNothing: afterNothing, label: label},
		{set: set[:middle], isolateLeft: isolateLeft, afterNothing: afterNothing, label: label},
	}
}

// cascadeStops reports whether a failed call would fail the same way if it
// were made again, so that the cascade should stop and not work down its
// rungs: a refusal by the client's policy, a spent budget, a key the provider
// does not accept, a configuration that cannot run, a route that cannot take the
// request, a scheduler that is closed, and the caller's own context ending.
//
// ctx is the caller's. One request's own deadline, the client's Timeout, is not
// the caller's context ending: it is no signal about the chunk (outcomeForCallErr
// reads it so), the next request has a deadline of its own, and the cascade goes
// on. A refusal by the provider's own filter is not a refusal by policy: it is
// about the request's items and the same request over fewer of them can pass, so it
// does not stop the cascade (a refusal carries the mark that tells them apart).
func cascadeStops(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx.Err() != nil {
		return true
	}
	if isRefusal(err) {
		return false
	}
	switch types.KindOf(err) {
	case types.KindPolicyViolation, types.KindBudgetExceeded, types.KindAuthentication,
		types.KindConfiguration, types.KindCanceled, types.KindShutdown, types.KindUnsupportedCapability:
		return true
	}
	return false
}

// isRefusal reports whether err is the provider's refusal to answer, or its filter
// withholding the answer, as the call path marks it, and not the client's own policy.
func isRefusal(err error) bool {
	return types.KindOf(err) == types.KindPolicyViolation && types.NarrowingOf(err) == types.NarrowRefusal
}

// failedInTransportKind reports whether err is a failure of the way to the provider
// and not of what was asked: a rate limit, an outage, a request's own timeout, an
// open circuit and a scheduler that refuses to queue the request.
func failedInTransportKind(err error) bool {
	switch types.KindOf(err) {
	case types.KindRateLimited, types.KindProviderUnavailable, types.KindTimeout, types.KindCircuitOpen, types.KindAdmissionRejected:
		return true
	}
	return false
}

// passResult is what one batched request did.
type passResult struct {
	// outcome is what AdaptiveChunkState reacts to.
	outcome AdaptiveOutcome

	// unresolved is the global index of every item of the request still without an
	// answer, in ascending order.
	unresolved []int

	// resolved is how many items the request resolved.
	resolved int

	// refused is true when the provider's filter refused the request as a whole.
	refused bool

	// giveUp is true when asking about these items in a batch again would repeat what
	// just happened (the request could not be encoded, or failed in transport): the
	// items go to the atomic rung.
	giveUp bool

	// stop is the error of a request that fails the same way whenever it is sent.
	stop error
}

// pass runs exactly one batched request over the items named by pending (global
// indices into the items), decodes as much of the response as
// mergeIDBatchPartial's partial coverage allows, writes every resolved item into
// the records with ModeMDSP provenance, and says what happened.
//
// The caller's own code runs here: the batch Encode, the codec's DecodeItem, and
// the contract's invariants and Normalize. Each runs on a goroutine of the
// library's, so that a panic or a runtime.Goexit in any of them is the failure of
// the chunk (Encode) or of the item it ran for, as a panic in the atomic rung is,
// and not the end of the process or of the caller's goroutine.
func (c *cascade[In, Out]) pass(ctx context.Context, pending []int) passResult {
	subset := make([]In, len(pending))
	for i, gi := range pending {
		subset[i] = c.items[gi]
		c.records[gi].tried = true
	}

	recordCtx, recs := withCallRecording(WithOperationName(ctx, c.op.ID.Name))

	var system, user string
	encErr := onWorker(ctx, "batch", func() error {
		var err error
		system, user, err = c.op.Batch.Encode(subset, c.opt)
		return err
	})
	var offered []string
	if encErr == nil {
		offered, encErr = offeredBatchIDs(user, len(subset))
	}
	if encErr != nil {
		// Nothing was sent -- no call to record cost or attempts for, and no
		// point retrying an encode that takes the same input every time: what is
		// unresolved goes to the atomic fallback, which does not use the batch
		// encoding.
		failure := withKindIfNone(types.KindConfiguration, encErr)
		for _, gi := range pending {
			c.records[gi].err = classifiedFailure(failure)
		}
		return passResult{outcome: outcomeNoSignal, unresolved: pending, giveUp: true}
	}

	// The client's deadline binds each request, as it binds RunOpBatch's, and
	// each request has a deadline of its own: the wait of one pass does not use
	// up the next one's.
	requestCtx, cancelDeadline := clientDeadline(recordCtx)
	body, callErr := callLLM(requestCtx, system, user, c.opt)
	cancelDeadline()
	meta := recs.meta(c.op.ID.String())
	c.spent.add(meta)

	if callErr != nil {
		applyPassOutcome(c.records, pending, nil, nil, meta, callErr)
		switch {
		case cascadeStops(ctx, callErr):
			c.halt(callErr)
			return passResult{outcome: outcomeForCallErr(callErr), unresolved: pending, stop: callErr}
		case isRefusal(callErr):
			c.answered()
			return passResult{outcome: outcomeNoSignal, unresolved: pending, refused: true}
		case failedInTransportKind(callErr):
			if halted := c.failedInTransport(callErr); halted != nil {
				return passResult{outcome: outcomeNoSignal, unresolved: pending, stop: halted}
			}
			return passResult{outcome: outcomeNoSignal, unresolved: pending, giveUp: true}
		}
		c.answered()
		return passResult{outcome: outcomeForCallErr(callErr), unresolved: pending}
	}
	c.answered()

	// The level the route that answered delivered, held to the caller's floor as
	// RunOpBatchResult holds a chunk to it. Nothing the request decoded is
	// accepted when it falls short, and the cascade stops: the next request would
	// be answered by the same route.
	delivered := negotiatedContractLevel(batchContractLevel(c.op), c.opt, meta.Provider, meta.Model)
	if policyErr := enforceContractPolicy(ctx, delivered); policyErr != nil {
		applyPassOutcome(c.records, pending, nil, nil, meta, policyErr)
		c.halt(policyErr)
		return passResult{outcome: outcomeNoSignal, unresolved: pending, stop: policyErr}
	}

	merged := mergeIDBatchPartial(body, offered, subset, c.guardedCodec(ctx))

	// An item resolved from the batch is held to the operation's invariants,
	// as one resolved by a single call is. One that breaks them is not
	// resolved: it goes on to the next rung with the invariant as its error.
	broken := map[int]error{}
	for li, value := range merged.resolved {
		checked, checkErr := c.check(ctx, value)
		if checkErr != nil {
			broken[li] = checkErr
			continue
		}
		merged.resolved[li] = checked
	}
	unresolvedLocal := merged.unresolved
	for li := range broken {
		delete(merged.resolved, li)
		unresolvedLocal = append(unresolvedLocal, li)
	}
	sort.Ints(unresolvedLocal)

	applyPassOutcome(c.records, pending, merged.resolved, merged.causes, meta, merged.err)
	for li, checkErr := range broken {
		c.records[pending[li]].err = checkErr
	}
	for li := range merged.resolved {
		c.records[pending[li]].delivered = delivered
	}

	unresolvedGlobal := make([]int, len(unresolvedLocal))
	for i, li := range unresolvedLocal {
		unresolvedGlobal[i] = pending[li]
	}

	result := passResult{unresolved: unresolvedGlobal, resolved: len(merged.resolved)}
	switch {
	case merged.err != nil:
		result.outcome = OutcomeMalformedProtocol
	case merged.cutOff:
		result.outcome = OutcomeTruncated
	default:
		result.outcome = classifyCoverageOutcome(merged.coverage)
	}
	return result
}

// onWorker runs a function of the caller's on a goroutine of the library's and
// returns what it failed with: a panic in it, or a runtime.Goexit that would end
// the goroutine it runs on, is the failure of that call, and the caller's goroutine
// goes on. The failure is classified as a panic and carries no value.
func onWorker(ctx context.Context, op string, run func() error) error {
	var failure error
	ran := runBounded(ctx, op, 1, boundedOptions{workers: 1, runAfterCancel: true}, func(context.Context, int) error {
		failure = run()
		return nil
	})
	if ran.errs[0] != nil {
		return ran.errs[0]
	}
	return failure
}

// guardedCodec is the codec with its DecodeItem run where a panic or a
// runtime.Goexit in it is the item's error. The codec is the caller's.
func (c *cascade[In, Out]) guardedCodec(ctx context.Context) BatchItemCodec[In, Out] {
	return BatchItemCodec[In, Out]{DecodeItem: func(raw json.RawMessage) (Out, error) {
		var value Out
		err := onWorker(ctx, "batch", func() error {
			var decodeErr error
			value, decodeErr = c.codec.DecodeItem(raw)
			return decodeErr
		})
		if err != nil {
			var none Out
			return none, err
		}
		return value, nil
	}}
}

// check holds one item decoded from a batched response to the operation's
// invariants and then normalizes it, under the same guard. The error it returns is
// classified, and says only that the item broke the operation's own check: the
// check's text is the caller's and stays in the cause.
func (c *cascade[In, Out]) check(ctx context.Context, value Out) (Out, error) {
	var checked Out
	err := onWorker(ctx, "batch", func() error {
		for _, invariant := range c.op.Contract.Invariants {
			if invErr := invariant(value); invErr != nil {
				return invErr
			}
		}
		checked = value
		if c.op.Contract.Normalize != nil {
			checked = c.op.Contract.Normalize(value)
		}
		return nil
	})
	if err == nil {
		return checked, nil
	}
	var none Out
	if types.NarrowingOf(err) == types.NarrowPanic {
		return none, err
	}
	return none, &types.OperationError{
		Kind:    kindOrDefault(err, types.KindInvariantViolation),
		Op:      "batch",
		Message: "an item of the batch broke a check of the operation",
		Cause:   err,
	}
}

// kindOrDefault is the kind err carries, or def when nothing in its chain says.
func kindOrDefault(err error, def types.ErrorKind) types.ErrorKind {
	if kind := types.KindOf(err); kind != types.KindUnknown {
		return kind
	}
	return def
}

// classifiedFailure is err with a kind: its own, or what the call path calls a
// failure it does not know. The text and the chain are err's own.
func classifiedFailure(err error) error {
	return withKindIfNone(batchFailureKind(err), err)
}

// applyPassOutcome folds one batched pass's result into the records: every item
// in pending gets its attempt count incremented (one batched request touched it
// this pass, regardless of any retry callLLM itself performed inside that one
// call -- see the field-level note below) and an even share of the pass's
// measured cost, and a resolved item additionally gets its value, ModeMDSP
// provenance, and its error cleared.
//
// Cost attribution convention: one batched request's price covers every item it
// was asked about, resolved or not, and there is no way to know from the
// provider's usage figures which item "cost more" -- so the call's measured
// total is split evenly across pending. This is an attribution convention
// over a real, measured total, not an invented price (AGENTS.md's "no cost
// from a substituted price" is about a *different* model's rate being
// applied; this is the same rate applied to a fair share of the same bill).
// Summing every item's Cost.TotalCost for one pass reproduces that pass's
// true total exactly. The measured total of the whole run is BatchResult.Total.
//
// Attempts convention: an item's Attempts field counts the requests made for it
// (types.ItemResult.Attempts): here one for each batched request that asked about
// it, which is the number that actually varies per item in a cascade (some items
// resolve on pass 1, others need an isolate pass). A batched request carries
// several items, so the items' attempts do not add up to the requests the run
// made; BatchResult.Total.Attempts does.
//
// causes is each unresolved item's own reason, by local index, from
// mergeIDBatchPartial; it may be nil.
//
// An item that goes on to the atomic fallback has that call's own attempt
// count added (cascade.atomic), so the field is the batched requests plus the
// provider calls of its own for such an item; the Mode says which rungs the item
// went through.
func applyPassOutcome[Out any](records []recoverRecord[Out], pending []int, resolvedLocal map[int]Out, causes map[int]error, meta types.Meta, passErr error) {
	n := len(pending)
	if n == 0 {
		return
	}
	share := splitCost(meta.Cost, n)

	for li, gi := range pending {
		r := &records[gi]
		r.attempts++
		r.cost = addCost(r.cost, share)

		if resolvedLocal != nil {
			if val, ok := resolvedLocal[li]; ok {
				r.resolved = true
				r.value = val
				r.mode = ModeMDSP
				r.err = nil
				continue
			}
		}

		// The item's own cause comes first: the pass can have parsed and still
		// have left this item unresolved for a reason of its own.
		if cause := causes[li]; cause != nil {
			r.err = classifiedFailure(cause)
		} else if passErr != nil {
			r.err = classifiedFailure(passErr)
		} else {
			r.err = types.Newf(types.KindBatchProtocolViolation, "batch", "the batch response did not resolve this item")
		}
	}
}

// splitCost divides c's spend evenly across n items. Returns the zero
// CostInfo (unpriced) when c itself is unpriced -- the rule that an unknown cost
// stays unknown rather than becoming a confident zero survives division: 0/n is
// still "unknown", not "free".
func splitCost(c types.CostInfo, n int) types.CostInfo {
	if n <= 0 || !c.Priced {
		return types.CostInfo{}
	}
	f := 1.0 / float64(n)
	return types.CostInfo{
		Priced:         true,
		TotalCost:      c.TotalCost * f,
		PromptCost:     c.PromptCost * f,
		CompletionCost: c.CompletionCost * f,
		CachedCost:     c.CachedCost * f,
		ReasoningCost:  c.ReasoningCost * f,
		Currency:       c.Currency,
		PricingSource:  c.PricingSource,
		Quality:        c.Quality,
	}
}

// outcomeForCallErr classifies a callLLM failure into the AdaptiveOutcome
// closest to what actually happened, reading the same taxonomy classify.go
// already produces rather than a second opinion about what the error means
// (AGENTS.md's rule against writing a second one).
//
// A rate limit, an outage, a timeout and a scheduler that refuses to queue the
// request are not the model breaking the protocol, and a smaller chunk does not
// cure them. They give outcomeNoSignal, which leaves the size alone. So does a
// refusal by the provider's filter, which is about one item in the chunk and not
// about how many there are.
func outcomeForCallErr(err error) AdaptiveOutcome {
	if isRefusal(err) {
		return outcomeNoSignal
	}
	switch types.KindOf(err) {
	case types.KindOutputTruncated, types.KindContextTooLarge:
		return OutcomeTruncated
	case types.KindRateLimited, types.KindProviderUnavailable, types.KindTimeout, types.KindCircuitOpen, types.KindAdmissionRejected:
		return outcomeNoSignal
	}
	return OutcomeMalformedProtocol
}

// outcomeNoSignal is a pass outcome that says nothing about the chunk size, so
// AdaptiveChunkState is not told it. It is outside the values adaptive.go
// defines.
const outcomeNoSignal AdaptiveOutcome = 255

// classifyCoverageOutcome turns a BatchCoverage into the AdaptiveOutcome
// the adaptive state reacts to. Checked in a fixed order -- duplicate before
// missing -- so a response with both always reports the same outcome, the same
// determinism boundViolation (plan.go) keeps for its own bound checks.
//
// A response that answered every offered id exactly once is compliant whatever
// else it held: an element with an id nobody offered fills no item and leaves none
// unanswered, so it is no reason to ask for less.
func classifyCoverageOutcome(coverage BatchCoverage) AdaptiveOutcome {
	switch {
	case coverage.Complete():
		return OutcomeCompliant
	case len(coverage.Duplicate) > 0:
		return OutcomeDuplicateID
	default:
		return OutcomeOmission
	}
}

// atomic runs one request per item named by pendingIdx -- the last rung --
// bounded to concurrency in flight, writing each result into the records. An item
// that fails its atomic call is left unresolved with that call's own error, which is
// the terminal failure ItemFailed already expresses. An item that no batched request
// asked about (an oversized item, a chunk of one) has the mode of one sent alone, and
// one a batched request did not resolve has ModeAtomicFallback.
//
// The calls run on the shared bounded pool, under the guard it puts around
// every step: a step that panics, or ends its goroutine, is that item's
// failure and not the program's, and the items after it still run. Every item
// is offered its call whatever the others do, as before, so a context that is
// over is reported by each call itself, except that once the run is halted (a
// failure that repeats, or outageRequestLimit in a row) the items that have not
// started fail with it and nothing more is sent.
func (c *cascade[In, Out]) atomic(ctx context.Context, concurrency int, pendingIdx []int) {
	ran := runBounded(ctx, "recover", len(pendingIdx), boundedOptions{workers: concurrency, runAfterCancel: true}, func(ctx context.Context, k int) error {
		gi := pendingIdx[k]
		r := &c.records[gi]
		if halted := c.haltedBy(); halted != nil {
			r.stopped = true
			r.err = classifiedFailure(halted)
			return nil
		}
		res, err := runOpItem(ctx, c.op, c.items[gi], c.opt)
		c.spent.add(res.Meta)
		if r.tried {
			r.mode = types.ModeAtomicFallback
		} else {
			r.mode = types.ShapeAtomic.String()
		}
		r.attempts += res.Meta.Attempts
		r.repairs += res.Meta.Repairs
		r.cost = addCost(r.cost, res.Meta.Cost)
		if err != nil {
			r.resolved = false
			r.err = err
			switch {
			case cascadeStops(ctx, err):
				c.halt(err)
			case failedInTransportKind(err):
				c.failedInTransport(err)
			default:
				c.answered()
			}
			return nil
		}
		c.answered()
		r.resolved = true
		r.value = res.Value
		r.delivered = res.Meta.DeliveredContract
		r.err = nil
		return nil
	})

	// The step reports no error of its own (a failed call is recorded above),
	// so an error here is a step that did not finish.
	for k, err := range ran.errs {
		if err == nil {
			continue
		}
		var none Out
		r := &c.records[pendingIdx[k]]
		if r.tried {
			r.mode = types.ModeAtomicFallback
		}
		r.resolved = false
		r.value = none
		r.err = err
	}
}
