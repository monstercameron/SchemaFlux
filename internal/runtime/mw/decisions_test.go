package mw

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// scripted is one half of a pair: a provider that answers from a function and
// keeps the requests it was sent.
type scripted struct {
	name   string
	model  string // what ModelSentFor answers, when set
	limits *llm.DecisionLimits
	answer func(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error)

	mu       sync.Mutex
	requests []llm.CompletionRequest
}

func (s *scripted) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()
	return s.answer(ctx, req)
}
func (s *scripted) Name() string                               { return s.name }
func (s *scripted) EstimateCost(llm.CompletionRequest) float64 { return 0.25 }
func (s *scripted) RetryPolicy() (int, time.Duration)          { return 2, 7 * time.Millisecond }
func (s *scripted) ModelSentFor(requested string) string {
	if s.model != "" {
		return s.model
	}
	return requested
}
func (s *scripted) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

// limited is a scripted decider that declares limits.
type limited struct{ *scripted }

func (l limited) DecisionLimits() llm.DecisionLimits { return *l.limits }

func generatorAnswering(content string) *scripted {
	return &scripted{name: "openai", answer: func(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
		return llm.CompletionResponse{Content: content, Provider: "openai", Model: req.Model, FinishReason: "stop",
			Usage: types.TokenUsage{PromptTokens: 400, CompletionTokens: 20, TotalTokens: 420}}, nil
	}}
}

func deciderAnswering(answers ...llm.DecisionAnswer) *scripted {
	return &scripted{name: "jev", model: "jev-latest", answer: func(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
		return llm.CompletionResponse{Provider: "jev", Model: "jev-1.13.0", DecisionAnswers: answers,
			Usage: types.TokenUsage{PromptTokens: 300, CompletionTokens: 40, TotalTokens: 340}}, nil
	}}
}

// answersTo is a valid typed answer to each question of decision, each reported
// at confidence: yes to a yes or no, the first option of a choice, the low end of
// a scale.
func answersTo(decision *llm.Decision, confidence float64) []llm.DecisionAnswer {
	answers := make([]llm.DecisionAnswer, len(decision.Questions))
	for i, question := range decision.Questions {
		answers[i] = llm.DecisionAnswer{Kind: question.Kind, ModelConfidence: confidence, ConfidenceReported: true}
		switch question.Kind {
		case llm.DecisionYesNo:
			answers[i].Yes = true
		case llm.DecisionChoice:
			if len(question.Options) > 0 {
				answers[i].Label = question.Options[0]
			}
		case llm.DecisionScale:
			answers[i].Level = question.Low
		}
	}
	return answers
}

// deciderAnsweringEvery is a decision provider that answers whatever decision it
// is asked, every question at confidence.
func deciderAnsweringEvery(confidence float64) *scripted {
	return &scripted{name: "jev", model: "jev-latest", answer: func(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
		return llm.CompletionResponse{Provider: "jev", Model: "jev-1.13.0", DecisionAnswers: answersTo(req.Decision, confidence),
			Usage: types.TokenUsage{PromptTokens: 300, CompletionTokens: 40, TotalTokens: 340}}, nil
	}}
}

func deciderFailing(err error) *scripted {
	return &scripted{name: "jev", model: "jev-latest", answer: func(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
		return llm.CompletionResponse{}, err
	}}
}

func yes(confidence float64) llm.DecisionAnswer {
	return llm.DecisionAnswer{Kind: llm.DecisionYesNo, Yes: true, ModelConfidence: confidence, ConfidenceReported: true}
}

// decisionRequest is a request as Is builds it: the prompts for a generating
// model, and the same question typed.
func decisionRequest(questions ...llm.DecisionQuestion) llm.CompletionRequest {
	if len(questions) == 0 {
		questions = []llm.DecisionQuestion{{Kind: llm.DecisionYesNo, Ask: "Is the customer asking for a refund?"}}
	}
	return llm.CompletionRequest{
		Model:          "gpt-6-luna",
		SystemPrompt:   "You answer questions about a subject.",
		UserPrompt:     "Questions:\nq1 (yes or no): \"Is the customer asking for a refund?\"\n\nSubject:\n\"Please refund my order.\"",
		ResponseFormat: "json",
		SchemaName:     "assessment",
		JSONSchema:     map[string]any{"type": "object"},
		MaxTokens:      2000,
		Decision:       &llm.Decision{State: `"Please refund my order."`, Questions: questions},
	}
}

const generatorJSON = `{"q1":false}`

func TestDecisionsRoutesByWhetherTheRequestDecides(t *testing.T) {
	text := llm.CompletionRequest{Model: "gpt-6-luna", SystemPrompt: "Summarize the text.", UserPrompt: "A long document.", MaxTokens: 4000}
	structure := llm.CompletionRequest{Model: "gpt-6-luna", SystemPrompt: "Return a JSON object.", UserPrompt: "x", ResponseFormat: "json", JSONSchema: map[string]any{"type": "object"}}
	empty := decisionRequest()
	empty.Decision = &llm.Decision{State: `"x"`}

	cases := []struct {
		name       string
		req        llm.CompletionRequest
		options    DecisionOptions
		toDecider  int
		toGenerate int
		typed      bool
	}{
		{"a request for text goes to the generator", text, DecisionOptions{}, 0, 1, false},
		{"a request for a structure goes to the generator", structure, DecisionOptions{Floor: 0.9}, 0, 1, false},
		{"a decision with no question goes to the generator", empty, DecisionOptions{}, 0, 1, false},
		{"a decision goes to the decider", decisionRequest(), DecisionOptions{}, 1, 0, true},
		{"a decision above the floor is one call", decisionRequest(), DecisionOptions{Floor: 0.9}, 1, 0, true},
		{"a probability equal to the floor is not below it", decisionRequest(), DecisionOptions{Floor: 0.97}, 1, 0, true},
		{"a decision below the floor is two calls", decisionRequest(), DecisionOptions{Floor: 0.98}, 1, 1, false},
		{"a floor of one sends everything short of certain on", decisionRequest(), DecisionOptions{Floor: 1}, 1, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			generator, decider := generatorAnswering(generatorJSON), deciderAnswering(yes(0.97))
			pair := Decisions(generator, decider, tc.options)
			resp, err := pair.Complete(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if decider.calls() != tc.toDecider || generator.calls() != tc.toGenerate {
				t.Errorf("%d calls to the decider and %d to the generator, want %d and %d", decider.calls(), generator.calls(), tc.toDecider, tc.toGenerate)
			}
			if typed := len(resp.DecisionAnswers) > 0; typed != tc.typed {
				t.Errorf("the answer is typed: %v, want %v", typed, tc.typed)
			}
			if tc.toGenerate == 1 && !reflect.DeepEqual(generator.requests[0], tc.req) {
				t.Errorf("the generator was not sent the request as it was:\n got %+v\nwant %+v", generator.requests[0], tc.req)
			}
			wantEscalated := tc.toDecider == 1 && tc.toGenerate == 1
			if (resp.DecisionEscalation != nil) != wantEscalated {
				t.Errorf("escalation %+v, want one: %v", resp.DecisionEscalation, wantEscalated)
			}
		})
	}
}

