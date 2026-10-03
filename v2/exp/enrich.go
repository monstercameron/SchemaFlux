package exp

import (
	"context"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// EnrichOptions configures Enrich and EnrichInPlace. Zero values use the
// operation defaults; embedded Options supplies common call settings.
type EnrichOptions struct {
	schemaflux.Options
	// Fields names fields to derive. Nil means the model chooses.
	Fields []string
	// Rules gives derivation instructions by field. Nil means none.
	Rules map[string]string
	// Background supplies outside prompt material as JSON. Nil means none.
	Background map[string]any
	// Domain names the subject area. Empty means none.
	Domain string
	// ExcludeFields names fields the answer may not change. Nil means none.
	ExcludeFields []string
	// AllowChanges allows changing existing fields. False refuses changes.
	AllowChanges bool
	// IncludeConfidence asks Enrich for per-field model scores. False omits them;
	// EnrichInPlace refuses true because its return value cannot carry scores.
	IncludeConfidence bool
}

// EnrichResult contains the enriched value and computed and model-reported details.
type EnrichResult[U any] struct {
	// Enriched is the value returned by the model.
	Enriched U `json:"enriched"`
	// AddedFields is computed from the input and returned value.
	AddedFields []string `json:"added_fields"`
	// ModelClaimedAddedFields is the model's account of added fields.
	ModelClaimedAddedFields []string `json:"model_claimed_added_fields,omitempty"`
	// ModelConfidence contains the model's confidence claims, when requested.
	ModelConfidence map[string]float64 `json:"confidence,omitempty"`
	// Derivations contains the model's explanations by field.
	Derivations map[string]string `json:"derivations,omitempty"`
}

func mergeEnrichOptions(values []EnrichOptions) EnrichOptions {
	return mergeExpValues(values, func(o, later EnrichOptions) EnrichOptions {
		o.Options = mergeCommonOptions([]schemaflux.Options{o.Options, later.Options})
		if later.Fields != nil {
			o.Fields = later.Fields
		}
		if later.Rules != nil {
			o.Rules = later.Rules
		}
		if later.Background != nil {
			o.Background = later.Background
		}
		if later.Domain != "" {
			o.Domain = later.Domain
		}
		if later.ExcludeFields != nil {
			o.ExcludeFields = later.ExcludeFields
		}
		if later.AllowChanges {
			o.AllowChanges = true
		}
		if later.IncludeConfidence {
			o.IncludeConfidence = true
		}
		return o
	})
}

func enrichCommon(o EnrichOptions, inplace bool) (bind.Common, error) {
	c, err := commonSettings(o.Options)
	if err != nil {
		return bind.Common{}, err
	}
	if err := c.Check("exp.enrich"); err != nil {
		return bind.Common{}, err
	}
	if inplace && o.IncludeConfidence {
		return bind.Common{}, types.Newf(types.KindConfiguration, "enrich", "IncludeConfidence is not available for EnrichInPlace")
	}
	return c, nil
}

func enrichInternal(ctx context.Context, o EnrichOptions, c bind.Common) ops.EnrichOptions {
	in := ops.NewEnrichOptions()
	in.DeriveFields = o.Fields
	in.DerivationRules = o.Rules
	in.Context = o.Background
	in.Domain = o.Domain
	in.ExcludeFields = o.ExcludeFields
	in.AddOnly = !o.AllowChanges
	in.IncludeConfidence = o.IncludeConfidence
	applyFlatCommon(ctx, c, &in.CommonOptions, &in.OpOptions)
	return in
}

// Enrich derives fields and returns a value of U. Default tier Fast, mode Transform.
// It records exp.enrich at ContractWellFormed: the answer is checked as JSON but no schema is sent.
func Enrich[U any, T any](ctx context.Context, input T, options ...EnrichOptions) (EnrichResult[U], error) {
	var zero EnrichResult[U]
	call, err := bind.Begin(ctx, "exp.enrich")
	if err != nil {
		return zero, err
	}
	o := mergeEnrichOptions(options)
	c, err := enrichCommon(o, false)
	if err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		res, err := ops.Enrich[T, U](input, enrichInternal(call.Context(), o, c))
		return EnrichResult[U]{Enriched: res.Enriched, AddedFields: res.AddedFields, ModelClaimedAddedFields: res.ModelClaimedAddedFields, ModelConfidence: res.ModelConfidence, Derivations: res.Derivations}, call.End(nil, err)
	}
	res, err := bind.Recorded(&call, types.ContractJSONWellFormed, func(ctx context.Context) (ops.EnrichResult[U], error) {
		return ops.Enrich[T, U](input, enrichInternal(ctx, o, c))
	})
	return EnrichResult[U]{Enriched: res.Enriched, AddedFields: res.AddedFields, ModelClaimedAddedFields: res.ModelClaimedAddedFields, ModelConfidence: res.ModelConfidence, Derivations: res.Derivations}, err
}

// EnrichInPlace fills fields and returns the original type. IncludeConfidence is refused.
// It records exp.enrichinplace at ContractWellFormed without sending a schema.
func EnrichInPlace[T any](ctx context.Context, input T, options ...EnrichOptions) (T, error) {
	var zero T
	call, err := bind.Begin(ctx, "exp.enrichinplace")
	if err != nil {
		return zero, err
	}
	o := mergeEnrichOptions(options)
	c, err := enrichCommon(o, true)
	if err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		res, err := ops.EnrichInPlace(input, enrichInternal(call.Context(), o, c))
		return res, call.End(nil, err)
	}
	res, err := bind.Recorded(&call, types.ContractJSONWellFormed, func(ctx context.Context) (T, error) {
		return ops.EnrichInPlace(input, enrichInternal(ctx, o, c))
	})
	return res, err
}
