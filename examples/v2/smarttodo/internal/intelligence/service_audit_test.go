package intelligence

import (
	"errors"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/examples/v2/smarttodo/internal/models"
	"github.com/monstercameron/schemaflux/schemafluxtest"
	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// What the board review says about risks is read from the audit's verdict and
// not only from the findings it lists. The list holds what reached the
// threshold, and an audit fails on a violation of a policy at any severity:
// read from the list alone, a board that failed its audit on a finding under
// the threshold was told "No critical structural issues detected."
func TestAuditRisks(t *testing.T) {
	finding := func(issue string) schemaflux.AuditFinding { return schemaflux.AuditFinding{Issue: issue} }
	result := func(passes bool, below int, findings ...schemaflux.AuditFinding) schemaflux.AuditResult[BoardSnapshot] {
		audit := schemaflux.AuditResult[BoardSnapshot]{Findings: findings}
		audit.Summary.PassesAudit = passes
		audit.Summary.BelowThreshold = below
		audit.Summary.TotalFindings = len(findings)
		return audit
	}
	const clean = "No critical structural issues detected."

	cases := []struct {
		name   string
		result schemaflux.AuditResult[BoardSnapshot]
		err    error
		want   []string
		absent string
	}{
		{"the audit failed to run", result(false, 0), errors.New("the provider is down"), nil, clean},
		{"passes, nothing found", result(true, 0), nil, []string{clean}, ""},
		{"passes, with a finding listed", result(true, 0, finding("two tasks have no owner")), nil, []string{"two tasks have no owner"}, clean},
		{"fails, with a finding listed", result(false, 0, finding("nine tasks are overdue")), nil, []string{"nine tasks are overdue"}, clean},
		{"fails on one finding under the threshold", result(false, 1), nil, []string{"does not pass its audit: 1 lesser finding"}, clean},
		{"fails on three findings under the threshold", result(false, 3), nil, []string{"does not pass its audit: 3 lesser finding"}, clean},
		{"fails, one listed and one under the threshold", result(false, 1, finding("nine tasks are overdue")), nil,
			[]string{"nine tasks are overdue", "does not pass its audit: 1 lesser finding"}, clean},
		{"passes, with a finding under the threshold", result(true, 2), nil, []string{clean}, "does not pass"},
		{"two findings listed", result(false, 0, finding("first"), finding("second")), nil, []string{"first", "second"}, clean},
		{"a long finding is cut", result(false, 0, finding(strings.Repeat("long ", 60))), nil, []string{"long long"}, clean},
		{"a finding with space around it", result(false, 0, finding("  padded  ")), nil, []string{"padded"}, clean},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			risks := auditRisks(tc.result, tc.err)
			joined := strings.Join(risks, "\n")
			if tc.want == nil && risks != nil {
				t.Fatalf("risks = %v for an audit that did not run", risks)
			}
			for _, want := range tc.want {
				if !strings.Contains(joined, want) {
					t.Errorf("the risks do not say %q: %v", want, risks)
				}
			}
			if tc.absent != "" && strings.Contains(joined, tc.absent) {
				t.Errorf("the risks say %q: %v", tc.absent, risks)
			}
			for _, risk := range risks {
				if len(risk) > 130 {
					t.Errorf("a risk is %d characters long", len(risk))
				}
			}
		})
	}
}

// The board review hands the audit to Synthesize with the other findings. An
// audit that failed has found nothing, and the empty result it leaves behind
// was handed over as though it were a finding of nothing wrong: the model
// could write a clean bill of health from it, and the synthesized risks won
// unless empty. A review built from a failed audit has to say the audit could
// not be run, whatever the synthesis wrote.
func TestBuildReviewSaysWhenTheAuditCouldNotBeRun(t *testing.T) {
	const synthesisMarker = "synthesizing information from multiple sources"
	todos := []*models.SmartTodo{
		{ID: "1", Title: "File the report", Priority: "high"},
		{ID: "2", Title: "Book the venue", Priority: "low"},
	}
	synthesized := func(risks string) string {
		return `{"synthesized":{"summary":"s","focus_areas":["a"],"quick_wins":["b"],"biggest_blocker":"c","risks":` + risks +
			`,"recommended_moves":["d"],"forecast":{"outlook":"o"}},"facts":[],"insights":[],"conflicts":[],"source_coverage":{}}`
	}

	cases := []struct {
		name string
		// synthesis is what the scripted model answers to Synthesize; an empty
		// string makes that call fail as well.
		synthesis string
	}{
		{"the synthesis writes a clean bill of health", synthesized(`["No critical structural issues detected."]`)},
		{"the synthesis writes its own risks", synthesized(`["Nothing is overdue.","The board is healthy."]`)},
		{"the synthesis writes no risks", synthesized(`[]`)},
		{"the synthesis writes no risks field value", synthesized(`null`)},
		{"the synthesis fails too", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := schemafluxtest.New().ReplyFunc(func(_ int, req schemaflux.CompletionRequest) (string, error) {
				if tc.synthesis != "" && strings.Contains(req.SystemPrompt, synthesisMarker) {
					return tc.synthesis, nil
				}
				// The audit, and everything else the review asks for, fails.
				return "", errors.New("the provider is down")
			})
			schemafluxtest.Install(t, provider)

			review, err := NewService().BuildReview(todos)
			if err != nil {
				t.Fatalf("BuildReview: %v", err)
			}
			if len(review.Risks) != 1 || review.Risks[0] != auditUnavailableRisk {
				t.Errorf("Risks = %q, want only %q", review.Risks, auditUnavailableRisk)
			}

			synthesisRequests := 0
			for _, req := range provider.Requests() {
				if !strings.Contains(req.SystemPrompt, synthesisMarker) {
					continue
				}
				synthesisRequests++
				// What the synthesis is shown must not hold the empty audit
				// result a failed audit leaves behind.
				if strings.Contains(req.UserPrompt, "passes_audit") || strings.Contains(req.UserPrompt, "total_findings") {
					t.Errorf("the synthesis was handed the result of an audit that did not run:\n%s", req.UserPrompt)
				}
				if !strings.Contains(req.UserPrompt, auditUnavailableRisk) {
					t.Errorf("the synthesis was not told the audit could not be run:\n%s", req.UserPrompt)
				}
			}
			// Without this the loop above would pass for a review that never
			// reached the synthesis at all.
			if synthesisRequests == 0 {
				t.Error("the review never asked for a synthesis, so the path under test did not run")
			}
		})
	}
}

