package schemaflux_test

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

// comparedPlan is a value of the caller's own to compare.
type comparedPlan struct {
	Name  string  `json:"name"`
	Price float64 `json:"price"`
}

const (
	// compareFullAnswer names a likeness, a difference whose severity is not
	// written the way the prompt asks, a verdict and two aspect scores.
	compareFullAnswer = `{"similarity_score":0.8,"similarities":[{"aspect":"goal","description":"both cut cost"}],` +
		`"differences":[{"aspect":"price","description":"B costs more","severity":" Major "}],` +
		`"verdict":"alike in goal","aspect_scores":{"price":0.4,"risk":0.6}}`

	// compareBareAnswer is the smallest answer the call accepts: no aspect is
	// scored, so it is only good for a call that names none.
	compareBareAnswer = `{"similarity_score":0.5,"similarities":[],"differences":[],"verdict":"alike","aspect_scores":{}}`
)

var (
	comparedA = comparedPlan{"basic", 10}
	comparedB = comparedPlan{"pro", 25}
)

// compareRequest makes one call with the options given on a model that answers
// with a good answer for them and returns the request it sent.
func compareRequest(t *testing.T, opts ...schemaflux.CompareOptions) schemaflux.Request {
	t.Helper()
	model := answeringWith(compareFullAnswer)
	if _, err := schemaflux.Compare(operationClient(model), comparedA, comparedB, opts...); err != nil {
		t.Fatalf("the call failed: %v", err)
	}
	if model.Calls() != 1 {
		t.Fatalf("%d requests were sent, want 1", model.Calls())
	}
	return model.Last()
}

func TestCompareReturnsTheTwoValuesAndWhatTheModelReportedAboutThem(t *testing.T) {
	t.Parallel()
	model := answeringWith(compareFullAnswer)
	res, err := schemaflux.Compare(operationClient(model), comparedA, comparedB, schemaflux.CompareOptions{Aspects: []string{"price", "risk"}})
	if err != nil {
		t.Fatal(err)
	}
	want := schemaflux.CompareResult[comparedPlan]{
		ItemA: comparedA, ItemB: comparedB, SimilarityScore: 0.8, Verdict: "alike in goal",
		Similarities: []schemaflux.ComparisonPoint{{Aspect: "goal", Description: "both cut cost"}},
		Differences:  []schemaflux.ComparisonPoint{{Aspect: "price", Description: "B costs more", Severity: "major"}},
		AspectScores: map[string]float64{"price": 0.4, "risk": 0.6},
	}
	if !reflect.DeepEqual(res, want) {
		t.Errorf("got %+v, want %+v", res, want)
	}
	req := model.Last()
	wantText(t, req, `"name":"basic"`)
	wantText(t, req, `"name":"pro"`)
	if req.Format != "json" {
		t.Errorf("format = %q, want json", req.Format)
	}
}

func TestCompareReturnsNilListsAndAnEmptyScoreMapForAnAnswerWithNone(t *testing.T) {
	t.Parallel()
	res, err := schemaflux.Compare(operationClient(answeringWith(compareBareAnswer)), "a", "b")
	if err != nil {
		t.Fatal(err)
	}
	if res.Similarities != nil || res.Differences != nil || res.AspectScores == nil || len(res.AspectScores) != 0 {
		t.Errorf("similarities %#v differences %#v scores %#v, want nil, nil and an empty map", res.Similarities, res.Differences, res.AspectScores)
	}
}

func TestCompareWithNoOptionsAsksForDepthFiveBothFocusesAndASimilarityScore(t *testing.T) {
	t.Parallel()
	req := compareRequest(t)
	wantText(t, req, "Depth level: 5/10")
	wantText(t, req, "Focus on: both")
	wantText(t, req, "Report a similarity score alongside the differences")
	wantNoText(t, req, "Compare these aspects")
	if req.Model != "test-model" || req.WebSearch {
		t.Errorf("model %q web search %v", req.Model, req.WebSearch)
	}
}

