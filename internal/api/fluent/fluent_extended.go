package fluent

import "context"

// AnnotateRequest is a fluent builder for Annotate.
type AnnotateRequest[T any] struct {
	requestBase[AnnotateRequest[T], AnnotateOptions]
	input T
}

func newAnnotateRequest[T any](input T, opts AnnotateOptions) AnnotateRequest[T] {
	return AnnotateRequest[T]{
		requestBase: requestBase[AnnotateRequest[T], AnnotateOptions]{
			opts: opts,
			lift: func(next AnnotateOptions) AnnotateRequest[T] {
				return newAnnotateRequest(input, next)
			},
			setSteering: func(o AnnotateOptions, s string) AnnotateOptions {
				o.CommonOptions = o.CommonOptions.WithSteering(s)
				return o
			},
			setThreshold: func(o AnnotateOptions, v float64) AnnotateOptions {
				o.CommonOptions = o.CommonOptions.WithThreshold(v)
				return o
			},
			setMode: func(o AnnotateOptions, m Mode) AnnotateOptions {
				o.CommonOptions = o.CommonOptions.WithMode(m)
				return o
			},
			setIntelligence: func(o AnnotateOptions, s Speed) AnnotateOptions {
				o.CommonOptions = o.CommonOptions.WithIntelligence(s)
				return o
			},
			setModel: func(o AnnotateOptions, model string) AnnotateOptions {
				o.CommonOptions = o.CommonOptions.WithModel(model)
				return o
			},
			setContext: func(o AnnotateOptions, ctx context.Context) AnnotateOptions {
				o.CommonOptions = o.CommonOptions.WithContext(ctx)
				return o
			},
			setRequestID: func(o AnnotateOptions, id string) AnnotateOptions {
				o.CommonOptions = o.CommonOptions.WithRequestID(id)
				return o
			},
			setCorrelationID: func(o AnnotateOptions, id string) AnnotateOptions {
				o.CommonOptions = o.CommonOptions.WithCorrelationID(id)
				return o
			},
		},
		input: input,
	}
}

// Annotating starts a fluent Annotate request.
func Annotating[T any](input T) AnnotateRequest[T] {
	return newAnnotateRequest(input, NewAnnotateOptions())
}

func (r AnnotateRequest[T]) Types(annotationTypes ...string) AnnotateRequest[T] {
	opts := r.opts
	opts.AnnotationTypes = append([]string(nil), annotationTypes...)
	return r.WithOptions(opts)
}

func (r AnnotateRequest[T]) Run(ctx ...context.Context) (AnnotateResult, error) {
	r.opts = r.optsWithRunContext(ctx)
	if err := buildError(r.opts); err != nil {
		var zero AnnotateResult
		return zero, err
	}
	return Annotate[T](r.input, r.opts)
}

// ClusterRequest is a fluent builder for Cluster.
type ClusterRequest[T any] struct {
	requestBase[ClusterRequest[T], ClusterOptions]
	items []T
}

func newClusterRequest[T any](items []T, opts ClusterOptions) ClusterRequest[T] {
	return ClusterRequest[T]{
		requestBase: requestBase[ClusterRequest[T], ClusterOptions]{
			opts: opts,
			lift: func(next ClusterOptions) ClusterRequest[T] {
				return newClusterRequest(items, next)
			},
			setSteering: func(o ClusterOptions, s string) ClusterOptions {
				o.CommonOptions = o.CommonOptions.WithSteering(s)
				return o
			},
			setThreshold: func(o ClusterOptions, v float64) ClusterOptions {
				o.CommonOptions = o.CommonOptions.WithThreshold(v)
				return o
			},
			setMode: func(o ClusterOptions, m Mode) ClusterOptions {
				o.CommonOptions = o.CommonOptions.WithMode(m)
				return o
			},
			setIntelligence: func(o ClusterOptions, s Speed) ClusterOptions {
				o.CommonOptions = o.CommonOptions.WithIntelligence(s)
				return o
			},
			setModel: func(o ClusterOptions, model string) ClusterOptions {
				o.CommonOptions = o.CommonOptions.WithModel(model)
				return o
			},
			setContext: func(o ClusterOptions, ctx context.Context) ClusterOptions {
				o.CommonOptions = o.CommonOptions.WithContext(ctx)
				return o
			},
			setRequestID: func(o ClusterOptions, id string) ClusterOptions {
				o.CommonOptions = o.CommonOptions.WithRequestID(id)
				return o
			},
			setCorrelationID: func(o ClusterOptions, id string) ClusterOptions {
				o.CommonOptions = o.CommonOptions.WithCorrelationID(id)
				return o
			},
		},
		items: items,
	}
}

// Clustering starts a fluent Cluster request.
func Clustering[T any](items []T) ClusterRequest[T] {
	return newClusterRequest(items, NewClusterOptions())
}

func (r ClusterRequest[T]) By(criteria string) ClusterRequest[T] {
	opts := r.opts
	opts.ClusterBy = criteria
	return r.WithOptions(opts)
}

func (r ClusterRequest[T]) Clusters(n int) ClusterRequest[T] {
	opts := r.opts
	opts.NumClusters = n
	return r.WithOptions(opts)
}

func (r ClusterRequest[T]) Run(ctx ...context.Context) (ClusterResult[T], error) {
	r.opts = r.optsWithRunContext(ctx)
	if err := buildError(r.opts); err != nil {
		var zero ClusterResult[T]
		return zero, err
	}
	return Cluster[T](r.items, r.opts)
}

// RankRequest is a fluent builder for Rank.
type RankRequest[T any] struct {
	requestBase[RankRequest[T], RankOptions]
	items []T
}

