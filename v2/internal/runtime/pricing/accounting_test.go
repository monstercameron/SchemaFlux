package pricing

import (
	"math"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// TI-010. Two properties, and the first one is the one that matters: an
// unpriced model must report unpriced, never $0.00. Six of eight providers had
// no pricing entry and reported zero, which reads as free.
func TestUnpricedModelsReportUnpricedRatherThanZero(t *testing.T) {
	usage := &types.TokenUsage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500}

	unpriced := []string{
		"gpt-5.6-sol",
		"gpt-5-pro",
		"gpt-5.4-mini",
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
				t.Fatalf("%s is priced (%v from %q); a model with a rate card belongs in a different test",
					model, cost.TotalCost, cost.PricingSource)
			}
			if cost.Quality != types.PricingUnknown {
				t.Errorf("Quality = %q, want %q", cost.Quality, types.PricingUnknown)
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
	installRateCard(t, PricingModel{
		Provider:                "test",
		Model:                   model,
		PricePerPromptToken:     0.001, // $1.00 per 1M
		PricePerCompletionToken: 0.002, // $2.00 per 1M
		Currency:                "USD",
		EffectiveDate:           time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})

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

// installRateCard puts a rate card in the table for one test and removes it
// afterwards, under the lock the table is guarded by.
func installRateCard(t *testing.T, card PricingModel) {
	t.Helper()
	name := card.Model
	pricingMu.Lock()
	pricingModels[name] = card
	pricingMu.Unlock()
	t.Cleanup(func() {
		pricingMu.Lock()
		delete(pricingModels, name)
		pricingMu.Unlock()
	})
}

// Each token is priced once. Providers report cached tokens as part of the
// prompt tokens and reasoning tokens as part of the completion tokens, so a
// call's cost is the uncached prompt at the prompt rate, the cached prompt at
// the cached rate, and the completion at the output rate -- not those three
// plus the cached and reasoning tokens a second time.
//
// The usage figures below are the shapes the decoders in internal/llm produce:
// the Responses API's input_tokens with input_tokens_details.cached_tokens,
// chat completions' prompt_tokens with prompt_tokens_details.cached_tokens, and
// Anthropic's input_tokens, which the Anthropic decoder reports without any
// cached figure.
func TestEveryTokenIsPricedOnce(t *testing.T) {
	const (
		plain       = "test-model-plain-rates"
		cachedRate  = "test-model-cached-rate"
		reasonRate  = "test-model-reasoning-rate"
		bothRates   = "test-model-cached-and-reasoning-rates"
		perMillion1 = 0.001 // $1.00 per 1M
		perMillion2 = 0.002 // $2.00 per 1M
	)
	for _, card := range []PricingModel{
		{Provider: "test", Model: plain, PricePerPromptToken: perMillion1, PricePerCompletionToken: perMillion2},
		{Provider: "test", Model: cachedRate, PricePerPromptToken: perMillion1, PricePerCompletionToken: perMillion2,
			PriceCachedToken: 0.0001},
		{Provider: "test", Model: reasonRate, PricePerPromptToken: perMillion1, PricePerCompletionToken: perMillion2,
			PriceReasoningToken: 0.003},
		{Provider: "test", Model: bothRates, PricePerPromptToken: perMillion1, PricePerCompletionToken: perMillion2,
			PriceCachedToken: 0.0001, PriceReasoningToken: 0.003},
	} {
		installRateCard(t, card)
	}

	cases := []struct {
		name  string
		model string
		usage types.TokenUsage
		// prompt, completion, cached and reasoning are the four cost fields; the
		// total is their sum.
		prompt, completion, cached, reasoning float64
	}{
		{
			name:  "responses API: 9,000 of 10,000 input tokens were cache reads",
			model: cachedRate,
			usage: types.TokenUsage{PromptTokens: 10_000, CompletionTokens: 2_000, TotalTokens: 12_000, CachedTokens: 9_000},
			// 1,000 uncached input at $1.00/1M, 9,000 cached at $0.10/1M, 2,000 output at $2.00/1M.
			prompt: 0.001, cached: 0.0009, completion: 0.004,
		},
		{
			name:  "responses API: reasoning tokens are part of the output and are billed as output",
			model: plain,
			usage: types.TokenUsage{PromptTokens: 1_000, CompletionTokens: 10_000, TotalTokens: 11_000, ReasoningTokens: 6_000},
			// No reasoning rate, so all 10,000 output tokens are billed at $2.00/1M.
			prompt: 0.001, completion: 0.02,
		},
		{
			name:  "a model with a reasoning rate bills reasoning tokens at it and the rest of the output at the output rate",
			model: reasonRate,
			usage: types.TokenUsage{PromptTokens: 1_000, CompletionTokens: 10_000, TotalTokens: 11_000, ReasoningTokens: 6_000},
			// 4,000 output at $2.00/1M and 6,000 reasoning at $3.00/1M.
			prompt: 0.001, completion: 0.008, reasoning: 0.018,
		},
		{
			name:  "chat completions: prompt_tokens_details.cached_tokens and completion_tokens_details.reasoning_tokens",
			model: bothRates,
			usage: types.TokenUsage{PromptTokens: 2_000, CompletionTokens: 1_000, TotalTokens: 3_000, CachedTokens: 1_500, ReasoningTokens: 400},
			// 500 prompt at $1.00/1M, 1,500 cached at $0.10/1M, 600 output at $2.00/1M, 400 reasoning at $3.00/1M.
			prompt: 0.0005, cached: 0.00015, completion: 0.0012, reasoning: 0.0012,
		},
		{
			name:   "anthropic: the decoder reports input and output tokens and no cached figure",
			model:  cachedRate,
			usage:  types.TokenUsage{PromptTokens: 5_000, CompletionTokens: 500, TotalTokens: 5_500},
			prompt: 0.005, completion: 0.001,
		},
		{
			name:   "a fully cached prompt costs only the cached rate",
			model:  cachedRate,
			usage:  types.TokenUsage{PromptTokens: 4_000, TotalTokens: 4_000, CachedTokens: 4_000},
			cached: 0.0004,
		},
		{
			name:   "no cached rate: cached tokens are not discounted by a rate nobody published",
			model:  plain,
			usage:  types.TokenUsage{PromptTokens: 1_000_000, TotalTokens: 1_000_000, CachedTokens: 900_000},
			prompt: 1.0,
		},
		{
			name:  "more cached tokens than prompt tokens is held at the prompt tokens",
			model: cachedRate,
			usage: types.TokenUsage{PromptTokens: 100_000, TotalTokens: 100_000, CachedTokens: 900_000},
			// 100,000 cached at $0.10/1M, nothing else: never billed past the prompt.
			cached: 0.01,
		},
		{
			name:      "more reasoning tokens than output tokens is held at the output tokens",
			model:     reasonRate,
			usage:     types.TokenUsage{CompletionTokens: 1_000, TotalTokens: 1_000, ReasoningTokens: 5_000},
			reasoning: 0.003,
		},
		{
			name:  "negative counts are read as zero",
			model: bothRates,
			usage: types.TokenUsage{PromptTokens: -1, CompletionTokens: -1, TotalTokens: -2, CachedTokens: -5, ReasoningTokens: -5},
		},
		{
			name:  "no usage",
			model: bothRates,
			usage: types.TokenUsage{},
		},
		{
			name:       "reasoning rate set but no reasoning reported",
			model:      reasonRate,
			usage:      types.TokenUsage{CompletionTokens: 1_000, TotalTokens: 1_000},
			completion: 0.002,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cost := CalculateCost(&tc.usage, tc.model, "test")
			if cost == nil || !cost.Priced {
				t.Fatalf("%s is not priced: %+v", tc.model, cost)
			}
			if !closeEnough(cost.PromptCost, tc.prompt) {
				t.Errorf("PromptCost = %v, want %v", cost.PromptCost, tc.prompt)
			}
			if !closeEnough(cost.CompletionCost, tc.completion) {
				t.Errorf("CompletionCost = %v, want %v", cost.CompletionCost, tc.completion)
			}
			if !closeEnough(cost.CachedCost, tc.cached) {
				t.Errorf("CachedCost = %v, want %v", cost.CachedCost, tc.cached)
			}
			if !closeEnough(cost.ReasoningCost, tc.reasoning) {
				t.Errorf("ReasoningCost = %v, want %v", cost.ReasoningCost, tc.reasoning)
			}
			want := tc.prompt + tc.completion + tc.cached + tc.reasoning
			if !closeEnough(cost.TotalCost, want) {
				t.Errorf("TotalCost = %v, want %v", cost.TotalCost, want)
			}
		})
	}
}

// The default model with a call that read almost all of its prompt from the
// cache, through the built-in table rather than a test card: the figure the
// double count overstated by about 75%.
func TestACacheHitOnTheDefaultModelIsPricedOnce(t *testing.T) {
	cost := CalculateCost(&types.TokenUsage{
		PromptTokens: 10_000, CompletionTokens: 2_000, TotalTokens: 12_000, CachedTokens: 9_000,
	}, "gpt-6-luna", "openai")
	if cost == nil || !cost.Priced {
		t.Fatalf("gpt-6-luna is not priced: %+v", cost)
	}
	// 1,000 input at $0.10/1M, 9,000 cached at $0.01/1M, 2,000 output at $0.50/1M.
	const want = 0.0001 + 0.00009 + 0.001
	if !closeEnough(cost.TotalCost, want) {
		t.Errorf("TotalCost = %v, want %v", cost.TotalCost, want)
	}
}

// A reasoning model's thinking is part of its output and is billed at the
// output rate: 10,000 output tokens of which 6,000 were reasoning cost
// 10,000 times the output rate, not that plus a second charge for the 6,000.
func TestReasoningTokensOnTheO1ModelsAreBilledAsOutput(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  float64
	}{
		{"o1-preview", 10_000 * 0.06 / 1000},
		{"o1-mini", 10_000 * 0.012 / 1000},
	} {
		cost := CalculateCost(&types.TokenUsage{CompletionTokens: 10_000, ReasoningTokens: 6_000, TotalTokens: 10_000}, tc.model, "openai")
		if !closeEnough(cost.TotalCost, tc.want) || cost.ReasoningCost != 0 {
			t.Errorf("%s: total %v, reasoning cost %v; want %v and 0", tc.model, cost.TotalCost, cost.ReasoningCost, tc.want)
		}
	}
}

