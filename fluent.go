package schemaflux

import (
	builder "github.com/monstercameron/schemaflux/internal/api/fluent"
	"github.com/monstercameron/schemaflux/internal/ops"
)

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