func newRankRequest[T any](items []T, opts RankOptions) RankRequest[T] {
	return RankRequest[T]{
		requestBase: requestBase[RankRequest[T], RankOptions]{
			opts: opts,
			lift: func(next RankOptions) RankRequest[T] {
				return newRankRequest(items, next)
			},
			setSteering: func(o RankOptions, s string) RankOptions {
				o.CommonOptions = o.CommonOptions.WithSteering(s)
				return o
			},
			setThreshold: func(o RankOptions, v float64) RankOptions {
				o.CommonOptions = o.CommonOptions.WithThreshold(v)
				return o
			},
			setMode: func(o RankOptions, m Mode) RankOptions {
				o.CommonOptions = o.CommonOptions.WithMode(m)
				return o
			},
			setIntelligence: func(o RankOptions, s Speed) RankOptions {
				o.CommonOptions = o.CommonOptions.WithIntelligence(s)
				return o
			},
			setModel: func(o RankOptions, model string) RankOptions {
				o.CommonOptions = o.CommonOptions.WithModel(model)
				return o
			},
			setContext: func(o RankOptions, ctx context.Context) RankOptions {
				o.CommonOptions = o.CommonOptions.WithContext(ctx)
				return o
			},
			setRequestID: func(o RankOptions, id string) RankOptions {
				o.CommonOptions = o.CommonOptions.WithRequestID(id)
				return o
			},
			setCorrelationID: func(o RankOptions, id string) RankOptions {
				o.CommonOptions = o.CommonOptions.WithCorrelationID(id)
				return o
			},
		},
		items: items,
	}
}

// Ranking starts a fluent Rank request.
func Ranking[T any](items []T) RankRequest[T] {
	return newRankRequest(items, NewRankOptions())
}

func (r RankRequest[T]) By(query string) RankRequest[T] {
	opts := r.opts
	opts.Query = query
	return r.WithOptions(opts)
}

func (r RankRequest[T]) Top(n int) RankRequest[T] {
	opts := r.opts
	opts.TopK = n
	return r.WithOptions(opts)
}

func (r RankRequest[T]) MinScore(score float64) RankRequest[T] {
	opts := r.opts
	opts.MinScore = score
	return r.WithOptions(opts)
}

func (r RankRequest[T]) Run(ctx ...context.Context) (RankResult[T], error) {
	r.opts = r.optsWithRunContext(ctx)
	if err := buildError(r.opts); err != nil {
		var zero RankResult[T]
		return zero, err
	}
	return Rank[T](r.items, r.opts)
}

// CompressRequest is a fluent builder for Compress.
type CompressRequest[T any] struct {
	requestBase[CompressRequest[T], CompressOptions]
	input T
}

func newCompressRequest[T any](input T, opts CompressOptions) CompressRequest[T] {
	return CompressRequest[T]{
		requestBase: requestBase[CompressRequest[T], CompressOptions]{
			opts: opts,
			lift: func(next CompressOptions) CompressRequest[T] {
				return newCompressRequest(input, next)
			},
			setSteering: func(o CompressOptions, s string) CompressOptions {
				o.CommonOptions = o.CommonOptions.WithSteering(s)
				return o
			},
			setThreshold: func(o CompressOptions, v float64) CompressOptions {
				o.CommonOptions = o.CommonOptions.WithThreshold(v)
				return o
			},
			setMode: func(o CompressOptions, m Mode) CompressOptions {
				o.CommonOptions = o.CommonOptions.WithMode(m)
				return o
			},
			setIntelligence: func(o CompressOptions, s Speed) CompressOptions {
				o.CommonOptions = o.CommonOptions.WithIntelligence(s)
				return o
			},
			setModel: func(o CompressOptions, model string) CompressOptions {
				o.CommonOptions = o.CommonOptions.WithModel(model)
				return o
			},
			setContext: func(o CompressOptions, ctx context.Context) CompressOptions {
				o.CommonOptions = o.CommonOptions.WithContext(ctx)
				return o
			},
			setRequestID: func(o CompressOptions, id string) CompressOptions {
				o.CommonOptions = o.CommonOptions.WithRequestID(id)
				return o
			},
			setCorrelationID: func(o CompressOptions, id string) CompressOptions {
				o.CommonOptions = o.CommonOptions.WithCorrelationID(id)
				return o
			},
		},
		input: input,
	}
}

// Compressing starts a fluent Compress request.
func Compressing[T any](input T) CompressRequest[T] {
	return newCompressRequest(input, NewCompressOptions())
}

func (r CompressRequest[T]) Run(ctx ...context.Context) (CompressResult[T], error) {
	r.opts = r.optsWithRunContext(ctx)
	if err := buildError(r.opts); err != nil {
		var zero CompressResult[T]
		return zero, err
	}
	return Compress[T](r.input, r.opts)
}

// CompressTextRequest is a fluent builder for CompressText.
type CompressTextRequest struct {
	requestBase[CompressTextRequest, CompressOptions]
	input string
}

func newCompressTextRequest(input string, opts CompressOptions) CompressTextRequest {
	return CompressTextRequest{
		requestBase: requestBase[CompressTextRequest, CompressOptions]{
			opts: opts,
			lift: func(next CompressOptions) CompressTextRequest {
				return newCompressTextRequest(input, next)
			},
			setSteering: func(o CompressOptions, s string) CompressOptions {
				o.CommonOptions = o.CommonOptions.WithSteering(s)
				return o
			},
			setThreshold: func(o CompressOptions, v float64) CompressOptions {
				o.CommonOptions = o.CommonOptions.WithThreshold(v)
				return o
			},
			setMode: func(o CompressOptions, m Mode) CompressOptions {
				o.CommonOptions = o.CommonOptions.WithMode(m)
				return o
			},
			setIntelligence: func(o CompressOptions, s Speed) CompressOptions {
				o.CommonOptions = o.CommonOptions.WithIntelligence(s)
				return o
			},
			setModel: func(o CompressOptions, model string) CompressOptions {
				o.CommonOptions = o.CommonOptions.WithModel(model)
				return o
			},
			setContext: func(o CompressOptions, ctx context.Context) CompressOptions {
				o.CommonOptions = o.CommonOptions.WithContext(ctx)
				return o
			},
			setRequestID: func(o CompressOptions, id string) CompressOptions {
				o.CommonOptions = o.CommonOptions.WithRequestID(id)
				return o
			},
			setCorrelationID: func(o CompressOptions, id string) CompressOptions {
				o.CommonOptions = o.CommonOptions.WithCorrelationID(id)
				return o
			},
		},
		input: input,
	}
}

