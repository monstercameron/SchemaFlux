package ops

import (
	"context"
	"errors"
	"fmt"

	"github.com/monstercameron/schemaflux/internal/types"
)

// CF-002. Three samples agreeing on an invented figure is three samples wrong
// -- correlated models share hallucinations, so "the samples agree" is not
// evidence the answer is right, only evidence the samples are alike. That is
// why reconciliation is not built in here as majority-wins: exact agreement,
// field-level voting, deterministic validation then selection, evidence
// weighting, an adjudicator model -- which of those is trustworthy for a given
// operation is a policy decision the caller makes, and a library that bakes
// one in as the default is making it for them without saying so.
//
// A Reconciler is therefore consulted, never bypassed, and it is allowed to
// abstain: Vote does not fall back to plurality when the reconciler declines,
// because a caller who wrote a reconciler that abstains on a split vote wrote
// it that way on purpose.

// ReconcileOutcome is what a Reconciler decides about one set of samples.
type ReconcileOutcome[T any] struct {
	// Winner is the reconciler's chosen answer -- or, when Abstain is true,
	// its best account of what it saw, kept for a reviewer to look at, not for
	// a caller to use without checking Abstain first.
	Winner T

	// Agree is how many of the samples the reconciler counted as agreeing
	// with Winner, by whatever notion of agreement it implements (exact
	// equality, a field match, a validator passing). It is a count over the
	// samples the reconciler was given, not a judgement about whether Winner
	// is correct.
	Agree int

	// Abstain, when true, means the reconciler declines to certify Winner.
	Abstain bool

	// Evidence, FailedChecks, and SuggestedAction feed a ReviewPacket when
	// Abstain is true. They are ignored otherwise.
	Evidence        []string
	FailedChecks    []string
	SuggestedAction string
}

// Reconciler turns a set of samples into one winner, or an abstention.
//
// It receives every sample that succeeded, in no particular order beyond
// "the order MapReduce's worker pool finished them" -- a reconciler that cares
// about order should sort by its own key. An error return that is not the
// review sentinel is a reconciler malfunction (a bug in the reconciler itself,
// or a validator it called failing for its own reasons), and is propagated as
// a plain error rather than folded into an abstention: those are different
// facts, and a caller debugging a reconciler needs to tell them apart from a
// caller reading a review packet.
type Reconciler[T any] func(ctx context.Context, samples []T) (ReconcileOutcome[T], error)

// VoteOptions configures how the samples are run.
type VoteOptions struct {
	// Concurrency bounds how many samples run at once. Zero means
	// DefaultConcurrency.
	Concurrency int
}

// VoteRecord reports what Vote measured about its own samples -- not what it
// concluded about correctness. See the note on AgreementRate below for why
// that distinction gets a whole comment.
type VoteRecord struct {
	// Requested is how many samples were asked for.
	Requested int

	// Succeeded is how many samples actually returned a value.
	Succeeded int

	// Failed is Requested minus Succeeded.
	Failed int

	// Agree is the reconciler's own count of samples that agreed with the
	// winner it returned.
	Agree int

	// AgreementRate is Agree / Succeeded, and it is named for exactly what it
	// measures: the fraction of samples that produced (the reconciler's
	// notion of) the same answer. It is deliberately not called Confidence.
	//
	// Judgement call, spelled out because the task asked for it to be
	// defended: sample agreement is a measurement of the samples, not a
	// probability the winner is correct, and the review this library is being
	// rebuilt against exists because a model's self-reported score got read as
	// a measurement once already (D-09). Exposing a single "confidence" float
	// here would be the same mistake with better PR -- a caller who sees a
	// number between 0 and 1 next to an answer reads it as "how likely is this
	// right", and correlated models can produce a high agreement rate while
	// being uniformly wrong (the same training-data gap, the same prompt
	// leading every sample the same way). Naming it AgreementRate and keeping
	// it in Meta-shaped territory, next to Requested/Succeeded/Agree rather
	// than beside the answer, is the honest version: it says what was counted,
	// not what it means. A caller who wants a trust signal builds one from the
	// counts plus their own knowledge of whether the samples were actually
	// independent -- that composition is exactly what Reconciler exists to let
	// them own.
	//
	// Zero when no sample succeeded; there is nothing to have agreed on.
	AgreementRate float64
}

