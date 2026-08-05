package core

import (
	"context"
	"time"
)

// Mode defines the reasoning approach for LLM operations.
// Different modes optimize for different use cases and accuracy requirements.
type Mode int

const (
	// Strict enforces exact schema matching and validation.
	// Use for structured data extraction where accuracy is critical.
	Strict Mode = iota

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
	// Smart uses the highest quality model (GPT-4 class).
	// ~2-5s latency, best for complex reasoning and critical decisions.
	Smart Speed = iota

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
	// Examples: "Focus on financial data", "Use formal tone", "Sort by importance"
	Steering string

	// Threshold sets the minimum confidence level (0.0-1.0).
	// Operations below this confidence may return errors.
	Threshold float64

	// Mode determines the reasoning approach (Strict/Transform/Creative).
	// Default: Transform
	Mode Mode

	// Intelligence sets the quality/speed tradeoff (Smart/Fast/Quick).
	// Default: Fast
	Intelligence Speed

	// Context for cancellation
	Context context.Context

	// RequestID for tracing
	RequestID string
}

// Result wraps an operation result with metadata.
// Used for operations that need to return confidence scores.
type Result[T any] struct {
	Value T // The actual result value
	// ModelConfidence is the model's own claim about this result, not a measurement.
	// It is not calibrated and is not comparable across models or prompts.
	ModelConfidence float64        // ModelConfidence score (0.0-1.0)
	Error           error          // Any error that occurred
	Metadata        map[string]any // Additional metadata (tokens used, model, etc.)
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
	RequestID    string `json:"request_id"`
	TraceID      string `json:"trace_id"`
	SpanID       string `json:"span_id"`
	ParentSpanID string `json:"parent_span_id,omitempty"`

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
