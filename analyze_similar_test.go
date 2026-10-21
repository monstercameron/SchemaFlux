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
	similarAnswer       = `{"score":0.8,"explanation":"the same meaning"}`
	similarAspectAnswer = `{"score":0.8,"explanation":"alike","aspects":{"a1":{"score":0.9,"reason":"same claim"},"a2":{"score":0.3,"reason":"other voice"}}}`
)

// similarUnwritable is a value that cannot be written as JSON, and whose error
// says what it holds.
type similarUnwritable struct{ secret string }

func (u similarUnwritable) MarshalJSON() ([]byte, error) {
	return nil, errors.New("cannot write " + u.secret)
}

func TestSimilarAppliesTheThresholdToTheModelsScoreAndReportsIt(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		score         string
		opts          []schemaflux.SimilarOptions
		wantSimilar   bool
		wantThreshold float64
	}{
		{"the default threshold is 0.7 and a score on it is similar", "0.7", nil, true, 0.7},
		{"a score just under the default is not similar", "0.69", nil, false, 0.7},
		{"a zero threshold means the default", "0.6", []schemaflux.SimilarOptions{{Threshold: 0}}, false, 0.7},
		{"a stricter threshold", "0.8", []schemaflux.SimilarOptions{{Threshold: 0.9}}, false, 0.9},
		{"a laxer threshold", "0.6", []schemaflux.SimilarOptions{{Threshold: 0.5}}, true, 0.5},
		{"a threshold of one needs a perfect score", "0.99", []schemaflux.SimilarOptions{{Threshold: 1}}, false, 1},
		{"a threshold of one is met by a perfect score", "1", []schemaflux.SimilarOptions{{Threshold: 1}}, true, 1},
		{"a negative threshold means zero: everything is similar", "0", []schemaflux.SimilarOptions{{Threshold: -0.5}}, true, 0},
		{"the smallest negative threshold means zero", "0", []schemaflux.SimilarOptions{{Threshold: -math.SmallestNonzeroFloat64}}, true, 0},
		{"the model's score is returned as it is", "0.123", nil, false, 0.7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(`{"score":` + tc.score + `,"explanation":"why"}`)
			res, err := schemaflux.Similar(operationClient(model), "alpha", "beta", tc.opts...)
			if err != nil {
				t.Fatal(err)
			}
			var wantScore float64
			if err := json.Unmarshal([]byte(tc.score), &wantScore); err != nil {
				t.Fatal(err)
			}
			if res.IsSimilar != tc.wantSimilar || res.Threshold != tc.wantThreshold || res.Score != wantScore || res.Explanation != "why" {
				t.Errorf("got %+v, want similar %v at threshold %v with score %v", res, tc.wantSimilar, tc.wantThreshold, wantScore)
			}
			if res.MatchedAspects != nil || res.DifferingAspects != nil {
				t.Errorf("no aspect was asked for and the result has %+v and %+v", res.MatchedAspects, res.DifferingAspects)
			}
		})
	}
}

func TestSimilarIgnoresTheModelsOwnVerdict(t *testing.T) {
	t.Parallel()
	model := answeringWith(`{"score":0.2,"explanation":"why","is_similar":true,"similar":true}`)
	res, err := schemaflux.Similar(operationClient(model), "alpha", "beta")
	if err != nil || res.IsSimilar {
		t.Fatalf("got %+v, %v: a score of 0.2 against a threshold of 0.7 is not similar whatever the model says", res, err)
	}
}

func TestSimilarSortsEachAspectByItsOwnScoreInTheOrderTheyWereGiven(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		opts         schemaflux.SimilarOptions
		wantMatched  []schemaflux.AspectMatch
		wantDiffer   []schemaflux.AspectMatch
		wantDiffNote string
	}{
		{"at the default threshold", schemaflux.SimilarOptions{Aspects: []string{"meaning", " Tone "}},
			[]schemaflux.AspectMatch{{Aspect: "meaning", Score: 0.9, Reason: "same claim"}},
			[]schemaflux.AspectMatch{{Aspect: " Tone ", Score: 0.3, Reason: "other voice"}}, "the aspect is as the caller wrote it"},
		{"at a stricter threshold both differ", schemaflux.SimilarOptions{Threshold: 0.95, Aspects: []string{"meaning", "tone"}},
			nil,
			[]schemaflux.AspectMatch{{Aspect: "meaning", Score: 0.9, Reason: "same claim"}, {Aspect: "tone", Score: 0.3, Reason: "other voice"}}, "in the order they were given"},
		{"at a laxer threshold both match", schemaflux.SimilarOptions{Threshold: 0.2, Aspects: []string{"meaning", "tone"}},
			[]schemaflux.AspectMatch{{Aspect: "meaning", Score: 0.9, Reason: "same claim"}, {Aspect: "tone", Score: 0.3, Reason: "other voice"}},
			nil, "in the order they were given"},
		{"a score on the threshold matches", schemaflux.SimilarOptions{Threshold: 0.3, Aspects: []string{"meaning", "tone"}},
			[]schemaflux.AspectMatch{{Aspect: "meaning", Score: 0.9, Reason: "same claim"}, {Aspect: "tone", Score: 0.3, Reason: "other voice"}},
			nil, "an aspect at the threshold is matched, as the overall score is"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, err := schemaflux.Similar(operationClient(answeringWith(similarAspectAnswer)), "alpha", "beta", tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(res.MatchedAspects, tc.wantMatched) || !reflect.DeepEqual(res.DifferingAspects, tc.wantDiffer) {
				t.Errorf("matched %+v, differing %+v; want %+v and %+v (%s)", res.MatchedAspects, res.DifferingAspects, tc.wantMatched, tc.wantDiffer, tc.wantDiffNote)
			}
		})
	}
}

