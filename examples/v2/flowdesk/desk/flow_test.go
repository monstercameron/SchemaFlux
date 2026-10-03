package desk_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/examples/v2/flowdesk/desk"
	"github.com/monstercameron/schemaflux/examples/v2/flowdesk/sim"
)

// The desk's four flows, run against the simulator. Nothing here reaches a
// model or the network: the client is built with no key and is handed the
// simulator as its provider.

func TestMain(m *testing.M) {
	// The library logs each operation's start and end at info level, and each
	// failure at error level, to standard error. A hundred runs of twenty
	// operations bury a test's own failure in it, and some tests here fail
	// their calls on purpose.
	schemaflux.ConfigureLogging(schemaflux.LoggerConfig{Level: schemaflux.LogError, DisableStderr: true})
	os.Exit(m.Run())
}

// A flow is one of the four ways the desk handles a message.
type flow struct {
	name   string
	handle func(*desk.Desk, context.Context, desk.Message) (desk.Outcome, error)
}

var (
	fused      = flow{"fused", (*desk.Desk).Handle}
	oneAtATime = flow{"one at a time", (*desk.Desk).HandleOneAtATime}
	fast       = flow{"fast", (*desk.Desk).HandleFast}
	ahead      = flow{"ahead", (*desk.Desk).HandleAhead}
	allFlows   = []flow{fused, oneAtATime, fast, ahead}
)

// looksAtOnce says that a flow asks for the details and the ticket match beside
// its first look at a message, before it knows whether the message is spam.
func looksAtOnce(with flow) bool {
	return with.name == fast.name || with.name == ahead.name
}

// plain is what a message in English with no personal data, whose draft passes
// the audit, costs each flow by operation: nine calls and four waits for the
// fused flow, fifteen and fifteen for the one-at-a-time flow, six and three for
// the fast one and for the one that works ahead.
var plain = map[string]map[string]int{
	fused.name:      {sim.OpAssess: 5, sim.OpExtract: 1, sim.OpGenerate: 1, sim.OpSummarize: 1, sim.OpAudit: 1},
	oneAtATime.name: {sim.OpAssess: 11, sim.OpExtract: 1, sim.OpGenerate: 1, sim.OpSummarize: 1, sim.OpAudit: 1},
	fast.name:       {sim.OpAssess: 2, sim.OpExtract: 1, sim.OpGenerate: 1, sim.OpSummarize: 1, sim.OpAudit: 1},
	ahead.name:      {sim.OpAssess: 2, sim.OpExtract: 1, sim.OpGenerate: 1, sim.OpSummarize: 1, sim.OpAudit: 1},
}

// plainWaits are the waits of such a message.
var plainWaits = map[string]int{fused.name: 4, oneAtATime.name: 15, fast.name: 3, ahead.name: 3}

// widestGroup is the most calls any of the flow's waits holds at once: the
// draft and the hand-off with the next action and the prediction, in the fused
// flow, and the three questions of the first look in the fast one.
var widestGroup = map[string]int{fused.name: 4, oneAtATime.name: 1, fast.name: 3, ahead.name: 3}

// run handles one case with one flow, against a simulator and a client of its
// own, and reports the outcome, the simulator and how long the flow took.
func run(t *testing.T, with flow, labelled sim.Case, delay time.Duration) (desk.Outcome, *sim.Simulator, time.Duration) {
	t.Helper()
	provider := sim.New(delay)
	client := schemaflux.NewClient("").WithProviderInstance(provider)
	ctx := client.Context(context.Background())

	started := time.Now()
	outcome, err := with.handle(desk.New(sim.OpenTickets()), ctx, labelled.Message)
	wall := time.Since(started)
	if err != nil {
		t.Fatalf("%s, %s flow: %v", labelled.Message.ID, with.name, err)
	}
	return outcome, provider, wall
}

// extraSteps is how many steps a case's labels add to a plain message, each of
// which is one call and one wait in any flow: the translation into English,
// the translation of the reply back (which a language the desk cannot name
// does not get), the redaction, and the redraft.
func extraSteps(labelled sim.Case) int {
	steps := 0
	if labelled.TranslatedIn() {
		steps++
	}
	if labelled.TranslatedBack() {
		steps++
	}
	if labelled.Screen.PersonalData {
		steps++
	}
	if labelled.Redraft != "" {
		steps++
	}
	return steps
}

