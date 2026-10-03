package ops

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

type lx029Record struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Password string `json:"password"`
	Team     string `json:"team"`
}

var lx029Subject = lx029Record{ID: "1", Email: "sam@example.com", Password: "hunter2-SECRET", Team: "R&D <core>"}

type auditCall struct {
	system, user string
	opts         types.OpOptions
	calls        int
}

// auditWith runs Audit against a scripted answer and returns what was sent.
func auditWith(t *testing.T, response string, opts AuditOptions) (AuditResult[lx029Record], error, *auditCall) {
	t.Helper()
	sent := &auditCall{}
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(_ context.Context, system, user string, opt types.OpOptions) (string, error) {
		sent.system, sent.user, sent.opts = system, user, opt
		sent.calls++
		return response, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})
	result, err := Audit(lx029Subject, opts)
	return result, err, sent
}

func lx029Finding(category, severity, field string) string {
	return `{"category":"` + category + `","severity":` + severity + `,"field":"` + field + `","issue":"something is wrong","evidence":"what shows it","recommendation":"fix it"}`
}

const (
	lx029PolicyA = "Passwords must not be stored in plain text"
	lx029PolicyB = "Email addresses must be on the company domain"
)

// LX-029. A finding counts toward the verdict when it is about something the
// caller asked to have checked: a violation of a policy they supplied, or a
// problem in a category they named. Live, compliant records failed an audit of
// three policies on findings that had nothing to do with any of them.
//
// And the verdict is worked out before the severity threshold hides anything.
// Threshold is the lowest severity a caller wants listed. It was applied before
// the verdict, so raising it made a failing audit pass.
func TestAuditCountsOnlyWhatTheCallerAskedToHaveChecked(t *testing.T) {
	both := []string{lx029PolicyA, lx029PolicyB}
	answer := func(a, b, findings string) string {
		return `{"policies":{"p1":[` + a + `],"p2":[` + b + `]},"findings":[` + findings + `]}`
	}
	plain := func(findings string) string { return `{"findings":[` + findings + `]}` }

	cases := []struct {
		name     string
		opts     AuditOptions
		response string
		passes   bool
		critical bool
		listed   int            // findings in Findings
		other    int            // findings reported apart, which cannot change the verdict
		byPolicy map[string]int // nil when no policies were supplied
		hidden   int            // counted findings the threshold kept out of the list
	}{
		{"a policy violated, critically", AuditOptions{Policies: both},
			answer(lx029Finding("security", "0.95", "password"), "", ""),
			false, true, 1, 0, map[string]int{lx029PolicyA: 1, lx029PolicyB: 0}, 0},
		{"no policy violated", AuditOptions{Policies: both},
			answer("", "", ""),
			true, false, 0, 0, map[string]int{lx029PolicyA: 0, lx029PolicyB: 0}, 0},
		{"a critical problem that is no policy's, with policies and no categories", AuditOptions{Policies: both},
			answer("", "", lx029Finding("quality", "0.95", "team")),
			true, false, 0, 1, map[string]int{lx029PolicyA: 0, lx029PolicyB: 0}, 0},
		{"the same problem when its category was asked for", AuditOptions{Policies: both, Categories: []string{"quality"}},
			answer("", "", lx029Finding("quality", "0.95", "team")),
			false, true, 1, 0, map[string]int{lx029PolicyA: 0, lx029PolicyB: 0}, 0},
		{"a problem with no policies and no categories asked", AuditOptions{},
			plain(lx029Finding("security", "0.95", "password")),
			false, true, 1, 0, nil, 0},
		{"a problem in a category that was asked for, no policies", AuditOptions{Categories: []string{"security"}},
			plain(lx029Finding("security", "0.8", "password")),
			false, false, 1, 0, nil, 0},
		{"a policy violated at a moderate severity still fails", AuditOptions{Policies: both},
			answer(lx029Finding("security", "0.5", "password"), "", ""),
			false, false, 1, 0, map[string]int{lx029PolicyA: 1, lx029PolicyB: 0}, 0},
		{"a policy violated at the lowest severity still fails", AuditOptions{Policies: both},
			answer(lx029Finding("security", "0", "password"), "", ""),
			false, false, 1, 0, map[string]int{lx029PolicyA: 1, lx029PolicyB: 0}, 0},
		{"a moderate problem in a category asked for does not fail", AuditOptions{Policies: both, Categories: []string{"quality"}},
			answer("", "", lx029Finding("quality", "0.5", "team")),
			true, false, 1, 0, map[string]int{lx029PolicyA: 0, lx029PolicyB: 0}, 0},
		{"two violations of one policy and one of the other", AuditOptions{Policies: both},
			answer(lx029Finding("security", "0.9", "password")+","+lx029Finding("security", "0.4", "id"), lx029Finding("compliance", "0.7", "email"), ""),
			false, true, 3, 0, map[string]int{lx029PolicyA: 2, lx029PolicyB: 1}, 0},
		{"a violation the threshold hides still fails the audit", AuditOptions{Policies: both, Threshold: 0.8},
			answer(lx029Finding("security", "0.75", "password"), "", ""),
			false, false, 0, 0, map[string]int{lx029PolicyA: 1, lx029PolicyB: 0}, 1},
		{"a critical violation the threshold hides", AuditOptions{Policies: both, Threshold: 0.99},
			answer(lx029Finding("security", "0.95", "password"), "", ""),
			false, true, 0, 0, map[string]int{lx029PolicyA: 1, lx029PolicyB: 0}, 1},
		{"an observation the threshold hides", AuditOptions{Threshold: 0.3},
			plain(lx029Finding("quality", "0.1", "team") + "," + lx029Finding("quality", "0.4", "id")),
			true, false, 1, 0, nil, 1},
		{"a finding exactly at the threshold is listed", AuditOptions{Threshold: 0.4},
			plain(lx029Finding("quality", "0.4", "id")),
			true, false, 1, 0, nil, 0},
		{"a policy violation counts whatever its category", AuditOptions{Policies: both, Categories: []string{"security"}},
			answer("", lx029Finding("compliance", "0.9", "email"), ""),
			false, true, 1, 0, map[string]int{lx029PolicyA: 0, lx029PolicyB: 1}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err, _ := auditWith(t, tc.response, tc.opts)
			if err != nil {
				t.Fatalf("Audit: %v", err)
			}
			if result.Summary.PassesAudit != tc.passes || result.Summary.Critical != tc.critical {
				t.Errorf("passes = %v, critical = %v; want %v, %v", result.Summary.PassesAudit, result.Summary.Critical, tc.passes, tc.critical)
			}
			if len(result.Findings) != tc.listed || len(result.Other) != tc.other {
				t.Errorf("%d findings listed and %d apart; want %d and %d", len(result.Findings), len(result.Other), tc.listed, tc.other)
			}
			if !reflect.DeepEqual(result.Summary.ByPolicy, tc.byPolicy) && (len(result.Summary.ByPolicy) != 0 || len(tc.byPolicy) != 0) {
				t.Errorf("ByPolicy = %v, want %v", result.Summary.ByPolicy, tc.byPolicy)
			}
			if hidden, _ := result.Metadata["findings_below_threshold"].(int); hidden != tc.hidden {
				t.Errorf("findings_below_threshold = %v, want %d", result.Metadata["findings_below_threshold"], tc.hidden)
			}
			// A finding under a policy names it, in the caller's own words;
			// one that is no policy's names none.
			for _, finding := range result.Findings {
				if finding.Policy != "" && finding.Policy != lx029PolicyA && finding.Policy != lx029PolicyB {
					t.Errorf("a finding names policy %q, which the caller did not supply", finding.Policy)
				}
			}
			for _, finding := range result.Other {
				if finding.Policy != "" {
					t.Errorf("a finding reported apart names policy %q", finding.Policy)
				}
			}
			if result.Original != lx029Subject {
				t.Errorf("Original = %+v", result.Original)
			}
		})
	}
}

