// Package schemaflux provides the main API for SchemaFlux operations.
// This is the single entry point for all SchemaFlux functionality.
//
// Example usage:
//
//	import "github.com/monstercameron/schemaflux"
//
//	// Initialize from environment
//	if err := schemaflux.InitWithEnv(); err != nil {
//	    panic(err)
//	}
//
//	// Extract structured data from unstructured input
//	person, err := schemaflux.Extract[Person](jsonInput, schemaflux.NewExtractOptions())
package schemaflux

import (
	"context"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/requesttracking"
	telemetry "github.com/monstercameron/schemaflux/internal/telemetry"
	"github.com/monstercameron/schemaflux/internal/types"
)

// Re-export types for public API
type (
	// OpOptions configures individual LLM operations.
	OpOptions = types.OpOptions

	// Mode defines the reasoning approach for LLM operations.
	Mode = types.Mode

	// Speed defines the quality vs latency tradeoff for operations.
	Speed = types.Speed

	// LoggerConfig configures the global structured logger.
	LoggerConfig = telemetry.LoggerConfig

	// LogEntry is a captured structured log record.
	LogEntry = telemetry.LogEntry

	// LogLevel controls logger verbosity.
	LogLevel = telemetry.LogLevel

	// Provider is the shared abstraction for LLM backends.
	Provider = llm.Provider

	// ProviderConfig configures provider construction.
	ProviderConfig = llm.ProviderConfig

	// ProviderFactory creates a provider from configuration.
	ProviderFactory = llm.ProviderFactory

	// APIError reports a provider request that failed with an HTTP status.
	// Recover it with errors.As to branch on StatusCode rather than
	// substring-matching an error message. Error() withholds the raw response
	// body -- a provider's error can quote your input back, and that does not
	// belong in your logs by default. Detail() includes it.
	APIError = llm.APIError

	// RateLimitError is an APIError that also carries the wait the server
	// asked for. errors.As recovers it as either type.
	RateLimitError = llm.RateLimitError

	// CompletionRequest is the low-level provider request shape.
	CompletionRequest = llm.CompletionRequest

	// CompletionResponse is the low-level provider response shape.
	CompletionResponse = llm.CompletionResponse

	// TokenUsage is the token accounting a provider reports. It was not
	// exported, so a caller implementing Provider — or a test asserting on
	// cost — could not name the type they were required to return.
	TokenUsage = types.TokenUsage

	// CostInfo is the computed cost of a request.
	CostInfo = types.CostInfo

	// ResultMetadata carries the per-request accounting an operation produced.
	ResultMetadata = types.ResultMetadata

	// RequestTrackingConfig configures request/correlation tracking.
	RequestTrackingConfig = requesttracking.Config

	// RequestTrackingMetadata contains resolved request and correlation IDs.
	RequestTrackingMetadata = requesttracking.Metadata

	// RequestIDStrategy controls how request IDs are generated.
	RequestIDStrategy = requesttracking.IDStrategy

	// CorrelationStrategy controls how correlation IDs are resolved.
	CorrelationStrategy = requesttracking.CorrelationStrategy
)

// The old Result[T] lived here: a struct holding a value, a model-reported
// confidence, an error, and a map of "additional metadata". It had no users in
// this module, and it is the shape the envelope replaces -- an error inside a
// result means every caller has two places to check, and a bag called metadata
// is where a model's claim and a measured token count end up indistinguishable.
// See the Result alias below.

// Re-export operation-specific options types
type (
	ExtractOptions     = ops.ExtractOptions
	TransformOptions   = ops.TransformOptions
	GenerateOptions    = ops.GenerateOptions
	ChooseOptions      = ops.ChooseOptions
	FilterOptions      = ops.FilterOptions
	SortOptions        = ops.SortOptions
	ClassifyOptions    = ops.ClassifyOptions
	ScoreOptions       = ops.ScoreOptions
	CompareOptions     = ops.CompareOptions
	SimilarOptions     = ops.SimilarOptions
	InferOptions       = ops.InferOptions
	DiffOptions        = ops.DiffOptions
	DiffResult         = ops.DiffResult
	ExplainOptions     = ops.ExplainOptions
	ExplainResult      = ops.ExplainResult
	ParseOptions       = ops.ParseOptions
	ParseResult[T any] = ops.ParseResult[T]
	SummarizeOptions   = ops.SummarizeOptions
	RewriteOptions     = ops.RewriteOptions
	TranslateOptions   = ops.TranslateOptions
	ExpandOptions      = ops.ExpandOptions
	SuggestOptions     = ops.SuggestOptions
	SuggestStrategy    = ops.SuggestStrategy
	RedactOptions      = ops.RedactOptions
	RedactStrategy     = ops.RedactStrategy
	JumbleMode         = ops.JumbleMode
	// Extended operations types
	ValidationResult      = ops.ValidationResult
	ValidateOptions       = ops.ValidateOptions
	ValidateResult[T any] = ops.ValidateResult[T]
	ValidationIssue       = ops.ValidationIssue
	QuestionOptions       = ops.QuestionOptions
	QuestionResult[A any] = ops.QuestionResult[A]
	// Procedural operations types
	Decision[T any] = ops.Decision[T]
	DecisionResult  = ops.DecisionResult
	DecideOptions   = ops.DecideOptions
	GuardResult     = ops.GuardResult

	// Control-flow types
	Case = types.Case

	// Map-reduce types
	MapReduceOptions = ops.MapReduceOptions
	MapReduceResult  = ops.MapReduceResult
	ChunkError       = ops.ChunkError

	// New LLM operation types (v2)
	AnnotateOptions           = ops.AnnotateOptions
	Annotation                = ops.Annotation
	AnnotateResult            = ops.AnnotateResult
	ClusterOptions            = ops.ClusterOptions
	ClusterInfo[T any]        = ops.ClusterInfo[T]
	ClusterResult[T any]      = ops.ClusterResult[T]
	RankOptions               = ops.RankOptions
	RankedItem[T any]         = ops.RankedItem[T]
	RankResult[T any]         = ops.RankResult[T]
	CompressOptions           = ops.CompressOptions
	CompressResult[T any]     = ops.CompressResult[T]
	DecomposeOptions          = ops.DecomposeOptions
	DecomposedPart[T any]     = ops.DecomposedPart[T]
	DecomposeResult[T any]    = ops.DecomposeResult[T]
	EnrichOptions             = ops.EnrichOptions
	EnrichResult[T any]       = ops.EnrichResult[T]
	NormalizeOptions          = ops.NormalizeOptions
	NormalizeChange           = ops.NormalizeChange
	NormalizeResult[T any]    = ops.NormalizeResult[T]
	MatchOptions              = ops.MatchOptions
	MatchPair[S any, T any]   = ops.MatchPair[S, T]
	MatchResult[S any, T any] = ops.MatchResult[S, T]
	CritiqueOptions           = ops.CritiqueOptions
	CritiqueIssue             = ops.CritiqueIssue
	CritiquePositive          = ops.CritiquePositive
	CritiqueResult            = ops.CritiqueResult
	SynthesizeOptions         = ops.SynthesizeOptions
	SynthesisFact             = ops.SynthesisFact
	SynthesisInsight          = ops.SynthesisInsight
	SynthesisConflict         = ops.SynthesisConflict
	SynthesizeResult[T any]   = ops.SynthesizeResult[T]
	PredictOptions            = ops.PredictOptions
	PredictionInterval        = ops.PredictionInterval
	PredictionScenario        = ops.PredictionScenario
	PredictionFactor          = ops.PredictionFactor
	PredictResult[T any]      = ops.PredictResult[T]
	VerifyOptions             = ops.VerifyOptions
	ClaimVerification         = ops.ClaimVerification
	LogicIssue                = ops.LogicIssue
	ConsistencyIssue          = ops.ConsistencyIssue
	VerifyResult              = ops.VerifyResult

	// Analysis operation result types (refactored for Go-native generics)
	ClassifyResult[C ~string]      = ops.ClassifyResult[C]
	ClassifyAlternative[C ~string] = ops.ClassifyAlternative[C]
	ScoreResult                    = ops.ScoreResult
	CompareResult[T any]           = ops.CompareResult[T]
	ComparisonPoint                = ops.ComparisonPoint
	SimilarResult                  = ops.SimilarResult
	AspectMatch                    = ops.AspectMatch

	// Data-centric LLM operations (v3)
	NegotiateOptions       = ops.NegotiateOptions
	Tradeoff               = ops.Tradeoff
	NegotiateResult[T any] = ops.NegotiateResult[T]

	ResolveOptions       = ops.ResolveOptions
	Conflict             = ops.Conflict
	ResolveResult[T any] = ops.ResolveResult[T]

	DeriveOptions       = ops.DeriveOptions
	Derivation          = ops.Derivation
	DeriveResult[U any] = ops.DeriveResult[U]

	ConformOptions       = ops.ConformOptions
	Adjustment           = ops.Adjustment
	ConformResult[T any] = ops.ConformResult[T]

	InterpolateOptions       = ops.InterpolateOptions
	FilledItem               = ops.FilledItem
	InterpolateResult[T any] = ops.InterpolateResult[T]

	ArbitrateOptions       = ops.ArbitrateOptions
	RuleEvaluation         = ops.RuleEvaluation
	OptionEvaluation       = ops.OptionEvaluation
	ArbitrateResult[T any] = ops.ArbitrateResult[T]

	ProjectOptions       = ops.ProjectOptions
	FieldMapping         = ops.FieldMapping
	ProjectResult[U any] = ops.ProjectResult[U]

	AuditOptions       = ops.AuditOptions
	AuditFinding       = ops.AuditFinding
	AuditSummary       = ops.AuditSummary
	AuditResult[T any] = ops.AuditResult[T]

	ComposeOptions       = ops.ComposeOptions
	ComposedField        = ops.ComposedField
	ComposeResult[T any] = ops.ComposeResult[T]

	PivotOptions       = ops.PivotOptions
	PivotMapping       = ops.PivotMapping
	PivotStats         = ops.PivotStats
	PivotResult[U any] = ops.PivotResult[U]

	// Text operation result types with metadata
	Summary                = ops.Summary
	Rewritten              = ops.Rewritten
	Translation            = ops.Translation
	TranslationAlternative = ops.TranslationAlternative
	Expansion              = ops.Expansion

	// Extended operation result types with metadata
	FormatResult       = ops.FormatResult
	MergeResult[T any] = ops.MergeResult[T]
	MergeConflict      = ops.MergeConflict
)

