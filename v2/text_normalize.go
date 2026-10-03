package schemaflux

import (
	"context"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
)

// NormalizeOptions are the settings of Normalize. The zero value is the default:
// fix typos, normalize spacing, change nothing else.
//
// The common settings of Options come first and are set without naming the
// embedded type: NormalizeOptions{Model: "gpt-6-luna", Case: "lower"}.
type NormalizeOptions struct {
	Options

	// Standard names a standard to normalize to: "ISO_8601", "US_POSTAL". It is a
	// request, and any name is passed to the model. Empty means none.
	Standard string

	// Rules give a rule for a field, keyed by field name. Nil means none.
	Rules map[string]string

	// CanonicalMappings replace one spelling with another. Nil means none.
	CanonicalMappings map[string]string

	// Case is "lower", "upper", "title" or "sentence". Empty leaves case alone. Any
	// other value is a configuration error.
	Case string

	// NoFixTypos withholds the instruction to fix spelling. False fixes it.
	NoFixTypos bool

	// NoNormalizeWhitespace withholds the instruction to normalize spacing. False
	// normalizes it.
	NoNormalizeWhitespace bool

	// Fields are the only top-level fields that may change. Enforced: an answer
	// that changes another is refused with ErrInvalidAnswer. Names are compared
	// without regard to case. Nil means all.
	Fields []string

	// SkipFields are top-level fields that must not change. Enforced: an answer
	// that changes one is refused with ErrInvalidAnswer. Nil means none.
	SkipFields []string

	// Locale is the locale to normalize for. Empty means unstated.
	Locale string

	// Cautious asks the model to leave a value alone, and say so, when it is not
	// sure. A request: Go cannot tell an uncertain normalization from a certain
	// one, so the answer is not checked against it. False does not ask.
	Cautious bool
}

// NormalizeResult is a normalized value with the model's account of what it
// changed and what actually differs.
type NormalizeResult[T any] struct {
	// Normalized is the normalized value.
	Normalized T `json:"normalized"`

	// Changes is the model's account of what it changed, and it is the model's
	// account: a reason for a change cannot be recovered from the values.
	Changes []NormalizeChange `json:"changes"`

	// ChangedFields names the top-level fields whose values differ between the
	// input and Normalized, worked out here. For a value that is not a struct or
	// a map it is "$" when the value changed. Nil when nothing differs.
	ChangedFields []string `json:"changed_fields,omitempty"`

	// Unreported names the fields that changed and that Changes does not mention.
	// A list that is not empty means the model's account is incomplete.
	Unreported []string `json:"unreported,omitempty"`

	// TotalChanges is len(ChangedFields).
	TotalChanges int `json:"total_changes"`

	// Warnings are the model's warnings. Nil when it gave none.
	Warnings []string `json:"warnings,omitempty"`
}

// NormalizeChange is one change as the model reported it.
type NormalizeChange struct {
	// Field is the field that changed.
	Field string `json:"field"`

	// Original is its value before.
	Original string `json:"original"`

	// Normalized is its value after.
	Normalized string `json:"normalized"`

	// Reason is why, in the model's words. Empty when it gave none.
	Reason string `json:"reason,omitempty"`

	// ModelConfidence is the model's own claim about the change, from 0 to 1. It
	// is not a measurement.
	ModelConfidence float64 `json:"confidence,omitempty"`
}

// mergeNormalizeOptions merges the values of a call (M0).
func mergeNormalizeOptions(opts []NormalizeOptions) NormalizeOptions {
	return mergeValues(opts, func(o, later NormalizeOptions) NormalizeOptions {
		o.Options = o.Options.merge(later.Options)
		lay(&o.Standard, later.Standard)
		layMap(&o.Rules, later.Rules)
		layMap(&o.CanonicalMappings, later.CanonicalMappings)
		lay(&o.Case, later.Case)
		lay(&o.NoFixTypos, later.NoFixTypos)
		lay(&o.NoNormalizeWhitespace, later.NoNormalizeWhitespace)
		laySlice(&o.Fields, later.Fields)
		laySlice(&o.SkipFields, later.SkipFields)
		lay(&o.Locale, later.Locale)
		lay(&o.Cautious, later.Cautious)
		return o
	})
}

