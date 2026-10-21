package schemaflux

import (
	"reflect"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/ops"
)

func TestMergeRewriteOptionsCopiesEveryField(t *testing.T) {
	t.Parallel()
	requireMergeCopiesEveryField(t, mergeRewriteOptions)
}

func TestMergeTranslateOptionsCopiesEveryField(t *testing.T) {
	t.Parallel()
	requireMergeCopiesEveryField(t, mergeTranslateOptions)
}

func TestMergeExpandOptionsCopiesEveryField(t *testing.T) {
	t.Parallel()
	requireMergeCopiesEveryField(t, mergeExpandOptions)
}

func TestMergeCompressOptionsCopiesEveryField(t *testing.T) {
	t.Parallel()
	requireMergeCopiesEveryField(t, mergeCompressOptions)
}

func TestMergeNormalizeOptionsCopiesEveryField(t *testing.T) {
	t.Parallel()
	requireMergeCopiesEveryField(t, mergeNormalizeOptions)
}

// The result of the internal call beside an error is the zero value, and the
// conversion of a zero value is the zero value: nothing is invented for a call
// that failed.
func TestTheTextResultsOfTheInternalCallsConvertTheZeroValueToTheZeroValue(t *testing.T) {
	t.Parallel()
	if got := rewrittenOf(ops.Rewritten{}); !reflect.DeepEqual(got, Rewritten{}) {
		t.Errorf("rewrittenOf(zero) = %+v", got)
	}
	if got := translationOf(ops.Translation{}); !reflect.DeepEqual(got, Translation{}) {
		t.Errorf("translationOf(zero) = %+v", got)
	}
	if got := expansionOf(ops.Expansion{}); !reflect.DeepEqual(got, Expansion{}) {
		t.Errorf("expansionOf(zero) = %+v", got)
	}
	if got := compressResult(ops.CompressResult[string]{}); !reflect.DeepEqual(got, CompressResult[string]{}) {
		t.Errorf("compressResult(zero) = %+v", got)
	}
	if got := compressResult(ops.CompressResult[map[string]int]{}); !reflect.DeepEqual(got, CompressResult[map[string]int]{}) {
		t.Errorf("compressResult(zero) of a map = %+v", got)
	}
	if got := normalizeResult(ops.NormalizeResult[string]{}); !reflect.DeepEqual(got, NormalizeResult[string]{}) {
		t.Errorf("normalizeResult(zero) = %+v", got)
	}
	if got := normalizeResult(ops.NormalizeResult[struct{ A []int }]{}); !reflect.DeepEqual(got, NormalizeResult[struct{ A []int }]{}) {
		t.Errorf("normalizeResult(zero) of a struct = %+v", got)
	}
}

// The internal metadata maps are not carried into the results: nothing the
// root declares holds a map the model could put a claim in.
func TestTheTextResultsDropTheInternalMetadataAndKeepEverythingElse(t *testing.T) {
	t.Parallel()
	meta := map[string]any{"length_measured": 3}
	if got, want := rewrittenOf(ops.Rewritten{Text: "t", ChangesMade: []string{"c"}, ModelConfidence: 0.5, ToneAchieved: "x", Metadata: meta}),
		(Rewritten{Text: "t", ChangesMade: []string{"c"}, ToneAchieved: "x", ModelConfidence: 0.5}); !reflect.DeepEqual(got, want) {
		t.Errorf("rewrittenOf = %+v, want %+v", got, want)
	}
	if got, want := translationOf(ops.Translation{Text: "t", SourceLanguageDetected: "en", ModelConfidence: 0.5, Metadata: meta,
		Alternatives: []ops.TranslationAlternative{{Phrase: "p", Alternative: "a", Context: "c"}}}),
		(Translation{Text: "t", SourceLanguageDetected: "en", ModelConfidence: 0.5, Alternatives: []TranslationAlternative{{Phrase: "p", Alternative: "a", Context: "c"}}}); !reflect.DeepEqual(got, want) {
		t.Errorf("translationOf = %+v, want %+v", got, want)
	}
	if got, want := expansionOf(ops.Expansion{Text: "t", ExpansionRatio: 2, AddedContent: []string{"a"}, ModelConfidence: 0.5, Metadata: meta}),
		(Expansion{Text: "t", AddedContent: []string{"a"}, ModelConfidence: 0.5, ExpansionRatio: 2}); !reflect.DeepEqual(got, want) {
		t.Errorf("expansionOf = %+v, want %+v", got, want)
	}
	if got, want := compressResult(ops.CompressResult[string]{Compressed: "c", OriginalSize: 9, CompressedSize: 1, ActualRatio: 0.1, PreservedInfo: []string{"p"}, RemovedInfo: []string{"r"}, Metadata: meta}),
		(CompressResult[string]{Compressed: "c", OriginalSize: 9, CompressedSize: 1, ActualRatio: 0.1, PreservedInfo: []string{"p"}, RemovedInfo: []string{"r"}}); !reflect.DeepEqual(got, want) {
		t.Errorf("compressResult = %+v, want %+v", got, want)
	}
	if got, want := normalizeResult(ops.NormalizeResult[string]{Normalized: "n", Changes: []ops.NormalizeChange{{Field: "f", Original: "o", Normalized: "n", Reason: "r", ModelConfidence: 0.5}},
		ChangedFields: []string{"f"}, Unreported: []string{"u"}, TotalChanges: 1, Warnings: []string{"w"}, Metadata: meta}),
		(NormalizeResult[string]{Normalized: "n", Changes: []NormalizeChange{{Field: "f", Original: "o", Normalized: "n", Reason: "r", ModelConfidence: 0.5}},
			ChangedFields: []string{"f"}, Unreported: []string{"u"}, TotalChanges: 1, Warnings: []string{"w"}}); !reflect.DeepEqual(got, want) {
		t.Errorf("normalizeResult = %+v, want %+v", got, want)
	}
}

