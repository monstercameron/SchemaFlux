package schemaflux

import (
	"context"
	"errors"
	"fmt"

	"github.com/monstercameron/schemaflux/internal/ops"
)

// The functions of this file are the control flow of a program whose decisions
// are made by the library's operations: a step, a branch, a switch, steps run at
// once and a step run over a list. None of them asks a model, so none adds a
// Record. Each of them is written over the combinator of its own name in
// internal/ops, which is where what runs at once is decided; what is added here
// is the part that belongs to the root: a failure of the combinator itself is an
// *Error, and an error a caller's step returned is handed back as it was given.
//
// The steps of a combinator that runs them on goroutines of its own (Gather and
// Each) are wrapped so that what they return is told apart from what the
// combinator itself reports. flowCallerError is the mark that carries a step's
// error through the internal combinator, and flowEnd takes it off again before
// the caller sees it.

// Step is one piece of a flow: a function that produces a value or fails. It
// takes the context rather than closing over one, so that a combinator that
// runs it can cancel it, and so that the client placed on the context with
// WithClient is the one the step's own operations use.
//
// A step is an ordinary function, usually a closure over what it needs:
//
//	summarize := func(ctx context.Context) (string, error) {
//		return schemaflux.Summarize(ctx, article)
//	}
type Step[T any] func(context.Context) (T, error)

// Route is one way a program can go: a label and the step that runs when the
// label is the one chosen. Build one with To.
type Route[T any] struct {
	// Label names the route. Select compares it exactly with the label it is
	// given. It must not be blank, and no two routes of one call may have the
	// same label once the space around it is trimmed.
	Label string

	// Run is what happens when the route is chosen. It must not be nil.
	Run Step[T]
}

// To builds a Route: the step run when the label is the one chosen.
//
//	schemaflux.To("billing", handleBilling)
//
// It asks no model and takes no context.
func To[T any](label string, run Step[T]) Route[T] {
	return Route[T]{Label: label, Run: run}
}

// Branch runs then when yes is true and otherwise when it is not, and returns
// what the one it ran returned. It is an if-else whose condition was decided
// somewhere else: by Is, or as a field of what Assess returned, or by code of
// your own. The step that was not chosen is not called.
//
//	reply, err := schemaflux.Branch(ctx, triage.Spam, discard, handle)
//
// Both steps are required, the one that is not chosen as well: a nil step is an
// *Error of kind ErrInvalidRequest and neither is called, since a missing
// answer is never a zero value. A caller who wants "otherwise nothing" writes
// the step that returns the zero value.
//
// The step runs on the caller's goroutine, under ctx, so a panic in it is the
// caller's panic, as it would be from an ordinary call. An error it returns is
// returned as it is, the same value. Nothing is returned beside an error.
//
// Branch calls no model. It returns an *Error of kind ErrInvalidRequest for a
// nil ctx and of kind ErrCanceled or ErrTimeout, with the context's own error
// beneath it, for a ctx that is already done; in both cases neither step is
// called.
func Branch[T any](ctx context.Context, yes bool, then, otherwise Step[T]) (T, error) {
	var zero T
	if err := flowBegin(ctx, "branch"); err != nil {
		return zero, err
	}
	if then == nil {
		return zero, &Error{Kind: ErrInvalidRequest, Op: "branch", Message: "there is no step for when the condition is true"}
	}
	if otherwise == nil {
		return zero, &Error{Kind: ErrInvalidRequest, Op: "branch", Message: "there is no step for when the condition is false"}
	}
	value, err := ops.Branch(ctx, yes, ops.Step[T](then), ops.Step[T](otherwise))
	if err != nil {
		return zero, err
	}
	return value, nil
}

