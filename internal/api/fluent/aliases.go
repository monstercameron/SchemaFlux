package fluent

import (
	"context"

	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
)

type (
	CommonOptions = ops.CommonOptions

	Mode  = types.Mode
	Speed = types.Speed

	ExtractOptions             = ops.ExtractOptions
	TransformOptions           = ops.TransformOptions
	GenerateOptions            = ops.GenerateOptions
	ChooseOptions              = ops.ChooseOptions
	FilterOptions              = ops.FilterOptions
	SortOptions                = ops.SortOptions
	ClassifyOptions            = ops.ClassifyOptions
	ClassifyResult[C any]      = ops.ClassifyResult[C]
	ScoreOptions               = ops.ScoreOptions
	ScoreResult                = ops.ScoreResult
	CompareOptions             = ops.CompareOptions
	CompareResult[T any]       = ops.CompareResult[T]
	SimilarOptions             = ops.SimilarOptions
	SimilarResult              = ops.SimilarResult
	InferOptions               = ops.InferOptions
	DiffOptions                = ops.DiffOptions
	DiffResult                 = ops.DiffResult
	ExplainOptions             = ops.ExplainOptions
	ExplainResult              = ops.ExplainResult
	ParseOptions               = ops.ParseOptions
	ParseResult[T any]         = ops.ParseResult[T]
	SummarizeOptions           = ops.SummarizeOptions
	SummarizeResult            = ops.SummarizeResult
	RewriteOptions             = ops.RewriteOptions
	RewriteResult              = ops.RewriteResult
	TranslateOptions           = ops.TranslateOptions
	TranslateResult            = ops.TranslateResult
	ExpandOptions              = ops.ExpandOptions
	ExpandResult               = ops.ExpandResult
	SuggestOptions             = ops.SuggestOptions
	SuggestStrategy            = ops.SuggestStrategy
	RedactOptions              = ops.RedactOptions
	RedactStrategy             = ops.RedactStrategy
	RedactLLMOptions           = ops.RedactLLMOptions
	RedactLLMResult            = ops.RedactLLMResult
	CompleteOptions            = ops.CompleteOptions
	CompleteResult             = ops.CompleteResult
	CompleteFieldOptions       = ops.CompleteFieldOptions
	CompleteFieldResult[T any] = ops.CompleteFieldResult[T]
	ValidateOptions            = ops.ValidateOptions
	ValidateResult[T any]      = ops.ValidateResult[T]
	QuestionOptions            = ops.QuestionOptions
	QuestionResult[A any]      = ops.QuestionResult[A]
	AnnotateOptions            = ops.AnnotateOptions
	AnnotateResult             = ops.AnnotateResult
	ClusterOptions             = ops.ClusterOptions
	ClusterResult[T any]       = ops.ClusterResult[T]
	RankOptions                = ops.RankOptions
	RankResult[T any]          = ops.RankResult[T]
	CompressOptions            = ops.CompressOptions
	CompressResult[T any]      = ops.CompressResult[T]
	DecomposeOptions           = ops.DecomposeOptions
	DecomposeResult[T any]     = ops.DecomposeResult[T]
	EnrichOptions              = ops.EnrichOptions
	EnrichResult[T any]        = ops.EnrichResult[T]
	NormalizeOptions           = ops.NormalizeOptions
	NormalizeResult[T any]     = ops.NormalizeResult[T]
	MatchOptions               = ops.MatchOptions
	MatchPair[S any, T any]    = ops.MatchPair[S, T]
	MatchResult[S any, T any]  = ops.MatchResult[S, T]
	CritiqueOptions            = ops.CritiqueOptions
	CritiqueResult             = ops.CritiqueResult
	SynthesizeOptions          = ops.SynthesizeOptions
	SynthesizeResult[T any]    = ops.SynthesizeResult[T]
	PredictOptions             = ops.PredictOptions
	PredictResult[T any]       = ops.PredictResult[T]
	VerifyOptions              = ops.VerifyOptions
	VerifyResult               = ops.VerifyResult
	ClaimVerification          = ops.ClaimVerification
	NegotiateOptions           = ops.NegotiateOptions
	NegotiateResult[T any]     = ops.NegotiateResult[T]
	AdversarialPosition[T any] = ops.AdversarialPosition[T]
	AdversarialContext[T any]  = ops.AdversarialContext[T]
	AdversarialOptions         = ops.AdversarialOptions
	AdversarialResult[T any]   = ops.AdversarialResult[T]
	ResolveOptions             = ops.ResolveOptions
	ResolveResult[T any]       = ops.ResolveResult[T]
	DeriveOptions              = ops.DeriveOptions
	DeriveResult[U any]        = ops.DeriveResult[U]
	ConformOptions             = ops.ConformOptions
	ConformResult[T any]       = ops.ConformResult[T]
	InterpolateOptions         = ops.InterpolateOptions
	InterpolateResult[T any]   = ops.InterpolateResult[T]
	ArbitrateOptions           = ops.ArbitrateOptions
	ArbitrateResult[T any]     = ops.ArbitrateResult[T]
	ProjectOptions             = ops.ProjectOptions
	ProjectResult[U any]       = ops.ProjectResult[U]
	AuditOptions               = ops.AuditOptions
	AuditResult[T any]         = ops.AuditResult[T]
	ComposeOptions             = ops.ComposeOptions
	ComposeResult[T any]       = ops.ComposeResult[T]
	PivotOptions               = ops.PivotOptions
	PivotResult[U any]         = ops.PivotResult[U]
)

