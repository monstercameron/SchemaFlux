package exp

import (
	"context"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// AssembleOptions configures Assemble. Its zero value uses the smart merge strategy.
type AssembleOptions struct {
	schemaflux.Options

	// Template describes the structure wanted. Empty means the type alone.
	Template string
	// MergeStrategy resolves disagreements: "first", "last", "combine" or "smart". Empty means "smart".
	MergeStrategy string
	// FillGaps asks the model to infer missing fields. False rejects reported filled gaps.
	FillGaps bool
}

// AssembleResult contains the composed value and the model's account of how it was assembled.
type AssembleResult[T any] struct {
	// Composed is the assembled value.
	Composed T `json:"composed"`
	// FieldSources records the reported source and method for each field.
	FieldSources []ComposedField `json:"field_sources"`
	// ConflictsResolved is the reported number of conflicts resolved.
	ConflictsResolved int `json:"conflicts_resolved"`
	// GapsFilled lists fields the model says it inferred.
	GapsFilled []string `json:"gaps_filled,omitempty"`
	// UnusedParts lists part indexes the model says contributed nothing.
	UnusedParts []int `json:"unused_parts,omitempty"`
	// Completeness is the model's own claim of completeness, from 0 to 1; zero means none was reported.
	Completeness float64 `json:"completeness"`
}

// ComposedField describes where a composed field came from and how disagreements were resolved.
type ComposedField struct {
	// Field is the path of the composed field.
	Field string `json:"field"`
	// Sources lists the part indexes the model says contributed.
	Sources []int `json:"sources"`
	// Method is "single", "merged" or "inferred".
	Method string `json:"method"`
	// Conflicts reports whether the model says the field had conflicting values.
	Conflicts bool `json:"conflicts"`
	// Resolution describes the reported resolution, if any.
	Resolution string `json:"resolution,omitempty"`
}

func mergeAssembleOptions(values []AssembleOptions) AssembleOptions {
	return mergeExpValues(values, func(out, later AssembleOptions) AssembleOptions {
		out.Options = mergeCommonOptions([]schemaflux.Options{out.Options, later.Options})
		if later.Template != "" {
			out.Template = later.Template
		}
		if later.MergeStrategy != "" {
			out.MergeStrategy = later.MergeStrategy
		}
		if later.FillGaps {
			out.FillGaps = true
		}
		return out
	})
}

func assembleCommon(o AssembleOptions) (bind.Common, error) {
	c, err := commonSettings(o.Options)
	if err != nil {
		return bind.Common{}, err
	}
	if err := c.Check("exp.assemble"); err != nil {
		return bind.Common{}, err
	}
	return c, nil
}

func assembleInternal(ctx context.Context, o AssembleOptions, c bind.Common) ops.ComposeOptions {
	in := ops.NewComposeOptions()
	if o.Template != "" {
		in.Template = o.Template
	}
	if o.MergeStrategy != "" {
		in.MergeStrategy = o.MergeStrategy
	}
	in.FillGaps = o.FillGaps
	in.Context = ctx
	if c.Model != "" {
		in.Model = c.Model
	}
	if c.Tier != 0 {
		in.Intelligence = c.Tier
	}
	if c.Effort != "" {
		in.Effort = c.Effort
	}
	if c.Mode != 0 {
		in.Mode = c.Mode
	}
	if c.Steering != "" {
		in.Steering = c.Steering
	}
	if c.MaxOutputTokens != 0 {
		in.MaxOutputTokens = c.MaxOutputTokens
	}
	if c.WebSearch {
		in.WebSearch = true
	}
	return in
}

func assembleResult[T any](res ops.ComposeResult[T]) AssembleResult[T] {
	out := AssembleResult[T]{Composed: res.Composed, ConflictsResolved: res.ConflictsResolved, Completeness: res.Completeness}
	if len(res.FieldSources) > 0 {
		out.FieldSources = make([]ComposedField, len(res.FieldSources))
		for i, f := range res.FieldSources {
			out.FieldSources[i] = ComposedField{Field: f.Field, Sources: f.Sources, Method: f.Method, Conflicts: f.Conflicts, Resolution: f.Resolution}
		}
	}
	out.GapsFilled, out.UnusedParts = res.GapsFilled, res.UnusedParts
	return out
}

// Assemble builds a typed value from several parts. Default tier Smart, mode Transform.
//
//	result, err := exp.Assemble[Profile](ctx, []any{basics, address, preferences})
//	if err == nil { fmt.Println(result.Composed) }
//
// At least one part is required. Its Record is "exp.assemble", delivered at ContractJSONWellFormed.
func Assemble[T any](ctx context.Context, parts []any, options ...AssembleOptions) (AssembleResult[T], error) {
	var zero AssembleResult[T]
	call, err := bind.Begin(ctx, "exp.assemble")
	if err != nil {
		return zero, err
	}
	o := mergeAssembleOptions(options)
	if err := refuseExpEmpty(&call, "parts", len(parts)); err != nil {
		return zero, err
	}
	c, err := assembleCommon(o)
	if err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		res, err := ops.Assemble[T](parts, assembleInternal(call.Context(), o, c))
		return assembleResult(res), call.End(nil, err)
	}
	res, err := bind.Recorded(&call, types.ContractJSONWellFormed, func(ctx context.Context) (ops.ComposeResult[T], error) {
		return ops.Assemble[T](parts, assembleInternal(ctx, o, c))
	})
	return assembleResult(res), err
}
