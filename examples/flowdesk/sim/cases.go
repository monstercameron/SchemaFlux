package sim

import (
	"github.com/monstercameron/schemaflux/examples/flowdesk/desk"
)

// Sensitive is one piece of personal data in a message: the value as it is
// written there, and what kind of thing it is.
type Sensitive struct {
	Text     string
	Category string
}

// Case is one message together with the truth about it: the answer to every
// question the desk asks of it, and what each of the desk's writing steps
// produces for it. The simulator answers from these, and a test or a benchmark
// compares what the desk did with them.
//
// Every body carries a reference such as "[ref M-07]". It is how the simulator
// knows which case a call is about once the text has been translated and
// redacted, so the simulated translations and replies keep it.
type Case struct {
	// Kind says what the case is there to cover.
	Kind    string
	Message desk.Message

	// Screen and Triage hold the true answer to each question the desk's own
	// types of those names ask.
	Screen desk.Screen
	Triage desk.Triage

	// English is the text of the message in English, subject and body as
	// Message.Text puts them together, for a message written in another
	// language.
	English string
	// Personal is the personal data in the body, as it reads in English.
	Personal []Sensitive
	// Details is what the message says in so many words.
	Details desk.Details
	// FollowsUp is the ID of the open ticket the message is a follow-up to, or
	// empty when it is about something new.
	FollowsUp string

	// Draft is the reply the model writes first: an answer when the message
	// says enough to act on, and a question back when it does not.
	Draft string
	// Breaks is a phrase from the policy the first draft breaks, and Redraft
	// is the draft written again so that it does not. Both are empty when the
	// first draft is sound.
	Breaks  string
	Redraft string
	// Sent is the final reply in the customer's own language, for a message
	// written in another language.
	Sent string

	// Handoff is the note for whoever picks the ticket up.
	Handoff string
	// NextAction is what the desk does after replying.
	NextAction string
	// WritesAgain says whether the customer writes about this again.
	WritesAgain bool

	// Status is how the message ends.
	Status string
}

// Want is the outcome the labels call for.
func (c Case) Want() desk.Outcome {
	if c.Screen.Spam {
		return desk.Outcome{MessageID: c.Message.ID, Status: c.Status}
	}
	want := desk.Outcome{
		MessageID: c.Message.ID, Status: c.Status,
		Language: c.Screen.Language, Team: c.Triage.Team, Intent: c.Triage.Intent, Urgency: c.Triage.Urgency,
		TicketID: "T-" + c.Message.ID, New: true,
		Details: c.Details, Reply: c.Draft, Redrafted: c.Redraft != "",
		Handoff: c.Handoff, NextAction: c.NextAction, WillWriteAgain: c.WritesAgain,
	}
	if c.FollowsUp != "" {
		want.TicketID, want.New = c.FollowsUp, false
	}
	if c.Redraft != "" {
		want.Reply = c.Redraft
	}
	if c.Sent != "" {
		want.Reply = c.Sent
	}
	return want
}

// The two languages the desk treats apart from the rest: English, which is not
// translated, and the label for every language the desk has no name for, which
// is translated in and not back.
const (
	English         = "English"
	AnotherLanguage = "another language"
)

// TranslatedIn says that the desk translates the message into English to read
// it.
func (c Case) TranslatedIn() bool {
	return c.Screen.Language != English
}

// TranslatedBack says that the desk translates its reply into the language the
// message was written in. A language it cannot name gets the reply in English.
func (c Case) TranslatedBack() bool {
	return c.TranslatedIn() && c.Screen.Language != AnotherLanguage
}

// OpenTickets is the desk's open tickets in every case. The customers of the
// first and the third are the senders of M-05 and M-12, which follow them up;
// no other message is from the customer of an open ticket.
func OpenTickets() []desk.Ticket {
	return []desk.Ticket{
		{ID: "T-1001", Customer: "omar@example.com", Summary: "Charged twice for the March invoice on the Team plan"},
		{ID: "T-1002", Customer: "ravi@example.com", Summary: "Parcel with the standing desk frame has not arrived"},
		{ID: "T-1003", Customer: "claire@example.com", Summary: "Cannot sign in after changing the account email address"},
	}
}

