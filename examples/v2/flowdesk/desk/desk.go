// Package desk is a support desk that handles an incoming message from start
// to finish, and whose every decision about what happens next is made by an
// operation of schemaflux.
//
// It exists to benchmark the library. A program built this way spends nearly
// all of its time waiting for a model, so how many times it waits, how many of
// those waits overlap, and how long each one is are the whole of its speed.
//
// # The rule, and what its test proves
//
// The package is written so that the code never decides anything about a
// message itself: whether it is spam, which team owns it, whether a person has
// to approve the reply, all of that is asked of the model, and the branch is
// taken by a combinator (schemaflux.Branch, schemaflux.Select) that is handed
// the answer. TestThePackageBranchesOnlyOnErrors holds the package's source
// files to it, and what it checks is this, and no more. It reads the syntax of
// every non-test file in this directory and rejects:
//
//   - an `if` whose condition is not an error check, or that has an `else`. An
//     error check is `x != nil` with x an identifier named err or ending in
//     Err, or a call to errors.Is or errors.As;
//   - a `switch`, a type switch, a `select`, a `for` that is not a range, a
//     `goto`, a labelled statement, a `defer`;
//   - `&&` and `||`;
//   - any comparison (==, !=, <, <=, >, >=) that is not the condition of such
//     an `if`, so one passed as an argument or assigned is found;
//   - a call to the builtins min, max and recover, to anything in the packages
//     cmp and slices, and to strings.Contains, ContainsAny, ContainsRune,
//     ContainsFunc, HasPrefix, HasSuffix and EqualFold;
//   - a `return`, `break` or `continue` in the body of a `for` or a range loop,
//     outside any function literal in it, which is a branch on the first item
//     written as a loop;
//   - a map or slice indexed by a comparison, a logical expression or a
//     negation, a map whose keys are bool, and the comma-ok forms of a type
//     assertion, a map index and a receive.
//
// It is a check of syntax. It has no types, so it cannot see a boolean held in
// a variable and used to index something that was not declared as a map of
// bool, and it does not follow a call into another package: a function that
// branches inside its own body is outside it, and the check does not claim
// otherwise. A range loop is allowed because it maps a list to a list and
// chooses nothing. The loops in this package are of two kinds: those that map
// configuration (the options and routes made from the open tickets, the labels
// of a tag with their spaces trimmed), and the one that collects the
// recommendations of an audit into the changes a redraft is asked for. None of
// them leaves early or tests anything, which the rule on loop bodies holds.
//
// # Known limits
//
// The first draft is audited against the desk's policies, and a draft that
// fails is written again. The redraft is not audited again, so a redraft that
// breaks a policy is sent. A reply that is translated into the customer's
// language is not audited either: the audit is of the English draft.
//
// A message in a language the desk cannot name ("another language") is
// translated into English to be read, and the reply goes in English, because
// the desk has no language to translate the reply into. Outcome.Language says
// "another language" and nothing else says that the reply was not translated.
package desk

