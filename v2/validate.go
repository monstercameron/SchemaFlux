package schemaflux

import (
	"context"
	"strings"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// ValidateOptions are Validate's settings. The zero value is the default.
//
// The common settings of Options come first and are set without naming the
// embedded type: ValidateOptions{Model: "gpt-6-luna", FieldRules: rules}.
type ValidateOptions struct {
	Options

	// Rules are requirements in plain language, for a model. Empty means none.
	Rules string

	// FieldRules give a rule for a field, keyed by field name. Nil means none.
	FieldRules map[string]string

	// SchemaHints describe fields for the model, keyed by field name. Nil means
	// none.
	SchemaHints map[string]string

	// Deterministic never calls a model: a rule Go cannot decide, any Rules and any
	// SchemaHints are a configuration error. False lets a model judge them. The
	// context and the seven common settings are not used for the call, since
	// nothing is sent; a bad one is still refused.
	Deterministic bool

	// FailOn is the least severity that fails the validation: "error", "warning" or
	// "info". Empty means "error". Any other value is a configuration error.
	FailOn string

	// OmitCorrection leaves the proposed correction out of the request. False asks
	// for one.
	OmitCorrection bool

	// OmitExplanations leaves the per-issue explanation out of the request. False
	// asks for it.
	OmitExplanations bool
}

// ValidateResult is a value's validation.
type ValidateResult[T any] struct {
	Judgment[T]

	// Corrected is the model's proposed fix: the data with the correction laid over
	// it. Nothing checked that it resolves the issues. Nil when none was proposed,
	// and when OmitCorrection is set.
	Corrected *T `json:"corrected,omitempty"`

	// DeterministicOnly reports that every rule was decided in Go and no model was
	// asked.
	DeterministicOnly bool `json:"deterministic_only,omitempty"`
}

// mergeValidateOptions merges the values of a call (M0).
func mergeValidateOptions(opts []ValidateOptions) ValidateOptions {
	return mergeValues(opts, func(o, later ValidateOptions) ValidateOptions {
		o.Options = o.Options.merge(later.Options)
		lay(&o.Rules, later.Rules)
		layMap(&o.FieldRules, later.FieldRules)
		layMap(&o.SchemaHints, later.SchemaHints)
		lay(&o.Deterministic, later.Deterministic)
		lay(&o.FailOn, later.FailOn)
		lay(&o.OmitCorrection, later.OmitCorrection)
		lay(&o.OmitExplanations, later.OmitExplanations)
		return o
	})
}

// checkValidateOptions returns the internal error of a setting no validation can
// use, or nil. Of a Deterministic call it also refuses what a model would have to
// judge, in words of this package: the internal call words the same refusals for
// its own names and, for a rule it cannot decide, lists the field names.
func checkValidateOptions(o ValidateOptions) error {
	if err := o.common().Check("validate"); err != nil {
		return err
	}
	if err := checkWordOf("validate", "FailOn", o.FailOn, "error", "warning", "info"); err != nil {
		return err
	}
	if o.Deterministic {
		switch {
		case strings.TrimSpace(o.Rules) != "":
			return types.Newf(types.KindConfiguration, "validate", "Deterministic is set and Rules is free text, which needs a model")
		case len(o.SchemaHints) > 0:
			return types.Newf(types.KindConfiguration, "validate", "Deterministic is set and SchemaHints needs a model")
		case len(o.FieldRules) == 0:
			return types.Newf(types.KindConfiguration, "validate", "there is nothing to validate against: give FieldRules")
		}
	}
	return nil
}

// validateInternal builds the internal options of a Validate call on ctx.
func validateInternal(ctx context.Context, o ValidateOptions) ops.ValidateOptions {
	opts := ops.NewValidateOptions()
	lay(&opts.Rules, o.Rules)
	layMap(&opts.SchemaHints, o.SchemaHints)
	layMap(&opts.FieldRules, o.FieldRules)
	lay(&opts.FailOn, o.FailOn)
	turnOff(&opts.AutoCorrect, o.OmitCorrection)
	turnOff(&opts.IncludeExplanations, o.OmitExplanations)
	settleHalves(ctx, o.common(), &opts.CommonOptions, &opts.OpOptions)
	return opts
}

// validateRun makes the internal call of a Validate: the one that decides every
// rule in Go when the caller asked for that, and the one that asks a model about
// what Go cannot decide otherwise.
func validateRun[T any](data T, in ops.ValidateOptions, deterministic bool) (ValidateResult[T], error) {
	var zero ValidateResult[T]
	if deterministic {
		j, err := ops.ValidateDeterministically(data, in)
		if err != nil {
			// Every other refusal of this call was made before it, so what is left
			// is a field rule Go cannot decide. The internal message lists the field
			// names, which are the caller's text, and names an internal function.
			return zero, types.Newf(types.KindConfiguration, "validate", "Deterministic is set and a rule in FieldRules is not one Go can decide")
		}
		res := validateResultOf(j)
		res.DeterministicOnly = true
		return res, nil
	}
	j, err := ops.ValidateHybrid(data, in)
	return validateResultOf(j), err
}

// validateResultOf is the result of the internal call as the root declares it.
// The two claims it carries are read with the comma-ok form: a call that proposed
// no correction has no entry for it, and a bare assertion would panic.
func validateResultOf[T any](j types.JudgmentResult[T]) ValidateResult[T] {
	res := ValidateResult[T]{Judgment: judgmentOf(j)}
	if corrected, ok := j.ModelClaims["corrected"].(*T); ok {
		res.Corrected = corrected
	}
	if only, ok := j.ModelClaims["deterministic_only"].(bool); ok {
		res.DeterministicOnly = only
	}
	return res
}

// Validate checks a value against rules. Field rules Go can decide exactly
// ("email", "min:18", "iso3166-alpha2") are decided in Go; a model is asked only
// about what is left, and not at all when nothing is. With nothing to validate
// against, which is no Rules, no FieldRules and no SchemaHints, the call is an
// error of kind ErrConfiguration. Default tier Fast, mode Strict.
//
//	res, err := schemaflux.Validate(ctx, signup, schemaflux.ValidateOptions{FieldRules: map[string]string{"Email": "email", "Age": "min:18"}})
//	if res.Verdict == schemaflux.VerdictFail { for _, i := range res.Issues { ... } }
//
// The verdict is worked out here from the issues and FailOn; the model is not
// asked for one. The Record of the call is "validate", delivered at
// ContractSchemaConstrained when the request declares a schema for the answer,
// which is read into a declared type, and nothing relational is checked beyond
// that. With a correction asked for (OmitCorrection false, the default) the
// schema holds the corrected value, so a T no strict schema can describe (a map,
// a recursive type, an interface, a type nested deeper than six levels) leaves
// the request without one: the call is then delivered at ContractJSONWellFormed,
// and a floor above that level refuses it before anything is sent. With
// OmitCorrection set the schema does not depend on T. A call with Deterministic
// set is held to the schema-constrained level though nothing is sent.
func Validate[T any](ctx context.Context, data T, opts ...ValidateOptions) (ValidateResult[T], error) {
	var zero ValidateResult[T]
	call, err := bind.Begin(ctx, "validate")
	if err != nil {
		return zero, err
	}
	o := mergeValidateOptions(opts)
	if err := checkValidateOptions(o); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		res, err := validateRun(data, validateInternal(call.Context(), o), o.Deterministic)
		return res, call.End(nil, err)
	}
	// The level follows the type only where the schema does: with a correction
	// asked for, and a model to ask. A deterministic call sends nothing and is
	// held to the row's level.
	level := ops.ValidateContract[T](!o.OmitCorrection && !o.Deterministic)
	return bind.Recorded(&call, level, func(ctx context.Context) (ValidateResult[T], error) {
		return validateRun(data, validateInternal(ctx, o), o.Deterministic)
	})
}
