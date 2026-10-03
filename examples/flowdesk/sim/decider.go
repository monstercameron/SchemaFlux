package sim

import (
	"context"
	"fmt"
	"strconv"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
)

// Decider is a stand-in for a decision model: a provider that answers the
// desk's bounded questions typed, from the same labels the simulator answers
// from, and writes no text. Paired with a Simulator (mw.Decisions) it lets the
// desk's flows be run and timed with their decisions on a decision provider and
// everything else on a generating one, with no model and no network.
//
// It reads what a decision provider reads: the typed decision on the request
// (CompletionRequest.Decision), its questions and its state, and never the
// prompts. A request that carries no decision is refused as unsupported, which
// is what sends it to the generating provider.
type Decider struct {
	delay time.Duration
	cases map[string]Case
	log   Log

	// ModelConfidence is the probability reported for every answer. It is the
	// simulator's and stands for no model's: it is there so that a floor can be
	// seen to send decisions on, or not.
	ModelConfidence float64
}

// DeciderConfidence is the probability a new Decider reports for its answers.
const DeciderConfidence = 0.99

// NewDecider returns a decider that takes delay to answer each call.
func NewDecider(delay time.Duration) *Decider {
	cases := make(map[string]Case)
	for _, c := range Cases() {
		cases[c.Message.ID] = c
	}
	return &Decider{delay: delay, cases: cases, ModelConfidence: DeciderConfidence}
}

// Name is the decider's own. It needs no model of the library's: paired with a
// generating provider, the pair goes by that provider's name.
func (d *Decider) Name() string { return "sim-decider" }

// EstimateCost is zero: nothing is spent.
func (d *Decider) EstimateCost(schemaflux.CompletionRequest) float64 { return 0 }

// RetryPolicy asks for no retries. A request the decider refuses would be
// refused the same way again.
func (d *Decider) RetryPolicy() (int, time.Duration) { return 0, 0 }

// DecisionLimits is what the decision model the decider stands in for takes:
// 255 options, 10 levels and a state of 32 thousand tokens.
func (d *Decider) DecisionLimits() schemaflux.DecisionLimits {
	return schemaflux.DecisionLimits{MaxOptions: 255, MaxLevels: 10, MaxStateTokens: 32000}
}

// Count is how many decisions the decider answered or refused after its delay.
func (d *Decider) Count() int { return d.log.Count() }

// Complete answers the questions of the request's decision after the decider's
// delay, or returns the context's error if it ends first.
func (d *Decider) Complete(ctx context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	if err := d.DecisionLimits().Check(d.Name(), d.Name(), req.Decision); err != nil {
		return schemaflux.CompletionResponse{}, err
	}
	end := d.log.Begin(OpAssess)
	defer end()

	timer := time.NewTimer(d.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return schemaflux.CompletionResponse{}, ctx.Err()
	case <-timer.C:
	}

	answers, err := d.decide(*req.Decision)
	if err != nil {
		return schemaflux.CompletionResponse{}, err
	}
	// No usage is reported, as the simulator reports none.
	return schemaflux.CompletionResponse{Provider: d.Name(), Model: d.Name(), DecisionAnswers: answers}, nil
}

// decide answers a typed decision from the labels of the case its state is
// about.
func (d *Decider) decide(decision schemaflux.TypedDecision) ([]schemaflux.DecisionAnswer, error) {
	about, err := d.caseIn(decision.State)
	if err != nil {
		return nil, err
	}
	questions := make([]asked, len(decision.Questions))
	for i, question := range decision.Questions {
		questions[i] = asked{key: "q" + strconv.Itoa(i+1), text: question.Ask, options: question.Options, low: question.Low, high: question.High}
		switch question.Kind {
		case schemaflux.DecisionYesNo:
			questions[i].kind = kindYesNo
		case schemaflux.DecisionChoice:
			questions[i].kind = kindChoice
		case schemaflux.DecisionScale:
			questions[i].kind = kindRating
		default:
			return nil, fmt.Errorf("sim: question %d of the decision has no kind", i+1)
		}
	}
	labels, err := labelsFor(about, questions, decision.State)
	if err != nil {
		return nil, err
	}

	answers := make([]schemaflux.DecisionAnswer, len(questions))
	for i, question := range questions {
		answer := schemaflux.DecisionAnswer{Kind: decision.Questions[i].Kind, ModelConfidence: d.ModelConfidence, ConfidenceReported: true}
		switch value := labels[question.key].(type) {
		case bool:
			answer.Yes = value
		case int:
			if question.kind == kindChoice {
				answer.Label = question.options[value-1]
			} else {
				answer.Level = value
			}
		}
		answers[i] = answer
	}
	return answers, nil
}

// caseIn finds the case a state is about: by the reference in the text, or,
// for a message that is passed whole, by its ID.
func (d *Decider) caseIn(state string) (Case, error) {
	for _, reference := range []interface{ FindStringSubmatch(string) []string }{referenceInText, referenceInID} {
		if match := reference.FindStringSubmatch(state); match != nil {
			if found, known := d.cases[match[1]]; known {
				return found, nil
			}
			return Case{}, fmt.Errorf("sim: there is no labelled case %s", match[1])
		}
	}
	return Case{}, fmt.Errorf("sim: the decision's state carries no message reference, so the decider cannot tell which case it is about")
}

// Reply is what a call answered, as the text a trace reads: the response's own
// content, or, for a decision provider's typed answers, the same answers
// written the way a generating model writes them, {"q1":true,"q2":3}. It is for
// showing a run and is not what the library reads.
func Reply(req schemaflux.CompletionRequest, resp schemaflux.CompletionResponse) string {
	if resp.Content != "" || req.Decision == nil || len(resp.DecisionAnswers) != len(req.Decision.Questions) {
		return resp.Content
	}
	reply := make(map[string]any, len(resp.DecisionAnswers))
	for i, answer := range resp.DecisionAnswers {
		key := "q" + strconv.Itoa(i+1)
		switch answer.Kind {
		case schemaflux.DecisionChoice:
			for n, label := range req.Decision.Questions[i].Options {
				if label == answer.Label {
					reply[key] = n + 1
				}
			}
		case schemaflux.DecisionScale:
			reply[key] = answer.Level
		default:
			reply[key] = answer.Yes
		}
	}
	written, err := asJSON(reply)
	if err != nil {
		return ""
	}
	return written
}
