package schemaflux

import (
	"context"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// CompleteOptions are the settings of Complete and CompleteField. The zero value
// is the default.
//
// The common settings of Options come first and are set without naming the
// embedded type: CompleteOptions{Mode: ModeStrict, MaxLength: 400}.
type CompleteOptions struct {
	Options

	// Background is earlier text the model should take into account. Nil means
	// none.
	Background []string

	// MaxLength is the most characters to add. Zero means 100. A negative value
	// is a configuration error.
	MaxLength int

	// StopSequences end the completion at the first place any of them occurs, and
	// are dropped. An empty one is ignored. Nil means none.
	StopSequences []string
}

// CompleteResult is a text and what was added to it.
type CompleteResult struct {
	// Text is the original followed by the completion.
	Text string `json:"text"`

	// Original is the text that was given.
	Original string `json:"original"`

	// Length is the number of characters added.
	Length int `json:"length"`
}

// CompleteFieldResult is a struct with one field completed.
type CompleteFieldResult[T any] struct {
	// Data is a shallow copy of the struct with the field completed.
	Data T `json:"data"`

	// Field is the Go name of the field that was completed.
	Field string `json:"field"`

	// Original is the field's value before.
	Original string `json:"original"`

	// Completed is the field's value after: the original followed by the
	// completion.
	Completed string `json:"completed"`

	// Length is the number of characters added.
	Length int `json:"length"`
}

// mergeCompleteOptions merges the values of a call (M0).
func mergeCompleteOptions(opts []CompleteOptions) CompleteOptions {
	return mergeValues(opts, func(o, later CompleteOptions) CompleteOptions {
		o.Options = o.Options.merge(later.Options)
		laySlice(&o.Background, later.Background)
		lay(&o.MaxLength, later.MaxLength)
		laySlice(&o.StopSequences, later.StopSequences)
		return o
	})
}

// checkCompleteOptions returns the internal error of a setting no call can use,
// or nil. op is the operation the error names: the function that was called.
func checkCompleteOptions(op string, o CompleteOptions) error {
	if err := o.common().Check(op); err != nil {
		return err
	}
	return checkCount(op, "MaxLength", o.MaxLength)
}

// completeInternal builds the internal options of a Complete or CompleteField
// call on ctx. The constructor's sampling settings stay at its values, so that
// the internal check of them passes: nothing sends them to a model.
func completeInternal(ctx context.Context, o CompleteOptions) ops.CompleteOptions {
	opts := ops.NewCompleteOptions()
	laySlice(&opts.Background, o.Background)
	lay(&opts.MaxLength, o.MaxLength)
	laySlice(&opts.StopSequences, o.StopSequences)
	o.common().Apply(&opts.OpOptions)
	opts.OpOptions.Context = ctx
	return opts
}

// completeResult is the result of the internal call as the root declares it. The
// zero result, which the internal call returns beside an error, converts to the
// zero result.
func completeResult(res ops.CompleteResult) CompleteResult {
	return CompleteResult{Text: res.Text, Original: res.Original, Length: res.Length}
}

// completeFieldResult is the result of the internal call as the root declares
// it. The zero result converts to the zero result.
func completeFieldResult[T any](res ops.CompleteFieldResult[T]) CompleteFieldResult[T] {
	return CompleteFieldResult[T]{Data: res.Data, Field: res.Field, Original: res.Original, Completed: res.Completed, Length: res.Length}
}

// Complete continues a text and returns the whole of it. Default tier Smart,
// mode Creative.
//
//	res, err := schemaflux.Complete(ctx, "The quick brown fox")
//	fmt.Println(res.Text)
//
// A blank text is an error of kind ErrInvalidRequest, and so is a completion
// that adds nothing to it, which is refused as an invalid answer.
//
// The Record of the call is "complete". The model's text is not a declared
// shape, so the answer is delivered at ContractPromptOnly: a client whose
// DataPolicy sets any floor refuses the call with ErrPolicy before anything is
// sent.
func Complete(ctx context.Context, text string, opts ...CompleteOptions) (CompleteResult, error) {
	var zero CompleteResult
	call, err := bind.Begin(ctx, "complete")
	if err != nil {
		return zero, err
	}
	o := mergeCompleteOptions(opts)
	if err := refuseBlank(&call, "text", text); err != nil {
		return zero, err
	}
	if err := checkCompleteOptions("complete", o); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		res, err := ops.Complete(call.Context(), text, completeInternal(call.Context(), o))
		return completeResult(res), call.End(nil, err)
	}
	res, err := bind.Recorded(&call, types.ContractPromptOnly, func(ctx context.Context) (ops.CompleteResult, error) {
		return ops.Complete(ctx, text, completeInternal(ctx, o))
	})
	return completeResult(res), err
}

