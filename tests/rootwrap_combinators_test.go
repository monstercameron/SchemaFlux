package tests

import (
	"context"
	"errors"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
)

// This file covers Escalate, Fallback, and Until -- the pure Go control-flow
// combinators that compose caller-supplied Steps with no provider of their
// own. Each test proves the wrapper reaches ops' real decision logic (which
// step ran, and why) rather than only that a value came back.

// Escalate must run the stronger step when first fails with a non-terminal
// error, and report why.
func TestEscalateRunsStrongerOnNonTerminalFailure(t *testing.T) {
	firstCalls, strongerCalls := 0, 0
	first := func(context.Context) (string, error) {
		firstCalls++
		return "", errors.New("transient failure")
	}
	stronger := func(context.Context) (string, error) {
		strongerCalls++
		return "strong answer", nil
	}

	value, record, err := schemaflux.Escalate(context.Background(), first, stronger, nil)
	if err != nil {
		t.Fatalf("Escalate: %v", err)
	}
	if value != "strong answer" {
		t.Errorf("value = %q, want the stronger step's answer", value)
	}
	if !record.Escalated || record.Reason != "error" {
		t.Errorf("record = %+v, want Escalated=true Reason=error", record)
	}
	if firstCalls != 1 || strongerCalls != 1 {
		t.Errorf("firstCalls=%d strongerCalls=%d, want exactly one of each", firstCalls, strongerCalls)
	}
}

// A terminal failure (KindInvalidRequest) must NOT escalate: retrying a
// malformed request on a stronger model fails identically and only spends
// money.
func TestEscalateDoesNotEscalateOnTerminalFailure(t *testing.T) {
	// A 400 classifies as KindInvalidRequest, which OperationError.Terminal()
	// reports true for: a malformed request fails identically on a stronger
	// model, so escalating would only spend money for nothing.
	terminalErr := &schemaflux.APIError{StatusCode: 400}
	strongerCalled := false
	first := func(context.Context) (string, error) { return "", terminalErr }
	stronger := func(context.Context) (string, error) {
		strongerCalled = true
		return "should not run", nil
	}

	_, record, err := schemaflux.Escalate(context.Background(), first, stronger, nil)
	if err == nil {
		t.Fatal("expected the terminal failure to surface")
	}
	if strongerCalled {
		t.Error("the stronger step ran for a terminal failure, which only spends money for nothing")
	}
	if record.Escalated {
		t.Errorf("record.Escalated = true, want false for a terminal failure")
	}
}

// accept turning down a successful first answer must escalate too, and say
// "rejected" rather than "error".
func TestEscalateRunsStrongerWhenAcceptRejects(t *testing.T) {
	first := func(context.Context) (int, error) { return 1, nil }
	stronger := func(context.Context) (int, error) { return 9, nil }
	accept := func(v int) bool { return v > 5 }

	value, record, err := schemaflux.Escalate(context.Background(), first, stronger, accept)
	if err != nil {
		t.Fatalf("Escalate: %v", err)
	}
	if value != 9 {
		t.Errorf("value = %d, want the stronger step's answer after rejection", value)
	}
	if !record.Escalated || record.Reason != "rejected" {
		t.Errorf("record = %+v, want Escalated=true Reason=rejected", record)
	}
}

// accept approving the first answer must NOT run the stronger step at all.
func TestEscalateAcceptsFirstWithoutRunningStronger(t *testing.T) {
	strongerCalled := false
	first := func(context.Context) (int, error) { return 7, nil }
	stronger := func(context.Context) (int, error) {
		strongerCalled = true
		return 99, nil
	}
	accept := func(v int) bool { return v > 5 }

	value, record, err := schemaflux.Escalate(context.Background(), first, stronger, accept)
	if err != nil {
		t.Fatalf("Escalate: %v", err)
	}
	if value != 7 || record.Escalated || strongerCalled {
		t.Errorf("value=%d record=%+v strongerCalled=%v, want the accepted first answer with no escalation", value, record, strongerCalled)
	}
}

// Fallback is Escalate with no accept function: any success from primary is
// kept, any failure moves to alternate.
func TestFallbackUsesAlternateOnPrimaryFailure(t *testing.T) {
	primary := func(context.Context) (string, error) { return "", errors.New("primary down") }
	alternate := func(context.Context) (string, error) { return "backup", nil }

	value, err := schemaflux.Fallback(context.Background(), primary, alternate)
	if err != nil {
		t.Fatalf("Fallback: %v", err)
	}
	if value != "backup" {
		t.Errorf("value = %q, want the alternate's answer", value)
	}
}

func TestFallbackKeepsPrimaryOnSuccess(t *testing.T) {
	alternateCalled := false
	primary := func(context.Context) (string, error) { return "primary", nil }
	alternate := func(context.Context) (string, error) {
		alternateCalled = true
		return "backup", nil
	}

	value, err := schemaflux.Fallback(context.Background(), primary, alternate)
	if err != nil {
		t.Fatalf("Fallback: %v", err)
	}
	if value != "primary" || alternateCalled {
		t.Errorf("value=%q alternateCalled=%v, want the primary answer with no fallback call", value, alternateCalled)
	}
}

// Until must retry until pred is satisfied, and report the attempt count.
func TestUntilRetriesUntilPredicateSatisfied(t *testing.T) {
	calls := 0
	step := func(context.Context) (int, error) {
		calls++
		return calls, nil
	}
	pred := func(v int) bool { return v >= 3 }

	value, attempts, err := schemaflux.Until(context.Background(), step, pred, 5)
	if err != nil {
		t.Fatalf("Until: %v", err)
	}
	if value != 3 || attempts != 3 {
		t.Errorf("value=%d attempts=%d, want value=3 attempts=3", value, attempts)
	}
}

// Running out of attempts is an error, and the last (still-rejected) answer
// comes back alongside it for inspection -- Until must not fail open by
// returning the rejected answer as a success.
func TestUntilRunningOutOfAttemptsIsAnError(t *testing.T) {
	step := func(context.Context) (int, error) { return 1, nil }
	pred := func(v int) bool { return v >= 100 }

	value, attempts, err := schemaflux.Until(context.Background(), step, pred, 3)
	if err == nil {
		t.Fatal("expected an error when the predicate is never satisfied")
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3 (the max)", attempts)
	}
	if value != 1 {
		t.Errorf("value = %d, want the last rejected answer returned alongside the error", value)
	}
}
