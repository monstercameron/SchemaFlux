package types

import "fmt"

// Plural runs that report what happened to each item.
//
// A bare `([]T, error)` cannot express "7 of 10 succeeded, here is which and
// why": a caller reading a shorter slice with no error is reading success, not a
// partial result, and nothing distinguishes "asked for 7" from "3 were silently
// dropped". BatchResult names every item's status, and its only route to a plain
// []Out (Values) hands back a bool the caller must read alongside it. A caller
// who ignores the bool still gets an honestly short slice, not a padded one, but
// the bool is what stands between this type and the failure it exists to remove.
//
// BatchMetrics is computed from a BatchResult's own items, because the numbers it
// reports (valid-item ratio, cost per accepted item) are meaningless without the
// same per-item accounting.

// ItemStatus is one item's terminal state in a plural run.
type ItemStatus uint8

const (
	// ItemUnspecified is the zero value -- a bug if it survives to a
	// returned BatchResult, since every item the engine dispatched or
	// deliberately skipped is given a real status before it is reported.
	ItemUnspecified ItemStatus = iota

	// ItemSucceeded: the item produced a usable value.
	ItemSucceeded

	// ItemFailed: the item was attempted and did not produce a usable value.
	ItemFailed

	// ItemCancelled: not finished, because the run was stopped: the policy
	// running the batch stopped dispatching new items after an earlier item's
	// terminal failure (PolicyFailFast), or the context of the call ended. It is
	// the same status under every policy and in both engines. Distinct from
	// ItemFailed on purpose: nothing about this item's own input or a
	// provider's answer to it was ever found wrong, so folding it into
	// "failed" would blame the item for a decision that had nothing to do with
	// it, and a caller's own cancellation would count as the items' failures.
	ItemCancelled
)

// String renders the status for a log line or a BatchResult.String summary.
func (s ItemStatus) String() string {
	switch s {
	case ItemSucceeded:
		return "succeeded"
	case ItemFailed:
		return "failed"
	case ItemCancelled:
		return "cancelled"
	default:
		return "unspecified"
	}
}

// ItemResult is one input's outcome from a plural run.
type ItemResult[Out any] struct {
	// Index is the item's position in the caller's input slice -- the
	// caller's order, never a response's, the same rule the id protocol
	// already enforces within a single chunk, carried up to the whole batch.
	Index int

	Status ItemStatus

	// Value is meaningful only when Status is ItemSucceeded; the zero value
	// otherwise, so a caller who skips the Status check gets a visibly
	// empty answer rather than a plausible-looking one borrowed from
	// nowhere.
	Value Out

	// Attempts is how many requests were made for this item: for an item run
	// alone, the provider requests it cost, across the first pass and any retry
	// pass. For an item that went through a batched pass it counts the batched
	// requests that carried it, one for each, and the provider requests of the
	// call of its own it was sent after them. A batched request carries several
	// items and is counted once for each of them, so the sum of every item's
	// Attempts is not the number of requests the run made: BatchResult.Total
	// .Attempts, and BatchMetrics.Requests, are.
	Attempts int

	// Repairs is the subset of Attempts that were repairs rather than fresh
	// tries, read straight from the same envelope every other operation
	// already reports through (Meta.Repairs).
	Repairs int

	// Cost is this item's own provider spend, summed across every attempt.
	// For an item of a batched pass it is an even share of what the pass cost,
	// which is an attribution and not a measurement: the measured figure is
	// BatchResult.Total. Cost.Priced follows the same discipline as
	// CostEstimate.Priced: false means unknown, not free.
	Cost CostInfo

	// Mode names how this item ran: "atomic" for an item sent alone by the
	// one-call-per-item engine, "mdsp" for one answered from a batched
	// response (ExecutionShape's own string for that shape), and
	// ModeAtomicFallback for one the batched passes did not resolve.
	Mode string

	// Err explains a non-succeeded item. Nil for ItemSucceeded. Never the
	// caller's payload -- whatever produced it already went through this
	// library's own error classification before reaching here.
	Err error
}