const (
	ModeUnset     = types.ModeUnset
	Strict        = types.Strict
	TransformMode = types.TransformMode
	Creative      = types.Creative

	TierUnset = types.TierUnset
	Smart     = types.Smart
	Fast      = types.Fast
	Quick     = types.Quick
)

var (
	NewExtractOptions       = ops.NewExtractOptions
	NewTransformOptions     = ops.NewTransformOptions
	NewGenerateOptions      = ops.NewGenerateOptions
	NewChooseOptions        = ops.NewChooseOptions
	NewFilterOptions        = ops.NewFilterOptions
	NewSortOptions          = ops.NewSortOptions
	NewClassifyOptions      = ops.NewClassifyOptions
	NewScoreOptions         = ops.NewScoreOptions
	NewCompareOptions       = ops.NewCompareOptions
	NewSimilarOptions       = ops.NewSimilarOptions
	NewInferOptions         = ops.NewInferOptions
	NewDiffOptions          = ops.NewDiffOptions
	NewExplainOptions       = ops.NewExplainOptions
	NewParseOptions         = ops.NewParseOptions
	NewSummarizeOptions     = ops.NewSummarizeOptions
	NewRewriteOptions       = ops.NewRewriteOptions
	NewTranslateOptions     = ops.NewTranslateOptions
	NewExpandOptions        = ops.NewExpandOptions
	NewSuggestOptions       = ops.NewSuggestOptions
	NewRedactOptions        = ops.NewRedactOptions
	NewRedactLLMOptions     = ops.NewRedactLLMOptions
	NewCompleteOptions      = ops.NewCompleteOptions
	NewCompleteFieldOptions = ops.NewCompleteFieldOptions
	NewValidateOptions      = ops.NewValidateOptions
	NewQuestionOptions      = ops.NewQuestionOptions
	NewAnnotateOptions      = ops.NewAnnotateOptions
	NewClusterOptions       = ops.NewClusterOptions
	NewRankOptions          = ops.NewRankOptions
	NewCompressOptions      = ops.NewCompressOptions
	NewDecomposeOptions     = ops.NewDecomposeOptions
	NewEnrichOptions        = ops.NewEnrichOptions
	NewNormalizeOptions     = ops.NewNormalizeOptions
	NewMatchOptions         = ops.NewMatchOptions
	NewCritiqueOptions      = ops.NewCritiqueOptions
	NewSynthesizeOptions    = ops.NewSynthesizeOptions
	NewPredictOptions       = ops.NewPredictOptions
	NewVerifyOptions        = ops.NewVerifyOptions
)

func Extract[T any](input any, opts ExtractOptions) (T, error) {
	return ops.Extract[T](input, opts)
}

