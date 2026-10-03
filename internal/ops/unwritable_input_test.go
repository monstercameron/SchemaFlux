package ops

import (
	"context"
	"errors"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// unwritableCase is one operation called with an argument encoding/json cannot
// write, or with no value at all where the operation turns its input into text.
type unwritableCase struct {
	name string
	call func() error
}

// An argument of a call that cannot be written as JSON is an invalid request,
// whichever operation was handed it: the caller's value is unusable, which is a
// different thing from an option or a client setting that is wrong. Each
// operation is called with a value no JSON encoding exists for, with no
// provider reachable, and the error has to carry that one kind and not the
// configuration kind a bad option has.
func TestEveryOperationRefusesAnArgumentJSONCannotWrite(t *testing.T) {
	swapLLMCaller(t, func(context.Context, string, string, types.OpOptions) (string, error) {
		t.Error("a model was asked, and the call was to be refused before that")
		return "", errors.New("no model")
	})

	badFunc := func() {}
	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	hostile := struct {
		Name string   `json:"name"`
		Work chan int `json:"work"`
	}{Name: "a", Work: refuseChan()}
	fifty := func() []chan int {
		items := make([]chan int, sortScoringThreshold+1)
		for i := range items {
			items[i] = refuseChan()
		}
		return items
	}

	cases := []unwritableCase{
		{"Classify", func() error { _, err := Classify[chan int, string](refuseChan(), labelOptions()); return err }},
		{"Score", func() error { _, err := Score(refuseChan(), NewScoreOptions()); return err }},
		{"Compare", func() error {
			_, err := Compare(refuseChan(), refuseChan(), NewCompareOptions().WithComparisonAspects([]string{"tone"}))
			return err
		}},
		{"Similar", func() error {
			_, err := Similar(refuseChan(), refuseChan(), NewSimilarOptions().WithAspects([]string{"meaning"}))
			return err
		}},
		{"Annotate of nothing", func() error { _, err := Annotate[any](nil, NewAnnotateOptions()); return err }},
		{"Arbitrate", func() error {
			_, err := Arbitrate([]chan int{refuseChan(), refuseChan()}, ArbitrateOptions{Rules: []string{"cheaper"}})
			return err
		}},
		{"Audit", func() error {
			_, err := Audit(refuseChan(), AuditOptions{Policies: []string{"no secrets"}})
			return err
		}},
		{"Cluster", func() error {
			_, err := Cluster([]chan int{refuseChan(), refuseChan()}, NewClusterOptions())
			return err
		}},
		{"Choose", func() error {
			_, err := Choose([]chan int{refuseChan(), refuseChan()}, NewChooseOptions().WithCriteria([]string{"best"}))
			return err
		}},
		{"Filter", func() error {
			_, err := Filter([]chan int{refuseChan()}, NewFilterOptions().WithCriteria("keep"))
			return err
		}},
		{"Sort", func() error {
			_, err := Sort([]chan int{refuseChan(), refuseChan()}, NewSortOptions().WithCriteria("size"))
			return err
		}},
		{"Sort of more items than one call scores", func() error {
			_, err := Sort(fifty(), NewSortOptions().WithCriteria("size"))
			return err
		}},
		{"Assemble", func() error { _, err := Assemble[flatRecord]([]any{flatRecords[0], refuseChan()}); return err }},
		{"Assemble with a function", func() error { _, err := Assemble[flatRecord]([]any{badFunc}); return err }},
		{"Compress of nothing", func() error { _, err := Compress[any](nil, NewCompressOptions()); return err }},
		{"Conform", func() error { _, err := Conform(refuseChan(), "ISO8601"); return err }},
		{"Conform a cycle", func() error { _, err := Conform(cyclic, "ISO8601"); return err }},
		{"Transform", func() error {
			_, err := Transform[chan int, flatRecord](refuseChan(), NewTransformOptions())
			return err
		}},
		{"Extract of nothing", func() error { _, err := Extract[flatRecord](nil, NewExtractOptions()); return err }},
		{"Critique of nothing", func() error {
			_, err := Critique[any](nil, NewCritiqueOptions().WithCriteria([]string{"clarity"}))
			return err
		}},
		{"Decompose", func() error { _, err := Decompose(refuseChan(), NewDecomposeOptions()); return err }},
		{"DecomposeToSlice", func() error {
			_, err := DecomposeToSlice[chan int, flatRecord](refuseChan(), NewDecomposeOptions())
			return err
		}},
		{"Derive", func() error { _, err := Derive[chan int, flatRecord](refuseChan(), NewDeriveOptions()); return err }},
		{"Enrich", func() error { _, err := Enrich[chan int, flatRecord](refuseChan(), NewEnrichOptions()); return err }},
		{"EnrichInPlace", func() error { _, err := EnrichInPlace(refuseChan(), NewEnrichOptions()); return err }},
		{"Explain", func() error { _, err := Explain(hostile, NewExplainOptions()); return err }},
		{"Explain a function", func() error { _, err := Explain(badFunc, NewExplainOptions()); return err }},
		{"Validate", func() error {
			_, err := Validate(refuseChan(), NewValidateOptions().WithRules("an email"))
			return err
		}},
		{"ValidateLegacy", func() error { _, err := ValidateLegacy(refuseChan(), "an email"); return err }},
		{"Merge", func() error { _, err := Merge([]chan int{refuseChan(), refuseChan()}, "first"); return err }},
		{"MergeWithMetadata", func() error { _, err := MergeWithMetadata([]chan int{refuseChan(), refuseChan()}, "first"); return err }},
		{"Question", func() error {
			_, err := Question[chan int, string](refuseChan(), NewQuestionOptions("what"))
			return err
		}},
		{"Deduplicate", func() error { _, err := Deduplicate([]chan int{refuseChan(), refuseChan()}, 0.5); return err }},
		{"Infer", func() error { _, err := Infer(refuseChan(), NewInferOptions()); return err }},
		{"Interpolate", func() error { _, err := Interpolate([]chan int{refuseChan(), refuseChan()}); return err }},
		{"SemanticMatch sources", func() error {
			_, err := SemanticMatch([]chan int{refuseChan()}, []string{"t"}, NewMatchOptions())
			return err
		}},
		{"SemanticMatch targets", func() error {
			_, err := SemanticMatch([]string{"s"}, []chan int{refuseChan()}, NewMatchOptions())
			return err
		}},
		{"Settle", func() error { _, err := Settle[flatRecord](refuseChan()); return err }},
		{"SettleAdversarial", func() error {
			_, err := SettleAdversarial(AdversarialContext[chan int]{Ours: AdversarialPosition[chan int]{Position: refuseChan()}})
			return err
		}},
		{"Normalize", func() error { _, err := Normalize(refuseChan(), NewNormalizeOptions()); return err }},
		{"Parse of nothing", func() error { _, err := Parse[flatRecord](nil, NewParseOptions()); return err }},
		{"Pivot", func() error { _, err := Pivot[chan int, flatRecord](refuseChan()); return err }},
		{"Predict", func() error { _, err := Predict[flatRecord](refuseChan(), NewPredictOptions()); return err }},
		{"Project", func() error { _, err := Project[chan int, flatRecord](refuseChan()); return err }},
		{"Project with fields excluded", func() error {
			_, err := Project[chan int, flatRecord](refuseChan(), ProjectOptions{Exclude: []string{"name"}})
			return err
		}},
		{"Rank", func() error {
			_, err := Rank([]chan int{refuseChan()}, NewRankOptions().WithQuery("q"))
			return err
		}},
		{"Resolve", func() error { _, err := Resolve([]chan int{refuseChan(), refuseChan()}); return err }},
		{"Suggest", func() error { _, err := Suggest[string](refuseChan(), NewSuggestOptions()); return err }},
		{"Synthesize", func() error {
			_, err := Synthesize[flatRecord]([]any{"a", refuseChan()}, NewSynthesizeOptions())
			return err
		}},
		{"Verify of nothing", func() error { _, err := Verify(nil, NewVerifyOptions()); return err }},
		{"Format", func() error { _, err := Format(refuseChan(), "a table"); return err }},
		{"FormatWithMetadata", func() error { _, err := FormatWithMetadata(refuseChan(), "a table"); return err }},
		{"QuestionLegacy", func() error { _, err := QuestionLegacy(refuseChan(), "what is it?"); return err }},
		{"Extract", func() error { _, err := Extract[flatRecord](refuseChan(), NewExtractOptions()); return err }},
		{"Annotate", func() error { _, err := Annotate(refuseChan(), NewAnnotateOptions()); return err }},
		{"Critique", func() error {
			_, err := Critique(refuseChan(), NewCritiqueOptions().WithCriteria([]string{"clarity"}))
			return err
		}},
		{"Compress", func() error { _, err := Compress(refuseChan(), NewCompressOptions()); return err }},
		{"Parse", func() error { _, err := Parse[flatRecord](refuseChan(), NewParseOptions()); return err }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatal("no error")
			}
			if got := types.KindOf(err); got != types.KindInvalidRequest {
				t.Errorf("kind = %v, want invalid request (%v)", got, err)
			}
			if !errors.Is(err, types.ErrInvalidRequest) {
				t.Errorf("errors.Is(err, ErrInvalidRequest) is false for %v", err)
			}
			if errors.Is(err, types.ErrConfiguration) {
				t.Errorf("errors.Is(err, ErrConfiguration) is true for %v: an argument is not an option", err)
			}
		})
	}
}

