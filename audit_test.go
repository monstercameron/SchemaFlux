package schemaflux_test

import (
	"context"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// auditRecord is a value to audit.
type auditRecord struct {
	Name     string `json:"name"`
	Password string `json:"password"`
}

var auditCustomer = auditRecord{Name: "Ada", Password: "hunter2"}

// auditFinding writes one finding of the answer.
func auditFinding(category string, severity float64, issue string) string {
	return `{"category":"` + category + `","severity":` + strconv.FormatFloat(severity, 'f', -1, 64) + `,"field":"password","issue":"` + issue + `","evidence":"e","recommendation":"fix"}`
}

// auditAnswer writes an answer to an audit with no policies.
func auditAnswer(findings ...string) string {
	return `{"findings":[` + strings.Join(findings, ",") + `]}`
}

// auditPolicyAnswer writes an answer to an audit of two policies: the findings
// that are no policy's, then the violations of each policy.
func auditPolicyAnswer(other []string, first, second []string) string {
	return `{"findings":[` + strings.Join(other, ",") + `],"policies":{"p1":[` + strings.Join(first, ",") + `],"p2":[` + strings.Join(second, ",") + `]}}`
}

var auditTwoPolicies = schemaflux.AuditOptions{Policies: []string{"no plain text passwords", "names must be present"}}

func TestAuditReturnsTheFindingsAsIssuesAndTheCountsOfThem(t *testing.T) {
	t.Parallel()
	model := answeringWith(auditAnswer(auditFinding("security", 0.8, "stored in plain text")))
	res, err := schemaflux.Audit(operationClient(model), auditCustomer)
	if err != nil {
		t.Fatal(err)
	}
	want := []schemaflux.JudgmentIssue{{Subject: "password", Category: "security", Severity: "error", Message: "stored in plain text", Suggestion: "fix", Evidence: []string{"e"}}}
	if !reflect.DeepEqual(res.Issues, want) {
		t.Errorf("issues = %+v", res.Issues)
	}
	if res.Verdict != schemaflux.VerdictFail || res.Subject != auditCustomer || res.ModelConfidence != 0 || res.Evidence != nil {
		t.Errorf("got %+v", res)
	}
	if !reflect.DeepEqual(res.BySeverity, map[string]int{"high": 1}) || !reflect.DeepEqual(res.ByCategory, map[string]int{"security": 1}) {
		t.Errorf("by severity %v by category %v", res.BySeverity, res.ByCategory)
	}
	if res.ByPolicy != nil || res.Other != nil || res.Critical || res.BelowThreshold != 0 || res.OtherHighestSeverity != 0 {
		t.Errorf("by policy %v other %v critical %v below %d other highest %v", res.ByPolicy, res.Other, res.Critical, res.BelowThreshold, res.OtherHighestSeverity)
	}
	if res.Summary != "1 finding(s) across 1 categor(ies), critical=false" {
		t.Errorf("summary = %q", res.Summary)
	}
	req := model.Last()
	wantText(t, req, `"password":"hunter2"`)
	if req.Schema == nil || req.SchemaName != "audit" {
		t.Errorf("schema %v named %q: the request has to declare one", req.Schema, req.SchemaName)
	}
}

func TestAuditOfNothingFoundPassesWithNoIssues(t *testing.T) {
	t.Parallel()
	res, err := schemaflux.Audit(operationClient(answeringWith(auditAnswer())), auditCustomer)
	if err != nil || res.Verdict != schemaflux.VerdictPass || res.Issues != nil || len(res.BySeverity) != 0 {
		t.Errorf("got %+v, %v", res, err)
	}
}

func TestAuditWorksTheVerdictOutFromTheSeverityOfWhatCounts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		severity float64
		want     schemaflux.Verdict
		critical bool
	}{
		{"info", 0.1, schemaflux.VerdictPass, false},
		{"just under high", 0.69, schemaflux.VerdictPass, false},
		{"high", 0.7, schemaflux.VerdictFail, false},
		{"just under critical", 0.89, schemaflux.VerdictFail, false},
		{"critical", 0.9, schemaflux.VerdictFail, true},
		{"the top of the scale", 1, schemaflux.VerdictFail, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, err := schemaflux.Audit(operationClient(answeringWith(auditAnswer(auditFinding("quality", tc.severity, "issue")))), auditCustomer)
			if err != nil || res.Verdict != tc.want || res.Critical != tc.critical {
				t.Errorf("verdict = %q critical %v, %v", res.Verdict, res.Critical, err)
			}
		})
	}
}

