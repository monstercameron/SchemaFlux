package schemaflux

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/ops"
)

func TestSummarizeAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	const text = "The quarterly report shows revenue grew twelve percent while costs stayed flat."
	cases := map[string]SummarizeOptions{
		"no options":           {},
		"one options value":    {TargetLength: 3, LengthUnit: "words", Style: "executive", FocusAreas: []string{"revenue"}, Model: "alloc-model"},
		"every common setting": {Model: "alloc-model", Tier: TierSmart, Effort: EffortLow, Mode: ModeStrict, Steering: "plainly", MaxOutputTokens: 100, WebSearch: true},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply: "Revenue grew.",
				call: func(ctx context.Context) error {
					_, err := Summarize(ctx, text, o)
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.Summarize(text, summarizeInternal(ctx, o))
					return err
				},
			})
		})
	}
}

// SummarizeDetail calls its twin in every state, so there is no plain function
// to compare with: it is compared with the twin, which it must add nothing to
// but the call's own bookkeeping.
func TestSummarizeDetailAllocatesAsItsTwinInThePlainState(t *testing.T) {
	const text = "The quarterly report shows revenue grew twelve percent while costs stayed flat."
	o := SummarizeOptions{TargetLength: 4, LengthUnit: "words"}
	requireAllocsLikeTheInternalCall(t, allocCase{
		reply: `{"text":"Revenue grew twelve percent.","key_points":[],"confidence":0.85}`,
		call: func(ctx context.Context) error {
			_, err := SummarizeDetail(ctx, text, o)
			return err
		},
		internal: func(ctx context.Context) error {
			_, err := ops.SummarizeResult(text, summarizeInternal(ctx, o))
			return err
		},
	})
}

// The text functions beside Summarize, in the plain state: each allocates what
// the internal call it wraps allocates. A Detail form is compared with its twin.
// A result that carries a list of a type the root declares itself (the
// alternatives of a translation, the changes of a normalization) is converted
// into a new list, which is one allocation more than the internal call makes when
// the list is not empty, and the tests of those say so.

const allocText = "The quarterly report shows revenue grew twelve percent while costs stayed flat."

type allocMemo struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

func TestRewriteAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	cases := map[string]RewriteOptions{
		"no options":           {},
		"one options value":    {Tone: "formal", Formality: 8, Audience: "executives", Changes: []string{"shorten"}, AvoidWords: []string{"synergy"}, Model: "alloc-model"},
		"every common setting": {Model: "alloc-model", Tier: TierSmart, Effort: EffortLow, Mode: ModeStrict, Steering: "plainly", MaxOutputTokens: 100, WebSearch: true},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply: "Dear team, the system failed again.",
				call: func(ctx context.Context) error {
					_, err := Rewrite(ctx, allocText, o)
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.Rewrite(allocText, rewriteInternal(ctx, o))
					return err
				},
			})
		})
	}
}

func TestRewriteDetailAllocatesAsItsTwinInThePlainState(t *testing.T) {
	o := RewriteOptions{Tone: "formal"}
	requireAllocsLikeTheInternalCall(t, allocCase{
		reply: `{"text":"Dear team, the system failed again.","changes_made":["formal"],"tone_achieved":"formal","confidence":0.9}`,
		call: func(ctx context.Context) error {
			_, err := RewriteDetail(ctx, allocText, o)
			return err
		},
		internal: func(ctx context.Context) error {
			_, err := ops.RewriteResult(allocText, rewriteInternal(ctx, o))
			return err
		},
	})
}

func TestTranslateAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	cases := map[string]TranslateOptions{
		"no options":           {},
		"one options value":    {SourceLanguage: "English", CulturalAdaptation: -1, Formality: "formal", Dialect: "Mexican", Glossary: map[string]string{"report": "informe"}},
		"every common setting": {Model: "alloc-model", Tier: TierSmart, Effort: EffortLow, Mode: ModeStrict, Steering: "plainly", MaxOutputTokens: 100, WebSearch: true},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply: "El informe trimestral muestra que los ingresos crecieron.",
				call: func(ctx context.Context) error {
					_, err := Translate(ctx, allocText, "Spanish", o)
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.Translate(allocText, translateInternal(ctx, "Spanish", o))
					return err
				},
			})
		})
	}
}

func TestTranslateDetailAllocatesAsItsTwinInThePlainState(t *testing.T) {
	o := TranslateOptions{Dialect: "Mexican"}
	requireAllocsLikeTheInternalCall(t, allocCase{
		reply: `{"text":"El informe.","source_language_detected":"English","confidence":0.9,"alternatives":[]}`,
		call: func(ctx context.Context) error {
			_, err := TranslateDetail(ctx, allocText, "Spanish", o)
			return err
		},
		internal: func(ctx context.Context) error {
			_, err := ops.TranslateResult(allocText, translateInternal(ctx, "Spanish", o))
			return err
		},
	})
}

func TestTranslateDetailWithAlternativesAllocatesOneListMoreThanItsTwin(t *testing.T) {
	model := &allocProvider{reply: `{"text":"El informe.","source_language_detected":"English","confidence":0.9,"alternatives":[{"phrase":"p","alternative":"a","context":"c"}]}`}
	ctx := WithClient(context.Background(), Client{Provider: model})
	o := TranslateOptions{}
	wrapped := testing.AllocsPerRun(allocRuns, func() { _, _ = TranslateDetail(ctx, allocText, "Spanish", o) })
	direct := testing.AllocsPerRun(allocRuns, func() { _, _ = ops.TranslateResult(allocText, translateInternal(ctx, "Spanish", o)) })
	if wrapped != direct+1 {
		t.Errorf("the function allocates %v times and its twin %v: want one more, for the list of alternatives", wrapped, direct)
	}
}

func TestExpandAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	cases := map[string]ExpandOptions{
		"no options":           {},
		"one options value":    {Factor: 3, DetailLevel: 8, IncludeExamples: true, ElaborateOn: []string{"growth"}, AddContext: []string{"market"}, Style: "technical"},
		"every common setting": {Model: "alloc-model", Tier: TierSmart, Effort: EffortLow, Mode: ModeStrict, Steering: "plainly", MaxOutputTokens: 100, WebSearch: true},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply: "Revenue grew twelve percent over the previous quarter.",
				call: func(ctx context.Context) error {
					_, err := Expand(ctx, allocText, o)
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.Expand(allocText, expandInternal(ctx, o))
					return err
				},
			})
		})
	}
}

func TestExpandDetailAllocatesAsItsTwinInThePlainState(t *testing.T) {
	o := ExpandOptions{DetailLevel: 7}
	requireAllocsLikeTheInternalCall(t, allocCase{
		reply: `{"text":"Revenue grew twelve percent over the previous quarter.","added_content":["the cause"],"confidence":0.8}`,
		call: func(ctx context.Context) error {
			_, err := ExpandDetail(ctx, allocText, o)
			return err
		},
		internal: func(ctx context.Context) error {
			_, err := ops.ExpandResult(allocText, expandInternal(ctx, o))
			return err
		},
	})
}

func TestCompressAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	in := allocMemo{Title: "Q3 report", Body: allocText}
	cases := map[string]CompressOptions{
		"no options":           {},
		"one options value":    {Ratio: 0.5, RetainFields: []string{"title"}, RemoveFields: []string{"missing"}, Strategy: "lossy", Priority: "facts", OutputFormat: "same", MaxSize: 200},
		"every common setting": {Model: "alloc-model", Tier: TierSmart, Effort: EffortLow, Mode: ModeStrict, Steering: "plainly", MaxOutputTokens: 100, WebSearch: true},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply: `{"compressed":{"title":"Q3 report","body":"Revenue up."},"preserved_info":[],"removed_info":[]}`,
				call: func(ctx context.Context) error {
					_, err := Compress(ctx, in, o)
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.Compress(in, compressInternal(ctx, o))
					return err
				},
			})
		})
	}
}

func TestNormalizeAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	in := allocMemo{Title: "q3  report", Body: allocText}
	cases := map[string]NormalizeOptions{
		"no options":           {},
		"one options value":    {Standard: "ISO_8601", Rules: map[string]string{"title": "title case"}, Case: "title", NoFixTypos: true, Fields: []string{"title"}, Locale: "en-US", Cautious: true},
		"every common setting": {Model: "alloc-model", Tier: TierSmart, Effort: EffortLow, Mode: ModeStrict, Steering: "plainly", MaxOutputTokens: 100, WebSearch: true},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply: `{"normalized":{"title":"q3  report","body":"` + allocText + `"},"changes":[],"warnings":[]}`,
				call: func(ctx context.Context) error {
					_, err := Normalize(ctx, in, o)
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.Normalize(in, normalizeInternal(ctx, o))
					return err
				},
			})
		})
	}
}

func TestNormalizeWithChangesAllocatesOneListMoreThanTheInternalCall(t *testing.T) {
	in := allocMemo{Title: "q3  report", Body: allocText}
	model := &allocProvider{reply: `{"normalized":{"title":"Q3 report","body":"` + allocText + `"},"changes":[{"field":"title","original":"q3  report","normalized":"Q3 report","reason":"r","confidence":0.9}],"warnings":[]}`}
	ctx := WithClient(context.Background(), Client{Provider: model})
	o := NormalizeOptions{}
	wrapped := testing.AllocsPerRun(allocRuns, func() { _, _ = Normalize(ctx, in, o) })
	direct := testing.AllocsPerRun(allocRuns, func() { _, _ = ops.Normalize(in, normalizeInternal(ctx, o)) })
	if wrapped != direct+1 {
		t.Errorf("the function allocates %v times and the internal call %v: want one more, for the list of changes", wrapped, direct)
	}
}

func TestFormatAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	rows := []allocMemo{{Title: "a", Body: "b"}}
	cases := map[string]Options{
		"no options":           {},
		"every common setting": {Model: "alloc-model", Tier: TierFast, Effort: EffortLow, Mode: ModeStrict, Steering: "plainly", MaxOutputTokens: 100, WebSearch: true},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply: "| title | body |",
				call: func(ctx context.Context) error {
					_, err := Format(ctx, rows, "markdown table", o)
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.Format(rows, "markdown table", settingsOnlyOptions(ctx, o))
					return err
				},
			})
		})
	}
}

func TestMergeAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	sources := []allocMemo{{Title: "a", Body: "b"}, {Title: "c", Body: ""}}
	cases := map[string]Options{
		"no options":           {},
		"every common setting": {Model: "alloc-model", Tier: TierFast, Effort: EffortLow, Mode: ModeStrict, Steering: "plainly", MaxOutputTokens: 100, WebSearch: true},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply: `{"title":"a","body":"b"}`,
				call: func(ctx context.Context) error {
					_, err := Merge(ctx, sources, "prefer the first", o)
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.Merge(sources, "prefer the first", settingsOnlyOptions(ctx, o))
					return err
				},
			})
		})
	}
}
