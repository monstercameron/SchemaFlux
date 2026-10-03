package schemaflux

import (
	"context"
	"reflect"
	"testing"

	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
)

// The tests of the pieces beside Annotate, Explain and Question: the merge of
// their options values (M0), the internal options each builds, and the
// conversion of their results.

func TestMergeAnnotateOptionsCopiesEveryField(t *testing.T) {
	t.Parallel()
	requireMergeCopiesEveryField(t, mergeAnnotateOptions)
}

func TestMergeExplainOptionsCopiesEveryField(t *testing.T) {
	t.Parallel()
	requireMergeCopiesEveryField(t, mergeExplainOptions)
}

func TestMergeQuestionOptionsCopiesEveryField(t *testing.T) {
	t.Parallel()
	requireMergeCopiesEveryField(t, mergeQuestionOptions)
}

func TestMergeOfNoOptionsValuesIsTheZeroValueForTheThreeFunctions(t *testing.T) {
	t.Parallel()
	if !reflect.DeepEqual(mergeAnnotateOptions(nil), AnnotateOptions{}) || !reflect.DeepEqual(mergeExplainOptions(nil), ExplainOptions{}) ||
		!reflect.DeepEqual(mergeQuestionOptions(nil), QuestionOptions{}) {
		t.Error("no options value did not merge to the zero value")
	}
}

func TestAnnotateInternalAppliesTheMeaningOfZeroAndOfANegativeMinConfidence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := []struct {
		name string
		o    AnnotateOptions
		want float64
	}{
		{"zero means 0.5", AnnotateOptions{}, 0.5},
		{"a value is the value", AnnotateOptions{MinConfidence: 0.7}, 0.7},
		{"1 is the value", AnnotateOptions{MinConfidence: 1}, 1},
		{"a negative value is no floor", AnnotateOptions{MinConfidence: -0.2}, 0},
		{"a very negative value is no floor", AnnotateOptions{MinConfidence: -1e9}, 0},
	}
	for _, tc := range cases {
		if got := annotateInternal(ctx, tc.o).MinConfidence; got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestAnnotateInternalStartsFromTheConstructorAndLaysOnlyWhatIsSet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	zero := annotateInternal(ctx, AnnotateOptions{})
	if !reflect.DeepEqual(zero.AnnotationTypes, []string{"entities", "sentiment"}) || !zero.IncludeConfidence || zero.Format != "structured" ||
		zero.CustomSchema != nil || zero.Language != "" || zero.Domain != "" {
		t.Errorf("the zero value did not leave the constructor's value: %+v", zero)
	}
	if err := zero.Validate(); err != nil {
		t.Errorf("the internal check refuses the zero options: %v", err)
	}
	if got := annotateInternal(ctx, AnnotateOptions{Types: []string{}}).AnnotationTypes; got == nil || len(got) != 0 {
		t.Errorf("an empty list of kinds was not laid as a value: %#v", got)
	}
	set := annotateInternal(ctx, AnnotateOptions{
		Types: []string{"topics"}, CustomSchema: map[string]string{"k": "d"}, OmitConfidence: true, Language: "fr", Domain: "law",
		Options: Options{Model: "m", Tier: TierSmart, MaxOutputTokens: 9, WebSearch: true},
	})
	if set.AnnotationTypes[0] != "topics" || set.CustomSchema["k"] != "d" || set.IncludeConfidence || set.Language != "fr" || set.Domain != "law" {
		t.Errorf("an own field was not laid: %+v", set)
	}
	if set.OpOptions.Model != "m" || set.CommonOptions.Model != "m" || set.CommonOptions.Intelligence != types.Smart || set.OpOptions.MaxOutputTokens != 9 || !set.OpOptions.WebSearch {
		t.Errorf("a common setting was not laid on both halves: %+v", set)
	}
	if set.CommonOptions.Context != ctx {
		t.Error("the context is not on the half the internal call reads it from")
	}
}

func TestExplainInternalStartsFromTheConstructorSoTheZeroOptionsAreNotRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	zero := explainInternal(ctx, ExplainOptions{})
	if zero.Audience != "non-technical" || zero.Depth != 2 || zero.Format != "paragraph" || zero.Focus != "overview" || zero.Background != "" ||
		zero.OpOptions.Context != ctx {
		t.Errorf("the zero value did not leave the constructor's value: %+v", zero)
	}
	if err := zero.Validate(); err != nil {
		t.Errorf("the internal check refuses the zero options: %v", err)
	}
	set := explainInternal(ctx, ExplainOptions{Audience: "expert", Depth: 4, Format: "qa", Focus: "usage", Background: "b", Options: Options{Tier: TierSmart}})
	if set.Audience != "expert" || set.Depth != 4 || set.Format != "qa" || set.Focus != "usage" || set.Background != "b" || set.Intelligence != types.Smart {
		t.Errorf("set options: %+v", set)
	}
}

