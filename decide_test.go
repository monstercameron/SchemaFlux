package schemaflux_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/mw"
	"github.com/monstercameron/schemaflux/v2/provider"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

const (
	isYes       = `{"q1":true}`
	isNo        = `{"q1":false}`
	isYesSure   = `{"q1":true,"confidence":0.8}`
	isRefundAsk = "Is the customer asking for a refund?"
	isRefundMsg = "Please refund my order."
)

func TestIsAnswersTrueAndFalseAndSendsTheQuestionAndTheSubject(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		body string
		want bool
	}{{isYes, true}, {isNo, false}} {
		model := answeringWith(tc.body)
		got, err := schemaflux.Is(operationClient(model), isRefundMsg, isRefundAsk)
		if err != nil || got != tc.want {
			t.Fatalf("Is = %v, %v, want %v", got, err, tc.want)
		}
		req := model.Last()
		wantText(t, req, isRefundAsk)
		wantText(t, req, isRefundMsg)
		if model.Calls() != 1 || req.Schema == nil || req.SchemaName != "assessment" || req.Format != "json" {
			t.Errorf("%d requests, schema %v name %q format %q: want one request declaring the assessment schema", model.Calls(), req.Schema != nil, req.SchemaName, req.Format)
		}
	}
}

func TestIsWithNoOptionsAsksForNoReasoningAtTheFastTier(t *testing.T) {
	t.Parallel()
	model := answeringWith(isYes)
	if _, err := schemaflux.Is(operationClient(model), isRefundMsg, isRefundAsk); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	if req.Effort != "none" || !req.EffortPreferred || req.MaxTokens != 2000 || req.Model != "test-model" || req.WebSearch {
		t.Errorf("effort %q preferred %v ceiling %d model %q web %v: want a preference for none, the fast tier's ceiling and no web search",
			req.Effort, req.EffortPreferred, req.MaxTokens, req.Model, req.WebSearch)
	}
}

func TestIsEachOptionChangesTheRequestAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  schemaflux.Options
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"Model", schemaflux.Options{Model: "pinned-model"}, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"Steering", schemaflux.Options{Steering: "refunds only"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "refunds only")
		}},
		{"TierSmart", schemaflux.Options{Tier: schemaflux.TierSmart}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 4000 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"TierQuick", schemaflux.Options{Tier: schemaflux.TierQuick}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 1000 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"MaxOutputTokens", schemaflux.Options{MaxOutputTokens: 31}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 31 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.Options{WebSearch: true}, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("no web search")
			}
		}},
		{"Effort replaces the preference with the caller's requirement", schemaflux.Options{Effort: schemaflux.EffortMedium}, func(t *testing.T, req schemaflux.Request) {
			if req.Effort != "medium" || req.EffortPreferred {
				t.Errorf("effort %q preferred %v", req.Effort, req.EffortPreferred)
			}
		}},
		{"ModeStrict", schemaflux.Options{Mode: schemaflux.ModeStrict}, func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.1) > 1e-6 {
				t.Errorf("temperature = %v", req.Temperature)
			}
		}},
		{"ModeCreative", schemaflux.Options{Mode: schemaflux.ModeCreative}, func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.7) > 1e-6 {
				t.Errorf("temperature = %v", req.Temperature)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(isYes)
			if _, err := schemaflux.Is(operationClient(model), isRefundMsg, isRefundAsk, tc.opts); err != nil {
				t.Fatal(err)
			}
			tc.check(t, model.Last())
		})
	}
}

func TestIsRefusesAnAnswerThatIsNotAPlainTrueOrFalse(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"a string":                    `{"q1":"yes"}`,
		"a number":                    `{"q1":1}`,
		"null":                        `{"q1":null}`,
		"no answer":                   `{}`,
		"an answer under another key": `{"answer":true}`,
		"a list":                      `[true]`,
		"text":                        `Yes.`,
		"empty":                       ``,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.Is(operationClient(answeringWith(body)), isRefundMsg, isRefundAsk)
			wantError(t, err, schemaflux.ErrInvalidAnswer, "is")
			if got {
				t.Error("a refused answer was read as a yes")
			}
		})
	}
}

func TestIsRefusesABlankQuestionBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	for name, question := range map[string]string{"empty": "", "a space": " ", "whitespace": " \t\n ", "a no-break space": " "} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(isYes)
			_, err := schemaflux.Is(operationClient(model), isRefundMsg, question)
			e := wantError(t, err, schemaflux.ErrInvalidRequest, "is")
			if e.Message != "question is blank" || model.Calls() != 0 {
				t.Errorf("message %q after %d requests", e.Message, model.Calls())
			}
		})
	}
}

func TestIsRefusesABadOptionBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	cases := map[string]schemaflux.Options{
		"a negative ceiling":       {MaxOutputTokens: -1},
		"a tier that is not one":   {Tier: 7},
		"a mode that is not one":   {Mode: 7},
		"a bad setting among good": {Model: "m", Steering: "s", MaxOutputTokens: -3},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(isYes)
			_, err := schemaflux.Is(operationClient(model), isRefundMsg, isRefundAsk, opts)
			wantError(t, err, schemaflux.ErrConfiguration, "is")
			if model.Calls() != 0 {
				t.Errorf("%d requests were sent", model.Calls())
			}
		})
	}
}

func TestIsWithANilContextIsAnInvalidRequest(t *testing.T) {
	t.Parallel()
	var nilContext context.Context
	_, err := schemaflux.Is(nilContext, isRefundMsg, isRefundAsk)
	if e := wantError(t, err, schemaflux.ErrInvalidRequest, "is"); e.Message != "the context is nil" {
		t.Errorf("message = %q", e.Message)
	}
}

func TestIsMergesSeveralOptionsValuesFromLeftToRight(t *testing.T) {
	t.Parallel()
	t.Run("a later value wins and a later zero leaves the earlier", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(isYes)
		_, err := schemaflux.Is(operationClient(model), isRefundMsg, isRefundAsk,
			schemaflux.Options{Model: "first", Steering: "keep this", MaxOutputTokens: 9},
			schemaflux.Options{Model: "second"},
			schemaflux.Options{})
		if err != nil {
			t.Fatal(err)
		}
		req := model.Last()
		if req.Model != "second" || req.MaxTokens != 9 {
			t.Errorf("model %q ceiling %d", req.Model, req.MaxTokens)
		}
		wantText(t, req, "keep this")
	})
	t.Run("a bool turned on stays on", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(isYes)
		_, err := schemaflux.Is(operationClient(model), isRefundMsg, isRefundAsk, schemaflux.Options{WebSearch: true}, schemaflux.Options{})
		if err != nil || !model.Last().WebSearch {
			t.Errorf("web search %v, err %v", model.Last().WebSearch, err)
		}
	})
	t.Run("a bad setting in a later value is refused", func(t *testing.T) {
		t.Parallel()
		_, err := schemaflux.Is(operationClient(answeringWith(isYes)), isRefundMsg, isRefundAsk, schemaflux.Options{}, schemaflux.Options{MaxOutputTokens: -1})
		wantError(t, err, schemaflux.ErrConfiguration, "is")
	})
}

func TestIsReadsAnyKindOfSubject(t *testing.T) {
	t.Parallel()
	type message struct {
		Body string `json:"body"`
	}
	cases := map[string]struct {
		subject any
		seen    string
	}{
		"text":      {"a refund please", "a refund please"},
		"a struct":  {message{Body: "refund me now"}, "refund me now"},
		"a map":     {map[string]int{"orders": 3}, "orders"},
		"a slice":   {[]string{"one", "two"}, "two"},
		"a pointer": {&message{Body: "by pointer"}, "by pointer"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(isYes)
			if _, err := schemaflux.Is(operationClient(model), tc.subject, isRefundAsk); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(model.Last().User, tc.seen) {
				t.Errorf("%q is not in the user message: %s", tc.seen, excerpt(model.Last().User))
			}
		})
	}
}

func TestIsReturnsAnErrorTheCallerCanClassifyWhenTheProviderFails(t *testing.T) {
	t.Parallel()
	for name, kind := range map[string]schemaflux.Kind{
		"unavailable":  schemaflux.ErrUnavailable,
		"rate limited": schemaflux.ErrRateLimited,
		"unauthorized": schemaflux.ErrUnauthorized,
		"timed out":    schemaflux.ErrTimeout,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var model schemafluxtest.Provider
			model.Fail(&schemaflux.Error{Kind: kind})
			got, err := schemaflux.Is(operationClient(&model), isRefundMsg, isRefundAsk)
			wantError(t, err, kind, "is")
			if got {
				t.Error("a failed call returned true")
			}
		})
	}
}

func TestIsWithACancelledContextReturnsCanceledAndSendsNothing(t *testing.T) {
	t.Parallel()
	model := answeringWith(isYes)
	ctx, cancel := context.WithCancel(operationClient(model))
	cancel()
	_, err := schemaflux.Is(ctx, isRefundMsg, isRefundAsk)
	wantError(t, err, schemaflux.ErrCanceled, "is")
	if !errors.Is(err, context.Canceled) || model.Calls() != 0 {
		t.Errorf("err %v after %d requests", err, model.Calls())
	}
}

