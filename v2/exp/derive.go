package exp

import (
	"context"
	"math"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// DeriveOptions configures Derive. Its zero value derives all fields and uses a 0.6 confidence floor.
type DeriveOptions struct {
	schemaflux.Options
	// Fields are the output fields to derive; nil means all fields.
	Fields []string
	// Rules gives derivation guidance keyed by output field; nil means no field-specific rules.
	Rules map[string]string
	// MinConfidence is the minimum accepted model claim, from 0 to 1. Zero means 0.6; a negative value disables the floor. A value outside 0 to 1 is a configuration error.
	MinConfidence float64
	// OmitReasoning leaves per-field reasoning out of the request. False asks for reasoning.
	OmitReasoning bool
}

// DeriveResult contains the derived value and model-reported explanations and confidence claims.
type DeriveResult[U any] struct {
	// Derived is the value inferred from the input.
	Derived U `json:"derived"`
	// Derivations describes how output fields were derived.
	Derivations []Derivation `json:"derivations,omitempty"`
	// FieldConfidence contains the model's own confidence claim for each field.
	FieldConfidence map[string]float64 `json:"field_confidence"`
	// ModelOverallConfidence is the model's own overall confidence claim.
	ModelOverallConfidence float64 `json:"overall_confidence"`
}

// Derivation describes the model's claimed source and method for an output field.
type Derivation struct {
	// Field names the derived field.
	Field string `json:"field"`
	// SourceFields lists input fields used in the derivation.
	SourceFields []string `json:"source_fields,omitempty"`
	// Method describes the method the model says it used.
	Method string `json:"method"`
	// Reasoning is the model's explanation when requested.
	Reasoning string `json:"reasoning,omitempty"`
	// ModelConfidence is the model's own confidence claim for this derivation.
	ModelConfidence float64 `json:"confidence"`
}

func mergeDeriveOptions(v []DeriveOptions) DeriveOptions {
	return mergeExpValues(v, func(a, b DeriveOptions) DeriveOptions {
		a.Options = mergeCommonOptions([]schemaflux.Options{a.Options, b.Options})
		if b.Fields != nil {
			a.Fields = b.Fields
		}
		if b.Rules != nil {
			a.Rules = b.Rules
		}
		if b.MinConfidence != 0 {
			a.MinConfidence = b.MinConfidence
		}
		if b.OmitReasoning {
			a.OmitReasoning = true
		}
		return a
	})
}

func checkDeriveOptions(o DeriveOptions) error {
	if math.IsNaN(o.MinConfidence) || math.IsInf(o.MinConfidence, 0) || o.MinConfidence > 1 {
		return types.Newf(types.KindConfiguration, "exp.derive", "MinConfidence is outside its allowed range")
	}
	return nil
}

func deriveInternal[T, U any](ctx context.Context, input T, o DeriveOptions, c bind.Common) ops.DeriveOptions {
	in := ops.NewDeriveOptions()
	if o.Fields != nil {
		in.Fields = o.Fields
	}
	if o.Rules != nil {
		in.Rules = o.Rules
	}
	if o.MinConfidence < 0 {
		in.MinConfidence = math.SmallestNonzeroFloat64
	} else if o.MinConfidence > 0 {
		in.MinConfidence = o.MinConfidence
	}
	in.IncludeReasoning = !o.OmitReasoning
	in.Context = ctx
	common := types.OpOptions{Model: in.Model, Intelligence: in.Intelligence, Effort: in.Effort, Mode: in.Mode, Steering: in.Steering, MaxOutputTokens: in.MaxOutputTokens, WebSearch: in.WebSearch}
	c.Apply(&common)
	in.Model = common.Model
	in.Intelligence = common.Intelligence
	in.Effort = common.Effort
	in.Mode = common.Mode
	in.Steering = common.Steering
	in.MaxOutputTokens = common.MaxOutputTokens
	in.WebSearch = common.WebSearch
	return in
}

func deriveResult[U any](r ops.DeriveResult[U]) DeriveResult[U] {
	if len(r.Derivations) == 0 {
		return DeriveResult[U]{Derived: r.Derived, FieldConfidence: r.FieldConfidence, ModelOverallConfidence: r.ModelOverallConfidence}
	}
	out := DeriveResult[U]{Derived: r.Derived, FieldConfidence: r.FieldConfidence, ModelOverallConfidence: r.ModelOverallConfidence}
	if len(r.Derivations) > 0 {
		out.Derivations = make([]Derivation, len(r.Derivations))
		for i, d := range r.Derivations {
			out.Derivations[i] = Derivation{Field: d.Field, SourceFields: d.SourceFields, Method: d.Method, Reasoning: d.Reasoning, ModelConfidence: d.ModelConfidence}
		}
	}
	return out
}

// Derive infers a typed value from input. It enforces the configured confidence floor. Default tier Fast, mode Transform.
// The Record is "exp.derive" and is delivered at ContractJSONWellFormed.
func Derive[U any, T any](ctx context.Context, input T, options ...DeriveOptions) (DeriveResult[U], error) {
	var zero DeriveResult[U]
	call, err := bind.Begin(ctx, "exp.derive")
	if err != nil {
		return zero, err
	}
	o := mergeDeriveOptions(options)
	if err := checkDeriveOptions(o); err != nil {
		return zero, call.End(nil, err)
	}
	c, err := commonSettings(o.Options)
	if err != nil {
		return zero, call.End(nil, err)
	}
	if err = c.Check("exp.derive"); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		r, e := ops.Derive[T, U](input, deriveInternal[T, U](call.Context(), input, o, c))
		return deriveResult(r), call.End(nil, e)
	}
	r, e := bind.Recorded(&call, types.ContractJSONWellFormed, func(ctx context.Context) (ops.DeriveResult[U], error) {
		return ops.Derive[T, U](input, deriveInternal[T, U](ctx, input, o, c))
	})
	return deriveResult(r), e
}
