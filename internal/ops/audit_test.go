package ops

import (
	"strings"
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

// A policy is reported in the words the caller supplied it in, trailing space
// included: Policy and the keys of ByPolicy are documented that way, and a
// lookup by the caller's own string missed when the library had trimmed it. The
// prompt still carries it trimmed.
func TestAuditReportsPoliciesInTheCallersOwnWords(t *testing.T) {
	policy := "PII must not be stored \t"
	response := `{"policies":{"p1":[` + lx029Finding("compliance", "0.8", "email") + `]},"findings":[]}`

	result, err, sent := auditWith(t, response, AuditOptions{Policies: []string{policy}})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if len(result.Findings) != 1 || result.Findings[0].Policy != policy {
		t.Errorf("Findings = %+v, want the finding's Policy to be %q exactly", result.Findings, policy)
	}
	if count, ok := result.Summary.ByPolicy[policy]; !ok || count != 1 {
		t.Errorf("ByPolicy = %v, want %q found with a count of 1", result.Summary.ByPolicy, policy)
	}
	if !strings.Contains(sent.user, `"PII must not be stored"`) {
		t.Errorf("the prompt does not carry the policy trimmed and quoted:\n%s", sent.user)
	}
}

func TestAuditTreatsPoliciesThatDifferOnlyInSpaceAsOne(t *testing.T) {
	response := `{"policies":{"p1":[]},"findings":[]}`
	result, err, _ := auditWith(t, response, AuditOptions{Policies: []string{"no secrets", "  no secrets", "", "   "}})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if len(result.Summary.ByPolicy) != 1 {
		t.Errorf("ByPolicy = %v, want the one policy once", result.Summary.ByPolicy)
	}
	if _, ok := result.Summary.ByPolicy["no secrets"]; !ok {
		t.Errorf("ByPolicy = %v, want the first spelling kept", result.Summary.ByPolicy)
	}
}

func TestAuditRefusesASecondOptionsValue(t *testing.T) {
	seen := answerAnalysis(t, `{"findings":[]}`)
	result, err := Audit(lx029Subject, AuditOptions{}, AuditOptions{Threshold: 0.9})
	if err == nil {
		t.Fatalf("a second options value was ignored: %+v", result)
	}
	if types.KindOf(err) != types.KindConfiguration || seen.calls != 0 {
		t.Errorf("kind %v, %d calls: %v", types.KindOf(err), seen.calls, err)
	}
}

const auditCompleteFinding = `{"category":"security","severity":0.5,"field":"f","issue":"i","evidence":"e","recommendation":"r"}`

// A verdict that rests on an empty list of findings is a pass, so a list that is
// not in the answer, or a finding that leaves out its severity, its category or
// what is wrong, is refused with nothing beside the error and is never an audit
// with nothing in it.
func TestAuditRefusesAnAnswerMissingAnyMemberItAsksFor(t *testing.T) {
	t.Run("with no policies", func(t *testing.T) {
		complete := `{"findings":[` + auditCompleteFinding + `]}`
		members := [][]any{
			{"findings"},
			{"findings", 0, "category"},
			{"findings", 0, "severity"},
			{"findings", 0, "issue"},
		}
		refuseEachMissingMemberInOwnWords(t, complete, members, func(response string) (any, error) {
			answerAnalysis(t, response)
			return Audit(coreTarget{Name: "x"})
		})
	})

	t.Run("with a policy", func(t *testing.T) {
		complete := `{"policies":{"p1":[` + auditCompleteFinding + `]},"findings":[` + auditCompleteFinding + `]}`
		members := [][]any{
			{"policies"},
			{"policies", "p1"},
			{"policies", "p1", 0, "category"},
			{"policies", "p1", 0, "severity"},
			{"policies", "p1", 0, "issue"},
			{"findings"},
			{"findings", 0, "category"},
			{"findings", 0, "severity"},
			{"findings", 0, "issue"},
		}
		refuseEachMissingMemberInOwnWords(t, complete, members, func(response string) (any, error) {
			answerAnalysis(t, response)
			return Audit(coreTarget{Name: "x"}, AuditOptions{Policies: []string{"no secrets"}})
		})
	})
}

func TestAuditRefusesANullWhereNothingCanBeNull(t *testing.T) {
	for name, body := range map[string]string{
		"findings":                  strings.Replace(`{"findings":[`+auditCompleteFinding+`]}`, `[`+auditCompleteFinding+`]`, `null`, 1),
		"the severity of a finding": strings.Replace(`{"findings":[`+auditCompleteFinding+`]}`, `"severity":0.5`, `"severity":null`, 1),
		"the issue of a finding":    strings.Replace(`{"findings":[`+auditCompleteFinding+`]}`, `"issue":"i"`, `"issue":null`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			answerAnalysis(t, body)
			result, err := Audit(coreTarget{Name: "x"})
			wantKind(t, err, types.KindSchemaViolation)
			if result.Summary.PassesAudit || result.Findings != nil || result.Original != (coreTarget{}) {
				t.Errorf("result = %+v beside the error", result)
			}
		})
	}
}

// The field, the evidence and the recommendation of a finding are not in the
// schema's list of required members, so a finding without them is complete.
func TestAuditReadsAFindingWithOnlyWhatTheSchemaRequires(t *testing.T) {
	answerAnalysis(t, `{"findings":[{"category":"security","severity":0.5,"issue":"i"}]}`)
	result, err := Audit(coreTarget{Name: "x"})
	if err != nil || len(result.Findings) != 1 || result.Findings[0].Issue != "i" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
}
