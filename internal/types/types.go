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

	// ExactFields and CompleteFields are Strict's two rules, separated so a
	// caller can ask for either without the other (DX-007).
	//
	// Strict has always meant both at once: reject a property the schema does
	// not name, AND refuse an answer with any required field left empty. They
	// are unrelated checks bundled under one word, and the bundle is a trap in
	// both directions. A caller reaching for Strict wanting exact decoding gets
	// mandatory non-empty fields as an unannounced second effect -- reported
	// from ArticleFlux, which had Strict on four operations whose contracts
	// tolerate an extra field, so a model that answered the question and also
	// volunteered a confidence score failed the whole call. On the batched ones
	// that discarded sixty good answers over one key nobody would have read.
	// Going the other way, a caller who drops Strict to tolerate that extra
	// field silently loses the required-field check too.
	//
	// These are additive: Strict still implies both, and setting either alone
	// turns on that rule without the other. There is deliberately no way to
	// subtract a rule from Strict, because both halves are reachable directly
	// and "Strict except not really" is a worse thing to read than either.
	ExactFields    bool
	CompleteFields bool

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

	// Model pins the exact model for this call, bypassing the Intelligence
	// tier's mapping. TC-005.
	//
	// The tier is deliberately not a pin -- Speed.String() documents
	// Smart/Fast/Quick as floating, and what "Smart" resolves to changes as
	// models are released. That is the right default and the wrong thing for a
	// caller reproducing a result, comparing two models, or holding a
	// regression suite still. This is the escape hatch, and it is a separate
	// field rather than an overload of Intelligence precisely so that "I chose
	// a tier" and "I chose a model" stay distinguishable in the envelope.
	//
	// Empty means no pin: the tier mapping decides, as before.
	Model string

	// WebSearch declares the provider's built-in web-search tool on the
	// call, giving the model live web access it otherwise has none of. The
	// model still decides per-request whether to actually search. False
	// leaves the wire request exactly as it was before this field existed;
	// providers without a built-in search tool ignore it. See
	// llm.CompletionRequest.WebSearch.
	WebSearch bool

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

	// MaxOutputTokens overrides the output ceiling CallLLM sends as the
	// provider's max-tokens field. Zero (the default) leaves the ceiling at
	// config.GetMaxTokens(Intelligence) -- 4000/2000/1000 by tier.
	//
	// The tier ceiling used to be the only ceiling there was, which conflates
	// "how smart" with "how long": a caller who wants a long answer from a fast
	// model, or a short one from a smart model to control cost, had no way to
	// ask. I-09. This does not change what a caller who sets nothing gets --
	// the tier default still applies -- it only gives the caller who has an
	// opinion a place to say so.
	MaxOutputTokens int

	// ParentResultIDs names the results this call's answer should be traced
	// back to, in a pipeline where one stage's output feeds the next stage's
	// input -- ops.buildProvenance (internal/ops/provenance.go) copies this
	// into the produced Result's Meta.Provenance.ParentResultIDs. TC-003.
	//
	// Nothing sets this automatically: only the caller building a pipeline
	// knows which results are actually related, so a caller wanting a
	// traceable chain sets it to the previous stage's
	// Result.Meta.Provenance.ResultID before making the next call. Left
	// empty, a result simply has no recorded parent, which is the correct
	// answer for every call that is not part of a pipeline.
	ParentResultIDs []string
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

	// Quality says how much the figures above are worth (OB-003).
	//
	// Priced is a boolean and therefore cannot distinguish the two things that
	// both make a cost zero: a model nobody has a rate card for, and a model
	// that genuinely costs nothing. Reading the first as the second understates
	// spend; reading the second as the first hides a working configuration
	// behind a warning. Quality separates them, and separates both from a
	// figure that was estimated rather than computed from reported usage.
	Quality PricingQuality `json:"pricing_quality,omitempty"`

	// Cost tracking metadata
	BillingPeriod  string `json:"billing_period,omitempty"`
	OrganizationID string `json:"organization_id,omitempty"`
	ProjectID      string `json:"project_id,omitempty"`
}

// PricingQuality says what a cost figure is worth. It exists because a
// boolean cannot: zero is produced by "no rate card", by "genuinely free", and
// by "nothing was spent", and those need different reactions.
type PricingQuality string

const (
	// PricingUnknown means no rate card was found for this model. The cost
	// fields are zero because nothing is known, not because nothing was spent,
	// and summing these with real costs understates the bill by exactly the
	// amount nobody can see.
	PricingUnknown PricingQuality = "unknown"

	// PricingExact means the figures were computed from a rate card for this
	// exact model, using usage the provider reported.
	PricingExact PricingQuality = "exact"

	// PricingEstimated means the figures came from a rate card but the usage
	// was estimated rather than reported -- a plan made before the call, or a
	// provider that answered without usage. Marked rather than presented as
	// exact, because a number that looks measured and is not is the whole
	// failure this library is being rebuilt against.
	PricingEstimated PricingQuality = "estimated"

	// PricingFree means the model has a rate card and that rate card is zero:
	// a local or mock provider, or a genuinely free tier. Distinct from
	// unknown, because this one is a fact.
	PricingFree PricingQuality = "free"
)

// Known reports whether the figures mean anything at all -- true for exact,
// estimated, and free, false for unknown.
func (q PricingQuality) Known() bool {
	switch q {
	case PricingExact, PricingEstimated, PricingFree:
		return true
	}
	return false
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
	Model string `json:"model"`

	// RequestedModel is the model the request asked for -- the caller's pin,
	// or whatever the tier mapping resolved to. TC-005 needs it because a
	// provider that substitutes is visible only as a difference between what
	// was asked for and what answered, and a single "model" field reports the
	// second while every reader assumes it is the first.
	RequestedModel string `json:"requested_model,omitempty"`

	// ObservedModel is the model string the provider itself returned, raw and
	// **not** defaulted. Model, above, falls back to the requested model when a
	// provider echoes nothing, which is right for pricing and logging and wrong
	// for drift: it would make an unobserved substitution indistinguishable
	// from an observed agreement. Empty here means the provider said nothing,
	// and the envelope reports drift as unknown rather than absent.
	ObservedModel string `json:"observed_model,omitempty"`

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
