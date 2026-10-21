package schemaflux

import (
	"context"

	"github.com/monstercameron/schemaflux/v2/internal/ops"
)

// Whether and RouteBy are the one-line forms of the two commonest decisions:
// each is its two halves exactly. Whether is Is followed by Branch, and RouteBy is
// Which followed by Select. They are written here, over the exported functions,
// and not over the internal combinators of the same names: the decision is the
// root's own Is or Which, so it takes the same path they do (a decision provider
// when the client has one in front, a floor, the recorder) and adds the Record
// they add, before the step runs. Neither adds a Record of its own.
//
// A failure of the combinator itself (a nil step, a set of routes that cannot be
// chosen between, a nil context) is an *Error of kind ErrInvalidRequest named for
// Whether or RouteBy, found before anything is asked, so nothing is paid for and
// no Record is added. The step's own error is returned as the step returned it.

// Whether asks a yes-or-no question about a subject and runs then when the
// answer is yes and otherwise when it is no. When the question cannot be answered
// neither runs: a failure is never read as a no. It is Is followed by Branch.
// Default tier Fast.
//
//	reply, err := schemaflux.Whether(ctx, message, "Is this message spam?", discard, handle)
//
// The question is asked as Is asks it, with the same options, and adds Is's
// Record ("is") when the answer is given or refused, before the chosen step runs.
// A failure to answer, including one of Is's checks (a blank question, a bad
// option), is returned as Is returns it, and neither step runs.
//
// A nil step is an error of kind ErrInvalidRequest, whichever step the answer
// would choose, and the model is not asked. The chosen step runs once, on the
// caller's goroutine, under ctx, so a panic in it is the caller's panic; an
// error it returns is returned as it is, the same value, and the Record of the
// decision does not carry it. Nothing is returned beside an error. A nil ctx is
// an error of kind ErrInvalidRequest.
func Whether[T any](ctx context.Context, subject any, question string, then, otherwise Step[T], opts ...Options) (T, error) {
	var zero T
	if ctx == nil {
		return zero, &Error{Kind: ErrInvalidRequest, Op: "whether", Message: "the context is nil"}
	}
	if then == nil {
		return zero, &Error{Kind: ErrInvalidRequest, Op: "whether", Message: "then is nil"}
	}
	if otherwise == nil {
		return zero, &Error{Kind: ErrInvalidRequest, Op: "whether", Message: "otherwise is nil"}
	}
	yes, err := Is(ctx, subject, question, opts...)
	if err != nil {
		return zero, err
	}
	return Branch(ctx, yes, then, otherwise)
}

// RouteBy asks which of the routes' labels answers a question about a subject
// and runs that route. When the question cannot be answered no route runs: a
// failure is never read as the first route. It is Which followed by Select.
// Default tier Fast.
//
//	reply, err := schemaflux.RouteBy(ctx, message, "Which team should handle this?",
//		[]schemaflux.Route[Reply]{
//			schemaflux.To("billing", handleBilling),
//			schemaflux.To("technical", handleTechnical),
//		})
//
// There have to be at least two routes, no label may be blank or repeated (once
// the space around it is trimmed), and no route may have a nil Run: a call that
// breaks one is an error of kind ErrInvalidRequest before anything is asked. The
// labels are the question's options, as Which takes them, and the route run is
// the one whose label the answer is, compared exactly.
//
// The question is asked as Which asks it, with the same options, and adds
// Which's Record ("which") when the answer is given or refused, before the chosen
// route runs. A failure to answer, including one of Which's checks (a blank
// question, a bad option), is returned as Which returns it, and no route runs.
//
// The route runs once, on the caller's goroutine, under ctx, so a panic in it is
// the caller's panic; an error it returns is returned as it is, the same value,
// and the Record of the decision does not carry it. Nothing is returned beside an
// error. A nil ctx is an error of kind ErrInvalidRequest. The routes are not
// changed.
func RouteBy[T any](ctx context.Context, subject any, question string, routes []Route[T], opts ...Options) (T, error) {
	var zero T
	if ctx == nil {
		return zero, &Error{Kind: ErrInvalidRequest, Op: "routeby", Message: "the context is nil"}
	}
	// The rules of the routes are the internal check's, which Select makes too:
	// found here, before anything is asked, so that nothing is paid for a set of
	// routes that cannot be chosen between.
	chosen := make([]ops.Route[T], len(routes))
	labels := make([]string, len(routes))
	for i, route := range routes {
		chosen[i] = ops.Route[T]{Label: route.Label, Run: ops.Step[T](route.Run)}
		labels[i] = route.Label
	}
	if fault := ops.RouteFault(chosen); fault != "" {
		return zero, &Error{Kind: ErrInvalidRequest, Op: "routeby", Message: fault}
	}
	if len(routes) < 2 {
		return zero, &Error{Kind: ErrInvalidRequest, Op: "routeby", Message: "there have to be at least two routes to choose between"}
	}
	label, err := Which(ctx, subject, question, labels, opts...)
	if err != nil {
		return zero, err
	}
	return Select(ctx, label, routes...)
}
