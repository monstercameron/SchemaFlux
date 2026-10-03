package llm

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/config/configtest"
	"github.com/monstercameron/schemaflux/internal/types"
)

// Every built-in provider type, including the ones that promote the method from
// the compatible provider they hold, is a TierModels.
var (
	_ TierModels = (*OpenAIProvider)(nil)
	_ TierModels = (*AnthropicProvider)(nil)
	_ TierModels = (*OpenAICompatibleProvider)(nil)
	_ TierModels = (*OpenRouterProvider)(nil)
	_ TierModels = (*CerebrasProvider)(nil)
	_ TierModels = (*DeepSeekProvider)(nil)
	_ TierModels = (*QwenProvider)(nil)
	_ TierModels = (*ZAIProvider)(nil)
	_ TierModels = (*LocalProvider)(nil)
)

// tierlessProvider is a provider that knows no model for a tier.
type tierlessProvider struct{ name string }

func (tierlessProvider) Complete(context.Context, CompletionRequest) (CompletionResponse, error) {
	return CompletionResponse{}, nil
}
func (p tierlessProvider) Name() string                         { return p.name }
func (tierlessProvider) EstimateCost(CompletionRequest) float64 { return 0 }
func (tierlessProvider) RetryPolicy() (int, time.Duration)      { return 0, 0 }

// tierAnswerer is a provider that answers every tier with one model, or with
// the empty string.
type tierAnswerer struct {
	tierlessProvider
	model string
}

func (p tierAnswerer) ModelFor(types.Speed) string { return p.model }

// tierWrapper is a provider that wraps another and says so, and answers nothing
// itself.
type tierWrapper struct {
	tierlessProvider
	inner Provider
}

func (w tierWrapper) Unwrap() Provider { return w.inner }

// tierWrapperThatAnswers wraps another provider and has a model of its own.
type tierWrapperThatAnswers struct {
	tierAnswerer
	inner Provider
}

func (w tierWrapperThatAnswers) Unwrap() Provider { return w.inner }

type tierSelfWrapper struct{ tierlessProvider }

func (w *tierSelfWrapper) Unwrap() Provider { return w }

func TestModelForLooksThroughWrappers(t *testing.T) {
	t.Parallel()

	bottom := tierAnswerer{model: "bottom-model"}
	other := tierAnswerer{model: "other-model"}
	tests := []struct {
		name      string
		provider  Provider
		wantModel string
		wantOK    bool
	}{
		{"a provider that answers", bottom, "bottom-model", true},
		{"one wrapper over it", tierWrapper{inner: bottom}, "bottom-model", true},
		{"three wrappers over it", tierWrapper{inner: tierWrapper{inner: tierWrapper{inner: bottom}}}, "bottom-model", true},
		{"ten wrappers over it", func() Provider {
			var p Provider = bottom
			for i := 0; i < 10; i++ {
				p = tierWrapper{inner: p}
			}
			return p
		}(), "bottom-model", true},
		{"a provider that knows nothing", tierlessProvider{}, "", false},
		{"a wrapper over a provider that knows nothing", tierWrapper{inner: tierlessProvider{}}, "", false},
		{"a wrapper over nothing", tierWrapper{}, "", false},
		{"nothing", nil, "", false},
		{"the outer one answers first", tierWrapperThatAnswers{tierAnswerer: tierAnswerer{model: "outer-model"}, inner: other}, "outer-model", true},
		{"an outer one that names none is looked past", tierWrapperThatAnswers{inner: other}, "other-model", true},
		{"an answerer that names none", tierAnswerer{}, "", false},
		{"a wrapper that is its own inner is not followed for ever", &tierSelfWrapper{}, "", false},
		{"a built-in provider", mustBuildForTierModels(t, "anthropic"), "claude-3-5-sonnet-20240620", true},
		{"a wrapper over a built-in provider", tierWrapper{inner: mustBuildForTierModels(t, "deepseek")}, "deepseek-reasoner", true},
		{"a built-in provider with no table", mustBuildForTierModels(t, "qwen"), "", false},
		{"a wrapper over a built-in provider with no table", tierWrapper{inner: mustBuildForTierModels(t, "zai")}, "", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			model, ok := ModelFor(test.provider, types.Smart)
			if model != test.wantModel || ok != test.wantOK {
				t.Fatalf("ModelFor = %q, %v; want %q, %v", model, ok, test.wantModel, test.wantOK)
			}
		})
	}
}

func mustBuildForTierModels(t *testing.T, name string) Provider {
	t.Helper()
	provider, err := CreateProvider(name, ProviderConfig{APIKey: "test-key"})
	if err != nil {
		t.Fatalf("CreateProvider(%q): %v", name, err)
	}
	return provider
}

// A built-in provider with no setting of its own answers what the process-wide
// lookup answers for a program with one provider.
func TestBuiltInProvidersAnswerWhatTheProcessWideLookupAnswers(t *testing.T) {
	for _, name := range []string{"SCHEMAFLUX_MODEL", "SCHEMAFLUX_MODEL_SMART", "SCHEMAFLUX_MODEL_FAST", "SCHEMAFLUX_MODEL_QUICK"} {
		configtest.Setenv(t, name, "")
	}

	names := []string{"openai", "anthropic", "openrouter", "cerebras", "deepseek", "qwen", "zai", "local", "mock"}
	tiers := []types.Speed{types.Smart, types.Fast, types.Quick, types.TierUnset, types.Speed(-1), types.Speed(9)}
	for _, name := range names {
		provider := mustBuildForTierModels(t, name)
		for _, tier := range tiers {
			t.Run(fmt.Sprintf("%s/%d", name, tier), func(t *testing.T) {
				want := config.GetModel(tier, provider.Name())
				got, answered := ModelFor(provider, tier)
				if got != want {
					t.Fatalf("ModelFor = %q, config.GetModel = %q", got, want)
				}
				if answered != (want != "") {
					t.Fatalf("answered = %v for %q", answered, want)
				}
				if direct := provider.(TierModels).ModelFor(tier); direct != want {
					t.Fatalf("the method answers %q, want %q", direct, want)
				}
			})
		}
	}
}

