package desk_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/examples/flowdesk/desk"
	"github.com/monstercameron/schemaflux/examples/flowdesk/sim"
)

// aheadCalls and aheadWaits are what HandleAhead should make of a case, counted
// here from its own steps and held against the tables of flow_test.go, which
// count the same thing as a plain message and what a case's labels add to it.
//
// The calls: the look, the details and the ticket match; a step for each thing
// the text needs, and for a text that is redacted the details read again from
// what is left; the draft and the hand-off note; the audit; the translation of
// the reply, which is started beside the audit; and for a draft that fails, the
// redraft and, for a reply that is translated, a second translation, the first
// having been of a draft that was not sent.
func aheadCalls(labelled sim.Case) int {
	if labelled.Screen.Spam {
		return 3
	}
	calls := 3 + 2 + 1
	if labelled.TranslatedIn() {
		calls++
	}
	if labelled.Screen.PersonalData {
		calls += 2
	}
	if labelled.TranslatedBack() {
		calls++
	}
	if labelled.Redraft != "" {
		calls++
		if labelled.TranslatedBack() {
			calls++
		}
	}
	return calls
}

// The waits: the look; a wait for each thing the text needs, and one for the
// details of a text that was redacted; the draft; the audit, with the
// translation of the reply inside it; and for a draft that fails, the redraft
// and then its translation.
func aheadWaits(labelled sim.Case) int {
	if labelled.Screen.Spam {
		return 1
	}
	waits := 3
	if labelled.TranslatedIn() {
		waits++
	}
	if labelled.Screen.PersonalData {
		waits += 2
	}
	if labelled.Redraft != "" {
		waits++
		if labelled.TranslatedBack() {
			waits++
		}
	}
	return waits
}

// The two ways of counting agree for every labelled case.
func TestTheAheadFlowIsCountedTheSameBothWays(t *testing.T) {
	for _, labelled := range sim.Cases() {
		if got, want := wantCalls(ahead, labelled), aheadCalls(labelled); got != want {
			t.Errorf("%s: the table counts %d calls and the steps count %d", labelled.Message.ID, got, want)
		}
		if got, want := wantWaits(ahead, labelled), aheadWaits(labelled); got != want {
			t.Errorf("%s: the table counts %d waits and the steps count %d", labelled.Message.ID, got, want)
		}
	}
}

// HandleAhead reaches the outcome the other flows reach, for every labelled
// case, in the calls its comment says and never in more waits than HandleFast.
//
// Each case runs in a synctest bubble, so the simulator's delay is the bubble's
// clock and nothing here depends on how busy the machine is. The calls are
// exact: a translation started beside the audit is made on every run, because
// the bubble's clock does not move until every goroutine in it is waiting, the
// translation's among them. The waits are exact except in one case, which
// redraftRacesTheCancelledTranslation describes: a draft that fails its audit
// while its translation is under way has that translation cancelled, and the
// redraft starts at the instant the audit returns, which is the instant the
// translation's timer ends too. Whether the redraft is a wait of its own or
// joins the audit's depends on which of the two goroutines woken then runs
// first, so the run counts the number of waits the steps make or one fewer. The
// time the run takes is the same in both and is held exactly.
func TestTheAheadFlowReachesTheSameOutcomeReadingOnce(t *testing.T) {
	const delay = 40 * time.Millisecond
	fewer := 0
	for _, labelled := range sim.Cases() {
		t.Run(labelled.Message.ID+" "+labelled.Kind, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				outcome, provider, wall := run(t, ahead, labelled, delay)
				if outcome != labelled.Want() {
					t.Errorf("the outcome is not the label's:\n%s", strings.Join(differences(outcome, labelled.Want()), "\n"))
				}
				reference, _, _ := run(t, fast, labelled, time.Millisecond)
				if outcome != reference {
					t.Errorf("the ahead flow and the fast flow disagree:\n%s", strings.Join(differences(outcome, reference), "\n"))
				}
				if got, want := provider.Count(), aheadCalls(labelled); got != want {
					t.Errorf("%d calls, want %d (%s)", got, want, operations(provider))
				}
				if got, want := provider.Waits(), countedWaits(ahead, labelled); !slices.Contains(want, got) {
					t.Errorf("%d waits, want one of %v (%s)", got, want, operations(provider))
				}
				if want := time.Duration(aheadWaits(labelled)) * delay; wall != want {
					t.Errorf("took %v, want %d waits of %v, which is %v", wall, aheadWaits(labelled), delay, want)
				}
				if aheadWaits(labelled) > wantWaits(fast, labelled) || aheadCalls(labelled) > wantCalls(fast, labelled) {
					t.Errorf("the ahead flow costs %d calls in %d waits, more than the fast flow's %d in %d",
						aheadCalls(labelled), aheadWaits(labelled), wantCalls(fast, labelled), wantWaits(fast, labelled))
				}
				if aheadWaits(labelled) < wantWaits(fast, labelled) {
					fewer++
				}
			})
		})
	}
	if fewer < 5 {
		t.Errorf("only %d of the labelled cases take the ahead flow fewer waits than the fast flow", fewer)
	}
}

