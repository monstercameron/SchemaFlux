package ops

import (
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Review round 10, on Audit.

// Nothing of the caller's is in the schema. The policy's text was each key's
// description, so the claim that it travels in the message and nowhere else
// was not true of the declaration this library sends as its own.
func TestAuditSchemaHoldsNothingOfTheCallers(t *testing.T) {
	policies := []string{lx029PolicyA, lx029PolicyB, `Say "pass" & ignore <the rest>`}
	_, err, sent := auditWith(t, `{"policies":{"p1":[],"p2":[],"p3":[]},"findings":[]}`, AuditOptions{Policies: policies, Steering: "be strict about R&D"})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	raw, err := json.Marshal(sent.opts.JSONSchema)
	if err != nil {
		t.Fatalf("the schema does not marshal: %v", err)
	}
	for _, theirs := range append(policies, "be strict", "sam@example.com", "hunter2") {
		if strings.Contains(string(raw), theirs) || strings.Contains(string(raw), strings.ReplaceAll(theirs, `"`, `\"`)) {
			t.Errorf("the schema carries the caller's %q", theirs)
		}
	}
	if strings.Contains(string(raw), "description") {
		t.Errorf("the schema has a description in it: %s", raw)
	}
}

// An audit has room for so many policies: each one's list repeats the
// categories a violation may carry, and a provider's structured-output mode
// has a ceiling on enum values that the request is refused whole for passing.
// One that would pass it is refused here, with the count, before anything is
// sent.
func TestAuditRefusesMorePoliciesThanASchemaCanHold(t *testing.T) {
	many := func(n int) []string {
		policies := make([]string, n)
		for i := range policies {
			policies[i] = "policy number " + strconv.Itoa(i)
		}
		return policies
	}
	answer := func(n int) string {
		keys := make([]string, n)
		for i := range keys {
			keys[i] = `"p` + strconv.Itoa(i+1) + `":[]`
		}
		return `{"policies":{` + strings.Join(keys, ",") + `},"findings":[]}`
	}

	cases := []struct {
		name       string
		policies   int
		categories []string
		refused    bool
	}{
		{"one policy", 1, nil, false},
		{"twenty policies", 20, nil, false},
		{"ninety-nine policies, the most five categories leave room for", 99, nil, false},
		{"one hundred policies", 100, nil, true},
		{"two hundred policies", 200, nil, true},
		{"fewer fit when the caller adds a category", 84, []string{"privacy"}, true},
		{"and one fewer than that does", 83, []string{"privacy"}, false},
		{"no policies and many categories", 0, many(400), false},
		{"no policies and too many categories", 0, many(501), true},
		{"one policy and too many categories", 1, many(300), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if values := auditEnumValues(many(tc.policies), tc.categories); (values > auditEnumBudget) != tc.refused {
				t.Fatalf("%d enum values against a budget of %d, and the case expects refused = %v", values, auditEnumBudget, tc.refused)
			}
			result, err, sent := auditWith(t, answer(tc.policies), AuditOptions{Policies: many(tc.policies), Categories: tc.categories})
			if tc.refused {
				if !errors.Is(err, types.ErrConfiguration) || sent.calls != 0 {
					t.Fatalf("err = %v with %d calls; want a configuration error and no call", err, sent.calls)
				}
				if !reflect.DeepEqual(result, AuditResult[lx029Record]{}) {
					t.Errorf("a refused audit returned a result")
				}
				return
			}
			if err != nil {
				t.Fatalf("Audit: %v", err)
			}
			if problem := llm.StrictSchemaProblem(sent.opts.JSONSchema); problem != "" {
				t.Fatalf("the schema breaks a strict-mode rule: %s", problem)
			}
		})
	}
}

