package ops

import (
	"context"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// What a decision provider answers typed, llm.RenderDecisionAnswers writes in
// the form readAssessAnswer reads. The two are inverses, and this is where that
// is held: what is read from the rendered form is what was rendered, for every
// kind of question and for several at once.

// typedAnswers is the reader's answers as a decision provider would have given
// them.
func typedAnswers(questions []assessQuestion, answers []answer, confidence []float64) []llm.DecisionAnswer {
	typed := make([]llm.DecisionAnswer, len(questions))
	for i, q := range questions {
		switch q.kind {
		case questionChoice:
			typed[i] = llm.DecisionAnswer{Kind: llm.DecisionChoice, Label: q.labels[answers[i].choice]}
		case questionRating:
			typed[i] = llm.DecisionAnswer{Kind: llm.DecisionScale, Level: answers[i].rating}
		default:
			typed[i] = llm.DecisionAnswer{Kind: llm.DecisionYesNo, Yes: answers[i].yes}
		}
		if i < len(confidence) {
			typed[i].ModelConfidence, typed[i].ConfidenceReported = confidence[i], true
		}
	}
	return typed
}

func TestTypedAnswersRoundTripThroughTheDecisionReader(t *testing.T) {
	yesNo := assessQuestion{kind: questionYesNo, ask: "Is the customer asking for a refund?"}
	team := assessQuestion{kind: questionChoice, ask: "Which team?", labels: []string{"billing", "technical", "shipping"}}
	urgency := assessQuestion{kind: questionRating, ask: "How urgent?", low: 1, high: 5}
	signed := assessQuestion{kind: questionRating, ask: "How far off?", low: -3, high: 3}
	orNone := assessQuestion{kind: questionChoice, ask: "Which ticket?", labels: []string{"Late parcel", "Double charge", whichNone}}

	cases := []struct {
		name       string
		questions  []assessQuestion
		answers    []answer
		confidence []float64
		want       float64
	}{
		{"a yes", []assessQuestion{yesNo}, []answer{{yes: true}}, []float64{0.96}, 0.96},
		{"a no", []assessQuestion{yesNo}, []answer{{yes: false}}, []float64{0.8}, 0.8},
		{"the first label", []assessQuestion{team}, []answer{{choice: 0}}, []float64{0.67}, 0.67},
		{"the last label", []assessQuestion{team}, []answer{{choice: 2}}, []float64{1}, 1},
		{"none of these", []assessQuestion{orNone}, []answer{{choice: 2}}, []float64{0.51}, 0.51},
		{"the bottom of a scale", []assessQuestion{urgency}, []answer{{rating: 1}}, []float64{0.3}, 0.3},
		{"the top of a scale", []assessQuestion{urgency}, []answer{{rating: 5}}, []float64{0.99}, 0.99},
		{"a level below zero", []assessQuestion{signed}, []answer{{rating: -3}}, []float64{0}, 0},
		{"three kinds at once, the lowest probability", []assessQuestion{yesNo, team, urgency},
			[]answer{{yes: true}, {choice: 1}, {rating: 4}}, []float64{0.96, 0.67, 0.99}, 0.67},
		{"thirteen questions", []assessQuestion{yesNo, yesNo, team, yesNo, team, team, urgency, yesNo, yesNo, yesNo, yesNo, orNone, yesNo},
			[]answer{{}, {yes: true}, {choice: 0}, {}, {choice: 2}, {choice: 1}, {rating: 3}, {yes: true}, {}, {yes: true}, {}, {choice: 2}, {yes: true}},
			[]float64{0.9, 0.91, 0.92, 0.93, 0.94, 0.95, 0.96, 0.97, 0.98, 0.99, 0.5, 0.75, 1}, 0.5},
		{"a very small probability", []assessQuestion{yesNo}, []answer{{yes: true}}, []float64{1e-9}, 1e-9},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision := &llm.Decision{State: `"x"`, Questions: decisionQuestions(tc.questions)}

			// With a probability on every answer: the confidence is read back.
			rendered, err := llm.RenderDecisionAnswers(decision, typedAnswers(tc.questions, tc.answers, tc.confidence))
			if err != nil {
				t.Fatalf("RenderDecisionAnswers: %v", err)
			}
			for _, asked := range []bool{true, false} {
				got, confidence, err := readAssessAnswer(rendered, tc.questions, asked)
				if err != nil {
					t.Fatalf("the reader refused %s: %v", rendered, err)
				}
				if !reflect.DeepEqual(got, tc.answers) {
					t.Errorf("read %+v from %s, want %+v", got, rendered, tc.answers)
				}
				want := 0.0
				if asked {
					want = tc.want
				}
				if math.Abs(confidence-want) > 1e-12 {
					t.Errorf("read a confidence of %v from %s, want %v", confidence, rendered, want)
				}
			}

			// With no probability reported: the answers are read, and a reader
			// that was asked for a confidence refuses, since none was given.
			bare, err := llm.RenderDecisionAnswers(decision, typedAnswers(tc.questions, tc.answers, nil))
			if err != nil {
				t.Fatalf("RenderDecisionAnswers without probabilities: %v", err)
			}
			got, _, err := readAssessAnswer(bare, tc.questions, false)
			if err != nil || !reflect.DeepEqual(got, tc.answers) {
				t.Errorf("read %+v, %v from %s, want %+v", got, err, bare, tc.answers)
			}
			if _, _, err := readAssessAnswer(bare, tc.questions, true); types.KindOf(err) != types.KindSchemaViolation {
				t.Errorf("a confidence was read from %s, which has none: %v", bare, err)
			}

			// And the other way: what the reader reads, rendered, is the form
			// it read.
			again, err := llm.RenderDecisionAnswers(decision, typedAnswers(tc.questions, got, nil))
			if err != nil || again != bare {
				t.Errorf("rendering what was read gave %s, %v; want %s", again, err, bare)
			}
		})
	}
}