func TestDecisionsBelowTheFloorTheGeneratorsAnswerIsTheAnswer(t *testing.T) {
	scale := llm.DecisionQuestion{Kind: llm.DecisionScale, Ask: "How urgent?", Low: 1, High: 5}
	yesNo := llm.DecisionQuestion{Kind: llm.DecisionYesNo, Ask: "Is it?"}
	unreported := llm.DecisionAnswer{Kind: llm.DecisionYesNo, Yes: true}
	level := llm.DecisionAnswer{Kind: llm.DecisionScale, Level: 4, ModelConfidence: 0.41, ConfidenceReported: true}

	cases := []struct {
		name       string
		questions  []llm.DecisionQuestion
		answers    []llm.DecisionAnswer
		floor      float64
		escalated  bool
		confidence float64
		reported   bool
	}{
		{"one answer below", []llm.DecisionQuestion{yesNo}, []llm.DecisionAnswer{yes(0.55)}, 0.7, true, 0.55, true},
		{"one answer just below", []llm.DecisionQuestion{yesNo}, []llm.DecisionAnswer{yes(0.6999)}, 0.7, true, 0.6999, true},
		{"one answer above", []llm.DecisionQuestion{yesNo}, []llm.DecisionAnswer{yes(0.71)}, 0.7, false, 0, false},
		{"the least sure of three decides", []llm.DecisionQuestion{yesNo, scale, yesNo}, []llm.DecisionAnswer{yes(0.99), level, yes(0.95)}, 0.7, true, 0.41, true},
		{"three answers all above", []llm.DecisionQuestion{yesNo, yesNo, yesNo}, []llm.DecisionAnswer{yes(0.99), yes(0.8), yes(0.95)}, 0.7, false, 0, false},
		{"an answer with no probability cannot clear a floor", []llm.DecisionQuestion{yesNo}, []llm.DecisionAnswer{unreported}, 0.7, true, 0, false},
		{"one of two with no probability", []llm.DecisionQuestion{yesNo, yesNo}, []llm.DecisionAnswer{yes(0.99), unreported}, 0.7, true, 0, false},
		{"with no floor an answer with no probability is the answer", []llm.DecisionQuestion{yesNo}, []llm.DecisionAnswer{unreported}, 0, false, 0, false},
		{"with no floor a probability of nothing is the answer", []llm.DecisionQuestion{yesNo}, []llm.DecisionAnswer{yes(0)}, 0, false, 0, false},
		{"a probability of nothing under any floor", []llm.DecisionQuestion{yesNo}, []llm.DecisionAnswer{yes(0)}, 0.01, true, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			generator, decider := generatorAnswering(generatorJSON), deciderAnswering(tc.answers...)
			req := decisionRequest(tc.questions...)
			resp, err := Decisions(generator, decider, DecisionOptions{Floor: tc.floor}).Complete(context.Background(), req)
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if !tc.escalated {
				if generator.calls() != 0 || resp.DecisionEscalation != nil || resp.Provider != "jev" || len(resp.DecisionAnswers) != len(tc.answers) {
					t.Errorf("the decider's answer was not the answer: %+v, %d calls to the generator", resp, generator.calls())
				}
				return
			}
			if decider.calls() != 1 || generator.calls() != 1 {
				t.Fatalf("%d calls to the decider and %d to the generator, want one each", decider.calls(), generator.calls())
			}
			if resp.Content != generatorJSON || resp.Provider != "openai" || len(resp.DecisionAnswers) != 0 {
				t.Errorf("the answer is not the generator's: %+v", resp)
			}
			want := &llm.DecisionEscalation{Provider: "jev", Model: "jev-1.13.0", Reason: llm.DecisionEscalatedBelowFloor, ModelConfidence: tc.confidence, ConfidenceReported: tc.reported}
			if !reflect.DeepEqual(resp.DecisionEscalation, want) {
				t.Errorf("the record says %+v, want %+v", resp.DecisionEscalation, want)
			}
			if !reflect.DeepEqual(generator.requests[0], req) {
				t.Errorf("the generator was asked something other than the same request")
			}
		})
	}
}

func TestDecisionsWhenTheDeciderFailsOrRefuses(t *testing.T) {
	apiError := func(status int) error {
		return llm.NewAPIError("jev", "jev-latest", status, `{"error":{"message":"x"}}`)
	}
	unsupported := &types.OperationError{Kind: types.KindUnsupportedCapability, Op: "llm.decide", Provider: "jev", Message: "more options than it takes"}

	cases := []struct {
		name       string
		err        error
		options    DecisionOptions
		toGenerate int
		reason     string
		wantErr    types.ErrorKind
	}{
		{"unavailable falls to the generator", apiError(503), DecisionOptions{}, 1, llm.DecisionEscalatedUnavailable, types.KindUnknown},
		{"overloaded falls to the generator", apiError(529), DecisionOptions{Floor: 0.7}, 1, llm.DecisionEscalatedUnavailable, types.KindUnknown},
		{"rate limited falls to the generator", &llm.RateLimitError{APIError: llm.NewAPIError("jev", "jev-latest", 429, ""), RetryAfter: time.Second}, DecisionOptions{}, 1, llm.DecisionEscalatedUnavailable, types.KindUnknown},
		{"a timeout of the decider's own falls to the generator", &types.OperationError{Kind: types.KindTimeout, Op: "llm.decide"}, DecisionOptions{}, 1, llm.DecisionEscalatedUnavailable, types.KindUnknown},
		{"unavailable with failover off is the error", apiError(503), DecisionOptions{NoFailover: true}, 0, "", types.KindProviderUnavailable},
		{"rate limited with failover off is the error", apiError(429), DecisionOptions{NoFailover: true}, 0, "", types.KindRateLimited},
		{"a refusal for capability goes to the generator", unsupported, DecisionOptions{}, 1, llm.DecisionEscalatedUnsupported, types.KindUnknown},
		{"a refusal for capability goes to the generator with failover off too", unsupported, DecisionOptions{NoFailover: true}, 1, llm.DecisionEscalatedUnsupported, types.KindUnknown},
		{"a rejected key is the error", apiError(401), DecisionOptions{}, 0, "", types.KindAuthentication},
		{"a request the API rejects is the error", apiError(400), DecisionOptions{}, 0, "", types.KindInvalidRequest},
		{"an answer that is not the protocol's is the error", &types.OperationError{Kind: types.KindMalformedOutput, Op: "llm.decide"}, DecisionOptions{}, 0, "", types.KindMalformedOutput},
		{"a cancellation is the error", context.Canceled, DecisionOptions{}, 0, "", types.KindCanceled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			generator, decider := generatorAnswering(generatorJSON), deciderFailing(tc.err)
			resp, err := Decisions(generator, decider, tc.options).Complete(context.Background(), decisionRequest())
			if generator.calls() != tc.toGenerate || decider.calls() != 1 {
				t.Errorf("%d calls to the generator and %d to the decider, want %d and 1", generator.calls(), decider.calls(), tc.toGenerate)
			}
			if tc.wantErr != types.KindUnknown {
				if kind := llm.Classify(err); kind != tc.wantErr || !errors.Is(err, tc.err) {
					t.Errorf("error %v (kind %v), want the decider's own, of kind %v", err, kind, tc.wantErr)
				}
				if !reflect.DeepEqual(resp, llm.CompletionResponse{}) {
					t.Errorf("an error came with %+v beside it", resp)
				}
				return
			}
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			want := &llm.DecisionEscalation{Provider: "jev", Model: "jev-latest", Reason: tc.reason}
			if resp.Content != generatorJSON || !reflect.DeepEqual(resp.DecisionEscalation, want) {
				t.Errorf("answered %q with the record %+v, want the generator's answer and %+v", resp.Content, resp.DecisionEscalation, want)
			}
		})
	}
}

