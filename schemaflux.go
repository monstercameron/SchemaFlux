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

	"github.com/monstercameron/schemaflux/internal/api/client"
	builder "github.com/monstercameron/schemaflux/internal/api/fluent"
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

	// TypedDecision is what an operation is deciding, typed: the subject and the
	// questions about it. The bounded operations (Is, Which, WhichOrNone, Rate,
	// Assess, Whether, RouteBy, Classify, Choose) fill CompletionRequest.Decision
	// with it, beside the prompts. A provider for a decision API reads it and
	// answers in CompletionResponse.DecisionAnswers; a generating provider does
	// not look at it. These names are exported so that such a provider can be
	// written outside this module: implementing Provider and reading the
	// decision off the request is all an adapter needs. (Decision is the result
	// of the Decide operation, which is why this is not called that.)
	TypedDecision = llm.Decision

	// DecisionQuestion is one question of a TypedDecision.
	DecisionQuestion = llm.DecisionQuestion

	// DecisionKind is the kind of answer a DecisionQuestion takes.
	DecisionKind = llm.DecisionKind

	// DecisionAnswer is a decision provider's answer to one question, with the
	// probability the model reported for it, which is the model's own claim.
	DecisionAnswer = llm.DecisionAnswer

	// DecisionLimits is what a decision provider declares it can take, with a
	// DecisionLimits method. Its Check refuses, with the classified error, a
	// request that carries no decision and a decision past the limits.
	DecisionLimits = llm.DecisionLimits

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

// GenerateResult runs Generate and returns the envelope alongside the value:
// what the call cost, how many attempts it took, how long it waited, and the
// request and correlation ids it carried, which are reported on a refusal that
// never reached a provider as well as on an answer. Meta.DeliveredContract is
// schema-constrained for a structured type and prompt-only for a string, which
// is the model's text as it came.
func GenerateResult[T any](prompt string, opts GenerateOptions) (Result[T], error) {
	return ops.GenerateResult[T](prompt, opts)
}

// RedactLLMWithMeta runs RedactLLM and returns the envelope alongside the
// redaction. It is not RedactLLMResult, which is the name of the redaction's own
// type. Like RedactLLM it takes its context from opts.Context.
func RedactLLMWithMeta(text string, opts RedactLLMOptions) (Result[RedactLLMResult], error) {
	return ops.RedactLLMWithMeta(opts.Context, text, opts)
}

