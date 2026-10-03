package exp

import (
	"context"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// InterpolateOptions configures sequence gap filling.
type InterpolateOptions struct {
	schemaflux.Options
	// Method selects linear, trend, semantic, pattern or automatic filling. Empty means automatic.
	Method string
	// GapIndices identifies gaps explicitly. Nil lets the model find them.
	GapIndices []int
	// SequenceField names the field that orders the sequence. Empty uses slice order.
	SequenceField string
	// ContextWindow is the number of neighbours to consider. Zero means three. A negative value is a configuration error.
	ContextWindow int
	// Constraints gives guidance for filled values. Nil means no additional guidance; Go does not validate it.
	Constraints []string
}

// InterpolateResult contains the completed sequence and details reported for filled gaps.
type InterpolateResult[T any] struct {
	// Complete is the sequence with gaps filled, at the same length as the input.
	Complete []T `json:"complete"`
	// Filled lists each item the model reports as filled.
	Filled []FilledItem `json:"filled,omitempty"`
	// GapCount is the number of filled items, equal to len(Filled).
	GapCount int `json:"gap_count"`
	// Method is the method reported by the model or requested by the caller.
	Method string `json:"method"`
	// AverageConfidence is the mean of the model's own confidence claims, or zero when no gaps were filled.
	AverageConfidence float64 `json:"average_confidence"`
}

// FilledItem describes one value the model reports as filled.
type FilledItem struct {
	// Index is the position of the filled value in the sequence.
	Index int `json:"index"`
	// Method is the method reported for this value.
	Method string `json:"method"`
	// BasedOn lists the input positions used to infer the value.
	BasedOn []int `json:"based_on,omitempty"`
	// Reasoning is the model's explanation for the inferred value.
	Reasoning string `json:"reasoning,omitempty"`
	// ModelConfidence is the model's own confidence claim.
	ModelConfidence float64 `json:"confidence"`
}

func mergeInterpolateOptions(values []InterpolateOptions) InterpolateOptions {
	return mergeExpValues(values, func(out, later InterpolateOptions) InterpolateOptions {
		out.Options = mergeCommonOptions([]schemaflux.Options{out.Options, later.Options})
		if later.Method != "" {
			out.Method = later.Method
		}
		if later.GapIndices != nil {
			out.GapIndices = later.GapIndices
		}
		if later.SequenceField != "" {
			out.SequenceField = later.SequenceField
		}
		if later.ContextWindow != 0 {
			out.ContextWindow = later.ContextWindow
		}
		if later.Constraints != nil {
			out.Constraints = later.Constraints
		}
		return out
	})
}

func interpolateCommon(o InterpolateOptions) (bind.Common, error) {
	c, err := commonSettings(o.Options)
	if err != nil {
		return bind.Common{}, err
	}
	if err := c.Check("exp.interpolate"); err != nil {
		return bind.Common{}, err
	}
	if o.ContextWindow < 0 {
		return bind.Common{}, bind.Refuse("exp.interpolate", types.KindConfiguration, "ContextWindow cannot be negative")
	}
	return c, nil
}

func interpolateInternal(ctx context.Context, o InterpolateOptions, c bind.Common) ops.InterpolateOptions {
	in := ops.NewInterpolateOptions()
	if o.Method != "" {
		in.Method = o.Method
	}
	if o.GapIndices != nil {
		in.GapIndices = o.GapIndices
	}
	if o.SequenceField != "" {
		in.SequenceField = o.SequenceField
	}
	if o.ContextWindow != 0 {
		in.ContextWindow = o.ContextWindow
	}
	if o.Constraints != nil {
		in.Constraints = o.Constraints
	}
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

// Interpolate fills gaps in a sequence while preserving its length and every explicitly non-gap item.
// The Record is exp.interpolate and the parsed answer is delivered at ContractJSONWellFormed.
func Interpolate[T any](ctx context.Context, items []T, options ...InterpolateOptions) (InterpolateResult[T], error) {
	var zero InterpolateResult[T]
	call, err := bind.Begin(ctx, "exp.interpolate")
	if err != nil {
		return zero, err
	}
	o := mergeInterpolateOptions(options)
	if err := refuseExpEmpty(&call, "items", len(items)); err != nil {
		return zero, err
	}
	c, err := interpolateCommon(o)
	if err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		res, err := ops.Interpolate[T](items, interpolateInternal(call.Context(), o, c))
		return interpolateResult(res), call.End(nil, err)
	}
	res, err := bind.Recorded(&call, types.ContractJSONWellFormed, func(ctx context.Context) (ops.InterpolateResult[T], error) {
		return ops.Interpolate[T](items, interpolateInternal(ctx, o, c))
	})
	return interpolateResult(res), err
}

func interpolateResult[T any](res ops.InterpolateResult[T]) InterpolateResult[T] {
	out := InterpolateResult[T]{Complete: res.Complete, GapCount: res.GapCount, Method: res.Method, AverageConfidence: res.AverageConfidence}
	if len(res.Filled) != 0 {
		out.Filled = make([]FilledItem, len(res.Filled))
		for i, f := range res.Filled {
			out.Filled[i] = FilledItem{Index: f.Index, Method: f.Method, BasedOn: f.BasedOn, Reasoning: f.Reasoning, ModelConfidence: f.ModelConfidence}
		}
	}
	return out
}