// Select runs the route whose label is the one given and returns what it
// returned. It is a switch whose value was decided somewhere else: by Which, or
// as a field of what Assess returned.
//
//	reply, err := schemaflux.Select(ctx, triage.Team,
//		schemaflux.To("billing", handleBilling),
//		schemaflux.To("technical", handleTechnical))
//
// The label is compared exactly. A label that is none of the routes' is an
// error of kind ErrInvalidRequest and nothing runs: nothing here is a default,
// since a value the program did not expect is not one it knows what to do with.
// There must be at least one route, no label may be blank or repeated (once the
// space around it is trimmed), and no route may have a nil Run; a call that
// breaks one of these is an error of kind ErrInvalidRequest before any route
// runs, whichever route the label names.
//
// The route runs on the caller's goroutine, under ctx. An error it returns is
// returned as it is, the same value. Nothing is returned beside an error. Select
// calls no model. A nil ctx is an *Error of kind ErrInvalidRequest, and a ctx
// that is already done is one of kind ErrCanceled or ErrTimeout; no route runs.
func Select[T any](ctx context.Context, label string, routes ...Route[T]) (T, error) {
	var zero T
	if err := flowBegin(ctx, "select"); err != nil {
		return zero, err
	}
	chosen := make([]ops.Route[T], len(routes))
	known := false
	for i, route := range routes {
		chosen[i] = ops.Route[T]{Label: route.Label, Run: ops.Step[T](route.Run)}
		known = known || route.Label == label
	}
	if fault := ops.RouteFault(chosen); fault != "" {
		return zero, &Error{Kind: ErrInvalidRequest, Op: "select", Message: fault}
	}
	if !known {
		return zero, &Error{
			Kind:    ErrInvalidRequest,
			Op:      "select",
			Message: fmt.Sprintf("the label is not the label of any of the %d routes", len(routes)),
		}
	}
	value, err := ops.Select(ctx, label, chosen...)
	if err != nil {
		return zero, err
	}
	return value, nil
}

// Whether and RouteBy are not written yet. They are the one-line forms of the
// two commonest decisions, and each is its two halves exactly: v2's own Is
// followed by Branch, and v2's own Which followed by Select. They wait for Is and
// Which, which are the decision operations and are written by another lane. They
// add the Record of the decision, and Branch and Select add none. Their
// declarations, as they are specified:
//
//	// Whether asks a yes-or-no question about a subject and runs then when the
//	// answer is yes and otherwise when it is no. When the question cannot be
//	// answered neither runs: a failure is never read as a no. It is Is followed
//	// by Branch. Default tier Fast.
//	func Whether[T any](ctx context.Context, subject any, question string, then, otherwise Step[T], opts ...Options) (T, error)
//
//	// RouteBy asks which of the routes' labels answers a question about a
//	// subject and runs that route. When the question cannot be answered no route
//	// runs: a failure is never read as the first route. It is Which followed by
//	// Select. There have to be at least two routes, no label blank or repeated,
//	// and no nil Run; a call that breaks one is an error of kind ErrInvalidRequest
//	// before anything is asked. Default tier Fast.
//	func RouteBy[T any](ctx context.Context, subject any, question string, routes []Route[T], opts ...Options) (T, error)

// Gather runs steps that do not depend on each other at once and returns their
// results in the order the steps were given. A program that needs three answers
// waits as long as the slowest of them takes, where one after another it waits
// for all three.
//
//	results, err := schemaflux.Gather(ctx, summarize, translate, tag)
//
// Every step runs at once on a goroutine of its own, however many there are, so
// pass as many as the caller can afford to have in flight. The steps run under
// ctx: the calls they make are bounded by the Scheduler of the client on it,
// which is what bounds how many requests are with the provider at once, and the
// client's budget is still charged for each.
//
// The first step to fail cancels the context the others run under, and its
// error is the one returned, the same value the step returned; the errors of the
// steps that were cancelled because of it are not returned. No results are
// returned with an error. Cancelling is a request: Gather returns when every
// step has returned, so a step that does not look at its context holds the
// return until it finishes. No goroutine outlives the call.
//
// A step that panics, or ends its goroutine with runtime.Goexit as t.FailNow
// does, is an *Error of kind ErrInternal with nothing of the panic in it. Every
// step is checked before the first one starts: a nil step is an *Error of kind
// ErrInvalidRequest and starts none. A nil ctx is one of the same kind, and a
// ctx that is already done is one of kind ErrCanceled or ErrTimeout; no step
// starts. For no steps the result is empty and not nil.
//
// Gather calls no model itself.
func Gather[T any](ctx context.Context, steps ...Step[T]) ([]T, error) {
	if err := flowBegin(ctx, "gather"); err != nil {
		return nil, err
	}
	own := make([]ops.Step[T], len(steps))
	for i, step := range steps {
		if step == nil {
			return nil, flowNilStep("gather", i)
		}
		own[i] = flowOwn(step)
	}
	results, err := ops.Gather(ctx, own...)
	if err != nil {
		return nil, flowEnd("gather", err)
	}
	return results, nil
}