func TestCompareEachOptionFieldChangesTheRequestAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	base := compareRequest(t)
	cases := []struct {
		name  string
		opts  schemaflux.CompareOptions
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"Aspects", schemaflux.CompareOptions{Aspects: []string{"price", "risk"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Compare these aspects: price, risk")
			wantText(t, req, `Comparison Aspects: ["price","risk"]`)
		}},
		{"FocusOn differences", schemaflux.CompareOptions{FocusOn: "differences"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Focus on: differences")
			wantNoText(t, req, "Focus on: both")
		}},
		{"FocusOn similarities", schemaflux.CompareOptions{FocusOn: "similarities"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Focus on: similarities")
		}},
		{"FocusOn both is the default", schemaflux.CompareOptions{FocusOn: "both"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Focus on: both")
		}},
		{"Depth 1", schemaflux.CompareOptions{Depth: 1}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Depth level: 1/10")
		}},
		{"Depth 10", schemaflux.CompareOptions{Depth: 10}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Depth level: 10/10")
			wantNoText(t, req, "Depth level: 5/10")
		}},
		{"OmitSimilarity removes the sentence and keeps the score in the answer's shape", schemaflux.CompareOptions{OmitSimilarity: true}, func(t *testing.T, req schemaflux.Request) {
			wantNoText(t, req, "Report a similarity score")
			wantText(t, req, `"similarity_score": number between 0 and 1`)
		}},
		{"Steering", schemaflux.CompareOptions{Options: schemaflux.Options{Steering: "weigh cost most"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "weigh cost most")
			wantText(t, req, "Depth level: 5/10")
		}},
		{"Model", schemaflux.CompareOptions{Options: schemaflux.Options{Model: "pinned-model"}}, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"Tier", schemaflux.CompareOptions{Options: schemaflux.Options{Tier: schemaflux.TierSmart}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens == base.MaxTokens {
				t.Errorf("the output ceiling is %d at both tiers", req.MaxTokens)
			}
		}},
		{"Mode", schemaflux.CompareOptions{Options: schemaflux.Options{Mode: schemaflux.ModeCreative}}, func(t *testing.T, req schemaflux.Request) {
			if req.Temperature == base.Temperature {
				t.Errorf("the temperature is %v in both modes", req.Temperature)
			}
		}},
		{"Effort", schemaflux.CompareOptions{Options: schemaflux.Options{Effort: schemaflux.EffortHigh}}, func(t *testing.T, req schemaflux.Request) {
			if req.Effort != "high" {
				t.Errorf("effort = %q", req.Effort)
			}
		}},
		{"MaxOutputTokens", schemaflux.CompareOptions{Options: schemaflux.Options{MaxOutputTokens: 77}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 77 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.CompareOptions{Options: schemaflux.Options{WebSearch: true}}, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("no web search")
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.check(t, compareRequest(t, tc.opts))
		})
	}
}

func TestCompareRefusesASettingNoCallCanUseBeforeSendingAnything(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		opts schemaflux.CompareOptions
	}{
		{"a negative depth", schemaflux.CompareOptions{Depth: -1}},
		{"the lowest depth there is", schemaflux.CompareOptions{Depth: math.MinInt}},
		{"a depth of 11", schemaflux.CompareOptions{Depth: 11}},
		{"the highest depth there is", schemaflux.CompareOptions{Depth: math.MaxInt}},
		{"a focus no one knows", schemaflux.CompareOptions{FocusOn: "everything"}},
		{"a focus in another case", schemaflux.CompareOptions{FocusOn: "Differences"}},
		{"a negative output ceiling", schemaflux.CompareOptions{Options: schemaflux.Options{MaxOutputTokens: -1}}},
		{"a tier that is not one", schemaflux.CompareOptions{Options: schemaflux.Options{Tier: schemaflux.Tier(99)}}},
		{"a mode that is not one", schemaflux.CompareOptions{Options: schemaflux.Options{Mode: schemaflux.Mode(99)}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(compareBareAnswer)
			res, err := schemaflux.Compare(operationClient(model), "a", "b", tc.opts)
			wantError(t, err, schemaflux.ErrConfiguration, "compare")
			if model.Calls() != 0 {
				t.Errorf("%d requests were sent for a refused setting", model.Calls())
			}
			if !reflect.DeepEqual(res, schemaflux.CompareResult[string]{}) {
				t.Errorf("a refusal left %+v beside the error", res)
			}
		})
	}
	t.Run("a setting that is fine at the edge of its range", func(t *testing.T) {
		t.Parallel()
		for _, depth := range []int{1, 10} {
			if _, err := schemaflux.Compare(operationClient(answeringWith(compareBareAnswer)), "a", "b", schemaflux.CompareOptions{Depth: depth}); err != nil {
				t.Errorf("depth %d: %v", depth, err)
			}
		}
	})
}

