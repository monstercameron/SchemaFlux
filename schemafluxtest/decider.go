package schemafluxtest

import (
	"context"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// deciderName is the name of a Decider that was not given another.
const deciderName = "test-decider"

// A Decider is a scripted decision model: it answers the typed questions of a
// request's Decision with typed answers, and writes no text, as a decision
// model does. A Provider cannot stand in for one, since a Provider answers with
// a body. The zero value is ready to use, and answers nothing until it is
// scripted; a question it has no answer for fails the request (see below).
//
//	decider := schemafluxtest.NewDecider(schemafluxtest.Yes(0.97), schemafluxtest.Chose("billing", 0.8))
//
// The answers are a list handed out one to each question asked, in order,
// across requests: the first question of the first request takes the first
// answer, the next question the next, and so on, whatever the request that
// asks it. The kind of an answer is not checked against its question. A test
// may script a wrong one on purpose, to see the library refuse it. A question
// with no answer left is not given a zero answer: the request fails with an
// *Error of kind ErrConfiguration whose message says which question of which
// request was unanswered, what kind it was, and what was scripted.
//
// Like a decision model it declares the limits it takes (SetLimits,
// DecisionLimits), refuses a request that carries no decision, or a decision
// past its limits, with ErrUnsupported, and records every request it is
// sent, the refused ones too, so that a test can show that nothing it must not
// be asked reached it. It is named "test-decider", knows one model,
// "test-model", and retries nothing.
//
// A Decider is safe for concurrent use and must not be copied after first use.
// Requests that overlap take answers in the order they reach the Decider, which
// a test cannot control: such a test answers by question with Respond.
type Decider struct {
	mu sync.Mutex

	// name is the name Name returns. Empty means deciderName.
	name string

	// usage is the token count every answer reports.
	usage schemaflux.Usage

	// limits are the limits DecisionLimits returns. The zero value declares
	// none.
	limits schemaflux.DecisionLimits

	// answers are the scripted answers, and used is how many have been handed
	// out.
	answers []schemaflux.DecisionAnswer
	used    int

	// respond, when not nil, answers instead of the list.
	respond func(call int, d schemaflux.Decision) ([]schemaflux.DecisionAnswer, error)

	// failing is the error the first failures requests fail with, or every
	// request when failAlways is set.
	failing    error
	failures   int
	failAlways bool

	// requests are the requests received, in order.
	requests []schemaflux.Request
}

// NewDecider returns a Decider scripted with answers, one for each question the
// test expects to be asked, in order. With none it answers nothing, which fails
// the first question asked.
func NewDecider(answers ...schemaflux.DecisionAnswer) *Decider {
	d := new(Decider)
	d.Reply(answers...)
	return d
}

// Yes is the answer yes to a yes-or-no question, with the probability the model
// reports for it when one is given: Yes() reports none, which is not a
// probability of zero, and Yes(0.97) reports 0.97. A probability that is not a
// number from 0 to 1, or more than one of them, is a mistake in the test and
// panics.
func Yes(probability ...float64) schemaflux.DecisionAnswer {
	return answer(schemaflux.DecisionAnswer{Kind: schemaflux.DecisionYesNo, Yes: true}, probability)
}

// No is the answer no to a yes-or-no question, with the probability the model
// reports for it when one is given. See Yes.
func No(probability ...float64) schemaflux.DecisionAnswer {
	return answer(schemaflux.DecisionAnswer{Kind: schemaflux.DecisionYesNo}, probability)
}

// Chose is the answer of a choice: the label, which must be one the question
// offered, with the probability the model reports for it when one is given. See
// Yes.
func Chose(label string, probability ...float64) schemaflux.DecisionAnswer {
	return answer(schemaflux.DecisionAnswer{Kind: schemaflux.DecisionChoice, Label: label}, probability)
}

// Level is the answer of a scale: the level, which must be on the question's
// scale, with the probability the model reports for it when one is given. See
// Yes.
func Level(n int, probability ...float64) schemaflux.DecisionAnswer {
	return answer(schemaflux.DecisionAnswer{Kind: schemaflux.DecisionScale, Level: n}, probability)
}

// answer sets a's probability from the optional argument of Yes, No, Chose and
// Level.
func answer(a schemaflux.DecisionAnswer, probability []float64) schemaflux.DecisionAnswer {
	switch len(probability) {
	case 0:
		return a
	case 1:
		p := probability[0]
		if math.IsNaN(p) || p < 0 || p > 1 {
			panic("schemafluxtest: a probability is a number from 0 to 1")
		}
		a.ModelConfidence, a.ConfidenceReported = p, true
		return a
	}
	panic("schemafluxtest: an answer takes at most one probability")
}

// Reply sets the answers, one for each question, in order, and starts handing
// them out from the first. It replaces the answers set before and removes a
// Respond function.
func (d *Decider) Reply(answers ...schemaflux.DecisionAnswer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.answers = slices.Clone(answers)
	d.used = 0
	d.respond = nil
}

// Respond answers each request from the decision it carries instead of from the
// list, which a test needs when requests overlap and the order they reach the
// Decider is not the test's. call is the zero-based position of the request
// among those received since the Decider was made or last Reset. The function
// runs after the limits are checked and the scripted failures are applied, with
// no lock held. The answers it returns are handed back as they are, the count
// of them included, and its error is returned as it is. Respond panics if f is
// nil, as Provider.Respond does: Reply with the answers puts the list back.
func (d *Decider) Respond(f func(call int, decision schemaflux.Decision) ([]schemaflux.DecisionAnswer, error)) {
	if f == nil {
		panic("schemafluxtest: Respond needs a function, and was given none; Reply sets the answers again")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.respond = f
}

// Fail makes every request fail with err, after the limits are checked, for a
// test of what happens when the decision model is down. A nil err is the
// provider being unavailable.
func (d *Decider) Fail(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failing, d.failures, d.failAlways = failure(err), 0, true
}

// FailThen makes the first failures requests fail with err, and the requests
// after them answer as scripted. A request is counted whether or not it passed
// the limits. A negative failures is none.
func (d *Decider) FailThen(failures int, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failing, d.failures, d.failAlways = failure(err), max(failures, 0), false
}

// SetLimits sets what the Decider declares it can take. The zero value, which
// is what it declares until this is called, declares none.
func (d *Decider) SetLimits(l schemaflux.DecisionLimits) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.limits = l
}

// DecisionLimits returns the limits the Decider declares, as a decision model
// does, so that a provider that wraps it and a generating one can send it only
// what it can take.
func (d *Decider) DecisionLimits() schemaflux.DecisionLimits {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.limits
}

// SetUsage sets the token count every answer reports. The zero Usage means the
// decision model reports none.
func (d *Decider) SetUsage(u schemaflux.Usage) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.usage = u
}

