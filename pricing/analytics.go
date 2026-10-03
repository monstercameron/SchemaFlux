package pricing

import "time"

// CostSummary aggregates request, token, and cost data for a time range.
type CostSummary struct {
	RequestCount int
	// UnpricedRequests counts the requests in the range whose cost was not
	// priced. They add nothing to TotalCost or the averages derived from it, so
	// when this is not zero the figures are a floor.
	UnpricedRequests        int
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
	snapshot := costRecordsSnapshot()
	records := make([]CostRecord, 0, len(snapshot))
	for _, held := range snapshot {
		record := *held
		if record.Timestamp.Before(since) || !matchesFilters(record, filters) {
			continue
		}
		records = append(records, cloneCostRecord(record))
	}

	return records
}

// GetRequestCost returns a single request-level cost record by request ID.
func GetRequestCost(requestID string) (CostRecord, bool) {
	costMutex.RLock()
	slot, ok := costIndex[requestID]
	var held *CostRecord
	if ok && slot < len(costHistory) {
		held = costHistory[slot]
	}
	costMutex.RUnlock()

	if held == nil {
		return CostRecord{}, false
	}
	return cloneCostRecord(*held), true
}

// GetCostSummary returns aggregate totals and averages for a time range.
func GetCostSummary(since time.Time, filters map[string]string) CostSummary {
	var summary CostSummary

	forEachCostRecord(func(record CostRecord) {
		if record.Timestamp.Before(since) || !matchesFilters(record, filters) {
			return
		}

		summary.RequestCount++
		if !record.Cost.Priced {
			summary.UnpricedRequests++
		}
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
	unpricedCalls = nil
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
	cloned.pairs, cloned.hasPairs = nil, false
	switch {
	case record.hasPairs:
		cloned.Tags = make(map[string]string, len(record.pairs))
		for _, pair := range record.pairs {
			cloned.Tags[pair.key] = pair.value
		}
	case len(record.Tags) > 0:
		cloned.Tags = make(map[string]string, len(record.Tags))
		for key, value := range record.Tags {
			cloned.Tags[key] = value
		}
	}
	return cloned
}
