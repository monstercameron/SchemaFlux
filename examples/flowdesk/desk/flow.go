package desk

import (
	"context"

	schemaflux "github.com/monstercameron/schemaflux"
)

// Handle takes a message from arrival to outcome.
//
// It makes nine calls and waits for the model four times for an English
// message with no personal data in it whose draft passes the desk's policies:
// the screen; then the triage, the details and the ticket match, at once; then
// the draft, the hand-off note, the next action and the repeat-contact
// prediction, at once; then the audit of the draft. With no open ticket there
// is nothing to match against and the match is not asked: eight calls, the same
// four waits. A translation into English, a redaction, a redraft and a
// translation of the reply back each add a call and a wait; a message in a
// language the desk cannot name is not translated back. Spam ends at the
// screen, one call.
//
// Every question is asked of the same text, Message.Text, as it is after
// translation and redaction: the screen is asked of it as it arrived. The
// other two flows ask the same questions of the same text. HandleOneAtATime
// makes the same decisions one call at a time, and is what this is measured
// against.
func (d *Desk) Handle(ctx context.Context, message Message) (Outcome, error) {
	screen, err := schemaflux.Assess[Screen](ctx, message.Text(), d.Options)
	if err != nil {
		return Outcome{}, err
	}
	return schemaflux.Branch(ctx, screen.Spam, d.discard(message), d.work(message, screen))
}

// discard is where a message that is not from a customer ends.
func (d *Desk) discard(message Message) schemaflux.Step[Outcome] {
	return keep(Outcome{MessageID: message.ID, Status: StatusDiscarded})
}

// work handles a message that is from a customer.
func (d *Desk) work(message Message, screen Screen) schemaflux.Step[Outcome] {
	return func(ctx context.Context) (Outcome, error) {
		// Nothing after these two steps reads the text as it arrived.
		english, err := schemaflux.Select(ctx, screen.Language, d.toEnglish(message.Text())...)
		if err != nil {
			return Outcome{}, err
		}
		text, err := schemaflux.Branch(ctx, screen.PersonalData, d.redact(english), keep(english))
		if err != nil {
			return Outcome{}, err
		}

		triage, details, matched, err := schemaflux.Gather3(ctx, d.triage(text), d.details(text), d.match(message.From, text))
		if err != nil {
			return Outcome{}, err
		}
		playbook, err := schemaflux.Select(ctx, triage.Team, d.playbooks()...)
		if err != nil {
			return Outcome{}, err
		}
		about := brief{
			Message: text, Team: triage.Team, Intent: triage.Intent,
			Guidance: playbook.Guidance, Details: details, Policies: d.Policies,
		}

		reply := func(ctx context.Context) (string, error) {
			return schemaflux.Branch(ctx, triage.Answerable, d.answer(about), d.ask(about))
		}
		draft, handoff, next, again, err := schemaflux.Gather4(ctx, reply, d.handoff(text), d.next(text), d.again(text))
		if err != nil {
			return Outcome{}, err
		}

		audit, err := d.audit(draft)(ctx)
		if err != nil {
			return Outcome{}, err
		}
		final, err := schemaflux.Branch(ctx, audit.Summary.PassesAudit, keep(draft), d.redraft(draft, audit))
		if err != nil {
			return Outcome{}, err
		}
		// The customer is answered in the language they wrote in.
		sent, err := d.send(final, screen)(ctx)
		if err != nil {
			return Outcome{}, err
		}

		ticket, err := schemaflux.Branch(ctx, matched.Found, d.attach(matched), d.open(message))
		if err != nil {
			return Outcome{}, err
		}
		status, err := d.status(ctx, triage)
		if err != nil {
			return Outcome{}, err
		}

		return Outcome{
			MessageID: message.ID, Status: status,
			Language: screen.Language, Team: triage.Team, Intent: triage.Intent, Urgency: triage.Urgency,
			TicketID: ticket.ID, New: ticket.New,
			Details: details, Reply: sent, Redrafted: !audit.Summary.PassesAudit,
			Handoff: handoff, NextAction: next, WillWriteAgain: again,
		}, nil
	}
}

// status is where a message that was worked ends: held for a person, or
// answered, or with a question back to the customer.
func (d *Desk) status(ctx context.Context, triage Triage) (string, error) {
	released := func(ctx context.Context) (string, error) {
		return schemaflux.Branch(ctx, triage.Answerable, keep(StatusAnswered), keep(StatusAsked))
	}
	return schemaflux.Branch(ctx, triage.NeedsHuman, keep(StatusHeld), released)
}

// filed is the ticket a message went to.
type filed struct {
	ID  string
	New bool
}

// attach files a message under the open ticket it was matched to. The routes
// are the open tickets, by ID.
func (d *Desk) attach(matched Matched) schemaflux.Step[filed] {
	return func(ctx context.Context) (filed, error) {
		routes := make([]schemaflux.Route[filed], len(d.Open))
		for i, ticket := range d.Open {
			routes[i] = schemaflux.To(ticket.ID, keep(filed{ID: ticket.ID}))
		}
		return schemaflux.Select(ctx, matched.ID, routes...)
	}
}

// open files a message under a new ticket.
func (d *Desk) open(message Message) schemaflux.Step[filed] {
	return keep(filed{ID: "T-" + message.ID, New: true})
}