// CompressingText starts a fluent CompressText request.
func CompressingText(input string) CompressTextRequest {
	return newCompressTextRequest(input, NewCompressOptions())
}

func (r CompressTextRequest) Run(ctx ...context.Context) (string, error) {
	r.opts = r.optsWithRunContext(ctx)
	if err := buildError(r.opts); err != nil {
		var zero string
		return zero, err
	}
	return CompressText(r.input, r.opts)
}

// DecomposeRequest is a fluent builder for Decompose.
type DecomposeRequest[T any] struct {
	requestBase[DecomposeRequest[T], DecomposeOptions]
	input T
}

func newDecomposeRequest[T any](input T, opts DecomposeOptions) DecomposeRequest[T] {
	return DecomposeRequest[T]{
		requestBase: requestBase[DecomposeRequest[T], DecomposeOptions]{
			opts: opts,
			lift: func(next DecomposeOptions) DecomposeRequest[T] {
				return newDecomposeRequest(input, next)
			},
			setSteering: func(o DecomposeOptions, s string) DecomposeOptions {
				o.CommonOptions = o.CommonOptions.WithSteering(s)
				return o
			},
			setThreshold: func(o DecomposeOptions, v float64) DecomposeOptions {
				o.CommonOptions = o.CommonOptions.WithThreshold(v)
				return o
			},
			setMode: func(o DecomposeOptions, m Mode) DecomposeOptions {
				o.CommonOptions = o.CommonOptions.WithMode(m)
				return o
			},
			setIntelligence: func(o DecomposeOptions, s Speed) DecomposeOptions {
				o.CommonOptions = o.CommonOptions.WithIntelligence(s)
				return o
			},
			setModel: func(o DecomposeOptions, model string) DecomposeOptions {
				o.CommonOptions = o.CommonOptions.WithModel(model)
				return o
			},
			setContext: func(o DecomposeOptions, ctx context.Context) DecomposeOptions {
				o.CommonOptions = o.CommonOptions.WithContext(ctx)
				return o
			},
			setRequestID: func(o DecomposeOptions, id string) DecomposeOptions {
				o.CommonOptions = o.CommonOptions.WithRequestID(id)
				return o
			},
			setCorrelationID: func(o DecomposeOptions, id string) DecomposeOptions {
				o.CommonOptions = o.CommonOptions.WithCorrelationID(id)
				return o
			},
		},
		input: input,
	}
}

// Decomposing starts a fluent Decompose request.
func Decomposing[T any](input T) DecomposeRequest[T] {
	return newDecomposeRequest(input, NewDecomposeOptions())
}

func (r DecomposeRequest[T]) Run(ctx ...context.Context) (DecomposeResult[T], error) {
	r.opts = r.optsWithRunContext(ctx)
	if err := buildError(r.opts); err != nil {
		var zero DecomposeResult[T]
		return zero, err
	}
	return Decompose[T](r.input, r.opts)
}

// DecomposeSliceRequest is a fluent builder for DecomposeToSlice.
type DecomposeSliceRequest[T any, U any] struct {
	requestBase[DecomposeSliceRequest[T, U], DecomposeOptions]
	input T
}

func newDecomposeSliceRequest[T any, U any](input T, opts DecomposeOptions) DecomposeSliceRequest[T, U] {
	return DecomposeSliceRequest[T, U]{
		requestBase: requestBase[DecomposeSliceRequest[T, U], DecomposeOptions]{
			opts: opts,
			lift: func(next DecomposeOptions) DecomposeSliceRequest[T, U] {
				return newDecomposeSliceRequest[T, U](input, next)
			},
			setSteering: func(o DecomposeOptions, s string) DecomposeOptions {
				o.CommonOptions = o.CommonOptions.WithSteering(s)
				return o
			},
			setThreshold: func(o DecomposeOptions, v float64) DecomposeOptions {
				o.CommonOptions = o.CommonOptions.WithThreshold(v)
				return o
			},
			setMode: func(o DecomposeOptions, m Mode) DecomposeOptions {
				o.CommonOptions = o.CommonOptions.WithMode(m)
				return o
			},
			setIntelligence: func(o DecomposeOptions, s Speed) DecomposeOptions {
				o.CommonOptions = o.CommonOptions.WithIntelligence(s)
				return o
			},
			setModel: func(o DecomposeOptions, model string) DecomposeOptions {
				o.CommonOptions = o.CommonOptions.WithModel(model)
				return o
			},
			setContext: func(o DecomposeOptions, ctx context.Context) DecomposeOptions {
				o.CommonOptions = o.CommonOptions.WithContext(ctx)
				return o
			},
			setRequestID: func(o DecomposeOptions, id string) DecomposeOptions {
				o.CommonOptions = o.CommonOptions.WithRequestID(id)
				return o
			},
			setCorrelationID: func(o DecomposeOptions, id string) DecomposeOptions {
				o.CommonOptions = o.CommonOptions.WithCorrelationID(id)
				return o
			},
		},
		input: input,
	}
}

// DecomposingInto starts a fluent DecomposeToSlice request.
func DecomposingInto[T any, U any](input T) DecomposeSliceRequest[T, U] {
	return newDecomposeSliceRequest[T, U](input, NewDecomposeOptions())
}

func (r DecomposeSliceRequest[T, U]) Run(ctx ...context.Context) ([]U, error) {
	r.opts = r.optsWithRunContext(ctx)
	if err := buildError(r.opts); err != nil {
		var zero []U
		return zero, err
	}
	return DecomposeToSlice[T, U](r.input, r.opts)
}

// EnrichRequest is a fluent builder for Enrich.
type EnrichRequest[T any, U any] struct {
	requestBase[EnrichRequest[T, U], EnrichOptions]
	input T
}

