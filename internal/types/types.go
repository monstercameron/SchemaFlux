package types

import (
	"context"
	"time"
)

// Mode defines the reasoning approach for LLM operations.
// Different modes optimize for different use cases and accuracy requirements.
type Mode int

const (
	// ModeUnset means the caller did not choose a mode, and the operation's own
	// default applies.
	//
	// It occupies zero deliberately. Strict used to, which made every merge
	// guard of the form `if user.Mode != 0` treat "the caller asked for Strict"
	// and "the caller said nothing" as the same thing -- so .Strict() was
	// unrepresentable on roughly ten operations. Closes half of F-01.
	ModeUnset Mode = iota

	// Strict enforces exact schema matching and validation.
	// Use for structured data extraction where accuracy is critical.
	Strict

	// TransformMode enables semantic mapping between related concepts.
	// Default mode for most operations, balances flexibility and accuracy.
	TransformMode

	// Creative allows open-ended generation and interpretation.
	// Use for content generation and creative tasks.
	Creative
)

// String returns the string representation of a Mode
func (m Mode) String() string {
	switch m {
	case ModeUnset:
		return "unset"
	case Strict:
		return "strict"
	case TransformMode:
		return "transform"
	case Creative:
		return "creative"
	default:
		return "unknown"
	}
}

// Speed defines the quality vs latency tradeoff for operations.
// Higher quality models have higher latency but better results.
type Speed int

const (
	// TierUnset means the caller did not choose a tier, and the operation's own
	// default applies. Zero for the same reason as ModeUnset: Smart used to sit
	// here, so `.Smart()` was indistinguishable from silence. The other half of
	// F-01.
	TierUnset Speed = iota

	// Smart uses the highest quality model (GPT-4 class).
	// ~2-5s latency, best for complex reasoning and critical decisions.
	Smart

	// Fast uses balanced performance models (GPT-3.5 Turbo).
	// ~1-2s latency, default for most operations.
	Fast

	// Quick uses the fastest available model.
	// <1s latency, for real-time and high-volume operations.
	Quick
)

// String returns the string representation of a Speed
func (s Speed) String() string {
	switch s {
	case TierUnset:
		return "unset"
	case Smart:
		return "smart"
	case Fast:
		return "fast"
	case Quick:
		return "quick"
	default:
		return "unknown"
	}
}

// OpOptions configures individual LLM operations.
// All fields are optional with sensible defaults.
type OpOptions struct {
	// Steering provides natural language guidance for the operation.
	Steering string

	// Threshold sets the minimum confidence level (0.0-1.0).
	Threshold float64

	// Mode determines the reasoning approach (Strict/Transform/Creative).
	Mode Mode

	// Intelligence sets the quality/speed tradeoff (Smart/Fast/Quick).
	Intelligence Speed

	// Context for cancellation
	Context context.Context

	// RequestID for tracing
	RequestID string

	// CorrelationID groups related requests across call chains.
	CorrelationID string

	// JSONSchema, when set, is enforced by providers that support structured
	// outputs. An operation that knows its target type fills this in; the
	// provider falls back to free-form JSON when the type cannot be expressed
	// as a strict schema, and records which path it used.
	JSONSchema map[string]any

	// SchemaName names the schema in the provider request.
	SchemaName string

	// SchemaID is the schema's full identity -- name, version, hash, dialect --
	// rendered for a log line, a cache key, or a stored result's provenance.
	//
	// It is separate from SchemaName because the provider wants a short label
	// and the caller wants to know which contract produced an answer. The Go
	// type's name answers neither: it changes when a field is renamed and does
	// not change when a field's type changes. S-002.
	SchemaID string

	// ResponseFormat declares whether the operation needs structured output:
	// "json", "text", or "" to infer it.
	//
	// It exists because the format used to be decided by searching the
	// concatenated system AND user prompts for phrases like "json object". A
	// caller whose input happened to contain that phrase flipped a text
	// operation into JSON mode, which makes the response format depend on the
	// data — an injection-adjacent control path in a typed library. An
	// operation knows statically what it needs; this is where it says so.
	ResponseFormat string

	// CacheIdentity is the operation-and-schema half of a provider prompt-cache
	// key, built with ops.SchemaCacheKey(operationName, operationVersion,
	// descriptor). It is separate from SchemaID: SchemaID is provenance for a
	// stored result, this is an input to a routing decision, and the two
	// happen to share a source but not a purpose.
	//
	// Only operations that know their schema identity set it (Extract does,
	// today). It is empty for the rest, and CallLLM still derives a cache key
	// for those from the resolved model and the rendered prompt template, so
	// an unset CacheIdentity degrades the key's precision rather than losing
	// caching entirely. P-009.
	CacheIdentity string
}

// Case represents a pattern matching case for the Match function.
// Used for conditional execution based on fuzzy matching.
type Case struct {
	Condition any    // String pattern, type, or value to match
	Action    func() // Function to execute when matched
}

// Extended types for internal use (not in CORE.md spec but needed for implementation)