// An answer that does not report on what was asked is refused, and a refused
// audit returns nothing a caller could mistake for a pass. `{}` and
// `{"findings": null}` both used to come back as an audit with no findings,
// which passes.
func TestAuditRefusesAnAnswerThatDoesNotReport(t *testing.T) {
	both := []string{lx029PolicyA, lx029PolicyB}
	good := lx029Finding("security", "0.9", "password")
	withPolicies := AuditOptions{Policies: both}

	cases := []struct {
		name     string
		opts     AuditOptions
		response string
	}{
		{"not JSON", AuditOptions{}, `the record looks fine, hunter2-SECRET aside`},
		{"an empty object", AuditOptions{}, `{}`},
		{"findings null", AuditOptions{}, `{"findings":null}`},
		{"findings an object", AuditOptions{}, `{"findings":{"a":1}}`},
		{"findings a string", AuditOptions{}, `{"findings":"none, hunter2-SECRET"}`},
		{"policies asked and not reported", withPolicies, `{"findings":[]}`},
		{"policies null", withPolicies, `{"policies":null,"findings":[]}`},
		{"one policy not reported", withPolicies, `{"policies":{"p1":[]},"findings":[]}`},
		{"a policy reported as null", withPolicies, `{"policies":{"p1":[],"p2":null},"findings":[]}`},
		{"a policy that was not asked", withPolicies, `{"policies":{"p1":[],"p2":[],"p3":[]},"findings":[]}`},
		{"policies as a list", withPolicies, `{"policies":[[],[]],"findings":[]}`},
		{"policies reported when none were asked", AuditOptions{}, `{"policies":{"p1":[` + good + `]},"findings":[]}`},
		{"a finding with no severity", AuditOptions{}, `{"findings":[{"category":"security","field":"password","issue":"x"}]}`},
		{"a severity above the scale", AuditOptions{}, `{"findings":[` + lx029Finding("security", "1.5", "password") + `]}`},
		{"a severity below the scale", AuditOptions{}, `{"findings":[` + lx029Finding("security", "-0.1", "password") + `]}`},
		{"a severity that is text", AuditOptions{}, `{"findings":[{"category":"security","severity":"high","field":"password","issue":"x"}]}`},
		{"a finding with no issue", AuditOptions{}, `{"findings":[{"category":"security","severity":0.9,"field":"password","issue":"  "}]}`},
		{"a category that is not one of the five", AuditOptions{}, `{"findings":[` + lx029Finding("vibes", "0.9", "password") + `]}`},
		{"a category the caller did not ask for", AuditOptions{Categories: []string{"security"}}, `{"findings":[` + lx029Finding("quality", "0.9", "team") + `]}`},
		{"a violation with no severity", withPolicies, `{"policies":{"p1":[{"category":"security","field":"password","issue":"x"}],"p2":[]},"findings":[]}`},
		{"a violation with a blank category", withPolicies, `{"policies":{"p1":[` + lx029Finding("", "0.9", "password") + `],"p2":[]},"findings":[]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err, _ := auditWith(t, tc.response, tc.opts)
			if err == nil {
				t.Fatalf("the answer was accepted: %+v", result)
			}
			// A number off the scale is an invariant broken, as it is for
			// every other operation that reads a score.
			if kind := types.KindOf(err); kind != types.KindSchemaViolation && kind != types.KindMalformedOutput && kind != types.KindInvariantViolation {
				t.Errorf("the error is classified %v", kind)
			}
			if !reflect.DeepEqual(result, AuditResult[lx029Record]{}) {
				t.Errorf("a refused audit returned a result: %+v", result)
			}
			if strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "vibes") {
				t.Errorf("the error carries the model's or the caller's text: %v", err)
			}
		})
	}
}

// What is sent. The instructions are the same for every call and hold nothing
// of the caller's: the policies, categories, guidance and data all travel in
// the message. The model is not told the threshold, which is this library's to
// apply, and it is given a schema for its answer.
func TestAuditRequest(t *testing.T) {
	both := []string{lx029PolicyA, lx029PolicyB}
	cases := []struct {
		name     string
		opts     AuditOptions
		response string
		inUser   []string
		policies []string // the keys the schema requires, nil for no policies property
		enum     []any    // the categories a finding outside the policies may carry
	}{
		{"nothing asked", AuditOptions{}, `{"findings":[]}`,
			[]string{`"email":"sam@example.com"`, "R&D <core>"}, nil,
			[]any{"security", "compliance", "quality", "consistency", "completeness"}},
		{"policies", AuditOptions{Policies: both}, `{"policies":{"p1":[],"p2":[]},"findings":[]}`,
			[]string{"p1", lx029PolicyA, "p2", lx029PolicyB}, []string{"p1", "p2"},
			[]any{"security", "compliance", "quality", "consistency", "completeness"}},
		{"categories", AuditOptions{Categories: []string{"security", "privacy"}}, `{"findings":[]}`,
			[]string{"security", "privacy"}, nil, []any{"security", "privacy"}},
		{"policies and categories", AuditOptions{Policies: both, Categories: []string{"privacy"}}, `{"policies":{"p1":[],"p2":[]},"findings":[]}`,
			[]string{lx029PolicyA, "privacy"}, []string{"p1", "p2"}, []any{"privacy"}},
		{"guidance", AuditOptions{Steering: "Treat the R&D team as internal <staff>"}, `{"findings":[]}`,
			[]string{"Treat the R&D team as internal <staff>"}, nil,
			[]any{"security", "compliance", "quality", "consistency", "completeness"}},
		{"a threshold", AuditOptions{Threshold: 0.37}, `{"findings":[]}`, nil, nil,
			[]any{"security", "compliance", "quality", "consistency", "completeness"}},
		{"blank and repeated policies are one policy", AuditOptions{Policies: []string{" ", lx029PolicyA, lx029PolicyA, ""}}, `{"policies":{"p1":[]},"findings":[]}`,
			[]string{"p1", lx029PolicyA}, []string{"p1"},
			[]any{"security", "compliance", "quality", "consistency", "completeness"}},
		{"blank and repeated categories are dropped", AuditOptions{Categories: []string{"security", " ", "security"}}, `{"findings":[]}`,
			[]string{"security"}, nil, []any{"security"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.opts.RequestID, tc.opts.CorrelationID = "req-1", "corr-1"
			_, err, sent := auditWith(t, tc.response, tc.opts)
			if err != nil {
				t.Fatalf("Audit: %v", err)
			}
			if sent.calls != 1 {
				t.Fatalf("%d calls were made, want 1", sent.calls)
			}

			// Nothing of the caller's is in the instructions.
			for _, theirs := range []string{lx029PolicyA, lx029PolicyB, "privacy", "sam@example.com", "hunter2", "R&D", "internal <staff>", "0.37"} {
				if strings.Contains(sent.system, theirs) {
					t.Errorf("the instructions carry the caller's %q", theirs)
				}
			}
			if sent.system != auditSystemPrompt {
				t.Error("the instructions are not the one fixed prompt")
			}
			if strings.Contains(sent.user, "0.37") {
				t.Error("the model is told the threshold")
			}
			for _, want := range tc.inUser {
				if !strings.Contains(sent.user, want) {
					t.Errorf("the message does not carry %q:\n%s", want, sent.user)
				}
			}
			if sent.opts.RequestID != "req-1" || sent.opts.CorrelationID != "corr-1" {
				t.Errorf("request id %q, correlation id %q: not carried", sent.opts.RequestID, sent.opts.CorrelationID)
			}

			// The answer is declared, and the declaration is one the API takes.
			schema := sent.opts.JSONSchema
			if schema == nil || sent.opts.SchemaName != "audit" || sent.opts.ResponseFormat != "json" {
				t.Fatalf("schema %v, name %q, format %q", schema != nil, sent.opts.SchemaName, sent.opts.ResponseFormat)
			}
			if problem := llm.StrictSchemaProblem(schema); problem != "" {
				t.Fatalf("the schema breaks a strict-mode rule: %s", problem)
			}
			properties := schema["properties"].(map[string]any)
			policies, asked := properties["policies"].(map[string]any)
			if asked != (tc.policies != nil) {
				t.Fatalf("policies asked for = %v, want %v", asked, tc.policies != nil)
			}
			if asked {
				if got := policies["required"]; !reflect.DeepEqual(got, tc.policies) {
					t.Errorf("the schema requires policies %v, want %v", got, tc.policies)
				}
			}
			finding := properties["findings"].(map[string]any)["items"].(map[string]any)
			category := finding["properties"].(map[string]any)["category"].(map[string]any)
			if got := category["enum"]; !reflect.DeepEqual(got, tc.enum) {
				t.Errorf("a finding's categories are %v, want %v", got, tc.enum)
			}
		})
	}
}

// Options that cannot mean anything are refused before a call is made.
func TestAuditRefusesAThresholdOffTheScale(t *testing.T) {
	cases := []struct {
		name      string
		threshold float64
		refused   bool
	}{
		{"zero", 0, false},
		{"a quarter", 0.25, false},
		{"one", 1, false},
		{"above one", 1.01, true},
		{"well above one", 7, true},
		{"negative", -0.2, true},
		{"not a number", math.NaN(), true},
		{"infinite", math.Inf(1), true},
		{"negative infinity", math.Inf(-1), true},
		{"just under one", 0.999, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err, sent := auditWith(t, `{"findings":[]}`, AuditOptions{Threshold: tc.threshold})
			if tc.refused {
				if !errors.Is(err, types.ErrConfiguration) || sent.calls != 0 {
					t.Fatalf("err = %v with %d calls made; want a configuration error and no call", err, sent.calls)
				}
				return
			}
			if err != nil {
				t.Fatalf("Audit: %v", err)
			}
		})
	}
}

// AuditWithModel is the form the root package recommends, and it dropped the
// policy a finding belongs to. Each issue now names its rule, and what was
// found outside the caller's policies and categories is kept apart there too.
func TestAuditWithModelSaysWhichPolicyAFindingBelongsTo(t *testing.T) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return `{"policies":{"p1":[` + lx029Finding("security", "0.95", "password") + `],"p2":[]},"findings":[` + lx029Finding("quality", "0.9", "team") + `]}`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	judged, err := AuditWithModel(lx029Subject, AuditOptions{Policies: []string{lx029PolicyA, lx029PolicyB}})
	if err != nil {
		t.Fatalf("AuditWithModel: %v", err)
	}
	if judged.Verdict != types.VerdictFail {
		t.Errorf("Verdict = %v, want fail", judged.Verdict)
	}
	if len(judged.Issues) != 1 || judged.Issues[0].Rule != lx029PolicyA || judged.Issues[0].Subject != "password" {
		t.Fatalf("Issues = %+v, want the one violation, naming its policy", judged.Issues)
	}
	other, _ := judged.ModelClaims["other_findings"].([]types.JudgmentIssue)
	if len(other) != 1 || other[0].Subject != "team" || other[0].Rule != "" {
		t.Errorf("other_findings = %+v, want the one finding that is no policy's", judged.ModelClaims["other_findings"])
	}
	if byPolicy, _ := judged.ModelClaims["by_policy"].(map[string]int); byPolicy[lx029PolicyA] != 1 || len(byPolicy) != 2 {
		t.Errorf("by_policy = %v", judged.ModelClaims["by_policy"])
	}
}

// The merge of options left out the two identifiers, so a caller's request id
// never reached the call.
func TestMergeAuditOptionsCarriesTheIdentifiers(t *testing.T) {
	merged := mergeAuditOptions(AuditOptions{Mode: types.TransformMode}, AuditOptions{RequestID: "r", CorrelationID: "c"})
	if merged.RequestID != "r" || merged.CorrelationID != "c" {
		t.Errorf("RequestID = %q, CorrelationID = %q", merged.RequestID, merged.CorrelationID)
	}
}
