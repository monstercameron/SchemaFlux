// Package sim is a stand-in for a model that answers the support desk's
// questions from a table of labelled messages.
//
// It exists so that the desk can be tested and timed with no model and no
// network. Each answer is the one the labels call for and takes a fixed time
// to arrive, so what a run measures is the shape of the program: how many
// calls it makes and how many of them it waits for one after another.
//
// The simulator does not know which flow is calling it. It reads each request
// as a provider would, works out which operation of the library made it and
// which message it is about, and answers in the shape that operation reads. A
// request it does not recognise, or a question the labels have no answer to,
// is an error and not a guess.
package sim

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/monstercameron/schemaflux/examples/v2/flowdesk/desk"
	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// matchQuestion is the one question the desk asks that is not a field of one of
// its types: the ticket match. It is the desk's own constant, so that the
// simulator and the desk cannot word it differently.
const matchQuestion = desk.MatchQuestion

// noneOfThese is the option the library adds to a choice that may have no
// answer.
const noneOfThese = "none of these"

// answers is the table every question is answered from, keyed by the question
// as it is put. The fused flow asks several of these in one request and the
// one-at-a-time flow asks each in a request of its own; both are answered from
// here, so both are given the same answers.
var answers = questionTable()

func questionTable() map[string]func(Case) any {
	table := map[string]func(Case) any{
		matchQuestion: func(c Case) any { return ticketOption(c.FollowsUp) },
	}
	// The questions of Screen, Triage and After are read from the desk's own
	// field tags, and each is answered by the field of the same name in the
	// label. After's are the labels' NextAction and WritesAgain.
	fields := func(kind reflect.Type, labelled func(Case) reflect.Value) {
		for i := 0; i < kind.NumField(); i++ {
			table[kind.Field(i).Tag.Get("ask")] = func(c Case) any { return labelled(c).Field(i).Interface() }
		}
	}
	fields(reflect.TypeFor[desk.Screen](), func(c Case) reflect.Value { return reflect.ValueOf(c.Screen) })
	fields(reflect.TypeFor[desk.Triage](), func(c Case) reflect.Value { return reflect.ValueOf(c.Triage) })
	fields(reflect.TypeFor[desk.After](), func(c Case) reflect.Value {
		return reflect.ValueOf(desk.After{Next: c.NextAction, Again: c.WritesAgain})
	})
	return table
}

// ticketOption is the option the desk shows for an open ticket, or the
// library's "none" for no ticket.
func ticketOption(id string) string {
	for _, ticket := range OpenTickets() {
		if ticket.ID == id {
			return ticket.Option()
		}
	}
	return noneOfThese
}

// Simulator is a provider that answers from the labelled cases. It is safe for
// concurrent use.
type Simulator struct {
	delay time.Duration
	cases map[string]Case
	log   Log
}

// New returns a simulator that takes delay to answer each call.
func New(delay time.Duration) *Simulator {
	cases := make(map[string]Case)
	for _, c := range Cases() {
		cases[c.Message.ID] = c
	}
	return &Simulator{delay: delay, cases: cases}
}

// Name is "local", the name the library has a model for with nothing
// configured. A provider under a name of its own would have no model and every
// call would fail before reaching it.
func (s *Simulator) Name() string { return "local" }

// EstimateCost is zero: nothing is spent.
func (s *Simulator) EstimateCost(schemaflux.Request) float64 { return 0 }

// RetryPolicy asks for no retries. A request the simulator refuses would be
// refused the same way again.
func (s *Simulator) RetryPolicy() (int, time.Duration) { return 0, 0 }

// Count is how many calls were made.
func (s *Simulator) Count() int { return s.log.Count() }

// MaxInFlight is the largest number of calls that were in flight at once.
func (s *Simulator) MaxInFlight() int { return s.log.MaxInFlight() }

// Waits is how many times the program waited: see Log.Waits.
func (s *Simulator) Waits() int { return s.log.Waits() }