// Result pairs a value with the record of how it was produced: what it cost,
// how many attempts it took, which contract was delivered, and which checks the
// library ran.
//
// The plain form and the detailed form are the same execution -- the envelope
// is built either way and `Extract` drops it -- because two return types that
// execute differently is how the two drift.
//
//	result, err := schemaflux.ExtractResult[Invoice](text, schemaflux.NewExtractOptions())
//	if result.Meta.Degraded() {
//	    // asked for more than was delivered; result.Meta.Checks says what ran
//	}
type (
	Result[T any] = types.Result[T]
	Meta          = types.Meta
	Check         = types.Check
	ContractLevel = types.ContractLevel
)

const (
	ContractPromptOnly                = types.ContractPromptOnly
	ContractJSONWellFormed            = types.ContractJSONWellFormed
	ContractSchemaConstrained         = types.ContractSchemaConstrained
	ContractSchemaAndInvariantChecked = types.ContractSchemaAndInvariantChecked
	ContractEvidenceChecked           = types.ContractEvidenceChecked
	ContractFullyGoverned             = types.ContractFullyGoverned
)

// ExtractResult runs Extract and returns the envelope alongside the value.
func ExtractResult[T any](input any, opts ExtractOptions) (Result[T], error) {
	return ops.ExtractResult[T](input, opts)
}

// SortResult runs Sort and returns the envelope alongside the ordering, which
// is the only way to see which strategy ran: above the scoring threshold Sort
// scores items in parallel rather than ordering the whole list in one call,
// and a caller comparing two runs needs to know which of those produced the
// answer. Meta.Strategy is "trivial", "whole-list", "scoring", or
// "scoring-fallback".
func SortResult[T any](items []T, opts SortOptions) (Result[[]T], error) {
	return ops.SortResult(items, opts)
}

// The failure taxonomy.
//
// Branch on these instead of matching substrings against an error message: the
// message changes when someone improves the wording, and it changes per
// provider. Every failure this library produces is classified, and errors.Is
// reaches the sentinel through any amount of wrapping.
//
//	if errors.Is(err, schemaflux.ErrRateLimited) {
//	    // back off; the provider said how long, and the library already waited
//	}
//	if errors.Is(err, schemaflux.ErrSchemaViolation) {
//	    // the answer did not fit the type; retrying sends the same question
//	}
//
// For the disposition rather than the identity, reach the error itself:
//
//	var opErr *schemaflux.OperationError
//	if errors.As(err, &opErr) && opErr.Terminal() {
//	    // nothing this library can do will help
//	}
type (
	// OperationError is the classified failure, carrying the context an
	// automated recovery needs and none of the caller's payload.
	OperationError = types.OperationError

	// ErrorKind is what kind of failure it was.
	ErrorKind = types.ErrorKind
)

var (
	ErrConfiguration          = types.ErrConfiguration
	ErrAuthentication         = types.ErrAuthentication
	ErrPermission             = types.ErrPermission
	ErrInvalidRequest         = types.ErrInvalidRequest
	ErrPolicyViolation        = types.ErrPolicyViolation
	ErrBudgetExceededKind     = types.ErrBudgetExceeded
	ErrAdmissionRejected      = types.ErrAdmissionRejected
	ErrRateLimited            = types.ErrRateLimited
	ErrProviderUnavailable    = types.ErrProviderUnavailable
	ErrTimeout                = types.ErrTimeout
	ErrCanceled               = types.ErrCanceled
	ErrCircuitOpen            = types.ErrCircuitOpen
	ErrShutdown               = types.ErrShutdown
	ErrContextTooLarge        = types.ErrContextTooLarge
	ErrOutputTruncated        = types.ErrOutputTruncated
	ErrMalformedOutput        = types.ErrMalformedOutput
	ErrSchemaViolation        = types.ErrSchemaViolation
	ErrBatchProtocolViolation = types.ErrBatchProtocolViolation
	ErrInvariantViolation     = types.ErrInvariantViolation
	ErrEvidenceViolation      = types.ErrEvidenceViolation
	ErrUnsupportedCapability  = types.ErrUnsupportedCapability
	ErrRepairExhausted        = types.ErrRepairExhausted
	ErrReviewRequired         = types.ErrReviewRequired
)

// KindOf reports how a failure was classified, or ErrorKind zero when nothing
// classified it -- which is a gap to close rather than a category to handle.
func KindOf(err error) ErrorKind { return types.KindOf(err) }

// Mode constants
const (
	// ModeUnset means no mode was chosen and the operation's default applies.
	// It is the zero value, so a Mode field a caller never touched says so.
	ModeUnset = types.ModeUnset

	// Strict enforces exact schema matching and validation.
	Strict = types.Strict

	// TransformMode enables semantic mapping between related concepts.
	TransformMode = types.TransformMode

	// Creative allows open-ended generation and interpretation.
	Creative = types.Creative
)

// Speed constants (Intelligence levels)
const (
	// TierUnset means no tier was chosen and the operation's default applies.
	TierUnset = types.TierUnset

	// Smart uses the highest quality model (GPT-4 class).
	Smart = types.Smart

	// Fast uses balanced performance models (GPT-3.5 Turbo).
	Fast = types.Fast

	// Quick uses the fastest available model.
	Quick = types.Quick
)

// Log level constants.
const (
	LogDebug = telemetry.DebugLevel
	LogInfo  = telemetry.InfoLevel
	LogWarn  = telemetry.WarnLevel
	LogError = telemetry.ErrorLevel
	LogFatal = telemetry.FatalLevel
)

// Request tracking constants.
const (
	RequestIDAuto      = requesttracking.IDStrategyAuto
	RequestIDUUID      = requesttracking.IDStrategyUUID
	RequestIDTimestamp = requesttracking.IDStrategyTimestamp
	RequestIDTrace     = requesttracking.IDStrategyTrace
	RequestIDNone      = requesttracking.IDStrategyNone

	CorrelationInherit  = requesttracking.CorrelationStrategyInherit
	CorrelationRequest  = requesttracking.CorrelationStrategyRequest
	CorrelationGenerate = requesttracking.CorrelationStrategyGenerate
	CorrelationNone     = requesttracking.CorrelationStrategyNone
)

// Redact strategy constants
const (
	RedactNil      = ops.RedactNil
	RedactMask     = ops.RedactMask
	RedactRemove   = ops.RedactRemove
	RedactJumble   = ops.RedactJumble
	RedactScramble = ops.RedactScramble
)

// Jumble mode constants
const (
	JumbleBasic     = ops.JumbleBasic
	JumbleSmart     = ops.JumbleSmart
	JumbleTypeAware = ops.JumbleTypeAware
)

// Suggest strategy constants
const (
	SuggestContextual = ops.SuggestContextual
	SuggestPattern    = ops.SuggestPattern
	SuggestGoal       = ops.SuggestGoal
	SuggestHybrid     = ops.SuggestHybrid
)