func TestIsSendsTheSameRequestWhateverRecordsTheCall(t *testing.T) {
	t.Parallel()
	plain := answeringWith(isYes)
	if _, err := schemaflux.Is(operationClient(plain), isRefundMsg, isRefundAsk); err != nil {
		t.Fatal(err)
	}
	var recorder schemaflux.Recorder
	recorded := answeringWith(isYes)
	if _, err := schemaflux.Is(operationClient(recorded, recordingTo(&recorder)), isRefundMsg, isRefundAsk); err != nil {
		t.Fatal(err)
	}
	if requestText(plain.Last()) != requestText(recorded.Last()) || plain.Last().SchemaName != recorded.Last().SchemaName {
		t.Error("a recorder changed what the model is asked")
	}
	wantNoText(t, recorded.Last(), "confidence")
	if onlyRecord(t, &recorder).Op != "is" {
		t.Error("the Record has the wrong operation")
	}
}

func TestIsAddsOneRecordForEachCallWithTheOperationsName(t *testing.T) {
	t.Parallel()
	t.Run("a call that succeeds", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		if _, err := schemaflux.Is(operationClient(answeringWith(isYes), recordingTo(&recorder)), isRefundMsg, isRefundAsk); err != nil {
			t.Fatal(err)
		}
		r := onlyRecord(t, &recorder)
		if r.Op != "is" || r.Err != nil || r.Requests != 1 || r.Provider != "test" || r.AnsweredBy != "test" || r.Typed {
			t.Errorf("record = %q err %v requests %d provider %q answered by %q typed %v", r.Op, r.Err, r.Requests, r.Provider, r.AnsweredBy, r.Typed)
		}
		if r.Requested != schemaflux.ContractSchemaAndInvariantChecked || r.Delivered != schemaflux.ContractSchemaAndInvariantChecked {
			t.Errorf("levels = %v and %v", r.Requested, r.Delivered)
		}
		if len(r.Asked) != 0 {
			t.Errorf("a recorder that did not ask for decisions was given %+v", r.Asked)
		}
	})
	t.Run("a recorder that asks for decisions is told what was asked and answered", func(t *testing.T) {
		t.Parallel()
		recorder := schemaflux.Recorder{Decisions: true}
		if _, err := schemaflux.Is(operationClient(answeringWith(isYes), recordingTo(&recorder)), isRefundMsg, isRefundAsk); err != nil {
			t.Fatal(err)
		}
		r := onlyRecord(t, &recorder)
		if len(r.Asked) != 1 || r.Asked[0].Question != isRefundAsk || r.Asked[0].Answer != true {
			t.Errorf("asked = %+v", r.Asked)
		}
	})
	t.Run("a call that fails on its answer", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		_, err := schemaflux.Is(operationClient(answeringWith("Yes."), recordingTo(&recorder)), isRefundMsg, isRefundAsk)
		wantError(t, err, schemaflux.ErrInvalidAnswer, "is")
		r := onlyRecord(t, &recorder)
		if r.Op != "is" || r.Err != err || r.Delivered != schemaflux.ContractPromptOnly {
			t.Errorf("record = %q err %v delivered %v", r.Op, r.Err, r.Delivered)
		}
	})
	t.Run("a call the provider fails", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		var model schemafluxtest.Provider
		model.Fail(&schemaflux.Error{Kind: schemaflux.ErrRateLimited})
		_, err := schemaflux.Is(operationClient(&model, recordingTo(&recorder)), isRefundMsg, isRefundAsk)
		r := onlyRecord(t, &recorder)
		wantError(t, r.Err, schemaflux.ErrRateLimited, "is")
		if r.Op != "is" || r.Err != err {
			t.Errorf("record = %q err %v", r.Op, r.Err)
		}
	})
	t.Run("a call refused before anything is sent: a blank question", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		model := answeringWith(isYes)
		_, err := schemaflux.Is(operationClient(model, recordingTo(&recorder)), isRefundMsg, " ")
		r := onlyRecord(t, &recorder)
		wantError(t, r.Err, schemaflux.ErrInvalidRequest, "is")
		if r.Op != "is" || r.Err != err || r.Requests != 0 || model.Calls() != 0 {
			t.Errorf("record = %q err %v requests %d", r.Op, r.Err, r.Requests)
		}
	})
	t.Run("a call refused before anything is sent: a bad option", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		_, err := schemaflux.Is(operationClient(answeringWith(isYes), recordingTo(&recorder)), isRefundMsg, isRefundAsk, schemaflux.Options{MaxOutputTokens: -1})
		r := onlyRecord(t, &recorder)
		wantError(t, r.Err, schemaflux.ErrConfiguration, "is")
		if r.Op != "is" || r.Err != err || r.Requests != 0 {
			t.Errorf("record = %q err %v requests %d", r.Op, r.Err, r.Requests)
		}
	})
}

// decidingPair is a generating model and a decision model put together with
// mw.Decisions, and the client's recorder.
func decidingPair(options mw.DecisionOptions, generator *schemafluxtest.Provider, decider *schemafluxtest.Decider) (context.Context, *schemaflux.Recorder) {
	recorder := &schemaflux.Recorder{Decisions: true}
	return operationClient(mw.Decisions(generator, decider, options), recordingTo(recorder)), recorder
}

func TestIsGoesThroughTheDecisionProviderWhenOneIsInFrontAndTheRecordSaysWhoAnswered(t *testing.T) {
	t.Parallel()
	t.Run("the decision provider answers", func(t *testing.T) {
		t.Parallel()
		generator := answeringWith(isNo)
		decider := schemafluxtest.NewDecider(schemafluxtest.Yes(0.97))
		ctx, recorder := decidingPair(mw.DecisionOptions{}, generator, decider)
		got, err := schemaflux.Is(ctx, isRefundMsg, isRefundAsk)
		if err != nil || !got {
			t.Fatalf("Is = %v, %v, want the decider's yes", got, err)
		}
		if generator.Calls() != 0 || decider.Calls() != 1 {
			t.Errorf("the generator was asked %d times and the decider %d", generator.Calls(), decider.Calls())
		}
		r := onlyRecord(t, recorder)
		if r.Op != "is" || r.AnsweredBy != "test-decider" || !r.Typed || r.AskedFirst != "" || r.Escalated != "" {
			t.Errorf("record = %q answered by %q typed %v asked first %q escalated %q", r.Op, r.AnsweredBy, r.Typed, r.AskedFirst, r.Escalated)
		}
		if r.Requested != schemaflux.ContractSchemaAndInvariantChecked || r.Delivered != schemaflux.ContractSchemaAndInvariantChecked {
			t.Errorf("a typed answer is delivered at the model's level: %v and %v", r.Requested, r.Delivered)
		}
		if len(r.Asked) != 1 || r.Asked[0].Question != isRefundAsk || r.Asked[0].Answer != true {
			t.Errorf("asked = %+v", r.Asked)
		}
		if q := decider.Last().Decision; q == nil || len(q.Questions) != 1 || q.Questions[0].Kind != schemaflux.DecisionYesNo || q.Questions[0].Ask != isRefundAsk {
			t.Errorf("the decider was sent %+v", q)
		}
	})
	t.Run("the decision provider says no", func(t *testing.T) {
		t.Parallel()
		generator := answeringWith(isYes)
		ctx, _ := decidingPair(mw.DecisionOptions{}, generator, schemafluxtest.NewDecider(schemafluxtest.No(0.9)))
		got, err := schemaflux.Is(ctx, isRefundMsg, isRefundAsk)
		if err != nil || got || generator.Calls() != 0 {
			t.Errorf("Is = %v, %v after %d generator requests", got, err, generator.Calls())
		}
	})
	t.Run("an answer below the floor is passed over to the model, and the Record says both were asked", func(t *testing.T) {
		t.Parallel()
		generator := answeringWith(isNo)
		decider := schemafluxtest.NewDecider(schemafluxtest.Yes(0.4))
		ctx, recorder := decidingPair(mw.DecisionOptions{Floor: 0.9}, generator, decider)
		got, err := schemaflux.Is(ctx, isRefundMsg, isRefundAsk)
		if err != nil || got {
			t.Fatalf("Is = %v, %v, want the model's no", got, err)
		}
		r := onlyRecord(t, recorder)
		if r.AnsweredBy != "test" || r.Typed || r.AskedFirst != "test-decider" || r.Escalated != "below the floor" || !r.AskedFirstReported || r.AskedFirstConfidence != 0.4 {
			t.Errorf("record = answered by %q typed %v asked first %q escalated %q reported %v at %v",
				r.AnsweredBy, r.Typed, r.AskedFirst, r.Escalated, r.AskedFirstReported, r.AskedFirstConfidence)
		}
		if r.Requests != 2 {
			t.Errorf("the record counts %d requests, want the decider's and the model's", r.Requests)
		}
	})
	t.Run("a decision provider that is unavailable is passed over", func(t *testing.T) {
		t.Parallel()
		generator := answeringWith(isYes)
		decider := schemafluxtest.NewDecider()
		decider.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable})
		ctx, recorder := decidingPair(mw.DecisionOptions{}, generator, decider)
		got, err := schemaflux.Is(ctx, isRefundMsg, isRefundAsk)
		if err != nil || !got {
			t.Fatalf("Is = %v, %v", got, err)
		}
		if r := onlyRecord(t, recorder); r.AnsweredBy != "test" || r.AskedFirst != "test-decider" || r.Escalated != "unavailable" {
			t.Errorf("record = answered by %q asked first %q escalated %q", r.AnsweredBy, r.AskedFirst, r.Escalated)
		}
	})
	t.Run("a model answers when no decision provider is in front", func(t *testing.T) {
		t.Parallel()
		generator := answeringWith(isYes)
		recorder := &schemaflux.Recorder{Decisions: true}
		if _, err := schemaflux.Is(operationClient(generator, recordingTo(recorder)), isRefundMsg, isRefundAsk); err != nil {
			t.Fatal(err)
		}
		if r := onlyRecord(t, recorder); r.AnsweredBy != "test" || r.Typed || r.AskedFirst != "" {
			t.Errorf("record = answered by %q typed %v asked first %q", r.AnsweredBy, r.Typed, r.AskedFirst)
		}
	})
}

