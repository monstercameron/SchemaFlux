package desk_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/examples/v2/flowdesk/desk"
	"github.com/monstercameron/schemaflux/examples/v2/flowdesk/sim"
)

// request is one request a recorder saw: what it was, what it carried, the
// model it asked for, and when it started and ended counted on a clock that
// ticks once at every start and every end, so that "after" means after the
// other request had ended and does not depend on the time of day.
type request struct {
	operation string
	model     string
	prompt    string
	started   int
	ended     int
}

// recorder is a provider that keeps every request's user prompt, in order.
type recorder struct {
	schemaflux.Provider

	mu       sync.Mutex
	clock    int
	requests []*request
}

func (r *recorder) Complete(ctx context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	r.mu.Lock()
	r.clock++
	seen := &request{operation: sim.Operation(req), model: req.Model, prompt: req.UserPrompt, started: r.clock}
	r.requests = append(r.requests, seen)
	r.mu.Unlock()

	resp, err := r.Provider.Complete(ctx, req)

	r.mu.Lock()
	r.clock++
	seen.ended = r.clock
	r.mu.Unlock()
	return resp, err
}

// seen is the requests made so far.
func (r *recorder) seen() []request {
	r.mu.Lock()
	defer r.mu.Unlock()
	all := make([]request, len(r.requests))
	for i, one := range r.requests {
		all[i] = *one
	}
	return all
}

// runRecorded handles a message with a flow through a recorder.
func runRecorded(t *testing.T, with flow, labelled sim.Case) []request {
	t.Helper()
	recording := &recorder{Provider: sim.New(time.Millisecond)}
	ctx := schemaflux.NewClient("").WithProviderInstance(recording).Context(context.Background())
	if _, err := with.handle(desk.New(sim.OpenTickets()), ctx, labelled.Message); err != nil {
		t.Fatalf("%s, %s flow: %v", labelled.Message.ID, with.name, err)
	}
	return recording.seen()
}

// after is the requests that started once the request that is the step had
// ended.
func after(requests []request, step request) []request {
	var later []request
	for _, one := range requests {
		if one.started > step.ended {
			later = append(later, one)
		}
	}
	return later
}

// leaks names every request that carries one of the strings.
func leaks(requests []request, needles []string) []string {
	var found []string
	for _, one := range requests {
		for _, needle := range needles {
			if strings.Contains(one.prompt, needle) {
				found = append(found, one.operation+" carries "+needle)
			}
		}
	}
	return found
}

// lacks names every request of an operation that is one of operations and does
// not carry the string.
func lacks(requests []request, operations []string, needle string) []string {
	var found []string
	for _, one := range requests {
		for _, operation := range operations {
			if one.operation == operation && !strings.Contains(one.prompt, needle) {
				found = append(found, one.operation+" does not carry "+needle)
			}
		}
	}
	return found
}

