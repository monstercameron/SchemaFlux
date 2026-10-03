package schemaflux

import (
	"context"
	"math"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
)

// ClassifyOptions are Classify's settings. The zero value is the default.
//
// The common settings of Options come first and are set without naming the
// embedded type: ClassifyOptions{Model: "gpt-6-luna", MinConfidence: 0.8}.
type ClassifyOptions struct {
	Options

	// MinConfidence refuses an answer the model reports less confidence in. It is
	// a number from 0 to 1. Zero means 0.5, and a negative value means no floor.
	// A value above 1, or one that is not a number, is a configuration error.
	MinConfidence float64

	// Descriptions say what a category means, keyed by category. Nil means none.
	Descriptions map[string]string

	// Examples give sample inputs for a category, keyed by category. Nil means
	// none.
	Examples map[string][]string
}

// ClassifyResult is the category a value was put in, with what the model
// reported about the choice.
type ClassifyResult[C ~string] struct {
	// Category is the category chosen. It is always one of the categories the
	// call was given, as it was written there.
	Category C `json:"category"`

	// ModelConfidence is the model's own claim, from 0 to 1. It is not a
	// measurement and not comparable between models or prompts; the floor of
	// ClassifyOptions.MinConfidence is applied to it.
	ModelConfidence float64 `json:"confidence"`

	// Alternatives are other categories the model considered, with its claim for
	// each. Nil when it named none.
	Alternatives []ClassifyAlternative[C] `json:"alternatives,omitempty"`

	// Reasoning is why, in the model's words. Empty when it gave none.
	Reasoning string `json:"reasoning,omitempty"`
}

// ClassifyAlternative is a category the model considered and did not choose.
type ClassifyAlternative[C ~string] struct {
	// Category is the alternative, one of the categories the call was given.
	Category C `json:"category"`

	// ModelConfidence is the model's own claim for this category, from 0 to 1.
	ModelConfidence float64 `json:"confidence"`
}

// mergeClassifyOptions merges the values of a call (M0).
func mergeClassifyOptions(opts []ClassifyOptions) ClassifyOptions {
	return mergeValues(opts, func(o, later ClassifyOptions) ClassifyOptions {
		o.Options = o.Options.merge(later.Options)
		lay(&o.MinConfidence, later.MinConfidence)
		layMap(&o.Descriptions, later.Descriptions)
		layMap(&o.Examples, later.Examples)
		return o
	})
}

// checkClassifyOptions returns the internal error of a setting no call can use,
// or nil. The number is checked on the caller's own value, before the meaning
// of its zero and of a negative is applied.
func checkClassifyOptions(o ClassifyOptions) error {
	if err := o.common().Check("classify"); err != nil {
		return err
	}
	return checkFloat("classify", "MinConfidence", o.MinConfidence, math.Inf(-1), 1)
}

// classifyInternal builds the internal options of a Classify call on ctx.
func classifyInternal[C ~string](ctx context.Context, categories []C, o ClassifyOptions) ops.ClassifyOptions {
	opts := ops.NewClassifyOptions()
	opts.Categories = categoryStrings(categories)
	switch {
	case o.MinConfidence > 0:
		opts.MinConfidence = o.MinConfidence
	case o.MinConfidence < 0:
		opts.MinConfidence = 0
	}
	layMap(&opts.CategoryDescriptions, o.Descriptions)
	layMap(&opts.CategoryExamples, o.Examples)
	settleHalves(ctx, o.common(), &opts.CommonOptions, &opts.OpOptions)
	return opts
}

// categoryStrings is the categories as the strings the internal call takes.
func categoryStrings[C ~string](categories []C) []string {
	out := make([]string, len(categories))
	for i, c := range categories {
		out[i] = string(c)
	}
	return out
}

// classifyResult is the result of the internal call as the root declares it.
// The zero result, which the internal call returns beside an error, converts to
// the zero result.
func classifyResult[C ~string](res ops.ClassifyResult[C]) ClassifyResult[C] {
	out := ClassifyResult[C]{Category: res.Category, ModelConfidence: res.ModelConfidence, Reasoning: res.Reasoning}
	if len(res.Alternatives) > 0 {
		out.Alternatives = make([]ClassifyAlternative[C], len(res.Alternatives))
		for i, a := range res.Alternatives {
			out.Alternatives[i] = ClassifyAlternative[C]{Category: a.Category, ModelConfidence: a.ModelConfidence}
		}
	}
	return out
}

// Classify puts a value in one of the categories given. The answer is always
// one of them: anything else is an error, and so is an answer the model is less
// sure of than MinConfidence. Both type parameters are inferred.
// Default tier Fast, mode Transform.
//
//	res, err := schemaflux.Classify(ctx, review, []string{"positive", "negative", "neutral"})
//	res, err := schemaflux.Classify(ctx, ticket, []Team{Billing, Support}, schemaflux.ClassifyOptions{MinConfidence: 0.8})
//
// There has to be at least one category: none is an error of kind
// ErrInvalidRequest, returned before anything is sent. The Record of the call is
// "classify", delivered at ContractJSONWellFormed: the answer is parsed and each
// category in it is checked against the ones offered, but no schema is declared
// to the provider.
func Classify[C ~string, T any](ctx context.Context, input T, categories []C, opts ...ClassifyOptions) (ClassifyResult[C], error) {
	var zero ClassifyResult[C]
	call, err := bind.Begin(ctx, "classify")
	if err != nil {
		return zero, err
	}
	o := mergeClassifyOptions(opts)
	if err := refuseEmpty(&call, "categories", len(categories)); err != nil {
		return zero, err
	}
	if err := checkClassifyOptions(o); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		res, err := ops.Classify[T, C](input, classifyInternal(call.Context(), categories, o))
		return classifyResult(res), call.End(nil, err)
	}
	res, err := bind.Recorded(&call, types.ContractJSONWellFormed, func(ctx context.Context) (ops.ClassifyResult[C], error) {
		return ops.Classify[T, C](input, classifyInternal(ctx, categories, o))
	})
	return classifyResult(res), err
}
