package flow

import (
	"context"
	"sync/atomic"

	schemaflux "github.com/monstercameron/schemaflux/v2"

	"github.com/monstercameron/schemaflux/internal/ops"
)

// Three samples agreeing on an invented figure is three samples wrong:
// correlated models share hallucinations, so "the samples agree" is not evidence
// that the answer is right, only that the samples are alike. That is why
// reconciliation is not built into Vote as majority wins. Exact agreement,
// agreement by field, a validator that picks among the candidates, evidence, an
// adjudicator model: which of those can be trusted for an operation is a policy
// the caller decides, and a library that bakes one in as the default decides it
// without saying so. A Reconciler is consulted and never bypassed, and it may
// abstain; Vote does not fall back to a plurality when it does.

// ReconcileOutcome is what a Reconciler decides about one set of samples.
type ReconcileOutcome[T any] struct {
	// Winner is the reconciler's chosen answer. When Abstain is true it is the
	// reconciler's best account of what it saw, kept for a reviewer to look at
	// and not for a caller to use without checking Abstain.
	Winner T

	// Agree is how many of the samples the reconciler counted as agreeing with
	// Winner, by whatever notion of agreement it implements (exact equality, a
	// field match, a validator passing). It is a count over the samples the
	// reconciler was given, so it is from zero to the number of samples that
	// succeeded; a count outside that is an error of the reconciler. It is not a
	// judgement about whether Winner is correct.
	Agree int

	// Abstain, when true, says the reconciler declines to certify Winner. Vote
	// then returns a review request instead of the answer. False means Winner is
	// the answer.
	Abstain bool

	// Evidence, FailedChecks and SuggestedAction describe the abstention, and
	// become the Review of the error Vote returns. They are the reconciler's own
	// findings (which rule failed, how the samples split), never the caller's
	// data. They are ignored when Abstain is false.
	Evidence        []string
	FailedChecks    []string
	SuggestedAction string
}

// A Reconciler turns the samples of a Vote into one winner, or an abstention.
//
// It receives every sample that succeeded, in the order the samples were
// started; a reconciler that cares about any other order sorts by its own key.
// It runs on the caller's goroutine, once, after every sample has finished. An
// error it returns is a malfunction of the reconciler (a bug in it, or a
// validator it called failing for its own reasons) and is returned by Vote as
// it was given, and never folded into an abstention: a caller debugging a
// reconciler has to be able to tell it from a caller reading a review. An
// abstention is not an error: it is ReconcileOutcome.Abstain.
type Reconciler[T any] func(ctx context.Context, samples []T) (ReconcileOutcome[T], error)

// maxSamples is the most samples one Vote takes. Agreement among more than a
// few answers is not a thing a program waits for, and a count that a caller
// computes or reads is refused before anything is sized by it: a number such as
// math.MaxInt must not become a request for memory or a bill.
const maxSamples = 64

// VoteOptions configures how the samples of a Vote are run. The zero value is
// the defaults. Several values passed to Vote are merged left to right, a later
// field that is not zero winning.
type VoteOptions struct {
	// Concurrency bounds how many samples run at once. Zero means 4. One makes
	// the run sequential, which is what a caller wants when the provider is rate
	// limited. More than there are samples is as many as there are samples. A
	// negative value is an error of kind ErrConfiguration: there is no such thing
	// as no samples at once.
	Concurrency int
}

// VoteRecord reports what Vote measured about its own samples, and nothing
// about whether the answer is correct. It is returned beside the answer and
// beside an error alike, so that a refused vote can still be inspected.
type VoteRecord struct {
	// Requested is how many samples were asked for.
	Requested int

	// Succeeded is how many samples returned a value.
	Succeeded int

	// Failed is Requested minus Succeeded.
	Failed int

	// Internal is how many of the failed samples failed with an error of kind
	// ErrInternal: a panic or a broken rule of the library, a defect and not a
	// disagreement. It is part of Failed, never added to it, and a vote with a
	// defect among its samples still reconciles the ones that succeeded: the
	// record is where it says so.
	Internal int

	// Agree is the reconciler's own count of the samples that agreed with the
	// winner it returned. Zero when the vote did not reach the reconciler.
	Agree int

	// AgreementRate is Agree divided by Succeeded, and it is named for exactly
	// what it counts: the fraction of samples that produced, by the reconciler's
	// notion of it, the same answer. It is not a confidence. Sample agreement
	// measures the samples and not the chance that the winner is right, and
	// correlated models agree with each other while being wrong. A caller who
	// wants a trust signal builds one from the counts and their own knowledge of
	// whether the samples were independent. Zero when no sample succeeded.
	AgreementRate float64
}