// stepOf finds the one request of an operation whose prompt carries a string.
func stepOf(t *testing.T, requests []request, operation, carrying string) request {
	t.Helper()
	var found []request
	for _, one := range requests {
		if one.operation == operation && strings.Contains(one.prompt, carrying) {
			found = append(found, one)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d %s requests carry %q, want one", len(found), operation, carrying)
	}
	return found[0]
}

// The operations that are asked something of the text, as opposed to a reply.
var asksOfTheText = []string{sim.OpAssess, sim.OpExtract, sim.OpSummarize, sim.OpGenerate, sim.OpRedact}

// What translation and redaction are for: nothing the desk asks after the
// redaction reads the personal data, and nothing it asks after the translation
// reads the original. A step whose result was thrown away, with the text it
// was given used afterwards, still makes its call, and the simulator answers
// the same whatever it is sent, so the outcome is the same and no other test
// can see it. This reads the requests.
//
// The two messages: M-04 has a phone number and a card number in English, and
// M-13 is in Spanish with a phone number in it, so that a redaction of the
// Spanish text and a redaction of the English one are different calls.
//
// Each run is in a synctest bubble, so the translation that the flow that works
// ahead starts beside the audit is made on every run and the counts of requests
// that follow do not depend on how busy the machine is.
func TestNothingIsReadAfterTheRedactionAndTheTranslationThatTheyWereForSomeoneElse(t *testing.T) {
	byID := map[string]sim.Case{}
	for _, labelled := range sim.Cases() {
		byID[labelled.Message.ID] = labelled
	}
	for _, with := range allFlows {
		t.Run(with.name+", redacted", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				labelled := byID["M-04"]
				requests := runRecorded(t, with, labelled)
				redaction := stepOf(t, requests, sim.OpRedact, "Find all sensitive data")

				var personal []string
				for _, sensitive := range labelled.Personal {
					personal = append(personal, sensitive.Text)
				}
				if len(personal) != 2 {
					t.Fatalf("M-04 has %d personal strings, want a phone number and a card number", len(personal))
				}
				// The strings are there to be found: the redaction was sent them.
				if found := leaks([]request{redaction}, personal); len(found) != len(personal) {
					t.Fatalf("the redaction request does not carry the personal data it is to mask: %v", found)
				}
				// At least the details, the draft, the hand-off note and the audit,
				// which is all the flow that works ahead makes of this message once
				// it is redacted; the others ask their questions as well.
				later := after(requests, redaction)
				if len(later) < 4 {
					t.Fatalf("only %d requests were made after the redaction", len(later))
				}
				if found := leaks(later, personal); len(found) != 0 {
					t.Errorf("personal data was sent after it was redacted: %v", found)
				}
				// What they were asked of is the text, subject and body, redacted.
				if found := lacks(later, asksOfTheText, labelled.Message.Subject); len(found) != 0 {
					t.Errorf("a question after the redaction was not asked of the subject as well as the body: %v", found)
				}
				if found := lacks(later, asksOfTheText, "I cannot change the phone number on my account"); len(found) != 0 {
					t.Errorf("a question after the redaction was not asked of the body: %v", found)
				}
			})
		})

		t.Run(with.name+", translated and redacted", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				labelled := byID["M-13"]
				requests := runRecorded(t, with, labelled)
				translation := stepOf(t, requests, sim.OpTranslate, "Translate to English")
				redaction := stepOf(t, requests, sim.OpRedact, "Find all sensitive data")
				if redaction.started < translation.ended {
					t.Fatalf("the text was redacted before it was translated, so what was redacted is the original")
				}

				personal := []string{labelled.Personal[0].Text}
				if found := leaks([]request{redaction}, personal); len(found) != 1 {
					t.Fatalf("the redaction request does not carry the personal data it is to mask: %v", found)
				}
				if found := leaks([]request{redaction}, []string{"I was charged 89 euros twice for order E-7745."}); len(found) != 1 {
					t.Errorf("the redaction was not of the English text: %v", redaction.prompt)
				}
				later := after(requests, redaction)
				if len(later) < 5 {
					t.Fatalf("only %d requests were made after the redaction", len(later))
				}
				if found := leaks(later, personal); len(found) != 0 {
					t.Errorf("personal data was sent after it was redacted: %v", found)
				}

				// After the translation, nothing carries the original, and every
				// question of the text carries the English, subject and body.
				original := []string{"Cobro doble", "Me cobraron dos veces", "Mi teléfono"}
				afterTranslation := after(requests, translation)
				if len(afterTranslation) < 7 {
					t.Fatalf("only %d requests were made after the translation", len(afterTranslation))
				}
				if found := leaks(afterTranslation, original); len(found) != 0 {
					t.Errorf("the original was sent after it was translated: %v", found)
				}
				for _, english := range []string{"Double charge", "I was charged 89 euros twice for order E-7745."} {
					if found := lacks(afterTranslation, asksOfTheText, english); len(found) != 0 {
						t.Errorf("a question after the translation was not asked of the English text: %v", found)
					}
				}
			})
		})
	}
}

