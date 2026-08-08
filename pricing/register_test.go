package pricing

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// PR-002. The built-in table is a snapshot of public list prices and will
// always be behind, and a caller on negotiated rates had no way to say so:
// their costs were either computed from somebody else's prices or reported
// unpriced.

// restorePricing puts the table back after a test writes to it, so tests that
// register a model do not price other tests' requests.
func restorePricing(t *testing.T) {
	t.Helper()

	pricingMu.Lock()
	saved := make(map[string]PricingModel, len(pricingModels))
	for name, model := range pricingModels {
		saved[name] = model
	}
	pricingMu.Unlock()

	t.Cleanup(func() {
		pricingMu.Lock()
		pricingModels = saved
		pricingMu.Unlock()
	})
}

func TestRegisteredRatesArePricedAndUsed(t *testing.T) {
	restorePricing(t)

	err := RegisterPricingModel(PricingModel{
		Provider:                "openai",
		Model:                   "acme-negotiated-1",
		PricePerPromptToken:     0.001,
		PricePerCompletionToken: 0.002,
		Currency:                "USD",
		EffectiveDate:           time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("RegisterPricingModel: %v", err)
	}

	usage := &types.TokenUsage{PromptTokens: 1000, CompletionTokens: 1000, TotalTokens: 2000}
	cost := CalculateCost(usage, "acme-negotiated-1", "openai")

	if cost == nil || !cost.Priced {
		t.Fatalf("a registered model reported unpriced: %+v", cost)
	}
	// 1000 prompt tokens at 0.001/1K plus 1000 completion at 0.002/1K.
	if diff := cost.TotalCost - 0.003; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("TotalCost = %v, want 0.003", cost.TotalCost)
	}
}

func TestRegisteringReplacesAnExistingRate(t *testing.T) {
	restorePricing(t)

	usage := &types.TokenUsage{PromptTokens: 1000, TotalTokens: 1000}
	before := CalculateCost(usage, "gpt-5", "openai")
	if before == nil || !before.Priced {
		t.Fatal("gpt-5 was not priced to begin with")
	}

	if err := RegisterPricingModel(PricingModel{
		Provider:                "openai",
		Model:                   "gpt-5",
		PricePerPromptToken:     before.PromptCost * 2,
		PricePerCompletionToken: 0.01,
	}); err != nil {
		t.Fatalf("RegisterPricingModel: %v", err)
	}

	after := CalculateCost(usage, "gpt-5", "openai")
	if after.PromptCost <= before.PromptCost {
		t.Errorf("the replacement rate did not take effect: %v then %v", before.PromptCost, after.PromptCost)
	}
}

// Lookups normalise, so registration has to normalise the same way or a caller
// registers a rate that never matches anything.
func TestRegistrationNormalisesTheModelName(t *testing.T) {
	restorePricing(t)

	if err := RegisterPricingModel(PricingModel{
		Provider:                "openai",
		Model:                   "  ACME-Mixed-Case  ",
		PricePerPromptToken:     0.001,
		PricePerCompletionToken: 0.001,
	}); err != nil {
		t.Fatalf("RegisterPricingModel: %v", err)
	}

	cost := CalculateCost(&types.TokenUsage{PromptTokens: 1000, TotalTokens: 1000}, "acme-mixed-case", "openai")
	if cost == nil || !cost.Priced {
		t.Error("a model registered with mixed case and spaces did not price its lower-case name")
	}
}

// The mistake that actually happens: rates in this table are per 1,000 tokens,
// and every public price list quotes per 1,000,000. A caller who pastes the
// list price registers rates a thousand times too high, and every invoice
// afterwards is wrong by that factor with nothing to reveal it.
func TestARatePerMillionIsRefused(t *testing.T) {
	restorePricing(t)

	err := RegisterPricingModel(PricingModel{
		Provider:                "openai",
		Model:                   "acme-oops",
		PricePerPromptToken:     1.25, // meant $1.25 per 1M, wrote per 1K
		PricePerCompletionToken: 10,
	})
	if err == nil {
		t.Fatal("a per-million rate was accepted as per-thousand")
	}
	if !errors.Is(err, ErrInvalidPricingModel) {
		t.Errorf("err = %v, want ErrInvalidPricingModel", err)
	}
	if !strings.Contains(err.Error(), "1,000,000") {
		t.Errorf("the error does not name the likely mistake: %v", err)
	}
}

// "Priced at nothing" is a more convincing lie than "unpriced", which is why
// PR-001 made unpriced its own state.
func TestARateCardOfAllZerosIsRefused(t *testing.T) {
	restorePricing(t)

	err := RegisterPricingModel(PricingModel{Provider: "openai", Model: "acme-free"})
	if err == nil {
		t.Fatal("a rate card pricing everything at zero was accepted")
	}
	if !strings.Contains(err.Error(), "unpriced") {
		t.Errorf("the error does not point at the honest alternative: %v", err)
	}
}

