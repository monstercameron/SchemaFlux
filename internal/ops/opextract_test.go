package ops

import (
	"context"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// A-012's own tests: the descriptor Extract now runs through, checked
// directly rather than only through the pre-existing extract tests (which
// this port leaves untouched and unmoved).

type opExtractTicket struct {
	ID     string  `json:"id"`
	Amount float64 `json:"amount"`
}

func TestExtractOpMetaIdentity(t *testing.T) {
	if extractOpMeta.ID.Name != "extract" {
		t.Fatalf("Name = %q, want extract", extractOpMeta.ID.Name)
	}
	if extractOpMeta.ID.Version != extractPromptVersion {
		t.Fatalf("Version = %q, want %q (Extract's existing cache-key version)", extractOpMeta.ID.Version, extractPromptVersion)
	}
}

func TestExtractOpMetaBatchClass(t *testing.T) {
	if extractOpMeta.Batch.Class != types.BatchNone {
		t.Fatalf("Batch.Class = %v, want BatchNone", extractOpMeta.Batch.Class)
	}
}

func TestExtractOpMetaSemantics(t *testing.T) {
	s := extractOpMeta.Semantics
	if s.Category != types.CategoryExtraction {
		t.Fatalf("Category = %v, want CategoryExtraction", s.Category)
	}
	if !s.InferencePermitted {
		t.Fatal("InferencePermitted = false; Extract's Transform mode infers missing fields")
	}
	if s.Stability != types.StabilityStable {
		t.Fatalf("Stability = %v, want stable", s.Stability)
	}
}

// TestNewExtractOpTransformModeUsesParseJSONStrict and its Strict sibling
// check that the two decode paths ported into newExtractOp's closure match
// what core.go's Extract did before the port: transform mode tolerates a
// well-formed answer with no field-level strictness, strict mode does not.
func TestNewExtractOpTransformModeUsesParseJSONStrict(t *testing.T) {
	op := newExtractOp[opExtractTicket](types.OpOptions{Mode: types.TransformMode}, "sys", "usr")

	value, err := op.Contract.Decode(`{"id":"T-1","amount":42.5}`)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if value.ID != "T-1" || value.Amount != 42.5 {
		t.Fatalf("Decode() = %+v", value)
	}
}

func TestNewExtractOpStrictModeRejectsUnknownField(t *testing.T) {
	op := newExtractOp[opExtractTicket](types.OpOptions{Mode: types.Strict}, "sys", "usr")

	_, err := op.Contract.Decode(`{"id":"T-1","amount":42.5,"extra":"surprise"}`)
	if err == nil {
		t.Fatal("strict Decode accepted an unrecognised field")
	}
}

func TestNewExtractOpStrictModeAcceptsExactMatch(t *testing.T) {
	op := newExtractOp[opExtractTicket](types.OpOptions{Mode: types.Strict}, "sys", "usr")

	value, err := op.Contract.Decode(`{"id":"T-1","amount":42.5}`)
	if err != nil {
		t.Fatalf("strict Decode rejected an exact match: %v", err)
	}
	if value.ID != "T-1" {
		t.Fatalf("Decode() = %+v", value)
	}
}

// TestNewExtractOpBuildPromptReturnsGivenPrompts checks BuildPrompt is a
// closure over the already-rendered prompts, not a second rendering path --
// the property that keeps the golden prompt snapshot from moving.
func TestNewExtractOpBuildPromptReturnsGivenPrompts(t *testing.T) {
	op := newExtractOp[opExtractTicket](types.OpOptions{}, "SYSTEM TEXT", "USER TEXT")
	system, user := op.BuildPrompt(nil, types.OpOptions{})
	if system != "SYSTEM TEXT" || user != "USER TEXT" {
		t.Fatalf("BuildPrompt() = (%q, %q)", system, user)
	}
}

// TestRunExtractOpEndToEnd drives runExtractOp -- the function core.go's
// Extract now calls -- directly, with a fake provider, so the port is
// checked at the level the task asks for rather than only transitively
// through schemaflux.Extract's own test suite.
func TestRunExtractOpEndToEnd(t *testing.T) {
	var gotSystem, gotUser string
	setLLMCaller(func(_ context.Context, system, user string, _ types.OpOptions) (string, error) {
		gotSystem, gotUser = system, user
		return `{"id":"T-9","amount":7}`, nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	value, repair, err := runExtractOp[opExtractTicket](context.Background(),
		types.OpOptions{Mode: types.TransformMode}, "the system prompt", "the user prompt")
	if err != nil {
		t.Fatalf("runExtractOp: %v", err)
	}
	if value.ID != "T-9" || value.Amount != 7 {
		t.Fatalf("runExtractOp() = %+v", value)
	}
	if repair.Attempts != 1 || repair.Repaired {
		t.Fatalf("repair = %+v, want a clean first-try success", repair)
	}
	if gotSystem != "the system prompt" || gotUser != "the user prompt" {
		t.Fatalf("the prompt reaching the provider was rewritten: system=%q user=%q", gotSystem, gotUser)
	}
}

// TestRunExtractOpRepairsOnMalformedBody exercises the same repair path the
// pre-port Extract had: a body that fails to decode is fed back, and the
// second attempt's prompt names the problem.
func TestRunExtractOpRepairsOnMalformedBody(t *testing.T) {
	calls := 0
	setLLMCaller(func(_ context.Context, _, user string, _ types.OpOptions) (string, error) {
		calls++
		if calls == 1 {
			return "not json at all", nil
		}
		if !strings.Contains(user, "could not be used") {
			t.Fatalf("repair prompt does not name the failure: %q", user)
		}
		return `{"id":"T-2","amount":1}`, nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	value, repair, err := runExtractOp[opExtractTicket](context.Background(),
		types.OpOptions{Mode: types.TransformMode}, "sys", "usr")
	if err != nil {
		t.Fatalf("runExtractOp: %v", err)
	}
	if value.ID != "T-2" {
		t.Fatalf("runExtractOp() = %+v", value)
	}
	if !repair.Repaired || repair.Attempts != 2 {
		t.Fatalf("repair = %+v, want a repaired 2-attempt result", repair)
	}
	if calls != 2 {
		t.Fatalf("provider called %d times, want 2", calls)
	}
}

// TestRunExtractOpRespectsConfiguredRepairBudget proves extractOpMeta's
// DefaultPolicy.RepairAttempts of zero really does mean "read
// config.GetRepairAttempts()", the same as the pre-port Extract's bare
// RepairPolicy{}, rather than silently pinning the budget to
// DefaultRepairAttempts. This is the case the comment in opextract.go
// documents; this test is what would fail if that decision were reverted.
func TestRunExtractOpRespectsConfiguredRepairBudget(t *testing.T) {
	t.Setenv("SCHEMAFLUX_REPAIR_ATTEMPTS", "3")

	calls := 0
	setLLMCaller(func(_ context.Context, _, _ string, _ types.OpOptions) (string, error) {
		calls++
		return "still not json", nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	_, repair, err := runExtractOp[opExtractTicket](context.Background(),
		types.OpOptions{Mode: types.TransformMode}, "sys", "usr")
	if err == nil {
		t.Fatal("expected an error after exhausting the repair budget")
	}
	// 1 first try + 3 repairs = 4 calls, reading the env override rather
	// than a value fixed inside the Op.
	if calls != 4 {
		t.Fatalf("provider called %d times, want 4 (1 + SCHEMAFLUX_REPAIR_ATTEMPTS=3)", calls)
	}
	if repair.Attempts != 4 {
		t.Fatalf("repair.Attempts = %d, want 4", repair.Attempts)
	}
}

// TestExtractUnchangedForCallerCode is a narrow smoke test that
// schemaflux.Extract's own package -- ops -- still produces the same value
// through the public Extract[T] entry point core.go exposes, now that its
// body delegates to runExtractOp. The fuller behavioural coverage (modes,
// steering, schema hints, error shapes) is the existing extract test suite,
// left untouched per A-012's instruction; this test exists so opextract_test.go
// itself demonstrates the top-level entry point, not only the internal
// helpers it introduces.
func TestExtractUnchangedForCallerCode(t *testing.T) {
	setLLMCaller(func(_ context.Context, _, _ string, _ types.OpOptions) (string, error) {
		return `{"id":"T-77","amount":100}`, nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	value, err := Extract[opExtractTicket]("irrelevant input text", NewExtractOptions())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if value.ID != "T-77" || value.Amount != 100 {
		t.Fatalf("Extract() = %+v", value)
	}
}