// The checks above are only worth what they can find. Each is run on requests
// made up to leak, and has to find the leak: a flow that masks the text and
// then goes on with the text it had before has requests like these.
func TestTheCheckForLeaksFindsAThingThatLeaks(t *testing.T) {
	ordered := []request{
		{operation: sim.OpAssess, prompt: "the screen of 612 555 019", started: 1, ended: 2},
		{operation: sim.OpRedact, prompt: "Find all sensitive data: 612 555 019", started: 3, ended: 4},
		{operation: sim.OpAssess, prompt: "the triage of 612 555 019", started: 5, ended: 8},
		{operation: sim.OpExtract, prompt: "the details of [masked]", started: 6, ended: 7},
		{operation: sim.OpAudit, prompt: "the reply", started: 9, ended: 10},
	}
	redaction := ordered[1]

	if got := after(ordered, redaction); len(got) != 3 || got[0].started != 5 {
		t.Errorf("after the redaction: %+v", got)
	}
	if got := after(ordered, ordered[4]); len(got) != 0 {
		t.Errorf("after the last request: %+v", got)
	}
	if got := after(ordered, request{ended: 5}); len(got) != 2 || got[0].started != 6 {
		t.Errorf("a request that started the moment another ended is not after it: %+v", got)
	}

	found := leaks(after(ordered, redaction), []string{"612 555 019"})
	if len(found) != 1 || !strings.Contains(found[0], sim.OpAssess) {
		t.Errorf("the leak into the triage was not found: %v", found)
	}
	if found := leaks(ordered[:2], []string{"612 555 019", "4111"}); len(found) != 2 {
		t.Errorf("a leak before the redaction is not one of the check's, and both requests carry the number: %v", found)
	}
	if found := leaks(nil, []string{"x"}); len(found) != 0 {
		t.Errorf("a leak in no requests: %v", found)
	}
	if found := leaks(ordered, nil); len(found) != 0 {
		t.Errorf("a leak of nothing: %v", found)
	}
	if found := leaks(ordered, []string{"the reply", "the details"}); len(found) != 2 {
		t.Errorf("two needles in two requests: %v", found)
	}

	if found := lacks(ordered, []string{sim.OpAssess}, "of 612 555 019"); len(found) != 0 {
		t.Errorf("both assessments carry it: %v", found)
	}
	if found := lacks(ordered, []string{sim.OpExtract}, "of 612 555 019"); len(found) != 1 {
		t.Errorf("the extraction does not carry it: %v", found)
	}
	if found := lacks(ordered, []string{sim.OpAssess, sim.OpExtract, sim.OpAudit}, "of "); len(found) != 1 {
		t.Errorf("only the audit lacks it: %v", found)
	}
	if found := lacks(ordered, nil, "x"); len(found) != 0 {
		t.Errorf("no operation is asked of: %v", found)
	}
}

// Desk.Judgment is laid over Desk.Options for the triage questions and for no
// others, in every flow: in the one-at-a-time flow each triage question's
// call, in the fused flow the triage's, and in the fast flow and the one that
// works ahead the look, which carries the screen and what follows the reply
// with the triage and so takes it whole. The two are told apart by the model each names; the steps that are
// not decisions take neither.
func TestJudgmentIsLaidOverTheTriageQuestionsInEveryFlow(t *testing.T) {
	var triage []string
	for i := range reflect.TypeFor[desk.Triage]().NumField() {
		triage = append(triage, reflect.TypeFor[desk.Triage]().Field(i).Tag.Get("ask"))
	}
	if len(triage) != 5 {
		t.Fatalf("the triage asks %d questions, want 5", len(triage))
	}
	labelled := sim.Cases()[1]

	for _, with := range allFlows {
		t.Run(with.name, func(t *testing.T) {
			recording := &recorder{Provider: sim.New(time.Millisecond)}
			ctx := schemaflux.NewClient("").WithProviderInstance(recording).Context(context.Background())
			support := desk.New(sim.OpenTickets())
			support.Options.Model = "options-model"
			support.Judgment.Model = "judgment-model"
			if _, err := with.handle(support, ctx, labelled.Message); err != nil {
				t.Fatal(err)
			}

			judged := map[string]bool{}
			for _, one := range recording.seen() {
				if one.operation != sim.OpAssess {
					if one.model == "options-model" || one.model == "judgment-model" {
						t.Errorf("a %s call, which is not a decision, took the desk's options: %s", one.operation, one.model)
					}
					continue
				}
				answers, err := sim.Answers(schemaflux.CompletionRequest{UserPrompt: one.prompt}, "{}")
				if err != nil {
					t.Fatal(err)
				}
				asksTriage := false
				for _, answered := range answers {
					for _, question := range triage {
						if answered.Question == question {
							asksTriage = true
							judged[question] = one.model == "judgment-model"
						}
					}
				}
				want := "options-model"
				if asksTriage {
					want = "judgment-model"
				}
				if one.model != want {
					t.Errorf("an assessment of %d questions ran on %q, want %q", len(answers), one.model, want)
				}
				if asksTriage && !looksAtOnce(with) && len(answers) != len(triage) && len(answers) != 1 {
					t.Errorf("a call that took the judgment options asked %d questions, which is not the triage's", len(answers))
				}
			}
			for _, question := range triage {
				if !judged[question] {
					t.Errorf("the triage question %q was not asked under the judgment options", question)
				}
			}
		})
	}
}