func TestSimilarWithNoOptionsAsksAtTheFastTierForADeclaredSchemaWithNoReasoning(t *testing.T) {
	t.Parallel()
	model := answeringWith(similarAnswer)
	if _, err := schemaflux.Similar(operationClient(model), "alpha", "beta"); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	if math.Abs(req.Temperature-0.3) > 1e-6 || req.MaxTokens != 2000 || req.Model != "test-model" || req.WebSearch {
		t.Errorf("temperature %v ceiling %d model %q web %v", req.Temperature, req.MaxTokens, req.Model, req.WebSearch)
	}
	if req.Schema == nil || req.SchemaName != "similarity" {
		t.Errorf("schema declared %v under the name %q", req.Schema != nil, req.SchemaName)
	}
	if req.Effort != "none" || !req.EffortPreferred {
		t.Errorf("effort %q preferred %v: with nothing numeric in the values it prefers no reasoning", req.Effort, req.EffortPreferred)
	}
	wantText(t, req, "Item A:\n\"alpha\"")
	wantText(t, req, "Item B:\n\"beta\"")
}

func TestSimilarPrefersNoReasoningOnlyWhenNothingTheCallerSuppliedMentionsANumber(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		a, b       string
		opts       schemaflux.SimilarOptions
		wantEffort schemaflux.Effort
	}{
		{"nothing numeric", "alpha", "beta", schemaflux.SimilarOptions{}, schemaflux.EffortNone},
		{"a number in the first value", "order 12", "beta", schemaflux.SimilarOptions{}, ""},
		{"a number in the second value", "alpha", "144 each", schemaflux.SimilarOptions{}, ""},
		{"a number in the steering", "alpha", "beta", schemaflux.SimilarOptions{Options: schemaflux.Options{Steering: "within 5 percent"}}, ""},
		{"a number in an aspect", "alpha", "beta", schemaflux.SimilarOptions{Aspects: []string{"price in 2026"}}, ""},
		{"the caller's own effort replaces the preference", "alpha", "beta", schemaflux.SimilarOptions{Options: schemaflux.Options{Effort: schemaflux.EffortHigh}}, schemaflux.EffortHigh},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reply := similarAnswer
			if len(tc.opts.Aspects) > 0 {
				reply = `{"score":0.8,"explanation":"x","aspects":{"a1":{"score":0.9,"reason":"r"}}}`
			}
			model := answeringWith(reply)
			if _, err := schemaflux.Similar(operationClient(model), tc.a, tc.b, tc.opts); err != nil {
				t.Fatal(err)
			}
			req := model.Last()
			if req.Effort != tc.wantEffort {
				t.Errorf("effort = %q, want %q", req.Effort, tc.wantEffort)
			}
			if wantPreferred := tc.wantEffort == schemaflux.EffortNone; req.EffortPreferred != wantPreferred {
				t.Errorf("effort preferred = %v, want %v: an effort the caller asks for is a requirement", req.EffortPreferred, wantPreferred)
			}
		})
	}
}

