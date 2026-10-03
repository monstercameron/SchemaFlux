package exp

import (
	"context"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// ProjectOptions configures Project. Empty mappings and exclusions leave the
// model to map by meaning and disclose all input fields, respectively.
type ProjectOptions struct {
	schemaflux.Options
	// Mappings maps source field names to target names; nil lets the model map by meaning.
	Mappings map[string]string
	// Exclude names source fields to withhold before serialization; nil excludes none.
	Exclude []string
	// InferMissing permits the model to claim it inferred a target field. False refuses such answers.
	InferMissing bool
}

// FieldMapping is the model's report of one source to target field mapping.
type FieldMapping struct {
	// SourceField is the original field name; empty means the model says the field was inferred.
	SourceField string `json:"source_field,omitempty"`
	// TargetField is the destination field name.
	TargetField string `json:"target_field"`
	// Method describes the reported mapping method.
	Method string `json:"method"`
	// Transformation describes a reported value transformation, when present.
	Transformation string `json:"transformation,omitempty"`
}

// ProjectResult contains the projected value and reported and computed field mappings.
type ProjectResult[U any] struct {
	// Projected is the transformed value.
	Projected U `json:"projected"`
	// Mappings is the model's account of how each field was mapped.
	Mappings []FieldMapping `json:"mappings"`
	// Lost lists source fields absent from the projected answer, computed by Go.
	Lost []string `json:"lost,omitempty"`
	// Inferred lists answer fields without a same-named source field, computed by Go.
	Inferred []string `json:"inferred,omitempty"`
	// FieldsCompared reports whether both values had comparable object field names.
	FieldsCompared bool `json:"fields_compared"`
	// ModelClaimedLost is the model's unverified claim about omitted source fields.
	ModelClaimedLost []string `json:"model_claimed_lost,omitempty"`
	// ModelClaimedInferred is the model's unverified claim about inferred fields.
	ModelClaimedInferred []string `json:"model_claimed_inferred,omitempty"`
	// ModelConfidence is the model's claimed confidence, from zero to one.
	ModelConfidence float64 `json:"confidence"`
}

func mergeProjectOptions(values []ProjectOptions) ProjectOptions {
	return mergeExpValues(values, func(out, later ProjectOptions) ProjectOptions {
		out.Options = mergeCommonOptions([]schemaflux.Options{out.Options, later.Options})
		if later.Mappings != nil {
			out.Mappings = later.Mappings
		}
		if later.Exclude != nil {
			out.Exclude = later.Exclude
		}
		if later.InferMissing {
			out.InferMissing = true
		}
		return out
	})
}

func projectCommon(o ProjectOptions) (bind.Common, error) {
	c, err := commonSettings(o.Options)
	if err != nil {
		return bind.Common{}, err
	}
	if err := c.Check("exp.project"); err != nil {
		return bind.Common{}, err
	}
	return c, nil
}

func projectInternal(ctx context.Context, o ProjectOptions, c bind.Common) ops.ProjectOptions {
	in := ops.NewProjectOptions()
	if o.Mappings != nil {
		in.Mappings = o.Mappings
	}
	if o.Exclude != nil {
		in.Exclude = o.Exclude
	}
	in.InferMissing = o.InferMissing
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

// Project reshapes input T into U and reports the model's mappings alongside
// field-name differences computed in Go. Default tier Fast and mode Transform.
// The call records exp.project at ContractJSONWellFormed because its JSON is
// parsed, but the request declares no schema.
func Project[T any, U any](ctx context.Context, input T, options ...ProjectOptions) (ProjectResult[U], error) {
	var zero ProjectResult[U]
	call, err := bind.Begin(ctx, "exp.project")
	if err != nil {
		return zero, err
	}
	o := mergeProjectOptions(options)
	c, err := projectCommon(o)
	if err != nil {
		return zero, call.End(nil, err)
	}
	res, err := bind.Recorded(&call, types.ContractJSONWellFormed, func(ctx context.Context) (ops.ProjectResult[U], error) {
		return ops.Project[T, U](input, projectInternal(ctx, o, c))
	})
	return projectResult(res), err
}

func projectResult[U any](res ops.ProjectResult[U]) ProjectResult[U] {
	out := ProjectResult[U]{Projected: res.Projected, FieldsCompared: res.FieldsCompared, ModelConfidence: res.ModelConfidence}
	if len(res.Mappings) > 0 {
		out.Mappings = make([]FieldMapping, len(res.Mappings))
		for i, v := range res.Mappings {
			out.Mappings[i] = FieldMapping{SourceField: v.SourceField, TargetField: v.TargetField, Method: v.Method, Transformation: v.Transformation}
		}
	}
	if len(res.Lost) > 0 {
		out.Lost = append([]string(nil), res.Lost...)
	}
	if len(res.Inferred) > 0 {
		out.Inferred = append([]string(nil), res.Inferred...)
	}
	if len(res.ModelClaimedLost) > 0 {
		out.ModelClaimedLost = append([]string(nil), res.ModelClaimedLost...)
	}
	if len(res.ModelClaimedInferred) > 0 {
		out.ModelClaimedInferred = append([]string(nil), res.ModelClaimedInferred...)
	}
	return out
}