func TestCompatibleProviderAnswersForItsOwnName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tier types.Speed
		want string
	}{
		{"groq", types.Smart, "llama-3.3-70b-versatile"},
		{"groq", types.Quick, "llama-3.1-8b-instant"},
		{"Mistral", types.Smart, "mistral-large-latest"},
		{"mistral", types.Fast, "mistral-small-latest"},
		{"my-gateway", types.Smart, ""},
		{"my-gateway", types.Fast, ""},
		{"ollama", types.Quick, ""},
	}
	for _, test := range tests {
		t.Run(fmt.Sprintf("%s/%d", test.name, test.tier), func(t *testing.T) {
			t.Parallel()
			provider, err := NewOpenAICompatibleProvider(test.name, ProviderConfig{APIKey: "k", BaseURL: "https://gateway.example.test/v1"})
			if err != nil {
				t.Fatal(err)
			}
			if got := provider.ModelFor(test.tier); got != test.want {
				t.Fatalf("ModelFor = %q, want %q", got, test.want)
			}
		})
	}
}

func TestTierModelPrefersTheSettingToTheBuiltInTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider string
		setting  [3]string
		tier     types.Speed
		want     string
	}{
		{"no setting, openai", "openai", [3]string{}, types.Smart, config.ModelDefaultSmart},
		{"no setting, anthropic", "anthropic", [3]string{}, types.Fast, "claude-3-haiku-20240307"},
		{"smart set", "openai", [3]string{"s", "", ""}, types.Smart, "s"},
		{"fast set", "openai", [3]string{"", "f", ""}, types.Fast, "f"},
		{"quick set", "openai", [3]string{"", "", "q"}, types.Quick, "q"},
		{"an empty entry is the built-in one: smart", "openai", [3]string{"", "f", "q"}, types.Smart, config.ModelDefaultSmart},
		{"an empty entry is the built-in one: fast", "anthropic", [3]string{"s", "", "q"}, types.Fast, "claude-3-haiku-20240307"},
		{"an empty entry is the built-in one: quick", "deepseek", [3]string{"s", "f", ""}, types.Quick, "deepseek-chat"},
		{"all three set", "cerebras", [3]string{"s", "f", "q"}, types.Quick, "q"},
		{"a provider with no table and a setting", "qwen", [3]string{"qwen-max", "qwen-plus", "qwen-turbo"}, types.Smart, "qwen-max"},
		{"a provider with no table and no setting", "qwen", [3]string{}, types.Smart, ""},
		{"a provider with no table and the setting of another tier", "zai", [3]string{"", "glm-4", ""}, types.Smart, ""},
		{"a tier that is not set has no entry", "openai", [3]string{"s", "f", "q"}, types.TierUnset, config.ModelDefaultFast},
		{"a tier past the end has no entry", "openai", [3]string{"s", "f", "q"}, types.Speed(4), config.ModelDefaultFast},
		{"a negative tier has no entry", "anthropic", [3]string{"s", "f", "q"}, types.Speed(-1), "claude-3-haiku-20240307"},
		{"the name is folded like the table's", " Anthropic ", [3]string{}, types.Smart, "claude-3-5-sonnet-20240620"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := tierModel(test.provider, test.setting, test.tier); got != test.want {
				t.Fatalf("tierModel(%q, %v, %d) = %q, want %q", test.provider, test.setting, test.tier, got, test.want)
			}
		})
	}
}

// ProviderConfig.Models, when it exists, is what ModelFor reads. This test
// sets it by reflection so that it compiles before the field is declared, and
// runs the day it is: a provider built with a model for each tier answers it.
func TestProviderConfigModelsReachModelFor(t *testing.T) {
	field, declared := reflect.TypeOf(ProviderConfig{}).FieldByName("Models")
	if !declared {
		t.Skip("ProviderConfig has no Models field yet")
	}
	if field.Type != reflect.TypeOf([3]string{}) {
		t.Fatalf("ProviderConfig.Models is a %v, want [3]string: smart, fast, quick", field.Type)
	}

	for _, name := range []string{"openai", "anthropic", "openrouter", "cerebras", "deepseek", "qwen", "zai", "local"} {
		t.Run(name, func(t *testing.T) {
			var built ProviderConfig
			built.APIKey = "test-key"
			reflect.ValueOf(&built).Elem().FieldByName("Models").Set(reflect.ValueOf([3]string{"smart-x", "", "quick-x"}))

			provider, err := CreateProvider(name, built)
			if err != nil {
				t.Fatal(err)
			}
			if got, _ := ModelFor(provider, types.Smart); got != "smart-x" {
				t.Errorf("smart = %q", got)
			}
			if got, _ := ModelFor(provider, types.Quick); got != "quick-x" {
				t.Errorf("quick = %q", got)
			}
			// The entry that was left empty is the built-in model.
			wantFast := tierModel(name, [3]string{}, types.Fast)
			if got, _ := ModelFor(provider, types.Fast); got != wantFast {
				t.Errorf("fast = %q, want the built-in %q", got, wantFast)
			}
		})
	}
}
