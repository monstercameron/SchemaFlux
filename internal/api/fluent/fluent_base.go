package fluent

import (
	"context"
)

// accumulateSteering joins a new steering instruction onto whatever the
// caller already accumulated (FL-004 / F-04). It is the shared rule behind
// every Steer() in this package -- commonRequest's (via
// CommonOptions.WithSteering in internal/ops/options.go), opRequest's, and
// directRequest's setSteering closures in fluent_advanced.go all use it, so
// two builders built from the same fluent call chain a caller might switch
// between behave identically rather than one silently keeping only the last
// call.
func accumulateSteering(existing, next string) string {
	if next == "" {
		return existing
	}
	if existing == "" {
		return next
	}
	return existing + "; " + next
}

// validatable is implemented by every options type in internal/ops (each has
// its own Validate() error, called first thing by its op function -- see
// e.g. Choose in internal/ops/collection.go). This package cannot add a
// method to those types (they are aliased from ops, which is out of scope
// for this change), so it recovers the method through a type assertion
// instead.
type validatable interface {
	Validate() error
}

// buildError reports whether opts, as built, is invalid -- FL-006 / F-07: a
// mis-built request (e.g. Filtering(items).Run() with no .By(...) criteria
// ever set) used to be discovered only after Run() had handed the options to
// an op function, which does whatever setup work it does before reaching its
// own opts.Validate() call. Every Run() and RunResult() in this package
// calls buildError first, so an invalid request is refused at the point the
// caller asked for it to run, before this package -- and therefore before
// any op function -- does anything with a provider.
//
// It is not a second opinion about what "invalid" means: it calls the exact
// same Validate() the op function would have called, just earlier. Two
// different answers to "is this valid" between fluent and ops would be the
// same class of bug AGENTS.md forbids for retry classification.
func buildError[Opt any](opts Opt) error {
	if v, ok := any(opts).(validatable); ok {
		return v.Validate()
	}
	return nil
}

// requestBase is FL-005 / F-05's single builder base.
//
// Before this, the eleven methods below (WithOptions, Configure, Steer,
// Threshold, Mode, Strict/TransformMode/Creative, Intelligence,
// Smart/Fast/Quick, Context, RequestID, CorrelationID) were implemented
// three times -- commonRequest, opRequest, and directRequest -- because the
// Options types behind the ~40 request builders in this package disagree
// about where the seven settable fields (steering, threshold, mode,
// intelligence, context, request ID, correlation ID) live:
//
//   - Some embed a nested ops.CommonOptions and go through its own With*
//     methods (ClassifyOptions, ValidateOptions, ...).
//   - Some embed types.OpOptions directly with no CommonOptions wrapper
//     (SimilarOptions, ParseOptions, ...).
//   - Eleven (NegotiateOptions, ResolveOptions, ConformOptions, ...) embed
//     neither: Mode, Intelligence, Context, and Steering are plain fields on
//     the struct itself, and several of them have no Threshold field at all.
//
// M06 made the field *names* and *types* uniform (S-001..S-011); it did not
// -- and was not asked to -- make every Options struct embed the same
// wrapper type, so the three access patterns above are still three
// genuinely different shapes to read from Go's perspective. What collapses
// them is moving the difference out of the base's structure and into seven
// per-field setter closures supplied at construction, the same shape
// directRequest already used for the eleven struct-literal types. A type
// missing a field (the common case is Threshold on the eleven direct types)
// just supplies a nil setter for it; every setter is nil-checked, so
// omitting one silently no-ops that single method instead of failing to
// compile or forcing a fake field into a struct that never had one.
//
// wiring_test.go's TestEveryBuilderWiresEverySetter is the regression test
// that a construction site did not forget one of the seven closures.
type requestBase[Self any, Opt any] struct {
	opts Opt
	lift func(Opt) Self

	setSteering      func(Opt, string) Opt
	setThreshold     func(Opt, float64) Opt
	setMode          func(Opt, Mode) Opt
	setIntelligence  func(Opt, Speed) Opt
	setModel         func(Opt, string) Opt
	setContext       func(Opt, context.Context) Opt
	setRequestID     func(Opt, string) Opt
	setCorrelationID func(Opt, string) Opt
}

func (r requestBase[Self, Opt]) WithOptions(opts Opt) Self {
	return r.lift(opts)
}

func (r requestBase[Self, Opt]) Configure(fn func(Opt) Opt) Self {
	return r.lift(fn(r.opts))
}

