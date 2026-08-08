package fluent

import (
	"context"

	"github.com/monstercameron/schemaflux/internal/ops"
)

// FL-007 / CF-09: this package had no composition primitive of its own.
// CF-009's review finding (CF-09, "composition is linear and untyped")
// described the old `Pipeline` construct -- a straight line of
// `func(context.Context, any) (any, error)` steps with no type safety
// anywhere in it. CF-008 already retired that construct, so there was
// nothing left here to patch, only something to add: a way to feed a fluent
// builder's Run into the typed combinators M08 built
// (internal/ops/combinators.go -- Escalate, Fallback, Until), instead of a
// caller hand-writing an ops.Step[T] closure around every Run call, which is
// exactly the kind of hand-rolled control flow the combinators exist to
// replace.
//
// Escalate, Fallback, and Until are re-exported here as thin generic
// wrappers (not a var of the generic ops function -- Go does not let a
// package-level var stay generic, so each is a one-line pass-through
// function) so a caller composing fluent builders does not also need to
// import internal/ops directly for the combinator names, only for nothing:
// AsStep and AsStepCtx are the only new code; Escalate/Fallback/Until below
// call straight through to ops' own implementations, which is where their
// behaviour (terminal-failure detection, the EscalationRecord shape, and so
// on) is defined and tested. This file adds no second opinion about what
// they do.

// Step is one attempt at producing a value -- see ops.Step's doc comment
// for why it takes a context rather than closing over one.
type Step[T any] = ops.Step[T]

// EscalationRecord is ops.EscalationRecord, re-exported so a caller reading
// Escalate's second return value does not need an internal/ops import for
// one struct.
type EscalationRecord = ops.EscalationRecord

// Escalate runs first, and runs stronger only if it has to. See
// ops.Escalate's doc comment for exactly what "has to" means and why a
// terminal failure does not escalate.
func Escalate[T any](ctx context.Context, first, stronger Step[T], accept func(T) bool) (T, EscalationRecord, error) {
	return ops.Escalate(ctx, first, stronger, accept)
}

// Fallback runs primary, and alternate if primary fails. See ops.Fallback's
// doc comment for why it is Escalate with no accept function rather than a
// second implementation.
func Fallback[T any](ctx context.Context, primary, alternate Step[T]) (T, error) {
	return ops.Fallback(ctx, primary, alternate)
}

// Until runs step until the answer satisfies pred, or the attempts run out.
// See ops.Until's doc comment for why running out is an error rather than a
// silent return of the last (by definition failing) answer.
func Until[T any](ctx context.Context, step Step[T], pred func(T) bool, max int) (T, int, error) {
	return ops.Until(ctx, step, pred, max)
}

// AsStep adapts a fluent builder's Run() (T, error) -- the shape every
// builder in fluent_analysis.go, fluent_extended.go, and fluent_advanced.go
// has -- into a Step[T], so it can be handed to Escalate, Fallback, or
// Until directly:
//
//	cheap := Classifying[Ticket, Category](t).Fast()
//	strong := Classifying[Ticket, Category](t).Smart()
//	result, record, err := Escalate(ctx,
//	    AsStep(cheap.Run), AsStep(strong.Run),
//	    func(c Category) bool { return c != "" })
//
// The adapted Step ignores the context the combinator passes it: these
// builders have no ctx parameter on Run at all, because their context is
// set once at build time via .Context(ctx) (or left as
// context.Background()). A retry loop built from AsStep therefore cannot
// cancel an attempt already in flight -- only the combinator's own
// pre-attempt ctx.Err() check (Escalate, runStronger, and Until in
// internal/ops/combinators.go all make one) stops a *future* attempt from
// starting. A caller who needs mid-attempt cancellation from the
// combinator's ctx wants AsStepCtx and one of the six entrypoints.go
// builders instead.
func AsStep[T any](run func() (T, error)) Step[T] {
	return func(context.Context) (T, error) {
		return run()
	}
}

// AsStepCtx adapts a Run(ctx ...context.Context) (T, error) method -- the
// six entrypoints.go builders (Extracting, Transforming, Generating,
// Choosing, Filtering, Sorting) -- into a Step[T] that threads the
// combinator's own ctx through to the call, so a per-attempt cancellation
// or deadline set by Escalate/Fallback/Until's caller actually reaches the
// provider call in progress rather than only gating the next attempt:
//
//	cheap := Extracting[Invoice](doc).Quick()
//	strong := Extracting[Invoice](doc).Smart()
//	invoice, record, err := Escalate(ctx,
//	    AsStepCtx(cheap.Run), AsStepCtx(strong.Run),
//	    func(inv Invoice) bool { return inv.Total > 0 })
func AsStepCtx[T any](run func(...context.Context) (T, error)) Step[T] {
	return func(ctx context.Context) (T, error) {
		return run(ctx)
	}
}