// A dated snapshot prices from its base model; a version bump does not. That
// distinction is PR-007, and it is the difference between pricing
// gpt-5-2025-08-07 correctly and pricing gpt-5.6-luna as gpt-5.
func TestSnapshotsPriceFromTheBaseAndVersionBumpsDoNot(t *testing.T) {
	base, ok := lookupPricingModel("gpt-5")
	if !ok {
		t.Fatal("gpt-5 is not in the table; this case is about its snapshots")
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
		t.Fatal("that model has a rate; this case is about a model without one")
	}

	TrackCost(cost, &types.ResultMetadata{RequestID: "req-1", EndTime: time.Now()})

	record, ok := GetRequestCost("req-1")
	if !ok {
		t.Fatal("the record was not stored")
	}
	if record.Cost.Priced {
		t.Error("an unpriced call was recorded as priced")
	}
	summary := GetCostSummary(time.Time{}, nil)
	if summary.TotalCost != 0 {
		t.Errorf("TotalCost = %v; an unpriced call contributed a number", summary.TotalCost)
	}
	if summary.UnpricedRequests != 1 || summary.RequestCount != 1 {
		t.Errorf("summary counted %d unpriced of %d requests, want 1 of 1", summary.UnpricedRequests, summary.RequestCount)
	}
	if usd, complete := GetTotalSpend(time.Time{}, nil); usd != 0 || complete {
		t.Errorf("GetTotalSpend = (%v, %v), want (0, false): a total holding an unpriced call is incomplete", usd, complete)
	}
}

func closeEnough(got, want float64) bool {
	return math.Abs(got-want) < 1e-9
}