func Transform[T any, U any](input T, opts TransformOptions) (U, error) {
	return ops.Transform[T, U](input, opts)
}

func Generate[T any](prompt string, opts GenerateOptions) (T, error) {
	return ops.Generate[T](prompt, opts)
}

func Choose[T any](options []T, opts ChooseOptions) (T, error) {
	return ops.Choose(options, opts)
}

func Filter[T any](items []T, opts FilterOptions) ([]T, error) {
	return ops.Filter(items, opts)
}

func Sort[T any](items []T, opts SortOptions) ([]T, error) {
	return ops.Sort(items, opts)
}

func Classify[T any, C any](input T, opts ClassifyOptions) (ClassifyResult[C], error) {
	return ops.Classify[T, C](input, opts)
}

func Score[T any](input T, opts ScoreOptions) (ScoreResult, error) {
	return ops.Score(input, opts)
}

func Compare[T any](left, right T, opts CompareOptions) (CompareResult[T], error) {
	return ops.Compare(left, right, opts)
}

func Similar[T any](left, right T, opts SimilarOptions) (SimilarResult, error) {
	return ops.Similar(left, right, opts)
}

func Infer[T any](input T, opts InferOptions) (T, error) {
	return ops.Infer(input, opts)
}

func Diff[T any](oldValue, newValue T, opts DiffOptions) (DiffResult, error) {
	return ops.Diff(oldValue, newValue, opts)
}

func Explain(input any, opts ExplainOptions) (ExplainResult, error) {
	return ops.Explain(input, opts)
}

func Parse[T any](input any, opts ParseOptions) (ParseResult[T], error) {
	return ops.Parse[T](input, opts)
}

func Summarize(input string, opts SummarizeOptions) (string, error) {
	return ops.Summarize(input, opts)
}

func SummarizeWithMetadata(input string, opts SummarizeOptions) (SummarizeResult, error) {
	return ops.SummarizeWithMetadata(input, opts)
}

func Rewrite(input string, opts RewriteOptions) (string, error) {
	return ops.Rewrite(input, opts)
}

func RewriteWithMetadata(input string, opts RewriteOptions) (RewriteResult, error) {
	return ops.RewriteWithMetadata(input, opts)
}

func Translate(input string, opts TranslateOptions) (string, error) {
	return ops.Translate(input, opts)
}

func TranslateWithMetadata(input string, opts TranslateOptions) (TranslateResult, error) {
	return ops.TranslateWithMetadata(input, opts)
}

func Expand(input string, opts ExpandOptions) (string, error) {
	return ops.Expand(input, opts)
}

func ExpandWithMetadata(input string, opts ExpandOptions) (ExpandResult, error) {
	return ops.ExpandWithMetadata(input, opts)
}

func Suggest[T any](input any, opts SuggestOptions) ([]T, error) {
	return ops.Suggest[T](input, opts)
}

func Redact[T any](input T, opts RedactOptions) (T, error) {
	return ops.Redact(input, opts)
}

func RedactLLM(input string, opts RedactLLMOptions) (RedactLLMResult, error) {
	return ops.RedactLLM(context.Background(), input, opts)
}

func Complete(input string, opts CompleteOptions) (CompleteResult, error) {
	return ops.Complete(context.Background(), input, opts)
}

func CompleteField[T any](input T, opts CompleteFieldOptions) (CompleteFieldResult[T], error) {
	return ops.CompleteField[T](context.Background(), input, opts)
}

func Validate[T any](input T, opts ValidateOptions) (ValidateResult[T], error) {
	return ops.Validate[T](input, opts)
}

func Question[T any, A any](input T, opts QuestionOptions) (QuestionResult[A], error) {
	return ops.Question[T, A](input, opts)
}

func Annotate[T any](input T, opts AnnotateOptions) (AnnotateResult, error) {
	return ops.Annotate[T](input, opts)
}

func Cluster[T any](items []T, opts ClusterOptions) (ClusterResult[T], error) {
	return ops.Cluster[T](items, opts)
}

func Rank[T any](items []T, opts RankOptions) (RankResult[T], error) {
	return ops.Rank[T](items, opts)
}