// checkNormalizeOptions returns the internal error of a setting no normalization
// can use, or nil. op is the operation the error names: the function that was
// called.
func checkNormalizeOptions(op string, o NormalizeOptions) error {
	if err := o.common().Check(op); err != nil {
		return err
	}
	switch o.Case {
	case "", "lower", "upper", "title", "sentence":
		return nil
	}
	return types.Newf(types.KindConfiguration, op, "Case is not one of lower, upper, title and sentence")
}

// normalizeInternal builds the internal options of a normalization on ctx: the
// constructor's value, then each setting that is not its zero value.
func normalizeInternal(ctx context.Context, o NormalizeOptions) ops.NormalizeOptions {
	opts := ops.NewNormalizeOptions()
	lay(&opts.Standard, o.Standard)
	layMap(&opts.Rules, o.Rules)
	turnOff(&opts.FixTypos, o.NoFixTypos)
	lay(&opts.NormalizeCase, o.Case)
	turnOff(&opts.NormalizeWhitespace, o.NoNormalizeWhitespace)
	layMap(&opts.CanonicalMappings, o.CanonicalMappings)
	laySlice(&opts.Fields, o.Fields)
	laySlice(&opts.SkipFields, o.SkipFields)
	lay(&opts.Locale, o.Locale)
	lay(&opts.Strict, o.Cautious)
	settleHalves(ctx, o.common(), &opts.CommonOptions, &opts.OpOptions)
	return opts
}

// normalizeResult is the result of the internal call as the root declares it. The
// internal metadata map is never written for a normalization and is not carried
// over. The zero result, which the internal call returns beside an error,
// converts to the zero result, and a list that is empty stays nil.
func normalizeResult[T any](r ops.NormalizeResult[T]) NormalizeResult[T] {
	out := NormalizeResult[T]{
		Normalized: r.Normalized, ChangedFields: r.ChangedFields, Unreported: r.Unreported,
		TotalChanges: r.TotalChanges, Warnings: r.Warnings,
	}
	if r.Changes != nil {
		out.Changes = make([]NormalizeChange, len(r.Changes))
		for i, c := range r.Changes {
			out.Changes[i] = NormalizeChange(c)
		}
	}
	return out
}

// Normalize puts a value into a consistent form: dates, phone numbers, case,
// spacing, spelling, a named standard. It reports what the model says it changed
// and what actually differs. Fields and SkipFields are enforced against what
// differs, and an answer that breaks one is an error of kind ErrInvalidAnswer;
// the rest of the options are requests. Default tier Fast, mode Transform.
//
//	res, err := schemaflux.Normalize(ctx, address)
//	clean, err := schemaflux.Normalize(ctx, "  hELLO   wORLD ", schemaflux.NormalizeOptions{Case: "sentence"})
//
// To normalize several values, call it for each one with Each.
//
// The Record of the call is "normalize". The model is asked for a declared schema
// of the value's type and the answer is delivered at ContractSchemaConstrained
// when a schema can be built for T. For a T none can (a map, a recursive type, an
// interface, a type nested deeper than five levels, since the answer holds it
// inside an envelope) the call is made without a schema and is delivered at
// ContractJSONWellFormed; a floor above that level refuses it before anything is
// sent.
func Normalize[T any](ctx context.Context, input T, opts ...NormalizeOptions) (NormalizeResult[T], error) {
	var zero NormalizeResult[T]
	call, err := bind.Begin(ctx, "normalize")
	if err != nil {
		return zero, err
	}
	o := mergeNormalizeOptions(opts)
	if err := checkNormalizeOptions("normalize", o); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		res, err := ops.Normalize(input, normalizeInternal(call.Context(), o))
		return normalizeResult(res), call.End(nil, err)
	}
	res, err := bind.Recorded(&call, ops.ContractLevelFor[T]("normalize"), func(ctx context.Context) (ops.NormalizeResult[T], error) {
		return ops.Normalize(input, normalizeInternal(ctx, o))
	})
	return normalizeResult(res), err
}