// Nested structures are inspected unless the caller asks for the top level
// only. The switch was Deep, on by default and copied from the caller's options
// as given, so Audit(x) looked inside nested structures and
// Audit(x, AuditOptions{Threshold: 0.3}) did not.
func TestAuditInspectsNestedStructuresUnlessToldNotTo(t *testing.T) {
	cases := []struct {
		name    string
		opts    *AuditOptions // nil for no options at all
		shallow bool
	}{
		{"no options", nil, false},
		{"empty options", &AuditOptions{}, false},
		{"a threshold and nothing else", &AuditOptions{Threshold: 0.3}, false},
		{"policies and nothing else", &AuditOptions{Policies: []string{"p"}}, false},
		{"Deep set", &AuditOptions{Deep: true}, false},
		{"Deep left unset beside another option", &AuditOptions{Deep: false, Steering: "s"}, false},
		{"Shallow set", &AuditOptions{Shallow: true}, true},
		{"Shallow with policies", &AuditOptions{Shallow: true, Policies: []string{"p"}}, true},
		{"Shallow with a threshold", &AuditOptions{Shallow: true, Threshold: 0.5}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := `{"findings":[]}`
			opts := AuditOptions{}
			if tc.opts != nil {
				opts = *tc.opts
				if len(opts.Policies) > 0 {
					response = `{"policies":{"p1":[]},"findings":[]}`
				}
			}
			_, err, sent := auditWith(t, response, opts)
			if err != nil {
				t.Fatalf("Audit: %v", err)
			}
			nested := strings.Contains(sent.user, "nested structures as well")
			top := strings.Contains(sent.user, "top level only")
			if nested == tc.shallow || top != tc.shallow {
				t.Errorf("shallow = %v, and the message says nested %v, top level only %v:\n%s", tc.shallow, nested, top, sent.user)
			}
		})
	}
}

// Asking for both at once is a contradiction, and is refused before a call.
func TestAuditRefusesDeepAndShallowTogether(t *testing.T) {
	_, err, sent := auditWith(t, `{"findings":[]}`, AuditOptions{Deep: true, Shallow: true})
	if !errors.Is(err, types.ErrConfiguration) || sent.calls != 0 {
		t.Fatalf("err = %v with %d calls; want a configuration error and no call", err, sent.calls)
	}
}

// The summary's counts are of the findings listed, and the verdict is of every
// finding that counts. When the threshold leaves a failing finding out, the
// result says how many it left out, in the summary and in the judgment, where
// it used to read "fail" over "0 finding(s)" and nothing else.
func TestAuditSaysWhatTheThresholdLeftOut(t *testing.T) {
	both := []string{lx029PolicyA, lx029PolicyB}
	answer := func(a, findings string) string {
		return `{"policies":{"p1":[` + a + `],"p2":[]},"findings":[` + findings + `]}`
	}
	high := lx029Finding("security", "0.75", "password")
	critical := lx029Finding("security", "0.95", "password")
	low := lx029Finding("security", "0.2", "id")

	cases := []struct {
		name       string
		opts       AuditOptions
		response   string
		total      int
		bySeverity map[string]int
		byCategory map[string]int
		below      int
		passes     bool
		critical   bool
		summary    string // what the judgment's summary has to hold
	}{
		{"nothing hidden", AuditOptions{Policies: both}, answer(high, ""), 1, map[string]int{"high": 1}, map[string]int{"security": 1}, 0, false, false, "1 finding(s)"},
		{"a failing finding hidden", AuditOptions{Policies: both, Threshold: 0.9}, answer(high, ""), 0, map[string]int{}, map[string]int{}, 1, false, false, "1 more below the listing threshold"},
		{"a critical finding hidden", AuditOptions{Policies: both, Threshold: 0.99}, answer(critical, ""), 0, map[string]int{}, map[string]int{}, 1, false, true, "1 more below"},
		{"one listed and one hidden", AuditOptions{Policies: both, Threshold: 0.5}, answer(high+","+low, ""), 1, map[string]int{"high": 1}, map[string]int{"security": 1}, 1, false, false, "1 more below"},
		{"two minor violations hidden: a violated policy fails whatever its severity", AuditOptions{Policies: both, Threshold: 0.5}, answer(low+","+low, ""), 0, map[string]int{}, map[string]int{}, 2, false, false, "2 more below"},
		{"nothing found", AuditOptions{Policies: both, Threshold: 0.5}, answer("", ""), 0, map[string]int{}, map[string]int{}, 0, true, false, "0 finding(s)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err, _ := auditWith(t, tc.response, tc.opts)
			if err != nil {
				t.Fatalf("Audit: %v", err)
			}
			summary := result.Summary
			if summary.TotalFindings != tc.total || summary.TotalFindings != len(result.Findings) {
				t.Errorf("TotalFindings = %d with %d listed, want %d", summary.TotalFindings, len(result.Findings), tc.total)
			}
			if !reflect.DeepEqual(summary.BySeverity, tc.bySeverity) || !reflect.DeepEqual(summary.ByCategory, tc.byCategory) {
				t.Errorf("BySeverity %v, ByCategory %v; want %v, %v", summary.BySeverity, summary.ByCategory, tc.bySeverity, tc.byCategory)
			}
			if summary.BelowThreshold != tc.below || summary.PassesAudit != tc.passes || summary.Critical != tc.critical {
				t.Errorf("below %d, passes %v, critical %v; want %d, %v, %v", summary.BelowThreshold, summary.PassesAudit, summary.Critical, tc.below, tc.passes, tc.critical)
			}

			judged := auditResultToJudgment(result)
			if (judged.Verdict == types.VerdictPass) != tc.passes {
				t.Errorf("the judgment's verdict is %v", judged.Verdict)
			}
			if !strings.Contains(judged.Summary, tc.summary) {
				t.Errorf("the judgment's summary is %q, want it to say %q", judged.Summary, tc.summary)
			}
			if got, _ := judged.ModelClaims["findings_below_threshold"].(int); got != tc.below {
				t.Errorf("the judgment carries %v below the threshold, want %d", judged.ModelClaims["findings_below_threshold"], tc.below)
			}
			if len(judged.Issues) != tc.total {
				t.Errorf("%d issues for %d listed findings", len(judged.Issues), tc.total)
			}
		})
	}
}