func TestIsUnderAFloorIsHeldToTheLevelItEstablishes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		floor   schemaflux.ContractLevel
		refused bool
	}{
		{"a floor of schema constrained is met", schemaflux.ContractSchemaConstrained, false},
		{"a floor of schema and invariants is met", schemaflux.ContractSchemaAndInvariantChecked, false},
		{"a floor of evidence is not", schemaflux.ContractEvidenceChecked, true},
		{"a floor nothing meets", schemaflux.ContractFullyGoverned, true},
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
				model := namedModel(isYes)
				settings := []func(*schemaflux.Client){underFloor(tc.floor)}
				if withRecorder {
					settings = append(settings, recordingTo(&recorder))
				}
				got, err := schemaflux.Is(operationClient(model, settings...), isRefundMsg, isRefundAsk)
				if tc.refused {
					wantError(t, err, schemaflux.ErrPolicy, "is")
					if got || model.Calls() != 0 {
						t.Errorf("answer %v after %d requests", got, model.Calls())
					}
				} else if err != nil || !got {
					t.Fatalf("Is = %v, %v", got, err)
				}
				if withRecorder {
					r := onlyRecord(t, &recorder)
					if r.Op != "is" || (r.Err != nil) != tc.refused || r.Requested != schemaflux.ContractSchemaAndInvariantChecked {
						t.Errorf("record = %q err %v requested %v", r.Op, r.Err, r.Requested)
					}
				}
			})
		}
	}
}

func TestIsUnderAFloorADecisionProvidersAnswerIsDeliveredAtTheModelsLevel(t *testing.T) {
	t.Parallel()
	provider.DeclareRoute("operation-floor", "test-model", []string{"eu"}, 0)
	provider.DeclareRoute("operation-floor-decider", "test-model", []string{"eu"}, 0)
	generator := namedModel(isNo)
	decider := schemafluxtest.NewDecider(schemafluxtest.Yes(0.95))
	decider.SetName("operation-floor-decider")
	recorder := &schemaflux.Recorder{}
	ctx := operationClient(mw.Decisions(generator, decider, mw.DecisionOptions{}), recordingTo(recorder), func(c *schemaflux.Client) {
		c.Policy = &schemaflux.DataPolicy{
			Providers:   []string{"operation-floor", "operation-floor-decider"},
			Regions:     []string{"eu"},
			MinContract: schemaflux.ContractSchemaAndInvariantChecked,
		}
	})
	got, err := schemaflux.Is(ctx, isRefundMsg, isRefundAsk)
	if err != nil || !got {
		t.Fatalf("Is = %v, %v, want the decider's yes", err, got)
	}
	r := onlyRecord(t, recorder)
	if !r.Typed || r.AnsweredBy != "operation-floor-decider" || r.Delivered != schemaflux.ContractSchemaAndInvariantChecked {
		t.Errorf("record = typed %v answered by %q delivered %v", r.Typed, r.AnsweredBy, r.Delivered)
	}
}

func TestIsUnderAnObserverSendsTheSameRequestAndRecordsOnce(t *testing.T) {
	// Not parallel: an observer is process state.
	var seen []string
	restore := telemetry.RegisterObserver(operationWatcher{finished: func(r telemetry.Result) {
		if r.Scope == telemetry.ScopeOperation {
			seen = append(seen, r.Operation)
		}
	}})
	defer restore()

	plain := answeringWith(isYes)
	if _, err := schemaflux.Is(operationClient(plain), isRefundMsg, isRefundAsk); err != nil {
		t.Fatal(err)
	}
	var recorder schemaflux.Recorder
	watched := answeringWith(isYes)
	got, err := schemaflux.Is(operationClient(watched, recordingTo(&recorder)), isRefundMsg, isRefundAsk)
	if err != nil || !got {
		t.Fatalf("Is = %v, %v", got, err)
	}
	if requestText(plain.Last()) != requestText(watched.Last()) {
		t.Error("an observer changed what the model is asked")
	}
	if onlyRecord(t, &recorder).Op != "is" {
		t.Error("the Record under an observer has the wrong operation")
	}
	if len(seen) != 2 || seen[0] != "is" || seen[1] != "is" {
		t.Errorf("the observer saw %v, want one is for each call", seen)
	}
}

func TestIsKeepsThePayloadOutOfEverythingItLeavesBehind(t *testing.T) {
	t.Parallel()
	const canary = "CANARY-3a8e61"
	opts := schemaflux.Options{Steering: "steer " + canary}
	subject := map[string]string{canary + "-key": "text " + canary}
	question := "Is it " + canary + "?"
	run := func(ctx context.Context) error {
		_, err := schemaflux.Is(ctx, subject, question, opts)
		return err
	}

	t.Run("a call that succeeds", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(isYes) }, true, run)
	})
	t.Run("an answer that is refused", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(`{"q1":"` + canary + `"}`) }, true, run)
	})
	t.Run("an answer that is text", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("the answer is " + canary) }, true, run)
	})
	t.Run("a provider that fails with a vendor message beneath its error", func(t *testing.T) {
		t.Parallel()
		vendor := &schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "the provider is down", Err: errors.New("vendor said " + canary)}
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Fail(vendor) }, false, run)
	})
	t.Run("a refused option", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(isYes) }, true,
			func(ctx context.Context) error {
				_, err := schemaflux.Is(ctx, subject, question, schemaflux.Options{Steering: canary, MaxOutputTokens: -1})
				return err
			})
	})
	t.Run("a blank question", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(isYes) }, true,
			func(ctx context.Context) error {
				_, err := schemaflux.Is(ctx, subject, "  ", opts)
				return err
			})
	})
	t.Run("a decision provider's answer of the wrong kind", func(t *testing.T) {
		t.Parallel()
		decider := schemafluxtest.NewDecider(schemafluxtest.Chose(canary))
		requireNoPayloadOn(t, canary, mw.Decisions(&schemafluxtest.Provider{}, decider, mw.DecisionOptions{}), true, run)
	})
}

func TestIsDetailReturnsTheAnswerWithTheModelsConfidence(t *testing.T) {
	t.Parallel()
	model := answeringWith(isYesSure)
	got, err := schemaflux.IsDetail(operationClient(model), isRefundMsg, isRefundAsk)
	if err != nil {
		t.Fatal(err)
	}
	if got != (schemaflux.Judged[bool]{Answer: true, ModelConfidence: 0.8}) {
		t.Errorf("got %+v", got)
	}
	wantText(t, model.Last(), "confidence")
	wantText(t, model.Last(), isRefundAsk)
}

func TestIsDetailAsksAnotherQuestionThanIsDoes(t *testing.T) {
	t.Parallel()
	plain, detail := answeringWith(isYes), answeringWith(isYesSure)
	if _, err := schemaflux.Is(operationClient(plain), isRefundMsg, isRefundAsk); err != nil {
		t.Fatal(err)
	}
	if _, err := schemaflux.IsDetail(operationClient(detail), isRefundMsg, isRefundAsk); err != nil {
		t.Fatal(err)
	}
	wantNoText(t, plain.Last(), "confidence")
	wantText(t, detail.Last(), "confidence")
}

func TestIsDetailRefusesAnAnswerThatLeavesOutOrMisstatesTheConfidence(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"no confidence":             isYes,
		"a confidence above one":    `{"q1":true,"confidence":1.4}`,
		"a confidence below zero":   `{"q1":true,"confidence":-0.1}`,
		"a confidence that is text": `{"q1":true,"confidence":"high"}`,
		"no answer":                 `{"confidence":0.9}`,
		"an answer that is text":    `{"q1":"yes","confidence":0.9}`,
		"text":                      `Yes, 90%.`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.IsDetail(operationClient(answeringWith(body)), isRefundMsg, isRefundAsk)
			wantError(t, err, schemaflux.ErrInvalidAnswer, "isdetail")
			if got != (schemaflux.Judged[bool]{}) {
				t.Errorf("a refused answer left %+v beside the error", got)
			}
		})
	}
}