func newEnrichRequest[T any, U any](input T, opts EnrichOptions) EnrichRequest[T, U] {
	return EnrichRequest[T, U]{
		requestBase: requestBase[EnrichRequest[T, U], EnrichOptions]{
			opts: opts,
			lift: func(next EnrichOptions) EnrichRequest[T, U] {
				return newEnrichRequest[T, U](input, next)
			},
			setSteering: func(o EnrichOptions, s string) EnrichOptions {
				o.CommonOptions = o.CommonOptions.WithSteering(s)
				return o
			},
			setThreshold: func(o EnrichOptions, v float64) EnrichOptions {
				o.CommonOptions = o.CommonOptions.WithThreshold(v)
				return o
			},
			setMode: func(o EnrichOptions, m Mode) EnrichOptions {
				o.CommonOptions = o.CommonOptions.WithMode(m)
				return o
			},
			setIntelligence: func(o EnrichOptions, s Speed) EnrichOptions {
				o.CommonOptions = o.CommonOptions.WithIntelligence(s)
				return o
			},
			setModel: func(o EnrichOptions, model string) EnrichOptions {
				o.CommonOptions = o.CommonOptions.WithModel(model)
				return o
			},
			setContext: func(o EnrichOptions, ctx context.Context) EnrichOptions {
				o.CommonOptions = o.CommonOptions.WithContext(ctx)
				return o
			},
			setRequestID: func(o EnrichOptions, id string) EnrichOptions {
				o.CommonOptions = o.CommonOptions.WithRequestID(id)
				return o
			},
			setCorrelationID: func(o EnrichOptions, id string) EnrichOptions {
				o.CommonOptions = o.CommonOptions.WithCorrelationID(id)
				return o
			},
		},
		input: input,
	}
}

// Enriching starts a fluent Enrich request.
func Enriching[T any, U any](input T) EnrichRequest[T, U] {
	return newEnrichRequest[T, U](input, NewEnrichOptions())
}

func (r EnrichRequest[T, U]) Run(ctx ...context.Context) (EnrichResult[U], error) {
	r.opts = r.optsWithRunContext(ctx)
	if err := buildError(r.opts); err != nil {
		var zero EnrichResult[U]
		return zero, err
	}
	return Enrich[T, U](r.input, r.opts)
}

// EnrichInPlaceRequest is a fluent builder for EnrichInPlace.
type EnrichInPlaceRequest[T any] struct {
	requestBase[EnrichInPlaceRequest[T], EnrichOptions]
	input T
}

func newEnrichInPlaceRequest[T any](input T, opts EnrichOptions) EnrichInPlaceRequest[T] {
	return EnrichInPlaceRequest[T]{
		requestBase: requestBase[EnrichInPlaceRequest[T], EnrichOptions]{
			opts: opts,
			lift: func(next EnrichOptions) EnrichInPlaceRequest[T] {
				return newEnrichInPlaceRequest(input, next)
			},
			setSteering: func(o EnrichOptions, s string) EnrichOptions {
				o.CommonOptions = o.CommonOptions.WithSteering(s)
				return o
			},
			setThreshold: func(o EnrichOptions, v float64) EnrichOptions {
				o.CommonOptions = o.CommonOptions.WithThreshold(v)
				return o
			},
			setMode: func(o EnrichOptions, m Mode) EnrichOptions {
				o.CommonOptions = o.CommonOptions.WithMode(m)
				return o
			},
			setIntelligence: func(o EnrichOptions, s Speed) EnrichOptions {
				o.CommonOptions = o.CommonOptions.WithIntelligence(s)
				return o
			},
			setModel: func(o EnrichOptions, model string) EnrichOptions {
				o.CommonOptions = o.CommonOptions.WithModel(model)
				return o
			},
			setContext: func(o EnrichOptions, ctx context.Context) EnrichOptions {
				o.CommonOptions = o.CommonOptions.WithContext(ctx)
				return o
			},
			setRequestID: func(o EnrichOptions, id string) EnrichOptions {
				o.CommonOptions = o.CommonOptions.WithRequestID(id)
				return o
			},
			setCorrelationID: func(o EnrichOptions, id string) EnrichOptions {
				o.CommonOptions = o.CommonOptions.WithCorrelationID(id)
				return o
			},
		},
		input: input,
	}
}

// EnrichingInPlace starts a fluent EnrichInPlace request.
func EnrichingInPlace[T any](input T) EnrichInPlaceRequest[T] {
	return newEnrichInPlaceRequest(input, NewEnrichOptions())
}

func (r EnrichInPlaceRequest[T]) Run(ctx ...context.Context) (T, error) {
	r.opts = r.optsWithRunContext(ctx)
	if err := buildError(r.opts); err != nil {
		var zero T
		return zero, err
	}
	return EnrichInPlace[T](r.input, r.opts)
}

// NormalizeRequest is a fluent builder for Normalize.
type NormalizeRequest[T any] struct {
	requestBase[NormalizeRequest[T], NormalizeOptions]
	input T
}

func newNormalizeRequest[T any](input T, opts NormalizeOptions) NormalizeRequest[T] {
	return NormalizeRequest[T]{
		requestBase: requestBase[NormalizeRequest[T], NormalizeOptions]{
			opts: opts,
			lift: func(next NormalizeOptions) NormalizeRequest[T] {
				return newNormalizeRequest(input, next)
			},
			setSteering: func(o NormalizeOptions, s string) NormalizeOptions {
				o.CommonOptions = o.CommonOptions.WithSteering(s)
				return o
			},
			setThreshold: func(o NormalizeOptions, v float64) NormalizeOptions {
				o.CommonOptions = o.CommonOptions.WithThreshold(v)
				return o
			},
			setMode: func(o NormalizeOptions, m Mode) NormalizeOptions {
				o.CommonOptions = o.CommonOptions.WithMode(m)
				return o
			},
			setIntelligence: func(o NormalizeOptions, s Speed) NormalizeOptions {
				o.CommonOptions = o.CommonOptions.WithIntelligence(s)
				return o
			},
			setModel: func(o NormalizeOptions, model string) NormalizeOptions {
				o.CommonOptions = o.CommonOptions.WithModel(model)
				return o
			},
			setContext: func(o NormalizeOptions, ctx context.Context) NormalizeOptions {
				o.CommonOptions = o.CommonOptions.WithContext(ctx)
				return o
			},
			setRequestID: func(o NormalizeOptions, id string) NormalizeOptions {
				o.CommonOptions = o.CommonOptions.WithRequestID(id)
				return o
			},
			setCorrelationID: func(o NormalizeOptions, id string) NormalizeOptions {
				o.CommonOptions = o.CommonOptions.WithCorrelationID(id)
				return o
			},
		},
		input: input,
	}
}

