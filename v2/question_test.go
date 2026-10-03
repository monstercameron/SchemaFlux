package schemaflux_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// salesReport is the value the Question tests ask about.
type salesReport struct {
	Region  string  `json:"region"`
	Revenue float64 `json:"revenue"`
}

// riskList is an answer type that is a struct.
type riskList struct {
	Risks []string `json:"risks"`
}

const (
	questionAsked  = "What is the main finding?"
	questionAnswer = `{"answer":"Revenue grew","confidence":0.9,"reasoning":"the report says so","evidence":["revenue up 12%"]}`
)

func TestQuestionReturnsTheAnswerWithItsConfidenceReasoningAndEvidence(t *testing.T) {
	t.Parallel()
	model := answeringWith(questionAnswer)
	got, err := schemaflux.Question[string](operationClient(model), salesReport{Region: "EU", Revenue: 12}, questionAsked)
	if err != nil {
		t.Fatal(err)
	}
	if got.Answer != "Revenue grew" || got.ModelConfidence != 0.9 || got.Reasoning != "the report says so" ||
		len(got.Evidence) != 1 || got.Evidence[0] != "revenue up 12%" {
		t.Errorf("got %+v", got)
	}
	if model.Calls() != 1 {
		t.Fatalf("%d requests were sent, want 1", model.Calls())
	}
	wantText(t, model.Last(), `Question: "What is the main finding?"`)
	wantText(t, model.Last(), `"region":"EU"`)
}