func TestDecisionsSendsTheDeciderOnlyWhatItDeclaresItTakes(t *testing.T) {
	labels := []string{"a", "b", "c", "d"}
	limits := &llm.DecisionLimits{MaxOptions: 3, MaxLevels: 10, MaxStateTokens: 20, MaxQuestions: 2}
	yesNo := llm.DecisionQuestion{Kind: llm.DecisionYesNo, Ask: "Is it?"}
	withState := func(state string) llm.CompletionRequest {
		req := decisionRequest(yesNo)
		req.Decision = &llm.Decision{State: state, Questions: []llm.DecisionQuestion{yesNo}}
		return req
	}

	cases := []struct {
		name      string
		req       llm.CompletionRequest
		toDecider int
	}{
		{"within every limit", decisionRequest(yesNo), 1},
		{"a choice at the limit", decisionRequest(llm.DecisionQuestion{Kind: llm.DecisionChoice, Ask: "Which?", Options: labels[:3]}), 1},
		{"a scale at the limit", decisionRequest(llm.DecisionQuestion{Kind: llm.DecisionScale, Ask: "How?", Low: 1, High: 10}), 1},
		{"two questions", decisionRequest(yesNo, yesNo), 1},
		{"more options than its limit", decisionRequest(llm.DecisionQuestion{Kind: llm.DecisionChoice, Ask: "Which?", Options: labels}), 0},
		{"a scale with more levels than it takes", decisionRequest(llm.DecisionQuestion{Kind: llm.DecisionScale, Ask: "How?", Low: 1, High: 11}), 0},
		{"a scale of a hundred levels", decisionRequest(llm.DecisionQuestion{Kind: llm.DecisionScale, Ask: "How?", Low: 1, High: 100}), 0},
		{"a state longer than its context", withState(`"` + string(make([]byte, 100)) + `"`), 0},
		{"more questions than it takes", decisionRequest(yesNo, yesNo, yesNo), 0},
		{"a question it cannot be asked at all", decisionRequest(llm.DecisionQuestion{Ask: "?"}), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			generator := generatorAnswering(generatorJSON)
			inner := deciderAnsweringEvery(0.99)
			inner.limits = limits
			pair := Decisions(generator, limited{inner}, DecisionOptions{NoFailover: true})
			resp, err := pair.Complete(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if inner.calls() != tc.toDecider || generator.calls() != 1-tc.toDecider {
				t.Errorf("%d calls to the decider and %d to the generator", inner.calls(), generator.calls())
			}
			// The decider was not asked, so nothing says it was asked first.
			if resp.DecisionEscalation != nil {
				t.Errorf("an escalation is recorded for a decision the decider was never sent: %+v", resp.DecisionEscalation)
			}
			if tc.toDecider == 0 && !reflect.DeepEqual(generator.requests[0], tc.req) {
				t.Error("the generator was not sent the request as it was")
			}
		})
	}
}

func TestDecisionsAGeneratorThatFailsAfterALowAnswerIsAnErrorWithNothingBesideIt(t *testing.T) {
	down := llm.NewAPIError("openai", "gpt-6-luna", 503, "")
	cases := []struct {
		name    string
		decider *scripted
		options DecisionOptions
		err     error
	}{
		{"after an answer below the floor", deciderAnswering(yes(0.4)), DecisionOptions{Floor: 0.7}, down},
		{"after an answer with no probability", deciderAnswering(llm.DecisionAnswer{Kind: llm.DecisionYesNo, Yes: true}), DecisionOptions{Floor: 0.7}, down},
		{"after the decider was down", deciderFailing(llm.NewAPIError("jev", "jev-latest", 503, "")), DecisionOptions{}, down},
		{"after the decider refused", deciderFailing(&types.OperationError{Kind: types.KindUnsupportedCapability}), DecisionOptions{}, down},
		{"a generator that rejects the request", deciderAnswering(yes(0.4)), DecisionOptions{Floor: 0.7}, llm.NewAPIError("openai", "gpt-6-luna", 400, "")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			generator := &scripted{name: "openai", answer: func(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
				return llm.CompletionResponse{}, tc.err
			}}
			resp, err := Decisions(generator, tc.decider, tc.options).Complete(context.Background(), decisionRequest())
			if !errors.Is(err, tc.err) {
				t.Fatalf("error %v, want the generator's", err)
			}
			if !reflect.DeepEqual(resp, llm.CompletionResponse{}) {
				t.Errorf("the low-confidence answer, or anything else, came back beside the error: %+v", resp)
			}
			if generator.calls() != 1 || tc.decider.calls() != 1 {
				t.Errorf("%d calls to the generator and %d to the decider, want one each", generator.calls(), tc.decider.calls())
			}
		})
	}
}