// substituting is a provider that answers one operation with what a function
// makes of the request, and every other as the simulator does.
type substituting struct {
	schemaflux.Provider
	operation string
	reply     func(schemaflux.CompletionRequest) string
}

func (s substituting) Complete(ctx context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	if sim.Operation(req) != s.operation {
		return s.Provider.Complete(ctx, req)
	}
	return schemaflux.CompletionResponse{Content: s.reply(req), Provider: "local", FinishReason: "stop"}, nil
}

// What the check would see if a step's work were thrown away. A flow that kept
// the text it had before a step, and not the step's result, sends the later
// questions what a step that did nothing would have left: here the translation
// hands back the Spanish it was given, and the same requests that are read in
// the test above have to show it in every flow.
//
// The redaction used to be the other half of this: a model that named no span
// left the phone number in the text, and the check saw it go on. It no longer
// can. The library's redaction now runs its own detectors over the text beside
// the model, for the categories they cover, so what a model fails to name is
// masked all the same; a model answering nothing is the case a hostile message
// would try to bring about. So that half now holds the stronger thing: with a
// model that names nothing, nothing personal goes on.
func TestTheRequestsShowAStepThatDidNothing(t *testing.T) {
	byID := map[string]sim.Case{}
	for _, labelled := range sim.Cases() {
		byID[labelled.Message.ID] = labelled
	}
	recordedWith := func(t *testing.T, with flow, labelled sim.Case, provider func(schemaflux.Provider) schemaflux.Provider) []request {
		t.Helper()
		recording := &recorder{Provider: provider(sim.New(time.Millisecond))}
		ctx := schemaflux.NewClient("").WithProviderInstance(recording).Context(context.Background())
		if _, err := with.handle(desk.New(sim.OpenTickets()), ctx, labelled.Message); err != nil {
			t.Fatalf("%s, %s flow: %v", labelled.Message.ID, with.name, err)
		}
		return recording.seen()
	}

	for _, with := range allFlows {
		t.Run(with.name+", a model that names nothing to redact", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				labelled := byID["M-04"]
				requests := recordedWith(t, with, labelled, func(inner schemaflux.Provider) schemaflux.Provider {
					return substituting{Provider: inner, operation: sim.OpRedact, reply: func(schemaflux.CompletionRequest) string { return `{"spans":[]}` }}
				})
				var personal []string
				for _, sensitive := range labelled.Personal {
					personal = append(personal, sensitive.Text)
				}
				if len(personal) != 2 {
					t.Fatalf("M-04 has %d personal strings, want a phone number and a card number", len(personal))
				}
				redaction := stepOf(t, requests, sim.OpRedact, "Find all sensitive data")
				// The strings were there to be found: the redaction was sent them.
				if sent := leaks([]request{redaction}, personal); len(sent) != len(personal) {
					t.Fatalf("the redaction request does not carry the personal data it is to mask: %v", sent)
				}
				later := after(requests, redaction)
				if len(later) == 0 {
					t.Fatal("nothing was asked after the redaction")
				}
				if found := leaks(later, personal); len(found) != 0 {
					t.Errorf("the model named nothing and personal data went on: %v", found)
				}
			})
		})

		t.Run(with.name+", a translation that hands back the original", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				labelled := byID["M-13"]
				requests := recordedWith(t, with, labelled, func(inner schemaflux.Provider) schemaflux.Provider {
					return substituting{Provider: inner, operation: sim.OpTranslate, reply: func(req schemaflux.CompletionRequest) string {
						// Only the translation into English is made to fail: the
						// reply back is answered as the simulator answers it.
						_, text, _ := strings.Cut(req.UserPrompt, "Translate this text:\n")
						if strings.Contains(req.UserPrompt, "Translate to English") {
							return text
						}
						return labelled.Sent
					}}
				})
				translation := stepOf(t, requests, sim.OpTranslate, "Translate to English")
				found := leaks(after(requests, translation), []string{"Cobro doble", "Me cobraron dos veces"})
				if len(found) == 0 {
					t.Error("the original was sent on after a translation that changed nothing, and the check did not see it")
				}
				if missing := lacks(after(requests, translation), asksOfTheText, "I was charged 89 euros twice for order E-7745."); len(missing) == 0 {
					t.Error("questions were asked of the original and the check for the English text did not see it")
				}
			})
		})
	}
}

