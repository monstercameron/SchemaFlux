package fluent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/ops"
)

// Review round 12, the builders' two options that had five cases each.

// bite12ReasoningProperties counts the "reasoning" properties a schema
// declares. One is the answer's own; each evaluation of an option on a rule
// adds one when a reason was asked for.
func bite12ReasoningProperties(t *testing.T, schema map[string]any) int {
	t.Helper()
	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("a schema that cannot be written: %v", err)
	}
	return strings.Count(string(encoded), `"reasoning":`)
}

func TestBite12ArbitrateRequestIncludeReasoning(t *testing.T) {
	const evaluated = `{"options":{"o1":{"r1":{"passed":true,"score":0.9,"reasoning":"why"}},"o2":{"r1":{"passed":true,"score":0.2,"reasoning":"why"}}},"reasoning":"x","confidence":0.8}`
	vendors := []round11Vendor{{Name: "a"}, {Name: "b"}}
	start := func() ArbitrateRequest[round11Vendor] { return Arbitrating(vendors) }

	cases := []struct {
		name    string
		build   func() ArbitrateRequest[round11Vendor]
		reasons bool
	}{
		{"as it starts", func() ArbitrateRequest[round11Vendor] { return start().Rules("cheap") }, false},
		{"asked for", func() ArbitrateRequest[round11Vendor] { return start().Rules("cheap").IncludeReasoning(true) }, true},
		{"asked for, before the rules", func() ArbitrateRequest[round11Vendor] { return start().IncludeReasoning(true).Rules("cheap") }, true},
		{"asked for, then not", func() ArbitrateRequest[round11Vendor] {
			return start().Rules("cheap").IncludeReasoning(true).IncludeReasoning(false)
		}, false},
		{"not, then asked for", func() ArbitrateRequest[round11Vendor] {
			return start().Rules("cheap").IncludeReasoning(false).IncludeReasoning(true)
		}, true},
		{"asked for twice", func() ArbitrateRequest[round11Vendor] {
			return start().Rules("cheap").IncludeReasoning(true).IncludeReasoning(true)
		}, true},
		{"asked for, with steering after", func() ArbitrateRequest[round11Vendor] {
			return start().Rules("cheap").IncludeReasoning(true).Steer("prefer small vendors")
		}, true},
		{"asked for, with steering before", func() ArbitrateRequest[round11Vendor] {
			return start().Steer("prefer small vendors").Rules("cheap").IncludeReasoning(true)
		}, true},
		{"options replaced after it, which start without", func() ArbitrateRequest[round11Vendor] {
			return start().IncludeReasoning(true).WithOptions(bite12ArbitrateOptions("cheap"))
		}, false},
		{"asked for on options given whole", func() ArbitrateRequest[round11Vendor] {
			return start().WithOptions(bite12ArbitrateOptions("cheap")).IncludeReasoning(true)
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &requestRecorder{body: evaluated}
			restore := installStubProvider(t, provider)
			defer restore()

			result, err := tc.build().Run(context.Background())
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if asked := strings.Contains(provider.last.UserPrompt, "Give a reason with every evaluation."); asked != tc.reasons {
				t.Errorf("the message asks for a reason = %v, want %v", asked, tc.reasons)
			}
			want := 1
			if tc.reasons {
				want = 3
			}
			if got := bite12ReasoningProperties(t, provider.last.JSONSchema); got != want {
				t.Errorf("the schema declares %d reasoning properties, want %d", got, want)
			}
			if kept := result.Evaluations[0].RuleResults[0].Reasoning != ""; kept != tc.reasons {
				t.Errorf("a reason was returned = %v, want %v", kept, tc.reasons)
			}
		})
	}

	// A request is a value: asking for reasons on one does not ask on the one it
	// was made from.
	t.Run("the request it was made from is unchanged", func(t *testing.T) {
		provider := &requestRecorder{body: evaluated}
		restore := installStubProvider(t, provider)
		defer restore()

		base := start().Rules("cheap")
		withReasons := base.IncludeReasoning(true)
		if _, err := base.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if strings.Contains(provider.last.UserPrompt, "Give a reason with every evaluation.") {
			t.Error("reasons were asked for by a request that did not ask for them")
		}
		if _, err := withReasons.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if !strings.Contains(provider.last.UserPrompt, "Give a reason with every evaluation.") {
			t.Error("reasons were not asked for by the request that asked")
		}
	})
}

