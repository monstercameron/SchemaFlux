package schemaflux_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

const (
	scoreAnswer = `{"value":8,"breakdown":{"clarity":7,"evidence":9},"reasoning":"direct","strengths":["focused"],"weaknesses":["short"]}`
	scoreBare   = `{"value":1.5,"breakdown":{},"reasoning":"","strengths":[],"weaknesses":[]}`
)

// scoreAnswerOf is an answer with the value given and nothing else.
func scoreAnswerOf(value string) string {
	return `{"value":` + value + `,"breakdown":{},"reasoning":"why","strengths":[],"weaknesses":[]}`
}

// scoreUnwritable is an input that cannot be written as JSON, and whose error
// says what it holds.
type scoreUnwritable struct{ secret string }

func (u scoreUnwritable) MarshalJSON() ([]byte, error) {
	return nil, errors.New("cannot write " + u.secret)
}

func TestScoreReturnsTheModelsAssessment(t *testing.T) {
	t.Parallel()
	res, err := schemaflux.Score(operationClient(answeringWith(scoreAnswer)), "an essay")
	if err != nil {
		t.Fatal(err)
	}
	want := schemaflux.ScoreResult{
		Value: 8, NormalizedValue: 0.8, Breakdown: map[string]float64{"clarity": 7, "evidence": 9},
		Reasoning: "direct", Strengths: []string{"focused"}, Weaknesses: []string{"short"},
	}
	if !reflect.DeepEqual(res, want) {
		t.Errorf("got %+v, want %+v", res, want)
	}
}

func TestScoreWorksOutTheNormalizedValueFromTheScaleItAskedFor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  schemaflux.ScoreOptions
		value string
		want  float64
	}{
		{"the default scale is 0 to 10", schemaflux.ScoreOptions{}, "8", 0.8},
		{"the bottom of the default scale", schemaflux.ScoreOptions{}, "0", 0},
		{"the top of the default scale", schemaflux.ScoreOptions{}, "10", 1},
		{"a scale of 1 to 5", schemaflux.ScoreOptions{ScaleMin: 1, ScaleMax: 5}, "4", 0.75},
		{"a maximum alone is a scale from 0", schemaflux.ScoreOptions{ScaleMax: 5}, "4", 0.8},
		{"a negative minimum", schemaflux.ScoreOptions{ScaleMin: -2, ScaleMax: 2}, "1", 0.75},
		{"a minimum below zero and a maximum of zero", schemaflux.ScoreOptions{ScaleMin: -10}, "-5", 0.5},
		{"a scale below zero", schemaflux.ScoreOptions{ScaleMin: -10, ScaleMax: -5}, "-7.5", 0.5},
		{"a scale of 0 to 100", schemaflux.ScoreOptions{ScaleMax: 100}, "50", 0.5},
		{"a scale of fractions", schemaflux.ScoreOptions{ScaleMin: 0.25, ScaleMax: 0.75}, "0.5", 0.5},
		{"Normalize changes nothing in the result", schemaflux.ScoreOptions{Normalize: true}, "8", 0.8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, err := schemaflux.Score(operationClient(answeringWith(scoreAnswerOf(tc.value))), "essay", tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			var wantValue float64
			if err := json.Unmarshal([]byte(tc.value), &wantValue); err != nil {
				t.Fatal(err)
			}
			if res.Value != wantValue || math.Abs(res.NormalizedValue-tc.want) > 1e-12 {
				t.Errorf("value %v normalized %v, want %v and %v", res.Value, res.NormalizedValue, wantValue, tc.want)
			}
		})
	}
}

func TestScoreReturnsAnEmptyBreakdownAndEmptyListsNeverNilForAnswerThatGivesNone(t *testing.T) {
	t.Parallel()
	res, err := schemaflux.Score(operationClient(answeringWith(scoreBare)), "essay")
	if err != nil {
		t.Fatal(err)
	}
	if res.Breakdown == nil || len(res.Breakdown) != 0 {
		t.Errorf("breakdown = %#v, want an empty map that is not nil", res.Breakdown)
	}
	if res.Reasoning != "" || len(res.Strengths) != 0 || len(res.Weaknesses) != 0 {
		t.Errorf("result = %+v", res)
	}
}

func TestScoreClampsAValueThatOnlyJustPassesTheEndOfTheScaleToTheEnd(t *testing.T) {
	t.Parallel()
	res, err := schemaflux.Score(operationClient(answeringWith(scoreAnswerOf("10.0000000001"))), "essay")
	if err != nil || res.Value != 10 || res.NormalizedValue != 1 {
		t.Fatalf("got %+v, %v: float noise at the end of the scale is not a refusal and never makes a result above 1", res, err)
	}
}