// The answers a case can give for the team, the intent and the next action,
// spelled as the desk offers them.
const (
	billing   = "billing"
	technical = "technical"
	shipping  = "shipping"
	account   = "account"

	question  = "asking a question"
	complaint = "making a complaint"
	request   = "requesting an action"
	feedback  = "giving feedback"

	closeTicket = "close the ticket"
	waitFor     = "wait for the customer"
	followUp    = "follow up tomorrow"
	escalate    = "escalate to a manager"
)

// Cases returns the labelled messages, in the order they are run.
func Cases() []Case {
	return []Case{
		{
			Kind: "spam",
			Message: desk.Message{
				ID: "M-01", From: "offers@growth-rocket.example", Subject: "Triple your traffic in 7 days",
				Body: "Limited offer for site owners: our SEO package triples your traffic or your money back. Click to claim your discount today. [ref M-01]",
			},
			Screen: desk.Screen{Spam: true, Language: "English"},
			Status: desk.StatusDiscarded,
		},
		{
			Kind: "plain billing question",
			Message: desk.Message{
				ID: "M-02", From: "priya@example.com", Subject: "October invoice",
				Body: "Why is my October invoice for order A-2207 $49 and not $39? I am on the Team plan and nobody told me about a change. [ref M-02]",
			},
			Screen:     desk.Screen{Language: "English"},
			Triage:     desk.Triage{Team: billing, Intent: question, Urgency: 2, Answerable: true},
			Details:    desk.Details{OrderID: "A-2207", Product: "Team plan", Amount: "$49"},
			Draft:      "The October invoice for order A-2207 is $49 because the price of the Team plan changed at the start of the month. We will check whether the change was announced to you and write back with what we find. Your reference is [ref M-02].",
			Handoff:    "Customer asks why the October invoice for order A-2207 on the Team plan is $49 and not $39, and says the change was not announced.",
			NextAction: closeTicket, Status: desk.StatusAnswered,
		},
		{
			Kind: "Spanish, translated in and back",
			Message: desk.Message{
				ID: "M-03", From: "lucia@example.com", Subject: "Pedido B-5521",
				Body: "Hola, mi paquete del pedido B-5521 todavía no ha llegado y el seguimiento no cambia desde hace cinco días. ¿Dónde está? [ref M-03]",
			},
			Screen:     desk.Screen{Language: "Spanish"},
			English:    "Order B-5521\n\nHello, my parcel for order B-5521 still has not arrived and the tracking has not changed for five days. Where is it? [ref M-03]",
			Triage:     desk.Triage{Team: shipping, Intent: question, Urgency: 3, Answerable: true},
			Details:    desk.Details{OrderID: "B-5521"},
			Draft:      "The tracking for order B-5521 has not moved for five days, so we have asked the carrier to trace the parcel. We will write to you as soon as they answer. Your reference is [ref M-03].",
			Sent:       "El seguimiento del pedido B-5521 lleva cinco días sin cambios, así que hemos pedido al transportista que localice el paquete. Le escribiremos en cuanto responda. Su referencia es [ref M-03].",
			Handoff:    "Parcel for order B-5521 has not arrived and its tracking has not changed for five days. The carrier has been asked to trace it.",
			NextAction: followUp, WritesAgain: true, Status: desk.StatusAnswered,
		},
		{
			Kind: "personal data, redacted",
			Message: desk.Message{
				ID: "M-04", From: "dana@example.com", Subject: "Change my phone number",
				Body: "I cannot change the phone number on my account. The old one is 555-014-2298, and my card is 4111 1111 1111 1111 if you need it to find me. How do I change it? [ref M-04]",
			},
			Screen: desk.Screen{Language: "English", PersonalData: true},
			Personal: []Sensitive{
				{Text: "555-014-2298", Category: "phone"},
				{Text: "4111 1111 1111 1111", Category: "credit_card"},
			},
			Triage:     desk.Triage{Team: account, Intent: question, Urgency: 2, Answerable: true},
			Draft:      "You can change the phone number under Settings, then Profile, then Contact details. We have removed the card number from your message, and we never need one to find your account. Your reference is [ref M-04].",
			Handoff:    "Customer cannot find where to change the phone number on the account. The message held a phone number and a card number, both removed.",
			NextAction: closeTicket, Status: desk.StatusAnswered,
		},
		{
			Kind: "follow-up to an open ticket",
			Message: desk.Message{
				ID: "M-05", From: "omar@example.com", Subject: "Still charged twice",
				Body: "This is the second time I am writing about the double charge on my March invoice for the Team plan. Nobody has answered and I am close to cancelling. [ref M-05]",
			},
			Screen:     desk.Screen{Language: "English"},
			Triage:     desk.Triage{Team: billing, Intent: complaint, Urgency: 4, Answerable: true},
			Details:    desk.Details{Product: "Team plan"},
			FollowsUp:  "T-1001",
			Draft:      "We are sorry you had to write twice about the double charge on your March invoice. Your message has been added to the open ticket and a manager is now looking at it. Your reference is [ref M-05].",
			Handoff:    "Second message about the double charge on the March invoice for the Team plan. The customer has had no answer and is close to cancelling.",
			NextAction: escalate, WritesAgain: true, Status: desk.StatusAnswered,
		},
		{
			Kind: "does not say enough, asked a question back",
			Message: desk.Message{
				ID: "M-06", From: "sam@example.com", Subject: "Broken",
				Body: "It does not work. Please fix it. [ref M-06]",
			},
			Screen:     desk.Screen{Language: "English"},
			Triage:     desk.Triage{Team: technical, Intent: complaint, Urgency: 2},
			Draft:      "We are sorry it is not working. Could you tell us which product this is about, and what happens when you try to use it? Your reference is [ref M-06].",
			Handoff:    "Customer reports that something does not work, and does not say what or how.",
			NextAction: waitFor, WritesAgain: true, Status: desk.StatusAsked,
		},
		{
			Kind: "needs a human",
			Message: desk.Message{
				ID: "M-07", From: "helen@example.com", Subject: "Refund for cancelled plan",
				Body: "I was charged $240 for the annual plan I cancelled in May, order C-0912. I want the money back or I will dispute the charge with my bank. [ref M-07]",
			},
			Screen:     desk.Screen{Language: "English"},
			Triage:     desk.Triage{Team: billing, Intent: request, Urgency: 5, Answerable: true, NeedsHuman: true},
			Details:    desk.Details{OrderID: "C-0912", Product: "annual plan", Amount: "$240"},
			Draft:      "We are sorry about the $240 charge for the annual plan on order C-0912. A member of the billing team is reviewing the cancellation and the charge, and will write to you with the result. Your reference is [ref M-07].",
			Handoff:    "Customer was charged $240 for an annual plan cancelled in May, order C-0912, wants the money back and threatens a bank dispute.",
			NextAction: escalate, WritesAgain: true, Status: desk.StatusHeld,
		},
		{
			Kind: "first draft fails the audit, redrafted",
			Message: desk.Message{
				ID: "M-08", From: "kofi@example.com", Subject: "Late order",
				Body: "My order D-3310, the monitor arm, was due last Friday. When will it get here? [ref M-08]",
			},
			Screen:     desk.Screen{Language: "English"},
			Triage:     desk.Triage{Team: shipping, Intent: question, Urgency: 3, Answerable: true},
			Details:    desk.Details{OrderID: "D-3310", Product: "monitor arm"},
			Draft:      "We are sorry order D-3310 is late. It will be delivered by Thursday. Your reference is [ref M-08].",
			Breaks:     "a delivery date",
			Redraft:    "We are sorry order D-3310 is late. We have asked the carrier where the parcel is and will write to you with what they say. Your reference is [ref M-08].",
			Handoff:    "Order D-3310, a monitor arm, was due last Friday and has not arrived. The customer asks when it will.",
			NextAction: followUp, WritesAgain: true, Status: desk.StatusAnswered,
		},
		{
			Kind: "technical team",
			Message: desk.Message{
				ID: "M-09", From: "ines@example.com", Subject: "App crashes on reports",
				Body: "Since version 4.2 the mobile app crashes every time I open the reports tab. I am on Android 14. [ref M-09]",
			},
			Screen:     desk.Screen{Language: "English"},
			Triage:     desk.Triage{Team: technical, Intent: complaint, Urgency: 3, Answerable: true},
			Details:    desk.Details{Product: "mobile app"},
			Draft:      "Thank you for telling us about the crash on the reports tab. 1. Clear the app's cache in the Android settings. 2. Open the reports tab again. If it still crashes, reply to this message and we will pass it to the developers. Your reference is [ref M-09].",
			Handoff:    "Mobile app version 4.2 on Android 14 crashes whenever the reports tab is opened.",
			NextAction: waitFor, Status: desk.StatusAnswered,
		},
		{
			Kind: "account team",
			Message: desk.Message{
				ID: "M-10", From: "tom@example.com", Subject: "Second user",
				Body: "How do I add a second user to my account? [ref M-10]",
			},
			Screen:     desk.Screen{Language: "English"},
			Triage:     desk.Triage{Team: account, Intent: question, Urgency: 1, Answerable: true},
			Draft:      "You can add a second user under Settings, then Team, then Invite. They will get an email with a link to set their own password. Your reference is [ref M-10].",
			Handoff:    "Customer asks how to add a second user to the account.",
			NextAction: closeTicket, Status: desk.StatusAnswered,
		},
		{
			Kind: "feedback",
			Message: desk.Message{
				ID: "M-11", From: "wei@example.com", Subject: "New invoices",
				Body: "The new invoice layout is much easier to read. Thank you for changing it. [ref M-11]",
			},
			Screen:     desk.Screen{Language: "English"},
			Triage:     desk.Triage{Team: billing, Intent: feedback, Urgency: 1, Answerable: true},
			Details:    desk.Details{Product: "invoice layout"},
			Draft:      "Thank you for telling us the new invoice layout is easier to read. We have passed your message to the people who made it. Your reference is [ref M-11].",
			Handoff:    "Customer likes the new invoice layout. Nothing to do.",
			NextAction: closeTicket, Status: desk.StatusAnswered,
		},
		{
			Kind: "French follow-up to an open ticket",
			Message: desk.Message{
				ID: "M-12", From: "claire@example.com", Subject: "Connexion impossible",
				Body: "Bonjour, je ne peux toujours pas me connecter depuis que j'ai changé l'adresse e-mail de mon compte. Pouvez-vous m'aider ? [ref M-12]",
			},
			Screen:     desk.Screen{Language: "French"},
			English:    "Cannot sign in\n\nHello, I still cannot sign in since I changed the email address on my account. Can you help me? [ref M-12]",
			Triage:     desk.Triage{Team: account, Intent: request, Urgency: 4, Answerable: true},
			FollowsUp:  "T-1003",
			Draft:      "We are sorry you still cannot sign in. Open the sign-in page, choose Forgot password, and enter the new email address: the link we send there lets you back in. Your reference is [ref M-12].",
			Sent:       "Nous sommes désolés que vous ne puissiez toujours pas vous connecter. Ouvrez la page de connexion, choisissez Mot de passe oublié et saisissez la nouvelle adresse e-mail : le lien que nous y envoyons vous redonne accès. Votre référence est [ref M-12].",
			Handoff:    "Customer still cannot sign in after changing the account email address. Told to reset the password through the new address.",
			NextAction: waitFor, WritesAgain: true, Status: desk.StatusAnswered,
		},
		{
			Kind: "Spanish, personal data, redrafted and held",
			Message: desk.Message{
				ID: "M-13", From: "mateo@example.com", Subject: "Cobro doble",
				Body: "Me cobraron dos veces 89 euros por el pedido E-7745. Mi teléfono es 612 555 019. Quiero que me devuelvan el dinero. [ref M-13]",
			},
			Screen:     desk.Screen{Language: "Spanish", PersonalData: true},
			English:    "Double charge\n\nI was charged 89 euros twice for order E-7745. My phone number is 612 555 019. I want my money back. [ref M-13]",
			Personal:   []Sensitive{{Text: "612 555 019", Category: "phone"}},
			Triage:     desk.Triage{Team: billing, Intent: request, Urgency: 4, Answerable: true, NeedsHuman: true},
			Details:    desk.Details{OrderID: "E-7745", Amount: "89 euros"},
			Draft:      "We are sorry about the double charge on order E-7745. We will refund the 89 euros within three days. Your reference is [ref M-13].",
			Breaks:     "promise a refund",
			Redraft:    "We are sorry about the double charge on order E-7745. The billing team is reviewing both charges and will write to you with the result. Your reference is [ref M-13].",
			Sent:       "Lamentamos el cobro doble del pedido E-7745. El equipo de facturación está revisando los dos cargos y le escribirá con el resultado. Su referencia es [ref M-13].",
			Handoff:    "Customer was charged 89 euros twice for order E-7745 and wants one charge returned. A phone number in the message was removed.",
			NextAction: escalate, WritesAgain: true, Status: desk.StatusHeld,
		},
		{
			Kind: "automated notification in German, discarded before translation",
			Message: desk.Message{
				ID: "M-14", From: "no-reply@newsletter.example", Subject: "Ihr Wochenbericht",
				Body: "Ihr Wochenbericht ist fertig. Dies ist eine automatisch erzeugte Nachricht, bitte antworten Sie nicht darauf. [ref M-14]",
			},
			Screen: desk.Screen{Spam: true, Language: "German"},
			Status: desk.StatusDiscarded,
		},
		{
			Kind: "German, translated in and back",
			Message: desk.Message{
				ID: "M-15", From: "jonas@example.com", Subject: "Rechnung doppelt",
				Body: "Guten Tag, meine Rechnung R-4410 für den Team-Tarif wurde diesen Monat zweimal abgebucht. Bitte prüfen Sie das. [ref M-15]",
			},
			Screen:     desk.Screen{Language: "German"},
			English:    "Invoice twice\n\nHello, my invoice R-4410 for the Team plan was charged twice this month. Please check it. [ref M-15]",
			Triage:     desk.Triage{Team: billing, Intent: complaint, Urgency: 3, Answerable: true},
			Details:    desk.Details{OrderID: "R-4410", Product: "Team plan"},
			Draft:      "Invoice R-4410 for the Team plan shows two charges this month. We have asked the billing team to look at both and will write to you with what they find. Your reference is [ref M-15].",
			Sent:       "Auf der Rechnung R-4410 für den Team-Tarif stehen diesen Monat zwei Abbuchungen. Wir haben das Abrechnungsteam gebeten, beide zu prüfen, und schreiben Ihnen, sobald es etwas gefunden hat. Ihre Referenz ist [ref M-15].",
			Handoff:    "Invoice R-4410 for the Team plan was charged twice this month, and the customer asks the desk to check it.",
			NextAction: followUp, Status: desk.StatusAnswered,
		},
		{
			Kind: "Portuguese, translated in and back",
			Message: desk.Message{
				ID: "M-16", From: "beatriz@example.com", Subject: "Aplicativo fecha sozinho",
				Body: "Olá, o aplicativo móvel fecha sozinho assim que eu abro a tela de relatórios. Uso a versão 4.2 em um iPhone. [ref M-16]",
			},
			Screen:     desk.Screen{Language: "Portuguese"},
			English:    "App closes by itself\n\nHello, the mobile app closes by itself as soon as I open the reports screen. I use version 4.2 on an iPhone. [ref M-16]",
			Triage:     desk.Triage{Team: technical, Intent: complaint, Urgency: 3, Answerable: true},
			Details:    desk.Details{Product: "mobile app"},
			Draft:      "Thank you for the details about the crash on the reports screen. 1. Update the app to the latest version. 2. Open the reports screen again. If it still closes, reply to this message and we will pass it to the developers. Your reference is [ref M-16].",
			Sent:       "Obrigado pelos detalhes sobre a falha na tela de relatórios. 1. Atualize o aplicativo para a versão mais recente. 2. Abra a tela de relatórios novamente. Se ele ainda fechar, responda a esta mensagem e passaremos o caso aos desenvolvedores. Sua referência é [ref M-16].",
			Handoff:    "Mobile app version 4.2 on an iPhone closes by itself whenever the reports screen is opened.",
			NextAction: waitFor, WritesAgain: true, Status: desk.StatusAnswered,
		},
		{
			Kind: "a language the desk cannot name, translated in and answered in English",
			Message: desk.Message{
				ID: "M-17", From: "daan@example.com", Subject: "Pakket niet aangekomen",
				Body: "Goedendag, mijn pakket met bestelnummer F-8830 is nog steeds niet bezorgd. Kunt u mij zeggen waar het is? [ref M-17]",
			},
			Screen:     desk.Screen{Language: AnotherLanguage},
			English:    "Parcel has not arrived\n\nGood day, my parcel with order number F-8830 has still not been delivered. Can you tell me where it is? [ref M-17]",
			Triage:     desk.Triage{Team: shipping, Intent: question, Urgency: 3, Answerable: true},
			Details:    desk.Details{OrderID: "F-8830"},
			Draft:      "Order F-8830 has not been delivered yet, so we have asked the carrier to trace the parcel. We will write to you in English as soon as they answer. Your reference is [ref M-17].",
			Handoff:    "Parcel for order F-8830 has still not been delivered, and the customer asks where it is. The message was in a language the desk cannot name.",
			NextAction: followUp, WritesAgain: true, Status: desk.StatusAnswered,
		},
	}
}