// A violation of a supplied policy fails the audit at any severity, and a
// finding that is no policy's fails it from 0.7. The verdict used to turn on
// the model's severity for both, so the same missing field passed or failed an
// audit of "every record must name a data owner" depending on whether the
// model called it moderate or serious.
func TestAViolatedPolicyFailsTheAuditWhateverItsSeverity(t *testing.T) {
	both := []string{lx029PolicyA, lx029PolicyB}
	for _, severity := range []string{"0", "0.1", "0.25", "0.3", "0.5", "0.69", "0.7", "0.85", "0.9", "1"} {
		t.Run("a violation at "+severity, func(t *testing.T) {
			result, err, _ := auditWith(t, `{"policies":{"p1":[],"p2":[`+lx029Finding("compliance", severity, "email")+`]},"findings":[]}`, AuditOptions{Policies: both})
			if err != nil {
				t.Fatalf("Audit: %v", err)
			}
			if result.Summary.PassesAudit {
				t.Errorf("a record that violates a supplied policy passed, at severity %s", severity)
			}
			if result.Summary.ByPolicy[lx029PolicyB] != 1 {
				t.Errorf("ByPolicy = %v", result.Summary.ByPolicy)
			}
		})
		t.Run("a finding that is no policy's at "+severity, func(t *testing.T) {
			result, err, _ := auditWith(t, `{"policies":{"p1":[],"p2":[]},"findings":[`+lx029Finding("quality", severity, "team")+`]}`,
				AuditOptions{Policies: both, Categories: []string{"quality"}})
			if err != nil {
				t.Fatalf("Audit: %v", err)
			}
			serious := severity == "0.7" || severity == "0.85" || severity == "0.9" || severity == "1"
			if result.Summary.PassesAudit == serious {
				t.Errorf("passes = %v at severity %s", result.Summary.PassesAudit, severity)
			}
		})
	}
}

// What is set apart does not count, and the result says how severe the worst of
// it is, so a critical problem outside the caller's policies is not missed for
// being outside them.
func TestAuditSaysHowSevereWhatItSetApartIs(t *testing.T) {
	both := []string{lx029PolicyA, lx029PolicyB}
	answer := func(findings string) string { return `{"policies":{"p1":[],"p2":[]},"findings":[` + findings + `]}` }

	cases := []struct {
		name     string
		opts     AuditOptions
		response string
		other    int
		highest  float64
		passes   bool
	}{
		{"nothing set apart", AuditOptions{Policies: both}, answer(""), 0, 0, true},
		{"one, minor", AuditOptions{Policies: both}, answer(lx029Finding("quality", "0.3", "id")), 1, 0.3, true},
		{"one, critical", AuditOptions{Policies: both}, answer(lx029Finding("quality", "1", "id")), 1, 1, true},
		{"several", AuditOptions{Policies: both}, answer(lx029Finding("quality", "0.3", "id") + "," + lx029Finding("security", "0.85", "email") + "," + lx029Finding("quality", "0.1", "team")), 3, 0.85, true},
		{"the same finding counts when its category was asked for, and nothing is set apart",
			AuditOptions{Policies: both, Categories: []string{"quality"}}, answer(lx029Finding("quality", "1", "id")), 0, 0, false},
		{"no policies: nothing is set apart", AuditOptions{}, `{"findings":[` + lx029Finding("quality", "1", "id") + `]}`, 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err, _ := auditWith(t, tc.response, tc.opts)
			if err != nil {
				t.Fatalf("Audit: %v", err)
			}
			if len(result.Other) != tc.other || result.Summary.OtherHighestSeverity != tc.highest || result.Summary.PassesAudit != tc.passes {
				t.Errorf("%d set apart, highest %v, passes %v; want %d, %v, %v",
					len(result.Other), result.Summary.OtherHighestSeverity, result.Summary.PassesAudit, tc.other, tc.highest, tc.passes)
			}
			judged := auditResultToJudgment(result)
			if got, _ := judged.ModelClaims["other_highest_severity"].(float64); got != tc.highest {
				t.Errorf("the judgment carries %v, want %v", judged.ModelClaims["other_highest_severity"], tc.highest)
			}
		})
	}
}

