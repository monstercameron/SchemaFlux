package ops

import (
	"context"
	"fmt"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/pricing"
)

// BM-012. A decision log.
//
// A program whose control flow is decided by a model has to be able to say why
// it took a path. A client can register an observer (Client.WithDecisionObserver)
// that is told, after every decision, what was asked and what was read.
//
// The record goes to the caller's own function and nowhere else. It holds the
// caller's questions and the model's answers, so it is not logged here and is
// not put in any error.

// DecisionRecord is one decision, after it was made.
type DecisionRecord struct {
	// Operation is the decision's kind: "is", "which", "rate" or "assess".
	// WhichOrNone is a "which", asked with one more option than the caller gave
	// it, "none of these", and the record lists the options that were put to the
	// model.
	Operation string

	// Asked is every question put, in the order it was put. A decision that
	// failed lists its questions too, with no answers.
	Asked []AnsweredQuestion

	// ModelConfidence is the model's own confidence in its answers taken
	// together, a claim and not a measurement. It is set when the Result form
	// of the decision asked for it, and is nil otherwise and when the decision
	// failed.
	ModelConfidence *float64

	// Elapsed is how long the decision took, from the call to the answer or to
	// the failure.
	Elapsed time.Duration

	// Err is why the decision failed, or nil. It is the error the caller was
	// given.
	Err error

	// AnsweredBy is the provider that gave the answers that were read, as its
	// response named it. It is empty when the decision failed.
	AnsweredBy string

	// Typed is true when the answers came typed from a decision provider, and
	// false when a generating provider wrote them as JSON.
	Typed bool

	// AskedFirst is the decision provider that was asked before AnsweredBy,
	// when the answers that were read are not its own: its probability was
	// below the floor, or it failed or refused, and the generating provider was
	// asked the same questions (mw.Decisions). It is empty for a decision one
	// provider was asked.
	AskedFirst string

	// Escalated is why AskedFirst's answer was not used: "below the floor",
	// "unavailable" or "unsupported". It is empty when AskedFirst is.
	Escalated string

	// AskedFirstModelConfidence is the lowest probability AskedFirst reported for
	// its answers, when it answered and reported one: that model's claim about
	// the answer that was not used. It is nil otherwise.
	AskedFirstModelConfidence *float64
}

// decisionAnswered is who gave the answers of a decision, as the provider's
// call reported it.
type decisionAnswered struct {
	by         string
	typed      bool
	escalation *llm.DecisionEscalation
}

// answered is what the provider's call reported of the answer.
func (call *decisionCall) answered() decisionAnswered {
	return decisionAnswered{by: call.answeredBy, typed: call.typed, escalation: call.escalation}
}

// describe writes who answered into a decision's record. A decision that
// failed has no answers to attribute, and says only what was asked first when
// that is known.
func (answered decisionAnswered) describe(record *DecisionRecord) {
	if record.Err == nil {
		record.AnsweredBy, record.Typed = answered.by, answered.typed
	}
	if escalation := answered.escalation; escalation != nil {
		record.AskedFirst, record.Escalated = escalation.Provider, escalation.Reason
		if escalation.ConfidenceReported {
			confidence := escalation.ModelConfidence
			record.AskedFirstModelConfidence = &confidence
		}
	}
}

// ChargeAskedFirst records what a provider's call cost when its answer is not
// the one a wrapping provider returns: the decision provider of mw.Decisions,
// asked first and then passed over for the generating provider, or left with
// an answer nobody can use because the generating provider then failed.
//
// CallLLM records the response a provider returns, and a provider that wraps
// two returns one. The call that was made first was made and is billed, at its
// own model's rate, so it is charged here to the same three places: the
// client's budget, the process-wide ledger, and the records the call's envelope
// is summed from, where it counts as an attempt. asked is the provider that was
// called and model the model it was called under, which stand in where the
// response names none.
func ChargeAskedFirst(ctx context.Context, asked llm.Provider, model string, resp llm.CompletionResponse, started time.Time) {
	if asked == nil || resp.Cached {
		return
	}
	chargeBudget(ctx, asked, model, resp)

	actualModel := resp.Model
	if actualModel == "" {
		actualModel = model
	}
	actualProvider := resp.Provider
	if actualProvider == "" {
		actualProvider = asked.Name()
	}
	usage := resp.Usage
	record := &types.ResultMetadata{
		StartTime:      started,
		EndTime:        time.Now(),
		Duration:       time.Since(started),
		Model:          actualModel,
		RequestedModel: model,
		ObservedModel:  resp.Model,
		Provider:       actualProvider,
		Timing:         callTiming(types.Timing{}, false, resp),
		Custom:         map[string]any{customDecisionAskedFirst: actualProvider},
	}
	// A response that reports no usage has no figure to price. It is recorded
	// with none, which the envelope reads as spend of unknown size.
	if reportsUsage(usage) {
		cost := pricing.CalculateCostLogged(loggerFor(ctx), &usage, actualModel, actualProvider)
		record.TokenUsage, record.CostInfo = &usage, cost
		pricing.TrackCost(cost, record)
	}
	publishCallRecord(ctx, record)
}

// AnsweredQuestion is one question of a decision and the answer read for it.
type AnsweredQuestion struct {
	// Question is the question as the caller wrote it.
	Question string

	// Options are the labels of a choice, in the order they were numbered.
	Options []string

	// Low and High are the ends of a rating's scale, both included.
	Low, High int

	// Answer is a bool for a yes or no, the label chosen (a string) for a
	// choice, and the level (an int) for a rating. It is nil when the decision
	// failed: a question the model did not answer validly has no answer here.
	Answer any
}

// dec12DecisionRecord builds the record of a decision from what was asked and
// what was read. answers is empty when err is not nil, since nothing is
// returned with an error, and an answer that is not there is left nil and never
// filled in.
func dec12DecisionRecord(operation string, questions []assessQuestion, answers []answer, confidence bool, modelConfidence float64, err error, elapsed time.Duration) DecisionRecord {
	record := DecisionRecord{Operation: operation, Elapsed: elapsed, Err: err}
	if len(questions) > 0 {
		record.Asked = make([]AnsweredQuestion, len(questions))
	}
	for i, q := range questions {
		asked := AnsweredQuestion{Question: q.ask}
		switch q.kind {
		case questionChoice:
			// A copy: the labels are the caller's slice, and a record that
			// shared it could be changed by the observer under the next call.
			asked.Options = append([]string(nil), q.labels...)
		case questionRating:
			asked.Low, asked.High = q.low, q.high
		}
		if err == nil && i < len(answers) {
			switch q.kind {
			case questionChoice:
				if choice := answers[i].choice; choice >= 0 && choice < len(q.labels) {
					asked.Answer = q.labels[choice]
				}
			case questionRating:
				asked.Answer = answers[i].rating
			default:
				asked.Answer = answers[i].yes
			}
		}
		record.Asked[i] = asked
	}
	if err == nil && confidence {
		record.ModelConfidence = &modelConfidence
	}
	return record
}

// dec12NotifyDecision hands a record to the observer. The observer is the
// caller's code, and a panic in it does not take the decision down: it is
// caught and logged without the record, which holds the caller's text.
func dec12NotifyDecision(ctx context.Context, observer func(context.Context, DecisionRecord), record DecisionRecord) {
	defer func() {
		if recovered := recover(); recovered != nil {
			loggerFor(ctx).Error("A decision observer panicked; the decision is not affected",
				"operation", record.Operation, "panic_type", fmt.Sprintf("%T", recovered))
		}
	}()
	if ctx == nil {
		ctx = context.Background()
	}
	observer(ctx, record)
}