func TestScoreWithNoOptionsAsksAtTheFastTierForTheDefaultScaleAndABreakdown(t *testing.T) {
	t.Parallel()
	model := answeringWith(scoreBare)
	if _, err := schemaflux.Score(operationClient(model), "an essay"); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	if math.Abs(req.Temperature-0.3) > 1e-6 || req.MaxTokens != 2000 || req.Model != "test-model" || req.WebSearch {
		t.Errorf("temperature %v ceiling %d model %q web %v", req.Temperature, req.MaxTokens, req.Model, req.WebSearch)
	}
	if req.Schema != nil {
		t.Error("a schema was declared: Score declares none")
	}
	wantText(t, req, "Score Range: 0.0 to 10.0")
	wantText(t, req, "Score this input:\n\"an essay\"")
	wantText(t, req, "Report a per-criterion breakdown")
	wantNoText(t, req, "Normalise the score")
}

func TestScoreEachOptionFieldChangesTheRequestAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  schemaflux.ScoreOptions
		reply string
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"Criteria are listed in the order given", schemaflux.ScoreOptions{Criteria: []string{"evidence", "clarity"}}, scoreBare, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `Criteria: ["evidence","clarity"]`)
			wantText(t, req, "Criteria: evidence, clarity")
		}},
		{"Rubric describes a criterion", schemaflux.ScoreOptions{Criteria: []string{"clarity"}, Rubric: map[string]string{"clarity": "easy to read"}}, scoreBare, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "clarity: easy to read")
		}},
		{"Rubric keys are the criteria when there are none, in alphabetical order", schemaflux.ScoreOptions{Rubric: map[string]string{"tone": "polite", "clarity": "easy to read"}}, scoreBare, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `Criteria: ["clarity","tone"]`)
		}},
		{"Weights are told to the model", schemaflux.ScoreOptions{Criteria: []string{"clarity", "tone"}, Weights: map[string]float64{"clarity": 2, "tone": 0.5}}, scoreBare, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `Criterion weights (relative importance): {"clarity":2,"tone":0.5}`)
		}},
		{"Weights on the keys of a Rubric", schemaflux.ScoreOptions{Rubric: map[string]string{"clarity": "easy"}, Weights: map[string]float64{"clarity": 3}}, scoreBare, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `{"clarity":3}`)
		}},
		{"no Weights say nothing of weights", schemaflux.ScoreOptions{Criteria: []string{"clarity"}}, scoreBare, func(t *testing.T, req schemaflux.Request) {
			wantNoText(t, req, "Criterion weights")
		}},
		{"a scale", schemaflux.ScoreOptions{ScaleMin: -2, ScaleMax: 2}, scoreAnswerOf("1"), func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Score Range: -2.0 to 2.0")
			wantText(t, req, "Score range: -2.0 to 2.0")
		}},
		{"a maximum alone", schemaflux.ScoreOptions{ScaleMax: 5}, scoreAnswerOf("1"), func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Score Range: 0.0 to 5.0")
		}},
		{"OmitBreakdown", schemaflux.ScoreOptions{OmitBreakdown: true}, scoreBare, func(t *testing.T, req schemaflux.Request) {
			wantNoText(t, req, "Report a per-criterion breakdown")
			wantText(t, req, "Provide a breakdown score for each criterion")
		}},
		{"Normalize", schemaflux.ScoreOptions{Normalize: true}, scoreBare, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Normalise the score to the range 0.0 to 10.0")
		}},
		{"Steering", schemaflux.ScoreOptions{Options: schemaflux.Options{Steering: "mark harshly"}}, scoreBare, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "mark harshly")
		}},
		{"Model", schemaflux.ScoreOptions{Options: schemaflux.Options{Model: "pinned-model"}}, scoreBare, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"ModeStrict", schemaflux.ScoreOptions{Options: schemaflux.Options{Mode: schemaflux.ModeStrict}}, scoreBare, func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.1) > 1e-6 {
				t.Errorf("temperature = %v", req.Temperature)
			}
		}},
		{"TierSmart", schemaflux.ScoreOptions{Options: schemaflux.Options{Tier: schemaflux.TierSmart}}, scoreBare, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 4000 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"MaxOutputTokens", schemaflux.ScoreOptions{Options: schemaflux.Options{MaxOutputTokens: 55}}, scoreBare, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 55 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.ScoreOptions{Options: schemaflux.Options{WebSearch: true}}, scoreBare, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("no web search")
			}
		}},
		{"Effort", schemaflux.ScoreOptions{Options: schemaflux.Options{Effort: schemaflux.EffortHigh}}, scoreBare, func(t *testing.T, req schemaflux.Request) {
			if req.Effort != "high" || req.EffortPreferred {
				t.Errorf("effort = %q preferred %v, want high as the caller's requirement", req.Effort, req.EffortPreferred)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(tc.reply)
			if _, err := schemaflux.Score(operationClient(model), "essay", tc.opts); err != nil {
				t.Fatal(err)
			}
			tc.check(t, model.Last())
		})
	}
}