// The categories a finding may carry are named in the message, whoever chose
// them. They were only in the schema, which a provider that does not enforce
// one never shows the model.
func TestAuditNamesTheCategoriesInTheMessage(t *testing.T) {
	five := `["security","compliance","quality","consistency","completeness"]`
	cases := []struct {
		name string
		opts AuditOptions
		want []string
	}{
		{"nothing asked", AuditOptions{}, []string{"Categories for a finding: " + five}},
		{"policies", AuditOptions{Policies: []string{"p"}}, []string{"Categories for a violation: " + five, "Categories for a finding: " + five}},
		{"categories", AuditOptions{Categories: []string{"privacy"}}, []string{`the only ones a finding may carry: ["privacy"]`}},
		{"both", AuditOptions{Policies: []string{"p"}, Categories: []string{"privacy"}},
			[]string{`Categories for a violation: ["privacy","security","compliance","quality","consistency","completeness"]`, `the only ones a finding may carry: ["privacy"]`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := `{"findings":[]}`
			if len(tc.opts.Policies) > 0 {
				response = `{"policies":{"p1":[]},"findings":[]}`
			}
			_, err, sent := auditWith(t, response, tc.opts)
			if err != nil {
				t.Fatalf("Audit: %v", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(sent.user, want) {
					t.Errorf("the message does not say %q:\n%s", want, sent.user)
				}
			}
			if strings.Contains(sent.system, "privacy") {
				t.Error("the caller's category is in the instructions")
			}
		})
	}
}

// auditAnswer on its own, with a provider that does not hold to the schema.
func TestAuditAnswer(t *testing.T) {
	finding := `{"category":"security","severity":0.5,"field":null,"issue":"x","evidence":null,"recommendation":null}`
	two := []string{"a", "b"}
	cases := []struct {
		name       string
		response   string
		policies   []string
		violations []int // how many under each policy; nil with an error expected
		other      int
	}{
		{"no policies, no findings", `{"findings":[]}`, nil, []int{}, 0},
		{"no policies, one finding", `{"findings":[` + finding + `]}`, nil, []int{}, 1},
		{"an empty policies object when none were asked", `{"policies":{},"findings":[]}`, nil, []int{}, 0},
		{"the same with space in it", `{"policies":{ },"findings":[]}`, nil, []int{}, 0},
		{"policies null when none were asked", `{"policies":null,"findings":[]}`, nil, []int{}, 0},
		{"policies reported when none were asked", `{"policies":{"p1":[]},"findings":[]}`, nil, nil, 0},
		{"two policies, nothing found", `{"policies":{"p1":[],"p2":[]},"findings":[]}`, two, []int{0, 0}, 0},
		{"a violation of the second", `{"policies":{"p1":[],"p2":[` + finding + `]},"findings":[]}`, two, []int{0, 1}, 0},
		{"violations and a finding", `{"policies":{"p1":[` + finding + `,` + finding + `],"p2":[]},"findings":[` + finding + `]}`, two, []int{2, 0}, 1},
		{"the keys in the other order", `{"findings":[],"policies":{"p2":[],"p1":[` + finding + `]}}`, two, []int{1, 0}, 0},
		{"keys in other capitals are not the keys", `{"policies":{"P1":[],"P2":[]},"findings":[]}`, two, nil, 0},
		{"a key left out", `{"policies":{"p1":[]},"findings":[]}`, two, nil, 0},
		{"a key too many", `{"policies":{"p1":[],"p2":[],"p3":[]},"findings":[]}`, two, nil, 0},
		{"the right number of keys and the wrong ones", `{"policies":{"p1":[],"p3":[]},"findings":[]}`, two, nil, 0},
		{"policies as a list", `{"policies":[[],[]],"findings":[]}`, two, nil, 0},
		{"no findings list", `{"policies":{"p1":[],"p2":[]}}`, two, nil, 0},
		{"prose with no object in it", `There is nothing wrong with this record.`, nil, nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			violations, other, err := auditAnswer(tc.response, tc.policies, nil)
			if tc.violations == nil {
				if err == nil {
					t.Fatalf("the answer was accepted: %v, %v", violations, other)
				}
				if violations != nil || other != nil {
					t.Errorf("findings came back with the error")
				}
				return
			}
			if err != nil {
				t.Fatalf("auditAnswer: %v", err)
			}
			counts := make([]int, len(violations))
			for i, found := range violations {
				counts[i] = len(found)
				for _, finding := range found {
					if finding.Policy != tc.policies[i] {
						t.Errorf("a violation under policy %d names %q", i+1, finding.Policy)
					}
				}
			}
			if !reflect.DeepEqual(counts, tc.violations) || len(other) != tc.other {
				t.Errorf("violations %v and %d others; want %v and %d", counts, len(other), tc.violations, tc.other)
			}
		})
	}
}