// rereads says that the fast flow reads a case's message a second time, which
// it does when the text has to be translated or redacted: a call for the
// triage and what follows, one for the details and one for the ticket match,
// all at once.
func rereads(labelled sim.Case) bool {
	return labelled.TranslatedIn() || labelled.Screen.PersonalData
}

// wantOperations is what a flow should make of a case, by operation. Spam ends
// at the first look: one call in the fused and the one-at-a-time flows, and in
// the fast flow and the one that works ahead the three calls of the look, whose
// details and ticket match are thrown away.
//
// The flow that works ahead reads a message once, so a translation costs it the
// translation and nothing more. A redaction costs it the redaction and the
// details read again, from the text with the personal data masked: the details
// are what the reply is written from. And a draft that fails its audit, in a
// language the reply is translated back into, has been translated once already
// by the time the audit says so, beside the translation of the redraft.
func wantOperations(with flow, labelled sim.Case) map[string]int {
	if labelled.Screen.Spam {
		if looksAtOnce(with) {
			return map[string]int{sim.OpAssess: 2, sim.OpExtract: 1}
		}
		return map[string]int{sim.OpAssess: 1}
	}
	want := map[string]int{}
	for operation, count := range plain[with.name] {
		want[operation] = count
	}
	if labelled.TranslatedIn() {
		want[sim.OpTranslate]++
	}
	if labelled.TranslatedBack() {
		want[sim.OpTranslate]++
	}
	if labelled.Screen.PersonalData {
		want[sim.OpRedact] = 1
	}
	if labelled.Redraft != "" {
		want[sim.OpRewrite] = 1
	}
	if with.name == fast.name && rereads(labelled) {
		want[sim.OpAssess] += 2
		want[sim.OpExtract]++
	}
	if with.name == ahead.name && labelled.Screen.PersonalData {
		want[sim.OpExtract]++
	}
	if with.name == ahead.name && labelled.Redraft != "" && labelled.TranslatedBack() {
		want[sim.OpTranslate]++
	}
	return want
}

// wantCalls and wantWaits are what a flow should make of a case.
func wantCalls(with flow, labelled sim.Case) int {
	total := 0
	for _, count := range wantOperations(with, labelled) {
		total += count
	}
	return total
}

func wantWaits(with flow, labelled sim.Case) int {
	if labelled.Screen.Spam {
		return 1
	}
	waits := plainWaits[with.name] + extraSteps(labelled)
	if with.name == fast.name && rereads(labelled) {
		waits++
	}
	if with.name == ahead.name {
		// The reply is translated beside the audit, so its translation is no
		// wait of its own, unless the draft fails: then the redraft is
		// translated after it is written.
		if labelled.TranslatedBack() && labelled.Redraft == "" {
			waits--
		}
		// The details are read again once the text has been redacted.
		if labelled.Screen.PersonalData {
			waits++
		}
	}
	return waits
}

// differences names every field of an outcome that is not what was wanted.
func differences(got, want desk.Outcome) []string {
	var found []string
	gotFields, wantFields := reflect.ValueOf(got), reflect.ValueOf(want)
	for i := 0; i < gotFields.NumField(); i++ {
		if !reflect.DeepEqual(gotFields.Field(i).Interface(), wantFields.Field(i).Interface()) {
			found = append(found, fmt.Sprintf("%s = %#v, want %#v",
				gotFields.Type().Field(i).Name, gotFields.Field(i).Interface(), wantFields.Field(i).Interface()))
		}
	}
	return found
}

// operations is how many calls of each operation a run made, written out in a
// fixed order.
func operations(provider *sim.Simulator) string {
	counts := map[string]int{}
	for _, call := range provider.Calls() {
		counts[call.Operation]++
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = fmt.Sprintf("%s %d", name, counts[name])
	}
	return strings.Join(parts, ", ")
}

