// Package pricing - Pricing and cost tracking for LLM operations
package pricing

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/types"
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

		// OpenAI o1 models (with reasoning tokens)
		"o1-preview": {
			Provider:                "openai",
			Model:                   "o1-preview",
			PricePerPromptToken:     0.015, // $15 per 1M tokens
			PricePerCompletionToken: 0.06,  // $60 per 1M tokens
			PriceReasoningToken:     0.015, // $15 per 1M reasoning tokens
			Currency:                "USD",
			EffectiveDate:           time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		"o1-mini": {
			Provider:                "openai",
			Model:                   "o1-mini",
			PricePerPromptToken:     0.003, // $3 per 1M tokens
			PricePerCompletionToken: 0.012, // $12 per 1M tokens
			PriceReasoningToken:     0.003, // $3 per 1M reasoning tokens
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

		// Cerebras. Priced here rather than left to the provider's flat default
		// because an unpriced model reports zero cost, and a secondary provider
		// whose whole appeal is being cheaper is exactly the one a caller will
		// want a real number for.
		"gemma-4-31b": {
			Provider:                "cerebras",
			Model:                   "gemma-4-31b",
			PricePerPromptToken:     0.00099, // $0.99 per 1M tokens
			PricePerCompletionToken: 0.00149, // $1.49 per 1M tokens
			Currency:                "USD",
			EffectiveDate:           time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC),
		},
	}

	// Cost tracking state
	costMutex   sync.RWMutex
	totalCosts  map[string]float64 // Track costs by dimension (e.g., "daily", "weekly", "monthly")
	costHistory []CostRecord

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
	// integration got one notification per call for the rest of the day.
	budgetNotified map[string]bool

	// budgetEnforce turns the alert into a limit. The default is false: budgets
	// have always been advisory here, and silently starting to refuse calls
	// would be a worse surprise than the noise it replaces.
	budgetEnforce bool
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
}

