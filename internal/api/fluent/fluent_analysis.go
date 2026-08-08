package fluent

import "github.com/monstercameron/schemaflux/internal/types"

// ClassifyRequest is a fluent builder for Classify.
type ClassifyRequest[T any, C ~string] struct {
	commonRequest[ClassifyRequest[T, C], ClassifyOptions]
	input T
}

func newClassifyRequest[T any, C ~string](input T, opts ClassifyOptions) ClassifyRequest[T, C] {
	return ClassifyRequest[T, C]{
		commonRequest: commonRequest[ClassifyRequest[T, C], ClassifyOptions]{
			opts: opts,
			lift: func(next ClassifyOptions) ClassifyRequest[T, C] {
				return newClassifyRequest[T, C](input, next)
			},
			mutate: func(current ClassifyOptions, fn func(CommonOptions) CommonOptions) ClassifyOptions {
				current.CommonOptions = fn(current.CommonOptions)
				return current
			},
		},
		input: input,
	}
}

// Classifying starts a fluent Classify request.
func Classifying[T any, C ~string](input T) ClassifyRequest[T, C] {
	return newClassifyRequest[T, C](input, NewClassifyOptions())
}

func (r ClassifyRequest[T, C]) Categories(categories ...string) ClassifyRequest[T, C] {
	opts := r.opts
	opts.Categories = append([]string(nil), categories...)
	return r.WithOptions(opts)
}

func (r ClassifyRequest[T, C]) MultiLabel(allow bool) ClassifyRequest[T, C] {
	opts := r.opts
	opts.MultiLabel = allow
	return r.WithOptions(opts)
}

func (r ClassifyRequest[T, C]) Run() (ClassifyResult[C], error) {
	return Classify[T, C](r.input, r.opts)
}

// ScoreRequest is a fluent builder for Score.
type ScoreRequest[T any] struct {
	commonRequest[ScoreRequest[T], ScoreOptions]
	input T
}

func newScoreRequest[T any](input T, opts ScoreOptions) ScoreRequest[T] {
	return ScoreRequest[T]{
		commonRequest: commonRequest[ScoreRequest[T], ScoreOptions]{
			opts: opts,
			lift: func(next ScoreOptions) ScoreRequest[T] {
				return newScoreRequest[T](input, next)
			},
			mutate: func(current ScoreOptions, fn func(CommonOptions) CommonOptions) ScoreOptions {
				current.CommonOptions = fn(current.CommonOptions)
				return current
			},
		},
		input: input,
	}
}

// Scoring starts a fluent Score request.
func Scoring[T any](input T) ScoreRequest[T] {
	return newScoreRequest(input, NewScoreOptions())
}

func (r ScoreRequest[T]) By(criteria ...string) ScoreRequest[T] {
	opts := r.opts
	opts.Criteria = append([]string(nil), criteria...)
	return r.WithOptions(opts)
}

func (r ScoreRequest[T]) Scale(min, max float64) ScoreRequest[T] {
	opts := r.opts
	opts.ScaleMin = min
	opts.ScaleMax = max
	return r.WithOptions(opts)
}

func (r ScoreRequest[T]) Run() (ScoreResult, error) {
	return Score[T](r.input, r.opts)
}

// CompareRequest is a fluent builder for Compare.
type CompareRequest[T any] struct {
	commonRequest[CompareRequest[T], CompareOptions]
	left  T
	right T
}

func newCompareRequest[T any](left, right T, opts CompareOptions) CompareRequest[T] {
	return CompareRequest[T]{
		commonRequest: commonRequest[CompareRequest[T], CompareOptions]{
			opts: opts,
			lift: func(next CompareOptions) CompareRequest[T] {
				return newCompareRequest(left, right, next)
			},
			mutate: func(current CompareOptions, fn func(CommonOptions) CommonOptions) CompareOptions {
				current.CommonOptions = fn(current.CommonOptions)
				return current
			},
		},
		left:  left,
		right: right,
	}
}

// Comparing starts a fluent Compare request.
func Comparing[T any](left, right T) CompareRequest[T] {
	return newCompareRequest(left, right, NewCompareOptions())
}

func (r CompareRequest[T]) Aspects(aspects ...string) CompareRequest[T] {
	opts := r.opts
	opts.ComparisonAspects = append([]string(nil), aspects...)
	return r.WithOptions(opts)
}

