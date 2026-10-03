package schemaflux

import (
	"context"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
)

// ExtractOptions are Extract's settings. The zero value is the default.
//
// The common settings of Options come first and are set without naming the
// embedded type: ExtractOptions{Mode: ModeStrict, StrictSchema: true}.
type ExtractOptions struct {
	Options

	// SchemaHints describes fields in plain language, keyed by field name. Nil
	// means none.
	SchemaHints map[string]string

	// FieldRules gives a rule for a field, keyed by field name. Nil means none.
	FieldRules map[string]string

	// Examples are sample outputs the model is shown, written as JSON. Nil means
	// none.
	Examples []any

	// StrictSchema tells the model that every field must be present and valid.
	// False tells it, when any hint, rule or example is given, that a partial
	// extraction is allowed.
	StrictSchema bool

	// NoPartial withholds the sentence that allows a partial extraction without
	// asking for a strict one. False leaves it in. It has an effect only when a
	// hint, a rule or an example is given.
	NoPartial bool

	// ExactFields refuses an answer with a property T does not name. False
	// tolerates one. ModeStrict implies it.
	ExactFields bool

	// CompleteFields refuses an answer that leaves a required field empty. False
	// tolerates one. ModeStrict implies it.
	CompleteFields bool
}

// mergeExtractOptions merges the values of a call (M0).
func mergeExtractOptions(opts []ExtractOptions) ExtractOptions {
	return mergeValues(opts, func(o, later ExtractOptions) ExtractOptions {
		o.Options = o.Options.merge(later.Options)
		layMap(&o.SchemaHints, later.SchemaHints)
		layMap(&o.FieldRules, later.FieldRules)
		laySlice(&o.Examples, later.Examples)
		lay(&o.StrictSchema, later.StrictSchema)
		lay(&o.NoPartial, later.NoPartial)
		lay(&o.ExactFields, later.ExactFields)
		lay(&o.CompleteFields, later.CompleteFields)
		return o
	})
}

// extractInternal builds the internal options of an Extract call on ctx: the
// constructor's value, then each setting that is not its zero value, then the
// common settings.
func extractInternal(ctx context.Context, o ExtractOptions) ops.ExtractOptions {
	opts := ops.NewExtractOptions()
	layMap(&opts.SchemaHints, o.SchemaHints)
	layMap(&opts.FieldRules, o.FieldRules)
	laySlice(&opts.Examples, o.Examples)
	lay(&opts.StrictSchema, o.StrictSchema)
	// A strict schema and a partial extraction contradict each other, and the
	// internal options refuse the pair: StrictSchema implies NoPartial.
	turnOff(&opts.AllowPartial, o.StrictSchema || o.NoPartial)
	lay(&opts.CommonOptions.ExactFields, o.ExactFields)
	lay(&opts.CommonOptions.CompleteFields, o.CompleteFields)
	settleHalves(ctx, o.common(), &opts.CommonOptions, &opts.OpOptions)
	return opts
}

// Extract reads input into a T. The input is any value: text, JSON, a struct.
// An answer that does not fit T is an error, never a partly filled T: it is
// refused with ErrInvalidAnswer, after the model has been sent the refused
// answer back once to correct (Client.Repairs). Default tier Fast, mode
// Transform.
//
//	invoice, err := schemaflux.Extract[Invoice](ctx, emailBody)
//	if errors.Is(err, schemaflux.ErrInvalidAnswer) { ... }
//
// The Record of the call is "extract". It asks the model for a declared schema
// and delivers at ContractSchemaConstrained, or at ContractSchemaAndInvariantChecked
// with ModeStrict, ExactFields or CompleteFields. That is for a T a strict schema
// can describe. For a T none can (a map, a recursive type, an interface, a type
// nested deeper than six levels) no schema is sent, and the call is delivered at
// ContractJSONWellFormed whatever the options: a floor above that level refuses
// it before anything is sent.
func Extract[T any](ctx context.Context, input any, opts ...ExtractOptions) (T, error) {
	var zero T
	call, err := bind.Begin(ctx, "extract")
	if err != nil {
		return zero, err
	}
	o := mergeExtractOptions(opts)
	if err := o.common().Check("extract"); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		value, err := ops.Extract[T](input, extractInternal(call.Context(), o))
		return value, call.End(nil, err)
	}
	// The twin sends the same request and reports what the Record carries:
	// the checks, the repairs, the provenance and the level actually delivered.
	res, err := ops.ExtractResult[T](input, extractInternal(call.Context(), o))
	return res.Value, call.End(&res.Meta, err)
}

