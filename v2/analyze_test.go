package schemaflux_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

// classifiedTeam is a category type of the caller's own.
type classifiedTeam string

const (
	classifiedBillingAnswer = `{"category":"billing","confidence":0.9,"alternatives":[],"reasoning":"a charge"}`
	classifiedBilling       = "billing"
)

var classifyCategories = []string{"billing", "technical"}

func TestClassifyReturnsTheCategoryAndWhatTheModelReportedAboutIt(t *testing.T) {
	t.Parallel()
	model := answeringWith(`{"category":"billing","confidence":0.9,"alternatives":[{"category":"technical","confidence":0.3}],"reasoning":"a charge"}`)
	res, err := schemaflux.Classify(operationClient(model), "my card was charged twice", classifyCategories)
	if err != nil {
		t.Fatal(err)
	}
	want := schemaflux.ClassifyResult[string]{
		Category: "billing", ModelConfidence: 0.9, Reasoning: "a charge",
		Alternatives: []schemaflux.ClassifyAlternative[string]{{Category: "technical", ModelConfidence: 0.3}},
	}
	if !equalClassify(res, want) {
		t.Errorf("got %+v, want %+v", res, want)
	}
	if model.Calls() != 1 {
		t.Fatalf("%d requests were sent, want 1", model.Calls())
	}
	req := model.Last()
	wantText(t, req, `Available Categories: ["billing","technical"]`)
	wantText(t, req, "my card was charged twice")
	if req.Format != "json" {
		t.Errorf("format = %q", req.Format)
	}
}

// equalClassify compares two results, treating no alternatives as an empty list.
func equalClassify[C ~string](a, b schemaflux.ClassifyResult[C]) bool {
	if a.Category != b.Category || a.ModelConfidence != b.ModelConfidence || a.Reasoning != b.Reasoning || len(a.Alternatives) != len(b.Alternatives) {
		return false
	}
	for i := range a.Alternatives {
		if a.Alternatives[i] != b.Alternatives[i] {
			return false
		}
	}
	return true
}

func TestClassifyReturnsTheCallersOwnCategoryType(t *testing.T) {
	t.Parallel()
	model := answeringWith(classifiedBillingAnswer)
	res, err := schemaflux.Classify(operationClient(model), "x", []classifiedTeam{"billing", "support"})
	if err != nil {
		t.Fatal(err)
	}
	var team classifiedTeam = res.Category
	if team != "billing" {
		t.Errorf("category = %q", team)
	}
}

func TestClassifyReturnsTheCategoryAsTheCallerWroteItWhateverCaseTheModelUsed(t *testing.T) {
	t.Parallel()
	model := answeringWith(`{"category":"BILLING","confidence":0.9,"alternatives":[],"reasoning":"r"}`)
	res, err := schemaflux.Classify(operationClient(model), "x", []string{"Billing", "technical"})
	if err != nil || res.Category != "Billing" {
		t.Errorf("got %+v, %v, want the category as it was offered", res, err)
	}
}

func TestClassifyReturnsNoAlternativesAsNil(t *testing.T) {
	t.Parallel()
	res, err := schemaflux.Classify(operationClient(answeringWith(classifiedBillingAnswer)), "x", classifyCategories)
	if err != nil || res.Alternatives != nil {
		t.Errorf("alternatives = %#v, err %v, want nil", res.Alternatives, err)
	}
}

func TestClassifyWithNoOptionsAsksForHalfConfidenceAtTheFastTier(t *testing.T) {
	t.Parallel()
	model := answeringWith(classifiedBillingAnswer)
	if _, err := schemaflux.Classify(operationClient(model), "x", classifyCategories); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	wantText(t, req, "Minimum confidence: 0.50")
	if math.Abs(req.Temperature-0.3) > 1e-6 || req.MaxTokens != 2000 || req.Model != "test-model" || req.WebSearch {
		t.Errorf("temperature %v ceiling %d model %q web %v", req.Temperature, req.MaxTokens, req.Model, req.WebSearch)
	}
}

