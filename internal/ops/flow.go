package ops

import (
	"context"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/monstercameron/schemaflux/internal/types"
)

// BM-002. Combinators that take a decision and run the branch, so a program
// whose every decision is an operation of this library has no condition of its
// own to write. Each is a function over a context and typed values; none holds
// state.

// flowContext is the context a combinator runs its steps under: the caller's,
// or the background context when the caller gave none. Every combinator here
// does the same, so a step is never handed a nil context by one of them and not
// by another.
func flowContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// Branch runs then when yes is true and otherwise when it is not. It is an
// if-else whose condition was decided somewhere else, by Is or as a field of
// what Assess returned. A nil step returns the zero value.
func Branch[T any](ctx context.Context, yes bool, then, otherwise Step[T]) (T, error) {
	ctx = flowContext(ctx)
	step := otherwise
	if yes {
		step = then
	}
	if step == nil {
		var zero T
		return zero, nil
	}
	return step(ctx)
}

// Whether asks a yes-or-no question about a subject and runs then when the
// answer is yes and otherwise when it is no.
//
//	reply, err := schemaflux.Whether(ctx, message, "Is this message spam?", discard, handle)
//
// When the question cannot be answered, neither step runs and the error is
// returned: a failure is never read as a no. A nil step returns the zero value,
// as it does for Branch.
func Whether[T any](ctx context.Context, subject any, question string, then, otherwise Step[T], opts ...types.OpOptions) (T, error) {
	ctx = flowContext(ctx)
	yes, err := Is(ctx, subject, question, opts...)
	if err != nil {
		var zero T
		return zero, err
	}
	return Branch(ctx, yes, then, otherwise)
}

// Route is one way a program can go: a label, and the step that is run when
// the label is the one chosen.
type Route[T any] struct {
	Label string
	Run   Step[T]
}

// To builds a Route.
func To[T any](label string, run Step[T]) Route[T] {
	return Route[T]{Label: label, Run: run}
}

// RouteFault says what is wrong with a set of routes, in words that name the
// route and never its label: there are none, one has a blank label, one repeats
// the label of an earlier one, or one has nothing to run. It is empty when the
// routes can be chosen between. Labels are compared without the space around
// them, so "a" and "a " are one label given twice and are refused: two routes a
// log or a prompt cannot tell apart are a mistake, not a choice. (Select itself
// compares the label it is given exactly, as written.)
//
// It is the one check the route functions of the root and of the flow package
// make before anything runs, each reporting it as the error of its own kind.
func RouteFault[T any](routes []Route[T]) string {
	if len(routes) == 0 {
		return "there are no routes to choose between"
	}
	seen := make(map[string]bool, len(routes))
	for i, route := range routes {
		label := strings.TrimSpace(route.Label)
		switch {
		case label == "":
			return fmt.Sprintf("route %d has no label", i+1)
		case seen[label]:
			return fmt.Sprintf("route %d repeats the label of an earlier one", i+1)
		case route.Run == nil:
			return fmt.Sprintf("route %d has nothing to run", i+1)
		}
		seen[label] = true
	}
	return ""
}

// checkRoutes is RouteFault as an error of kind configuration for the operation
// called op.
func checkRoutes[T any](op string, routes []Route[T]) error {
	if fault := RouteFault(routes); fault != "" {
		return types.Newf(types.KindConfiguration, op, "%s", fault)
	}
	return nil
}

// Select runs the route whose label is the one given. It is a switch whose
// value was decided somewhere else, by Which or as a field of what Assess
// returned. A label that is none of the routes' is an error: nothing here is a
// default, since a value this program did not expect is not one it knows what
// to do with.
func Select[T any](ctx context.Context, label string, routes ...Route[T]) (T, error) {
	ctx = flowContext(ctx)
	var zero T
	if err := checkRoutes("select", routes); err != nil {
		return zero, err
	}
	for _, route := range routes {
		if route.Label == label {
			return route.Run(ctx)
		}
	}
	return zero, types.Newf(types.KindInvariantViolation, "select", "the label is not the label of any of the %d routes", len(routes))
}

