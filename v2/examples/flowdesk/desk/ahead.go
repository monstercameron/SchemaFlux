package desk

import (
	"context"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	flow "github.com/monstercameron/schemaflux/v2/flow"
)

// HandleAhead is HandleFast with the two waits taken out of it that the
// measurements said were not buying anything.
//
// It asks its questions of a message once. HandleFast puts the text of a
// message right, by translating it and masking the personal data in it, and
// then asks its questions a second time of the text as it then is. But a model
// that can translate a message can say which team it is for in the language it
// came in, and masking a phone number changes nothing about how urgent the
// message is. So what the first look decided stands, and the ticket match made
// beside it stands, and the text is put right only for the steps that write
// from it: the draft and the hand-off note. A message that is translated costs
// HandleAhead that one step, where it cost HandleFast the step and a second
// look of three calls.
//
// The details are the exception, for a message that holds personal data. They
// are read out of the text, so what was read before the redaction can hold
// what the redaction masks: a customer who gives a phone number where an order
// number would go has it read as the order number. The reply is written from
// the details, and they are reported in the outcome. So a message that is
// redacted has its details read again, from the redacted text, and the first
// reading is dropped: two calls, where HandleFast makes four.
//
// And it does not wait for the audit before it starts on what follows a draft
// that passes. The reply to a customer who wrote in another language is
// translated back, and most drafts pass their audit, so the translation is
// started beside the audit (schemaflux.BranchAhead) and kept when the audit
// passes. When it fails, the translation is cancelled, the draft is written
// again and that is translated: a translation the provider may already have
// been sent, and bills for, is the price, paid only for the drafts that fail.
// For a reply that goes in English there is nothing to start ahead and nothing
// is paid.
//
// The waits for a message in Spanish with no personal data in it whose draft
// passes: the look, the details and the ticket match, at once; the translation
// into English; the draft and the hand-off note, at once; the audit and the
// translation of the reply, at once. Four, where HandleFast takes six.
//
// What it gives up, beside what HandleFast already does: the questions are
// answered from the text as it arrived, so a triage that would have gone
// differently on the translation goes the way it went on the original, and the
// details of a message with no personal data in it are read from the text
// before it is translated. Nothing after the redaction reads the personal data,
// or anything that was read out of the text before it; the look, the first
// details and the match, which come before it, do read the text as it arrived.
func (d *Desk) HandleAhead(ctx context.Context, message Message) (Outcome, error) {
	arrived := message.Text()
	look, details, matched, err := schemaflux.Gather3(ctx, d.look(arrived), d.details(arrived), d.match(message.From, arrived))
	if err != nil {
		return Outcome{}, err
	}
	return schemaflux.Branch(ctx, look.Spam, d.discard(message), d.finishAhead(message, look, details, matched))
}

// finishAhead handles a message that is from a customer, from the one reading
// of it.
func (d *Desk) finishAhead(message Message, look Look, details Details, matched Matched) schemaflux.Step[Outcome] {
	return func(ctx context.Context) (Outcome, error) {
		// The text is put right for the steps that write from it. Nothing is
		// asked of it again.
		english, err := schemaflux.Select(ctx, look.Language, d.toEnglish(message.Text())...)
		if err != nil {
			return Outcome{}, err
		}
		text, err := schemaflux.Branch(ctx, look.PersonalData, d.redact(english), keep(english))
		if err != nil {
			return Outcome{}, err
		}

		// What was read out of the text before it was redacted is not written
		// from: the details of a message that held personal data are read
		// again, from what the redaction left.
		details, err := schemaflux.Branch(ctx, look.PersonalData, d.details(text), keep(details))
		if err != nil {
			return Outcome{}, err
		}

		playbook, err := schemaflux.Select(ctx, look.Team, d.playbooks()...)
		if err != nil {
			return Outcome{}, err
		}
		about := brief{
			Message: text, Team: look.Team, Intent: look.Intent,
			Guidance: playbook.Guidance, Details: details, Policies: d.Policies,
		}

		reply := func(ctx context.Context) (string, error) {
			return schemaflux.Branch(ctx, look.Answerable, d.answer(about), d.ask(about))
		}
		draft, handoff, err := schemaflux.Gather2(ctx, reply, d.handoff(text))
		if err != nil {
			return Outcome{}, err
		}

		// The audit is the decision, and what it found is kept for the branch
		// that needs it: the redraft runs only once the audit has answered, so
		// it reads what the audit wrote and never beside it.
		var found schemaflux.AuditResult[Reply]
		passes := func(ctx context.Context) (bool, error) {
			audit, err := d.audit(draft)(ctx)
			found = audit
			return audit.Verdict == schemaflux.VerdictPass, err
		}
		again := func(ctx context.Context) (string, error) {
			final, err := d.redraft(draft, found)(ctx)
			if err != nil {
				return "", err
			}
			return d.send(final, look.Screen)(ctx)
		}
		sent, err := flow.BranchAhead(ctx, passes, d.send(draft, look.Screen), again)
		if err != nil {
			return Outcome{}, err
		}

		ticket, err := schemaflux.Branch(ctx, matched.Found, d.attach(matched), d.open(message))
		if err != nil {
			return Outcome{}, err
		}
		status, err := d.status(ctx, look.Triage)
		if err != nil {
			return Outcome{}, err
		}

		return Outcome{
			MessageID: message.ID, Status: status,
			Language: look.Language, Team: look.Team, Intent: look.Intent, Urgency: look.Urgency,
			TicketID: ticket.ID, New: ticket.New,
			Details: details, Reply: sent, Redrafted: found.Verdict != schemaflux.VerdictPass,
			Handoff: handoff, NextAction: look.Next, WillWriteAgain: look.Again,
		}, nil
	}
}