// Legacy option constructors retained for backward compatibility.
// New integrations should prefer the fluent request builders in `fluent.go`.
var (
	NewExtractOptions   = ops.NewExtractOptions
	NewTransformOptions = ops.NewTransformOptions
	NewGenerateOptions  = ops.NewGenerateOptions
	NewChooseOptions    = ops.NewChooseOptions
	NewFilterOptions    = ops.NewFilterOptions
	NewSortOptions      = ops.NewSortOptions
	NewClassifyOptions  = ops.NewClassifyOptions
	NewScoreOptions     = ops.NewScoreOptions
	NewCompareOptions   = ops.NewCompareOptions
	NewSimilarOptions   = ops.NewSimilarOptions
	NewInferOptions     = ops.NewInferOptions
	NewDiffOptions      = ops.NewDiffOptions
	NewExplainOptions   = ops.NewExplainOptions
	NewParseOptions     = ops.NewParseOptions
	NewSummarizeOptions = ops.NewSummarizeOptions
	NewRewriteOptions   = ops.NewRewriteOptions
	NewTranslateOptions = ops.NewTranslateOptions
	NewExpandOptions    = ops.NewExpandOptions
	NewSuggestOptions   = ops.NewSuggestOptions
	NewRedactOptions    = ops.NewRedactOptions

	// New LLM operation option constructors (v2)
	NewAnnotateOptions   = ops.NewAnnotateOptions
	NewClusterOptions    = ops.NewClusterOptions
	NewRankOptions       = ops.NewRankOptions
	NewCompressOptions   = ops.NewCompressOptions
	NewDecomposeOptions  = ops.NewDecomposeOptions
	NewEnrichOptions     = ops.NewEnrichOptions
	NewNormalizeOptions  = ops.NewNormalizeOptions
	NewMatchOptions      = ops.NewMatchOptions
	NewDecideOptions     = ops.NewDecideOptions
	NewCritiqueOptions   = ops.NewCritiqueOptions
	NewSynthesizeOptions = ops.NewSynthesizeOptions
	NewPredictOptions    = ops.NewPredictOptions
	NewVerifyOptions     = ops.NewVerifyOptions
	NewValidateOptions   = ops.NewValidateOptions
	NewQuestionOptions   = ops.NewQuestionOptions

	NewOpenAIProvider           = llm.NewOpenAIProvider
	NewAnthropicProvider        = llm.NewAnthropicProvider
	NewOpenRouterProvider       = llm.NewOpenRouterProvider
	NewCerebrasProvider         = llm.NewCerebrasProvider
	NewDeepSeekProvider         = llm.NewDeepSeekProvider
	NewQwenProvider             = llm.NewQwenProvider
	NewZAIProvider              = llm.NewZAIProvider
	NewLocalProvider            = llm.NewLocalProvider
	NewOpenAICompatibleProvider = llm.NewOpenAICompatibleProvider

	RegisterProvider        = llm.RegisterProvider
	RegisterProviderFactory = llm.RegisterProviderFactory
	CreateProvider          = llm.CreateProvider
	ListProviders           = llm.ListProviders
)

// Legacy direct-call operations retained for backward compatibility.
// New integrations should prefer the fluent request builders in `fluent.go`.

// Extract converts unstructured data into strongly-typed Go structs.
//
// Example:
//
//	type Person struct {
//	    Name string `json:"name"`
//	    Age  int    `json:"age"`
//	}
//	person, err := schemaflux.Extract[Person](jsonInput, schemaflux.NewExtractOptions())
func Extract[T any](input any, opts ExtractOptions) (T, error) {
	return ops.Extract[T](input, opts)
}

// Transform converts data from one type to another using LLM intelligence.
//
// Example:
//
//	result, err := schemaflux.Transform[InputType, OutputType](input, schemaflux.NewTransformOptions())
func Transform[T any, U any](input T, opts TransformOptions) (U, error) {
	return ops.Transform[T, U](input, opts)
}

// Generate creates new data based on a prompt.
//
// Example:
//
//	story, err := schemaflux.Generate[Story]("Write a short story about...", schemaflux.NewGenerateOptions())
func Generate[T any](prompt string, opts GenerateOptions) (T, error) {
	return ops.Generate[T](prompt, opts)
}

// Choose selects the best option from a list based on criteria.
//
// Example:
//
//	best, err := schemaflux.Choose(options, schemaflux.NewChooseOptions().WithCriteria("most relevant"))
func Choose[T any](options []T, opts ChooseOptions) (T, error) {
	return ops.Choose(options, opts)
}

// Filter filters items based on natural language criteria.
//
// Example:
//
//	filtered, err := schemaflux.Filter(items, schemaflux.NewFilterOptions().WithCondition("completed tasks"))
func Filter[T any](items []T, opts FilterOptions) ([]T, error) {
	return ops.Filter(items, opts)
}

// Sort sorts items based on natural language criteria.
//
// Example:
//
//	sorted, err := schemaflux.Sort(items, schemaflux.NewSortOptions().WithCriteria("by priority"))
func Sort[T any](items []T, opts SortOptions) ([]T, error) {
	return ops.Sort(items, opts)
}

// Classify categorizes any Go type into typed categories.
//
// Type parameter T specifies the input type.
// Type parameter C specifies the category type (typically string or custom enum).
//
// Example:
//
//	result, err := schemaflux.Classify[string, string]("Great product!",
//	    schemaflux.NewClassifyOptions().WithCategories([]string{"positive", "negative", "neutral"}))
//	fmt.Printf("Category: %s (%.0f%% confidence)\n", result.Category, result.ModelConfidence*100)
func Classify[T any, C ~string](input T, opts ClassifyOptions) (ClassifyResult[C], error) {
	return ops.Classify[T, C](input, opts)
}

// Score rates any Go type based on specified criteria.
//
// Type parameter T specifies the input type.
//
// Example:
//
//	result, err := schemaflux.Score[Essay](essay,
//	    schemaflux.NewScoreOptions().WithCriteria([]string{"clarity", "grammar"}))
//	fmt.Printf("Score: %.1f/10\n", result.Value)
func Score[T any](input T, opts ScoreOptions) (ScoreResult, error) {
	return ops.Score(input, opts)
}

// Compare analyzes similarities and differences between two items of the same type.
//
// Type parameter T specifies the type of items being compared.
//
// Example:
//
//	result, err := schemaflux.Compare[Product](product1, product2, schemaflux.NewCompareOptions())
//	fmt.Printf("Similarity: %.0f%%\n", result.SimilarityScore*100)
func Compare[T any](itemA, itemB T, opts CompareOptions) (CompareResult[T], error) {
	return ops.Compare(itemA, itemB, opts)
}

// Similar checks semantic similarity between two items of the same type.
//
// Type parameter T specifies the type of items being compared.
//
// Example:
//
//	result, err := schemaflux.Similar[string]("AI is great", "Artificial intelligence is wonderful",
//	    schemaflux.NewSimilarOptions())
//	fmt.Printf("Similar: %v (score: %.0f%%)\n", result.IsSimilar, result.Score*100)
func Similar[T any](itemA, itemB T, opts SimilarOptions) (SimilarResult, error) {
	return ops.Similar(itemA, itemB, opts)
}

// Infer fills in missing fields in partial data using LLM intelligence.
//
// Example:
//
//	complete, err := schemaflux.Infer(partialData, schemaflux.NewInferOptions())
func Infer[T any](partialData T, opts InferOptions) (T, error) {
	return ops.Infer(partialData, opts)
}

// Diff compares two data instances and explains the differences.
//
// Example:
//
//	diff, err := schemaflux.Diff(oldData, newData, schemaflux.NewDiffOptions())
func Diff[T any](oldData, newData T, opts DiffOptions) (DiffResult, error) {
	return ops.Diff(oldData, newData, opts)
}

// Explain generates human-readable explanations for complex data.
//
// Example:
//
//	explanation, err := schemaflux.Explain(complexData, schemaflux.NewExplainOptions())
func Explain(data any, opts ExplainOptions) (ExplainResult, error) {
	return ops.Explain(data, opts)
}

// Parse intelligently parses data from various formats into strongly-typed structs.
//
// Example:
//
//	result, err := schemaflux.Parse[Person](rawInput, schemaflux.NewParseOptions())
func Parse[T any](input any, opts ParseOptions) (ParseResult[T], error) {
	return ops.Parse[T](input, opts)
}

// Summarize generates a summary of the input text.
//
// Example:
//
//	summary, err := schemaflux.Summarize(longText, schemaflux.NewSummarizeOptions().WithMaxLength(100))
func Summarize(input string, opts SummarizeOptions) (string, error) {
	return ops.Summarize(input, opts)
}

// SummarizeWithMetadata summarizes text and returns rich metadata including
// compression ratio, key points, and confidence score.
//
// Example:
//
//	result, err := schemaflux.SummarizeWithMetadata(longText, schemaflux.NewSummarizeOptions())
//	fmt.Printf("Summary: %s\nKey points: %v\nCompression: %.0f%%\n",
//	    result.Text, result.KeyPoints, result.CompressionRatio*100)
//
// Deprecated: use SummarizeResult, which returns the same Summary inside a
// Result whose Meta separates what the runtime measured from what the
// model claimed. This shape carries a Metadata map instead, where the two
// are indistinguishable. See OP-401.
func SummarizeWithMetadata(input string, opts SummarizeOptions) (Summary, error) {
	return ops.SummarizeWithMetadata(input, opts)
}

