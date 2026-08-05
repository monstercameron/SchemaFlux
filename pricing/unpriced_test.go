package pricing

import (
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

func usage() *types.TokenUsage {
	return &types.TokenUsage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000, TotalTokens: 2_000_000}
}

// An unknown model must report that it is unpriced rather than reporting $0.00,
// which is indistinguishable from a free call.
func TestUnknownModelIsReportedUnpricedNotFree(t *testing.T) {
	cost := CalculateCost(usage(), "some-model-we-have-never-seen", "openai")
	if cost == nil {
		t.Fatal("CalculateCost returned nil")
	}
	if cost.Priced {
		t.Error("Priced must be false for an unknown model")
	}
	if cost.PricingSource != "" {
		t.Errorf("PricingSource = %q, want empty for an unknown model", cost.PricingSource)
	}
	if cost.TotalCost != 0 {
		t.Errorf("TotalCost = %v, want 0 when nothing is known", cost.TotalCost)
	}
}

// The regression that motivated this: every unrecognised Anthropic model was
// priced at claude-3-haiku's rates. An Opus call was reported at roughly 1/60th
// of its real cost, as a precise figure.
func TestUnknownAnthropicModelIsNotPricedAsHaiku(t *testing.T) {
	haiku := CalculateCost(usage(), "claude-3-haiku", "anthropic")
	if !haiku.Priced {
		t.Fatal("claude-3-haiku must be priced; the fixture is wrong")
	}

	unknown := CalculateCost(usage(), "claude-4-opus-not-in-table", "anthropic")
	if unknown.Priced {
		t.Fatal("an unrecognised Anthropic model must not be priced")
	}
	if unknown.TotalCost == haiku.TotalCost && haiku.TotalCost != 0 {
		t.Fatal("unrecognised model was priced at claude-3-haiku's rates")
	}
}

// A known model still reports real figures and names its source.
func TestKnownModelReportsSource(t *testing.T) {
	cost := CalculateCost(usage(), "gpt-4", "openai")
	if !cost.Priced {
		t.Fatal("gpt-4 must be priced")
	}
	if cost.PricingSource != "gpt-4" {
		t.Errorf("PricingSource = %q, want gpt-4", cost.PricingSource)
	}
	if cost.TotalCost <= 0 {
		t.Errorf("TotalCost = %v, want a positive figure", cost.TotalCost)
	}
}

// The gpt-5.6 family has no published rates in the table yet. It must therefore
// report unpriced -- not zero, and not another model's rates. When TODOS.md
// PR-002 adds the entries this test is expected to fail and be updated.
func TestGPT56FamilyIsUnpricedUntilRatesAreKnown(t *testing.T) {
	if _, known := pricingModels["gpt-5.6-luna"]; known {
		t.Skip("the gpt-5.6 family now has rates; update this test alongside TODOS.md PR-002")
	}
	for _, model := range []string{"gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra"} {
		cost := CalculateCost(usage(), model, "openai")
		if cost.Priced {
			t.Errorf("%s: reported as priced, but no rates exist for it", model)
		}
		if cost.TotalCost != 0 {
			t.Errorf("%s: unpriced model reported a non-zero cost %v", model, cost.TotalCost)
		}
	}
}

// Dated snapshots must still resolve to their base model's rates -- that is
// what prefix matching is for.
func TestDatedSnapshotsStillPriceFromBaseModel(t *testing.T) {
	// Snapshots that carry their own table entry resolve exactly; these are
	// deliberately dates with no entry, so only the prefix path can price them.
	for _, tc := range []struct{ model, wantSource string }{
		{"gpt-5-2099-01-01", "gpt-5"},
		{"gpt-5-mini-2099-01-01", "gpt-5-mini"},
		{"gpt-5-nano-2099-01-01", "gpt-5-nano"},
	} {
		cost := CalculateCost(usage(), tc.model, "openai")
		if !cost.Priced {
			t.Errorf("%s: snapshot must price from %s", tc.model, tc.wantSource)
			continue
		}
		if cost.PricingSource != tc.wantSource {
			t.Errorf("%s: PricingSource = %q, want %q", tc.model, cost.PricingSource, tc.wantSource)
		}
	}
}

