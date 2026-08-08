package ops

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// cover_analysis_test.go raises coverage of internal/ops/analysis.go's
// under-tested surface: Score, Compare, Similar and SimilarOptions.Validate.
// Score, Compare and Similar were previously untested by anything in this
// package -- Classify (the fourth function in the file) already has its own
// coverage in classify_labels_test.go and is not repeated here.

// installAnalysisResponse installs a fixed-body caller and restores whatever
// was there before, the same shape classify_labels_test.go's
// installClassifyResponse uses.
func installAnalysisResponse(t *testing.T, body string) {
	t.Helper()
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return body, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})
}

// --- Score ---

func TestScoreParsesStructuredResponse(t *testing.T) {
	installAnalysisResponse(t, `{
		"value": 8.5,
		"breakdown": {"clarity": 9, "grammar": 8},
		"reasoning": "well written",
		"strengths": ["clear"],
		"weaknesses": ["long"]
	}`)

	result, err := Score[string]("some essay text", NewScoreOptions().
		WithCriteria([]string{"clarity", "grammar"}))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if result.Value != 8.5 {
		t.Fatalf("Value = %v, want 8.5", result.Value)
	}
	wantNormalized := 8.5 / 10.0
	if result.NormalizedValue != wantNormalized {
		t.Fatalf("NormalizedValue = %v, want %v", result.NormalizedValue, wantNormalized)
	}
	if result.Breakdown["clarity"] != 9 {
		t.Fatalf("Breakdown[clarity] = %v, want 9", result.Breakdown["clarity"])
	}
	if len(result.Strengths) != 1 || result.Strengths[0] != "clear" {
		t.Fatalf("Strengths = %v", result.Strengths)
	}
}

// TestScoreClampsOutOfRangeValue proves a model that returns a value outside
// the caller's declared scale is clamped rather than passed straight through
// -- the scale is a contract the caller set, not a suggestion the model is
// free to ignore.
func TestScoreClampsOutOfRangeValue(t *testing.T) {
	installAnalysisResponse(t, `{"value": 999, "reasoning": "way too high"}`)

	result, err := Score[string]("text", NewScoreOptions().WithScaleMin(0).WithScaleMax(10))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if result.Value != 10 {
		t.Fatalf("Value = %v, want clamped to 10", result.Value)
	}
	if result.NormalizedValue != 1 {
		t.Fatalf("NormalizedValue = %v, want 1", result.NormalizedValue)
	}
}

// TestScoreFallsBackToBareNumber exercises the legacy fallback path: a
// response that is not the structured JSON object but a bare quoted number.
func TestScoreFallsBackToBareNumber(t *testing.T) {
	installAnalysisResponse(t, `"7.5"`)

	result, err := Score[string]("text", NewScoreOptions())
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if result.Value != 7.5 {
		t.Fatalf("Value = %v, want 7.5 from the bare-number fallback", result.Value)
	}
}

// TestScoreRefusesUnparseableResponse is the refusal case: neither the
// structured object nor a bare number, so Score must return an error rather
// than a zero-value result presented as an answer.
func TestScoreRefusesUnparseableResponse(t *testing.T) {
	installAnalysisResponse(t, "not json and not a number")

	_, err := Score[string]("text", NewScoreOptions())
	if err == nil {
		t.Fatal("Score accepted an unparseable response")
	}
}

// TestScoreRejectsInvalidOptions exercises the option-validation refusal path
// (ScaleMin >= ScaleMax) without a provider ever being consulted.
func TestScoreRejectsInvalidOptions(t *testing.T) {
	_, err := Score[string]("text", NewScoreOptions().WithScaleMin(10).WithScaleMax(1))
	if err == nil {
		t.Fatal("Score accepted ScaleMin >= ScaleMax")
	}
}

func TestScoreSurfacesLLMFailure(t *testing.T) {
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "", ErrNoProvider
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	_, err := Score[string]("text", NewScoreOptions())
	if err == nil {
		t.Fatal("Score did not surface the provider's failure")
	}
}

// --- Compare ---

type comparedThing struct {
	Name string
}

func TestCompareParsesStructuredResponse(t *testing.T) {
	installAnalysisResponse(t, `{
		"similarity_score": 0.6,
		"similarities": [{"aspect": "purpose", "description": "both are widgets"}],
		"differences": [{"aspect": "color", "description": "one is red", "severity": "minor"}],
		"verdict": "mostly alike",
		"aspect_scores": {"purpose": 0.9}
	}`)

	a := comparedThing{Name: "A"}
	b := comparedThing{Name: "B"}
	result, err := Compare[comparedThing](a, b, NewCompareOptions().
		WithComparisonAspects([]string{"purpose", "color"}))
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if result.SimilarityScore != 0.6 {
		t.Fatalf("SimilarityScore = %v, want 0.6", result.SimilarityScore)
	}
	if result.Verdict != "mostly alike" {
		t.Fatalf("Verdict = %q", result.Verdict)
	}
	if len(result.Similarities) != 1 || result.Similarities[0].Aspect != "purpose" {
		t.Fatalf("Similarities = %+v", result.Similarities)
	}
	if len(result.Differences) != 1 || result.Differences[0].Severity != "minor" {
		t.Fatalf("Differences = %+v", result.Differences)
	}
	if result.ItemA.Name != "A" || result.ItemB.Name != "B" {
		t.Fatalf("ItemA/ItemB = %+v / %+v, want the original inputs echoed back", result.ItemA, result.ItemB)
	}
}

func TestCompareRefusesUnparseableResponse(t *testing.T) {
	installAnalysisResponse(t, "prose, not JSON")

	_, err := Compare[comparedThing](comparedThing{Name: "A"}, comparedThing{Name: "B"}, NewCompareOptions())
	if err == nil {
		t.Fatal("Compare accepted an unparseable response")
	}
}