func TestClassifyEachOptionFieldChangesTheRequestAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  schemaflux.ClassifyOptions
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"MinConfidence", schemaflux.ClassifyOptions{MinConfidence: 0.8}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Minimum confidence: 0.80")
		}},
		{"MinConfidence negative means no floor", schemaflux.ClassifyOptions{MinConfidence: -1}, func(t *testing.T, req schemaflux.Request) {
			wantNoText(t, req, "Minimum confidence")
		}},
		{"Descriptions", schemaflux.ClassifyOptions{Descriptions: map[string]string{"billing": "anything about money"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "billing: anything about money")
		}},
		{"Examples", schemaflux.ClassifyOptions{Examples: map[string][]string{"technical": {"it crashed", "it froze"}}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Examples of technical: it crashed; it froze")
		}},
		{"Steering", schemaflux.ClassifyOptions{Steering: "prefer billing"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "prefer billing")
		}},
		{"Model", schemaflux.ClassifyOptions{Model: "pinned-model"}, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"ModeStrict", schemaflux.ClassifyOptions{Mode: schemaflux.ModeStrict}, func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.1) > 1e-6 {
				t.Errorf("temperature = %v", req.Temperature)
			}
		}},
		{"TierSmart", schemaflux.ClassifyOptions{Tier: schemaflux.TierSmart}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 4000 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"MaxOutputTokens", schemaflux.ClassifyOptions{MaxOutputTokens: 55}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 55 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.ClassifyOptions{WebSearch: true}, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("no web search")
			}
		}},
		{"Effort", schemaflux.ClassifyOptions{Effort: schemaflux.EffortHigh}, func(t *testing.T, req schemaflux.Request) {
			if req.Effort != "high" || req.EffortPreferred {
				t.Errorf("effort = %q preferred %v, want high as the caller's requirement", req.Effort, req.EffortPreferred)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(classifiedBillingAnswer)
			if _, err := schemaflux.Classify(operationClient(model), "x", classifyCategories, tc.opts); err != nil {
				t.Fatal(err)
			}
			tc.check(t, model.Last())
		})
	}
}

func TestClassifyRefusesAnAnswerTheModelIsLessSureOfThanTheFloor(t *testing.T) {
	t.Parallel()
	const unsure = `{"category":"billing","confidence":0.3,"alternatives":[],"reasoning":"a guess"}`
	cases := []struct {
		name    string
		opts    schemaflux.ClassifyOptions
		refused bool
	}{
		{"the default floor is 0.5", schemaflux.ClassifyOptions{}, true},
		{"a lower floor accepts it", schemaflux.ClassifyOptions{MinConfidence: 0.2}, false},
		{"a negative floor accepts it", schemaflux.ClassifyOptions{MinConfidence: -1}, false},
		{"a higher floor refuses it", schemaflux.ClassifyOptions{MinConfidence: 0.9}, true},
		{"a floor of one refuses anything less than certain", schemaflux.ClassifyOptions{MinConfidence: 1}, true},
		{"a floor of exactly the confidence accepts it", schemaflux.ClassifyOptions{MinConfidence: 0.3}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, err := schemaflux.Classify(operationClient(answeringWith(unsure)), "x", classifyCategories, tc.opts)
			if !tc.refused {
				if err != nil || res.Category != classifiedBilling {
					t.Fatalf("got %+v, %v", res, err)
				}
				return
			}
			wantError(t, err, schemaflux.ErrInvalidAnswer, "classify")
			if !equalClassify(res, schemaflux.ClassifyResult[string]{}) {
				t.Errorf("a refused answer left %+v beside the error", res)
			}
		})
	}
}

