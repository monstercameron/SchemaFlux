package schemaflux

import (
	"context"
	"reflect"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// The tests of the pieces beside Complete, CompleteField and Suggest: the merge
// of their options values (M0), the internal options each builds, and the
// conversion of their results.

func TestMergeCompleteOptionsCopiesEveryField(t *testing.T) {
	t.Parallel()
	requireMergeCopiesEveryField(t, mergeCompleteOptions)
}

func TestMergeSuggestOptionsCopiesEveryField(t *testing.T) {
	t.Parallel()
	requireMergeCopiesEveryField(t, mergeSuggestOptions)
}

func TestMergeOfNoOptionsValuesIsTheZeroValueForCompleteAndSuggest(t *testing.T) {
	t.Parallel()
	if !reflect.DeepEqual(mergeCompleteOptions(nil), CompleteOptions{}) || !reflect.DeepEqual(mergeSuggestOptions(nil), SuggestOptions{}) {
		t.Error("no options value did not merge to the zero value")
	}
}

func TestCompleteInternalKeepsTheConstructorsSamplingSettingsSoTheInternalCheckPasses(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	got := completeInternal(ctx, CompleteOptions{})
	want := ops.NewCompleteOptions()
	if got.MaxLength != 100 || got.Temperature != want.Temperature || got.TopP != want.TopP || got.TopK != want.TopK ||
		got.Mode != types.Creative || got.Intelligence != types.Smart || got.OpOptions.Context != ctx {
		t.Errorf("zero options: %+v", got)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("the internal check refuses the zero options: %v", err)
	}
	set := completeInternal(ctx, CompleteOptions{Background: []string{"b"}, MaxLength: 7, StopSequences: []string{"END"}, Options: Options{Tier: TierFast, Mode: ModeStrict}})
	if set.Background[0] != "b" || set.MaxLength != 7 || set.StopSequences[0] != "END" || set.Intelligence != types.Fast || set.Mode != types.Strict {
		t.Errorf("set options: %+v", set)
	}
}

func TestCompleteFieldInternalNamesTheFieldAndKeepsTheOptionsOfComplete(t *testing.T) {
	t.Parallel()
	got := completeFieldInternal(context.Background(), "Body", CompleteOptions{MaxLength: 12})
	if got.FieldName != "Body" || got.MaxLength != 12 || got.Temperature != 0.7 {
		t.Errorf("got %+v", got)
	}
}

func TestSuggestInternalStartsFromTheConstructorAndLaysOnlyWhatIsSet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	zero := suggestInternal(ctx, SuggestOptions{})
	if zero.Strategy != ops.SuggestContextual || zero.TopN != 5 || !zero.Ranked || zero.IncludeScores || zero.IncludeReasons ||
		zero.Domain != "" || zero.Constraints != nil || zero.Categories != nil || zero.CommonOptions.Mode != types.TransformMode || zero.CommonOptions.Intelligence != types.Smart {
		t.Errorf("the zero value did not leave the constructor's value: %+v", zero)
	}
	set := suggestInternal(ctx, SuggestOptions{
		Strategy: "hybrid", TopN: 2, NoRanking: true, IncludeScores: true, IncludeReasons: true, Domain: "d",
		Constraints: []string{"c"}, Categories: []string{"k"}, Options: Options{Tier: TierQuick, MaxOutputTokens: 9},
	})
	if set.Strategy != ops.SuggestHybrid || set.TopN != 2 || set.Ranked || !set.IncludeScores || !set.IncludeReasons || set.Domain != "d" ||
		set.Constraints[0] != "c" || set.Categories[0] != "k" || set.CommonOptions.Intelligence != types.Quick || set.OpOptions.MaxOutputTokens != 9 {
		t.Errorf("set options: %+v", set)
	}
	if set.CommonOptions.Context != ctx {
		t.Error("the context is not on the half the internal call reads it from")
	}
	if err := set.Validate(); err != nil {
		t.Errorf("the internal check refuses the mapped options: %v", err)
	}
}

func TestTheResultsOfCompleteAndCompleteFieldConvertAndTheirZeroConvertsToTheZero(t *testing.T) {
	t.Parallel()
	type post struct{ Body string }
	if got := completeResult(ops.CompleteResult{}); got != (CompleteResult{}) {
		t.Errorf("the zero result converted to %+v", got)
	}
	got := completeResult(ops.CompleteResult{Text: "a b", Original: "a", Length: 2, ModelConfidence: 0.9, Metadata: map[string]any{"k": 1}})
	if got != (CompleteResult{Text: "a b", Original: "a", Length: 2}) {
		t.Errorf("got %+v", got)
	}
	if got := completeFieldResult(ops.CompleteFieldResult[post]{}); got != (CompleteFieldResult[post]{}) {
		t.Errorf("the zero result converted to %+v", got)
	}
	field := completeFieldResult(ops.CompleteFieldResult[post]{Data: post{Body: "x y"}, Field: "Body", Original: "x", Completed: "x y", Length: 2, ModelConfidence: 0.5})
	if field != (CompleteFieldResult[post]{Data: post{Body: "x y"}, Field: "Body", Original: "x", Completed: "x y", Length: 2}) {
		t.Errorf("got %+v", field)
	}
}
