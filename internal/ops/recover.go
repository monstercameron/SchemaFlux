package ops

import (
	"context"
	"fmt"
	"sync"

	"github.com/monstercameron/schemaflux/internal/types"
)

// PL-009: the progressive recovery cascade.
//
// RunOpBatch (batchprotocol.go) runs one MDSP call and requires exact id
// coverage before decoding anything: a chunk that omits, duplicates, or
// invents even one id fails the WHOLE chunk, and the eighteen items that came
// back correctly are thrown away along with the two that did not. That is
// the gap this file closes -- EXE-18's "recovery branches on failure class"
// and PL-009's own text: preferred MDSP, keep the valid items, isolate only
// the unresolved ones, retry a smaller MDSP, fall back to atomic.
//
// The honest part of this contract, and the reason it is not just "retry
// until it works": every item the caller offered ends up in the returned
// BatchResult exactly once, and ItemResult.Mode says whether that item's
// value came from a batched call or an atomic retry -- a batched answer and
// an atomic retry are different provenance, and a caller reconciling a bill
// or a quality regression needs to tell them apart. A valid item from an
// early pass is never replayed because a sibling in the same chunk failed;
// only the unresolved subset moves to the next pass.
//
// AdaptiveChunkState (adaptive.go, PL-005) had no caller before this: it is
// fed the first-pass outcome of every top-level chunk (the isolate passes
// and the atomic fallback do not feed it -- they are this chunk's own
// recovery, not a signal about the *next* chunk's size), and its Size()
// decides how large the next top-level chunk is. A run that keeps omitting
// items converges on a smaller top-level chunk size over its own lifetime,
// exactly PL-005's "a provider that truncates above 20 items converges to a
// stable size and stays there".
//
// What this delivers: preferred MDSP -> keep valid items -> isolate
// unresolved ids -> smaller MDSP (bounded number of passes) -> atomic.
//
// What it does not: escalating to a different model or provider before the
// atomic fallback -- PL-009's own text conditions that on "the minimum
// contract and data policy survive", which is a capability-negotiation
// question (CP-001) this delivery has no seam for, so every fallback call
// runs under the same opt the caller supplied. And a review packet or
// terminal-item-failure report at budget exhaustion: an item that fails its
// own atomic call is reported ItemFailed with the error that produced it
// (PL-008's existing terminal vocabulary), which is the material a review
// packet would be built from, but nothing here assembles that packet.
// GlobalValidate (BatchAggregate operations such as a future Sort/Cluster)
// is also out of scope -- this cascade is for BatchItemwise-shaped
// operations, where each item maps to exactly one independent output; an
// aggregate operation's whole-set validation cannot be checked against a
// partially recovered set at all, which is PL-011's problem, not this one.

// RecoverConfig configures RunOpManyRecover.
type RecoverConfig struct {
	// MaxItemsPerCall bounds every MDSP call's chunk size -- the top-level
	// chunk's ceiling and the ceiling AdaptiveChunkState may grow back up to.
	// Zero (the common case) means defaultMaxItemsPerCall (plan.go), the same
	// default a first plan already uses.
	MaxItemsPerCall int

	// StartItemsPerCall is the size of the very first top-level chunk, and
	// AdaptiveChunkState's starting point. Zero means MaxItemsPerCall -- start
	// at the ceiling, the same starting point a first-time caller with no
	// history of this provider's behaviour already gets from a plain plan.
	StartItemsPerCall int

	// MaxIsolatePasses bounds how many times an unresolved remainder is
	// retried at a smaller MDSP size, isolated from the items that already
	// resolved, before whatever is still unresolved falls back to atomic.
	// Zero means DefaultMaxIsolatePasses. There is no way to ask for zero
	// isolate passes (go straight from the first MDSP call to atomic) through
	// this field -- the same "zero means default" convention MaxRetries
	// (partial.go) already uses in this package.
	MaxIsolatePasses int

	// Concurrency bounds the atomic-fallback stage's items in flight. Zero
	// means DefaultConcurrency.
	Concurrency int
}

// DefaultMaxIsolatePasses is RecoverConfig.MaxIsolatePasses' default: one
// retry at a smaller size before whatever is left falls back to atomic. Not
// unbounded, for the same reason DefaultMaxRetries (partial.go) is not: an
// id that a provider keeps failing to answer for a reason isolation cannot
// fix (a malformed protocol on every call, not a size problem) must not
// spend the whole run's budget retrying something that will not come back.
const DefaultMaxIsolatePasses = 1

