package llm

import (
	"strings"
	"testing"
)

// TestPanicTBHelperIsANoOp proves Helper() does nothing observable -- it
// exists only to satisfy the protocolTB interface newProtocolProvider takes,
// mirroring testing.T's own Helper().
func TestPanicTBHelperIsANoOp(t *testing.T) {
	// Calling it must not panic and must not require any setup.
	panicTB{}.Helper()
}

// TestPanicTBFatalfPanics proves Fatalf turns a formatted message into a
// panic carrying that message -- the mechanism newProtocolProvider relies on
// to report a configuration failure as a failedOutcome via runProtocolCheck's
// recover, without threading an error return through every check.
func TestPanicTBFatalfPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("Fatalf did not panic")
		}
		err, ok := r.(error)
		if !ok {
			t.Fatalf("panic value = %v (%T), want an error", r, r)
		}
		if !strings.Contains(err.Error(), "boom 42") {
			t.Errorf("panic message = %q, want it to contain the formatted message", err.Error())
		}
	}()
	panicTB{}.Fatalf("boom %d", 42)
}

// TestRunProtocolCheckRecoversAPanicIntoAFailedOutcome proves a check body
// that panics -- e.g. newProtocolProvider's Fatalf, or a bug in the check
// itself -- is turned into a failedOutcome by runProtocolCheck rather than
// crashing the whole conformance run.
func TestRunProtocolCheckRecoversAPanicIntoAFailedOutcome(t *testing.T) {
	outcome := runProtocolCheck(func() ConformanceOutcome {
		panic("synthetic check failure")
	})
	if outcome.Passed {
		t.Error("a panicking check reported Passed=true")
	}
	if !strings.Contains(outcome.Detail, "synthetic check failure") {
		t.Errorf("Detail = %q, want it to mention the panic value", outcome.Detail)
	}
}

// TestRunProtocolCheckPassesThroughANormalOutcome proves the non-panicking
// path returns the check's own outcome unchanged.
func TestRunProtocolCheckPassesThroughANormalOutcome(t *testing.T) {
	want := passedOutcome("everything fine")
	got := runProtocolCheck(func() ConformanceOutcome { return want })
	if got != want {
		t.Errorf("runProtocolCheck(non-panicking) = %+v, want %+v unchanged", got, want)
	}
}
