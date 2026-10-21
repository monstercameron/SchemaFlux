package desk

import (
	"context"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// After is what follows the reply: what the desk does next, and whether the
// customer is expected back.
type After struct {
	Next  string `ask:"What should the desk do after replying?" oneof:"close the ticket,wait for the customer,follow up tomorrow,escalate to a manager"`
	Again bool   `ask:"Is this customer likely to write to the desk again about the same matter within a week?"`
}

// Look is everything the desk asks about a message, asked as it arrives and in
// one call: the screen, the triage and what follows the reply.
type Look struct {
	Screen
	Triage
	After
}

// Second is the triage and what follows, asked again of a message's text once
// it has been translated or redacted.
type Second struct {
	Triage
	After
}

// reading is what the desk knows about a message once it has been read: the
// text every later step works from, and what was decided about it.
type reading struct {
	Text    string
	Triage  Triage
	After   After
	Details Details
	Matched Matched
}

// HandleFast takes a message from arrival to outcome in three waits where
// Handle takes four, and six calls where Handle makes nine.
//
// Handle looks at a message, and only then, knowing whether it has to be
// translated or redacted, asks what it is about. Most messages need neither,
// and for those the second look is at the same text as the first. HandleFast
// asks everything of the message as it arrives, in one call, beside the
// extraction and the ticket match. When the message turns out to need neither
// translating nor redacting, what it learned stands. When it needs either, the
// text is put right and the questions are asked again of the text as it then
// is: that message costs more than Handle would have spent on it, and every
// other message costs less. The same Select on the language that Handle
// translates by decides here whether the message is read again.
//
// The waits for a message in English with no personal data in it whose draft
// passes: the look, the details and the ticket match, at once; the draft and
// the hand-off note, at once; the audit of the draft.
//
// Two things it does differently from Handle, and they are the price of that:
//
// The details and the ticket match are sent the text as it arrived, in the
// same wait as the look, before anything has said whether it holds personal
// data. Handle sends the unredacted text to one call, the screen. HandleFast
// sends it to three: the look, the details and the match. For a message that is
// redacted, the first reading is thrown away and the second is of the redacted
// text, but the three calls have been made.
//
// A call whose result is thrown away can still fail the run. Spam is
// discarded, and the details and the ticket match made beside the look are
// discarded with it; if either of them fails, the run returns its error where
// Handle would have returned Discarded after one call. The same goes for the
// first details and match of a message that is read again. Spam costs three
// calls here, where Handle spends one.
//
// Desk.Judgment is applied to the look as a whole, which carries the screen
// and what follows the reply with the triage, and to the second look.
func (d *Desk) HandleFast(ctx context.Context, message Message) (Outcome, error) {
	arrived := message.Text()
	look, details, matched, err := schemaflux.Gather3(ctx, d.look(arrived), d.details(arrived), d.match(message.From, arrived))
	if err != nil {
		return Outcome{}, err
	}
	first := reading{Text: arrived, Triage: look.Triage, After: look.After, Details: details, Matched: matched}
	return schemaflux.Branch(ctx, look.Spam, d.discard(message), d.finish(message, look.Screen, first))
}

// look asks everything about a message as it arrives.
func (d *Desk) look(arrived string) schemaflux.Step[Look] {
	return func(ctx context.Context) (Look, error) {
		return schemaflux.Assess[Look](ctx, arrived, d.Options, d.Judgment)
	}
}

// readings is how a message is read once the screen has said what language it
// is in: the first reading stands, as far as the message needs no redaction,
// for a message in English, and any other language puts the text right and
// reads it again. The labels are those of the Language field of Screen, held to
// them by a test.
func (d *Desk) readings(standing, rereading schemaflux.Step[reading]) []schemaflux.Route[reading] {
	return []schemaflux.Route[reading]{
		schemaflux.To("English", standing),
		schemaflux.To("Spanish", rereading),
		schemaflux.To("French", rereading),
		schemaflux.To("German", rereading),
		schemaflux.To("Portuguese", rereading),
		schemaflux.To("another language", rereading),
	}
}

// finish handles a message that is from a customer, from a first reading of
// it.
func (d *Desk) finish(message Message, screen Screen, first reading) schemaflux.Step[Outcome] {
	return func(ctx context.Context) (Outcome, error) {
		// The first reading stands unless the text has to be translated or
		// redacted, in which case it was a reading of text the desk does not
		// work from, and the message is read again.
		reread := d.reread(message, screen)
		untranslated := func(ctx context.Context) (reading, error) {
			return schemaflux.Branch(ctx, screen.PersonalData, reread, keep(first))
		}
		read, err := schemaflux.Select(ctx, screen.Language, d.readings(untranslated, reread)...)
		if err != nil {
			return Outcome{}, err
		}

		playbook, err := schemaflux.Select(ctx, read.Triage.Team, d.playbooks()...)
		if err != nil {
			return Outcome{}, err
		}
		about := brief{
			Message: read.Text, Team: read.Triage.Team, Intent: read.Triage.Intent,
			Guidance: playbook.Guidance, Details: read.Details, Policies: d.Policies,
		}

		reply := func(ctx context.Context) (string, error) {
			return schemaflux.Branch(ctx, read.Triage.Answerable, d.answer(about), d.ask(about))
		}
		draft, handoff, err := schemaflux.Gather2(ctx, reply, d.handoff(read.Text))
		if err != nil {
			return Outcome{}, err
		}

		audit, err := d.audit(draft)(ctx)
		if err != nil {
			return Outcome{}, err
		}
		final, err := schemaflux.Branch(ctx, audit.Verdict == schemaflux.VerdictPass, keep(draft), d.redraft(draft, audit))
		if err != nil {
			return Outcome{}, err
		}
		sent, err := d.send(final, screen)(ctx)
		if err != nil {
			return Outcome{}, err
		}

		ticket, err := schemaflux.Branch(ctx, read.Matched.Found, d.attach(read.Matched), d.open(message))
		if err != nil {
			return Outcome{}, err
		}
		status, err := d.status(ctx, read.Triage)
		if err != nil {
			return Outcome{}, err
		}

		return Outcome{
			MessageID: message.ID, Status: status,
			Language: screen.Language, Team: read.Triage.Team, Intent: read.Triage.Intent, Urgency: read.Triage.Urgency,
			TicketID: ticket.ID, New: ticket.New,
			Details: read.Details, Reply: sent, Redrafted: audit.Verdict != schemaflux.VerdictPass,
			Handoff: handoff, NextAction: read.After.Next, WillWriteAgain: read.After.Again,
		}, nil
	}
}

// reread puts a message's text right, translating it and masking the personal
// data in it as the screen says it needs, and reads it again.
func (d *Desk) reread(message Message, screen Screen) schemaflux.Step[reading] {
	return func(ctx context.Context) (reading, error) {
		english, err := schemaflux.Select(ctx, screen.Language, d.toEnglish(message.Text())...)
		if err != nil {
			return reading{}, err
		}
		text, err := schemaflux.Branch(ctx, screen.PersonalData, d.redact(english), keep(english))
		if err != nil {
			return reading{}, err
		}
		second, details, matched, err := schemaflux.Gather3(ctx, d.second(text), d.details(text), d.match(message.From, text))
		if err != nil {
			return reading{}, err
		}
		return reading{Text: text, Triage: second.Triage, After: second.After, Details: details, Matched: matched}, nil
	}
}

// second asks the triage, and what follows the reply, of a message's text.
func (d *Desk) second(text string) schemaflux.Step[Second] {
	return func(ctx context.Context) (Second, error) {
		return schemaflux.Assess[Second](ctx, text, d.Options, d.Judgment)
	}
}
