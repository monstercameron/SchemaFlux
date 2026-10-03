package flow

import (
	"context"

	schemaflux "github.com/monstercameron/schemaflux/v2"

	"github.com/monstercameron/schemaflux/internal/ops"
)

// EscalationRecord says what Escalate did, because "it worked" and "it worked
// the second time, on the expensive model" are different facts and a caller
// tuning cost needs to tell them apart.
type EscalationRecord struct {
	// Escalated reports whether the stronger step ran. False means the first
	// step's answer was the result, or the first step failed in a way that is
	// not worth a second try and its error was returned, or the context ended
	// between the two steps and the stronger one was not run.
	Escalated bool

	// Reason says why the stronger step was called for, in the library's own
	// words: "error" when the first step failed and "rejected" when it succeeded
	// and accept turned its answer down. It is empty when the first step was
	// accepted or failed terminally.
	Reason string

	// FirstError is the first step's failure, as the step returned it, kept so
	// that a caller can see what the cheap route said even when the stronger one
	// succeeded. It is not part of the returned error on success: the call
	// succeeded. It is nil when the first step did not fail.
	FirstError error
}

// Escalate runs first, and runs stronger only if it has to.
//
//	invoice, record, err := flow.Escalate(ctx, cheap, strong, func(inv Invoice) bool {
//		return inv.Total > 0
//	})
//
// "Has to" means one of two things, and they are deliberately different: first
// returned an error that a different route could plausibly survive, or first
// succeeded and accept said the answer was not good enough. A nil accept means
// any successful answer is accepted, which makes Escalate a pure failure route.
// accept runs on the caller's goroutine.
//
// A terminal failure does not escalate. A malformed request, a policy refusal, a
// refused credential, an exhausted budget, a configuration error, a cancelled
// context and a review request fail the same way on a stronger model, so
// escalating buys nothing and spends real money to buy it. A defect, an error of
// kind ErrInternal, is not a failure a stronger model can repair. A step's own
// timeout is no signal while the caller's context is live, and is escalated as an
// unavailable provider is; the caller's context having ended is what stops it. The first
// step's error is then the error returned, the same value the step returned, and
// EscalationRecord.FirstError holds it too. The package comment has the table of
// what is done for each kind. An error the library does not
// recognise is not terminal: it gets the second attempt. What counts is the
// kind an *Error carries, or, for any other error, what the library can read of
// it.
//
// What is returned:
//
//   - first succeeded and was accepted: its value, a record that did not
//     escalate, and a nil error.
//   - stronger ran and succeeded: its value, and a record that says why it ran.
//   - stronger ran and failed: its error, the same value stronger returned, and
//     the record, whose FirstError is the first step's error when that was why
//     stronger ran. Both failures are in hand: the one returned and the one in the
//     record.
//
// No value is returned beside an error. Both steps run on the caller's
// goroutine, one after the other, never at once, so a panic in one is the
// caller's panic. The context is checked before each step: one that is done is
// an *Error of kind ErrCanceled or ErrTimeout, with the context's own error
// beneath it, and the step does not run. A nil step is an *Error of kind
// ErrInvalidRequest, as is a nil ctx, and nothing runs. Escalate calls no model
// itself.
func Escalate[T any](ctx context.Context, first, stronger schemaflux.Step[T], accept func(T) bool) (T, EscalationRecord, error) {
	var zero T
	if err := begin(ctx, "flow.escalate"); err != nil {
		return zero, EscalationRecord{}, err
	}
	if first == nil {
		return zero, EscalationRecord{}, refuse("flow.escalate", "there is no first step to run")
	}
	if stronger == nil {
		return zero, EscalationRecord{}, refuse("flow.escalate", "there is no stronger step to escalate to")
	}

	// The internal combinator wraps both errors into one when both steps fail,
	// and the caller is owed the stronger step's, as it was returned. Each
	// step's error is kept as it was given.
	var firstErr, strongerErr error
	var ranStronger bool
	value, inner, err := ops.Escalate(ctx,
		func(ctx context.Context) (T, error) {
			value, err := first(ctx)
			if err != nil {
				firstErr = err
				return value, own(err)
			}
			return value, nil
		},
		func(ctx context.Context) (T, error) {
			ranStronger = true
			value, err := stronger(ctx)
			if err != nil {
				strongerErr = err
				return value, own(err)
			}
			return value, nil
		}, accept)

	// The internal record says the stronger step was called for; whether it ran
	// is what Escalated reports, and a context that ended in between is a case
	// where it was called for and did not.
	record := EscalationRecord{Escalated: ranStronger, Reason: inner.Reason}
	if inner.FirstError != nil {
		record.FirstError = firstErr
	}
	if err == nil {
		return value, record, nil
	}
	if strongerErr != nil {
		return zero, record, strongerErr
	}
	return zero, record, end("flow.escalate", err)
}