// Vote runs step samples times with bounded concurrency, then asks reconcile
// to turn the results into one answer or an abstention.
//
// Concurrency reuses MapReduce's worker pool (CF-009) instead of starting
// goroutines by hand, for the same reason CF-004 built it: an ad hoc semaphore
// dispatches in whatever order the scheduler wakes goroutines, which is exactly
// the kind of inconsistency a caller asking for Concurrency: 1 (a rate-limited
// provider) is trying to avoid.
//
// A sample failure does not abort the run -- ContinueOnError is set on the
// underlying MapReduce call, because one bad sample (a transient timeout) is
// not a reason to throw away the samples that already succeeded. All samples
// failing is: there is nothing to reconcile, and Vote returns that as a plain
// error rather than calling reconcile with an empty slice.
//
// Evidence and invariant checks that apply to a single answer are not
// re-applied here. Vote's Step already ran whatever checks the operation it
// samples performs on each attempt; a vote decides between already-checked
// candidates, it does not replace the checking.
func Vote[T any](ctx context.Context, samples int, step Step[T], reconcile Reconciler[T], opts VoteOptions) (T, VoteRecord, error) {
	var zero T

	if step == nil {
		return zero, VoteRecord{}, errors.New("vote: no step to sample")
	}
	if reconcile == nil {
		// No default reconciler on purpose: agreement is a policy, not
		// something this package gets to decide silently on the caller's
		// behalf.
		return zero, VoteRecord{}, errors.New("vote: no reconciler; agreement is a policy the caller must supply")
	}
	if samples <= 0 {
		return zero, VoteRecord{}, fmt.Errorf("vote: %d is not a number of samples", samples)
	}
	if err := ctx.Err(); err != nil {
		return zero, VoteRecord{}, err
	}

	indices := make([]int, samples)
	for i := range indices {
		indices[i] = i
	}

	mrOpts := MapReduceOptions{
		ChunkSize:       1,
		Concurrency:     opts.Concurrency,
		ContinueOnError: true,
	}

	results, summary, err := MapReduce(ctx, indices, mrOpts,
		func(runCtx context.Context, _ []int) (T, error) {
			return step(runCtx)
		})
	if err != nil {
		// MapReduce with ContinueOnError only returns an error for a nil
		// operation or an empty item list, neither of which is reachable here
		// (operation is a closure over step, samples > 0 was checked above).
		// Handled rather than assumed unreachable, so a change to MapReduce's
		// contract cannot silently turn into a nil pointer here.
		return zero, VoteRecord{}, err
	}

	record := VoteRecord{
		Requested: samples,
		Succeeded: len(results),
		Failed:    len(summary.Failed),
	}

	if len(results) == 0 {
		var firstErr error
		if len(summary.Failed) > 0 {
			firstErr = summary.Failed[0].Err
		}
		return zero, record, fmt.Errorf("vote: all %d samples failed, first error: %w", samples, firstErr)
	}

	outcome, err := reconcile(ctx, results)
	if err != nil {
		return zero, record, fmt.Errorf("vote: reconciler failed: %w", err)
	}

	record.Agree = outcome.Agree
	if record.Succeeded > 0 {
		record.AgreementRate = float64(outcome.Agree) / float64(record.Succeeded)
	}

	if outcome.Abstain {
		evidence := append([]string{
			fmt.Sprintf("%d of %d samples agreed with the reconciler's candidate", outcome.Agree, record.Succeeded),
		}, outcome.Evidence...)

		packet := types.ReviewPacket[T]{
			Candidate:       outcome.Winner,
			Evidence:        evidence,
			FailedChecks:    outcome.FailedChecks,
			Attempts:        record.Succeeded,
			SuggestedAction: outcome.SuggestedAction,
		}
		// The zero value is returned for use, not outcome.Winner: an abstained
		// vote must not hand back a plausible-looking answer for a caller that
		// forgot to check the error. The best guess still travels inside the
		// review packet, for whoever handles the review.
		return zero, record, types.NewReviewRequired(packet)
	}

	return outcome.Winner, record, nil
}

// ExactAgreement is a ready Reconciler for comparable types: the largest group
// of identical samples wins, and the vote abstains when that group's size is
// below min.
//
// It is provided as the trivial pluggable case, not as a recommended default.
// "Largest group wins" cannot tell a correct answer from a popular one, and
// correlated models make correlated mistakes -- the exact failure mode CF-002
// exists to name. Ties are broken by first occurrence in samples, which keeps
// the result deterministic without needing samples to be orderable.
func ExactAgreement[T comparable](min int) Reconciler[T] {
	return func(_ context.Context, samples []T) (ReconcileOutcome[T], error) {
		counts := make(map[T]int, len(samples))
		for _, sample := range samples {
			counts[sample]++
		}

		var winner T
		best := 0
		for _, sample := range samples {
			if counts[sample] > best {
				best = counts[sample]
				winner = sample
			}
		}

		outcome := ReconcileOutcome[T]{Winner: winner, Agree: best}
		if best < min {
			outcome.Abstain = true
			outcome.FailedChecks = []string{"agreement below minimum"}
			outcome.SuggestedAction = fmt.Sprintf(
				"no sample group reached the required agreement of %d; review manually", min)
		}
		return outcome, nil
	}
}
