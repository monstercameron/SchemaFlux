package schemaflux

import (
	"context"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// SummarizeOptions are the settings of Summarize and SummarizeDetail. The zero
// value is the default.
//
// The common settings of Options come first and are set without naming the
// embedded type: SummarizeOptions{Model: "gpt-6-luna", TargetLength: 3}.
type SummarizeOptions struct {
	Options

	// TargetLength is the length asked for, in LengthUnit. Zero means no target.
	// A negative value is a configuration error.
	TargetLength int

	// LengthUnit is "words", "sentences" or "paragraphs". Empty means
	// "sentences". Any other value is a configuration error.
	LengthUnit string

	// Style is "bullet", "paragraph" or "executive". Empty means "paragraph". It
	// is not checked: it is a request, and any word is passed to the model.
	Style string

	// BulletPoints asks for bullet points. False leaves the form to Style.
	BulletPoints bool

	// FocusAreas are subjects to concentrate on. Nil means none.
	FocusAreas []string

	// PreserveInfo names information that must survive. Nil means none.
	PreserveInfo []string
}

// Summary is a summary with what the model reported about it.
type Summary struct {
	// Text is the summary.
	Text string `json:"text"`

	// KeyPoints are the main points, as the model listed them. Nil when it
	// listed none.
	KeyPoints []string `json:"key_points,omitempty"`

	// ModelConfidence is the model's own claim, from 0 to 1. It is not a
	// measurement.
	ModelConfidence float64 `json:"confidence"`

	// CompressionRatio is the length of Text over the length of the input, in
	// runes. It is measured here.
	CompressionRatio float64 `json:"compression_ratio"`

	// Length is the length of Text in LengthUnit, measured here. Zero when no
	// TargetLength was set.
	Length int `json:"length,omitempty"`
}

// mergeSummarizeOptions merges the values of a call (M0).
func mergeSummarizeOptions(opts []SummarizeOptions) SummarizeOptions {
	return mergeValues(opts, func(o, later SummarizeOptions) SummarizeOptions {
		o.Options = o.Options.merge(later.Options)
		lay(&o.TargetLength, later.TargetLength)
		lay(&o.LengthUnit, later.LengthUnit)
		lay(&o.Style, later.Style)
		lay(&o.BulletPoints, later.BulletPoints)
		laySlice(&o.FocusAreas, later.FocusAreas)
		laySlice(&o.PreserveInfo, later.PreserveInfo)
		return o
	})
}

// checkSummarizeOptions returns the internal error of a setting no summary can
// use, or nil. op is the operation the error names: the function that was called.
func checkSummarizeOptions(op string, o SummarizeOptions) error {
	if err := o.common().Check(op); err != nil {
		return err
	}
	if err := checkCount(op, "TargetLength", o.TargetLength); err != nil {
		return err
	}
	switch o.LengthUnit {
	case "", "words", "sentences", "paragraphs":
		return nil
	}
	return types.Newf(types.KindConfiguration, op, "LengthUnit is not one of words, sentences and paragraphs")
}

// summarizeInternal builds the internal options of a summary on ctx. The
// constructor's MaxCompression stays: with the Metadata forms gone nothing
// reads it.
func summarizeInternal(ctx context.Context, o SummarizeOptions) ops.SummarizeOptions {
	opts := ops.NewSummarizeOptions()
	lay(&opts.TargetLength, o.TargetLength)
	lay(&opts.LengthUnit, o.LengthUnit)
	lay(&opts.Style, o.Style)
	lay(&opts.BulletPoints, o.BulletPoints)
	laySlice(&opts.FocusAreas, o.FocusAreas)
	laySlice(&opts.PreserveInfo, o.PreserveInfo)
	settleHalves(ctx, o.common(), &opts.CommonOptions, &opts.OpOptions)
	return opts
}

// summaryOf is the summary of the internal call as the root declares it. The
// length measured here is in the internal summary's metadata, and the metadata
// itself is not carried over. The zero summary, which the internal call returns
// beside an error, converts to the zero summary.
func summaryOf(s ops.Summary) Summary {
	out := Summary{Text: s.Text, KeyPoints: s.KeyPoints, ModelConfidence: s.ModelConfidence, CompressionRatio: s.CompressionRatio}
	if measured, ok := s.Metadata["length_measured"].(int); ok {
		out.Length = measured
	}
	return out
}

// Summarize returns a summary of a text. TargetLength is a request: this form
// returns the model's text as it came, trimmed, and measures nothing; an empty
// answer is an error. Default tier Fast, mode Transform.
//
//	short, err := schemaflux.Summarize(ctx, article)
//
// The Record of the call is "summarize". The model's text is not a declared
// shape, so the answer is delivered at ContractPromptOnly: a client whose
// DataPolicy sets any floor refuses the call with ErrPolicy before anything is
// sent. SummarizeDetail asks for more and is held to more.
func Summarize(ctx context.Context, text string, opts ...SummarizeOptions) (string, error) {
	call, err := bind.Begin(ctx, "summarize")
	if err != nil {
		return "", err
	}
	o := mergeSummarizeOptions(opts)
	if err := checkSummarizeOptions("summarize", o); err != nil {
		return "", call.End(nil, err)
	}
	if call.Plain() {
		summary, err := ops.Summarize(text, summarizeInternal(call.Context(), o))
		return summary, call.End(nil, err)
	}
	return bind.Recorded(&call, types.ContractPromptOnly, func(ctx context.Context) (string, error) {
		return ops.Summarize(text, summarizeInternal(ctx, o))
	})
}

// SummarizeDetail asks for the summary together with its key points and the
// model's confidence, and measures it: a summary more than 20% over
// TargetLength is an error of kind ErrInvalidAnswer. It is a different request
// from Summarize, and the model is sent the same one in every state of the call.
//
//	sum, err := schemaflux.SummarizeDetail(ctx, article, schemaflux.SummarizeOptions{TargetLength: 3})
//
// The Record of the call is "summarizedetail", delivered at ContractJSONWellFormed.
func SummarizeDetail(ctx context.Context, text string, opts ...SummarizeOptions) (Summary, error) {
	var zero Summary
	call, err := bind.Begin(ctx, "summarizedetail")
	if err != nil {
		return zero, err
	}
	o := mergeSummarizeOptions(opts)
	if err := checkSummarizeOptions("summarizedetail", o); err != nil {
		return zero, call.End(nil, err)
	}
	call.Form("summarize")
	res, err := ops.SummarizeResult(text, summarizeInternal(call.Context(), o))
	return summaryOf(res.Value), call.End(&res.Meta, err)
}

// checkWholeRange returns the internal error of kind configuration for a count
// that is neither zero nor from lo to hi, and nil otherwise. Zero is the
// default of the options that use it, so it is always acceptable. field is the
// name of the options field. The check is made on the caller's own value before
// the default is applied to it.
func checkWholeRange(op, field string, n, lo, hi int) error {
	if n == 0 || (n >= lo && n <= hi) {
		return nil
	}
	return types.Newf(types.KindConfiguration, op, "%s must be a whole number from %d to %d, or zero for the default", field, lo, hi)
}