// ModeAtomicFallback is the ItemResult.Mode value of an item that no batched
// pass resolved and that was recovered instead by one isolated atomic call
// over just that item -- set whether or not that atomic call itself succeeded,
// since "it reached the fallback stage" and "the fallback stage worked" are
// different facts and BatchMetrics needs the first one even when the second is
// false. The paired value for an item resolved from a batched response is "mdsp"
// (ExecutionShape.String() for ShapeMDSP, which is also the Strategy a batched
// call reports in its Meta).
const ModeAtomicFallback = "atomic_fallback"

// modeMDSP mirrors ExecutionShape's own "mdsp" string without importing the
// ExecutionShape type here -- Metrics below only needs the literal to
// compare ItemResult.Mode against, not the enum.
const modeMDSP = "mdsp"

// BatchSummary is a plural run's aggregate outcome.
type BatchSummary struct {
	Total     int
	Succeeded int
	Failed    int
	Cancelled int

	// Policy is the policy that actually ran: the caller's own, or
	// PolicyCollectFailures when the caller left it PolicyUnspecified. Recorded
	// here so a stored BatchResult can be read without also keeping the call that
	// produced it.
	Policy BatchPolicy

	// PolicyReason explains Policy when it was resolved rather than
	// specified. Empty when the caller named the policy directly.
	PolicyReason string
}

// BatchResult is a plural run's honest result: what happened to every item,
// not only the ones that succeeded.
type BatchResult[Out any] struct {
	// Items is always len(Items) == Summary.Total once a run completes --
	// every offered item gets an ItemResult, including the ones a policy
	// cancelled rather than attempted.
	Items   []ItemResult[Out]
	Summary BatchSummary

	// Total is what the whole run spent, summed over every request it made: the
	// batched passes, every item's own calls, the retries and the repairs. It is
	// measured, from the same call records a single call reports through, and so
	// it is the figure to reconcile a bill against, where ItemResult.Cost is an
	// attribution. Total.Elapsed is the time the whole call took on the clock,
	// not a sum, since requests that ran together add up to more than the time
	// that passed (Total.Timing is the sum). Total.RequestedContract is the level
	// the run was planned to deliver and Total.DeliveredContract the weakest level
	// any item that succeeded was delivered at. It is the zero Meta for a run that
	// sent nothing.
	Total Meta

	// Ledger is the post-execution decision ledger: every adaptive choice this
	// run made -- a chunk size held, grown, or halved; each batched request that
	// asked about items again, with how many; an atomic fallback -- with the
	// reason each one happened, in order. It says how many requests a chunk took
	// and why, which is what a caller asking why a run took more requests than its
	// plan estimated needs. Its zero value (an empty DecisionLedger) is correct
	// and honest for an engine that makes no adaptive decisions: the engine that
	// dispatches one call per item unconditionally leaves this empty rather than
	// fabricating entries for decisions that were never made. The recovering
	// engine is where it is populated. It holds counts and names, never a
	// caller's data or a model's text.
	Ledger DecisionLedger
}

// Complete reports whether every item succeeded -- no failures, no
// cancellations. It reads both records a result keeps, the summary's counts and
// the items themselves, and is true only when neither says otherwise: a result
// assembled with a summary that lags its items does not report itself complete
// beside a non-empty Failures().
func (r BatchResult[Out]) Complete() bool {
	if r.Summary.Failed != 0 || r.Summary.Cancelled != 0 {
		return false
	}
	for _, item := range r.Items {
		if item.Status != ItemSucceeded {
			return false
		}
	}
	return true
}