func TestScoreWritesAnyKindOfValueAsTheModelSeesIt(t *testing.T) {
	t.Parallel()
	type essay struct {
		Title string `json:"title"`
		Words int    `json:"words"`
	}
	cases := []struct {
		name string
		call func(ctx context.Context) (schemaflux.ScoreResult, error)
		want string
	}{
		{"a string", func(ctx context.Context) (schemaflux.ScoreResult, error) { return schemaflux.Score(ctx, "plain") }, `"plain"`},
		{"a struct", func(ctx context.Context) (schemaflux.ScoreResult, error) {
			return schemaflux.Score(ctx, essay{"On Tea", 900})
		}, `{"title":"On Tea","words":900}`},
		{"a pointer", func(ctx context.Context) (schemaflux.ScoreResult, error) {
			return schemaflux.Score(ctx, &essay{"On Tea", 900})
		}, `{"title":"On Tea","words":900}`},
		{"a map", func(ctx context.Context) (schemaflux.ScoreResult, error) {
			return schemaflux.Score(ctx, map[string]int{"a": 1})
		}, `{"a":1}`},
		{"a slice", func(ctx context.Context) (schemaflux.ScoreResult, error) {
			return schemaflux.Score(ctx, []string{"x", "y"})
		}, `["x","y"]`},
		{"a number", func(ctx context.Context) (schemaflux.ScoreResult, error) { return schemaflux.Score(ctx, 12.5) }, `12.5`},
		{"raw JSON", func(ctx context.Context) (schemaflux.ScoreResult, error) {
			return schemaflux.Score(ctx, json.RawMessage(`{"k":"v"}`))
		}, `{"k":"v"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(scoreBare)
			if _, err := tc.call(operationClient(model)); err != nil {
				t.Fatal(err)
			}
			wantText(t, model.Last(), "Score this input:\n"+tc.want)
		})
	}
}

func TestScoreMergesSeveralOptionsValuesFromLeftToRight(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  []schemaflux.ScoreOptions
		reply string
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"no value is the default", nil, scoreBare, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Score Range: 0.0 to 10.0")
		}},
		{"one value is used as it is", []schemaflux.ScoreOptions{{ScaleMin: 1, ScaleMax: 5, Criteria: []string{"clarity"}}}, scoreAnswerOf("3"), func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Score Range: 1.0 to 5.0")
			wantText(t, req, "Criteria: clarity")
		}},
		{"each value adds its fields", []schemaflux.ScoreOptions{{Criteria: []string{"clarity"}}, {ScaleMax: 5}, {Normalize: true}}, scoreAnswerOf("3"), func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Criteria: clarity")
			wantText(t, req, "Score Range: 0.0 to 5.0")
			wantText(t, req, "Normalise the score to the range 0.0 to 5.0")
		}},
		{"the later criteria replace the earlier", []schemaflux.ScoreOptions{{Criteria: []string{"clarity"}}, {Criteria: []string{"tone"}}}, scoreBare, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Criteria: tone")
			wantNoText(t, req, "Criteria: clarity")
		}},
		{"later nil criteria leave the earlier", []schemaflux.ScoreOptions{{Criteria: []string{"clarity"}}, {Normalize: true}}, scoreBare, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Criteria: clarity")
		}},
		{"the ends of the scale merge one by one", []schemaflux.ScoreOptions{{ScaleMin: -5, ScaleMax: 5}, {ScaleMax: 9}}, scoreAnswerOf("1"), func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Score Range: -5.0 to 9.0")
		}},
		{"a later false does not turn a bool off", []schemaflux.ScoreOptions{{OmitBreakdown: true, Normalize: true}, {}}, scoreBare, func(t *testing.T, req schemaflux.Request) {
			wantNoText(t, req, "Report a per-criterion breakdown")
			wantText(t, req, "Normalise the score")
		}},
		{"rubric and weights merge", []schemaflux.ScoreOptions{{Rubric: map[string]string{"clarity": "easy"}}, {Weights: map[string]float64{"clarity": 2}}}, scoreBare, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "clarity: easy")
			wantText(t, req, `{"clarity":2}`)
		}},
		{"the common settings merge too", []schemaflux.ScoreOptions{
			{Options: schemaflux.Options{Steering: "be strict", WebSearch: true}},
			{Options: schemaflux.Options{Model: "later-model"}},
			{Options: schemaflux.Options{MaxOutputTokens: 77}},
		}, scoreBare, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "be strict")
			if req.Model != "later-model" || req.MaxTokens != 77 || !req.WebSearch {
				t.Errorf("model %q ceiling %d web %v", req.Model, req.MaxTokens, req.WebSearch)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(tc.reply)
			if _, err := schemaflux.Score(operationClient(model), "essay", tc.opts...); err != nil {
				t.Fatal(err)
			}
			tc.check(t, model.Last())
		})
	}
}

func TestScoreRefusesASettingNoCallCanUseBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	const secret = "TONE-secret-criterion"
	cases := []struct {
		name string
		opts []schemaflux.ScoreOptions
	}{
		{"a minimum above the maximum", []schemaflux.ScoreOptions{{ScaleMin: 5, ScaleMax: 2}}},
		{"a minimum equal to the maximum", []schemaflux.ScoreOptions{{ScaleMin: 3, ScaleMax: 3}}},
		{"a minimum alone", []schemaflux.ScoreOptions{{ScaleMin: 1}}},
		{"a negative maximum alone", []schemaflux.ScoreOptions{{ScaleMax: -2}}},
		{"a minimum that is not a number", []schemaflux.ScoreOptions{{ScaleMin: math.NaN()}}},
		{"a maximum that is not a number", []schemaflux.ScoreOptions{{ScaleMax: math.NaN()}}},
		{"an infinite maximum", []schemaflux.ScoreOptions{{ScaleMax: math.Inf(1)}}},
		{"an infinite minimum", []schemaflux.ScoreOptions{{ScaleMin: math.Inf(-1), ScaleMax: 5}}},
		{"a scale whose width overflows", []schemaflux.ScoreOptions{{ScaleMin: -math.MaxFloat64, ScaleMax: math.MaxFloat64}}},
		{"a negative weight", []schemaflux.ScoreOptions{{Criteria: []string{secret}, Weights: map[string]float64{secret: -1}}}},
		{"the smallest negative weight", []schemaflux.ScoreOptions{{Criteria: []string{secret}, Weights: map[string]float64{secret: -math.SmallestNonzeroFloat64}}}},
		{"a weight that is not a number", []schemaflux.ScoreOptions{{Criteria: []string{secret}, Weights: map[string]float64{secret: math.NaN()}}}},
		{"an infinite weight", []schemaflux.ScoreOptions{{Criteria: []string{secret}, Weights: map[string]float64{secret: math.Inf(1)}}}},
		{"a negative infinite weight", []schemaflux.ScoreOptions{{Criteria: []string{secret}, Weights: map[string]float64{secret: math.Inf(-1)}}}},
		{"a weight for a criterion that is not scored", []schemaflux.ScoreOptions{{Criteria: []string{"clarity"}, Weights: map[string]float64{secret: 1}}}},
		{"a weight when there are no criteria at all", []schemaflux.ScoreOptions{{Weights: map[string]float64{secret: 1}}}},
		{"a weight for a rubric key when criteria are given", []schemaflux.ScoreOptions{{Criteria: []string{"clarity"}, Rubric: map[string]string{secret: "x"}, Weights: map[string]float64{secret: 1}}}},
		{"a weight for another case of a criterion", []schemaflux.ScoreOptions{{Criteria: []string{secret}, Weights: map[string]float64{strings.ToLower(secret): 1}}}},
		{"a negative output ceiling", []schemaflux.ScoreOptions{{Options: schemaflux.Options{MaxOutputTokens: -1}}}},
		{"a tier that does not exist", []schemaflux.ScoreOptions{{Options: schemaflux.Options{Tier: 9}}}},
		{"a mode that does not exist", []schemaflux.ScoreOptions{{Options: schemaflux.Options{Mode: 9}}}},
		{"a bad scale in the later of two values", []schemaflux.ScoreOptions{{ScaleMax: 5}, {ScaleMin: 6}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(scoreBare)
			res, err := schemaflux.Score(operationClient(model), "essay", tc.opts...)
			e := wantError(t, err, schemaflux.ErrConfiguration, "score")
			if model.Calls() != 0 {
				t.Errorf("%d requests were sent", model.Calls())
			}
			if !reflect.DeepEqual(res, schemaflux.ScoreResult{}) {
				t.Errorf("a refused call left %+v beside the error", res)
			}
			if strings.Contains(strings.ToLower(e.Error()), strings.ToLower(secret)) {
				t.Errorf("the error repeats a criterion the caller wrote: %v", e)
			}
			if strings.Contains(e.Error(), "invalid options") {
				t.Errorf("the refusal is the internal call's and not the function's own: %v", e)
			}
		})
	}
}

func TestScoreNamesTheFieldOfABadScale(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		opts schemaflux.ScoreOptions
		want string
	}{
		"minimum not a number": {schemaflux.ScoreOptions{ScaleMin: math.NaN()}, "ScaleMin"},
		"maximum infinite":     {schemaflux.ScoreOptions{ScaleMax: math.Inf(1)}, "ScaleMax"},
		"minimum above":        {schemaflux.ScoreOptions{ScaleMin: 4, ScaleMax: 2}, "ScaleMin"},
		"ends equal":           {schemaflux.ScoreOptions{ScaleMin: 3, ScaleMax: 3}, "ScaleMin"},
		"minimum alone":        {schemaflux.ScoreOptions{ScaleMin: 1}, "ScaleMin"},
		"negative maximum":     {schemaflux.ScoreOptions{ScaleMax: -2}, "ScaleMin"},
		"overflowing width":    {schemaflux.ScoreOptions{ScaleMin: -math.MaxFloat64, ScaleMax: math.MaxFloat64}, "ScaleMin"},
		"weights":              {schemaflux.ScoreOptions{Criteria: []string{"a"}, Weights: map[string]float64{"a": -1}}, "Weights"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := schemaflux.Score(operationClient(answeringWith(scoreBare)), "x", tc.opts)
			if e := wantError(t, err, schemaflux.ErrConfiguration, "score"); !strings.Contains(e.Error(), tc.want) {
				t.Errorf("the error does not name %s: %v", tc.want, e)
			}
		})
	}
}

func TestScoreAcceptsTheEdgesOfWhatItRefuses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  schemaflux.ScoreOptions
		reply string
	}{
		{"a weight of zero", schemaflux.ScoreOptions{Criteria: []string{"a"}, Weights: map[string]float64{"a": 0}}, scoreBare},
		{"a negative zero weight", schemaflux.ScoreOptions{Criteria: []string{"a"}, Weights: map[string]float64{"a": math.Copysign(0, -1)}}, scoreBare},
		{"the largest weight", schemaflux.ScoreOptions{Criteria: []string{"a"}, Weights: map[string]float64{"a": math.MaxFloat64}}, scoreBare},
		{"a weight for each of several criteria", schemaflux.ScoreOptions{Criteria: []string{"a", "b", "c"}, Weights: map[string]float64{"a": 1, "c": 2}}, scoreBare},
		{"a weight for a rubric key when there are no criteria", schemaflux.ScoreOptions{Rubric: map[string]string{"a": "x"}, Weights: map[string]float64{"a": 1}}, scoreBare},
		{"a scale only just wide enough to be finite", schemaflux.ScoreOptions{ScaleMin: -math.MaxFloat64 / 2, ScaleMax: math.MaxFloat64 / 2}, scoreAnswerOf("0")},
		{"the smallest scale", schemaflux.ScoreOptions{ScaleMin: 0, ScaleMax: math.SmallestNonzeroFloat64}, scoreAnswerOf("0")},
		{"a scale that only has a minimum below zero", schemaflux.ScoreOptions{ScaleMin: -1}, scoreAnswerOf("-0.5")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(tc.reply)
			res, err := schemaflux.Score(operationClient(model), "essay", tc.opts)
			if err != nil || model.Calls() != 1 {
				t.Fatalf("got %+v, %v after %d requests", res, err, model.Calls())
			}
			if math.IsNaN(res.NormalizedValue) || res.NormalizedValue < 0 || res.NormalizedValue > 1 {
				t.Errorf("normalized value %v is not from 0 to 1", res.NormalizedValue)
			}
		})
	}
}

func TestScoreRefusesAnAnswerThatIsNotWhatWasAskedFor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  schemaflux.ScoreOptions
		reply string
	}{
		{"text", schemaflux.ScoreOptions{}, "eight out of ten"},
		{"no answer at all", schemaflux.ScoreOptions{}, ""},
		{"null", schemaflux.ScoreOptions{}, "null"},
		{"a bare number", schemaflux.ScoreOptions{}, "8"},
		{"an empty object", schemaflux.ScoreOptions{}, "{}"},
		{"no value", schemaflux.ScoreOptions{}, `{"breakdown":{},"reasoning":"x","strengths":[],"weaknesses":[]}`},
		{"a null value", schemaflux.ScoreOptions{}, `{"value":null,"breakdown":{},"reasoning":"x","strengths":[],"weaknesses":[]}`},
		{"a value that is text", schemaflux.ScoreOptions{}, `{"value":"high","breakdown":{},"reasoning":"x","strengths":[],"weaknesses":[]}`},
		{"a value above the default scale", schemaflux.ScoreOptions{}, scoreAnswerOf("10.5")},
		{"a value far above the default scale", schemaflux.ScoreOptions{}, scoreAnswerOf("87")},
		{"a value below the default scale", schemaflux.ScoreOptions{}, scoreAnswerOf("-0.5")},
		{"a value above a scale of 1 to 5", schemaflux.ScoreOptions{ScaleMin: 1, ScaleMax: 5}, scoreAnswerOf("6")},
		{"a value below a scale of 1 to 5", schemaflux.ScoreOptions{ScaleMin: 1, ScaleMax: 5}, scoreAnswerOf("0")},
		{"a value outside by more than noise", schemaflux.ScoreOptions{}, scoreAnswerOf("10.000001")},
		{"a criterion's score above the scale", schemaflux.ScoreOptions{}, `{"value":5,"breakdown":{"clarity":11},"reasoning":"x","strengths":[],"weaknesses":[]}`},
		{"a criterion's score below the scale", schemaflux.ScoreOptions{}, `{"value":5,"breakdown":{"clarity":-1},"reasoning":"x","strengths":[],"weaknesses":[]}`},
		{"one criterion's score off the scale among good ones", schemaflux.ScoreOptions{}, `{"value":5,"breakdown":{"a":5,"b":6,"c":99},"reasoning":"x","strengths":[],"weaknesses":[]}`},
		{"a breakdown that is a list", schemaflux.ScoreOptions{}, `{"value":5,"breakdown":[5],"reasoning":"x","strengths":[],"weaknesses":[]}`},
		{"strengths that are not a list", schemaflux.ScoreOptions{}, `{"value":5,"breakdown":{},"reasoning":"x","strengths":"good","weaknesses":[]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(tc.reply)
			res, err := schemaflux.Score(operationClient(model), "essay", tc.opts)
			wantError(t, err, schemaflux.ErrInvalidAnswer, "score")
			if model.Calls() == 0 {
				t.Error("nothing was sent")
			}
			if !reflect.DeepEqual(res, schemaflux.ScoreResult{}) {
				t.Errorf("a refused answer left %+v beside the error", res)
			}
		})
	}
}