func TestQuestionInternalNegatesEachOmittedPart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := []struct {
		name                     string
		o                        QuestionOptions
		evidence, confidence, re bool
	}{
		{"nothing omitted", QuestionOptions{}, true, true, true},
		{"evidence omitted", QuestionOptions{OmitEvidence: true}, false, true, true},
		{"confidence omitted", QuestionOptions{OmitConfidence: true}, true, false, true},
		{"reasoning omitted", QuestionOptions{OmitReasoning: true}, true, true, false},
		{"everything omitted", QuestionOptions{OmitEvidence: true, OmitConfidence: true, OmitReasoning: true}, false, false, false},
	}
	for _, tc := range cases {
		got := questionInternal(ctx, "Why?", tc.o)
		if got.Question != "Why?" || got.IncludeEvidence != tc.evidence || got.IncludeConfidence != tc.confidence || got.IncludeReasoning != tc.re {
			t.Errorf("%s: %+v", tc.name, got)
		}
		if got.CommonOptions.Context != ctx {
			t.Errorf("%s: the context is not on the half the internal call reads it from", tc.name)
		}
		if err := got.Validate(); err != nil {
			t.Errorf("%s: the internal check refuses the mapped options: %v", tc.name, err)
		}
	}
}

func TestTheResultOfAnnotateConvertsEachAnnotationAndBuildsTheMapByTypeFromThem(t *testing.T) {
	t.Parallel()
	meta := map[string]any{"kind": "person"}
	first := ops.Annotation{Type: "entity", Value: "Ada", Text: "Ada", Start: 0, End: 3, ModelConfidence: 0.9, Metadata: meta}
	second := ops.Annotation{Type: "sentiment", Value: "positive", ModelConfidence: 0.6}
	third := ops.Annotation{Type: "entity", Value: "Go", Start: 10, End: 12, ModelConfidence: 0.7}
	got := annotateResult(ops.AnnotateResult{
		Original:    "Ada writes Go",
		Annotations: []ops.Annotation{first, second, third},
		ByType:      map[string][]ops.Annotation{"entity": {first, third}, "sentiment": {second}},
		Summary:     map[string]any{"n": 3},
	})
	wantFirst := Annotation{Type: "entity", Value: "Ada", Text: "Ada", Start: 0, End: 3, ModelConfidence: 0.9, Attributes: meta}
	wantThird := Annotation{Type: "entity", Value: "Go", Start: 10, End: 12, ModelConfidence: 0.7}
	want := AnnotateResult{
		Original:    "Ada writes Go",
		Annotations: []Annotation{wantFirst, {Type: "sentiment", Value: "positive", ModelConfidence: 0.6}, wantThird},
		ByType:      map[string][]Annotation{"entity": {wantFirst, wantThird}, "sentiment": {{Type: "sentiment", Value: "positive", ModelConfidence: 0.6}}},
		Summary:     map[string]any{"n": 3},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestTheResultsOfTheThreeFunctionsConvertAndTheirZeroConvertsToTheZero(t *testing.T) {
	t.Parallel()
	if got := annotateResult(ops.AnnotateResult{}); !reflect.DeepEqual(got, AnnotateResult{}) {
		t.Errorf("the zero result of Annotate converted to %+v", got)
	}
	if got := explainResult(ops.ExplainResult{}); !reflect.DeepEqual(got, ExplainResult{}) {
		t.Errorf("the zero result of Explain converted to %+v", got)
	}
	if got := questionResult(ops.QuestionResult[string]{}); !reflect.DeepEqual(got, QuestionResult[string]{}) {
		t.Errorf("the zero result of Question converted to %+v", got)
	}
	// An empty result that is not the zero one keeps its empty, non-nil members.
	empty := annotateResult(ops.AnnotateResult{Original: "x", Annotations: []ops.Annotation{}, ByType: map[string][]ops.Annotation{}})
	if empty.Annotations == nil || empty.ByType == nil || len(empty.Annotations) != 0 || len(empty.ByType) != 0 {
		t.Errorf("an empty answer did not convert to empty members: %#v", empty)
	}
}

func TestTheResultOfExplainTakesTheThreeWorkedOutFieldsFromTheMetadata(t *testing.T) {
	t.Parallel()
	got := explainResult(ops.ExplainResult{
		Explanation: "e", Summary: "s", KeyPoints: []string{"k"}, Audience: "expert", Complexity: "advanced",
		Metadata: map[string]any{"data_type": "main.plan", "field_count": 7, "estimated_complexity": "medium", "explanation_depth": 2},
	})
	want := ExplainResult{Explanation: "e", Summary: "s", KeyPoints: []string{"k"}, DataType: "main.plan", FieldCount: 7, DataComplexity: "medium"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	// A metadata entry of another type is left out and does not panic.
	odd := explainResult(ops.ExplainResult{Metadata: map[string]any{"data_type": 3, "field_count": "x", "estimated_complexity": 1.5}})
	if !reflect.DeepEqual(odd, ExplainResult{}) {
		t.Errorf("a metadata entry of the wrong type was carried: %+v", odd)
	}
}

func TestTheResultOfQuestionConvertsAndDropsTheMetadata(t *testing.T) {
	t.Parallel()
	got := questionResult(ops.QuestionResult[int]{Answer: 3, ModelConfidence: 0.4, Reasoning: "r", Evidence: []string{"e"}, Metadata: map[string]any{"k": 1}})
	if !reflect.DeepEqual(got, QuestionResult[int]{Answer: 3, ModelConfidence: 0.4, Reasoning: "r", Evidence: []string{"e"}}) {
		t.Errorf("got %+v", got)
	}
}
