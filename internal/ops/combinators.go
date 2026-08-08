package ops

import (
	"context"
	"errors"
	"fmt"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// The combinators from M08. Each one is a control-flow shape callers were
// writing by hand around operations, written once here so the fail-open
// mistakes in those hand-rolled versions are made in one place and fixed in
// one place.
//
// They compose Steps rather than wrapping specific operations, so a caller can
// escalate an Extract, retry a Classify, or fall back on their own function
// without this package needing to know which.

// Step is one attempt at producing a value.
//
// It takes the context rather than closing over one, because a combinator that
// runs its step twice with a context captured at construction cannot be
// cancelled between attempts -- which is the whole point of a retry loop
// having a deadline.
type Step[T any] func(context.Context) (T, error)

// EscalationRecord says what actually happened, because "it worked" and "it
// worked the second time, on the expensive model" are different facts and a
// caller tuning cost needs to tell them apart.
type EscalationRecord struct {
	// Escalated reports whether the stronger step ran.
	Escalated bool

	// Reason names why, in the library's own vocabulary: "" when the first
	// step was accepted, "error" when it failed, "rejected" when it succeeded
	// and the caller's accept function turned the answer down.
	Reason string

	// FirstError is the first step's failure, kept so a caller can see what
	// the cheap route said even when the expensive one succeeded. It is not
	// wrapped into the returned error on success: the call succeeded, and
	// returning an error alongside a good answer is how callers end up
	// checking the wrong thing.
	FirstError error
}

// Escalate runs first, and runs stronger only if it has to.
//
// "Has to" means one of two things, and they are deliberately different: first
// returned an error that a different route could plausibly survive, or first
// succeeded and accept said the answer was not good enough. A nil accept means
// any successful answer is accepted, which makes Escalate a pure failure
// route.
//
// A terminal failure does not escalate. A malformed request, a policy refusal,
// or an exhausted budget fails identically on a stronger model, so escalating
// buys nothing and spends real money to buy it -- the disposition comes from
// types.OperationError, the same one every other layer uses, rather than a
// second opinion about what is worth retrying.
func Escalate[T any](ctx context.Context, first, stronger Step[T], accept func(T) bool) (T, EscalationRecord, error) {
	var zero T

	if first == nil {
		return zero, EscalationRecord{}, errors.New("escalate: no first step to run")
	}
	if stronger == nil {
		return zero, EscalationRecord{}, errors.New("escalate: no stronger step to escalate to")
	}
	if err := ctx.Err(); err != nil {
		return zero, EscalationRecord{}, err
	}

	value, err := first(ctx)

	switch {
	case err != nil:
		if isTerminal(err) {
			// Returned as-is. Wrapping it in "escalation failed" would bury
			// the classification every caller branches on.
			return zero, EscalationRecord{FirstError: err}, err
		}
		record := EscalationRecord{Escalated: true, Reason: "error", FirstError: err}
		return runStronger(ctx, stronger, record)

	case accept == nil || accept(value):
		return value, EscalationRecord{}, nil

	default:
		record := EscalationRecord{Escalated: true, Reason: "rejected"}
		return runStronger(ctx, stronger, record)
	}
}

func runStronger[T any](ctx context.Context, stronger Step[T], record EscalationRecord) (T, EscalationRecord, error) {
	if err := ctx.Err(); err != nil {
		var zero T
		return zero, record, err
	}

	value, err := stronger(ctx)
	if err != nil {
		var zero T
		if record.FirstError != nil {
			// Both routes failed, and the caller needs both: the first
			// explains why it escalated, the second why that did not help.
			return zero, record, fmt.Errorf("escalated after %w, and the stronger route also failed: %w",
				record.FirstError, err)
		}
		return zero, record, err
	}
	return value, record, nil
}

// Fallback runs primary, and alternate if primary fails.
//
// It is Escalate with no accept function, and it is written that way on
// purpose rather than as a second implementation: two functions that decide
// separately whether an error is worth retrying eventually disagree, and the
// disagreement only shows up under the failure they were both written for.
func Fallback[T any](ctx context.Context, primary, alternate Step[T]) (T, error) {
	value, _, err := Escalate(ctx, primary, alternate, nil)
	return value, err
}

// Until runs step until the answer satisfies pred, or the attempts run out.
//
// Running out is an error. The obvious alternative -- return the last answer
// with no error and let the caller decide -- is the fail-open this list exists
// to remove: the last answer is by definition one that failed the predicate,
// and handing it back as a success means the caller who wrote the predicate is
// the one who does not find out it was violated.
//
// The value is still returned alongside the error, so a caller who wants to
// inspect the rejected answer can, and a caller who checks err first cannot
// use it by accident.
func Until[T any](ctx context.Context, step Step[T], pred func(T) bool, max int) (T, int, error) {
	var zero T

	if step == nil {
		return zero, 0, errors.New("until: no step to run")
	}
	if pred == nil {
		// A nil predicate would make the first answer satisfy the condition,
		// which turns Until into a plain call and silently drops the check the
		// caller asked for.
		return zero, 0, errors.New("until: no predicate, so there is no condition to run until")
	}
	if max <= 0 {
		return zero, 0, fmt.Errorf("until: %d attempts is not a number of attempts", max)
	}

	var last T
	for attempt := 1; attempt <= max; attempt++ {
		if err := ctx.Err(); err != nil {
			return last, attempt - 1, err
		}

		value, err := step(ctx)
		if err != nil {
			// A failure is not a rejected answer. Retrying a terminal failure
			// spends the whole budget on a call that cannot succeed.
			if isTerminal(err) {
				return zero, attempt, err
			}
			last = zero
			if attempt == max {
				return zero, attempt, fmt.Errorf("until: %d attempts, the last one failed: %w", attempt, err)
			}
			continue
		}

		last = value
		if pred(value) {
			return value, attempt, nil
		}
	}

	return last, max, &types.OperationError{
		Kind: types.KindRepairExhausted,
		Op:   "until",
		Message: fmt.Sprintf(
			"the condition was not satisfied in %d attempts", max),
	}
}

// isTerminal asks the shared taxonomy whether a failure is worth trying again
// by another route. An unclassifiable error is treated as non-terminal, so an
// error this library has never seen gets the second attempt rather than being
// silently ruled out by a classifier that did not recognise it.
func isTerminal(err error) bool {
	if err == nil {
		return false
	}
	return (&types.OperationError{Kind: llm.Classify(err)}).Terminal()
}