func TestAuditSourceForSynthesis(t *testing.T) {
	passing := schemaflux.AuditResult[BoardSnapshot]{}
	passing.Summary.PassesAudit = true

	cases := []struct {
		name   string
		result schemaflux.AuditResult[BoardSnapshot]
		err    error
		failed bool
	}{
		{"an audit that ran and passed", passing, nil, false},
		{"an audit that ran and failed its policies", schemaflux.AuditResult[BoardSnapshot]{}, nil, false},
		{"an audit that could not run", schemaflux.AuditResult[BoardSnapshot]{}, errors.New("the provider is down"), true},
		{"an audit that could not run and left a result", passing, errors.New("cancelled"), true},
		{"an audit that timed out", schemaflux.AuditResult[BoardSnapshot]{}, errors.New("context deadline exceeded"), true},
		{"an audit refused as malformed", schemaflux.AuditResult[BoardSnapshot]{}, errors.New("audit: malformed output"), true},
		{"an audit with findings", schemaflux.AuditResult[BoardSnapshot]{Findings: []schemaflux.AuditFinding{{Issue: "x"}}}, nil, false},
		{"an audit with other findings only", schemaflux.AuditResult[BoardSnapshot]{Other: []schemaflux.AuditFinding{{Issue: "x"}}}, nil, false},
		{"a failed audit with findings in its result", schemaflux.AuditResult[BoardSnapshot]{Findings: []schemaflux.AuditFinding{{Issue: "x"}}}, errors.New("late"), true},
		{"a failed audit with a different error", schemaflux.AuditResult[BoardSnapshot]{}, errors.New("budget exhausted"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := auditSourceForSynthesis(tc.result, tc.err)
			text, isText := got.(string)
			if tc.failed {
				if !isText || text != auditUnavailableRisk {
					t.Fatalf("got %#v, want the statement that the audit could not be run", got)
				}
				return
			}
			if isText {
				t.Fatalf("got the text %q for an audit that ran", text)
			}
			if _, ok := got.(schemaflux.AuditResult[BoardSnapshot]); !ok {
				t.Fatalf("got %T, want the audit result itself", got)
			}
		})
	}
}

// extractAuditRisks reads an audit that ran, so it is tested on its own and not
// only through auditRisks.
func TestExtractAuditRisks(t *testing.T) {
	finding := func(issue string) schemaflux.AuditFinding { return schemaflux.AuditFinding{Issue: issue} }
	audit := func(passes bool, below int, findings ...schemaflux.AuditFinding) schemaflux.AuditResult[BoardSnapshot] {
		result := schemaflux.AuditResult[BoardSnapshot]{Findings: findings}
		result.Summary.PassesAudit = passes
		result.Summary.BelowThreshold = below
		return result
	}
	const clean = "No critical structural issues detected."

	cases := []struct {
		name   string
		result schemaflux.AuditResult[BoardSnapshot]
		want   []string
	}{
		{"a zero result", schemaflux.AuditResult[BoardSnapshot]{}, []string{clean}},
		{"passes and lists nothing", audit(true, 0), []string{clean}},
		{"fails and lists nothing and has nothing under the threshold", audit(false, 0), []string{clean}},
		{"passes with findings under the threshold", audit(true, 4), []string{clean}},
		{"one finding", audit(false, 0, finding("a")), []string{"a"}},
		{"two findings keep their order", audit(false, 0, finding("b"), finding("a")), []string{"b", "a"}},
		{"fails on one under the threshold", audit(false, 1), []string{"The board does not pass its audit: 1 lesser finding(s) are not listed here."}},
		{"fails on seven under the threshold", audit(false, 7), []string{"The board does not pass its audit: 7 lesser finding(s) are not listed here."}},
		{"a listed finding then the count under the threshold", audit(false, 2, finding("a")),
			[]string{"a", "The board does not pass its audit: 2 lesser finding(s) are not listed here."}},
		{"a passing audit lists its findings and no count", audit(true, 3, finding("a")), []string{"a"}},
		{"a finding is trimmed", audit(false, 0, finding("  a  ")), []string{"a"}},
		{"a finding is cut at 120 characters", audit(false, 0, finding(strings.Repeat("x", 300))), nil},
		{"a blank finding is listed as blank", audit(false, 0, finding("   ")), []string{""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractAuditRisks(tc.result)
			if tc.want == nil {
				if len(got) != 1 || len(got[0]) > 130 || !strings.HasPrefix(got[0], "xxx") {
					t.Fatalf("got %q, want one finding cut short", got)
				}
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("risk %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
			for _, risk := range got {
				if strings.Contains(risk, "%d") {
					t.Errorf("a risk was left unformatted: %q", risk)
				}
			}
		})
	}
}