func TestAuditFailsOnAnyViolationOfASuppliedPolicyWhateverItsSeverity(t *testing.T) {
	t.Parallel()
	body := auditPolicyAnswer(nil, []string{auditFinding("security", 0.05, "plain text")}, nil)
	res, err := schemaflux.Audit(operationClient(answeringWith(body)), auditCustomer, auditTwoPolicies)
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != schemaflux.VerdictFail || len(res.Issues) != 1 || res.Issues[0].Rule != "no plain text passwords" || res.Issues[0].Severity != "info" {
		t.Errorf("got %+v", res)
	}
	if want := map[string]int{"no plain text passwords": 1, "names must be present": 0}; !reflect.DeepEqual(res.ByPolicy, want) {
		t.Errorf("by policy = %v, want %v: every supplied policy is there, at zero when nothing was found", res.ByPolicy, want)
	}
}

func TestAuditKeepsWhatIsNoPolicysApartAndOutOfTheVerdict(t *testing.T) {
	t.Parallel()
	body := auditPolicyAnswer([]string{auditFinding("quality", 0.95, "a different problem")}, nil, nil)
	res, err := schemaflux.Audit(operationClient(answeringWith(body)), auditCustomer, auditTwoPolicies)
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != schemaflux.VerdictPass || res.Issues != nil || len(res.Other) != 1 || res.Other[0].Category != "quality" || res.OtherHighestSeverity != 0.95 {
		t.Errorf("got %+v", res)
	}
	if res.Critical {
		t.Error("what does not count made the audit critical")
	}
}

func TestAuditCountsWhatIsNoPolicysWhenCategoriesAreNamed(t *testing.T) {
	t.Parallel()
	body := auditPolicyAnswer([]string{auditFinding("quality", 0.8, "a different problem")}, nil, nil)
	res, err := schemaflux.Audit(operationClient(answeringWith(body)), auditCustomer,
		schemaflux.AuditOptions{Policies: auditTwoPolicies.Policies, Categories: []string{"quality"}})
	if err != nil || res.Verdict != schemaflux.VerdictFail || len(res.Issues) != 1 || res.Other != nil {
		t.Errorf("got %+v, %v", res, err)
	}
}

func TestAuditThresholdDecidesWhatIsListedAndNeverTheVerdict(t *testing.T) {
	t.Parallel()
	body := auditAnswer(auditFinding("security", 0.95, "severe"), auditFinding("quality", 0.4, "minor"))
	cases := []struct {
		name      string
		threshold float64
		listed    int
		below     int
	}{
		{"zero lists everything", 0, 2, 0},
		{"a low threshold", 0.3, 2, 0},
		{"a threshold at a severity lists it", 0.4, 2, 0},
		{"a middle threshold", 0.5, 1, 1},
		{"a threshold above the severe one", 0.99, 0, 2},
		{"a threshold of one", 1, 0, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, err := schemaflux.Audit(operationClient(answeringWith(body)), auditCustomer, schemaflux.AuditOptions{Threshold: tc.threshold})
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Issues) != tc.listed || res.BelowThreshold != tc.below || res.Verdict != schemaflux.VerdictFail || !res.Critical {
				t.Errorf("listed %d below %d verdict %q critical %v", len(res.Issues), res.BelowThreshold, res.Verdict, res.Critical)
			}
		})
	}
}

func TestAuditWithNoOptionsInspectsNestedValuesAtTheSmartTier(t *testing.T) {
	t.Parallel()
	model := answeringWith(auditAnswer())
	if _, err := schemaflux.Audit(operationClient(model), auditCustomer); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	wantText(t, req, "Inspect nested structures as well as the top level.")
	wantText(t, req, `Categories for a finding: ["security","compliance","quality","consistency","completeness"]`)
	wantNoText(t, req, "Policies:")
	wantNoText(t, req, "Guidance:")
	if math.Abs(req.Temperature-0.3) > 1e-6 || req.MaxTokens != 4000 || req.Model != "test-model" {
		t.Errorf("temperature %v ceiling %d model %q", req.Temperature, req.MaxTokens, req.Model)
	}
}