// A value of an option that cannot be written as JSON is a configuration error,
// which is the other half of the rule above: the caller's call is well formed
// and one of the settings on it is not. The operation is called with a valid
// argument and an option holding a value no JSON encoding exists for, with no
// provider reachable. Annotate quotes only strings, which JSON can always write,
// so the same refusal there cannot be reached from a call.
func TestEveryOptionValueJSONCannotWriteIsAConfigurationError(t *testing.T) {
	swapLLMCaller(t, func(context.Context, string, string, types.OpOptions) (string, error) {
		t.Error("a model was asked, and the call was to be refused before that")
		return "", errors.New("no model")
	})

	subject := coreTarget{Name: "a", Age: 3}
	cases := []unwritableCase{
		{"Extract examples, a channel", func() error {
			_, err := Extract[coreTarget]("Ada is 36", NewExtractOptions().WithExamples(refuseChan()))
			return err
		}},
		{"Extract examples, a function", func() error {
			_, err := Extract[coreTarget]("Ada is 36", NewExtractOptions().WithExamples(subject, func() {}))
			return err
		}},
		{"Extract examples, a cycle", func() error {
			cyclic := map[string]any{}
			cyclic["self"] = cyclic
			_, err := Extract[coreTarget]("Ada is 36", NewExtractOptions().WithExamples(cyclic))
			return err
		}},
		{"Generate seed data, a channel", func() error {
			_, err := Generate[coreTarget]("make one", NewGenerateOptions().WithSeedData(refuseChan()))
			return err
		}},
		{"Generate seed data, a function", func() error {
			_, err := Generate[coreTarget]("make one", NewGenerateOptions().WithSeedData(func() {}))
			return err
		}},
		{"Generate examples, a channel", func() error {
			_, err := Generate[coreTarget]("make one", NewGenerateOptions().WithExamples(refuseChan()))
			return err
		}},
		{"Generate examples, one of several", func() error {
			_, err := Generate[coreTarget]("make one", NewGenerateOptions().WithExamples(subject, subject, refuseChan()))
			return err
		}},
		{"Enrich context, a channel", func() error {
			_, err := Enrich[coreTarget, coreTarget](subject, NewEnrichOptions().WithContext(map[string]any{"c": refuseChan()}))
			return err
		}},
		{"Enrich context, a function", func() error {
			_, err := Enrich[coreTarget, coreTarget](subject, NewEnrichOptions().WithContext(map[string]any{"c": func() {}}))
			return err
		}},
		{"EnrichInPlace context, a channel", func() error {
			_, err := EnrichInPlace(subject, NewEnrichOptions().WithContext(map[string]any{"c": refuseChan()}))
			return err
		}},
		{"Verify sources, a channel", func() error {
			_, err := Verify("the sky is blue", NewVerifyOptions().WithSources([]any{"a", refuseChan()}))
			return err
		}},
		{"Verify sources, a function", func() error {
			_, err := Verify("the sky is blue", NewVerifyOptions().WithSources([]any{func() {}}))
			return err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatal("no error")
			}
			if got := types.KindOf(err); got != types.KindConfiguration {
				t.Errorf("kind = %v, want configuration (%v)", got, err)
			}
			if !errors.Is(err, types.ErrConfiguration) {
				t.Errorf("errors.Is(err, ErrConfiguration) is false for %v", err)
			}
			if errors.Is(err, types.ErrInvalidRequest) {
				t.Errorf("errors.Is(err, ErrInvalidRequest) is true for %v: an option is not an argument", err)
			}
		})
	}
}

// Diff writes the data of a summary it was asked for, and a value it cannot
// write is reported on the result beside the comparison, which is the answer and
// is kept. The report is an invalid request like any other unusable argument.
func TestDiffReportsADataValueJSONCannotWriteAsAnInvalidRequest(t *testing.T) {
	swapLLMCaller(t, func(context.Context, string, string, types.OpOptions) (string, error) {
		t.Error("a model was asked, and the data cannot be written for it")
		return "", errors.New("no model")
	})
	type record struct {
		Name string   `json:"name"`
		Work chan int `json:"work"`
	}
	result, err := Diff(record{Name: "a"}, record{Name: "b"}, NewDiffOptions().WithIntelligence(types.Smart))
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(result.Modified) != 1 {
		t.Fatalf("the comparison is lost: %+v", result)
	}
	summary := result.SummaryError
	if summary == nil {
		t.Fatal("the summary that could not be written reports no error")
	}
	if types.KindOf(summary) != types.KindInvalidRequest || !errors.Is(summary, types.ErrInvalidRequest) || errors.Is(summary, types.ErrConfiguration) {
		t.Errorf("summary error %v is not an invalid request alone", summary)
	}
}
