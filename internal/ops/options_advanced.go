// package ops - constructors for the "advanced" fluent surface's option types
//
// FL-003 / F-03: eleven fluent entrypoints (fluent_advanced.go's Negotiating,
// NegotiatingAdversarially, Resolving, Deriving, Conforming, Interpolating,
// Arbitrating, Projecting, Auditing, Assembling, Pivoting) built their options
// from a bare `XOptions{}` literal instead of a constructor, because no
// constructor existed for these eleven types -- unlike ExtractOptions,
// TransformOptions, GenerateOptions, ChooseOptions, FilterOptions, and
// SortOptions, each of which already has a NewXOptions() in options.go. A
// caller going through the fluent surface therefore got Mode: 0 (StrictMode)
// and Intelligence: 0 (whichever Speed zero-values to) instead of the
// defaults the direct functional API applies, and the two public APIs
// disagreed on defaults purely by which spelling the caller chose.
//
// These constructors do not invent new defaults: each one reproduces, field
// for field, the "Apply defaults" literal already built inside the
// corresponding op function (Negotiate, SettleAdversarial, Resolve, Derive,
// Conform, Interpolate, Arbitrate, Project, auditCore, Compose, Pivot) --
// negotiate.go, resolve.go, derive.go, conform.go, interpolate.go,
// arbitrate.go, project.go, audit.go, compose.go, pivot.go, none of which
// this task may edit. Divergence between this file and those literals would
// silently reopen exactly the gap FL-003 exists to close, so a test
// (fl003_advanced_options_test.go) asserts equality against the value each
// op function actually runs when given no caller options at all.
package ops

import "github.com/monstercameron/schemaflux/internal/types"

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
func NewArbitrateOptions() ArbitrateOptions {
	return ArbitrateOptions{
		IncludeReasoning: true,
		Tiebreaker:       "most-confident",
		Mode:             types.TransformMode,
		Intelligence:     types.Fast,
	}
}

// NewProjectOptions creates ProjectOptions with the defaults Project applies
// internally when the caller supplies none.
func NewProjectOptions() ProjectOptions {
	return ProjectOptions{
		InferMissing: false,
		Mode:         types.TransformMode,
		Intelligence: types.Fast,
	}
}

// NewAuditOptions creates AuditOptions with the defaults auditCore applies
// internally when the caller supplies none.
func NewAuditOptions() AuditOptions {
	return AuditOptions{
		Threshold:    0.0,
		Deep:         true,
		Mode:         types.TransformMode,
		Intelligence: types.Smart,
	}
}

// NewComposeOptions creates ComposeOptions with the defaults Compose applies
// internally when the caller supplies none.
func NewComposeOptions() ComposeOptions {
	return ComposeOptions{
		MergeStrategy: "smart",
		FillGaps:      false,
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
		Flatten:      false,
		Mode:         types.TransformMode,
		Intelligence: types.Smart,
	}
}
