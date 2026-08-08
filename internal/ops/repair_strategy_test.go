package ops

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// TC-006. Strategy is chosen by failure class: repairStrategyFor classifies,
// syntaxRepairPrompt/repairPrompt/detectUnrelatedFieldLoss are what act on
// the choice. These tests cover the classification, the loss detector, and
// then the end-to-end repair loop for the two verify-bullet cases: a repair
// that fixes the flagged field while dropping a valid unrelated one is
// rejected, and an invariant failure produces a regeneration rather than an
// edit.

func TestRepairStrategyFor(t *testing.T) {
	cases := []struct {
		kind types.ErrorKind
		want repairStrategy
	}{
		{types.KindMalformedOutput, strategySyntax},
		{types.KindInvariantViolation, strategyRegenerate},
		{types.KindEvidenceViolation, strategyRegenerate},
		{types.KindSchemaViolation, strategyPatch},
		{types.KindBatchProtocolViolation, strategyPatch},
		{types.KindOutputTruncated, strategyPatch},
		{types.KindUnknown, strategyPatch},
		{types.KindTimeout, strategyPatch},
		{types.KindRateLimited, strategyPatch},
		{types.KindReviewRequired, strategyPatch},
	}
	for _, tc := range cases {
		if got := repairStrategyFor(tc.kind); got != tc.want {
			t.Errorf("repairStrategyFor(%v) = %v, want %v", tc.kind, got, tc.want)
		}
	}
}

func TestDetectUnrelatedFieldLoss(t *testing.T) {
	cases := []struct {
		name     string
		previous string
		next     string
		wantErr  bool
	}{
		{
			"patch adds the missing field, keeps the rest",
			`{"name":"Ada","email":"ada@example.com"}`,
			`{"name":"Ada","email":"ada@example.com","age":36}`,
			false,
		},
		{
			"patch drops a previously-present, unrelated field",
			`{"name":"Ada","email":"ada@example.com"}`,
			`{"name":"Ada","age":36}`,
			true,
		},
		{
			"value changes are allowed -- may be the fix itself",
			`{"name":"Ada","age":35}`,
			`{"name":"Ada","age":36}`,
			false,
		},
		{
			"no shared keys at all: treated as a full replacement, not a loss",
			`{"unrelated":true}`,
			`{"name":"Ada","age":36}`,
			false,
		},
		{
			"identical bodies",
			`{"name":"Ada","age":36}`,
			`{"name":"Ada","age":36}`,
			false,
		},
		{
			"previous body is not JSON: nothing to compare",
			`not json at all`,
			`{"name":"Ada"}`,
			false,
		},
		{
			"new body is not JSON: nothing to compare",
			`{"name":"Ada"}`,
			`not json either`,
			false,
		},
		{
			"previous field explicitly null is not counted as present",
			`{"name":"Ada","note":null}`,
			`{"name":"Ada"}`,
			false,
		},
		{
			"multiple unrelated fields dropped",
			`{"name":"Ada","email":"a@example.com","phone":"555-1234"}`,
			`{"name":"Ada","age":36}`,
			true,
		},
		{
			"empty previous body: nothing to lose",
			`{}`,
			`{"name":"Ada"}`,
			false,
		},
		{
			"field becomes explicitly null in the new body: counted as lost",
			`{"name":"Ada","email":"a@example.com"}`,
			`{"name":"Ada","email":null}`,
			true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := detectUnrelatedFieldLoss(tc.previous, tc.next)
			if tc.wantErr && err == nil {
				t.Fatalf("detectUnrelatedFieldLoss(%q, %q) = nil, want an error", tc.previous, tc.next)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("detectUnrelatedFieldLoss(%q, %q) = %v, want nil", tc.previous, tc.next, err)
			}
		})
	}
}