// The order of events is held by channels: the decider reports that it was
// asked, the test acts, and only then lets the decider answer.
func TestDecisionsCancelledBetweenTheTwoCallsAsksTheGeneratorNothing(t *testing.T) {
	cases := []struct {
		name   string
		answer func() (llm.CompletionResponse, error)
	}{
		{"after an answer below the floor", func() (llm.CompletionResponse, error) {
			return llm.CompletionResponse{Provider: "jev", Model: "jev-1.13.0", DecisionAnswers: []llm.DecisionAnswer{yes(0.3)},
				Usage: types.TokenUsage{PromptTokens: 1_000_000, TotalTokens: 1_000_000}}, nil
		}},
		{"after the decider failed", func() (llm.CompletionResponse, error) {
			return llm.CompletionResponse{}, llm.NewAPIError("jev", "jev-latest", 503, "")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			asked, release := make(chan struct{}), make(chan struct{})
			decider := &scripted{name: "jev", model: "jev-latest", answer: func(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
				close(asked)
				<-release
				return tc.answer()
			}}
			generator := generatorAnswering(generatorJSON)
			budget := &chargedBudget{}
			ctx, cancel := context.WithCancel(ops.WithExecConfig(context.Background(), &ops.ExecConfig{Budget: budget}))
			defer cancel()

			type outcome struct {
				resp llm.CompletionResponse
				err  error
			}
			done := make(chan outcome, 1)
			go func() {
				resp, err := Decisions(generator, decider, DecisionOptions{Floor: 0.7}).Complete(ctx, decisionRequest())
				done <- outcome{resp, err}
			}()

			<-asked
			cancel()
			close(release)
			got := <-done

			if !errors.Is(got.err, context.Canceled) {
				t.Errorf("error %v, want the cancellation", got.err)
			}
			if generator.calls() != 0 {
				t.Errorf("the generator was asked %d times on a context that was over", generator.calls())
			}
			if !reflect.DeepEqual(got.resp, llm.CompletionResponse{}) {
				t.Errorf("an answer came back beside the cancellation: %+v", got.resp)
			}
		})
	}

	t.Run("cancelled while the generator is answering", func(t *testing.T) {
		asked := make(chan struct{})
		generator := &scripted{name: "openai", answer: func(ctx context.Context, _ llm.CompletionRequest) (llm.CompletionResponse, error) {
			close(asked)
			<-ctx.Done()
			return llm.CompletionResponse{}, ctx.Err()
		}}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := Decisions(generator, deciderAnswering(yes(0.3)), DecisionOptions{Floor: 0.7}).Complete(ctx, decisionRequest())
			done <- err
		}()
		<-asked
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("error %v, want the cancellation", err)
		}
	})
}

// chargedBudget is a client budget that keeps what it was charged.
type chargedBudget struct {
	mu     sync.Mutex
	costs  []float64
	priced []bool
}

func (b *chargedBudget) Check() error { return nil }
func (b *chargedBudget) Record(cost float64, priced bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.costs = append(b.costs, cost)
	b.priced = append(b.priced, priced)
}

// The decider's call is charged here when its answer is not the one returned:
// CallLLM charges the response it is handed, and that is the generator's.
func TestDecisionsChargesTheDecidersCallWhenItsAnswerIsNotReturned(t *testing.T) {
	million := types.TokenUsage{PromptTokens: 1_000_000, TotalTokens: 1_000_000}
	lowAnswer := func() *scripted {
		return &scripted{name: "jev", model: "jev-latest", answer: func(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
			return llm.CompletionResponse{Provider: "jev", Model: "jev-1.13.0", DecisionAnswers: []llm.DecisionAnswer{yes(0.3)}, Usage: million}, nil
		}}
	}
	failingGenerator := &scripted{name: "openai", answer: func(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
		return llm.CompletionResponse{}, llm.NewAPIError("openai", "gpt-6-luna", 503, "")
	}}

	cases := []struct {
		name      string
		generator *scripted
		decider   *scripted
		floor     float64
		charged   []float64
	}{
		{"an answer that is returned is left to the call path", generatorAnswering(generatorJSON), lowAnswer(), 0, nil},
		{"an answer above the floor is left to the call path", generatorAnswering(generatorJSON), deciderAnswering(yes(0.99)), 0.7, nil},
		{"an answer below the floor is charged at the decider's rate", generatorAnswering(generatorJSON), lowAnswer(), 0.7, []float64{0.042}},
		{"and is charged when the generator then fails", failingGenerator, lowAnswer(), 0.7, []float64{0.042}},
		{"a decider that failed sent no answer to charge", generatorAnswering(generatorJSON), deciderFailing(llm.NewAPIError("jev", "jev-latest", 503, "")), 0.7, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			budget := &chargedBudget{}
			ctx := ops.WithExecConfig(context.Background(), &ops.ExecConfig{Budget: budget})
			_, _ = Decisions(tc.generator, tc.decider, DecisionOptions{Floor: tc.floor}).Complete(ctx, decisionRequest())
			if len(budget.costs) != len(tc.charged) {
				t.Fatalf("charged %v, want %v", budget.costs, tc.charged)
			}
			for i, want := range tc.charged {
				if math.Abs(budget.costs[i]-want) > 1e-9 || !budget.priced[i] {
					t.Errorf("charge %d is %v (priced %v), want %v at the decision model's own rate", i+1, budget.costs[i], budget.priced[i], want)
				}
			}
		})
	}
}