func TestAuditEachOptionFieldChangesTheRequestAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  schemaflux.AuditOptions
		body  string
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"Policies", auditTwoPolicies, auditPolicyAnswer(nil, nil, nil), func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `p1: "no plain text passwords"`)
			wantText(t, req, `p2: "names must be present"`)
		}},
		{"Categories", schemaflux.AuditOptions{Categories: []string{"security", "privacy"}}, auditAnswer(), func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `Categories to check, and the only ones a finding may carry: ["security","privacy"]`)
		}},
		{"Shallow", schemaflux.AuditOptions{Shallow: true}, auditAnswer(), func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Inspect the top level only")
			wantNoText(t, req, "Inspect nested structures as well")
		}},
		{"Steering", schemaflux.AuditOptions{Options: schemaflux.Options{Steering: "mind GDPR"}}, auditAnswer(), func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `Guidance: "mind GDPR"`)
		}},
		{"Model", schemaflux.AuditOptions{Options: schemaflux.Options{Model: "pinned-model"}}, auditAnswer(), func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"ModeStrict", schemaflux.AuditOptions{Options: schemaflux.Options{Mode: schemaflux.ModeStrict}}, auditAnswer(), func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.1) > 1e-6 {
				t.Errorf("temperature = %v", req.Temperature)
			}
		}},
		{"TierFast", schemaflux.AuditOptions{Options: schemaflux.Options{Tier: schemaflux.TierFast}}, auditAnswer(), func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 2000 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"MaxOutputTokens", schemaflux.AuditOptions{Options: schemaflux.Options{MaxOutputTokens: 55}}, auditAnswer(), func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 55 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.AuditOptions{Options: schemaflux.Options{WebSearch: true}}, auditAnswer(), func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("no web search")
			}
		}},
		{"Effort", schemaflux.AuditOptions{Options: schemaflux.Options{Effort: schemaflux.EffortLow}}, auditAnswer(), func(t *testing.T, req schemaflux.Request) {
			if req.Effort != "low" {
				t.Errorf("effort = %q", req.Effort)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(tc.body)
			if _, err := schemaflux.Audit(operationClient(model), auditCustomer, tc.opts); err != nil {
				t.Fatal(err)
			}
			tc.check(t, model.Last())
		})
	}
}

func TestAuditRefusesABadOptionBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	cases := map[string]schemaflux.AuditOptions{
		"a negative threshold":          {Threshold: -0.1},
		"a threshold above one":         {Threshold: 1.1},
		"a threshold that is no number": {Threshold: math.NaN()},
		"a threshold of infinity":       {Threshold: math.Inf(1)},
		"a negative output ceiling":     {Options: schemaflux.Options{MaxOutputTokens: -1}},
		"a tier that is not one":        {Options: schemaflux.Options{Tier: 9}},
		"a mode that is not one":        {Options: schemaflux.Options{Mode: 9}},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(auditAnswer())
			_, err := schemaflux.Audit(operationClient(model), auditCustomer, opts)
			wantError(t, err, schemaflux.ErrConfiguration, "audit")
			if model.Calls() != 0 {
				t.Errorf("%d requests were sent", model.Calls())
			}
		})
	}
}

func TestAuditNamesTheFieldOfABadThreshold(t *testing.T) {
	t.Parallel()
	_, err := schemaflux.Audit(operationClient(answeringWith(auditAnswer())), auditCustomer, schemaflux.AuditOptions{Threshold: 2})
	if e := wantError(t, err, schemaflux.ErrConfiguration, "audit"); e.Message != "Threshold must be a number from 0 to 1" {
		t.Errorf("message = %q", e.Message)
	}
}

func TestAuditRefusesMorePoliciesThanOneAnswerCanDeclareBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	policies := make([]string, 300)
	for i := range policies {
		policies[i] = "policy " + strconv.Itoa(i)
	}
	model := answeringWith(auditAnswer())
	_, err := schemaflux.Audit(operationClient(model), auditCustomer, schemaflux.AuditOptions{Policies: policies})
	wantError(t, err, schemaflux.ErrConfiguration, "audit")
	if model.Calls() != 0 {
		t.Errorf("%d requests were sent", model.Calls())
	}
}