// The labelled cases have to cover what the desk can do, or a test that passes
// on all of them says less than it looks.
func TestTheLabelledCasesCoverTheDesk(t *testing.T) {
	cases := sim.Cases()
	if len(cases) < 17 {
		t.Fatalf("%d labelled cases, want at least seventeen", len(cases))
	}
	covered := map[string]bool{}
	seen := map[string]bool{}
	for _, labelled := range cases {
		if seen[labelled.Message.ID] {
			t.Errorf("%s is labelled twice", labelled.Message.ID)
		}
		seen[labelled.Message.ID] = true
		if !strings.Contains(labelled.Message.Body, "[ref "+labelled.Message.ID+"]") {
			t.Errorf("%s does not carry its reference in its body", labelled.Message.ID)
		}
		covered["status "+labelled.Status] = true
		covered["screen language "+labelled.Screen.Language] = true
		if labelled.Screen.Spam {
			continue
		}
		covered["language "+labelled.Screen.Language] = true
		covered["team "+labelled.Triage.Team] = true
		covered["intent "+labelled.Triage.Intent] = true
		covered["next "+labelled.NextAction] = true
		covered[fmt.Sprintf("translated in %t", labelled.TranslatedIn())] = true
		covered[fmt.Sprintf("translated back %t", labelled.TranslatedBack())] = true
		covered[fmt.Sprintf("personal data %t", labelled.Screen.PersonalData)] = true
		covered[fmt.Sprintf("follow-up %t", labelled.FollowsUp != "")] = true
		covered[fmt.Sprintf("redrafted %t", labelled.Redraft != "")] = true
		covered[fmt.Sprintf("writes again %t", labelled.WritesAgain)] = true
	}
	needed := []string{
		"status " + desk.StatusDiscarded, "status " + desk.StatusAnswered, "status " + desk.StatusAsked, "status " + desk.StatusHeld,
		"team billing", "team technical", "team shipping", "team account",
		"intent asking a question", "intent making a complaint", "intent requesting an action", "intent giving feedback",
		"next close the ticket", "next wait for the customer", "next follow up tomorrow", "next escalate to a manager",
		"translated in true", "translated in false", "translated back true", "translated back false",
		"personal data true", "personal data false",
		"follow-up true", "follow-up false", "redrafted true", "redrafted false",
		"writes again true", "writes again false",
	}
	// Every language the screen offers, for a message that is worked, and not
	// only for spam.
	for _, language := range []string{"English", "Spanish", "French", "German", "Portuguese", "another language"} {
		needed = append(needed, "language "+language)
	}
	for _, want := range needed {
		if !covered[want] {
			t.Errorf("no labelled case covers %q", want)
		}
	}
}

// Every labelled case that is worked carries what the flows need to be checked
// against it.
func TestTheLabelsAreWhole(t *testing.T) {
	tickets := map[string]sim.Case{}
	for _, ticket := range sim.OpenTickets() {
		tickets[ticket.Customer] = sim.Case{}
	}
	for _, labelled := range sim.Cases() {
		id := labelled.Message.ID
		if labelled.Screen.Spam {
			if labelled.Status != desk.StatusDiscarded {
				t.Errorf("%s is spam and is labelled %q", id, labelled.Status)
			}
			continue
		}
		if labelled.Draft == "" || labelled.Handoff == "" || labelled.NextAction == "" {
			t.Errorf("%s is worked and has no draft, hand-off note or next action", id)
		}
		if labelled.TranslatedIn() && labelled.English == "" {
			t.Errorf("%s is translated and has no English text", id)
		}
		if labelled.TranslatedBack() && labelled.Sent == "" {
			t.Errorf("%s is answered in its language and has no reply in it", id)
		}
		if !labelled.TranslatedBack() && labelled.Sent != "" {
			t.Errorf("%s is answered in English and has a reply in another language", id)
		}
		if labelled.Screen.PersonalData && len(labelled.Personal) == 0 {
			t.Errorf("%s has personal data and names none", id)
		}
		// Only the senders of the follow-ups are customers of an open ticket.
		_, customer := tickets[labelled.Message.From]
		if customer != (labelled.FollowsUp != "") {
			t.Errorf("%s: the sender is a customer of an open ticket (%t) and the label says it follows one up (%t)", id, customer, labelled.FollowsUp != "")
		}
	}
}

// Every message, through every flow: each field of the outcome is the one the
// label calls for, the flows agree, and each made the calls it should.
//
// Each case runs in a synctest bubble. The flow that works ahead starts the
// translation of the reply beside the audit, and on a machine that is busy the
// audit could end and cancel it before its goroutine had made its call, which
// would leave a call out of the count. The bubble's clock does not move until
// that goroutine is waiting on its call, so the call is made on every run.
func TestAllFlowsReachTheLabelledOutcome(t *testing.T) {
	for _, labelled := range sim.Cases() {
		t.Run(labelled.Message.ID+" "+labelled.Kind, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				want := labelled.Want()
				outcomes := map[string]desk.Outcome{}
				for _, with := range allFlows {
					outcome, provider, _ := run(t, with, labelled, time.Millisecond)
					outcomes[with.name] = outcome
					for _, difference := range differences(outcome, want) {
						t.Errorf("%s flow: %s", with.name, difference)
					}
					if got, want := provider.Count(), wantCalls(with, labelled); got != want {
						t.Errorf("%s flow made %d calls, want %d (%s)", with.name, got, want, operations(provider))
					}
				}
				for _, with := range allFlows[1:] {
					if outcomes[with.name] != outcomes[fused.name] {
						t.Errorf("the %s flow and the fused flow disagree:\n%s", with.name,
							strings.Join(differences(outcomes[with.name], outcomes[fused.name]), "\n"))
					}
				}
			})
		})
	}
}

