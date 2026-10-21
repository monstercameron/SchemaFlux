package ops

import (
	"math"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Every text operation refuses its options before it asks a model. The refusal
// is the text it was, and it now has a kind: the caller passed an option that
// cannot be used.
func TestTextOperationsRefuseBadOptionsWithAKind(t *testing.T) {
	config := types.ErrConfiguration

	summarize := func(edit func(*SummarizeOptions)) SummarizeOptions {
		opts := NewSummarizeOptions()
		edit(&opts)
		return opts
	}
	rewrite := func(edit func(*RewriteOptions)) RewriteOptions {
		opts := NewRewriteOptions()
		edit(&opts)
		return opts
	}
	translate := func(edit func(*TranslateOptions)) TranslateOptions {
		opts := NewTranslateOptions()
		opts.TargetLanguage = "French"
		edit(&opts)
		return opts
	}
	expand := func(edit func(*ExpandOptions)) ExpandOptions {
		opts := NewExpandOptions()
		edit(&opts)
		return opts
	}

	checkEarlyRefusals(t, []earlyRefusal{
		{
			name: "summarize with a threshold above one",
			run: func() error {
				_, err := Summarize("text", summarize(func(o *SummarizeOptions) { o.CommonOptions.Threshold = 2 }))
				return err
			},
			text: "invalid options: threshold must be between 0 and 1, got 2.000000",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "summarize with a length unit it does not know",
			run: func() error {
				_, err := Summarize("text", summarize(func(o *SummarizeOptions) { o.LengthUnit = "furlongs" }))
				return err
			},
			text: "invalid options: length unit must be words, sentences, or paragraphs",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "summarize with a compression ceiling above one",
			run: func() error {
				_, err := Summarize("text", summarize(func(o *SummarizeOptions) { o.MaxCompression = 1.5 }))
				return err
			},
			text: "invalid options: max compression must be between 0 and 1, got 1.500000",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "summarize with metadata and a negative compression ceiling",
			run: func() error {
				_, err := SummarizeWithMetadata("text", summarize(func(o *SummarizeOptions) { o.MaxCompression = -0.5 }))
				return err
			},
			text: "invalid options: max compression must be between 0 and 1, got -0.500000",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "summarize with metadata and a threshold that is not a number",
			run: func() error {
				_, err := SummarizeWithMetadata("text", summarize(func(o *SummarizeOptions) { o.CommonOptions.Threshold = math.NaN() }))
				return err
			},
			text: "invalid options: threshold must be between 0 and 1, got NaN",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "rewrite with no formality level",
			run: func() error {
				_, err := Rewrite("text", rewrite(func(o *RewriteOptions) { o.FormalityLevel = 0 }))
				return err
			},
			text: "invalid options: formality level must be between 1 and 10, got 0",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "rewrite with a formality level above ten",
			run: func() error {
				_, err := Rewrite("text", rewrite(func(o *RewriteOptions) { o.FormalityLevel = 11 }))
				return err
			},
			text: "invalid options: formality level must be between 1 and 10, got 11",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "rewrite with metadata and a negative formality level",
			run: func() error {
				_, err := RewriteWithMetadata("text", rewrite(func(o *RewriteOptions) { o.FormalityLevel = -3 }))
				return err
			},
			text: "invalid options: formality level must be between 1 and 10, got -3",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "rewrite with metadata and a threshold below zero",
			run: func() error {
				_, err := RewriteWithMetadata("text", rewrite(func(o *RewriteOptions) { o.CommonOptions.Threshold = -0.1 }))
				return err
			},
			text: "invalid options: threshold must be between 0 and 1, got -0.100000",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "translate with no target language",
			run: func() error {
				_, err := Translate("text", translate(func(o *TranslateOptions) { o.TargetLanguage = "" }))
				return err
			},
			text: "invalid options: target language is required",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "translate with a cultural adaptation above ten",
			run: func() error {
				_, err := Translate("text", translate(func(o *TranslateOptions) { o.CulturalAdaptation = 11 }))
				return err
			},
			text: "invalid options: cultural adaptation must be between 0 and 10, got 11",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "translate with metadata and no target language",
			run: func() error {
				_, err := TranslateWithMetadata("text", translate(func(o *TranslateOptions) { o.TargetLanguage = "" }))
				return err
			},
			text: "invalid options: target language is required",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "translate with metadata and a negative cultural adaptation",
			run: func() error {
				_, err := TranslateWithMetadata("text", translate(func(o *TranslateOptions) { o.CulturalAdaptation = -1 }))
				return err
			},
			text: "invalid options: cultural adaptation must be between 0 and 10, got -1",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "expand with a factor below one",
			run: func() error {
				_, err := Expand("text", expand(func(o *ExpandOptions) { o.ExpansionFactor = 0.5 }))
				return err
			},
			text: "invalid options: expansion factor must be at least 1, got 0.500000",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "expand with a detail level of zero",
			run: func() error {
				_, err := Expand("text", expand(func(o *ExpandOptions) { o.DetailLevel = 0 }))
				return err
			},
			text: "invalid options: detail level must be between 1 and 10, got 0",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "expand with metadata and a detail level above ten",
			run: func() error {
				_, err := ExpandWithMetadata("text", expand(func(o *ExpandOptions) { o.DetailLevel = 11 }))
				return err
			},
			text: "invalid options: detail level must be between 1 and 10, got 11",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "expand with metadata and a factor of zero",
			run: func() error {
				_, err := ExpandWithMetadata("text", expand(func(o *ExpandOptions) { o.ExpansionFactor = 0 }))
				return err
			},
			text: "invalid options: expansion factor must be at least 1, got 0.000000",
			kind: types.KindConfiguration, matches: []error{config},
		},
	})
}
