package exp

import (
	"context"
	"math"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// ResolveOptions configures Resolve. Its zero value uses most-complete
// resolution, a 0.8 conflict threshold, Fast tier and Transform mode.
type ResolveOptions struct {
	schemaflux.Options

	// Strategy is newest, most-complete, majority, authoritative or merge.
	// Empty means most-complete.
	Strategy string
	// AuthoritativeSource is the source index preferred by authoritative
	// resolution. Zero means the first source; a negative or unavailable index
	// is a configuration error.
	AuthoritativeSource int
	// FieldPriorities maps field names to their preferred source indexes. Nil
	// means no field-specific priorities. A negative or unavailable index is a
	// configuration error.
	FieldPriorities map[string]int
	// ConflictThreshold is the similarity below which values count as a
	// conflict, from 0 to 1. Zero means 0.8. A value outside 0 to 1 is a
	// configuration error.
	ConflictThreshold float64
}

// ResolveResult contains the resolved value and the model's reported conflict
// details. ModelConfidence is the model's claim, and ModelAsked is false when
// a single source was returned without a model call.
type ResolveResult[T any] struct {
	// Resolved is the unified value.
	Resolved T `json:"resolved"`
	// Conflicts lists reported disagreements and their resolutions.
	Conflicts []Conflict `json:"conflicts,omitempty"`
	// SourceContributions maps each source index to the fields it contributed.
	SourceContributions map[int][]string `json:"source_contributions"`
	// Strategy is the strategy used for the resolution.
	Strategy string `json:"strategy"`
	// ModelConfidence is the model's own confidence claim, from 0 to 1.
	ModelConfidence float64 `json:"confidence"`
	// ModelAsked is false when one source made a model call unnecessary.
	ModelAsked bool `json:"model_asked"`
}

// Conflict describes a disagreement the model reported between sources.
type Conflict struct {
	// Field is the field name where the conflict occurred.
	Field string `json:"field"`
	// Values maps source indexes to the conflicting values.
	Values map[int]any `json:"values"`
	// Resolution describes how the disagreement was resolved.
	Resolution string `json:"resolution"`
	// ChosenSource is the index of the selected source.
	ChosenSource int `json:"chosen_source"`
	// ChosenValue is the selected value.
	ChosenValue any `json:"chosen_value"`
	// Reasoning explains the model's choice when it supplied an explanation.
	Reasoning string `json:"reasoning,omitempty"`
}

func mergeResolveOptions(values []ResolveOptions) ResolveOptions {
	return mergeExpValues(values, func(out, later ResolveOptions) ResolveOptions {
		out.Options = mergeCommonOptions([]schemaflux.Options{out.Options, later.Options})
		if later.Strategy != "" {
			out.Strategy = later.Strategy
		}
		if later.AuthoritativeSource != 0 {
			out.AuthoritativeSource = later.AuthoritativeSource
		}
		if later.FieldPriorities != nil {
			out.FieldPriorities = later.FieldPriorities
		}
		if later.ConflictThreshold != 0 {
			out.ConflictThreshold = later.ConflictThreshold
		}
		return out
	})
}

func resolveCheckOptions(o ResolveOptions, sourceCount int) error {
	if o.ConflictThreshold < 0 || math.IsNaN(o.ConflictThreshold) || math.IsInf(o.ConflictThreshold, 0) || o.ConflictThreshold > 1 {
		return types.Newf(types.KindConfiguration, "exp.resolve", "ConflictThreshold must be from 0 to 1")
	}
	if o.AuthoritativeSource < 0 || o.AuthoritativeSource >= sourceCount {
		return types.Newf(types.KindConfiguration, "exp.resolve", "AuthoritativeSource is outside the supplied sources")
	}
	switch o.Strategy {
	case "", "newest", "most-complete", "majority", "authoritative", "merge":
	default:
		return types.Newf(types.KindConfiguration, "exp.resolve", "Strategy is not supported")
	}
	for field, source := range o.FieldPriorities {
		if field == "" || source < 0 || source >= sourceCount {
			return types.Newf(types.KindConfiguration, "exp.resolve", "FieldPriorities contains an invalid source")
		}
	}
	return nil
}

func resolveInternal(ctx context.Context, o ResolveOptions, c bind.Common) ops.ResolveOptions {
	in := ops.NewResolveOptions()
	if o.Strategy != "" {
		in.Strategy = o.Strategy
	}
	if o.AuthoritativeSource != 0 {
		in.AuthoritativeSource = o.AuthoritativeSource
	}
	if o.FieldPriorities != nil {
		in.FieldPriorities = o.FieldPriorities
	}
	if o.ConflictThreshold != 0 {
		in.ConflictThreshold = o.ConflictThreshold
	}
	var common types.OpOptions
	c.Apply(&common)
	if common.Model != "" {
		in.Model = common.Model
	}
	if common.Intelligence != 0 {
		in.Intelligence = common.Intelligence
	}
	if common.Effort != "" {
		in.Effort = common.Effort
	}
	if common.Mode != 0 {
		in.Mode = common.Mode
	}
	if common.Steering != "" {
		in.Steering = common.Steering
	}
	if common.MaxOutputTokens != 0 {
		in.MaxOutputTokens = common.MaxOutputTokens
	}
	if common.WebSearch {
		in.WebSearch = true
	}
	in.Context = ctx
	return in
}

// Resolve makes one T out of several that disagree. One source is returned as
// it is, without a call. Default tier Fast, mode Transform. Its answer is
// parsed as JSON and source indexes are checked against the supplied sources.
//
//	result, err := exp.Resolve[Customer](ctx, []Customer{crm, billing})
//	if err == nil { customer := result.Resolved }
func Resolve[T any](ctx context.Context, sources []T, options ...ResolveOptions) (ResolveResult[T], error) {
	var zero ResolveResult[T]
	call, err := bind.Begin(ctx, "exp.resolve")
	if err != nil {
		return zero, err
	}
	o := mergeResolveOptions(options)
	if err := refuseExpEmpty(&call, "sources", len(sources)); err != nil {
		return zero, err
	}
	if err := resolveCheckOptions(o, len(sources)); err != nil {
		return zero, call.End(nil, err)
	}
	c, err := commonSettings(o.Options)
	if err != nil {
		return zero, call.End(nil, err)
	}
	if err := c.Check("exp.resolve"); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		res, err := ops.Resolve[T](sources, resolveInternal(call.Context(), o, c))
		return resolveResult(res), call.End(nil, err)
	}
	res, err := bind.Recorded(&call, types.ContractJSONWellFormed, func(ctx context.Context) (ops.ResolveResult[T], error) {
		return ops.Resolve[T](sources, resolveInternal(ctx, o, c))
	})
	return resolveResult(res), err
}

func resolveResult[T any](in ops.ResolveResult[T]) ResolveResult[T] {
	out := ResolveResult[T]{Resolved: in.Resolved, SourceContributions: in.SourceContributions, Strategy: in.Strategy, ModelConfidence: in.ModelConfidence}
	if asked, exists := in.Metadata["model_asked"]; exists {
		out.ModelAsked = asked != false
	} else {
		out.ModelAsked = true
	}
	if len(in.Conflicts) != 0 {
		out.Conflicts = make([]Conflict, len(in.Conflicts))
		for i, c := range in.Conflicts {
			out.Conflicts[i] = Conflict{Field: c.Field, Values: c.Values, Resolution: c.Resolution, ChosenSource: c.ChosenSource, ChosenValue: c.ChosenValue, Reasoning: c.Reasoning}
		}
	}
	return out
}
