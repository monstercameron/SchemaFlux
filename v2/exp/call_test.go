package exp

import (
	"reflect"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

func TestMergeCommonOptionsUsesLaterNonzeroValues(t *testing.T) {
	base := schemaflux.Options{Model: "model-a", Tier: schemaflux.TierFast, Effort: schemaflux.EffortLow, Mode: schemaflux.ModeStrict, Steering: "first", MaxOutputTokens: 100, WebSearch: true}
	later := schemaflux.Options{Model: "model-b", Tier: schemaflux.TierQuick, Effort: schemaflux.EffortHigh, Mode: schemaflux.ModeCreative, Steering: "second", MaxOutputTokens: 200}
	got := mergeCommonOptions([]schemaflux.Options{base, later})
	want := schemaflux.Options{Model: "model-b", Tier: schemaflux.TierQuick, Effort: schemaflux.EffortHigh, Mode: schemaflux.ModeCreative, Steering: "second", MaxOutputTokens: 200, WebSearch: true}
	if got != want {
		t.Fatalf("mergeCommonOptions() = %+v, want %+v", got, want)
	}
}

func TestMergeCommonOptionsZeroAndOneValues(t *testing.T) {
	if got := mergeCommonOptions(nil); got != (schemaflux.Options{}) {
		t.Fatalf("zero merge = %+v", got)
	}
	want := schemaflux.Options{Model: "single", WebSearch: true}
	if got := mergeCommonOptions([]schemaflux.Options{want}); got != want {
		t.Fatalf("single merge = %+v", got)
	}
}

func TestMergeSynthesizeOptionsCopiesEveryField(t *testing.T) {
	base := SynthesizeOptions{
		Options:  schemaflux.Options{Model: "a", Tier: schemaflux.TierFast, Effort: schemaflux.EffortLow, Mode: schemaflux.ModeStrict, Steering: "a", MaxOutputTokens: 10, WebSearch: true},
		Strategy: "merge", ConflictResolution: "majority", SourcePriorities: []int{0}, OmitCitations: true,
		OmitInsights: true, OutputStructure: "a", FocusAreas: []string{"a"}, ExcludeAspects: []string{"a"},
	}
	later := SynthesizeOptions{
		Options:  schemaflux.Options{Model: "b", Tier: schemaflux.TierQuick, Effort: schemaflux.EffortHigh, Mode: schemaflux.ModeCreative, Steering: "b", MaxOutputTokens: 20},
		Strategy: "compare", ConflictResolution: "latest-wins", SourcePriorities: []int{1},
		OutputStructure: "b", FocusAreas: []string{"b"}, ExcludeAspects: []string{"b"},
	}
	want := later
	want.Options.WebSearch = true
	want.OmitCitations = true
	want.OmitInsights = true
	if got := mergeSynthesizeOptions([]SynthesizeOptions{base, later}); !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeSynthesizeOptions() = %+v, want %+v", got, want)
	}
	if got := mergeSynthesizeOptions(nil); !reflect.DeepEqual(got, SynthesizeOptions{}) {
		t.Fatalf("zero merge = %+v", got)
	}
	if got := mergeSynthesizeOptions([]SynthesizeOptions{base}); !reflect.DeepEqual(got, base) {
		t.Fatalf("one-value merge = %+v, want %+v", got, base)
	}
}