// SummarizeResult summarizes text and returns the answer with the record of how it ran:
// what the call cost, how many attempts it took, and the model's own claims
// kept apart from the measurements.
//
// Example:
//
//	result, err := schemaflux.SummarizeResult(text, schemaflux.NewSummarizeOptions())
//	fmt.Println(result.Value.Text, result.Meta.Cost)
func SummarizeResult(input string, opts SummarizeOptions) (Result[Summary], error) {
	return ops.SummarizeResult(input, opts)
}

// Rewrite rewrites text according to specified instructions.
//
// Example:
//
//	rewritten, err := schemaflux.Rewrite(text, schemaflux.NewRewriteOptions().WithStyle("formal"))
func Rewrite(input string, opts RewriteOptions) (string, error) {
	return ops.Rewrite(input, opts)
}

// RewriteWithMetadata rewrites text and returns metadata including
// what changes were made, tone achieved, and confidence score.
//
// Example:
//
//	result, err := schemaflux.RewriteWithMetadata(text, schemaflux.NewRewriteOptions().WithTargetTone("professional"))
//	fmt.Printf("Rewritten: %s\nChanges: %v\nTone: %s\n",
//	    result.Text, result.ChangesMade, result.ToneAchieved)
//
// Deprecated: use RewriteResult, which returns the same Rewritten inside a
// Result whose Meta separates what the runtime measured from what the
// model claimed. This shape carries a Metadata map instead, where the two
// are indistinguishable. See OP-401.
func RewriteWithMetadata(input string, opts RewriteOptions) (Rewritten, error) {
	return ops.RewriteWithMetadata(input, opts)
}

// RewriteResult rewrites text and returns the answer with the record of how it ran:
// what the call cost, how many attempts it took, and the model's own claims
// kept apart from the measurements.
//
// Example:
//
//	result, err := schemaflux.RewriteResult(text, schemaflux.NewRewriteOptions())
//	fmt.Println(result.Value.Text, result.Meta.Cost)
func RewriteResult(input string, opts RewriteOptions) (Result[Rewritten], error) {
	return ops.RewriteResult(input, opts)
}

// Translate translates text to a target language.
//
// Example:
//
//	translated, err := schemaflux.Translate(text, schemaflux.NewTranslateOptions().WithTargetLanguage("Spanish"))
func Translate(input string, opts TranslateOptions) (string, error) {
	return ops.Translate(input, opts)
}

// TranslateWithMetadata translates text and returns metadata including
// detected source language, confidence, and alternative translations.
//
// Example:
//
//	result, err := schemaflux.TranslateWithMetadata(text, schemaflux.NewTranslateOptions().WithTargetLanguage("French"))
//	fmt.Printf("Translation: %s\nDetected language: %s\nConfidence: %.0f%%\n",
//	    result.Text, result.SourceLanguageDetected, result.ModelConfidence*100)
//
// Deprecated: use TranslateResult, which returns the same Translation inside a
// Result whose Meta separates what the runtime measured from what the
// model claimed. This shape carries a Metadata map instead, where the two
// are indistinguishable. See OP-401.
func TranslateWithMetadata(input string, opts TranslateOptions) (Translation, error) {
	return ops.TranslateWithMetadata(input, opts)
}

// TranslateResult translates text and returns the answer with the record of how it ran:
// what the call cost, how many attempts it took, and the model's own claims
// kept apart from the measurements.
//
// Example:
//
//	result, err := schemaflux.TranslateResult(text, schemaflux.NewTranslateOptions())
//	fmt.Println(result.Value.Text, result.Meta.Cost)
func TranslateResult(input string, opts TranslateOptions) (Result[Translation], error) {
	return ops.TranslateResult(input, opts)
}

// Expand expands brief text into more detailed content.
//
// Example:
//
//	expanded, err := schemaflux.Expand(briefText, schemaflux.NewExpandOptions().WithTargetLength(500))
func Expand(input string, opts ExpandOptions) (string, error) {
	return ops.Expand(input, opts)
}

// ExpandWithMetadata expands text and returns metadata including
// expansion ratio, what content was added, and confidence score.
//
// Example:
//
//	result, err := schemaflux.ExpandWithMetadata(briefText, schemaflux.NewExpandOptions())
//	fmt.Printf("Expanded: %s\nExpansion ratio: %.1fx\nAdded: %v\n",
//	    result.Text, result.ExpansionRatio, result.AddedContent)
//
// Deprecated: use ExpandResult, which returns the same Expansion inside a
// Result whose Meta separates what the runtime measured from what the
// model claimed. This shape carries a Metadata map instead, where the two
// are indistinguishable. See OP-401.
func ExpandWithMetadata(input string, opts ExpandOptions) (Expansion, error) {
	return ops.ExpandWithMetadata(input, opts)
}

// ExpandResult expands text and returns the answer with the record of how it ran:
// what the call cost, how many attempts it took, and the model's own claims
// kept apart from the measurements.
//
// Example:
//
//	result, err := schemaflux.ExpandResult(text, schemaflux.NewExpandOptions())
//	fmt.Println(result.Value.Text, result.Meta.Cost)
func ExpandResult(input string, opts ExpandOptions) (Result[Expansion], error) {
	return ops.ExpandResult(input, opts)
}

// Suggest generates context-aware suggestions.
//
// Example:
//
//	suggestions, err := schemaflux.Suggest[Suggestion](context, schemaflux.NewSuggestOptions().WithCount(5))
func Suggest[T any](input any, opts SuggestOptions) ([]T, error) {
	return ops.Suggest[T](input, opts)
}

// Redact removes or masks sensitive information from data.
//
// Example:
//
//	redacted, err := schemaflux.Redact(sensitiveData, schemaflux.NewRedactOptions().WithPatterns([]string{"SSN", "email"}))
func Redact[T any](input T, opts RedactOptions) (T, error) {
	return ops.Redact(input, opts)
}

// Re-export LLM-powered Redact types and functions
type (
	RedactLLMOptions = ops.RedactLLMOptions
	RedactLLMResult  = ops.RedactLLMResult
	RedactSpan       = ops.RedactSpan
)

var NewRedactLLMOptions = ops.NewRedactLLMOptions

// RedactLLM uses LLM to intelligently identify and mask sensitive data at character level.
// Unlike the regex-based Redact, this uses AI to understand context and detect sensitive
// information that patterns might miss.
//
// Example:
//
//	result, err := schemaflux.RedactLLM("Contact john@email.com for support",
//	    schemaflux.NewRedactLLMOptions().
//	        WithCategories([]string{"email", "phone"}).
//	        WithMaskChar('*').
//	        WithShowFirst(2).
//	        WithShowLast(2))
//	// result.Text = "Contact jo**********om for support"
//	// result.Spans = [{Start: 8, End: 22, Category: "email", Original: "john@email.com"}]
func RedactLLM(text string, opts RedactLLMOptions) (RedactLLMResult, error) {
	return ops.RedactLLM(context.Background(), text, opts)
}

// Re-export Complete types and functions
type (
	CompleteOptions = ops.CompleteOptions
	CompleteResult  = ops.CompleteResult
)

var NewCompleteOptions = ops.NewCompleteOptions

// NewCompleteFieldOptions creates options for completing a specific field in a struct.
var NewCompleteFieldOptions = ops.NewCompleteFieldOptions

// CompleteFieldResult is the result of completing a field in a struct.
type CompleteFieldResult[T any] = ops.CompleteFieldResult[T]

// CompleteFieldOptions configures the CompleteField operation.
type CompleteFieldOptions = ops.CompleteFieldOptions

// Complete intelligently completes partial text using LLM intelligence.
// Note: This is a simplified wrapper that uses the default provider.
//
// Example:
//
//	result, err := schemaflux.Complete(partialText, schemaflux.NewCompleteOptions())
func Complete(partialText string, opts CompleteOptions) (CompleteResult, error) {
	return ops.Complete(context.Background(), partialText, opts)
}

// CompleteField completes a specific string field in a struct and returns a new copy
// with the completed field. The struct's other fields provide context for completion.
//
// Example:
//
//	type BlogPost struct {
//	    Title string `json:"title"`
//	    Body  string `json:"body"`
//	}
//	post := BlogPost{Title: "AI in Healthcare", Body: "Artificial intelligence is transforming"}
//	result, err := schemaflux.CompleteField[BlogPost](post, schemaflux.NewCompleteFieldOptions("Body"))
//	// result.Data.Body now contains the completed text
func CompleteField[T any](data T, opts CompleteFieldOptions) (CompleteFieldResult[T], error) {
	return ops.CompleteField[T](context.Background(), data, opts)
}

// Validate checks if data meets specified validation rules with rich result.
//
// Example:
//
//	result, err := schemaflux.Validate[Person](person, schemaflux.NewValidateOptions().
//	    WithRules("age must be 18-100, email must be valid"))
//	if !result.Valid {
//	    for _, err := range result.Errors {
//	        fmt.Printf("Error: %s\n", err.Message)
//	    }
//	}
//
// Deprecated: use ValidateHybrid, or ValidateDeterministically when every rule is Go-decidable
// and no provider call should ever happen. The old name did not say whether the
// check was deterministic or model-assisted, so a caller could not tell from
// the call site whether it was about to make a network request. See OP-206.
func Validate[T any](data T, opts ValidateOptions) (ValidateResult[T], error) {
	return ops.Validate(data, opts)
}