func (r CompareRequest[T]) Focus(focus string) CompareRequest[T] {
	opts := r.opts
	opts.FocusOn = focus
	return r.WithOptions(opts)
}

func (r CompareRequest[T]) Run() (CompareResult[T], error) {
	return Compare[T](r.left, r.right, r.opts)
}

// SimilarRequest is a fluent builder for Similar.
type SimilarRequest[T any] struct {
	opRequest[SimilarRequest[T], SimilarOptions]
	left  T
	right T
}

func newSimilarRequest[T any](left, right T, opts SimilarOptions) SimilarRequest[T] {
	return SimilarRequest[T]{
		opRequest: opRequest[SimilarRequest[T], SimilarOptions]{
			opts: opts,
			lift: func(next SimilarOptions) SimilarRequest[T] {
				return newSimilarRequest(left, right, next)
			},
			mutate: func(current SimilarOptions, fn func(types.OpOptions) types.OpOptions) SimilarOptions {
				current.OpOptions = fn(current.OpOptions)
				return current
			},
		},
		left:  left,
		right: right,
	}
}

// CheckingSimilarity starts a fluent Similar request.
func CheckingSimilarity[T any](left, right T) SimilarRequest[T] {
	return newSimilarRequest(left, right, NewSimilarOptions())
}

func (r SimilarRequest[T]) Aspects(aspects ...string) SimilarRequest[T] {
	opts := r.opts
	opts.Aspects = append([]string(nil), aspects...)
	return r.WithOptions(opts)
}

func (r SimilarRequest[T]) Threshold(threshold float64) SimilarRequest[T] {
	opts := r.opts
	opts.SimilarityThreshold = threshold
	return r.WithOptions(opts)
}

func (r SimilarRequest[T]) Run() (SimilarResult, error) {
	return Similar[T](r.left, r.right, r.opts)
}

// InferRequest is a fluent builder for Infer.
type InferRequest[T any] struct {
	opRequest[InferRequest[T], InferOptions]
	input T
}

func newInferRequest[T any](input T, opts InferOptions) InferRequest[T] {
	return InferRequest[T]{
		opRequest: opRequest[InferRequest[T], InferOptions]{
			opts: opts,
			lift: func(next InferOptions) InferRequest[T] {
				return newInferRequest(input, next)
			},
			mutate: func(current InferOptions, fn func(types.OpOptions) types.OpOptions) InferOptions {
				current.OpOptions = fn(current.OpOptions)
				return current
			},
		},
		input: input,
	}
}

// Inferring starts a fluent Infer request.
func Inferring[T any](input T) InferRequest[T] {
	return newInferRequest(input, NewInferOptions())
}

func (r InferRequest[T]) Run() (T, error) {
	return Infer[T](r.input, r.opts)
}

// DiffRequest is a fluent builder for Diff.
type DiffRequest[T any] struct {
	opRequest[DiffRequest[T], DiffOptions]
	oldValue T
	newValue T
}

func newDiffRequest[T any](oldValue, newValue T, opts DiffOptions) DiffRequest[T] {
	return DiffRequest[T]{
		opRequest: opRequest[DiffRequest[T], DiffOptions]{
			opts: opts,
			lift: func(next DiffOptions) DiffRequest[T] {
				return newDiffRequest(oldValue, newValue, next)
			},
			mutate: func(current DiffOptions, fn func(types.OpOptions) types.OpOptions) DiffOptions {
				current.OpOptions = fn(current.OpOptions)
				return current
			},
		},
		oldValue: oldValue,
		newValue: newValue,
	}
}

// Diffing starts a fluent Diff request.
func Diffing[T any](oldValue, newValue T) DiffRequest[T] {
	return newDiffRequest(oldValue, newValue, NewDiffOptions())
}

func (r DiffRequest[T]) Run() (DiffResult, error) {
	return Diff[T](r.oldValue, r.newValue, r.opts)
}

// ExplainRequest is a fluent builder for Explain.
type ExplainRequest struct {
	opRequest[ExplainRequest, ExplainOptions]
	input any
}