// SetName sets the name Name returns. Empty means "test-decider". See
// Provider.SetName for why a parallel test sets one.
func (d *Decider) SetName(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.name = name
}

// Name returns the decision model's name: "test-decider" unless SetName set
// another.
func (d *Decider) Name() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.nameLocked()
}

func (d *Decider) nameLocked() string {
	if d.name == "" {
		return deciderName
	}
	return d.name
}

// Requests returns every request the Decider received, in order, the ones it
// refused included. The slice is a copy.
func (d *Decider) Requests() []schemaflux.Request {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.requests)
}

// Last returns the most recent request, or the zero Request when there has been
// none.
func (d *Decider) Last() schemaflux.Request {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.requests) == 0 {
		return schemaflux.Request{}
	}
	return d.requests[len(d.requests)-1]
}

// Calls returns how many requests the Decider has received.
func (d *Decider) Calls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.requests)
}

// Reset forgets the requests received and hands the answers out again from the
// first. The answers, the limits, the failures, the name and the usage stay.
func (d *Decider) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.requests = nil
	d.used = 0
}

// Complete records the request and answers its decision. It is the Decider's
// implementation of schemaflux.Provider, which the library calls. The order of
// what it does is fixed: the request is counted; a context that has ended fails
// it; the limits are checked (a request with no decision, or past them, is
// refused with ErrUnsupported, and a decision that is not well formed with
// ErrInvalidRequest); the scripted failures apply; and then the answers are
// given.
func (d *Decider) Complete(ctx context.Context, req schemaflux.Request) (schemaflux.Response, error) {
	d.mu.Lock()
	call := len(d.requests)
	d.requests = append(d.requests, req)
	limits, usage, name := d.limits, d.usage, d.nameLocked()
	var fail error
	switch {
	case d.failAlways:
		fail = d.failing
	case call < d.failures:
		fail = d.failing
	}
	respond := d.respond
	d.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return schemaflux.Response{}, err
	}
	model := modelOf(req)
	if err := limits.Check(name, model, req.Decision); err != nil {
		return schemaflux.Response{}, err
	}
	if fail != nil {
		return schemaflux.Response{}, fail
	}

	var answers []schemaflux.DecisionAnswer
	if respond != nil {
		var err error
		if answers, err = respond(call, *req.Decision); err != nil {
			return schemaflux.Response{}, err
		}
	} else {
		var err error
		if answers, err = d.take(call, req.Decision); err != nil {
			return schemaflux.Response{}, err
		}
	}
	return schemaflux.Response{Model: model, Usage: usage, Answers: answers}, nil
}