// bite12ArbitrateOptions is a whole set of options, as a caller who builds them
// by hand gives them.
func bite12ArbitrateOptions(rules ...string) ops.ArbitrateOptions {
	opts := ops.NewArbitrateOptions()
	opts.Rules = rules
	return opts
}

func TestBite12AuditRequestShallow(t *testing.T) {
	vendor := round11Vendor{Name: "a"}
	const policies = `{"policies":{"p1":[]},"findings":[]}`
	start := func() AuditRequest[round11Vendor] { return Auditing(vendor) }

	cases := []struct {
		name    string
		build   func() AuditRequest[round11Vendor]
		shallow bool
		body    string // policies are reported on by key, so a case with them cannot take the default
	}{
		{"as it starts", func() AuditRequest[round11Vendor] { return start() }, false, ""},
		{"asked for", func() AuditRequest[round11Vendor] { return start().Shallow(true) }, true, ""},
		{"asked for, then not", func() AuditRequest[round11Vendor] { return start().Shallow(true).Shallow(false) }, false, ""},
		{"not, then asked for", func() AuditRequest[round11Vendor] { return start().Shallow(false).Shallow(true) }, true, ""},
		{"asked for twice", func() AuditRequest[round11Vendor] { return start().Shallow(true).Shallow(true) }, true, ""},
		{"said outright that it is not", func() AuditRequest[round11Vendor] { return start().Shallow(false) }, false, ""},
		{"with categories, before", func() AuditRequest[round11Vendor] { return start().Categories("quality").Shallow(true) }, true, ""},
		{"with categories, after", func() AuditRequest[round11Vendor] { return start().Shallow(true).Categories("quality") }, true, ""},
		{"with policies, before", func() AuditRequest[round11Vendor] {
			return start().Policies("every vendor has a name").Shallow(true)
		}, true, policies},
		{"with policies, after", func() AuditRequest[round11Vendor] {
			return start().Shallow(true).Policies("every vendor has a name")
		}, true, policies},
		{"with policies, and not asked for", func() AuditRequest[round11Vendor] {
			return start().Policies("every vendor has a name")
		}, false, policies},
		{"with steering", func() AuditRequest[round11Vendor] { return start().Shallow(true).Steer("be brief") }, true, ""},
		{"with policies and categories", func() AuditRequest[round11Vendor] {
			return start().Policies("every vendor has a name").Categories("quality").Shallow(true)
		}, true, policies},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.body
			if body == "" {
				body = `{"findings":[]}`
			}
			provider := &requestRecorder{body: body}
			restore := installStubProvider(t, provider)
			defer restore()

			if _, err := tc.build().Run(context.Background()); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if shallow := strings.Contains(provider.last.UserPrompt, "top level only"); shallow != tc.shallow {
				t.Errorf("asked for the top level only = %v, want %v", shallow, tc.shallow)
			}
		})
	}

	t.Run("the request it was made from is unchanged", func(t *testing.T) {
		provider := &requestRecorder{body: `{"findings":[]}`}
		restore := installStubProvider(t, provider)
		defer restore()

		base := start()
		shallow := base.Shallow(true)
		if _, err := base.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if strings.Contains(provider.last.UserPrompt, "top level only") {
			t.Error("the top level only was asked for by a request that did not ask for it")
		}
		if _, err := shallow.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if !strings.Contains(provider.last.UserPrompt, "top level only") {
			t.Error("the top level only was not asked for by the request that asked")
		}
	})
}
