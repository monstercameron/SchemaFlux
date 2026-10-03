// Package pricing - Pricing and cost tracking for LLM operations
package pricing

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/telemetry"
)

// PricingModel defines the cost structure for a specific model
type PricingModel struct {
	Provider                string
	Model                   string
	PricePerPromptToken     float64 // Price per 1K tokens
	PricePerCompletionToken float64 // Price per 1K tokens
	PriceCachedToken        float64 // Price per 1K cached tokens (if applicable)
	PriceReasoningToken     float64 // Price per 1K reasoning tokens (for o1 models)
	Currency                string
	EffectiveDate           time.Time
}

var (
	// Pricing data for different models (prices per 1K tokens in USD)
	pricingModels = map[string]PricingModel{
		// OpenAI GPT-6 models
		//
		// Luna only, and for the reason given for the 5.6 family below: sol,
		// astra and 6.1-sol are on the account, and no rate card with both an
		// input and an output price was found for them, so they stay unpriced.
		//
		// The default model since 2026-10-01 (LX-120). Rates as published on
		// that date: $0.10 per 1M input tokens, $0.50 per 1M output tokens and
		// $0.01 per 1M cached input tokens. All three are published figures;
		// none is derived. A prompt over 272,000 input tokens is billed at a
		// higher long-context rate that this table does not model.
		"gpt-6-luna": {
			Provider:                "openai",
			Model:                   "gpt-6-luna",
			PricePerPromptToken:     0.0001,  // $0.10 per 1M tokens
			PricePerCompletionToken: 0.0005,  // $0.50 per 1M tokens
			PriceCachedToken:        0.00001, // $0.01 per 1M tokens
			Currency:                "USD",
			// The model's launch date.
			EffectiveDate: time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC),
		},

		// OpenAI GPT-5.6 models
		//
		// Luna only. Sol and Terra are real models on the same account and are
		// deliberately absent: the published figures found for them were input
		// prices without a matching output price, and half a price is not a
		// price. They stay unpriced until both numbers are known, which reports
		// them as Unpriced rather than valuing them wrongly.
		"gpt-5.6-luna": {
			Provider:                "openai",
			Model:                   "gpt-5.6-luna",
			PricePerPromptToken:     0.0002, // $0.20 per 1M tokens
			PricePerCompletionToken: 0.0012, // $1.20 per 1M tokens
			// Ten percent of the prompt price, which is the ratio every other
			// OpenAI entry in this table uses and is NOT a published figure for
			// this model -- no cached rate was listed anywhere it was checked.
			// Stated as an inference rather than left at zero, because zero
			// would price a cached token as free and quietly understate a bill.
			PriceCachedToken: 0.00002,
			Currency:         "USD",
			// The 80% cut that took input from $1.00 to $0.20 and output from
			// $6.00 to $1.20.
			EffectiveDate: time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
		},

		// OpenAI GPT-5.4 models
		"gpt-5.4": {
			Provider:                "openai",
			Model:                   "gpt-5.4",
			PricePerPromptToken:     0.0025, // $2.50 per 1M tokens
			PricePerCompletionToken: 0.015,  // $15 per 1M tokens
			PriceCachedToken:        0.00025,
			Currency:                "USD",
			EffectiveDate:           time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC),
		},

		// OpenAI GPT-5 models
		"gpt-5": {
			Provider:                "openai",
			Model:                   "gpt-5",
			PricePerPromptToken:     0.00125, // $1.25 per 1M tokens
			PricePerCompletionToken: 0.01,    // $10 per 1M tokens
			PriceCachedToken:        0.000125,
			Currency:                "USD",
			EffectiveDate:           time.Date(2025, 8, 7, 0, 0, 0, 0, time.UTC),
		},
		"gpt-5-2025-08-07": {
			Provider:                "openai",
			Model:                   "gpt-5-2025-08-07",
			PricePerPromptToken:     0.00125, // $1.25 per 1M tokens
			PricePerCompletionToken: 0.01,    // $10 per 1M tokens
			PriceCachedToken:        0.000125,
			Currency:                "USD",
			EffectiveDate:           time.Date(2025, 8, 7, 0, 0, 0, 0, time.UTC),
		},
		"gpt-5-nano": {
			Provider:                "openai",
			Model:                   "gpt-5-nano",
			PricePerPromptToken:     0.00005, // $0.05 per 1M tokens
			PricePerCompletionToken: 0.0004,  // $0.40 per 1M tokens
			PriceCachedToken:        0.000005,
			Currency:                "USD",
			EffectiveDate:           time.Date(2025, 8, 7, 0, 0, 0, 0, time.UTC),
		},
		"gpt-5-nano-2025-08-07": {
			Provider:                "openai",
			Model:                   "gpt-5-nano-2025-08-07",
			PricePerPromptToken:     0.00005, // $0.05 per 1M tokens
			PricePerCompletionToken: 0.0004,  // $0.40 per 1M tokens
			PriceCachedToken:        0.000005,
			Currency:                "USD",
			EffectiveDate:           time.Date(2025, 8, 7, 0, 0, 0, 0, time.UTC),
		},
		"gpt-5-mini": {
			Provider:                "openai",
			Model:                   "gpt-5-mini",
			PricePerPromptToken:     0.00025, // $0.25 per 1M tokens
			PricePerCompletionToken: 0.002,   // $2 per 1M tokens
			PriceCachedToken:        0.000025,
			Currency:                "USD",
			EffectiveDate:           time.Date(2025, 8, 7, 0, 0, 0, 0, time.UTC),
		},
		"gpt-5-mini-2025-08-07": {
			Provider:                "openai",
			Model:                   "gpt-5-mini-2025-08-07",
			PricePerPromptToken:     0.00025, // $0.25 per 1M tokens
			PricePerCompletionToken: 0.002,   // $2 per 1M tokens
			PriceCachedToken:        0.000025,
			Currency:                "USD",
			EffectiveDate:           time.Date(2025, 8, 7, 0, 0, 0, 0, time.UTC),
		},

		// OpenAI GPT-4 models
		"gpt-4-turbo-preview": {
			Provider:                "openai",
			Model:                   "gpt-4-turbo-preview",
			PricePerPromptToken:     0.01, // $10 per 1M tokens
			PricePerCompletionToken: 0.03, // $30 per 1M tokens
			Currency:                "USD",
			EffectiveDate:           time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		"gpt-4": {
			Provider:                "openai",
			Model:                   "gpt-4",
			PricePerPromptToken:     0.03, // $30 per 1M tokens
			PricePerCompletionToken: 0.06, // $60 per 1M tokens
			Currency:                "USD",
			EffectiveDate:           time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		"gpt-4-32k": {
			Provider:                "openai",
			Model:                   "gpt-4-32k",
			PricePerPromptToken:     0.06, // $60 per 1M tokens
			PricePerCompletionToken: 0.12, // $120 per 1M tokens
			Currency:                "USD",
			EffectiveDate:           time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},

		// OpenAI GPT-3.5 models
		"gpt-3.5-turbo": {
			Provider:                "openai",
			Model:                   "gpt-3.5-turbo",
			PricePerPromptToken:     0.0005, // $0.50 per 1M tokens
			PricePerCompletionToken: 0.0015, // $1.50 per 1M tokens
			Currency:                "USD",
			EffectiveDate:           time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		"gpt-3.5-turbo-16k": {
			Provider:                "openai",
			Model:                   "gpt-3.5-turbo-16k",
			PricePerPromptToken:     0.003, // $3 per 1M tokens
			PricePerCompletionToken: 0.004, // $4 per 1M tokens
			Currency:                "USD",
			EffectiveDate:           time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},

		// OpenAI o1 models. Their reasoning tokens are billed as output tokens,
		// so the entries carry no reasoning rate of their own: the rates this
		// table held for them were the input prices, and CalculateCost used to
		// add them on top of the output price for the same tokens.
		"o1-preview": {
			Provider:                "openai",
			Model:                   "o1-preview",
			PricePerPromptToken:     0.015, // $15 per 1M tokens
			PricePerCompletionToken: 0.06,  // $60 per 1M tokens
			Currency:                "USD",
			EffectiveDate:           time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		"o1-mini": {
			Provider:                "openai",
			Model:                   "o1-mini",
			PricePerPromptToken:     0.003, // $3 per 1M tokens
			PricePerCompletionToken: 0.012, // $12 per 1M tokens
			Currency:                "USD",
			EffectiveDate:           time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},

		// Anthropic Claude models
		"claude-3-opus": {
			Provider:                "anthropic",
			Model:                   "claude-3-opus",
			PricePerPromptToken:     0.015,   // $15 per 1M tokens
			PricePerCompletionToken: 0.075,   // $75 per 1M tokens
			PriceCachedToken:        0.00187, // $1.875 per 1M cached tokens
			Currency:                "USD",
			EffectiveDate:           time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		"claude-3-sonnet": {
			Provider:                "anthropic",
			Model:                   "claude-3-sonnet",
			PricePerPromptToken:     0.003,   // $3 per 1M tokens
			PricePerCompletionToken: 0.015,   // $15 per 1M tokens
			PriceCachedToken:        0.00038, // $0.375 per 1M cached tokens
			Currency:                "USD",
			EffectiveDate:           time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		"claude-3-haiku": {
			Provider:                "anthropic",
			Model:                   "claude-3-haiku",
			PricePerPromptToken:     0.00025, // $0.25 per 1M tokens
			PricePerCompletionToken: 0.00125, // $1.25 per 1M tokens
			PriceCachedToken:        0.00003, // $0.03 per 1M cached tokens
			Currency:                "USD",
			EffectiveDate:           time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},

		// Cerebras. Priced here so that a secondary provider whose whole appeal
		// is being cheaper reports a real figure rather than unpriced, which is
		// what a caller comparing providers by cost would otherwise get.
		"gemma-4-31b": {
			Provider:                "cerebras",
			Model:                   "gemma-4-31b",
			PricePerPromptToken:     0.00099, // $0.99 per 1M tokens
			PricePerCompletionToken: 0.00149, // $1.49 per 1M tokens
			Currency:                "USD",
			EffectiveDate:           time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC),
		},

		// TypeSafe's Jev, the decision model (LX-100): $0.042 per 1M input
		// tokens and nothing for the answer, which is typed and a few tokens
		// long. Source: the owner's study of 2026-10-01, which was billed at
		// this rate, and TypeSafe's published price on that date. There is no
		// cached rate and none is assumed.
		//
		// Two names, one rate. "jev-latest" is what a request asks for, and the
		// API answers under the version it resolved to, "jev-1.13.0" on the date
		// above. A call is priced by the name it was answered under, and that
		// name ends in a dotted version, which the lookup does not read as a
		// version tag (it reads a date, or a run of digits), so the alias's entry
		// alone would leave every answered call unpriced. A later version is
		// unpriced until it is added here, which is the rule for every model.
		"jev-latest": {
			Provider:                "jev",
			Model:                   "jev-latest",
			PricePerPromptToken:     0.000042, // $0.042 per 1M tokens
			PricePerCompletionToken: 0,        // the answer is not billed
			Currency:                "USD",
			EffectiveDate:           time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		},
		"jev-1.13.0": {
			Provider:                "jev",
			Model:                   "jev-1.13.0",
			PricePerPromptToken:     0.000042, // $0.042 per 1M tokens
			PricePerCompletionToken: 0,        // the answer is not billed
			Currency:                "USD",
			EffectiveDate:           time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		},

		// The same model through OpenRouter, which the "jev-openrouter" preset
		// asks for as "~typesafe/jev-latest": the tilde marks an alias that follows
		// the family's newest version, and the answer names the version it
		// resolved to, with or without the vendor in front. Priced at TypeSafe's
		// rate, the one the owner's study of 2026-10-01 was billed at, on that
		// date. OpenRouter's own listing of the model has not been read: nothing
		// here reaches the network. If it prices the model differently, these
		// entries are wrong by that difference until they are corrected.
		"~typesafe/jev-latest": {
			Provider:                "jev-openrouter",
			Model:                   "~typesafe/jev-latest",
			PricePerPromptToken:     0.000042, // $0.042 per 1M tokens
			PricePerCompletionToken: 0,        // the answer is not billed
			Currency:                "USD",
			EffectiveDate:           time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		},
		"typesafe/jev-latest": {
			Provider:                "jev-openrouter",
			Model:                   "typesafe/jev-latest",
			PricePerPromptToken:     0.000042, // $0.042 per 1M tokens
			PricePerCompletionToken: 0,        // the answer is not billed
			Currency:                "USD",
			EffectiveDate:           time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		},
		"typesafe/jev-1.13.0": {
			Provider:                "jev-openrouter",
			Model:                   "typesafe/jev-1.13.0",
			PricePerPromptToken:     0.000042, // $0.042 per 1M tokens
			PricePerCompletionToken: 0,        // the answer is not billed
			Currency:                "USD",
			EffectiveDate:           time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		},
	}

	// Cost tracking state. costMutex guards all of it, and is held by a call
	// that records a cost only for the few writes that change it (recordCost);
	// what a record holds is worked out before the lock is taken.
	costMutex  sync.RWMutex
	totalCosts map[string]float64 // Track costs by dimension (e.g., "daily", "weekly", "monthly")

	// costHistory holds pointers to records that are never changed once they are
	// in it. A reader that wants to walk the history copies the pointers under
	// the read lock and walks them after it has let go, so a walk of ten thousand
	// records does not hold up the calls that are recording.
	costHistory []*CostRecord

	// unpricedCalls counts, per the same keys as totalCosts, the calls recorded
	// without a price. A total beside a non-zero count is a floor.
	unpricedCalls map[string]int

	// costStart and costCount turn costHistory into a ring buffer. It used to be
	// an unbounded slice appended to on every call and never evicted, in a
	// library whose whole purpose is to be called in a loop: a long-running
	// process leaked a record per request forever, and GetRequestCost,
	// GetCostSummary, and GetTotalCost each linear-scanned the leak under a lock.
	costStart int
	costCount int
	costLimit = DefaultCostHistoryLimit

	// costIndex maps a request ID to its slot, so GetRequestCost is a lookup
	// rather than a scan. Entries are removed as their records are evicted.
	costIndex map[string]int

	budgetLimits   map[string]float64
	budgetCallback func(current, limit float64, period string)

	// budgetNotified remembers which period keys have already fired, so the
	// callback is edge-triggered. It used to fire on every request once spend
	// passed 80% of a limit -- with no debounce and no state, an alerting
	// integration got one notification per call for the rest of the day. The
	// value has a bit set for each threshold of budgetThresholds already sent.
	budgetNotified map[string]uint8

	// budgetEnforcing turns the alert into a limit. The default is false:
	// budgets have always been advisory here, and silently starting to refuse
	// calls would be a worse surprise than the noise it replaces. It is atomic
	// so that CheckBudget, which every call makes before it is sent, has nothing
	// to read under the lock when enforcement is off. It is written only with
	// costMutex held.
	budgetEnforcing atomic.Bool
)

// DefaultCostHistoryLimit bounds the in-memory cost history. Ten thousand
// records is a few megabytes and covers any interactive session; a service that
// wants more should export the records, not retain them here.
const DefaultCostHistoryLimit = 10000

// ErrBudgetExceeded reports that a configured budget is exhausted and enforcing
// mode is on. It is returned before the provider call, not after.
var ErrBudgetExceeded = errors.New("cost budget exceeded")

// CostRecord represents a single cost entry
type CostRecord struct {
	Timestamp     time.Time
	RequestID     string
	CorrelationID string
	Operation     string
	Model         string
	Provider      string
	TokenUsage    types.TokenUsage
	Cost          types.CostInfo
	Tags          map[string]string

	// pairs are the tags of a record the ledger holds, as the call made them
	// and not yet as a map. Building the map was most of what recording a call
	// cost, and nobody reads it until they ask for the record, when the copy
	// they are handed has it (cloneCostRecord). hasPairs says there were tags,
	// none of them or more, so that a call with an empty set of them and a call
	// with no set are still told apart in what a reader gets.
	pairs    []tagPair
	hasPairs bool
}

type tagPair struct{ key, value string }

// tag looks up a tag on a record the ledger holds or on one a caller built.
func (r CostRecord) tag(key string) (string, bool) {
	if value, ok := r.Tags[key]; ok {
		return value, true
	}
	for _, pair := range r.pairs {
		if pair.key == key {
			return pair.value, true
		}
	}
	return "", false
}

// unmeteredProviders names transports that do not bill the caller per token,
// so no rate card can describe what one of their calls cost.
//
// `codex` runs against the CLI's own subscription login (PS-013): the tokens
// are real and are reported, but they are drawn against a flat-rate plan
// rather than invoiced at a per-token rate, so multiplying them by any rate
// card produces a number that is not the bill.
var unmeteredProviders = map[string]bool{
	"codex": true,
}

// CalculateCost prices the usage of one completed call.
//
// Usage follows the providers' own accounting, which the decoders in
// internal/llm preserve: cached tokens are the part of the prompt tokens that
// was read from the provider's cache, and reasoning tokens are the part of the
// completion tokens that was spent thinking. Each token is therefore priced
// once. Cached tokens cost the cached rate and the rest of the prompt costs the
// prompt rate; reasoning tokens cost the output rate unless the model has a
// rate of its own for them. A model with no cached rate prices its cached
// tokens at the prompt rate, because a discount nobody published is not one to
// assume. A count above its total (more cached than prompt tokens) is held at
// the total, and a negative count is read as zero.
//
// A model with no rate card, and a transport that does not bill per token, are
// reported unpriced: zero in every cost field with Priced false. The warning
// that says so goes to the process logger; CalculateCostLogged sends it to the
// logger of a call.
func CalculateCost(usage *types.TokenUsage, model string, provider string) *types.CostInfo {
	return CalculateCostLogged(nil, usage, model, provider)
}

// CalculateCostLogged is CalculateCost for a call that has a logger of its own:
// the warning that a model has no rate card is written to log, which is the
// logger of the client the call was made on, and not to the process's. A nil log
// is the process logger, which is what CalculateCost uses. The warning is still
// written once for each (provider, model), by whichever logger prices it first.
func CalculateCostLogged(log *logger.Logger, usage *types.TokenUsage, model string, provider string) *types.CostInfo {
	if usage == nil {
		return nil
	}

	// A transport that does not bill per token is unpriced whatever the model
	// is called. PS-013.
	//
	// This is checked before the lookup because the lookup keys on the model
	// alone: a subscription transport that runs `gpt-5.6-sol` would otherwise
	// be priced at that model's API rates and report a precise USD figure for
	// a call that was never metered that way -- a flat-rate subscription's
	// marginal cost is not the API list price, and it is not zero either.
	// Unpriced says the one true thing, which is that this library does not
	// know what the turn cost. PR-001's rule, one layer out: the failure is
	// not an absent number, it is a confident wrong one.
	if unmeteredProviders[strings.ToLower(strings.TrimSpace(provider))] {
		return &types.CostInfo{
			Currency: "USD",
			Priced:   false,
			Quality:  types.PricingUnknown,
		}
	}

	// Look up pricing for this model: an exact entry, or the entry of the model
	// this one is a dated snapshot of. There is deliberately no fallback to any
	// other model's rates: the previous behaviour priced every unrecognised
	// Anthropic model at claude-3-haiku, understating an Opus call by roughly
	// 60x while presenting the result as a precise USD figure.
	pricing, exists := lookupPricingModel(model)
	if !exists {
		warnUnpricedOnce(log, model, provider)
		return &types.CostInfo{
			Currency: "USD",
			Priced:   false,
			Quality:  types.PricingUnknown,
		}
	}

	// Calculate costs (prices are per 1K tokens). Every token is priced once:
	// the cached tokens are inside the prompt tokens and the reasoning tokens
	// are inside the completion tokens, so each is taken out of its total
	// before that total is priced.
	prompt := max(usage.PromptTokens, 0)
	completion := max(usage.CompletionTokens, 0)
	cached := min(max(usage.CachedTokens, 0), prompt)
	reasoning := min(max(usage.ReasoningTokens, 0), completion)

	if pricing.PriceCachedToken <= 0 {
		cached = 0
	}
	if pricing.PriceReasoningToken <= 0 {
		reasoning = 0
	}

	promptCost := float64(prompt-cached) * pricing.PricePerPromptToken / 1000.0
	completionCost := float64(completion-reasoning) * pricing.PricePerCompletionToken / 1000.0
	cachedCost := float64(cached) * pricing.PriceCachedToken / 1000.0
	reasoningCost := float64(reasoning) * pricing.PriceReasoningToken / 1000.0

	totalCost := promptCost + completionCost + cachedCost + reasoningCost

	return &types.CostInfo{
		TotalCost:      totalCost,
		PromptCost:     promptCost,
		CompletionCost: completionCost,
		CachedCost:     cachedCost,
		ReasoningCost:  reasoningCost,
		Currency:       pricing.Currency,
		// Both rates are per 1,000 tokens although the field names say per
		// token; internal/ops reads them as per 1,000 (lookupTokenPrices), so
		// the unit stays and a consumer multiplying by tokens must divide by
		// 1,000.
		PricePerPromptToken:     pricing.PricePerPromptToken,
		PricePerCompletionToken: pricing.PricePerCompletionToken,
		Priced:                  true,
		// The entry the rates came from: the model itself for an exact entry,
		// the base model for a dated snapshot. A caller can tell the two apart
		// by comparing it with the model it asked about.
		PricingSource: pricing.Model,
		// A rate card that prices everything at zero is a fact -- a local or
		// mock provider, or a free tier -- and is not the same as having no
		// rate card at all. Reporting both as "priced: true, total: 0" made
		// them indistinguishable in the other direction (OB-003).
		Quality: pricingQualityFor(pricing),
	}
}

// pricingQualityFor classifies a rate card that was found. Usage here is always
// the provider's reported figures, so a found rate card is exact; an estimate
// is produced by whoever priced a call *before* making it, and marks itself.
func pricingQualityFor(model PricingModel) types.PricingQuality {
	if model.PricePerPromptToken == 0 && model.PricePerCompletionToken == 0 &&
		model.PriceCachedToken == 0 && model.PriceReasoningToken == 0 {
		return types.PricingFree
	}
	return types.PricingExact
}

// EstimateCost prices a call that has not been made yet, from projected token
// counts, and marks the result estimated.
//
// A plan that reports a cost has to say that the number is a projection: the
// token counts are guesses, and a projection presented as a measurement is the
// same failure as an invented confidence. An unpriced model still reports
// unknown here rather than zero.
func EstimateCost(projected *types.TokenUsage, model, provider string) *types.CostInfo {
	cost := CalculateCost(projected, model, provider)
	if cost == nil {
		return nil
	}
	if cost.Quality == types.PricingExact {
		cost.Quality = types.PricingEstimated
	}
	return cost
}

// TrackCost records a cost entry for monitoring and budgeting.
//
// A call whose cost is not priced is recorded and counted as unpriced: it adds
// nothing to the totals, and GetTotalSpend and GetCostSummary say so. A cost
// whose amount is not a finite, non-negative number is recorded as unpriced
// too, because adding NaN to a running total would turn every later total and
// every budget comparison into NaN, which compares false against any limit.
//
// The record is in the ledger when TrackCost returns. If the call took a budget
// threshold over its limit, the callback has been run for it by then, with one
// exception: when another goroutine is in the middle of delivering an alert,
// this call hands its alerts to that goroutine, which delivers them before it
// returns from its own call. So an alert is never left for a later call to
// deliver, but the call that caused it may return before the callback has run
// for it. Alerts are delivered one at a time, in the order the thresholds were
// crossed (80 percent before 100), never at once, and never while the ledger is
// locked, so a callback may call any function in this package. A callback that
// records a cost has the alerts for that cost delivered after its own returns.
func TrackCost(cost *types.CostInfo, metadata *types.ResultMetadata) {
	if cost == nil || metadata == nil {
		return
	}
	cost = usableCost(cost)

	// Everything that can be worked out without the ledger is worked out
	// first, and the lock is taken for the few writes that change it. With the
	// whole of it under one lock, calls on different cores waited for each other
	// for about two microseconds each.
	now := time.Now()
	entry := &costEntry{keys: periodKeysFor(now)}
	fillCostRecord(&entry.record, cost, metadata, now)

	costMutex.Lock()
	deliver := recordCost(entry)
	costMutex.Unlock()
	if deliver {
		budgetNotices.deliver()
	}

	// A line about a call that went well is not written at info.
	if cost.TotalCost > 0.10 { // Log operations over $0.10
		if log := logger.GetLogger(); log.Enabled(context.Background(), slog.LevelDebug) {
			totalTokens := 0
			if metadata.TokenUsage != nil {
				totalTokens = metadata.TokenUsage.TotalTokens
			}
			log.Debug("High-cost operation tracked",
				"requestID", metadata.RequestID,
				"operation", metadata.Operation,
				"model", metadata.Model,
				"cost", fmt.Sprintf("$%.4f", cost.TotalCost),
				"tokens", totalTokens,
			)
		}
	}
}

// usableCost returns cost, or an unpriced cost in its place when any amount in
// it is NaN, infinite or negative.
func usableCost(cost *types.CostInfo) *types.CostInfo {
	for _, amount := range []float64{cost.TotalCost, cost.PromptCost, cost.CompletionCost, cost.CachedCost, cost.ReasoningCost} {
		if !(amount >= 0) || math.IsInf(amount, 0) {
			logger.GetLogger().Warn("Cost with an amount that is not a finite non-negative number recorded as unpriced")
			return &types.CostInfo{Currency: cost.Currency, Priced: false, Quality: types.PricingUnknown}
		}
	}
	return cost
}

// costEntry is a call's record on its way into the ledger, with the keys of the
// totals it counts toward. The history keeps a pointer to the record in it, so
// the two are one allocation.
type costEntry struct {
	record CostRecord
	keys   *periodKeys
}

// recordCost stores the entry's record, adds it to the totals and queues the
// budget alerts it brought due. It reports whether the caller must deliver the
// queue once it has released the lock. The caller holds costMutex.
func recordCost(entry *costEntry) (deliver bool) {
	if totalCosts == nil {
		totalCosts = make(map[string]float64)
	}
	if unpricedCalls == nil {
		unpricedCalls = make(map[string]int)
	}

	pushCostRecord(&entry.record)

	total := entry.record.Cost.TotalCost
	updateCostTotal(allTimeKey, total)
	for _, period := range entry.keys.periods {
		updateCostTotal(period.key, total)
	}
	if !entry.record.Cost.Priced {
		unpricedCalls[allTimeKey]++
		for _, period := range entry.keys.periods {
			unpricedCalls[period.key]++
		}
	}

	// The alerts are collected and queued now, while the totals are consistent
	// and the lock fixes the order calls were recorded in, and delivered later.
	if notices := dueBudgetNotices(entry.keys); len(notices) > 0 {
		return budgetNotices.add(notices, budgetCallback)
	}
	return false
}

// alertQueue holds the budget alerts that are due and have not been delivered.
// Alerts are added while the ledger is locked, so the queue is in the order the
// thresholds were crossed, and one goroutine at a time delivers it. The
// goroutine that finds nobody delivering does so, and does not stop until the
// queue is empty, so an alert added by any call is delivered by a goroutine that
// is inside a call to TrackCost and has not yet returned from it.
type alertQueue struct {
	mu         sync.Mutex
	queued     []queuedAlert
	delivering bool
}

type queuedAlert struct {
	notice   budgetNotice
	callback func(current, limit float64, period string)
}

var budgetNotices alertQueue

// add queues the notices for callback and reports whether the caller has to
// deliver: true when nobody else is delivering, in which case the caller now is.
func (q *alertQueue) add(notices []budgetNotice, callback func(current, limit float64, period string)) (deliver bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, notice := range notices {
		q.queued = append(q.queued, queuedAlert{notice: notice, callback: callback})
	}
	if q.delivering {
		return false
	}
	q.delivering = true
	return true
}

// deliver runs the callbacks of the queued alerts, oldest first, until the queue
// is empty. It is called only by the goroutine that add said must deliver, and
// holds no lock while a callback runs. The flag is cleared in the same critical
// section that finds the queue empty, so an alert added after that sees nobody
// delivering and delivers it itself.
func (q *alertQueue) deliver() {
	finished := false
	defer func() {
		if finished {
			return
		}
		// A callback ended this goroutine (runtime.Goexit, as t.Fatal does). The
		// alerts behind it still have to go out, so a new goroutine takes over.
		q.mu.Lock()
		defer q.mu.Unlock()
		if len(q.queued) > 0 {
			go q.deliver()
			return
		}
		q.delivering = false
	}()
	for {
		q.mu.Lock()
		if len(q.queued) == 0 {
			q.queued = nil
			q.delivering = false
			q.mu.Unlock()
			finished = true
			return
		}
		next := q.queued[0]
		q.queued[0] = queuedAlert{}
		q.queued = q.queued[1:]
		q.mu.Unlock()
		notifyBudget(next.callback, next.notice)
	}
}

// fillCostRecord sets record to the history's record of one call. The record and
// its tags are never changed afterwards, which is what lets readers hold them
// after letting go of the ledger lock.
func fillCostRecord(record *CostRecord, cost *types.CostInfo, metadata *types.ResultMetadata, now time.Time) {
	*record = CostRecord{
		Timestamp:     now,
		RequestID:     metadata.RequestID,
		CorrelationID: metadata.CorrelationID,
		Operation:     metadata.Operation,
		Model:         metadata.Model,
		Provider:      metadata.Provider,
		Cost:          *cost,
	}
	if !metadata.EndTime.IsZero() {
		record.Timestamp = metadata.EndTime
	}

	if metadata.TokenUsage != nil {
		record.TokenUsage = *metadata.TokenUsage
	}

	if metadata.Custom != nil {
		record.hasPairs = true
		if len(metadata.Custom) > 0 {
			record.pairs = make([]tagPair, 0, len(metadata.Custom))
		}
		for k, v := range metadata.Custom {
			// A string is what "%v" would make of it, without the formatting.
			if text, ok := v.(string); ok {
				record.pairs = append(record.pairs, tagPair{k, text})
				continue
			}
			record.pairs = append(record.pairs, tagPair{k, fmt.Sprintf("%v", v)})
		}
	}
}

// budgetNotice is one budget threshold that a recorded cost has just crossed.
type budgetNotice struct {
	current, limit float64
	period         string
}

// notifyBudget delivers one notice to the caller's callback. The callback is
// the caller's code, and a panic in it must not unwind through the operation
// that happened to record the cost, so it is recovered; only the type of the
// panic value is logged, because the value can hold anything the callback saw.
func notifyBudget(callback func(current, limit float64, period string), notice budgetNotice) {
	if callback == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.GetLogger().Error("Budget callback panicked",
				"period", notice.period, "panic_type", fmt.Sprintf("%T", recovered))
		}
	}()
	callback(notice.current, notice.limit, notice.period)
}

// GetTotalCost returns the cost accumulated over a time period.
//
// Unpriced calls add nothing to it, so the figure is a floor when any call in
// the period was unpriced. It cannot say so; GetTotalSpend returns the same
// figure with a flag that does.
func GetTotalCost(since time.Time, filters map[string]string) float64 {
	total, _ := GetTotalSpend(since, filters)
	return total
}

// GetTotalSpend returns the cost accumulated over a time period and whether it
// is complete. complete is false when any call in the period was unpriced, in
// which case the figure is a lower bound on what was spent, not the total.
func GetTotalSpend(since time.Time, filters map[string]string) (usd float64, complete bool) {
	complete = true
	forEachCostRecord(func(record CostRecord) {
		if record.Timestamp.Before(since) || !matchesFilters(record, filters) {
			return
		}
		usd += record.Cost.TotalCost
		if !record.Cost.Priced {
			complete = false
		}
	})

	return usd, complete
}

// SetBudget configures cost limits and alerts.
//
// A limit of zero, below zero or not a number is no limit for that period: it
// raises no alert and, under enforcement, refuses nothing.
func SetBudget(daily, weekly, monthly float64, callback func(current, limit float64, period string)) {
	costMutex.Lock()
	defer costMutex.Unlock()

	if budgetLimits == nil {
		budgetLimits = make(map[string]float64)
	}

	budgetLimits["daily"] = daily
	budgetLimits["weekly"] = weekly
	budgetLimits["monthly"] = monthly
	budgetCallback = callback

	logger.GetLogger().Info("Budget limits configured",
		"daily", fmt.Sprintf("$%.2f", daily),
		"weekly", fmt.Sprintf("$%.2f", weekly),
		"monthly", fmt.Sprintf("$%.2f", monthly),
	)
}

// GetCostBreakdown returns the cost for a period by model, operation and
// provider, with the period's total under "total". Unpriced calls add nothing
// to any entry; GetTotalSpend says whether the total is complete.
func GetCostBreakdown(since time.Time) map[string]float64 {
	breakdown := make(map[string]float64)

	forEachCostRecord(func(record CostRecord) {
		if record.Timestamp.Before(since) {
			return
		}

		// By model
		modelKey := fmt.Sprintf("model_%s", record.Model)
		breakdown[modelKey] += record.Cost.TotalCost

		// By operation
		opKey := fmt.Sprintf("operation_%s", record.Operation)
		breakdown[opKey] += record.Cost.TotalCost

		// By provider
		providerKey := fmt.Sprintf("provider_%s", record.Provider)
		breakdown[providerKey] += record.Cost.TotalCost

		// Total
		breakdown["total"] += record.Cost.TotalCost
	})

	return breakdown
}

// ExportCostReport generates a detailed cost report in the given format, "csv"
// or "json".
//
// The CSV has one row per record. Cost is written at full precision, because a
// call on the default model costs about a hundred-thousandth of a dollar and
// rounds to zero at four places. For an unpriced record Cost is empty rather
// than 0, and the Priced and PricingQuality columns say why. Text fields that
// hold a comma, a quote or a line break are quoted, and one that begins with =,
// +, - or @ is written with a leading apostrophe so a spreadsheet reads it as
// text, not as a formula.
func ExportCostReport(since time.Time, format string) (string, error) {
	var report string

	switch format {
	case "csv":
		var out strings.Builder
		writer := csv.NewWriter(&out)
		_ = writer.Write([]string{"Timestamp", "RequestID", "CorrelationID", "Operation", "Model", "Provider",
			"PromptTokens", "CompletionTokens", "TotalTokens", "Cost", "Priced", "PricingQuality"})
		forEachCostRecord(func(record CostRecord) {
			if record.Timestamp.Before(since) {
				return
			}
			cost := ""
			if record.Cost.Priced {
				cost = strconv.FormatFloat(record.Cost.TotalCost, 'f', -1, 64)
			}
			_ = writer.Write([]string{
				record.Timestamp.Format(time.RFC3339),
				spreadsheetSafe(record.RequestID),
				spreadsheetSafe(record.CorrelationID),
				spreadsheetSafe(record.Operation),
				spreadsheetSafe(record.Model),
				spreadsheetSafe(record.Provider),
				strconv.Itoa(record.TokenUsage.PromptTokens),
				strconv.Itoa(record.TokenUsage.CompletionTokens),
				strconv.Itoa(record.TokenUsage.TotalTokens),
				cost,
				strconv.FormatBool(record.Cost.Priced),
				string(record.Cost.Quality),
			})
		})
		writer.Flush()
		if err := writer.Error(); err != nil {
			return "", fmt.Errorf("failed to write cost report: %w", err)
		}
		report = out.String()
	case "json":
		records := make([]CostRecord, 0)
		forEachCostRecord(func(record CostRecord) {
			if record.Timestamp.Before(since) {
				return
			}
			records = append(records, cloneCostRecord(record))
		})
		data, err := json.MarshalIndent(records, "", "  ")
		if err != nil {
			return "", fmt.Errorf("failed to marshal cost report: %w", err)
		}
		report = string(data)
	default:
		return "", fmt.Errorf("unsupported format: %s", format)
	}

	return report, nil
}

// Helper functions

// lookupPricingModel returns the rate card that prices model: the entry for
// that exact name, or the entry for the model it is a dated snapshot of.
func lookupPricingModel(model string) (PricingModel, bool) {
	// Normalise before the exact lookup, not after. Every key in the table is
	// lower case, so a caller passing "GPT-4" or a value with stray whitespace
	// would otherwise miss the entry and be reported unpriced -- an accidental
	// gap in the one signal that is supposed to mean "we genuinely do not know
	// this model's rate".
	model = strings.TrimSpace(strings.ToLower(model))

	pricingMu.RLock()
	defer pricingMu.RUnlock()

	if pricing, exists := pricingModels[model]; exists {
		return pricing, true
	}

	// A name that is not in the table is priced only when it is a version of a
	// model that is: the same name with its version tag removed, which is how
	// providers name a snapshot ("gpt-5-2025-08-07", "claude-3-opus-20240229").
	// A suffix that names a different model ("gpt-5-pro", "gpt-5.4-mini",
	// "gpt-6-luna-mini") is not a tag, so it is not stripped and the model is
	// unpriced: a sibling of a priced model has its own rates, and the base
	// model's would be a confident wrong figure reported as exact.
	if base := llm.WithoutVersionTag(model); base != model {
		if pricing, exists := pricingModels[base]; exists {
			return pricing, true
		}
	}

	return PricingModel{}, false
}

// spreadsheetSafe returns text unchanged unless a spreadsheet would read it as
// a formula, in which case it gets a leading apostrophe.
func spreadsheetSafe(text string) string {
	if text != "" && strings.ContainsRune("=+-@\t\r", rune(text[0])) {
		return "'" + text
	}
	return text
}

// unpricedWarnLimit bounds how many distinct unpriced models are remembered
// for warning once. Model names come from callers, so an unbounded set would
// grow with whatever they pass.
const unpricedWarnLimit = 256

var (
	unpricedWarnMu sync.Mutex
	unpricedWarned = map[string]bool{}
)

// warnUnpricedOnce logs that a model has no rate card the first time it is
// seen. A loop of ten thousand calls on an unpriced model used to log ten
// thousand identical lines. Once the remembered set is full, further new
// models are not logged; the cost they report is still unpriced.
//
// The line goes to log, the logger of the call that priced the model, and to the
// process logger when the call has none. The model and the provider are names
// the caller or a provider chose, so they are written bounded as a metric tag
// is (telemetry.BoundedTagValue): a name with a newline in it cannot forge a
// line of a log, and a long one cannot fill one.
func warnUnpricedOnce(log *logger.Logger, model, provider string) {
	key := provider + "\x00" + model
	unpricedWarnMu.Lock()
	seen := unpricedWarned[key]
	if !seen && len(unpricedWarned) < unpricedWarnLimit {
		unpricedWarned[key] = true
	} else {
		seen = true
	}
	unpricedWarnMu.Unlock()
	if !seen {
		if log == nil {
			log = logger.GetLogger()
		}
		log.Warn("No pricing information available; cost reported as unpriced",
			"model", telemetry.BoundedTagValue(model), "provider", telemetry.BoundedTagValue(provider))
	}
}

func updateCostTotal(key string, amount float64) {
	totalCosts[key] += amount
}

// allTimeKey is the key of the total that has no period.
const allTimeKey = "all_time"

// periodKeys are the keys the running totals of one calendar day are kept
// under, with the day they are for.
type periodKeys struct {
	year    int
	month   time.Month
	day     int
	periods [3]budgetPeriod
}

// currentPeriodKeys holds the keys for the day somebody last asked about. The
// keys of the totals were made with four Sprintf calls and three time
// formattings on every call, to find out what is the same all day.
var currentPeriodKeys atomic.Pointer[periodKeys]

// periodKeysFor returns the keys for the calendar day of now, in now's own
// zone, which is what the formatted dates they replace were in. The value is
// shared and is not changed.
func periodKeysFor(now time.Time) *periodKeys {
	year, month, day := now.Date()
	if held := currentPeriodKeys.Load(); held != nil && held.year == year && held.month == month && held.day == day {
		return held
	}
	made := &periodKeys{
		year:  year,
		month: month,
		day:   day,
		periods: [3]budgetPeriod{
			{"daily", "daily_" + dateKey(year, month, day)},
			{"weekly", "weekly_" + weekKey(now)},
			{"monthly", "monthly_" + monthKey(year, month)},
		},
	}
	currentPeriodKeys.Store(made)
	return made
}

// dateKey, weekKey and monthKey are what time.Format("2006-01-02"), the ISO
// week as "%d-W%02d" and time.Format("2006-01") give, written
// without them: the keys are made on the first call of a process, and fmt and
// the formatter are a good deal of code to run for the first time then.
func dateKey(year int, month time.Month, day int) string {
	var buffer [10]byte
	encoded := appendPadded(buffer[:0], year, 4)
	encoded = append(encoded, '-')
	encoded = appendPadded(encoded, int(month), 2)
	encoded = append(encoded, '-')
	encoded = appendPadded(encoded, day, 2)
	return string(encoded)
}

func monthKey(year int, month time.Month) string {
	var buffer [7]byte
	encoded := appendPadded(buffer[:0], year, 4)
	encoded = append(encoded, '-')
	encoded = appendPadded(encoded, int(month), 2)
	return string(encoded)
}

func weekKey(t time.Time) string {
	year, week := t.ISOWeek()
	var buffer [8]byte
	encoded := strconv.AppendInt(buffer[:0], int64(year), 10)
	encoded = append(encoded, '-', 'W')
	encoded = appendPadded(encoded, week, 2)
	return string(encoded)
}

// appendPadded appends n in decimal, with leading zeros to at least width
// digits.
func appendPadded(destination []byte, n, width int) []byte {
	digits := strconv.AppendInt(nil, int64(n), 10)
	for i := len(digits); i < width; i++ {
		destination = append(destination, '0')
	}
	return append(destination, digits...)
}

func matchesFilters(record CostRecord, filters map[string]string) bool {
	for key, value := range filters {
		switch key {
		case "request_id":
			if record.RequestID != value {
				return false
			}
		case "correlation_id":
			if record.CorrelationID != value {
				return false
			}
		case "model":
			if record.Model != value {
				return false
			}
		case "provider":
			if record.Provider != value {
				return false
			}
		case "operation":
			if record.Operation != value {
				return false
			}
		default:
			// Check tags
			if tagValue, exists := record.tag(key); !exists || tagValue != value {
				return false
			}
		}
	}
	return true
}

// budgetThresholds are the fractions of a limit worth telling somebody about.
//
// The old check fired above 80% and had no memory, so an integration that pages
// on the callback got paged once per request for the rest of the period --
// which is how a budget alert becomes a filter rule, and then becomes nothing.
var budgetThresholds = []float64{0.8, 1.0}

// budgetPeriod is one budget window and the key its running total is kept under.
type budgetPeriod struct {
	name string
	key  string
}

// dueBudgetNotices returns the thresholds the totals have newly crossed and
// marks them delivered. The caller holds costMutex and delivers the notices
// after releasing it. keys are the period keys of the call that was just
// recorded.
func dueBudgetNotices(keys *periodKeys) []budgetNotice {
	if budgetCallback == nil || budgetLimits == nil {
		return nil
	}

	if budgetNotified == nil {
		budgetNotified = make(map[string]uint8)
	}

	var notices []budgetNotice
	for _, period := range keys.periods {
		limit, exists := budgetLimits[period.name]
		if !exists || !(limit > 0) {
			continue
		}

		current, ok := totalCosts[period.key]
		if !ok {
			continue
		}

		for index, fraction := range budgetThresholds {
			if current < limit*fraction {
				continue
			}
			// The key carries the period instance, so tomorrow's budget alerts
			// again rather than staying silent because yesterday's fired. One
			// bit per threshold, so that looking an announcement up builds no
			// key while the ledger is locked.
			bit := uint8(1) << index
			if budgetNotified[period.key]&bit != 0 {
				continue
			}
			budgetNotified[period.key] |= bit
			notices = append(notices, budgetNotice{current: current, limit: limit, period: period.name})
		}
	}
	return notices
}

// pushCostRecord appends a record to the bounded history, evicting the oldest
// when the ring is full. The caller holds costMutex, and the record is not
// changed afterwards.
func pushCostRecord(record *CostRecord) {
	if costLimit <= 0 {
		costLimit = DefaultCostHistoryLimit
	}
	if costIndex == nil {
		costIndex = make(map[string]int, min(costLimit, 1024))
	}
	if costHistory == nil {
		costHistory = make([]*CostRecord, 0, min(costLimit, 1024))
	}

	if costCount < costLimit {
		slot := (costStart + costCount) % costLimit
		if slot < len(costHistory) {
			costHistory[slot] = record
		} else {
			costHistory = append(costHistory, record)
			slot = len(costHistory) - 1
		}
		costCount++
		indexCostRecord(record, slot)
		return
	}

	// Full: overwrite the oldest and move the window forward. The evicted
	// record's index entry goes with it, or GetRequestCost would return a slot
	// now holding somebody else's request.
	slot := costStart
	evicted := costHistory[slot]
	if existing, ok := costIndex[evicted.RequestID]; ok && existing == slot {
		delete(costIndex, evicted.RequestID)
	}
	costHistory[slot] = record
	costStart = (costStart + 1) % costLimit
	indexCostRecord(record, slot)
}

func indexCostRecord(record *CostRecord, slot int) {
	if record.RequestID == "" {
		return
	}
	// A repeated request ID resolves to its most recent record, which is what a
	// caller asking "what did this request cost" means after a retry.
	costIndex[record.RequestID] = slot
}

// costRecordsSnapshot returns the retained records, oldest first. The read lock
// is held for the copy of the pointers and not for what the caller then does
// with them: a walk of the whole history under it held every call that was
// recording a cost behind the walk. The records are not changed once they are
// in the history, so they can be read without it.
func costRecordsSnapshot() []*CostRecord {
	costMutex.RLock()
	defer costMutex.RUnlock()

	if costLimit <= 0 {
		return nil
	}
	records := make([]*CostRecord, costCount)
	for i := range records {
		records[i] = costHistory[(costStart+i)%costLimit]
	}
	return records
}

// forEachCostRecord walks the history oldest-first, taking the read lock only
// to copy it (costRecordsSnapshot). The caller does not hold costMutex.
func forEachCostRecord(visit func(CostRecord)) {
	for _, record := range costRecordsSnapshot() {
		visit(*record)
	}
}

// SetCostHistoryLimit bounds how many cost records are retained in memory.
//
// Changing the limit discards the existing history rather than resizing it: the
// alternative is a partial copy whose contents depend on the old and new sizes,
// and a caller setting this is configuring a process, not curating a dataset.
// A limit of zero or less restores the default.
func SetCostHistoryLimit(limit int) {
	costMutex.Lock()
	defer costMutex.Unlock()

	if limit <= 0 {
		limit = DefaultCostHistoryLimit
	}
	costLimit = limit
	costHistory = nil
	costIndex = nil
	costStart = 0
	costCount = 0
}

// CostHistoryLen reports how many records are currently retained.
func CostHistoryLen() int {
	costMutex.RLock()
	defer costMutex.RUnlock()
	return costCount
}

// SetBudgetEnforcement controls whether an exhausted budget refuses calls.
//
// Off by default. Budgets in this library have always been advisory, and a
// release that quietly started returning errors mid-run would be a worse
// surprise than the alert noise it replaces.
func SetBudgetEnforcement(enforce bool) {
	costMutex.Lock()
	defer costMutex.Unlock()
	budgetEnforcing.Store(enforce)
}

// CheckBudget reports whether a call may proceed, so the caller can refuse
// before spending rather than discovering the overrun in the invoice.
//
// It returns ErrBudgetExceeded only when enforcement is on. With enforcement
// off it always allows the call, and the callback does the talking.
//
// The spend it compares with a limit is the priced spend. A call of unknown
// cost adds nothing to it, so the comparison is against a floor and a period
// can be over its limit without the check knowing; the check does not guess at
// what unpriced calls cost, and it does not refuse every call because some
// were unpriced, which would lock out a caller that mixes in a subscription
// transport. When it refuses it says how many calls of unknown cost the period
// holds, and GetTotalSpend reports whether a total is complete.
func CheckBudget() error {
	// With enforcement off, which is the default, there is nothing to look at
	// and the lock that recording a cost takes is not touched by the check every
	// call makes.
	if !budgetEnforcing.Load() {
		return nil
	}

	costMutex.RLock()
	defer costMutex.RUnlock()

	if !budgetEnforcing.Load() || budgetLimits == nil {
		return nil
	}

	// The windows containing now, shortest first, so that when more than one is
	// over its limit the one reported is always the same.
	for _, period := range periodKeysFor(time.Now()).periods {
		limit, ok := budgetLimits[period.name]
		if !ok || !(limit > 0) {
			continue
		}
		if spent := totalCosts[period.key]; spent >= limit {
			note := ""
			if unknown := unpricedCalls[period.key]; unknown > 0 {
				note = fmt.Sprintf(" (plus %d call(s) of unknown cost, so the spend is a floor)", unknown)
			}
			// Classified, so that the error has a kind. Its text is a message callers
			// already read and match on, and it still matches ErrBudgetExceeded.
			return types.Classified(types.KindBudgetExceeded, fmt.Errorf("%w: %s spend $%.4f has reached the $%.2f limit%s",
				ErrBudgetExceeded, period.name, spent, limit, note))
		}
	}

	return nil
}

// ResetBudget clears the configured limits, the callback, and the notification
// state, leaving the recorded history alone.
//
// It is separate from ResetCostTracking on purpose. That function used to null
// budgetLimits and budgetCallback as well, so a test or a service clearing its
// history silently disabled budget alerting -- the two operations have nothing
// to do with each other, and the one people call routinely was the one with the
// side effect.
func ResetBudget() {
	costMutex.Lock()
	defer costMutex.Unlock()

	budgetLimits = nil
	budgetCallback = nil
	budgetNotified = nil
	budgetEnforcing.Store(false)
}

// pricingMu guards the rate table. It exists because RegisterPricingModel
// makes the table writable at runtime, and a map written by one goroutine
// while another prices a request is a data race that shows up as a corrupted
// invoice rather than a crash.
var pricingMu sync.RWMutex

// ErrInvalidPricingModel reports a rate card this package will not accept.
var ErrInvalidPricingModel = errors.New("pricing: invalid pricing model")

// RegisterPricingModel adds or replaces a model's rates.
//
// It exists because the built-in table is a snapshot of public list prices and
// will always be behind, and because a caller on negotiated or enterprise
// rates has never been able to say so: their costs were either computed from
// somebody else's prices or reported unpriced. Registering their own rate card
// is the difference between an accounting figure they can reconcile and one
// they cannot.
//
// The model name is normalised the same way lookups are, so registering
// "GPT-5.6-Luna" prices "gpt-5.6-luna".
//
// Rates are per 1,000 tokens, matching the built-in table. Getting that wrong
// by a factor of a thousand is the obvious mistake here, so a rate above
// oneThousandTokenSanityCeiling is refused: no real model costs more than a
// dollar per thousand tokens, and a caller who meant per-million has said
// something this package can tell is wrong.
func RegisterPricingModel(model PricingModel) error {
	name := strings.TrimSpace(strings.ToLower(model.Model))
	if name == "" {
		return fmt.Errorf("%w: no model name", ErrInvalidPricingModel)
	}
	if strings.TrimSpace(model.Provider) == "" {
		return fmt.Errorf("%w: %q has no provider", ErrInvalidPricingModel, name)
	}

	rates := map[string]float64{
		"prompt":     model.PricePerPromptToken,
		"completion": model.PricePerCompletionToken,
		"cached":     model.PriceCachedToken,
		"reasoning":  model.PriceReasoningToken,
	}
	for _, kind := range []string{"prompt", "completion", "cached", "reasoning"} {
		rate := rates[kind]
		// NaN is neither below zero nor above the ceiling, so it needs its own
		// test: one NaN rate would make every cost priced from this card NaN.
		if math.IsNaN(rate) {
			return fmt.Errorf("%w: %q has a %s rate that is not a number", ErrInvalidPricingModel, name, kind)
		}
		if rate < 0 {
			return fmt.Errorf("%w: %q has a negative %s rate (%v)", ErrInvalidPricingModel, name, kind, rate)
		}
		if rate > oneThousandTokenSanityCeiling {
			return fmt.Errorf(
				"%w: %q has a %s rate of %v per 1K tokens, which is implausible -- rates in this table are per 1,000 tokens, not per 1,000,000",
				ErrInvalidPricingModel, name, kind, rate)
		}
	}

	// A rate card with every rate at zero would report real spending as free.
	// PR-001's rule is that unpriced is not free; this is the same rule from
	// the other side, because "priced at nothing" is the more convincing lie.
	if model.PricePerPromptToken == 0 && model.PricePerCompletionToken == 0 {
		return fmt.Errorf(
			"%w: %q prices both prompt and completion tokens at zero, which would report real spending as free; omit the model instead and let it report unpriced",
			ErrInvalidPricingModel, name)
	}

	model.Model = name
	if strings.TrimSpace(model.Currency) == "" {
		model.Currency = "USD"
	}

	pricingMu.Lock()
	defer pricingMu.Unlock()
	pricingModels[name] = model
	return nil
}

// oneThousandTokenSanityCeiling is the highest per-1K-token rate this package
// will accept. The most expensive public model is comfortably under a cent per
// thousand tokens; a dollar leaves three orders of magnitude of headroom and
// still catches the per-million mix-up, which is the mistake that actually
// happens.
const oneThousandTokenSanityCeiling = 1.0

// PricingModelNamed returns the rate card the table holds under exactly this
// name, which is normalised as a lookup normalises it, and whether there is one.
// It does not fall back to the model a dated snapshot is a version of: it says
// what an entry is, so that a function that replaces one can put it back.
func PricingModelNamed(model string) (PricingModel, bool) {
	name := strings.TrimSpace(strings.ToLower(model))
	pricingMu.RLock()
	defer pricingMu.RUnlock()
	card, ok := pricingModels[name]
	return card, ok
}

// ReplacePricingModel puts card in the table under the name, exactly as it is
// given and without the checks RegisterPricingModel makes, or removes the entry
// when card is nil. It is how an entry that RegisterPricingModel replaced is put
// back: the card it is given was in the table, and was checked then.
func ReplacePricingModel(model string, card *PricingModel) {
	name := strings.TrimSpace(strings.ToLower(model))
	pricingMu.Lock()
	defer pricingMu.Unlock()
	if card == nil {
		delete(pricingModels, name)
		return
	}
	pricingModels[name] = *card
}

// RegisteredPricingModels reports the models with known rates, sorted, so a
// caller can see what is priced without guessing. A model absent from this
// list reports unpriced rather than free.
func RegisteredPricingModels() []string {
	pricingMu.RLock()
	defer pricingMu.RUnlock()

	names := make([]string, 0, len(pricingModels))
	for name := range pricingModels {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
