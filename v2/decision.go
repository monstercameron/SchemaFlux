package schemaflux

import (
	"slices"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
)

// A Decision is what an operation is deciding, typed: the subject and the
// questions about it. A provider that generates text ignores it and answers the
// prompts of the request. A decision provider reads it, never the prompts, and
// answers in Response.Answers. Everything in it is read-only and is shared
// between calls: a Provider that wants to change it copies it first.
type Decision struct {
	// State is the subject the questions are about, written as JSON, as the
	// operation sends it to a model that generates text: a JSON string for
	// text, an object for a struct. It is the caller's data and never an
	// instruction. Empty means the questions have no subject.
	State string

	// Guidance is the steering the caller gave for the judgment: the calling
	// program's own instruction, which travels apart from the state. Empty
	// means none.
	Guidance string

	// Questions are what is asked, in the order the answers come back in. A
	// Decision with none asks nothing.
	Questions []DecisionQuestion
}

// A DecisionQuestion is one question of a Decision.
type DecisionQuestion struct {
	// Kind says which of the fields below apply. The zero value is no kind,
	// and a question with it is not well formed.
	Kind DecisionKind

	// Ask is the question as the caller wrote it. Empty is not a question.
	Ask string

	// Options are the labels of a choice, at least two, each different from
	// the others. The answer is one of them, as written here. Nil for a yes or
	// no and for a scale.
	Options []string

	// Criteria describes each option, in the options' order, when the
	// operation has descriptions for them: a category's description, an item's
	// content. Nil means it has none; otherwise it is as long as Options, with
	// "" for an option that has nothing to say of it.
	Criteria []string

	// Low and High are the two ends of a scale, both included, High above Low.
	// Zero for a question that is not a scale.
	Low, High int
}

// A DecisionKind is the kind of answer a question takes. The zero value is no
// kind: an answer left at it is an answer that was not given.
type DecisionKind uint8

const (
	// DecisionYesNo is answered yes or no.
	DecisionYesNo DecisionKind = iota + 1

	// DecisionChoice is answered with one of the question's Options.
	DecisionChoice

	// DecisionScale is answered with a whole number from Low to High.
	DecisionScale
)

// A DecisionAnswer is the answer to one question of a Decision.
type DecisionAnswer struct {
	// Kind is the kind of the question answered. The zero value is no answer.
	Kind DecisionKind

	// Yes is the answer to a yes or no. False for any other kind.
	Yes bool

	// Label is the option chosen, exactly as the question offered it. Empty
	// for any other kind.
	Label string

	// Level is the level on the scale. Zero for any other kind.
	Level int

	// ModelConfidence is the probability the model reported for this answer,
	// from 0 to 1, when ConfidenceReported is true. It is the model's own claim
	// about its answer: not an accuracy, not calibrated by this library, and
	// not comparable between models. Zero when none was reported.
	ModelConfidence float64

	// ConfidenceReported is false when the model reported no probability,
	// which is not a probability of zero.
	ConfidenceReported bool
}

// A DecisionEscalation says that a decision provider was asked first and its
// answer was passed over: its probability was below the floor, or it failed or
// refused and the provider that generates text answered. Response.Provider and
// Response.Model name who did answer.
type DecisionEscalation struct {
	// Provider and Model are the decision provider that was asked first, and
	// the model it was asked under.
	Provider string
	Model    string

	// Reason is why its answer was passed over: "below the floor",
	// "unavailable" or "unsupported".
	Reason string

	// ModelConfidence is the lowest probability the decision provider reported
	// for its answers, when it answered and ConfidenceReported is true: that
	// model's claim about the answer that was not used. Zero when none was
	// reported.
	ModelConfidence float64

	// ConfidenceReported is false when the decision provider reported no
	// probability, which is not a probability of zero.
	ConfidenceReported bool
}

// DecisionLimits is what a decision provider declares it can take. A field left
// at zero declares no limit of that kind.
type DecisionLimits struct {
	// MaxQuestions is the most questions one request may ask.
	MaxQuestions int

	// MaxOptions is the most options a choice may offer.
	MaxOptions int

	// MaxLevels is the most levels a scale may have.
	MaxLevels int

	// MaxStateTokens is the longest state, in tokens, estimated at four bytes
	// to a token as the cost estimate does. It is an estimate: a state it lets
	// through and the provider finds too long comes back as the provider's own
	// refusal.
	MaxStateTokens int
}

// Check refuses a request a decision provider cannot answer, before anything is
// sent. provider and model name the route in the error. A request that carries
// no decision asks for text or a structure, which a decision provider cannot
// write, and a decision past a declared limit is one it would refuse or answer
// wrongly: both are an error of kind ErrUnsupported, which a provider that wraps
// this one reads as "ask the provider that generates text". A decision that is
// not well formed is an error of kind ErrInvalidRequest. The messages count and
// name positions; they repeat nothing of the state, the questions or the
// options.
func (l DecisionLimits) Check(provider, model string, d *Decision) error {
	err := llm.DecisionLimits(l.internal()).Check(provider, model, d.internal())
	if err == nil {
		return nil
	}
	return convertError("decision", err)
}

