package sim

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// The operations the desk makes, as the simulator and the benchmark name them.
const (
	OpAssess    = "assess"
	OpAudit     = "audit"
	OpRedact    = "redact"
	OpExtract   = "extract"
	OpGenerate  = "generate"
	OpTranslate = "translate"
	OpSummarize = "summarize"
	OpRewrite   = "rewrite"
)

// The library names the answer it declares for three of the operations, and
// the name is the same for every call of that operation.
var operationBySchema = map[string]string{
	"assessment":      OpAssess,
	"audit":           OpAudit,
	"redaction_spans": OpRedact,
}

// The other five are told apart by the first sentence of their instructions,
// which the library writes and which does not change with the call. Extract
// and Generate do declare an answer, but under the name of the caller's own
// type, which says what the answer is and not which operation asked for it.
var operationByInstruction = []struct {
	opening   string
	operation string
}{
	{"You are a data extraction expert.", OpExtract},
	{"You are a data generation expert.", OpGenerate},
	{"You are a translation expert.", OpTranslate},
	{"You are a text summarization expert.", OpSummarize},
	{"You are a text rewriting expert.", OpRewrite},
}

// Operation reports which operation of the library made a request, or "" when
// it is none the desk makes.
func Operation(req schemaflux.Request) string {
	if operation, named := operationBySchema[req.SchemaName]; named {
		return operation
	}
	for _, known := range operationByInstruction {
		if strings.Contains(req.System, known.opening) {
			return known.operation
		}
	}
	return ""
}

// Answered is one question of an assessment and the answer a reply gave it.
type Answered struct {
	Question string
	// Answer is "yes" or "no", the label of the option chosen, or the rating
	// as a number. It is "no answer" when the reply did not answer the
	// question in the kind it was asked.
	Answer string
}

// Answers reads the questions of an assessment out of its request and the
// answers out of its reply, in the order they were asked. It is how the
// benchmark shows what a run decided: the request and the reply are all a
// provider sees of a decision, and the reply alone is {"q1":true,"q2":3}.
func Answers(req schemaflux.Request, reply string) ([]Answered, error) {
	asked, err := questions(req.User)
	if err != nil {
		return nil, err
	}
	var given map[string]json.RawMessage
	if err := json.Unmarshal([]byte(reply), &given); err != nil {
		return nil, fmt.Errorf("sim: the reply to the assessment is not a JSON object")
	}
	found := make([]Answered, 0, len(asked))
	for _, question := range asked {
		found = append(found, Answered{Question: question.text, Answer: answerTo(question, given[question.key])})
	}
	return found, nil
}

// answerTo writes one answer of a reply the way a person would say it.
func answerTo(question asked, raw json.RawMessage) string {
	const none = "no answer"
	switch question.kind {
	case kindYesNo:
		var yes bool
		if json.Unmarshal(raw, &yes) != nil {
			return none
		}
		if yes {
			return "yes"
		}
		return "no"
	case kindChoice:
		number, whole := wholeNumber(raw)
		if !whole || number < 1 || number > len(question.options) {
			return none
		}
		return question.options[number-1]
	default:
		level, whole := wholeNumber(raw)
		if !whole {
			return none
		}
		return strconv.Itoa(level)
	}
}

// wholeNumber reads a JSON number that is a whole number however it is written:
// 2 and 2.0 are the same, as the library reads them. A string, null, a number with a
// fraction and one too large for an int are not.
func wholeNumber(raw json.RawMessage) (int, bool) {
	var number float64
	if string(bytes.TrimSpace(raw)) == "null" || json.Unmarshal(raw, &number) != nil || number != math.Trunc(number) || math.Abs(number) > 1<<31 {
		return 0, false
	}
	return int(number), true
}

// The kinds of answer a question of an assessment takes.
const (
	kindYesNo  = "yes or no"
	kindChoice = "one of the options below"
	kindRating = "a rating"
)

// asked is one question of an assessment, as the request puts it.
type asked struct {
	key  string
	kind string
	text string
	// options are the labels of a choice, in the order they are numbered.
	options []string
	// low and high are the ends of a rating's scale.
	low, high int
}

var (
	questionLine = regexp.MustCompile(`^(q\d+) \((yes or no|one of the options below|a rating from (-?\d+) to (-?\d+))\): (".*")$`)
	optionLine   = regexp.MustCompile(`^  (\d+): (".*")$`)
)

// questions reads the questions of an assessment out of its message. They sit
// between the "Questions:" heading and the subject, one to a line, each with
// its text written as a JSON string, and a choice is followed by its numbered
// options.
//
// A message that is not laid out that way is an error. Reading what could be
// read and answering that would leave a question unanswered, which the library
// refuses, and the refusal would name the answer and not the layout.
func questions(message string) ([]asked, error) {
	const heading = "Questions:\n"
	start := strings.Index(message, heading)
	if start < 0 {
		return nil, fmt.Errorf("sim: the assessment has no list of questions")
	}
	list := message[start+len(heading):]
	// The subject is written as JSON on one line, so the first heading is the
	// real one whatever the subject says.
	if end := strings.Index(list, "\nSubject:\n"); end >= 0 {
		list = list[:end]
	}

	var found []asked
	for _, line := range strings.Split(list, "\n") {
		if match := questionLine.FindStringSubmatch(line); match != nil {
			question := asked{key: match[1], kind: match[2]}
			if err := json.Unmarshal([]byte(match[5]), &question.text); err != nil {
				return nil, fmt.Errorf("sim: the text of %s is not a JSON string", question.key)
			}
			if match[3] != "" {
				question.kind = kindRating
				question.low, _ = strconv.Atoi(match[3])
				question.high, _ = strconv.Atoi(match[4])
			}
			found = append(found, question)
			continue
		}
		if match := optionLine.FindStringSubmatch(line); match != nil {
			if len(found) == 0 || found[len(found)-1].kind != kindChoice {
				return nil, fmt.Errorf("sim: an option is listed under no choice")
			}
			var label string
			if err := json.Unmarshal([]byte(match[2]), &label); err != nil {
				return nil, fmt.Errorf("sim: an option of %s is not a JSON string", found[len(found)-1].key)
			}
			last := &found[len(found)-1]
			last.options = append(last.options, label)
		}
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("sim: the assessment asks no question the simulator can read")
	}
	return found, nil
}
