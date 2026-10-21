package telemetry

import (
	"strings"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// OutcomeOK is the outcome tag of a request that was answered and whose answer
// was accepted. A request that failed carries the library's error kind there
// instead.
const OutcomeOK = "ok"

// maxTagValueLength bounds a tag value that comes from outside the program.
const maxTagValueLength = 64

// RecordLLMMetrics records the metrics of a request that completed: one count
// on llm_requests with the outcome "ok", its duration, the token counts the
// provider reported, and the cost when the call was priced. Series for cached
// and reasoning tokens and costs appear only when their value is positive.
//
// Every series is tagged with the provider, the model, the mode and the tier;
// llm_requests also carries the outcome. The provider and model are those that
// answered, which a provider names in its response, so both are cut and made
// printable before they become tags. Each count is recorded under one name:
// the prompt and completion counts are not repeated as input and output.
//
// A request that failed is recorded by RecordLLMFailure, so llm_requests counts
// every request and its outcome tag splits them.
//
// With metrics off, which is the default, it does nothing: it is asked once for
// every provider call, and used to build its tag map and ask eight times, each
// time reading the environment, whether there was anything to record.
func RecordLLMMetrics(metadata *types.ResultMetadata) {
	if metadata == nil || !MetricsEnabled() {
		return
	}

	tags := map[string]string{
		"provider":     BoundedTagValue(metadata.Provider),
		"model":        BoundedTagValue(metadata.Model),
		"mode":         metadata.Mode.String(),
		"intelligence": metadata.Intelligence.String(),
	}

	RecordMetric("llm_requests", 1, withTag(tags, "outcome", OutcomeOK))
	RecordMetric("llm_request_duration_ms", metadata.Duration.Milliseconds(), tags)

	if metadata.TokenUsage != nil {
		usage := metadata.TokenUsage
		RecordMetric("llm_tokens_prompt", int64(usage.PromptTokens), tags)
		RecordMetric("llm_tokens_completion", int64(usage.CompletionTokens), tags)
		RecordMetric("llm_tokens_total", int64(usage.TotalTokens), tags)

		if usage.CachedTokens > 0 {
			RecordMetric("llm_tokens_cached", int64(usage.CachedTokens), tags)
		}
		if usage.ReasoningTokens > 0 {
			RecordMetric("llm_tokens_reasoning", int64(usage.ReasoningTokens), tags)
		}
	}

	// An unpriced call carries a CostInfo whose figures are zero because nothing
	// is known about the price. Recording them would pull the cost series'
	// average below that of any real request, so they are not recorded, the
	// same rule RecordBatchMetrics follows.
	if metadata.CostInfo != nil && metadata.CostInfo.Priced {
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

// RecordLLMFailure counts a request that ended without an accepted answer, on
// llm_requests with the outcome set to errorKind: one of the library's error
// kinds, never an error's message. The provider and model are those the
// request was sent to, since nothing answered. An empty kind, or one that
// would read as a success, is counted as "unknown".
//
// Nothing else is recorded for a failure: its duration, tokens and cost were
// not measured. With metrics off it does nothing.
func RecordLLMFailure(provider, model string, mode types.Mode, intelligence types.Speed, errorKind string) {
	if !MetricsEnabled() {
		return
	}
	if errorKind == "" || errorKind == OutcomeOK {
		errorKind = types.KindUnknown.String()
	}
	RecordMetric("llm_requests", 1, map[string]string{
		"provider":     BoundedTagValue(provider),
		"model":        BoundedTagValue(model),
		"mode":         mode.String(),
		"intelligence": intelligence.String(),
		"outcome":      BoundedTagValue(errorKind),
	})
}

// BoundedTagValue returns a tag value fit to be one: at most 64 bytes, each a
// printable ASCII character, with any other byte replaced by "?". A name that
// is already both is returned as it is. A tag whose value does not come from
// the library's own vocabulary goes through it: a model or provider name from a
// response, and the string form of a Go type, which for an anonymous struct
// prints every field tag.
func BoundedTagValue(value string) string {
	clean := len(value) <= maxTagValueLength
	for index := 0; clean && index < len(value); index++ {
		clean = value[index] >= 0x20 && value[index] <= 0x7e
	}
	if clean {
		return value
	}

	if len(value) > maxTagValueLength {
		value = value[:maxTagValueLength]
	}
	var bounded strings.Builder
	bounded.Grow(len(value))
	for index := 0; index < len(value); index++ {
		if value[index] >= 0x20 && value[index] <= 0x7e {
			bounded.WriteByte(value[index])
		} else {
			bounded.WriteByte('?')
		}
	}
	return bounded.String()
}