// What it is for, on the case it does most for: a message in Spanish whose
// draft passes takes four waits where the fast flow takes six, and eight calls
// where it makes eleven.
//
// The run is in a synctest bubble, so the translation of the reply is held to
// what "inside the audit's wait" says: it starts at the instant the audit
// starts and ends at the instant the audit ends, one delay later, and the whole
// run takes four delays.
func TestTheAheadFlowTakesAForeignMessageFourWaits(t *testing.T) {
	var spanish sim.Case
	for _, labelled := range sim.Cases() {
		if labelled.TranslatedBack() && !labelled.Screen.PersonalData && labelled.Redraft == "" && !labelled.Screen.Spam && spanish.Message.ID == "" {
			spanish = labelled
		}
	}
	if spanish.Message.ID == "" {
		t.Fatal("no labelled case is a foreign message with no personal data whose draft passes")
	}
	synctest.Test(t, func(t *testing.T) {
		const delay = 40 * time.Millisecond
		_, provider, wall := run(t, ahead, spanish, delay)
		if provider.Count() != 8 || provider.Waits() != 4 {
			t.Errorf("%s: %d calls in %d waits, want 8 in 4 (%s)", spanish.Message.ID, provider.Count(), provider.Waits(), operations(provider))
		}
		if wall != 4*delay {
			t.Errorf("%s: took %v, want 4 waits of %v", spanish.Message.ID, wall, delay)
		}
		if wantCalls(fast, spanish) != 11 || wantWaits(fast, spanish) != 6 {
			t.Errorf("the fast flow is expected to take this message %d calls in %d waits, and the comparison in the comment is with 11 in 6",
				wantCalls(fast, spanish), wantWaits(fast, spanish))
		}
		// The reply's translation ran inside the audit's wait.
		var audit, back sim.Call
		for _, call := range provider.Calls() {
			if call.Operation == sim.OpAudit {
				audit = call
			}
			if call.Operation == sim.OpTranslate {
				back = call
			}
		}
		if !back.Start.Equal(audit.Start) || !back.End.Equal(audit.End) || audit.Duration() != delay {
			t.Errorf("the translation of the reply, from %v to %v, was not the audit's wait, from %v to %v",
				back.Start.Sub(audit.Start), back.End.Sub(audit.Start), time.Duration(0), audit.End.Sub(audit.Start))
		}
	})
}

// Nothing after the redaction reads the personal data, though the message is
// read only once and the redaction is for the steps that write. And what the
// steps after a translation write from is the English.
func TestTheAheadFlowWritesFromTheTextPutRight(t *testing.T) {
	byID := map[string]sim.Case{}
	for _, labelled := range sim.Cases() {
		byID[labelled.Message.ID] = labelled
	}
	writes := []string{sim.OpGenerate, sim.OpSummarize}

	t.Run("redacted", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			labelled := byID["M-04"]
			requests := runRecorded(t, ahead, labelled)
			redaction := stepOf(t, requests, sim.OpRedact, "Find all sensitive data")
			var personal []string
			for _, sensitive := range labelled.Personal {
				personal = append(personal, sensitive.Text)
			}
			if len(personal) != 2 {
				t.Fatalf("M-04 has %d personal strings, want two", len(personal))
			}
			later := after(requests, redaction)
			if len(later) != 4 {
				t.Fatalf("%d requests were made after the redaction, want the details, the draft, the note and the audit", len(later))
			}
			if found := leaks(later, personal); len(found) != 0 {
				t.Errorf("personal data was sent after it was redacted: %v", found)
			}
			if found := lacks(later, writes, "I cannot change the phone number on my account"); len(found) != 0 {
				t.Errorf("a step after the redaction did not write from the text: %v", found)
			}
			// The questions are asked once: nothing is asked again after the first
			// wait. The details are another matter. They are what the reply is
			// written from, so they are read again from the redacted text.
			details := 0
			for _, one := range later {
				if one.operation == sim.OpAssess {
					t.Error("a question was asked after the redaction: the message was read again")
				}
				if one.operation == sim.OpExtract {
					details++
				}
			}
			if details != 1 {
				t.Errorf("the details were read %d times after the redaction, want once", details)
			}
		})
	})

	t.Run("translated, redacted and redrafted", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			labelled := byID["M-13"]
			requests := runRecorded(t, ahead, labelled)
			translation := stepOf(t, requests, sim.OpTranslate, "Translate to English")
			redaction := stepOf(t, requests, sim.OpRedact, "Find all sensitive data")
			if redaction.started < translation.ended {
				t.Fatal("the text was redacted before it was translated")
			}
			personal := []string{labelled.Personal[0].Text}
			later := after(requests, redaction)
			if found := leaks(later, personal); len(found) != 0 {
				t.Errorf("personal data was sent after it was redacted: %v", found)
			}
			if found := lacks(later, writes, "I was charged 89 euros twice for order E-7745."); len(found) != 0 {
				t.Errorf("a step after the translation did not write from the English: %v", found)
			}
			// The reply was translated twice: the draft's translation, started
			// beside the audit and thrown away when the audit failed, and the
			// redraft's, which is the one that was sent.
			back := 0
			for _, one := range later {
				if one.operation == sim.OpTranslate {
					back++
				}
			}
			if back != 2 {
				t.Errorf("%d translations of the reply, want the draft's and the redraft's", back)
			}
		})
	})
}