// RouteBy asks which of the routes' labels answers a question about a subject
// and runs that route.
//
//	reply, err := schemaflux.RouteBy(ctx, message, "Which team should handle this?",
//	    []schemaflux.Route[Reply]{
//	        schemaflux.To("billing", handleBilling),
//	        schemaflux.To("technical", handleTechnical),
//	    })
//
// When the question cannot be answered, no route runs and the error is
// returned: a failure is never read as the first route. There must be at least
// two routes: a question with one possible answer asks the model nothing, and
// the program that wants one route has no decision to make. That is refused
// here, before any call, as a configuration error.
func RouteBy[T any](ctx context.Context, subject any, question string, routes []Route[T], opts ...types.OpOptions) (T, error) {
	ctx = flowContext(ctx)
	var zero T
	if err := checkRoutes("route", routes); err != nil {
		return zero, err
	}
	if len(routes) < 2 {
		return zero, types.Newf(types.KindConfiguration, "route", "RouteBy needs at least two routes to choose between, and was given %d", len(routes))
	}
	labels := make([]string, len(routes))
	for i, route := range routes {
		labels[i] = route.Label
	}
	label, err := Which(ctx, subject, question, labels, opts...)
	if err != nil {
		return zero, err
	}
	return Select(ctx, label, routes...)
}

// stepPanicked is the error a step that did not return is reported as: it
// panicked, or it ended its goroutine with runtime.Goexit, as t.FailNow does.
// It is an invariant violation and not an unknown: a step is supposed to return,
// and retry and escalation logic, which treats an unknown as a gap, has to be
// able to tell this from a transient failure. The panic's own value is not in
// it: it can hold anything, the caller's data included.
//
// The error is marked as a panic. The kind stays the invariant violation, and
// the sentinel errors.Is matches stays its sentinel; the mark is what lets a
// boundary that reports a failure of the library itself apart from an answer
// that failed its checks read this as the first.
func stepPanicked(op string) error {
	failure := types.Newf(types.KindInvariantViolation, op, "a step panicked or ended its goroutine without returning")
	failure.Narrow = types.NarrowPanic
	return failure
}

// refuseNilStep reports the first of the steps that is nil, given as whether
// each one is. It is called before any step starts, so a program that passes a
// nil step has not paid for the others.
func refuseNilStep(op string, isNil ...bool) error {
	for i, missing := range isNil {
		if missing {
			return types.Newf(types.KindConfiguration, op, "step %d is nil", i+1)
		}
	}
	return nil
}

// gather runs n steps at once and waits for them all. The first step to fail
// cancels the context the others run under, and its error is the one
// returned; the errors of steps that were cancelled because of it are not. Every
// step runs in a goroutine of its own, one step included, so that a step that
// ends its goroutine is reported as an error by this function and does not end
// the caller's.
func gather(ctx context.Context, op string, n int, run func(ctx context.Context, i int) error) error {
	ctx = flowContext(ctx)
	if n == 0 {
		return nil
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wait  sync.WaitGroup
		once  sync.Once
		first error
	)
	wait.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			var err error
			returned := false
			defer func() {
				if !returned {
					// runtime.Goexit cannot be recovered, and runs no code after
					// the step: without this the step's goroutine would end with
					// no error recorded, and the call would deliver a zero result
					// as a success.
					err = stepPanicked(op)
				}
				if err != nil {
					once.Do(func() {
						first = err
						cancel()
					})
				}
				wait.Done()
			}()
			err = runGuarded(ctx, op, i, run)
			returned = true
		}(i)
	}
	wait.Wait()
	return first
}

// runGuarded runs one step and turns a panic in it into an error, so that a
// step that panics in a goroutine of this library's does not take the process
// down with it. A step that ends its goroutine with runtime.Goexit cannot be
// turned into anything here: runGuarded does not return for it, and the
// goroutine's owner reports it, as gather does.
func runGuarded(ctx context.Context, op string, i int, run func(ctx context.Context, i int) error) (err error) {
	completed := false
	defer func() {
		if completed {
			return
		}
		// The stack says where. It prints the arguments of the functions on it
		// as raw words, so it is for the operator's log and not for a caller;
		// the panic's value is not logged, only what type it was, since that
		// is what tells a nil map from a bad index and holds none of the data.
		if recovered := recover(); recovered != nil {
			loggerFor(ctx).Error("A step panicked", "operation", op, "panic_type", fmt.Sprintf("%T", recovered), "stack", string(debug.Stack()))
			err = stepPanicked(op)
			return
		}
		loggerFor(ctx).Error("A step ended its goroutine without returning", "operation", op, "stack", string(debug.Stack()))
		err = stepPanicked(op)
	}()
	err = run(ctx, i)
	completed = true
	return err
}