// Steer accumulates steering instructions rather than replacing the
// previous one (FL-004 / F-04: a second .Steer(...) call used to silently
// discard the first). Accumulation itself happens inside each construction
// site's setSteering closure (via accumulateSteering, or via
// CommonOptions.WithSteering for the types that route through it), not
// here, so this method is one line for every builder in the package
// regardless of which of the three field shapes above its Opt uses.
func (r requestBase[Self, Opt]) Steer(steering string) Self {
	if r.setSteering == nil {
		return r.lift(r.opts)
	}
	return r.lift(r.setSteering(r.opts, steering))
}

func (r requestBase[Self, Opt]) Threshold(threshold float64) Self {
	if r.setThreshold == nil {
		return r.lift(r.opts)
	}
	return r.lift(r.setThreshold(r.opts, threshold))
}

func (r requestBase[Self, Opt]) Mode(mode Mode) Self {
	if r.setMode == nil {
		return r.lift(r.opts)
	}
	return r.lift(r.setMode(r.opts, mode))
}

func (r requestBase[Self, Opt]) Strict() Self {
	return r.Mode(Strict)
}

func (r requestBase[Self, Opt]) TransformMode() Self {
	return r.Mode(TransformMode)
}

func (r requestBase[Self, Opt]) Creative() Self {
	return r.Mode(Creative)
}

func (r requestBase[Self, Opt]) Intelligence(level Speed) Self {
	if r.setIntelligence == nil {
		return r.lift(r.opts)
	}
	return r.lift(r.setIntelligence(r.opts, level))
}

func (r requestBase[Self, Opt]) Smart() Self {
	return r.Intelligence(Smart)
}

func (r requestBase[Self, Opt]) Fast() Self {
	return r.Intelligence(Fast)
}

func (r requestBase[Self, Opt]) Quick() Self {
	return r.Intelligence(Quick)
}

// Model pins the exact model for this call, bypassing the Intelligence tier's
// mapping. An empty string clears the pin and restores the tier.
//
// The pin itself is TC-005 and has worked since; what was missing was any way
// to ask for it from the fluent API, which is the entry point every example and
// every README line uses. A caller could reach it only by building an options
// struct by hand and passing it to WithOptions, which is the escape hatch, not
// the interface.
//
// The workaround a caller is pushed into without this is worse than the gap:
// name a provider whose tier mapping resolves to *something*, then substitute
// the real model further down. That is two lies at once -- about which provider
// is configured, and about where the model is decided -- and both are invisible
// in the envelope, which would go on reporting the provider that was named.
func (r requestBase[Self, Opt]) Model(model string) Self {
	if r.setModel == nil {
		return r.lift(r.opts)
	}
	return r.lift(r.setModel(r.opts, model))
}

// optsWithRunContext applies a trailing Run(ctx) argument, returning the
// options unchanged when the caller passed none.
//
// It routes through the builder's own setContext closure rather than reaching
// for a field, because the option types in this package come in three shapes --
// embedding CommonOptions, embedding types.OpOptions, or spelling the common
// fields out flat -- and the closure is the one thing that already knows which.
//
// Why this exists: `Run` took a context on six builders and nothing at all on
// forty-nine, so a caller who learned `Extracting[T](x).Run(ctx)` and then
// reached for `Summarizing(x).Run(ctx)` got a compile error and had to discover
// `.Context(ctx).Run()`. Two spellings of the same idea in one package, with
// nothing to indicate which applies where.
func (r requestBase[Self, Opt]) optsWithRunContext(ctx []context.Context) Opt {
	if len(ctx) == 0 || r.setContext == nil {
		return r.opts
	}
	return r.setContext(r.opts, ctx[0])
}

func (r requestBase[Self, Opt]) Context(ctx context.Context) Self {
	if r.setContext == nil {
		return r.lift(r.opts)
	}
	return r.lift(r.setContext(r.opts, ctx))
}

func (r requestBase[Self, Opt]) RequestID(requestID string) Self {
	if r.setRequestID == nil {
		return r.lift(r.opts)
	}
	return r.lift(r.setRequestID(r.opts, requestID))
}

func (r requestBase[Self, Opt]) CorrelationID(correlationID string) Self {
	if r.setCorrelationID == nil {
		return r.lift(r.opts)
	}
	return r.lift(r.setCorrelationID(r.opts, correlationID))
}