// recoverRecord is one item's state while the cascade is still running,
// before it is rendered into a types.ItemResult. Distinct from partial.go's
// itemOutcome: this tracks resolution across several passes at possibly
// different chunk memberships, accumulating attempts and cost across all of
// them, where itemOutcome is written once per RunOpResult call.
type recoverRecord[Out any] struct {
	resolved bool
	value    Out
	attempts int
	repairs  int
	cost     types.CostInfo
	mode     string
	err      error
}

// ModeMDSP is the ItemResult.Mode value for an item resolved from a batched
// MDSP response -- types.ExecutionShape's own "mdsp" string
// (types.ShapeMDSP.String()), reused rather than a second literal, since
// RunOpBatchResult (batchprotocol.go) already writes the identical value
// into Meta.Strategy for the same reason. ModeAtomicFallback lives in
// internal/types (types.ModeAtomicFallback) rather than here, because
// types.BatchMetrics.Metrics() has to compare ItemResult.Mode against it and
// internal/types cannot import internal/ops.
var ModeMDSP = types.ShapeMDSP.String()

// RunOpManyRecover runs op over items through PL-009's progressive recovery
// cascade: preferred MDSP over top-level chunks sized by an AdaptiveChunkState
// that reacts to what actually came back, isolate-and-retry for any chunk
// that does not cover its own ids exactly, and an atomic fallback call for
// whatever isolation could not resolve.
//
// codec is the same BatchItemCodec op's Batch.Encode/Merge were built from
// (NewIDBatchAlgebra, batchprotocol.go) -- it has to be supplied directly
// rather than recovered from op.Batch, because op.Batch.Merge is an opaque
// closure with no way to ask it for a partial decode; mergeIDBatchPartial
// needs codec.DecodeItem itself. A caller who built op through
// NewIDBatchAlgebra already has this value on hand from doing so.
//
// It returns a nil error whenever the cascade ran to completion, whatever the
// outcome -- the same "read BatchResult.Complete(), not the error return"
// contract types.PolicyCollectFailures documents, because a cascade IS the
// caller asking for whatever can be recovered rather than an all-or-nothing
// call. Summary.Policy is recorded as types.PolicyCollectFailures with a
// PolicyReason explaining it is this cascade's own terminal step, since
// PL-009 does not add a sixth types.BatchPolicy value for behaviour that is
// not a scheduling policy over atomic calls but a recovery ladder over
// batched ones.
func RunOpManyRecover[In, Out any](ctx context.Context, op Op[In, Out], items []In, codec BatchItemCodec[In, Out], opt types.OpOptions, cfg RecoverConfig) (types.BatchResult[Out], error) {
	var result types.BatchResult[Out]
	result.Summary.Total = len(items)
	result.Summary.Policy = types.PolicyCollectFailures
	result.Summary.PolicyReason = "PL-009 progressive recovery cascade: every item runs through MDSP, isolation, and atomic fallback before being reported; read BatchResult.Complete() and Failures(), not an error return"

	if len(items) == 0 {
		return result, nil
	}
	if op.Batch.Encode == nil {
		return result, fmt.Errorf("op %s: no batch Encode to run an MDSP cascade over", op.ID)
	}
	if op.BuildPrompt == nil {
		return result, fmt.Errorf("op %s: no BuildPrompt to run the atomic fallback stage", op.ID)
	}
	if op.Contract.Decode == nil {
		return result, fmt.Errorf("op %s: no Decode to turn an atomic fallback response into a value", op.ID)
	}

	maxItems := cfg.MaxItemsPerCall
	if maxItems <= 0 {
		maxItems = defaultMaxItemsPerCall
	}
	start := cfg.StartItemsPerCall
	if start <= 0 {
		start = maxItems
	}
	maxIsolate := cfg.MaxIsolatePasses
	if maxIsolate <= 0 {
		maxIsolate = DefaultMaxIsolatePasses
	}
	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = DefaultConcurrency
	}

	records := make([]recoverRecord[Out], len(items))
	state := NewAdaptiveChunkState(start, maxItems)

	offset := 0
	for offset < len(items) {
		size := state.Size()
		if size > len(items)-offset {
			size = len(items) - offset
		}
		if size < 1 {
			size = 1
		}

		chunkIdx := make([]int, size)
		for i := range chunkIdx {
			chunkIdx[i] = offset + i
		}

		outcome := runChunkCascade(ctx, op, items, codec, chunkIdx, opt, maxIsolate, records)
		state.Record(outcome)

		offset += size
	}

	var pendingAtomic []int
	for i, r := range records {
		if !r.resolved {
			pendingAtomic = append(pendingAtomic, i)
		}
	}
	if len(pendingAtomic) > 0 {
		runAtomicFallback(ctx, op, items, opt, concurrency, pendingAtomic, records)
	}

	result.Items = make([]types.ItemResult[Out], len(items))
	for i, r := range records {
		status := types.ItemFailed
		if r.resolved {
			status = types.ItemSucceeded
		}
		result.Items[i] = types.ItemResult[Out]{
			Index:    i,
			Status:   status,
			Value:    r.value,
			Attempts: r.attempts,
			Repairs:  r.repairs,
			Cost:     r.cost,
			Mode:     r.mode,
			Err:      r.err,
		}
		switch status {
		case types.ItemSucceeded:
			result.Summary.Succeeded++
		case types.ItemFailed:
			result.Summary.Failed++
		}
	}

	return result, nil
}

