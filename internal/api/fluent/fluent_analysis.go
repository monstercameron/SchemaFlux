package fluent

import "context"

// ClassifyRequest is a fluent builder for Classify.
type ClassifyRequest[T any, C ~string] struct {
	requestBase[ClassifyRequest[T, C], ClassifyOptions]
	input T
}

func newClassifyRequest[T any, C ~string](input T, opts ClassifyOptions) ClassifyRequest[T, C] {
	return ClassifyRequest[T, C]{
		requestBase: requestBase[ClassifyRequest[T, C], ClassifyOptions]{
			opts: opts,
			lift: func(next ClassifyOptions) ClassifyRequest[T, C] {
				return newClassifyRequest[T, C](input, next)
			},
			setSteering: func(o ClassifyOptions, s string) ClassifyOptions {
				o.CommonOptions = o.CommonOptions.WithSteering(s)
				return o
			},
			setThreshold: func(o ClassifyOptions, v float64) ClassifyOptions {
				o.CommonOptions = o.CommonOptions.WithThreshold(v)
				return o
			},
			setMode: func(o ClassifyOptions, m Mode) ClassifyOptions {
				o.CommonOptions = o.CommonOptions.WithMode(m)
				return o
			},
			setIntelligence: func(o ClassifyOptions, s Speed) ClassifyOptions {
				o.CommonOptions = o.CommonOptions.WithIntelligence(s)
				return o
			},
			setContext: func(o ClassifyOptions, ctx context.Context) ClassifyOptions {
				o.CommonOptions = o.CommonOptions.WithContext(ctx)
				return o
			},
			setRequestID: func(o ClassifyOptions, id string) ClassifyOptions {
				o.CommonOptions = o.CommonOptions.WithRequestID(id)
				return o
			},
			setCorrelationID: func(o ClassifyOptions, id string) ClassifyOptions {
				o.CommonOptions = o.CommonOptions.WithCorrelationID(id)
				return o
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
	if err := buildError(r.opts); err != nil {
		var zero ClassifyResult[C]
		return zero, err
	}
	return Classify[T, C](r.input, r.opts)
}

// ScoreRequest is a fluent builder for Score.
type ScoreRequest[T any] struct {
	requestBase[ScoreRequest[T], ScoreOptions]
	input T
}

func newScoreRequest[T any](input T, opts ScoreOptions) ScoreRequest[T] {
	return ScoreRequest[T]{
		requestBase: requestBase[ScoreRequest[T], ScoreOptions]{
			opts: opts,
			lift: func(next ScoreOptions) ScoreRequest[T] {
				return newScoreRequest[T](input, next)
			},
			setSteering: func(o ScoreOptions, s string) ScoreOptions {
				o.CommonOptions = o.CommonOptions.WithSteering(s)
				return o
			},
			setThreshold: func(o ScoreOptions, v float64) ScoreOptions {
				o.CommonOptions = o.CommonOptions.WithThreshold(v)
				return o
			},
			setMode: func(o ScoreOptions, m Mode) ScoreOptions {
				o.CommonOptions = o.CommonOptions.WithMode(m)
				return o
			},
			setIntelligence: func(o ScoreOptions, s Speed) ScoreOptions {
				o.CommonOptions = o.CommonOptions.WithIntelligence(s)
				return o
			},
			setContext: func(o ScoreOptions, ctx context.Context) ScoreOptions {
				o.CommonOptions = o.CommonOptions.WithContext(ctx)
				return o
			},
			setRequestID: func(o ScoreOptions, id string) ScoreOptions {
				o.CommonOptions = o.CommonOptions.WithRequestID(id)
				return o
			},
			setCorrelationID: func(o ScoreOptions, id string) ScoreOptions {
				o.CommonOptions = o.CommonOptions.WithCorrelationID(id)
				return o
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
	if err := buildError(r.opts); err != nil {
		var zero ScoreResult
		return zero, err
	}
	return Score[T](r.input, r.opts)
}

// CompareRequest is a fluent builder for Compare.
type CompareRequest[T any] struct {
	requestBase[CompareRequest[T], CompareOptions]
	left  T
	right T
}

func newCompareRequest[T any](left, right T, opts CompareOptions) CompareRequest[T] {
	return CompareRequest[T]{
		requestBase: requestBase[CompareRequest[T], CompareOptions]{
			opts: opts,
			lift: func(next CompareOptions) CompareRequest[T] {
				return newCompareRequest(left, right, next)
			},
			setSteering: func(o CompareOptions, s string) CompareOptions {
				o.CommonOptions = o.CommonOptions.WithSteering(s)
				return o
			},
			setThreshold: func(o CompareOptions, v float64) CompareOptions {
				o.CommonOptions = o.CommonOptions.WithThreshold(v)
				return o
			},
			setMode: func(o CompareOptions, m Mode) CompareOptions {
				o.CommonOptions = o.CommonOptions.WithMode(m)
				return o
			},
			setIntelligence: func(o CompareOptions, s Speed) CompareOptions {
				o.CommonOptions = o.CommonOptions.WithIntelligence(s)
				return o
			},
			setContext: func(o CompareOptions, ctx context.Context) CompareOptions {
				o.CommonOptions = o.CommonOptions.WithContext(ctx)
				return o
			},
			setRequestID: func(o CompareOptions, id string) CompareOptions {
				o.CommonOptions = o.CommonOptions.WithRequestID(id)
				return o
			},
			setCorrelationID: func(o CompareOptions, id string) CompareOptions {
				o.CommonOptions = o.CommonOptions.WithCorrelationID(id)
				return o
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
	if err := buildError(r.opts); err != nil {
		var zero CompareResult[T]
		return zero, err
	}
	return Compare[T](r.left, r.right, r.opts)
}

// SimilarRequest is a fluent builder for Similar.
type SimilarRequest[T any] struct {
	requestBase[SimilarRequest[T], SimilarOptions]
	left  T
	right T
}

func newSimilarRequest[T any](left, right T, opts SimilarOptions) SimilarRequest[T] {
	return SimilarRequest[T]{
		requestBase: requestBase[SimilarRequest[T], SimilarOptions]{
			opts: opts,
			lift: func(next SimilarOptions) SimilarRequest[T] {
				return newSimilarRequest(left, right, next)
			},
			setSteering: func(o SimilarOptions, s string) SimilarOptions {
				o.OpOptions.Steering = accumulateSteering(o.OpOptions.Steering, s)
				return o
			},
			setThreshold: func(o SimilarOptions, v float64) SimilarOptions {
				o.OpOptions.Threshold = v
				return o
			},
			setMode: func(o SimilarOptions, m Mode) SimilarOptions {
				o.OpOptions.Mode = m
				return o
			},
			setIntelligence: func(o SimilarOptions, s Speed) SimilarOptions {
				o.OpOptions.Intelligence = s
				return o
			},
			setContext: func(o SimilarOptions, ctx context.Context) SimilarOptions {
				o.OpOptions.Context = ctx
				return o
			},
			setRequestID: func(o SimilarOptions, id string) SimilarOptions {
				o.OpOptions.RequestID = id
				return o
			},
			setCorrelationID: func(o SimilarOptions, id string) SimilarOptions {
				o.OpOptions.CorrelationID = id
				return o
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
	if err := buildError(r.opts); err != nil {
		var zero SimilarResult
		return zero, err
	}
	return Similar[T](r.left, r.right, r.opts)
}

// InferRequest is a fluent builder for Infer.
type InferRequest[T any] struct {
	requestBase[InferRequest[T], InferOptions]
	input T
}

func newInferRequest[T any](input T, opts InferOptions) InferRequest[T] {
	return InferRequest[T]{
		requestBase: requestBase[InferRequest[T], InferOptions]{
			opts: opts,
			lift: func(next InferOptions) InferRequest[T] {
				return newInferRequest(input, next)
			},
			setSteering: func(o InferOptions, s string) InferOptions {
				o.OpOptions.Steering = accumulateSteering(o.OpOptions.Steering, s)
				return o
			},
			setThreshold: func(o InferOptions, v float64) InferOptions {
				o.OpOptions.Threshold = v
				return o
			},
			setMode: func(o InferOptions, m Mode) InferOptions {
				o.OpOptions.Mode = m
				return o
			},
			setIntelligence: func(o InferOptions, s Speed) InferOptions {
				o.OpOptions.Intelligence = s
				return o
			},
			setContext: func(o InferOptions, ctx context.Context) InferOptions {
				o.OpOptions.Context = ctx
				return o
			},
			setRequestID: func(o InferOptions, id string) InferOptions {
				o.OpOptions.RequestID = id
				return o
			},
			setCorrelationID: func(o InferOptions, id string) InferOptions {
				o.OpOptions.CorrelationID = id
				return o
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
	if err := buildError(r.opts); err != nil {
		var zero T
		return zero, err
	}
	return Infer[T](r.input, r.opts)
}

// DiffRequest is a fluent builder for Diff.
type DiffRequest[T any] struct {
	requestBase[DiffRequest[T], DiffOptions]
	oldValue T
	newValue T
}

func newDiffRequest[T any](oldValue, newValue T, opts DiffOptions) DiffRequest[T] {
	return DiffRequest[T]{
		requestBase: requestBase[DiffRequest[T], DiffOptions]{
			opts: opts,
			lift: func(next DiffOptions) DiffRequest[T] {
				return newDiffRequest(oldValue, newValue, next)
			},
			setSteering: func(o DiffOptions, s string) DiffOptions {
				o.OpOptions.Steering = accumulateSteering(o.OpOptions.Steering, s)
				return o
			},
			setThreshold: func(o DiffOptions, v float64) DiffOptions {
				o.OpOptions.Threshold = v
				return o
			},
			setMode: func(o DiffOptions, m Mode) DiffOptions {
				o.OpOptions.Mode = m
				return o
			},
			setIntelligence: func(o DiffOptions, s Speed) DiffOptions {
				o.OpOptions.Intelligence = s
				return o
			},
			setContext: func(o DiffOptions, ctx context.Context) DiffOptions {
				o.OpOptions.Context = ctx
				return o
			},
			setRequestID: func(o DiffOptions, id string) DiffOptions {
				o.OpOptions.RequestID = id
				return o
			},
			setCorrelationID: func(o DiffOptions, id string) DiffOptions {
				o.OpOptions.CorrelationID = id
				return o
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
	if err := buildError(r.opts); err != nil {
		var zero DiffResult
		return zero, err
	}
	return Diff[T](r.oldValue, r.newValue, r.opts)
}

// ExplainRequest is a fluent builder for Explain.
type ExplainRequest struct {
	requestBase[ExplainRequest, ExplainOptions]
	input any
}

func newExplainRequest(input any, opts ExplainOptions) ExplainRequest {
	return ExplainRequest{
		requestBase: requestBase[ExplainRequest, ExplainOptions]{
			opts: opts,
			lift: func(next ExplainOptions) ExplainRequest {
				return newExplainRequest(input, next)
			},
			setSteering: func(o ExplainOptions, s string) ExplainOptions {
				o.OpOptions.Steering = accumulateSteering(o.OpOptions.Steering, s)
				return o
			},
			setThreshold: func(o ExplainOptions, v float64) ExplainOptions {
				o.OpOptions.Threshold = v
				return o
			},
			setMode: func(o ExplainOptions, m Mode) ExplainOptions {
				o.OpOptions.Mode = m
				return o
			},
			setIntelligence: func(o ExplainOptions, s Speed) ExplainOptions {
				o.OpOptions.Intelligence = s
				return o
			},
			setContext: func(o ExplainOptions, ctx context.Context) ExplainOptions {
				o.OpOptions.Context = ctx
				return o
			},
			setRequestID: func(o ExplainOptions, id string) ExplainOptions {
				o.OpOptions.RequestID = id
				return o
			},
			setCorrelationID: func(o ExplainOptions, id string) ExplainOptions {
				o.OpOptions.CorrelationID = id
				return o
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
	if err := buildError(r.opts); err != nil {
		var zero ExplainResult
		return zero, err
	}
	return Explain(r.input, r.opts)
}

// ParseRequest is a fluent builder for Parse.
type ParseRequest[T any] struct {
	requestBase[ParseRequest[T], ParseOptions]
	input any
}

func newParseRequest[T any](input any, opts ParseOptions) ParseRequest[T] {
	return ParseRequest[T]{
		requestBase: requestBase[ParseRequest[T], ParseOptions]{
			opts: opts,
			lift: func(next ParseOptions) ParseRequest[T] {
				return newParseRequest[T](input, next)
			},
			setSteering: func(o ParseOptions, s string) ParseOptions {
				o.OpOptions.Steering = accumulateSteering(o.OpOptions.Steering, s)
				return o
			},
			setThreshold: func(o ParseOptions, v float64) ParseOptions {
				o.OpOptions.Threshold = v
				return o
			},
			setMode: func(o ParseOptions, m Mode) ParseOptions {
				o.OpOptions.Mode = m
				return o
			},
			setIntelligence: func(o ParseOptions, s Speed) ParseOptions {
				o.OpOptions.Intelligence = s
				return o
			},
			setContext: func(o ParseOptions, ctx context.Context) ParseOptions {
				o.OpOptions.Context = ctx
				return o
			},
			setRequestID: func(o ParseOptions, id string) ParseOptions {
				o.OpOptions.RequestID = id
				return o
			},
			setCorrelationID: func(o ParseOptions, id string) ParseOptions {
				o.OpOptions.CorrelationID = id
				return o
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
	if err := buildError(r.opts); err != nil {
		var zero ParseResult[T]
		return zero, err
	}
	return Parse[T](r.input, r.opts)
}

// SummarizeRequest is a fluent builder for Summarize.
type SummarizeRequest struct {
	requestBase[SummarizeRequest, SummarizeOptions]
	input string
}

func newSummarizeRequest(input string, opts SummarizeOptions) SummarizeRequest {
	return SummarizeRequest{
		requestBase: requestBase[SummarizeRequest, SummarizeOptions]{
			opts: opts,
			lift: func(next SummarizeOptions) SummarizeRequest {
				return newSummarizeRequest(input, next)
			},
			setSteering: func(o SummarizeOptions, s string) SummarizeOptions {
				o.CommonOptions = o.CommonOptions.WithSteering(s)
				return o
			},
			setThreshold: func(o SummarizeOptions, v float64) SummarizeOptions {
				o.CommonOptions = o.CommonOptions.WithThreshold(v)
				return o
			},
			setMode: func(o SummarizeOptions, m Mode) SummarizeOptions {
				o.CommonOptions = o.CommonOptions.WithMode(m)
				return o
			},
			setIntelligence: func(o SummarizeOptions, s Speed) SummarizeOptions {
				o.CommonOptions = o.CommonOptions.WithIntelligence(s)
				return o
			},
			setContext: func(o SummarizeOptions, ctx context.Context) SummarizeOptions {
				o.CommonOptions = o.CommonOptions.WithContext(ctx)
				return o
			},
			setRequestID: func(o SummarizeOptions, id string) SummarizeOptions {
				o.CommonOptions = o.CommonOptions.WithRequestID(id)
				return o
			},
			setCorrelationID: func(o SummarizeOptions, id string) SummarizeOptions {
				o.CommonOptions = o.CommonOptions.WithCorrelationID(id)
				return o
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
	if err := buildError(r.opts); err != nil {
		var zero string
		return zero, err
	}
	return Summarize(r.input, r.opts)
}

func (r SummarizeRequest) RunDetailed() (Summary, error) {
	return SummarizeWithMetadata(r.input, r.opts)
}

// RewriteRequest is a fluent builder for Rewrite.
type RewriteRequest struct {
	requestBase[RewriteRequest, RewriteOptions]
	input string
}

func newRewriteRequest(input string, opts RewriteOptions) RewriteRequest {
	return RewriteRequest{
		requestBase: requestBase[RewriteRequest, RewriteOptions]{
			opts: opts,
			lift: func(next RewriteOptions) RewriteRequest {
				return newRewriteRequest(input, next)
			},
			setSteering: func(o RewriteOptions, s string) RewriteOptions {
				o.CommonOptions = o.CommonOptions.WithSteering(s)
				return o
			},
			setThreshold: func(o RewriteOptions, v float64) RewriteOptions {
				o.CommonOptions = o.CommonOptions.WithThreshold(v)
				return o
			},
			setMode: func(o RewriteOptions, m Mode) RewriteOptions {
				o.CommonOptions = o.CommonOptions.WithMode(m)
				return o
			},
			setIntelligence: func(o RewriteOptions, s Speed) RewriteOptions {
				o.CommonOptions = o.CommonOptions.WithIntelligence(s)
				return o
			},
			setContext: func(o RewriteOptions, ctx context.Context) RewriteOptions {
				o.CommonOptions = o.CommonOptions.WithContext(ctx)
				return o
			},
			setRequestID: func(o RewriteOptions, id string) RewriteOptions {
				o.CommonOptions = o.CommonOptions.WithRequestID(id)
				return o
			},
			setCorrelationID: func(o RewriteOptions, id string) RewriteOptions {
				o.CommonOptions = o.CommonOptions.WithCorrelationID(id)
				return o
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
	if err := buildError(r.opts); err != nil {
		var zero string
		return zero, err
	}
	return Rewrite(r.input, r.opts)
}

func (r RewriteRequest) RunDetailed() (Rewritten, error) {
	return RewriteWithMetadata(r.input, r.opts)
}

// TranslateRequest is a fluent builder for Translate.
type TranslateRequest struct {
	requestBase[TranslateRequest, TranslateOptions]
	input string
}

func newTranslateRequest(input string, opts TranslateOptions) TranslateRequest {
	return TranslateRequest{
		requestBase: requestBase[TranslateRequest, TranslateOptions]{
			opts: opts,
			lift: func(next TranslateOptions) TranslateRequest {
				return newTranslateRequest(input, next)
			},
			setSteering: func(o TranslateOptions, s string) TranslateOptions {
				o.CommonOptions = o.CommonOptions.WithSteering(s)
				return o
			},
			setThreshold: func(o TranslateOptions, v float64) TranslateOptions {
				o.CommonOptions = o.CommonOptions.WithThreshold(v)
				return o
			},
			setMode: func(o TranslateOptions, m Mode) TranslateOptions {
				o.CommonOptions = o.CommonOptions.WithMode(m)
				return o
			},
			setIntelligence: func(o TranslateOptions, s Speed) TranslateOptions {
				o.CommonOptions = o.CommonOptions.WithIntelligence(s)
				return o
			},
			setContext: func(o TranslateOptions, ctx context.Context) TranslateOptions {
				o.CommonOptions = o.CommonOptions.WithContext(ctx)
				return o
			},
			setRequestID: func(o TranslateOptions, id string) TranslateOptions {
				o.CommonOptions = o.CommonOptions.WithRequestID(id)
				return o
			},
			setCorrelationID: func(o TranslateOptions, id string) TranslateOptions {
				o.CommonOptions = o.CommonOptions.WithCorrelationID(id)
				return o
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
	if err := buildError(r.opts); err != nil {
		var zero string
		return zero, err
	}
	return Translate(r.input, r.opts)
}

func (r TranslateRequest) RunDetailed() (Translation, error) {
	return TranslateWithMetadata(r.input, r.opts)
}

// ExpandRequest is a fluent builder for Expand.
type ExpandRequest struct {
	requestBase[ExpandRequest, ExpandOptions]
	input string
}

func newExpandRequest(input string, opts ExpandOptions) ExpandRequest {
	return ExpandRequest{
		requestBase: requestBase[ExpandRequest, ExpandOptions]{
			opts: opts,
			lift: func(next ExpandOptions) ExpandRequest {
				return newExpandRequest(input, next)
			},
			setSteering: func(o ExpandOptions, s string) ExpandOptions {
				o.CommonOptions = o.CommonOptions.WithSteering(s)
				return o
			},
			setThreshold: func(o ExpandOptions, v float64) ExpandOptions {
				o.CommonOptions = o.CommonOptions.WithThreshold(v)
				return o
			},
			setMode: func(o ExpandOptions, m Mode) ExpandOptions {
				o.CommonOptions = o.CommonOptions.WithMode(m)
				return o
			},
			setIntelligence: func(o ExpandOptions, s Speed) ExpandOptions {
				o.CommonOptions = o.CommonOptions.WithIntelligence(s)
				return o
			},
			setContext: func(o ExpandOptions, ctx context.Context) ExpandOptions {
				o.CommonOptions = o.CommonOptions.WithContext(ctx)
				return o
			},
			setRequestID: func(o ExpandOptions, id string) ExpandOptions {
				o.CommonOptions = o.CommonOptions.WithRequestID(id)
				return o
			},
			setCorrelationID: func(o ExpandOptions, id string) ExpandOptions {
				o.CommonOptions = o.CommonOptions.WithCorrelationID(id)
				return o
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
	if err := buildError(r.opts); err != nil {
		var zero string
		return zero, err
	}
	return Expand(r.input, r.opts)
}

func (r ExpandRequest) RunDetailed() (Expansion, error) {
	return ExpandWithMetadata(r.input, r.opts)
}

// SuggestRequest is a fluent builder for Suggest.
type SuggestRequest[T any] struct {
	requestBase[SuggestRequest[T], SuggestOptions]
	input any
}

func newSuggestRequest[T any](input any, opts SuggestOptions) SuggestRequest[T] {
	return SuggestRequest[T]{
		requestBase: requestBase[SuggestRequest[T], SuggestOptions]{
			opts: opts,
			lift: func(next SuggestOptions) SuggestRequest[T] {
				return newSuggestRequest[T](input, next)
			},
			setSteering: func(o SuggestOptions, s string) SuggestOptions {
				o.CommonOptions = o.CommonOptions.WithSteering(s)
				return o
			},
			setThreshold: func(o SuggestOptions, v float64) SuggestOptions {
				o.CommonOptions = o.CommonOptions.WithThreshold(v)
				return o
			},
			setMode: func(o SuggestOptions, m Mode) SuggestOptions {
				o.CommonOptions = o.CommonOptions.WithMode(m)
				return o
			},
			setIntelligence: func(o SuggestOptions, s Speed) SuggestOptions {
				o.CommonOptions = o.CommonOptions.WithIntelligence(s)
				return o
			},
			setContext: func(o SuggestOptions, ctx context.Context) SuggestOptions {
				o.CommonOptions = o.CommonOptions.WithContext(ctx)
				return o
			},
			setRequestID: func(o SuggestOptions, id string) SuggestOptions {
				o.CommonOptions = o.CommonOptions.WithRequestID(id)
				return o
			},
			setCorrelationID: func(o SuggestOptions, id string) SuggestOptions {
				o.CommonOptions = o.CommonOptions.WithCorrelationID(id)
				return o
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
	if err := buildError(r.opts); err != nil {
		var zero []T
		return zero, err
	}
	return Suggest[T](r.input, r.opts)
}

// RedactRequest is a fluent builder for Redact.
type RedactRequest[T any] struct {
	requestBase[RedactRequest[T], RedactOptions]
	input T
}

func newRedactRequest[T any](input T, opts RedactOptions) RedactRequest[T] {
	return RedactRequest[T]{
		requestBase: requestBase[RedactRequest[T], RedactOptions]{
			opts: opts,
			lift: func(next RedactOptions) RedactRequest[T] {
				return newRedactRequest(input, next)
			},
			setSteering: func(o RedactOptions, s string) RedactOptions {
				o.OpOptions.Steering = accumulateSteering(o.OpOptions.Steering, s)
				return o
			},
			setThreshold: func(o RedactOptions, v float64) RedactOptions {
				o.OpOptions.Threshold = v
				return o
			},
			setMode: func(o RedactOptions, m Mode) RedactOptions {
				o.OpOptions.Mode = m
				return o
			},
			setIntelligence: func(o RedactOptions, s Speed) RedactOptions {
				o.OpOptions.Intelligence = s
				return o
			},
			setContext: func(o RedactOptions, ctx context.Context) RedactOptions {
				o.OpOptions.Context = ctx
				return o
			},
			setRequestID: func(o RedactOptions, id string) RedactOptions {
				o.OpOptions.RequestID = id
				return o
			},
			setCorrelationID: func(o RedactOptions, id string) RedactOptions {
				o.OpOptions.CorrelationID = id
				return o
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
	if err := buildError(r.opts); err != nil {
		var zero T
		return zero, err
	}
	return Redact[T](r.input, r.opts)
}

// RedactTextRequest is a fluent builder for RedactLLM.
type RedactTextRequest struct {
	requestBase[RedactTextRequest, RedactLLMOptions]
	input string
}

func newRedactTextRequest(input string, opts RedactLLMOptions) RedactTextRequest {
	return RedactTextRequest{
		requestBase: requestBase[RedactTextRequest, RedactLLMOptions]{
			opts: opts,
			lift: func(next RedactLLMOptions) RedactTextRequest {
				return newRedactTextRequest(input, next)
			},
			setSteering: func(o RedactLLMOptions, s string) RedactLLMOptions {
				o.OpOptions.Steering = accumulateSteering(o.OpOptions.Steering, s)
				return o
			},
			setThreshold: func(o RedactLLMOptions, v float64) RedactLLMOptions {
				o.OpOptions.Threshold = v
				return o
			},
			setMode: func(o RedactLLMOptions, m Mode) RedactLLMOptions {
				o.OpOptions.Mode = m
				return o
			},
			setIntelligence: func(o RedactLLMOptions, s Speed) RedactLLMOptions {
				o.OpOptions.Intelligence = s
				return o
			},
			setContext: func(o RedactLLMOptions, ctx context.Context) RedactLLMOptions {
				o.OpOptions.Context = ctx
				return o
			},
			setRequestID: func(o RedactLLMOptions, id string) RedactLLMOptions {
				o.OpOptions.RequestID = id
				return o
			},
			setCorrelationID: func(o RedactLLMOptions, id string) RedactLLMOptions {
				o.OpOptions.CorrelationID = id
				return o
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
	if err := buildError(r.opts); err != nil {
		var zero RedactLLMResult
		return zero, err
	}
	return RedactLLM(r.input, r.opts)
}

// CompleteRequest is a fluent builder for Complete.
type CompleteRequest struct {
	requestBase[CompleteRequest, CompleteOptions]
	input string
}

func newCompleteRequest(input string, opts CompleteOptions) CompleteRequest {
	return CompleteRequest{
		requestBase: requestBase[CompleteRequest, CompleteOptions]{
			opts: opts,
			lift: func(next CompleteOptions) CompleteRequest {
				return newCompleteRequest(input, next)
			},
			setSteering: func(o CompleteOptions, s string) CompleteOptions {
				o.OpOptions.Steering = accumulateSteering(o.OpOptions.Steering, s)
				return o
			},
			setThreshold: func(o CompleteOptions, v float64) CompleteOptions {
				o.OpOptions.Threshold = v
				return o
			},
			setMode: func(o CompleteOptions, m Mode) CompleteOptions {
				o.OpOptions.Mode = m
				return o
			},
			setIntelligence: func(o CompleteOptions, s Speed) CompleteOptions {
				o.OpOptions.Intelligence = s
				return o
			},
			setContext: func(o CompleteOptions, ctx context.Context) CompleteOptions {
				o.OpOptions.Context = ctx
				return o
			},
			setRequestID: func(o CompleteOptions, id string) CompleteOptions {
				o.OpOptions.RequestID = id
				return o
			},
			setCorrelationID: func(o CompleteOptions, id string) CompleteOptions {
				o.OpOptions.CorrelationID = id
				return o
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
	if err := buildError(r.opts); err != nil {
		var zero CompleteResult
		return zero, err
	}
	return Complete(r.input, r.opts)
}

// CompleteFieldRequest is a fluent builder for CompleteField.
type CompleteFieldRequest[T any] struct {
	requestBase[CompleteFieldRequest[T], CompleteFieldOptions]
	input T
}

func newCompleteFieldRequest[T any](input T, opts CompleteFieldOptions) CompleteFieldRequest[T] {
	return CompleteFieldRequest[T]{
		requestBase: requestBase[CompleteFieldRequest[T], CompleteFieldOptions]{
			opts: opts,
			lift: func(next CompleteFieldOptions) CompleteFieldRequest[T] {
				return newCompleteFieldRequest(input, next)
			},
			setSteering: func(o CompleteFieldOptions, s string) CompleteFieldOptions {
				o.OpOptions.Steering = accumulateSteering(o.OpOptions.Steering, s)
				return o
			},
			setThreshold: func(o CompleteFieldOptions, v float64) CompleteFieldOptions {
				o.OpOptions.Threshold = v
				return o
			},
			setMode: func(o CompleteFieldOptions, m Mode) CompleteFieldOptions {
				o.OpOptions.Mode = m
				return o
			},
			setIntelligence: func(o CompleteFieldOptions, s Speed) CompleteFieldOptions {
				o.OpOptions.Intelligence = s
				return o
			},
			setContext: func(o CompleteFieldOptions, ctx context.Context) CompleteFieldOptions {
				o.OpOptions.Context = ctx
				return o
			},
			setRequestID: func(o CompleteFieldOptions, id string) CompleteFieldOptions {
				o.OpOptions.RequestID = id
				return o
			},
			setCorrelationID: func(o CompleteFieldOptions, id string) CompleteFieldOptions {
				o.OpOptions.CorrelationID = id
				return o
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
	if err := buildError(r.opts); err != nil {
		var zero CompleteFieldResult[T]
		return zero, err
	}
	return CompleteField[T](r.input, r.opts)
}

// ValidateRequest is a fluent builder for Validate.
type ValidateRequest[T any] struct {
	requestBase[ValidateRequest[T], ValidateOptions]
	input T
}

func newValidateRequest[T any](input T, opts ValidateOptions) ValidateRequest[T] {
	return ValidateRequest[T]{
		requestBase: requestBase[ValidateRequest[T], ValidateOptions]{
			opts: opts,
			lift: func(next ValidateOptions) ValidateRequest[T] {
				return newValidateRequest(input, next)
			},
			setSteering: func(o ValidateOptions, s string) ValidateOptions {
				o.CommonOptions = o.CommonOptions.WithSteering(s)
				return o
			},
			setThreshold: func(o ValidateOptions, v float64) ValidateOptions {
				o.CommonOptions = o.CommonOptions.WithThreshold(v)
				return o
			},
			setMode: func(o ValidateOptions, m Mode) ValidateOptions {
				o.CommonOptions = o.CommonOptions.WithMode(m)
				return o
			},
			setIntelligence: func(o ValidateOptions, s Speed) ValidateOptions {
				o.CommonOptions = o.CommonOptions.WithIntelligence(s)
				return o
			},
			setContext: func(o ValidateOptions, ctx context.Context) ValidateOptions {
				o.CommonOptions = o.CommonOptions.WithContext(ctx)
				return o
			},
			setRequestID: func(o ValidateOptions, id string) ValidateOptions {
				o.CommonOptions = o.CommonOptions.WithRequestID(id)
				return o
			},
			setCorrelationID: func(o ValidateOptions, id string) ValidateOptions {
				o.CommonOptions = o.CommonOptions.WithCorrelationID(id)
				return o
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
	if err := buildError(r.opts); err != nil {
		var zero ValidateResult[T]
		return zero, err
	}
	return Validate[T](r.input, r.opts)
}

// QuestionRequest is a fluent builder for Question.
type QuestionRequest[T any, A any] struct {
	requestBase[QuestionRequest[T, A], QuestionOptions]
	input T
}

func newQuestionRequest[T any, A any](input T, opts QuestionOptions) QuestionRequest[T, A] {
	return QuestionRequest[T, A]{
		requestBase: requestBase[QuestionRequest[T, A], QuestionOptions]{
			opts: opts,
			lift: func(next QuestionOptions) QuestionRequest[T, A] {
				return newQuestionRequest[T, A](input, next)
			},
			setSteering: func(o QuestionOptions, s string) QuestionOptions {
				o.CommonOptions = o.CommonOptions.WithSteering(s)
				return o
			},
			setThreshold: func(o QuestionOptions, v float64) QuestionOptions {
				o.CommonOptions = o.CommonOptions.WithThreshold(v)
				return o
			},
			setMode: func(o QuestionOptions, m Mode) QuestionOptions {
				o.CommonOptions = o.CommonOptions.WithMode(m)
				return o
			},
			setIntelligence: func(o QuestionOptions, s Speed) QuestionOptions {
				o.CommonOptions = o.CommonOptions.WithIntelligence(s)
				return o
			},
			setContext: func(o QuestionOptions, ctx context.Context) QuestionOptions {
				o.CommonOptions = o.CommonOptions.WithContext(ctx)
				return o
			},
			setRequestID: func(o QuestionOptions, id string) QuestionOptions {
				o.CommonOptions = o.CommonOptions.WithRequestID(id)
				return o
			},
			setCorrelationID: func(o QuestionOptions, id string) QuestionOptions {
				o.CommonOptions = o.CommonOptions.WithCorrelationID(id)
				return o
			},
		},
		input: input,
	}
}

// Asking starts a fluent Question request.
//
// Despite the name, Run resolves it in a single model round trip -- see
// ops.Question's doc comment for what that means and where the real
// multi-turn primitive (ops.Session) lives (PS-005, Revised ARC-20).
func Asking[T any, A any](input T, question string) QuestionRequest[T, A] {
	return newQuestionRequest[T, A](input, NewQuestionOptions(question))
}

func (r QuestionRequest[T, A]) Question(question string) QuestionRequest[T, A] {
	opts := r.opts
	opts.Question = question
	return r.WithOptions(opts)
}

func (r QuestionRequest[T, A]) Run() (QuestionResult[A], error) {
	if err := buildError(r.opts); err != nil {
		var zero QuestionResult[A]
		return zero, err
	}
	return Question[T, A](r.input, r.opts)
}
