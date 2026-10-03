package desk_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/examples/v2/flowdesk/desk"
	"github.com/monstercameron/schemaflux/examples/v2/flowdesk/sim"
)

// HandleFast reaches the outcome Handle reaches for every labelled case, and
// for a plain message does it in the calls and the waits its comment says:
// three waits and six calls, where Handle takes four and nine. What each flow
// makes of every case is held in flow_test.go; this is the comparison the fast
// flow is there for.
func TestTheFastFlowReachesTheSameOutcomeInFewerWaits(t *testing.T) {
	plainMessages := 0
	for _, labelled := range sim.Cases() {
		if labelled.Screen.Spam || extraSteps(labelled) != 0 {
			continue
		}
		plainMessages++
		t.Run(labelled.Message.ID+" "+labelled.Kind, func(t *testing.T) {
			// In a synctest bubble the calls of a group are all started before
			// any of them can end, so three calls made at once are one wait
			// however busy the machine is.
			synctest.Test(t, func(t *testing.T) {
				const delay = 40 * time.Millisecond
				outcome, provider, wall := run(t, fast, labelled, delay)
				reference, _, _ := run(t, fused, labelled, time.Millisecond)
				if outcome != reference {
					t.Errorf("the fast flow and the fused flow disagree:\n%s", strings.Join(differences(outcome, reference), "\n"))
				}
				if got := provider.Count(); got != 6 {
					t.Errorf("%d calls, want 6 (%s)", got, operations(provider))
				}
				if got := provider.Waits(); got != 3 {
					t.Errorf("%d waits, want 3 (%s)", got, operations(provider))
				}
				if wall != 3*delay {
					t.Errorf("took %v, want 3 waits of %v", wall, delay)
				}
				if wantWaits(fast, labelled) >= wantWaits(fused, labelled) || wantCalls(fast, labelled) >= wantCalls(fused, labelled) {
					t.Errorf("the fast flow is no faster than the fused one")
				}
			})
		})
	}
	if plainMessages < 5 {
		t.Errorf("only %d of the labelled cases are plain messages; the comparison rests on them", plainMessages)
	}
}

// A message that has to be translated or redacted costs the fast flow more
// than the fused flow: it is read twice. That is the price its comment names.
func TestTheFastFlowReadsATranslatedOrRedactedMessageTwice(t *testing.T) {
	for _, labelled := range sim.Cases() {
		if labelled.Screen.Spam || !rereads(labelled) {
			continue
		}
		t.Run(labelled.Message.ID+" "+labelled.Kind, func(t *testing.T) {
			_, provider, _ := run(t, fast, labelled, time.Millisecond)
			if got, want := provider.Count(), 6+extraSteps(labelled)+3; got != want {
				t.Errorf("%d calls, want %d: the plain six, the steps, and the second look of three (%s)", got, want, operations(provider))
			}
		})
	}
}

// What the fast flow does that the fused flow does not: a call whose result is
// thrown away can still fail the run, and spam costs three calls. Both are in
// the flow's doc comment.
func TestTheFastFlowFailsOnACallWhoseResultItDiscards(t *testing.T) {
	spam := sim.Cases()[0]
	if !spam.Screen.Spam {
		t.Fatalf("%s is not spam", spam.Message.ID)
	}
	broken := errors.New("the extraction service is down")

	t.Run("a failing extraction beside the look at spam", func(t *testing.T) {
		withBrokenExtraction := func() context.Context {
			provider := faulty{Provider: sim.New(time.Millisecond), operation: sim.OpExtract, err: broken}
			return schemaflux.NewClient("").WithProviderInstance(provider).Context(context.Background())
		}

		// The fused flow stops at the first question and never asks.
		outcome, err := fused.handle(desk.New(sim.OpenTickets()), withBrokenExtraction(), spam.Message)
		if err != nil || outcome.Status != desk.StatusDiscarded {
			t.Errorf("fused: %+v, %v; want the message discarded", outcome, err)
		}
		// The fast flow asked, and its error is the run's.
		outcome, err = fast.handle(desk.New(sim.OpenTickets()), withBrokenExtraction(), spam.Message)
		if err == nil || outcome != (desk.Outcome{}) {
			t.Errorf("fast: %+v, %v; want an error and no outcome", outcome, err)
		}
	})

	t.Run("spam costs three calls in a flow that asks everything at once and one in the others", func(t *testing.T) {
		for _, with := range allFlows {
			_, provider, _ := run(t, with, spam, 25*time.Millisecond)
			want := 1
			if looksAtOnce(with) {
				want = 3
			}
			if provider.Count() != want {
				t.Errorf("%s: %d calls for spam, want %d (%s)", with.name, provider.Count(), want, operations(provider))
			}
		}
	})
}