// ValidateLegacy is the legacy validation function for backward compatibility.
//
// Example:
//
//	result, err := schemaflux.ValidateLegacy(person, "age must be 18-100")
//
// DeduplicateResult holds the unique items, the groups that collapsed into
// them, and how many were removed.
type DeduplicateResult[T any] = ops.DeduplicateResult[T]

// Deduplicate removes semantically duplicate items, keeping the caller's own
// values.
//
// It was implemented in internal/ops and exported by nothing, so no consumer
// could call it -- while `core/doc.go` listed it among the available
// operations. A documented operation that cannot be reached is worse than an
// undocumented one: the reader concludes the library is broken rather than that
// the doc is (A-10).
//
// The threshold is a similarity floor between 0 and 1. Note what this costs: it
// asks the model about pairs, so it is O(n^2) calls in the worst case. PS-006
// tracks doing it with embeddings, which is the right shape for this problem.
func Deduplicate[T any](items []T, threshold float64, opts ...OpOptions) (DeduplicateResult[T], error) {
	return ops.Deduplicate(items, threshold, opts...)
}

// Deprecated: use Validate. ValidationResult is a bool, a []string, and a
// model-reported confidence; ValidateResult[T] says which field failed, how
// badly, and offers a correction. See internal/ops for the full note.
func ValidateLegacy[T any](data T, rules string, opts ...OpOptions) (ValidationResult, error) {
	return ops.ValidateLegacy(data, rules, opts...)
}

// Question answers questions about data and returns a typed answer.
//
// Example:
//
//	result, err := schemaflux.Question[Report, string](report, schemaflux.NewQuestionOptions("What is the main finding?"))
//	fmt.Println(result.Answer, "confidence:", result.ModelConfidence)
func Question[T any, A any](data T, opts QuestionOptions) (QuestionResult[A], error) {
	return ops.Question[T, A](data, opts)
}

// QuestionLegacy answers questions about data using the legacy string interface.
//
// Example:
//
//	answer, err := schemaflux.QuestionLegacy(report, "What are the top 3 risks?")
//
// Deprecated: use Question, which returns the answer with its supporting
// detail rather than a bare string.
func QuestionLegacy(data any, question string, opts ...OpOptions) (string, error) {
	return ops.QuestionLegacy(data, question, opts...)
}

// Merge combines multiple data sources using a specified strategy.
//
// Example:
//
//	merged, err := schemaflux.Merge(sources, "first-wins")
func Merge[T any](sources []T, strategy string, opts ...OpOptions) (T, error) {
	return ops.Merge(sources, strategy, opts...)
}

// MergeWithMetadata combines data sources and returns metadata including
// which sources were used, any conflicts, and how they were resolved.
//
// Example:
//
//	result, err := schemaflux.MergeWithMetadata(sources, "prefer-newest")
//	fmt.Printf("Merged: %+v\nConflicts: %d\nConfidence: %.0f%%\n",
//	    result.Merged, len(result.Conflicts), result.ModelConfidence*100)
func MergeWithMetadata[T any](sources []T, strategy string, opts ...OpOptions) (MergeResult[T], error) {
	return ops.MergeWithMetadata(sources, strategy, opts...)
}

// Format converts data to a specific output format.
//
// Example:
//
//	formatted, err := schemaflux.Format(data, "markdown table")
func Format(data any, template string, opts ...OpOptions) (string, error) {
	return ops.Format(data, template, opts...)
}

// FormatWithMetadata formats data and returns metadata including
// what format was applied, transformation notes, and confidence score.
//
// Example:
//
//	result, err := schemaflux.FormatWithMetadata(data, "professional bio in third person")
//	fmt.Printf("Formatted: %s\nFormat applied: %s\n", result.Text, result.FormatApplied)
func FormatWithMetadata(data any, template string, opts ...OpOptions) (FormatResult, error) {
	return ops.FormatWithMetadata(data, template, opts...)
}

// Decide chooses among decisions, first by their programmatic conditions and
// then by asking the model. situation is the prompt data describing the
// circumstances; ctx governs cancellation.
//
// Without a fallback, a provider failure or an unusable answer is an error.
// Pass NewDecideOptions().WithFallback(i) to select a branch instead, which
// sets Fallback on the result so the caller can tell a real decision from a
// default one.
//
// Example:
//
//	chosen, decision, err := schemaflux.Decide(ctx, ticket, departments)
func Decide[T any](ctx context.Context, situation any, decisions []Decision[T], opts ...DecideOptions) (T, DecisionResult, error) {
	return ops.Decide(ctx, situation, decisions, opts...)
}

// Match runs the action of the first case whose condition holds and reports
// whether any case ran. The default case is evaluated last wherever it appears.
//
// Example:
//
//	matched, err := schemaflux.Match(ctx, ticket,
//	    schemaflux.When("billing question", handleBilling),
//	    schemaflux.Otherwise(handleGeneral))
func Match(ctx context.Context, input any, cases ...Case) (bool, error) {
	return ops.Match(ctx, input, cases...)
}

// When builds a case from any condition: a string evaluated semantically, a
// reflect.Type, an error value, or a value whose type is compared.
func When(condition any, action func()) Case {
	return ops.When(condition, action)
}

// Like builds a case from a natural-language template.
func Like(template string, action func()) Case {
	return ops.Like(template, action)
}

// Otherwise builds the default case, which runs only if no other case matched.
func Otherwise(action func()) Case {
	return ops.Otherwise(action)
}

// StatusCodeFrom recovers the HTTP status from a provider failure, through
// whatever wrapping the call stack added. It is the supported way to ask "was
// this a 429 or a 400" -- the alternative is matching on an English sentence.
func StatusCodeFrom(err error) (int, bool) { return llm.StatusCodeFrom(err) }

// RetryAfterFrom returns the wait a rate-limited provider asked for, and
// whether the failure was a rate limit at all.
func RetryAfterFrom(err error) (time.Duration, bool) { return llm.RetryAfterFrom(err) }

// MapReduce splits items into chunks, runs operation on each with bounded
// concurrency, and returns the per-chunk results in input order.
//
// It exists because a collection larger than one context window had no
// supported handling: the collection operations could not take one, and there
// was no primitive for a caller to build around. Chunking a Filter is safe
// automatically, because the merge is a concatenation; chunking a Sort is not,
// because interleaving two sorted runs needs knowledge only the caller has.
// This is that knowledge's home.
//
// Example:
//
//	ranked, summary, err := schemaflux.MapReduce(ctx, candidates,
//	    schemaflux.MapReduceOptions{ChunkSize: 20},
//	    func(ctx context.Context, chunk []Candidate) ([]Candidate, error) {
//	        return schemaflux.Sort(chunk, schemaflux.NewSortOptions().WithCriteria("strongest first"))
//	    })
//	// ... then merge the sorted runs however your domain requires.
func MapReduce[T any, R any](
	ctx context.Context,
	items []T,
	opts MapReduceOptions,
	operation func(ctx context.Context, chunk []T) (R, error),
) ([]R, MapReduceResult, error) {
	return ops.MapReduce(ctx, items, opts, operation)
}

// MapReduceFlat is MapReduce for the common case where each chunk produces a
// slice and the caller wants them concatenated in input order.
func MapReduceFlat[T any, R any](
	ctx context.Context,
	items []T,
	opts MapReduceOptions,
	operation func(ctx context.Context, chunk []T) ([]R, error),
) ([]R, MapReduceResult, error) {
	return ops.MapReduceFlat(ctx, items, opts, operation)
}

// Chunk splits a slice into runs of at most size.
func Chunk[T any](items []T, size int) [][]T {
	return ops.Chunk(items, size)
}

// Guard checks if conditions are met before proceeding.
//
// Example:
//
//	result := schemaflux.Guard(ctx, state, check1, check2)
func Guard[T any](ctx context.Context, state T, checks ...func(T) (bool, string)) GuardResult {
	return ops.Guard(ctx, state, checks...)
}

// === New LLM Operations (v2) ===

// Annotate extracts semantic annotations (entities, sentiments, topics) from text.
//
// Example:
//
//	result, err := schemaflux.Annotate(document, schemaflux.NewAnnotateOptions().WithTypes([]string{"entities", "sentiment"}))
func Annotate[T any](input T, opts AnnotateOptions) (AnnotateResult, error) {
	return ops.Annotate(input, opts)
}

// Cluster groups items by semantic similarity into natural clusters.
//
// Example:
//
//	result, err := schemaflux.Cluster(documents, schemaflux.NewClusterOptions().WithMaxClusters(5))
func Cluster[T any](items []T, opts ClusterOptions) (ClusterResult[T], error) {
	return ops.Cluster(items, opts)
}