func TestSimilarEachOptionFieldChangesTheRequestAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  schemaflux.SimilarOptions
		reply string
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"Aspects name each aspect and its key", schemaflux.SimilarOptions{Aspects: []string{"meaning", "tone"}}, similarAspectAnswer, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `a1: "meaning"`)
			wantText(t, req, `a2: "tone"`)
			encoded, _ := json.Marshal(req.Schema)
			if !strings.Contains(string(encoded), `"a1"`) || !strings.Contains(string(encoded), `"a2"`) || strings.Contains(string(encoded), `"a3"`) {
				t.Errorf("the schema does not ask for exactly two aspects: %s", encoded)
			}
		}},
		{"an empty Aspects asks for none", schemaflux.SimilarOptions{Aspects: []string{}}, similarAnswer, func(t *testing.T, req schemaflux.Request) {
			wantNoText(t, req, "Aspects to score")
		}},
		{"Steering", schemaflux.SimilarOptions{Options: schemaflux.Options{Steering: "ignore the greeting"}}, similarAnswer, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "ignore the greeting")
		}},
		{"Model", schemaflux.SimilarOptions{Options: schemaflux.Options{Model: "pinned-model"}}, similarAnswer, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"ModeStrict", schemaflux.SimilarOptions{Options: schemaflux.Options{Mode: schemaflux.ModeStrict}}, similarAnswer, func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.1) > 1e-6 {
				t.Errorf("temperature = %v", req.Temperature)
			}
		}},
		{"TierSmart", schemaflux.SimilarOptions{Options: schemaflux.Options{Tier: schemaflux.TierSmart}}, similarAnswer, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 4000 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"MaxOutputTokens", schemaflux.SimilarOptions{Options: schemaflux.Options{MaxOutputTokens: 55}}, similarAnswer, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 55 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.SimilarOptions{Options: schemaflux.Options{WebSearch: true}}, similarAnswer, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("no web search")
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(tc.reply)
			if _, err := schemaflux.Similar(operationClient(model), "alpha", "beta", tc.opts); err != nil {
				t.Fatal(err)
			}
			tc.check(t, model.Last())
		})
	}
}

func TestSimilarDoesNotTellTheModelTheThreshold(t *testing.T) {
	t.Parallel()
	asked := func(opts ...schemaflux.SimilarOptions) string {
		model := answeringWith(similarAnswer)
		if _, err := schemaflux.Similar(operationClient(model), "alpha", "beta", opts...); err != nil {
			t.Fatal(err)
		}
		return requestText(model.Last())
	}
	if asked() != asked(schemaflux.SimilarOptions{Threshold: 0.95}) || asked() != asked(schemaflux.SimilarOptions{Threshold: -1}) {
		t.Error("the threshold changed what the model is asked: it is applied in Go, after the answer")
	}
}

func TestSimilarWritesAnyKindOfValueAsTheModelSeesIt(t *testing.T) {
	t.Parallel()
	type pair struct {
		Name string `json:"name"`
		Qty  int    `json:"qty"`
	}
	cases := []struct {
		name  string
		call  func(ctx context.Context) (schemaflux.SimilarResult, error)
		wants []string
	}{
		{"strings", func(ctx context.Context) (schemaflux.SimilarResult, error) {
			return schemaflux.Similar(ctx, "alpha", "beta")
		}, []string{`"alpha"`, `"beta"`}},
		{"structs", func(ctx context.Context) (schemaflux.SimilarResult, error) {
			return schemaflux.Similar(ctx, pair{"nut", 1}, pair{"bolt", 2})
		}, []string{`{"name":"nut","qty":1}`, `{"name":"bolt","qty":2}`}},
		{"pointers", func(ctx context.Context) (schemaflux.SimilarResult, error) {
			return schemaflux.Similar(ctx, &pair{"nut", 1}, &pair{"bolt", 2})
		}, []string{`{"name":"nut","qty":1}`, `{"name":"bolt","qty":2}`}},
		{"maps", func(ctx context.Context) (schemaflux.SimilarResult, error) {
			return schemaflux.Similar(ctx, map[string]int{"a": 1}, map[string]int{"b": 2})
		}, []string{`{"a":1}`, `{"b":2}`}},
		{"slices", func(ctx context.Context) (schemaflux.SimilarResult, error) {
			return schemaflux.Similar(ctx, []string{"x"}, []string{"y"})
		}, []string{`["x"]`, `["y"]`}},
		{"numbers", func(ctx context.Context) (schemaflux.SimilarResult, error) { return schemaflux.Similar(ctx, 12, 13) }, []string{"12", "13"}},
		{"a type that writes its own JSON", func(ctx context.Context) (schemaflux.SimilarResult, error) {
			return schemaflux.Similar(ctx, json.RawMessage(`{"k":"v"}`), json.RawMessage(`{"k":"w"}`))
		}, []string{`{"k":"v"}`, `{"k":"w"}`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(similarAnswer)
			res, err := tc.call(operationClient(model))
			if err != nil || !res.IsSimilar {
				t.Fatalf("got %+v, %v", res, err)
			}
			for _, want := range tc.wants {
				wantText(t, model.Last(), want)
			}
		})
	}
}