// maxAttempts is the most attempts one Until makes. A number that is read or
// computed, math.MaxInt for "as long as it takes", is refused and not run as a
// loop that ends when the money does.
const maxAttempts = 1000

// Until runs step until the answer satisfies pred, or the attempts run out, and
// returns the answer and the number of attempts that were made.
//
//	draft, attempts, err := flow.Until(ctx, write, func(d Draft) bool { return d.Words <= 200 }, 3)
//
// Running out is an error: an *Error of kind ErrInvalidAnswer, and attempts is the
// number that were made. The obvious alternative, returning the last answer with
// no error and letting the caller decide, is the fail-open this function exists to
// remove: the last answer is by definition one that failed the predicate, and
// handing it back as a success means the caller who wrote the predicate is the one
// who does not find out it was violated. A caller who wants to look at the
// rejected answers records them in step.
//
// The step is run on the caller's goroutine, one attempt after another. A failure
// is not a rejected answer: a step that fails with an error that is terminal (a
// malformed request, a refusal, an exhausted budget, a cancelled context, a
// defect of kind ErrInternal) ends the call on that attempt and its
// error is returned, the same value the step returned; retrying it would spend
// every attempt on a call that cannot succeed. A step's own timeout is no signal while
// the caller's context is live and is tried again; the context having ended stops
// the call. The package comment has the table
// of what is done for each kind.
// A failure that is not terminal uses up its attempt and the next one is made. If
// the last attempt fails, its error is the one returned, the same value the step
// returned, and attempts equals the number given.
//
// Like every function here, Until returns the zero value beside an error: the
// rejected answer is not handed back, since a caller who checks the error first
// and one who does not would both be holding a value that failed the predicate.
// pred runs on the caller's goroutine.
//
// The context is checked before every attempt: a done one is an *Error of kind
// ErrCanceled or ErrTimeout, with attempts the number made so far. A nil step, a
// nil pred, an attempts below 1 or above 1000 and a nil ctx are errors of kind
// ErrInvalidRequest and nothing runs: a call repeated without a bound spends
// without one, and a caller who wants it bounded by time passes a context that
// ends. Until calls no model itself.
func Until[T any](ctx context.Context, step schemaflux.Step[T], pred func(T) bool, attempts int) (T, int, error) {
	var zero T
	if err := begin(ctx, "flow.until"); err != nil {
		return zero, 0, err
	}
	if step == nil {
		return zero, 0, refuse("flow.until", "there is no step to run")
	}
	if pred == nil {
		// A nil predicate would make the first answer satisfy the condition and
		// turn Until into a plain call, dropping the check the caller asked for.
		return zero, 0, refuse("flow.until", "there is no predicate, so there is no condition to run until")
	}
	if attempts < 1 || attempts > maxAttempts {
		return zero, 0, refuse("flow.until", "the number of attempts must be from 1 to %d", maxAttempts)
	}

	value, made, err := ops.Until(ctx, func(ctx context.Context) (T, error) {
		value, err := step(ctx)
		if err != nil {
			return value, own(err)
		}
		return value, nil
	}, pred, attempts)
	if err != nil {
		return zero, made, end("flow.until", err)
	}
	return value, made, nil
}
