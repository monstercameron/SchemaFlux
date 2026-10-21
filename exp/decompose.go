package exp

import (
	"context"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// DecomposeOptions configures Decompose. Its zero value uses hierarchical
// decomposition to depth three, with dependencies and hierarchy preserved.
type DecomposeOptions struct {
	schemaflux.Options
	// Strategy is hierarchical, sequential, parallel or functional. Empty uses hierarchical.
	Strategy string
	// By describes what to split on. Empty leaves the choice to the model.
	By string
	// MaxDepth limits tree depth. Zero uses three; a negative value is invalid.
	MaxDepth int
	// MinGranularity says when a part is small enough to stop at. Empty leaves it unstated.
	MinGranularity string
	// TargetParts is the number of parts aimed for. Zero lets the model decide; negative is invalid.
	TargetParts int
	// OmitDependencies omits dependency links when true. False asks for them.
	OmitDependencies bool
	// NoHierarchy withholds parent-child links when true. False preserves them.
	NoHierarchy bool
	// IncludeEstimates asks for time or effort estimates. False omits them.
	IncludeEstimates bool
}

// DecomposeToSliceOptions configures DecomposeToSlice. Its zero value uses
// hierarchical decomposition and leaves the number of parts to the model.
type DecomposeToSliceOptions struct {
	schemaflux.Options
	// Strategy is hierarchical, sequential, parallel or functional. Empty uses hierarchical.
	Strategy string
	// TargetParts is the number of parts aimed for. Zero lets the model decide; negative is invalid.
	TargetParts int
}

// DecomposeResult is the tree returned by Decompose. Metadata is omitted
// because its values are model claims without a typed contract.
type DecomposeResult[T any] struct {
	// Original is the input value.
	Original T
	// Parts contains every part in the returned tree.
	Parts []DecomposedPart[T]
	// RootParts lists ids of parts without a parent.
	RootParts []string
	// TotalParts is the number of parts returned.
	TotalParts int
	// MaxDepth is the greatest depth among the returned parts.
	MaxDepth int
	// Dependencies maps a part id to ids it depends on.
	Dependencies map[string][]string
}

// DecomposedPart is one typed item in a decomposition tree.
type DecomposedPart[T any] struct {
	// ID uniquely identifies this part within the result.
	ID string
	// Name is the short label for this part.
	Name string
	// Description gives more detail about this part.
	Description string
	// Content is the part's value in the input type.
	Content T
	// ParentID is the id of this part's parent, or empty for a root.
	ParentID string
	// Children lists the ids of this part's direct children.
	Children []string
	// Dependencies lists the ids this part depends on.
	Dependencies []string
	// Depth is this part's depth in the tree.
	Depth int
	// Order is this part's order among the decomposition parts.
	Order int
	// Estimate is the requested model estimate, or empty when omitted.
	Estimate string
}

func mergeDecomposeOptions(values []DecomposeOptions) DecomposeOptions {
	return mergeExpValues(values, func(out, later DecomposeOptions) DecomposeOptions {
		out.Options = mergeCommonOptions([]schemaflux.Options{out.Options, later.Options})
		if later.Strategy != "" {
			out.Strategy = later.Strategy
		}
		if later.By != "" {
			out.By = later.By
		}
		if later.MaxDepth != 0 {
			out.MaxDepth = later.MaxDepth
		}
		if later.MinGranularity != "" {
			out.MinGranularity = later.MinGranularity
		}
		if later.TargetParts != 0 {
			out.TargetParts = later.TargetParts
		}
		if later.OmitDependencies {
			out.OmitDependencies = true
		}
		if later.NoHierarchy {
			out.NoHierarchy = true
		}
		if later.IncludeEstimates {
			out.IncludeEstimates = true
		}
		return out
	})
}

func mergeDecomposeToSliceOptions(values []DecomposeToSliceOptions) DecomposeToSliceOptions {
	return mergeExpValues(values, func(out, later DecomposeToSliceOptions) DecomposeToSliceOptions {
		out.Options = mergeCommonOptions([]schemaflux.Options{out.Options, later.Options})
		if later.Strategy != "" {
			out.Strategy = later.Strategy
		}
		if later.TargetParts != 0 {
			out.TargetParts = later.TargetParts
		}
		return out
	})
}

func decomposeCommon(o schemaflux.Options, op string) (bind.Common, error) {
	c, err := commonSettings(o)
	if err != nil {
		return bind.Common{}, err
	}
	if err := c.Check(op); err != nil {
		return bind.Common{}, err
	}
	return c, nil
}

func decomposeInternal(ctx context.Context, o DecomposeOptions, c bind.Common) ops.DecomposeOptions {
	in := ops.NewDecomposeOptions()
	if o.Strategy != "" {
		in.Strategy = o.Strategy
	}
	if o.By != "" {
		in.DecomposeBy = o.By
	}
	if o.MaxDepth != 0 {
		in.MaxDepth = o.MaxDepth
	}
	if o.MinGranularity != "" {
		in.MinGranularity = o.MinGranularity
	}
	if o.TargetParts != 0 {
		in.TargetParts = o.TargetParts
	}
	in.IncludeDependencies = !o.OmitDependencies
	in.PreserveHierarchy = !o.NoHierarchy
	in.IncludeEstimates = o.IncludeEstimates
	applyFlatCommon(ctx, c, &in.CommonOptions, &in.OpOptions)
	return in
}

func decomposeSliceInternal(ctx context.Context, o DecomposeToSliceOptions, c bind.Common) ops.DecomposeOptions {
	in := ops.NewDecomposeOptions()
	if o.Strategy != "" {
		in.Strategy = o.Strategy
	}
	if o.TargetParts != 0 {
		in.TargetParts = o.TargetParts
	}
	applyFlatCommon(ctx, c, &in.CommonOptions, &in.OpOptions)
	return in
}

func decomposeResult[T any](in ops.DecomposeResult[T]) DecomposeResult[T] {
	out := DecomposeResult[T]{Original: in.Original, RootParts: in.RootParts, TotalParts: in.TotalParts, MaxDepth: in.MaxDepth, Dependencies: in.Dependencies}
	if len(in.Parts) != 0 {
		out.Parts = make([]DecomposedPart[T], len(in.Parts))
		for i, p := range in.Parts {
			out.Parts[i] = DecomposedPart[T]{ID: p.ID, Name: p.Name, Description: p.Description, Content: p.Content, ParentID: p.ParentID, Children: p.Children, Dependencies: p.Dependencies, Depth: p.Depth, Order: p.Order, Estimate: p.Estimate}
		}
	}
	return out
}

// Decompose breaks a value into typed parts with ids, hierarchy and dependencies.
// Default tier Fast, mode Transform. A schema is sent when T supports one; its
// contract level is computed for T. The Record Op is "exp.decompose".
func Decompose[T any](ctx context.Context, input T, opts ...DecomposeOptions) (DecomposeResult[T], error) {
	var zero DecomposeResult[T]
	call, err := bind.Begin(ctx, "exp.decompose")
	if err != nil {
		return zero, err
	}
	o := mergeDecomposeOptions(opts)
	c, err := decomposeCommon(o.Options, "exp.decompose")
	if err != nil {
		return zero, call.End(nil, err)
	}
	if o.MaxDepth < 0 || o.TargetParts < 0 {
		return zero, call.End(nil, types.Newf(types.KindConfiguration, "exp.decompose", "numeric option is negative"))
	}
	if call.Plain() {
		res, err := ops.Decompose[T](input, decomposeInternal(call.Context(), o, c))
		return decomposeResult(res), call.End(nil, err)
	}
	level := ops.ContractLevelFor[T]("exp.decompose")
	res, err := bind.Recorded(&call, level, func(ctx context.Context) (ops.DecomposeResult[T], error) {
		return ops.Decompose[T](input, decomposeInternal(ctx, o, c))
	})
	return decomposeResult(res), err
}

// DecomposeToSlice returns a flat list of U without tree ids or dependencies.
// Default tier Fast, mode Transform. The answer is parsed as JSON without a
// declared schema; the Record Op is "exp.decomposetoslice".
func DecomposeToSlice[U any, T any](ctx context.Context, input T, opts ...DecomposeToSliceOptions) ([]U, error) {
	var zero []U
	call, err := bind.Begin(ctx, "exp.decomposetoslice")
	if err != nil {
		return zero, err
	}
	o := mergeDecomposeToSliceOptions(opts)
	c, err := decomposeCommon(o.Options, "exp.decomposetoslice")
	if err != nil {
		return zero, call.End(nil, err)
	}
	if o.TargetParts < 0 {
		return zero, call.End(nil, types.Newf(types.KindConfiguration, "exp.decomposetoslice", "target parts must not be negative"))
	}
	if call.Plain() {
		res, err := ops.DecomposeToSlice[T, U](input, decomposeSliceInternal(call.Context(), o, c))
		return res, call.End(nil, err)
	}
	res, err := bind.Recorded(&call, types.ContractJSONWellFormed, func(ctx context.Context) ([]U, error) {
		return ops.DecomposeToSlice[T, U](input, decomposeSliceInternal(ctx, o, c))
	})
	return res, err
}