func newExplainRequest(input any, opts ExplainOptions) ExplainRequest {
	return ExplainRequest{
		opRequest: opRequest[ExplainRequest, ExplainOptions]{
			opts: opts,
			lift: func(next ExplainOptions) ExplainRequest {
				return newExplainRequest(input, next)
			},
			mutate: func(current ExplainOptions, fn func(types.OpOptions) types.OpOptions) ExplainOptions {
				current.OpOptions = fn(current.OpOptions)
				return current
			},
		},
		input: input,
	}
}

// Explaining starts a fluent Explain request.
func Explaining(input any) ExplainRequest {
	return newExplainRequest(input, NewExplainOptions())
}

func (r ExplainRequest) Run() (ExplainResult, error) {
	return Explain(r.input, r.opts)
}

// ParseRequest is a fluent builder for Parse.
type ParseRequest[T any] struct {
	opRequest[ParseRequest[T], ParseOptions]
	input any
}

func newParseRequest[T any](input any, opts ParseOptions) ParseRequest[T] {
	return ParseRequest[T]{
		opRequest: opRequest[ParseRequest[T], ParseOptions]{
			opts: opts,
			lift: func(next ParseOptions) ParseRequest[T] {
				return newParseRequest[T](input, next)
			},
			mutate: func(current ParseOptions, fn func(types.OpOptions) types.OpOptions) ParseOptions {
				current.OpOptions = fn(current.OpOptions)
				return current
			},
		},
		input: input,
	}
}

// Parsing starts a fluent Parse request.
func Parsing[T any](input any) ParseRequest[T] {
	return newParseRequest[T](input, NewParseOptions())
}

func (r ParseRequest[T]) AllowLLMFallback(allow bool) ParseRequest[T] {
	opts := r.opts
	opts.AllowLLMFallback = allow
	return r.WithOptions(opts)
}

func (r ParseRequest[T]) AutoFix(autoFix bool) ParseRequest[T] {
	opts := r.opts
	opts.AutoFix = autoFix
	return r.WithOptions(opts)
}

func (r ParseRequest[T]) FormatHints(hints ...string) ParseRequest[T] {
	opts := r.opts
	opts.FormatHints = append([]string(nil), hints...)
	return r.WithOptions(opts)
}

func (r ParseRequest[T]) Run() (ParseResult[T], error) {
	return Parse[T](r.input, r.opts)
}

// SummarizeRequest is a fluent builder for Summarize.
type SummarizeRequest struct {
	commonRequest[SummarizeRequest, SummarizeOptions]
	input string
}

func newSummarizeRequest(input string, opts SummarizeOptions) SummarizeRequest {
	return SummarizeRequest{
		commonRequest: commonRequest[SummarizeRequest, SummarizeOptions]{
			opts: opts,
			lift: func(next SummarizeOptions) SummarizeRequest {
				return newSummarizeRequest(input, next)
			},
			mutate: func(current SummarizeOptions, fn func(CommonOptions) CommonOptions) SummarizeOptions {
				current.CommonOptions = fn(current.CommonOptions)
				return current
			},
		},
		input: input,
	}
}

// Summarizing starts a fluent Summarize request.
func Summarizing(input string) SummarizeRequest {
	return newSummarizeRequest(input, NewSummarizeOptions())
}

func (r SummarizeRequest) MaxLength(max int) SummarizeRequest {
	opts := r.opts
	opts.TargetLength = max
	return r.WithOptions(opts)
}

func (r SummarizeRequest) Run() (string, error) {
	return Summarize(r.input, r.opts)
}

func (r SummarizeRequest) RunDetailed() (Summary, error) {
	return SummarizeWithMetadata(r.input, r.opts)
}

// RewriteRequest is a fluent builder for Rewrite.
type RewriteRequest struct {
	commonRequest[RewriteRequest, RewriteOptions]
	input string
}

func newRewriteRequest(input string, opts RewriteOptions) RewriteRequest {
	return RewriteRequest{
		commonRequest: commonRequest[RewriteRequest, RewriteOptions]{
			opts: opts,
			lift: func(next RewriteOptions) RewriteRequest {
				return newRewriteRequest(input, next)
			},
			mutate: func(current RewriteOptions, fn func(CommonOptions) CommonOptions) RewriteOptions {
				current.CommonOptions = fn(current.CommonOptions)
				return current
			},
		},
		input: input,
	}
}

// Rewriting starts a fluent Rewrite request.
func Rewriting(input string) RewriteRequest {
	return newRewriteRequest(input, NewRewriteOptions())
}