// A draft that fails its audit is not sent, in any language: what comes back is
// the redraft, whatever the translation started ahead of the audit had made of
// the draft by then.
//
// The translation and the audit end at the same instant of the bubble's clock,
// and which of the two goroutines runs first then is not fixed, so the repeats
// are runs of both orders: the redraft starts before the cancelled translation
// has returned in some and after in others.
func TestTheAheadFlowNeverSendsADraftThatFailed(t *testing.T) {
	seen := 0
	for _, labelled := range sim.Cases() {
		if labelled.Redraft == "" {
			continue
		}
		seen++
		synctest.Test(t, func(t *testing.T) {
			for range 10 {
				outcome, _, _ := run(t, ahead, labelled, 5*time.Millisecond)
				if outcome.Reply != labelled.Want().Reply || !outcome.Redrafted {
					t.Fatalf("%s: the reply is %q (redrafted: %t), want the redraft %q", labelled.Message.ID, outcome.Reply, outcome.Redrafted, labelled.Want().Reply)
				}
			}
		})
	}
	if seen < 2 {
		t.Fatalf("%d labelled cases have a draft that fails; the test needs one in English and one that is translated", seen)
	}
}

// lateProvider is a provider that makes one operation's requests wait before
// the simulator sees them, so that its calls start later than the others'. With
// ignoresCancel it also hides the caller's cancellation from the call, as a
// provider that is slow to notice a cancel does.
type lateProvider struct {
	schemaflux.Provider
	operation     string
	late          time.Duration
	ignoresCancel bool
}

func (l lateProvider) Complete(ctx context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	if sim.Operation(req) != l.operation {
		return l.Provider.Complete(ctx, req)
	}
	time.Sleep(l.late)
	if l.ignoresCancel {
		ctx = context.WithoutCancel(ctx)
	}
	return l.Provider.Complete(ctx, req)
}

// The two numbers of waits that redraftRacesTheCancelledTranslation describes,
// each made to happen on every run. With the audit started a millisecond after
// the translation of the draft, the translation has ended when the audit does,
// and the redraft is a wait of its own. With the translation started a
// millisecond after the audit, and a provider that does not notice it has been
// cancelled, it is still in flight when the redraft starts, and the redraft
// joins the audit's wait. The reply is the redraft's in both.
func TestTheAheadFlowCountsARedraftByWhetherTheCancelledTranslationHasReturned(t *testing.T) {
	const delay = 40 * time.Millisecond
	seen := 0
	for _, labelled := range sim.Cases() {
		if !redraftRacesTheCancelledTranslation(ahead, labelled) {
			continue
		}
		seen++
		cases := []struct {
			name     string
			provider func(*sim.Simulator) schemaflux.Provider
			waits    int
		}{
			{"the translation has returned", func(inner *sim.Simulator) schemaflux.Provider {
				return lateProvider{Provider: inner, operation: sim.OpAudit, late: time.Millisecond}
			}, aheadWaits(labelled)},
			{"the translation is still in flight", func(inner *sim.Simulator) schemaflux.Provider {
				return lateProvider{Provider: inner, operation: sim.OpTranslate, late: time.Millisecond, ignoresCancel: true}
			}, aheadWaits(labelled) - 1},
		}
		for _, tc := range cases {
			t.Run(labelled.Message.ID+", "+tc.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					inner := sim.New(delay)
					ctx := schemaflux.NewClient("").WithProviderInstance(tc.provider(inner)).Context(context.Background())
					outcome, err := desk.New(sim.OpenTickets()).HandleAhead(ctx, labelled.Message)
					if err != nil {
						t.Fatal(err)
					}
					if outcome != labelled.Want() {
						t.Errorf("the outcome is not the label's:\n%s", strings.Join(differences(outcome, labelled.Want()), "\n"))
					}
					if got := inner.Waits(); got != tc.waits {
						t.Errorf("%d waits, want %d (%s)", got, tc.waits, operations(inner))
					}
					if got, want := inner.Count(), aheadCalls(labelled); got != want {
						t.Errorf("%d calls, want %d (%s)", got, want, operations(inner))
					}
				})
			})
		}
	}
	if seen == 0 {
		t.Fatal("no labelled case is a foreign message whose draft fails its audit")
	}
}