// CriticalPath is the sum over the waits of the longest call of each: see
// Log.CriticalPath.
func (s *Simulator) CriticalPath() time.Duration { return s.log.CriticalPath() }

// Busy is the time during which at least one call was in flight: see Log.Busy.
func (s *Simulator) Busy() time.Duration { return s.log.Busy() }

// Calls returns every call made, in the order they started.
func (s *Simulator) Calls() []Call { return s.log.Calls() }

// Complete answers one request after the simulator's delay, or returns the
// context's error if it ends first.
func (s *Simulator) Complete(ctx context.Context, req schemaflux.Request) (schemaflux.Response, error) {
	operation := Operation(req)
	end := s.log.Begin(operation)
	defer end()

	timer := time.NewTimer(s.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return schemaflux.Response{}, ctx.Err()
	case <-timer.C:
	}

	content, err := s.answer(operation, req)
	if err != nil {
		return schemaflux.Response{}, err
	}
	// No usage is reported: the simulator reads no tokens, and a count made up
	// here would be added to a ledger as if it had been measured.
	return schemaflux.Response{
		Content:      content,
		Provider:     s.Name(),
		Model:        req.Model,
		FinishReason: "stop",
	}, nil
}

var (
	referenceInText = regexp.MustCompile(`\[ref (M-\d+)\]`)
	referenceInID   = regexp.MustCompile(`"id":"(M-\d+)"`)
)

// WithoutReferences returns the cases with the reference taken out of each
// message's text. The reference is how the simulator tells the cases apart, and
// a model has no use for it: left in, it was measured to be read as an order
// number and quoted in the hand-off note.
func WithoutReferences(cases []Case) []Case {
	plain := make([]Case, len(cases))
	for i, labelled := range cases {
		labelled.Message.Body = strings.TrimSpace(referenceInText.ReplaceAllString(labelled.Message.Body, ""))
		plain[i] = labelled
	}
	return plain
}

// caseOf finds the case a request is about: by the reference in the text, or,
// for a message that is passed whole, by its ID.
func (s *Simulator) caseOf(req schemaflux.Request) (Case, error) {
	for _, reference := range []*regexp.Regexp{referenceInText, referenceInID} {
		if match := reference.FindStringSubmatch(req.User); match != nil {
			if found, known := s.cases[match[1]]; known {
				return found, nil
			}
			return Case{}, fmt.Errorf("sim: there is no labelled case %s", match[1])
		}
	}
	return Case{}, fmt.Errorf("sim: the request carries no message reference, so the simulator cannot tell which case it is about")
}

func (s *Simulator) answer(operation string, req schemaflux.Request) (string, error) {
	if operation == "" {
		return "", fmt.Errorf("sim: the request is from no operation the desk makes (schema name %q)", req.SchemaName)
	}
	about, err := s.caseOf(req)
	if err != nil {
		return "", err
	}
	switch operation {
	case OpAssess:
		return assess(about, req.User)
	case OpAudit:
		return audit(about, req.User)
	case OpRedact:
		return redact(about)
	case OpExtract:
		return asJSON(about.Details)
	case OpGenerate:
		return generate(about, req.User)
	case OpTranslate:
		return translate(about, req.User)
	case OpSummarize:
		return labelled(about, "hand-off note", about.Handoff)
	default: // OpRewrite
		return labelled(about, "redraft", about.Redraft)
	}
}

func asJSON(value any) (string, error) {
	written, err := json.Marshal(value)
	return string(written), err
}

// labelled returns a text from the label, and refuses when the label has none:
// the desk asked for something the case says does not happen.
func labelled(about Case, what, text string) (string, error) {
	if text == "" {
		return "", fmt.Errorf("sim: case %s has no %s", about.Message.ID, what)
	}
	return text, nil
}

// assess answers the questions of an assessment as {"q1":...}: true or false
// for a yes or no, the 1-based number of the option for a choice, a whole
// number for a rating.
func assess(about Case, message string) (string, error) {
	asked, err := questions(message)
	if err != nil {
		return "", err
	}
	reply, err := labelsFor(about, asked, message)
	if err != nil {
		return "", err
	}
	return asJSON(reply)
}