// mergeAuditOptions, field by field.
func TestMergeAuditOptions(t *testing.T) {
	defaults := AuditOptions{Mode: types.TransformMode, Intelligence: types.Smart}
	cases := []struct {
		name string
		user AuditOptions
		read func(AuditOptions) any
		want any
	}{
		{"policies", AuditOptions{Policies: []string{"p"}}, func(o AuditOptions) any { return o.Policies }, []string{"p"}},
		{"categories", AuditOptions{Categories: []string{"c"}}, func(o AuditOptions) any { return o.Categories }, []string{"c"}},
		{"a threshold", AuditOptions{Threshold: 0.4}, func(o AuditOptions) any { return o.Threshold }, 0.4},
		{"a zero threshold leaves the default", AuditOptions{}, func(o AuditOptions) any { return o.Threshold }, 0.0},
		{"shallow", AuditOptions{Shallow: true}, func(o AuditOptions) any { return o.Shallow }, true},
		{"shallow left alone", AuditOptions{Threshold: 0.4}, func(o AuditOptions) any { return o.Shallow }, false},
		{"steering", AuditOptions{Steering: "s"}, func(o AuditOptions) any { return o.Steering }, "s"},
		{"a mode", AuditOptions{Mode: types.Strict}, func(o AuditOptions) any { return o.Mode }, types.Strict},
		{"no mode leaves the default", AuditOptions{}, func(o AuditOptions) any { return o.Mode }, types.TransformMode},
		{"a tier", AuditOptions{Intelligence: types.Quick}, func(o AuditOptions) any { return o.Intelligence }, types.Quick},
		{"no tier leaves the default", AuditOptions{}, func(o AuditOptions) any { return o.Intelligence }, types.Smart},
		{"a model", AuditOptions{Model: "m"}, func(o AuditOptions) any { return o.Model }, "m"},
		{"an effort", AuditOptions{Effort: types.EffortLow}, func(o AuditOptions) any { return o.Effort }, types.EffortLow},
		{"a request id", AuditOptions{RequestID: "r"}, func(o AuditOptions) any { return o.RequestID }, "r"},
		{"a correlation id", AuditOptions{CorrelationID: "c"}, func(o AuditOptions) any { return o.CorrelationID }, "c"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.read(mergeAuditOptions(defaults, tc.user)); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// buildAuditSummary: the bands, at their edges.
func TestBuildAuditSummaryAtTheEdgesOfItsBands(t *testing.T) {
	cases := []struct {
		severity float64
		level    string
		passes   bool
		critical bool
	}{
		{0, "info", true, false}, {0.29, "info", true, false},
		{0.3, "low", true, false}, {0.49, "low", true, false},
		{0.5, "medium", true, false}, {0.69, "medium", true, false},
		{0.7, "high", false, false}, {0.89, "high", false, false},
		{0.9, "critical", false, true}, {1, "critical", false, true},
	}
	for _, tc := range cases {
		summary := buildAuditSummary([]AuditFinding{{Category: "security", Severity: tc.severity}})
		if summary.BySeverity[tc.level] != 1 || summary.PassesAudit != tc.passes || summary.Critical != tc.critical || summary.TotalFindings != 1 {
			t.Errorf("severity %v: %+v; want level %q, passes %v, critical %v", tc.severity, summary, tc.level, tc.passes, tc.critical)
		}
	}
	if summary := buildAuditSummary(nil); !summary.PassesAudit || summary.TotalFindings != 0 {
		t.Errorf("no findings: %+v", summary)
	}
}
