package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// TestValidateModelResolutionAcceptsEveryMappedProvider is the positive half:
// a provider with a row resolves at all three tiers, and so does OpenAI, whose
// models come from the tier constants rather than the map.
func TestValidateModelResolutionAcceptsEveryMappedProvider(t *testing.T) {
	clearModelEnv(t)

	for _, provider := range KnownProviders() {
		t.Run(provider, func(t *testing.T) {
			if err := ValidateModelResolution(provider); err != nil {
				t.Fatalf("ValidateModelResolution(%q) = %v, want nil", provider, err)
			}
		})
	}

	// The empty name means "the default provider", which is OpenAI.
	if err := ValidateModelResolution(""); err != nil {
		t.Fatalf("ValidateModelResolution(\"\") = %v, want nil", err)
	}
}

// TestValidateModelResolutionRejectsUnmappedProviders is the failure this task
// exists for: WithProvider("qwen") used to be reported as configured, and the
// first operation to run discovered there was no model for it.
func TestValidateModelResolutionRejectsUnmappedProviders(t *testing.T) {
	clearModelEnv(t)

	for _, provider := range []string{
		"qwen",
		"zai",
		"ollama",
		"my-internal-gateway",
		"totally-made-up",
		"OpenAI-Compatible-Thing",
	} {
		t.Run(provider, func(t *testing.T) {
			err := ValidateModelResolution(provider)
			if err == nil {
				t.Fatalf("ValidateModelResolution(%q) = nil, want an error", provider)
			}
			if !errors.Is(err, ErrNoModelMapping) {
				t.Errorf("errors.Is(err, ErrNoModelMapping) = false; err = %v", err)
			}
			if !strings.Contains(err.Error(), provider) {
				t.Errorf("error does not name the provider: %v", err)
			}
			// The caller needs to be told the way out, not just the problem.
			if !strings.Contains(err.Error(), "SCHEMAFLUX_MODEL") {
				t.Errorf("error does not name the variable to set: %v", err)
			}
			for _, tier := range []string{"Smart", "Fast", "Quick"} {
				if !strings.Contains(err.Error(), tier) {
					t.Errorf("error does not name the %s tier as unresolved: %v", tier, err)
				}
			}
		})
	}
}

// TestGlobalModelOverrideRescuesAnUnmappedProvider — a caller pointing the
// library at their own gateway is a supported configuration, and setting the
// model is how they do it.
func TestGlobalModelOverrideRescuesAnUnmappedProvider(t *testing.T) {
	clearModelEnv(t)
	t.Setenv("SCHEMAFLUX_MODEL", "my-local-model")

	if err := ValidateModelResolution("my-internal-gateway"); err != nil {
		t.Fatalf("ValidateModelResolution = %v, want nil with SCHEMAFLUX_MODEL set", err)
	}
}

// TestPartialTierOverridesStillFail is the case a per-tier check would miss.
// SCHEMAFLUX_MODEL_SMART alone gives a caller one working path and two broken
// ones, and the operation that finds out is whichever runs first — most default
// to Fast.
func TestPartialTierOverridesStillFail(t *testing.T) {
	cases := []struct {
		name    string
		env     string
		missing []string
	}{
		{"smart only", "SCHEMAFLUX_MODEL_SMART", []string{"Fast", "Quick"}},
		{"fast only", "SCHEMAFLUX_MODEL_FAST", []string{"Smart", "Quick"}},
		{"quick only", "SCHEMAFLUX_MODEL_QUICK", []string{"Smart", "Fast"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearModelEnv(t)
			t.Setenv(tc.env, "some-model")

			err := ValidateModelResolution("qwen")
			if err == nil {
				t.Fatalf("ValidateModelResolution = nil, want an error with only %s set", tc.env)
			}
			for _, tier := range tc.missing {
				if !strings.Contains(err.Error(), tier) {
					t.Errorf("error does not name the unresolved %s tier: %v", tier, err)
				}
			}
		})
	}
}

// TestAllThreeTierOverridesSucceed — the complete version of the case above.
func TestAllThreeTierOverridesSucceed(t *testing.T) {
	clearModelEnv(t)
	t.Setenv("SCHEMAFLUX_MODEL_SMART", "big")
	t.Setenv("SCHEMAFLUX_MODEL_FAST", "medium")
	t.Setenv("SCHEMAFLUX_MODEL_QUICK", "small")

	if err := ValidateModelResolution("zai"); err != nil {
		t.Fatalf("ValidateModelResolution = %v, want nil with all three tiers overridden", err)
	}
}

// TestValidateModelResolutionAgreesWithGetModel keeps the check and the thing it
// checks from drifting: validation must fail exactly when GetModel would return
// the empty string that CallLLM turns into an error.
func TestValidateModelResolutionAgreesWithGetModel(t *testing.T) {
	clearModelEnv(t)

	providers := append(KnownProviders(), "qwen", "zai", "made-up", "")
	tiers := []types.Speed{types.Smart, types.Fast, types.Quick}

	for _, provider := range providers {
		anyEmpty := false
		for _, tier := range tiers {
			if GetModel(tier, provider) == "" {
				anyEmpty = true
			}
		}

		err := ValidateModelResolution(provider)
		if anyEmpty && err == nil {
			t.Errorf("provider %q: GetModel returns an empty model but validation passed", provider)
		}
		if !anyEmpty && err != nil {
			t.Errorf("provider %q: every tier resolves but validation failed: %v", provider, err)
		}
	}
}

// TestValidateModelResolutionToleratesSpellings — the same folding GetModel
// applies, so a caller who wrote " DeepSeek " is not told their provider is
// unknown.
func TestValidateModelResolutionToleratesSpellings(t *testing.T) {
	clearModelEnv(t)

	for _, spelling := range []string{"deepseek", "DeepSeek", " DEEPSEEK ", "  cerebras", "Anthropic"} {
		if err := ValidateModelResolution(spelling); err != nil {
			t.Errorf("ValidateModelResolution(%q) = %v, want nil", spelling, err)
		}
	}
}