func (r RewriteRequest) Style(style string) RewriteRequest {
	opts := r.opts
	opts.StyleGuide = style
	return r.WithOptions(opts)
}

func (r RewriteRequest) Tone(tone string) RewriteRequest {
	opts := r.opts
	opts.TargetTone = tone
	return r.WithOptions(opts)
}

func (r RewriteRequest) Run() (string, error) {
	return Rewrite(r.input, r.opts)
}

func (r RewriteRequest) RunDetailed() (Rewritten, error) {
	return RewriteWithMetadata(r.input, r.opts)
}

// TranslateRequest is a fluent builder for Translate.
type TranslateRequest struct {
	commonRequest[TranslateRequest, TranslateOptions]
	input string
}

func newTranslateRequest(input string, opts TranslateOptions) TranslateRequest {
	return TranslateRequest{
		commonRequest: commonRequest[TranslateRequest, TranslateOptions]{
			opts: opts,
			lift: func(next TranslateOptions) TranslateRequest {
				return newTranslateRequest(input, next)
			},
			mutate: func(current TranslateOptions, fn func(CommonOptions) CommonOptions) TranslateOptions {
				current.CommonOptions = fn(current.CommonOptions)
				return current
			},
		},
		input: input,
	}
}

// Translating starts a fluent Translate request.
func Translating(input string) TranslateRequest {
	return newTranslateRequest(input, NewTranslateOptions())
}

func (r TranslateRequest) To(language string) TranslateRequest {
	opts := r.opts
	opts.TargetLanguage = language
	return r.WithOptions(opts)
}

func (r TranslateRequest) Run() (string, error) {
	return Translate(r.input, r.opts)
}

func (r TranslateRequest) RunDetailed() (Translation, error) {
	return TranslateWithMetadata(r.input, r.opts)
}

// ExpandRequest is a fluent builder for Expand.
type ExpandRequest struct {
	commonRequest[ExpandRequest, ExpandOptions]
	input string
}

func newExpandRequest(input string, opts ExpandOptions) ExpandRequest {
	return ExpandRequest{
		commonRequest: commonRequest[ExpandRequest, ExpandOptions]{
			opts: opts,
			lift: func(next ExpandOptions) ExpandRequest {
				return newExpandRequest(input, next)
			},
			mutate: func(current ExpandOptions, fn func(CommonOptions) CommonOptions) ExpandOptions {
				current.CommonOptions = fn(current.CommonOptions)
				return current
			},
		},
		input: input,
	}
}

// Expanding starts a fluent Expand request.
func Expanding(input string) ExpandRequest {
	return newExpandRequest(input, NewExpandOptions())
}

func (r ExpandRequest) Factor(factor float64) ExpandRequest {
	opts := r.opts
	opts.ExpansionFactor = factor
	return r.WithOptions(opts)
}

func (r ExpandRequest) Run() (string, error) {
	return Expand(r.input, r.opts)
}

func (r ExpandRequest) RunDetailed() (Expansion, error) {
	return ExpandWithMetadata(r.input, r.opts)
}

// SuggestRequest is a fluent builder for Suggest.
type SuggestRequest[T any] struct {
	commonRequest[SuggestRequest[T], SuggestOptions]
	input any
}

func newSuggestRequest[T any](input any, opts SuggestOptions) SuggestRequest[T] {
	return SuggestRequest[T]{
		commonRequest: commonRequest[SuggestRequest[T], SuggestOptions]{
			opts: opts,
			lift: func(next SuggestOptions) SuggestRequest[T] {
				return newSuggestRequest[T](input, next)
			},
			mutate: func(current SuggestOptions, fn func(CommonOptions) CommonOptions) SuggestOptions {
				current.CommonOptions = fn(current.CommonOptions)
				return current
			},
		},
		input: input,
	}
}

// Suggesting starts a fluent Suggest request.
func Suggesting[T any](input any) SuggestRequest[T] {
	return newSuggestRequest[T](input, NewSuggestOptions())
}

func (r SuggestRequest[T]) Top(n int) SuggestRequest[T] {
	opts := r.opts
	opts.TopN = n
	return r.WithOptions(opts)
}

func (r SuggestRequest[T]) Strategy(strategy SuggestStrategy) SuggestRequest[T] {
	opts := r.opts
	opts.Strategy = strategy
	return r.WithOptions(opts)
}