// Gather2 is Gather for two steps whose results are of different types.
//
//	summary, tags, err := schemaflux.Gather2(ctx, summarize, tag)
//
// It follows Gather's rules in every respect: both steps run at once, the first
// to fail cancels the other and its error is returned unchanged, and nothing is
// returned beside an error.
func Gather2[A, B any](ctx context.Context, first Step[A], second Step[B]) (A, B, error) {
	var (
		noA A
		noB B
	)
	if err := flowBegin(ctx, "gather"); err != nil {
		return noA, noB, err
	}
	if err := flowRefuseNil("gather", first == nil, second == nil); err != nil {
		return noA, noB, err
	}
	a, b, err := ops.Gather2(ctx, flowOwn(first), flowOwn(second))
	if err != nil {
		return noA, noB, flowEnd("gather", err)
	}
	return a, b, nil
}

// Gather3 is Gather for three steps whose results are of different types, with
// Gather's rules.
func Gather3[A, B, C any](ctx context.Context, first Step[A], second Step[B], third Step[C]) (A, B, C, error) {
	var (
		noA A
		noB B
		noC C
	)
	if err := flowBegin(ctx, "gather"); err != nil {
		return noA, noB, noC, err
	}
	if err := flowRefuseNil("gather", first == nil, second == nil, third == nil); err != nil {
		return noA, noB, noC, err
	}
	a, b, c, err := ops.Gather3(ctx, flowOwn(first), flowOwn(second), flowOwn(third))
	if err != nil {
		return noA, noB, noC, flowEnd("gather", err)
	}
	return a, b, c, nil
}

// Gather4 is Gather for four steps whose results are of different types, with
// Gather's rules. Past four, Gather over steps of one type or Each over a list
// is the form.
func Gather4[A, B, C, D any](ctx context.Context, first Step[A], second Step[B], third Step[C], fourth Step[D]) (A, B, C, D, error) {
	var (
		noA A
		noB B
		noC C
		noD D
	)
	if err := flowBegin(ctx, "gather"); err != nil {
		return noA, noB, noC, noD, err
	}
	if err := flowRefuseNil("gather", first == nil, second == nil, third == nil, fourth == nil); err != nil {
		return noA, noB, noC, noD, err
	}
	a, b, c, d, err := ops.Gather4(ctx, flowOwn(first), flowOwn(second), flowOwn(third), flowOwn(fourth))
	if err != nil {
		return noA, noB, noC, noD, flowEnd("gather", err)
	}
	return a, b, c, d, nil
}