// A minor-version bump is a different model, never a snapshot.
func TestVersionBumpIsNotTreatedAsSnapshot(t *testing.T) {
	if isSnapshotOf("gpt-5.6-luna", "gpt-5") {
		t.Error("gpt-5.6-luna must not be treated as a snapshot of gpt-5")
	}
	if !isSnapshotOf("gpt-5-2025-08-07", "gpt-5") {
		t.Error("gpt-5-2025-08-07 is a snapshot of gpt-5")
	}
	if !isSnapshotOf("gpt-5", "gpt-5") {
		t.Error("a model is a snapshot of itself")
	}
}

// A priced model's arithmetic must be checkable by hand, not merely non-zero.
func TestPricedCostArithmeticIsCorrect(t *testing.T) {
	model, ok := pricingModels["gpt-4"]
	if !ok {
		t.Skip("gpt-4 not in the table")
	}

	use := &types.TokenUsage{PromptTokens: 1000, CompletionTokens: 2000, TotalTokens: 3000}
	cost := CalculateCost(use, "gpt-4", "openai")

	wantPrompt := 1000 * model.PricePerPromptToken / 1000.0
	wantCompletion := 2000 * model.PricePerCompletionToken / 1000.0

	if cost.PromptCost != wantPrompt {
		t.Errorf("PromptCost = %v, want %v", cost.PromptCost, wantPrompt)
	}
	if cost.CompletionCost != wantCompletion {
		t.Errorf("CompletionCost = %v, want %v", cost.CompletionCost, wantCompletion)
	}
	if cost.TotalCost != wantPrompt+wantCompletion {
		t.Errorf("TotalCost = %v, want the sum %v", cost.TotalCost, wantPrompt+wantCompletion)
	}
}

// Zero usage is free, and must still be reported as priced so the caller can
// tell "nothing was used" from "we do not know the rate".
func TestZeroUsageOnPricedModelIsFreeButPriced(t *testing.T) {
	cost := CalculateCost(&types.TokenUsage{}, "gpt-4", "openai")
	if !cost.Priced {
		t.Error("a known model must report Priced even at zero usage")
	}
	if cost.TotalCost != 0 {
		t.Errorf("TotalCost = %v, want 0", cost.TotalCost)
	}
}

// Nil usage must not panic.
func TestNilUsageIsHandled(t *testing.T) {
	if cost := CalculateCost(nil, "gpt-4", "openai"); cost != nil {
		t.Errorf("nil usage should produce a nil cost, got %+v", cost)
	}
}

// Every provider the library ships must resolve predictably: either priced with
// a named source, or explicitly unpriced. Never a confident zero.
func TestEveryProviderResolvesPredictably(t *testing.T) {
	for _, provider := range []string{
		"openai", "anthropic", "openrouter", "cerebras",
		"deepseek", "qwen", "zai", "local",
	} {
		cost := CalculateCost(usage(), "some-unknown-model-for-"+provider, provider)
		if cost.Priced {
			t.Errorf("%s: an unknown model must not be priced", provider)
		}
		if cost.TotalCost != 0 {
			t.Errorf("%s: unpriced model reported cost %v", provider, cost.TotalCost)
		}
		if cost.PricingSource != "" {
			t.Errorf("%s: unpriced model named a source %q", provider, cost.PricingSource)
		}
	}
}

// Cached and reasoning tokens must be priced when the model publishes a rate,
// and must never silently inflate a total when it does not.
func TestCachedAndReasoningTokensArePricedOnlyWhenRatesExist(t *testing.T) {
	use := &types.TokenUsage{
		PromptTokens: 1000, CompletionTokens: 1000, TotalTokens: 2000,
		CachedTokens: 500, ReasoningTokens: 250,
	}
	cost := CalculateCost(use, "gpt-4", "openai")

	model := pricingModels["gpt-4"]
	if model.PriceCachedToken > 0 && cost.CachedCost == 0 {
		t.Error("a published cached rate must produce a cached cost")
	}
	if model.PriceCachedToken == 0 && cost.CachedCost != 0 {
		t.Error("no published cached rate must produce no cached cost")
	}
	if model.PriceReasoningToken == 0 && cost.ReasoningCost != 0 {
		t.Error("no published reasoning rate must produce no reasoning cost")
	}
}

// Case and whitespace must not change whether a model is recognised.
func TestModelLookupToleratesCaseAndWhitespace(t *testing.T) {
	for _, spelling := range []string{"gpt-4", "GPT-4", "  gpt-4  ", "Gpt-4"} {
		if cost := CalculateCost(usage(), spelling, "openai"); !cost.Priced {
			t.Errorf("%q was not recognised as gpt-4", spelling)
		}
	}
}