func TestQuestionTakesTheAnswerTypeFirstAndInfersTheValuesType(t *testing.T) {
	t.Parallel()
	t.Run("a number", func(t *testing.T) {
		t.Parallel()
		got, err := schemaflux.Question[float64](operationClient(answeringWith(`{"answer":112.5,"confidence":0.8,"reasoning":"r","evidence":[]}`)), salesReport{}, "What is the total with tax?")
		if err != nil || got.Answer != 112.5 || len(got.Evidence) != 0 || got.Evidence == nil {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
	t.Run("a boolean", func(t *testing.T) {
		t.Parallel()
		got, err := schemaflux.Question[bool](operationClient(answeringWith(`{"answer":true,"confidence":0.8,"reasoning":"r","evidence":["e"]}`)), "a document", "Is there PII?")
		if err != nil || !got.Answer {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
	t.Run("a struct", func(t *testing.T) {
		t.Parallel()
		got, err := schemaflux.Question[riskList](operationClient(answeringWith(`{"answer":{"risks":["a","b"]},"confidence":0.8,"reasoning":"r","evidence":["e"]}`)), salesReport{}, "What are the risks?")
		if err != nil || len(got.Answer.Risks) != 2 {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
	t.Run("a number given in text is read as text for a string answer", func(t *testing.T) {
		t.Parallel()
		got, err := schemaflux.Question[string](operationClient(answeringWith(`{"answer":42,"confidence":0.8,"reasoning":"r","evidence":["e"]}`)), 7, "How many?")
		if err != nil || got.Answer != "42" {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
}

func TestQuestionWithNoOptionsAsksForEverythingOnTheFastTierInTransformMode(t *testing.T) {
	t.Parallel()
	model := answeringWith(questionAnswer)
	if _, err := schemaflux.Question[string](operationClient(model), salesReport{}, questionAsked); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	if math.Abs(req.Temperature-0.3) > 1e-6 || req.MaxTokens != 2000 || req.Model != "test-model" {
		t.Errorf("temperature %v, ceiling %d, model %q: want 0.3, 2000, test-model", req.Temperature, req.MaxTokens, req.Model)
	}
	wantText(t, req, `"confidence": 0.0-1.0`)
	wantText(t, req, `"reasoning": "explanation`)
	wantText(t, req, `"evidence": [`)
	if req.Format != "json" || req.SchemaName != "question_answer" || req.Schema == nil {
		t.Errorf("request = format %q schema name %q schema %v, want a declared envelope schema", req.Format, req.SchemaName, req.Schema != nil)
	}
}

func TestQuestionEachOptionFieldChangesTheRequestAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  schemaflux.QuestionOptions
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"OmitEvidence", schemaflux.QuestionOptions{OmitEvidence: true}, func(t *testing.T, req schemaflux.Request) {
			wantNoText(t, req, `"evidence"`)
			wantText(t, req, `"confidence"`)
			wantText(t, req, `"reasoning"`)
			if _, in := req.Schema["properties"].(map[string]any)["evidence"]; in {
				t.Error("the declared schema still has evidence")
			}
		}},
		{"OmitConfidence", schemaflux.QuestionOptions{OmitConfidence: true}, func(t *testing.T, req schemaflux.Request) {
			wantNoText(t, req, `"confidence"`)
			wantText(t, req, `"evidence"`)
			wantText(t, req, `"reasoning"`)
			if _, in := req.Schema["properties"].(map[string]any)["confidence"]; in {
				t.Error("the declared schema still has confidence")
			}
		}},
		{"OmitReasoning", schemaflux.QuestionOptions{OmitReasoning: true}, func(t *testing.T, req schemaflux.Request) {
			wantNoText(t, req, `"reasoning"`)
			wantText(t, req, `"evidence"`)
			wantText(t, req, `"confidence"`)
			if _, in := req.Schema["properties"].(map[string]any)["reasoning"]; in {
				t.Error("the declared schema still has reasoning")
			}
		}},
		{"all three omitted", schemaflux.QuestionOptions{OmitEvidence: true, OmitConfidence: true, OmitReasoning: true}, func(t *testing.T, req schemaflux.Request) {
			for _, absent := range []string{`"evidence"`, `"confidence"`, `"reasoning"`} {
				wantNoText(t, req, absent)
			}
		}},
		{"Steering", schemaflux.QuestionOptions{Options: schemaflux.Options{Steering: "answer in one word"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "answer in one word")
		}},
		{"Model", schemaflux.QuestionOptions{Options: schemaflux.Options{Model: "pinned-model"}}, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"ModeStrict", schemaflux.QuestionOptions{Options: schemaflux.Options{Mode: schemaflux.ModeStrict}}, func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.1) > 1e-6 {
				t.Errorf("temperature = %v, want 0.1", req.Temperature)
			}
		}},
		{"TierSmart", schemaflux.QuestionOptions{Options: schemaflux.Options{Tier: schemaflux.TierSmart}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 4000 {
				t.Errorf("ceiling = %d, want the smart tier's 4000", req.MaxTokens)
			}
		}},
		{"MaxOutputTokens", schemaflux.QuestionOptions{Options: schemaflux.Options{MaxOutputTokens: 77}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 77 {
				t.Errorf("ceiling = %d, want 77", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.QuestionOptions{Options: schemaflux.Options{WebSearch: true}}, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("the request does not declare web search")
			}
		}},
		{"Effort", schemaflux.QuestionOptions{Options: schemaflux.Options{Effort: schemaflux.EffortLow}}, func(t *testing.T, req schemaflux.Request) {
			if req.Effort != "low" || req.EffortPreferred {
				t.Errorf("effort = %q preferred %v, want low, asked for by the caller", req.Effort, req.EffortPreferred)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(`{"answer":"x","confidence":0.5,"reasoning":"r","evidence":["e"]}`)
			opts := tc.opts
			// An answer carrying every member is read whatever was asked for.
			if _, err := schemaflux.Question[string](operationClient(model), salesReport{}, questionAsked, opts); err != nil {
				t.Fatal(err)
			}
			tc.check(t, model.Last())
		})
	}
}

func TestQuestionReturnsOnlyWhatWasAskedFor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		opts schemaflux.QuestionOptions
		want schemaflux.QuestionResult[string]
	}{
		{"all of it", schemaflux.QuestionOptions{}, schemaflux.QuestionResult[string]{Answer: "x", ModelConfidence: 0.5, Reasoning: "r", Evidence: []string{"e"}}},
		{"no evidence", schemaflux.QuestionOptions{OmitEvidence: true}, schemaflux.QuestionResult[string]{Answer: "x", ModelConfidence: 0.5, Reasoning: "r"}},
		{"no confidence", schemaflux.QuestionOptions{OmitConfidence: true}, schemaflux.QuestionResult[string]{Answer: "x", Reasoning: "r", Evidence: []string{"e"}}},
		{"no reasoning", schemaflux.QuestionOptions{OmitReasoning: true}, schemaflux.QuestionResult[string]{Answer: "x", ModelConfidence: 0.5, Evidence: []string{"e"}}},
		{"only the answer", schemaflux.QuestionOptions{OmitEvidence: true, OmitConfidence: true, OmitReasoning: true}, schemaflux.QuestionResult[string]{Answer: "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// The model wrote every member whatever it was asked for.
			got, err := schemaflux.Question[string](operationClient(answeringWith(`{"answer":"x","confidence":0.5,"reasoning":"r","evidence":["e"]}`)), 1, questionAsked, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if got.Answer != tc.want.Answer || got.ModelConfidence != tc.want.ModelConfidence || got.Reasoning != tc.want.Reasoning ||
				strings.Join(got.Evidence, "|") != strings.Join(tc.want.Evidence, "|") || (got.Evidence == nil) != (tc.want.Evidence == nil) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestQuestionRefusesWhatCannotBeSentBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		question string
		opts     schemaflux.QuestionOptions
		kind     schemaflux.Kind
	}{
		{"an empty question", "", schemaflux.QuestionOptions{}, schemaflux.ErrInvalidRequest},
		{"a question of only space", " \n\t", schemaflux.QuestionOptions{}, schemaflux.ErrInvalidRequest},
		{"a negative output ceiling", questionAsked, schemaflux.QuestionOptions{Options: schemaflux.Options{MaxOutputTokens: -1}}, schemaflux.ErrConfiguration},
		{"a tier that is not one", questionAsked, schemaflux.QuestionOptions{Options: schemaflux.Options{Tier: 6}}, schemaflux.ErrConfiguration},
		{"a mode that is not one", questionAsked, schemaflux.QuestionOptions{Options: schemaflux.Options{Mode: 6}}, schemaflux.ErrConfiguration},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(questionAnswer)
			got, err := schemaflux.Question[string](operationClient(model), salesReport{}, tc.question, tc.opts)
			wantError(t, err, tc.kind, "question")
			if got.Answer != "" || got.Evidence != nil || model.Calls() != 0 {
				t.Errorf("got %+v after %d requests", got, model.Calls())
			}
		})
	}
	t.Run("data that cannot be written as JSON", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(questionAnswer)
		_, err := schemaflux.Question[string](operationClient(model), make(chan int), questionAsked)
		wantError(t, err, schemaflux.ErrInvalidRequest, "question")
		if model.Calls() != 0 {
			t.Errorf("%d requests were sent", model.Calls())
		}
	})
}

func TestQuestionMergesSeveralOptionsValuesFromLeftToRight(t *testing.T) {
	t.Parallel()
	t.Run("a later value that is not zero wins and a later zero leaves the earlier alone", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(questionAnswer)
		first := schemaflux.QuestionOptions{OmitEvidence: true, Options: schemaflux.Options{Model: "first", MaxOutputTokens: 11}}
		second := schemaflux.QuestionOptions{OmitReasoning: true, Options: schemaflux.Options{Model: "second"}}
		if _, err := schemaflux.Question[string](operationClient(model), 1, questionAsked, first, second); err != nil {
			t.Fatal(err)
		}
		req := model.Last()
		if req.Model != "second" || req.MaxTokens != 11 {
			t.Errorf("model %q ceiling %d, want second and 11", req.Model, req.MaxTokens)
		}
		wantNoText(t, req, `"evidence"`)
		wantNoText(t, req, `"reasoning"`)
		wantText(t, req, `"confidence"`)
	})
	t.Run("a bool set by an earlier value is not cleared by a later one", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(questionAnswer)
		_, err := schemaflux.Question[string](operationClient(model), 1, questionAsked,
			schemaflux.QuestionOptions{OmitConfidence: true}, schemaflux.QuestionOptions{OmitConfidence: false})
		if err != nil {
			t.Fatal(err)
		}
		wantNoText(t, model.Last(), `"confidence"`)
	})
	t.Run("a bad setting in any value is refused", func(t *testing.T) {
		t.Parallel()
		_, err := schemaflux.Question[string](operationClient(answeringWith(questionAnswer)), 1, questionAsked,
			schemaflux.QuestionOptions{}, schemaflux.QuestionOptions{Options: schemaflux.Options{MaxOutputTokens: -3}})
		wantError(t, err, schemaflux.ErrConfiguration, "question")
	})
}

func TestQuestionRefusesAnAnswerThatDoesNotReportWhatWasAsked(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		opts   schemaflux.QuestionOptions
		answer string
	}{
		{"it is not JSON", schemaflux.QuestionOptions{}, "Revenue grew."},
		{"it is empty", schemaflux.QuestionOptions{}, ""},
		{"it has no answer", schemaflux.QuestionOptions{}, `{"confidence":0.9,"reasoning":"r","evidence":["e"]}`},
		{"its answer is null", schemaflux.QuestionOptions{}, `{"answer":null,"confidence":0.9,"reasoning":"r","evidence":["e"]}`},
		{"its answer is empty text", schemaflux.QuestionOptions{}, `{"answer":"","confidence":0.9,"reasoning":"r","evidence":["e"]}`},
		{"the confidence is missing and was asked for", schemaflux.QuestionOptions{}, `{"answer":"x","reasoning":"r","evidence":["e"]}`},
		{"the reasoning is missing and was asked for", schemaflux.QuestionOptions{}, `{"answer":"x","confidence":0.5,"evidence":["e"]}`},
		{"the evidence is missing and was asked for", schemaflux.QuestionOptions{}, `{"answer":"x","confidence":0.5,"reasoning":"r"}`},
		{"the confidence is above the scale", schemaflux.QuestionOptions{}, `{"answer":"x","confidence":1.4,"reasoning":"r","evidence":["e"]}`},
		{"the confidence is below the scale", schemaflux.QuestionOptions{}, `{"answer":"x","confidence":-0.4,"reasoning":"r","evidence":["e"]}`},
		{"the answer is an object where text was asked for", schemaflux.QuestionOptions{}, `{"answer":{"a":1},"confidence":0.5,"reasoning":"r","evidence":["e"]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.Question[string](operationClient(answeringWith(tc.answer)), 1, questionAsked, tc.opts)
			e := wantError(t, err, schemaflux.ErrInvalidAnswer, "question")
			if got.Answer != "" || got.ModelConfidence != 0 || got.Reasoning != "" || got.Evidence != nil {
				t.Errorf("a refused answer left %+v beside the error", got)
			}
			if strings.Contains(e.Error(), "Revenue grew") {
				t.Errorf("the error repeats the model's text: %q", e.Error())
			}
		})
	}
	t.Run("an answer of the wrong type for a number", func(t *testing.T) {
		t.Parallel()
		got, err := schemaflux.Question[float64](operationClient(answeringWith(`{"answer":"many","confidence":0.5,"reasoning":"r","evidence":["e"]}`)), 1, questionAsked)
		wantError(t, err, schemaflux.ErrInvalidAnswer, "question")
		if got.Answer != 0 {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("a missing member is not asked for and so is not required", func(t *testing.T) {
		t.Parallel()
		got, err := schemaflux.Question[string](operationClient(answeringWith(`{"answer":"x"}`)), 1, questionAsked,
			schemaflux.QuestionOptions{OmitEvidence: true, OmitConfidence: true, OmitReasoning: true})
		if err != nil || got.Answer != "x" {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
}

func TestQuestionReturnsAnErrorTheCallerCanClassifyWhenTheProviderFails(t *testing.T) {
	t.Parallel()
	for name, kind := range map[string]schemaflux.Kind{
		"unavailable":  schemaflux.ErrUnavailable,
		"rate limited": schemaflux.ErrRateLimited,
		"unauthorized": schemaflux.ErrUnauthorized,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var model schemafluxtest.Provider
			model.Fail(&schemaflux.Error{Kind: kind})
			_, err := schemaflux.Question[string](operationClient(&model), 1, questionAsked)
			wantError(t, err, kind, "question")
		})
	}
}

func TestQuestionWithACancelledContextReturnsCanceledAndSendsNothing(t *testing.T) {
	t.Parallel()
	model := answeringWith(questionAnswer)
	ctx, cancel := context.WithCancel(operationClient(model))
	cancel()
	_, err := schemaflux.Question[string](ctx, 1, questionAsked)
	wantError(t, err, schemaflux.ErrCanceled, "question")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the context's own error is not reachable: %v", err)
	}
	if model.Calls() != 0 {
		t.Errorf("%d requests were sent", model.Calls())
	}
}

func TestQuestionResultCarriesTheJSONTagsOfTheInternalResult(t *testing.T) {
	t.Parallel()
	got, err := schemaflux.Question[string](operationClient(answeringWith(questionAnswer)), 1, questionAsked)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := marshaledText(got)
	want := `{"answer":"Revenue grew","confidence":0.9,"reasoning":"the report says so","evidence":["revenue up 12%"]}`
	if err != nil || encoded != want {
		t.Errorf("encoded = %s, %v", encoded, err)
	}
}

func TestQuestionKeepsThePayloadOutOfEverythingItLeavesBehind(t *testing.T) {
	t.Parallel()
	const canary = "CANARY-b3d75a"
	type canaryAnswer struct {
		Value string `json:"value" desc:"CANARY-b3d75a-tag"`
	}
	opts := schemaflux.QuestionOptions{Options: schemaflux.Options{Steering: "steer " + canary}}
	data := map[string]string{canary + "-key": "text " + canary}
	question := "question " + canary
	call := func(question string, o schemaflux.QuestionOptions) func(ctx context.Context) error {
		return func(ctx context.Context) error {
			_, err := schemaflux.Question[canaryAnswer](ctx, data, question, o)
			return err
		}
	}

	t.Run("a call that succeeds", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) {
			p.Reply(`{"answer":{"value":"fine"},"confidence":0.5,"reasoning":"r","evidence":["e"]}`)
		}, true, call(question, opts))
	})
	t.Run("an answer that is refused", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("the answer is " + canary + " not json") }, true, call(question, opts))
	})
	t.Run("an answer of the wrong type that quotes the data", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) {
			p.Reply(`{"answer":{"value":["` + canary + `"]},"confidence":0.5,"reasoning":"` + canary + `","evidence":["` + canary + `"]}`)
		}, true, call(question, opts))
	})
	t.Run("an answer that is missing what was asked for", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(`{"answer":{"value":"` + canary + `"}}`) }, true, call(question, opts))
	})
	t.Run("a provider that fails with a vendor message beneath its error", func(t *testing.T) {
		t.Parallel()
		vendor := &schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "the provider is down", Err: errors.New("vendor said " + canary)}
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Fail(vendor) }, false, call(question, opts))
	})
	t.Run("a blank question next to a canary in the options", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("{}") }, true, call("  ", opts))
	})
	t.Run("a refused option", func(t *testing.T) {
		t.Parallel()
		bad := opts
		bad.MaxOutputTokens = -1
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("{}") }, true, call(question, bad))
	})
}

func TestQuestionIntoAMapIsDeliveredAsWellFormedAndHeldToThatLevelByAFloor(t *testing.T) {
	t.Parallel()
	report := salesReport{Region: "EU", Revenue: 12}
	wantLevelFollowsTheType(t, levelCall{
		op:          "question",
		mapReply:    `{"answer":{"region":"EU"},"confidence":0.9,"reasoning":"r","evidence":["e"]}`,
		structReply: `{"answer":{"risks":["churn"]},"confidence":0.9,"reasoning":"r","evidence":["e"]}`,
		intoMap: func(ctx context.Context) error {
			got, err := schemaflux.Question[map[string]string](ctx, report, questionAsked)
			if err == nil && got.Answer["region"] != "EU" {
				t.Errorf("got %+v", got)
			}
			return err
		},
		intoStruct: func(ctx context.Context) error {
			_, err := schemaflux.Question[riskList](ctx, report, questionAsked)
			return err
		},
	})
	t.Run("a string answer, as the default, is schema constrained", func(t *testing.T) {
		var recorder schemaflux.Recorder
		if _, err := schemaflux.Question[string](operationClient(namedModel(questionAnswer), recordingTo(&recorder), underFloor(schemaflux.ContractSchemaConstrained)), report, questionAsked); err != nil {
			t.Fatal(err)
		}
		if r := onlyRecord(t, &recorder); r.Delivered != schemaflux.ContractSchemaConstrained {
			t.Errorf("delivered %v", r.Delivered)
		}
	})
}