// Each runs a step for every item of a list, up to 64 at once, and returns the
// results in the order of the items. It is how an operation is run over many
// items:
//
//	triaged, err := schemaflux.Each(ctx, tickets, func(ctx context.Context, t Ticket) (Triage, error) {
//		return schemaflux.Assess[Triage](ctx, t.Body)
//	})
//
// At most 64 steps are running at any moment, however long the list is: that
// bounds the goroutines. What bounds the requests is the Scheduler of the client
// on ctx, which holds back the calls the steps make; to hold them to fewer than
// 64, set one on a copy of the client and pass the copy with WithClient.
//
// The first step to fail stops the rest: no item after it starts, the context
// the running ones hold is cancelled, and its error is returned, the same value
// the step returned. No results are returned with an error. Stopping is a
// request: Each returns when every step that started has returned, and no
// goroutine outlives the call.
//
// To work through every item whatever fails, have the step return its failure
// inside its result and a nil error, and look at each result afterwards:
//
//	type outcome struct {
//		triage Triage
//		err    error
//	}
//	outcomes, _ := schemaflux.Each(ctx, tickets, func(ctx context.Context, t Ticket) (outcome, error) {
//		triage, err := schemaflux.Assess[Triage](ctx, t.Body)
//		return outcome{triage, err}, nil
//	})
//
// A step that panics, or ends its goroutine with runtime.Goexit, is an *Error of
// kind ErrInternal with nothing of the panic in it. A nil step is an *Error of
// kind ErrInvalidRequest, as is a nil ctx; a ctx that is already done is one of
// kind ErrCanceled or ErrTimeout, and no step starts. For no items the result is
// empty and not nil. Each calls no model itself.
func Each[T, R any](ctx context.Context, items []T, step func(context.Context, T) (R, error)) ([]R, error) {
	if err := flowBegin(ctx, "each"); err != nil {
		return nil, err
	}
	if step == nil {
		return nil, &Error{Kind: ErrInvalidRequest, Op: "each", Message: "there is no step to run"}
	}
	results, err := ops.Each(ctx, items, func(ctx context.Context, item T) (R, error) {
		value, err := step(ctx, item)
		if err != nil {
			return value, &flowCallerError{err: err}
		}
		return value, nil
	})
	if err != nil {
		return nil, flowEnd("each", err)
	}
	return results, nil
}

// flowCallerError is the error of a step of the caller's while it travels
// through an internal combinator, which returns the first error it is given
// as it is. It is how the root tells that error from one the combinator made
// itself: the first is handed back to the caller as it was given, the second is
// converted. It never leaves this file.
type flowCallerError struct{ err error }

func (e *flowCallerError) Error() string { return e.err.Error() }
func (e *flowCallerError) Unwrap() error { return e.err }

// flowOwn is step as the internal combinator's step, with what it returns as an
// error marked as the caller's. A nil step stays nil.
func flowOwn[T any](step Step[T]) ops.Step[T] {
	if step == nil {
		return nil
	}
	return func(ctx context.Context) (T, error) {
		value, err := step(ctx)
		if err != nil {
			return value, &flowCallerError{err: err}
		}
		return value, nil
	}
}

// flowEnd is the error to return for err, which an internal combinator of the
// operation called op returned: the caller's own step error as it was given, or
// the combinator's own failure as an *Error. The chain is searched, as the flow
// package's end searches it, because a combinator may add words around the
// caller's error. It is nil for a nil error.
func flowEnd(op string, err error) error {
	if err == nil {
		return nil
	}
	var marked *flowCallerError
	if errors.As(err, &marked) {
		return marked.err
	}
	return convertError(op, err)
}

// flowBegin refuses a nil context and a context that is already done, both
// before anything starts, with an *Error. A done context gives the kind its
// cause has (ErrCanceled, ErrTimeout) with the context's own error beneath it,
// so errors.Is(err, context.Canceled) and context.DeadlineExceeded hold.
func flowBegin(ctx context.Context, op string) error {
	if ctx == nil {
		return &Error{Kind: ErrInvalidRequest, Op: op, Message: "the context is nil"}
	}
	if err := ctx.Err(); err != nil {
		return convertError(op, err)
	}
	return nil
}

// flowRefuseNil is the error for the first of the steps that is nil, given as
// whether each one is, and nil when none is. It is called before any step starts.
func flowRefuseNil(op string, isNil ...bool) error {
	for i, missing := range isNil {
		if missing {
			return flowNilStep(op, i)
		}
	}
	return nil
}

// flowNilStep is the error for the step at index i, counting from zero, being
// nil.
func flowNilStep(op string, i int) error {
	return &Error{Kind: ErrInvalidRequest, Op: op, Message: fmt.Sprintf("step %d is nil", i+1)}
}
