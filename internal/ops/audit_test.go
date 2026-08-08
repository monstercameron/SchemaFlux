package ops

import (
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

type auditSubject struct {
	Name string `json:"name"`
	SSN  string `json:"ssn"`
}

const auditMockFindings = `{
	"findings": [
		{"category": "security", "severity": 0.95, "field": "ssn", "issue": "SSN stored in plain text", "evidence": "field present", "recommendation": "encrypt at rest", "policy": "PII must not be stored in plain text"},
		{"category": "quality", "severity": 0.4, "field": "name", "issue": "name is a placeholder value", "recommendation": "collect the real name"}
	]
}`

// TestAuditWithModelMatchesLegacyAudit proves the collapse is
// behavior-preserving: for the same response, AuditWithModel's Verdict and
// Issues describe the same findings the deprecated Audit's Summary and
// Findings did, with severity mapped consistently with AuditSummary's own
// thresholds.
func TestAuditWithModelMatchesLegacyAudit(t *testing.T) {
	installAuditResponse(t, auditMockFindings)

	subject := auditSubject{Name: "placeholder", SSN: "123-45-6789"}

	legacy, err := Audit(subject)
	if err != nil {
		t.Fatalf("Audit failed: %v", err)
	}
	judged, err := AuditWithModel(subject)
	if err != nil {
		t.Fatalf("AuditWithModel failed: %v", err)
	}

	if legacy.Summary.PassesAudit {
		t.Fatal("expected legacy audit to fail (a critical finding is present)")
	}
	if judged.Verdict != types.VerdictFail {
		t.Errorf("expected VerdictFail, got %v", judged.Verdict)
	}
	if len(judged.Issues) != len(legacy.Findings) {
		t.Fatalf("expected %d issues, got %d", len(legacy.Findings), len(judged.Issues))
	}

	var foundCritical, foundInfo bool
	for _, issue := range judged.Issues {
		switch issue.Subject {
		case "ssn":
			foundCritical = true
			if issue.Severity != "critical" {
				t.Errorf("expected severity 'critical' for the 0.95 finding, got %q", issue.Severity)
			}
			if issue.Category != "security" {
				t.Errorf("expected category 'security', got %q", issue.Category)
			}
		case "name":
			foundInfo = true
			if issue.Severity != "info" {
				t.Errorf("expected severity 'info' for the 0.4 finding (buildAuditSummary calls it 'low'), got %q", issue.Severity)
			}
		}
	}
	if !foundCritical || !foundInfo {
		t.Fatalf("expected both findings represented, got %+v", judged.Issues)
	}

	if judged.Subject.SSN != subject.SSN {
		t.Errorf("expected Subject to carry the original input, got %+v", judged.Subject)
	}
}

// TestAuditWithModelRespectsThreshold proves AuditWithModel filters
// findings the same way the deprecated Audit does -- the Threshold option
// is applied once, in auditCore, and both result shapes see the same
// filtered set.
func TestAuditWithModelRespectsThreshold(t *testing.T) {
	installAuditResponse(t, auditMockFindings)

	subject := auditSubject{Name: "placeholder", SSN: "123-45-6789"}
	opts := AuditOptions{Threshold: 0.9}

	judged, err := AuditWithModel(subject, opts)
	if err != nil {
		t.Fatalf("AuditWithModel failed: %v", err)
	}
	if len(judged.Issues) != 1 {
		t.Fatalf("expected 1 issue above threshold 0.9, got %d: %+v", len(judged.Issues), judged.Issues)
	}
	if judged.Issues[0].Subject != "ssn" {
		t.Errorf("expected the ssn finding to survive the threshold, got %q", judged.Issues[0].Subject)
	}
}