// The steps a label calls for are the ones the desk takes: a translation in
// and one back for a message in a language it can name, a translation in alone
// for one it cannot, a redaction for personal data, a rewrite for a draft that
// fails the audit, and none of them otherwise. It runs in a synctest bubble for
// the reason TestAllFlowsReachTheLabelledOutcome does.
func TestEachFlowMakesTheOperationsTheLabelCallsFor(t *testing.T) {
	for _, labelled := range sim.Cases() {
		for _, with := range allFlows {
			t.Run(labelled.Message.ID+" "+with.name, func(t *testing.T) {
				t.Parallel()
				synctest.Test(t, func(t *testing.T) {
					_, provider, _ := run(t, with, labelled, time.Millisecond)
					got := map[string]int{}
					for _, call := range provider.Calls() {
						got[call.Operation]++
						if call.End.Before(call.Start) {
							t.Errorf("a %s call ends before it starts", call.Operation)
						}
					}
					if want := wantOperations(with, labelled); !reflect.DeepEqual(got, want) {
						t.Errorf("operations made: %v, want %v", got, want)
					}
				})
			})
		}
	}
}

// redraftRacesTheCancelledTranslation says that a flow's wait count for a case
// is one of two numbers. It is so for the flow that works ahead, for a draft
// that fails its audit when the reply is translated: the translation of the
// draft was started beside the audit with the same delay, so it ends at the
// same instant as the audit, and the redraft starts at that instant as the
// audit's call returns. The simulator counts a wait when a call starts with
// none in flight. If the cancelled translation has not yet returned when the
// redraft starts, the redraft joins the audit's wait and the run counts one wait
// fewer; if it has, the redraft is a wait of its own. Which comes first is
// decided by which of two goroutines woken at the same instant runs first, and
// nothing in the flow orders them. In a synctest bubble both outcomes were
// counted in a thousand runs at each of GOMAXPROCS 1, 2, 4 and 8, each between
// 30 and 70 percent of them. The time the run takes is the same either way,
// since the redraft starts at that instant in both.
func redraftRacesTheCancelledTranslation(with flow, labelled sim.Case) bool {
	return with.name == ahead.name && labelled.Redraft != "" && labelled.TranslatedBack()
}

// countedWaits is the numbers of waits the simulator may count for a case in a
// flow: the one wantWaits says, and for redraftRacesTheCancelledTranslation also
// one fewer.
func countedWaits(with flow, labelled sim.Case) []int {
	waits := wantWaits(with, labelled)
	if redraftRacesTheCancelledTranslation(with, labelled) {
		return []int{waits, waits - 1}
	}
	return []int{waits}
}

// Every flow waits for the model the number of times its comment says.
//
// The run is in a synctest bubble, so the simulator's delay is a delay of the
// bubble's clock and the figures are exact. The simulator counts the times a
// call started with none in flight, and that is held to the count the table
// says, with the most calls a wait held. The time the run took is held to the
// number of waits times the delay, with nothing added: every wait is one delay
// long, nothing the flow does between calls takes any of the bubble's time, and
// so a group of calls that were made one after another and not at once would
// show as more time. For the flow that works ahead and a redraft of a reply that
// is translated the wait count has two values, and redraftRacesTheCancelledTranslation
// says why; the time is the same under both.
func TestTheFlowsWaitAsOftenAsTheySay(t *testing.T) {
	const delay = 50 * time.Millisecond
	for _, labelled := range sim.Cases() {
		for _, with := range allFlows {
			t.Run(labelled.Message.ID+" "+with.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					widest := widestGroup[with.name]
					if labelled.Screen.Spam && !looksAtOnce(with) {
						widest = 1
					}

					_, provider, wall := run(t, with, labelled, delay)

					if got := provider.Waits(); !slices.Contains(countedWaits(with, labelled), got) {
						t.Errorf("waited %d times, want one of %v (%d calls: %s)", got, countedWaits(with, labelled), provider.Count(), operations(provider))
					}
					if got := provider.MaxInFlight(); got != widest {
						t.Errorf("%d calls in flight at once, want %d", got, widest)
					}
					if want := time.Duration(wantWaits(with, labelled)) * delay; wall != want {
						t.Errorf("took %v, want %d waits of %v, which is %v", wall, wantWaits(with, labelled), delay, want)
					}
				})
			})
		}
	}
}