// AuditWithMeta runs AuditWithModel and returns the envelope alongside the
// judgment, so an application can see what an audit cost and how long it
// waited. It is not AuditResult, which is the name of the audit's own type.
func AuditWithMeta[T any](data T, opts ...AuditOptions) (Result[JudgmentResult[T]], error) {
	return ops.AuditWithMeta(data, opts...)
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

// Effort is how much the model reasons before it answers, set per call with
// OpOptions.Effort or a WithEffort method.
//
// It is the largest single lever on latency. On gpt-6-luna, with reasoning off,
// judgments scored as they do at the default and ran about a third faster, and
// arithmetic failed nearly half the time. So EffortNone suits a choice, a match
// or a yes or no, and anything that computes wants EffortLow or more.
//
// Classify, Similar and Deduplicate prefer EffortNone when a caller sets
// nothing, under two conditions. The model is gpt-6-luna, the one it was
// measured on. And nothing supplied to the call mentions a number: no digit or
// other numeral, and no English word for a quantity, in the input or in the
// categories, descriptions, examples, aspects or steering. A number written
// out in another language is not seen, so a caller with such input sets the
// effort. Every other operation, and these three on any other model, leave
// the effort to the provider.
//
// A value the model does not take is refused before the request is sent.
type Effort = types.Effort

// Timing says where the time of a call to a model went: the wait for a slot in
// the client's scheduler, the connection, the wait for the provider, and the
// reading of the answer. Result.Meta.Timing carries it for the operations that
// return a Result, and CompletionResponse.Timing for one call to a provider.
type Timing = types.Timing

const (
	// EffortDefault leaves the effort to the operation and the provider.
	EffortDefault = types.EffortDefault
	// EffortNone turns reasoning off: fastest, and not for arithmetic.
	EffortNone    = types.EffortNone
	EffortMinimal = types.EffortMinimal
	EffortLow     = types.EffortLow
	EffortMedium  = types.EffortMedium
	EffortHigh    = types.EffortHigh
	EffortXHigh   = types.EffortXHigh
	EffortMax     = types.EffortMax
)

// The kinds of question a Decision asks, which a provider for a decision API
// switches on.
const (
	// DecisionYesNo is answered yes or no.
	DecisionYesNo = llm.DecisionYesNo
	// DecisionChoice is answered with one of the question's Options.
	DecisionChoice = llm.DecisionChoice
	// DecisionScale is answered with a whole number from Low to High.
	DecisionScale = llm.DecisionScale
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
//	filtered, err := schemaflux.Filter(items, schemaflux.NewFilterOptions().WithCriteria("completed tasks"))
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
//	opts := schemaflux.NewRewriteOptions()
//	opts.TargetTone = "professional"
//	result, err := schemaflux.RewriteWithMetadata(text, opts)
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
//	opts := schemaflux.NewExpandOptions()
//	opts.ExpansionFactor = 3
//	expanded, err := schemaflux.Expand(briefText, opts)
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
//	redacted, err := schemaflux.Redact(sensitiveData, schemaflux.NewRedactOptions().WithCategories([]string{"PII"}))
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
//
// Span offsets are in characters, not bytes: the span is []rune(text)[Start:End].
// On any error the result carries no text.
//
// The context is opts.Context. This function, Complete and CompleteField used to
// pass context.Background() instead, so a caller's cancellation and deadline
// reached nothing, and so did the client: Client.Context puts the client's
// provider on the context, and without it the call went to whichever client had
// been built last (LX-061). A nil context still means "no deadline of the
// caller's", and the operation applies its own default.
func RedactLLM(text string, opts RedactLLMOptions) (RedactLLMResult, error) {
	return ops.RedactLLM(opts.Context, text, opts)
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
	return ops.Complete(opts.Context, partialText, opts)
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
	return ops.CompleteField[T](opts.Context, data, opts)
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

// DeduplicateResult holds the unique items, the groups that collapsed into
// them, how many were removed, and where each sat in the input.
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
// The threshold is a floor between 0 and 1 on a fixed scale, where 1.0 is the
// same item, 0.9 the same item written differently, and 0.7 the same item with
// one detail in disagreement. The model reports how close each match is and the
// threshold is applied in Go, so a value outside 0 to 1 is refused.
//
// It is one model call for the whole list, so the list has to fit in the
// model's context. PS-006 tracks doing it with embeddings, which is the right
// shape for a long list.
//
// The result maps back to the input: UniqueIndices and DuplicateIndices give
// positions in items. An answer that cannot be read, or that names an item the
// list does not have, is an error and never "no duplicates" (LX-021).
func Deduplicate[T any](items []T, threshold float64, opts ...OpOptions) (DeduplicateResult[T], error) {
	return ops.Deduplicate(items, threshold, opts...)
}

// Deprecated: use ValidateHybrid. ValidationResult is a bool, a []string, and
// a model-reported confidence; JudgmentResult says which field failed, how
// badly, and offers a correction. It named Validate before, which is itself
// deprecated now -- a deprecation pointing at a deprecation sends a caller on
// two migrations instead of one.
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
//
// Deprecated: use Merge, which returns the same result. This spelling carries
// a Metadata map beside the value, where a measurement and a model's claim are
// indistinguishable. See OP-401 for why that shape is being retired.
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
//
// Deprecated: use Format. Same operation; this spelling carries the Metadata
// map that OP-401 is retiring.
func FormatWithMetadata(data any, template string, opts ...OpOptions) (FormatResult, error) {
	return ops.FormatWithMetadata(data, template, opts...)
}

// Decide chooses among decisions, first by their programmatic conditions and
// then by asking the model. situation is the prompt data describing the
// circumstances; ctx governs cancellation.
//
// Without a fallback, a provider failure or an unusable answer is an error,
// and an answer that selects nothing is unusable: it is never read as the
// first branch. Pass NewDecideOptions().WithFallback(i) to select a branch
// instead, which sets Fallback on the result so the caller can tell a real
// decision from a default one. The fallback covers a provider that is
// unavailable, rate limited or timed out, and an answer that could not be
// used. It never covers a budget that is spent, a data policy that refuses
// the call, a cancelled context or a configuration error: those are returned.
// A branch taken because its own Condition held sets ByCondition, and carries
// no model confidence, since no model was asked.
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
//	result, err := schemaflux.Annotate(document, schemaflux.NewAnnotateOptions().WithAnnotationTypes([]string{"entities", "sentiment"}))
func Annotate[T any](input T, opts AnnotateOptions) (AnnotateResult, error) {
	return ops.Annotate(input, opts)
}

// Cluster groups items by semantic similarity into natural clusters.
//
// Example:
//
//	result, err := schemaflux.Cluster(documents, schemaflux.NewClusterOptions().WithNumClusters(5))
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
//	result, err := schemaflux.Compress(document, schemaflux.NewCompressOptions().WithCompressionRatio(0.3))
func Compress[T any](input T, opts CompressOptions) (CompressResult[T], error) {
	return ops.Compress(input, opts)
}

// CompressText compresses text content using semantic compression.
//
// Example:
//
//	compressed, err := schemaflux.CompressText("long text here...", schemaflux.NewCompressOptions().WithCompressionRatio(0.5))
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
//	result, err := schemaflux.Synthesize[Summary](articles, schemaflux.NewSynthesizeOptions().WithStrategy("integrate"))
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

// Verify checks facts, logic, and consistency in content. The model reports
// what it finds, and OverallVerdict is worked out from that by this library:
// "verified", "false", "mixed" or "unverifiable".
//
// Example:
//
//	result, err := schemaflux.Verify("The Earth is flat.", schemaflux.NewVerifyOptions().WithCheckLogic(false))
//
// Deprecated: use VerifyWithModel, whose name says that this review is
// model-assisted rather than a deterministic check, and which returns the
// shared JudgmentResult instead of this operation's own shape. See OP-206.
func Verify(input string, opts VerifyOptions) (VerifyResult, error) {
	return ops.Verify(input, opts)
}

// VerifyClaim checks a specific claim. When the model finds several claims in
// it, the verdict is the worst of them. When the fact check is on and the
// model assesses no claim at all, it is an error and not a verdict.
//
// Example:
//
//	result, err := schemaflux.VerifyClaim("GDP grew 5%", schemaflux.NewVerifyOptions())
//
// Deprecated: use VerifyWithModel, whose name says the findings are a model's
// review rather than a deterministic check, and which returns the shared
// JudgmentResult. See OP-206.
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

// Arbitrate picks one of several options by rules, and returns how every
// option did on every rule.
//
// The model evaluates; this library decides. The model says, for each option
// and each rule, whether the rule is met and how well. The winner is worked
// out here: the caller's Weights are applied, an option that fails a rule is
// disqualified when RequireAllRules is set, the highest total wins, and a tie
// is broken by Tiebreaker. So the weights are exact, and the same evaluations
// always give the same winner (unless the tiebreaker is "random").
//
// When RequireAllRules is set and no option meets every rule, there is no
// winner and the error is ErrReviewRequired. An answer that leaves out an
// option or a rule is an error, never a default. With one option and
// RequireAllRules unset, that option wins without a model call, and the
// result carries no scores, since none were given.
//
// Type parameter T specifies the type of options to choose from.
//
// Example:
//
//	result, err := schemaflux.Arbitrate(candidates, schemaflux.ArbitrateOptions{
//	    Rules:   []string{"has three or more years of experience", "is local"},
//	    Weights: []float64{2, 1},
//	})
//	if errors.Is(err, schemaflux.ErrReviewRequired) {
//	    // every candidate failed a required rule
//	}
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

// Judged is an answer together with the model's own confidence in it, which is
// a claim and not a measurement. The Result forms of the decisions return it.
type Judged[T any] = ops.Judged[T]

// DecisionRecord is one decision, after it was made: what was asked and what was
// read, handed to the function registered with Client.WithDecisionObserver. It
// holds the caller's questions and the model's answers, and goes nowhere else.
type DecisionRecord = ops.DecisionRecord

// AnsweredQuestion is one question of a DecisionRecord and the answer read for
// it: a bool, the label chosen, or the level, and nil when the decision failed.
type AnsweredQuestion = ops.AnsweredQuestion

// Picked is the answer of WhichOrNoneResult: the label chosen and whether one
// was, which is false when none of the labels fits.
type Picked = ops.Picked

// Is answers a yes-or-no question about a subject. The subject is any value
// that can be written as JSON; text is the usual one.
//
//	spam, err := schemaflux.Is(ctx, message, "Is this message spam?")
//
// An answer that is not a plain true or false is an error, never a "no".
func Is(ctx context.Context, subject any, question string, opts ...OpOptions) (bool, error) {
	return ops.Is(ctx, subject, question, opts...)
}

// IsResult is Is with the model's confidence and the execution envelope.
func IsResult(ctx context.Context, subject any, question string, opts ...OpOptions) (Result[Judged[bool]], error) {
	return ops.IsResult(ctx, subject, question, opts...)
}

// Which answers a question by choosing one of the labels, and returns that
// label as it was given.
//
//	team, err := schemaflux.Which(ctx, message, "Which team should handle this?",
//	    []string{"billing", "technical", "shipping"})
//
// The answer is always one of the labels: an answer that names none of them is
// an error, never the first label.
func Which(ctx context.Context, subject any, question string, labels []string, opts ...OpOptions) (string, error) {
	return ops.Which(ctx, subject, question, labels, opts...)
}

// WhichResult is Which with the model's confidence and the execution envelope.
func WhichResult(ctx context.Context, subject any, question string, labels []string, opts ...OpOptions) (Result[Judged[string]], error) {
	return ops.WhichResult(ctx, subject, question, labels, opts...)
}

// WhichOrNone is Which with "none of these" as an answer the model can give.
// It returns the label and true, or "" and false. With no labels to choose
// from the answer is none, and nothing is asked.
func WhichOrNone(ctx context.Context, subject any, question string, labels []string, opts ...OpOptions) (string, bool, error) {
	return ops.WhichOrNone(ctx, subject, question, labels, opts...)
}

// WhichOrNoneResult is WhichOrNone with the model's confidence and the
// execution envelope: the answer is a Picked, the label and whether one was
// found.
func WhichOrNoneResult(ctx context.Context, subject any, question string, labels []string, opts ...OpOptions) (Result[Judged[Picked]], error) {
	return ops.WhichOrNoneResult(ctx, subject, question, labels, opts...)
}

// Rate answers a question with a whole number on a scale, both ends included.
//
//	urgency, err := schemaflux.Rate(ctx, message, "How urgent is this?", 1, 5)
//
// An answer off the scale is an error, never the nearest end of it.
func Rate(ctx context.Context, subject any, question string, low, high int, opts ...OpOptions) (int, error) {
	return ops.Rate(ctx, subject, question, low, high, opts...)
}

// RateResult is Rate with the model's confidence and the execution envelope.
func RateResult(ctx context.Context, subject any, question string, low, high int, opts ...OpOptions) (Result[Judged[int]], error) {
	return ops.RateResult(ctx, subject, question, low, high, opts...)
}

// Assess answers several questions about one subject in one call, and returns
// them as a struct. Each exported field of T asks a question in its `ask` tag,
// and the field's type says what kind of answer it takes: a bool is a yes or
// no; a string with a `oneof` tag chooses among the labels in it, separated by
// commas; an integer with a `scale` tag, "1-5", is a rating.
//
//	type Triage struct {
//	    Spam    bool   `ask:"Is this message spam?"`
//	    Team    string `ask:"Which team should handle it?" oneof:"billing,technical,shipping"`
//	    Urgency int    `ask:"How urgent is it?" scale:"1-5"`
//	}
//	triage, err := schemaflux.Assess[Triage](ctx, message)
//
// It is one call to the provider where Is, Which and Rate would be three, so a
// program that needs several judgments of the same subject waits once. Every
// question has to be answered: an answer that leaves one out is an error and
// no field is filled.
func Assess[T any](ctx context.Context, subject any, opts ...OpOptions) (T, error) {
	return ops.Assess[T](ctx, subject, opts...)
}

// AssessResult is Assess with the model's confidence in its answers taken
// together and the execution envelope.
func AssessResult[T any](ctx context.Context, subject any, opts ...OpOptions) (Result[Judged[T]], error) {
	return ops.AssessResult[T](ctx, subject, opts...)
}

// Branch runs then when yes is true and otherwise when it is not. It is an
// if-else whose condition was decided somewhere else, by Is or as a field of
// what Assess returned. A nil step returns the zero value.
func Branch[T any](ctx context.Context, yes bool, then, otherwise Step[T]) (T, error) {
	return ops.Branch(ctx, yes, then, otherwise)
}

// Whether asks a yes-or-no question about a subject and runs then when the
// answer is yes and otherwise when it is no. When the question cannot be
// answered neither runs: a failure is never read as a no.
//
//	reply, err := schemaflux.Whether(ctx, message, "Is this message spam?", discard, handle)
func Whether[T any](ctx context.Context, subject any, question string, then, otherwise Step[T], opts ...OpOptions) (T, error) {
	return ops.Whether(ctx, subject, question, then, otherwise, opts...)
}

// Route is one way a program can go: a label, and the step that is run when
// the label is the one chosen.
type Route[T any] = ops.Route[T]

// To builds a Route.
func To[T any](label string, run Step[T]) Route[T] {
	return ops.To(label, run)
}

// Select runs the route whose label is the one given. It is a switch whose
// value was decided somewhere else, by Which or as a field of what Assess
// returned. A label that is none of the routes' is an error.
func Select[T any](ctx context.Context, label string, routes ...Route[T]) (T, error) {
	return ops.Select(ctx, label, routes...)
}

// RouteBy asks which of the routes' labels answers a question about a subject
// and runs that route. When the question cannot be answered no route runs: a
// failure is never read as the first route.
//
//	reply, err := schemaflux.RouteBy(ctx, message, "Which team should handle this?",
//	    []schemaflux.Route[Reply]{
//	        schemaflux.To("billing", handleBilling),
//	        schemaflux.To("technical", handleTechnical),
//	    })
func RouteBy[T any](ctx context.Context, subject any, question string, routes []Route[T], opts ...OpOptions) (T, error) {
	return ops.RouteBy(ctx, subject, question, routes, opts...)
}

// Gather runs steps that do not depend on each other at once and returns their
// results in the order the steps were given: a program that needs three
// answers waits as long as the slowest takes, where one after another it waits
// for all three. The steps run under the caller's context, so a client's
// scheduler and budget still apply. The first step to fail cancels the others
// and its error is returned; no results are returned with it.
func Gather[T any](ctx context.Context, steps ...Step[T]) ([]T, error) {
	return ops.Gather(ctx, steps...)
}

// Gather2 is Gather for two steps whose results are of different types.
func Gather2[A, B any](ctx context.Context, first Step[A], second Step[B]) (A, B, error) {
	return ops.Gather2(ctx, first, second)
}

// Gather3 is Gather for three steps whose results are of different types.
func Gather3[A, B, C any](ctx context.Context, first Step[A], second Step[B], third Step[C]) (A, B, C, error) {
	return ops.Gather3(ctx, first, second, third)
}

// Gather4 is Gather for four steps whose results are of different types.
func Gather4[A, B, C, D any](ctx context.Context, first Step[A], second Step[B], third Step[C], fourth Step[D]) (A, B, C, D, error) {
	return ops.Gather4(ctx, first, second, third, fourth)
}

// Each runs a step for every item of a list, several at once, and returns the
// results in the order of the items. The first step to fail stops the rest and
// its error is returned; no results are returned with it.
func Each[T, R any](ctx context.Context, items []T, step func(context.Context, T) (R, error)) ([]R, error) {
	return ops.Each(ctx, items, step)
}

// BranchAhead decides and starts yes at once, and runs no only when the
// decision says no. A yes costs one wait where deciding and then calling Branch
// costs two; a no cancels yes and discards what it returned or failed with.
// This is speculation, and it is off unless asked for by calling this: a branch
// that is cancelled may already have been sent to the provider and is billed by
// the provider, so use it for a branch that is taken most of the time. A
// decision that fails returns its own error, and no goroutine outlives the
// call; a step that ignores its context holds the call until it returns.
func BranchAhead[T any](ctx context.Context, decide Step[bool], yes, no Step[T]) (T, error) {
	return ops.BranchAhead(ctx, decide, yes, no)
}

// BranchBoth is BranchAhead for two branches that are about as likely: both
// start at once with the decision, the one the decision picks is kept and the
// other is cancelled. It costs both branches every time, and buys one wait in
// both directions.
func BranchBoth[T any](ctx context.Context, decide Step[bool], yes, no Step[T]) (T, error) {
	return ops.BranchBoth(ctx, decide, yes, no)
}

// RouteAhead decides which route to take and starts the route labelled likely
// at once. When the decision names it, its result is kept; when it names
// another route, the likely one is cancelled and the named one runs. A likely
// that is no route's label, a label given twice and a nil step are
// configuration errors found before anything starts. Like BranchAhead it costs
// money whenever the guess is wrong, since the route that is cancelled may
// already have been billed.
func RouteAhead[T any](ctx context.Context, decide Step[string], likely string, routes ...Route[T]) (T, error) {
	return ops.RouteAhead(ctx, decide, likely, routes...)
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

// VerifyWithModel checks content with a model: its claims, its reasoning and
// its consistency, each of which can be switched off. The findings are the
// model's, which is what the name says. The verdict is not: it is worked out
// here from the findings, as the worst of the checks that were run, so Pass
// means every claim was verified and nothing else was found. A check that is
// on and that the model did not report on is an error, not a pass.
func VerifyWithModel(input string, opts VerifyOptions) (JudgmentResult[any], error) {
	return ops.VerifyWithModel(input, opts)
}

// AuditWithModel inspects a value with a model against the policies and
// categories the caller names. The findings are the model's. The verdict is
// worked out here: it fails when a policy the caller supplied is violated,
// whatever severity the model gave the violation, or when a problem in a
// category the caller named is serious (a severity of 0.7 or above).
//
// Each issue's Rule is the policy it violates, in the caller's own words. With
// policies supplied and no categories, a problem that is no policy's is in
// ModelClaims["other_findings"] and does not count: an audit of three policies
// does not fail on something outside all three.
//
// AuditOptions.Threshold is the lowest severity to list. It does not change
// the verdict. An answer that does not report on every policy is an error and
// not a pass.
//
//	result, err := schemaflux.AuditWithModel(customer, schemaflux.AuditOptions{
//	    Policies: []string{"Passwords must not be stored in plain text"},
//	})
//	if err == nil && result.Verdict == schemaflux.VerdictFail {
//	    for _, issue := range result.Issues {
//	        fmt.Println(issue.Rule, issue.Subject, issue.Message)
//	    }
//	}
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

// --- Diagnostics (A-011).
//
// Errors from this library never carry the caller's payload. That is correct
// and it leaves debugging with nothing, which is why there is a second
// channel: a sink the caller provides, off unless they do, bounded, scrubbed
// of credentials, and referenced from the ordinary error by an ID and a digest
// rather than by content.
type (
	// DiagnosticSink receives captured bodies. Implementations are called
	// inline with the failing call, so a slow sink slows the caller — hand off
	// to a queue rather than writing to a network from here.
	DiagnosticSink = types.DiagnosticSink

	// DiagnosticRecord is one captured body, already truncated and redacted.
	DiagnosticRecord = types.DiagnosticRecord

	// DiagnosticRef is what the error carries: an ID and a content digest,
	// both safe to print.
	DiagnosticRef = types.DiagnosticRef

	// DiagnosticPolicy bounds what is captured and for how long.
	DiagnosticPolicy = types.DiagnosticPolicy
)

// WithDiagnosticSink turns capture on for operations run with the returned
// context. Without it nothing is captured, which is the default.
func WithDiagnosticSink(ctx context.Context, sink DiagnosticSink, policy DiagnosticPolicy) context.Context {
	return ops.WithDiagnosticSink(ctx, sink, policy)
}

// RunOpResult runs an operation described by an Op and returns the envelope,
// including the repairs a caller's own invariants caused (A-009).
func RunOpResult[In, Out any](ctx context.Context, op Op[In, Out], input In, opt OpOptions) (Result[Out], error) {
	return ops.RunOpResult(ctx, op, input, opt)
}

// --- Planning and execution shapes (M11).
//
// A plan is what the library intends to do, produced without doing any of it:
// eligibility, chunking, the maximum number of calls, the budget, and the
// estimated cost. Preflight makes no provider call, which is the property that
// makes it worth having — a preflight that spends money to tell you what
// something will cost is a call, not a plan.
type (
	// ExecutionShape is how an operation runs over its input: one call per
	// item, one call for many items, many calls for one item, or one call over
	// everything.
	ExecutionShape = types.ExecutionShape

	// PlanRequest is the input to Preflight.
	PlanRequest = types.PlanRequest

	// Plan is the immutable result. It serializes without any of the caller's
	// values — shapes, counts, and identifiers only — so it can be logged,
	// stored, and diffed.
	Plan = types.Plan

	// ChunkPlan is one planned call's share of the work.
	ChunkPlan = types.ChunkPlan

	// OversizedItem is an item no chunk can hold, named with the bound it
	// broke rather than silently truncated.
	OversizedItem = types.OversizedItem

	// CostEstimate is a plan's projected cost. It reports unpriced honestly
	// rather than estimating zero for a model with no rate card.
	CostEstimate = types.CostEstimate

	// PolicySnapshot and CapabilitySnapshot are the inputs a plan is
	// deterministic with respect to.
	PolicySnapshot     = types.PolicySnapshot
	CapabilitySnapshot = types.CapabilitySnapshot

	// AlgebraKind is what a batched operation's answer has to satisfy as a
	// whole: independent, a subset, a permutation, a partition, and so on.
	AlgebraKind = types.AlgebraKind

	// BatchCoverage reports how a batched response measured up against the ids
	// it was given: which were missing, duplicated, or unknown.
	BatchCoverage = ops.BatchCoverage

	// PlanBuilder forces a shape. Forcing atomicity for risk reasons and
	// forcing batching for cost reasons are both legitimate, so both are
	// expressible.
	PlanBuilder = ops.PlanBuilder
)

const (
	ShapeUnspecified = types.ShapeUnspecified
	ShapeAtomic      = types.ShapeAtomic
	ShapeMDSP        = types.ShapeMDSP
	ShapeSDMP        = types.ShapeSDMP
	ShapeGlobal      = types.ShapeGlobal

	AlgebraIndependent  = types.AlgebraIndependent
	AlgebraSubset       = types.AlgebraSubset
	AlgebraPermutation  = types.AlgebraPermutation
	AlgebraPartition    = types.AlgebraPartition
	AlgebraGraph        = types.AlgebraGraph
	AlgebraHierarchical = types.AlgebraHierarchical
	AlgebraSequential   = types.AlgebraSequential
)

// Preflight returns the plan for running op over items, without running any of
// it and without calling a provider.
func Preflight[In, Out any](op Op[In, Out], items []In, req PlanRequest) (Plan, error) {
	return ops.Preflight(op, items, req)
}

// StagedPreflight is the plan for a staged job and what it holds for each stage:
// what will be asked, in how many requests, at what estimated cost, and whether the
// estimate is an upper bound because the stage's input does not exist yet.
type StagedPreflight = ops.StagedPreflight

// PreflightStagedPlan returns the plan for taking datum through the stages of plan,
// without running any of it and without calling a provider, for the client ctx
// carries. Each stage is priced by the estimator Preflight uses, from what its
// operation renders for the datum; a stage after the first is priced from the datum
// too, as an upper bound, and says so. A stage that cannot be rendered makes the
// plan ineligible, with the reason and its kind. A nil context or a plan with no
// stages is an invalid request.
func PreflightStagedPlan[T any](ctx context.Context, plan StagedPlan[T], datum T, req PlanRequest) (StagedPreflight, error) {
	return ops.PreflightStagedPlan(ctx, plan, datum, req)
}

// NewPlanBuilder starts a plan whose shape the caller chooses.
func NewPlanBuilder(opt OpOptions) *PlanBuilder {
	return ops.NewPlanBuilder(opt)
}

// RunOpBatch runs one operation over many items in a single call, using the id
// protocol: items go out tagged with ids made for this request, the answer names
// ids, and coverage is checked in Go. Output position is never trusted: an answer
// belongs to the item whose id it carries, and an element with an id that was not
// offered answers nothing. A model that writes one item's answer under another
// item's id cannot be caught; send one request per item (RunOpManyPartial) when that
// matters.
func RunOpBatch[In, Out any](ctx context.Context, op Op[In, Out], items []In, opt OpOptions) ([]Out, error) {
	return ops.RunOpBatch(ctx, op, items, opt)
}

// RunOpBatchResult is RunOpBatch with the envelope.
func RunOpBatchResult[In, Out any](ctx context.Context, op Op[In, Out], items []In, opt OpOptions) (Result[[]Out], error) {
	return ops.RunOpBatchResult(ctx, op, items, opt)
}

// RunOpMany plans and then executes, returning one result per item alongside
// the plan that produced them.
func RunOpMany[In, Out any](ctx context.Context, op Op[In, Out], items []In, req PlanRequest) ([]Result[Out], Plan, error) {
	return ops.RunOpMany(ctx, op, items, req)
}

// --- Partial and recovering runs.
//
// RunOpMany fails the whole call on one bad item or one bad chunk. These two
// entry points are for a caller who wants the items that worked, and a record
// of what happened to each of the others: a BatchResult has one ItemResult per
// input, in input order, and its only route to a plain slice (Values) hands back
// whether every item succeeded beside it, so a short slice cannot be mistaken
// for a complete one.
//
// Read the items, not the error: a run that collects its failures returns a nil
// error even when every item failed.
//
//	result, err := schemaflux.RunOpManyPartial(ctx, op, items, opt, cfg)
//	if err != nil { return err }
//	for _, item := range result.Failures() { log(item.Index, item.Err) }
type (
	// BatchResult is a plural run's honest result: the status, value, attempts,
	// repairs, cost and error of every item, a summary of the run, Total, what the
	// whole run cost as measured (the figure to reconcile a bill against, where an
	// item's Cost is an attribution), and Ledger, the decisions an adaptive run made.
	BatchResult[Out any] = types.BatchResult[Out]

	// ItemResult is one input's outcome from a plural run. Its Value is meaningful
	// only when Status is ItemSucceeded.
	ItemResult[Out any] = types.ItemResult[Out]

	// ItemStatus is one item's terminal state: succeeded, failed, or cancelled.
	ItemStatus = types.ItemStatus

	// BatchSummary counts a plural run's items by status and records the policy
	// that ran.
	BatchSummary = types.BatchSummary

	// BatchPolicy is what a partial run does when an item fails: stop, collect,
	// retry, or require all.
	BatchPolicy = types.BatchPolicy

	// BatchMetrics is a BatchResult's per-item measurement: the valid-item
	// ratio, the cost per accepted item, and how many items needed an isolate
	// pass or an atomic fallback. It is computed by BatchResult.Metrics.
	BatchMetrics = types.BatchMetrics

	// DecisionLedger is the ordered record of the adaptive decisions a
	// recovering run made, each with the reason it was made.
	DecisionLedger = types.DecisionLedger

	// DecisionEntry is one adaptive decision in a DecisionLedger.
	DecisionEntry = types.DecisionEntry

	// PartialConfig configures RunOpManyPartial: the policy, how many items run
	// at once (zero is the client scheduler's limit, else 4), and how many times a
	// failed item is retried (at most five rounds). A retry multiplies what an item
	// costs: an item can make up to (1+MaxRetries) x (RepairAttempts+1) x (Client
	// retries+1) provider requests.
	PartialConfig = ops.PartialConfig

	// RecoverConfig configures RunOpManyRecover: the chunk sizes, the number of
	// isolate passes (at most three) and how many atomic calls run at once. The
	// most requests a run sends for n items is 2n + 2k x floor(n/2) for k isolate
	// passes, 3n at the default of one.
	RecoverConfig = ops.RecoverConfig

	// BatchItemCodec reads one item's answer out of a batched response. The
	// recovering run needs it beside the operation, because it decodes the items
	// that were answered correctly out of a response whose other items were not.
	BatchItemCodec[In, Out any] = ops.BatchItemCodec[In, Out]
)

const (
	ItemUnspecified = types.ItemUnspecified
	ItemSucceeded   = types.ItemSucceeded
	ItemFailed      = types.ItemFailed
	ItemCancelled   = types.ItemCancelled

	PolicyUnspecified      = types.PolicyUnspecified
	PolicyFailFast         = types.PolicyFailFast
	PolicyCollectFailures  = types.PolicyCollectFailures
	PolicyRetryFailedItems = types.PolicyRetryFailedItems
	PolicyRetryThenCollect = types.PolicyRetryThenCollect
	PolicyRequireAll       = types.PolicyRequireAll

	// ModeAtomicFallback is the ItemResult.Mode of an item that no batched
	// pass resolved and that was answered by a call of its own. ModeMDSP is the
	// mode of an item answered from a batched response. An item sent alone from the
	// start, by RunOpManyPartial or because it is too large to share a request, has
	// ShapeAtomic.String() as its mode.
	ModeAtomicFallback = types.ModeAtomicFallback
	ModeMDSP           = ops.ModeMDSP
)

// RunOpManyPartial runs op over items with one call per item, applying
// cfg.Policy, and returns a BatchResult naming what happened to every item. A
// policy left unspecified is PolicyCollectFailures, whatever the number of items,
// and the summary records it.
//
// A call that cannot be run is refused before anything is sent, with the empty
// result and an error: a nil context, a context that is already over, a descriptor
// that cannot be run, or a contract floor it cannot reach. Once requests have been
// sent, the error is non-nil exactly when the policy says a partial result fails
// the call: PolicyFailFast, PolicyRequireAll, and PolicyRetryFailedItems when
// retrying did not resolve every failure. It is always nil for
// PolicyCollectFailures and PolicyRetryThenCollect, whose result is read with
// BatchResult.Complete, and then it is nil even when every item failed. The
// BatchResult is returned beside the error, so an error never hides which items
// got through.
//
// A failed or cancelled item's Err is classified, and its text is the library's
// own (the operation, the kind and the item's place); what the failure was, a
// vendor's message or the caller's own error, is its cause, and not in the text. An
// item the run's context ended is cancelled, under every policy. Concurrency zero is
// the client scheduler's limit, or 4. A retry round multiplies the requests an item
// costs (see PartialConfig). Total is what every request cost, including a request
// whose answer the caller's own function panicked on.
//
// A function of the caller's that blocks and ignores the context it cannot see holds
// the call.
func RunOpManyPartial[In, Out any](ctx context.Context, op Op[In, Out], items []In, opt OpOptions, cfg PartialConfig) (BatchResult[Out], error) {
	return ops.RunOpManyPartial(ctx, op, items, opt, cfg)
}

// RunOpManyRecover runs op over items in batched calls and recovers from a
// response that answers some of its items wrongly: the items that were answered
// correctly are kept, only the others are asked again in a smaller batch, and
// whatever that does not resolve is asked one call per item. The size of each
// chunk follows how the one before it was answered, within what the planner cuts
// for the client's model. It returns a nil error whenever the cascade ran to its
// end, even when every item failed, and the BatchResult says which items
// resolved, how (ItemResult.Mode) and what the others failed with; a call that
// would fail the same way again, such as a refusal by the client's policy or a
// spent budget, or several requests in a row that fail in transport, stops the
// cascade and fails every item not yet resolved. A request the provider's own
// content filter refused is about some item, not the run: the request is split down
// to the item refused alone, which fails with a refusal. A context that ends leaves
// the items that finished as they were and cancels the others, with an error that
// matches context.Canceled or context.DeadlineExceeded; a context already over is
// refused before anything is sent.
//
// An answer is attributed to an item by the id its element carries and by nothing
// else; ids are random, made for each request, and an element whose id was not
// offered fills no item, so a response that drops an answer or adds one cannot move
// the others. What cannot be detected is a model that writes one item's answer under
// another item's id: nothing in Go can compare an answer with its question, so run
// one request per item (RunOpManyPartial) when that matters.
//
// The most requests it sends for n items is 2n + 2k x floor(n/2) for k isolate
// passes (RecoverConfig), 3n at the default of one, each of which the client may
// repeat and, for a request of one item, repair. The error of an item is classified
// with a text of the library's own. A function of the caller's that blocks and
// ignores the context it cannot see holds the call; a panic or runtime.Goexit in one
// fails that item. The client's budget can be overshot by the requests of the final
// rung that overlap.
//
// codec is the BatchItemCodec the operation's batch algebra was built from with
// NewIDBatchAlgebra, and has to be given again here: nothing checks that the two
// agree. The operation's batch has to be an itemwise one built by NewIDBatchAlgebra
// and must not require evidence; anything else is refused before a request is sent.
func RunOpManyRecover[In, Out any](ctx context.Context, op Op[In, Out], items []In, codec BatchItemCodec[In, Out], opt OpOptions, cfg RecoverConfig) (BatchResult[Out], error) {
	return ops.RunOpManyRecover(ctx, op, items, codec, opt, cfg)
}

// NewIDBatchAlgebra builds the batch algebra that tags every item with an id made
// for the request, asks for the answers by id and checks their coverage in Go,
// which is what RunOpBatch, RunOpMany and RunOpManyRecover expect of an operation
// that is batched. systemPrompt is the instruction the batch is sent with, and codec
// reads one item's answer; a codec with no DecodeItem is refused before a request
// is sent. An answer is attributed to an item by its id alone: a model that writes
// one item's answer under another item's id cannot be caught, so run one request
// per item when that matters.
func NewIDBatchAlgebra[In, Out any](class BatchClass, kind AlgebraKind, systemPrompt string, codec BatchItemCodec[In, Out]) BatchAlgebra[In, Out] {
	return ops.NewIDBatchAlgebra(class, kind, systemPrompt, codec)
}

// --- Staged plans.
//
// A staged plan runs several stages over one datum, one after another: the
// structured output of one stage is the input of the next, one set of options is
// shared by every stage, and a stage whose deterministic check already
// establishes what it requires is skipped without a call. The requests the
// stages make are bounded by the client's scheduler; the plan has none of its
// own and no groups of stages (Gather over RunOpResult runs operations at once).
type (
	// Stage is one step of a staged plan over a datum of type T: an operation, and
	// an optional deterministic check that can stand in for it.
	Stage[T any] = ops.Stage[T]

	// StagedPlan is an ordered list of stages over one datum.
	StagedPlan[T any] = ops.StagedPlan[T]

	// StageOutcome is what one stage produced: its value, whether it was skipped,
	// the contract level established and, for a stage that called its operation,
	// the envelope. For a stage that failed it holds the failure and the envelope.
	StageOutcome[T any] = ops.StageOutcome[T]

	// StagedPlanResult is what running a staged plan produced: the final datum
	// and one outcome per stage that ran, in the order the plan declared them.
	StagedPlanResult[T any] = ops.StagedPlanResult[T]
)

// RunStagedPlan runs plan's stages in order against input and returns the final
// datum with every stage's outcome. A stage that fails ends the plan with that
// failure, and the result beside it holds no value but the outcomes of the stages
// that ran, the failed one included, so what was spent is not lost; a stage that
// a deterministic check settled makes no provider call, and its outcome says it
// was skipped, and the level its check attests to is held to the client's contract
// floor: below it the stage is asked. opt is shared by every stage, except
// ParentResultIDs, which are the caller's followed by the previous stage's result.
//
// The error of a failed stage, and its StageOutcome.Err, is classified with a text
// of the library's own (the stage's name and "stage 2 of 3 failed"); what the stage
// failed with is the cause. A panic in a stage's BuildPrompt, Decode, invariant,
// Normalize or check is that stage's failure, of kind invariant violation, and the
// result keeps what the earlier stages cost; so is a runtime.Goexit, which the
// stage's functions are run where it can be caught. A call with no stages, a nil
// context or a context already over is refused before anything runs, as an invalid
// request. A function of the caller's that blocks and ignores the context it cannot
// see holds the call.
func RunStagedPlan[T any](ctx context.Context, plan StagedPlan[T], input T, opt OpOptions) (StagedPlanResult[T], error) {
	return ops.RunStagedPlan(ctx, plan, input, opt)
}

// --- Sessions.

// A conversation with a model that may ask for tools to be run. The zero Session
// is ready to use; set Instructions and Tools and call Send:
//
//	var s schemaflux.Session
//	s.Instructions = "You answer questions about weather. Use get_weather."
//	s.Tools = []schemaflux.Tool{weather}
//	reply, err := s.Send(ctx, "How warm is Oslo?")
//	for err == nil && len(reply.Value.ToolCalls) > 0 {
//	    results := map[string]string{}
//	    for _, call := range reply.Value.ToolCalls {
//	        results[call.ID] = lookup(call) // the caller runs the tool; the library never does
//	    }
//	    reply, err = s.SendResults(ctx, results)
//	}
//	fmt.Println(reply.Value.Content)
type (
	// Session is a conversation: its settings, its tools and its History, which
	// Send and SendResults check whole before every request and append to when a
	// turn succeeds. A turn that fails or is cancelled leaves History as it was. A
	// second turn begun while one is running is refused at once. It is not safe
	// for concurrent use otherwise, and is not copied after its first use.
	//
	// Each turn is one operation (chat.send or chat.sendresults) to the observers,
	// goes through the client's data policy, budget, scheduler, deadline and
	// retries as an operation's request does, and returns the Result of the turn:
	// the model's reply as the Value and what the turn cost and used as the Meta.
	// Session.Total is the sum of the turns.
	Session = ops.Session

	// Message is one turn of a conversation: a user message, the model's reply,
	// or the result of a tool. Its Role is "user", "assistant" or "tool"; the
	// system prompt is Session.Instructions and is not a message.
	Message = llm.Message

	// Tool declares one function a model may ask to have called. It is a
	// declaration and never an invocation: the library does not run tools.
	Tool = llm.Tool

	// ToolCall is a model's request to have one of the declared tools run. The
	// library has checked that Name is a declared tool, that ID is not empty and
	// not repeated, and that Arguments is a JSON object. The arguments are model
	// output, as untrusted as any, and are never written to an error.
	ToolCall = llm.ToolCall
)

// ErrSessionInvariant is matched by the error a Session returns for a history that
// no real conversation produces: a message in an order the model never made it in,
// a tool result that answers no call of the message before it, or a call left
// unanswered before the next message. The error is an invalid request as well,
// and names the position of the message and never what it says.
var ErrSessionInvariant = ops.ErrSessionInvariant

// NewSession returns a Session. The zero Session is as ready as the one this
// returns.
func NewSession() *Session {
	return ops.NewSession()
}

// --- The client and catalogue, re-exported.
//
// Their implementation moved to internal/api/client so it could live beside the
// tests that reach its unexported state -- Go requires a package's own tests to
// sit in its directory, so keeping them apart meant keeping nine test files at
// the repository root.
//
// These are aliases and thin forwarders, not a second implementation. A type
// alias is the same type, so *schemaflux.Client and *client.Client are
// identical to the compiler and every method comes along without being listed
// here.

type (
	Client            = client.Client
	OperationCategory = client.OperationCategory
	OperationTier     = client.OperationTier
	CatalogEntry      = client.CatalogEntry
)

func NewClient(apiKey string) *Client { return client.NewClient(apiKey) }

// What Client.WithScheduler and Client.WithDataPolicy take. The two methods
// were exported with arguments of types that were not, so nothing outside this
// module could call either (LX-126).
type (
	// Scheduler bounds how many requests are with a provider at once, and how
	// many may wait. Give one to a client with Client.WithScheduler.
	Scheduler = ops.Scheduler

	// SchedulerLimits are a Scheduler's bounds. Zero on any of them means no
	// bound on that axis.
	SchedulerLimits = ops.SchedulerLimits

	// SchedulerStats is what Scheduler.Stats reports.
	SchedulerStats = ops.SchedulerStats

	// DataPolicy says which routes a client's calls may take. It does not fail
	// open: a list left empty allows nothing, so the zero value refuses every
	// call. See Client.WithDataPolicy for what is enforced.
	DataPolicy = types.DataPolicy

	// RetentionDays is a length of retention in days, for
	// DataPolicy.MaxRetentionDays.
	RetentionDays = types.RetentionDays
)

// RetentionUnspecified is DataPolicy.MaxRetentionDays with nothing stated,
// under which only a route that keeps nothing is allowed.
const RetentionUnspecified = types.RetentionUnspecified

// NewScheduler returns a scheduler with the given limits. Close it when the
// client it was given to is done with, so that anything waiting is refused
// and not left waiting.
//
//	scheduler := schemaflux.NewScheduler(schemaflux.SchedulerLimits{MaxConcurrent: 4, MaxQueued: 64})
//	client := schemaflux.NewClient(key).WithScheduler(scheduler)
func NewScheduler(limits SchedulerLimits) *Scheduler { return ops.NewScheduler(limits) }

// DeclareRoute declares where a provider's model runs and how long it keeps
// what it is sent. A client with a data policy refuses a route that has
// declared nothing, so every route such a client uses is declared first, at
// start-up and before any request is made:
//
//	schemaflux.DeclareRoute("openai", "gpt-6-luna", []string{"eu"}, 0)
//
// regions is every region the route may run in, and retentionDays the longest
// it keeps content, zero for none. These are the caller's statements about
// their own agreement with the provider. This library has no way to check
// them and takes them as given.
func DeclareRoute(provider, model string, regions []string, retentionDays int) {
	llm.DeclareRoute(provider, model, regions, retentionDays)
}

func Init(key string) error             { return client.Init(key) }
func InitWithEnv(paths ...string) error { return client.InitWithEnv(paths...) }

func SetDefaultClient(c *Client) { client.SetDefaultClient(c) }
func GetDefaultClient() *Client  { return client.GetDefaultClient() }

func Catalog() []CatalogEntry                   { return client.Catalog() }
func Describe(name string) (CatalogEntry, bool) { return client.Describe(name) }

func GetLogger() *telemetry.Logger { return client.GetLogger() }
func ConfigureLogging(cfg telemetry.LoggerConfig) *telemetry.Logger {
	return client.ConfigureLogging(cfg)
}
func SetLogLevel(level telemetry.LogLevel) { client.SetLogLevel(level) }
func GetLogEntries() []telemetry.LogEntry  { return client.GetLogEntries() }
func ResetLogEntries()                     { client.ResetLogEntries() }

func ConfigureRequestTracking(cfg requesttracking.Config) { client.ConfigureRequestTracking(cfg) }
func GetRequestTrackingConfig() requesttracking.Config {
	return client.GetRequestTrackingConfig()
}
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return client.WithRequestID(ctx, requestID)
}
func WithCorrelationID(ctx context.Context, correlationID string) context.Context {
	return client.WithCorrelationID(ctx, correlationID)
}
func WithRequestTrackingMetadata(ctx context.Context, metadata requesttracking.Metadata) context.Context {
	return client.WithRequestTrackingMetadata(ctx, metadata)
}
func RequestTrackingFromContext(ctx context.Context) requesttracking.Metadata {
	return client.RequestTrackingFromContext(ctx)
}
func InjectRequestTracking(ctx context.Context, carrier map[string]string) {
	client.InjectRequestTracking(ctx, carrier)
}
func ExtractRequestTracking(ctx context.Context, carrier map[string]string) context.Context {
	return client.ExtractRequestTracking(ctx, carrier)
}

// --- from fluent.go: the builder entry points ---

type CommonOptions = ops.CommonOptions

var NewCommonOptions = ops.NewCommonOptions

type (
	ExtractRequest[T any]                = builder.ExtractRequest[T]
	TransformRequest[T any, U any]       = builder.TransformRequest[T, U]
	GenerateRequest[T any]               = builder.GenerateRequest[T]
	ChooseRequest[T any]                 = builder.ChooseRequest[T]
	FilterRequest[T any]                 = builder.FilterRequest[T]
	SortRequest[T any]                   = builder.SortRequest[T]
	ClassifyRequest[T any, C ~string]    = builder.ClassifyRequest[T, C]
	ScoreRequest[T any]                  = builder.ScoreRequest[T]
	CompareRequest[T any]                = builder.CompareRequest[T]
	SimilarRequest[T any]                = builder.SimilarRequest[T]
	InferRequest[T any]                  = builder.InferRequest[T]
	DiffRequest[T any]                   = builder.DiffRequest[T]
	ExplainRequest                       = builder.ExplainRequest
	ParseRequest[T any]                  = builder.ParseRequest[T]
	SummarizeRequest                     = builder.SummarizeRequest
	RewriteRequest                       = builder.RewriteRequest
	TranslateRequest                     = builder.TranslateRequest
	ExpandRequest                        = builder.ExpandRequest
	SuggestRequest[T any]                = builder.SuggestRequest[T]
	RedactRequest[T any]                 = builder.RedactRequest[T]
	RedactTextRequest                    = builder.RedactTextRequest
	CompleteRequest                      = builder.CompleteRequest
	CompleteFieldRequest[T any]          = builder.CompleteFieldRequest[T]
	ValidateRequest[T any]               = builder.ValidateRequest[T]
	QuestionRequest[T any, A any]        = builder.QuestionRequest[T, A]
	AnnotateRequest[T any]               = builder.AnnotateRequest[T]
	ClusterRequest[T any]                = builder.ClusterRequest[T]
	RankRequest[T any]                   = builder.RankRequest[T]
	CompressRequest[T any]               = builder.CompressRequest[T]
	CompressTextRequest                  = builder.CompressTextRequest
	DecomposeRequest[T any]              = builder.DecomposeRequest[T]
	DecomposeSliceRequest[T any, U any]  = builder.DecomposeSliceRequest[T, U]
	EnrichRequest[T any, U any]          = builder.EnrichRequest[T, U]
	EnrichInPlaceRequest[T any]          = builder.EnrichInPlaceRequest[T]
	NormalizeRequest[T any]              = builder.NormalizeRequest[T]
	NormalizeTextRequest                 = builder.NormalizeTextRequest
	NormalizeBatchRequest[T any]         = builder.NormalizeBatchRequest[T]
	SemanticMatchRequest[S any, T any]   = builder.SemanticMatchRequest[S, T]
	MatchOneRequest[S any, T any]        = builder.MatchOneRequest[S, T]
	CritiqueRequest[T any]               = builder.CritiqueRequest[T]
	SynthesizeRequest[T any]             = builder.SynthesizeRequest[T]
	PredictRequest[T any]                = builder.PredictRequest[T]
	VerifyRequest                        = builder.VerifyRequest
	VerifyClaimRequest                   = builder.VerifyClaimRequest
	NegotiateRequest[T any]              = builder.NegotiateRequest[T]
	AdversarialNegotiationRequest[T any] = builder.AdversarialNegotiationRequest[T]
	ResolveRequest[T any]                = builder.ResolveRequest[T]
	DeriveRequest[T any, U any]          = builder.DeriveRequest[T, U]
	ConformRequest[T any]                = builder.ConformRequest[T]
	InterpolateRequest[T any]            = builder.InterpolateRequest[T]
	ArbitrateRequest[T any]              = builder.ArbitrateRequest[T]
	ProjectRequest[T any, U any]         = builder.ProjectRequest[T, U]
	AuditRequest[T any]                  = builder.AuditRequest[T]
	AssembleRequest[T any]               = builder.AssembleRequest[T]
	PivotRequest[T any, U any]           = builder.PivotRequest[T, U]
)

func Extracting[T any](input any) ExtractRequest[T] {
	return builder.Extracting[T](input)
}

func Transforming[T any, U any](input T) TransformRequest[T, U] {
	return builder.Transforming[T, U](input)
}

func Generating[T any](prompt string) GenerateRequest[T] {
	return builder.Generating[T](prompt)
}

func Choosing[T any](options []T) ChooseRequest[T] {
	return builder.Choosing[T](options)
}

func Filtering[T any](items []T) FilterRequest[T] {
	return builder.Filtering[T](items)
}

func Sorting[T any](items []T) SortRequest[T] {
	return builder.Sorting[T](items)
}

func ChooseBy[T any](options []T, criteria ...string) (T, error) {
	return builder.ChooseBy[T](options, criteria...)
}

func FilterBy[T any](items []T, criteria string) ([]T, error) {
	return builder.FilterBy[T](items, criteria)
}

func SortBy[T any](items []T, criteria string) ([]T, error) {
	return builder.SortBy[T](items, criteria)
}

func Classifying[T any, C ~string](input T) ClassifyRequest[T, C] {
	return builder.Classifying[T, C](input)
}

func Scoring[T any](input T) ScoreRequest[T] {
	return builder.Scoring[T](input)
}

func Comparing[T any](left, right T) CompareRequest[T] {
	return builder.Comparing[T](left, right)
}

func CheckingSimilarity[T any](left, right T) SimilarRequest[T] {
	return builder.CheckingSimilarity[T](left, right)
}

func Inferring[T any](input T) InferRequest[T] {
	return builder.Inferring[T](input)
}

func Diffing[T any](oldValue, newValue T) DiffRequest[T] {
	return builder.Diffing[T](oldValue, newValue)
}

func Explaining(input any) ExplainRequest {
	return builder.Explaining(input)
}

func Parsing[T any](input any) ParseRequest[T] {
	return builder.Parsing[T](input)
}

func Summarizing(input string) SummarizeRequest {
	return builder.Summarizing(input)
}

func Rewriting(input string) RewriteRequest {
	return builder.Rewriting(input)
}

func Translating(input string) TranslateRequest {
	return builder.Translating(input)
}

func Expanding(input string) ExpandRequest {
	return builder.Expanding(input)
}

func Suggesting[T any](input any) SuggestRequest[T] {
	return builder.Suggesting[T](input)
}

func Redacting[T any](input T) RedactRequest[T] {
	return builder.Redacting[T](input)
}

func LLMRedacting(input string) RedactTextRequest {
	return builder.LLMRedacting(input)
}

func Completing(input string) CompleteRequest {
	return builder.Completing(input)
}

func CompletingField[T any](input T, fieldName string) CompleteFieldRequest[T] {
	return builder.CompletingField[T](input, fieldName)
}

func Validating[T any](input T) ValidateRequest[T] {
	return builder.Validating[T](input)
}

// Asking runs one model round trip despite its conversational name; see
// ops.Question's doc comment for why and where a real multi-turn primitive
// lives instead (PS-005, Revised ARC-20).
func Asking[T any, A any](input T, question string) QuestionRequest[T, A] {
	return builder.Asking[T, A](input, question)
}

func Annotating[T any](input T) AnnotateRequest[T] {
	return builder.Annotating[T](input)
}

func Clustering[T any](items []T) ClusterRequest[T] {
	return builder.Clustering[T](items)
}

func Ranking[T any](items []T) RankRequest[T] {
	return builder.Ranking[T](items)
}

func Compressing[T any](input T) CompressRequest[T] {
	return builder.Compressing[T](input)
}

func CompressingText(input string) CompressTextRequest {
	return builder.CompressingText(input)
}

func Decomposing[T any](input T) DecomposeRequest[T] {
	return builder.Decomposing[T](input)
}

func DecomposingInto[T any, U any](input T) DecomposeSliceRequest[T, U] {
	return builder.DecomposingInto[T, U](input)
}

func Enriching[T any, U any](input T) EnrichRequest[T, U] {
	return builder.Enriching[T, U](input)
}

func EnrichingInPlace[T any](input T) EnrichInPlaceRequest[T] {
	return builder.EnrichingInPlace[T](input)
}

func Normalizing[T any](input T) NormalizeRequest[T] {
	return builder.Normalizing[T](input)
}

func NormalizingText(input string) NormalizeTextRequest {
	return builder.NormalizingText(input)
}

func NormalizingBatch[T any](items []T) NormalizeBatchRequest[T] {
	return builder.NormalizingBatch[T](items)
}

func Matching[S any, T any](sources []S, targets []T) SemanticMatchRequest[S, T] {
	return builder.Matching[S, T](sources, targets)
}

func MatchingOne[S any, T any](source S, targets []T) MatchOneRequest[S, T] {
	return builder.MatchingOne[S, T](source, targets)
}

func Critiquing[T any](input T) CritiqueRequest[T] {
	return builder.Critiquing[T](input)
}

func Synthesizing[T any](sources []any) SynthesizeRequest[T] {
	return builder.Synthesizing[T](sources)
}

func Predicting[T any](input any) PredictRequest[T] {
	return builder.Predicting[T](input)
}

func Verifying(input string) VerifyRequest {
	return builder.Verifying(input)
}

func VerifyingClaim(claim string) VerifyClaimRequest {
	return builder.VerifyingClaim(claim)
}

// Negotiating runs one model round trip despite its conversational name; see
// ops.Negotiate's doc comment for why and where a real multi-turn primitive
// lives instead (PS-005, Revised ARC-20).
func Negotiating[T any](constraints any) NegotiateRequest[T] {
	return builder.Negotiating[T](constraints)
}

// NegotiatingAdversarially runs one model round trip despite its two-party
// framing; see ops.NegotiateAdversarial's doc comment for why and where a
// real multi-turn primitive lives instead (PS-005, Revised ARC-20).
func NegotiatingAdversarially[T any](ctx AdversarialContext[T]) AdversarialNegotiationRequest[T] {
	return builder.NegotiatingAdversarially[T](ctx)
}

func Resolving[T any](sources []T) ResolveRequest[T] {
	return builder.Resolving[T](sources)
}

func Deriving[T any, U any](input T) DeriveRequest[T, U] {
	return builder.Deriving[T, U](input)
}

func Conforming[T any](input T, standard string) ConformRequest[T] {
	return builder.Conforming[T](input, standard)
}

func Interpolating[T any](items []T) InterpolateRequest[T] {
	return builder.Interpolating[T](items)
}

func Arbitrating[T any](options []T) ArbitrateRequest[T] {
	return builder.Arbitrating[T](options)
}

func Projecting[T any, U any](input T) ProjectRequest[T, U] {
	return builder.Projecting[T, U](input)
}

func Auditing[T any](input T) AuditRequest[T] {
	return builder.Auditing[T](input)
}

func Assembling[T any](parts []any) AssembleRequest[T] {
	return builder.Assembling[T](parts)
}

func Pivoting[T any, U any](input T) PivotRequest[T, U] {
	return builder.Pivoting[T, U](input)
}

// The catalogue's constants, re-exported alongside its types.
const (
	CategoryExtract        = client.CategoryExtract
	CategoryTransform      = client.CategoryTransform
	CategoryGenerate       = client.CategoryGenerate
	CategoryClassify       = client.CategoryClassify
	CategorySelect         = client.CategorySelect
	CategoryOrder          = client.CategoryOrder
	CategoryReview         = client.CategoryReview
	CategoryValidate       = client.CategoryValidate
	CategoryInfrastructure = client.CategoryInfrastructure

	TierStable       = client.TierStable
	TierExperimental = client.TierExperimental
	TierDeprecated   = client.TierDeprecated
)
