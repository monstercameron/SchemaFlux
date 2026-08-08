package ops

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// This file closes the remaining coverage gaps in audit.go and verify.go
// after an earlier pass: AuditWithModel/VerifyWithModel's own error paths
// (auditCore/verifyCore failing and the wrapper propagating the zero value
// rather than manufacturing a JudgmentResult), buildAuditSummary's severity
// bucketing, mergeAuditOptions' field-by-field merge, and the confidence
// floor Verify enforces on its own model-reported confidence.

// --- audit.go: refusal paths ---------------------------------------------

// TestAuditRefusesUnmarshalableInput proves a value json.Marshal cannot
// serialise is rejected before any provider call, rather than silently
// auditing an empty payload.
func TestAuditRefusesUnmarshalableInput(t *testing.T) {
	calls := 0
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		calls++
		return auditMockFindings, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	unmarshalable := map[string]any{"fn": func() {}}
	_, err := Audit(unmarshalable)
	if err == nil {
		t.Fatal("expected Audit to reject a value it cannot marshal")
	}
	if calls != 0 {
		t.Fatalf("Audit called the provider %d times despite a marshal failure -- no call should have been made", calls)
	}
}

// TestAuditWithModelPropagatesAuditCoreFailure proves AuditWithModel does
// not manufacture a JudgmentResult (a Pass verdict, an empty Issues slice)
// when the underlying auditCore call fails -- it returns the zero value and
// the same error.
func TestAuditWithModelPropagatesAuditCoreFailure(t *testing.T) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "not json at all", nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	judged, err := AuditWithModel(auditSubject{Name: "x"})
	if err == nil {
		t.Fatal("expected AuditWithModel to fail when the provider returns unparseable JSON")
	}
	zero := types.JudgmentResult[auditSubject]{}
	if judged.Verdict != zero.Verdict || len(judged.Issues) != 0 {
		t.Fatalf("AuditWithModel returned a non-zero result on failure: %+v", judged)
	}
}

// TestAuditWithModelLLMCallFailure covers the callLLM error branch directly,
// distinct from the parse-failure branch above.
func TestAuditWithModelLLMCallFailure(t *testing.T) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "", context.DeadlineExceeded
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	_, err := AuditWithModel(auditSubject{Name: "x"})
	if err == nil {
		t.Fatal("expected AuditWithModel to fail when the LLM call itself errors")
	}
}

// --- audit.go: buildAuditSummary's severity buckets -----------------------

func TestBuildAuditSummaryBucketsBySeverity(t *testing.T) {
	findings := []AuditFinding{
		{Category: "a", Severity: 0.95}, // critical
		{Category: "b", Severity: 0.75}, // high
		{Category: "b", Severity: 0.55}, // medium
		{Category: "c", Severity: 0.35}, // low
		{Category: "c", Severity: 0.1},  // info
	}
	summary := buildAuditSummary(findings)

	if summary.TotalFindings != 5 {
		t.Fatalf("TotalFindings = %d, want 5", summary.TotalFindings)
	}
	wantSeverity := map[string]int{"critical": 1, "high": 1, "medium": 1, "low": 1, "info": 1}
	for level, want := range wantSeverity {
		if summary.BySeverity[level] != want {
			t.Errorf("BySeverity[%q] = %d, want %d (full map: %+v)", level, summary.BySeverity[level], want, summary.BySeverity)
		}
	}
	if !summary.Critical {
		t.Error("expected Critical=true with a 0.95 finding present")
	}
	if summary.PassesAudit {
		t.Error("expected PassesAudit=false with a critical finding present")
	}
	if summary.ByCategory["b"] != 2 {
		t.Errorf("ByCategory[b] = %d, want 2", summary.ByCategory["b"])
	}
}

// TestBuildAuditSummaryHighFindingFailsWithoutCritical proves a high-severity
// (but not critical) finding alone is enough to fail PassesAudit -- the two
// conditions are independent, not "only critical fails the audit".
func TestBuildAuditSummaryHighFindingFailsWithoutCritical(t *testing.T) {
	summary := buildAuditSummary([]AuditFinding{{Category: "x", Severity: 0.7}})
	if summary.Critical {
		t.Error("a 0.7 finding must not be marked Critical")
	}
	if summary.PassesAudit {
		t.Error("a high-severity finding must fail PassesAudit even without a critical one")
	}
}

// TestBuildAuditSummaryOnlyLowAndInfoPasses proves low/info findings alone
// do not fail the audit.
func TestBuildAuditSummaryOnlyLowAndInfoPasses(t *testing.T) {
	summary := buildAuditSummary([]AuditFinding{
		{Category: "x", Severity: 0.3},
		{Category: "x", Severity: 0.0},
	})
	if !summary.PassesAudit {
		t.Error("low/info findings alone should not fail PassesAudit")
	}
}

func TestBuildAuditSummaryEmptyFindingsPasses(t *testing.T) {
	summary := buildAuditSummary(nil)
	if summary.TotalFindings != 0 || !summary.PassesAudit || summary.Critical {
		t.Fatalf("unexpected summary for no findings: %+v", summary)
	}
}

// --- audit.go: mergeAuditOptions's field-by-field merge --------------------