func (r SuggestRequest[T]) Constraints(constraints ...string) SuggestRequest[T] {
	opts := r.opts
	opts.Constraints = append([]string(nil), constraints...)
	return r.WithOptions(opts)
}

func (r SuggestRequest[T]) Run() ([]T, error) {
	return Suggest[T](r.input, r.opts)
}

// RedactRequest is a fluent builder for Redact.
type RedactRequest[T any] struct {
	opRequest[RedactRequest[T], RedactOptions]
	input T
}

func newRedactRequest[T any](input T, opts RedactOptions) RedactRequest[T] {
	return RedactRequest[T]{
		opRequest: opRequest[RedactRequest[T], RedactOptions]{
			opts: opts,
			lift: func(next RedactOptions) RedactRequest[T] {
				return newRedactRequest(input, next)
			},
			mutate: func(current RedactOptions, fn func(types.OpOptions) types.OpOptions) RedactOptions {
				current.OpOptions = fn(current.OpOptions)
				return current
			},
		},
		input: input,
	}
}

// Redacting starts a fluent Redact request.
func Redacting[T any](input T) RedactRequest[T] {
	return newRedactRequest(input, NewRedactOptions())
}

func (r RedactRequest[T]) Patterns(patterns ...string) RedactRequest[T] {
	opts := r.opts
	opts.CustomPatterns = append([]string(nil), patterns...)
	return r.WithOptions(opts)
}

func (r RedactRequest[T]) Strategy(strategy RedactStrategy) RedactRequest[T] {
	opts := r.opts
	opts.Strategy = strategy
	return r.WithOptions(opts)
}

func (r RedactRequest[T]) Run() (T, error) {
	return Redact[T](r.input, r.opts)
}

// RedactTextRequest is a fluent builder for RedactLLM.
type RedactTextRequest struct {
	opRequest[RedactTextRequest, RedactLLMOptions]
	input string
}

func newRedactTextRequest(input string, opts RedactLLMOptions) RedactTextRequest {
	return RedactTextRequest{
		opRequest: opRequest[RedactTextRequest, RedactLLMOptions]{
			opts: opts,
			lift: func(next RedactLLMOptions) RedactTextRequest {
				return newRedactTextRequest(input, next)
			},
			mutate: func(current RedactLLMOptions, fn func(types.OpOptions) types.OpOptions) RedactLLMOptions {
				current.OpOptions = fn(current.OpOptions)
				return current
			},
		},
		input: input,
	}
}

// LLMRedacting starts a fluent RedactLLM request.
func LLMRedacting(input string) RedactTextRequest {
	return newRedactTextRequest(input, NewRedactLLMOptions())
}

func (r RedactTextRequest) Categories(categories ...string) RedactTextRequest {
	opts := r.opts
	opts.Categories = append([]string(nil), categories...)
	return r.WithOptions(opts)
}

func (r RedactTextRequest) Run() (RedactLLMResult, error) {
	return RedactLLM(r.input, r.opts)
}

// CompleteRequest is a fluent builder for Complete.
type CompleteRequest struct {
	opRequest[CompleteRequest, CompleteOptions]
	input string
}

func newCompleteRequest(input string, opts CompleteOptions) CompleteRequest {
	return CompleteRequest{
		opRequest: opRequest[CompleteRequest, CompleteOptions]{
			opts: opts,
			lift: func(next CompleteOptions) CompleteRequest {
				return newCompleteRequest(input, next)
			},
			mutate: func(current CompleteOptions, fn func(types.OpOptions) types.OpOptions) CompleteOptions {
				current.OpOptions = fn(current.OpOptions)
				return current
			},
		},
		input: input,
	}
}

// Completing starts a fluent Complete request.
func Completing(input string) CompleteRequest {
	return newCompleteRequest(input, NewCompleteOptions())
}

func (r CompleteRequest) MaxLength(max int) CompleteRequest {
	opts := r.opts
	opts.MaxLength = max
	return r.WithOptions(opts)
}

func (r CompleteRequest) Temperature(temperature float32) CompleteRequest {
	opts := r.opts
	opts.Temperature = temperature
	return r.WithOptions(opts)
}

func (r CompleteRequest) Run() (CompleteResult, error) {
	return Complete(r.input, r.opts)
}

