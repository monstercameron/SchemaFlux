package schemafluxtest_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/schemafluxtest"
)

func deciderRequest(questions ...schemaflux.DecisionQuestion) schemaflux.CompletionRequest {
	return schemaflux.CompletionRequest{
		Model:    "gpt-6-luna",
		Decision: &schemaflux.TypedDecision{State: `"Please refund my order."`, Questions: questions},
	}
}

func TestDeciderAnswersAsItWasScripted(t *testing.T) {
	yesNo := schemaflux.DecisionQuestion{Kind: schemaflux.DecisionYesNo, Ask: "Is it?"}
	choice := schemaflux.DecisionQuestion{Kind: schemaflux.DecisionChoice, Ask: "Which?", Options: []string{"billing", "technical"}}
	scale := schemaflux.DecisionQuestion{Kind: schemaflux.DecisionScale, Ask: "How?", Low: 1, High: 5}
	down := errors.New("the decider is down")
	usage := schemaflux.TokenUsage{PromptTokens: 300, CompletionTokens: 20, TotalTokens: 320}

	cases := []struct {
		name    string
		decider *schemafluxtest.Decider
		req     schemaflux.CompletionRequest
		want    []schemaflux.DecisionAnswer
		wantErr error
		refused bool
	}{
		{"a yes", schemafluxtest.NewDecider().Answer(schemafluxtest.Yes(0.9)), deciderRequest(yesNo),
			[]schemaflux.DecisionAnswer{{Kind: schemaflux.DecisionYesNo, Yes: true, ModelConfidence: 0.9, ConfidenceReported: true}}, nil, false},
		{"a no", schemafluxtest.NewDecider().Answer(schemafluxtest.No(0.8)), deciderRequest(yesNo),
			[]schemaflux.DecisionAnswer{{Kind: schemaflux.DecisionYesNo, ModelConfidence: 0.8, ConfidenceReported: true}}, nil, false},
		{"a label", schemafluxtest.NewDecider().Answer(schemafluxtest.Chose("technical", 0.7)), deciderRequest(choice),
			[]schemaflux.DecisionAnswer{{Kind: schemaflux.DecisionChoice, Label: "technical", ModelConfidence: 0.7, ConfidenceReported: true}}, nil, false},
		{"a level", schemafluxtest.NewDecider().Answer(schemafluxtest.Level(4, 0.6)), deciderRequest(scale),
			[]schemaflux.DecisionAnswer{{Kind: schemaflux.DecisionScale, Level: 4, ModelConfidence: 0.6, ConfidenceReported: true}}, nil, false},
		{"three answers in order", schemafluxtest.NewDecider().Answer(schemafluxtest.Yes(1), schemafluxtest.Chose("billing", 1), schemafluxtest.Level(1, 1)), deciderRequest(yesNo, choice, scale),
			[]schemaflux.DecisionAnswer{schemafluxtest.Yes(1), schemafluxtest.Chose("billing", 1), schemafluxtest.Level(1, 1)}, nil, false},
		{"nothing scripted answers nothing", schemafluxtest.NewDecider(), deciderRequest(yesNo), nil, nil, false},
		{"an answer from the decision", schemafluxtest.NewDecider().AnswerFunc(func(_ int, decision schemaflux.TypedDecision) ([]schemaflux.DecisionAnswer, error) {
			return []schemaflux.DecisionAnswer{schemafluxtest.Chose(decision.Questions[0].Options[1], 0.5)}, nil
		}), deciderRequest(choice), []schemaflux.DecisionAnswer{schemafluxtest.Chose("technical", 0.5)}, nil, false},
		{"an error from the answer function", schemafluxtest.NewDecider().AnswerFunc(func(int, schemaflux.TypedDecision) ([]schemaflux.DecisionAnswer, error) {
			return nil, down
		}), deciderRequest(yesNo), nil, down, false},
		{"a scripted failure", schemafluxtest.NewDecider().Answer(schemafluxtest.Yes(1)).Fail(down), deciderRequest(yesNo), nil, down, false},
		{"Answer clears a failure", schemafluxtest.NewDecider().Fail(down).Answer(schemafluxtest.Yes(1)), deciderRequest(yesNo),
			[]schemaflux.DecisionAnswer{schemafluxtest.Yes(1)}, nil, false},
		{"a request with no decision is refused", schemafluxtest.NewDecider().Answer(schemafluxtest.Yes(1)),
			schemaflux.CompletionRequest{SystemPrompt: "Summarize.", UserPrompt: "text"}, nil, nil, true},
		{"a decision past the limits is refused", schemafluxtest.NewDecider().Limits(schemaflux.DecisionLimits{MaxLevels: 3}).Answer(schemafluxtest.Level(1, 1)),
			deciderRequest(scale), nil, nil, true},
		{"a decision within the limits is answered", schemafluxtest.NewDecider().Limits(schemaflux.DecisionLimits{MaxLevels: 5, MaxOptions: 2}).Answer(schemafluxtest.Level(1, 1)),
			deciderRequest(scale), []schemaflux.DecisionAnswer{schemafluxtest.Level(1, 1)}, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decider := tc.decider.WithUsage(usage)
			resp, err := decider.Complete(context.Background(), tc.req)
			switch {
			case tc.refused:
				if schemaflux.KindOf(err).String() != "unsupported capability" && !errors.Is(err, schemaflux.ErrUnsupportedCapability) {
					t.Fatalf("error %v, want an unsupported capability", err)
				}
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error %v, want %v", err, tc.wantErr)
				}
			default:
				if err != nil {
					t.Fatalf("Complete: %v", err)
				}
				if !reflect.DeepEqual(resp.DecisionAnswers, tc.want) && (len(resp.DecisionAnswers) != 0 || len(tc.want) != 0) {
					t.Errorf("answered %+v, want %+v", resp.DecisionAnswers, tc.want)
				}
				if resp.Content != "" || resp.Provider != "local" || resp.Model != "gpt-6-luna" || resp.Usage != usage {
					t.Errorf("the response is %+v", resp)
				}
			}
			// Refused or not, the request is kept: a test can show what reached it.
			if decider.CallCount() != 1 || !reflect.DeepEqual(decider.Requests()[0], tc.req) {
				t.Errorf("the decider kept %d requests", decider.CallCount())
			}
		})
	}
}