func TestClassifyRefusesAnAnswerThatIsNotOneOfTheCategories(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"a category that was not offered": `{"category":"shipping","confidence":0.9,"alternatives":[],"reasoning":"r"}`,
		"no category":                     `{"confidence":0.9,"alternatives":[],"reasoning":"r"}`,
		"an empty category":               `{"category":"","confidence":0.9,"alternatives":[],"reasoning":"r"}`,
		"a confidence off the scale":      `{"category":"billing","confidence":1.5,"alternatives":[],"reasoning":"r"}`,
		"no confidence":                   `{"category":"billing","alternatives":[],"reasoning":"r"}`,
		"no reasoning":                    `{"category":"billing","confidence":0.9,"alternatives":[]}`,
		"text":                            `It is billing.`,
		"a list":                          `["billing"]`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			res, err := schemaflux.Classify(operationClient(answeringWith(body)), "x", classifyCategories)
			wantError(t, err, schemaflux.ErrInvalidAnswer, "classify")
			if !equalClassify(res, schemaflux.ClassifyResult[string]{}) {
				t.Errorf("a refused answer left %+v beside the error", res)
			}
		})
	}
}

func TestClassifyRefusesNoCategoriesAsAnInvalidRequestBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	for name, categories := range map[string][]string{"nil": nil, "an empty list": {}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(classifiedBillingAnswer)
			_, err := schemaflux.Classify(operationClient(model), "x", categories)
			e := wantError(t, err, schemaflux.ErrInvalidRequest, "classify")
			if e.Message != "categories is empty" || model.Calls() != 0 {
				t.Errorf("message %q after %d requests", e.Message, model.Calls())
			}
		})
	}
}

func TestClassifyRefusesABadOptionBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	cases := map[string]schemaflux.ClassifyOptions{
		"a floor above one":              {MinConfidence: 1.01},
		"a floor that is not a number":   {MinConfidence: math.NaN()},
		"a floor of positive infinity":   {MinConfidence: math.Inf(1)},
		"a floor of negative infinity":   {MinConfidence: math.Inf(-1)},
		"a negative output ceiling":      {MaxOutputTokens: -1},
		"a tier that is not one":         {Tier: 9},
		"a mode that is not one":         {Mode: 9},
		"a bad setting beside good ones": {MinConfidence: 0.5, Descriptions: map[string]string{"billing": "x"}, MaxOutputTokens: -5},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(classifiedBillingAnswer)
			_, err := schemaflux.Classify(operationClient(model), "x", classifyCategories, opts)
			wantError(t, err, schemaflux.ErrConfiguration, "classify")
			if model.Calls() != 0 {
				t.Errorf("%d requests were sent", model.Calls())
			}
		})
	}
}

func TestClassifyNamesTheFieldOfABadFloor(t *testing.T) {
	t.Parallel()
	_, err := schemaflux.Classify(operationClient(answeringWith(classifiedBillingAnswer)), "x", classifyCategories, schemaflux.ClassifyOptions{MinConfidence: 2})
	e := wantError(t, err, schemaflux.ErrConfiguration, "classify")
	if e.Message != "MinConfidence must be a number up to 1" {
		t.Errorf("message = %q", e.Message)
	}
	if want := "classify: configuration: MinConfidence must be a number up to 1"; e.Error() != want {
		t.Errorf("Error() = %q, want %q", e.Error(), want)
	}
}

func TestClassifyWithANilContextIsAnInvalidRequest(t *testing.T) {
	t.Parallel()
	var nilContext context.Context
	_, err := schemaflux.Classify(nilContext, "x", classifyCategories)
	if e := wantError(t, err, schemaflux.ErrInvalidRequest, "classify"); e.Message != "the context is nil" {
		t.Errorf("message = %q", e.Message)
	}
}

