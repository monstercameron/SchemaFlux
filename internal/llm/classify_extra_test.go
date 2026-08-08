package llm

import (
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// TestKindForStatusEveryBranch pins every named branch in kindForStatus,
// including the two status codes (409, 422) the existing
// TestClassifyMapsProviderFailures table does not exercise, and the two
// numeric-range fallbacks with a status that is neither obviously 4xx-named
// nor 5xx-named (e.g. 300).
func TestKindForStatusEveryBranch(t *testing.T) {
	cases := []struct {
		status int
		want   types.ErrorKind
	}{
		{401, types.KindAuthentication},
		{403, types.KindPermission},
		{429, types.KindRateLimited},
		{413, types.KindContextTooLarge},
		{408, types.KindTimeout},
		{504, types.KindTimeout},
		{500, types.KindProviderUnavailable},
		{502, types.KindProviderUnavailable},
		{503, types.KindProviderUnavailable},
		{409, types.KindProviderUnavailable}, // named alongside 500/502/503
		{400, types.KindInvalidRequest},
		{422, types.KindInvalidRequest}, // named alongside 400/404
		{404, types.KindInvalidRequest},
		{599, types.KindProviderUnavailable}, // falls through to the >=500 range check
		{499, types.KindInvalidRequest},      // falls through to the >=400 range check
		{300, types.KindUnknown},             // below the >=400 range check entirely
		{0, types.KindUnknown},
	}

	for _, tc := range cases {
		if got := kindForStatus(tc.status); got != tc.want {
			t.Errorf("kindForStatus(%d) = %v, want %v", tc.status, got, tc.want)
		}
	}
}

// TestKindFromTextEveryBranch calls the fallback classifier directly, so
// every phrase it recognises is pinned regardless of which caller
// (context-wrapped or bare) currently reaches it through Classify.
func TestKindFromTextEveryBranch(t *testing.T) {
	cases := []struct {
		name    string
		message string
		want    types.ErrorKind
	}{
		{"no provider configured", "no LLM provider configured: call Init", types.KindConfiguration},
		{"no default model", `no default model for provider "qwen"`, types.KindConfiguration},
		{"no model mapping", "provider has no model mapping for this tier", types.KindConfiguration},
		{"deadline exceeded text", "operation failed: context deadline exceeded", types.KindTimeout},
		{"canceled text", "operation failed: context canceled", types.KindCanceled},
		{"budget", "monthly budget exceeded for this account", types.KindBudgetExceeded},
		{"budget case insensitive", "BUDGET limit hit", types.KindBudgetExceeded},
		{"unrecognised", "the printer is on fire", types.KindUnknown},
		{"empty", "", types.KindUnknown},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := kindFromText(tc.message); got != tc.want {
				t.Errorf("kindFromText(%q) = %v, want %v", tc.message, got, tc.want)
			}
		})
	}
}
