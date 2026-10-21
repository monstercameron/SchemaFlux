package sim

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/examples/flowdesk/desk"
	"github.com/monstercameron/schemaflux/v2/mw"
)

// paired returns a simulator, a decider and a context whose calls reach the
// two as a pair: decisions to the decider, everything else to the simulator.
func paired(delay time.Duration, options mw.DecisionOptions) (*Simulator, *Decider, context.Context) {
	generator, decider := New(delay), NewDecider(delay/10)
	client := schemaflux.Client{Provider: mw.Decisions(generator, decider, options)}
	return generator, decider, schemaflux.WithClient(context.Background(), client)
}

// Every flow, over every labelled message, with the decisions on the decider:
// the outcome is the label's, the decider answered every decision, and the
// simulator was asked for none of them.
func TestTheDeskRunsWithItsDecisionsOnTheDecider(t *testing.T) {
	flows := []struct {
		name   string
		handle func(*desk.Desk, context.Context, desk.Message) (desk.Outcome, error)
	}{
		{"fused", (*desk.Desk).Handle},
		{"one at a time", (*desk.Desk).HandleOneAtATime},
		{"fast", (*desk.Desk).HandleFast},
		{"ahead", (*desk.Desk).HandleAhead},
	}
	for _, flow := range flows {
		for _, labelled := range Cases() {
			t.Run(flow.name+"/"+labelled.Message.ID, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					generator, decider, ctx := paired(50*time.Millisecond, mw.DecisionOptions{})
					outcome, err := flow.handle(desk.New(OpenTickets()), ctx, labelled.Message)
					if err != nil {
						t.Fatalf("the flow failed: %v", err)
					}
					if outcome != labelled.Want() {
						t.Errorf("the outcome is not the label's:\n got %+v\nwant %+v", outcome, labelled.Want())
					}
					if decider.Count() == 0 {
						t.Error("the decider was asked nothing")
					}
					for _, call := range generator.Calls() {
						if call.Operation == OpAssess {
							t.Errorf("the simulator was asked a decision the decider takes")
						}
					}
				})
			})
		}
	}
}

// With a floor above what the decider reports, every decision is asked of the
// decider and then of the simulator, and the outcome is still the label's.
func TestBelowTheFloorTheSimulatorAnswersTheDeskDecisions(t *testing.T) {
	for _, labelled := range Cases()[:6] {
		t.Run(labelled.Message.ID, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				generator, decider, ctx := paired(50*time.Millisecond, mw.DecisionOptions{Floor: 0.995})
				outcome, err := desk.New(OpenTickets()).Handle(ctx, labelled.Message)
				if err != nil {
					t.Fatalf("the flow failed: %v", err)
				}
				if outcome != labelled.Want() {
					t.Errorf("the outcome is not the label's:\n got %+v\nwant %+v", outcome, labelled.Want())
				}
				assessed := 0
				for _, call := range generator.Calls() {
					if call.Operation == OpAssess {
						assessed++
					}
				}
				if assessed == 0 || assessed != decider.Count() {
					t.Errorf("the decider was asked %d decisions and the simulator %d of them, want every one sent on", decider.Count(), assessed)
				}
			})
		})
	}
}