func TestIsDetailRefusesABlankQuestionABadOptionAndANilContextBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	model := answeringWith(isYesSure)
	_, err := schemaflux.IsDetail(operationClient(model), isRefundMsg, "\t")
	if e := wantError(t, err, schemaflux.ErrInvalidRequest, "isdetail"); e.Message != "question is blank" {
		t.Errorf("message = %q", e.Message)
	}
	_, err = schemaflux.IsDetail(operationClient(model), isRefundMsg, isRefundAsk, schemaflux.Options{MaxOutputTokens: -1})
	wantError(t, err, schemaflux.ErrConfiguration, "isdetail")
	_, err = schemaflux.IsDetail(operationClient(model), isRefundMsg, isRefundAsk, schemaflux.Options{Tier: 8})
	wantError(t, err, schemaflux.ErrConfiguration, "isdetail")
	var nilContext context.Context
	_, err = schemaflux.IsDetail(nilContext, isRefundMsg, isRefundAsk)
	if e := wantError(t, err, schemaflux.ErrInvalidRequest, "isdetail"); e.Message != "the context is nil" {
		t.Errorf("message = %q", e.Message)
	}
	if model.Calls() != 0 {
		t.Errorf("%d requests were sent", model.Calls())
	}
}

func TestIsDetailEachOptionChangesTheRequest(t *testing.T) {
	t.Parallel()
	model := answeringWith(isYesSure)
	_, err := schemaflux.IsDetail(operationClient(model), isRefundMsg, isRefundAsk,
		schemaflux.Options{Model: "pinned", Steering: "refunds only", MaxOutputTokens: 44, WebSearch: true, Effort: schemaflux.EffortLow, Mode: schemaflux.ModeStrict})
	if err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	if req.Model != "pinned" || req.MaxTokens != 44 || !req.WebSearch || req.Effort != "low" || math.Abs(req.Temperature-0.1) > 1e-6 {
		t.Errorf("request = model %q ceiling %d web %v effort %q temperature %v", req.Model, req.MaxTokens, req.WebSearch, req.Effort, req.Temperature)
	}
	wantText(t, req, "refunds only")
}

func TestIsDetailSendsTheDetailRequestInEveryState(t *testing.T) {
	t.Parallel()
	plain := answeringWith(isYesSure)
	if _, err := schemaflux.IsDetail(operationClient(plain), isRefundMsg, isRefundAsk); err != nil {
		t.Fatal(err)
	}
	var recorder schemaflux.Recorder
	recorded := answeringWith(isYesSure)
	if _, err := schemaflux.IsDetail(operationClient(recorded, recordingTo(&recorder)), isRefundMsg, isRefundAsk); err != nil {
		t.Fatal(err)
	}
	floored := namedModel(isYesSure)
	if _, err := schemaflux.IsDetail(operationClient(floored, underFloor(schemaflux.ContractSchemaAndInvariantChecked)), isRefundMsg, isRefundAsk); err != nil {
		t.Fatal(err)
	}
	for name, other := range map[string]*schemafluxtest.Provider{"a recorder": recorded, "a floor": floored} {
		if requestText(plain.Last()) != requestText(other.Last()) || plain.Last().SchemaName != other.Last().SchemaName {
			t.Errorf("%s changed what the model is asked", name)
		}
	}
}

func TestIsDetailAddsOneRecordWithTheTwinsMeta(t *testing.T) {
	t.Parallel()
	t.Run("a call that succeeds", func(t *testing.T) {
		t.Parallel()
		recorder := schemaflux.Recorder{Decisions: true}
		if _, err := schemaflux.IsDetail(operationClient(answeringWith(isYesSure), recordingTo(&recorder)), isRefundMsg, isRefundAsk); err != nil {
			t.Fatal(err)
		}
		r := onlyRecord(t, &recorder)
		if r.Op != "isdetail" || r.Err != nil || r.Requests != 1 || r.AnsweredBy != "test" || r.Typed {
			t.Errorf("record = %q err %v requests %d answered by %q typed %v", r.Op, r.Err, r.Requests, r.AnsweredBy, r.Typed)
		}
		if r.Requested != schemaflux.ContractSchemaAndInvariantChecked || r.Delivered != schemaflux.ContractSchemaAndInvariantChecked {
			t.Errorf("levels = %v and %v", r.Requested, r.Delivered)
		}
		if len(r.Asked) != 1 || r.Asked[0].Answer != true {
			t.Errorf("asked = %+v", r.Asked)
		}
	})
	t.Run("a call that fails on its answer", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		_, err := schemaflux.IsDetail(operationClient(answeringWith(isYes), recordingTo(&recorder)), isRefundMsg, isRefundAsk)
		r := onlyRecord(t, &recorder)
		wantError(t, r.Err, schemaflux.ErrInvalidAnswer, "isdetail")
		if r.Op != "isdetail" || r.Err != err {
			t.Errorf("record = %q err %v", r.Op, r.Err)
		}
	})
	t.Run("a call the provider fails", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		var model schemafluxtest.Provider
		model.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable})
		_, err := schemaflux.IsDetail(operationClient(&model, recordingTo(&recorder)), isRefundMsg, isRefundAsk)
		r := onlyRecord(t, &recorder)
		wantError(t, r.Err, schemaflux.ErrUnavailable, "isdetail")
		if r.Err != err {
			t.Errorf("record err %v, call err %v", r.Err, err)
		}
	})
	t.Run("a call refused before anything is sent", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		model := answeringWith(isYesSure)
		_, err := schemaflux.IsDetail(operationClient(model, recordingTo(&recorder)), isRefundMsg, "")
		r := onlyRecord(t, &recorder)
		wantError(t, r.Err, schemaflux.ErrInvalidRequest, "isdetail")
		if r.Op != "isdetail" || r.Err != err || r.Requests != 0 || model.Calls() != 0 {
			t.Errorf("record = %q err %v requests %d", r.Op, r.Err, r.Requests)
		}
	})
}

func TestIsDetailUnderAFloorIsHeldToTheLevelItEstablishes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		floor   schemaflux.ContractLevel
		refused bool
	}{
		{"a floor it meets", schemaflux.ContractSchemaAndInvariantChecked, false},
		{"a floor above it", schemaflux.ContractEvidenceChecked, true},
	} {
		for _, withRecorder := range []bool{false, true} {
			name := tc.name
			if withRecorder {
				name += ", with a recorder"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				var recorder schemaflux.Recorder
				model := namedModel(isYesSure)
				settings := []func(*schemaflux.Client){underFloor(tc.floor)}
				if withRecorder {
					settings = append(settings, recordingTo(&recorder))
				}
				got, err := schemaflux.IsDetail(operationClient(model, settings...), isRefundMsg, isRefundAsk)
				if tc.refused {
					wantError(t, err, schemaflux.ErrPolicy, "isdetail")
					if got != (schemaflux.Judged[bool]{}) || model.Calls() != 0 {
						t.Errorf("answer %+v after %d requests", got, model.Calls())
					}
				} else if err != nil || !got.Answer {
					t.Fatalf("IsDetail = %+v, %v", got, err)
				}
				if withRecorder {
					if r := onlyRecord(t, &recorder); r.Op != "isdetail" || (r.Err != nil) != tc.refused {
						t.Errorf("record = %q err %v", r.Op, r.Err)
					}
				}
			})
		}
	}
}

func TestIsDetailGoesThroughTheDecisionProviderAndTheRecordSaysWhoAnswered(t *testing.T) {
	t.Parallel()
	t.Run("the decision provider reports a probability", func(t *testing.T) {
		t.Parallel()
		generator := answeringWith(isNo)
		decider := schemafluxtest.NewDecider(schemafluxtest.Yes(0.97))
		ctx, recorder := decidingPair(mw.DecisionOptions{}, generator, decider)
		got, err := schemaflux.IsDetail(ctx, isRefundMsg, isRefundAsk)
		if err != nil || got != (schemaflux.Judged[bool]{Answer: true, ModelConfidence: 0.97}) {
			t.Fatalf("IsDetail = %+v, %v", got, err)
		}
		r := onlyRecord(t, recorder)
		if r.Op != "isdetail" || r.AnsweredBy != "test-decider" || !r.Typed || generator.Calls() != 0 {
			t.Errorf("record = %q answered by %q typed %v, generator asked %d times", r.Op, r.AnsweredBy, r.Typed, generator.Calls())
		}
	})
	t.Run("a decision provider that reports no probability gives no confidence", func(t *testing.T) {
		t.Parallel()
		ctx, _ := decidingPair(mw.DecisionOptions{}, answeringWith(isNo), schemafluxtest.NewDecider(schemafluxtest.Yes()))
		got, err := schemaflux.IsDetail(ctx, isRefundMsg, isRefundAsk)
		wantError(t, err, schemaflux.ErrInvalidAnswer, "isdetail")
		if got != (schemaflux.Judged[bool]{}) {
			t.Errorf("got %+v beside the error", got)
		}
	})
	t.Run("an answer below the floor is passed over to the model", func(t *testing.T) {
		t.Parallel()
		ctx, recorder := decidingPair(mw.DecisionOptions{Floor: 0.9}, answeringWith(isYesSure), schemafluxtest.NewDecider(schemafluxtest.No(0.5)))
		got, err := schemaflux.IsDetail(ctx, isRefundMsg, isRefundAsk)
		if err != nil || !got.Answer || got.ModelConfidence != 0.8 {
			t.Fatalf("IsDetail = %+v, %v", got, err)
		}
		if r := onlyRecord(t, recorder); r.AnsweredBy != "test" || r.AskedFirst != "test-decider" || r.Escalated != "below the floor" {
			t.Errorf("record = answered by %q asked first %q escalated %q", r.AnsweredBy, r.AskedFirst, r.Escalated)
		}
	})
}