// TokenUsage tracks token consumption for cost calculation
type TokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`

	// Detailed breakdown for advanced models
	InputTokens     int `json:"input_tokens,omitempty"`
	OutputTokens    int `json:"output_tokens,omitempty"`
	CachedTokens    int `json:"cached_tokens,omitempty"`
	ReasoningTokens int `json:"reasoning_tokens,omitempty"` // For o1-style models

	// CacheWriteTokens is the input the provider wrote into its prompt cache on
	// this call. It is billed differently from a cache read and from an
	// uncached token, so cost accounting that ignores it under-reports the
	// first call of a cached prefix. The live Responses API reports it as
	// usage.input_tokens_details.cache_write_tokens.
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}

// CostInfo tracks financial costs of operations
type CostInfo struct {
	// Total cost in USD
	TotalCost float64 `json:"total_cost"`

	// Cost breakdown
	PromptCost     float64 `json:"prompt_cost"`
	CompletionCost float64 `json:"completion_cost"`
	CachedCost     float64 `json:"cached_cost,omitempty"`
	ReasoningCost  float64 `json:"reasoning_cost,omitempty"`

	// Pricing model information
	Currency                string  `json:"currency"`
	PricePerPromptToken     float64 `json:"price_per_prompt_token"`
	PricePerCompletionToken float64 `json:"price_per_completion_token"`

	// Priced reports whether a price for this exact model was found. When it is
	// false the cost fields are zero because nothing is known, not because the
	// call was free -- the two are indistinguishable without this flag, and
	// treating an unpriced call as $0.00 understates spend silently.
	Priced bool `json:"priced"`

	// PricingSource names the pricing entry the figures came from. It equals the
	// model when priced exactly. A cost is never computed from a different
	// model's rates: substituting one produces a confident, precisely wrong
	// number, which is worse than reporting nothing.
	PricingSource string `json:"pricing_source,omitempty"`

	// Cost tracking metadata
	BillingPeriod  string `json:"billing_period,omitempty"`
	OrganizationID string `json:"organization_id,omitempty"`
	ProjectID      string `json:"project_id,omitempty"`
}

// DebugInfo provides detailed debugging information for an operation
type DebugInfo struct {
	RequestID   string        `json:"request_id"`
	Operation   string        `json:"operation"`
	StartTime   time.Time     `json:"start_time"`
	EndTime     time.Time     `json:"end_time"`
	Duration    time.Duration `json:"duration"`
	Input       any           `json:"input,omitempty"`
	Output      any           `json:"output,omitempty"`
	Error       error         `json:"error,omitempty"`
	LLMCalls    []LLMCallInfo `json:"llm_calls,omitempty"`
	MemoryUsage MemoryStats   `json:"memory_usage"`
	StackTrace  []string      `json:"stack_trace,omitempty"`
}

// LLMCallInfo contains information about a single LLM call
type LLMCallInfo struct {
	Model       string        `json:"model"`
	Prompt      string        `json:"prompt"`
	Response    string        `json:"response"`
	TokensUsed  int           `json:"tokens_used"`
	Duration    time.Duration `json:"duration"`
	Retries     int           `json:"retries"`
	Temperature float32       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens"`
}

// MemoryStats contains memory usage statistics
type MemoryStats struct {
	Allocated      uint64 `json:"allocated"`
	TotalAllocated uint64 `json:"total_allocated"`
	System         uint64 `json:"system"`
	NumGC          uint32 `json:"num_gc"`
}

// ResultMetadata contains detailed information about an operation's execution
type ResultMetadata struct {
	// Request identification and tracing
	RequestID     string `json:"request_id"`
	CorrelationID string `json:"correlation_id,omitempty"`
	TraceID       string `json:"trace_id"`
	SpanID        string `json:"span_id"`
	ParentSpanID  string `json:"parent_span_id,omitempty"`

	// Timing information
	StartTime time.Time     `json:"start_time"`
	EndTime   time.Time     `json:"end_time"`
	Duration  time.Duration `json:"duration"`

	// Model and operation details
	Model        string `json:"model"`
	Provider     string `json:"provider"`
	Operation    string `json:"operation"`
	Mode         Mode   `json:"mode"`
	Intelligence Speed  `json:"intelligence"`

	// Token usage tracking
	TokenUsage *TokenUsage `json:"token_usage,omitempty"`

	// Cost information
	CostInfo *CostInfo `json:"cost_info,omitempty"`

	// Performance metrics
	RetryCount int           `json:"retry_count"`
	CacheHit   bool          `json:"cache_hit"`
	LatencyP50 time.Duration `json:"latency_p50,omitempty"`
	LatencyP95 time.Duration `json:"latency_p95,omitempty"`

	// Error information
	ErrorType string `json:"error_type,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`

	// Custom metadata
	Custom map[string]any `json:"custom,omitempty"`

	// Debug info
	DebugInfo *DebugInfo `json:"debug_info,omitempty"`
}
