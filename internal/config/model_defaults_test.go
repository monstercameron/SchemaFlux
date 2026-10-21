package config

import (
	"os"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Every OpenAI tier must resolve to a model that actually exists. The defaults
// were gpt-5.4 / gpt-5-mini / gpt-5-nano, then the gpt-5.6 family, and moved to
// gpt-6-luna on 2026-10-01 (LX-120).
func TestGetModelUsesGPT6FamilyForOpenAI(t *testing.T) {
	setenvAndReload(t, "SCHEMAFLUX_MODEL", "")
	setenvAndReload(t, "SCHEMAFLUX_MODEL_SMART", "")
	setenvAndReload(t, "SCHEMAFLUX_MODEL_FAST", "")
	setenvAndReload(t, "SCHEMAFLUX_MODEL_QUICK", "")

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
			if !strings.HasPrefix(got, "gpt-6") {
				t.Fatalf("GetModel(%s) = %q, want a gpt-6 model", tc.name, got)
			}
		})
	}
}

// The tier assignment is what has been measured and no more. gpt-6-luna is the
// only model in its family that has been run through this library, so all three
// tiers name it. A split between tiers is a claim that one model is faster or
// better than another, and nothing has compared them. When LX-121 produces that
// measurement, this test is where it gets recorded.
func TestTierDefaultsMatchTheMeasurement(t *testing.T) {
	for name, model := range map[string]string{
		"ModelDefaultSmart": ModelDefaultSmart,
		"ModelDefaultFast":  ModelDefaultFast,
		"ModelDefaultQuick": ModelDefaultQuick,
	} {
		if model != "gpt-6-luna" {
			t.Errorf("%s = %q, want gpt-6-luna; a different model needs a measurement recorded in config.go", name, model)
		}
	}
}

// Every tier must name a model that was on the account when the defaults were
// set, so a tier can never silently resolve to a model that cannot be called.
// Listed by GET /v1/models on 2026-10-01.
func TestEveryTierNamesAKnownModel(t *testing.T) {
	known := map[string]struct{}{
		"gpt-6-luna":  {},
		"gpt-6-sol":   {},
		"gpt-6-astra": {},
		"gpt-6.1-sol": {},
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
				t.Errorf("the %s tier resolves to %q, which is not a model the account listed", tc.name, tc.model)
			}
		})
	}
}

// An explicit override must still win over the tier default, otherwise a caller
// has no way to reach another model in the family.
func TestExplicitModelOverrideBeatsTierDefault(t *testing.T) {
	setenvAndReload(t, "SCHEMAFLUX_MODEL", "")
	setenvAndReload(t, "SCHEMAFLUX_MODEL_SMART", "gpt-6-sol")

	if got := GetModel(types.Smart, "openai"); got != "gpt-6-sol" {
		t.Fatalf("GetModel(Smart) = %q, want the SCHEMAFLUX_MODEL_SMART override", got)
	}
}

// The global override must beat every tier, and the per-tier override must beat
// the default. These are the only ways to reach another model in the family.
func TestModelOverridePrecedence(t *testing.T) {
	cases := []struct {
		name   string
		env    map[string]string
		speed  types.Speed
		expect string
	}{
		{"global_beats_all", map[string]string{"SCHEMAFLUX_MODEL": "gpt-6-astra"}, types.Smart, "gpt-6-astra"},
		{"global_beats_tier", map[string]string{
			"SCHEMAFLUX_MODEL":       "gpt-6-astra",
			"SCHEMAFLUX_MODEL_SMART": "gpt-6-sol",
		}, types.Smart, "gpt-6-astra"},
		{"tier_smart", map[string]string{"SCHEMAFLUX_MODEL_SMART": "gpt-6-sol"}, types.Smart, "gpt-6-sol"},
		{"tier_fast", map[string]string{"SCHEMAFLUX_MODEL_FAST": "gpt-6-astra"}, types.Fast, "gpt-6-astra"},
		{"tier_quick", map[string]string{"SCHEMAFLUX_MODEL_QUICK": "gpt-6-sol"}, types.Quick, "gpt-6-sol"},
		{"tier_does_not_cross", map[string]string{"SCHEMAFLUX_MODEL_SMART": "gpt-6-sol"}, types.Quick, ModelDefaultQuick},
		{"no_override", nil, types.Smart, ModelDefaultSmart},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, name := range []string{"SCHEMAFLUX_MODEL", "SCHEMAFLUX_MODEL_SMART", "SCHEMAFLUX_MODEL_FAST", "SCHEMAFLUX_MODEL_QUICK"} {
				setenvAndReload(t, name, "")
				os.Unsetenv(name)
			}
			for name, value := range tc.env {
				setenvAndReload(t, name, value)
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
		setenvAndReload(t, name, "")
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

// ST-003. GetMaxTokens becomes the DEFAULT ceiling once ops.CallLLM's per-call
// MaxOutputTokens override exists, not the only one -- but a caller who never
// touches the option must see exactly the ceiling they always got. Pinning the
// exact numbers (not just "positive", which TestTierParametersAreDefinedForEveryTier
// already checks) is what catches a refactor that quietly changes what
// "unchanged" means.
func TestGetMaxTokensExactTierDefaultsUnchanged(t *testing.T) {
	cases := []struct {
		name string
		tier types.Speed
		want int
	}{
		{"smart", types.Smart, 4000},
		{"fast", types.Fast, 2000},
		{"quick", types.Quick, 1000},
		{"unset falls back to fast's ceiling", types.TierUnset, 2000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := GetMaxTokens(tc.tier); got != tc.want {
				t.Errorf("GetMaxTokens(%v) = %d, want %d", tc.tier, got, tc.want)
			}
		})
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
