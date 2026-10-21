package exp

import (
	"context"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// ConformOptions configures Conform. Its zero value preserves unknown fields.
type ConformOptions struct {
	schemaflux.Options
	// Strict refuses a result that reports any violation. False returns violations.
	Strict bool
	// DropUnknown asks to remove fields outside the standard. False asks to preserve them.
	DropUnknown bool
	// CustomRules adds caller-defined conformance rules keyed by rule name. Nil means none.
	CustomRules map[string]string
}

// ConformResult contains the converted value and the model-reported adjustments and violations.
type ConformResult[T any] struct {
	// Conformed is the value transformed to meet the named standard.
	Conformed T `json:"conformed"`
	// Adjustments lists changes the model reports making.
	Adjustments []Adjustment `json:"adjustments,omitempty"`
	// Violations lists requirements the model reports it could not meet.
	Violations []string `json:"violations,omitempty"`
	// Compliance is the model's reported overall compliance score.
	Compliance float64 `json:"compliance"`
	// Standard names the standard applied.
	Standard string `json:"standard"`
}

// Adjustment describes a change the model reports making to conform data.
type Adjustment struct {
	// Field names the adjusted field.
	Field string `json:"field"`
	// OriginalValue is the value before adjustment.
	OriginalValue any `json:"original_value"`
	// ConformedValue is the value after adjustment.
	ConformedValue any `json:"conformed_value"`
	// Rule names the standard rule that required the adjustment.
	Rule string `json:"rule"`
	// Description explains the reported change.
	Description string `json:"description"`
}

func mergeConformOptions(values []ConformOptions) ConformOptions {
	return mergeExpValues(values, func(out, later ConformOptions) ConformOptions {
		out.Options = mergeCommonOptions([]schemaflux.Options{out.Options, later.Options})
		if later.Strict {
			out.Strict = true
		}
		if later.DropUnknown {
			out.DropUnknown = true
		}
		if later.CustomRules != nil {
			out.CustomRules = later.CustomRules
		}
		return out
	})
}

func conformCommon(o ConformOptions) (bind.Common, error) {
	c, err := commonSettings(o.Options)
	if err != nil {
		return bind.Common{}, err
	}
	if err := c.Check("exp.conform"); err != nil {
		return bind.Common{}, err
	}
	return c, nil
}

func conformInternal(ctx context.Context, o ConformOptions, c bind.Common) ops.ConformOptions {
	in := ops.NewConformOptions()
	in.Strict = o.Strict
	in.PreserveUnknown = !o.DropUnknown
	in.CustomRules = o.CustomRules
	in.Context = ctx
	in.Model = c.Model
	in.Intelligence = c.Tier
	in.Effort = c.Effort
	in.Mode = c.Mode
	in.Steering = c.Steering
	in.MaxOutputTokens = c.MaxOutputTokens
	in.WebSearch = c.WebSearch
	return in
}

// Conform rewrites a value to meet a named standard. An answer without a conformed value is an error. Default tier Fast, mode Transform.
func Conform[T any](ctx context.Context, input T, standard string, options ...ConformOptions) (ConformResult[T], error) {
	var zero ConformResult[T]
	call, err := bind.Begin(ctx, "exp.conform")
	if err != nil {
		return zero, err
	}
	o := mergeConformOptions(options)
	if err := refuseExpBlank(&call, "standard", standard); err != nil {
		return zero, err
	}
	c, err := conformCommon(o)
	if err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		res, err := ops.Conform[T](input, standard, conformInternal(call.Context(), o, c))
		return conformResult(res), call.End(nil, err)
	}
	res, err := bind.Recorded(&call, types.ContractJSONWellFormed, func(ctx context.Context) (ops.ConformResult[T], error) {
		return ops.Conform[T](input, standard, conformInternal(ctx, o, c))
	})
	return conformResult(res), err
}

func conformResult[T any](res ops.ConformResult[T]) ConformResult[T] {
	if len(res.Adjustments) == 0 && len(res.Violations) == 0 {
		return ConformResult[T]{Conformed: res.Conformed, Compliance: res.Compliance, Standard: res.Standard}
	}
	out := ConformResult[T]{Conformed: res.Conformed, Compliance: res.Compliance, Standard: res.Standard}
	if len(res.Adjustments) > 0 {
		out.Adjustments = make([]Adjustment, len(res.Adjustments))
		for i, a := range res.Adjustments {
			out.Adjustments[i] = Adjustment{Field: a.Field, OriginalValue: a.OriginalValue, ConformedValue: a.ConformedValue, Rule: a.Rule, Description: a.Description}
		}
	}
	if len(res.Violations) > 0 {
		out.Violations = res.Violations
	}
	return out
}
