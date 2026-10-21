package desk

import (
	"context"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/redact"
)

// The desk's smart features, each one a step: a function of a context that
// asks the model for one thing. flow.go puts them in order.

// translate renders text in another language.
func (d *Desk) translate(text, language string) schemaflux.Step[string] {
	return func(ctx context.Context) (string, error) {
		return schemaflux.Translate(ctx, text, language)
	}
}

// toEnglish is the text in English, as the routes a language leads to: the
// text as it is when it is in English, and otherwise its translation. A
// language the desk cannot name is translated too, from whatever it is: it is
// the reply that cannot be written in it, not the message that cannot be read.
//
// The labels are those of the Language field of Screen. A test holds the two
// to each other, since a label with no route is found only when a message in
// that language arrives.
func (d *Desk) toEnglish(text string) []schemaflux.Route[string] {
	into := d.translate(text, "English")
	return []schemaflux.Route[string]{
		schemaflux.To("English", keep(text)),
		schemaflux.To("Spanish", into),
		schemaflux.To("French", into),
		schemaflux.To("German", into),
		schemaflux.To("Portuguese", into),
		schemaflux.To("another language", into),
	}
}

// inTheirLanguage is the reply in the language the customer wrote in, as the
// routes a language leads to. A language the desk cannot name is one it cannot
// ask a translation into, and that reply goes in English: handed the label
// "another language" as if it were a language, the translation was measured
// to come back in Spanish for a customer who had written in French.
//
// The labels are those of the Language field of Screen, held to them by a
// test.
func (d *Desk) inTheirLanguage(reply string) []schemaflux.Route[string] {
	return []schemaflux.Route[string]{
		schemaflux.To("English", keep(reply)),
		schemaflux.To("Spanish", d.translate(reply, "Spanish")),
		schemaflux.To("French", d.translate(reply, "French")),
		schemaflux.To("German", d.translate(reply, "German")),
		schemaflux.To("Portuguese", d.translate(reply, "Portuguese")),
		schemaflux.To("another language", keep(reply)),
	}
}

// send is the reply as the customer will read it.
func (d *Desk) send(reply string, screen Screen) schemaflux.Step[string] {
	return func(ctx context.Context) (string, error) {
		return schemaflux.Select(ctx, screen.Language, d.inTheirLanguage(reply)...)
	}
}

// sensitive is what the desk masks: what the screen's question about personal
// data lists.
var sensitive = []string{"credit_card", "password", "phone", "address", "ssn"}

// redact masks the personal data in text, so that nothing after this step
// reads it.
func (d *Desk) redact(text string) schemaflux.Step[string] {
	return func(ctx context.Context) (string, error) {
		// The categories are the ones the screen asks about. Left at the
		// library's "all", the redaction was measured to mask "89 euros" as
		// financial data, and the amount the customer wants back was gone
		// from everything after it.
		redacted, err := redact.Text(ctx, text, redact.TextOptions{Categories: sensitive})
		return redacted.Text, err
	}
}

// triage answers everything the desk needs to know to act, in one call.
func (d *Desk) triage(text string) schemaflux.Step[Triage] {
	return func(ctx context.Context) (Triage, error) {
		return schemaflux.Assess[Triage](ctx, text, d.Options, d.Judgment)
	}
}

// details pulls out what the message says in so many words.
func (d *Desk) details(text string) schemaflux.Step[Details] {
	return func(ctx context.Context) (Details, error) {
		opts := schemaflux.ExtractOptions{Options: schemaflux.Options{Steering: "Leave a field empty when the message does not mention it. Do not guess."}}
		return schemaflux.Extract[Details](ctx, text, opts)
	}
}

// MatchQuestion is asked of a sender and a message, with the open tickets as
// the options. Its second sentence is there because of what the first one
// alone was measured to do: a parcel that had not arrived was attached to the
// open ticket about another customer's parcel, the two being about the same
// thing.
const MatchQuestion = "Which of these open tickets, if any, is this message from the same customer about the same case? A message about a similar problem with a different order, invoice or account is not about the same case."

// whoWrote is what the ticket match is asked of: the text, and the address it
// came from, since a ticket is the same customer's only if the sender is the
// customer it was opened for.
type whoWrote struct {
	From string `json:"from"`
	Text string `json:"text"`
}