func TestDecisionsHoldsTheDecidersRouteToTheClientsDataPolicy(t *testing.T) {
	llm.ResetCapabilityRegistryForTest()
	t.Cleanup(llm.ResetCapabilityRegistryForTest)
	llm.DeclareRoute("jev", "jev-latest", []string{"us"}, 0)
	llm.DeclareRoute("jev", "jev-eu", []string{"eu"}, 0)
	llm.DeclareRoute("jev", "jev-kept", []string{"us"}, 30)
	llm.DeclareRoute("openai", "gpt-6-luna", []string{"us"}, 0)

	policy := func(providers ...string) context.Context {
		return ops.WithExecConfig(context.Background(), &ops.ExecConfig{
			DataPolicy:         types.DataPolicy{AllowedProviders: providers, AllowedRegions: []string{"us"}},
			DataPolicyDeclared: true,
		})
	}
	cases := []struct {
		name      string
		ctx       context.Context
		sends     string // the model the decider says it sends
		options   DecisionOptions
		toDecider int
	}{
		{"no policy", context.Background(), "jev-latest", DecisionOptions{}, 1},
		{"a policy that allows both", policy("openai", "jev"), "jev-latest", DecisionOptions{}, 1},
		{"a policy that allows only the generator", policy("openai"), "jev-latest", DecisionOptions{}, 0},
		{"a policy that allows only the decider", policy("jev"), "jev-latest", DecisionOptions{}, 1},
		{"a policy that allows neither", policy("anthropic"), "jev-latest", DecisionOptions{}, 0},
		{"a policy that allows no provider at all", policy(), "jev-latest", DecisionOptions{}, 0},
		{"the decider's route runs in a region the policy does not allow", policy("openai", "jev"), "jev-eu", DecisionOptions{}, 0},
		{"the decider's route keeps what it is sent", policy("openai", "jev"), "jev-kept", DecisionOptions{}, 0},
		{"the decider's route was never declared", policy("openai", "jev"), "jev-undeclared", DecisionOptions{}, 0},
		{"the model the pair names for the decider is the route checked", policy("openai", "jev"), "", DecisionOptions{Model: "jev-eu"}, 0},
		{"and is allowed when that route is", policy("openai", "jev"), "", DecisionOptions{Model: "jev-latest"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			generator, decider := generatorAnswering(generatorJSON), deciderAnswering(yes(0.99))
			decider.model = tc.sends
			resp, err := Decisions(generator, decider, tc.options).Complete(tc.ctx, decisionRequest())
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			// What the pair does not send to the decider it sends to the
			// generator, whose own route the call path checked before the pair
			// was reached; a call with neither route allowed never gets here.
			if decider.calls() != tc.toDecider || generator.calls() != 1-tc.toDecider {
				t.Errorf("%d calls to the decider and %d to the generator, want %d and %d", decider.calls(), generator.calls(), tc.toDecider, 1-tc.toDecider)
			}
			if resp.DecisionEscalation != nil {
				t.Errorf("an escalation is recorded for a decider that was never sent the decision: %+v", resp.DecisionEscalation)
			}
		})
	}
}

func TestDecisionsNamesTheModelTheDeciderRunsOn(t *testing.T) {
	cases := []struct {
		name    string
		options DecisionOptions
		want    string
	}{
		{"with none named the request's model is passed on", DecisionOptions{}, "gpt-6-luna"},
		{"a named model replaces it for the decider", DecisionOptions{Model: "jev-1.13.0"}, "jev-1.13.0"},
		{"space around the name is dropped", DecisionOptions{Model: "  jev-preview "}, "jev-preview"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			generator := generatorAnswering(generatorJSON)
			decider := &scripted{name: "jev", answer: func(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
				// It names neither itself nor its model: the pair speaks for it.
				return llm.CompletionResponse{DecisionAnswers: []llm.DecisionAnswer{yes(0.2)}}, nil
			}}
			req := decisionRequest()
			above, err := Decisions(generator, decider, tc.options).Complete(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if decider.requests[0].Model != tc.want {
				t.Errorf("the decider was asked under %q, want %q", decider.requests[0].Model, tc.want)
			}
			if above.Provider != "jev" || above.Model != tc.want {
				t.Errorf("an answer that names nobody is put down to %s/%s, want jev/%s", above.Provider, above.Model, tc.want)
			}
			if req.Model != "gpt-6-luna" {
				t.Error("the caller's request was changed")
			}

			below, err := Decisions(generator, decider, DecisionOptions{Floor: 0.5, Model: tc.options.Model}).Complete(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if generator.requests[0].Model != "gpt-6-luna" {
				t.Errorf("the generator was asked under %q, and the decider's model is not its own", generator.requests[0].Model)
			}
			if below.DecisionEscalation == nil || below.DecisionEscalation.Model != tc.want {
				t.Errorf("the record names the decider's model as %+v, want %q", below.DecisionEscalation, tc.want)
			}
		})
	}
}

func TestDecisionsRefusesAFloorThatIsNotOne(t *testing.T) {
	for _, floor := range []float64{-0.1, 1.01, 70, math.NaN(), math.Inf(1), math.Inf(-1)} {
		generator, decider := generatorAnswering(generatorJSON), deciderAnswering(yes(0.99))
		pair := Decisions(generator, decider, DecisionOptions{Floor: floor})

		_, err := pair.Complete(context.Background(), decisionRequest())
		if types.KindOf(err) != types.KindConfiguration {
			t.Errorf("a floor of %v: error %v, want a configuration error", floor, err)
		}
		if decider.calls() != 0 || generator.calls() != 0 {
			t.Errorf("a floor of %v: something was sent", floor)
		}
		// What does not decide is not held up by it.
		if _, err := pair.Complete(context.Background(), llm.CompletionRequest{Model: "gpt-6-luna", UserPrompt: "summarize"}); err != nil || generator.calls() != 1 {
			t.Errorf("a floor of %v: a request for text was refused: %v", floor, err)
		}
	}
	for _, floor := range []float64{0, 0.5, 1} {
		if _, err := Decisions(generatorAnswering(generatorJSON), deciderAnswering(yes(1)), DecisionOptions{Floor: floor}).Complete(context.Background(), decisionRequest()); err != nil {
			t.Errorf("a floor of %v was refused: %v", floor, err)
		}
	}
}

func TestDecisionsIsItsGeneratorToTheCallPath(t *testing.T) {
	generator, decider := generatorAnswering(generatorJSON), deciderAnswering(yes(0.9))
	generator.model = "the-model-the-generator-sends"
	pair := Decisions(generator, decider, DecisionOptions{})

	if pair.Name() != "openai" {
		t.Errorf("named %q, want the generator's name", pair.Name())
	}
	if retries, backoff := pair.RetryPolicy(); retries != 2 || backoff != 7*time.Millisecond {
		t.Errorf("retry policy %d, %v", retries, backoff)
	}
	if pair.EstimateCost(llm.CompletionRequest{}) != 0.25 {
		t.Error("the estimate is not the generator's")
	}
	if got := llm.ModelSentBy(pair, "gpt-6-luna"); got != "the-model-the-generator-sends" {
		t.Errorf("ModelSentBy = %q, want what the generator sends", got)
	}
	if unwrapped, ok := pair.(interface{ Unwrap() Handler }); !ok || unwrapped.Unwrap() != Handler(generator) {
		t.Error("the pair does not unwrap to its generator")
	}
	if _, declared := llm.DecisionLimitsOf(pair); declared {
		t.Error("the pair declares decision limits, and it takes any request")
	}

	// A pair with one half missing is the other half.
	if got := Decisions(generator, nil, DecisionOptions{}); got != Handler(generator) {
		t.Error("with no decider the pair is not the generator")
	}
	if got := Decisions(nil, decider, DecisionOptions{}); got != Handler(decider) {
		t.Error("with no generator the pair is not the decider")
	}
	if got := Decisions(nil, nil, DecisionOptions{}); got != nil {
		t.Error("a pair of nothing is something")
	}
}

// decisionProbe is a decision provider that keeps the decision of every request
// it is asked to complete and of every request it is asked to estimate, so that
// a test can see whether a middleware handed on the decision it was given.
type decisionProbe struct {
	name string
	// failures are returned, one to a call, before the probe answers.
	failures []error
	// first, when set, is run by the first call before it does anything else, and
	// its error is that call's. It lets a test hold a copy until it is cancelled.
	first func(ctx context.Context) error
	reply llm.CompletionResponse

	mu        sync.Mutex
	completed []*llm.Decision
	estimated []*llm.Decision
	calls     int
}

func (p *decisionProbe) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.mu.Lock()
	n := p.calls
	p.calls++
	p.completed = append(p.completed, req.Decision)
	p.mu.Unlock()
	if n == 0 && p.first != nil {
		if err := p.first(ctx); err != nil {
			return llm.CompletionResponse{}, err
		}
	}
	if n < len(p.failures) && p.failures[n] != nil {
		return llm.CompletionResponse{}, p.failures[n]
	}
	return p.reply, nil
}