// TransformOptions are Transform's settings. The zero value is the default.
//
// The common settings of Options come first and are set without naming the
// embedded type: TransformOptions{Mode: ModeStrict, Logic: "uppercase the name"}.
type TransformOptions struct {
	Options

	// MappingRules says where a target field comes from, keyed by target field.
	// Nil means none.
	MappingRules map[string]string

	// PreserveFields names fields to carry over unchanged. Nil means none.
	PreserveFields []string

	// MergeStrategy is "replace", "merge" or "append". Empty means "replace".
	// Any other value is a configuration error.
	MergeStrategy string

	// Logic describes the transformation in plain language. Empty means none.
	Logic string
}

// mergeTransformOptions merges the values of a call (M0).
func mergeTransformOptions(opts []TransformOptions) TransformOptions {
	return mergeValues(opts, func(o, later TransformOptions) TransformOptions {
		o.Options = o.Options.merge(later.Options)
		layMap(&o.MappingRules, later.MappingRules)
		laySlice(&o.PreserveFields, later.PreserveFields)
		lay(&o.MergeStrategy, later.MergeStrategy)
		lay(&o.Logic, later.Logic)
		return o
	})
}

// checkTransformOptions returns the internal error of a setting no call can
// use, or nil. The merge strategy is named in the message and never repeated:
// the error goes wherever the caller's logs go.
func checkTransformOptions(o TransformOptions) error {
	if err := o.common().Check("transform"); err != nil {
		return err
	}
	switch o.MergeStrategy {
	case "", "replace", "merge", "append":
		return nil
	}
	return types.Newf(types.KindConfiguration, "transform", "MergeStrategy is not one of replace, merge and append")
}

// transformInternal builds the internal options of a Transform call on ctx.
func transformInternal(ctx context.Context, o TransformOptions) ops.TransformOptions {
	opts := ops.NewTransformOptions()
	layMap(&opts.MappingRules, o.MappingRules)
	laySlice(&opts.PreserveFields, o.PreserveFields)
	lay(&opts.MergeStrategy, o.MergeStrategy)
	lay(&opts.TransformLogic, o.Logic)
	settleHalves(ctx, o.common(), &opts.CommonOptions, &opts.OpOptions)
	return opts
}

// Transform turns a value of one type into a value of another: U is the type
// asked for, and the input's type is inferred. Default tier Fast, mode
// Transform.
//
//	summary, err := schemaflux.Transform[OrderSummary](ctx, order)
//	dto, err := schemaflux.Transform[UserDTO](ctx, user, schemaflux.TransformOptions{PreserveFields: []string{"id"}})
//
// An answer that does not fit U is an error, never a partly filled U. The Record
// of the call is "transform". It is delivered at ContractSchemaConstrained when
// a schema can be built for U, which is declared to the provider, and at
// ContractJSONWellFormed when it cannot (a map, a recursive type, an interface, a
// type nested deeper than six levels): no schema is sent then and the answer is
// only read as JSON. A floor above the level of U refuses the call before
// anything is sent.
func Transform[U any, T any](ctx context.Context, input T, opts ...TransformOptions) (U, error) {
	var zero U
	call, err := bind.Begin(ctx, "transform")
	if err != nil {
		return zero, err
	}
	o := mergeTransformOptions(opts)
	if err := checkTransformOptions(o); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		value, err := ops.Transform[T, U](input, transformInternal(call.Context(), o))
		return value, call.End(nil, err)
	}
	return bind.Recorded(&call, ops.ContractLevelFor[U]("transform"), func(ctx context.Context) (U, error) {
		return ops.Transform[T, U](input, transformInternal(ctx, o))
	})
}

// GenerateOptions are Generate's settings. The zero value is the default.
//
// The common settings of Options come first and are set without naming the
// embedded type: GenerateOptions{Mode: ModeStrict, Style: "dry and brief"}.
type GenerateOptions struct {
	Options

	// Template is a pattern the output should follow. Empty means none.
	Template string

	// Style is a style to write in. Empty means none.
	Style string

	// Constraints are requirements stated as name and value, written into the
	// request in key order. Nil means none.
	Constraints map[string]any

	// SeedData is a value to base the output on, written as JSON. Nil means none.
	// A value that cannot be written as JSON is a configuration error.
	SeedData any

	// Examples are sample outputs, written as JSON. Nil means none. A value that
	// cannot be written as JSON is a configuration error.
	Examples []any

	// EnsureUnique tells the model not to repeat a value of the seed data or the
	// examples. False says nothing about it.
	EnsureUnique bool
}

// mergeGenerateOptions merges the values of a call (M0).
func mergeGenerateOptions(opts []GenerateOptions) GenerateOptions {
	return mergeValues(opts, func(o, later GenerateOptions) GenerateOptions {
		o.Options = o.Options.merge(later.Options)
		lay(&o.Template, later.Template)
		lay(&o.Style, later.Style)
		layMap(&o.Constraints, later.Constraints)
		if later.SeedData != nil {
			o.SeedData = later.SeedData
		}
		laySlice(&o.Examples, later.Examples)
		lay(&o.EnsureUnique, later.EnsureUnique)
		return o
	})
}