func TestScoreReturnsTheErrorOfAProviderThatFailsAndNothingElse(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  *schemaflux.Error
	}{
		{"unavailable", &schemaflux.Error{Kind: schemaflux.ErrUnavailable}},
		{"rate limited", &schemaflux.Error{Kind: schemaflux.ErrRateLimited, Status: 429}},
		{"unauthorized", &schemaflux.Error{Kind: schemaflux.ErrUnauthorized, Status: 401}},
		{"timed out", &schemaflux.Error{Kind: schemaflux.ErrTimeout}},
		{"refused", &schemaflux.Error{Kind: schemaflux.ErrRefused}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var model schemafluxtest.Provider
			model.Fail(tc.err)
			res, err := schemaflux.Score(operationClient(&model), "essay")
			wantError(t, err, tc.err.Kind, "score")
			if !reflect.DeepEqual(res, schemaflux.ScoreResult{}) {
				t.Errorf("a failed call left %+v beside the error", res)
			}
		})
	}
}

func TestScoreRefusesAnInputThatCannotBeWrittenAsJSONBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	model := answeringWith(scoreBare)
	res, err := schemaflux.Score(operationClient(model), make(chan int))
	wantError(t, err, schemaflux.ErrInvalidRequest, "score")
	if model.Calls() != 0 || !reflect.DeepEqual(res, schemaflux.ScoreResult{}) {
		t.Errorf("%d requests were sent, result %+v", model.Calls(), res)
	}
}

