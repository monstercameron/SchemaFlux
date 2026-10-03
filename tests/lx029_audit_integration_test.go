package tests

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/testfixtures"
)

type lx029Customer struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

const (
	lx029Plain  = "Passwords must not be stored in plain text"
	lx029Domain = "Email addresses must be on the company domain"
)

func lx029Found(category, severity, field string) string {
	return `{"category":"` + category + `","severity":` + severity + `,"field":"` + field + `","issue":"a problem","evidence":"what shows it","recommendation":"fix it"}`
}

// LX-029 through the exported API and the real OpenAI provider. A finding
// counts toward the verdict when it is about something the caller asked to have
// checked, the threshold does not change the verdict, and an answer that does
// not report on what was asked is an error.
func TestIntegrationAuditCountsWhatWasAskedAbout(t *testing.T) {
	subject := lx029Customer{ID: "1", Email: "sam@example.com", Password: "hunter2"}
	both := []string{lx029Plain, lx029Domain}
	keyed := func(a, b, findings string) string {
		return `{"policies":{"p1":[` + a + `],"p2":[` + b + `]},"findings":[` + findings + `]}`
	}

	cases := []struct {
		name    string
		opts    schemaflux.AuditOptions
		body    string
		wantErr error
		verdict schemaflux.Verdict
		issues  int
		rule    string // the rule of the first issue
		other   int
	}{
		{"a policy violated", schemaflux.AuditOptions{Policies: both},
			keyed(lx029Found("security", "0.95", "password"), "", ""), nil, schemaflux.VerdictFail, 1, lx029Plain, 0},
		{"the second policy violated", schemaflux.AuditOptions{Policies: both},
			keyed("", lx029Found("compliance", "0.8", "email"), ""), nil, schemaflux.VerdictFail, 1, lx029Domain, 0},
		{"nothing violated", schemaflux.AuditOptions{Policies: both},
			keyed("", "", ""), nil, schemaflux.VerdictPass, 0, "", 0},
		{"a severe problem outside the policies does not fail them", schemaflux.AuditOptions{Policies: both},
			keyed("", "", lx029Found("quality", "0.95", "id")), nil, schemaflux.VerdictPass, 0, "", 1},
		{"the same problem in a category that was asked for", schemaflux.AuditOptions{Policies: both, Categories: []string{"quality"}},
			keyed("", "", lx029Found("quality", "0.95", "id")), nil, schemaflux.VerdictFail, 1, "", 0},
		{"a minor violation still fails: the policy is the caller's rule", schemaflux.AuditOptions{Policies: both},
			keyed(lx029Found("security", "0.3", "password"), "", ""), nil, schemaflux.VerdictFail, 1, lx029Plain, 0},
		{"a violation the threshold hides still fails", schemaflux.AuditOptions{Policies: both, Threshold: 0.9},
			keyed(lx029Found("security", "0.75", "password"), "", ""), nil, schemaflux.VerdictFail, 0, "", 0},
		{"no policies and no categories: everything found counts", schemaflux.AuditOptions{},
			`{"findings":[` + lx029Found("security", "0.95", "password") + `]}`, nil, schemaflux.VerdictFail, 1, "", 0},
		{"one policy not reported on", schemaflux.AuditOptions{Policies: both},
			`{"policies":{"p1":[]},"findings":[]}`, schemaflux.ErrSchemaViolation, "", 0, "", 0},
		{"an empty answer", schemaflux.AuditOptions{Policies: both},
			`{}`, schemaflux.ErrSchemaViolation, "", 0, "", 0},
		{"no list of findings", schemaflux.AuditOptions{},
			`{"findings":null}`, schemaflux.ErrSchemaViolation, "", 0, "", 0},
		{"a severity off the scale", schemaflux.AuditOptions{},
			`{"findings":[` + lx029Found("security", "4", "password") + `]}`, schemaflux.ErrInvariantViolation, "", 0, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, captured := capturingResponsesAPI(t, testfixtures.ResponsesMessage(tc.body))

			result, err := schemaflux.AuditWithModel(subject, tc.opts)
			if len(*captured) != 1 {
				t.Fatalf("%d requests reached the provider, want 1", len(*captured))
			}

			// What went out: a strict schema, the fixed instructions, and the
			// caller's policies in the message and nowhere else.
			body := (*captured)[0]
			text, _ := body["text"].(map[string]any)
			format, _ := text["format"].(map[string]any)
			if format["type"] != "json_schema" || format["strict"] != true || format["name"] != "audit" {
				t.Fatalf("expected the strict audit schema on the wire, got %v", format)
			}
			schema, _ := format["schema"].(map[string]any)
			if problem := llm.StrictSchemaProblem(schema); problem != "" {
				t.Fatalf("the schema on the wire breaks a strict-mode rule: %s", problem)
			}
			instructions, _ := body["instructions"].(string)
			declared, _ := json.Marshal(schema)
			for _, policy := range tc.opts.Policies {
				if strings.Contains(instructions, policy) {
					t.Errorf("the instructions carry the caller's policy %q", policy)
				}
				// Nor the schema: the policy's text was each key's description.
				if strings.Contains(string(declared), policy) {
					t.Errorf("the schema carries the caller's policy %q", policy)
				}
			}
			if strings.Contains(instructions, "hunter2") {
				t.Error("the instructions carry the caller's data")
			}

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				if result.Verdict != "" || len(result.Issues) != 0 || result.Summary != "" {
					t.Fatalf("a failed audit returned a usable result: %+v", result)
				}
				return
			}
			if err != nil {
				t.Fatalf("AuditWithModel: %v", err)
			}
			if result.Verdict != tc.verdict {
				t.Errorf("Verdict = %v, want %v", result.Verdict, tc.verdict)
			}
			if len(result.Issues) != tc.issues {
				t.Fatalf("Issues = %+v, want %d", result.Issues, tc.issues)
			}
			if tc.issues > 0 && result.Issues[0].Rule != tc.rule {
				t.Errorf("the first issue's rule is %q, want %q", result.Issues[0].Rule, tc.rule)
			}
			other, _ := result.ModelClaims["other_findings"].([]schemaflux.JudgmentIssue)
			if len(other) != tc.other {
				t.Errorf("%d findings reported apart, want %d", len(other), tc.other)
			}
		})
	}
}