// match finds the open ticket a message belongs to, if there is one. The
// options are the tickets' IDs with their customers and summaries, and the
// answer is mapped back to the ticket by ID, so two tickets with the same
// summary are two choices. With no open tickets nothing is asked.
func (d *Desk) match(from, text string) schemaflux.Step[Matched] {
	return func(ctx context.Context) (Matched, error) {
		shown := make([]string, len(d.Open))
		ids := make(map[string]string, len(d.Open))
		for i, ticket := range d.Open {
			shown[i] = ticket.Option()
			ids[shown[i]] = ticket.ID
		}
		option, found, err := schemaflux.WhichOrNone(ctx, whoWrote{From: from, Text: text}, MatchQuestion, shown, d.Options)
		return Matched{ID: ids[option], Found: found}, err
	}
}

// Matched is the open ticket a message belongs to, by its ID, or none.
type Matched struct {
	ID    string
	Found bool
}

// playbooks is how each team answers, as the routes a message can take. The
// labels are those of the Team field of Triage, held to them by a test.
func (d *Desk) playbooks() []schemaflux.Route[Playbook] {
	team := func(name, guidance string) schemaflux.Route[Playbook] {
		return schemaflux.To(name, keep(Playbook{Team: name, Guidance: guidance}))
	}
	return []schemaflux.Route[Playbook]{
		team("billing", "Explain the charge in plain words. Say what will be looked at and by when the customer will hear back."),
		team("technical", "Give the next thing to try as numbered steps. Ask for the app version only if it matters."),
		team("shipping", "Say where the parcel is as far as the message tells you, and what the desk will check."),
		team("account", "Say how to reach the account settings involved. Never ask for a password."),
	}
}

// brief is what a reply is written from.
type brief struct {
	Message  string   `json:"message"`
	Team     string   `json:"team"`
	Intent   string   `json:"intent"`
	Guidance string   `json:"guidance"`
	Details  Details  `json:"details"`
	Policies []string `json:"policies"`
}

// answer drafts a reply that deals with the message.
func (d *Desk) answer(about brief) schemaflux.Step[string] {
	return d.draft("Write the support desk's reply to this customer message. Deal with what they asked. Four sentences at most.", about)
}

// ask drafts a reply that asks for what the desk needs before it can act.
func (d *Desk) ask(about brief) schemaflux.Step[string] {
	return d.draft("Write the support desk's reply to this customer message. The message does not say enough to act on: ask, politely, for exactly what is missing. Three sentences at most.", about)
}

func (d *Desk) draft(instruction string, about brief) schemaflux.Step[string] {
	return func(ctx context.Context) (string, error) {
		opts := schemaflux.GenerateOptions{Options: schemaflux.Options{Steering: about.Guidance}, SeedData: about}
		reply, err := schemaflux.Generate[Reply](ctx, instruction, opts)
		return reply.Body, err
	}
}

// audit holds a draft to the desk's policies.
func (d *Desk) audit(draft string) schemaflux.Step[schemaflux.AuditResult[Reply]] {
	return func(ctx context.Context) (schemaflux.AuditResult[Reply], error) {
		return schemaflux.Audit(ctx, Reply{Body: draft}, schemaflux.AuditOptions{Policies: d.Policies})
	}
}

// redraft writes a draft again so that it no longer breaks the policies the
// audit found it breaking.
func (d *Desk) redraft(draft string, audit schemaflux.AuditResult[Reply]) schemaflux.Step[string] {
	return func(ctx context.Context) (string, error) {
		changes := make([]string, 0, len(audit.Issues)+len(d.Policies))
		for _, finding := range audit.Issues {
			changes = append(changes, finding.Suggestion)
		}
		changes = append(changes, d.Policies...)
		opts := schemaflux.RewriteOptions{Changes: changes}
		return schemaflux.Rewrite(ctx, draft, opts)
	}
}

// handoff summarises the message for whoever picks the ticket up.
func (d *Desk) handoff(text string) schemaflux.Step[string] {
	return func(ctx context.Context) (string, error) {
		opts := schemaflux.SummarizeOptions{TargetLength: 30, LengthUnit: "words"}
		return schemaflux.Summarize(ctx, text, opts)
	}
}

// next chooses what the desk does after the reply, from the question and the
// choices of the After type, which HandleFast asks in its first look.
func (d *Desk) next(text string) schemaflux.Step[string] {
	return func(ctx context.Context) (string, error) {
		return schemaflux.Which(ctx, text, question[After]("Next"), options[After]("Next"), d.Options)
	}
}

// again predicts whether the customer will write about this again.
func (d *Desk) again(text string) schemaflux.Step[bool] {
	return func(ctx context.Context) (bool, error) {
		return schemaflux.Is(ctx, text, question[After]("Again"), d.Options)
	}
}