func TestIsDetailUnderAnObserverRecordsOnce(t *testing.T) {
	// Not parallel: an observer is process state.
	var seen []string
	restore := telemetry.RegisterObserver(operationWatcher{finished: func(r telemetry.Result) {
		if r.Scope == telemetry.ScopeOperation {
			seen = append(seen, r.Operation)
		}
	}})
	defer restore()

	var recorder schemaflux.Recorder
	got, err := schemaflux.IsDetail(operationClient(answeringWith(isYesSure), recordingTo(&recorder)), isRefundMsg, isRefundAsk)
	if err != nil || !got.Answer {
		t.Fatalf("IsDetail = %+v, %v", got, err)
	}
	if onlyRecord(t, &recorder).Op != "isdetail" {
		t.Error("the Record under an observer has the wrong operation")
	}
	// One scope for the call, named as the Record of the call is.
	if len(seen) != 1 || seen[0] != "isdetail" {
		t.Errorf("the observer saw %v, want one operation, isdetail", seen)
	}
}

func TestIsDetailKeepsThePayloadOutOfEverythingItLeavesBehind(t *testing.T) {
	t.Parallel()
	const canary = "CANARY-b47c20"
	opts := schemaflux.Options{Steering: "steer " + canary}
	subject := map[string]string{canary + "-key": "text " + canary}
	run := func(ctx context.Context) error {
		_, err := schemaflux.IsDetail(ctx, subject, "Is it "+canary+"?", opts)
		return err
	}
	for name, setup := range map[string]func(*schemafluxtest.Provider){
		"a call that succeeds":         func(p *schemafluxtest.Provider) { p.Reply(isYesSure) },
		"an answer without confidence": func(p *schemafluxtest.Provider) { p.Reply(`{"q1":true,"x":"` + canary + `"}`) },
		"a confidence that is text":    func(p *schemafluxtest.Provider) { p.Reply(`{"q1":true,"confidence":"` + canary + `"}`) },
		"an answer that is text":       func(p *schemafluxtest.Provider) { p.Reply("sure, " + canary) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			requireNoPayload(t, canary, setup, true, run)
		})
	}
	t.Run("a provider that fails with a vendor message beneath its error", func(t *testing.T) {
		t.Parallel()
		vendor := &schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "the provider is down", Err: errors.New("vendor said " + canary)}
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Fail(vendor) }, false, run)
	})
}

func TestJudgedMarshalsAsItsFieldNamesSay(t *testing.T) {
	t.Parallel()
	got, err := json.Marshal(schemaflux.Judged[bool]{Answer: true, ModelConfidence: 0.5})
	if err != nil || string(got) != `{"Answer":true,"ModelConfidence":0.5}` {
		t.Errorf("got %s, %v", got, err)
	}
}

// decidingClient returns a client whose provider is a decision provider in front
// of a generating provider that has nothing scripted, so that a request reaching
// the generating provider fails.
func decidingClient(decider *schemafluxtest.Decider) context.Context {
	return operationClient(mw.Decisions(&schemafluxtest.Provider{}, decider, mw.DecisionOptions{}))
}

// decisionCanary is in the struct tags of assessCanaried and in the text the
// payload tests put into every other function.
const decisionCanary = "CANARY-7f2d19"

// assessCanaried is the type the shared tests ask Assess about: three questions
// of the three kinds, with the canary in a question and in an option.
type assessCanaried struct {
	Spam    bool   `ask:"Is it CANARY-7f2d19?"`
	Team    string `ask:"Which team, CANARY-7f2d19?" oneof:"billing,technical,CANARY-7f2d19"`
	Urgency int    `ask:"How urgent is it?" scale:"1-5"`
}

// decisionInput is what the shared tests give a decision function.
type decisionInput struct {
	subject  any
	question string
	// label is added to the last label of the functions that take labels, so that
	// a test can put its canary there.
	label string
	opts  []schemaflux.Options
}

// decisionFunction is one of the decision functions as the shared tests call it.
type decisionFunction struct {
	name string
	// op is the Op of the Record the call adds.
	op string
	// asks is false for Assess, whose questions are its type's.
	asks bool
	// detail is true for the forms that ask the model for its confidence.
	detail bool
	// reply is a body the model answers with, and typed is what a decision
	// provider answers with, each the same answer.
	reply string
	typed []schemaflux.DecisionAnswer
	// questions is how many questions the call puts.
	questions int
	// answer and typedAnswer are what the call returns, written with fmt.Sprint,
	// for reply and for typed.
	answer, typedAnswer string
	// ask makes the call and returns what it returned, written with fmt.Sprint.
	ask func(ctx context.Context, in decisionInput) (string, error)
}

// call makes the call and returns its error.
func (f decisionFunction) call(ctx context.Context, in decisionInput) error {
	_, err := f.ask(ctx, in)
	return err
}

func labelsFor(in decisionInput) []string {
	return []string{"billing", "technical", "shipping" + in.label}
}

// decisionFunctions are the eight functions this file's tests share.
func decisionFunctions() []decisionFunction {
	chose := schemafluxtest.Chose("technical", 0.9)
	return []decisionFunction{
		{name: "Which", op: "which", asks: true, reply: whichSecond, typed: []schemaflux.DecisionAnswer{chose}, questions: 1,
			answer: "technical", typedAnswer: "technical",
			ask: func(ctx context.Context, in decisionInput) (string, error) {
				v, err := schemaflux.Which(ctx, in.subject, in.question, labelsFor(in), in.opts...)
				return fmt.Sprint(v), err
			}},
		{name: "WhichDetail", op: "whichdetail", asks: true, detail: true, reply: whichSecondSure, typed: []schemaflux.DecisionAnswer{chose}, questions: 1,
			answer: "{technical 0.8}", typedAnswer: "{technical 0.9}",
			ask: func(ctx context.Context, in decisionInput) (string, error) {
				v, err := schemaflux.WhichDetail(ctx, in.subject, in.question, labelsFor(in), in.opts...)
				return fmt.Sprint(v), err
			}},
		{name: "WhichOrNone", op: "whichornone", asks: true, reply: whichSecond, typed: []schemaflux.DecisionAnswer{chose}, questions: 1,
			answer: "technical true", typedAnswer: "technical true",
			ask: func(ctx context.Context, in decisionInput) (string, error) {
				label, found, err := schemaflux.WhichOrNone(ctx, in.subject, in.question, labelsFor(in), in.opts...)
				return fmt.Sprint(label, " ", found), err
			}},
		{name: "WhichOrNoneDetail", op: "whichornonedetail", asks: true, detail: true, reply: whichSecondSure, typed: []schemaflux.DecisionAnswer{chose}, questions: 1,
			answer: "{technical 0.8} true", typedAnswer: "{technical 0.9} true",
			ask: func(ctx context.Context, in decisionInput) (string, error) {
				j, found, err := schemaflux.WhichOrNoneDetail(ctx, in.subject, in.question, labelsFor(in), in.opts...)
				return fmt.Sprint(j, " ", found), err
			}},
		{name: "Rate", op: "rate", asks: true, reply: `{"q1":4}`, typed: []schemaflux.DecisionAnswer{schemafluxtest.Level(4, 0.9)}, questions: 1,
			answer: "4", typedAnswer: "4",
			ask: func(ctx context.Context, in decisionInput) (string, error) {
				v, err := schemaflux.Rate(ctx, in.subject, in.question, 1, 5, in.opts...)
				return fmt.Sprint(v), err
			}},
		{name: "RateDetail", op: "ratedetail", asks: true, detail: true, reply: `{"q1":4,"confidence":0.8}`, typed: []schemaflux.DecisionAnswer{schemafluxtest.Level(4, 0.9)}, questions: 1,
			answer: "{4 0.8}", typedAnswer: "{4 0.9}",
			ask: func(ctx context.Context, in decisionInput) (string, error) {
				v, err := schemaflux.RateDetail(ctx, in.subject, in.question, 1, 5, in.opts...)
				return fmt.Sprint(v), err
			}},
		{name: "Assess", op: "assess", reply: assessAnswer,
			typed:     []schemaflux.DecisionAnswer{schemafluxtest.No(0.9), schemafluxtest.Chose("technical", 0.9), schemafluxtest.Level(4, 0.9)},
			questions: 3,
			answer:    "{true technical 4}", typedAnswer: "{false technical 4}",
			ask: func(ctx context.Context, in decisionInput) (string, error) {
				v, err := schemaflux.Assess[assessCanaried](ctx, in.subject, in.opts...)
				return fmt.Sprint(v), err
			}},
		{name: "AssessDetail", op: "assessdetail", detail: true, reply: assessAnswerSur,
			typed:     []schemaflux.DecisionAnswer{schemafluxtest.No(0.9), schemafluxtest.Chose("technical", 0.9), schemafluxtest.Level(4, 0.9)},
			questions: 3,
			answer:    "{{true technical 4} 0.7}", typedAnswer: "{{false technical 4} 0.9}",
			ask: func(ctx context.Context, in decisionInput) (string, error) {
				v, err := schemaflux.AssessDetail[assessCanaried](ctx, in.subject, in.opts...)
				return fmt.Sprint(v), err
			}},
	}
}