func TestRegistrationRejectsWhatItCannotUse(t *testing.T) {
	restorePricing(t)

	cases := []struct {
		name  string
		model PricingModel
	}{
		{"no model name", PricingModel{Provider: "openai", PricePerPromptToken: 0.001}},
		{"blank model name", PricingModel{Provider: "openai", Model: "   ", PricePerPromptToken: 0.001}},
		{"no provider", PricingModel{Model: "acme", PricePerPromptToken: 0.001}},
		{"negative prompt rate", PricingModel{Provider: "o", Model: "acme", PricePerPromptToken: -0.001, PricePerCompletionToken: 0.001}},
		{"negative completion rate", PricingModel{Provider: "o", Model: "acme", PricePerPromptToken: 0.001, PricePerCompletionToken: -1}},
		{"negative cached rate", PricingModel{Provider: "o", Model: "acme", PricePerPromptToken: 0.001, PriceCachedToken: -1}},
		{"negative reasoning rate", PricingModel{Provider: "o", Model: "acme", PricePerPromptToken: 0.001, PriceReasoningToken: -1}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := RegisterPricingModel(tc.model); err == nil {
				t.Errorf("%s was accepted", tc.name)
			} else if !errors.Is(err, ErrInvalidPricingModel) {
				t.Errorf("err = %v, want ErrInvalidPricingModel", err)
			}
		})
	}
}

func TestCurrencyDefaultsToUSD(t *testing.T) {
	restorePricing(t)

	if err := RegisterPricingModel(PricingModel{
		Provider:                "openai",
		Model:                   "acme-nocurrency",
		PricePerPromptToken:     0.001,
		PricePerCompletionToken: 0.001,
	}); err != nil {
		t.Fatalf("RegisterPricingModel: %v", err)
	}

	pricingMu.RLock()
	stored := pricingModels["acme-nocurrency"]
	pricingMu.RUnlock()

	if stored.Currency != "USD" {
		t.Errorf("Currency = %q, want USD", stored.Currency)
	}
}

func TestRegisteredPricingModelsListsWhatIsPriced(t *testing.T) {
	restorePricing(t)

	before := len(RegisteredPricingModels())
	if before == 0 {
		t.Fatal("the built-in table reports no priced models at all")
	}

	if err := RegisterPricingModel(PricingModel{
		Provider:                "openai",
		Model:                   "acme-listed",
		PricePerPromptToken:     0.001,
		PricePerCompletionToken: 0.001,
	}); err != nil {
		t.Fatalf("RegisterPricingModel: %v", err)
	}

	names := RegisteredPricingModels()
	if len(names) != before+1 {
		t.Errorf("list length %d, want %d", len(names), before+1)
	}

	found := false
	for i, name := range names {
		if name == "acme-listed" {
			found = true
		}
		if i > 0 && names[i-1] > name {
			t.Fatalf("the list is not sorted: %q before %q", names[i-1], name)
		}
	}
	if !found {
		t.Error("a registered model is missing from the list")
	}
}

// The table is read while pricing every request, so making it writable made a
// lock necessary. -race does not run on this machine, so this asserts the
// weaker but still useful property: concurrent registration and pricing does
// not corrupt the table or panic.
func TestRegistrationIsSafeBesideConcurrentPricing(t *testing.T) {
	restorePricing(t)

	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				_ = RegisterPricingModel(PricingModel{
					Provider:                "openai",
					Model:                   "acme-concurrent",
					PricePerPromptToken:     0.001,
					PricePerCompletionToken: 0.001,
				})
			}
		}(worker)
	}
	for reader := 0; reader < 4; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				CalculateCost(&types.TokenUsage{PromptTokens: 10, TotalTokens: 10}, "gpt-5", "openai")
			}
		}()
	}
	wg.Wait()

	cost := CalculateCost(&types.TokenUsage{PromptTokens: 1000, TotalTokens: 1000}, "acme-concurrent", "openai")
	if cost == nil || !cost.Priced {
		t.Error("the table did not survive concurrent registration")
	}
}

// The 5.6 family is the library's own default, and it is deliberately absent
// from the built-in table: nobody has published its rates here, and inventing
// them would report a confident figure that is wrong. Unpriced is the honest
// answer, and this pins that it stays honest rather than quietly acquiring a
// guess.
func TestTheDefaultModelsReportUnpricedRatherThanGuessed(t *testing.T) {
	for _, model := range []string{"gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra"} {
		t.Run(model, func(t *testing.T) {
			cost := CalculateCost(&types.TokenUsage{PromptTokens: 1000, TotalTokens: 1000}, model, "openai")
			if cost != nil && cost.Priced {
				t.Errorf("%s reports a price of %v; if real rates were added, update this test deliberately rather than deleting it",
					model, cost.TotalCost)
			}
		})
	}
}