func (p *decisionProbe) Name() string { return p.name }

func (p *decisionProbe) EstimateCost(req llm.CompletionRequest) float64 {
	p.mu.Lock()
	p.estimated = append(p.estimated, req.Decision)
	p.mu.Unlock()
	return 0.001
}

func (p *decisionProbe) RetryPolicy() (int, time.Duration) { return 0, 0 }

func (p *decisionProbe) decisions() []*llm.Decision {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*llm.Decision(nil), p.completed...)
}

func probeReply() llm.CompletionResponse {
	return llm.CompletionResponse{
		Provider: "jev", Model: "jev-1.13.0",
		Usage: types.TokenUsage{PromptTokens: 300, CompletionTokens: 40, TotalTokens: 340},
		DecisionAnswers: []llm.DecisionAnswer{
			{Kind: llm.DecisionYesNo, Yes: true, ModelConfidence: 0.91, ConfidenceReported: true},
			{Kind: llm.DecisionChoice, Label: "shipping", ModelConfidence: 0.62, ConfidenceReported: true},
			{Kind: llm.DecisionScale, Level: 4},
		},
		DecisionEscalation: &llm.DecisionEscalation{Provider: "jev", Model: "jev-latest", Reason: llm.DecisionEscalatedBelowFloor, ModelConfidence: 0.4, ConfidenceReported: true},
	}
}

func probeDecision() *llm.Decision {
	return &llm.Decision{
		State: `"Please refund my order."`, Guidance: "Refunds are allowed for thirty days.",
		Questions: []llm.DecisionQuestion{
			{Kind: llm.DecisionYesNo, Ask: "Is the customer asking for a refund?"},
			{Kind: llm.DecisionChoice, Ask: "Which team?", Options: []string{"billing", "shipping"}, Criteria: []string{"money", ""}},
			{Kind: llm.DecisionScale, Ask: "How urgent?", Low: 1, High: 5},
		},
	}
}

// A request's decision and a response's typed answers and escalation go through
// every middleware as they came: the same decision to the provider, the same
// answers and record back, and a decision that is retried, hedged or failed over
// is the same decision on every attempt.
func TestMiddlewaresPassADecisionAndItsAnswersThrough(t *testing.T) {
	down := llm.NewAPIError("jev", "jev-latest", 503, "")
	noSleep := func(context.Context, time.Duration) error { return nil }

	cases := []struct {
		name     string
		failures []error // the primary probe's, before it answers
		build    func(primary, alternate *decisionProbe) Handler
		attempts int // requests the primary probe is expected to see
		failover int // requests the alternate is expected to see
	}{
		{"retry, one attempt", nil, func(p, _ *decisionProbe) Handler {
			return Retry(WithMaxAttempts(3), WithBaseDelay(time.Millisecond))(p)
		}, 1, 0},
		{"retry, after two failures", []error{down, down}, func(p, _ *decisionProbe) Handler {
			retried := Retry(WithMaxAttempts(3), WithBaseDelay(time.Millisecond))(p)
			retried.(*retryProvider).sleep = noSleep
			return retried
		}, 3, 0},
		{"rate limit", nil, func(p, _ *decisionProbe) Handler { return RateLimit(10, time.Second)(p) }, 1, 0},
		{"rate limit that rejects", nil, func(p, _ *decisionProbe) Handler { return RateLimit(10, time.Second, Reject())(p) }, 1, 0},
		{"bulkhead", nil, func(p, _ *decisionProbe) Handler { return Bulkhead(2)(p) }, 1, 0},
		{"bulkhead that rejects when full", nil, func(p, _ *decisionProbe) Handler { return Bulkhead(2, RejectWhenFull())(p) }, 1, 0},
		{"circuit breaker", nil, func(p, _ *decisionProbe) Handler {
			return CircuitBreaker(ops.CircuitBreakerConfig{FailureThreshold: 5})(p)
		}, 1, 0},
		{"circuit breaker keyed by model", nil, func(p, _ *decisionProbe) Handler {
			return CircuitBreaker(ops.CircuitBreakerConfig{FailureThreshold: 5}, KeyByModel())(p)
		}, 1, 0},
		{"budget", nil, func(p, _ *decisionProbe) Handler { return Budget(1)(p) }, 1, 0},
		{"budget with no cost function", nil, func(p, _ *decisionProbe) Handler { return Budget(1, WithBudgetCostFunc(nil))(p) }, 1, 0},
		{"fallback, the primary answers", nil, func(p, alt *decisionProbe) Handler {
			return Fallback([]FallbackRoute{{Handler: alt}})(p)
		}, 1, 0},
		{"fallback, the primary fails", []error{down}, func(p, alt *decisionProbe) Handler {
			return Fallback([]FallbackRoute{{Handler: alt}})(p)
		}, 1, 1},
		{"hedge that does not need its second copy", nil, func(p, _ *decisionProbe) Handler { return Hedge(time.Hour)(p) }, 1, 0},
		{"hedge with the delay off", nil, func(p, _ *decisionProbe) Handler { return Hedge(0)(p) }, 1, 0},
		{"cache, a miss", nil, func(p, _ *decisionProbe) Handler { return Cache()(p) }, 1, 0},
		{"redaction with nothing to redact", nil, func(p, _ *decisionProbe) Handler { return RedactEgress()(p) }, 1, 0},
		{"the pair, the decider answers", nil, func(p, _ *decisionProbe) Handler {
			return Decisions(generatorAnswering(generatorJSON), p, DecisionOptions{})
		}, 1, 0},
		{"a chain of all of them", nil, func(p, _ *decisionProbe) Handler {
			return Chain(p, RedactEgress(), Cache(), Retry(WithMaxAttempts(2)), Budget(1), RateLimit(10, time.Second), Bulkhead(2),
				CircuitBreaker(ops.CircuitBreakerConfig{FailureThreshold: 5}), Hedge(time.Hour))
		}, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reply := probeReply()
			primary := &decisionProbe{name: "jev", failures: tc.failures, reply: reply}
			alternate := &decisionProbe{name: "alt", reply: reply}
			decision := probeDecision()
			req := llm.CompletionRequest{Model: "jev-latest", UserPrompt: "Questions: ...", Decision: decision}

			resp, err := tc.build(primary, alternate).Complete(context.Background(), req)
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if !reflect.DeepEqual(resp.DecisionAnswers, reply.DecisionAnswers) {
				t.Errorf("the answers are %+v, want %+v", resp.DecisionAnswers, reply.DecisionAnswers)
			}
			if resp.DecisionEscalation != reply.DecisionEscalation {
				t.Errorf("the escalation is %+v, want the provider's record itself, %+v", resp.DecisionEscalation, reply.DecisionEscalation)
			}
			for name, probe := range map[string]*decisionProbe{"primary": primary, "alternate": alternate} {
				want := tc.attempts
				if probe == alternate {
					want = tc.failover
				}
				seen := probe.decisions()
				if len(seen) != want {
					t.Fatalf("the %s saw %d requests, want %d", name, len(seen), want)
				}
				for i, got := range seen {
					if got != decision {
						t.Errorf("the %s was sent %p on attempt %d, not the caller's decision %p", name, got, i+1, decision)
					}
				}
			}
			if !reflect.DeepEqual(decision, probeDecision()) {
				t.Errorf("the caller's decision was changed: %+v", decision)
			}
		})
	}
}