// CompleteField continues the string field named field of a struct, using the
// other fields as context, and returns a copy with that field completed. The
// copy is shallow: slices and maps in it are shared with the original. field is
// the Go name of an exported string field, either of the struct itself or
// promoted from a struct embedded in it. Default tier Smart, mode Creative.
//
//	res, err := schemaflux.CompleteField(ctx, post, "Body", schemaflux.CompleteOptions{MaxLength: 400})
//	post = res.Data
//
// The other fields are shown to the model under the names encoding/json gives
// them, and a field the type tags json:"-" is not shown: what is not serialised
// is not sent to a provider. A blank field is an error of kind ErrInvalidRequest.
// A field that does not exist, is not exported, is not a string or is empty, data
// that is not a struct, and a field promoted through a nil embedded pointer, are
// errors of kind ErrConfiguration, returned before anything is sent.
//
// The Record of the call is "completefield", delivered at ContractPromptOnly: a
// client whose DataPolicy sets any floor refuses the call with ErrPolicy.
func CompleteField[T any](ctx context.Context, data T, field string, opts ...CompleteOptions) (CompleteFieldResult[T], error) {
	var zero CompleteFieldResult[T]
	call, err := bind.Begin(ctx, "completefield")
	if err != nil {
		return zero, err
	}
	o := mergeCompleteOptions(opts)
	if err := refuseBlank(&call, "field", field); err != nil {
		return zero, err
	}
	if err := checkCompleteOptions("completefield", o); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		res, err := ops.CompleteField[T](call.Context(), data, completeFieldInternal(call.Context(), field, o))
		return completeFieldResult(res), call.End(nil, err)
	}
	res, err := bind.Recorded(&call, types.ContractPromptOnly, func(ctx context.Context) (ops.CompleteFieldResult[T], error) {
		return ops.CompleteField[T](ctx, data, completeFieldInternal(ctx, field, o))
	})
	return completeFieldResult(res), err
}

// completeFieldInternal builds the internal options of a CompleteField call on
// ctx: those of Complete and the field's name.
func completeFieldInternal(ctx context.Context, field string, o CompleteOptions) ops.CompleteFieldOptions {
	return ops.CompleteFieldOptions{CompleteOptions: completeInternal(ctx, o), FieldName: field}
}

// SuggestOptions are Suggest's settings. The zero value is the default.
//
// The common settings of Options come first and are set without naming the
// embedded type: SuggestOptions{Mode: ModeStrict, TopN: 3}.
type SuggestOptions struct {
	Options

	// Strategy is how suggestions are arrived at: "contextual", "pattern", "goal"
	// or "hybrid". Empty means "contextual". Any other value is a configuration
	// error.
	Strategy string

	// TopN is the most suggestions to return. Zero means 5. A negative value is
	// a configuration error. An answer with more suggestions than TopN is
	// refused as an invalid answer.
	TopN int

	// NoRanking leaves the order to the model. False asks for the best first.
	NoRanking bool

	// IncludeScores asks the model to weigh a score for each suggestion. The
	// scores are not returned. False does not ask.
	IncludeScores bool

	// IncludeReasons asks the model to reason about each suggestion. The reasons
	// are not returned. False does not ask.
	IncludeReasons bool

	// Domain names the subject area. Empty means none.
	Domain string

	// Constraints are requirements a suggestion must meet. Nil means none.
	Constraints []string

	// Categories are kinds of suggestion to draw from. Nil means any.
	Categories []string
}

