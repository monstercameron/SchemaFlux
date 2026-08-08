package types

import "fmt"

// PL-008: partial success and failure policies.
//
// (`[]T, error`) cannot express "7 of 10 succeeded, here is which and why".
// A caller reading a shorter slice with no error is reading success, not a
// partial result -- nothing distinguishes "asked for 7" from "3 were
// silently dropped". BatchResult is what this task's Verify line requires
// instead: a value that names every item's status, and whose only route to
// a plain []Out (Values) hands back a bool the caller must read alongside
// it. A caller who ignores the bool still gets an honestly-short slice, not
// a padded one -- but the bool is what stands between this type and the
// exact failure mode PL-008 exists to remove.
//
// PL-013 (per-item metrics) is layered on the same type: BatchMetrics is
// computed from a BatchResult's own ItemResults, because the numbers PL-013
// asks for (valid-item ratio, cost per accepted item) are meaningless
// without the same per-item accounting PL-008 already has to keep.

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

	// ItemCancelled: never attempted, because the policy running the batch
	// stopped dispatching new items after an earlier item's terminal
	// failure (PolicyFailFast). Distinct from ItemFailed on purpose --
	// nothing about this item's own input or a provider's answer to it was
	// ever found wrong, so folding it into "failed" would blame the item
	// for a policy decision that had nothing to do with it.
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
	// caller's order, never a response's, the same rule PL-003's id
	// protocol already enforces within a single chunk, carried up to the
	// whole batch.
	Index int

	Status ItemStatus

	// Value is meaningful only when Status is ItemSucceeded; the zero value
	// otherwise, so a caller who skips the Status check gets a visibly
	// empty answer rather than a plausible-looking one borrowed from
	// nowhere.
	Value Out

	// Attempts is every provider call spent on this item, across the
	// initial pass and any retry pass a policy ran for it.
	Attempts int

	// Repairs is the subset of Attempts that were repairs rather than fresh
	// tries -- PL-013's repair count, read straight from the same envelope
	// every other operation already reports through (types.Meta.Repairs).
	Repairs int

	// Cost is this item's own provider spend, summed across every attempt.
	// Cost.Priced follows the same discipline as CostEstimate.Priced: false
	// means unknown, not free.
	Cost CostInfo

	// Mode names how this item ran. PL-008 is delivered as an item-level
	// engine -- see RunOpManyPartial's doc comment in internal/ops/partial.go
	// -- so this is "atomic" for everything RunOpManyPartial produces today.
	// The field exists now so a future MDSP-chunk-aware partial engine
	// (PL-009) can report "mdsp" per item without changing this type.
	Mode string

	// Err explains a non-succeeded item. Nil for ItemSucceeded. Never the
	// caller's payload -- whatever produced it already went through this
	// library's own error classification before reaching here.
	Err error
}

// ModeAtomicFallback is the ItemResult.Mode value PL-009's progressive
// recovery cascade (internal/ops/recover.go) uses for an item that no MDSP
// pass resolved and that was recovered instead by one isolated atomic call
// over just that item -- set whether or not that atomic call itself
// succeeded, since "it reached the fallback stage" and "the fallback stage
// worked" are different facts and BatchMetrics needs the first one even when
// the second is false. The paired value for an item resolved from a batched
// response is "mdsp" (ExecutionShape.String() for ShapeMDSP, reused rather
// than a second literal that could drift from it -- see
// RunOpBatchResult's Meta.Strategy in batchprotocol.go, which sets the
// identical string).
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

	// Policy is the policy that actually ran: the caller's own, or the one
	// DefaultPolicyFor resolved when the caller left it PolicyUnspecified.
	// Recorded here so a stored BatchResult can be read without also
	// keeping the call that produced it.
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
}

// Complete reports whether every item succeeded -- no failures, no
// cancellations.
func (r BatchResult[Out]) Complete() bool {
	return r.Summary.Failed == 0 && r.Summary.Cancelled == 0
}

// Values returns the succeeded items' values, in input order, and whether
// the result was Complete. The bool is the point: a caller cannot reach the
// []Out without also being handed the fact of whether it is all of them,
// which is the one thing a bare `([]T, error)` return cannot say when error
// is nil and the slice is short.
func (r BatchResult[Out]) Values() ([]Out, bool) {
	values := make([]Out, 0, r.Summary.Succeeded)
	for _, item := range r.Items {
		if item.Status == ItemSucceeded {
			values = append(values, item.Value)
		}
	}
	return values, r.Complete()
}

// Failures returns every item that did not succeed, in input order --
// PL-008's "a partial result must say which items succeeded and which did
// not", the "which did not" half.
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