func TestAuditRefusesAnAnswerThatDoesNotReportOnEveryPolicyOrBreaksItsRules(t *testing.T) {
	t.Parallel()
	good := auditFinding("security", 0.5, "issue")
	cases := []struct {
		name string
		body string
		opts schemaflux.AuditOptions
	}{
		{"no findings list", `{}`, schemaflux.AuditOptions{}},
		{"a null findings list", `{"findings":null}`, schemaflux.AuditOptions{}},
		{"policies when none were supplied", auditPolicyAnswer(nil, []string{good}, nil), schemaflux.AuditOptions{}},
		{"no report on the policies", auditAnswer(), auditTwoPolicies},
		{"a report on one policy of two", `{"findings":[],"policies":{"p1":[]}}`, auditTwoPolicies},
		{"a null report on a policy", `{"findings":[],"policies":{"p1":null,"p2":[]}}`, auditTwoPolicies},
		{"a category that was not asked", auditAnswer(auditFinding("privacy", 0.5, "issue")), schemaflux.AuditOptions{Categories: []string{"security"}}},
		{"no severity", auditAnswer(`{"category":"security","issue":"issue"}`), schemaflux.AuditOptions{}},
		{"a severity off the scale", auditAnswer(auditFinding("security", 1.5, "issue")), schemaflux.AuditOptions{}},
		{"a finding that says nothing", auditAnswer(auditFinding("security", 0.5, " ")), schemaflux.AuditOptions{}},
		{"text", `The record is fine.`, schemaflux.AuditOptions{}},
		{"a bare list", `[]`, schemaflux.AuditOptions{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, err := schemaflux.Audit(operationClient(answeringWith(tc.body)), auditCustomer, tc.opts)
			wantError(t, err, schemaflux.ErrInvalidAnswer, "audit")
			if res.Verdict != "" || res.Issues != nil || res.BySeverity != nil || res.Subject != (auditRecord{}) {
				t.Errorf("a refused answer left %+v beside the error", res)
			}
		})
	}
}

func TestAuditWithANilContextIsAnInvalidRequest(t *testing.T) {
	t.Parallel()
	var nilContext context.Context
	_, err := schemaflux.Audit(nilContext, auditCustomer)
	if e := wantError(t, err, schemaflux.ErrInvalidRequest, "audit"); e.Message != "the context is nil" {
		t.Errorf("message = %q", e.Message)
	}
}

func TestAuditMergesSeveralOptionsValuesFromLeftToRight(t *testing.T) {
	t.Parallel()
	t.Run("a later value wins and a later zero leaves the earlier", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(auditAnswer(auditFinding("security", 0.6, "issue")))
		res, err := schemaflux.Audit(operationClient(model), auditCustomer,
			schemaflux.AuditOptions{Threshold: 0.3, Categories: []string{"security"}, Options: schemaflux.Options{Model: "first-model"}},
			schemaflux.AuditOptions{Options: schemaflux.Options{Steering: "later"}},
			schemaflux.AuditOptions{Threshold: 0.9})
		if err != nil {
			t.Fatal(err)
		}
		req := model.Last()
		wantText(t, req, `["security"]`)
		wantText(t, req, "later")
		if req.Model != "first-model" || res.BelowThreshold != 1 || len(res.Issues) != 0 {
			t.Errorf("model %q below %d issues %d", req.Model, res.BelowThreshold, len(res.Issues))
		}
	})
	t.Run("a bool turned on stays on", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(auditAnswer())
		_, err := schemaflux.Audit(operationClient(model), auditCustomer, schemaflux.AuditOptions{Shallow: true}, schemaflux.AuditOptions{})
		if err != nil {
			t.Fatal(err)
		}
		wantText(t, model.Last(), "Inspect the top level only")
	})
	t.Run("a refused setting in a later value is refused", func(t *testing.T) {
		t.Parallel()
		_, err := schemaflux.Audit(operationClient(answeringWith(auditAnswer())), auditCustomer, schemaflux.AuditOptions{}, schemaflux.AuditOptions{Threshold: 5})
		wantError(t, err, schemaflux.ErrConfiguration, "audit")
	})
}

func TestAuditReturnsAnErrorTheCallerCanClassifyWhenTheProviderFails(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  *schemaflux.Error
	}{
		{"unavailable", &schemaflux.Error{Kind: schemaflux.ErrUnavailable}},
		{"rate limited", &schemaflux.Error{Kind: schemaflux.ErrRateLimited, Status: 429}},
		{"unauthorized", &schemaflux.Error{Kind: schemaflux.ErrUnauthorized, Status: 401}},
		{"timed out", &schemaflux.Error{Kind: schemaflux.ErrTimeout}},
		{"refused", &schemaflux.Error{Kind: schemaflux.ErrRefused}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var model schemafluxtest.Provider
			model.Fail(tc.err)
			res, err := schemaflux.Audit(operationClient(&model), auditCustomer)
			wantError(t, err, tc.err.Kind, "audit")
			if res.Verdict != "" || res.Subject != (auditRecord{}) {
				t.Errorf("a failed call left %+v beside the error", res)
			}
		})
	}
}

func TestAuditOfAValueJSONCannotWriteIsAnInvalidRequest(t *testing.T) {
	t.Parallel()
	model := answeringWith(auditAnswer())
	_, err := schemaflux.Audit(operationClient(model), map[string]any{"c": make(chan int)})
	wantError(t, err, schemaflux.ErrInvalidRequest, "audit")
	if model.Calls() != 0 {
		t.Errorf("%d requests were sent", model.Calls())
	}
}