// take hands out one answer for each question of decision, or fails the request
// having handed out none: a request is answered whole or not at all.
func (d *Decider) take(call int, decision *schemaflux.Decision) ([]schemaflux.DecisionAnswer, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	asked := len(decision.Questions)
	if left := len(d.answers) - d.used; left < asked {
		unanswered := decision.Questions[left]
		return nil, scriptFailure("no answer is scripted for question " + strconv.Itoa(left+1) +
			" of request " + strconv.Itoa(call+1) + " (" + describeQuestion(unanswered) +
			"; the request asks " + count(asked, "question", "questions") +
			"); scripted: " + d.describeAnswers())
	}
	answers := slices.Clone(d.answers[d.used : d.used+asked])
	d.used += asked
	return answers, nil
}

// describeAnswers lists the scripted answers and how many have been used, for
// the message of a question nothing answers. The caller holds the lock.
func (d *Decider) describeAnswers() string {
	const listed = 6
	if len(d.answers) == 0 {
		return "no answers"
	}
	parts := make([]string, 0, listed+1)
	for i, a := range d.answers {
		if i == listed {
			parts = append(parts, "and "+strconv.Itoa(len(d.answers)-listed)+" more")
			break
		}
		parts = append(parts, describeAnswer(a))
	}
	return count(len(d.answers), "answer", "answers") + " (" + strings.Join(parts, ", ") + "), " +
		strconv.Itoa(d.used) + " already used"
}

// describeQuestion is what kind of question it is and how big, never what it
// says: the wording is the caller's.
func describeQuestion(q schemaflux.DecisionQuestion) string {
	switch q.Kind {
	case schemaflux.DecisionYesNo:
		return "a yes or no"
	case schemaflux.DecisionChoice:
		return "a choice among " + count(len(q.Options), "option", "options")
	case schemaflux.DecisionScale:
		return "a scale from " + strconv.Itoa(q.Low) + " to " + strconv.Itoa(q.High)
	}
	return "a question of no kind"
}

// describeAnswer is one scripted answer, for a message.
func describeAnswer(a schemaflux.DecisionAnswer) string {
	var text string
	switch a.Kind {
	case schemaflux.DecisionYesNo:
		text = "no"
		if a.Yes {
			text = "yes"
		}
	case schemaflux.DecisionChoice:
		text = "chose " + quoted(a.Label, 24)
	case schemaflux.DecisionScale:
		text = "level " + strconv.Itoa(a.Level)
	default:
		text = "no kind"
	}
	if a.ConfidenceReported {
		text += " " + strconv.FormatFloat(a.ModelConfidence, 'g', 4, 64)
	}
	return text
}

// RetryPolicy implements provider.Retrier: a negative number of retries, which
// means never, so that a test that scripts one failure sees one.
func (d *Decider) RetryPolicy() (retries int, backoff time.Duration) { return -1, 0 }

// ModelFor returns the model the Decider means by a tier: "test-model" for every
// tier.
func (d *Decider) ModelFor(tier schemaflux.Tier) string { return modelName }