// Values returns the succeeded items' values, in input order, and whether
// the result was Complete. The bool is the point: a caller cannot reach the
// []Out without also being handed the fact of whether it is all of them,
// which is the one thing a bare `([]T, error)` return cannot say when error
// is nil and the slice is short.
func (r BatchResult[Out]) Values() ([]Out, bool) {
	// Sized from the items, not from the summary: a summary with a negative
	// count panicked here.
	values := make([]Out, 0, len(r.Items))
	for _, item := range r.Items {
		if item.Status == ItemSucceeded {
			values = append(values, item.Value)
		}
	}
	return values, r.Complete()
}

// Failures returns every item that did not succeed, in input order: the "which
// did not" half of a partial result.
func (r BatchResult[Out]) Failures() []ItemResult[Out] {
	var failures []ItemResult[Out]
	for _, item := range r.Items {
		if item.Status != ItemSucceeded {
			failures = append(failures, item)
		}
	}
	return failures
}

// String summarizes the result for a log line -- counts and the policy that
// ran, never a caller's value.
func (r BatchResult[Out]) String() string {
	s := fmt.Sprintf("%s: %d/%d succeeded", r.Summary.Policy, r.Summary.Succeeded, r.Summary.Total)
	if r.Summary.Failed > 0 {
		s += fmt.Sprintf(", %d failed", r.Summary.Failed)
	}
	if r.Summary.Cancelled > 0 {
		s += fmt.Sprintf(", %d cancelled", r.Summary.Cancelled)
	}
	return s
}

// BatchMetrics is the per-item measurement of a plural run: the numbers that
// say whether a batch strategy is working, because an HTTP 200 hides omissions
// and invalid output the same way a shorter slice hides a partial result.
//
// It is computed here, from a BatchResult's own items, rather than tracked
// separately during a run: a second accounting kept in sync by hand with the one
// the engines already keep is exactly the two execution paths that could drift,
// so Metrics is a pure derivation. It reads the items and not the summary, which
// is a second record of the same counts that a result can be assembled with
// wrong.
//
// It measures valid-item ratio, total attempts, total repairs, cost per accepted
// item, omissions, and atomic-fallback counts, all read from data the engines
// collect per item. Omissions and AtomicFallbacks are zero for every BatchResult
// of the one-call-per-item engine, honestly: it dispatches one call per item
// unconditionally, so there is no chunk-level omission or fallback event for it
// to have produced -- Mode is "atomic" for everything it reports, which never
// matches ModeAtomicFallback and whose Attempts already starts at 1, so neither
// counter can be nonzero by construction. A BatchResult from the recovering
// engine is where these two numbers are real.
type BatchMetrics struct {
	Total int

	// ValidItemRatio is Succeeded / Total -- the ratio named first, since it
	// answers "did this batch work" without reading anything else.
	// Zero when Total is zero.
	ValidItemRatio float64

	Succeeded int
	Failed    int
	Cancelled int

	// TotalAttempts and TotalRepairs sum every item's own count, across
	// every pass a policy ran (including the retry pass). TotalAttempts is a sum
	// of the items' Attempts, which counts a batched request once for every item
	// it carried: it is not the number of requests the run made, and not what a
	// bill is reconciled against. Requests is.
	TotalAttempts int
	TotalRepairs  int

	// Requests is how many provider requests the whole run made, as measured:
	// BatchResult.Total.Attempts. Zero for a run that sent nothing.
	Requests int

	// AcceptedCost sums Cost.TotalCost over succeeded items only. A failed
	// item's provider call was still paid for, but produced nothing usable
	// -- folding its cost into "cost per accepted item" would make a
	// worse-performing batch look cheaper per unit of value, which is
	// backwards, so FailedCost is reported alongside it instead of being
	// hidden inside AcceptedCost.
	AcceptedCost float64
	FailedCost   float64

	// CostPriced reports whether any item in the batch carried a priced
	// cost. False means unknown, not free: unknown cost stays unknown through
	// per-item accounting. A caller must check this (and Succeeded > 0)
	// before reading CostPerAcceptedItem.
	CostPriced bool
	Currency   string

	// CostPerAcceptedItem is AcceptedCost / Succeeded, "the number that
	// actually says whether a batch strategy is working". Zero and meaningless
	// when Succeeded is zero or CostPriced is false.
	CostPerAcceptedItem float64

	// CostQuality says what CostPerAcceptedItem is worth, reusing
	// PricingQuality rather than the plain CostPriced bool alone. A bool can
	// only say known/unknown; it cannot say that a known figure was assembled
	// from a projection rather than the provider's own reported usage.
	// PricingUnknown whenever CostPerAcceptedItem itself is meaningless --
	// CostPriced is false, or every priced item in the batch failed, leaving
	// nothing priced to divide into AcceptedCost. Otherwise the weakest quality
	// among the succeeded, priced items that make up AcceptedCost:
	// PricingEstimated if any of them is an estimate (a plan made before the
	// call, or a provider that answered with no usage), else the quality those
	// items share (PricingExact or PricingFree; in practice uniform within one
	// batch, since every item runs the same op against the same model's rate
	// card). A caller reading CostPerAcceptedItem alongside CostQuality can tell
	// a number backed entirely by reported usage from one where at least one
	// accepted item's spend is a guess, instead of a single average that would
	// look identical either way.
	CostQuality PricingQuality

	// Omissions counts items that were not resolved by the very first batched
	// pass that asked about them: an item whose final Mode is "mdsp" but
	// whose Attempts is greater than one (it took an isolate pass to
	// resolve), plus every item whose final Mode is ModeAtomicFallback
	// (every one of those was omitted by at least one batched pass before
	// falling back). It matters because an HTTP 200 hides omissions: a batch
	// that succeeds overall can still have paid for a chunk response that
	// dropped items nobody would otherwise see.
	Omissions int

	// AtomicFallbacks counts items whose final Mode is ModeAtomicFallback --
	// set whether or not that atomic call itself succeeded, so a batch
	// strategy that is failing badly enough to fall back on most of its
	// items shows up here even if every fallback call eventually succeeded
	// and ValidItemRatio looks fine.
	AtomicFallbacks int
}