// runChunkCascade runs one top-level chunk's own recovery ladder: a first
// MDSP pass over chunkIdx, then up to maxIsolate further MDSP passes over
// whatever remained unresolved, isolated from the items that already
// resolved. It writes every resolved item into records as it goes and
// returns the FIRST pass's outcome -- the signal AdaptiveChunkState.Record
// reacts to, because that is the one that says whether this top-level chunk
// SIZE was too large; an isolate pass's own outcome is about a already-known-
// bad subset, not new information about sizing.
func runChunkCascade[In, Out any](ctx context.Context, op Op[In, Out], items []In, codec BatchItemCodec[In, Out], chunkIdx []int, opt types.OpOptions, maxIsolate int, records []recoverRecord[Out]) AdaptiveOutcome {
	pending := chunkIdx
	firstOutcome := OutcomeCompliant
	haveFirst := false

	for pass := 0; pass <= maxIsolate && len(pending) > 0; pass++ {
		outcome, unresolvedGlobal := runOneMDSPPass(ctx, op, items, codec, pending, opt, records)
		if !haveFirst {
			firstOutcome = outcome
			haveFirst = true
		}
		pending = unresolvedGlobal
	}

	return firstOutcome
}

// runOneMDSPPass runs exactly one MDSP call over the items named by pending
// (global indices into items), decodes as much of the response as
// mergeIDBatchPartial's partial coverage allows, writes every resolved item
// into records with ModeMDSP provenance, and returns the pass's outcome
// (for AdaptiveChunkState) and the global indices still unresolved.
func runOneMDSPPass[In, Out any](ctx context.Context, op Op[In, Out], items []In, codec BatchItemCodec[In, Out], pending []int, opt types.OpOptions, records []recoverRecord[Out]) (AdaptiveOutcome, []int) {
	subset := make([]In, len(pending))
	for i, gi := range pending {
		subset[i] = items[gi]
	}

	ctxRec, recs := withCallRecording(ctx)
	system, user, encErr := op.Batch.Encode(subset, opt)
	if encErr != nil {
		// Nothing was sent -- no call to record cost or attempts for, and no
		// point retrying an encode that takes the same input every time.
		applyPassOutcome(records, pending, nil, types.Meta{}, encErr)
		return OutcomeMalformedProtocol, pending
	}

	body, callErr := callLLM(ctxRec, system, user, opt)
	meta := envelopeFrom(recs.collect(), op.ID.String())

	if callErr != nil {
		applyPassOutcome(records, pending, nil, meta, callErr)
		return outcomeForCallErr(callErr), pending
	}

	resolvedLocal, unresolvedLocal, coverage, mergeErr := mergeIDBatchPartial(body, subset, codec)
	applyPassOutcome(records, pending, resolvedLocal, meta, mergeErr)

	unresolvedGlobal := make([]int, len(unresolvedLocal))
	for i, li := range unresolvedLocal {
		unresolvedGlobal[i] = pending[li]
	}

	if mergeErr != nil {
		return OutcomeMalformedProtocol, unresolvedGlobal
	}
	return classifyCoverageOutcome(coverage), unresolvedGlobal
}