import (
	"context"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// Message is one incoming message.
type Message struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// Text is the text the desk works from: the subject and the body together, the
// subject first. Every question of every flow is asked of it, or of what
// translation and redaction make of it, so that the three flows read the same
// words.
func (m Message) Text() string {
	return m.Subject + "\n\n" + m.Body
}

// Ticket is a conversation the desk already has open, and the customer it was
// opened for.
type Ticket struct {
	ID string
	// Customer is the address the ticket was opened from.
	Customer string
	Summary  string
}

// Option is the ticket as the model is shown it when it is asked which ticket
// a message belongs to: the ID, the customer and the summary together. Two
// tickets with the same summary are two options, and the answer is mapped back
// to the ticket by its ID.
func (t Ticket) Option() string {
	return t.ID + " (" + t.Customer + "): " + t.Summary
}

// The statuses a message can end in.
const (
	StatusDiscarded = "discarded"
	StatusAnswered  = "answered"
	StatusAsked     = "asked for more"
	StatusHeld      = "held for approval"
)

// Screen is the first look at a message as it arrived: the things that decide
// what may be done with the text at all.
type Screen struct {
	Spam bool `ask:"Is this message spam, a marketing blast or an automated notification, and not a customer writing to a support desk?"`
	// Language decides whether the message is translated and what language the
	// reply is written in. A language the desk has no name for is "another
	// language".
	Language     string `ask:"What language is the message written in?" oneof:"English,Spanish,French,German,Portuguese,another language"`
	PersonalData bool   `ask:"Does the message contain a payment card number, a password, a phone number, a street address or a government ID number? An email address, a name or an order number does not count."`
}

// Triage is what the desk needs to know about a message to act on it.
type Triage struct {
	Team       string `ask:"Which team should handle this message?" oneof:"billing,technical,shipping,account"`
	Intent     string `ask:"What is the customer doing?" oneof:"asking a question,making a complaint,requesting an action,giving feedback"`
	Urgency    int    `ask:"How urgent is it, where 1 can wait a week and 5 means the customer is blocked or losing money right now?" scale:"1-5"`
	Answerable bool   `ask:"Does the message say what the problem or the request is, specifically enough that the desk knows what to look into? Answer no only when it is too vague to tell what it is about."`
	NeedsHuman bool   `ask:"Is the customer demanding money back, or threatening a chargeback or legal action, so that a person has to approve the reply before it is sent?"`
}

// Details is what the message says in so many words. Each is empty when the
// message does not mention it.
type Details struct {
	OrderID string `json:"order_id"`
	Product string `json:"product"`
	Amount  string `json:"amount"`
}

// Playbook is how one team answers.
type Playbook struct {
	Team     string
	Guidance string
}

// Reply is a drafted reply.
type Reply struct {
	Body string `json:"body"`
}

// Outcome is what became of a message.
type Outcome struct {
	MessageID string
	Status    string

	// Language is the language the message was written in. The reply is in it
	// too, except for "another language", where it is in English.
	Language string
	Team     string
	Intent   string
	Urgency  int

	// TicketID is the open ticket the message was attached to, and New reports
	// that there was none and one was opened.
	TicketID string
	New      bool

	Details Details
	Reply   string
	// Redrafted reports that the first draft failed the desk's policies and
	// was written again.
	Redrafted bool

	Handoff        string
	NextAction     string
	WillWriteAgain bool
}

// Desk is a support desk: the tickets it has open and the rules its replies
// are held to.
type Desk struct {
	Open     []Ticket
	Policies []string

	// Options are passed to every decision the desk makes: the questions of
	// the screen, of the triage, of the ticket match, and of what follows the
	// reply. They carry the model, the tier and the reasoning effort. The steps
	// that are not decisions (the translation, the redaction, the extraction
	// of details, the draft, the redraft, the hand-off note and the audit) take
	// nothing from here: they run on the client's model and effort.
	// The zero value leaves each to the library.
	Options schemaflux.Options

	// Judgment is laid over Options for the triage questions, in every flow:
	// the team, the intent, the urgency, whether the message says enough to act
	// on, and whether a person has to approve the reply. They are the questions
	// whose answers were measured to change when the model reasons less: whether
	// a message says enough to act on is a judgment a model answering at once
	// gets wrong more often. A desk that turns reasoning down everywhere else
	// can leave it up here.
	//
	// HandleOneAtATime and Handle ask the triage questions alone, so Judgment
	// applies to exactly them. HandleFast asks the screen, the triage and what
	// follows the reply in one call, so it is applied to that whole call: it
	// cannot be laid over some of the questions of one request.
	Judgment schemaflux.Options
}

// New returns a desk with the policies every reply is held to.
func New(open []Ticket) *Desk {
	return &Desk{
		Open: open,
		Policies: []string{
			"A reply must not promise a refund, a credit or a delivery date.",
			"A reply must not ask the customer for a password or a full card number.",
			"A reply must not blame the customer.",
		},
	}
}

// keep is the step that does nothing to a value: the other side of a branch
// that has work on one side only.
func keep[T any](value T) schemaflux.Step[T] {
	return func(context.Context) (T, error) { return value, nil }
}