func TestDecisionQuestionsAreTheQuestionsThatWereAsked(t *testing.T) {
	labels := []string{"billing", "technical"}
	cases := []struct {
		name string
		ask  assessQuestion
		want llm.DecisionQuestion
	}{
		{"a yes or no", assessQuestion{kind: questionYesNo, ask: "Is it spam?"}, llm.DecisionQuestion{Kind: llm.DecisionYesNo, Ask: "Is it spam?"}},
		{"a choice", assessQuestion{kind: questionChoice, ask: "Which?", labels: labels}, llm.DecisionQuestion{Kind: llm.DecisionChoice, Ask: "Which?", Options: labels}},
		{"a rating", assessQuestion{kind: questionRating, ask: "How?", low: 1, high: 5}, llm.DecisionQuestion{Kind: llm.DecisionScale, Ask: "How?", Low: 1, High: 5}},
		{"a rating below zero", assessQuestion{kind: questionRating, ask: "How?", low: -2, high: 2}, llm.DecisionQuestion{Kind: llm.DecisionScale, Ask: "How?", Low: -2, High: 2}},
		{"a wide rating", assessQuestion{kind: questionRating, ask: "How?", low: 0, high: 99}, llm.DecisionQuestion{Kind: llm.DecisionScale, Ask: "How?", Low: 0, High: 99}},
		{"a yes or no carries no scale", assessQuestion{kind: questionYesNo, ask: "Is it?", low: 4, high: 9}, llm.DecisionQuestion{Kind: llm.DecisionYesNo, Ask: "Is it?"}},
		{"a yes or no carries no labels", assessQuestion{kind: questionYesNo, ask: "Is it?", labels: labels}, llm.DecisionQuestion{Kind: llm.DecisionYesNo, Ask: "Is it?"}},
		{"a choice carries no scale", assessQuestion{kind: questionChoice, ask: "Which?", labels: labels, low: 1, high: 2}, llm.DecisionQuestion{Kind: llm.DecisionChoice, Ask: "Which?", Options: labels}},
		{"a rating carries no labels", assessQuestion{kind: questionRating, ask: "How?", labels: labels, low: 1, high: 2}, llm.DecisionQuestion{Kind: llm.DecisionScale, Ask: "How?", Low: 1, High: 2}},
		{"the question is carried as written", assessQuestion{kind: questionYesNo, ask: "  Is \"it\" <b>?  "}, llm.DecisionQuestion{Kind: llm.DecisionYesNo, Ask: "  Is \"it\" <b>?  "}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := decisionQuestion(tc.ask); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("decisionQuestion = %+v, want %+v", got, tc.want)
			}
			if got := decisionQuestions([]assessQuestion{tc.ask, tc.ask}); len(got) != 2 || !reflect.DeepEqual(got[1], tc.want) {
				t.Errorf("decisionQuestions = %+v", got)
			}
		})
	}
}

