package schemafluxtest

import (
	"context"
	"fmt"
	"sync"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
)

// Decider is a fake decision provider: one that answers the typed questions of
// a request (CompletionRequest.Decision) with typed answers and writes no text,
// as a decision model does. Provider cannot stand in for one, since it replies
// with a body. Its zero value is not usable; call NewDecider.
//
//	decider := schemafluxtest.NewDecider().Answer(schemafluxtest.Yes(0.97))
//	generator := schemafluxtest.New().Reply(`{"q1":false}`)
//	client := schemaflux.NewClient("test").WithProviderInstance(
//	    mw.Decisions(generator, decider, mw.DecisionOptions{Floor: 0.9}))
//
// Like a real decision provider it refuses a request that carries no decision,
// and a decision past the limits it was given, with the unsupported-capability
// error, and records the request all the same, so a test can show that nothing
// it should not have been asked reached it.
type Decider struct {
	mu sync.Mutex

	answers    []schemaflux.DecisionAnswer
	answerFunc func(call int, decision schemaflux.TypedDecision) ([]schemaflux.DecisionAnswer, error)
	errs       []error
	failAlways error
	limits     schemaflux.DecisionLimits
	usage      schemaflux.TokenUsage
	name       string
	model      string
	requests   []schemaflux.CompletionRequest
}

// NewDecider returns a decider that answers nothing until it is told what to
// answer, which the library refuses as a decision that was not answered.
func NewDecider() *Decider {
	return &Decider{name: "local"}
}

// Yes is a yes with the probability the model reports for it.
func Yes(confidence float64) schemaflux.DecisionAnswer {
	return schemaflux.DecisionAnswer{Kind: schemaflux.DecisionYesNo, Yes: true, ModelConfidence: confidence, ConfidenceReported: true}
}

// No is a no with the probability the model reports for it.
func No(confidence float64) schemaflux.DecisionAnswer {
	return schemaflux.DecisionAnswer{Kind: schemaflux.DecisionYesNo, ModelConfidence: confidence, ConfidenceReported: true}
}

// Chose is the choice of a label, with the probability the model reports for it.
func Chose(label string, confidence float64) schemaflux.DecisionAnswer {
	return schemaflux.DecisionAnswer{Kind: schemaflux.DecisionChoice, Label: label, ModelConfidence: confidence, ConfidenceReported: true}
}

// Level is a level on a scale, with the probability the model reports for it.
func Level(level int, confidence float64) schemaflux.DecisionAnswer {
	return schemaflux.DecisionAnswer{Kind: schemaflux.DecisionScale, Level: level, ModelConfidence: confidence, ConfidenceReported: true}
}

// Answer sets the answers every call returns, one for each question the test
// expects to be asked, in the questions' order. It clears a scripted failure.
func (d *Decider) Answer(answers ...schemaflux.DecisionAnswer) *Decider {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.answers = append([]schemaflux.DecisionAnswer(nil), answers...)
	d.answerFunc, d.errs, d.failAlways = nil, nil, nil
	return d
}

// AnswerFunc answers each call from the decision it was sent. It receives the
// zero-based call index and takes precedence over Answer.
func (d *Decider) AnswerFunc(fn func(call int, decision schemaflux.TypedDecision) ([]schemaflux.DecisionAnswer, error)) *Decider {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.answerFunc = fn
	return d
}

// Fail makes every call return err, for the paths where the decision provider
// is down.
func (d *Decider) Fail(err error) *Decider {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err == nil {
		err = fmt.Errorf("schemafluxtest: decider unavailable")
	}
	d.failAlways = err
	return d
}

// FailFirst makes the first calls fail with err and the rest answer as
// scripted.
func (d *Decider) FailFirst(failures int, err error) *Decider {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err == nil {
		err = fmt.Errorf("schemafluxtest: decider unavailable")
	}
	d.errs = make([]error, max(failures, 0))
	for i := range d.errs {
		d.errs[i] = err
	}
	d.failAlways = nil
	return d
}

// Limits sets what the decider declares it can take. A decision past them is
// refused, as a request with no decision is.
func (d *Decider) Limits(limits schemaflux.DecisionLimits) *Decider {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.limits = limits
	return d
}

// WithUsage sets the token usage every answer reports.
func (d *Decider) WithUsage(usage schemaflux.TokenUsage) *Decider {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.usage = usage
	return d
}

// As sets the provider name and the model the decider says answered. The
// default name is "local", the name reserved for an in-process double, and the
// default model is the one the request names.
func (d *Decider) As(name, model string) *Decider {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.name, d.model = name, model
	return d
}

// Requests returns every request the decider received, refused ones included,
// in order.
func (d *Decider) Requests() []schemaflux.CompletionRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]schemaflux.CompletionRequest(nil), d.requests...)
}

// CallCount returns how many calls were made.
func (d *Decider) CallCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.requests)
}

// DecisionLimits is what the decider was told it can take.
func (d *Decider) DecisionLimits() schemaflux.DecisionLimits {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.limits
}

// ModelSentFor is the model the decider answers under: its own when it was
// given one, and the requested one otherwise.
func (d *Decider) ModelSentFor(requested string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.model != "" {
		return d.model
	}
	return requested
}

// Complete implements schemaflux.Provider.
func (d *Decider) Complete(ctx context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	d.mu.Lock()
	call := len(d.requests)
	d.requests = append(d.requests, req)
	answers := append([]schemaflux.DecisionAnswer(nil), d.answers...)
	answerFunc, errs, failAlways := d.answerFunc, d.errs, d.failAlways
	limits, usage, name, model := d.limits, d.usage, d.name, d.model
	d.mu.Unlock()

	if model == "" {
		model = req.Model
	}
	if err := ctx.Err(); err != nil {
		return schemaflux.CompletionResponse{}, err
	}
	if err := limits.Check(name, model, req.Decision); err != nil {
		return schemaflux.CompletionResponse{}, err
	}
	if failAlways != nil {
		return schemaflux.CompletionResponse{}, failAlways
	}
	if call < len(errs) {
		return schemaflux.CompletionResponse{}, errs[call]
	}
	if answerFunc != nil {
		var err error
		if answers, err = answerFunc(call, *req.Decision); err != nil {
			return schemaflux.CompletionResponse{}, err
		}
	}
	return schemaflux.CompletionResponse{
		Provider:        name,
		Model:           model,
		Usage:           usage,
		DecisionAnswers: answers,
	}, nil
}

// Name implements schemaflux.Provider.
func (d *Decider) Name() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.name
}

// EstimateCost implements schemaflux.Provider. A double costs nothing.
func (d *Decider) EstimateCost(schemaflux.CompletionRequest) float64 { return 0 }

// RetryPolicy implements schemaflux.Provider. No retries, so a test that
// scripts one failure sees one failure.
func (d *Decider) RetryPolicy() (int, time.Duration) { return 0, 0 }