// ExampleAuditWithModel audits a record against two policies. The model found
// one violation, and also remarked on something neither policy covers; the
// remark is reported apart and the verdict is about the policies. It runs under
// go test with a scripted provider: no credential, no spend.
func ExampleAuditWithModel() {
	schemaflux.NewClient("example-key").WithProviderInstance(testfixtures.NewScripted(
		`{"policies": {
		    "p1": [{"category": "security", "severity": 0.95, "field": "password", "issue": "The password is stored as readable text.", "evidence": "a plain string", "recommendation": "Store a salted hash."}],
		    "p2": []},
		  "findings": [{"category": "quality", "severity": 0.9, "field": "id", "issue": "The id is not a UUID.", "evidence": null, "recommendation": null}]}`))

	type customer struct {
		ID       string `json:"id"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	result, err := schemaflux.AuditWithModel(
		customer{ID: "1", Email: "sam@example.com", Password: "hunter2"},
		schemaflux.AuditOptions{Policies: []string{
			"Passwords must not be stored in plain text",
			"Email addresses must be on the company domain",
		}})
	if err != nil {
		fmt.Println("the audit could not be run:", err)
		return
	}

	fmt.Println("verdict:", result.Verdict)
	for _, issue := range result.Issues {
		fmt.Printf("%s (%s) breaks %q: %s\n", issue.Subject, issue.Severity, issue.Rule, issue.Message)
	}
	other := result.ModelClaims["other_findings"].([]schemaflux.JudgmentIssue)
	fmt.Println("also noted, and not counted:", len(other))

	// Output:
	// verdict: fail
	// password (critical) breaks "Passwords must not be stored in plain text": The password is stored as readable text.
	// also noted, and not counted: 1
}
