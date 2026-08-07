package pricing

import "time"

// CostSummary aggregates request, token, and cost data for a time range.
type CostSummary struct {
	RequestCount            int
	TotalCost               float64
	AverageCostPerRequest   float64
	TotalPromptTokens       int
	TotalCompletionTokens   int
	TotalCachedTokens       int
	TotalReasoningTokens    int
	TotalTokens             int
	AveragePromptTokens     float64
	AverageCompletionTokens float64
	AverageCachedTokens     float64
	AverageReasoningTokens  float64
	AverageTokensPerRequest float64
	AveragePromptCost       float64
	AverageCompletionCost   float64
	AverageCachedCost       float64
	AverageReasoningCost    float64
}

// GetRequestCosts returns the request-level cost history for a time range.
func GetRequestCosts(since time.Time, filters map[string]string) []CostRecord {
	costMutex.RLock()
	defer costMutex.RUnlock()

	records := make([]CostRecord, 0, costCount)
	forEachCostRecord(func(record CostRecord) {
		if record.Timestamp.Before(since) || !matchesFilters(record, filters) {
			return
		}
		records = append(records, cloneCostRecord(record))
	})

	return records
}

// GetRequestCost returns a single request-level cost record by request ID.
func GetRequestCost(requestID string) (CostRecord, bool) {
	costMutex.RLock()
	defer costMutex.RUnlock()

	slot, ok := costIndex[requestID]
	if !ok || slot >= len(costHistory) {
		return CostRecord{}, false
	}

	return cloneCostRecord(costHistory[slot]), true
}

// GetCostSummary returns aggregate totals and averages for a time range.
func GetCostSummary(since time.Time, filters map[string]string) CostSummary {
	costMutex.RLock()
	defer costMutex.RUnlock()

	var summary CostSummary

	forEachCostRecord(func(record CostRecord) {
		if record.Timestamp.Before(since) || !matchesFilters(record, filters) {
			return
		}

		summary.RequestCount++
		summary.TotalCost += record.Cost.TotalCost
		summary.TotalPromptTokens += record.TokenUsage.PromptTokens
		summary.TotalCompletionTokens += record.TokenUsage.CompletionTokens
		summary.TotalCachedTokens += record.TokenUsage.CachedTokens
		summary.TotalReasoningTokens += record.TokenUsage.ReasoningTokens
		summary.TotalTokens += record.TokenUsage.TotalTokens
		summary.AveragePromptCost += record.Cost.PromptCost
		summary.AverageCompletionCost += record.Cost.CompletionCost
		summary.AverageCachedCost += record.Cost.CachedCost
		summary.AverageReasoningCost += record.Cost.ReasoningCost
	})

	if summary.RequestCount == 0 {
		return summary
	}

	requests := float64(summary.RequestCount)
	summary.AverageCostPerRequest = summary.TotalCost / requests
	summary.AveragePromptTokens = float64(summary.TotalPromptTokens) / requests
	summary.AverageCompletionTokens = float64(summary.TotalCompletionTokens) / requests
	summary.AverageCachedTokens = float64(summary.TotalCachedTokens) / requests
	summary.AverageReasoningTokens = float64(summary.TotalReasoningTokens) / requests
	summary.AverageTokensPerRequest = float64(summary.TotalTokens) / requests
	summary.AveragePromptCost /= requests
	summary.AverageCompletionCost /= requests
	summary.AverageCachedCost /= requests
	summary.AverageReasoningCost /= requests

	return summary
}

// ResetCostTracking clears the recorded history and the running totals.
//
// It deliberately leaves the budget configuration alone. It used to null
// budgetLimits and budgetCallback too, so clearing history -- which tests do
// between cases and services do on a schedule -- silently switched off budget
// alerting. Use ResetBudget for that.
func ResetCostTracking() {
	costMutex.Lock()
	defer costMutex.Unlock()

	totalCosts = nil
	costHistory = nil
	costIndex = nil
	costStart = 0
	costCount = 0

	// The notification state belongs to the totals, not to the limits: once
	// spend is back at zero, a later crossing is a new edge and must alert.
	budgetNotified = nil
}

func cloneCostRecord(record CostRecord) CostRecord {
	cloned := record
	if len(record.Tags) > 0 {
		cloned.Tags = make(map[string]string, len(record.Tags))
		for key, value := range record.Tags {
			cloned.Tags[key] = value
		}
	}
	return cloned
}
