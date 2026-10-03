package ops

import (
	"context"
	"fmt"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// CF-006. Approval is one use of a more general terminal outcome: automated
// recovery stops -- with types.ErrReviewRequired and a types.ReviewPacket --
// when evidence is contradictory, an invariant survives its budgeted
// regenerations, eligible providers disagree materially, or policy requires a
// human. Vote (CF-002) implements the "providers disagree" case directly,
// because it already has the samples and the reconciler's judgement about
// them. Approve is the general case: it is what a caller reaches for when the
// candidate needs a human's decision rather than another automated attempt,
// and it is also where a step's own exhausted-regeneration failure (Until
// returning KindRepairExhausted, a repair loop giving up with
// KindInvariantViolation or KindEvidenceViolation) gets turned into the same
// review shape instead of a bare error the caller cannot route anywhere.
//
// This is a successful safety outcome, not a failure. The alternative --
// looping the model until it says something acceptable -- is the thing this
// exists to replace, and the type says so: ErrReviewRequired is terminal
// (types.OperationError.Terminal(), and ReviewRequiredError.Is reaches the
// same sentinel), which tells a caller to stop and route, not to retry.
//
// The library supplies the structure (ReviewPacket) and the callback (the
// gate). It does not build a queue, a UI, or an approval workflow -- what
// happens to a ReviewPacket after Approve returns it is entirely the caller's.

// ApprovalOutcome is what a gate decides about one candidate answer.
type ApprovalOutcome struct {
	// Approved lets the candidate through when true.
	Approved bool

	// Evidence, FailedChecks, and SuggestedAction feed the ReviewPacket when
	// Approved is false. They are the gate's own findings -- rule names, which
	// providers disagreed, which invariant failed -- never the caller's data.
	Evidence        []string
	FailedChecks    []string
	SuggestedAction string
}

// ApprovalGate decides whether a candidate needs a human before it is used.
// It receives the context so a gate that itself calls out (a second model, a
// policy service) can be cancelled the same way step can.
type ApprovalGate[T any] func(context.Context, T) (ApprovalOutcome, error)

// Approve runs step once. If it produces a value, gate decides whether that
// value may be used; if gate declines, or if step itself fails with a kind
// that names an exhausted, budgeted attempt at automated recovery, Approve
// returns a types.ReviewPacket behind types.ErrReviewRequired instead of the
// value.
//
// inputRefs describes the inputs behind this call -- by reference and shape,
// via types.DescribeValue, never the values themselves -- because Approve has
// no way to know what step closed over and the payload rule (AGENTS.md: never
// log or embed the caller's payload) applies to what goes in a ReviewPacket
// exactly as it applies to an error. A caller with nothing worth naming may
// pass nil.
//
// The packet's Attempts is left zero: it counts the regenerations or samples
// spent before the candidate, and Approve runs step once and cannot see what
// step did inside. It used to say 1 whatever had happened.
//
// Rejection is not retried here, on purpose: whatever automated recovery a
// caller wanted (Until, Escalate, a repair loop) belongs inside step, with its
// own budget, before Approve is reached. Retrying after the caller's own gate
// says no would just be that same loop wearing Approve's name, which is
// exactly the failure mode this task exists to replace with a stop.
func Approve[T any](ctx context.Context, step Step[T], gate ApprovalGate[T], inputRefs []string) (T, error) {
	var zero T

	if step == nil {
		return zero, types.Newf(types.KindInvalidRequest, "approve", "no step to run")
	}
	if gate == nil {
		return zero, types.Newf(types.KindInvalidRequest, "approve", "no gate; approval is not a default this package chooses")
	}
	if err := ctx.Err(); err != nil {
		return zero, classifyBlockingErr(err)
	}

	value, stepErr := step(ctx)
	if stepErr != nil {
		if reviewable, reason := reviewableFailure(stepErr); reviewable {
			packet := types.ReviewPacket[T]{
				InputRefs:       inputRefs,
				FailedChecks:    []string{reason},
				SuggestedAction: "automated recovery for this step was exhausted; a human should decide the next step",
			}
			return zero, types.NewReviewRequired(packet)
		}
		// Anything else -- configuration, authentication, a cancelled
		// context -- is a bug or an operational problem, not a candidate that
		// needs a human's judgement about content, so it is returned as-is
		// rather than repackaged into a review.
		return zero, stepErr
	}

	outcome, gateErr := gate(ctx, value)
	if gateErr != nil {
		return zero, fmt.Errorf("approve: gate failed: %w", gateErr)
	}
	if outcome.Approved {
		return value, nil
	}

	packet := types.ReviewPacket[T]{
		Candidate:       value,
		InputRefs:       inputRefs,
		Evidence:        outcome.Evidence,
		FailedChecks:    outcome.FailedChecks,
		SuggestedAction: outcome.SuggestedAction,
	}
	return zero, types.NewReviewRequired(packet)
}

// reviewableFailure reports whether a step's failure is the kind that means
// "automated recovery ran out of budget on this candidate" rather than "this
// call could not be made at all". The first belongs in front of a human next;
// the second is an error the caller has to fix before trying again, and
// wrapping it in a review packet would tell a human to look at something no
// human can act on.
func reviewableFailure(err error) (bool, string) {
	kind := llm.Classify(err)
	switch kind {
	case types.KindRepairExhausted:
		return true, "the step failed with repair exhausted"
	case types.KindInvariantViolation:
		return true, "the step failed with an invariant violation"
	case types.KindEvidenceViolation:
		return true, "the step failed with an evidence violation"
	case types.KindSchemaViolation:
		return true, "the step failed with a schema violation"
	default:
		return false, ""
	}
}