// BatchMetrics is PL-013's per-item measurement: the numbers that say
// whether a batch strategy is working, because an HTTP 200 hides omissions
// and invalid output the same way a shorter slice hides a partial result.
//
// It is computed here, from a BatchResult's own ItemResults, rather than
// tracked separately during a run -- a second accounting pass kept in sync
// by hand with the one RunOpManyPartial already keeps is exactly the "two
// execution paths that could drift" AGENTS.md warns against, so Metrics is
// a pure derivation instead.
//
// What this delivery measures: valid-item ratio, total attempts, total
// repairs, cost per accepted item, omissions, and atomic-fallback counts --
// all read from data RunOpManyPartial or RunOpManyRecover (PL-009,
// internal/ops/recover.go) already collects per item. Omissions and
// AtomicFallbacks are zero for every BatchResult RunOpManyPartial produces,
// honestly: RunOpManyPartial dispatches one call per item unconditionally,
// so there is no chunk-level omission or fallback event for it to have
// produced -- Mode is "atomic" for everything it reports (see
// RunOpManyPartial's own doc comment, internal/ops/partial.go), which never
// matches ModeAtomicFallback and whose Attempts already starts at 1, so
// neither counter can be nonzero by construction. A BatchResult from
// RunOpManyRecover is where these two numbers are real.
type BatchMetrics struct {
	Total int

	// ValidItemRatio is Succeeded / Total -- the ratio PL-013 names first,
	// since it answers "did this batch work" without reading anything else.
	// Zero when Total is zero.
	ValidItemRatio float64

	Succeeded int
	Failed    int
	Cancelled int

	// TotalAttempts and TotalRepairs sum every item's own count, across
	// every pass a policy ran (including PL-008's retry pass).
	TotalAttempts int
	TotalRepairs  int

	// AcceptedCost sums Cost.TotalCost over succeeded items only. A failed
	// item's provider call was still paid for, but produced nothing usable
	// -- folding its cost into "cost per accepted item" would make a
	// worse-performing batch look cheaper per unit of value, which is
	// backwards, so FailedCost is reported alongside it instead of being
	// hidden inside AcceptedCost.
	AcceptedCost float64
	FailedCost   float64

	// CostPriced reports whether any item in the batch carried a priced
	// cost. False means unknown, not free -- PR-002's rule, carried into
	// per-item accounting. A caller must check this (and Succeeded > 0)
	// before reading CostPerAcceptedItem.
	CostPriced bool
	Currency   string

	// CostPerAcceptedItem is AcceptedCost / Succeeded -- PL-013's own
	// phrase, "the number that actually says whether a batch strategy is
	// working". Zero and meaningless when Succeeded is zero or CostPriced
	// is false.
	CostPerAcceptedItem float64

	// Omissions counts items that were not resolved by the very first MDSP
	// pass that asked about them: an item whose final Mode is "mdsp" but
	// whose Attempts is greater than one (it took an isolate pass to
	// resolve), plus every item whose final Mode is ModeAtomicFallback
	// (every one of those was omitted by at least one MDSP pass before
	// falling back). PL-013's own phrase for why this matters: "HTTP 200
	// hides omissions" -- a batch that succeeds overall can still have paid
	// for a chunk response that dropped items nobody would otherwise see.
	Omissions int

	// AtomicFallbacks counts items whose final Mode is ModeAtomicFallback --
	// set whether or not that atomic call itself succeeded, so a batch
	// strategy that is failing badly enough to fall back on most of its
	// items shows up here even if every fallback call eventually succeeded
	// and ValidItemRatio looks fine.
	AtomicFallbacks int
}

// Metrics computes PL-013's per-item measurement from r's own ItemResults.
func (r BatchResult[Out]) Metrics() BatchMetrics {
	m := BatchMetrics{
		Total:     r.Summary.Total,
		Succeeded: r.Summary.Succeeded,
		Failed:    r.Summary.Failed,
		Cancelled: r.Summary.Cancelled,
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
		if item.Status == ItemSucceeded {
			m.AcceptedCost += item.Cost.TotalCost
		} else {
			m.FailedCost += item.Cost.TotalCost
		}
	}

	if m.CostPriced && m.Succeeded > 0 {
		m.CostPerAcceptedItem = m.AcceptedCost / float64(m.Succeeded)
	}

	return m
}

// BatchPolicy is PL-008's five partial-success and failure policies. Each
// defines scheduling (does the run keep going after a failure), cancellation
// (does it stop work already in flight), retry (does it spend extra calls on
// a failed item), and return behaviour (does the call itself report an
// error, or only the BatchResult).
type BatchPolicy uint8

const (
	// PolicyUnspecified: the caller stated no policy. RunOpManyPartial
	// resolves it through DefaultPolicyFor(len(items)) and records the
	// resolution in BatchSummary.Policy/PolicyReason, so a returned result
	// never leaves PolicyUnspecified in place -- an unresolved policy on a
	// finished result would be as dishonest as an unexplained partial one.
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
	// only the items still failed, exactly PL-009's "a valid item is never
	// replayed because a sibling failed" applied at item granularity -- up
	// to a bounded number of additional attempts. If any item is still
	// failed once retries are exhausted, the call reports an error: this
	// policy's contract is "get everything, spending retries to do it",
	// and returning success quietly after the retry budget did not work
	// would hide that it did not work.
	PolicyRetryFailedItems

	// PolicyRetryThenCollect: identical scheduling and retry behaviour to
	// PolicyRetryFailedItems, but never fails the call -- whatever is still
	// failed after retries is reported the same way PolicyCollectFailures
	// reports it. This is the policy DefaultPolicyFor selects for a long
	// batch: a caller running hundreds of items does not want the whole
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

// LongBatchThreshold is DefaultPolicyFor's cutover point between
// PolicyFailFast (a small batch, where a caller is more likely watching and
// a fast stop wastes the least) and PolicyRetryThenCollect (the task's
// stated default for a long batch, where restarting hundreds of items over
// a couple of stragglers costs more than reporting them and moving on).
const LongBatchThreshold = 20

// DefaultPolicyFor resolves PolicyUnspecified into a concrete policy given
// how many items are being run, and says why -- so a caller inspecting a
// finished BatchResult can tell a deliberate choice from a fallback.
func DefaultPolicyFor(itemCount int) (policy BatchPolicy, reason string) {
	if itemCount > LongBatchThreshold {
		return PolicyRetryThenCollect, fmt.Sprintf(
			"no policy specified; %d items exceeds the long-batch threshold (%d), defaulting to retry-then-collect",
			itemCount, LongBatchThreshold)
	}
	return PolicyFailFast, fmt.Sprintf(
		"no policy specified; %d items is at or below the long-batch threshold (%d), defaulting to fail-fast",
		itemCount, LongBatchThreshold)
}