// repeating is a provider whose extraction answers, as the order number, any of
// the strings it finds in the text it is shown: what a model does with a
// message that gives a phone number where an order number would go. Everything
// else it leaves to the provider it wraps.
type repeating struct {
	schemaflux.Provider
	strings []string
}

func (r repeating) Complete(ctx context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	if sim.Operation(req) != sim.OpExtract {
		return r.Provider.Complete(ctx, req)
	}
	for _, one := range r.strings {
		if strings.Contains(req.UserPrompt, one) {
			answer, err := json.Marshal(desk.Details{OrderID: one})
			if err != nil {
				return schemaflux.CompletionResponse{}, err
			}
			return schemaflux.CompletionResponse{Content: string(answer), Provider: "local", FinishReason: "stop"}, nil
		}
	}
	return r.Provider.Complete(ctx, req)
}

// What a step reads out of a message is the message's text in another place.
// The simulator answers an extraction from the label, whatever it is sent, so a
// flow that took its details from the text before the redaction and wrote the
// reply from them passes every other test here. This one answers the
// extraction from what it was shown. With it, in every flow: nothing asked
// after the redaction carries the personal data, the details the flow reports
// do not hold it, and the extraction was asked of the redacted text, which is
// how the details come to be clean.
func TestWhatIsReadFromTheTextBeforeItIsRedactedDoesNotGoOn(t *testing.T) {
	var labelled sim.Case
	for _, one := range sim.Cases() {
		if one.Message.ID == "M-04" {
			labelled = one
		}
	}
	var personal []string
	for _, sensitive := range labelled.Personal {
		personal = append(personal, sensitive.Text)
	}
	if len(personal) != 2 {
		t.Fatalf("M-04 has %d personal strings, want a phone number and a card number", len(personal))
	}
	for _, with := range allFlows {
		t.Run(with.name, func(t *testing.T) {
			recording := &recorder{Provider: repeating{Provider: sim.New(time.Millisecond), strings: personal}}
			ctx := schemaflux.NewClient("").WithProviderInstance(recording).Context(context.Background())
			outcome, err := with.handle(desk.New(sim.OpenTickets()), ctx, labelled.Message)
			if err != nil {
				t.Fatal(err)
			}
			requests := recording.seen()
			redaction := stepOf(t, requests, sim.OpRedact, "Find all sensitive data")
			later := after(requests, redaction)
			if found := leaks(later, personal); len(found) != 0 {
				t.Errorf("personal data was sent on after the redaction: %v", found)
			}
			for _, one := range personal {
				if strings.Contains(outcome.Details.OrderID+outcome.Details.Product+outcome.Details.Amount, one) {
					t.Errorf("the details the flow reports hold personal data: %+v", outcome.Details)
				}
			}
			read := 0
			for _, one := range later {
				if one.operation == sim.OpExtract {
					read++
				}
			}
			if read != 1 {
				t.Errorf("the details were read %d times after the redaction, want once", read)
			}
		})
	}
}
