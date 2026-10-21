package redact

import (
	"context"
	"strings"
	"unicode/utf8"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// maxMinMask is the largest MinMask: a mask longer than this is refused. It is the
// longest fixed mask of Value, so that one number bounds both.
const maxMinMask = maxMaskLength

// textOp is the name of Text in a Record and in an error.
const textOp = "redact.text"

// TextOptions are Text's settings. The zero value is the default: every built-in
// category, a mask of '*' that covers the whole span and is never shorter than
// three characters, and the common settings' own defaults (the Fast tier and the
// Strict mode).
//
// The common settings of Options come first and are set without naming the
// embedded type: TextOptions{Categories: []string{"email", "phone"}, ShowLast: 2, Model: "gpt-6-luna"}.
type TextOptions struct {
	schemaflux.Options

	// Categories are the kinds of data to find: email, phone, ssn, name, address,
	// credit_card, password, api_key, ip, date_of_birth and financial, or a name
	// of the caller's own, which the model is asked to look for. Names are
	// compared without regard to case or the space around them. Nil means every
	// built-in category. An empty list, a blank name and a name with a comma in it
	// are configuration errors. The pattern detectors run only for the built-in
	// categories asked for, and a span the model labels with a category that was
	// not asked for is an error of kind ErrInvalidAnswer.
	Categories []string

	// MaskChar is the character a masked span is made of. Zero means '*'. A
	// negative number, a surrogate half or a number above the last character is a
	// configuration error.
	MaskChar rune

	// ShowFirst is how many characters at the start of a span stay readable. Zero
	// means none. A negative number is a configuration error. When ShowFirst and
	// ShowLast together cover the whole span, none is shown.
	ShowFirst int

	// ShowLast is how many characters at the end of a span stay readable. Zero
	// means none. A negative number is a configuration error.
	ShowLast int

	// MinMask is the fewest mask characters a span is replaced with, so that a
	// short value does not show its length. Zero means 3. A negative number, or
	// one above 65536, is a configuration error.
	MinMask int
}

// Span is one range of the text that was masked. Its offsets are counted in
// characters, not bytes.
type Span struct {
	// Start is the offset of the first character of the span in the original
	// text.
	Start int `json:"start"`

	// End is the offset after the last character of the span, so that the span is
	// the characters from Start up to End.
	End int `json:"end"`

	// Category is the kind of data the span was found to be. It is the name the
	// caller asked for, or a built-in one, whatever case the model wrote it in. A
	// span of two kinds holds both, joined with a comma. Empty when the model gave
	// none.
	Category string `json:"category"`

	// Original is the text the span covers: the sensitive data itself.
	Original string `json:"original"`
}

// TextResult is a text with its sensitive spans masked, and what was found.
//
// It holds the original text and, in each span, the data that was hidden. Keep it
// out of logs and out of anything that outlasts the call; log Text, and only that.
type TextResult struct {
	// Text is the text with each span replaced by its mask. It is the text as it
	// was given when it is blank.
	Text string `json:"text"`

	// Original is the text as it was given.
	Original string `json:"original"`

	// Spans are the ranges that were masked, in the order they appear in the text.
	// Empty, never nil, when nothing was found.
	Spans []Span `json:"spans"`

	// Categories is how many spans there are of each category. A span of two
	// categories counts in both, and a span with none counts in none. Empty,
	// never nil, when nothing was found.
	Categories map[string]int `json:"categories"`

	// DetectorSpans is how many of the spans the pattern detectors in Go found,
	// whatever the model said. They run on every call, for the built-in categories
	// asked for.
	DetectorSpans int `json:"detector_spans"`

	// LeftInsideWords is how many copies of a very short value were left readable
	// because they sat inside a longer word or number: "Al" in "called", "34" in
	// "$1345". A value is never left alone everywhere: if every copy would be, all
	// are masked.
	LeftInsideWords int `json:"left_inside_words"`
}

// mergeTextOptions merges the values of a call (M0): a later field that is not its
// zero value replaces the earlier one, and no value at all is the zero value.
func mergeTextOptions(options []TextOptions) TextOptions {
	switch len(options) {
	case 0:
		return TextOptions{}
	case 1:
		return options[0]
	}
	merged := options[0]
	for _, later := range options[1:] {
		merged.Options = mergeCommonOptions(merged.Options, later.Options)
		if later.Categories != nil {
			merged.Categories = later.Categories
		}
		if later.MaskChar != 0 {
			merged.MaskChar = later.MaskChar
		}
		if later.ShowFirst != 0 {
			merged.ShowFirst = later.ShowFirst
		}
		if later.ShowLast != 0 {
			merged.ShowLast = later.ShowLast
		}
		if later.MinMask != 0 {
			merged.MinMask = later.MinMask
		}
	}
	return merged
}

// mergeCommonOptions lays each setting of later that is not its zero value over
// earlier. It is Options.merge of the root, which a subpackage cannot call; a test
// holds it to every field of Options.
func mergeCommonOptions(earlier, later schemaflux.Options) schemaflux.Options {
	if later.Model != "" {
		earlier.Model = later.Model
	}
	if later.Tier != 0 {
		earlier.Tier = later.Tier
	}
	if later.Effort != "" {
		earlier.Effort = later.Effort
	}
	if later.Mode != 0 {
		earlier.Mode = later.Mode
	}
	if later.Steering != "" {
		earlier.Steering = later.Steering
	}
	if later.MaxOutputTokens != 0 {
		earlier.MaxOutputTokens = later.MaxOutputTokens
	}
	if later.WebSearch {
		earlier.WebSearch = true
	}
	return earlier
}

// textCommon is the common settings of a call, and the configuration error of one
// no call can use. Options that are all zero are the zero settings, which asks
// nothing of the root and allocates nothing.
func textCommon(o TextOptions) (bind.Common, error) {
	if o.Options == (schemaflux.Options{}) {
		return bind.Common{}, nil
	}
	return bind.CommonOf(o.Options)
}

// checkTextOptions returns the internal error of a setting no call can use, or
// nil, naming the field and the position and never repeating what the caller
// wrote: a category can be the very data the call is meant to hide.
func checkTextOptions(o TextOptions) error {
	if o.Categories != nil && len(o.Categories) == 0 {
		return types.Newf(types.KindConfiguration, textOp, "Categories is empty: leave it nil for every built-in category")
	}
	for i, category := range o.Categories {
		switch {
		case strings.TrimSpace(category) == "":
			return types.Newf(types.KindConfiguration, textOp, "Categories[%d] is blank", i)
		case strings.Contains(category, ","):
			return types.Newf(types.KindConfiguration, textOp, "Categories[%d] has a comma in its name, which separates two categories", i)
		}
	}
	switch {
	case o.ShowFirst < 0:
		return types.Newf(types.KindConfiguration, textOp, "ShowFirst must not be negative")
	case o.ShowLast < 0:
		return types.Newf(types.KindConfiguration, textOp, "ShowLast must not be negative")
	case o.MinMask < 0 || o.MinMask > maxMinMask:
		return types.Newf(types.KindConfiguration, textOp, "MinMask must be from 0 to %d", maxMinMask)
	case !utf8.ValidRune(o.MaskChar):
		return types.Newf(types.KindConfiguration, textOp, "MaskChar is not a valid character")
	}
	return nil
}

// textInternal builds the internal options of a Text call on ctx. It starts from
// the constructor's value, which is the Fast tier, the Strict mode, every
// built-in category, a mask of '*' and a minimum of three, so that a field left at
// its zero keeps the default.
func textInternal(ctx context.Context, o TextOptions, common bind.Common) ops.RedactLLMOptions {
	in := ops.NewRedactLLMOptions()
	if o.Categories != nil {
		in.Categories = o.Categories
	}
	if o.MaskChar != 0 {
		in.MaskChar = o.MaskChar
	}
	if o.ShowFirst != 0 {
		in.ShowFirst = o.ShowFirst
	}
	if o.ShowLast != 0 {
		in.ShowLast = o.ShowLast
	}
	if o.MinMask != 0 {
		in.MinMask = o.MinMask
	}
	common.Apply(&in.OpOptions)
	in.Context = ctx
	return in
}

// textResult is the result of the internal call as this package declares it. The
// zero result, which the internal call returns beside an error, converts to the
// zero result. The two counts are read from the internal result's metadata, and
// the spans are copied, since the two types are not the same type.
func textResult(res ops.RedactLLMResult) TextResult {
	out := TextResult{
		Text:            res.Text,
		Original:        res.Original,
		Categories:      res.Categories,
		DetectorSpans:   metadataCount(res.Metadata, "spans_from_detectors"),
		LeftInsideWords: metadataCount(res.Metadata, "left_inside_longer_words"),
	}
	if res.Spans != nil {
		out.Spans = make([]Span, len(res.Spans))
		for i, span := range res.Spans {
			out.Spans[i] = Span{Start: span.Start, End: span.End, Category: span.Category, Original: span.Original}
		}
	}
	return out
}

// metadataCount is a count the internal result keeps in its metadata, and zero
// when it keeps none.
func metadataCount(metadata map[string]any, key string) int {
	count, _ := metadata[key].(int)
	return count
}

// Text finds sensitive data in a text with a model and with pattern detectors
// written in Go, and masks it. It sends the text to the client's provider: for a
// Go value, which asks no model, use Value. Offsets are in characters. Default
// tier Fast, mode Strict.
//
//	res, err := redact.Text(ctx, ticket)
//	res, err := redact.Text(ctx, ticket, redact.TextOptions{Categories: []string{"email", "phone"}, ShowLast: 2})
//	log.Print(res.Text)
//
// The model names each sensitive value and the library finds it in the text, so
// the model is never trusted with a position; every copy of the value is masked,
// in any case. The detectors, for the built-in categories asked for, find emails,
// phone numbers, social security numbers, payment cards, IBANs, secrets and
// bearer tokens whatever the model says, so an answer of "nothing" does not undo
// them. What they do not cover (names, addresses, IP addresses, dates of birth and
// categories of the caller's own) depends on the model alone, and the model's
// detection varies from call to call.
//
// A blank text is returned as it is, without a request. A setting that cannot be
// used (see TextOptions) is an error of kind ErrConfiguration, returned before
// anything is sent, and a blank text does not excuse one. An answer that is not
// the JSON that was asked for, that names a value the text does not hold, that
// names no text, or that labels a span with a category that was not asked for is an
// error of kind ErrInvalidAnswer: never a text with the secret left in it. On any
// error the result is empty, and no error, Record or log line holds the text or
// anything the model wrote.
//
// The Record of the call is "redact.text", delivered at
// ContractSchemaAndInvariantChecked: the answer is declared to the provider as a
// schema and the library checks each span against the text and the categories. A
// client held to a floor above that level refuses the call before anything is sent.
func Text(ctx context.Context, text string, options ...TextOptions) (TextResult, error) {
	var zero TextResult
	call, err := bind.Begin(ctx, textOp)
	if err != nil {
		return zero, err
	}
	call.Form("redact_llm")
	o := mergeTextOptions(options)
	if err := checkTextOptions(o); err != nil {
		return zero, call.End(nil, err)
	}
	common, err := textCommon(o)
	if err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		res, err := ops.RedactLLM(call.Context(), text, textInternal(call.Context(), o, common))
		return textResult(res), call.End(nil, err)
	}
	res, err := ops.RedactLLMWithMeta(call.Context(), text, textInternal(call.Context(), o, common))
	return textResult(res.Value), call.End(&res.Meta, err)
}