// Metrics computes the per-item measurement from r's own items. Total is the
// number of items, and Succeeded, Failed and Cancelled are counted from their
// statuses; the summary is not read, so a result whose summary lies does not
// make its measurement lie. An item with no status counts in Total and in no
// outcome.
func (r BatchResult[Out]) Metrics() BatchMetrics {
	m := BatchMetrics{
		Total:       len(r.Items),
		CostQuality: PricingUnknown,
		Requests:    r.Total.Attempts,
	}

	for _, item := range r.Items {
		switch item.Status {
		case ItemSucceeded:
			m.Succeeded++
		case ItemFailed:
			m.Failed++
		case ItemCancelled:
			m.Cancelled++
		}
	}
	if m.Total > 0 {
		m.ValidItemRatio = float64(m.Succeeded) / float64(m.Total)
	}

	for _, item := range r.Items {
		m.TotalAttempts += item.Attempts
		m.TotalRepairs += item.Repairs

		switch {
		case item.Mode == ModeAtomicFallback:
			m.Omissions++
			m.AtomicFallbacks++
		case item.Mode == modeMDSP && item.Attempts > 1:
			m.Omissions++
		}

		if !item.Cost.Priced {
			continue
		}
		m.CostPriced = true
		m.Currency = item.Cost.Currency
		if item.Status != ItemSucceeded {
			m.FailedCost += item.Cost.TotalCost
			continue
		}
		m.AcceptedCost += item.Cost.TotalCost
		switch {
		case m.CostQuality == PricingUnknown && item.Cost.Quality.Known():
			// First succeeded, priced item with a known quality: adopt it. An
			// item whose engine did not set one says nothing about the figure,
			// and adopting its empty quality would keep a later one out.
			m.CostQuality = item.Cost.Quality
		case m.CostQuality == PricingFree && item.Cost.Quality == PricingExact:
			// A batch with both free and exact items has real spend in its
			// average: the exact figure is what the average is worth.
			m.CostQuality = PricingExact
		case item.Cost.Quality == PricingEstimated:
			// PricingEstimated is the weakest quality a priced figure can
			// carry (see CostQuality's own doc comment) -- once any accepted
			// item's cost is a projection, the whole per-item average is,
			// and a later Exact/Free item cannot undo that.
			m.CostQuality = PricingEstimated
		}
	}

	if m.CostPriced && m.Succeeded > 0 {
		m.CostPerAcceptedItem = m.AcceptedCost / float64(m.Succeeded)
	}

	return m
}

