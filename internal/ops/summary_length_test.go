package ops

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// A caller asking Summarize for 50 words could be given 400 and told nothing.
// The result form already refused a summary more than 20% over its target; what
// it did not do was say how long a summary it accepted was, so a summary of 55
// words against a target of 50 came back looking like one of 40. The figures are
// in the summary's Metadata now, measured in Go.

func dx12Words(n int) string { return strings.TrimSpace(strings.Repeat("word ", n)) }

func TestSummaryLengthIsMeasuredAgainstTheTarget(t *testing.T) {
	cases := []struct {
		name         string
		text         string
		target       int
		unit         string
		wantNil      bool
		wantMeasured int
		wantUnit     string
		wantWithin   bool
	}{
		{name: "no target says nothing", text: dx12Words(40), target: 0, unit: "words", wantNil: true},
		{name: "a negative target says nothing", text: dx12Words(40), target: -3, unit: "words", wantNil: true},
		{name: "words at the target", text: dx12Words(50), target: 50, unit: "words", wantMeasured: 50, wantUnit: "words", wantWithin: true},
		{name: "words under the target", text: dx12Words(40), target: 50, unit: "words", wantMeasured: 40, wantUnit: "words", wantWithin: true},
		{name: "words one over the target", text: dx12Words(51), target: 50, unit: "words", wantMeasured: 51, wantUnit: "words", wantWithin: false},
		{name: "words at the edge of the tolerance", text: dx12Words(60), target: 50, unit: "words", wantMeasured: 60, wantUnit: "words", wantWithin: false},
		{name: "sentences within the target", text: "One. Two.", target: 3, unit: "sentences", wantMeasured: 2, wantUnit: "sentences", wantWithin: true},
		{name: "sentences over the target", text: "One. Two. Three. Four.", target: 3, unit: "sentences", wantMeasured: 4, wantUnit: "sentences", wantWithin: false},
		{name: "paragraphs", text: "One.\n\nTwo.", target: 2, unit: "paragraphs", wantMeasured: 2, wantUnit: "paragraphs", wantWithin: true},
		{name: "characters are runes, not bytes", text: "日本語", target: 3, unit: "characters", wantMeasured: 3, wantUnit: "characters", wantWithin: true},
		{name: "no unit is measured in characters", text: "abcdef", target: 5, unit: "", wantMeasured: 6, wantUnit: "characters", wantWithin: false},
		{name: "words split on any whitespace", text: "one  two\nthree\tfour", target: 4, unit: "words", wantMeasured: 4, wantUnit: "words", wantWithin: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := NewSummarizeOptions()
			opts.TargetLength, opts.LengthUnit = tc.target, tc.unit

			got := dx12SummaryLength(tc.text, opts)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("a length was reported with no target: %v", got)
				}
				return
			}

			if got["length_measured"] != tc.wantMeasured {
				t.Errorf("length_measured = %v, want %d", got["length_measured"], tc.wantMeasured)
			}
			if got["length_unit"] != tc.wantUnit {
				t.Errorf("length_unit = %v, want %q", got["length_unit"], tc.wantUnit)
			}
			if got["length_target"] != tc.target {
				t.Errorf("length_target = %v, want %d", got["length_target"], tc.target)
			}
			if got["length_within_target"] != tc.wantWithin {
				t.Errorf("length_within_target = %v, want %v", got["length_within_target"], tc.wantWithin)
			}
			if got["length_tolerance"] != summaryLengthTolerance {
				t.Errorf("length_tolerance = %v, want %v", got["length_tolerance"], summaryLengthTolerance)
			}
		})
	}
}

// dx12Summarize runs the result form against a scripted answer.
func dx12Summarize(t *testing.T, summary string, opts SummarizeOptions) (types.Result[Summary], error) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"text": summary, "key_points": []string{"a point"}, "confidence": 0.9})
	if err != nil {
		t.Fatalf("marshal the scripted answer: %v", err)
	}

	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) { return string(body), nil })
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})
	return SummarizeResult("a long document", opts)
}

func TestSummarizeResultReportsTheLengthItAccepted(t *testing.T) {
	cases := []struct {
		name       string
		summary    string
		target     int
		unit       string
		wantErr    bool
		wantWithin bool
		wantNil    bool
	}{
		{name: "under the target", summary: dx12Words(40), target: 50, unit: "words", wantWithin: true},
		{name: "at the target", summary: dx12Words(50), target: 50, unit: "words", wantWithin: true},
		{name: "over the target and inside the tolerance is returned and says so", summary: dx12Words(55), target: 50, unit: "words", wantWithin: false},
		{name: "at the top of the tolerance", summary: dx12Words(60), target: 50, unit: "words", wantWithin: false},
		{name: "past the tolerance is refused", summary: dx12Words(61), target: 50, unit: "words", wantErr: true},
		{name: "eight times the target is refused", summary: dx12Words(400), target: 50, unit: "words", wantErr: true},
		{name: "sentences inside the tolerance", summary: "One. Two. Three. Four.", target: 4, unit: "sentences", wantWithin: true},
		{name: "no target reports nothing", summary: dx12Words(400), target: 0, unit: "words", wantNil: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := NewSummarizeOptions()
			opts.TargetLength, opts.LengthUnit = tc.target, tc.unit

			result, err := dx12Summarize(t, tc.summary, opts)
			if tc.wantErr {
				if err == nil {
					t.Fatal("a summary far past the target was accepted")
				}
				if result.Value.Text != "" || result.Value.Metadata != nil {
					t.Errorf("a summary came back beside the error: %+v", result.Value)
				}
				return
			}
			if err != nil {
				t.Fatalf("SummarizeResult: %v", err)
			}

			metadata := result.Value.Metadata
			if tc.wantNil {
				// The compression figures are reported whenever MaxCompression
				// is set, which NewSummarizeOptions does; the length figures
				// are the ones that need a target.
				for key := range metadata {
					if strings.HasPrefix(key, "length_") {
						t.Fatalf("a length was reported with no target: %v", metadata)
					}
				}
				return
			}
			if metadata["length_within_target"] != tc.wantWithin {
				t.Errorf("length_within_target = %v, want %v", metadata["length_within_target"], tc.wantWithin)
			}
			if want := MeasureLength(tc.summary, LengthUnit(tc.unit)); metadata["length_measured"] != want {
				t.Errorf("length_measured = %v, want %d", metadata["length_measured"], want)
			}
		})
	}
}
