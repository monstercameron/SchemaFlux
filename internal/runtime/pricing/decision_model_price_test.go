package pricing

import (
	"math"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// The decision model's price: $0.042 per million input tokens, nothing for the
// answer, under the name a request asks for and the name the API answers under.
func TestTheDecisionModelIsPricedAtItsInputRate(t *testing.T) {
	cases := []struct {
		name     string
		model    string
		provider string
		usage    types.TokenUsage
		priced   bool
		total    float64
		quality  types.PricingQuality
	}{
		{"a million input tokens under the alias", "jev-latest", "jev", types.TokenUsage{PromptTokens: 1_000_000, TotalTokens: 1_000_000}, true, 0.042, types.PricingExact},
		{"the version the API answers under", "jev-1.13.0", "jev", types.TokenUsage{PromptTokens: 1_000_000, TotalTokens: 1_000_000}, true, 0.042, types.PricingExact},
		{"the answer's tokens are not billed", "jev-latest", "jev", types.TokenUsage{PromptTokens: 0, CompletionTokens: 1_000_000, TotalTokens: 1_000_000}, true, 0, types.PricingExact},
		{"a decision of the size the study measured", "jev-1.13.0", "jev", types.TokenUsage{PromptTokens: 430, CompletionTokens: 60, TotalTokens: 490}, true, 430 * 0.042 / 1_000_000, types.PricingExact},
		{"cached tokens have no rate and are priced as input", "jev-latest", "jev", types.TokenUsage{PromptTokens: 1000, CachedTokens: 800, TotalTokens: 1000}, true, 1000 * 0.042 / 1_000_000, types.PricingExact},
		{"in capitals and with space", "  JEV-LATEST ", "jev", types.TokenUsage{PromptTokens: 2_000_000, TotalTokens: 2_000_000}, true, 0.084, types.PricingExact},
		{"through the LiteLLM proxy it is the same model", "jev-1.13.0", "jev-litellm", types.TokenUsage{PromptTokens: 1_000_000, TotalTokens: 1_000_000}, true, 0.042, types.PricingExact},
		{"a later version has no entry and is not guessed", "jev-1.14.0", "jev", types.TokenUsage{PromptTokens: 1_000_000, TotalTokens: 1_000_000}, false, 0, types.PricingUnknown},
		{"the preview has no entry", "jev-preview", "jev", types.TokenUsage{PromptTokens: 1_000_000, TotalTokens: 1_000_000}, false, 0, types.PricingUnknown},
		{"a gateway's name for it has no entry", "typesafe/jev-1.13-20260917", "jev-openrouter", types.TokenUsage{PromptTokens: 1_000_000, TotalTokens: 1_000_000}, false, 0, types.PricingUnknown},
		{"the bare family name is not a model", "jev", "jev", types.TokenUsage{PromptTokens: 1_000_000, TotalTokens: 1_000_000}, false, 0, types.PricingUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			usage := tc.usage
			cost := CalculateCost(&usage, tc.model, tc.provider)
			if cost == nil {
				t.Fatal("no cost at all")
			}
			if cost.Priced != tc.priced || cost.Quality != tc.quality {
				t.Errorf("priced %v and quality %q, want %v and %q", cost.Priced, cost.Quality, tc.priced, tc.quality)
			}
			if math.Abs(cost.TotalCost-tc.total) > 1e-12 {
				t.Errorf("cost %v, want %v", cost.TotalCost, tc.total)
			}
			if cost.CompletionCost != 0 {
				t.Errorf("the answer was billed %v", cost.CompletionCost)
			}
		})
	}
}

// A call through each of the three presets is priced, under the name the preset
// sends and under the names the endpoint can answer with. OpenRouter's model is
// an alias ("~typesafe/jev-latest"), and it answers as the version it resolved
// to, with or without the vendor in front; none of those names is TypeSafe's own.
func TestACallThroughEachDecisionPresetIsPriced(t *testing.T) {
	presets := []llm.SystemOnePreset{llm.SystemOneTypeSafe, llm.SystemOneOpenRouter, llm.SystemOneLiteLLM}
	answeredAs := map[string][]string{
		"jev":            {"jev-latest", "jev-1.13.0"},
		"jev-openrouter": {"~typesafe/jev-latest", "typesafe/jev-latest", "typesafe/jev-1.13.0"},
		"jev-litellm":    {"jev-latest", "jev-1.13.0"},
	}
	for _, preset := range presets {
		for _, name := range answeredAs[preset.Name] {
			t.Run(preset.Name+" as "+name, func(t *testing.T) {
				usage := types.TokenUsage{PromptTokens: 1_000_000, CompletionTokens: 50_000, TotalTokens: 1_050_000}
				cost := CalculateCost(&usage, name, preset.Name)
				if cost == nil || !cost.Priced || cost.Quality != types.PricingExact {
					t.Fatalf("the call is not priced: %+v", cost)
				}
				if math.Abs(cost.TotalCost-0.042) > 1e-12 || cost.CompletionCost != 0 {
					t.Errorf("the call cost %v (answer %v), want $0.042 for the input and nothing for the answer", cost.TotalCost, cost.CompletionCost)
				}
			})
		}
	}

	// The preset's own model is priced whatever it is changed to in the table of
	// presets: a new default for a preset without a price would leave its calls
	// unpriced, and this is where that is found.
	for _, preset := range presets {
		if _, ok := lookupPricingModel(preset.Model); !ok {
			t.Errorf("the model %q that %s sends has no price", preset.Model, preset.Name)
		}
	}
}

// Names near the OpenRouter ones that are not the model are not priced as it.
func TestNamesNearTheOpenRouterAliasAreNotPriced(t *testing.T) {
	for _, name := range []string{
		"~typesafe/jev-mini-latest", "typesafe/jevx-1.13.0", "~other/jev-latest", "other/jev-latest",
		"~typesafe/jev-1.13.0", "typesafe/jev", "~typesafe/jev",
	} {
		t.Run(name, func(t *testing.T) {
			usage := types.TokenUsage{PromptTokens: 1000, TotalTokens: 1000}
			if cost := CalculateCost(&usage, name, "jev-openrouter"); cost == nil || cost.Priced {
				t.Errorf("%q was priced: %+v", name, cost)
			}
		})
	}
}
