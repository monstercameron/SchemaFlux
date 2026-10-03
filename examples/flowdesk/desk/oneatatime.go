package desk

import (
	"context"

	schemaflux "github.com/monstercameron/schemaflux"
)

// HandleOneAtATime makes the decisions Handle makes, each as a call of its own
// and each after the one before. It is how a program is written when every
// judgment is a question put to the model as it comes up, and it is the
// baseline Handle is measured against: the same questions, the same answers,
// fifteen waits where Handle has four. (Fourteen for a desk with no open
// ticket, which has nothing to match a message against and does not ask.)
//
// The questions are read from the same struct tags Handle's are, so the two
// cannot drift apart, and they are asked of the same text: the screen's of the
// message as it arrived, the rest of it after translation and redaction. The
// triage questions take Desk.Judgment after Desk.Options, each in its own
// call.
func (d *Desk) HandleOneAtATime(ctx context.Context, message Message) (Outcome, error) {
	spam, err := schemaflux.Is(ctx, message.Text(), question[Screen]("Spam"), d.Options)
	if err != nil {
		return Outcome{}, err
	}
	return schemaflux.Branch(ctx, spam, d.discard(message), d.workOneAtATime(message))
}

func (d *Desk) workOneAtATime(message Message) schemaflux.Step[Outcome] {
	return func(ctx context.Context) (Outcome, error) {
		var screen Screen
		var triage Triage
		var err error
		arrived := message.Text()

		screen.Language, err = schemaflux.Which(ctx, arrived, question[Screen]("Language"), options[Screen]("Language"), d.Options)
		if err != nil {
			return Outcome{}, err
		}
		screen.PersonalData, err = schemaflux.Is(ctx, arrived, question[Screen]("PersonalData"), d.Options)
		if err != nil {
			return Outcome{}, err
		}
		english, err := schemaflux.Select(ctx, screen.Language, d.toEnglish(arrived)...)
		if err != nil {
			return Outcome{}, err
		}
		text, err := schemaflux.Branch(ctx, screen.PersonalData, d.redact(english), keep(english))
		if err != nil {
			return Outcome{}, err
		}

		low, high, err := scale[Triage]("Urgency")
		if err != nil {
			return Outcome{}, err
		}
		triage.Team, err = schemaflux.Which(ctx, text, question[Triage]("Team"), options[Triage]("Team"), d.Options, d.Judgment)
		if err != nil {
			return Outcome{}, err
		}
		triage.Intent, err = schemaflux.Which(ctx, text, question[Triage]("Intent"), options[Triage]("Intent"), d.Options, d.Judgment)
		if err != nil {
			return Outcome{}, err
		}
		triage.Urgency, err = schemaflux.Rate(ctx, text, question[Triage]("Urgency"), low, high, d.Options, d.Judgment)
		if err != nil {
			return Outcome{}, err
		}
		triage.Answerable, err = schemaflux.Is(ctx, text, question[Triage]("Answerable"), d.Options, d.Judgment)
		if err != nil {
			return Outcome{}, err
		}
		triage.NeedsHuman, err = schemaflux.Is(ctx, text, question[Triage]("NeedsHuman"), d.Options, d.Judgment)
		if err != nil {
			return Outcome{}, err
		}

		details, err := d.details(text)(ctx)
		if err != nil {
			return Outcome{}, err
		}
		matched, err := d.match(message.From, text)(ctx)
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

		draft, err := schemaflux.Branch(ctx, triage.Answerable, d.answer(about), d.ask(about))
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
		sent, err := d.send(final, screen)(ctx)
		if err != nil {
			return Outcome{}, err
		}

		handoff, err := d.handoff(text)(ctx)
		if err != nil {
			return Outcome{}, err
		}
		next, err := d.next(text)(ctx)
		if err != nil {
			return Outcome{}, err
		}
		again, err := d.again(text)(ctx)
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