// Vote runs step the given number of times, up to Concurrency at once, then asks
// reconcile to turn the samples into one answer or an abstention.
//
//	invoice, votes, err := flow.Vote(ctx, 5, extract, flow.ExactAgreement[Invoice](3))
//
// What runs at once: the samples are started in order, at most Concurrency of
// them running at any moment (4 unless VoteOptions says otherwise), and every
// one is run under ctx, so the Scheduler of the client on it bounds the requests
// they make. A sample's failure does not end the run: one failed sample, a
// timeout say, is not a reason to throw away the ones that succeeded. reconcile
// then runs once, on the caller's goroutine, over the samples that succeeded, in
// the order they were started. Vote returns when every sample has returned and
// reconcile has, and no goroutine outlives it.
//
// What is returned:
//
//   - the reconciler's winner, the VoteRecord, and a nil error.
//   - the reconciler abstained: the zero value and the VoteRecord, and an *Error
//     of kind ErrReviewRequired whose Err is a *ReviewError[T], which holds the
//     abstained winner as the Review's Candidate. An abstained vote does not hand
//     back a plausible-looking answer for a caller who forgot to check the error.
//   - every sample failed: the zero value, the VoteRecord, and the error of the
//     first of the samples to have been started, the same value its step
//     returned. With nothing to reconcile, reconcile is not called.
//   - a sample failed with an error of kind ErrInternal, a defect: it is a failed
//     sample like any other, and VoteRecord.Internal counts it. The vote goes on
//     with the samples that succeeded.
//   - reconcile returned an error: the zero value, the VoteRecord, and that error,
//     the same value. A reconciler that reports an Agree below zero or above the
//     number of samples that succeeded is an *Error of kind ErrConfiguration.
//   - ctx ended before every sample had run: the *Error for the context (kind
//     ErrCanceled or ErrTimeout) and an empty record, whether or not some samples
//     had succeeded; a run that was stopped did not finish.
//
// Evidence and invariant checks that apply to one answer are not repeated here.
// The step has already run whatever checks the operation it samples makes on each
// attempt, and a vote decides between answers that were already checked.
//
// A sample that panics, or ends its goroutine with runtime.Goexit, is a failed
// sample whose error is an *Error of kind ErrInternal. A nil step, a nil
// reconcile, a number of samples below 1 or above 64 and a nil ctx are errors of
// kind ErrInvalidRequest, and nothing runs and nothing is sized by the number
// given; a ctx that is already done is one of kind ErrCanceled or ErrTimeout.
// Vote calls no model itself.
func Vote[T any](ctx context.Context, samples int, step schemaflux.Step[T], reconcile Reconciler[T], opts ...VoteOptions) (T, VoteRecord, error) {
	var zero T
	if err := begin(ctx, "flow.vote"); err != nil {
		return zero, VoteRecord{}, err
	}
	if step == nil {
		return zero, VoteRecord{}, refuse("flow.vote", "there is no step to sample")
	}
	if reconcile == nil {
		// No default reconciler on purpose: agreement is a policy the caller
		// supplies, not something to decide silently on their behalf.
		return zero, VoteRecord{}, refuse("flow.vote", "there is no reconciler, and agreement is a policy the caller supplies")
	}
	if samples < 1 || samples > maxSamples {
		return zero, VoteRecord{}, refuse("flow.vote", "the number of samples must be from 1 to %d", maxSamples)
	}
	var merged VoteOptions
	for _, o := range opts {
		if o.Concurrency != 0 {
			merged.Concurrency = o.Concurrency
		}
	}
	if merged.Concurrency < 0 {
		return zero, VoteRecord{}, misconfigured("flow.vote", "Concurrency must not be negative")
	}

	reconciler := func(ctx context.Context, got []T) (ops.ReconcileOutcome[T], error) {
		outcome, err := reconcile(ctx, got)
		if err != nil {
			return ops.ReconcileOutcome[T]{}, own(err)
		}
		return ops.ReconcileOutcome[T](outcome), nil
	}
	var defects atomic.Int64
	counted := func(ctx context.Context) (T, error) {
		// A sample that panics or ends its goroutine never reaches the lines
		// after the call, and is a defect as well as a failure.
		returned := false
		defer func() {
			if !returned {
				defects.Add(1)
			}
		}()
		value, err := step(ctx)
		returned = true
		if err != nil && isDefect(err) {
			defects.Add(1)
		}
		return value, err
	}
	winner, record, err := ops.Vote(ctx, samples, ownStep(counted), reconciler, ops.VoteOptions{Concurrency: merged.Concurrency})
	out := VoteRecord{
		Requested:     record.Requested,
		Succeeded:     record.Succeeded,
		Failed:        record.Failed,
		Internal:      int(defects.Load()),
		Agree:         record.Agree,
		AgreementRate: record.AgreementRate,
	}
	if record == (ops.VoteRecord{}) {
		// A run that was stopped did not finish, and its record is empty: the
		// defects counted before it stopped are not a part of it.
		out = VoteRecord{}
	}
	if err != nil {
		return zero, out, endReview[T]("flow.vote", err)
	}
	return winner, out, nil
}

// ExactAgreement is a Reconciler for comparable types: the largest group of
// identical samples wins, and the vote abstains when that group has fewer than
// minimum members.
//
//	flow.ExactAgreement[string](2) // two of the samples must be the same
//
// It is provided as the plainest case and is not a recommended default. "Largest
// group wins" cannot tell a correct answer from a popular one, and correlated
// models make correlated mistakes. A tie is broken by first occurrence among the
// samples, which keeps the result deterministic without needing the samples to be
// orderable. Agree is the size of the winning group.
//
// minimum has to be at least 1: a minimum of zero or less can never abstain, so
// the reconciler it returns fails with an *Error of kind ErrInvalidRequest each
// time it is called, instead of agreeing to everything.
func ExactAgreement[T comparable](minimum int) Reconciler[T] {
	inner := ops.ExactAgreement[T](minimum)
	return func(ctx context.Context, samples []T) (ReconcileOutcome[T], error) {
		if minimum < 1 {
			return ReconcileOutcome[T]{}, refuse("flow.exactagreement", "a minimum of %d can never abstain, and has to be at least 1", minimum)
		}
		outcome, err := inner(ctx, samples)
		if err != nil {
			return ReconcileOutcome[T]{}, end("flow.exactagreement", err)
		}
		return ReconcileOutcome[T](outcome), nil
	}
}
