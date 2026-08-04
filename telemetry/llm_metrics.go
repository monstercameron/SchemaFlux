package telemetry

import "github.com/monstercameron/schemaflux/internal/types"

// RecordLLMMetrics records low-cardinality metrics for a completed LLM request.
func RecordLLMMetrics(metadata *types.ResultMetadata) {
	if metadata == nil {
		return
	}

	tags := map[string]string{
		"provider":     metadata.Provider,
		"model":        metadata.Model,
		"mode":         metadata.Mode.String(),
		"intelligence": metadata.Intelligence.String(),
	}

	RecordMetric("llm_requests", 1, tags)
	RecordMetric("llm_request_duration_ms", metadata.Duration.Milliseconds(), tags)

	if metadata.TokenUsage != nil {
		usage := metadata.TokenUsage
		RecordMetric("llm_tokens_prompt", int64(usage.PromptTokens), tags)
		RecordMetric("llm_tokens_completion", int64(usage.CompletionTokens), tags)
		RecordMetric("llm_tokens_total", int64(usage.TotalTokens), tags)

		if usage.InputTokens > 0 {
			RecordMetric("llm_tokens_input", int64(usage.InputTokens), tags)
		}
		if usage.OutputTokens > 0 {
			RecordMetric("llm_tokens_output", int64(usage.OutputTokens), tags)
		}
		if usage.CachedTokens > 0 {
			RecordMetric("llm_tokens_cached", int64(usage.CachedTokens), tags)
		}
		if usage.ReasoningTokens > 0 {
			RecordMetric("llm_tokens_reasoning", int64(usage.ReasoningTokens), tags)
		}
	}

	if metadata.CostInfo != nil {
		cost := metadata.CostInfo
		RecordMetricValue("llm_cost_total_usd", cost.TotalCost, tags)
		RecordMetricValue("llm_cost_prompt_usd", cost.PromptCost, tags)
		RecordMetricValue("llm_cost_completion_usd", cost.CompletionCost, tags)

		if cost.CachedCost > 0 {
			RecordMetricValue("llm_cost_cached_usd", cost.CachedCost, tags)
		}
		if cost.ReasoningCost > 0 {
			RecordMetricValue("llm_cost_reasoning_usd", cost.ReasoningCost, tags)
		}
	}
}