// CompleteFieldRequest is a fluent builder for CompleteField.
type CompleteFieldRequest[T any] struct {
	opRequest[CompleteFieldRequest[T], CompleteFieldOptions]
	input T
}

func newCompleteFieldRequest[T any](input T, opts CompleteFieldOptions) CompleteFieldRequest[T] {
	return CompleteFieldRequest[T]{
		opRequest: opRequest[CompleteFieldRequest[T], CompleteFieldOptions]{
			opts: opts,
			lift: func(next CompleteFieldOptions) CompleteFieldRequest[T] {
				return newCompleteFieldRequest(input, next)
			},
			mutate: func(current CompleteFieldOptions, fn func(types.OpOptions) types.OpOptions) CompleteFieldOptions {
				current.OpOptions = fn(current.OpOptions)
				return current
			},
		},
		input: input,
	}
}

// CompletingField starts a fluent CompleteField request.
func CompletingField[T any](input T, fieldName string) CompleteFieldRequest[T] {
	return newCompleteFieldRequest(input, NewCompleteFieldOptions(fieldName))
}

func (r CompleteFieldRequest[T]) MaxLength(max int) CompleteFieldRequest[T] {
	opts := r.opts
	opts.MaxLength = max
	return r.WithOptions(opts)
}

func (r CompleteFieldRequest[T]) Run() (CompleteFieldResult[T], error) {
	return CompleteField[T](r.input, r.opts)
}

// ValidateRequest is a fluent builder for Validate.
type ValidateRequest[T any] struct {
	commonRequest[ValidateRequest[T], ValidateOptions]
	input T
}

func newValidateRequest[T any](input T, opts ValidateOptions) ValidateRequest[T] {
	return ValidateRequest[T]{
		commonRequest: commonRequest[ValidateRequest[T], ValidateOptions]{
			opts: opts,
			lift: func(next ValidateOptions) ValidateRequest[T] {
				return newValidateRequest(input, next)
			},
			mutate: func(current ValidateOptions, fn func(CommonOptions) CommonOptions) ValidateOptions {
				current.CommonOptions = fn(current.CommonOptions)
				return current
			},
		},
		input: input,
	}
}

// Validating starts a fluent Validate request.
func Validating[T any](input T) ValidateRequest[T] {
	return newValidateRequest(input, NewValidateOptions())
}

func (r ValidateRequest[T]) Rules(rules string) ValidateRequest[T] {
	opts := r.opts
	opts.Rules = rules
	return r.WithOptions(opts)
}

func (r ValidateRequest[T]) FailOn(level string) ValidateRequest[T] {
	opts := r.opts
	opts.FailOn = level
	return r.WithOptions(opts)
}

func (r ValidateRequest[T]) AutoCorrect(enabled bool) ValidateRequest[T] {
	opts := r.opts
	opts.AutoCorrect = enabled
	return r.WithOptions(opts)
}

func (r ValidateRequest[T]) Run() (ValidateResult[T], error) {
	return Validate[T](r.input, r.opts)
}

// QuestionRequest is a fluent builder for Question.
type QuestionRequest[T any, A any] struct {
	commonRequest[QuestionRequest[T, A], QuestionOptions]
	input T
}

func newQuestionRequest[T any, A any](input T, opts QuestionOptions) QuestionRequest[T, A] {
	return QuestionRequest[T, A]{
		commonRequest: commonRequest[QuestionRequest[T, A], QuestionOptions]{
			opts: opts,
			lift: func(next QuestionOptions) QuestionRequest[T, A] {
				return newQuestionRequest[T, A](input, next)
			},
			mutate: func(current QuestionOptions, fn func(CommonOptions) CommonOptions) QuestionOptions {
				current.CommonOptions = fn(current.CommonOptions)
				return current
			},
		},
		input: input,
	}
}

// Asking starts a fluent Question request.
func Asking[T any, A any](input T, question string) QuestionRequest[T, A] {
	return newQuestionRequest[T, A](input, NewQuestionOptions(question))
}

func (r QuestionRequest[T, A]) Question(question string) QuestionRequest[T, A] {
	opts := r.opts
	opts.Question = question
	return r.WithOptions(opts)
}

func (r QuestionRequest[T, A]) Run() (QuestionResult[A], error) {
	return Question[T, A](r.input, r.opts)
}
