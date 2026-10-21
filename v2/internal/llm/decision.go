package llm

import (
	"math"
	"strconv"
	"strings"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// A decision is a question whose answer is bounded: a yes or a no, one of a
// set of labels, a level on a scale. A decision model answers exactly those,
// typed and with a probability, in a tenth of the time a generating model takes
// to write the same answer as JSON, and it cannot write text at all.
//
// A provider used to learn what was being decided in one way only, by reading
// the prompt, so a decision model could be plugged in only by an adapter that
// parsed prompts and wrote the JSON each operation expects. The request now
// says it: an operation whose answer is bounded describes the decision on
// CompletionRequest.Decision, beside the prompts it has always sent. A
// generating provider does not look at the field and answers the prompts. A
// decision provider reads the field, never the prompts, and answers on
// CompletionResponse.DecisionAnswers.
//
// # Writing an adapter for another decision API
//
// Implementing Provider and reading the decision off the request is all an
// adapter needs:
//
//   - refuse what cannot be answered, before anything is sent: a request with
//     no decision, and a decision past the API's limits. DecisionLimits.Check
//     does both and returns the classified error;
//   - ask the API the questions of req.Decision about req.Decision.State;
//   - return one DecisionAnswer for each question, in the questions' order, in
//     CompletionResponse.DecisionAnswers, with the probability the API reported
//     for each when it reports one. Leave Content empty: the library writes the
//     form its readers take (RenderDecisionAnswers), and refuses an answer that
//     is not one to the question;
//   - declare the limits with a DecisionLimits method, so that a provider that
//     wraps this one can route around a decision it cannot take.
//
// The decider test of this package's root has one written out in twenty lines.

// DecisionKind is the kind of answer a question takes.
type DecisionKind uint8

const (
	// DecisionYesNo is answered yes or no.
	DecisionYesNo DecisionKind = iota + 1

	// DecisionChoice is answered with one of the question's Options.
	DecisionChoice

	// DecisionScale is answered with a whole number from Low to High.
	DecisionScale
)

// String names the kind in the library's words. Zero, and any other value that
// is not one of the three, is no kind: an answer left at its zero value is an
// answer that was not given.
func (kind DecisionKind) String() string {
	switch kind {
	case DecisionYesNo:
		return "yes or no"
	case DecisionChoice:
		return "choice"
	case DecisionScale:
		return "scale"
	}
	return "no kind"
}

// Decision is what an operation is deciding, typed: the subject and the
// questions about it. Everything in it is read-only. The operation that fills
// it shares the questions with every call that asks them, and the options with
// its caller.
type Decision struct {
	// State is the subject the questions are about, written as JSON, as the
	// operation sends it to a generating model: a JSON string for text, an
	// object for a struct. It is the caller's data and never an instruction.
	State string

	// Guidance is the steering the caller gave for the judgment, or empty. It
	// is the calling program's instruction, which is why it travels apart from
	// the state.
	Guidance string

	// Questions are what is asked, in the order the answers come back in.
	Questions []DecisionQuestion
}

// DecisionQuestion is one question of a decision.
type DecisionQuestion struct {
	// Kind says which of the fields below apply.
	Kind DecisionKind

	// Ask is the question as the caller wrote it.
	Ask string

	// Options are the labels of a choice, at least two, each different from the
	// others. The answer is one of them, as it is written here.
	Options []string

	// Criteria describes each option, in the options' order, when the operation
	// has descriptions for them: a category's description, an item's content.
	// It is nil when it has none, and otherwise as long as Options; an option
	// with nothing to say of it has "".
	Criteria []string

	// Low and High are the two ends of a scale, both included, High above Low.
	Low, High int
}

// DecisionAnswer is the answer to one question.
type DecisionAnswer struct {
	// Kind is the kind of the question answered. Zero is no answer.
	Kind DecisionKind

	// Yes is the answer to a yes or no.
	Yes bool

	// Label is the option chosen, exactly as the question offered it.
	Label string

	// Level is the level on the scale.
	Level int

	// ModelConfidence is the probability the model reported for this answer,
	// from 0 to 1, when ConfidenceReported is true. It is the model's own claim
	// about its answer. It is not an accuracy, it is not calibrated by this
	// library, and it is not comparable between models.
	ModelConfidence float64

	// ConfidenceReported is false when the model reported no probability, which
	// is not a probability of zero.
	ConfidenceReported bool
}

// Why a decision that a decision provider was asked first was answered by the
// generating provider.
const (
	// DecisionEscalatedBelowFloor: a probability the decision provider reported
	// was below the floor, or it reported none while a floor was set.
	DecisionEscalatedBelowFloor = "below the floor"

	// DecisionEscalatedUnavailable: the decision provider failed in transit.
	DecisionEscalatedUnavailable = "unavailable"

	// DecisionEscalatedUnsupported: the decision provider refused the decision
	// as one it cannot take.
	DecisionEscalatedUnsupported = "unsupported"
)

// DecisionEscalation records that the answer on a response is not the answer of
// the decision provider that was asked first.
type DecisionEscalation struct {
	// Provider and Model are the decision provider that was asked first, and
	// the model it was asked under.
	Provider string
	Model    string

	// Reason is one of the DecisionEscalated constants.
	Reason string

	// ModelConfidence is the lowest probability the decision provider reported
	// for its answers, when it answered and ConfidenceReported is true. It is
	// that model's claim about the answer that was not used.
	ModelConfidence    float64
	ConfidenceReported bool
}

// LowestConfidence is the lowest probability among answers, and whether every
// answer reported one. A set of answers is no surer than its least sure answer,
// and a set with an answer that reports nothing has no confidence to give.
func LowestConfidence(answers []DecisionAnswer) (confidence float64, reported bool) {
	if len(answers) == 0 {
		return 0, false
	}
	lowest := math.Inf(1)
	for _, answer := range answers {
		if !answer.ConfidenceReported {
			return 0, false
		}
		lowest = math.Min(lowest, answer.ModelConfidence)
	}
	return lowest, true
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
	// through and the API finds too long comes back as the API's own refusal.
	MaxStateTokens int
}

// DecisionProvider is a provider that answers decisions typed and declares what
// it can take. A provider that wraps two, a generating one and a deciding one,
// reads the limits to send the deciding one only what it can take.
type DecisionProvider interface {
	Provider

	// DecisionLimits is what the provider can take.
	DecisionLimits() DecisionLimits
}

// DecisionLimitsOf returns the limits a provider declares, and whether it
// declares any. A provider held inside a middleware is reached through its
// Unwrap method, as Warm reaches it.
func DecisionLimitsOf(provider Provider) (DecisionLimits, bool) {
	// A chain is a handful of wrappers. The bound is for one that wraps itself.
	for depth := 0; provider != nil && depth < 32; depth++ {
		if decider, ok := provider.(interface{ DecisionLimits() DecisionLimits }); ok {
			return decider.DecisionLimits(), true
		}
		wrapper, ok := provider.(providerUnwrapper)
		if !ok {
			break
		}
		provider = wrapper.Unwrap()
	}
	return DecisionLimits{}, false
}

// Check refuses a request a decision provider cannot answer, before anything is
// sent. A request that carries no decision asks for text or a structure, which
// a decision provider cannot write, and a decision past a declared limit is one
// it would refuse or answer wrongly: both are an unsupported capability, which
// a provider that wraps this one reads as "ask the generating provider". A
// decision that is not well formed is an invalid request.
//
// The messages count and name positions. They repeat nothing of the state, the
// questions or the options, which are the caller's.
func (limits DecisionLimits) Check(provider, model string, decision *Decision) error {
	refuse := func(kind types.ErrorKind, message string) error {
		return &types.OperationError{Kind: kind, Op: "llm.decide", Provider: provider, Model: model, Message: message}
	}
	unsupported := func(message string) error { return refuse(types.KindUnsupportedCapability, message) }

	if decision == nil || len(decision.Questions) == 0 {
		return unsupported(provider + " answers decisions and nothing else, and the request carries no decision: it asks for text or a structure")
	}
	if limits.MaxQuestions > 0 && len(decision.Questions) > limits.MaxQuestions {
		return unsupported("the request asks " + strconv.Itoa(len(decision.Questions)) + " questions, and " + provider + " takes at most " + strconv.Itoa(limits.MaxQuestions))
	}
	for i, question := range decision.Questions {
		position := "question " + strconv.Itoa(i+1)
		if strings.TrimSpace(question.Ask) == "" {
			return refuse(types.KindInvalidRequest, position+" is blank")
		}
		switch question.Kind {
		case DecisionYesNo:
		case DecisionChoice:
			if len(question.Options) < 2 {
				return refuse(types.KindInvalidRequest, position+" offers "+strconv.Itoa(len(question.Options))+" options, and a choice needs two")
			}
			if question.Criteria != nil && len(question.Criteria) != len(question.Options) {
				return refuse(types.KindInvalidRequest, position+" describes "+strconv.Itoa(len(question.Criteria))+" options and offers "+strconv.Itoa(len(question.Options)))
			}
			if limits.MaxOptions > 0 && len(question.Options) > limits.MaxOptions {
				return unsupported(position + " offers " + strconv.Itoa(len(question.Options)) + " options, and " + provider + " takes at most " + strconv.Itoa(limits.MaxOptions))
			}
		case DecisionScale:
			if question.High <= question.Low {
				return refuse(types.KindInvalidRequest, "the scale of "+position+" leaves nothing to choose")
			}
			// Unsigned, so that a scale as wide as the ints does not wrap to a
			// width of nothing.
			width := uint64(question.High) - uint64(question.Low)
			if limits.MaxLevels > 0 && width >= uint64(limits.MaxLevels) {
				levels := "more"
				if width < math.MaxInt32 {
					levels = strconv.FormatUint(width+1, 10)
				}
				return unsupported("the scale of " + position + " has " + levels + " levels, and " + provider + " takes at most " + strconv.Itoa(limits.MaxLevels))
			}
		default:
			return refuse(types.KindInvalidRequest, position+" has no kind")
		}
	}
	if limits.MaxStateTokens > 0 && (len(decision.State)+3)/4 > limits.MaxStateTokens {
		return unsupported("the state is about " + strconv.Itoa((len(decision.State)+3)/4) + " tokens, and " + provider + " takes at most " + strconv.Itoa(limits.MaxStateTokens))
	}
	return nil
}

// RenderDecisionAnswers writes typed answers in the JSON form a generating
// model answers a decision in: one entry for each question under its key, a
// true or false for a yes or no, the number of the option for a choice, counted
// from one, and the level for a scale; and "confidence", the lowest probability
// reported, when every answer reports one.
//
//	{"q1":true,"q2":3,"q3":4,"confidence":0.67}
//
// It is the one place typed answers become that form, and it is the inverse of
// the decision reader in internal/ops, which is tested against it: what the
// reader reads from the rendered form is the answers that were rendered. A
// provider never writes this form itself.
//
// An answer that is not an answer to its question is refused, and nothing is
// rendered: fewer or more answers than questions, an answer of another kind or
// of none, a label the question did not offer, a level off the scale, a
// probability that is not a number from 0 to 1. A refusal names the question by
// its position and repeats nothing the model answered.
func RenderDecisionAnswers(decision *Decision, answers []DecisionAnswer) (string, error) {
	refuse := func(message string) (string, error) {
		return "", &types.OperationError{Kind: types.KindSchemaViolation, Op: "decision", Message: message}
	}
	if decision == nil || len(decision.Questions) == 0 {
		return refuse("typed answers came back for a request that carried no decision")
	}
	questions := decision.Questions
	if len(answers) != len(questions) {
		return refuse("the provider gave " + strconv.Itoa(len(answers)) + " answers to " + strconv.Itoa(len(questions)) + " questions")
	}

	rendered := make([]byte, 0, 12*len(questions)+32)
	rendered = append(rendered, '{')
	for i, question := range questions {
		answer := answers[i]
		position := "question " + strconv.Itoa(i+1)
		if answer.Kind != question.Kind {
			if answer.Kind < DecisionYesNo || answer.Kind > DecisionScale {
				return refuse("the provider did not answer " + position)
			}
			return refuse("the answer to " + position + " is a " + answer.Kind.String() + ", and the question takes a " + question.Kind.String())
		}
		if answer.ConfidenceReported && !(answer.ModelConfidence >= 0 && answer.ModelConfidence <= 1) {
			return refuse("the probability reported for " + position + " is not a number from 0 to 1")
		}
		if i > 0 {
			rendered = append(rendered, ',')
		}
		rendered = append(rendered, '"', 'q')
		rendered = strconv.AppendInt(rendered, int64(i+1), 10)
		rendered = append(rendered, '"', ':')
		switch question.Kind {
		case DecisionYesNo:
			rendered = strconv.AppendBool(rendered, answer.Yes)
		case DecisionChoice:
			number := 0
			for n, label := range question.Options {
				if label == answer.Label {
					number = n + 1
					break
				}
			}
			if number == 0 {
				return refuse("the answer to " + position + " is not one of its " + strconv.Itoa(len(question.Options)) + " options")
			}
			rendered = strconv.AppendInt(rendered, int64(number), 10)
		case DecisionScale:
			if answer.Level < question.Low || answer.Level > question.High {
				return refuse("the answer to " + position + " is not a level on its scale")
			}
			rendered = strconv.AppendInt(rendered, int64(answer.Level), 10)
		default:
			return refuse(position + " has no kind")
		}
	}
	if confidence, reported := LowestConfidence(answers); reported {
		rendered = append(rendered, `,"confidence":`...)
		rendered = strconv.AppendFloat(rendered, confidence, 'f', -1, 64)
	}
	rendered = append(rendered, '}')
	return string(rendered), nil
}