// Normalizing starts a fluent Normalize request.
func Normalizing[T any](input T) NormalizeRequest[T] {
	return newNormalizeRequest(input, NewNormalizeOptions())
}

func (r NormalizeRequest[T]) Run(ctx ...context.Context) (NormalizeResult[T], error) {
	r.opts = r.optsWithRunContext(ctx)
	if err := buildError(r.opts); err != nil {
		var zero NormalizeResult[T]
		return zero, err
	}
	return Normalize[T](r.input, r.opts)
}

// NormalizeTextRequest is a fluent builder for NormalizeText.
type NormalizeTextRequest struct {
	requestBase[NormalizeTextRequest, NormalizeOptions]
	input string
}

func newNormalizeTextRequest(input string, opts NormalizeOptions) NormalizeTextRequest {
	return NormalizeTextRequest{
		requestBase: requestBase[NormalizeTextRequest, NormalizeOptions]{
			opts: opts,
			lift: func(next NormalizeOptions) NormalizeTextRequest {
				return newNormalizeTextRequest(input, next)
			},
			setSteering: func(o NormalizeOptions, s string) NormalizeOptions {
				o.CommonOptions = o.CommonOptions.WithSteering(s)
				return o
			},
			setThreshold: func(o NormalizeOptions, v float64) NormalizeOptions {
				o.CommonOptions = o.CommonOptions.WithThreshold(v)
				return o
			},
			setMode: func(o NormalizeOptions, m Mode) NormalizeOptions {
				o.CommonOptions = o.CommonOptions.WithMode(m)
				return o
			},
			setIntelligence: func(o NormalizeOptions, s Speed) NormalizeOptions {
				o.CommonOptions = o.CommonOptions.WithIntelligence(s)
				return o
			},
			setModel: func(o NormalizeOptions, model string) NormalizeOptions {
				o.CommonOptions = o.CommonOptions.WithModel(model)
				return o
			},
			setContext: func(o NormalizeOptions, ctx context.Context) NormalizeOptions {
				o.CommonOptions = o.CommonOptions.WithContext(ctx)
				return o
			},
			setRequestID: func(o NormalizeOptions, id string) NormalizeOptions {
				o.CommonOptions = o.CommonOptions.WithRequestID(id)
				return o
			},
			setCorrelationID: func(o NormalizeOptions, id string) NormalizeOptions {
				o.CommonOptions = o.CommonOptions.WithCorrelationID(id)
				return o
			},
		},
		input: input,
	}
}

// NormalizingText starts a fluent NormalizeText request.
func NormalizingText(input string) NormalizeTextRequest {
	return newNormalizeTextRequest(input, NewNormalizeOptions())
}

func (r NormalizeTextRequest) Run(ctx ...context.Context) (string, error) {
	r.opts = r.optsWithRunContext(ctx)
	if err := buildError(r.opts); err != nil {
		var zero string
		return zero, err
	}
	return NormalizeText(r.input, r.opts)
}

// NormalizeBatchRequest is a fluent builder for NormalizeBatch.
type NormalizeBatchRequest[T any] struct {
	requestBase[NormalizeBatchRequest[T], NormalizeOptions]
	items []T
}

func newNormalizeBatchRequest[T any](items []T, opts NormalizeOptions) NormalizeBatchRequest[T] {
	return NormalizeBatchRequest[T]{
		requestBase: requestBase[NormalizeBatchRequest[T], NormalizeOptions]{
			opts: opts,
			lift: func(next NormalizeOptions) NormalizeBatchRequest[T] {
				return newNormalizeBatchRequest(items, next)
			},
			setSteering: func(o NormalizeOptions, s string) NormalizeOptions {
				o.CommonOptions = o.CommonOptions.WithSteering(s)
				return o
			},
			setThreshold: func(o NormalizeOptions, v float64) NormalizeOptions {
				o.CommonOptions = o.CommonOptions.WithThreshold(v)
				return o
			},
			setMode: func(o NormalizeOptions, m Mode) NormalizeOptions {
				o.CommonOptions = o.CommonOptions.WithMode(m)
				return o
			},
			setIntelligence: func(o NormalizeOptions, s Speed) NormalizeOptions {
				o.CommonOptions = o.CommonOptions.WithIntelligence(s)
				return o
			},
			setModel: func(o NormalizeOptions, model string) NormalizeOptions {
				o.CommonOptions = o.CommonOptions.WithModel(model)
				return o
			},
			setContext: func(o NormalizeOptions, ctx context.Context) NormalizeOptions {
				o.CommonOptions = o.CommonOptions.WithContext(ctx)
				return o
			},
			setRequestID: func(o NormalizeOptions, id string) NormalizeOptions {
				o.CommonOptions = o.CommonOptions.WithRequestID(id)
				return o
			},
			setCorrelationID: func(o NormalizeOptions, id string) NormalizeOptions {
				o.CommonOptions = o.CommonOptions.WithCorrelationID(id)
				return o
			},
		},
		items: items,
	}
}

// NormalizingBatch starts a fluent NormalizeBatch request.
func NormalizingBatch[T any](items []T) NormalizeBatchRequest[T] {
	return newNormalizeBatchRequest(items, NewNormalizeOptions())
}

func (r NormalizeBatchRequest[T]) Run(ctx ...context.Context) ([]NormalizeResult[T], error) {
	r.opts = r.optsWithRunContext(ctx)
	if err := buildError(r.opts); err != nil {
		var zero []NormalizeResult[T]
		return zero, err
	}
	return NormalizeBatch[T](r.items, r.opts)
}

// SemanticMatchRequest is a fluent builder for SemanticMatch.
type SemanticMatchRequest[S any, T any] struct {
	requestBase[SemanticMatchRequest[S, T], MatchOptions]
	sources []S
	targets []T
}