// A desk with no open ticket has nothing to match a message against, and the
// library asks nothing of an empty list: one call fewer in every flow, and the
// same waits, since the match is made beside other calls.
func TestADeskWithNoOpenTicketDoesNotAskForAMatch(t *testing.T) {
	labelled := sim.Cases()[1]
	if labelled.FollowsUp != "" || labelled.Screen.Spam {
		t.Fatalf("%s is not a plain message", labelled.Message.ID)
	}
	want := map[string]struct{ calls, waits int }{
		fused.name:      {8, 4},
		oneAtATime.name: {14, 14},
		fast.name:       {5, 3},
		ahead.name:      {5, 3},
	}
	for _, with := range allFlows {
		t.Run(with.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				provider := sim.New(25 * time.Millisecond)
				ctx := schemaflux.NewClient("").WithProviderInstance(provider).Context(context.Background())
				outcome, err := with.handle(desk.New(nil), ctx, labelled.Message)
				if err != nil {
					t.Fatal(err)
				}
				if outcome != labelled.Want() {
					t.Errorf("outcome differs:\n%s", strings.Join(differences(outcome, labelled.Want()), "\n"))
				}
				if got := provider.Count(); got != want[with.name].calls {
					t.Errorf("%d calls with no open ticket, want %d (%s)", got, want[with.name].calls, operations(provider))
				}
				if got := provider.Waits(); got != want[with.name].waits {
					t.Errorf("%d waits with no open ticket, want %d", got, want[with.name].waits)
				}
				if got, full := provider.Count(), wantCalls(with, labelled); got != full-1 {
					t.Errorf("%d calls with no open ticket and %d with three: the match is one call", got, full)
				}
			})
		})
	}
}

// A flow stops when its context ends: the error that comes back is the
// context's, the calls that had not been made are not made, and none starts
// after the context has ended.
//
// The context's deadline is on the bubble's clock, a delay and a half after the
// start: after the first wait and before the second can end. So the flow returns
// at the deadline exactly, and the calls of the second wait, which started at the
// end of the first, are the ones that are cut off.
func TestAFlowStopsWhenItsContextEnds(t *testing.T) {
	labelled := sim.Cases()[1]
	for _, with := range allFlows {
		t.Run(with.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				const delay = 100 * time.Millisecond
				provider := sim.New(delay)
				client := schemaflux.NewClient("").WithProviderInstance(provider)
				ctx, cancel := context.WithTimeout(client.Context(context.Background()), delay*3/2)
				defer cancel()
				deadline, _ := ctx.Deadline()

				started := time.Now()
				outcome, err := with.handle(desk.New(sim.OpenTickets()), ctx, labelled.Message)
				if err == nil {
					t.Fatalf("no error from a flow whose context ended; outcome %+v", outcome)
				}
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Errorf("the error is %v, which is not the context's", err)
				}
				if outcome != (desk.Outcome{}) {
					t.Errorf("a flow that failed returned an outcome: %+v", outcome)
				}
				if took := time.Since(started); took != delay*3/2 {
					t.Errorf("the flow returned after %v, want the context's %v", took, delay*3/2)
				}
				if made, all := provider.Count(), wantCalls(with, labelled); made >= all {
					t.Errorf("%d calls were made after the context ended, of %d in all", made, all)
				}
				for _, call := range provider.Calls() {
					if call.Start.After(deadline) {
						t.Errorf("a %s call started %v after the context ended", call.Operation, call.Start.Sub(deadline))
					}
				}
			})
		})
	}

	t.Run("a context that has ended before the flow starts makes no call", func(t *testing.T) {
		for _, with := range allFlows {
			provider := sim.New(time.Millisecond)
			client := schemaflux.NewClient("").WithProviderInstance(provider)
			ctx, cancel := context.WithCancel(client.Context(context.Background()))
			cancel()
			outcome, err := with.handle(desk.New(sim.OpenTickets()), ctx, labelled.Message)
			if !errors.Is(err, context.Canceled) {
				t.Errorf("%s: the error is %v, which is not the context's", with.name, err)
			}
			if outcome != (desk.Outcome{}) {
				t.Errorf("%s: a flow that failed returned an outcome: %+v", with.name, outcome)
			}
			if provider.Count() != 0 {
				t.Errorf("%s: %d calls were made on a context that had ended", with.name, provider.Count())
			}
		}
	})
}

