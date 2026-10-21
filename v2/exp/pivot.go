package exp

import (
	"context"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// PivotOptions configures Pivot. Its zero value uses the first aggregation,
// Smart tier and Transform mode.
type PivotOptions struct {
	schemaflux.Options
	// PivotOn names the source fields whose values become the new axis; nil
	// leaves the axis to the model.
	PivotOn []string
	// Aggregate is first, last, sum, avg, list or concat; empty means first.
	Aggregate string
	// GroupBy names fields used to group rows before pivoting; nil asks for
	// no grouping.
	GroupBy []string
	// Flatten asks for a flat key-value result; false leaves nesting to the model.
	Flatten bool
}

// PivotMapping is the model's report of one relocated field.
type PivotMapping struct {
	// SourcePath is the original location.
	SourcePath string `json:"source_path"`
	// TargetPath is the new location.
	TargetPath string `json:"target_path"`
	// Transformation describes a change to the value, when present.
	Transformation string `json:"transformation,omitempty"`
	// Aggregation describes the aggregation applied, when present.
	Aggregation string `json:"aggregation,omitempty"`
}

// PivotStats is the model's unverified account of the transformation.
type PivotStats struct {
	// SourceFields is the reported source field count.
	SourceFields int `json:"source_fields"`
	// TargetFields is the reported target field count.
	TargetFields int `json:"target_fields"`
	// Expansions is the reported number of one-to-many changes.
	Expansions int `json:"expansions"`
	// Compressions is the reported number of many-to-one changes.
	Compressions int `json:"compressions"`
	// DepthChange is the reported change in nesting depth.
	DepthChange int `json:"depth_change"`
}

// PivotResult contains the pivoted value, the model's account, and a field-name
// comparison computed from the input and the raw answer.
type PivotResult[U any] struct {
	// Pivoted is the restructured value.
	Pivoted U `json:"pivoted"`
	// Mappings is the model's unverified account of field relocation.
	Mappings []PivotMapping `json:"mappings"`
	// Stats is the model's unverified account of transformation counts.
	Stats PivotStats `json:"stats"`
	// DataLoss lists source field names absent from the raw pivoted answer.
	DataLoss []string `json:"data_loss,omitempty"`
	// DataLossChecked reports whether both sides had comparable field names.
	DataLossChecked bool `json:"data_loss_checked"`
	// ModelClaimedDataLoss is the model's unverified loss claim.
	ModelClaimedDataLoss []string `json:"model_claimed_data_loss,omitempty"`
	// Metadata is reserved and is not populated.
	Metadata map[string]any `json:"metadata,omitempty"`
}

func mergePivotOptions(values []PivotOptions) PivotOptions {
	return mergeExpValues(values, func(out, later PivotOptions) PivotOptions {
		out.Options = mergeCommonOptions([]schemaflux.Options{out.Options, later.Options})
		if later.PivotOn != nil {
			out.PivotOn = later.PivotOn
		}
		if later.Aggregate != "" {
			out.Aggregate = later.Aggregate
		}
		if later.GroupBy != nil {
			out.GroupBy = later.GroupBy
		}
		if later.Flatten {
			out.Flatten = true
		}
		return out
	})
}

func pivotInternal(ctx context.Context, o PivotOptions, c bind.Common) ops.PivotOptions {
	in := ops.NewPivotOptions()
	in.PivotOn = o.PivotOn
	if o.Aggregate != "" {
		in.Aggregate = o.Aggregate
	}
	in.GroupBy = o.GroupBy
	in.Flatten = o.Flatten
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

// Pivot reshapes input T into U. It records exp.pivot at
// ContractJSONWellFormed because the response is parsed but the request does
// not declare a schema.
func Pivot[T any, U any](ctx context.Context, input T, options ...PivotOptions) (PivotResult[U], error) {
	var zero PivotResult[U]
	call, err := bind.Begin(ctx, "exp.pivot")
	if err != nil {
		return zero, err
	}
	o := mergePivotOptions(options)
	c, err := commonSettings(o.Options)
	if err != nil {
		return zero, call.End(nil, err)
	}
	if err := c.Check("exp.pivot"); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		res, err := ops.Pivot[T, U](input, pivotInternal(call.Context(), o, c))
		if endErr := call.End(nil, err); endErr != nil {
			return zero, endErr
		}
		return pivotResult(res), nil
	}
	res, err := bind.Recorded(&call, types.ContractJSONWellFormed, func(ctx context.Context) (ops.PivotResult[U], error) {
		return ops.Pivot[T, U](input, pivotInternal(ctx, o, c))
	})
	if err != nil {
		return zero, err
	}
	return pivotResult(res), nil
}

func pivotResult[U any](res ops.PivotResult[U]) PivotResult[U] {
	out := PivotResult[U]{Pivoted: res.Pivoted, DataLossChecked: res.DataLossChecked,
		Stats: PivotStats{SourceFields: res.Stats.SourceFields, TargetFields: res.Stats.TargetFields,
			Expansions: res.Stats.Expansions, Compressions: res.Stats.Compressions, DepthChange: res.Stats.DepthChange}}
	if len(res.Mappings) > 0 {
		out.Mappings = make([]PivotMapping, len(res.Mappings))
		for i, v := range res.Mappings {
			out.Mappings[i] = PivotMapping{SourcePath: v.SourcePath, TargetPath: v.TargetPath,
				Transformation: v.Transformation, Aggregation: v.Aggregation}
		}
	}
	if len(res.DataLoss) > 0 {
		out.DataLoss = append([]string(nil), res.DataLoss...)
	}
	if len(res.ModelClaimedDataLoss) > 0 {
		out.ModelClaimedDataLoss = append([]string(nil), res.ModelClaimedDataLoss...)
	}
	return out
}