// A translation with no alternatives has none (nil), not an empty list, so that
// it marshals as the internal one does.
func TestATranslationWithNoAlternativesHasNilAlternatives(t *testing.T) {
	t.Parallel()
	for name, in := range map[string][]ops.TranslationAlternative{"nil": nil, "empty": {}} {
		if got := translationOf(ops.Translation{Text: "t", Alternatives: in}); got.Alternatives != nil {
			t.Errorf("%s: Alternatives = %#v, want nil", name, got.Alternatives)
		}
	}
}

// Each internal options value starts from the constructor's, so that a setting
// left at its zero keeps the operation's own default (M2): the defaults the
// documentation of each options type states.
func TestTheInternalTextOptionsKeepTheConstructorsDefaultsForZeroSettings(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	if o := rewriteInternal(ctx, RewriteOptions{}); o.FormalityLevel != 5 || !o.PreserveFacts {
		t.Errorf("rewrite: formality %d preserve facts %v", o.FormalityLevel, o.PreserveFacts)
	}
	if o := translateInternal(ctx, "French", TranslateOptions{}); o.CulturalAdaptation != 5 || !o.PreserveFormatting || o.Formality != "neutral" || o.TargetLanguage != "French" {
		t.Errorf("translate: adaptation %d preserve formatting %v formality %q language %q", o.CulturalAdaptation, o.PreserveFormatting, o.Formality, o.TargetLanguage)
	}
	if o := expandInternal(ctx, ExpandOptions{}); o.ExpansionFactor != 2 || o.DetailLevel != 5 || o.ExpansionStyle != "balanced" {
		t.Errorf("expand: factor %v detail %d style %q", o.ExpansionFactor, o.DetailLevel, o.ExpansionStyle)
	}
	if o := compressInternal(ctx, CompressOptions{}); o.CompressionRatio != 0.3 || o.Strategy != "semantic" || o.Priority != "all" || o.OutputFormat != "same" || o.MaxOutputSize != 0 {
		t.Errorf("compress: ratio %v strategy %q priority %q format %q max %d", o.CompressionRatio, o.Strategy, o.Priority, o.OutputFormat, o.MaxOutputSize)
	}
	if o := normalizeInternal(ctx, NormalizeOptions{}); !o.FixTypos || !o.NormalizeWhitespace || o.NormalizeCase != "" || o.Strict {
		t.Errorf("normalize: fix typos %v whitespace %v case %q strict %v", o.FixTypos, o.NormalizeWhitespace, o.NormalizeCase, o.Strict)
	}
}

func TestTheInternalTextOptionsCarryTheContextTheyAreBuiltOn(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	if rewriteInternal(ctx, RewriteOptions{}).CommonOptions.Context != ctx ||
		translateInternal(ctx, "x", TranslateOptions{}).CommonOptions.Context != ctx ||
		expandInternal(ctx, ExpandOptions{}).CommonOptions.Context != ctx ||
		compressInternal(ctx, CompressOptions{}).CommonOptions.Context != ctx ||
		normalizeInternal(ctx, NormalizeOptions{}).CommonOptions.Context != ctx ||
		settingsOnlyOptions(ctx, Options{}).Context != ctx {
		t.Error("an internal options value does not carry the context it was built on")
	}
}

func TestCheckWholeRangeAcceptsZeroAndTheRangeAndRefusesTheRest(t *testing.T) {
	t.Parallel()
	for n, ok := range map[int]bool{0: true, 1: true, 5: true, 10: true, -1: false, 11: false, -100: false, 1 << 40: false} {
		if err := checkWholeRange("rewrite", "Formality", n, 1, 10); (err == nil) != ok {
			t.Errorf("%d: err = %v, want ok = %v", n, err, ok)
		}
	}
}
