package schemaflux_test

import (
	"os"
	"strings"
	"testing"
)

// DOC-001. The README's claims are the first thing an evaluator reads and the
// last thing anyone updates. These check the ones that were false long enough
// to end up in a review: each phrase below describes behaviour a test elsewhere
// in this repository proves, and if the behaviour is removed the phrase has to
// go with it.
func TestREADMEClaimsMatchTheLibrary(t *testing.T) {
	readme := readREADME(t)

	claims := []struct {
		phrase string
		proves string
	}{
		// I-02: an unpriced model reported $0.00, which reads as free.
		{"Unpriced is not free", "pricing/accounting_test.go"},
		{"No model is ever priced at another model's rates", "pricing/unpriced_test.go"},

		// I-10: the cost history was unbounded and scanned linearly.
		{"The history is bounded", "pricing/history_budget_test.go"},

		// I-11: budgets fired on every request past 80% and blocked nothing.
		{"Budgets alert on an edge, and can enforce", "pricing/history_budget_test.go"},

		// D-03 / S-003: requiredness was inferred from omitempty.
		{"schemaflux:\"required\"", "internal/ops/requiredness_test.go"},
		{"serialization* directive", "internal/ops/requiredness_test.go"},

		// D-07 / S-006: Creative told the model to invent values.
		{"does **not** invent", "internal/ops/prompt_mode_test.go"},

		// A-02 / OP-201: MinConfidence was prompt-only.
		{"Confidence floors are enforced where a confidence is reported", "confidence_integration_test.go"},

		// A-05 / OP-205: every rule went to the model.
		{"no provider call at all", "internal/ops/rules_test.go"},

		// X-03: error bodies carried the caller's payload.
		{"not the raw response body", "internal/llm/apierror_test.go"},
	}

	for _, claim := range claims {
		if !strings.Contains(readme, claim.phrase) {
			t.Errorf("the README no longer says %q (proved by %s)", claim.phrase, claim.proves)
		}
	}
}

// The claims the review found false must not come back. Each of these is a
// promise the code did not keep at the time it was written.
func TestREADMEDoesNotMakeTheOldFalseClaims(t *testing.T) {
	readme := readREADME(t)

	forbidden := []struct {
		phrase string
		why    string
	}{
		{"privacy filtering", "D-08: Exclude was a prompt hint with no post-filter"},
		{"automatically redacts", "T-07: redaction is a pattern matcher, not a classifier"},
		{"guaranteed valid", "the type contract is decode plus checks, not proof"},
	}

	lower := strings.ToLower(readme)
	for _, entry := range forbidden {
		if strings.Contains(lower, strings.ToLower(entry.phrase)) {
			t.Errorf("the README claims %q again (%s)", entry.phrase, entry.why)
		}
	}
}

// DOC-002. Every breaking change in this session has a migration note, because
// a recompile does not catch a behaviour change and a caller has no other way
// to find out.
func TestREADMEDocumentsTheBreakingChanges(t *testing.T) {
	readme := readREADME(t)

	if !strings.Contains(readme, "Breaking changes, and what to do about them") {
		t.Fatal("the README has no breaking-changes section")
	}

	for _, change := range []string{
		"renumbered so zero means unset", // A-005
		"constrained to `~string`",       // OP-204
		"no longer take a provider",      // OP-404
		"takes typed options",            // OP-504
		"Confidence floors are enforced", // OP-201
		"checks `TargetLength`",          // OP-402
		"no longer clears budget",        // PR-004
		"SCHEMAFLUX_JAEGER_ENDPOINT",     // the removed exporter
		"Cost history is bounded",        // PR-003
	} {
		if !strings.Contains(readme, change) {
			t.Errorf("no migration note for %q", change)
		}
	}
}

func readREADME(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("reading README.md: %v", err)
	}
	// Markdown wraps, so the prose is flattened before matching rather than
	// checked against whatever happened to fit on one line today.
	return strings.Join(strings.Fields(string(raw)), " ")
}