func TestDeciderFailsFirstThenAnswersAndNamesItself(t *testing.T) {
	down := errors.New("the decider is down")
	decider := schemafluxtest.NewDecider().Answer(schemafluxtest.Yes(0.9)).FailFirst(2, down).As("jev", "jev-1.13.0")
	req := deciderRequest(schemaflux.DecisionQuestion{Kind: schemaflux.DecisionYesNo, Ask: "Is it?"})

	for call := 1; call <= 4; call++ {
		resp, err := decider.Complete(context.Background(), req)
		if failing := call <= 2; failing != (err != nil) {
			t.Fatalf("call %d: error %v", call, err)
		}
		if err == nil && (resp.Provider != "jev" || resp.Model != "jev-1.13.0" || len(resp.DecisionAnswers) != 1) {
			t.Errorf("call %d: %+v", call, resp)
		}
	}
	if decider.Name() != "jev" || decider.ModelSentFor("gpt-6-luna") != "jev-1.13.0" {
		t.Errorf("named %s, sending %s", decider.Name(), decider.ModelSentFor("gpt-6-luna"))
	}
	if plain := schemafluxtest.NewDecider(); plain.Name() != "local" || plain.ModelSentFor("gpt-6-luna") != "gpt-6-luna" {
		t.Errorf("a new decider is named %s and sends %s", plain.Name(), plain.ModelSentFor("gpt-6-luna"))
	}
	if retries, backoff := decider.RetryPolicy(); retries != 0 || backoff != 0 || decider.EstimateCost(req) != 0 {
		t.Error("a double retries or costs something")
	}
	if limits := decider.Limits(schemaflux.DecisionLimits{MaxOptions: 9}).DecisionLimits(); limits.MaxOptions != 9 {
		t.Errorf("limits %+v", limits)
	}

	// A context that is over is reported, so a test can check that its
	// cancellation reached the decider.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := decider.Complete(ctx, req); !errors.Is(err, context.Canceled) {
		t.Errorf("error %v on a cancelled context", err)
	}
	// A nil error scripts a failure all the same.
	if _, err := schemafluxtest.NewDecider().Fail(nil).Complete(context.Background(), req); err == nil {
		t.Error("Fail(nil) did not fail")
	}
	if _, err := schemafluxtest.NewDecider().FailFirst(1, nil).Complete(context.Background(), req); err == nil {
		t.Error("FailFirst(1, nil) did not fail")
	}
}
