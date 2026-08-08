package ops

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// CF-08 / P-03. Guard takes a list of Go predicates and used to call a model
// whenever one failed, to write suggestions nobody asked for. A caller handing
// this function pure functions has every reason to believe it runs pure
// functions; instead it spent their money, sent the failed-check messages --
// built from their own state -- to a provider, and did both on a hardcoded
// two-second timeout that ignored their configured tier and deadline.

type signup struct {
	Email string
	Age   int
}

func emailPresent(s signup) (bool, string) {
	if strings.Contains(s.Email, "@") {
		return true, ""
	}
	return false, "email is not an address"
}

func oldEnough(s signup) (bool, string) {
	if s.Age >= 18 {
		return true, ""
	}
	return false, "age is below 18"
}

// countingCaller installs an LLM caller that records how many times it ran, so
// a test can assert on calls that did not happen. Asserting on the returned
// value cannot do that: a guard that called a provider and ignored the answer
// returns exactly what one that never called returns.
func countingCaller(t *testing.T, reply string, err error) *int32 {
	t.Helper()

	var calls int32
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		atomic.AddInt32(&calls, 1)
		return reply, err
	})
	t.Cleanup(func() { setLLMCaller(nil) })
	return &calls
}

func TestGuardMakesNoProviderCallWhenACheckFails(t *testing.T) {
	calls := countingCaller(t, "suggestion", nil)

	result := Guard(context.Background(), signup{Email: "nope", Age: 14}, emailPresent, oldEnough)

	if result.CanProceed {
		t.Error("CanProceed is true with two failed checks")
	}
	if len(result.FailedChecks) != 2 {
		t.Errorf("FailedChecks = %v, want both", result.FailedChecks)
	}
	if got := atomic.LoadInt32(calls); got != 0 {
		t.Errorf("Guard made %d provider calls; a list of Go predicates must cost nothing", got)
	}
	if len(result.Suggestions) != 0 {
		t.Errorf("Guard produced suggestions without being asked: %v", result.Suggestions)
	}
}

func TestGuardMakesNoProviderCallWhenEverythingPasses(t *testing.T) {
	calls := countingCaller(t, "suggestion", nil)

	result := Guard(context.Background(), signup{Email: "a@b.com", Age: 30}, emailPresent, oldEnough)

	if !result.CanProceed {
		t.Errorf("CanProceed is false: %v", result.FailedChecks)
	}
	if got := atomic.LoadInt32(calls); got != 0 {
		t.Errorf("made %d provider calls", got)
	}
}

func TestGuardWithNoChecksPasses(t *testing.T) {
	calls := countingCaller(t, "", nil)

	result := Guard(context.Background(), signup{})
	if !result.CanProceed {
		t.Error("a guard with no checks refused")
	}
	if got := atomic.LoadInt32(calls); got != 0 {
		t.Errorf("made %d provider calls with no checks at all", got)
	}
}

// The failed-check messages are the caller's, written by the caller's own
// functions, and they are what got sent to a provider unasked.
func TestGuardKeepsTheCheckMessagesLocal(t *testing.T) {
	var sent []string
	setLLMCaller(func(_ context.Context, _, user string, _ types.OpOptions) (string, error) {
		sent = append(sent, user)
		return "", nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	Guard(context.Background(), signup{Email: "ssn 123-45-6789", Age: 12},
		func(s signup) (bool, string) { return false, "rejected: " + s.Email })

	if len(sent) != 0 {
		t.Errorf("the check message left the process: %q", sent)
	}
}

func TestGuardWithSuggestionsAsksOnlyWhenAChecksFailed(t *testing.T) {
	calls := countingCaller(t, "try a real address\nwait until 18", nil)

	passing := GuardWithSuggestions(context.Background(), signup{Email: "a@b.com", Age: 30},
		types.OpOptions{}, emailPresent, oldEnough)
	if !passing.CanProceed {
		t.Fatalf("CanProceed false: %v", passing.FailedChecks)
	}
	if got := atomic.LoadInt32(calls); got != 0 {
		t.Errorf("asked for suggestions with nothing to suggest (%d calls)", got)
	}

	failing := GuardWithSuggestions(context.Background(), signup{Email: "nope", Age: 14},
		types.OpOptions{}, emailPresent, oldEnough)
	if failing.CanProceed {
		t.Error("CanProceed true with failed checks")
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Errorf("made %d provider calls, want exactly 1", got)
	}
	if len(failing.Suggestions) != 2 {
		t.Errorf("Suggestions = %v, want the two lines the model returned", failing.Suggestions)
	}
}

// The caller's options reach the provider. Guard used to impose the Quick tier
// and a two-second deadline, so a caller who had configured neither got both.
func TestGuardWithSuggestionsUsesTheCallersOptions(t *testing.T) {
	var seen types.OpOptions
	setLLMCaller(func(_ context.Context, _, _ string, opts types.OpOptions) (string, error) {
		seen = opts
		return "a suggestion", nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	GuardWithSuggestions(context.Background(), signup{Age: 1},
		types.OpOptions{Intelligence: types.Smart, Steering: "be terse"},
		oldEnough)

	if seen.Intelligence != types.Smart {
		t.Errorf("Intelligence = %v, want the caller's Smart rather than an imposed Quick", seen.Intelligence)
	}
	if seen.Steering != "be terse" {
		t.Errorf("Steering = %q, want the caller's", seen.Steering)
	}
}

// A provider outage must not block an operation Go already approved or
// rejected: the verdict was decided before the suggestion was requested.
func TestGuardWithSuggestionsSurvivesAProviderFailure(t *testing.T) {
	countingCaller(t, "", errors.New("provider down"))

	result := GuardWithSuggestions(context.Background(), signup{Email: "nope", Age: 14},
		types.OpOptions{}, emailPresent, oldEnough)

	if result.CanProceed {
		t.Error("a provider outage flipped the verdict to proceed")
	}
	if len(result.FailedChecks) != 2 {
		t.Errorf("FailedChecks = %v; the verdict changed because suggestions failed", result.FailedChecks)
	}
	if len(result.Suggestions) != 0 {
		t.Errorf("Suggestions = %v after a failure", result.Suggestions)
	}
}

func TestGuardWithSuggestionsDropsBlankLines(t *testing.T) {
	countingCaller(t, "first\n\n   \nsecond\n", nil)

	result := GuardWithSuggestions(context.Background(), signup{Age: 1}, types.OpOptions{}, oldEnough)

	if len(result.Suggestions) != 2 {
		t.Errorf("Suggestions = %#v, want the two non-empty lines", result.Suggestions)
	}
	for _, s := range result.Suggestions {
		if strings.TrimSpace(s) == "" {
			t.Errorf("a blank suggestion survived: %q", s)
		}
	}
}

func TestGuardReportsEveryFailedCheckNotJustTheFirst(t *testing.T) {
	countingCaller(t, "", nil)

	result := Guard(context.Background(), signup{Email: "nope", Age: 1},
		emailPresent, oldEnough,
		func(signup) (bool, string) { return false, "third rule" })

	if len(result.FailedChecks) != 3 {
		t.Errorf("FailedChecks = %v, want all three", result.FailedChecks)
	}
}

func TestGuardChecksRunInOrder(t *testing.T) {
	countingCaller(t, "", nil)

	var order []string
	mark := func(name string) func(signup) (bool, string) {
		return func(signup) (bool, string) {
			order = append(order, name)
			return false, name
		}
	}

	Guard(context.Background(), signup{}, mark("first"), mark("second"), mark("third"))

	if strings.Join(order, ",") != "first,second,third" {
		t.Errorf("checks ran in order %v", order)
	}
}