// Gather runs steps that do not depend on each other at once and returns their
// results in the order the steps were given. A program that needs three
// answers waits as long as the slowest of them takes, where one after another
// it waits for all three.
//
// The steps run under the caller's context, so a client's scheduler still
// bounds how many of its calls are with the provider at once, and its budget
// is still charged. The first step to fail cancels the others and its error is
// returned; no results are returned with it. Cancelling is a request: the call
// returns when every step has returned, so a step that does not look at its
// context delays the return until it finishes. A step that panics, or ends its
// goroutine with runtime.Goexit, is an error. Every step is checked before the
// first one starts, and a nil step is a configuration error that starts none.
func Gather[T any](ctx context.Context, steps ...Step[T]) ([]T, error) {
	for i, step := range steps {
		if step == nil {
			return nil, types.Newf(types.KindConfiguration, "gather", "step %d is nil", i+1)
		}
	}
	results := make([]T, len(steps))
	err := gather(ctx, "gather", len(steps), func(ctx context.Context, i int) error {
		value, err := steps[i](ctx)
		if err != nil {
			return err
		}
		results[i] = value
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

// Gather2 is Gather for two steps whose results are of different types.
func Gather2[A, B any](ctx context.Context, first Step[A], second Step[B]) (A, B, error) {
	var (
		a A
		b B
	)
	err := refuseNilStep("gather", first == nil, second == nil)
	if err == nil {
		err = gather(ctx, "gather", 2, func(ctx context.Context, i int) (err error) {
			if i == 0 {
				a, err = first(ctx)
				return err
			}
			b, err = second(ctx)
			return err
		})
	}
	if err != nil {
		var (
			noA A
			noB B
		)
		return noA, noB, err
	}
	return a, b, nil
}

// Gather3 is Gather for three steps whose results are of different types.
func Gather3[A, B, C any](ctx context.Context, first Step[A], second Step[B], third Step[C]) (A, B, C, error) {
	var (
		a A
		b B
		c C
	)
	err := refuseNilStep("gather", first == nil, second == nil, third == nil)
	if err == nil {
		err = gather(ctx, "gather", 3, func(ctx context.Context, i int) (err error) {
			switch i {
			case 0:
				a, err = first(ctx)
			case 1:
				b, err = second(ctx)
			default:
				c, err = third(ctx)
			}
			return err
		})
	}
	if err != nil {
		var (
			noA A
			noB B
			noC C
		)
		return noA, noB, noC, err
	}
	return a, b, c, nil
}

// Gather4 is Gather for four steps whose results are of different types.
func Gather4[A, B, C, D any](ctx context.Context, first Step[A], second Step[B], third Step[C], fourth Step[D]) (A, B, C, D, error) {
	var (
		a A
		b B
		c C
		d D
	)
	err := refuseNilStep("gather", first == nil, second == nil, third == nil, fourth == nil)
	if err == nil {
		err = gather(ctx, "gather", 4, func(ctx context.Context, i int) (err error) {
			switch i {
			case 0:
				a, err = first(ctx)
			case 1:
				b, err = second(ctx)
			case 2:
				c, err = third(ctx)
			default:
				d, err = fourth(ctx)
			}
			return err
		})
	}
	if err != nil {
		var (
			noA A
			noB B
			noC C
			noD D
		)
		return noA, noB, noC, noD, err
	}
	return a, b, c, d, nil
}

// LAT-007. Speculation. A decision made by a model is a network wait, and the
// branch it picks is usually another, so a program that decides and then runs
// the branch waits twice. These start the branch that is likely beside the
// decision and keep it when the decision agrees. They are separate functions,
// not an option on Branch or RouteBy, so nothing is ever speculated by accident.

// aheadRun is a step started beside a decision, under a context of its own that
// can be cancelled without cancelling the caller's.
type aheadRun[T any] struct {
	cancel context.CancelFunc
	done   chan struct{}
	value  T
	err    error
}

// aheadStart starts a step in a goroutine. A nil step is a step that returns
// the zero value, as it is for Branch, and starts nothing.
func aheadStart[T any](ctx context.Context, op string, i int, step Step[T]) *aheadRun[T] {
	ctx, cancel := context.WithCancel(ctx)
	run := &aheadRun[T]{cancel: cancel, done: make(chan struct{})}
	if step == nil {
		close(run.done)
		return run
	}
	go func() {
		returned := false
		defer func() {
			if !returned {
				// The step ended its goroutine with runtime.Goexit, which nothing
				// recovers: it is an error, and not a result of the zero value.
				run.err = stepPanicked(op)
			}
			close(run.done)
		}()
		run.err = runGuarded(ctx, op, i, func(ctx context.Context, _ int) error {
			value, err := step(ctx)
			if err != nil {
				return err
			}
			run.value = value
			return nil
		})
		returned = true
	}()
	return run
}

// keep waits for the step and returns what it returned, an empty value beside
// an error.
func (run *aheadRun[T]) keep() (T, error) {
	<-run.done
	run.cancel()
	return run.value, run.err
}

// stop cancels the step and waits for it to return, and what it returned or
// failed with is discarded: it was a branch not taken. This is what keeps a
// cancelled step from outliving the call that started it. It is deferred by the
// callers, so that it also runs when the decision, which runs on the caller's
// goroutine, ends that goroutine with runtime.Goexit; it may be called twice.
func (run *aheadRun[T]) stop() {
	run.cancel()
	<-run.done
}

// aheadDecide runs the decision on the caller's goroutine. A decision that
// panics is an error. A caller whose context is done gets that context's error
// and never an answer, whichever way the decision went.
func aheadDecide[D any](ctx context.Context, op string, decide Step[D]) (D, error) {
	var answer D
	err := runGuarded(ctx, op, 0, func(ctx context.Context, _ int) (err error) {
		answer, err = decide(ctx)
		return err
	})
	if err == nil {
		err = contextOver(ctx)
	} else if done := contextOver(ctx); done != nil {
		err = done
	}
	if err != nil {
		var zero D
		return zero, err
	}
	return answer, nil
}

// aheadHere runs the step that was not started early, on the caller's
// goroutine, with a panic in it an error as it is in the others.
func aheadHere[T any](ctx context.Context, op string, i int, step Step[T]) (T, error) {
	var value T
	if step == nil {
		return value, nil
	}
	err := runGuarded(ctx, op, i, func(ctx context.Context, _ int) (err error) {
		value, err = step(ctx)
		return err
	})
	if err != nil {
		var zero T
		return zero, err
	}
	return value, nil
}

// aheadFailed is the error to return for one that happened while the caller's
// context was done: the context's own, classified as the rest of the library
// classifies it.
func aheadFailed(ctx context.Context, err error) error {
	if done := contextOver(ctx); done != nil {
		return done
	}
	return err
}

// BranchAhead decides and starts yes at once, and runs no only when the
// decision says no.
//
//   - Yes: the result of yes is the result, waited for if it is still running.
//     The program waited once where Branch after a decision waits twice.
//   - No: yes is cancelled, its result and its error are discarded, and no runs
//     and is the result. No is started as soon as yes has been told to stop, not
//     when yes has returned, so for a moment both are in flight; yes is waited
//     for, and what it returns discarded, before BranchAhead returns.
//   - The decision fails: yes is cancelled and the decision's error is returned,
//     with nothing of yes beside it.
//   - Yes fails before the decision arrives: if the decision then says yes that
//     error is the result, and if it says no the error is discarded.
//   - The caller's context is done: both stop and its error is returned.
//
// A step that panics is an error with no payload and the stack is logged, as it
// is for Gather. The decision runs on the caller's goroutine.
//
// It costs money when the decision says no. A branch that is cancelled may
// already have been sent to the provider and is billed by the provider; the
// budget ledger records what the cancelled call reported, if anything. Use it
// for a branch that is taken most of the time. When it is not, Branch after a
// decision is cheaper, and BranchBoth is the one for two branches that are
// equally likely.
//
// Cancelling is a request, and a branch that is cancelled may still complete
// what it was doing. A step that writes a record, sends a message or calls
// another service has done so by the time it notices its context is done, and
// its result being discarded does not undo it. Do not start a branch that has a
// side effect it would be wrong to have when the decision says no; the branch
// that is started early should be one that only reads or computes, or whose
// effect is safe to repeat or to reverse.
//
// No goroutine outlives the call: before it returns, yes has returned its result
// or been cancelled and waited for. A step that ignores its context holds the
// call until it returns, as one does for Gather. A nil yes or no returns the
// zero value, as it does for Branch; a nil decision is a configuration error and
// nothing is started.
func BranchAhead[T any](ctx context.Context, decide Step[bool], yes, no Step[T]) (T, error) {
	var zero T
	if decide == nil {
		return zero, types.Newf(types.KindConfiguration, "branch_ahead", "there is no decision to wait for")
	}
	ctx = flowContext(ctx)
	speculative := aheadStart(ctx, "branch_ahead", 0, yes)
	defer speculative.stop()
	answer, err := aheadDecide(ctx, "branch_ahead", decide)
	if err != nil {
		return zero, err
	}
	if answer {
		value, err := speculative.keep()
		if err != nil {
			return zero, aheadFailed(ctx, err)
		}
		return value, nil
	}
	// Yes is told to stop before no starts, and waited for after no is done (by
	// the deferred stop), so no is not held back by a step that is slow to notice.
	speculative.cancel()
	value, err := aheadHere(ctx, "branch_ahead", 1, no)
	if err != nil {
		return zero, aheadFailed(ctx, err)
	}
	return value, nil
}

// BranchBoth decides and starts both branches at once, keeps the one the
// decision picks, and cancels the other. It buys one wait in both directions at
// the price of both branches every time: a branch that is cancelled may already
// have been sent to the provider and is billed by the provider. Use it when the
// branches are about as likely and the time matters more than the money; when
// one is taken most of the time, BranchAhead pays for less.
//
// The results of the branch that is not taken, its error included, are
// discarded. Its side effects are not: cancelling is a request, and a branch
// that has a side effect, such as a write or a message sent, may complete it
// before it notices its context is done, in either direction and every time.
// Both branches are started, so use BranchBoth only for branches that read or
// compute, or whose effects are safe to repeat or to reverse.
//
// Both branches are in flight from the start, so the one the decision rules out
// is cancelled while it may still be running, and the call does not wait for it
// before it takes the other's result; it does wait for it before it returns, and
// what it returned is discarded. The other rules are BranchAhead's: a decision
// that fails cancels
// both and returns its own error, a context that is done returns its own error,
// a panic is an error with no payload, no goroutine outlives the call, and a nil
// branch returns the zero value.
func BranchBoth[T any](ctx context.Context, decide Step[bool], yes, no Step[T]) (T, error) {
	var zero T
	if decide == nil {
		return zero, types.Newf(types.KindConfiguration, "branch_both", "there is no decision to wait for")
	}
	ctx = flowContext(ctx)
	yesRun := aheadStart(ctx, "branch_both", 0, yes)
	noRun := aheadStart(ctx, "branch_both", 1, no)
	defer yesRun.stop()
	defer noRun.stop()
	answer, err := aheadDecide(ctx, "branch_both", decide)
	if err != nil {
		// Both are told before either is waited for, by the deferred stops.
		yesRun.cancel()
		noRun.cancel()
		return zero, err
	}
	kept, dropped := yesRun, noRun
	if !answer {
		kept, dropped = noRun, yesRun
	}
	dropped.cancel()
	value, err := kept.keep()
	if err != nil {
		return zero, aheadFailed(ctx, err)
	}
	return value, nil
}

// RouteAhead decides which route to take and starts the route labelled likely
// at once. When the decision names it, its result is the result, waited for if
// it is still running; when the decision names another, the likely route is
// cancelled, its result and its error are discarded, and the named route runs.
// The named route is started as soon as the likely one has been told to stop, not
// when it has returned, so for a moment both are in flight; the likely route is
// waited for, and what it returns discarded, before RouteAhead returns.
//
// A likely that is no route's label, a label given twice, a nil step and no
// routes at all are configuration errors found before anything starts, as for
// RouteBy. A decided label that is none of the routes' is the error Select
// returns, and the likely route is cancelled. The rest is BranchAhead's: a
// decision that fails cancels the likely route and returns its own error, a
// context that is done returns its own error, a panic is an error with no
// payload, and no goroutine outlives the call.
//
// It costs money whenever the decision names another route: the route that is
// cancelled may already have been sent to the provider and is billed by the
// provider. Use it when one route is taken most of the time.
//
// Cancelling is a request, and the likely route may complete what it was doing
// before it notices its context is done. A route with a side effect, such as a
// write or a message sent, has had it by the time the decision names another
// route, and discarding its result does not undo it; the likely route should be
// one that only reads or computes, or whose effect is safe to repeat or to
// reverse.
func RouteAhead[T any](ctx context.Context, decide Step[string], likely string, routes ...Route[T]) (T, error) {
	var zero T
	if err := checkRoutes("route_ahead", routes); err != nil {
		return zero, err
	}
	if decide == nil {
		return zero, types.Newf(types.KindConfiguration, "route_ahead", "there is no decision to wait for")
	}
	var speculated Step[T]
	for _, route := range routes {
		if route.Label == likely {
			speculated = route.Run
			break
		}
	}
	if speculated == nil {
		return zero, types.Newf(types.KindConfiguration, "route_ahead", "the likely label is not the label of any of the %d routes", len(routes))
	}
	ctx = flowContext(ctx)
	speculative := aheadStart(ctx, "route_ahead", 0, speculated)
	defer speculative.stop()
	label, err := aheadDecide(ctx, "route_ahead", decide)
	if err != nil {
		return zero, err
	}
	if label == likely {
		value, err := speculative.keep()
		if err != nil {
			return zero, aheadFailed(ctx, err)
		}
		return value, nil
	}
	var named Step[T]
	for _, route := range routes {
		if route.Label == label {
			named = route.Run
			break
		}
	}
	if named == nil {
		return zero, types.Newf(types.KindInvariantViolation, "select", "the label is not the label of any of the %d routes", len(routes))
	}
	speculative.cancel()
	value, err := aheadHere(ctx, "route_ahead", 1, named)
	if err != nil {
		return zero, aheadFailed(ctx, err)
	}
	return value, nil
}

// eachAtOnce is how many items Each works on at once. The calls they make are
// bounded by the client's scheduler whatever this is; this bounds the
// goroutines, so a list of a hundred thousand items is worked through by this
// many and not by one each.
const eachAtOnce = 64

// Each runs a step for every item of a list, several at once, and returns the
// results in the order of the items; for no items that is an empty list that is
// not nil, as it is for Gather. The first step to fail stops the rest and its
// error is returned; no results are returned with it. Stopping is a request: the
// call returns when every step that started has returned. A step that panics, or
// ends its goroutine with runtime.Goexit, is an error.
func Each[T, R any](ctx context.Context, items []T, step func(context.Context, T) (R, error)) ([]R, error) {
	if step == nil {
		return nil, types.Newf(types.KindConfiguration, "each", "there is no step to run")
	}
	if len(items) == 0 {
		return []R{}, nil
	}
	results := make([]R, len(items))
	workers := min(eachAtOnce, len(items))
	var next atomic.Int64
	err := gather(ctx, "each", workers, func(ctx context.Context, _ int) error {
		for {
			i := int(next.Add(1)) - 1
			if i >= len(items) {
				return nil
			}
			if err := contextOver(ctx); err != nil {
				return err
			}
			value, err := step(ctx, items[i])
			if err != nil {
				return err
			}
			results[i] = value
		}
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}