// applyPassOutcome folds one MDSP pass's result into records: every item in
// pending gets its attempt count incremented (one MDSP call touched it this
// pass, regardless of any retry callLLM itself performed inside that one
// call -- see the field-level note below) and an even share of the pass's
// measured cost, and a resolved item additionally gets its value, ModeMDSP
// provenance, and its error cleared.
//
// Cost attribution convention: one MDSP call's price covers every item it
// was asked about, resolved or not, and there is no way to know from the
// provider's usage figures which item "cost more" -- so the call's measured
// total is split evenly across pending. This is an attribution convention
// over a real, measured total, not an invented price (AGENTS.md's "no cost
// from a substituted price" is about a *different* model's rate being
// applied; this is the same rate applied to a fair share of the same bill).
// Summing every item's Cost.TotalCost for one pass reproduces that pass's
// true total exactly.
//
// Attempts convention: an item's Attempts field elsewhere in this package
// counts provider retries within a single logical call (see RunOpResult).
// Here it counts passes: how many separate MDSP calls were made that asked
// about this item, which is the number that actually varies per item in a
// cascade (some items resolve on pass 1, others need an isolate pass) and is
// an integer without needing to divide callLLM's own internal retry count
// across items that did not all see the same number of those retries.
func applyPassOutcome[Out any](records []recoverRecord[Out], pending []int, resolvedLocal map[int]Out, meta types.Meta, passErr error) {
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

		if passErr != nil {
			r.err = fmt.Errorf("mdsp batch: %w", passErr)
		} else {
			r.err = fmt.Errorf("mdsp batch did not resolve this item's id in the response")
		}
	}
}

// splitCost divides c's spend evenly across n items. Returns the zero
// CostInfo (unpriced) when c itself is unpriced -- PR-002's rule, that an
// unknown cost stays unknown rather than becoming a confident zero, survives
// division: 0/n is still "unknown", not "free".
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
func outcomeForCallErr(err error) AdaptiveOutcome {
	if types.KindOf(err) == types.KindOutputTruncated {
		return OutcomeTruncated
	}
	return OutcomeMalformedProtocol
}

// classifyCoverageOutcome turns a BatchCoverage into the AdaptiveOutcome
// PL-005 reacts to. Checked in a fixed order -- duplicate before missing --
// so a response with both always reports the same outcome, the same
// determinism boundViolation (plan.go) keeps for its own bound checks.
func classifyCoverageOutcome(coverage BatchCoverage) AdaptiveOutcome {
	switch {
	case coverage.OK():
		return OutcomeCompliant
	case len(coverage.Duplicate) > 0:
		return OutcomeDuplicateID
	case len(coverage.Missing) > 0:
		return OutcomeOmission
	default:
		// Unknown ids only (invented, no missing or duplicate): the response
		// answered in the right shape but with an id that was never offered,
		// which is a protocol-following failure the same way a wrong-shape
		// body is, not a sizing problem an omission or duplicate is.
		return OutcomeMalformedProtocol
	}
}

// runAtomicFallback runs RunOpResult once per item named by pendingIdx --
// PL-009's last rung -- bounded to concurrency in flight, writing each
// result into records with ModeAtomicFallback provenance. An item that
// fails its atomic call is left unresolved with that call's own error,
// which is the terminal failure PL-008's ItemFailed already expresses.
func runAtomicFallback[In, Out any](ctx context.Context, op Op[In, Out], items []In, opt types.OpOptions, concurrency int, pendingIdx []int, records []recoverRecord[Out]) {
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, gi := range pendingIdx {
		wg.Add(1)
		sem <- struct{}{}
		go func(gi int) {
			defer wg.Done()
			defer func() { <-sem }()

			res, err := RunOpResult(ctx, op, items[gi], opt)
			r := &records[gi]
			r.attempts += res.Meta.Attempts
			r.repairs += res.Meta.Repairs
			r.cost = addCost(r.cost, res.Meta.Cost)
			r.mode = types.ModeAtomicFallback
			if err != nil {
				r.resolved = false
				r.err = err
				return
			}
			r.resolved = true
			r.value = res.Value
			r.err = nil
		}(gi)
	}
	wg.Wait()
}
