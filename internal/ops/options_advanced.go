// Constructors for the "advanced" fluent surface's option types.
//
// The fluent entrypoints for these types once built their options from a bare
// `XOptions{}` literal, because no constructor existed, and so got Mode 0 and
// Intelligence 0 instead of the defaults the direct functional API applies. A
// constructor here is the one place a type's defaults are written. Pivot and
// Project call theirs for the options they run with when given none; the other
// operations still carry the same literal in their own function, and
// internal/api/fluent's defaults test is what compares the fluent entrypoint's
// options with the constructor, so a literal changed in only one place is a
// divergence nothing in this package would catch.

package ops

import "github.com/monstercameron/schemaflux/v2/internal/types"

// NewNegotiateOptions creates NegotiateOptions with the defaults Negotiate
// applies internally when the caller supplies none.
func NewNegotiateOptions() NegotiateOptions {
	return NegotiateOptions{
		MinSatisfaction: 0.6,
		MaxAlternatives: 3,
		Strategy:        "balanced",
		Mode:            types.TransformMode,
		Intelligence:    types.Fast,
	}
}

// NewAdversarialOptions creates AdversarialOptions with the defaults
// SettleAdversarial applies internally when the caller supplies none.
func NewAdversarialOptions() AdversarialOptions {
	return AdversarialOptions{
		Strategy:     "balanced",
		Mode:         types.TransformMode,
		Intelligence: types.Fast,
	}
}

// NewResolveOptions creates ResolveOptions with the defaults Resolve applies
// internally when the caller supplies none.
func NewResolveOptions() ResolveOptions {
	return ResolveOptions{
		Strategy:          "most-complete",
		ConflictThreshold: 0.8,
		Mode:              types.TransformMode,
		Intelligence:      types.Fast,
	}
}

// NewDeriveOptions creates DeriveOptions with the defaults Derive applies
// internally when the caller supplies none.
func NewDeriveOptions() DeriveOptions {
	return DeriveOptions{
		IncludeReasoning: true,
		MinConfidence:    0.6,
		Mode:             types.TransformMode,
		Intelligence:     types.Fast,
	}
}

// NewConformOptions creates ConformOptions with the defaults Conform applies
// internally when the caller supplies none.
func NewConformOptions() ConformOptions {
	return ConformOptions{
		PreserveUnknown: true,
		Validate:        true,
		Mode:            types.TransformMode,
		Intelligence:    types.Fast,
	}
}

// NewInterpolateOptions creates InterpolateOptions with the defaults
// Interpolate applies internally when the caller supplies none.
func NewInterpolateOptions() InterpolateOptions {
	return InterpolateOptions{
		Method:        "auto",
		ContextWindow: 3,
		Mode:          types.TransformMode,
		Intelligence:  types.Fast,
	}
}

// NewArbitrateOptions creates ArbitrateOptions with the defaults Arbitrate
// applies internally when the caller supplies none.
//
// It used to set IncludeReasoning, which Arbitrate does not: an arbitration
// built here, or with the fluent builder, asked for a reason with every
// evaluation, and the same one written as a struct did not.
func NewArbitrateOptions() ArbitrateOptions {
	return ArbitrateOptions{
		Tiebreaker:   "most-confident",
		Mode:         types.TransformMode,
		Intelligence: types.Fast,
	}
}

// NewProjectOptions creates ProjectOptions with the defaults Project applies
// internally when the caller supplies none.
func NewProjectOptions() ProjectOptions {
	return ProjectOptions{
		Mode:         types.TransformMode,
		Intelligence: types.Fast,
	}
}

// NewAuditOptions creates AuditOptions with the defaults auditCore applies
// internally when the caller supplies none.
func NewAuditOptions() AuditOptions {
	return AuditOptions{
		Mode:         types.TransformMode,
		Intelligence: types.Smart,
	}
}

// NewComposeOptions creates ComposeOptions with the defaults Compose applies
// internally when the caller supplies none.
func NewComposeOptions() ComposeOptions {
	return ComposeOptions{
		MergeStrategy: "smart",
		Validate:      true,
		Mode:          types.TransformMode,
		Intelligence:  types.Smart,
	}
}

// NewPivotOptions creates PivotOptions with the defaults Pivot applies
// internally when the caller supplies none.
func NewPivotOptions() PivotOptions {
	return PivotOptions{
		Aggregate:    "first",
		Mode:         types.TransformMode,
		Intelligence: types.Smart,
	}
}
