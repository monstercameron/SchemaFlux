package flow

import (
	"context"
	"errors"
	"strings"

	schemaflux "github.com/monstercameron/schemaflux/v2"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
)

// Review is what a person is asked to look at when automated recovery has
// stopped. It is a successful outcome and not a breakdown: the library has spent
// its budget and cannot certify an answer by itself. An invariant that survived
// its regenerations, samples that do not agree, a gate that declined: each
// reaches the caller as the same shape, so that a program that knows how to
// route one review routes them all. It is carried by a ReviewError.
type Review[T any] struct {
	// Candidate is the answer the call was about to return, the value itself,
	// so that whoever handles the review can look at what is being reviewed. It
	// is the zero value of T when there was no candidate: a step that never
	// produced a usable answer has nothing here.
	Candidate T

	// InputRefs describes the inputs that produced, or failed to produce, the
	// candidate, by reference and shape and not by value, as the caller passed
	// them to Approve. Nil when the caller named none, and always nil for a Vote.
	InputRefs []string

	// Evidence is what the call can say in the candidate's favour: rule names,
	// how many samples agreed, what the gate checked. They are findings of the
	// gate or the reconciler, never the caller's data.
	Evidence []string

	// FailedChecks names the checks the candidate did not pass, or the reason
	// that automated recovery gave up.
	FailedChecks []string

	// Attempts is how many samples were spent before the review was raised: the
	// samples of a Vote that succeeded. Zero for Approve, which runs its step once
	// and cannot see what the step did inside.
	Attempts int

	// SuggestedAction is a short hint of what would resolve the review, for a
	// person: "confirm the total". It is a hint, and the library stops here.
	SuggestedAction string
}

// A ReviewError is what an *schemaflux.Error of kind ErrReviewRequired carries
// when Approve or Vote stopped to ask for a person. Reach it with errors.As:
//
//	var review *flow.ReviewError[Invoice]
//	if errors.As(err, &review) {
//		queue(review.Review.Candidate, review.Review.FailedChecks)
//	}
//
// errors.Is(err, schemaflux.ErrReviewRequired) holds for the *Error and for the
// ReviewError itself. Its message names the failed checks and never the
// candidate or the input references, which are the caller's data: every caller
// logs the errors it returns.
type ReviewError[T any] struct {
	// Review is what is to be reviewed.
	Review Review[T]
}

// Error returns "review required", followed by the failed checks when there are
// any.
func (e *ReviewError[T]) Error() string {
	message := "review required"
	if e != nil && len(e.Review.FailedChecks) > 0 {
		message += ": " + strings.Join(e.Review.FailedChecks, ", ")
	}
	return message
}

// Is reports whether target is schemaflux.ErrReviewRequired, so that the
// sentinel is found through a ReviewError wherever it is.
func (e *ReviewError[T]) Is(target error) bool {
	return target == schemaflux.ErrReviewRequired
}

// ApprovalOutcome is what a gate decides about one candidate answer.
type ApprovalOutcome struct {
	// Approved lets the candidate through when true. False stops the call with a
	// review request.
	Approved bool

	// Evidence, FailedChecks and SuggestedAction become the Review of the error
	// when Approved is false, and are ignored when it is true. They are the
	// gate's own findings, never the caller's data.
	Evidence        []string
	FailedChecks    []string
	SuggestedAction string
}

// An ApprovalGate decides whether a candidate answer may be used or needs a
// person first. It receives the context, so that a gate that calls out itself (a
// second model, a policy service) is cancelled the way the step is. A gate runs
// on the caller's goroutine, once, only when the step produced a value.
type ApprovalGate[T any] func(context.Context, T) (ApprovalOutcome, error)

// Approve runs step once and lets gate decide whether the value may be used.
//
//	invoice, err := flow.Approve(ctx, extract, func(ctx context.Context, inv Invoice) (flow.ApprovalOutcome, error) {
//		if inv.Total > 10_000 {
//			return flow.ApprovalOutcome{FailedChecks: []string{"total over the limit"}}, nil
//		}
//		return flow.ApprovalOutcome{Approved: true}, nil
//	}, []string{"invoice 4471"})
//
// What is returned:
//
//   - the gate approved: the value and a nil error.
//   - the gate declined: the zero value and an *schemaflux.Error of kind
//     ErrReviewRequired whose Err is a *ReviewError[T]. Its Review holds the
//     candidate, the input references and what the gate said. A review is a
//     result to route and not a failure to retry.
//   - the step failed with an error that says automated recovery ran out of
//     budget on this candidate (an invalid answer that survived its repairs, a
//     broken invariant, evidence that is not in the source, a schema violation):
//     the same review, with no candidate and the reason in FailedChecks.
//   - the step failed any other way (a configuration error, a refused credential,
//     a cancelled context, a defect of kind ErrInternal): the step's error, the
//     same value it returned. That is a defect or an outage the caller has to fix,
//     and a person cannot decide it. A defect is never a reason to ask for a
//     review, whatever internal rule it broke.
//   - the gate itself failed: the gate's error, the same value it returned. A gate
//     that could not decide has not declined.
//
// inputRefs describes the inputs behind the call by reference and shape, never by
// value (a name, a row id, "invoice, 3 pages"): Approve cannot know what the step
// closed over, and the Review it may return is as safe to log as an error is. Nil
// is allowed.
//
// A rejection is not retried here, on purpose. Whatever automated recovery the
// caller wants (Until, Escalate, a repair loop) belongs inside step, with a budget
// of its own, before Approve is reached; retrying after the gate said no would be
// that loop under another name.
//
// The step and the gate run on the caller's goroutine, so a panic in either is
// the caller's panic. A nil step, a nil gate and a nil ctx are errors of kind
// ErrInvalidRequest, and nothing runs; a ctx that is already done is an error of
// kind ErrCanceled or ErrTimeout. Approve calls no model itself.
func Approve[T any](ctx context.Context, step schemaflux.Step[T], gate ApprovalGate[T], inputRefs []string) (T, error) {
	var zero T
	if err := begin(ctx, "flow.approve"); err != nil {
		return zero, err
	}
	if step == nil {
		return zero, refuse("flow.approve", "there is no step to run")
	}
	if gate == nil {
		return zero, refuse("flow.approve", "there is no gate, and approval is not a default the library chooses")
	}
	value, err := ops.Approve(ctx, ownStep(step), func(ctx context.Context, candidate T) (ops.ApprovalOutcome, error) {
		outcome, err := gate(ctx, candidate)
		if err != nil {
			return ops.ApprovalOutcome{}, own(err)
		}
		return ops.ApprovalOutcome(outcome), nil
	}, inputRefs)
	if err != nil {
		return zero, endReview[T]("flow.approve", err)
	}
	return value, nil
}

// endReview is end for the combinators that can stop for a review: the caller's
// own error as it was given, a review request as an *Error of kind
// ErrReviewRequired over a *ReviewError[T], and any other failure of the
// combinator as an *Error.
func endReview[T any](op string, err error) error {
	if err == nil {
		return nil
	}
	if marked, ok := asCaller(err); ok {
		return marked.err
	}
	var review *types.ReviewRequiredError[T]
	if errors.As(err, &review) {
		return &schemaflux.Error{
			Kind: schemaflux.ErrReviewRequired,
			Op:   op,
			Err:  &ReviewError[T]{Review: Review[T](review.Packet)},
		}
	}
	return bind.ConvertError(op, err)
}
