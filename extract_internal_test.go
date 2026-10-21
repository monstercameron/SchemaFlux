package schemaflux

import (
	"context"
	"reflect"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// The tests of the pieces beside Transform, Generate, Infer and Parse: the merge
// of their options values (M0), the internal options each builds, and the
// conversion of their results.

func TestMergeTransformOptionsCopiesEveryField(t *testing.T) {
	t.Parallel()
	requireMergeCopiesEveryField(t, mergeTransformOptions)
}

func TestMergeGenerateOptionsCopiesEveryField(t *testing.T) {
	t.Parallel()
	requireMergeCopiesEveryField(t, mergeGenerateOptions)
}

func TestMergeInferOptionsCopiesEveryField(t *testing.T) {
	t.Parallel()
	requireMergeCopiesEveryField(t, mergeInferOptions)
}

func TestMergeParseOptionsCopiesEveryField(t *testing.T) {
	t.Parallel()
	requireMergeCopiesEveryField(t, mergeParseOptions)
}

func TestMergeOfNoOptionsValuesIsTheZeroValueForTheFourFunctions(t *testing.T) {
	t.Parallel()
	if !reflect.DeepEqual(mergeTransformOptions(nil), TransformOptions{}) ||
		!reflect.DeepEqual(mergeGenerateOptions(nil), GenerateOptions{}) ||
		!reflect.DeepEqual(mergeInferOptions(nil), InferOptions{}) ||
		!reflect.DeepEqual(mergeParseOptions(nil), ParseOptions{}) {
		t.Error("no options value did not merge to the zero value")
	}
}

func TestMergeGenerateOptionsTakesALaterSeedDataAndKeepsTheEarlierOneWhenTheLaterIsNil(t *testing.T) {
	t.Parallel()
	first := GenerateOptions{SeedData: map[string]int{"a": 1}}
	later := GenerateOptions{SeedData: []int{1, 2}}
	if got := mergeGenerateOptions([]GenerateOptions{first, later}); !reflect.DeepEqual(got.SeedData, []int{1, 2}) {
		t.Errorf("a later seed was not taken: %+v", got.SeedData)
	}
	if got := mergeGenerateOptions([]GenerateOptions{first, {}}); !reflect.DeepEqual(got.SeedData, map[string]int{"a": 1}) {
		t.Errorf("a later nil seed replaced the earlier one: %+v", got.SeedData)
	}
}

func TestTransformInternalStartsFromTheConstructorAndLaysOnlyWhatIsSet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	zero := transformInternal(ctx, TransformOptions{})
	want := ops.NewTransformOptions()
	if zero.MergeStrategy != "replace" || zero.TransformLogic != "" || zero.MappingRules != nil || zero.PreserveFields != nil ||
		zero.CommonOptions.Mode != want.CommonOptions.Mode || zero.CommonOptions.Intelligence != want.CommonOptions.Intelligence {
		t.Errorf("the zero value did not leave the constructor's value: %+v", zero)
	}
	set := transformInternal(ctx, TransformOptions{
		MappingRules: map[string]string{"a": "b"}, PreserveFields: []string{"id"}, MergeStrategy: "append", Logic: "join",
		Options: Options{Model: "m", Tier: TierSmart, Effort: EffortLow, Mode: ModeStrict, Steering: "s", MaxOutputTokens: 9, WebSearch: true},
	})
	if set.MappingRules["a"] != "b" || set.PreserveFields[0] != "id" || set.MergeStrategy != "append" || set.TransformLogic != "join" {
		t.Errorf("an own field was not laid: %+v", set)
	}
	if set.CommonOptions.Model != "m" || set.OpOptions.Model != "m" || set.CommonOptions.Intelligence != types.Smart || set.CommonOptions.Effort != "low" || set.CommonOptions.Mode != types.Strict ||
		set.CommonOptions.Steering != "s" || set.OpOptions.MaxOutputTokens != 9 || !set.OpOptions.WebSearch {
		t.Errorf("a common setting was not laid on both halves: %+v", set)
	}
	if set.CommonOptions.Context != ctx {
		t.Error("the context is not on the half the internal call reads it from")
	}
}

func TestGenerateInternalKeepsTheCountThatTheInternalCallRuns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if got := generateInternal(ctx, GenerateOptions{}); got.Count != 1 || got.CommonOptions.Mode != types.Creative || got.CommonOptions.Intelligence != types.Fast {
		t.Errorf("zero options: count %d mode %v tier %v, want 1, creative, fast", got.Count, got.CommonOptions.Mode, got.CommonOptions.Intelligence)
	}
	set := generateInternal(ctx, GenerateOptions{
		Template: "t", Style: "s", Constraints: map[string]any{"k": 1}, SeedData: "seed", Examples: []any{"e"}, EnsureUnique: true,
		Options: Options{Mode: ModeStrict},
	})
	if set.Count != 1 || set.Template != "t" || set.Style != "s" || set.Constraints["k"] != 1 || set.SeedData != "seed" ||
		len(set.Examples) != 1 || !set.EnsureUnique || set.CommonOptions.Mode != types.Strict {
		t.Errorf("an own field was not laid: %+v", set)
	}
}

func TestInferAndParseInternalLayOnlyWhatIsSetAndPutTheContextWhereTheyReadIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if got := inferInternal(ctx, InferOptions{}); got.Background != "" || got.Mode != types.TransformMode || got.Intelligence != types.Fast || got.OpOptions.Context != ctx {
		t.Errorf("infer, zero options: %+v", got)
	}
	if got := inferInternal(ctx, InferOptions{Background: "b", Options: Options{Tier: TierQuick}}); got.Background != "b" || got.Intelligence != types.Quick {
		t.Errorf("infer, set: %+v", got)
	}
	if got := parseInternal(ctx, ParseOptions{}); got.AllowLLMFallback || got.AutoFix || got.FormatHints != nil || got.CustomDelimiters != nil || got.OpOptions.Context != ctx {
		t.Errorf("parse, zero options: %+v", got)
	}
	got := parseInternal(ctx, ParseOptions{AllowLLMFallback: true, AutoFix: true, FormatHints: []string{"json"}, CustomDelimiters: []string{";"}})
	if !got.AllowLLMFallback || !got.AutoFix || got.FormatHints[0] != "json" || got.CustomDelimiters[0] != ";" {
		t.Errorf("parse, set: %+v", got)
	}
}

func TestTheResultOfParseConvertsAndItsZeroConvertsToTheZero(t *testing.T) {
	t.Parallel()
	type person struct{ Name string }
	if got := parseResult(ops.ParseResult[person]{}); !reflect.DeepEqual(got, ParseResult[person]{}) {
		t.Errorf("the zero result converted to %+v", got)
	}
	got := parseResult(ops.ParseResult[person]{Data: person{Name: "Ada"}, Format: "json"})
	if got.Data.Name != "Ada" || got.Format != "json" {
		t.Errorf("got %+v", got)
	}
}