func TestClassifyMergesSeveralOptionsValuesFromLeftToRight(t *testing.T) {
	t.Parallel()
	t.Run("a later floor wins and a later zero leaves it", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(classifiedBillingAnswer)
		_, err := schemaflux.Classify(operationClient(model), "x", classifyCategories,
			schemaflux.ClassifyOptions{MinConfidence: 0.6, Model: "first"},
			schemaflux.ClassifyOptions{Steering: "later"},
			schemaflux.ClassifyOptions{MinConfidence: 0.7})
		if err != nil {
			t.Fatal(err)
		}
		req := model.Last()
		wantText(t, req, "Minimum confidence: 0.70")
		wantText(t, req, "later")
		if req.Model != "first" {
			t.Errorf("model = %q", req.Model)
		}
	})
	t.Run("a bool turned on stays on", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(classifiedBillingAnswer)
		_, err := schemaflux.Classify(operationClient(model), "x", classifyCategories,
			schemaflux.ClassifyOptions{WebSearch: true}, schemaflux.ClassifyOptions{})
		if err != nil || !model.Last().WebSearch {
			t.Errorf("web search %v, err %v", model.Last().WebSearch, err)
		}
	})
	t.Run("a later map replaces the earlier", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(classifiedBillingAnswer)
		_, err := schemaflux.Classify(operationClient(model), "x", classifyCategories,
			schemaflux.ClassifyOptions{Descriptions: map[string]string{"billing": "first"}},
			schemaflux.ClassifyOptions{Descriptions: map[string]string{"technical": "second"}})
		if err != nil {
			t.Fatal(err)
		}
		wantText(t, model.Last(), "technical: second")
		wantNoText(t, model.Last(), "billing: first")
	})
	t.Run("a refused setting in a later value is refused", func(t *testing.T) {
		t.Parallel()
		_, err := schemaflux.Classify(operationClient(answeringWith(classifiedBillingAnswer)), "x", classifyCategories,
			schemaflux.ClassifyOptions{}, schemaflux.ClassifyOptions{MinConfidence: 5})
		wantError(t, err, schemaflux.ErrConfiguration, "classify")
	})
}

func TestClassifyReadsAnyKindOfInput(t *testing.T) {
	t.Parallel()
	type ticket struct {
		Subject string `json:"subject"`
	}
	cases := map[string]struct {
		input any
		seen  string
	}{
		"text":      {"my card", "my card"},
		"a struct":  {ticket{Subject: "refund please"}, "refund please"},
		"a map":     {map[string]string{"k": "vv"}, "vv"},
		"a number":  {42, "42"},
		"a pointer": {&ticket{Subject: "via pointer"}, "via pointer"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(classifiedBillingAnswer)
			if _, err := schemaflux.Classify(operationClient(model), tc.input, classifyCategories); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(model.Last().User, tc.seen) {
				t.Errorf("%q is not in the user message: %s", tc.seen, excerpt(model.Last().User))
			}
		})
	}
}

func TestClassifyReturnsAnErrorTheCallerCanClassifyWhenTheProviderFails(t *testing.T) {
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
			res, err := schemaflux.Classify(operationClient(&model), "x", classifyCategories)
			wantError(t, err, tc.err.Kind, "classify")
			if !equalClassify(res, schemaflux.ClassifyResult[string]{}) {
				t.Errorf("a failed call left %+v beside the error", res)
			}
		})
	}
}

