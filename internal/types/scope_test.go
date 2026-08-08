package types

import (
	"strings"
	"testing"
)

func TestOptionScopeString(t *testing.T) {
	cases := []struct {
		s    OptionScope
		want string
	}{
		{ScopeUnspecified, "unspecified"},
		{ScopeProcess, "process"},
		{ScopeClient, "client"},
		{ScopeOperationDescriptor, "operation default"},
		{ScopeInvocation, "invocation"},
		{ScopeRequestContext, "request context"},
		{ScopeProviderRequest, "provider request"},
		{OptionScope(99), "unspecified"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.s.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolvedSettingString(t *testing.T) {
	unlocked := ResolvedSetting{Name: "timeout", Value: "30s", Source: ScopeInvocation}
	if got, want := unlocked.String(), "timeout=30s (invocation)"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}

	locked := ResolvedSetting{Name: "retention", Value: "7", Source: ScopeClient, Locked: true}
	if got, want := locked.String(), "retention=7 (client, locked)"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestResolutionPlanString(t *testing.T) {
	empty := ResolutionPlan{}
	if got := empty.String(); got != "" {
		t.Errorf("String() on an empty plan = %q, want empty", got)
	}

	plan := ResolutionPlan{Settings: []ResolvedSetting{
		{Name: "timeout", Value: "30s", Source: ScopeInvocation},
		{Name: "model", Value: "gpt-5.6-luna", Source: ScopeClient, Locked: true},
	}}
	rendered := plan.String()
	if !strings.Contains(rendered, "timeout=30s (invocation)") || !strings.Contains(rendered, "model=gpt-5.6-luna (client, locked)") {
		t.Errorf("String() = %q, missing an expected setting line", rendered)
	}
	if strings.Count(rendered, "\n") != 1 {
		t.Errorf("String() = %q, want exactly one newline joining two settings", rendered)
	}
}

func TestResolutionPlanGet(t *testing.T) {
	plan := ResolutionPlan{Settings: []ResolvedSetting{
		{Name: "timeout", Value: "30s", Source: ScopeInvocation},
	}}

	got, found := plan.Get("timeout")
	if !found || got.Value != "30s" {
		t.Errorf("Get(\"timeout\") = (%+v, %v), want the timeout setting and true", got, found)
	}

	_, found = plan.Get("does-not-exist")
	if found {
		t.Error("Get on a name never resolved reported found=true")
	}
}
