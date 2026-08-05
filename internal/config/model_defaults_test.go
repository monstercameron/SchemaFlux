package config

import (
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// Every OpenAI tier must resolve to a model that actually exists. The previous
// defaults (gpt-5.4 / gpt-5-mini / gpt-5-nano) predate the gpt-5.6 family.
func TestGetModelUsesGPT56FamilyForOpenAI(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "")
	t.Setenv("SCHEMAFLUX_MODEL_SMART", "")
	t.Setenv("SCHEMAFLUX_MODEL_FAST", "")
	t.Setenv("SCHEMAFLUX_MODEL_QUICK", "")

	for _, tc := range []struct {
		name  string
		speed types.Speed
	}{
		{"smart", types.Smart},
		{"fast", types.Fast},
		{"quick", types.Quick},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := GetModel(tc.speed, "openai")
			if !strings.HasPrefix(got, "gpt-5.6-") {
				t.Fatalf("GetModel(%s) = %q, want a gpt-5.6-* model", tc.name, got)
			}
		})
	}
}

// The tier constants are deliberately identical until TODOS.md P-013 measures
// the family. This test documents that as a decision rather than an oversight:
// when the split lands, it is expected to fail and be updated in the same
// commit.
func TestModelTierDefaultsAreDeliberatelyUnsplit(t *testing.T) {
	if ModelDefaultSmart != ModelDefaultFast || ModelDefaultFast != ModelDefaultQuick {
		t.Skip("tiers have been split; update this test alongside TODOS.md P-013")
	}
	if ModelDefaultSmart != "gpt-5.6-luna" {
		t.Errorf("ModelDefaultSmart = %q, want gpt-5.6-luna", ModelDefaultSmart)
	}
}

// An explicit override must still win over the tier default, otherwise a caller
// has no way to reach sol or terra before the split.
func TestExplicitModelOverrideBeatsTierDefault(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "")
	t.Setenv("SCHEMAFLUX_MODEL_SMART", "gpt-5.6-sol")

	if got := GetModel(types.Smart, "openai"); got != "gpt-5.6-sol" {
		t.Fatalf("GetModel(Smart) = %q, want the SCHEMAFLUX_MODEL_SMART override", got)
	}
}