func newSemanticMatchRequest[S any, T any](sources []S, targets []T, opts MatchOptions) SemanticMatchRequest[S, T] {
	return SemanticMatchRequest[S, T]{
		requestBase: requestBase[SemanticMatchRequest[S, T], MatchOptions]{
			opts: opts,
			lift: func(next MatchOptions) SemanticMatchRequest[S, T] {
				return newSemanticMatchRequest(sources, targets, next)
			},
			setSteering: func(o MatchOptions, s string) MatchOptions {
				o.CommonOptions = o.CommonOptions.WithSteering(s)
				return o
			},
			setThreshold: func(o MatchOptions, v float64) MatchOptions {
				o.CommonOptions = o.CommonOptions.WithThreshold(v)
				return o
			},
			setMode: func(o MatchOptions, m Mode) MatchOptions {
				o.CommonOptions = o.CommonOptions.WithMode(m)
				return o
			},
			setIntelligence: func(o MatchOptions, s Speed) MatchOptions {
				o.CommonOptions = o.CommonOptions.WithIntelligence(s)
				return o
			},
			setModel: func(o MatchOptions, model string) MatchOptions {
				o.CommonOptions = o.CommonOptions.WithModel(model)
				return o
			},
			setContext: func(o MatchOptions, ctx context.Context) MatchOptions {
				o.CommonOptions = o.CommonOptions.WithContext(ctx)
				return o
			},
			setRequestID: func(o MatchOptions, id string) MatchOptions {
				o.CommonOptions = o.CommonOptions.WithRequestID(id)
				return o
			},
			setCorrelationID: func(o MatchOptions, id string) MatchOptions {
				o.CommonOptions = o.CommonOptions.WithCorrelationID(id)
				return o
			},
		},
		sources: sources,
		targets: targets,
	}
}

// Matching starts a fluent SemanticMatch request.
func Matching[S any, T any](sources []S, targets []T) SemanticMatchRequest[S, T] {
	return newSemanticMatchRequest(sources, targets, NewMatchOptions())
}

func (r SemanticMatchRequest[S, T]) By(criteria string) SemanticMatchRequest[S, T] {
	opts := r.opts
	opts.MatchCriteria = criteria
	return r.WithOptions(opts)
}

func (r SemanticMatchRequest[S, T]) Strategy(strategy string) SemanticMatchRequest[S, T] {
	opts := r.opts
	opts.Strategy = strategy
	return r.WithOptions(opts)
}

func (r SemanticMatchRequest[S, T]) Run(ctx ...context.Context) (MatchResult[S, T], error) {
	r.opts = r.optsWithRunContext(ctx)
	if err := buildError(r.opts); err != nil {
		var zero MatchResult[S, T]
		return zero, err
	}
	return SemanticMatch[S, T](r.sources, r.targets, r.opts)
}

// MatchOneRequest is a fluent builder for MatchOne.
type MatchOneRequest[S any, T any] struct {
	requestBase[MatchOneRequest[S, T], MatchOptions]
	source  S
	targets []T
}

func newMatchOneRequest[S any, T any](source S, targets []T, opts MatchOptions) MatchOneRequest[S, T] {
	return MatchOneRequest[S, T]{
		requestBase: requestBase[MatchOneRequest[S, T], MatchOptions]{
			opts: opts,
			lift: func(next MatchOptions) MatchOneRequest[S, T] {
				return newMatchOneRequest(source, targets, next)
			},
			setSteering: func(o MatchOptions, s string) MatchOptions {
				o.CommonOptions = o.CommonOptions.WithSteering(s)
				return o
			},
			setThreshold: func(o MatchOptions, v float64) MatchOptions {
				o.CommonOptions = o.CommonOptions.WithThreshold(v)
				return o
			},
			setMode: func(o MatchOptions, m Mode) MatchOptions {
				o.CommonOptions = o.CommonOptions.WithMode(m)
				return o
			},
			setIntelligence: func(o MatchOptions, s Speed) MatchOptions {
				o.CommonOptions = o.CommonOptions.WithIntelligence(s)
				return o
			},
			setModel: func(o MatchOptions, model string) MatchOptions {
				o.CommonOptions = o.CommonOptions.WithModel(model)
				return o
			},
			setContext: func(o MatchOptions, ctx context.Context) MatchOptions {
				o.CommonOptions = o.CommonOptions.WithContext(ctx)
				return o
			},
			setRequestID: func(o MatchOptions, id string) MatchOptions {
				o.CommonOptions = o.CommonOptions.WithRequestID(id)
				return o
			},
			setCorrelationID: func(o MatchOptions, id string) MatchOptions {
				o.CommonOptions = o.CommonOptions.WithCorrelationID(id)
				return o
			},
		},
		source:  source,
		targets: targets,
	}
}

// MatchingOne starts a fluent MatchOne request.
func MatchingOne[S any, T any](source S, targets []T) MatchOneRequest[S, T] {
	return newMatchOneRequest(source, targets, NewMatchOptions())
}

func (r MatchOneRequest[S, T]) By(criteria string) MatchOneRequest[S, T] {
	opts := r.opts
	opts.MatchCriteria = criteria
	return r.WithOptions(opts)
}

func (r MatchOneRequest[S, T]) Strategy(strategy string) MatchOneRequest[S, T] {
	opts := r.opts
	opts.Strategy = strategy
	return r.WithOptions(opts)
}

func (r MatchOneRequest[S, T]) Run(ctx ...context.Context) ([]MatchPair[S, T], error) {
	r.opts = r.optsWithRunContext(ctx)
	if err := buildError(r.opts); err != nil {
		var zero []MatchPair[S, T]
		return zero, err
	}
	return MatchOne[S, T](r.source, r.targets, r.opts)
}

// CritiqueRequest is a fluent builder for Critique.
type CritiqueRequest[T any] struct {
	requestBase[CritiqueRequest[T], CritiqueOptions]
	input T
}