// A copy sent late by Hedge is the same decision as the first, and the answer
// that comes back is the winner's as it was.
func TestHedgeSendsTheSameDecisionInBothCopies(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reply := probeReply()
		probe := &decisionProbe{name: "jev", reply: reply, first: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}}
		decision := probeDecision()
		resp, err := Chain(probe, Hedge(50*time.Millisecond)).Complete(context.Background(),
			llm.CompletionRequest{Model: "jev-latest", Decision: decision})
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}
		seen := probe.decisions()
		if len(seen) != 2 || seen[0] != decision || seen[1] != decision {
			t.Fatalf("the copies were sent %v, want the caller's decision twice", seen)
		}
		if !reflect.DeepEqual(resp.DecisionAnswers, reply.DecisionAnswers) || resp.DecisionEscalation != reply.DecisionEscalation {
			t.Errorf("the winner's answers came back changed: %+v", resp)
		}
	})
}

// Anything that estimates a call's cost is handed the whole request, the
// decision included: a middleware that cut it down would estimate a smaller
// call than the one made.
func TestMiddlewaresHandTheEstimatorTheWholeRequest(t *testing.T) {
	decision := probeDecision()
	req := llm.CompletionRequest{Model: "jev-latest", Decision: decision}
	builders := map[string]func(Handler) Handler{
		"retry":           func(p Handler) Handler { return Retry()(p) },
		"rate limit":      func(p Handler) Handler { return RateLimit(1, time.Second)(p) },
		"bulkhead":        func(p Handler) Handler { return Bulkhead(1)(p) },
		"circuit breaker": func(p Handler) Handler { return CircuitBreaker(ops.CircuitBreakerConfig{})(p) },
		"budget":          func(p Handler) Handler { return Budget(1)(p) },
		"fallback":        func(p Handler) Handler { return Fallback(nil)(p) },
		"hedge":           func(p Handler) Handler { return Hedge(time.Second)(p) },
		"cache":           func(p Handler) Handler { return Cache()(p) },
		"redaction":       func(p Handler) Handler { return RedactEgress()(p) },
		"the pair": func(p Handler) Handler {
			return Decisions(p, &decisionProbe{name: "other"}, DecisionOptions{})
		},
	}
	for name, build := range builders {
		t.Run(name, func(t *testing.T) {
			probe := &decisionProbe{name: "jev", reply: probeReply()}
			if got := build(probe).EstimateCost(req); got != 0.001 {
				t.Errorf("the estimate is %v, want the provider's", got)
			}
			if len(probe.estimated) != 1 || probe.estimated[0] != decision {
				t.Errorf("the estimator was handed %v, want the caller's decision", probe.estimated)
			}
		})
	}
	t.Run("budget reserves the estimate of a decision before the call", func(t *testing.T) {
		probe := &decisionProbe{name: "jev", reply: probeReply()}
		refusing := Budget(0.0005)(probe)
		if _, err := refusing.Complete(context.Background(), req); types.KindOf(err) != types.KindBudgetExceeded {
			t.Fatalf("err = %v, want a refusal", err)
		}
		if probe.calls != 0 {
			t.Errorf("the provider was called %d times after the budget refused", probe.calls)
		}
	})
}

// A call whose decision provider was asked first and answered below the floor is
// paid twice, and the first call is not in the response the pair returns. A
// ledger listed before the pair counts the generating provider's call alone; one
// that wraps the decision provider counts the decision provider's.
func TestBudgetCountsTheDecidersCallOnlyWhereItWrapsTheDecider(t *testing.T) {
	perMillionth := func(usage *types.TokenUsage, _, _ string) *types.CostInfo {
		return &types.CostInfo{Priced: true, TotalCost: float64(usage.TotalTokens) / 1e6}
	}
	options := DecisionOptions{Floor: 0.9}

	t.Run("before the pair", func(t *testing.T) {
		generator, decider := generatorAnswering(generatorJSON), deciderAnswering(yes(0.3))
		ledger := Budget(10, WithBudgetCostFunc(perMillionth))(Decisions(generator, decider, options))
		if resp, err := ledger.Complete(context.Background(), decisionRequest()); err != nil || resp.DecisionEscalation == nil {
			t.Fatalf("Complete: %+v, %v", resp, err)
		}
		if spent := ledger.(*budgetProvider).Spent(); math.Abs(spent-420e-6) > 1e-12 {
			t.Errorf("the ledger holds %v, want the generating provider's 420 tokens and nothing for the first call", spent)
		}
	})

	t.Run("around the decision provider", func(t *testing.T) {
		generator, decider := generatorAnswering(generatorJSON), deciderAnswering(yes(0.3))
		ledger := Budget(10, WithBudgetCostFunc(perMillionth))(decider)
		pair := Decisions(generator, ledger, options)
		if resp, err := pair.Complete(context.Background(), decisionRequest()); err != nil || resp.DecisionEscalation == nil {
			t.Fatalf("Complete: %+v, %v", resp, err)
		}
		if spent := ledger.(*budgetProvider).Spent(); math.Abs(spent-340e-6) > 1e-12 {
			t.Errorf("the ledger holds %v, want the decision provider's 340 tokens", spent)
		}
	})
}

