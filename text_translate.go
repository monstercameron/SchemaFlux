package schemaflux

import (
	"context"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// TranslateOptions are the settings of Translate and TranslateDetail. The zero
// value is the default.
//
// The common settings of Options come first and are set without naming the
// embedded type: TranslateOptions{Model: "gpt-6-luna", Dialect: "Mexican"}.
type TranslateOptions struct {
	Options

	// SourceLanguage is the language of the text. Empty means the model detects
	// it.
	SourceLanguage string

	// NoPreserveFormatting withholds the instruction to preserve formatting.
	// False keeps it.
	NoPreserveFormatting bool

	// CulturalAdaptation is 0 (literal) to 10 (fully adapted). Zero means 5; a
	// negative value means 0. A value above 10 is a configuration error.
	CulturalAdaptation int

	// Formality is the register of the translation. It is not checked: it is a
	// request, and any word is passed to the model. Empty means "neutral".
	Formality string

	// Glossary fixes the translation of a term, keyed by the source term. It is
	// enforced: a term that occurs in the text, as a whole word and without
	// regard to case, has to come out as the glossary says, or the call is an
	// error of kind ErrInvalidAnswer. Nil means none.
	Glossary map[string]string

	// Dialect is a regional variety of the target language. Empty means none.
	Dialect string
}

// Translation is a translation with what the model reported about it.
type Translation struct {
	// Text is the translation.
	Text string `json:"text"`

	// SourceLanguageDetected is the language the model took the text to be in.
	SourceLanguageDetected string `json:"source_language_detected,omitempty"`

	// ModelConfidence is the model's own claim, from 0 to 1. It is not a
	// measurement.
	ModelConfidence float64 `json:"confidence"`

	// Alternatives are other renderings of ambiguous phrases. Nil when the model
	// gave none.
	Alternatives []TranslationAlternative `json:"alternatives,omitempty"`
}

// TranslationAlternative is another way to translate a phrase of the source.
type TranslationAlternative struct {
	// Phrase is the phrase of the source.
	Phrase string `json:"phrase"`

	// Alternative is another way to translate it.
	Alternative string `json:"alternative"`

	// Context is when the alternative fits. Empty when the model gave none.
	Context string `json:"context,omitempty"`
}

// mergeTranslateOptions merges the values of a call (M0).
func mergeTranslateOptions(opts []TranslateOptions) TranslateOptions {
	return mergeValues(opts, func(o, later TranslateOptions) TranslateOptions {
		o.Options = o.Options.merge(later.Options)
		lay(&o.SourceLanguage, later.SourceLanguage)
		lay(&o.NoPreserveFormatting, later.NoPreserveFormatting)
		lay(&o.CulturalAdaptation, later.CulturalAdaptation)
		lay(&o.Formality, later.Formality)
		layMap(&o.Glossary, later.Glossary)
		lay(&o.Dialect, later.Dialect)
		return o
	})
}

// checkTranslateOptions returns the internal error of a setting no translation
// can use, or nil. op is the operation the error names: the function that was
// called. A negative CulturalAdaptation is a setting (it means a literal
// translation), so only a value above the scale is refused.
func checkTranslateOptions(op string, o TranslateOptions) error {
	if err := o.common().Check(op); err != nil {
		return err
	}
	if o.CulturalAdaptation > 10 {
		return types.Newf(types.KindConfiguration, op, "CulturalAdaptation must not be above 10")
	}
	return nil
}

// translateInternal builds the internal options of a translation into language
// on ctx: the constructor's value, then each setting that is not its zero value.
func translateInternal(ctx context.Context, language string, o TranslateOptions) ops.TranslateOptions {
	opts := ops.NewTranslateOptions()
	opts.TargetLanguage = language
	lay(&opts.SourceLanguage, o.SourceLanguage)
	turnOff(&opts.PreserveFormatting, o.NoPreserveFormatting)
	switch {
	case o.CulturalAdaptation < 0:
		opts.CulturalAdaptation = 0
	case o.CulturalAdaptation > 0:
		opts.CulturalAdaptation = o.CulturalAdaptation
	}
	lay(&opts.Formality, o.Formality)
	layMap(&opts.Glossary, o.Glossary)
	lay(&opts.Dialect, o.Dialect)
	settleHalves(ctx, o.common(), &opts.CommonOptions, &opts.OpOptions)
	return opts
}

// translationOf is the translation of the internal call as the root declares it.
// The internal metadata map is never written for a translation and is not
// carried over. The zero translation, which the internal call returns beside an
// error, converts to the zero translation, and a translation with no alternatives
// has none (nil) here.
func translationOf(t ops.Translation) Translation {
	out := Translation{Text: t.Text, SourceLanguageDetected: t.SourceLanguageDetected, ModelConfidence: t.ModelConfidence}
	if len(t.Alternatives) > 0 {
		out.Alternatives = make([]TranslationAlternative, len(t.Alternatives))
		for i, a := range t.Alternatives {
			out.Alternatives[i] = TranslationAlternative(a)
		}
	}
	return out
}

// Translate returns a text in the language named. When a Glossary is given, a
// term of it that occurs in the text must be translated as the glossary says, or
// the call is an error of kind ErrInvalidAnswer. The model's text is returned
// trimmed, and an empty answer is an error. A blank language is an error of kind
// ErrInvalidRequest. Default tier Fast, mode Transform.
//
//	es, err := schemaflux.Translate(ctx, text, "Spanish")
//
// The Record of the call is "translate". The model's text is not a declared
// shape, so the answer is delivered at ContractPromptOnly: a client whose
// DataPolicy sets any floor refuses the call with ErrPolicy before anything is
// sent. TranslateDetail asks for more and is held to more.
func Translate(ctx context.Context, text, language string, opts ...TranslateOptions) (string, error) {
	call, err := bind.Begin(ctx, "translate")
	if err != nil {
		return "", err
	}
	o := mergeTranslateOptions(opts)
	if err := refuseBlank(&call, "language", language); err != nil {
		return "", err
	}
	if err := checkTranslateOptions("translate", o); err != nil {
		return "", call.End(nil, err)
	}
	if call.Plain() {
		translated, err := ops.Translate(text, translateInternal(call.Context(), language, o))
		return translated, call.End(nil, err)
	}
	return bind.Recorded(&call, types.ContractPromptOnly, func(ctx context.Context) (string, error) {
		return ops.Translate(text, translateInternal(ctx, language, o))
	})
}

// TranslateDetail asks for the translation together with the source language the
// model detected, alternatives for ambiguous phrases and its confidence. It is a
// different request from Translate, and the model is sent the same one in every
// state of the call. The glossary is enforced as Translate enforces it.
//
//	t, err := schemaflux.TranslateDetail(ctx, text, "French", schemaflux.TranslateOptions{Glossary: terms})
//
// The Record of the call is "translatedetail", delivered at ContractJSONWellFormed.
func TranslateDetail(ctx context.Context, text, language string, opts ...TranslateOptions) (Translation, error) {
	var zero Translation
	call, err := bind.Begin(ctx, "translatedetail")
	if err != nil {
		return zero, err
	}
	o := mergeTranslateOptions(opts)
	if err := refuseBlank(&call, "language", language); err != nil {
		return zero, err
	}
	if err := checkTranslateOptions("translatedetail", o); err != nil {
		return zero, call.End(nil, err)
	}
	call.Form("translate")
	res, err := ops.TranslateResult(text, translateInternal(call.Context(), language, o))
	return translationOf(res.Value), call.End(&res.Meta, err)
}