// labelsFor answers a set of questions from the labels of a case, each under
// its key: true or false, the 1-based number of the option, or the level.
// subject is the text the questions were asked of, which a ticket match has to
// name the sender in. It is the one reading of the labels, for a request that
// puts the questions in a message and for one that carries them typed.
func labelsFor(about Case, asked []asked, subject string) (map[string]any, error) {
	reply := make(map[string]any, len(asked))
	for _, question := range asked {
		answer, known := answers[question.text]
		if !known {
			return nil, fmt.Errorf("sim: the labels have no answer to question %s of the assessment", question.key)
		}
		// A ticket is the same customer's only if the sender is named: a match
		// asked of the text alone has no customer to compare it with.
		if question.text == matchQuestion && !strings.Contains(subject, about.Message.From) {
			return nil, fmt.Errorf("sim: the ticket match for case %s does not carry the sender", about.Message.ID)
		}
		value := answer(about)
		refuse := func() (map[string]any, error) {
			return nil, fmt.Errorf("sim: the label of case %s for question %s is a %T, which does not answer %s",
				about.Message.ID, question.key, value, question.kind)
		}
		switch question.kind {
		case kindYesNo:
			yes, ok := value.(bool)
			if !ok {
				return refuse()
			}
			reply[question.key] = yes
		case kindChoice:
			label, ok := value.(string)
			if !ok {
				return refuse()
			}
			number := 0
			for i, option := range question.options {
				if option == label {
					number = i + 1
				}
			}
			// A message that is spam has no triage in its label, and a desk
			// that asks everything at once asks it all the same. A model
			// answers something; this answers the first option, and nothing
			// reads it, since a spam message is discarded.
			if number == 0 && label == "" && about.Screen.Spam {
				number = 1
			}
			if number == 0 {
				return nil, fmt.Errorf("sim: the label of case %s for question %s is not one of its %d options",
					about.Message.ID, question.key, len(question.options))
			}
			reply[question.key] = number
		default:
			level, ok := value.(int)
			if !ok {
				return refuse()
			}
			if level == 0 && about.Screen.Spam {
				level = question.low
			}
			if level < question.low || level > question.high {
				return nil, fmt.Errorf("sim: the label of case %s for question %s is off its scale",
					about.Message.ID, question.key)
			}
			reply[question.key] = level
		}
	}
	return reply, nil
}

var policyLine = regexp.MustCompile(`(?m)^(p\d+): (".*")$`)

// finding is one violation as an audit reports it.
type finding struct {
	Category       string  `json:"category"`
	Severity       float64 `json:"severity"`
	Field          string  `json:"field"`
	Issue          string  `json:"issue"`
	Evidence       string  `json:"evidence"`
	Recommendation string  `json:"recommendation"`
}

// audit reports on every policy the request lists, as
// {"policies":{"p1":[...],...},"findings":[]}. The first draft of a case whose
// label says it breaks a policy is reported under that policy; any other text,
// which includes the same case's redraft, breaks none.
func audit(about Case, message string) (string, error) {
	const heading = "\nData:\n"
	at := strings.LastIndex(message, heading)
	if at < 0 {
		return "", fmt.Errorf("sim: the audit carries no data")
	}
	var reply struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal([]byte(message[at+len(heading):]), &reply); err != nil {
		return "", fmt.Errorf("sim: the data of the audit is not the reply the desk audits")
	}
	first := about.Breaks != "" && reply.Body == about.Draft

	policies := map[string][]finding{}
	broken := false
	for _, match := range policyLine.FindAllStringSubmatch(message[:at], -1) {
		var policy string
		if err := json.Unmarshal([]byte(match[2]), &policy); err != nil {
			return "", fmt.Errorf("sim: policy %s is not a JSON string", match[1])
		}
		policies[match[1]] = []finding{}
		if first && strings.Contains(policy, about.Breaks) {
			broken = true
			policies[match[1]] = []finding{{
				Category:       "compliance",
				Severity:       0.8,
				Field:          "body",
				Issue:          "The reply does what the policy rules out: it commits the desk to " + about.Breaks + ".",
				Evidence:       "One sentence of the reply states it as a fact.",
				Recommendation: "Take out the sentence that commits the desk to " + about.Breaks + ", and say what the desk will check.",
			}}
		}
	}
	if first && !broken {
		return "", fmt.Errorf("sim: case %s breaks a policy the audit was not given", about.Message.ID)
	}
	return asJSON(map[string]any{"policies": policies, "findings": []finding{}})
}