// generateInternal builds the internal options of a Generate call on ctx. The
// constructor's Count of 1 stays: it is the only value the internal call runs.
func generateInternal(ctx context.Context, o GenerateOptions) ops.GenerateOptions {
	opts := ops.NewGenerateOptions()
	lay(&opts.Template, o.Template)
	lay(&opts.Style, o.Style)
	layMap(&opts.Constraints, o.Constraints)
	if o.SeedData != nil {
		opts.SeedData = o.SeedData
	}
	laySlice(&opts.Examples, o.Examples)
	lay(&opts.EnsureUnique, o.EnsureUnique)
	settleHalves(ctx, o.common(), &opts.CommonOptions, &opts.OpOptions)
	return opts
}

// Generate makes a T from a prompt. With T a string the answer is the model's
// text as it came; any other T is decoded and refused if it does not fit.
// Default tier Fast, mode Creative.
//
//	bio, err := schemaflux.Generate[string](ctx, "Write a two-line bio for a Go developer")
//	plan, err := schemaflux.Generate[MealPlan](ctx, "A vegetarian week", schemaflux.GenerateOptions{WebSearch: true})
//
// The prompt with the clauses its options add is at most 10000 characters: a
// longer one is refused with ErrTooLarge, and nothing is cut. A blank prompt is
// an error of kind ErrInvalidRequest.
//
// The Record of the call is "generate". It is delivered at ContractPromptOnly
// for a string, which no floor above that level admits; at
// ContractSchemaConstrained when a schema can be built for T, which is declared
// to the provider; and at ContractJSONWellFormed when it cannot (a map, a
// recursive type, an interface, a type nested deeper than six levels): no schema
// is sent then. A floor above the level of T refuses the call before anything is
// sent.
func Generate[T any](ctx context.Context, prompt string, opts ...GenerateOptions) (T, error) {
	var zero T
	call, err := bind.Begin(ctx, "generate")
	if err != nil {
		return zero, err
	}
	o := mergeGenerateOptions(opts)
	if err := o.common().Check("generate"); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		value, err := ops.Generate[T](prompt, generateInternal(call.Context(), o))
		return value, call.End(nil, err)
	}
	// The twin sends the same request, and it holds the level of the type
	// argument (ops.ContractLevelFor): prompt only for a string, schema
	// constrained for a type that has a schema, well formed for one that has none.
	res, err := ops.GenerateResult[T](prompt, generateInternal(call.Context(), o))
	return res.Value, call.End(&res.Meta, err)
}

// InferOptions are Infer's settings. The zero value is the default.
//
// The common settings of Options come first and are set without naming the
// embedded type: InferOptions{Mode: ModeStrict, Background: "UK customers only"}.
type InferOptions struct {
	Options

	// Background is prose the model should take into account. Empty means none.
	Background string
}

// mergeInferOptions merges the values of a call (M0).
func mergeInferOptions(opts []InferOptions) InferOptions {
	return mergeValues(opts, func(o, later InferOptions) InferOptions {
		o.Options = o.Options.merge(later.Options)
		lay(&o.Background, later.Background)
		return o
	})
}

// inferInternal builds the internal options of an Infer call on ctx.
func inferInternal(ctx context.Context, o InferOptions) ops.InferOptions {
	opts := ops.NewInferOptions()
	lay(&opts.Background, o.Background)
	o.common().Apply(&opts.OpOptions)
	opts.OpOptions.Context = ctx
	return opts
}

// Infer fills the empty fields of a value and returns it. A field the caller
// filled comes back unchanged or the answer is refused. Default tier Fast, mode
// Transform.
//
//	full, err := schemaflux.Infer(ctx, Contact{Email: "ada@example.com"})
//	full, err := schemaflux.Infer(ctx, c, schemaflux.InferOptions{Background: "UK customers only"})
//
// An answer that changes a field the caller filled, or leaves a field of the
// type out, is an error of kind ErrInvalidAnswer. Nothing in the result marks
// which fields were inferred: compare it with what was passed in.
//
// The Record of the call is "infer", delivered at ContractJSONWellFormed: the
// answer is parsed and checked against the value, but no schema is declared to
// the provider.
func Infer[T any](ctx context.Context, partial T, opts ...InferOptions) (T, error) {
	var zero T
	call, err := bind.Begin(ctx, "infer")
	if err != nil {
		return zero, err
	}
	o := mergeInferOptions(opts)
	if err := o.common().Check("infer"); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		value, err := ops.Infer(partial, inferInternal(call.Context(), o))
		return value, call.End(nil, err)
	}
	return bind.Recorded(&call, types.ContractJSONWellFormed, func(ctx context.Context) (T, error) {
		return ops.Infer(partial, inferInternal(ctx, o))
	})
}