func TestMergeAuditOptionsEachFieldOverridesItsDefault(t *testing.T) {
	defaults := AuditOptions{
		Threshold:    0.0,
		Deep:         true,
		Mode:         types.TransformMode,
		Intelligence: types.Smart,
	}
	ctx := context.WithValue(context.Background(), struct{}{}, "marker")
	user := AuditOptions{
		Policies:      []string{"p1"},
		Categories:    []string{"security"},
		Threshold:     0.5,
		Deep:          false,
		Steering:      "be thorough",
		Mode:          types.Strict,
		Intelligence:  types.Fast,
		Model:         "gpt-x",
		Context:       ctx,
		RequestID:     "req-1",
		CorrelationID: "corr-1",
	}

	merged := mergeAuditOptions(defaults, user)

	if len(merged.Policies) != 1 || merged.Policies[0] != "p1" {
		t.Errorf("Policies = %v, want [p1]", merged.Policies)
	}
	if len(merged.Categories) != 1 || merged.Categories[0] != "security" {
		t.Errorf("Categories = %v, want [security]", merged.Categories)
	}
	if merged.Threshold != 0.5 {
		t.Errorf("Threshold = %v, want 0.5", merged.Threshold)
	}
	if merged.Deep != false {
		t.Error("Deep should have been overridden to false (explicit boolean assignment, not conditional)")
	}
	if merged.Steering != "be thorough" {
		t.Errorf("Steering = %q, want %q", merged.Steering, "be thorough")
	}
	if merged.Mode != types.Strict {
		t.Errorf("Mode = %v, want Strict", merged.Mode)
	}
	if merged.Intelligence != types.Fast {
		t.Errorf("Intelligence = %v, want Fast", merged.Intelligence)
	}
	if merged.Model != "gpt-x" {
		t.Errorf("Model = %q, want gpt-x", merged.Model)
	}
	if merged.Context != ctx {
		t.Error("Context was not carried through the merge")
	}
}

// TestMergeAuditOptionsZeroUserLeavesDefaults proves a zero-value user
// AuditOptions (the "opts...AuditOptions" variadic omitted case never
// reaches this, but an explicitly empty AuditOptions{} does) does not
// clobber the defaults it should fall back to -- except Deep, which is an
// explicit unconditional assignment and therefore always takes the user's
// (zero) value; see the code comment above mergeAuditOptions' Deep line.
func TestMergeAuditOptionsZeroUserLeavesDefaults(t *testing.T) {
	defaults := AuditOptions{
		Threshold:    0.2,
		Deep:         true,
		Mode:         types.TransformMode,
		Intelligence: types.Smart,
		Steering:     "keep me",
		Model:        "keep-model",
	}
	merged := mergeAuditOptions(defaults, AuditOptions{})

	if merged.Threshold != 0.2 {
		t.Errorf("Threshold = %v, want the default 0.2 preserved", merged.Threshold)
	}
	if merged.Steering != "keep me" {
		t.Errorf("Steering = %q, want the default preserved", merged.Steering)
	}
	if merged.Model != "keep-model" {
		t.Errorf("Model = %q, want the default preserved", merged.Model)
	}
	if merged.Mode != types.TransformMode {
		t.Errorf("Mode = %v, want the default preserved", merged.Mode)
	}
	// Deep is the one field this function assigns unconditionally.
	if merged.Deep != false {
		t.Errorf("Deep = %v, want false (an empty user AuditOptions always overwrites Deep)", merged.Deep)
	}
}

// --- verify.go: the confidence floor is a real refusal, not a suggestion --

// TestVerifyRefusesBelowConfiguredConfidenceFloor proves MinConfidence is
// enforced against the model's own overall_confidence, not merely embedded
// in the prompt as a polite request: verifyMockResponse reports
// overall_confidence 0.6, and a MinConfidence above that must fail the call.
func TestVerifyRefusesBelowConfiguredConfidenceFloor(t *testing.T) {
	installVerifyResponse(t)

	opts := NewVerifyOptions().WithMinConfidence(0.9)
	_, err := Verify("some claims", opts)
	if err == nil {
		t.Fatal("expected Verify to refuse a result below the configured confidence floor")
	}
}

// TestVerifyWithModelRefusesBelowConfiguredConfidenceFloor is the same
// refusal, checked through the wrapper: it must not manufacture a
// JudgmentResult out of a verifyCore failure.
func TestVerifyWithModelRefusesBelowConfiguredConfidenceFloor(t *testing.T) {
	installVerifyResponse(t)

	opts := NewVerifyOptions().WithMinConfidence(0.9)
	judged, err := VerifyWithModel("some claims", opts)
	if err == nil {
		t.Fatal("expected VerifyWithModel to refuse a result below the configured confidence floor")
	}
	zero := types.JudgmentResult[any]{}
	if judged.Verdict != zero.Verdict || len(judged.Issues) != 0 {
		t.Fatalf("VerifyWithModel returned a non-zero result on failure: %+v", judged)
	}
}

// TestVerifyWithModelPropagatesLLMCallFailure covers the callLLM error
// branch of verifyCore through the wrapper.
func TestVerifyWithModelPropagatesLLMCallFailure(t *testing.T) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "", context.DeadlineExceeded
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	_, err := VerifyWithModel("claims", NewVerifyOptions().WithMinConfidence(0))
	if err == nil {
		t.Fatal("expected VerifyWithModel to fail when the LLM call errors")
	}
}

// TestVerifyPropagatesUnparseableResponse covers verifyCore's ParseJSONStrict
// failure branch.
func TestVerifyPropagatesUnparseableResponse(t *testing.T) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "not json", nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	_, err := Verify("claims", NewVerifyOptions().WithMinConfidence(0))
	if err == nil {
		t.Fatal("expected Verify to fail on an unparseable response")
	}
}