func TestSimilarRefusesASettingNoCallCanUseBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	const secret = "TONE-secret-aspect"
	cases := []struct {
		name string
		opts schemaflux.SimilarOptions
	}{
		{"a threshold above one", schemaflux.SimilarOptions{Threshold: 1.1}},
		{"a threshold that is not a number", schemaflux.SimilarOptions{Threshold: math.NaN()}},
		{"an infinite threshold", schemaflux.SimilarOptions{Threshold: math.Inf(1)}},
		{"a negative infinite threshold", schemaflux.SimilarOptions{Threshold: math.Inf(-1)}},
		{"a negative output ceiling", schemaflux.SimilarOptions{Options: schemaflux.Options{MaxOutputTokens: -1}}},
		{"a tier that does not exist", schemaflux.SimilarOptions{Options: schemaflux.Options{Tier: 9}}},
		{"a mode that does not exist", schemaflux.SimilarOptions{Options: schemaflux.Options{Mode: 9}}},
		{"an empty aspect", schemaflux.SimilarOptions{Aspects: []string{"meaning", ""}}},
		{"a blank aspect", schemaflux.SimilarOptions{Aspects: []string{" \t\n"}}},
		{"a repeated aspect", schemaflux.SimilarOptions{Aspects: []string{secret, "other", secret}}},
		{"an aspect repeated in another case", schemaflux.SimilarOptions{Aspects: []string{secret, "other", strings.ToLower(secret)}}},
		{"an aspect repeated with other space around it", schemaflux.SimilarOptions{Aspects: []string{secret, " " + secret + "\t"}}},
		{"an aspect of the form of an answer key", schemaflux.SimilarOptions{Aspects: []string{"a1"}}},
		{"an aspect of the form of an answer key in capitals", schemaflux.SimilarOptions{Aspects: []string{"A12"}}},
		{"an aspect of the form of an answer key with space around it", schemaflux.SimilarOptions{Aspects: []string{" a3 "}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(similarAspectAnswer)
			res, err := schemaflux.Similar(operationClient(model), "alpha", "beta", tc.opts)
			e := wantError(t, err, schemaflux.ErrConfiguration, "similar")
			if model.Calls() != 0 {
				t.Errorf("%d requests were sent", model.Calls())
			}
			if !reflect.DeepEqual(res, schemaflux.SimilarResult{}) {
				t.Errorf("a refused call left %+v beside the error", res)
			}
			if strings.Contains(e.Error(), "invalid options") {
				t.Errorf("the refusal is the internal call's and not the function's own: %v", e)
			}
			if len(tc.opts.Aspects) > 0 && !strings.Contains(e.Error(), "Aspects[") {
				t.Errorf("the error does not name the option and the position of the aspect: %v", e)
			}
			for _, aspect := range tc.opts.Aspects {
				if name := strings.TrimSpace(aspect); name != "" && strings.Contains(strings.ToLower(e.Error()), strings.ToLower(name)) {
					t.Errorf("the error repeats the aspect %q the caller wrote: %v", name, e)
				}
			}
		})
	}
}

func TestSimilarNamesTheFieldOfABadThreshold(t *testing.T) {
	t.Parallel()
	_, err := schemaflux.Similar(operationClient(answeringWith(similarAnswer)), "a", "b", schemaflux.SimilarOptions{Threshold: 7})
	if e := wantError(t, err, schemaflux.ErrConfiguration, "similar"); !strings.Contains(e.Error(), "Threshold") {
		t.Errorf("the error does not name the field: %v", e)
	}
}

func TestSimilarAcceptsAspectsThatOnlyLookLikeAnswerKeys(t *testing.T) {
	t.Parallel()
	for _, aspect := range []string{"a", "ab1", "a1b", "a-1", "a 1", "1a", "aa1", "a１"} {
		t.Run(aspect, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(`{"score":0.8,"explanation":"x","aspects":{"a1":{"score":0.9,"reason":"r"}}}`)
			res, err := schemaflux.Similar(operationClient(model), "alpha", "beta", schemaflux.SimilarOptions{Aspects: []string{aspect}})
			if err != nil || len(res.MatchedAspects) != 1 || res.MatchedAspects[0].Aspect != aspect {
				t.Fatalf("got %+v, %v", res, err)
			}
		})
	}
}