func Compress[T any](input T, opts CompressOptions) (CompressResult[T], error) {
	return ops.Compress[T](input, opts)
}

func CompressText(input string, opts CompressOptions) (string, error) {
	return ops.CompressText(input, opts)
}

func Decompose[T any](input T, opts DecomposeOptions) (DecomposeResult[T], error) {
	return ops.Decompose[T](input, opts)
}

func DecomposeToSlice[T any, U any](input T, opts DecomposeOptions) ([]U, error) {
	return ops.DecomposeToSlice[T, U](input, opts)
}

func Enrich[T any, U any](input T, opts EnrichOptions) (EnrichResult[U], error) {
	return ops.Enrich[T, U](input, opts)
}

func EnrichInPlace[T any](input T, opts EnrichOptions) (T, error) {
	return ops.EnrichInPlace[T](input, opts)
}

func Normalize[T any](input T, opts NormalizeOptions) (NormalizeResult[T], error) {
	return ops.Normalize[T](input, opts)
}

func NormalizeText(input string, opts NormalizeOptions) (string, error) {
	return ops.NormalizeText(input, opts)
}

func NormalizeBatch[T any](items []T, opts NormalizeOptions) ([]NormalizeResult[T], error) {
	return ops.NormalizeBatch[T](items, opts)
}

func SemanticMatch[S any, T any](sources []S, targets []T, opts MatchOptions) (MatchResult[S, T], error) {
	return ops.SemanticMatch[S, T](sources, targets, opts)
}

func MatchOne[S any, T any](source S, targets []T, opts MatchOptions) ([]MatchPair[S, T], error) {
	return ops.MatchOne[S, T](source, targets, opts)
}

func Critique[T any](input T, opts CritiqueOptions) (CritiqueResult, error) {
	return ops.Critique[T](input, opts)
}

func Synthesize[T any](sources []any, opts SynthesizeOptions) (SynthesizeResult[T], error) {
	return ops.Synthesize[T](sources, opts)
}

func Predict[T any](input any, opts PredictOptions) (PredictResult[T], error) {
	return ops.Predict[T](input, opts)
}

func Verify(input string, opts VerifyOptions) (VerifyResult, error) {
	return ops.Verify(input, opts)
}

func VerifyClaim(claim string, opts VerifyOptions) (ClaimVerification, error) {
	return ops.VerifyClaim(claim, opts)
}

func Negotiate[T any](constraints any, opts NegotiateOptions) (NegotiateResult[T], error) {
	return ops.Negotiate[T](constraints, opts)
}

func NegotiateAdversarial[T any](ctx AdversarialContext[T], opts AdversarialOptions) (AdversarialResult[T], error) {
	return ops.NegotiateAdversarial[T](ctx, opts)
}

func Resolve[T any](sources []T, opts ResolveOptions) (ResolveResult[T], error) {
	return ops.Resolve[T](sources, opts)
}

func Derive[T any, U any](input T, opts DeriveOptions) (DeriveResult[U], error) {
	return ops.Derive[T, U](input, opts)
}

func Conform[T any](input T, standard string, opts ConformOptions) (ConformResult[T], error) {
	return ops.Conform[T](input, standard, opts)
}

func Interpolate[T any](items []T, opts InterpolateOptions) (InterpolateResult[T], error) {
	return ops.Interpolate[T](items, opts)
}

func Arbitrate[T any](options []T, opts ArbitrateOptions) (ArbitrateResult[T], error) {
	return ops.Arbitrate[T](options, opts)
}

func Project[T any, U any](input T, opts ProjectOptions) (ProjectResult[U], error) {
	return ops.Project[T, U](input, opts)
}

func Audit[T any](input T, opts AuditOptions) (AuditResult[T], error) {
	return ops.Audit[T](input, opts)
}

func Assemble[T any](parts []any, opts ComposeOptions) (ComposeResult[T], error) {
	return ops.Assemble[T](parts, opts)
}

func Pivot[T any, U any](input T, opts PivotOptions) (PivotResult[U], error) {
	return ops.Pivot[T, U](input, opts)
}