func TestCompareRefusesANilContextAndValuesThatAreNotJSON(t *testing.T) {
	t.Parallel()
	_, err := schemaflux.Compare[string](nil, "a", "b")
	wantError(t, err, schemaflux.ErrInvalidRequest, "compare")

	model := answeringWith(compareBareAnswer)
	res, err := schemaflux.Compare(operationClient(model), make(chan int), make(chan int))
	wantError(t, err, schemaflux.ErrInvalidRequest, "compare")
	if model.Calls() != 0 || res.ItemA != nil {
		t.Errorf("%d requests were sent and the result holds %+v", model.Calls(), res)
	}
}

func TestCompareRefusesAnAnswerThatDoesNotFitWhatWasAsked(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, answer string }{
		{"not JSON", `the plans are alike`},
		{"a score above one", `{"similarity_score":1.5,"similarities":[],"differences":[],"verdict":"v","aspect_scores":{}}`},
		{"a score below zero", `{"similarity_score":-0.1,"similarities":[],"differences":[],"verdict":"v","aspect_scores":{}}`},
		{"no score at all", `{"similarities":[],"differences":[],"verdict":"v","aspect_scores":{}}`},
		{"no verdict", `{"similarity_score":0.5,"similarities":[],"differences":[],"aspect_scores":{}}`},
		{"a blank verdict", `{"similarity_score":0.5,"similarities":[],"differences":[],"verdict":"  ","aspect_scores":{}}`},
		{"null similarities", `{"similarity_score":0.5,"similarities":null,"differences":[],"verdict":"v","aspect_scores":{}}`},
		{"null differences", `{"similarity_score":0.5,"similarities":[],"differences":null,"verdict":"v","aspect_scores":{}}`},
		{"a difference with no severity", `{"similarity_score":0.5,"similarities":[],"differences":[{"aspect":"a","description":"d"}],"verdict":"v","aspect_scores":{}}`},
		{"a difference of a severity no one named", `{"similarity_score":0.5,"similarities":[],"differences":[{"aspect":"a","description":"d","severity":"catastrophic"}],"verdict":"v","aspect_scores":{}}`},
		{"an aspect score above one", `{"similarity_score":0.5,"similarities":[],"differences":[],"verdict":"v","aspect_scores":{"x":2}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(tc.answer)
			res, err := schemaflux.Compare(operationClient(model), comparedA, comparedB)
			wantError(t, err, schemaflux.ErrInvalidAnswer, "compare")
			if model.Calls() != 1 {
				t.Errorf("%d requests were sent, want 1", model.Calls())
			}
			if !reflect.DeepEqual(res, schemaflux.CompareResult[comparedPlan]{}) {
				t.Errorf("a refused answer left %+v beside the error", res)
			}
		})
	}
}

func TestCompareHoldsTheAnswerToTheAspectsThatWereNamed(t *testing.T) {
	t.Parallel()
	t.Run("an aspect the answer leaves out is refused", func(t *testing.T) {
		t.Parallel()
		_, err := schemaflux.Compare(operationClient(answeringWith(compareFullAnswer)), "a", "b", schemaflux.CompareOptions{Aspects: []string{"price", "durability"}})
		wantError(t, err, schemaflux.ErrInvalidAnswer, "compare")
	})
	t.Run("an answer that scores none of them is refused", func(t *testing.T) {
		t.Parallel()
		_, err := schemaflux.Compare(operationClient(answeringWith(compareBareAnswer)), "a", "b", schemaflux.CompareOptions{Aspects: []string{"price"}})
		wantError(t, err, schemaflux.ErrInvalidAnswer, "compare")
	})
	t.Run("the scores come back under the caller's spelling, and an aspect nobody named is left out", func(t *testing.T) {
		t.Parallel()
		res, err := schemaflux.Compare(operationClient(answeringWith(compareFullAnswer)), "a", "b", schemaflux.CompareOptions{Aspects: []string{" Price "}})
		if err != nil {
			t.Fatal(err)
		}
		if want := map[string]float64{" Price ": 0.4}; !reflect.DeepEqual(res.AspectScores, want) {
			t.Errorf("scores = %v, want %v", res.AspectScores, want)
		}
	})
	t.Run("with none named, the aspects the model chose are kept as it wrote them", func(t *testing.T) {
		t.Parallel()
		res, err := schemaflux.Compare(operationClient(answeringWith(compareFullAnswer)), "a", "b")
		if err != nil {
			t.Fatal(err)
		}
		if want := map[string]float64{"price": 0.4, "risk": 0.6}; !reflect.DeepEqual(res.AspectScores, want) {
			t.Errorf("scores = %v, want %v", res.AspectScores, want)
		}
	})
	t.Run("an empty list that is not nil leaves the choice to the model and is sent as it is", func(t *testing.T) {
		t.Parallel()
		req := compareRequest(t, schemaflux.CompareOptions{Aspects: []string{}})
		wantText(t, req, "Comparison Aspects: []")
		wantNoText(t, req, "Compare these aspects")
	})
	t.Run("nil aspects are sent as null", func(t *testing.T) {
		t.Parallel()
		wantText(t, compareRequest(t), "Comparison Aspects: null")
	})
}

func TestCompareMergesSeveralOptionsValuesLeftToRight(t *testing.T) {
	t.Parallel()
	t.Run("a later value sets what it names and leaves the rest", func(t *testing.T) {
		t.Parallel()
		req := compareRequest(t,
			schemaflux.CompareOptions{Aspects: []string{"price"}, FocusOn: "differences", Depth: 3, Options: schemaflux.Options{Steering: "first"}},
			schemaflux.CompareOptions{Depth: 8, Options: schemaflux.Options{Model: "later-model"}},
			schemaflux.CompareOptions{})
		wantText(t, req, "Compare these aspects: price")
		wantText(t, req, "Focus on: differences")
		wantText(t, req, "Depth level: 8/10")
		wantText(t, req, "first")
		if req.Model != "later-model" {
			t.Errorf("model = %q", req.Model)
		}
	})
	t.Run("the later of two values for one field wins", func(t *testing.T) {
		t.Parallel()
		req := compareRequest(t, schemaflux.CompareOptions{FocusOn: "similarities"}, schemaflux.CompareOptions{FocusOn: "differences"})
		wantText(t, req, "Focus on: differences")
	})
	t.Run("a true bool is not cleared by a later false", func(t *testing.T) {
		t.Parallel()
		req := compareRequest(t, schemaflux.CompareOptions{OmitSimilarity: true}, schemaflux.CompareOptions{})
		wantNoText(t, req, "Report a similarity score")
	})
	t.Run("a later bad value is refused whatever came before it", func(t *testing.T) {
		t.Parallel()
		_, err := schemaflux.Compare(operationClient(answeringWith(compareBareAnswer)), "a", "b", schemaflux.CompareOptions{Depth: 3}, schemaflux.CompareOptions{Depth: 12})
		wantError(t, err, schemaflux.ErrConfiguration, "compare")
	})
}

func TestCompareReportsAProviderFailureWithItsKindAndNoResult(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		kind schemaflux.Kind
	}{
		{"a kind the provider gave", &schemaflux.Error{Kind: schemaflux.ErrRateLimited, Err: errors.New("slow down")}, schemaflux.ErrRateLimited},
		{"a plain error", errors.New("the connection broke"), schemaflux.ErrUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(compareBareAnswer)
			model.Fail(tc.err)
			res, err := schemaflux.Compare(operationClient(model), "a", "b")
			wantError(t, err, tc.kind, "compare")
			if !reflect.DeepEqual(res, schemaflux.CompareResult[string]{}) {
				t.Errorf("a failed call left %+v beside the error", res)
			}
		})
	}
	t.Run("a context that is already cancelled", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(operationClient(answeringWith(compareBareAnswer)))
		cancel()
		_, err := schemaflux.Compare(ctx, "a", "b")
		wantError(t, err, schemaflux.ErrCanceled, "compare")
	})
}

// compareCalled is the call the Record and payload tests make.
func compareCalled(ctx context.Context) error {
	_, err := schemaflux.Compare(ctx, comparedA, comparedB)
	return err
}

func TestCompareAddsOneRecordNamedCompareForEveryCall(t *testing.T) {
	t.Parallel()
	t.Run("a call that succeeds", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		if err := compareCalled(operationClient(answeringWith(compareBareAnswer), recordingTo(&recorder))); err != nil {
			t.Fatal(err)
		}
		r := onlyRecord(t, &recorder)
		if r.Op != "compare" || r.Err != nil || r.Requests != 1 || r.Provider != "test" {
			t.Errorf("record = %q err %v requests %d provider %q", r.Op, r.Err, r.Requests, r.Provider)
		}
		if r.Requested != schemaflux.ContractJSONWellFormed || r.Delivered != schemaflux.ContractJSONWellFormed {
			t.Errorf("levels = %v and %v, want json well-formed", r.Requested, r.Delivered)
		}
	})
	t.Run("a call whose answer is refused", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		err := compareCalled(operationClient(answeringWith("not an answer"), recordingTo(&recorder)))
		wantError(t, err, schemaflux.ErrInvalidAnswer, "compare")
		r := onlyRecord(t, &recorder)
		if r.Op != "compare" || r.Err != err || r.Requests != 1 {
			t.Errorf("record = %q err %v requests %d", r.Op, r.Err, r.Requests)
		}
	})
	t.Run("a call refused for its options, before anything was sent", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		_, err := schemaflux.Compare(operationClient(answeringWith(compareBareAnswer), recordingTo(&recorder)), "a", "b", schemaflux.CompareOptions{Depth: 99})
		wantError(t, err, schemaflux.ErrConfiguration, "compare")
		r := onlyRecord(t, &recorder)
		if r.Op != "compare" || r.Err != err || r.Requests != 0 {
			t.Errorf("record = %q err %v requests %d", r.Op, r.Err, r.Requests)
		}
	})
	t.Run("a call whose provider fails", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		model := answeringWith(compareBareAnswer)
		model.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable, Err: errors.New("down")})
		err := compareCalled(operationClient(model, recordingTo(&recorder)))
		wantError(t, err, schemaflux.ErrUnavailable, "compare")
		if r := onlyRecord(t, &recorder); r.Op != "compare" || r.Err != err {
			t.Errorf("record = %q err %v", r.Op, r.Err)
		}
	})
}

func TestCompareUnderAFloorIsRefusedAboveJSONWellFormedAndDeliveredAtIt(t *testing.T) {
	t.Parallel()
	for _, recorded := range []bool{false, true} {
		for _, floor := range []schemaflux.ContractLevel{schemaflux.ContractJSONWellFormed, schemaflux.ContractSchemaConstrained, schemaflux.ContractSchemaAndInvariantChecked} {
			name := "no recorder"
			if recorded {
				name = "a recorder"
			}
			t.Run(name+", a floor of "+floor.String(), func(t *testing.T) {
				t.Parallel()
				model := namedModel(compareBareAnswer)
				var recorder schemaflux.Recorder
				settings := []func(*schemaflux.Client){underFloor(floor)}
				if recorded {
					settings = append(settings, recordingTo(&recorder))
				}
				res, err := schemaflux.Compare(operationClient(model, settings...), "a", "b")
				refused := floor > schemaflux.ContractJSONWellFormed
				if refused {
					wantError(t, err, schemaflux.ErrPolicy, "compare")
					if model.Calls() != 0 || !reflect.DeepEqual(res, schemaflux.CompareResult[string]{}) {
						t.Errorf("a refusal sent %d requests and returned %+v", model.Calls(), res)
					}
				} else if err != nil || model.Calls() != 1 || res.Verdict != "alike" {
					t.Fatalf("at its own level: %+v, %v, %d requests", res, err, model.Calls())
				}
				if !recorded {
					return
				}
				r := onlyRecord(t, &recorder)
				if r.Op != "compare" || (r.Err != nil) != refused || r.Requested != schemaflux.ContractJSONWellFormed {
					t.Errorf("record = %q err %v requested %v", r.Op, r.Err, r.Requested)
				}
				if refused && r.Requests != 0 {
					t.Errorf("a refusal's Record counts %d requests", r.Requests)
				}
			})
		}
	}
}

func TestCompareRunsAsOneOperationScopeForOneCall(t *testing.T) {
	var scopes []string
	restore := telemetry.RegisterObserver(operationWatcher{finished: func(r telemetry.Result) {
		if r.Scope == telemetry.ScopeOperation {
			scopes = append(scopes, r.Operation)
		}
	}})
	defer restore()
	if err := compareCalled(operationClient(answeringWith(compareBareAnswer))); err != nil {
		t.Fatal(err)
	}
	if len(scopes) != 1 {
		t.Fatalf("%d operation scopes finished (%v), want 1", len(scopes), scopes)
	}
}

func TestCompareLeavesTheCallersTextOutOfEverythingItLeavesBehind(t *testing.T) {
	t.Parallel()
	const canary = "compare-private-canary-91c4"
	carrying := func(ctx context.Context) error {
		_, err := schemaflux.Compare(ctx,
			map[string]string{canary + "-key": "value " + canary}, map[string]string{"other": canary},
			schemaflux.CompareOptions{Aspects: []string{"aspect " + canary}, Options: schemaflux.Options{Steering: "steer " + canary}})
		return err
	}
	scores := `"aspect_scores":{"aspect ` + canary + `":0.5}`
	for name, answer := range map[string]string{
		"an answer that is not JSON":     "not json " + canary,
		"a verdict holding the canary":   `{"similarity_score":0.8,"similarities":[],"differences":[],"verdict":"` + canary + `",` + scores + `}`,
		"a severity holding the canary":  `{"similarity_score":0.8,"similarities":[],"differences":[{"aspect":"a","description":"d","severity":"` + canary + `"}],"verdict":"v",` + scores + `}`,
		"a score key holding the canary": `{"similarity_score":9,"similarities":[],"differences":[],"verdict":"v",` + scores + `}`,
		"a missing aspect":               `{"similarity_score":0.8,"similarities":[],"differences":[],"verdict":"v","aspect_scores":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(answer) }, true, carrying)
		})
	}
	t.Run("a provider that fails with a plain error", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Fail(errors.New("vendor said " + canary)) }, false, carrying)
	})
	t.Run("a provider that fails with an Error holding a vendor message", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) {
			p.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable, Err: errors.New("vendor said " + canary)})
		}, false, carrying)
	})
	t.Run("a call refused for an option holding the canary", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(compareBareAnswer) }, true, func(ctx context.Context) error {
			_, err := schemaflux.Compare(ctx, canary, canary, schemaflux.CompareOptions{FocusOn: canary, Aspects: []string{canary}})
			return err
		})
	})
	t.Run("values that are not JSON", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(compareBareAnswer) }, true, func(ctx context.Context) error {
			_, err := schemaflux.Compare(ctx, map[string]any{canary: make(chan int)}, map[string]any{"b": canary})
			return err
		})
	})
}