func TestADecisionCallCarriesTheDecisionAndNothingElseChanges(t *testing.T) {
	type marker struct{}
	parent := context.WithValue(context.Background(), marker{}, "kept")
	one := []assessQuestion{{kind: questionYesNo, ask: "Is it?"}}
	two := []assessQuestion{{kind: questionYesNo, ask: "Is it?"}, {kind: questionRating, ask: "How?", low: 1, high: 3}}
	ahead := decisionQuestions(two)

	cases := []struct {
		name      string
		ctx       context.Context
		questions []assessQuestion
		typed     []llm.DecisionQuestion
		want      int
	}{
		{"one question is held in the call", parent, one, nil, 1},
		{"two questions are worked out", parent, two, nil, 2},
		{"questions worked out ahead are the ones carried", parent, two, ahead, 2},
		{"a nil context", nil, one, nil, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call := newDecisionCall(tc.ctx, `"the subject"`, "be strict", tc.questions, tc.typed)
			decision := requestDecision(call)
			if decision == nil || len(decision.Questions) != tc.want || decision.State != `"the subject"` || decision.Guidance != "be strict" {
				t.Fatalf("the decision carried is %+v", decision)
			}
			if tc.typed != nil && &decision.Questions[0] != &tc.typed[0] {
				t.Error("the questions were copied, and were to be shared")
			}
			if decisionCallFrom(call) != call {
				t.Error("the call is not found on its own context")
			}
			// A context derived from it still carries it, and it is still a context.
			child, cancel := context.WithCancel(call)
			if decisionCallFrom(child) != call || requestDecision(child) != decision {
				t.Error("a context derived from the call lost the decision")
			}
			cancel()
			if child.Err() == nil || call.Err() != nil {
				t.Error("cancelling a child cancelled the call, or did not cancel the child")
			}
			if tc.ctx != nil && call.Value(marker{}) != "kept" {
				t.Error("a value of the parent context is not reachable through the call")
			}
			if dec12ReinforcementDeclined(call) {
				t.Error("a call that did not decline the reinforcement block reads as having declined it")
			}
			call.withoutReinforcement = true
			if !dec12ReinforcementDeclined(child) {
				t.Error("a call that declined the reinforcement block does not read as having declined it")
			}
		})
	}

	t.Run("a context with no decision call", func(t *testing.T) {
		for _, ctx := range []context.Context{nil, context.Background(), parent} {
			if decisionCallFrom(ctx) != nil || requestDecision(ctx) != nil {
				t.Errorf("a decision was found on %v", ctx)
			}
		}
	})
	t.Run("the parent's cancellation and deadline reach the call", func(t *testing.T) {
		bounded, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Hour))
		call := newDecisionCall(bounded, `"x"`, "", one, nil)
		if _, ok := call.Deadline(); !ok {
			t.Error("the call lost its parent's deadline")
		}
		cancel()
		<-call.Done()
		if call.Err() == nil {
			t.Error("the call did not end with its parent")
		}
	})
	t.Run("a call with no questions carries no decision", func(t *testing.T) {
		if got := requestDecision(newDecisionCall(context.Background(), `"x"`, "", nil, nil)); got != nil {
			t.Errorf("a decision with no question was carried: %+v", got)
		}
	})
}