// ParseOptions are Parse's settings. The zero value is the default.
//
// The seven common settings of Options have an effect only when AllowLLMFallback
// is set and the fallback runs: the parsers written in Go read no setting of
// them. They are checked all the same.
type ParseOptions struct {
	Options

	// AllowLLMFallback lets Parse ask a model when the local parsers cannot read
	// the input. False sends nothing anywhere.
	AllowLLMFallback bool

	// AutoFix asks the model, in the fallback, to repair malformed input. It does
	// nothing unless AllowLLMFallback is set.
	AutoFix bool

	// FormatHints name a format ("json", "xml", "csv", "yaml") or the columns of a
	// delimited record ("name|age|job"). Nil means the format is detected. A
	// hint with a "|" has to name fields of the target, or the call is refused as
	// a configuration error without asking a model.
	FormatHints []string

	// CustomDelimiters are tried in order on input no parser recognises. Nil
	// means none. An empty delimiter is a configuration error.
	CustomDelimiters []string
}

// ParseResult is what Parse read.
type ParseResult[T any] struct {
	// Data is the parsed value.
	Data T `json:"data"`

	// Format is the format that was read: "json", "xml", "csv", "tsv", "yaml",
	// "pipe-delimited", "custom-delimited" or "unknown"; with " (LLM-assisted)"
	// after it when a model read the input.
	Format string `json:"format"`
}

// mergeParseOptions merges the values of a call (M0).
func mergeParseOptions(opts []ParseOptions) ParseOptions {
	return mergeValues(opts, func(o, later ParseOptions) ParseOptions {
		o.Options = o.Options.merge(later.Options)
		lay(&o.AllowLLMFallback, later.AllowLLMFallback)
		lay(&o.AutoFix, later.AutoFix)
		laySlice(&o.FormatHints, later.FormatHints)
		laySlice(&o.CustomDelimiters, later.CustomDelimiters)
		return o
	})
}

// checkParseOptions returns the internal error of a setting no call can use, or
// nil. An empty delimiter would split every line into single characters.
func checkParseOptions(o ParseOptions) error {
	if err := o.common().Check("parse"); err != nil {
		return err
	}
	for i, delimiter := range o.CustomDelimiters {
		if delimiter == "" {
			return types.Newf(types.KindConfiguration, "parse", "CustomDelimiters[%d] is empty", i)
		}
	}
	return nil
}

// parseInternal builds the internal options of a Parse call on ctx.
func parseInternal(ctx context.Context, o ParseOptions) ops.ParseOptions {
	opts := ops.NewParseOptions()
	lay(&opts.AllowLLMFallback, o.AllowLLMFallback)
	lay(&opts.AutoFix, o.AutoFix)
	laySlice(&opts.FormatHints, o.FormatHints)
	laySlice(&opts.CustomDelimiters, o.CustomDelimiters)
	o.common().Apply(&opts.OpOptions)
	opts.OpOptions.Context = ctx
	return opts
}

// parseResult is the result of the internal call as the root declares it. The
// zero result, which the internal call returns beside an error, converts to the
// zero result.
func parseResult[T any](res ops.ParseResult[T]) ParseResult[T] {
	return ParseResult[T]{Data: res.Data, Format: res.Format}
}

// Parse reads structured text (JSON, XML, CSV, YAML, delimited) into a T with
// parsers written in Go, and reports the format it found. It asks a model only
// when AllowLLMFallback is set and no parser could read the input.
//
//	res, err := schemaflux.Parse[Person](ctx, `{"name":"Ada","age":36}`)
//	res, err := schemaflux.Parse[Person](ctx, "ADA|36", schemaflux.ParseOptions{FormatHints: []string{"name|age"}})
//	fmt.Println(res.Data.Name, res.Format)
//
// A blank input is an error of kind ErrInvalidRequest, and so is input that no
// parser reads when the fallback is not allowed.
//
// The Record of the call is "parse", delivered at ContractJSONWellFormed. A call
// the Go parsers read sends nothing and is held to the same level: a client
// whose DataPolicy sets a floor above it refuses the call all the same.
func Parse[T any](ctx context.Context, input any, opts ...ParseOptions) (ParseResult[T], error) {
	var zero ParseResult[T]
	call, err := bind.Begin(ctx, "parse")
	if err != nil {
		return zero, err
	}
	o := mergeParseOptions(opts)
	if err := checkParseOptions(o); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		res, err := ops.Parse[T](input, parseInternal(call.Context(), o))
		return parseResult(res), call.End(nil, err)
	}
	res, err := bind.Recorded(&call, types.ContractJSONWellFormed, func(ctx context.Context) (ops.ParseResult[T], error) {
		return ops.Parse[T](input, parseInternal(ctx, o))
	})
	return parseResult(res), err
}
