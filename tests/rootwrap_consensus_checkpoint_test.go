package tests

import (
	"context"
	"errors"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
)

// This file covers the sampling, checkpointing, and review wrappers: Vote,
// ExactAgreement, Checkpoint, NewMemoryCheckpointStore, and Approve. All are
// pure Go control flow over caller-supplied Steps -- no provider is involved.

// --- Vote / ExactAgreement --------------------------------------------------

func TestVoteReturnsTheAgreedWinner(t *testing.T) {
	answers := []string{"billing", "billing", "shipping"}
	call := 0
	step := func(context.Context) (string, error) {
		v := answers[call]
		call++
		return v, nil
	}

	winner, record, err := schemaflux.Vote(context.Background(), 3, step, schemaflux.ExactAgreement[string](2), schemaflux.VoteOptions{Concurrency: 1})
	if err != nil {
		t.Fatalf("Vote: %v", err)
	}
	if winner != "billing" {
		t.Errorf("winner = %q, want the 2-of-3 agreement", winner)
	}
	if record.Requested != 3 || record.Succeeded != 3 || record.Agree != 2 {
		t.Errorf("record = %+v, want Requested=3 Succeeded=3 Agree=2", record)
	}
}

// When no group reaches min, ExactAgreement abstains and Vote must report
// ErrReviewRequired instead of guessing a plurality winner.
func TestVoteAbstainsWhenNoAgreementReachesTheMinimum(t *testing.T) {
	answers := []string{"a", "b", "c"}
	call := 0
	step := func(context.Context) (string, error) {
		v := answers[call]
		call++
		return v, nil
	}

	_, _, err := schemaflux.Vote(context.Background(), 3, step, schemaflux.ExactAgreement[string](2), schemaflux.VoteOptions{Concurrency: 1})
	if err == nil {
		t.Fatal("expected an abstention error when nothing reached the agreement threshold")
	}
	if !errors.Is(err, schemaflux.ErrReviewRequired) {
		t.Errorf("err = %v, want it to match ErrReviewRequired", err)
	}
}

func TestVoteWithNoReconcilerIsAnError(t *testing.T) {
	step := func(context.Context) (string, error) { return "x", nil }
	_, _, err := schemaflux.Vote[string](context.Background(), 3, step, nil, schemaflux.VoteOptions{})
	if err == nil {
		t.Fatal("expected an error: agreement is a policy the caller must supply")
	}
}

// --- Checkpoint / NewMemoryCheckpointStore ----------------------------------

func TestCheckpointRunsOnceAndResumesFromTheStore(t *testing.T) {
	store := schemaflux.NewMemoryCheckpointStore()
	calls := 0
	step := func(context.Context) (string, error) {
		calls++
		return "computed value", nil
	}

	value1, outcome1, err := schemaflux.Checkpoint(context.Background(), store, "run-1", "step-a", "same input", step)
	if err != nil {
		t.Fatalf("Checkpoint (first): %v", err)
	}
	if outcome1.Resumed {
		t.Error("first call reported Resumed=true, want it to have actually run")
	}

	value2, outcome2, err := schemaflux.Checkpoint(context.Background(), store, "run-1", "step-a", "same input", step)
	if err != nil {
		t.Fatalf("Checkpoint (second): %v", err)
	}
	if !outcome2.Resumed {
		t.Error("second call with the same input did not resume from the store")
	}
	if value1 != value2 {
		t.Errorf("value1=%q value2=%q, want the resumed value to match", value1, value2)
	}
	if calls != 1 {
		t.Errorf("step ran %d times, want exactly 1 (the second call should have resumed)", calls)
	}
}

// A changed input must be detected, not silently served the stale value.
func TestCheckpointDetectsAChangedInput(t *testing.T) {
	store := schemaflux.NewMemoryCheckpointStore()
	calls := 0
	step := func(context.Context) (string, error) {
		calls++
		return "computed value", nil
	}

	_, _, err := schemaflux.Checkpoint(context.Background(), store, "run-1", "step-a", "input-v1", step)
	if err != nil {
		t.Fatalf("Checkpoint (first): %v", err)
	}
	_, outcome, err := schemaflux.Checkpoint(context.Background(), store, "run-1", "step-a", "input-v2", step)
	if err != nil {
		t.Fatalf("Checkpoint (second): %v", err)
	}
	if !outcome.InputChanged {
		t.Error("InputChanged = false, want the changed input to be detected")
	}
	if outcome.Resumed {
		t.Error("Resumed = true, want a fresh run for changed input")
	}
	if calls != 2 {
		t.Errorf("step ran %d times, want 2 (changed input reruns the step)", calls)
	}
}

func TestCheckpointWithNoStoreIsAnError(t *testing.T) {
	step := func(context.Context) (string, error) { return "x", nil }
	_, _, err := schemaflux.Checkpoint[string](context.Background(), nil, "run-1", "step-a", "input", step)
	if err == nil {
		t.Fatal("expected an error when no store is given")
	}
}

// --- Approve -----------------------------------------------------------------

func TestApproveLetsAnApprovedCandidateThrough(t *testing.T) {
	step := func(context.Context) (string, error) { return "candidate answer", nil }
	gate := func(context.Context, string) (schemaflux.ApprovalOutcome, error) {
		return schemaflux.ApprovalOutcome{Approved: true}, nil
	}

	value, err := schemaflux.Approve(context.Background(), step, gate, []string{"doc-1"})
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if value != "candidate answer" {
		t.Errorf("value = %q, want the approved candidate", value)
	}
}

// A declined gate must return ErrReviewRequired rather than the value or a
// generic error, so a caller can route it to a human queue.
func TestApproveDeclinedGateReturnsReviewRequired(t *testing.T) {
	step := func(context.Context) (string, error) { return "candidate answer", nil }
	gate := func(context.Context, string) (schemaflux.ApprovalOutcome, error) {
		return schemaflux.ApprovalOutcome{Approved: false, FailedChecks: []string{"low confidence"}}, nil
	}

	_, err := schemaflux.Approve(context.Background(), step, gate, []string{"doc-1"})
	if err == nil {
		t.Fatal("expected an error when the gate declines")
	}
	if !errors.Is(err, schemaflux.ErrReviewRequired) {
		t.Errorf("err = %v, want it to match ErrReviewRequired", err)
	}
}

func TestApproveWithNoGateIsAnError(t *testing.T) {
	step := func(context.Context) (string, error) { return "x", nil }
	_, err := schemaflux.Approve[string](context.Background(), step, nil, nil)
	if err == nil {
		t.Fatal("expected an error when no gate is given")
	}
}