func newCritiqueRequest[T any](input T, opts CritiqueOptions) CritiqueRequest[T] {
	return CritiqueRequest[T]{
		requestBase: requestBase[CritiqueRequest[T], CritiqueOptions]{
			opts: opts,
			lift: func(next CritiqueOptions) CritiqueRequest[T] {
				return newCritiqueRequest(input, next)
			},
			setSteering: func(o CritiqueOptions, s string) CritiqueOptions {
				o.CommonOptions = o.CommonOptions.WithSteering(s)
				return o
			},
			setThreshold: func(o CritiqueOptions, v float64) CritiqueOptions {
				o.CommonOptions = o.CommonOptions.WithThreshold(v)
				return o
			},
			setMode: func(o CritiqueOptions, m Mode) CritiqueOptions {
				o.CommonOptions = o.CommonOptions.WithMode(m)
				return o
			},
			setIntelligence: func(o CritiqueOptions, s Speed) CritiqueOptions {
				o.CommonOptions = o.CommonOptions.WithIntelligence(s)
				return o
			},
			setModel: func(o CritiqueOptions, model string) CritiqueOptions {
				o.CommonOptions = o.CommonOptions.WithModel(model)
				return o
			},
			setContext: func(o CritiqueOptions, ctx context.Context) CritiqueOptions {
				o.CommonOptions = o.CommonOptions.WithContext(ctx)
				return o
			},
			setRequestID: func(o CritiqueOptions, id string) CritiqueOptions {
				o.CommonOptions = o.CommonOptions.WithRequestID(id)
				return o
			},
			setCorrelationID: func(o CritiqueOptions, id string) CritiqueOptions {
				o.CommonOptions = o.CommonOptions.WithCorrelationID(id)
				return o
			},
		},
		input: input,
	}
}

// Critiquing starts a fluent Critique request.
func Critiquing[T any](input T) CritiqueRequest[T] {
	return newCritiqueRequest(input, NewCritiqueOptions())
}

func (r CritiqueRequest[T]) Run(ctx ...context.Context) (CritiqueResult, error) {
	r.opts = r.optsWithRunContext(ctx)
	if err := buildError(r.opts); err != nil {
		var zero CritiqueResult
		return zero, err
	}
	return Critique[T](r.input, r.opts)
}

// SynthesizeRequest is a fluent builder for Synthesize.
type SynthesizeRequest[T any] struct {
	requestBase[SynthesizeRequest[T], SynthesizeOptions]
	sources []any
}

func newSynthesizeRequest[T any](sources []any, opts SynthesizeOptions) SynthesizeRequest[T] {
	return SynthesizeRequest[T]{
		requestBase: requestBase[SynthesizeRequest[T], SynthesizeOptions]{
			opts: opts,
			lift: func(next SynthesizeOptions) SynthesizeRequest[T] {
				return newSynthesizeRequest[T](sources, next)
			},
			setSteering: func(o SynthesizeOptions, s string) SynthesizeOptions {
				o.CommonOptions = o.CommonOptions.WithSteering(s)
				return o
			},
			setThreshold: func(o SynthesizeOptions, v float64) SynthesizeOptions {
				o.CommonOptions = o.CommonOptions.WithThreshold(v)
				return o
			},
			setMode: func(o SynthesizeOptions, m Mode) SynthesizeOptions {
				o.CommonOptions = o.CommonOptions.WithMode(m)
				return o
			},
			setIntelligence: func(o SynthesizeOptions, s Speed) SynthesizeOptions {
				o.CommonOptions = o.CommonOptions.WithIntelligence(s)
				return o
			},
			setModel: func(o SynthesizeOptions, model string) SynthesizeOptions {
				o.CommonOptions = o.CommonOptions.WithModel(model)
				return o
			},
			setContext: func(o SynthesizeOptions, ctx context.Context) SynthesizeOptions {
				o.CommonOptions = o.CommonOptions.WithContext(ctx)
				return o
			},
			setRequestID: func(o SynthesizeOptions, id string) SynthesizeOptions {
				o.CommonOptions = o.CommonOptions.WithRequestID(id)
				return o
			},
			setCorrelationID: func(o SynthesizeOptions, id string) SynthesizeOptions {
				o.CommonOptions = o.CommonOptions.WithCorrelationID(id)
				return o
			},
		},
		sources: sources,
	}
}

// Synthesizing starts a fluent Synthesize request.
func Synthesizing[T any](sources []any) SynthesizeRequest[T] {
	return newSynthesizeRequest[T](sources, NewSynthesizeOptions())
}

func (r SynthesizeRequest[T]) Strategy(strategy string) SynthesizeRequest[T] {
	opts := r.opts
	opts.Strategy = strategy
	return r.WithOptions(opts)
}

func (r SynthesizeRequest[T]) Run(ctx ...context.Context) (SynthesizeResult[T], error) {
	r.opts = r.optsWithRunContext(ctx)
	if err := buildError(r.opts); err != nil {
		var zero SynthesizeResult[T]
		return zero, err
	}
	return Synthesize[T](r.sources, r.opts)
}

// PredictRequest is a fluent builder for Predict.
type PredictRequest[T any] struct {
	requestBase[PredictRequest[T], PredictOptions]
	input any
}

func newPredictRequest[T any](input any, opts PredictOptions) PredictRequest[T] {
	return PredictRequest[T]{
		requestBase: requestBase[PredictRequest[T], PredictOptions]{
			opts: opts,
			lift: func(next PredictOptions) PredictRequest[T] {
				return newPredictRequest[T](input, next)
			},
			setSteering: func(o PredictOptions, s string) PredictOptions {
				o.CommonOptions = o.CommonOptions.WithSteering(s)
				return o
			},
			setThreshold: func(o PredictOptions, v float64) PredictOptions {
				o.CommonOptions = o.CommonOptions.WithThreshold(v)
				return o
			},
			setMode: func(o PredictOptions, m Mode) PredictOptions {
				o.CommonOptions = o.CommonOptions.WithMode(m)
				return o
			},
			setIntelligence: func(o PredictOptions, s Speed) PredictOptions {
				o.CommonOptions = o.CommonOptions.WithIntelligence(s)
				return o
			},
			setModel: func(o PredictOptions, model string) PredictOptions {
				o.CommonOptions = o.CommonOptions.WithModel(model)
				return o
			},
			setContext: func(o PredictOptions, ctx context.Context) PredictOptions {
				o.CommonOptions = o.CommonOptions.WithContext(ctx)
				return o
			},
			setRequestID: func(o PredictOptions, id string) PredictOptions {
				o.CommonOptions = o.CommonOptions.WithRequestID(id)
				return o
			},
			setCorrelationID: func(o PredictOptions, id string) PredictOptions {
				o.CommonOptions = o.CommonOptions.WithCorrelationID(id)
				return o
			},
		},
		input: input,
	}
}

