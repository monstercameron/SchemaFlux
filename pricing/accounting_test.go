package pricing

import (
	"math"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// TI-010. Two properties, and the first one is the one that matters: an
// unpriced model must report unpriced, never $0.00. Six of eight providers had
// no pricing entry and reported zero, which reads as free.
func TestUnpricedModelsReportUnpricedRatherThanZero(t *testing.T) {
	usage := &types.TokenUsage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500}

	unpriced := []string{
		"gpt-5.6-luna",
		"gemma-4-31b-preview",
		"some-model-nobody-has-heard-of",
		"deepseek-reasoner",
		"llama-3.3-70b-versatile",
		"mistral-large-latest",
	}

	for _, model := range unpriced {
		t.Run(model, func(t *testing.T) {
			cost := CalculateCost(usage, model, "someprovider")
			if cost == nil {
				t.Fatal("CalculateCost returned nil")
			}
			if cost.Priced {
				t.Skipf("%s has a pricing entry now; this case is about the unpriced path", model)
			}
			if cost.TotalCost != 0 {
				t.Errorf("an unpriced model reported a cost of %v", cost.TotalCost)
			}
			// Zero with Priced=false is "unknown". Zero with Priced=true would
			// be "free", and the two must not be confusable.
			if cost.PricingSource != "" {
				t.Errorf("an unpriced model named a pricing source: %q", cost.PricingSource)
			}
		})
	}
}