// TestCompareRejectsInvalidOptions exercises CompareOptions.Validate's
// refusal paths: an unrecognised OutputFormat, FocusOn and an out-of-range
// Depth.
func TestCompareRejectsInvalidOptions(t *testing.T) {
	cases := []struct {
		name string
		opts CompareOptions
	}{
		{"bad output format", NewCompareOptions().WithOutputFormat("json")},
		{"bad focus", NewCompareOptions().WithFocusOn("everything")},
		{"depth too low", func() CompareOptions { o := NewCompareOptions(); o.Depth = 0; return o }()},
		{"depth too high", func() CompareOptions { o := NewCompareOptions(); o.Depth = 11; return o }()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.opts.Validate(); err == nil {
				t.Fatalf("Validate() accepted %+v", tc.opts)
			}
		})
	}
}

func TestCompareSurfacesLLMFailure(t *testing.T) {
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "", ErrNoProvider
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	_, err := Compare[comparedThing](comparedThing{Name: "A"}, comparedThing{Name: "B"}, NewCompareOptions())
	if err == nil {
		t.Fatal("Compare did not surface the provider's failure")
	}
}

// --- Similar ---

func TestSimilarParsesStructuredResponse(t *testing.T) {
	installAnalysisResponse(t, `{
		"is_similar": true,
		"score": 0.92,
		"matched_aspects": [{"aspect": "meaning", "score": 0.95, "reason": "same idea"}],
		"differing_aspects": [{"aspect": "tone", "score": 0.4, "reason": "one is formal"}],
		"explanation": "largely the same idea"
	}`)

	result, err := Similar[string]("AI is great", "Artificial intelligence is wonderful",
		NewSimilarOptions().WithAspects([]string{"meaning", "tone"}))
	if err != nil {
		t.Fatalf("Similar: %v", err)
	}
	if !result.IsSimilar {
		t.Fatal("IsSimilar = false, want true")
	}
	if result.Score != 0.92 {
		t.Fatalf("Score = %v, want 0.92", result.Score)
	}
	if result.Threshold != 0.7 {
		t.Fatalf("Threshold = %v, want the default 0.7", result.Threshold)
	}
	if len(result.MatchedAspects) != 1 || result.MatchedAspects[0].Aspect != "meaning" {
		t.Fatalf("MatchedAspects = %+v", result.MatchedAspects)
	}
	if len(result.DifferingAspects) != 1 || result.DifferingAspects[0].Aspect != "tone" {
		t.Fatalf("DifferingAspects = %+v", result.DifferingAspects)
	}
}

// TestSimilarFallsBackToBareBoolean exercises the legacy fallback: a plain
// "true"/"false" body rather than the structured object, deriving a score
// that straddles the configured threshold in the expected direction.
func TestSimilarFallsBackToBareBoolean(t *testing.T) {
	t.Run("true", func(t *testing.T) {
		installAnalysisResponse(t, "TRUE")
		result, err := Similar[string]("a", "b", NewSimilarOptions().WithSimilarityThreshold(0.5))
		if err != nil {
			t.Fatalf("Similar: %v", err)
		}
		if !result.IsSimilar {
			t.Fatal("IsSimilar = false, want true from the bare-boolean fallback")
		}
		if result.Score <= 0.5 {
			t.Fatalf("Score = %v, want above the 0.5 threshold", result.Score)
		}
	})
	t.Run("false", func(t *testing.T) {
		installAnalysisResponse(t, "false")
		result, err := Similar[string]("a", "b", NewSimilarOptions().WithSimilarityThreshold(0.5))
		if err != nil {
			t.Fatalf("Similar: %v", err)
		}
		if result.IsSimilar {
			t.Fatal("IsSimilar = true, want false from the bare-boolean fallback")
		}
		if result.Score >= 0.5 {
			t.Fatalf("Score = %v, want below the 0.5 threshold", result.Score)
		}
	})
}

func TestSimilarRefusesUnparseableResponse(t *testing.T) {
	installAnalysisResponse(t, "neither JSON nor a boolean")

	_, err := Similar[string]("a", "b", NewSimilarOptions())
	if err == nil {
		t.Fatal("Similar accepted an unparseable, non-boolean response")
	}
}

// TestSimilarOptionsValidateRejectsOutOfRangeThreshold covers
// SimilarOptions.Validate directly: 0.0 percent covered before this test existed.
func TestSimilarOptionsValidateRejectsOutOfRangeThreshold(t *testing.T) {
	cases := []struct {
		name      string
		threshold float64
		wantErr   bool
	}{
		{"below zero", -0.1, true},
		{"above one", 1.1, true},
		{"zero is valid", 0, false},
		{"one is valid", 1, false},
		{"midrange is valid", 0.5, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := NewSimilarOptions().WithSimilarityThreshold(tc.threshold)
			err := opts.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("Validate() accepted threshold %v", tc.threshold)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Validate() rejected threshold %v: %v", tc.threshold, err)
			}
		})
	}
}

// TestSimilarRejectsInvalidOptions exercises Similar's own call to
// opts.Validate() -- the refusal happens before any provider call.
func TestSimilarRejectsInvalidOptions(t *testing.T) {
	_, err := Similar[string]("a", "b", NewSimilarOptions().WithSimilarityThreshold(5))
	if err == nil {
		t.Fatal("Similar accepted an out-of-range threshold")
	}
}

func TestSimilarSurfacesLLMFailure(t *testing.T) {
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "", ErrNoProvider
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	_, err := Similar[string]("a", "b", NewSimilarOptions())
	if err == nil {
		t.Fatal("Similar did not surface the provider's failure")
	}
}