// eachDecisionFunction runs the test once for every function, in parallel.
func eachDecisionFunction(t *testing.T, test func(t *testing.T, f decisionFunction)) {
	t.Helper()
	for _, f := range decisionFunctions() {
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()
			test(t, f)
		})
	}
}

// ordinary is the input most shared tests use.
func ordinary(opts ...schemaflux.Options) decisionInput {
	return decisionInput{subject: isRefundMsg, question: isRefundAsk, opts: opts}
}

func TestEveryDecisionFunctionAddsOneRecordWithItsOperationsNameOnSuccessAndOnFailure(t *testing.T) {
	t.Parallel()
	eachDecisionFunction(t, func(t *testing.T, f decisionFunction) {
		t.Run("a call that succeeds", func(t *testing.T) {
			var recorder schemaflux.Recorder
			model := answeringWith(f.reply)
			if err := f.call(operationClient(model, recordingTo(&recorder)), ordinary()); err != nil {
				t.Fatal(err)
			}
			r := onlyRecord(t, &recorder)
			if r.Op != f.op || r.Err != nil || r.Requests != 1 || r.Provider != "test" || r.AnsweredBy != "test" || r.Typed || len(r.Asked) != 0 {
				t.Errorf("record = %q err %v requests %d provider %q answered by %q typed %v asked %d", r.Op, r.Err, r.Requests, r.Provider, r.AnsweredBy, r.Typed, len(r.Asked))
			}
			if r.Requested != schemaflux.ContractSchemaAndInvariantChecked || r.Delivered != schemaflux.ContractSchemaAndInvariantChecked {
				t.Errorf("levels = %v and %v", r.Requested, r.Delivered)
			}
		})
		t.Run("a call that fails on its answer", func(t *testing.T) {
			var recorder schemaflux.Recorder
			err := f.call(operationClient(answeringWith("Yes."), recordingTo(&recorder)), ordinary())
			wantError(t, err, schemaflux.ErrInvalidAnswer, f.op)
			if r := onlyRecord(t, &recorder); r.Op != f.op || r.Err != err || r.Delivered != schemaflux.ContractPromptOnly {
				t.Errorf("record = %q err %v delivered %v", r.Op, r.Err, r.Delivered)
			}
		})
		t.Run("a call the provider fails", func(t *testing.T) {
			var recorder schemaflux.Recorder
			var model schemafluxtest.Provider
			model.Fail(&schemaflux.Error{Kind: schemaflux.ErrRateLimited})
			err := f.call(operationClient(&model, recordingTo(&recorder)), ordinary())
			wantError(t, err, schemaflux.ErrRateLimited, f.op)
			if r := onlyRecord(t, &recorder); r.Op != f.op || r.Err != err {
				t.Errorf("record = %q err %v", r.Op, r.Err)
			}
		})
		t.Run("a call refused before anything is sent: a bad option", func(t *testing.T) {
			var recorder schemaflux.Recorder
			model := answeringWith(f.reply)
			err := f.call(operationClient(model, recordingTo(&recorder)), ordinary(schemaflux.Options{MaxOutputTokens: -1}))
			wantError(t, err, schemaflux.ErrConfiguration, f.op)
			if r := onlyRecord(t, &recorder); r.Op != f.op || r.Err != err || r.Requests != 0 || model.Calls() != 0 {
				t.Errorf("record = %q err %v requests %d", r.Op, r.Err, r.Requests)
			}
		})
		if f.asks {
			t.Run("a call refused before anything is sent: a blank question", func(t *testing.T) {
				var recorder schemaflux.Recorder
				model := answeringWith(f.reply)
				in := ordinary()
				in.question = " "
				err := f.call(operationClient(model, recordingTo(&recorder)), in)
				wantError(t, err, schemaflux.ErrInvalidRequest, f.op)
				if r := onlyRecord(t, &recorder); r.Op != f.op || r.Err != err || r.Requests != 0 || model.Calls() != 0 {
					t.Errorf("record = %q err %v requests %d", r.Op, r.Err, r.Requests)
				}
			})
		}
	})
}

func TestEveryDecisionFunctionRefusesANilContextABlankQuestionAndABadOption(t *testing.T) {
	t.Parallel()
	eachDecisionFunction(t, func(t *testing.T, f decisionFunction) {
		var nilContext context.Context
		err := f.call(nilContext, ordinary())
		if e := wantError(t, err, schemaflux.ErrInvalidRequest, f.op); e.Message != "the context is nil" {
			t.Errorf("message = %q", e.Message)
		}
		model := answeringWith(f.reply)
		ctx := operationClient(model)
		for _, opts := range []schemaflux.Options{{MaxOutputTokens: -1}, {Tier: 7}, {Mode: 7}} {
			wantError(t, f.call(ctx, ordinary(opts)), schemaflux.ErrConfiguration, f.op)
		}
		if f.asks {
			for _, question := range []string{"", " ", " \t\n ", " "} {
				in := ordinary()
				in.question = question
				if e := wantError(t, f.call(ctx, in), schemaflux.ErrInvalidRequest, f.op); e.Message != "question is blank" {
					t.Errorf("message = %q", e.Message)
				}
			}
		}
		if model.Calls() != 0 {
			t.Errorf("%d requests were sent", model.Calls())
		}
	})
}

func TestEveryDecisionFunctionWithACancelledContextReturnsCanceledAndSendsNothing(t *testing.T) {
	t.Parallel()
	eachDecisionFunction(t, func(t *testing.T, f decisionFunction) {
		model := answeringWith(f.reply)
		ctx, cancel := context.WithCancel(operationClient(model))
		cancel()
		err := f.call(ctx, ordinary())
		wantError(t, err, schemaflux.ErrCanceled, f.op)
		if !errors.Is(err, context.Canceled) || model.Calls() != 0 {
			t.Errorf("err %v after %d requests", err, model.Calls())
		}
	})
}

func TestEveryDecisionFunctionSendsTheSameRequestWhateverRecordsTheCall(t *testing.T) {
	t.Parallel()
	// The settings change every field the common options reach; the model is not
	// one of them, since a floor allows only the routes declared for the fake.
	settings := schemaflux.Options{Tier: schemaflux.TierSmart, Effort: schemaflux.EffortLow, Mode: schemaflux.ModeStrict, Steering: "teams only", MaxOutputTokens: 55, WebSearch: true}
	sameRequest := func(a, b schemaflux.Request) bool {
		return requestText(a) == requestText(b) && a.SchemaName == b.SchemaName && a.Model == b.Model && a.MaxTokens == b.MaxTokens &&
			a.Temperature == b.Temperature && a.Effort == b.Effort && a.EffortPreferred == b.EffortPreferred && a.WebSearch == b.WebSearch && a.Format == b.Format
	}
	eachDecisionFunction(t, func(t *testing.T, f decisionFunction) {
		for _, in := range []decisionInput{ordinary(), ordinary(settings)} {
			plain := answeringWith(f.reply)
			if err := f.call(operationClient(plain), in); err != nil {
				t.Fatal(err)
			}
			var recorder schemaflux.Recorder
			recorded := answeringWith(f.reply)
			if err := f.call(operationClient(recorded, recordingTo(&recorder)), in); err != nil {
				t.Fatal(err)
			}
			floored := namedModel(f.reply)
			if err := f.call(operationClient(floored, underFloor(schemaflux.ContractSchemaAndInvariantChecked)), in); err != nil {
				t.Fatal(err)
			}
			for name, other := range map[string]*schemafluxtest.Provider{"a recorder": recorded, "a floor": floored} {
				if !sameRequest(plain.Last(), other.Last()) {
					t.Errorf("%s changed what the model is asked (options %+v)", name, in.opts)
				}
			}
			if f.detail != strings.Contains(plain.Last().User, `"confidence"`) {
				t.Errorf("the request asks for a confidence: %v, want %v", !f.detail, f.detail)
			}
			wantText(t, plain.Last(), isRefundMsg)
		}
	})
}