// Rank orders items by relevance to a query using semantic understanding.
//
// Example:
//
//	result, err := schemaflux.Rank(documents, schemaflux.NewRankOptions().WithQuery("machine learning"))
func Rank[T any](items []T, opts RankOptions) (RankResult[T], error) {
	return ops.Rank(items, opts)
}

// Compress reduces content while preserving essential meaning.
//
// Example:
//
//	result, err := schemaflux.Compress(document, schemaflux.NewCompressOptions().WithRatio(0.3))
func Compress[T any](input T, opts CompressOptions) (CompressResult[T], error) {
	return ops.Compress(input, opts)
}

// CompressText compresses text content using semantic compression.
//
// Example:
//
//	compressed, err := schemaflux.CompressText("long text here...", schemaflux.NewCompressOptions().WithRatio(0.5))
func CompressText(input string, opts CompressOptions) (string, error) {
	return ops.CompressText(input, opts)
}

// Decompose breaks down complex items into atomic parts using LLM intelligence.
//
// Example:
//
//	result, err := schemaflux.Decompose(complexTask, schemaflux.NewDecomposeOptions().WithMode("hierarchical"))
func Decompose[T any](input T, opts DecomposeOptions) (DecomposeResult[T], error) {
	return ops.Decompose(input, opts)
}

// DecomposeToSlice breaks down complex items and returns just the parts.
//
// Example:
//
//	parts, err := schemaflux.DecomposeToSlice[Task, SubTask](complexTask, schemaflux.NewDecomposeOptions())
func DecomposeToSlice[T any, U any](input T, opts DecomposeOptions) ([]U, error) {
	return ops.DecomposeToSlice[T, U](input, opts)
}

// Enrich adds derived or inferred fields to existing data.
//
// Example:
//
//	result, err := schemaflux.Enrich[Person, EnrichedPerson](person, schemaflux.NewEnrichOptions().WithFields([]string{"age_bracket", "generation"}))
func Enrich[T any, U any](input T, opts EnrichOptions) (EnrichResult[U], error) {
	return ops.Enrich[T, U](input, opts)
}

// EnrichInPlace enriches data in place, returning the same type.
//
// Example:
//
//	enriched, err := schemaflux.EnrichInPlace(person, schemaflux.NewEnrichOptions())
func EnrichInPlace[T any](input T, opts EnrichOptions) (T, error) {
	return ops.EnrichInPlace(input, opts)
}

// Normalize standardizes data formats and values for consistency.
//
// Example:
//
//	result, err := schemaflux.Normalize(data, schemaflux.NewNormalizeOptions().WithRules([]string{"dates", "phone_numbers"}))
func Normalize[T any](input T, opts NormalizeOptions) (NormalizeResult[T], error) {
	return ops.Normalize(input, opts)
}

// NormalizeText normalizes text content.
//
// Example:
//
//	normalized, err := schemaflux.NormalizeText("messy text...", schemaflux.NewNormalizeOptions())
func NormalizeText(input string, opts NormalizeOptions) (string, error) {
	return ops.NormalizeText(input, opts)
}

// NormalizeBatch normalizes multiple items at once.
//
// Example:
//
//	results, err := schemaflux.NormalizeBatch(items, schemaflux.NewNormalizeOptions())
func NormalizeBatch[T any](items []T, opts NormalizeOptions) ([]NormalizeResult[T], error) {
	return ops.NormalizeBatch(items, opts)
}

// SemanticMatch pairs items from two sets based on semantic similarity.
// Note: This is different from the control-flow Match operation.
//
// Example:
//
//	result, err := schemaflux.SemanticMatch(resumes, jobs, schemaflux.NewMatchOptions().WithThreshold(0.7))
func SemanticMatch[S any, T any](sources []S, targets []T, opts MatchOptions) (MatchResult[S, T], error) {
	return ops.SemanticMatch(sources, targets, opts)
}

// MatchOne finds the best matching targets for a single source item.
//
// Example:
//
//	matches, err := schemaflux.MatchOne(resume, jobs, schemaflux.NewMatchOptions())
func MatchOne[S any, T any](source S, targets []T, opts MatchOptions) ([]MatchPair[S, T], error) {
	return ops.MatchOne(source, targets, opts)
}

// Critique provides constructive feedback on content quality.
//
// Example:
//
//	result, err := schemaflux.Critique(essay, schemaflux.NewCritiqueOptions().WithAspects([]string{"clarity", "argument_strength"}))
//
// Deprecated: use CritiqueWithModel, whose name says that this review is
// model-assisted rather than a deterministic check, and which returns the
// shared JudgmentResult instead of this operation's own shape. See OP-206.
func Critique[T any](input T, opts CritiqueOptions) (CritiqueResult, error) {
	return ops.Critique(input, opts)
}

// Synthesize combines information from multiple sources into a coherent whole.
//
// Example:
//
//	result, err := schemaflux.Synthesize[Summary](articles, schemaflux.NewSynthesizeOptions().WithPerspective("balanced"))
func Synthesize[T any](sources []any, opts SynthesizeOptions) (SynthesizeResult[T], error) {
	return ops.Synthesize[T](sources, opts)
}

// Predict forecasts future states or outcomes based on patterns.
//
// Example:
//
//	result, err := schemaflux.Predict[Forecast](salesData, schemaflux.NewPredictOptions().WithHorizon("next_quarter"))
func Predict[T any](historicalData any, opts PredictOptions) (PredictResult[T], error) {
	return ops.Predict[T](historicalData, opts)
}

// Verify checks facts, logic, and consistency in content.
//
// Example:
//
//	result, err := schemaflux.Verify("The Earth is flat.", schemaflux.NewVerifyOptions().WithMode("factual"))
//
// Deprecated: use VerifyWithModel, whose name says that this review is
// model-assisted rather than a deterministic check, and which returns the
// shared JudgmentResult instead of this operation's own shape. See OP-206.
func Verify(input string, opts VerifyOptions) (VerifyResult, error) {
	return ops.Verify(input, opts)
}

// VerifyClaim checks a specific claim.
//
// Example:
//
//	result, err := schemaflux.VerifyClaim("GDP grew 5%", schemaflux.NewVerifyOptions())
func VerifyClaim(claim string, opts VerifyOptions) (ClaimVerification, error) {
	return ops.VerifyClaim(claim, opts)
}

// === Data-Centric LLM Operations (v3) ===

// Negotiate reconciles competing constraints to find an optimal solution.
//
// Type parameter T specifies the output solution type.
//
// Example:
//
//	result, err := schemaflux.Negotiate[Schedule](constraints)
//	result, err := schemaflux.Negotiate[Schedule](constraints, schemaflux.NegotiateOptions{
//	    Strategy: "balanced",
//	})
//
// Deprecated: use Settle. This is one model round trip that produces a
// settlement from constraints, not an exchange, and a conversational name on a
// single-shot operation is what PS-005 forbids.
func Negotiate[T any](constraints any, opts ...NegotiateOptions) (NegotiateResult[T], error) {
	return ops.Negotiate[T](constraints, opts...)
}

// Settle produces a settlement satisfying the given constraints, in one model
// round trip.
func Settle[T any](constraints any, opts ...NegotiateOptions) (NegotiateResult[T], error) {
	return ops.Settle[T](constraints, opts...)
}

// AdversarialPosition represents one party's position in a negotiation.
type AdversarialPosition[T any] = ops.AdversarialPosition[T]

// AdversarialContext provides the negotiation dynamics between two parties.
type AdversarialContext[T any] = ops.AdversarialContext[T]

// TermMovement tracks how a specific term moved during negotiation.
type TermMovement = ops.TermMovement

// AdversarialResult contains the outcome of an adversarial negotiation.
type AdversarialResult[T any] = ops.AdversarialResult[T]

// AdversarialOptions configures the adversarial negotiation.
type AdversarialOptions = ops.AdversarialOptions

// NegotiateAdversarial conducts a two-party adversarial negotiation.
//
// This models real-world negotiations where two parties with opposing interests
// must find common ground. The leverage parameter determines who has more power
// and thus who should concede more.
//
// Type parameter T specifies the structure of positions and the final deal.
//
// Example:
//
//	type SalaryTerms struct {
//	    BaseSalary int `json:"base_salary"`
//	    RemoteDays int `json:"remote_days"`
//	}
//	ctx := schemaflux.AdversarialContext[SalaryTerms]{
//	    Ours:        schemaflux.AdversarialPosition[SalaryTerms]{Position: SalaryTerms{BaseSalary: 160000, RemoteDays: 5}},
//	    Theirs:      schemaflux.AdversarialPosition[SalaryTerms]{Position: SalaryTerms{BaseSalary: 130000, RemoteDays: 2}},
//	    OurLeverage: "strong",
//	}
//	result, err := schemaflux.NegotiateAdversarial[SalaryTerms](ctx)
//	// result.Deal has the final terms
//	// result.TermMovements shows who moved on each term
//	// result.WhoConcededMore indicates "they" since we had strong leverage
//
// Deprecated: use SettleAdversarial. One model round trip, not an exchange.
func NegotiateAdversarial[T any](context AdversarialContext[T], opts ...AdversarialOptions) (AdversarialResult[T], error) {
	return ops.NegotiateAdversarial[T](context, opts...)
}