// A decision provider's typed answers are checked against the questions before
// the pair reads a probability from them. An answer that is not an answer is the
// decision provider failing: the generating provider is asked and the response
// says why, or with NoFailover the request fails as an invalid answer does. It is
// never passed on, whatever the floor.
func TestDecisionsDoesNotPassOnAnAnswerThatIsNotAnAnswerToTheDecision(t *testing.T) {
	questions := []llm.DecisionQuestion{
		{Kind: llm.DecisionYesNo, Ask: "Is it?"},
		{Kind: llm.DecisionChoice, Ask: "Which?", Options: []string{"red", "green"}},
		{Kind: llm.DecisionScale, Ask: "How?", Low: 1, High: 5},
	}
	good := []llm.DecisionAnswer{
		{Kind: llm.DecisionYesNo, Yes: true, ModelConfidence: 0.9, ConfidenceReported: true},
		{Kind: llm.DecisionChoice, Label: "green", ModelConfidence: 0.9, ConfidenceReported: true},
		{Kind: llm.DecisionScale, Level: 4, ModelConfidence: 0.9, ConfidenceReported: true},
	}
	with := func(change func(a []llm.DecisionAnswer) []llm.DecisionAnswer) []llm.DecisionAnswer {
		return change(append([]llm.DecisionAnswer(nil), good...))
	}
	cases := []struct {
		name    string
		answers []llm.DecisionAnswer
		valid   bool
	}{
		{"every answer valid", good, true},
		{"a probability of exactly one", with(func(a []llm.DecisionAnswer) []llm.DecisionAnswer { a[0].ModelConfidence = 1; return a }), true},
		{"a probability that just clears the floor", with(func(a []llm.DecisionAnswer) []llm.DecisionAnswer { a[0].ModelConfidence = 0.5; return a }), true},
		{"none", nil, false},
		{"too few", good[:1], false},
		{"too many", append(append([]llm.DecisionAnswer(nil), good...), good[0]), false},
		{"a label that was not offered", with(func(a []llm.DecisionAnswer) []llm.DecisionAnswer { a[1].Label = "purple"; return a }), false},
		{"no label", with(func(a []llm.DecisionAnswer) []llm.DecisionAnswer { a[1].Label = ""; return a }), false},
		{"the wrong kind", with(func(a []llm.DecisionAnswer) []llm.DecisionAnswer { a[1].Kind = llm.DecisionYesNo; return a }), false},
		{"no kind", with(func(a []llm.DecisionAnswer) []llm.DecisionAnswer { a[0].Kind = 0; return a }), false},
		{"a level above the scale", with(func(a []llm.DecisionAnswer) []llm.DecisionAnswer { a[2].Level = 99; return a }), false},
		{"a level below the scale", with(func(a []llm.DecisionAnswer) []llm.DecisionAnswer { a[2].Level = 0; return a }), false},
		{"a probability above one", with(func(a []llm.DecisionAnswer) []llm.DecisionAnswer { a[0].ModelConfidence = 7; return a }), false},
		{"a negative probability", with(func(a []llm.DecisionAnswer) []llm.DecisionAnswer { a[0].ModelConfidence = -0.1; return a }), false},
		{"a probability that is not a number", with(func(a []llm.DecisionAnswer) []llm.DecisionAnswer { a[0].ModelConfidence = math.NaN(); return a }), false},
	}
	for _, tc := range cases {
		for _, floor := range []float64{0, 0.5} {
			t.Run(fmt.Sprintf("%s, floor %v", tc.name, floor), func(t *testing.T) {
				request := decisionRequest(questions...)
				decider := deciderAnswering(tc.answers...)
				generator := generatorAnswering(generatorJSON)
				resp, err := Decisions(generator, decider, DecisionOptions{Floor: floor}).Complete(context.Background(), request)
				if err != nil {
					t.Fatalf("Complete: %v", err)
				}
				if tc.valid {
					if generator.calls() != 0 || len(resp.DecisionAnswers) != len(tc.answers) || resp.DecisionEscalation != nil {
						t.Errorf("a valid answer was not returned: %d generator calls, %d answers, escalation %+v", generator.calls(), len(resp.DecisionAnswers), resp.DecisionEscalation)
					}
					return
				}
				if generator.calls() != 1 || decider.calls() != 1 {
					t.Errorf("%d calls to the generator and %d to the decider, want 1 and 1", generator.calls(), decider.calls())
				}
				if len(resp.DecisionAnswers) != 0 || resp.Content != generatorJSON {
					t.Errorf("the generator's answer was not the one returned: %+v", resp)
				}
				escalation := resp.DecisionEscalation
				if escalation == nil || escalation.Reason != llm.DecisionEscalatedUnavailable || escalation.Provider != "jev" {
					t.Errorf("the escalation is %+v, want the decision provider passed over as unavailable", escalation)
				}
			})
			t.Run(fmt.Sprintf("%s, floor %v, no failover", tc.name, floor), func(t *testing.T) {
				decider := deciderAnswering(tc.answers...)
				generator := generatorAnswering(generatorJSON)
				resp, err := Decisions(generator, decider, DecisionOptions{Floor: floor, NoFailover: true}).Complete(context.Background(), decisionRequest(questions...))
				if tc.valid {
					if err != nil || len(resp.DecisionAnswers) != len(tc.answers) {
						t.Errorf("a valid answer: err %v, %d answers", err, len(resp.DecisionAnswers))
					}
					return
				}
				if !errors.Is(err, types.ErrMalformedOutput) {
					t.Errorf("err = %v, want the kind of an answer that is not valid", err)
				}
				if generator.calls() != 0 {
					t.Errorf("the generator was asked %d times, want 0: NoFailover makes it the request's error", generator.calls())
				}
				if len(resp.DecisionAnswers) != 0 {
					t.Errorf("an answer came back beside the error: %+v", resp.DecisionAnswers)
				}
			})
		}
	}
}