// The arithmetic, against a figure computed by hand. Prices are per 1K tokens.
func TestPricedModelArithmeticMatchesAHandComputedFigure(t *testing.T) {
	// A model priced at exactly $1.00 and $2.00 per million, registered here so
	// the test does not depend on a real rate card that will change.
	const model = "test-model-for-arithmetic"
	pricingModels[model] = PricingModel{
		Provider:                "test",
		Model:                   model,
		PricePerPromptToken:     0.001, // $1.00 per 1M
		PricePerCompletionToken: 0.002, // $2.00 per 1M
		Currency:                "USD",
		EffectiveDate:           time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	t.Cleanup(func() { delete(pricingModels, model) })

	usage := &types.TokenUsage{
		PromptTokens:     1_000_000,
		CompletionTokens: 500_000,
		TotalTokens:      1_500_000,
	}

	cost := CalculateCost(usage, model, "test")
	if !cost.Priced {
		t.Fatal("a registered model reported unpriced")
	}

	// 1,000,000 prompt tokens at $1.00/1M = $1.00
	// 500,000 completion tokens at $2.00/1M = $1.00
	const wantPrompt, wantCompletion, wantTotal = 1.0, 1.0, 2.0

	if !closeEnough(cost.PromptCost, wantPrompt) {
		t.Errorf("PromptCost = %v, want %v", cost.PromptCost, wantPrompt)
	}
	if !closeEnough(cost.CompletionCost, wantCompletion) {
		t.Errorf("CompletionCost = %v, want %v", cost.CompletionCost, wantCompletion)
	}
	if !closeEnough(cost.TotalCost, wantTotal) {
		t.Errorf("TotalCost = %v, want %v", cost.TotalCost, wantTotal)
	}
	if cost.PricingSource != model {
		t.Errorf("PricingSource = %q, want the model it was priced from", cost.PricingSource)
	}
}

// Cached and reasoning tokens bill differently and are counted separately, or
// a reasoning model's cost is wrong by however much it thought.
func TestCachedAndReasoningTokensAreCostedSeparately(t *testing.T) {
	const model = "test-model-with-all-token-kinds"
	pricingModels[model] = PricingModel{
		Provider:                "test",
		Model:                   model,
		PricePerPromptToken:     0.001,
		PricePerCompletionToken: 0.002,
		PriceCachedToken:        0.0001, // a tenth of the prompt rate
		PriceReasoningToken:     0.002,
		Currency:                "USD",
	}
	t.Cleanup(func() { delete(pricingModels, model) })

	cost := CalculateCost(&types.TokenUsage{
		PromptTokens:     100_000,
		CompletionTokens: 10_000,
		CachedTokens:     900_000,
		ReasoningTokens:  50_000,
		TotalTokens:      1_060_000,
	}, model, "test")

	if !closeEnough(cost.CachedCost, 0.09) {
		t.Errorf("CachedCost = %v, want 0.09", cost.CachedCost)
	}
	if !closeEnough(cost.ReasoningCost, 0.10) {
		t.Errorf("ReasoningCost = %v, want 0.10", cost.ReasoningCost)
	}
	if !closeEnough(cost.TotalCost, 0.1+0.02+0.09+0.10) {
		t.Errorf("TotalCost = %v", cost.TotalCost)
	}

	// The point of separating them: charging cached tokens at the prompt rate
	// would be nine times too much here.
	if closeEnough(cost.CachedCost, 0.9) {
		t.Error("cached tokens were charged at the full prompt rate")
	}
}

// A model with no cached-token rate does not silently charge zero for them: it
// charges nothing because there is no rate, and the total says so by being the
// sum of what was priced.
func TestMissingRatesDoNotInventANumber(t *testing.T) {
	const model = "test-model-without-cache-pricing"
	pricingModels[model] = PricingModel{
		Provider:                "test",
		Model:                   model,
		PricePerPromptToken:     0.001,
		PricePerCompletionToken: 0.002,
		Currency:                "USD",
	}
	t.Cleanup(func() { delete(pricingModels, model) })

	cost := CalculateCost(&types.TokenUsage{
		PromptTokens: 1_000_000,
		CachedTokens: 1_000_000,
		TotalTokens:  2_000_000,
	}, model, "test")

	if cost.CachedCost != 0 {
		t.Errorf("CachedCost = %v with no cached rate configured", cost.CachedCost)
	}
	if !closeEnough(cost.TotalCost, 1.0) {
		t.Errorf("TotalCost = %v, want just the priced portion", cost.TotalCost)
	}
}

// A dated snapshot prices from its base model; a version bump does not. That
// distinction is PR-007, and it is the difference between pricing
// gpt-5-2025-08-07 correctly and pricing gpt-5.6-luna as gpt-5.
func TestSnapshotsPriceFromTheBaseAndVersionBumpsDoNot(t *testing.T) {
	base, ok := lookupPricingModel("gpt-5")
	if !ok {
		t.Skip("gpt-5 is not in the table; this case is about the prefix ladder")
	}

	snapshot, ok := lookupPricingModel("gpt-5-2026-01-15")
	if !ok {
		t.Error("a dated snapshot of gpt-5 did not resolve to its base model")
	} else if snapshot.PricePerPromptToken != base.PricePerPromptToken {
		t.Errorf("the snapshot priced at %v, the base at %v",
			snapshot.PricePerPromptToken, base.PricePerPromptToken)
	}

	// gpt-5.6-luna begins with "gpt-5" and is a different model. This used to
	// be proven by its absence from the table, which stopped proving anything
	// the moment it acquired real published rates -- absence and correctness
	// had been the same observation, and only one of them was the point.
	//
	// Asserted directly now: it resolves, and to its OWN rates rather than the
	// base model's. A prefix ladder that walked "gpt-5.6-luna" back to "gpt-5"
	// would price it at six times its input rate and ten times its output, and
	// that is the failure this case exists for.
	luna, ok := lookupPricingModel("gpt-5.6-luna")
	if !ok {
		t.Error("gpt-5.6-luna did not resolve at all; it has its own entry")
	} else if luna.PricePerPromptToken == base.PricePerPromptToken {
		t.Errorf("gpt-5.6-luna priced at gpt-5's rate (%v); a version bump is a different model",
			luna.PricePerPromptToken)
	}
}

// Nil usage is not an error and not a zero cost: there is nothing to price.
func TestNilUsageProducesNoCost(t *testing.T) {
	if cost := CalculateCost(nil, "any-model", "any-provider"); cost != nil {
		t.Errorf("CalculateCost(nil) = %+v, want nil", cost)
	}
}

// The recorded cost is what CalculateCost produced, so a summary of unpriced
// calls does not read as a summary of free ones.
func TestSummaryOfUnpricedCallsIsNotASummaryOfFreeOnes(t *testing.T) {
	resetAll(t)

	cost := CalculateCost(&types.TokenUsage{PromptTokens: 1000, TotalTokens: 1000},
		"a-model-with-no-rate-card", "someprovider")
	if cost.Priced {
		t.Skip("that model has a rate now")
	}

	TrackCost(cost, &types.ResultMetadata{RequestID: "req-1", EndTime: time.Now()})

	record, ok := GetRequestCost("req-1")
	if !ok {
		t.Fatal("the record was not stored")
	}
	if record.Cost.Priced {
		t.Error("an unpriced call was recorded as priced")
	}
	if summary := GetCostSummary(time.Time{}, nil); summary.TotalCost != 0 {
		t.Errorf("TotalCost = %v; an unpriced call contributed a number", summary.TotalCost)
	}
}

func closeEnough(got, want float64) bool {
	return math.Abs(got-want) < 1e-9
}
