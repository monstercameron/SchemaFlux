package flow

import (
	"context"

	schemaflux "github.com/monstercameron/schemaflux/v2"

	"github.com/monstercameron/schemaflux/v2/internal/ops"
)

// A decision made by a model is a network wait, and the branch it picks is
// usually another, so a program that decides and then runs the branch waits
// twice. BranchAhead, BranchBoth and RouteAhead start the branch that is likely
// beside the decision and keep it when the decision agrees. They are functions
// of their own and not an option of Branch or Select, so nothing is ever
// started early by accident.
//
// What a reader has to know before using one is what is in flight at each
// moment and what a branch that loses may still do, and each comment says both.
// The steps and the decision run under the context the caller gave, or under a
// context derived from it that the combinator can cancel without cancelling the
// caller's.

// BranchAhead decides and starts yes at once, and runs no only when the decision
// says no.
//
//	reply, err := flow.BranchAhead(ctx, isSpam, discard, handle)
//
// What is in flight, and when:
//
//   - At the start: yes, on a goroutine of its own, and the decision, on the
//     caller's goroutine. Both are running when BranchAhead has begun.
//   - The decision says yes: the result of yes is the result, waited for if it
//     is still running. The program waited once where Branch after a decision
//     waits twice. Nothing else is in flight.
//   - The decision says no: yes is cancelled, and its result and its error are
//     discarded. No is started as soon as yes has been told to stop, not when yes
//     has returned, so for a moment both are in flight; no runs on the caller's
//     goroutine, and yes is waited for, and what it returned discarded, before
//     BranchAhead returns.
//   - The decision fails: yes is cancelled and the decision's error is returned,
//     the same value the decision returned, with nothing of yes beside it. No
//     never starts.
//   - Yes fails before the decision arrives: if the decision then says yes, that
//     error is the result, the same value yes returned; if it says no, the error
//     is discarded.
//   - The caller's context is done: both stop and the *Error for the context is
//     returned, of kind ErrCanceled or ErrTimeout with the context's own error
//     beneath it, whichever of the steps failed because of it.
//
// It costs money when the decision says no. A branch that is cancelled may
// already have been sent to the provider and be billed by the provider; the
// budget ledger records what the cancelled call reported, if anything. Use it for
// a branch that is taken most of the time. When it is not, Branch after a
// decision is cheaper, and BranchBoth is the one for two branches that are
// equally likely.
//
// Cancelling is a request, and a branch that is cancelled may still complete
// what it was doing. A step that writes a record, sends a message or calls
// another service has done so by the time it notices its context is done, and
// its result being discarded does not undo it. Do not start a branch that has a
// side effect it would be wrong to have when the decision says no: the branch
// that is started early should be one that only reads or computes, or whose
// effect is safe to repeat or to reverse.
//
// No goroutine outlives the call: before BranchAhead returns, yes has returned
// its result or been cancelled and waited for. A step that ignores its context
// holds the call until it returns, as one does for schemaflux.Gather. A step or
// the decision that panics is an *Error of kind ErrInternal with nothing of the
// panic in it, and yes is cancelled and waited for; so is a yes that ends its
// goroutine with runtime.Goexit, as t.FailNow does. The decision and no run on
// the caller's goroutine, so one that ends it with Goexit ends the caller's, as it
// would from an ordinary call, after yes has been cancelled and waited for. A nil
// yes, a nil no, a nil decision and a nil ctx are errors of kind
// ErrInvalidRequest, as they are for schemaflux.Branch: a missing answer is never
// a zero value, and a caller who wants "otherwise nothing" writes the step that
// returns it. A ctx that is already done is an error of kind ErrCanceled or
// ErrTimeout. Nothing is started for any of them. No results are returned with an
// error. BranchAhead calls no model itself.
func BranchAhead[T any](ctx context.Context, decide schemaflux.Step[bool], yes, no schemaflux.Step[T]) (T, error) {
	var zero T
	if err := begin(ctx, "flow.branchahead"); err != nil {
		return zero, err
	}
	if decide == nil {
		return zero, refuse("flow.branchahead", "there is no decision to wait for")
	}
	if yes == nil || no == nil {
		return zero, refuse("flow.branchahead", "there is no step for %s", missingBranch(yes == nil, no == nil))
	}
	value, err := ops.BranchAhead(ctx, ownStep(decide), ownStep(yes), ownStep(no))
	if err != nil {
		return zero, end("flow.branchahead", err)
	}
	return value, nil
}

