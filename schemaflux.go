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

// Result wraps an operation result with metadata.
type Result[T any] struct {
	Value T // The actual result value
	// ModelConfidence is the model's own claim about this result, not a measurement.
	// It is not calibrated and is not comparable across models or prompts.
	ModelConfidence float64        // ModelConfidence score (0.0-1.0)
	Error           error          // Any error that occurred
	Metadata        map[string]any // Additional metadata
}

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
	ClassifyResult[C any]      = ops.ClassifyResult[C]
	ClassifyAlternative[C any] = ops.ClassifyAlternative[C]
	ScoreResult                = ops.ScoreResult
	CompareResult[T any]       = ops.CompareResult[T]
	ComparisonPoint            = ops.ComparisonPoint
	SimilarResult              = ops.SimilarResult
	AspectMatch                = ops.AspectMatch

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
	SummarizeResult        = ops.SummarizeResult
	RewriteResult          = ops.RewriteResult
	TranslateResult        = ops.TranslateResult
	TranslationAlternative = ops.TranslationAlternative
	ExpandResult           = ops.ExpandResult

	// Extended operation result types with metadata
	FormatResult       = ops.FormatResult
	MergeResult[T any] = ops.MergeResult[T]
	MergeConflict      = ops.MergeConflict
)

// Mode constants
const (
	// Strict enforces exact schema matching and validation.
	Strict = types.Strict

	// TransformMode enables semantic mapping between related concepts.
	TransformMode = types.TransformMode

	// Creative allows open-ended generation and interpretation.
	Creative = types.Creative
)

// Speed constants (Intelligence levels)
const (
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
func Classify[T any, C any](input T, opts ClassifyOptions) (ClassifyResult[C], error) {
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
func SummarizeWithMetadata(input string, opts SummarizeOptions) (SummarizeResult, error) {
	return ops.SummarizeWithMetadata(input, opts)
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
func RewriteWithMetadata(input string, opts RewriteOptions) (RewriteResult, error) {
	return ops.RewriteWithMetadata(input, opts)
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
func TranslateWithMetadata(input string, opts TranslateOptions) (TranslateResult, error) {
	return ops.TranslateWithMetadata(input, opts)
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
func ExpandWithMetadata(input string, opts ExpandOptions) (ExpandResult, error) {
	return ops.ExpandWithMetadata(input, opts)
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
	return ops.Complete(context.Background(), nil, partialText, opts)
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
	return ops.CompleteField[T](context.Background(), nil, data, opts)
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
func Validate[T any](data T, opts ValidateOptions) (ValidateResult[T], error) {
	return ops.Validate(data, opts)
}

// ValidateLegacy is the legacy validation function for backward compatibility.
//
// Example:
//
//	result, err := schemaflux.ValidateLegacy(person, "age must be 18-100")
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
func Negotiate[T any](constraints any, opts ...NegotiateOptions) (NegotiateResult[T], error) {
	return ops.Negotiate[T](constraints, opts...)
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
func NegotiateAdversarial[T any](context AdversarialContext[T], opts ...AdversarialOptions) (AdversarialResult[T], error) {
	return ops.NegotiateAdversarial[T](context, opts...)
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