// faulty is a provider that answers one operation wrongly and every other as
// the simulator does.
type faulty struct {
	schemaflux.Provider
	operation string
	content   string
	err       error
}

func (f faulty) Complete(ctx context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	if sim.Operation(req) != f.operation {
		return f.Provider.Complete(ctx, req)
	}
	if f.err != nil {
		return schemaflux.CompletionResponse{}, f.err
	}
	return schemaflux.CompletionResponse{Content: f.content, Provider: "local", FinishReason: "stop"}, nil
}

// A call that goes wrong stops the flow with an error the library has
// classified, and the flow returns no outcome: not the part of it that was
// made, and not a default.
func TestAFlowThatIsRefusedAnAnswerReturnsNoOutcomeAndAClassifiedError(t *testing.T) {
	personal := sim.Cases()[3]
	if !personal.Screen.PersonalData {
		t.Fatalf("%s has no personal data", personal.Message.ID)
	}
	simple := sim.Cases()[1]
	faults := []struct {
		name  string
		fault faulty
		with  sim.Case
		// status is the status of the provider's own error, for the faults that
		// are a failed call and not an answer that was read and refused.
		status int
	}{
		{"an assessment that is not JSON", faulty{operation: sim.OpAssess, content: "I think it is spam."}, simple, 0},
		{"an assessment that answers a question with a word", faulty{operation: sim.OpAssess, content: `{"q1":"yes","q2":"billing"}`}, simple, 0},
		{"an assessment whose call fails with a server error", faulty{operation: sim.OpAssess, err: &schemaflux.APIError{Provider: "local", StatusCode: 500, Body: "down"}}, simple, 500},
		{"an assessment that answers nothing", faulty{operation: sim.OpAssess, content: `{}`}, simple, 0},
		{"a redaction that names what is not in the text", faulty{operation: sim.OpRedact, content: `{"spans":[{"text":"not in the message","category":"phone"}]}`}, personal, 0},
		{"a redaction that is not JSON", faulty{operation: sim.OpRedact, content: "done"}, personal, 0},
		{"an audit that answers nothing usable", faulty{operation: sim.OpAudit, content: `{"policies":"fine"}`}, simple, 0},
		{"an audit that is not JSON", faulty{operation: sim.OpAudit, content: "passes"}, simple, 0},
		{"an audit whose call fails with a server error", faulty{operation: sim.OpAudit, err: &schemaflux.APIError{Provider: "local", StatusCode: 500, Body: "the audit service is down"}}, simple, 500},
		{"an audit whose call fails with a refusal", faulty{operation: sim.OpAudit, err: &schemaflux.APIError{Provider: "local", StatusCode: 400, Body: "the request was refused"}}, simple, 400},
	}
	for _, fault := range faults {
		for _, with := range allFlows {
			t.Run(fault.name+", "+with.name, func(t *testing.T) {
				fault.fault.Provider = sim.New(time.Millisecond)
				ctx := schemaflux.NewClient("").WithProviderInstance(fault.fault).Context(context.Background())
				outcome, err := with.handle(desk.New(sim.OpenTickets()), ctx, fault.with.Message)
				if err == nil {
					t.Fatalf("no error; outcome %+v", outcome)
				}
				if outcome != (desk.Outcome{}) {
					t.Errorf("an outcome came back beside the error: %+v", outcome)
				}
				// An answer that was read and refused is classified by the
				// operation that read it. A call that failed comes back as the
				// provider's own typed error: the library does not wrap it in
				// a classification.
				var apiErr *schemaflux.APIError
				switch {
				case fault.status == 0 && schemaflux.KindOf(err) == 0:
					t.Errorf("the error is not classified: %v", err)
				case fault.status != 0 && (!errors.As(err, &apiErr) || apiErr.StatusCode != fault.status):
					t.Errorf("the error is not the provider's error with status %d: %v", fault.status, err)
				}
				if strings.Contains(err.Error(), fault.with.Message.Body) {
					t.Errorf("the error repeats the message: %v", err)
				}
			})
		}
	}
}