func TestSimilarRefusesAnAnswerThatIsNotWhatWasAskedFor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		aspects []string
		reply   string
	}{
		{"text", nil, "they look alike"},
		{"no answer at all", nil, ""},
		{"null", nil, "null"},
		{"a bare verdict", nil, "true"},
		{"an empty object", nil, "{}"},
		{"no score", nil, `{"explanation":"x"}`},
		{"a null score", nil, `{"score":null,"explanation":"x"}`},
		{"a score that is text", nil, `{"score":"high","explanation":"x"}`},
		{"no explanation", nil, `{"score":0.9}`},
		{"a blank explanation", nil, `{"score":0.9,"explanation":"  "}`},
		{"a score above one", nil, `{"score":1.5,"explanation":"x"}`},
		{"a score below zero", nil, `{"score":-0.1,"explanation":"x"}`},
		{"no entry for an aspect that was asked", []string{"tone"}, `{"score":0.9,"explanation":"x"}`},
		{"an empty aspects object", []string{"tone"}, `{"score":0.9,"explanation":"x","aspects":{}}`},
		{"no entry for one of two aspects", []string{"tone", "length"}, `{"score":0.9,"explanation":"x","aspects":{"a1":{"score":0.5,"reason":"r"}}}`},
		{"an aspect with no score", []string{"tone"}, `{"score":0.9,"explanation":"x","aspects":{"a1":{"reason":"r"}}}`},
		{"an aspect score above one", []string{"tone"}, `{"score":0.9,"explanation":"x","aspects":{"a1":{"score":3,"reason":"r"}}}`},
		{"an aspect score below zero", []string{"tone"}, `{"score":0.9,"explanation":"x","aspects":{"a1":{"score":-1,"reason":"r"}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(tc.reply)
			res, err := schemaflux.Similar(operationClient(model), "alpha", "beta", schemaflux.SimilarOptions{Aspects: tc.aspects})
			wantError(t, err, schemaflux.ErrInvalidAnswer, "similar")
			if model.Calls() == 0 {
				t.Error("nothing was sent")
			}
			if !reflect.DeepEqual(res, schemaflux.SimilarResult{}) {
				t.Errorf("a refused answer left %+v beside the error", res)
			}
		})
	}
}

func TestSimilarReturnsTheErrorOfAProviderThatFailsAndNothingElse(t *testing.T) {
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
			res, err := schemaflux.Similar(operationClient(&model), "alpha", "beta")
			wantError(t, err, tc.err.Kind, "similar")
			if !reflect.DeepEqual(res, schemaflux.SimilarResult{}) {
				t.Errorf("a failed call left %+v beside the error", res)
			}
		})
	}
}

func TestSimilarRefusesValuesThatCannotBeWrittenAsJSONBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	model := answeringWith(similarAnswer)
	_, err := schemaflux.Similar(operationClient(model), make(chan int), make(chan int))
	wantError(t, err, schemaflux.ErrInvalidRequest, "similar")
	if model.Calls() != 0 {
		t.Errorf("%d requests were sent", model.Calls())
	}
}

func TestSimilarWithANilContextIsAnInvalidRequest(t *testing.T) {
	t.Parallel()
	var none context.Context
	res, err := schemaflux.Similar(none, "a", "b")
	e := wantError(t, err, schemaflux.ErrInvalidRequest, "similar")
	if !strings.Contains(e.Error(), "context") || !reflect.DeepEqual(res, schemaflux.SimilarResult{}) {
		t.Errorf("got %+v and %v", res, e)
	}
}

func TestSimilarMergesSeveralOptionsValuesFromLeftToRight(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		opts          []schemaflux.SimilarOptions
		wantThreshold float64
		wantAspects   []string
		check         func(t *testing.T, req schemaflux.Request)
	}{
		{"no value is the default", nil, 0.7, nil, nil},
		{"one value is used as it is", []schemaflux.SimilarOptions{{Threshold: 0.6, Aspects: []string{"meaning"}}}, 0.6, []string{"meaning"}, nil},
		{"the later threshold wins", []schemaflux.SimilarOptions{{Threshold: 0.6}, {Threshold: 0.5}}, 0.5, nil, nil},
		{"a later zero threshold does not undo an earlier one", []schemaflux.SimilarOptions{{Threshold: 0.6}, {}}, 0.6, nil, nil},
		{"a later negative threshold is a value", []schemaflux.SimilarOptions{{Threshold: 0.6}, {Threshold: -1}}, 0, nil, nil},
		{"later aspects replace earlier ones", []schemaflux.SimilarOptions{{Aspects: []string{"tone"}}, {Aspects: []string{"meaning"}}}, 0.7, []string{"meaning"}, nil},
		{"later nil aspects leave earlier ones", []schemaflux.SimilarOptions{{Aspects: []string{"tone"}}, {Threshold: 0.5}}, 0.5, []string{"tone"}, nil},
		{"the common settings merge too", []schemaflux.SimilarOptions{
			{Options: schemaflux.Options{Steering: "be strict", WebSearch: true}},
			{Options: schemaflux.Options{Model: "later-model"}},
			{Options: schemaflux.Options{MaxOutputTokens: 77}, Threshold: 0.9},
		}, 0.9, nil, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "be strict")
			if req.Model != "later-model" || req.MaxTokens != 77 || !req.WebSearch {
				t.Errorf("model %q ceiling %d web %v", req.Model, req.MaxTokens, req.WebSearch)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reply := similarAnswer
			if len(tc.wantAspects) > 0 {
				reply = `{"score":0.8,"explanation":"x","aspects":{"a1":{"score":0.6,"reason":"r"}}}`
			}
			model := answeringWith(reply)
			res, err := schemaflux.Similar(operationClient(model), "alpha", "beta", tc.opts...)
			if err != nil {
				t.Fatal(err)
			}
			if res.Threshold != tc.wantThreshold {
				t.Errorf("threshold = %v, want %v", res.Threshold, tc.wantThreshold)
			}
			var got []string
			for _, a := range append(append([]schemaflux.AspectMatch(nil), res.MatchedAspects...), res.DifferingAspects...) {
				got = append(got, a.Aspect)
			}
			if !reflect.DeepEqual(got, tc.wantAspects) {
				t.Errorf("aspects = %q, want %q", got, tc.wantAspects)
			}
			if tc.check != nil {
				tc.check(t, model.Last())
			}
		})
	}
}

func TestSimilarRefusesTheMergedValueOfSeveralWhenItIsNotUsable(t *testing.T) {
	t.Parallel()
	model := answeringWith(similarAnswer)
	_, err := schemaflux.Similar(operationClient(model), "a", "b", schemaflux.SimilarOptions{Threshold: 0.5}, schemaflux.SimilarOptions{Threshold: 4})
	wantError(t, err, schemaflux.ErrConfiguration, "similar")
	if model.Calls() != 0 {
		t.Errorf("%d requests were sent", model.Calls())
	}
}

func TestSimilarResultMarshalsWithTheTagsOfTheInternalResult(t *testing.T) {
	t.Parallel()
	res := schemaflux.SimilarResult{
		IsSimilar: true, Score: 0.8, Threshold: 0.7, Explanation: "why",
		MatchedAspects:   []schemaflux.AspectMatch{{Aspect: "meaning", Score: 0.9, Reason: "same"}},
		DifferingAspects: []schemaflux.AspectMatch{{Aspect: "tone", Score: 0.3}},
	}
	got, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"is_similar":true,"score":0.8,"threshold":0.7,"matched_aspects":[{"aspect":"meaning","score":0.9,"reason":"same"}],"differing_aspects":[{"aspect":"tone","score":0.3}],"explanation":"why"}`
	if string(got) != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestSimilarAddsOneRecordForEachCallWithTheOperationsName(t *testing.T) {
	t.Parallel()
	t.Run("a call that succeeds", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		model := answeringWith(similarAnswer)
		if _, err := schemaflux.Similar(operationClient(model, recordingTo(&recorder)), "a", "b"); err != nil {
			t.Fatal(err)
		}
		r := onlyRecord(t, &recorder)
		if r.Op != "similar" || r.Err != nil || r.Requests != 1 || r.Provider != "test" {
			t.Errorf("record = %q err %v requests %d provider %q", r.Op, r.Err, r.Requests, r.Provider)
		}
		if r.Requested != schemaflux.ContractSchemaConstrained || r.Delivered != schemaflux.ContractSchemaConstrained {
			t.Errorf("levels = %v and %v, want schema constrained", r.Requested, r.Delivered)
		}
	})
	t.Run("a call that fails on its answer", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		_, err := schemaflux.Similar(operationClient(answeringWith("not json"), recordingTo(&recorder)), "a", "b")
		wantError(t, err, schemaflux.ErrInvalidAnswer, "similar")
		r := onlyRecord(t, &recorder)
		if r.Op != "similar" || r.Err != err || r.Requests < 1 || r.Requested != schemaflux.ContractSchemaConstrained || r.Delivered >= schemaflux.ContractSchemaConstrained {
			t.Errorf("record = %q err %v requests %d requested %v delivered %v", r.Op, r.Err, r.Requests, r.Requested, r.Delivered)
		}
	})
	t.Run("a call the provider fails", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		var model schemafluxtest.Provider
		model.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable})
		_, err := schemaflux.Similar(operationClient(&model, recordingTo(&recorder)), "a", "b")
		r := onlyRecord(t, &recorder)
		wantError(t, r.Err, schemaflux.ErrUnavailable, "similar")
		if r.Op != "similar" || r.Err != err {
			t.Errorf("record = %q err %v", r.Op, r.Err)
		}
	})
	t.Run("a call refused before anything is sent: a bad option", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		model := answeringWith(similarAnswer)
		_, err := schemaflux.Similar(operationClient(model, recordingTo(&recorder)), "a", "b", schemaflux.SimilarOptions{Threshold: 3})
		r := onlyRecord(t, &recorder)
		wantError(t, r.Err, schemaflux.ErrConfiguration, "similar")
		if r.Op != "similar" || r.Err != err || r.Requests != 0 || model.Calls() != 0 {
			t.Errorf("record = %q err %v requests %d", r.Op, r.Err, r.Requests)
		}
	})
	t.Run("a call refused before anything is sent: a bad aspect", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		model := answeringWith(similarAnswer)
		_, err := schemaflux.Similar(operationClient(model, recordingTo(&recorder)), "a", "b", schemaflux.SimilarOptions{Aspects: []string{" "}})
		r := onlyRecord(t, &recorder)
		wantError(t, r.Err, schemaflux.ErrConfiguration, "similar")
		if r.Op != "similar" || r.Err != err || r.Requests != 0 || model.Calls() != 0 {
			t.Errorf("record = %q err %v requests %d", r.Op, r.Err, r.Requests)
		}
	})
	t.Run("a call refused before anything is sent: values that cannot be written", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		model := answeringWith(similarAnswer)
		_, err := schemaflux.Similar(operationClient(model, recordingTo(&recorder)), make(chan int), make(chan int))
		r := onlyRecord(t, &recorder)
		wantError(t, r.Err, schemaflux.ErrInvalidRequest, "similar")
		if r.Op != "similar" || r.Err != err || r.Requests != 0 || model.Calls() != 0 {
			t.Errorf("record = %q err %v requests %d", r.Op, r.Err, r.Requests)
		}
	})
	t.Run("a recorder on the context and one on the client each get the Record", func(t *testing.T) {
		t.Parallel()
		var onClient, onContext schemaflux.Recorder
		ctx := schemaflux.WithRecorder(operationClient(answeringWith(similarAnswer), recordingTo(&onClient)), &onContext)
		if _, err := schemaflux.Similar(ctx, "a", "b"); err != nil {
			t.Fatal(err)
		}
		if onlyRecord(t, &onClient).Op != "similar" || onlyRecord(t, &onContext).Op != "similar" {
			t.Error("a recorder was not given the Record")
		}
	})
	t.Run("a Record marshals with the operation's name", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		if _, err := schemaflux.Similar(operationClient(answeringWith(similarAnswer), recordingTo(&recorder)), "a", "b"); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(onlyRecord(t, &recorder))
		if err != nil || !strings.Contains(string(encoded), `"op":"similar"`) {
			t.Errorf("record JSON %s, %v", encoded, err)
		}
	})
}