// redact names the personal data in the text, as {"spans":[{"text","category"}]}.
// The library finds each value in the text and masks it.
func redact(about Case) (string, error) {
	type span struct {
		Text     string `json:"text"`
		Category string `json:"category"`
	}
	spans := make([]span, 0, len(about.Personal))
	for _, sensitive := range about.Personal {
		spans = append(spans, span(sensitive))
	}
	return asJSON(map[string]any{"spans": spans})
}

// generate writes the reply the desk asked for, as {"body":...}. The label
// holds one draft, which is an answer or a question back; a request for the
// other kind is the desk taking the wrong branch, and is refused.
func generate(about Case, message string) (string, error) {
	asks := strings.Contains(message, "ask, politely, for exactly what is missing")
	if asks == about.Triage.Answerable {
		return "", fmt.Errorf("sim: case %s was sent for the wrong kind of reply: its label says answerable is %t",
			about.Message.ID, about.Triage.Answerable)
	}
	draft, err := labelled(about, "draft", about.Draft)
	if err != nil {
		return "", err
	}
	return asJSON(desk.Reply{Body: draft})
}

var translateTo = regexp.MustCompile(`Translate to ([A-Za-z ]+)`)

// carries reports whether a request's message holds a text. The library
// writes the text an operation works on into the message as a JSON string, so
// that nothing in it can be taken for an instruction: a line break arrives as
// the two characters of its escape and a quotation mark with a backslash before
// it. The text is looked for as the library writes it, and as it stands, which
// is how an operation that does not quote its input would send it.
func carries(message, text string) bool {
	if strings.Contains(message, text) {
		return true
	}
	var quoted bytes.Buffer
	encoder := json.NewEncoder(&quoted)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(text); err != nil {
		return false
	}
	// Without the quotation marks at each end and the line break Encode adds.
	inner := strings.TrimSuffix(quoted.String(), "\n")
	inner = strings.TrimPrefix(strings.TrimSuffix(inner, "\""), "\"")
	return strings.Contains(message, inner)
}

// translate renders the message in English, or the reply in the language the
// message was written in. Both keep the reference. What is translated has to be
// what the desk works from: the message's text, subject and body, for the
// translation into English, and the reply as it stands for the one back. A
// request that carries something else is the desk translating the wrong text.
func translate(about Case, message string) (string, error) {
	match := translateTo.FindStringSubmatch(message)
	if match == nil {
		return "", fmt.Errorf("sim: the translation names no language")
	}
	switch strings.TrimSpace(match[1]) {
	case "English":
		if !carries(message, about.Message.Text()) {
			return "", fmt.Errorf("sim: the translation of case %s into English is not of the message's text", about.Message.ID)
		}
		return labelled(about, "English text", about.English)
	case about.Screen.Language:
		if !carries(message, finalReply(about)) {
			return "", fmt.Errorf("sim: the translation of case %s into %s is not of its reply", about.Message.ID, about.Screen.Language)
		}
		return labelled(about, "reply in "+about.Screen.Language, about.Sent)
	}
	return "", fmt.Errorf("sim: case %s was sent for translation into a language its label does not name", about.Message.ID)
}

// finalReply is the reply of a case before it is translated: the redraft when
// the first draft breaks a policy, and otherwise the draft.
func finalReply(about Case) string {
	if about.Redraft != "" {
		return about.Redraft
	}
	return about.Draft
}