// SettleAdversarial produces a settlement between opposed positions in one
// model round trip.
func SettleAdversarial[T any](context AdversarialContext[T], opts ...AdversarialOptions) (AdversarialResult[T], error) {
	return ops.SettleAdversarial[T](context, opts...)
}

// Resolve resolves conflicts when multiple typed sources disagree.
//
// Type parameter T specifies the type of sources and the resolved output.
//
// Example:
//
//	result, err := schemaflux.Resolve(conflictingSources)
//	result, err := schemaflux.Resolve(conflictingSources, schemaflux.ResolveOptions{
//	    Strategy: "most-complete",
//	})
func Resolve[T any](sources []T, opts ...ResolveOptions) (ResolveResult[T], error) {
	return ops.Resolve[T](sources, opts...)
}

// Derive infers new typed fields from existing data.
//
// Type parameter T specifies the input type.
// Type parameter U specifies the output type with derived fields.
//
// Example:
//
//	result, err := schemaflux.Derive[Person, EnrichedPerson](person)
//	result, err := schemaflux.Derive[Person, EnrichedPerson](person, schemaflux.DeriveOptions{
//	    TargetFields: []string{"age_category", "generation"},
//	})
func Derive[T any, U any](input T, opts ...DeriveOptions) (DeriveResult[U], error) {
	return ops.Derive[T, U](input, opts...)
}

// Conform transforms data to match specific standards (USPS, ISO8601, E164, etc.).
//
// Type parameter T specifies the data type to conform.
//
// Example:
//
//	result, err := schemaflux.Conform(address, "USPS")
//	result, err := schemaflux.Conform(phoneData, "E164", schemaflux.ConformOptions{
//	    Strict: true,
//	})
func Conform[T any](input T, standard string, opts ...ConformOptions) (ConformResult[T], error) {
	return ops.Conform[T](input, standard, opts...)
}

// Interpolate fills gaps in typed sequences intelligently.
//
// Type parameter T specifies the type of sequence items.
//
// Example:
//
//	result, err := schemaflux.Interpolate(timeSeriesData)
//	result, err := schemaflux.Interpolate(sparseRecords, schemaflux.InterpolateOptions{
//	    Method: "contextual",
//	})
func Interpolate[T any](items []T, opts ...InterpolateOptions) (InterpolateResult[T], error) {
	return ops.Interpolate[T](items, opts...)
}

// Arbitrate makes rule-based decisions with full audit trail.
//
// Type parameter T specifies the type of options to choose from.
//
// Example:
//
//	result, err := schemaflux.Arbitrate(candidates)
//	result, err := schemaflux.Arbitrate(candidates, schemaflux.ArbitrateOptions{
//	    Rules: []string{"must have 3+ years experience", "prefer local candidates"},
//	})
func Arbitrate[T any](options []T, opts ...ArbitrateOptions) (ArbitrateResult[T], error) {
	return ops.Arbitrate[T](options, opts...)
}

// Project transforms structure while preserving semantics.
//
// Type parameter T specifies the input type.
// Type parameter U specifies the projected output type.
//
// Example:
//
//	result, err := schemaflux.Project[Order, OrderSummary](order)
//	result, err := schemaflux.Project[UserProfile, PublicProfile](profile, schemaflux.ProjectOptions{
//	    Mappings: map[string]string{"full_name": "display_name"},
//	    Exclude:  []string{"password_hash", "ssn"},
//	})
func Project[T any, U any](input T, opts ...ProjectOptions) (ProjectResult[U], error) {
	return ops.Project[T, U](input, opts...)
}

// Audit performs deep inspection for issues, anomalies, and policy violations.
//
// Type parameter T specifies the type of data to audit.
//
// Example:
//
//	result, err := schemaflux.Audit(customerRecord)
//	result, err := schemaflux.Audit(financialData, schemaflux.AuditOptions{
//	    Policies:   []string{"PII must be encrypted", "Amounts must balance"},
//	    Categories: []string{"security", "compliance"},
//	})
//
// Deprecated: use AuditWithModel, whose name says that this review is
// model-assisted rather than a deterministic check, and which returns the
// shared JudgmentResult instead of this operation's own shape. See OP-206.
func Audit[T any](data T, opts ...AuditOptions) (AuditResult[T], error) {
	return ops.Audit[T](data, opts...)
}

// Assemble builds a complex typed object from multiple parts.
//
// Type parameter T specifies the target type to compose.
//
// Example:
//
//	result, err := schemaflux.Assemble[UserProfile]([]any{basicInfo, addressData, preferences})
//	result, err := schemaflux.Assemble[Document](parts, schemaflux.ComposeOptions{
//	    MergeStrategy: "smart",
//	    FillGaps:      true,
//	})
func Assemble[T any](parts []any, opts ...ComposeOptions) (ComposeResult[T], error) {
	return ops.Assemble[T](parts, opts...)
}

// Pivot restructures data relationships between typed objects.
//
// Type parameter T specifies the input type.
// Type parameter U specifies the pivoted output type.
//
// Example:
//
//	result, err := schemaflux.Pivot[[]SalesRow, []SalesPivot](sales, schemaflux.PivotOptions{
//	    PivotOn:   []string{"Month"},
//	    Aggregate: "sum",
//	})
//	result, err := schemaflux.Pivot[Nested, Flat](data, schemaflux.PivotOptions{
//	    Flatten: true,
//	})
func Pivot[T any, U any](input T, opts ...PivotOptions) (PivotResult[U], error) {
	return ops.Pivot[T, U](input, opts...)
}

// Step is one attempt at producing a value, for the control-flow combinators
// below. It takes the context rather than closing over one, so a combinator
// that runs it more than once can still be cancelled between attempts.
type Step[T any] = ops.Step[T]

// EscalationRecord reports whether Escalate had to run the stronger step, and
// why.
type EscalationRecord = ops.EscalationRecord

// Escalate runs first, and runs stronger only when it has to: when first
// failed in a way a different route could survive, or when it succeeded and
// accept turned the answer down. A nil accept means any successful answer is
// kept, which makes Escalate a pure failure route.
//
// A terminal failure does not escalate. A malformed request or a policy
// refusal fails the same way on a stronger model, so escalating spends real
// money to buy nothing.
//
// Example:
//
//	answer, record, err := schemaflux.Escalate(ctx,
//	    func(ctx context.Context) (Verdict, error) { return classifyFast(ctx, ticket) },
//	    func(ctx context.Context) (Verdict, error) { return classifySmart(ctx, ticket) },
//	    func(v Verdict) bool { return v.ModelConfidence >= 0.8 })
func Escalate[T any](ctx context.Context, first, stronger Step[T], accept func(T) bool) (T, EscalationRecord, error) {
	return ops.Escalate(ctx, first, stronger, accept)
}

// Fallback runs primary, and alternate if primary fails. It is Escalate with
// no accept function, so the two cannot disagree about which failures are
// worth another route.
func Fallback[T any](ctx context.Context, primary, alternate Step[T]) (T, error) {
	return ops.Fallback(ctx, primary, alternate)
}

// Until runs step until the answer satisfies pred, and returns an error when
// the attempts run out. Running out is a failure: the last answer is by
// definition one that failed the predicate, and returning it as a success
// means the caller who wrote the predicate never learns it was violated. The
// rejected answer is returned alongside the error for inspection.
func Until[T any](ctx context.Context, step Step[T], pred func(T) bool, max int) (T, int, error) {
	return ops.Until(ctx, step, pred, max)
}

// --- Judgment operations (OP-206).
//
// Validate, Verify, Audit, and Critique were one operation shape -- verdict,
// issues, summary -- wearing four vocabularies with four sets of field names.
// They share JudgmentResult now, and the model-assisted ones say so in their
// names: a review a model performed must not be spelled the same as a check
// Go decided, because the call site is where a reader decides whether a
// network request is about to happen.
type (
	// JudgmentResult is the shared shape: what was judged, the verdict, the
	// issues found, the evidence, and the model's own claims kept apart from
	// all of it.
	JudgmentResult[T any] = types.JudgmentResult[T]

	// JudgmentIssue is one finding.
	JudgmentIssue = types.JudgmentIssue

	// Verdict is the overall finding: Pass, Fail, Mixed, or Unknown.
	Verdict = types.Verdict
)

const (
	VerdictUnknown = types.VerdictUnknown
	VerdictPass    = types.VerdictPass
	VerdictFail    = types.VerdictFail
	VerdictMixed   = types.VerdictMixed
)

// ValidateDeterministically checks a value against rules Go can decide by
// itself and makes no provider call at all. A rule it cannot decide is an
// error rather than something quietly handed to a model, so a caller who
// wanted a deterministic check cannot accidentally be sold a judgement.
func ValidateDeterministically[T any](data T, opts ValidateOptions) (JudgmentResult[T], error) {
	return ops.ValidateDeterministically(data, opts)
}