// CalculateCost calculates the cost of an LLM operation
func CalculateCost(usage *types.TokenUsage, model string, provider string) *types.CostInfo {
	if usage == nil {
		return nil
	}

	// Look up pricing for this exact model. There is deliberately no fallback to
	// another model's rates: the previous behaviour priced every unrecognised
	// Anthropic model at claude-3-haiku, understating an Opus call by roughly
	// 60x while presenting the result as a precise USD figure.
	pricing, exists := lookupPricingModel(model)
	if !exists {
		logger.GetLogger().Warn("No pricing information available; cost reported as unpriced",
			"model", model, "provider", provider)
		return &types.CostInfo{
			Currency: "USD",
			Priced:   false,
			Quality:  types.PricingUnknown,
		}
	}

	// Calculate costs (prices are per 1K tokens)
	promptCost := float64(usage.PromptTokens) * pricing.PricePerPromptToken / 1000.0
	completionCost := float64(usage.CompletionTokens) * pricing.PricePerCompletionToken / 1000.0

	var cachedCost, reasoningCost float64
	if usage.CachedTokens > 0 && pricing.PriceCachedToken > 0 {
		cachedCost = float64(usage.CachedTokens) * pricing.PriceCachedToken / 1000.0
	}
	if usage.ReasoningTokens > 0 && pricing.PriceReasoningToken > 0 {
		reasoningCost = float64(usage.ReasoningTokens) * pricing.PriceReasoningToken / 1000.0
	}

	totalCost := promptCost + completionCost + cachedCost + reasoningCost

	return &types.CostInfo{
		TotalCost:               totalCost,
		PromptCost:              promptCost,
		CompletionCost:          completionCost,
		CachedCost:              cachedCost,
		ReasoningCost:           reasoningCost,
		Currency:                pricing.Currency,
		PricePerPromptToken:     pricing.PricePerPromptToken,
		PricePerCompletionToken: pricing.PricePerCompletionToken,
		Priced:                  true,
		PricingSource:           pricing.Model,
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

// TrackCost records a cost entry for monitoring and budgeting
func TrackCost(cost *types.CostInfo, metadata *types.ResultMetadata) {
	if cost == nil || metadata == nil {
		return
	}

	costMutex.Lock()
	defer costMutex.Unlock()

	// Initialize maps if needed
	if totalCosts == nil {
		totalCosts = make(map[string]float64)
	}

	// Create cost record
	record := CostRecord{
		Timestamp:     time.Now(),
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
		record.Tags = make(map[string]string)
		for k, v := range metadata.Custom {
			record.Tags[k] = fmt.Sprintf("%v", v)
		}
	}

	// Add to history
	pushCostRecord(record)

	// Update totals
	now := time.Now()
	updateCostTotal("all_time", cost.TotalCost)
	updateCostTotal(fmt.Sprintf("daily_%s", now.Format("2006-01-02")), cost.TotalCost)
	updateCostTotal(fmt.Sprintf("weekly_%s", getWeekKey(now)), cost.TotalCost)
	updateCostTotal(fmt.Sprintf("monthly_%s", now.Format("2006-01")), cost.TotalCost)

	// Check budget limits
	checkBudgetLimits()

	// Log high-cost operations
	if cost.TotalCost > 0.10 { // Log operations over $0.10
		totalTokens := 0
		if metadata.TokenUsage != nil {
			totalTokens = metadata.TokenUsage.TotalTokens
		}
		logger.GetLogger().Info("High-cost operation tracked",
			"requestID", metadata.RequestID,
			"operation", metadata.Operation,
			"model", metadata.Model,
			"cost", fmt.Sprintf("$%.4f", cost.TotalCost),
			"tokens", totalTokens,
		)
	}
}

// GetTotalCost returns accumulated cost for a time period
func GetTotalCost(since time.Time, filters map[string]string) float64 {
	costMutex.RLock()
	defer costMutex.RUnlock()

	var total float64
	forEachCostRecord(func(record CostRecord) {
		if record.Timestamp.Before(since) || !matchesFilters(record, filters) {
			return
		}
		total += record.Cost.TotalCost
	})

	return total
}

// SetBudget configures cost limits and alerts
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

// GetCostBreakdown returns detailed cost breakdown for a period
func GetCostBreakdown(since time.Time) map[string]float64 {
	costMutex.RLock()
	defer costMutex.RUnlock()

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

// ExportCostReport generates a detailed cost report
func ExportCostReport(since time.Time, format string) (string, error) {
	costMutex.RLock()
	defer costMutex.RUnlock()

	var report string

	switch format {
	case "csv":
		report = "Timestamp,RequestID,CorrelationID,Operation,Model,Provider,PromptTokens,CompletionTokens,TotalTokens,Cost\n"
		forEachCostRecord(func(record CostRecord) {
			if record.Timestamp.Before(since) {
				return
			}
			report += fmt.Sprintf("%s,%s,%s,%s,%s,%s,%d,%d,%d,%.4f\n",
				record.Timestamp.Format(time.RFC3339),
				record.RequestID,
				record.CorrelationID,
				record.Operation,
				record.Model,
				record.Provider,
				record.TokenUsage.PromptTokens,
				record.TokenUsage.CompletionTokens,
				record.TokenUsage.TotalTokens,
				record.Cost.TotalCost,
			)
		})
	case "json":
		records := make([]CostRecord, 0, costCount)
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

func lookupPricingModel(model string) (PricingModel, bool) {
	// Normalise before the exact lookup, not after. Every key in the table is
	// lower case, so a caller passing "GPT-4" or a value with stray whitespace
	// used to miss the exact match, fall through the prefix ladder, and be
	// reported unpriced -- an accidental gap in the one signal that is supposed
	// to mean "we genuinely do not know this model's rate".
	model = strings.TrimSpace(strings.ToLower(model))

	pricingMu.RLock()
	defer pricingMu.RUnlock()

	if pricing, exists := pricingModels[model]; exists {
		return pricing, true
	}

	// Prefix matching exists to price dated snapshots (gpt-5-2025-08-07) at
	// their base model's rates. It must not reach across model versions:
	// "gpt-5.6-luna" starts with "gpt-5" but is a different model with
	// different rates, and pricing it from the gpt-5 entry is the same
	// substitution error as pricing every Anthropic model as Haiku.
	for _, base := range []string{"gpt-5.4", "gpt-5-mini", "gpt-5-nano", "gpt-5"} {
		if isSnapshotOf(model, base) {
			pricing, ok := pricingModels[base]
			return pricing, ok
		}
	}

	return PricingModel{}, false
}

// isSnapshotOf reports whether model is base itself or a dated snapshot of it.
// A suffix beginning with "." denotes a different version, not a snapshot.
func isSnapshotOf(model, base string) bool {
	if !strings.HasPrefix(model, base) {
		return false
	}
	suffix := model[len(base):]
	return suffix == "" || strings.HasPrefix(suffix, "-")
}

func updateCostTotal(key string, amount float64) {
	totalCosts[key] += amount
}

func getWeekKey(t time.Time) string {
	year, week := t.ISOWeek()
	return fmt.Sprintf("%d-W%02d", year, week)
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
			if tagValue, exists := record.Tags[key]; !exists || tagValue != value {
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

func checkBudgetLimits() {
	if budgetCallback == nil || budgetLimits == nil {
		return
	}

	if budgetNotified == nil {
		budgetNotified = make(map[string]bool)
	}

	now := time.Now()
	periods := []struct {
		name string
		key  string
	}{
		{"daily", fmt.Sprintf("daily_%s", now.Format("2006-01-02"))},
		{"weekly", fmt.Sprintf("weekly_%s", getWeekKey(now))},
		{"monthly", fmt.Sprintf("monthly_%s", now.Format("2006-01"))},
	}

	for _, period := range periods {
		limit, exists := budgetLimits[period.name]
		if !exists || limit <= 0 {
			continue
		}

		current, ok := totalCosts[period.key]
		if !ok {
			continue
		}

		for _, fraction := range budgetThresholds {
			if current < limit*fraction {
				continue
			}
			// The key carries the period instance, so tomorrow's budget alerts
			// again rather than staying silent because yesterday's fired.
			seen := fmt.Sprintf("%s@%.2f", period.key, fraction)
			if budgetNotified[seen] {
				continue
			}
			budgetNotified[seen] = true
			budgetCallback(current, limit, period.name)
		}
	}
}

// pushCostRecord appends a record to the bounded history, evicting the oldest
// when the ring is full. The caller holds costMutex.
func pushCostRecord(record CostRecord) {
	if costLimit <= 0 {
		costLimit = DefaultCostHistoryLimit
	}
	if costIndex == nil {
		costIndex = make(map[string]int, costLimit)
	}
	if costHistory == nil {
		costHistory = make([]CostRecord, 0, min(costLimit, 1024))
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

func indexCostRecord(record CostRecord, slot int) {
	if record.RequestID == "" {
		return
	}
	// A repeated request ID resolves to its most recent record, which is what a
	// caller asking "what did this request cost" means after a retry.
	costIndex[record.RequestID] = slot
}

// forEachCostRecord walks the history oldest-first. The caller holds costMutex.
func forEachCostRecord(visit func(CostRecord)) {
	if costLimit <= 0 {
		return
	}
	for i := 0; i < costCount; i++ {
		visit(costHistory[(costStart+i)%costLimit])
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
	budgetEnforce = enforce
}

// CheckBudget reports whether a call may proceed, so the caller can refuse
// before spending rather than discovering the overrun in the invoice.
//
// It returns ErrBudgetExceeded only when enforcement is on. With enforcement
// off it always allows the call, and the callback does the talking.
func CheckBudget() error {
	costMutex.RLock()
	defer costMutex.RUnlock()

	if !budgetEnforce || budgetLimits == nil {
		return nil
	}

	now := time.Now()
	for period, key := range map[string]string{
		"daily":   fmt.Sprintf("daily_%s", now.Format("2006-01-02")),
		"weekly":  fmt.Sprintf("weekly_%s", getWeekKey(now)),
		"monthly": fmt.Sprintf("monthly_%s", now.Format("2006-01")),
	} {
		limit, ok := budgetLimits[period]
		if !ok || limit <= 0 {
			continue
		}
		if totalCosts[key] >= limit {
			return fmt.Errorf("%w: %s spend $%.4f has reached the $%.2f limit",
				ErrBudgetExceeded, period, totalCosts[key], limit)
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
	budgetEnforce = false
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