// BranchBoth decides and starts both branches at once, keeps the one the
// decision picks, and cancels the other. It buys one wait in both directions at
// the price of both branches every time: a branch that is cancelled may already
// have been sent to the provider and be billed by the provider. Use it when the
// branches are about as likely and the time matters more than the money; when
// one is taken most of the time, BranchAhead pays for less.
//
//	reply, err := flow.BranchBoth(ctx, isSpam, discard, handle)
//
// What is in flight, and when: at the start yes and no, each on a goroutine of
// its own, and the decision, on the caller's goroutine. All three run at once.
// When the decision answers, the branch it rules out is cancelled while it may
// still be running, and the call does not wait for it before it takes the other's
// result; it does wait for it before it returns, and what it returned is
// discarded.
//
// The results of the branch that is not taken, its error included, are
// discarded. Its side effects are not: cancelling is a request, and a branch
// that has a side effect, such as a write or a message sent, may complete it
// before it notices its context is done, in either direction and every time.
// Both branches are started, so use BranchBoth only for branches that read or
// compute, or whose effects are safe to repeat or to reverse.
//
// The other rules are BranchAhead's. A decision that fails cancels both branches
// and returns its own error, the same value the decision returned. A step or
// the decision that panics is an *Error of kind ErrInternal, and so is a branch
// that ends its goroutine with runtime.Goexit; a decision that does so ends the
// caller's goroutine, which it runs on, after both branches have been cancelled
// and waited for. A context that is done returns the *Error for the context. No
// goroutine outlives the call. A
// branch that fails is returned, the same value it returned, when it is the one
// the decision picked; the other's failure is discarded. A nil branch and a nil
// decision are errors of kind ErrInvalidRequest and nothing is started, as for
// BranchAhead. No results are returned with an error. BranchBoth calls no model
// itself.
func BranchBoth[T any](ctx context.Context, decide schemaflux.Step[bool], yes, no schemaflux.Step[T]) (T, error) {
	var zero T
	if err := begin(ctx, "flow.branchboth"); err != nil {
		return zero, err
	}
	if decide == nil {
		return zero, refuse("flow.branchboth", "there is no decision to wait for")
	}
	if yes == nil || no == nil {
		return zero, refuse("flow.branchboth", "there is no step for %s", missingBranch(yes == nil, no == nil))
	}
	value, err := ops.BranchBoth(ctx, ownStep(decide), ownStep(yes), ownStep(no))
	if err != nil {
		return zero, end("flow.branchboth", err)
	}
	return value, nil
}

// RouteAhead decides which route to take and starts the route labelled likely at
// once. When the decision names it, its result is the result, waited for if it
// is still running; when the decision names another, the likely route is
// cancelled, its result and its error are discarded, and the named route runs.
//
//	reply, err := flow.RouteAhead(ctx, which, "billing",
//		schemaflux.To("billing", handleBilling),
//		schemaflux.To("technical", handleTechnical))
//
// What is in flight, and when: at the start the likely route, on a goroutine of
// its own, and the decision, on the caller's goroutine. When the decision names
// another route, that route is started, on the caller's goroutine, as soon as the
// likely one has been told to stop, not when it has returned, so for a moment
// both are in flight; the likely route is waited for, and what it returned
// discarded, before RouteAhead returns.
//
// A likely that is no route's label, a label given twice, a blank label, a nil
// Run and no routes at all are errors of kind ErrInvalidRequest found before
// anything starts, as for schemaflux.Select. A decided label that is none of the
// routes' is an error of the same kind, and the likely route is cancelled. The
// rest is BranchAhead's: a decision that fails cancels the likely route and
// returns its own error, the same value the decision returned; a context that is
// done returns the *Error for the context; a route or the decision that panics
// is an *Error of kind ErrInternal, and so is a likely route that ends its
// goroutine with runtime.Goexit (the decision and the route that is named run on
// the caller's goroutine, so one that does so ends the caller's, after the likely
// route has been cancelled and waited for); and no goroutine outlives the call. No
// results are returned with an error.
//
// It costs money whenever the decision names another route: the route that is
// cancelled may already have been sent to the provider and be billed by the
// provider. Use it when one route is taken most of the time.
//
// Cancelling is a request, and the likely route may complete what it was doing
// before it notices its context is done. A route with a side effect, such as a
// write or a message sent, has had it by the time the decision names another
// route, and discarding its result does not undo it: the likely route should be
// one that only reads or computes, or whose effect is safe to repeat or to
// reverse. RouteAhead calls no model itself.
func RouteAhead[T any](ctx context.Context, decide schemaflux.Step[string], likely string, routes ...schemaflux.Route[T]) (T, error) {
	var zero T
	if err := begin(ctx, "flow.routeahead"); err != nil {
		return zero, err
	}
	chosen := make([]ops.Route[T], len(routes))
	for i, route := range routes {
		chosen[i] = ops.Route[T]{Label: route.Label, Run: ownStep(route.Run)}
	}
	if fault := ops.RouteFault(chosen); fault != "" {
		return zero, refuse("flow.routeahead", "%s", fault)
	}
	if decide == nil {
		return zero, refuse("flow.routeahead", "there is no decision to wait for")
	}
	if !hasLabel(routes, likely) {
		return zero, refuse("flow.routeahead", "the likely label is not the label of any of the %d routes", len(routes))
	}
	// A label the decision names that is none of the routes' is refused here, as
	// the decision's own failure, so that it is the *Error of kind
	// ErrInvalidRequest that Select gives for it and the likely route is
	// cancelled by the combinator as it is for any failed decision.
	decided := func(ctx context.Context) (string, error) {
		label, err := decide(ctx)
		if err != nil {
			return "", own(err)
		}
		if !hasLabel(routes, label) {
			return "", refuse("flow.routeahead", "the label is not the label of any of the %d routes", len(routes))
		}
		return label, nil
	}
	value, err := ops.RouteAhead(ctx, decided, likely, chosen...)
	if err != nil {
		return zero, end("flow.routeahead", err)
	}
	return value, nil
}

// missingBranch names the branch or branches a call was given no step for.
func missingBranch(noYes, noNo bool) string {
	switch {
	case noYes && noNo:
		return "yes or for no"
	case noYes:
		return "yes"
	}
	return "no"
}

// ownStep is step as the internal combinator's step, with what it returns as an
// error marked as the caller's. A nil step stays nil.
func ownStep[T any](step schemaflux.Step[T]) ops.Step[T] {
	if step == nil {
		return nil
	}
	return func(ctx context.Context) (T, error) {
		value, err := step(ctx)
		if err != nil {
			return value, own(err)
		}
		return value, nil
	}
}

// hasLabel reports whether one of the routes has exactly the label given.
func hasLabel[T any](routes []schemaflux.Route[T], label string) bool {
	for _, route := range routes {
		if route.Label == label {
			return true
		}
	}
	return false
}