func TestTheDeciderAnswersTypedFromTheLabels(t *testing.T) {
	plain := caseByID(t, "M-02")
	state := func(labelled Case) string { return `"` + labelled.Message.Text() + `"` }
	yesNo := func(ask string) schemaflux.DecisionQuestion {
		return schemaflux.DecisionQuestion{Kind: schemaflux.DecisionYesNo, Ask: ask}
	}
	team := schemaflux.DecisionQuestion{Kind: schemaflux.DecisionChoice, Ask: askOf[desk.Triage](t, "Team"), Options: []string{"billing", "technical", "shipping", "account"}}
	urgency := schemaflux.DecisionQuestion{Kind: schemaflux.DecisionScale, Ask: askOf[desk.Triage](t, "Urgency"), Low: 1, High: 5}
	spam := yesNo(askOf[desk.Screen](t, "Spam"))

	cases := []struct {
		name      string
		decision  *schemaflux.Decision
		want      []schemaflux.DecisionAnswer
		refused   string
		unsupport bool
	}{
		{"a yes or no", &schemaflux.Decision{State: state(plain), Questions: []schemaflux.DecisionQuestion{spam}},
			[]schemaflux.DecisionAnswer{{Kind: schemaflux.DecisionYesNo, Yes: plain.Screen.Spam}}, "", false},
		{"a choice", &schemaflux.Decision{State: state(plain), Questions: []schemaflux.DecisionQuestion{team}},
			[]schemaflux.DecisionAnswer{{Kind: schemaflux.DecisionChoice, Label: plain.Triage.Team}}, "", false},
		{"a level", &schemaflux.Decision{State: state(plain), Questions: []schemaflux.DecisionQuestion{urgency}},
			[]schemaflux.DecisionAnswer{{Kind: schemaflux.DecisionScale, Level: plain.Triage.Urgency}}, "", false},
		{"three at once, in order", &schemaflux.Decision{State: state(plain), Questions: []schemaflux.DecisionQuestion{urgency, spam, team}},
			[]schemaflux.DecisionAnswer{{Kind: schemaflux.DecisionScale, Level: plain.Triage.Urgency}, {Kind: schemaflux.DecisionYesNo, Yes: plain.Screen.Spam}, {Kind: schemaflux.DecisionChoice, Label: plain.Triage.Team}}, "", false},
		{"a question the labels have no answer to", &schemaflux.Decision{State: state(plain), Questions: []schemaflux.DecisionQuestion{yesNo("Is the moon full?")}},
			nil, "no answer to question q1", false},
		{"a label that is not among the options", &schemaflux.Decision{State: state(plain), Questions: []schemaflux.DecisionQuestion{{Kind: schemaflux.DecisionChoice, Ask: team.Ask, Options: []string{"sales", "legal"}}}},
			nil, "not one of its 2 options", false},
		{"a level off the scale that was asked", &schemaflux.Decision{State: state(plain), Questions: []schemaflux.DecisionQuestion{{Kind: schemaflux.DecisionScale, Ask: urgency.Ask, Low: 7, High: 9}}},
			nil, "off its scale", false},
		{"a state about no case", &schemaflux.Decision{State: `"a message with no reference"`, Questions: []schemaflux.DecisionQuestion{spam}},
			nil, "carries no message reference", false},
		{"a state about a case there is none of", &schemaflux.Decision{State: `"[ref M-9999] hello"`, Questions: []schemaflux.DecisionQuestion{spam}},
			nil, "no labelled case M-9999", false},
		{"a ticket match that does not name the sender", &schemaflux.Decision{State: state(plain), Questions: []schemaflux.DecisionQuestion{{Kind: schemaflux.DecisionChoice, Ask: matchQuestion, Options: []string{noneOfThese, "x"}}}},
			nil, "does not carry the sender", false},
		{"a request with no decision", nil, nil, "", true},
		{"a decision with no question", &schemaflux.Decision{State: state(plain)}, nil, "", true},
		{"a scale wider than the model takes", &schemaflux.Decision{State: state(plain), Questions: []schemaflux.DecisionQuestion{{Kind: schemaflux.DecisionScale, Ask: urgency.Ask, Low: 1, High: 50}}},
			nil, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decider := NewDecider(time.Millisecond)
			resp, err := decider.Complete(context.Background(), schemaflux.Request{Model: "local-mock", User: "a prompt the decider must not read", Decision: tc.decision})
			switch {
			case tc.unsupport:
				if !errors.Is(err, schemaflux.ErrUnsupported) {
					t.Fatalf("error %v, want an unsupported capability", err)
				}
				if decider.Count() != 0 {
					t.Error("a request the decider cannot answer was counted as a call")
				}
			case tc.refused != "":
				if err == nil || !strings.Contains(err.Error(), tc.refused) {
					t.Fatalf("error %v, want one that says %q", err, tc.refused)
				}
			default:
				if err != nil {
					t.Fatalf("Complete: %v", err)
				}
				if len(resp.Answers) != len(tc.want) || resp.Content != "" {
					t.Fatalf("answered %+v with content %q", resp.Answers, resp.Content)
				}
				for i, want := range tc.want {
					got := resp.Answers[i]
					if got.Kind != want.Kind || got.Yes != want.Yes || got.Label != want.Label || got.Level != want.Level {
						t.Errorf("answer %d = %+v, want %+v", i+1, got, want)
					}
					if !got.ConfidenceReported || got.ModelConfidence != DeciderConfidence {
						t.Errorf("answer %d reports %v (%v), want the decider's %v", i+1, got.ModelConfidence, got.ConfidenceReported, DeciderConfidence)
					}
				}
			}
		})
	}

	t.Run("it waits its delay and stops with the context", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			decider := NewDecider(time.Second)
			req := schemaflux.Request{Decision: &schemaflux.Decision{State: state(plain), Questions: []schemaflux.DecisionQuestion{spam}}}
			started := time.Now()
			if _, err := decider.Complete(context.Background(), req); err != nil || time.Since(started) != time.Second {
				t.Errorf("answered after %v with %v, want after its delay", time.Since(started), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			if _, err := decider.Complete(ctx, req); !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("error %v, want the context's", err)
			}
		})
	})
	t.Run("it names itself and what it takes", func(t *testing.T) {
		decider := NewDecider(time.Millisecond)
		if decider.Name() != "sim-decider" || decider.EstimateCost(schemaflux.Request{}) != 0 {
			t.Errorf("named %q", decider.Name())
		}
		if retries, _ := decider.RetryPolicy(); retries != 0 {
			t.Errorf("retries %d", retries)
		}
		if limits := decider.DecisionLimits(); limits.MaxOptions != 255 || limits.MaxLevels != 10 || limits.MaxStateTokens != 32000 {
			t.Errorf("limits %+v", limits)
		}
	})
}

