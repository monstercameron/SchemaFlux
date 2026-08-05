// Package pricing - Pricing and cost tracking for LLM operations
package pricing

import (
	"encoding/json"
	"fmt"
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
	}

	// Cost tracking state
	costMutex      sync.RWMutex
	totalCosts     map[string]float64 // Track costs by dimension (e.g., "daily", "weekly", "monthly")
	costHistory    []CostRecord
	budgetLimits   map[string]float64
	budgetCallback func(current, limit float64, period string)
)

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
	}
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
	if costHistory == nil {
		costHistory = make([]CostRecord, 0)
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
	costHistory = append(costHistory, record)

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
	for _, record := range costHistory {
		if record.Timestamp.Before(since) {
			continue
		}

		// Apply filters
		if !matchesFilters(record, filters) {
			continue
		}

		total += record.Cost.TotalCost
	}

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

	for _, record := range costHistory {
		if record.Timestamp.Before(since) {
			continue
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
	}

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
		for _, record := range costHistory {
			if record.Timestamp.Before(since) {
				continue
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
		}
	case "json":
		records := make([]CostRecord, 0, len(costHistory))
		for _, record := range costHistory {
			if record.Timestamp.Before(since) {
				continue
			}
			records = append(records, cloneCostRecord(record))
		}
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

// MatchesFilters checks if a record matches the given filters. Exported for testing.
func MatchesFilters(record CostRecord, filters map[string]string) bool {
	return matchesFilters(record, filters)
}

// Helper functions

func lookupPricingModel(model string) (PricingModel, bool) {
	if pricing, exists := pricingModels[model]; exists {
		return pricing, true
	}

	model = strings.TrimSpace(strings.ToLower(model))

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

func checkBudgetLimits() {
	if budgetCallback == nil || budgetLimits == nil {
		return
	}

	now := time.Now()

	// Check daily budget
	if limit, exists := budgetLimits["daily"]; exists && limit > 0 {
		dailyKey := fmt.Sprintf("daily_%s", now.Format("2006-01-02"))
		if current, ok := totalCosts[dailyKey]; ok && current > limit*0.8 {
			budgetCallback(current, limit, "daily")
		}
	}

	// Check weekly budget
	if limit, exists := budgetLimits["weekly"]; exists && limit > 0 {
		weeklyKey := fmt.Sprintf("weekly_%s", getWeekKey(now))
		if current, ok := totalCosts[weeklyKey]; ok && current > limit*0.8 {
			budgetCallback(current, limit, "weekly")
		}
	}

	// Check monthly budget
	if limit, exists := budgetLimits["monthly"]; exists && limit > 0 {
		monthlyKey := fmt.Sprintf("monthly_%s", now.Format("2006-01"))
		if current, ok := totalCosts[monthlyKey]; ok && current > limit*0.8 {
			budgetCallback(current, limit, "monthly")
		}
	}
}