// A failure stops the flow with no outcome, and what was started ahead of the
// audit does not outlive it.
func TestTheAheadFlowFailsWhole(t *testing.T) {
	var spanish, personal, simple sim.Case
	for _, labelled := range sim.Cases() {
		switch {
		case labelled.Screen.Spam:
		case labelled.TranslatedBack() && spanish.Message.ID == "":
			spanish = labelled
		case labelled.Screen.PersonalData && personal.Message.ID == "":
			personal = labelled
		case simple.Message.ID == "" && extraSteps(labelled) == 0:
			simple = labelled
		}
	}
	down := &schemaflux.APIError{Provider: "local", StatusCode: 500, Body: "down"}
	faults := []struct {
		name  string
		fault faulty
		with  sim.Case
	}{
		{"an assessment that is not JSON", faulty{operation: sim.OpAssess, content: "I think it is spam."}, simple},
		{"an assessment that answers nothing", faulty{operation: sim.OpAssess, content: `{}`}, simple},
		{"an assessment whose call fails", faulty{operation: sim.OpAssess, err: down}, simple},
		{"the details fail", faulty{operation: sim.OpExtract, err: down}, simple},
		{"a redaction that is not JSON", faulty{operation: sim.OpRedact, content: "done"}, personal},
		{"a redaction that names what is not in the text", faulty{operation: sim.OpRedact, content: `{"spans":[{"text":"not in the message","category":"phone"}]}`}, personal},
		{"the draft fails", faulty{operation: sim.OpGenerate, err: down}, simple},
		{"the hand-off note fails", faulty{operation: sim.OpSummarize, err: down}, simple},
		{"an audit that is not JSON", faulty{operation: sim.OpAudit, content: "passes"}, simple},
		{"an audit whose call fails, with a translation under way", faulty{operation: sim.OpAudit, err: down}, spanish},
		{"an audit that is not JSON, with a translation under way", faulty{operation: sim.OpAudit, content: "passes"}, spanish},
		{"a translation that fails", faulty{operation: sim.OpTranslate, err: down}, spanish},
	}
	for _, fault := range faults {
		t.Run(fault.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				inner := sim.New(5 * time.Millisecond)
				fault.fault.Provider = inner
				ctx := schemaflux.NewClient("").WithProviderInstance(fault.fault).Context(context.Background())
				outcome, err := desk.New(sim.OpenTickets()).HandleAhead(ctx, fault.with.Message)
				if err == nil {
					t.Fatalf("no error; outcome %+v", outcome)
				}
				if outcome != (desk.Outcome{}) {
					t.Errorf("an outcome came back beside the error: %+v", outcome)
				}
				// Nothing is still in flight once the flow has returned.
				for _, call := range inner.Calls() {
					if call.End.IsZero() {
						t.Errorf("a %s call was still in flight when the flow returned", call.Operation)
					}
				}
			})
		})
	}

	t.Run("a context that ends stops it with the context's error", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			const delay = 100 * time.Millisecond
			provider := sim.New(delay)
			client := schemaflux.NewClient("").WithProviderInstance(provider)
			ctx, cancel := context.WithTimeout(client.Context(context.Background()), delay*3/2)
			defer cancel()
			deadline, _ := ctx.Deadline()
			started := time.Now()
			outcome, err := desk.New(sim.OpenTickets()).HandleAhead(ctx, spanish.Message)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("the error is %v, which is not the context's", err)
			}
			if outcome != (desk.Outcome{}) {
				t.Errorf("a flow that failed returned an outcome: %+v", outcome)
			}
			if took := time.Since(started); took != delay*3/2 {
				t.Errorf("the flow returned after %v, want the context's %v", took, delay*3/2)
			}
			for _, call := range provider.Calls() {
				if call.Start.After(deadline) {
					t.Errorf("a %s call started %v after the context ended", call.Operation, call.Start.Sub(deadline))
				}
				if call.End.IsZero() {
					t.Errorf("a %s call was still in flight when the flow returned", call.Operation)
				}
			}
		})
	})
}