func TestReplyIsWhatACallAnsweredAsATraceReadsIt(t *testing.T) {
	decision := &schemaflux.Decision{State: `"x"`, Questions: []schemaflux.DecisionQuestion{
		{Kind: schemaflux.DecisionYesNo, Ask: "Is it?"},
		{Kind: schemaflux.DecisionChoice, Ask: "Which?", Options: []string{"billing", "technical"}},
		{Kind: schemaflux.DecisionScale, Ask: "How?", Low: 1, High: 5},
	}}
	typed := []schemaflux.DecisionAnswer{
		{Kind: schemaflux.DecisionYesNo, Yes: true},
		{Kind: schemaflux.DecisionChoice, Label: "technical"},
		{Kind: schemaflux.DecisionScale, Level: 4},
	}
	cases := []struct {
		name string
		req  schemaflux.Request
		resp schemaflux.Response
		want string
	}{
		{"a generating model's answer is its content", schemaflux.Request{Decision: decision}, schemaflux.Response{Content: `{"q1":false}`}, `{"q1":false}`},
		{"text is text", schemaflux.Request{}, schemaflux.Response{Content: "A summary."}, "A summary."},
		{"typed answers are written as a model writes them", schemaflux.Request{Decision: decision}, schemaflux.Response{Answers: typed}, `{"q1":true,"q2":2,"q3":4}`},
		{"content wins over typed answers beside it", schemaflux.Request{Decision: decision}, schemaflux.Response{Content: "x", Answers: typed}, "x"},
		{"typed answers with no decision to read them by", schemaflux.Request{}, schemaflux.Response{Answers: typed}, ""},
		{"fewer answers than questions", schemaflux.Request{Decision: decision}, schemaflux.Response{Answers: typed[:2]}, ""},
		{"nothing at all", schemaflux.Request{}, schemaflux.Response{}, ""},
		{"a label that was not offered is left out", schemaflux.Request{Decision: decision},
			schemaflux.Response{Answers: []schemaflux.DecisionAnswer{typed[0], {Kind: schemaflux.DecisionChoice, Label: "sales"}, typed[2]}}, `{"q1":true,"q3":4}`},
		{"a no", schemaflux.Request{Decision: &schemaflux.Decision{Questions: decision.Questions[:1]}},
			schemaflux.Response{Answers: []schemaflux.DecisionAnswer{{Kind: schemaflux.DecisionYesNo}}}, `{"q1":false}`},
		{"the first label", schemaflux.Request{Decision: &schemaflux.Decision{Questions: decision.Questions[1:2]}},
			schemaflux.Response{Answers: []schemaflux.DecisionAnswer{{Kind: schemaflux.DecisionChoice, Label: "billing"}}}, `{"q1":1}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Reply(tc.req, tc.resp); got != tc.want {
				t.Errorf("Reply = %q, want %q", got, tc.want)
			}
		})
	}
}