func TestSimilarUnderAFloorIsHeldToTheLevelItEstablishes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		floor   schemaflux.ContractLevel
		refused bool
	}{
		{"a floor of well-formed JSON is met", schemaflux.ContractJSONWellFormed, false},
		{"a floor of schema constrained is met: the schema is declared", schemaflux.ContractSchemaConstrained, false},
		{"a floor of schema and invariants is not", schemaflux.ContractSchemaAndInvariantChecked, true},
		{"a floor of evidence is not", schemaflux.ContractEvidenceChecked, true},
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
				model := namedModel(similarAnswer)
				settings := []func(*schemaflux.Client){underFloor(tc.floor)}
				if withRecorder {
					settings = append(settings, recordingTo(&recorder))
				}
				res, err := schemaflux.Similar(operationClient(model, settings...), "alpha", "beta")
				if tc.refused {
					wantError(t, err, schemaflux.ErrPolicy, "similar")
					if model.Calls() != 0 || !reflect.DeepEqual(res, schemaflux.SimilarResult{}) {
						t.Errorf("%d requests were sent, result %+v", model.Calls(), res)
					}
				} else if err != nil || !res.IsSimilar || model.Calls() != 1 {
					t.Fatalf("got %+v, %v after %d requests", res, err, model.Calls())
				}
				if withRecorder {
					r := onlyRecord(t, &recorder)
					if r.Op != "similar" || (r.Err != nil) != tc.refused || r.Requested != schemaflux.ContractSchemaConstrained {
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

func TestSimilarUnderAFloorAndABadOptionIsStillAConfigurationError(t *testing.T) {
	t.Parallel()
	model := namedModel(similarAnswer)
	_, err := schemaflux.Similar(operationClient(model, underFloor(schemaflux.ContractFullyGoverned)), "a", "b", schemaflux.SimilarOptions{Threshold: 2})
	wantError(t, err, schemaflux.ErrConfiguration, "similar")
}

func TestSimilarUnderAnObserverSendsTheSameRequestAndRecordsOnce(t *testing.T) {
	// Not parallel: an observer is process state.
	var seen []string
	restore := telemetry.RegisterObserver(operationWatcher{finished: func(r telemetry.Result) {
		if r.Scope == telemetry.ScopeOperation {
			seen = append(seen, r.Operation)
		}
	}})
	defer restore()

	opts := schemaflux.SimilarOptions{Aspects: []string{"meaning", "tone"}, Threshold: 0.95}
	plain := answeringWith(similarAspectAnswer)
	if _, err := schemaflux.Similar(operationClient(plain), "alpha", "beta", opts); err != nil {
		t.Fatal(err)
	}
	var recorder schemaflux.Recorder
	watched := answeringWith(similarAspectAnswer)
	res, err := schemaflux.Similar(operationClient(watched, recordingTo(&recorder)), "alpha", "beta", opts)
	if err != nil || len(res.MatchedAspects) != 0 || len(res.DifferingAspects) != 2 || res.Threshold != 0.95 {
		t.Fatalf("got %+v, %v", res, err)
	}
	if !reflect.DeepEqual(plain.Last(), watched.Last()) {
		t.Error("an observer changed what the model is asked")
	}
	if onlyRecord(t, &recorder).Op != "similar" {
		t.Error("the Record under an observer has the wrong operation")
	}
	if len(seen) != 2 {
		t.Errorf("the observer saw %v, want one operation scope for each call", seen)
	}
}

func TestSimilarKeepsThePayloadOutOfEverythingItLeavesBehind(t *testing.T) {
	t.Parallel()
	const canary = "CANARY-5e8c1d"
	opts := schemaflux.SimilarOptions{
		Options:   schemaflux.Options{Steering: "steer " + canary},
		Aspects:   []string{"tone", "length"},
		Threshold: 0.7,
	}
	a := map[string]string{canary + "-key": "text " + canary}
	b := map[string]string{"k": "other " + canary}
	run := func(ctx context.Context) error {
		_, err := schemaflux.Similar(ctx, a, a, opts)
		return err
	}
	runPair := func(ctx context.Context) error {
		_, err := schemaflux.Similar(ctx, a, b, opts)
		return err
	}
	good := `{"score":0.8,"explanation":"` + canary + `","aspects":{"a1":{"score":0.9,"reason":"` + canary + `"},"a2":{"score":0.9,"reason":"` + canary + `"}}}`

	t.Run("a call that succeeds", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(good) }, true, run)
	})
	t.Run("a call that succeeds with two different values", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(good) }, true, runPair)
	})
	t.Run("a call that succeeds with the canary in an aspect name", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(good) }, true, func(ctx context.Context) error {
			named := opts
			named.Aspects = []string{canary + "-aspect", "length"}
			_, err := schemaflux.Similar(ctx, a, b, named)
			return err
		})
	})
	t.Run("an answer that is text", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("they are alike: " + canary) }, true, run)
	})
	t.Run("an answer with a score off the scale", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) {
			p.Reply(`{"score":7,"explanation":"` + canary + `"}`)
		}, true, run)
	})
	t.Run("an answer with an aspect score off the scale", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) {
			p.Reply(`{"score":0.5,"explanation":"x","aspects":{"a1":{"score":9,"reason":"` + canary + `"},"a2":{"score":0.1,"reason":"x"}}}`)
		}, true, run)
	})
	t.Run("an answer that leaves an aspect out and names others", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) {
			p.Reply(`{"score":0.5,"explanation":"` + canary + `","aspects":{"` + canary + `":{"score":0.5,"reason":"` + canary + `"}}}`)
		}, true, run)
	})
	t.Run("an answer with no explanation", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) {
			p.Reply(`{"score":0.5,"explanation":" ","note":"` + canary + `"}`)
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
	t.Run("a refused option", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(good) }, true, func(ctx context.Context) error {
			bad := opts
			bad.Threshold = 9
			_, err := schemaflux.Similar(ctx, a, b, bad)
			return err
		})
	})
	t.Run("an aspect that repeats another", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(good) }, true, func(ctx context.Context) error {
			bad := opts
			bad.Aspects = []string{canary, " " + strings.ToUpper(canary)}
			_, err := schemaflux.Similar(ctx, a, b, bad)
			return err
		})
	})
	t.Run("values that cannot be written as JSON", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(good) }, true, func(ctx context.Context) error {
			_, err := schemaflux.Similar(ctx, similarUnwritable{canary}, similarUnwritable{canary}, opts)
			return err
		})
	})
}

