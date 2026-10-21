package flow

import (
	"context"
	"errors"
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux/v2"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// This file holds what the combinators of the package share about errors. A
// failure of a combinator itself is an *schemaflux.Error. An error returned by
// the caller's own function (a step, a gate, a reconciler, a store, an
// operation over chunks) is returned as it was given, the very same value, and
// the internal combinators, which return their first error as it is, are what
// carries it back. callerError is the mark that tells the two apart.

// callerError is an error of the caller's own function while it travels
// through an internal combinator. It never leaves this package: end takes it
// off before the caller sees anything.
//
// The internal combinators that decide whether a failure is worth another
// attempt (Escalate, Until) or worth a review (Approve) read its kind with
// llm.Classify, which finds the kind of a chain of internal errors and knows
// nothing of an *schemaflux.Error built by a caller or a provider outside the
// module, which carries a Kind and no internal error. For that case Unwrap
// offers the internal error of the same kind beside the caller's error, so that
// what a combinator concludes from a v2 error is what it would conclude from
// the internal error of that kind.
//
// One kind is decided here and not by the internal error beneath. An error of
// kind ErrInternal is a defect, which neither a stronger model nor a person can
// repair, and the internal kind it is filed under (an invariant violation) is
// one that Escalate and Until try again and Approve takes to a person. Unwrap
// puts an error of a terminal kind that is not a candidate for review first, so
// that it is the one the internal combinator reads; the caller's own error is
// still the one returned.
//
// A timeout is not decided here. A step's own ErrTimeout, or a deadline of its
// own, is no signal while the caller's context is live: it is read as an
// unavailable provider is. What stops a combinator is the caller's context
// having ended, which the internal combinators check on the context itself before
// every attempt and every stronger step.
type callerError struct{ err error }

// own marks err as the caller's own. A nil error is nil.
func own(err error) error {
	if err == nil {
		return nil
	}
	return &callerError{err: err}
}

func (e *callerError) Error() string { return e.err.Error() }

func (e *callerError) Unwrap() []error {
	if isDefect(e.err) {
		return []error{&types.OperationError{Kind: types.KindConfiguration, Op: "flow", Message: "not a failure to try again or to review"}, e.err}
	}
	if types.KindOf(e.err) == types.KindUnknown {
		var root *schemaflux.Error
		if errors.As(e.err, &root) && root != nil && root.Kind >= schemaflux.ErrConfiguration && root.Kind <= schemaflux.ErrInternal {
			kind, narrow := bind.InternalKind(uint8(root.Kind))
			return []error{e.err, &types.OperationError{Kind: kind, Narrow: narrow, Op: root.Op}}
		}
	}
	return []error{e.err}
}

// end is the error to return for err, which an internal combinator of the
// operation called op returned: the caller's own error as it was given when
// that is what it is, and otherwise the combinator's own failure as an *Error.
// The chain is searched, because an internal combinator may add words around
// the caller's error (Until does when the last attempt failed). It is nil for a
// nil error.
func end(op string, err error) error {
	if err == nil {
		return nil
	}
	if marked, ok := asCaller(err); ok {
		return marked.err
	}
	return bind.ConvertError(op, err)
}

// asCaller finds the caller's own error in err, which an internal combinator
// returned, and reports whether there is one.
func asCaller(err error) (*callerError, bool) {
	var marked *callerError
	ok := errors.As(err, &marked)
	return marked, ok
}

// begin refuses a nil context and a context that is already done, both before
// anything starts. A done context gives the kind its cause has (ErrCanceled,
// ErrTimeout) with the context's own error beneath it.
func begin(ctx context.Context, op string) error {
	if ctx == nil {
		return &schemaflux.Error{Kind: schemaflux.ErrInvalidRequest, Op: op, Message: "the context is nil"}
	}
	if err := ctx.Err(); err != nil {
		return bind.ConvertError(op, err)
	}
	return nil
}

// refuse is the *Error of kind ErrInvalidRequest for an argument the operation
// called op cannot use. The message is the library's own words and names the
// argument, never its value.
func refuse(op, format string, args ...any) error {
	return &schemaflux.Error{
		Kind:    schemaflux.ErrInvalidRequest,
		Op:      op,
		Message: fmt.Sprintf(format, args...),
	}
}

// misconfigured is the *Error of kind ErrConfiguration for an option the
// operation called op cannot take.
func misconfigured(op, format string, args ...any) error {
	return &schemaflux.Error{
		Kind:    schemaflux.ErrConfiguration,
		Op:      op,
		Message: fmt.Sprintf(format, args...),
	}
}

// isDefect reports whether err is a defect of the library, a program or a
// provider: it holds an *Error of kind ErrInternal.
func isDefect(err error) bool {
	var root *schemaflux.Error
	return errors.As(err, &root) && root != nil && root.Kind == schemaflux.ErrInternal
}