// BatchPolicy is the partial-success and failure policies of the
// one-call-per-item engine. Each defines scheduling (does the run keep going
// after a failure), cancellation (does it stop work already in flight), retry
// (does it spend extra calls on a failed item), and return behaviour (does the
// call itself report an error, or only the BatchResult).
type BatchPolicy uint8

const (
	// PolicyUnspecified: the caller stated no policy. RunOpManyPartial runs it
	// as PolicyCollectFailures, whatever the number of items, and records that in
	// BatchSummary.Policy and PolicyReason, so a returned result never leaves
	// PolicyUnspecified in place -- an unresolved policy on a finished result
	// would be as dishonest as an unexplained partial one. It used to resolve to
	// one policy up to twenty items and another above, which made a call return an
	// error at 20 items and a result with failures in it at 21.
	PolicyUnspecified BatchPolicy = iota

	// PolicyFailFast: stop dispatching new items the instant one fails
	// terminally, and stop the ones already in flight from starting more
	// work. Items never started are ItemCancelled. No retry. The call
	// reports an error -- staying alive after a failure is not this
	// policy's contract, stopping quickly is.
	PolicyFailFast

	// PolicyCollectFailures: run every item once, cancel nothing, retry
	// nothing. The call reports no error even when items failed -- a
	// caller choosing this policy is explicitly asking for whatever came
	// back, checked through BatchResult.Complete(), not through the error
	// return.
	PolicyCollectFailures

	// PolicyRetryFailedItems: run every item once, then retry -- isolated,
	// only the items still failed, so a valid item is never replayed because a
	// sibling failed -- up to a bounded number of additional attempts. If any
	// item is still failed once retries are exhausted, the call reports an
	// error: this policy's contract is "get everything, spending retries to do
	// it", and returning success quietly after the retry budget did not work
	// would hide that it did not work.
	PolicyRetryFailedItems

	// PolicyRetryThenCollect: identical scheduling and retry behaviour to
	// PolicyRetryFailedItems, but never fails the call -- whatever is still
	// failed after retries is reported the same way PolicyCollectFailures
	// reports it: a caller running hundreds of items does not want the whole
	// call to error out because two of them, even after retrying, did not
	// come back.
	PolicyRetryThenCollect

	// PolicyRequireAll: run every item once, cancel nothing, retry nothing
	// -- the same scheduling as PolicyCollectFailures -- but any failure
	// fails the call. It differs from PolicyFailFast (which stops at the
	// first failure, so a caller never learns about the others) by running
	// every item regardless, and from PolicyCollectFailures (which never
	// fails the call) only in the return value: same input, same items
	// attempted, different answer to "did this call succeed".
	PolicyRequireAll
)

// String renders the policy for a log line, a BatchSummary, or a plan
// explanation.
func (p BatchPolicy) String() string {
	switch p {
	case PolicyFailFast:
		return "fail_fast"
	case PolicyCollectFailures:
		return "collect_failures"
	case PolicyRetryFailedItems:
		return "retry_failed_items"
	case PolicyRetryThenCollect:
		return "retry_then_collect"
	case PolicyRequireAll:
		return "require_all"
	default:
		return "unspecified"
	}
}