func TestScoreWithANilContextIsAnInvalidRequest(t *testing.T) {
	t.Parallel()
	var none context.Context
	res, err := schemaflux.Score(none, "x")
	e := wantError(t, err, schemaflux.ErrInvalidRequest, "score")
	if !strings.Contains(e.Error(), "context") || !reflect.DeepEqual(res, schemaflux.ScoreResult{}) {
		t.Errorf("got %+v and %v", res, e)
	}
	_, err = schemaflux.Score(none, "x", schemaflux.ScoreOptions{ScaleMin: 9, ScaleMax: 1})
	if e := wantError(t, err, schemaflux.ErrInvalidRequest, "score"); !strings.Contains(e.Error(), "context") {
		t.Errorf("a nil context and a bad scale: %v, want the context named first", e)
	}
}

func TestScoreResultMarshalsWithTheTagsOfTheInternalResult(t *testing.T) {
	t.Parallel()
	res := schemaflux.ScoreResult{
		Value: 8, NormalizedValue: 0.8, Breakdown: map[string]float64{"clarity": 7},
		Reasoning: "why", Strengths: []string{"focused"}, Weaknesses: []string{"short"},
	}
	got, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"value":8,"normalized_value":0.8,"breakdown":{"clarity":7},"reasoning":"why","strengths":["focused"],"weaknesses":["short"]}`
	if string(got) != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestScoreAddsOneRecordForEachCallWithTheOperationsName(t *testing.T) {
	t.Parallel()
	t.Run("a call that succeeds", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		if _, err := schemaflux.Score(operationClient(answeringWith(scoreBare), recordingTo(&recorder)), "x"); err != nil {
			t.Fatal(err)
		}
		r := onlyRecord(t, &recorder)
		if r.Op != "score" || r.Err != nil || r.Requests != 1 || r.Provider != "test" {
			t.Errorf("record = %q err %v requests %d provider %q", r.Op, r.Err, r.Requests, r.Provider)
		}
		if r.Requested != schemaflux.ContractJSONWellFormed || r.Delivered != schemaflux.ContractJSONWellFormed {
			t.Errorf("levels = %v and %v, want json well-formed", r.Requested, r.Delivered)
		}
	})
	t.Run("a call that fails on its answer", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		_, err := schemaflux.Score(operationClient(answeringWith("not json"), recordingTo(&recorder)), "x")
		wantError(t, err, schemaflux.ErrInvalidAnswer, "score")
		r := onlyRecord(t, &recorder)
		if r.Op != "score" || r.Err != err || r.Requests < 1 || r.Requested != schemaflux.ContractJSONWellFormed || r.Delivered >= schemaflux.ContractJSONWellFormed {
			t.Errorf("record = %q err %v requests %d requested %v delivered %v", r.Op, r.Err, r.Requests, r.Requested, r.Delivered)
		}
	})
	t.Run("a call that fails on a score off the scale", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		_, err := schemaflux.Score(operationClient(answeringWith(scoreAnswerOf("99")), recordingTo(&recorder)), "x")
		wantError(t, err, schemaflux.ErrInvalidAnswer, "score")
		if r := onlyRecord(t, &recorder); r.Op != "score" || r.Err != err {
			t.Errorf("record = %q err %v", r.Op, r.Err)
		}
	})
	t.Run("a call the provider fails", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		var model schemafluxtest.Provider
		model.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable})
		_, err := schemaflux.Score(operationClient(&model, recordingTo(&recorder)), "x")
		r := onlyRecord(t, &recorder)
		wantError(t, r.Err, schemaflux.ErrUnavailable, "score")
		if r.Op != "score" || r.Err != err {
			t.Errorf("record = %q err %v", r.Op, r.Err)
		}
	})
	t.Run("a call refused before anything is sent: a bad scale", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		model := answeringWith(scoreBare)
		_, err := schemaflux.Score(operationClient(model, recordingTo(&recorder)), "x", schemaflux.ScoreOptions{ScaleMin: 3, ScaleMax: 2})
		r := onlyRecord(t, &recorder)
		wantError(t, r.Err, schemaflux.ErrConfiguration, "score")
		if r.Op != "score" || r.Err != err || r.Requests != 0 || model.Calls() != 0 {
			t.Errorf("record = %q err %v requests %d", r.Op, r.Err, r.Requests)
		}
	})
	t.Run("a call refused before anything is sent: a bad weight", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		model := answeringWith(scoreBare)
		_, err := schemaflux.Score(operationClient(model, recordingTo(&recorder)), "x", schemaflux.ScoreOptions{Weights: map[string]float64{"a": 1}})
		r := onlyRecord(t, &recorder)
		wantError(t, r.Err, schemaflux.ErrConfiguration, "score")
		if r.Op != "score" || r.Err != err || r.Requests != 0 || model.Calls() != 0 {
			t.Errorf("record = %q err %v requests %d", r.Op, r.Err, r.Requests)
		}
	})
	t.Run("a call refused before anything is sent: an input that cannot be written", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		model := answeringWith(scoreBare)
		_, err := schemaflux.Score(operationClient(model, recordingTo(&recorder)), make(chan int))
		r := onlyRecord(t, &recorder)
		wantError(t, r.Err, schemaflux.ErrInvalidRequest, "score")
		if r.Op != "score" || r.Err != err || r.Requests != 0 || model.Calls() != 0 {
			t.Errorf("record = %q err %v requests %d", r.Op, r.Err, r.Requests)
		}
	})
	t.Run("a recorder on the context and one on the client each get the Record", func(t *testing.T) {
		t.Parallel()
		var onClient, onContext schemaflux.Recorder
		ctx := schemaflux.WithRecorder(operationClient(answeringWith(scoreBare), recordingTo(&onClient)), &onContext)
		if _, err := schemaflux.Score(ctx, "x"); err != nil {
			t.Fatal(err)
		}
		if onlyRecord(t, &onClient).Op != "score" || onlyRecord(t, &onContext).Op != "score" {
			t.Error("a recorder was not given the Record")
		}
	})
}

func TestScoreUnderAFloorIsHeldToTheLevelItEstablishes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		floor   schemaflux.ContractLevel
		refused bool
	}{
		{"a floor of prompt only is met", schemaflux.ContractPromptOnly, false},
		{"a floor of well-formed JSON is met", schemaflux.ContractJSONWellFormed, false},
		{"a floor of schema constrained is not: no schema is declared", schemaflux.ContractSchemaConstrained, true},
		{"a floor of schema and invariants is not", schemaflux.ContractSchemaAndInvariantChecked, true},
	}
	for _, tc := range cases {
		for _, withRecorder := range []bool{false, true} {
			name := tc.name
			if withRecorder {
				name += ", with a recorder"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				var recorder schemaflux.Recorder
				model := namedModel(scoreAnswer)
				settings := []func(*schemaflux.Client){underFloor(tc.floor)}
				if withRecorder {
					settings = append(settings, recordingTo(&recorder))
				}
				res, err := schemaflux.Score(operationClient(model, settings...), "essay")
				if tc.refused {
					wantError(t, err, schemaflux.ErrPolicy, "score")
					if model.Calls() != 0 || !reflect.DeepEqual(res, schemaflux.ScoreResult{}) {
						t.Errorf("%d requests were sent, result %+v", model.Calls(), res)
					}
				} else if err != nil || res.Value != 8 || model.Calls() != 1 {
					t.Fatalf("got %+v, %v after %d requests", res, err, model.Calls())
				}
				if withRecorder {
					r := onlyRecord(t, &recorder)
					if r.Op != "score" || (r.Err != nil) != tc.refused || r.Requested != schemaflux.ContractJSONWellFormed {
						t.Errorf("record = %q err %v requested %v", r.Op, r.Err, r.Requested)
					}
					if tc.refused && r.Requests != 0 {
						t.Errorf("a refused call recorded %d requests", r.Requests)
					}
				}
			})
		}
	}
}

func TestScoreGivesTheSameResultAndSendsTheSameRequestInEveryStateOfTheCall(t *testing.T) {
	// Not parallel: it compares what each state sent with what the plain call sent.
	opts := schemaflux.ScoreOptions{Criteria: []string{"clarity", "evidence"}, ScaleMax: 10, Normalize: true, Options: schemaflux.Options{Steering: "be strict"}}
	want := schemaflux.ScoreResult{
		Value: 8, NormalizedValue: 0.8, Breakdown: map[string]float64{"clarity": 7, "evidence": 9},
		Reasoning: "direct", Strengths: []string{"focused"}, Weaknesses: []string{"short"},
	}
	var recorder schemaflux.Recorder
	states := []struct {
		name  string
		model *schemafluxtest.Provider
		set   []func(*schemaflux.Client)
	}{
		{"plain", answeringWith(scoreAnswer), nil},
		{"a recorder", answeringWith(scoreAnswer), []func(*schemaflux.Client){recordingTo(&recorder)}},
		{"a floor it meets", namedModel(scoreAnswer), []func(*schemaflux.Client){underFloor(schemaflux.ContractJSONWellFormed)}},
	}
	var plainRequest schemaflux.Request
	for i, state := range states {
		res, err := schemaflux.Score(operationClient(state.model, state.set...), "essay", opts)
		if err != nil || !reflect.DeepEqual(res, want) {
			t.Errorf("%s: got %+v, %v, want %+v", state.name, res, err, want)
		}
		if i == 0 {
			plainRequest = state.model.Last()
		} else if !reflect.DeepEqual(state.model.Last(), plainRequest) {
			t.Errorf("%s: the request differs from the plain call's", state.name)
		}
	}
}

func TestScoreUnderAnObserverSendsTheSameRequestAndRecordsOnce(t *testing.T) {
	// Not parallel: an observer is process state.
	var seen []string
	restore := telemetry.RegisterObserver(operationWatcher{finished: func(r telemetry.Result) {
		if r.Scope == telemetry.ScopeOperation {
			seen = append(seen, r.Operation)
		}
	}})
	defer restore()

	opts := schemaflux.ScoreOptions{Criteria: []string{"clarity", "evidence"}}
	plain := answeringWith(scoreAnswer)
	if _, err := schemaflux.Score(operationClient(plain), "essay", opts); err != nil {
		t.Fatal(err)
	}
	var recorder schemaflux.Recorder
	watched := answeringWith(scoreAnswer)
	res, err := schemaflux.Score(operationClient(watched, recordingTo(&recorder)), "essay", opts)
	if err != nil || res.Value != 8 {
		t.Fatalf("got %+v, %v", res, err)
	}
	if !reflect.DeepEqual(plain.Last(), watched.Last()) {
		t.Error("an observer changed what the model is asked")
	}
	if onlyRecord(t, &recorder).Op != "score" {
		t.Error("the Record under an observer has the wrong operation")
	}
	if len(seen) != 2 {
		t.Errorf("the observer saw %v, want one operation scope for each call", seen)
	}
}

func TestScoreKeepsThePayloadOutOfEverythingItLeavesBehind(t *testing.T) {
	t.Parallel()
	const canary = "CANARY-6a41f9"
	opts := schemaflux.ScoreOptions{
		Options:  schemaflux.Options{Steering: "steer " + canary},
		Criteria: []string{canary + "-criterion", "other"},
		Rubric:   map[string]string{canary + "-criterion": "means " + canary},
		Weights:  map[string]float64{canary + "-criterion": 2},
	}
	input := map[string]string{canary + "-key": "text " + canary}
	run := func(ctx context.Context) error {
		_, err := schemaflux.Score(ctx, input, opts)
		return err
	}
	good := `{"value":8,"breakdown":{"` + canary + `-criterion":7},"reasoning":"` + canary + `","strengths":["` + canary + `"],"weaknesses":["` + canary + `"]}`

	t.Run("a call that succeeds", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(good) }, true, run)
	})
	t.Run("an answer that is text", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("the score is eight: " + canary) }, true, run)
	})
	t.Run("an answer with a value off the scale", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) {
			p.Reply(`{"value":99,"breakdown":{},"reasoning":"` + canary + `","strengths":[],"weaknesses":[]}`)
		}, true, run)
	})
	t.Run("an answer with a criterion's score off the scale", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) {
			p.Reply(`{"value":5,"breakdown":{"` + canary + `":99},"reasoning":"` + canary + `","strengths":[],"weaknesses":[]}`)
		}, true, run)
	})
	t.Run("an answer with a value that is the canary", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) {
			p.Reply(`{"value":"` + canary + `","breakdown":{},"reasoning":"` + canary + `","strengths":[],"weaknesses":[]}`)
		}, true, run)
	})
	t.Run("a provider that fails with a vendor message beneath its error", func(t *testing.T) {
		t.Parallel()
		vendor := &schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "the provider is down", Err: errors.New("vendor said " + canary)}
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Fail(vendor) }, false, run)
	})
	t.Run("a provider that fails with an error of its own", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Fail(errors.New("vendor said " + canary)) }, false, run)
	})
	t.Run("a refused scale", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(good) }, true, func(ctx context.Context) error {
			bad := opts
			bad.ScaleMin, bad.ScaleMax = 9, 1
			_, err := schemaflux.Score(ctx, input, bad)
			return err
		})
	})
	t.Run("a refused weight", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(good) }, true, func(ctx context.Context) error {
			bad := opts
			bad.Weights = map[string]float64{canary + "-unknown": 1}
			_, err := schemaflux.Score(ctx, input, bad)
			return err
		})
	})
	t.Run("a weight that is negative", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(good) }, true, func(ctx context.Context) error {
			bad := opts
			bad.Weights = map[string]float64{canary + "-criterion": -1}
			_, err := schemaflux.Score(ctx, input, bad)
			return err
		})
	})
	t.Run("an input that cannot be written as JSON", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(good) }, true, func(ctx context.Context) error {
			_, err := schemaflux.Score(ctx, scoreUnwritable{canary}, opts)
			return err
		})
	})
}