// A repair that fixes the flagged field while dropping a valid unrelated one
// is rejected -- the task's own verify bullet, proven through the real
// repair loop rather than the detector alone.
func TestRepairThatDropsUnrelatedFieldIsRejected(t *testing.T) {
	type target struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Email string `json:"email"`
	}

	call := 0
	setLLMCaller(func(_ context.Context, _, _ string, _ types.OpOptions) (string, error) {
		call++
		switch call {
		case 1:
			// Missing "email" -- an invariant failure (schema violation
			// kind), strategyPatch. "id" and "name" are already correct.
			return `{"id":"abc","name":"Ada"}`, nil
		default:
			// Adds the missing email, but silently drops the previously
			// valid, unrelated "name" -- the loss this test exists to
			// catch. "id" is repeated so the two bodies share ground; a
			// repair that shares nothing with its predecessor is a full
			// replacement, not a loss (see the "no shared keys" case in
			// TestDetectUnrelatedFieldLoss).
			return `{"id":"abc","email":"ada@example.com"}`, nil
		}
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	// Decode never fails on a missing field -- it decodes whatever shape it
	// is given. The email requirement is an Invariant, so the first body
	// decodes successfully and fails validation for a reason
	// detectUnrelatedFieldLoss cannot see from the error text alone, which
	// is exactly why the check operates on the raw bodies instead.
	requireEmail := func(t target) error {
		if t.Email == "" {
			return &types.OperationError{Kind: types.KindSchemaViolation, Message: "missing email"}
		}
		return nil
	}

	op := Op[string, target]{
		ID:        types.OperationID{Name: "dropField", Version: "v1"},
		Semantics: types.Semantics{Stability: types.StabilityExperimental},
		Contract: OutputContract[target]{
			SchemaName: "target",
			Decode: func(body string) (target, error) {
				var t target
				if err := json.Unmarshal([]byte(body), &t); err != nil {
					return target{}, &types.OperationError{Kind: types.KindMalformedOutput, Message: "invalid JSON"}
				}
				return t, nil
			},
			Invariants: []func(target) error{requireEmail},
		},
		Batch:       BatchAlgebra[string, target]{Class: types.BatchNone},
		BuildPrompt: func(input string, _ types.OpOptions) (string, string) { return "sys", input },
	}

	_, repair, err := RunOp(context.Background(), op, "x", types.OpOptions{})
	if err == nil {
		t.Fatal("expected the repair to be rejected for dropping an unrelated field")
	}
	if !strings.Contains(err.Error(), "dropped") {
		t.Fatalf("error does not name the loss: %v", err)
	}
	if repair.Attempts < 2 {
		t.Fatalf("repair.Attempts = %d, want at least 2 (the repair was attempted)", repair.Attempts)
	}
}

// An invariant failure regenerates from source: the prior (invalid)
// candidate's raw body never appears in the next prompt.
func TestInvariantFailureRegeneratesRatherThanEdits(t *testing.T) {
	const fabricated = "the-model-invented-this-value"

	call := 0
	var repairPromptSeen string
	setLLMCaller(func(_ context.Context, _, user string, _ types.OpOptions) (string, error) {
		call++
		if call == 2 {
			repairPromptSeen = user
		}
		if call == 1 {
			return fabricated, nil
		}
		return "a-real-value", nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	mustBeReal := func(value string) error {
		if value == fabricated {
			return &types.OperationError{Kind: types.KindInvariantViolation, Message: "the value was not grounded in the source"}
		}
		return nil
	}

	value, repair, err := RunOp(context.Background(), echoOp(mustBeReal), "x", types.OpOptions{})
	if err != nil {
		t.Fatalf("RunOp: %v", err)
	}
	if value != "a-real-value" {
		t.Fatalf("value = %q", value)
	}
	if !repair.Repaired {
		t.Fatal("expected the second attempt to be recorded as a repair")
	}
	if strings.Contains(repairPromptSeen, fabricated) {
		t.Fatalf("the regeneration prompt echoed the fabricated answer back to the model: %q", repairPromptSeen)
	}
}

// An evidence failure regenerates the same way an invariant failure does.
func TestEvidenceFailureRegeneratesRatherThanEdits(t *testing.T) {
	const fabricated = "unsupported-claim"

	call := 0
	var repairPromptSeen string
	setLLMCaller(func(_ context.Context, _, user string, _ types.OpOptions) (string, error) {
		call++
		if call == 2 {
			repairPromptSeen = user
		}
		if call == 1 {
			return fabricated, nil
		}
		return "supported-claim", nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	needsEvidence := func(value string) error {
		if value == fabricated {
			return &types.OperationError{Kind: types.KindEvidenceViolation, Message: "no supporting span"}
		}
		return nil
	}

	value, repair, err := RunOp(context.Background(), echoOp(needsEvidence), "x", types.OpOptions{})
	if err != nil {
		t.Fatalf("RunOp: %v", err)
	}
	if value != "supported-claim" {
		t.Fatalf("value = %q", value)
	}
	if !repair.Repaired {
		t.Fatal("expected a repair")
	}
	if strings.Contains(repairPromptSeen, fabricated) {
		t.Fatalf("the regeneration prompt echoed the fabricated answer back: %q", repairPromptSeen)
	}
}

// A syntax failure is the one case that includes the previous body,
// bounded and delimited, so the model can see the exact bytes that failed.
func TestSyntaxFailureIncludesThePreviousResponseDelimited(t *testing.T) {
	const broken = `{"name": "Ada", "age": }` // trailing garbage, invalid JSON

	call := 0
	var repairPromptSeen string
	setLLMCaller(func(_ context.Context, _, user string, _ types.OpOptions) (string, error) {
		call++
		if call == 2 {
			repairPromptSeen = user
		}
		if call == 1 {
			return broken, nil
		}
		return `{"name":"Ada","age":36}`, nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	type target struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}
	op := Op[string, target]{
		ID:        types.OperationID{Name: "syntaxTest", Version: "v1"},
		Semantics: types.Semantics{Stability: types.StabilityExperimental},
		Contract: OutputContract[target]{
			SchemaName: "target",
			Decode: func(body string) (target, error) {
				var t target
				if err := json.Unmarshal([]byte(body), &t); err != nil {
					return target{}, &types.OperationError{Kind: types.KindMalformedOutput, Message: "invalid JSON"}
				}
				return t, nil
			},
		},
		Batch:       BatchAlgebra[string, target]{Class: types.BatchNone},
		BuildPrompt: func(input string, _ types.OpOptions) (string, string) { return "sys", input },
	}

	_, repair, err := RunOp(context.Background(), op, "x", types.OpOptions{})
	if err != nil {
		t.Fatalf("RunOp: %v", err)
	}
	if !repair.Repaired {
		t.Fatal("expected a repair")
	}
	if !strings.Contains(repairPromptSeen, broken) {
		t.Fatalf("the syntax repair prompt should include the previous broken response verbatim, got:\n%s", repairPromptSeen)
	}
	if !strings.Contains(repairPromptSeen, untrustedBoundary) || !strings.Contains(repairPromptSeen, untrustedBoundaryEnd) {
		t.Fatal("the previous response should be delimited, not blended into the instruction text")
	}
}