func TestEveryDecisionFunctionTakesEachCommonSettingAsIsDoes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  schemaflux.Options
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"Model", schemaflux.Options{Model: "pinned-model"}, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"Steering", schemaflux.Options{Steering: "refunds only"}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, "refunds only") }},
		{"TierSmart", schemaflux.Options{Tier: schemaflux.TierSmart}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 4000 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"TierQuick", schemaflux.Options{Tier: schemaflux.TierQuick}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 1000 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"MaxOutputTokens", schemaflux.Options{MaxOutputTokens: 31}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 31 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.Options{WebSearch: true}, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("no web search")
			}
		}},
		{"Effort", schemaflux.Options{Effort: schemaflux.EffortMedium}, func(t *testing.T, req schemaflux.Request) {
			if req.Effort != "medium" || req.EffortPreferred {
				t.Errorf("effort %q preferred %v", req.Effort, req.EffortPreferred)
			}
		}},
		{"ModeStrict", schemaflux.Options{Mode: schemaflux.ModeStrict}, func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.1) > 1e-6 {
				t.Errorf("temperature = %v", req.Temperature)
			}
		}},
		{"ModeCreative", schemaflux.Options{Mode: schemaflux.ModeCreative}, func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.7) > 1e-6 {
				t.Errorf("temperature = %v", req.Temperature)
			}
		}},
	}
	eachDecisionFunction(t, func(t *testing.T, f decisionFunction) {
		for _, tc := range cases {
			model := answeringWith(f.reply)
			if err := f.call(operationClient(model), ordinary(tc.opts)); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			t.Run(tc.name, func(t *testing.T) { tc.check(t, model.Last()) })
		}
		t.Run("several values merge from left to right", func(t *testing.T) {
			model := answeringWith(f.reply)
			err := f.call(operationClient(model), ordinary(
				schemaflux.Options{Model: "first", Steering: "keep this", MaxOutputTokens: 9},
				schemaflux.Options{Model: "second", WebSearch: true},
				schemaflux.Options{}))
			if err != nil {
				t.Fatal(err)
			}
			req := model.Last()
			if req.Model != "second" || req.MaxTokens != 9 || !req.WebSearch {
				t.Errorf("model %q ceiling %d web %v", req.Model, req.MaxTokens, req.WebSearch)
			}
			wantText(t, req, "keep this")
		})
		t.Run("a bad setting in a later value is refused", func(t *testing.T) {
			err := f.call(operationClient(answeringWith(f.reply)), ordinary(schemaflux.Options{}, schemaflux.Options{MaxOutputTokens: -1}))
			wantError(t, err, schemaflux.ErrConfiguration, f.op)
		})
	})
}

func TestEveryDecisionFunctionUnderAFloorIsHeldToTheLevelItEstablishes(t *testing.T) {
	t.Parallel()
	eachDecisionFunction(t, func(t *testing.T, f decisionFunction) {
		for _, tc := range []struct {
			name    string
			floor   schemaflux.ContractLevel
			refused bool
		}{
			{"a floor of schema constrained is met", schemaflux.ContractSchemaConstrained, false},
			{"a floor of schema and invariants is met", schemaflux.ContractSchemaAndInvariantChecked, false},
			{"a floor of evidence is not", schemaflux.ContractEvidenceChecked, true},
			{"a floor nothing meets", schemaflux.ContractFullyGoverned, true},
		} {
			for _, withRecorder := range []bool{false, true} {
				name := tc.name
				if withRecorder {
					name += ", with a recorder"
				}
				t.Run(name, func(t *testing.T) {
					var recorder schemaflux.Recorder
					model := namedModel(f.reply)
					settings := []func(*schemaflux.Client){underFloor(tc.floor)}
					if withRecorder {
						settings = append(settings, recordingTo(&recorder))
					}
					err := f.call(operationClient(model, settings...), ordinary())
					if tc.refused {
						wantError(t, err, schemaflux.ErrPolicy, f.op)
						if model.Calls() != 0 {
							t.Errorf("%d requests were sent", model.Calls())
						}
					} else if err != nil {
						t.Fatal(err)
					}
					if withRecorder {
						r := onlyRecord(t, &recorder)
						if r.Op != f.op || (r.Err != nil) != tc.refused || r.Requested != schemaflux.ContractSchemaAndInvariantChecked {
							t.Errorf("record = %q err %v requested %v", r.Op, r.Err, r.Requested)
						}
					}
				})
			}
		}
	})
}

func TestEveryDecisionFunctionGoesThroughTheDecisionProviderWhenOneIsInFrontAndTheRecordSaysWhoAnswered(t *testing.T) {
	t.Parallel()
	eachDecisionFunction(t, func(t *testing.T, f decisionFunction) {
		t.Run("the decision provider answers", func(t *testing.T) {
			generator := answeringWith(f.reply)
			decider := schemafluxtest.NewDecider(f.typed...)
			ctx, recorder := decidingPair(mw.DecisionOptions{}, generator, decider)
			if err := f.call(ctx, ordinary()); err != nil {
				t.Fatal(err)
			}
			if generator.Calls() != 0 || decider.Calls() != 1 {
				t.Errorf("the generator was asked %d times and the decider %d", generator.Calls(), decider.Calls())
			}
			r := onlyRecord(t, recorder)
			if r.Op != f.op || r.AnsweredBy != "test-decider" || !r.Typed || r.AskedFirst != "" || r.Escalated != "" || r.Err != nil {
				t.Errorf("record = %q answered by %q typed %v asked first %q escalated %q err %v", r.Op, r.AnsweredBy, r.Typed, r.AskedFirst, r.Escalated, r.Err)
			}
			if r.Requested != schemaflux.ContractSchemaAndInvariantChecked || r.Delivered != schemaflux.ContractSchemaAndInvariantChecked {
				t.Errorf("a typed answer is delivered at the model's level: %v and %v", r.Requested, r.Delivered)
			}
			if len(r.Asked) != f.questions {
				t.Errorf("asked = %+v, want %d questions", r.Asked, f.questions)
			}
			if q := decider.Last().Decision; q == nil || len(q.Questions) != f.questions {
				t.Errorf("the decider was sent %+v", q)
			}
		})
		t.Run("an answer below the floor is passed over to the model and the Record says both were asked", func(t *testing.T) {
			generator := answeringWith(f.reply)
			low := make([]schemaflux.DecisionAnswer, len(f.typed))
			for i, a := range f.typed {
				a.ModelConfidence = 0.4
				low[i] = a
			}
			decider := schemafluxtest.NewDecider(low...)
			ctx, recorder := decidingPair(mw.DecisionOptions{Floor: 0.9}, generator, decider)
			if err := f.call(ctx, ordinary()); err != nil {
				t.Fatal(err)
			}
			r := onlyRecord(t, recorder)
			if r.AnsweredBy != "test" || r.Typed || r.AskedFirst != "test-decider" || r.Escalated != "below the floor" || !r.AskedFirstReported || r.AskedFirstConfidence != 0.4 || r.Requests != 2 || r.Op != f.op {
				t.Errorf("record = %q answered by %q typed %v asked first %q escalated %q reported %v at %v requests %d",
					r.Op, r.AnsweredBy, r.Typed, r.AskedFirst, r.Escalated, r.AskedFirstReported, r.AskedFirstConfidence, r.Requests)
			}
		})
		t.Run("a decision provider that is unavailable is passed over", func(t *testing.T) {
			generator := answeringWith(f.reply)
			decider := schemafluxtest.NewDecider()
			decider.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable})
			ctx, recorder := decidingPair(mw.DecisionOptions{}, generator, decider)
			if err := f.call(ctx, ordinary()); err != nil {
				t.Fatal(err)
			}
			if r := onlyRecord(t, recorder); r.AnsweredBy != "test" || r.AskedFirst != "test-decider" || r.Escalated != "unavailable" {
				t.Errorf("record = answered by %q asked first %q escalated %q", r.AnsweredBy, r.AskedFirst, r.Escalated)
			}
		})
		t.Run("a decision provider that fails for good fails the call and the generator is not asked", func(t *testing.T) {
			generator := answeringWith(f.reply)
			decider := schemafluxtest.NewDecider()
			decider.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnauthorized})
			ctx, recorder := decidingPair(mw.DecisionOptions{}, generator, decider)
			err := f.call(ctx, ordinary())
			wantError(t, err, schemaflux.ErrUnauthorized, f.op)
			if r := onlyRecord(t, recorder); r.Op != f.op || r.Err != err || generator.Calls() != 0 {
				t.Errorf("record = %q err %v, generator asked %d times", r.Op, r.Err, generator.Calls())
			}
		})
		t.Run("a model answers when no decision provider is in front", func(t *testing.T) {
			recorder := &schemaflux.Recorder{Decisions: true}
			if err := f.call(operationClient(answeringWith(f.reply), recordingTo(recorder)), ordinary()); err != nil {
				t.Fatal(err)
			}
			if r := onlyRecord(t, recorder); r.AnsweredBy != "test" || r.Typed || r.AskedFirst != "" || len(r.Asked) != f.questions {
				t.Errorf("record = answered by %q typed %v asked first %q asked %d", r.AnsweredBy, r.Typed, r.AskedFirst, len(r.Asked))
			}
		})
		if f.detail {
			t.Run("a decision provider that reports no probability gives no confidence", func(t *testing.T) {
				plain := make([]schemaflux.DecisionAnswer, len(f.typed))
				for i, a := range f.typed {
					a.ModelConfidence, a.ConfidenceReported = 0, false
					plain[i] = a
				}
				ctx, recorder := decidingPair(mw.DecisionOptions{}, answeringWith(f.reply), schemafluxtest.NewDecider(plain...))
				err := f.call(ctx, ordinary())
				wantError(t, err, schemaflux.ErrInvalidAnswer, f.op)
				if r := onlyRecord(t, recorder); r.Err != err {
					t.Errorf("record err %v", r.Err)
				}
			})
		}
		wrongKind := func() []schemaflux.DecisionAnswer {
			wrong := make([]schemaflux.DecisionAnswer, len(f.typed))
			for i := range wrong {
				wrong[i] = schemafluxtest.Chose("nothing offered", 0.9)
				if f.typed[i].Kind == schemaflux.DecisionChoice {
					wrong[i] = schemafluxtest.Yes(0.9)
				}
			}
			return wrong
		}
		t.Run("a typed answer of the wrong kind is never passed on: with no failover the call fails", func(t *testing.T) {
			generator := answeringWith(f.reply)
			ctx, _ := decidingPair(mw.DecisionOptions{NoFailover: true}, generator, schemafluxtest.NewDecider(wrongKind()...))
			wantError(t, f.call(ctx, ordinary()), schemaflux.ErrInvalidAnswer, f.op)
			if generator.Calls() != 0 {
				t.Errorf("the model was asked %d times with failover off", generator.Calls())
			}
		})
		t.Run("a typed answer of the wrong kind is the decision provider failing: the model is asked", func(t *testing.T) {
			generator := answeringWith(f.reply)
			ctx, recorder := decidingPair(mw.DecisionOptions{}, generator, schemafluxtest.NewDecider(wrongKind()...))
			if err := f.call(ctx, ordinary()); err != nil {
				t.Fatalf("the model's answer was not taken: %v", err)
			}
			if r := onlyRecord(t, recorder); r.AnsweredBy != "test" || r.Typed || r.AskedFirst != "test-decider" || r.Escalated != "unavailable" || generator.Calls() != 1 {
				t.Errorf("record = answered by %q typed %v asked first %q escalated %q, model asked %d times", r.AnsweredBy, r.Typed, r.AskedFirst, r.Escalated, generator.Calls())
			}
		})
	})
}

