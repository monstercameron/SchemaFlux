package config

import (
	"os"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

func clearModelEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"SCHEMAFLUX_MODEL", "SCHEMAFLUX_MODEL_SMART", "SCHEMAFLUX_MODEL_FAST", "SCHEMAFLUX_MODEL_QUICK",
	} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
}

// GetModel special-cased three providers and let every other one fall through
// to the OpenAI defaults, so the README's own WithProvider("deepseek") example
// sent model: "gpt-5.4" to api.deepseek.com. No provider may receive another
// provider's model ID.
func TestNoProviderReceivesAnotherProvidersModel(t *testing.T) {
	cases := []struct {
		provider string
		reject   []string
	}{
		{"anthropic", []string{"gpt-", "llama", "deepseek", "mistral"}},
		{"openrouter", []string{"claude-", "llama-3.3-70b-versatile", "deepseek-"}},
		{"cerebras", []string{"gpt-", "claude-", "deepseek-"}},
		{"deepseek", []string{"gpt-", "claude-", "llama"}},
		{"groq", []string{"gpt-", "claude-", "deepseek-"}},
		{"mistral", []string{"gpt-", "claude-", "llama"}},
		{"openai", []string{"claude-", "llama", "deepseek-", "mistral-"}},
	}

	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			clearModelEnv(t)

			for _, speed := range []types.Speed{types.Smart, types.Fast, types.Quick} {
				model := GetModel(speed, tc.provider)
				if model == "" {
					t.Fatalf("%s has a mapping but resolved to nothing at tier %v", tc.provider, speed)
				}
				for _, forbidden := range tc.reject {
					if strings.Contains(model, forbidden) {
						t.Errorf("%s tier %v resolved to %q, which belongs to another provider (contains %q)",
							tc.provider, speed, model, forbidden)
					}
				}
			}
		})
	}
}

// A provider with no mapping gets nothing, not somebody else's model. The empty
// string is the fail-closed signal; CallLLM turns it into an error naming the
// variable to set.
func TestUnmappedProviderResolvesToNothing(t *testing.T) {
	for _, provider := range []string{"qwen", "zai", "my-internal-gateway", "totally-made-up", "ollama"} {
		t.Run(provider, func(t *testing.T) {
			clearModelEnv(t)

			for _, speed := range []types.Speed{types.Smart, types.Fast, types.Quick} {
				if model := GetModel(speed, provider); model != "" {
					t.Errorf("%s at tier %v resolved to %q; an unmapped provider must resolve to nothing",
						provider, speed, model)
				}
			}
		})
	}
}

// And an explicit override rescues it, which is the documented way to use a
// provider the library does not know.
func TestExplicitModelRescuesAnUnmappedProvider(t *testing.T) {
	cases := []struct {
		name  string
		env   string
		speed types.Speed
	}{
		{"global", "SCHEMAFLUX_MODEL", types.Smart},
		{"smart_tier", "SCHEMAFLUX_MODEL_SMART", types.Smart},
		{"fast_tier", "SCHEMAFLUX_MODEL_FAST", types.Fast},
		{"quick_tier", "SCHEMAFLUX_MODEL_QUICK", types.Quick},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearModelEnv(t)
			t.Setenv(tc.env, "qwen-max")

			if got := GetModel(tc.speed, "qwen"); got != "qwen-max" {
				t.Errorf("GetModel = %q, want the override qwen-max", got)
			}
		})
	}
}

// Provider names are matched case-insensitively and with surrounding space
// trimmed, because a caller writes what they write.
func TestProviderNameNormalisation(t *testing.T) {
	clearModelEnv(t)

	canonical := GetModel(types.Fast, "anthropic")
	for _, spelling := range []string{"Anthropic", "ANTHROPIC", "  anthropic  ", "AnThRoPiC"} {
		if got := GetModel(types.Fast, spelling); got != canonical {
			t.Errorf("%q resolved to %q, want %q", spelling, got, canonical)
		}
	}
}

// An empty provider name means the default, which is OpenAI.
func TestEmptyProviderIsOpenAI(t *testing.T) {
	clearModelEnv(t)

	if got := GetModel(types.Quick, ""); got != ModelDefaultQuick {
		t.Errorf("GetModel(Quick, \"\") = %q, want %q", got, ModelDefaultQuick)
	}
}

// HasModelMapping is the question a caller can ask before making a call.
func TestHasModelMapping(t *testing.T) {
	cases := []struct {
		provider string
		want     bool
	}{
		{"openai", true},
		{"", true},
		{"anthropic", true},
		{"deepseek", true},
		{"groq", true},
		{"mistral", true},
		{"openrouter", true},
		{"cerebras", true},
		{"local", true},
		{"qwen", false},
		{"zai", false},
		{"made-up", false},
	}

	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			if got := HasModelMapping(tc.provider); got != tc.want {
				t.Errorf("HasModelMapping(%q) = %v, want %v", tc.provider, got, tc.want)
			}
		})
	}
}

// KnownProviders is what the error message lists, so it has to be complete and
// stable.
func TestKnownProviders(t *testing.T) {
	names := KnownProviders()

	if len(names) < 8 {
		t.Fatalf("KnownProviders returned %d entries: %v", len(names), names)
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Errorf("KnownProviders is not sorted: %v", names)
			break
		}
	}

	seen := map[string]struct{}{}
	for _, name := range names {
		if _, duplicate := seen[name]; duplicate {
			t.Errorf("%q appears twice", name)
		}
		seen[name] = struct{}{}
		if !HasModelMapping(name) {
			t.Errorf("%q is listed but has no mapping", name)
		}
	}
	if _, ok := seen["openai"]; !ok {
		t.Error("openai is missing from KnownProviders")
	}
}

// Every mapped provider must define all three tiers, or a tier silently falls
// back to another one.
func TestEveryMappingCoversEveryTier(t *testing.T) {
	for provider, models := range providerModels {
		t.Run(provider, func(t *testing.T) {
			for _, speed := range []types.Speed{types.Smart, types.Fast, types.Quick} {
				if models[speed] == "" {
					t.Errorf("%s has no model for tier %v", provider, speed)
				}
			}
		})
	}
}
