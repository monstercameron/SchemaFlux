package config

import (
	"os"
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

// The tier assignment is what the live benchmark supports and no more: Quick
// takes terra because it was measurably fastest at no cost in accuracy, and
// Smart and Fast stay on luna because nothing in the measurement separated luna
// from sol. If a future benchmark splits them, this test is where the new
// evidence gets recorded.
func TestTierDefaultsMatchTheMeasurement(t *testing.T) {
	if ModelDefaultQuick != "gpt-5.6-terra" {
		t.Errorf("ModelDefaultQuick = %q, want gpt-5.6-terra (fastest at equal accuracy)", ModelDefaultQuick)
	}
	if ModelDefaultSmart != "gpt-5.6-luna" {
		t.Errorf("ModelDefaultSmart = %q, want gpt-5.6-luna", ModelDefaultSmart)
	}
	if ModelDefaultFast != "gpt-5.6-luna" {
		t.Errorf("ModelDefaultFast = %q, want gpt-5.6-luna", ModelDefaultFast)
	}

	// Smart and Fast being equal is a recorded absence of evidence, not an
	// oversight. When P-014 splits them, this assertion is the thing to change.
	if ModelDefaultSmart != ModelDefaultFast {
		t.Log("Smart and Fast have been split; update the comment in config.go with the measurement that justified it")
	}
}

// Every tier must name a model in the gpt-5.6 family, so a tier can never
// silently resolve to a model the account cannot call.
func TestEveryTierNamesAKnownModel(t *testing.T) {
	known := map[string]struct{}{
		"gpt-5.6-luna":  {},
		"gpt-5.6-sol":   {},
		"gpt-5.6-terra": {},
	}

	for _, tc := range []struct {
		name  string
		model string
	}{
		{"smart", ModelDefaultSmart},
		{"fast", ModelDefaultFast},
		{"quick", ModelDefaultQuick},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := known[tc.model]; !ok {
				t.Errorf("the %s tier resolves to %q, which is not a verified gpt-5.6 model", tc.name, tc.model)
			}
		})
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

// The global override must beat every tier, and the per-tier override must beat
// the default. These are the only ways to reach sol or terra before the split.
func TestModelOverridePrecedence(t *testing.T) {
	cases := []struct {
		name   string
		env    map[string]string
		speed  types.Speed
		expect string
	}{
		{"global_beats_all", map[string]string{"SCHEMAFLUX_MODEL": "gpt-5.6-terra"}, types.Smart, "gpt-5.6-terra"},
		{"global_beats_tier", map[string]string{
			"SCHEMAFLUX_MODEL":       "gpt-5.6-terra",
			"SCHEMAFLUX_MODEL_SMART": "gpt-5.6-sol",
		}, types.Smart, "gpt-5.6-terra"},
		{"tier_smart", map[string]string{"SCHEMAFLUX_MODEL_SMART": "gpt-5.6-sol"}, types.Smart, "gpt-5.6-sol"},
		{"tier_fast", map[string]string{"SCHEMAFLUX_MODEL_FAST": "gpt-5.6-terra"}, types.Fast, "gpt-5.6-terra"},
		{"tier_quick", map[string]string{"SCHEMAFLUX_MODEL_QUICK": "gpt-5.6-sol"}, types.Quick, "gpt-5.6-sol"},
		{"tier_does_not_cross", map[string]string{"SCHEMAFLUX_MODEL_SMART": "gpt-5.6-sol"}, types.Quick, ModelDefaultQuick},
		{"no_override", nil, types.Smart, ModelDefaultSmart},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, name := range []string{"SCHEMAFLUX_MODEL", "SCHEMAFLUX_MODEL_SMART", "SCHEMAFLUX_MODEL_FAST", "SCHEMAFLUX_MODEL_QUICK"} {
				t.Setenv(name, "")
				os.Unsetenv(name)
			}
			for name, value := range tc.env {
				t.Setenv(name, value)
			}
			if got := GetModel(tc.speed, "openai"); got != tc.expect {
				t.Errorf("GetModel = %q, want %q", got, tc.expect)
			}
		})
	}
}

// Providers with their own mapping must not receive an OpenAI model ID.
func TestNonOpenAIProvidersGetTheirOwnModels(t *testing.T) {
	for _, name := range []string{"SCHEMAFLUX_MODEL", "SCHEMAFLUX_MODEL_SMART", "SCHEMAFLUX_MODEL_FAST", "SCHEMAFLUX_MODEL_QUICK"} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}

	for _, provider := range []string{"anthropic", "openrouter", "cerebras"} {
		got := GetModel(types.Smart, provider)
		if strings.HasPrefix(got, "gpt-5.6") {
			t.Errorf("%s received the OpenAI default %q", provider, got)
		}
		if got == "" {
			t.Errorf("%s produced no model", provider)
		}
	}
}

// Temperature and token ceilings must be defined for every tier.
func TestTierParametersAreDefinedForEveryTier(t *testing.T) {
	for _, speed := range []types.Speed{types.Smart, types.Fast, types.Quick} {
		if got := GetMaxTokens(speed); got <= 0 {
			t.Errorf("GetMaxTokens(%v) = %d, want a positive ceiling", speed, got)
		}
	}
	for _, mode := range []types.Mode{types.Strict, types.TransformMode, types.Creative} {
		if got := GetTemperature(mode); got < 0 || got > 2 {
			t.Errorf("GetTemperature(%v) = %v, out of range", mode, got)
		}
	}
	if GetTemperature(types.Strict) >= GetTemperature(types.Creative) {
		t.Error("Strict must be less random than Creative")
	}
}