// mergeSuggestOptions merges the values of a call (M0).
func mergeSuggestOptions(opts []SuggestOptions) SuggestOptions {
	return mergeValues(opts, func(o, later SuggestOptions) SuggestOptions {
		o.Options = o.Options.merge(later.Options)
		lay(&o.Strategy, later.Strategy)
		lay(&o.TopN, later.TopN)
		lay(&o.NoRanking, later.NoRanking)
		lay(&o.IncludeScores, later.IncludeScores)
		lay(&o.IncludeReasons, later.IncludeReasons)
		lay(&o.Domain, later.Domain)
		laySlice(&o.Constraints, later.Constraints)
		laySlice(&o.Categories, later.Categories)
		return o
	})
}

// checkSuggestOptions returns the internal error of a setting no call can use,
// or nil. The strategy is not repeated in the message: the error goes wherever
// the caller's logs go.
func checkSuggestOptions(o SuggestOptions) error {
	if err := o.common().Check("suggest"); err != nil {
		return err
	}
	if err := checkCount("suggest", "TopN", o.TopN); err != nil {
		return err
	}
	switch o.Strategy {
	case "", "contextual", "pattern", "goal", "hybrid":
		return nil
	}
	return types.Newf(types.KindConfiguration, "suggest", "Strategy is not one of contextual, pattern, goal and hybrid")
}

// suggestInternal builds the internal options of a Suggest call on ctx.
func suggestInternal(ctx context.Context, o SuggestOptions) ops.SuggestOptions {
	opts := ops.NewSuggestOptions()
	if o.Strategy != "" {
		opts.Strategy = ops.SuggestStrategy(o.Strategy)
	}
	lay(&opts.TopN, o.TopN)
	turnOff(&opts.Ranked, o.NoRanking)
	lay(&opts.IncludeScores, o.IncludeScores)
	lay(&opts.IncludeReasons, o.IncludeReasons)
	lay(&opts.Domain, o.Domain)
	laySlice(&opts.Constraints, o.Constraints)
	laySlice(&opts.Categories, o.Categories)
	settleHalves(ctx, o.common(), &opts.CommonOptions, &opts.OpOptions)
	return opts
}

// Suggest proposes values of type T for an input: next actions, completions,
// options. It returns at most TopN. Default tier Smart, mode Transform.
//
//	next, err := schemaflux.Suggest[string](ctx, draft)
//	fixes, err := schemaflux.Suggest[Fix](ctx, report, schemaflux.SuggestOptions{TopN: 3, Domain: "billing"})
//
// An answer that holds no suggestion, or more than TopN, is an error of kind
// ErrInvalidAnswer; a suggestion that does not fit T is too.
//
// The Record of the call is "suggest", delivered at ContractJSONWellFormed: the
// answer is parsed and checked, but no schema is declared to the provider.
func Suggest[T any](ctx context.Context, input any, opts ...SuggestOptions) ([]T, error) {
	call, err := bind.Begin(ctx, "suggest")
	if err != nil {
		return nil, err
	}
	o := mergeSuggestOptions(opts)
	if err := checkSuggestOptions(o); err != nil {
		return nil, call.End(nil, err)
	}
	if call.Plain() {
		suggestions, err := ops.Suggest[T](input, suggestInternal(call.Context(), o))
		return suggestions, call.End(nil, err)
	}
	return bind.Recorded(&call, types.ContractJSONWellFormed, func(ctx context.Context) ([]T, error) {
		return ops.Suggest[T](input, suggestInternal(ctx, o))
	})
}