// internal is the limits as the internal package declares them.
func (l DecisionLimits) internal() llm.DecisionLimits {
	return llm.DecisionLimits{
		MaxQuestions:   l.MaxQuestions,
		MaxOptions:     l.MaxOptions,
		MaxLevels:      l.MaxLevels,
		MaxStateTokens: l.MaxStateTokens,
	}
}

// decisionLimitsFromInternal is the inverse of DecisionLimits.internal.
func decisionLimitsFromInternal(l llm.DecisionLimits) DecisionLimits {
	return DecisionLimits{
		MaxQuestions:   l.MaxQuestions,
		MaxOptions:     l.MaxOptions,
		MaxLevels:      l.MaxLevels,
		MaxStateTokens: l.MaxStateTokens,
	}
}

// internal is the decision as the internal package declares it, or nil for nil.
// The questions are copied one level deep (the slices of options and criteria
// are shared, as the internal contract has them read-only).
func (d *Decision) internal() *llm.Decision {
	if d == nil {
		return nil
	}
	converted := &llm.Decision{State: d.State, Guidance: d.Guidance}
	if d.Questions != nil {
		converted.Questions = make([]llm.DecisionQuestion, len(d.Questions))
		for i, q := range d.Questions {
			converted.Questions[i] = llm.DecisionQuestion{
				Kind:     llm.DecisionKind(q.Kind),
				Ask:      q.Ask,
				Options:  q.Options,
				Criteria: q.Criteria,
				Low:      q.Low,
				High:     q.High,
			}
		}
	}
	return converted
}

// decisionFromInternal is the inverse of Decision.internal.
func decisionFromInternal(d *llm.Decision) *Decision {
	if d == nil {
		return nil
	}
	converted := &Decision{State: d.State, Guidance: d.Guidance}
	if d.Questions != nil {
		converted.Questions = make([]DecisionQuestion, len(d.Questions))
		for i, q := range d.Questions {
			converted.Questions[i] = DecisionQuestion{
				Kind:     DecisionKind(q.Kind),
				Ask:      q.Ask,
				Options:  q.Options,
				Criteria: q.Criteria,
				Low:      q.Low,
				High:     q.High,
			}
		}
	}
	return converted
}

// answersToInternal converts a decision provider's answers, or returns nil for
// none. The slice is a copy.
func answersToInternal(answers []DecisionAnswer) []llm.DecisionAnswer {
	if answers == nil {
		return nil
	}
	converted := make([]llm.DecisionAnswer, len(answers))
	for i, a := range answers {
		converted[i] = llm.DecisionAnswer{
			Kind:               llm.DecisionKind(a.Kind),
			Yes:                a.Yes,
			Label:              a.Label,
			Level:              a.Level,
			ModelConfidence:    a.ModelConfidence,
			ConfidenceReported: a.ConfidenceReported,
		}
	}
	return converted
}

// answersFromInternal is the inverse of answersToInternal.
func answersFromInternal(answers []llm.DecisionAnswer) []DecisionAnswer {
	if answers == nil {
		return nil
	}
	converted := make([]DecisionAnswer, len(answers))
	for i, a := range answers {
		converted[i] = DecisionAnswer{
			Kind:               DecisionKind(a.Kind),
			Yes:                a.Yes,
			Label:              a.Label,
			Level:              a.Level,
			ModelConfidence:    a.ModelConfidence,
			ConfidenceReported: a.ConfidenceReported,
		}
	}
	return converted
}

// escalationToInternal converts an escalation, or returns nil for nil.
func escalationToInternal(e *DecisionEscalation) *llm.DecisionEscalation {
	if e == nil {
		return nil
	}
	return &llm.DecisionEscalation{
		Provider:           e.Provider,
		Model:              e.Model,
		Reason:             e.Reason,
		ModelConfidence:    e.ModelConfidence,
		ConfidenceReported: e.ConfidenceReported,
	}
}

// escalationFromInternal is the inverse of escalationToInternal.
func escalationFromInternal(e *llm.DecisionEscalation) *DecisionEscalation {
	if e == nil {
		return nil
	}
	return &DecisionEscalation{
		Provider:           e.Provider,
		Model:              e.Model,
		Reason:             e.Reason,
		ModelConfidence:    e.ModelConfidence,
		ConfidenceReported: e.ConfidenceReported,
	}
}

// cloneStrings returns a copy of s, nil for nil.
func cloneStrings(s []string) []string { return slices.Clone(s) }