func TestRecorderDecisionsFillsAskedForEveryDecisionFunctionAndOnlyWhenItAsksFor(t *testing.T) {
	t.Parallel()
	eachDecisionFunction(t, func(t *testing.T, f decisionFunction) {
		recorder := &schemaflux.Recorder{Decisions: true}
		if err := f.call(operationClient(answeringWith(f.reply), recordingTo(recorder)), ordinary()); err != nil {
			t.Fatal(err)
		}
		r := onlyRecord(t, recorder)
		if len(r.Asked) != f.questions {
			t.Fatalf("asked = %+v, want %d", r.Asked, f.questions)
		}
		for _, a := range r.Asked {
			if a.Question == "" || a.Answer == nil {
				t.Errorf("asked = %+v: a question and its answer", a)
			}
		}
		if f.asks && r.Asked[0].Question != isRefundAsk {
			t.Errorf("the question is %q", r.Asked[0].Question)
		}
	})
}

func TestEveryDecisionFunctionUnderAnObserverIsOneOperationAndSendsTheSameRequest(t *testing.T) {
	// Not parallel: an observer is process state.
	for _, f := range decisionFunctions() {
		t.Run(f.name, func(t *testing.T) {
			var finished []string
			restore := telemetry.RegisterObserver(operationWatcher{finished: func(r telemetry.Result) {
				if r.Scope == telemetry.ScopeOperation {
					finished = append(finished, r.Operation)
				}
			}})
			defer restore()
			var recorder schemaflux.Recorder
			model := answeringWith(f.reply)
			if err := f.call(operationClient(model, recordingTo(&recorder)), ordinary()); err != nil {
				t.Fatal(err)
			}
			if onlyRecord(t, &recorder).Op != f.op {
				t.Error("the Record under an observer has the wrong operation")
			}
			// The Detail twins name their own operation scope after the plain form;
			// only that the call is exactly one scope is the contract.
			if len(finished) != 1 {
				t.Errorf("the observer saw %v, want one operation", finished)
			}
			plain := answeringWith(f.reply)
			if err := f.call(operationClient(plain), ordinary()); err != nil {
				t.Fatal(err)
			}
			if requestText(plain.Last()) != requestText(model.Last()) {
				t.Error("an observer changed what the model is asked")
			}
		})
	}
}

func TestEveryDecisionFunctionKeepsThePayloadOutOfEverythingItLeavesBehind(t *testing.T) {
	t.Parallel()
	opts := schemaflux.Options{Steering: "steer " + decisionCanary}
	subject := map[string]string{decisionCanary + "-key": "text " + decisionCanary}
	eachDecisionFunction(t, func(t *testing.T, f decisionFunction) {
		in := decisionInput{subject: subject, question: "Is it " + decisionCanary + "?", label: "-" + decisionCanary, opts: []schemaflux.Options{opts}}
		run := func(ctx context.Context) error { return f.call(ctx, in) }
		t.Run("a call that succeeds", func(t *testing.T) {
			requireNoPayload(t, decisionCanary, func(p *schemafluxtest.Provider) { p.Reply(f.reply) }, true, run)
		})
		t.Run("an answer that is refused", func(t *testing.T) {
			requireNoPayload(t, decisionCanary, func(p *schemafluxtest.Provider) { p.Reply(`{"q1":"` + decisionCanary + `"}`) }, true, run)
		})
		t.Run("an answer that is text", func(t *testing.T) {
			requireNoPayload(t, decisionCanary, func(p *schemafluxtest.Provider) { p.Reply("the answer is " + decisionCanary) }, true, run)
		})
		t.Run("an answer that leaves a question out", func(t *testing.T) {
			requireNoPayload(t, decisionCanary, func(p *schemafluxtest.Provider) { p.Reply(`{"x":"` + decisionCanary + `"}`) }, true, run)
		})
		t.Run("a provider that fails with a vendor message beneath its error", func(t *testing.T) {
			vendor := &schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "the provider is down", Err: errors.New("vendor said " + decisionCanary)}
			requireNoPayload(t, decisionCanary, func(p *schemafluxtest.Provider) { p.Fail(vendor) }, false, run)
		})
		t.Run("a provider that fails with a plain error holding a vendor message", func(t *testing.T) {
			requireNoPayload(t, decisionCanary, func(p *schemafluxtest.Provider) { p.Fail(errors.New("vendor said " + decisionCanary)) }, false, run)
		})
		t.Run("a refused option", func(t *testing.T) {
			bad := in
			bad.opts = []schemaflux.Options{{Steering: decisionCanary, MaxOutputTokens: -1}}
			requireNoPayload(t, decisionCanary, func(p *schemafluxtest.Provider) { p.Reply(f.reply) }, true,
				func(ctx context.Context) error { return f.call(ctx, bad) })
		})
		if f.asks {
			t.Run("a blank question", func(t *testing.T) {
				blank := in
				blank.question = "  "
				requireNoPayload(t, decisionCanary, func(p *schemafluxtest.Provider) { p.Reply(f.reply) }, true,
					func(ctx context.Context) error { return f.call(ctx, blank) })
			})
		}
		t.Run("a decision provider's answer of the wrong kind", func(t *testing.T) {
			decider := schemafluxtest.NewDecider(schemafluxtest.Chose(decisionCanary), schemafluxtest.Chose(decisionCanary), schemafluxtest.Chose(decisionCanary))
			requireNoPayloadOn(t, decisionCanary, mw.Decisions(&schemafluxtest.Provider{}, decider, mw.DecisionOptions{}), true, run)
		})
	})
}

func TestEveryDecisionFunctionReturnsTheSameAnswerWhateverRecordsTheCallAndWhoAnswers(t *testing.T) {
	t.Parallel()
	eachDecisionFunction(t, func(t *testing.T, f decisionFunction) {
		check := func(t *testing.T, ctx context.Context, want string) {
			t.Helper()
			got, err := f.ask(ctx, ordinary())
			if err != nil || got != want {
				t.Errorf("the call returned %q, %v, want %q", got, err, want)
			}
		}
		t.Run("plain", func(t *testing.T) { check(t, operationClient(answeringWith(f.reply)), f.answer) })
		t.Run("a recorder", func(t *testing.T) {
			var recorder schemaflux.Recorder
			check(t, operationClient(answeringWith(f.reply), recordingTo(&recorder)), f.answer)
		})
		t.Run("a floor it meets", func(t *testing.T) {
			check(t, operationClient(namedModel(f.reply), underFloor(schemaflux.ContractSchemaAndInvariantChecked)), f.answer)
		})
		t.Run("a decision provider", func(t *testing.T) {
			ctx, _ := decidingPair(mw.DecisionOptions{}, answeringWith(f.reply), schemafluxtest.NewDecider(f.typed...))
			check(t, ctx, f.typedAnswer)
		})
		t.Run("a decision provider and a recorder", func(t *testing.T) {
			ctx, recorder := decidingPair(mw.DecisionOptions{}, answeringWith(f.reply), schemafluxtest.NewDecider(f.typed...))
			check(t, ctx, f.typedAnswer)
			if len(recorder.Records()) != 1 {
				t.Errorf("%d Records", len(recorder.Records()))
			}
		})
	})
}
