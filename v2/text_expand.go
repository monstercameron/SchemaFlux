package schemaflux

import (
	"context"
	"math"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
)

// ExpandOptions are the settings of Expand and ExpandDetail. The zero value is
// the default.
//
// The common settings of Options come first and are set without naming the
// embedded type: ExpandOptions{Model: "gpt-6-luna", Factor: 3}. The default mode
// of an expansion is ModeCreative.
type ExpandOptions struct {
	Options

	// Factor is how many times longer the result should be: a request, which
	// ExpandDetail measures and Expand does not. Zero means 2. A value below 1,
	// or one that is not a number, is a configuration error.
	Factor float64

	// DetailLevel is 1 to 10. Zero means 5. A value outside 1 to 10 is a
	// configuration error.
	DetailLevel int

	// IncludeExamples asks for examples. False does not.
	IncludeExamples bool

	// ElaborateOn are subjects to say more about. Nil means none.
	ElaborateOn []string

	// AddContext are subjects to add background on. Nil means none.
	AddContext []string

	// Style is "technical", "narrative", "educational" or "balanced". It is not
	// checked: it is a request, and any word is passed to the model. Empty means
	// "balanced".
	Style string
}

// Expansion is an expansion with what the model reported about it.
type Expansion struct {
	// Text is the expanded text.
	Text string `json:"text"`

	// AddedContent is what the model says it added. Nil when it listed none.
	AddedContent []string `json:"added_content,omitempty"`

	// ModelConfidence is the model's own claim, from 0 to 1. It is not a
	// measurement.
	ModelConfidence float64 `json:"confidence"`

	// ExpansionRatio is the length of Text over the length of the input, in
	// runes. It is measured here, and is zero for an empty input.
	ExpansionRatio float64 `json:"expansion_ratio"`
}

// mergeExpandOptions merges the values of a call (M0).
func mergeExpandOptions(opts []ExpandOptions) ExpandOptions {
	return mergeValues(opts, func(o, later ExpandOptions) ExpandOptions {
		o.Options = o.Options.merge(later.Options)
		lay(&o.Factor, later.Factor)
		lay(&o.DetailLevel, later.DetailLevel)
		lay(&o.IncludeExamples, later.IncludeExamples)
		laySlice(&o.ElaborateOn, later.ElaborateOn)
		laySlice(&o.AddContext, later.AddContext)
		lay(&o.Style, later.Style)
		return o
	})
}

// checkExpandOptions returns the internal error of a setting no expansion can
// use, or nil. op is the operation the error names: the function that was called.
// A Factor of zero is the default; any other value, a NaN included, has to be at
// least 1.
func checkExpandOptions(op string, o ExpandOptions) error {
	if err := o.common().Check(op); err != nil {
		return err
	}
	if o.Factor != 0 {
		if err := checkFloat(op, "Factor", o.Factor, 1, math.Inf(1)); err != nil {
			return err
		}
	}
	return checkWholeRange(op, "DetailLevel", o.DetailLevel, 1, 10)
}

// expandInternal builds the internal options of an expansion on ctx: the
// constructor's value, which is in ModeCreative, then each setting that is not
// its zero value.
func expandInternal(ctx context.Context, o ExpandOptions) ops.ExpandOptions {
	opts := ops.NewExpandOptions()
	lay(&opts.ExpansionFactor, o.Factor)
	lay(&opts.DetailLevel, o.DetailLevel)
	lay(&opts.IncludeExamples, o.IncludeExamples)
	laySlice(&opts.ElaborateOn, o.ElaborateOn)
	laySlice(&opts.AddContext, o.AddContext)
	lay(&opts.ExpansionStyle, o.Style)
	settleHalves(ctx, o.common(), &opts.CommonOptions, &opts.OpOptions)
	return opts
}

// expansionOf is the expansion of the internal call as the root declares it. The
// internal metadata map, which holds the figures of the factor check, is not
// carried over. The zero expansion, which the internal call returns beside an
// error, converts to the zero expansion.
func expansionOf(e ops.Expansion) Expansion {
	return Expansion{Text: e.Text, AddedContent: e.AddedContent, ModelConfidence: e.ModelConfidence, ExpansionRatio: e.ExpansionRatio}
}

// Expand returns a longer, more detailed version of a text. Factor is a request:
// this form returns the model's text as it came, trimmed, and measures nothing;
// an empty answer is an error. Default tier Fast, mode Creative.
//
//	long, err := schemaflux.Expand(ctx, outline)
//
// The Record of the call is "expand". The model's text is not a declared shape,
// so the answer is delivered at ContractPromptOnly: a client whose DataPolicy
// sets any floor refuses the call with ErrPolicy before anything is sent.
// ExpandDetail asks for more and is held to more.
func Expand(ctx context.Context, text string, opts ...ExpandOptions) (string, error) {
	call, err := bind.Begin(ctx, "expand")
	if err != nil {
		return "", err
	}
	o := mergeExpandOptions(opts)
	if err := checkExpandOptions("expand", o); err != nil {
		return "", call.End(nil, err)
	}
	if call.Plain() {
		expanded, err := ops.Expand(text, expandInternal(call.Context(), o))
		return expanded, call.End(nil, err)
	}
	return bind.Recorded(&call, types.ContractPromptOnly, func(ctx context.Context) (string, error) {
		return ops.Expand(text, expandInternal(ctx, o))
	})
}

// ExpandDetail asks for the expansion together with what was added and the
// model's confidence, and measures it: when a Factor other than 2 was asked for,
// an expansion more than 20% short of it is an error of kind ErrInvalidAnswer. It
// is a different request from Expand, and the model is sent the same one in every
// state of the call.
//
//	e, err := schemaflux.ExpandDetail(ctx, outline, schemaflux.ExpandOptions{Factor: 3})
//
// The Record of the call is "expanddetail", delivered at ContractJSONWellFormed.
func ExpandDetail(ctx context.Context, text string, opts ...ExpandOptions) (Expansion, error) {
	var zero Expansion
	call, err := bind.Begin(ctx, "expanddetail")
	if err != nil {
		return zero, err
	}
	o := mergeExpandOptions(opts)
	if err := checkExpandOptions("expanddetail", o); err != nil {
		return zero, call.End(nil, err)
	}
	call.Form("expand")
	res, err := ops.ExpandResult(text, expandInternal(call.Context(), o))
	return expansionOf(res.Value), call.End(&res.Meta, err)
}