func TestTypedAnswerContent(t *testing.T) {
	decision := &llm.Decision{State: `"x"`, Questions: []llm.DecisionQuestion{
		{Kind: llm.DecisionYesNo, Ask: "Is it?"},
		{Kind: llm.DecisionChoice, Ask: "Which?", Options: []string{"billing", "technical"}},
	}}
	yes := llm.DecisionAnswer{Kind: llm.DecisionYesNo, Yes: true, ModelConfidence: 0.9, ConfidenceReported: true}
	technical := llm.DecisionAnswer{Kind: llm.DecisionChoice, Label: "technical", ModelConfidence: 0.8, ConfidenceReported: true}

	cases := []struct {
		name    string
		req     llm.CompletionRequest
		resp    llm.CompletionResponse
		want    string
		refused bool
	}{
		{"a generating provider's answer is left alone", llm.CompletionRequest{Decision: decision}, llm.CompletionResponse{Content: `{"q1":false,"q2":1}`}, `{"q1":false,"q2":1}`, false},
		{"an answer to a request with no decision is left alone", llm.CompletionRequest{}, llm.CompletionResponse{Content: "a summary"}, "a summary", false},
		{"an empty answer is left empty", llm.CompletionRequest{Decision: decision}, llm.CompletionResponse{}, "", false},
		{"typed answers become the reader's form", llm.CompletionRequest{Decision: decision},
			llm.CompletionResponse{DecisionAnswers: []llm.DecisionAnswer{yes, technical}}, `{"q1":true,"q2":2,"confidence":0.8}`, false},
		{"typed answers replace content a provider wrote beside them", llm.CompletionRequest{Decision: decision},
			llm.CompletionResponse{Content: `{"q1":false,"q2":1}`, DecisionAnswers: []llm.DecisionAnswer{yes, technical}}, `{"q1":true,"q2":2,"confidence":0.8}`, false},
		{"one answer missing", llm.CompletionRequest{Decision: decision}, llm.CompletionResponse{DecisionAnswers: []llm.DecisionAnswer{yes}}, "", true},
		{"an answer left at zero", llm.CompletionRequest{Decision: decision}, llm.CompletionResponse{DecisionAnswers: []llm.DecisionAnswer{yes, {}}}, "", true},
		{"a label that was not offered", llm.CompletionRequest{Decision: decision},
			llm.CompletionResponse{DecisionAnswers: []llm.DecisionAnswer{yes, {Kind: llm.DecisionChoice, Label: "shipping"}}}, "", true},
		{"a probability that is not one", llm.CompletionRequest{Decision: decision},
			llm.CompletionResponse{DecisionAnswers: []llm.DecisionAnswer{{Kind: llm.DecisionYesNo, ModelConfidence: 2, ConfidenceReported: true}, technical}}, "", true},
		{"typed answers to a request that carried no decision", llm.CompletionRequest{}, llm.CompletionResponse{Content: "text", DecisionAnswers: []llm.DecisionAnswer{yes}}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := tc.resp
			err := typedAnswerContent(tc.req, &resp)
			if tc.refused != (err != nil) {
				t.Fatalf("error %v, refused should be %v", err, tc.refused)
			}
			if tc.refused && types.KindOf(err) != types.KindSchemaViolation {
				t.Errorf("kind %v, want a schema violation", types.KindOf(err))
			}
			if resp.Content != tc.want {
				t.Errorf("content %q, want %q", resp.Content, tc.want)
			}
		})
	}
}

func TestNoteDecisionAnswer(t *testing.T) {
	escalation := &llm.DecisionEscalation{Provider: "jev", Model: "jev-latest", Reason: llm.DecisionEscalatedBelowFloor, ModelConfidence: 0.4, ConfidenceReported: true}
	noConfidence := &llm.DecisionEscalation{Provider: "jev", Reason: llm.DecisionEscalatedUnavailable}
	typed := []llm.DecisionAnswer{{Kind: llm.DecisionYesNo, Yes: true}}
	one := []assessQuestion{{kind: questionYesNo, ask: "Is it?"}}

	cases := []struct {
		name       string
		resp       llm.CompletionResponse
		wantCustom map[string]any
		wantTyped  bool
		wantAsked  *llm.DecisionEscalation
	}{
		{"a generating provider's answer adds nothing", llm.CompletionResponse{Content: `{"q1":true}`}, map[string]any{}, false, nil},
		{"a typed answer", llm.CompletionResponse{DecisionAnswers: typed}, map[string]any{customDecisionTyped: true}, true, nil},
		{"an answer after an escalation", llm.CompletionResponse{Content: `{"q1":true}`, DecisionEscalation: escalation},
			map[string]any{customDecisionAskedFirst: "jev", customDecisionEscalated: llm.DecisionEscalatedBelowFloor, customDecisionAskedFirstConfidence: 0.4}, false, escalation},
		{"an escalation with no probability reports none", llm.CompletionResponse{Content: `{"q1":true}`, DecisionEscalation: noConfidence},
			map[string]any{customDecisionAskedFirst: "jev", customDecisionEscalated: llm.DecisionEscalatedUnavailable}, false, noConfidence},
	}
	for _, tc := range cases {
		for _, carried := range []bool{true, false} {
			name := tc.name + ", with no decision call on the context"
			if carried {
				name = tc.name
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				var call *decisionCall
				if carried {
					call = newDecisionCall(ctx, `"x"`, "", one, nil)
					// Left over from an earlier use, to show that it is set and not only added to.
					call.typed, call.escalation = !tc.wantTyped, escalation
					ctx = call
				}
				metadata := &types.ResultMetadata{Provider: "openai", Custom: map[string]any{}}
				noteDecisionAnswer(ctx, tc.resp, metadata)
				if !reflect.DeepEqual(metadata.Custom, tc.wantCustom) {
					t.Errorf("the record says %v, want %v", metadata.Custom, tc.wantCustom)
				}
				if call != nil && (call.typed != tc.wantTyped || call.escalation != tc.wantAsked || call.answeredBy != "openai") {
					t.Errorf("the call says typed %v, asked first %v, answered by %q", call.typed, call.escalation, call.answeredBy)
				}
			})
		}
	}
}