func TestSimilarGivesTheSameResultAndSendsTheSameRequestInEveryStateOfTheCall(t *testing.T) {
	// Not parallel: it compares what each state sent with what the plain call sent.
	opts := schemaflux.SimilarOptions{Aspects: []string{"meaning", "tone"}, Threshold: 0.95, Options: schemaflux.Options{Steering: "be strict"}}
	want := schemaflux.SimilarResult{
		Score: 0.8, Threshold: 0.95, Explanation: "alike",
		DifferingAspects: []schemaflux.AspectMatch{{Aspect: "meaning", Score: 0.9, Reason: "same claim"}, {Aspect: "tone", Score: 0.3, Reason: "other voice"}},
	}
	var recorder schemaflux.Recorder
	states := []struct {
		name  string
		model *schemafluxtest.Provider
		set   []func(*schemaflux.Client)
	}{
		{"plain", answeringWith(similarAspectAnswer), nil},
		{"a recorder", answeringWith(similarAspectAnswer), []func(*schemaflux.Client){recordingTo(&recorder)}},
		{"a floor it meets", namedModel(similarAspectAnswer), []func(*schemaflux.Client){underFloor(schemaflux.ContractSchemaConstrained)}},
	}
	var plainRequest schemaflux.Request
	for i, state := range states {
		res, err := schemaflux.Similar(operationClient(state.model, state.set...), "alpha", "beta", opts)
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