// ValidateHybrid decides in Go what it can and asks a model about the rest.
// This is what Validate always did; the name now says so.
func ValidateHybrid[T any](data T, opts ValidateOptions) (JudgmentResult[T], error) {
	return ops.ValidateHybrid(data, opts)
}

// VerifyWithModel checks claims with a model. The verdict is the model's, and
// the name says so.
func VerifyWithModel(input string, opts VerifyOptions) (JudgmentResult[any], error) {
	return ops.VerifyWithModel(input, opts)
}

// AuditWithModel inspects a value with a model against the audit's criteria.
func AuditWithModel[T any](data T, opts ...AuditOptions) (JudgmentResult[T], error) {
	return ops.AuditWithModel(data, opts...)
}

// CritiqueWithModel reviews a value with a model.
func CritiqueWithModel[T any](input T, opts CritiqueOptions) (JudgmentResult[T], error) {
	return ops.CritiqueWithModel(input, opts)
}

// --- Sampling, checkpointing, and review (CF-002, CF-005, CF-006).

type (
	// Reconciler decides what a set of samples agrees on, and may decline to
	// decide. Agreement is a policy, not a proof: correlated models share
	// hallucinations, so samples agreeing on an invented figure are samples
	// that are uniformly wrong.
	Reconciler[T any] = ops.Reconciler[T]

	// ReconcileOutcome is a reconciler's answer, including its right to abstain.
	ReconcileOutcome[T any] = ops.ReconcileOutcome[T]

	// VoteOptions configures sampling.
	VoteOptions = ops.VoteOptions

	// VoteRecord reports what the samples did. AgreementRate is named for what
	// it measures -- how many samples agreed -- and is not a probability that
	// the winner is correct.
	VoteRecord = ops.VoteRecord

	// CheckpointStore is where a resumable run keeps its progress. Callers
	// implement it; this library does not choose a database for them.
	CheckpointStore = ops.CheckpointStore

	// CheckpointRecord is one stored step.
	CheckpointRecord = ops.CheckpointRecord

	// CheckpointOutcome says whether a step was resumed, and whether the input
	// had changed since it was stored.
	CheckpointOutcome = ops.CheckpointOutcome

	// ApprovalGate is the caller's decision point.
	ApprovalGate[T any] = ops.ApprovalGate[T]

	// ApprovalOutcome is what the gate decided.
	ApprovalOutcome = ops.ApprovalOutcome

	// ReviewPacket is what a run hands back when it stops and asks for a human.
	// It carries references and shapes rather than the caller's values, because
	// a review packet that embeds the records leaks them into whatever handles
	// it.
	ReviewPacket[T any] = types.ReviewPacket[T]

	// ReviewRequiredError carries a ReviewPacket and matches ErrReviewRequired.
	ReviewRequiredError[T any] = types.ReviewRequiredError[T]
)

// Vote runs a step several times and asks a reconciler what they agree on.
//
// The reconciler may abstain, returning ErrReviewRequired rather than the
// majority answer -- which is the point: stopping and saying "these did not
// agree" is a better outcome than handing back a plurality as though it were
// a finding.
func Vote[T any](ctx context.Context, samples int, step Step[T], reconcile Reconciler[T], opts VoteOptions) (T, VoteRecord, error) {
	return ops.Vote(ctx, samples, step, reconcile, opts)
}

// ExactAgreement is a reconciler that requires min samples to be identical.
func ExactAgreement[T comparable](min int) Reconciler[T] {
	return ops.ExactAgreement[T](min)
}

// Checkpoint runs a step once per run, storing its result so a resumed run
// does not repeat work whose side effect already happened. A resume against a
// changed input is detected rather than silently accepted.
func Checkpoint[T any](ctx context.Context, store CheckpointStore, runID, step string, input any, produce Step[T]) (T, CheckpointOutcome, error) {
	return ops.Checkpoint(ctx, store, runID, step, input, produce)
}

// NewMemoryCheckpointStore returns an in-process store, which is what tests
// and single-process runs want.
func NewMemoryCheckpointStore() *ops.MemoryCheckpointStore {
	return ops.NewMemoryCheckpointStore()
}

// Approve runs a step and puts its result to a gate, returning
// ErrReviewRequired with a ReviewPacket when the gate declines or when
// automated recovery has run out.
//
// That is a successful safety outcome, not a failure: it is the alternative to
// looping a model until it says something acceptable.
func Approve[T any](ctx context.Context, step Step[T], gate ApprovalGate[T], inputRefs []string) (T, error) {
	return ops.Approve(ctx, step, gate, inputRefs)
}

// --- Streaming (ST-001, ST-002).

// TextStream is the caller-facing handle for a streaming text operation. Its
// buffer is bounded, breaking out of All cancels the remaining work unless
// Detach was called first, and items arrive in completion order.
type TextStream = ops.TextStream

// StreamSummarize summarizes text, delivering it as it arrives.
func StreamSummarize(input string, opts SummarizeOptions) (*TextStream, error) {
	return ops.StreamSummarize(input, opts)
}

// StreamRewrite rewrites text, delivering it as it arrives.
func StreamRewrite(input string, opts RewriteOptions) (*TextStream, error) {
	return ops.StreamRewrite(input, opts)
}

// StreamTranslate translates text, delivering it as it arrives.
func StreamTranslate(input string, opts TranslateOptions) (*TextStream, error) {
	return ops.StreamTranslate(input, opts)
}

// StreamExpand expands text, delivering it as it arrives.
func StreamExpand(input string, opts ExpandOptions) (*TextStream, error) {
	return ops.StreamExpand(input, opts)
}

// --- The operation descriptor (A-001).
//
// An Op is what an operation *is*, separated from the machinery that runs it:
// its identity and version, what it is allowed to do, the contract its output
// must satisfy, how it batches, and its default policy. It holds no context
// and no provider — it is data plus pure functions, which is what lets
// planning, recipes, and middleware work on operations without a second
// execution path. Extract runs through one (A-012), and its prompts did not
// move, which is the proof the descriptor carries a real operation rather than
// describing one.
type (
	// OperationID names an operation and its version. The version exists
	// because a prompt edit is a behaviour change with no Go API change to
	// show for it, and it needs something to hang on.
	OperationID = types.OperationID

	// Semantics says what an operation is allowed to do and what it promises:
	// its category, whether inference is permitted, whether evidence is
	// required, whether it preserves identity and order, and its stability
	// tier.
	Semantics = types.Semantics

	// Category buckets operations into the stable families.
	Category = types.Category

	// StabilityTier is the compatibility promise attached to an operation.
	StabilityTier = types.StabilityTier

	// BatchClass says how — or whether — an operation batches.
	BatchClass = types.BatchClass

	// DefaultPolicy is the operation's own default mode, tier, and repair
	// budget, before a caller's options are applied over it.
	DefaultPolicy = types.DefaultPolicy

	// OutputContract is what the answer has to satisfy: the schema, the
	// decoder, the invariants, and any normalization.
	OutputContract[Out any] = ops.OutputContract[Out]

	// BatchAlgebra is how an operation splits, merges, and validates a batch.
	BatchAlgebra[In, Out any] = ops.BatchAlgebra[In, Out]

	// Op is the descriptor itself.
	Op[In, Out any] = ops.Op[In, Out]

	// RepairResult reports what the repair loop did.
	RepairResult = ops.RepairResult
)

const (
	CategoryUnspecified    = types.CategoryUnspecified
	CategoryExtraction     = types.CategoryExtraction
	CategoryTransformation = types.CategoryTransformation
	CategoryGeneration     = types.CategoryGeneration
	CategorySelection      = types.CategorySelection
	CategoryJudgment       = types.CategoryJudgment

	StabilityUnspecified  = types.StabilityUnspecified
	StabilityExperimental = types.StabilityExperimental
	StabilityStable       = types.StabilityStable

	BatchUnspecified = types.BatchUnspecified
	BatchNone        = types.BatchNone
	BatchItemwise    = types.BatchItemwise
	BatchAggregate   = types.BatchAggregate
)

// NewOp validates a descriptor and returns it. A stable operation that has not
// declared how it batches is rejected: "stable" is a promise about behaviour a
// caller can rely on, and how an operation behaves in a batch is part of that.
func NewOp[In, Out any](op Op[In, Out]) (Op[In, Out], error) {
	return ops.NewOp(op)
}

// MustNewOp is NewOp for a descriptor built at package initialisation, where
// there is no caller to return an error to. It panics, which is what makes the
// batch-class rule a build-time check in practice: a binary whose operation
// descriptors are wrong fails to start rather than failing later on one code
// path.
func MustNewOp[In, Out any](op Op[In, Out]) Op[In, Out] {
	return ops.MustNewOp(op)
}

// RunOp executes an operation described by an Op.
func RunOp[In, Out any](ctx context.Context, op Op[In, Out], input In, opt OpOptions) (Out, RepairResult, error) {
	return ops.RunOp(ctx, op, input, opt)
}