// Predicting starts a fluent Predict request.
func Predicting[T any](input any) PredictRequest[T] {
	return newPredictRequest[T](input, NewPredictOptions())
}

func (r PredictRequest[T]) Horizon(horizon string) PredictRequest[T] {
	opts := r.opts
	opts.Horizon = horizon
	return r.WithOptions(opts)
}

func (r PredictRequest[T]) Run(ctx ...context.Context) (PredictResult[T], error) {
	r.opts = r.optsWithRunContext(ctx)
	if err := buildError(r.opts); err != nil {
		var zero PredictResult[T]
		return zero, err
	}
	return Predict[T](r.input, r.opts)
}

// VerifyRequest is a fluent builder for Verify.
type VerifyRequest struct {
	requestBase[VerifyRequest, VerifyOptions]
	input string
}

func newVerifyRequest(input string, opts VerifyOptions) VerifyRequest {
	return VerifyRequest{
		requestBase: requestBase[VerifyRequest, VerifyOptions]{
			opts: opts,
			lift: func(next VerifyOptions) VerifyRequest {
				return newVerifyRequest(input, next)
			},
			setSteering: func(o VerifyOptions, s string) VerifyOptions {
				o.CommonOptions = o.CommonOptions.WithSteering(s)
				return o
			},
			setThreshold: func(o VerifyOptions, v float64) VerifyOptions {
				o.CommonOptions = o.CommonOptions.WithThreshold(v)
				return o
			},
			setMode: func(o VerifyOptions, m Mode) VerifyOptions {
				o.CommonOptions = o.CommonOptions.WithMode(m)
				return o
			},
			setIntelligence: func(o VerifyOptions, s Speed) VerifyOptions {
				o.CommonOptions = o.CommonOptions.WithIntelligence(s)
				return o
			},
			setModel: func(o VerifyOptions, model string) VerifyOptions {
				o.CommonOptions = o.CommonOptions.WithModel(model)
				return o
			},
			setContext: func(o VerifyOptions, ctx context.Context) VerifyOptions {
				o.CommonOptions = o.CommonOptions.WithContext(ctx)
				return o
			},
			setRequestID: func(o VerifyOptions, id string) VerifyOptions {
				o.CommonOptions = o.CommonOptions.WithRequestID(id)
				return o
			},
			setCorrelationID: func(o VerifyOptions, id string) VerifyOptions {
				o.CommonOptions = o.CommonOptions.WithCorrelationID(id)
				return o
			},
		},
		input: input,
	}
}

// Verifying starts a fluent Verify request.
func Verifying(input string) VerifyRequest {
	return newVerifyRequest(input, NewVerifyOptions())
}

func (r VerifyRequest) Run(ctx ...context.Context) (VerifyResult, error) {
	r.opts = r.optsWithRunContext(ctx)
	if err := buildError(r.opts); err != nil {
		var zero VerifyResult
		return zero, err
	}
	return Verify(r.input, r.opts)
}

// VerifyClaimRequest is a fluent builder for VerifyClaim.
type VerifyClaimRequest struct {
	requestBase[VerifyClaimRequest, VerifyOptions]
	claim string
}

func newVerifyClaimRequest(claim string, opts VerifyOptions) VerifyClaimRequest {
	return VerifyClaimRequest{
		requestBase: requestBase[VerifyClaimRequest, VerifyOptions]{
			opts: opts,
			lift: func(next VerifyOptions) VerifyClaimRequest {
				return newVerifyClaimRequest(claim, next)
			},
			setSteering: func(o VerifyOptions, s string) VerifyOptions {
				o.CommonOptions = o.CommonOptions.WithSteering(s)
				return o
			},
			setThreshold: func(o VerifyOptions, v float64) VerifyOptions {
				o.CommonOptions = o.CommonOptions.WithThreshold(v)
				return o
			},
			setMode: func(o VerifyOptions, m Mode) VerifyOptions {
				o.CommonOptions = o.CommonOptions.WithMode(m)
				return o
			},
			setIntelligence: func(o VerifyOptions, s Speed) VerifyOptions {
				o.CommonOptions = o.CommonOptions.WithIntelligence(s)
				return o
			},
			setModel: func(o VerifyOptions, model string) VerifyOptions {
				o.CommonOptions = o.CommonOptions.WithModel(model)
				return o
			},
			setContext: func(o VerifyOptions, ctx context.Context) VerifyOptions {
				o.CommonOptions = o.CommonOptions.WithContext(ctx)
				return o
			},
			setRequestID: func(o VerifyOptions, id string) VerifyOptions {
				o.CommonOptions = o.CommonOptions.WithRequestID(id)
				return o
			},
			setCorrelationID: func(o VerifyOptions, id string) VerifyOptions {
				o.CommonOptions = o.CommonOptions.WithCorrelationID(id)
				return o
			},
		},
		claim: claim,
	}
}

// VerifyingClaim starts a fluent VerifyClaim request.
func VerifyingClaim(claim string) VerifyClaimRequest {
	return newVerifyClaimRequest(claim, NewVerifyOptions())
}

func (r VerifyClaimRequest) Run(ctx ...context.Context) (ClaimVerification, error) {
	r.opts = r.optsWithRunContext(ctx)
	if err := buildError(r.opts); err != nil {
		var zero ClaimVerification
		return zero, err
	}
	return VerifyClaim(r.claim, r.opts)
}