func TestClassifyResultMarshalsWithTheTagsOfTheInternalResult(t *testing.T) {
	t.Parallel()
	res := schemaflux.ClassifyResult[string]{
		Category: "billing", ModelConfidence: 0.9, Reasoning: "why",
		Alternatives: []schemaflux.ClassifyAlternative[string]{{Category: "technical", ModelConfidence: 0.3}},
	}
	got, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"category":"billing","confidence":0.9,"alternatives":[{"category":"technical","confidence":0.3}],"reasoning":"why"}`
	if string(got) != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestClassifyAddsOneRecordForEachCallWithTheOperationsName(t *testing.T) {
	t.Parallel()
	t.Run("a call that succeeds", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		model := answeringWith(classifiedBillingAnswer)
		if _, err := schemaflux.Classify(operationClient(model, recordingTo(&recorder)), "x", classifyCategories); err != nil {
			t.Fatal(err)
		}
		r := onlyRecord(t, &recorder)
		if r.Op != "classify" || r.Err != nil || r.Requests != 1 || r.Provider != "test" {
			t.Errorf("record = %q err %v requests %d provider %q", r.Op, r.Err, r.Requests, r.Provider)
		}
		if r.Requested != schemaflux.ContractJSONWellFormed || r.Delivered != schemaflux.ContractJSONWellFormed {
			t.Errorf("levels = %v and %v, want json well-formed", r.Requested, r.Delivered)
		}
	})
	t.Run("a call that fails on its answer", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		_, err := schemaflux.Classify(operationClient(answeringWith("not json"), recordingTo(&recorder)), "x", classifyCategories)
		wantError(t, err, schemaflux.ErrInvalidAnswer, "classify")
		r := onlyRecord(t, &recorder)
		if r.Op != "classify" || r.Err != err || r.Requests != 1 || r.Delivered != schemaflux.ContractPromptOnly {
			t.Errorf("record = %q err %v requests %d delivered %v", r.Op, r.Err, r.Requests, r.Delivered)
		}
	})
	t.Run("a call the provider fails", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		var model schemafluxtest.Provider
		model.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable})
		_, err := schemaflux.Classify(operationClient(&model, recordingTo(&recorder)), "x", classifyCategories)
		r := onlyRecord(t, &recorder)
		wantError(t, r.Err, schemaflux.ErrUnavailable, "classify")
		if r.Op != "classify" || r.Err != err {
			t.Errorf("record = %q err %v", r.Op, r.Err)
		}
	})
	t.Run("a call refused before anything is sent: no categories", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		model := answeringWith(classifiedBillingAnswer)
		_, err := schemaflux.Classify(operationClient(model, recordingTo(&recorder)), "x", []string{})
		r := onlyRecord(t, &recorder)
		wantError(t, r.Err, schemaflux.ErrInvalidRequest, "classify")
		if r.Op != "classify" || r.Err != err || r.Requests != 0 || model.Calls() != 0 {
			t.Errorf("record = %q err %v requests %d", r.Op, r.Err, r.Requests)
		}
	})
	t.Run("a call refused before anything is sent: a bad option", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		model := answeringWith(classifiedBillingAnswer)
		_, err := schemaflux.Classify(operationClient(model, recordingTo(&recorder)), "x", classifyCategories, schemaflux.ClassifyOptions{MinConfidence: 3})
		r := onlyRecord(t, &recorder)
		wantError(t, r.Err, schemaflux.ErrConfiguration, "classify")
		if r.Op != "classify" || r.Err != err || r.Requests != 0 {
			t.Errorf("record = %q err %v requests %d", r.Op, r.Err, r.Requests)
		}
	})
	t.Run("a recorder on the context and one on the client each get the Record", func(t *testing.T) {
		t.Parallel()
		var onClient, onContext schemaflux.Recorder
		ctx := schemaflux.WithRecorder(operationClient(answeringWith(classifiedBillingAnswer), recordingTo(&onClient)), &onContext)
		if _, err := schemaflux.Classify(ctx, "x", classifyCategories); err != nil {
			t.Fatal(err)
		}
		if onlyRecord(t, &onClient).Op != "classify" || onlyRecord(t, &onContext).Op != "classify" {
			t.Error("a recorder was not given the Record")
		}
	})
}

func TestClassifyUnderAFloorIsHeldToTheLevelItEstablishes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		floor   schemaflux.ContractLevel
		refused bool
	}{
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
				model := namedModel(classifiedBillingAnswer)
				settings := []func(*schemaflux.Client){underFloor(tc.floor)}
				if withRecorder {
					settings = append(settings, recordingTo(&recorder))
				}
				res, err := schemaflux.Classify(operationClient(model, settings...), "x", classifyCategories)
				if tc.refused {
					wantError(t, err, schemaflux.ErrPolicy, "classify")
					if model.Calls() != 0 || !equalClassify(res, schemaflux.ClassifyResult[string]{}) {
						t.Errorf("%d requests were sent, result %+v", model.Calls(), res)
					}
				} else if err != nil || res.Category != classifiedBilling {
					t.Fatalf("got %+v, %v", res, err)
				}
				if withRecorder {
					r := onlyRecord(t, &recorder)
					if r.Op != "classify" || (r.Err != nil) != tc.refused || r.Requested != schemaflux.ContractJSONWellFormed {
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

func TestClassifyUnderAnObserverSendsTheSameRequestAndRecordsOnce(t *testing.T) {
	// Not parallel: an observer is process state.
	var seen []string
	restore := telemetry.RegisterObserver(operationWatcher{finished: func(r telemetry.Result) {
		if r.Scope == telemetry.ScopeOperation {
			seen = append(seen, r.Operation)
		}
	}})
	defer restore()

	plain := answeringWith(classifiedBillingAnswer)
	if _, err := schemaflux.Classify(operationClient(plain), "x", classifyCategories); err != nil {
		t.Fatal(err)
	}
	var recorder schemaflux.Recorder
	watched := answeringWith(classifiedBillingAnswer)
	res, err := schemaflux.Classify(operationClient(watched, recordingTo(&recorder)), "x", classifyCategories)
	if err != nil || res.Category != classifiedBilling {
		t.Fatalf("got %+v, %v", res, err)
	}
	if requestText(plain.Last()) != requestText(watched.Last()) {
		t.Error("an observer changed what the model is asked")
	}
	if onlyRecord(t, &recorder).Op != "classify" {
		t.Error("the Record under an observer has the wrong operation")
	}
	if len(seen) != 2 || seen[0] != "classify" || seen[1] != "classify" {
		t.Errorf("the observer saw %v, want one classify for each call", seen)
	}
}

func TestClassifyKeepsThePayloadOutOfEverythingItLeavesBehind(t *testing.T) {
	t.Parallel()
	const canary = "CANARY-9d2b4f"
	opts := schemaflux.ClassifyOptions{
		Options:       schemaflux.Options{Steering: "steer " + canary},
		Descriptions:  map[string]string{"billing": "describes " + canary},
		Examples:      map[string][]string{"technical": {"example " + canary}},
		MinConfidence: 0.5,
	}
	categories := []string{"billing", "technical", canary + "-label"}
	input := map[string]string{canary + "-key": "text " + canary}
	run := func(ctx context.Context) error {
		_, err := schemaflux.Classify(ctx, input, categories, opts)
		return err
	}

	t.Run("a call that succeeds", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(classifiedBillingAnswer) }, true, run)
	})
	t.Run("an answer that is not one of the categories", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) {
			p.Reply(`{"category":"` + canary + `","confidence":0.9,"alternatives":[],"reasoning":"` + canary + `"}`)
		}, true, run)
	})
	t.Run("an answer the model is unsure of", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) {
			p.Reply(`{"category":"billing","confidence":0.1,"alternatives":[],"reasoning":"` + canary + `"}`)
		}, true, run)
	})
	t.Run("an answer that is text", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("the category is " + canary) }, true, run)
	})
	t.Run("a provider that fails with a vendor message beneath its error", func(t *testing.T) {
		t.Parallel()
		vendor := &schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "the provider is down", Err: errors.New("vendor said " + canary)}
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Fail(vendor) }, false, run)
	})
	t.Run("a refused option", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(classifiedBillingAnswer) }, true,
			func(ctx context.Context) error {
				bad := opts
				bad.MinConfidence = 9
				_, err := schemaflux.Classify(ctx, input, categories, bad)
				return err
			})
	})
	t.Run("no categories", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(classifiedBillingAnswer) }, true,
			func(ctx context.Context) error {
				_, err := schemaflux.Classify(ctx, input, []string(nil), opts)
				return err
			})
	})
}
