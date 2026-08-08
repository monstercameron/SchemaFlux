package fluent

import (
	"context"

	"github.com/monstercameron/schemaflux/internal/types"
)

// withRunContext applies an optional trailing context to opts, returning opts
// unchanged when none was given. It is what every Run(ctx ...context.Context)
// below calls: see run.go's doc comment for why Run stays variadic instead of
// taking a required context.Context -- a hard signature change would break
// provider_integration_test.go, a call site outside this package.
func withRunContext(common CommonOptions, ctx []context.Context) CommonOptions {
	if len(ctx) == 0 {
		return common
	}
	return common.WithContext(ctx[0])
}

// withCollectionRunContext is withRunContext's counterpart for Choose,
// Filter, and Sort.
//
// Those three live in internal/ops/collection.go, which this task cannot
// edit, and which reads opts.OpOptions.Context directly rather than the
// merged value opts.toOpOptions() would produce -- so setting only
// CommonOptions.Context (what Context(ctx) and every other builder in this
// file already did, before this task) never reached their actual provider
// call. This sets OpOptions.Context too, so Run(ctx)/RunResult(ctx)'s
// cancellation actually takes effect for these three. It is a workaround for
// an existing defect outside this change's edit scope, not a fix to it --
// see the task report for the citation (collection.go lines 222, 339, 425,
// 615, all `operationContext(opts.OpOptions.Context, ...)`).
func withCollectionRunContext(opts *types.OpOptions, common *CommonOptions, ctx []context.Context) {
	if len(ctx) == 0 {
		return
	}
	*common = common.WithContext(ctx[0])
	opts.Context = ctx[0]
}

// ExtractRequest is a fluent builder for Extract.
type ExtractRequest[T any] struct {
	input any
	opts  ExtractOptions
}

// Extracting starts a fluent Extract request.
func Extracting[T any](input any) ExtractRequest[T] {
	return ExtractRequest[T]{input: input, opts: NewExtractOptions()}
}

func (r ExtractRequest[T]) WithOptions(opts ExtractOptions) ExtractRequest[T] {
	r.opts = opts
	return r
}

func (r ExtractRequest[T]) Configure(fn func(ExtractOptions) ExtractOptions) ExtractRequest[T] {
	r.opts = fn(r.opts)
	return r
}

func (r ExtractRequest[T]) Steer(steering string) ExtractRequest[T] {
	r.opts = r.opts.WithSteering(steering)
	return r
}

func (r ExtractRequest[T]) Threshold(threshold float64) ExtractRequest[T] {
	r.opts = r.opts.WithThreshold(threshold)
	return r
}

func (r ExtractRequest[T]) Strict() ExtractRequest[T] {
	r.opts = r.opts.WithMode(Strict)
	return r
}

func (r ExtractRequest[T]) Smart() ExtractRequest[T] {
	r.opts = r.opts.WithIntelligence(Smart)
	return r
}

func (r ExtractRequest[T]) Fast() ExtractRequest[T] {
	r.opts = r.opts.WithIntelligence(Fast)
	return r
}

func (r ExtractRequest[T]) Quick() ExtractRequest[T] {
	r.opts = r.opts.WithIntelligence(Quick)
	return r
}

func (r ExtractRequest[T]) Context(ctx context.Context) ExtractRequest[T] {
	r.opts.CommonOptions = r.opts.CommonOptions.WithContext(ctx)
	return r
}

func (r ExtractRequest[T]) RequestID(requestID string) ExtractRequest[T] {
	r.opts.CommonOptions = r.opts.CommonOptions.WithRequestID(requestID)
	return r
}

func (r ExtractRequest[T]) CorrelationID(correlationID string) ExtractRequest[T] {
	r.opts.CommonOptions = r.opts.CommonOptions.WithCorrelationID(correlationID)
	return r
}

func (r ExtractRequest[T]) Partial(allow bool) ExtractRequest[T] {
	r.opts = r.opts.WithAllowPartial(allow)
	return r
}

func (r ExtractRequest[T]) SchemaHints(hints map[string]string) ExtractRequest[T] {
	r.opts = r.opts.WithSchemaHints(hints)
	return r
}

// Run executes the request. An optional ctx applies to this call only and,
// if cancelled, aborts it; called with no argument, Run uses whatever
// context the builder chain already set (Context(ctx)), or
// context.Background() if none -- exactly today's behaviour, preserved for
// provider_integration_test.go's Run() call. See run.go for why Run is
// variadic rather than Run(ctx context.Context).
func (r ExtractRequest[T]) Run(ctx ...context.Context) (T, error) {
	r.opts.CommonOptions = withRunContext(r.opts.CommonOptions, ctx)
	if err := buildError(r.opts); err != nil {
		var zero T
		return zero, err
	}
	return Extract[T](r.input, r.opts)
}

// RunResult executes identically to Run and returns the execution envelope
// alongside the value (A-013, API-11).
func (r ExtractRequest[T]) RunResult(ctx context.Context) (Result[T], error) {
	r.opts.CommonOptions = r.opts.CommonOptions.WithContext(ctx)
	if err := buildError(r.opts); err != nil {
		return Result[T]{}, err
	}
	return ExtractResult[T](r.input, r.opts)
}

// TransformRequest is a fluent builder for Transform.
type TransformRequest[T any, U any] struct {
	input T
	opts  TransformOptions
}

// Transforming starts a fluent Transform request.
func Transforming[T any, U any](input T) TransformRequest[T, U] {
	return TransformRequest[T, U]{input: input, opts: NewTransformOptions()}
}

func (r TransformRequest[T, U]) WithOptions(opts TransformOptions) TransformRequest[T, U] {
	r.opts = opts
	return r
}

func (r TransformRequest[T, U]) Configure(fn func(TransformOptions) TransformOptions) TransformRequest[T, U] {
	r.opts = fn(r.opts)
	return r
}

func (r TransformRequest[T, U]) Steer(steering string) TransformRequest[T, U] {
	r.opts = r.opts.WithSteering(steering)
	return r
}

func (r TransformRequest[T, U]) Strict() TransformRequest[T, U] {
	r.opts = r.opts.WithMode(Strict)
	return r
}

func (r TransformRequest[T, U]) Creative() TransformRequest[T, U] {
	r.opts = r.opts.WithMode(Creative)
	return r
}

func (r TransformRequest[T, U]) Smart() TransformRequest[T, U] {
	r.opts = r.opts.WithIntelligence(Smart)
	return r
}

func (r TransformRequest[T, U]) Fast() TransformRequest[T, U] {
	r.opts = r.opts.WithIntelligence(Fast)
	return r
}

func (r TransformRequest[T, U]) Quick() TransformRequest[T, U] {
	r.opts = r.opts.WithIntelligence(Quick)
	return r
}

func (r TransformRequest[T, U]) Merge(strategy string) TransformRequest[T, U] {
	r.opts = r.opts.WithMergeStrategy(strategy)
	return r
}

func (r TransformRequest[T, U]) Context(ctx context.Context) TransformRequest[T, U] {
	r.opts.CommonOptions = r.opts.CommonOptions.WithContext(ctx)
	return r
}

func (r TransformRequest[T, U]) RequestID(requestID string) TransformRequest[T, U] {
	r.opts.CommonOptions = r.opts.CommonOptions.WithRequestID(requestID)
	return r
}

func (r TransformRequest[T, U]) CorrelationID(correlationID string) TransformRequest[T, U] {
	r.opts.CommonOptions = r.opts.CommonOptions.WithCorrelationID(correlationID)
	return r
}

// Run executes the request; see ExtractRequest.Run for the ctx contract.
func (r TransformRequest[T, U]) Run(ctx ...context.Context) (U, error) {
	r.opts.CommonOptions = withRunContext(r.opts.CommonOptions, ctx)
	if err := buildError(r.opts); err != nil {
		var zero U
		return zero, err
	}
	return Transform[T, U](r.input, r.opts)
}

// RunResult executes identically to Run and returns a best-effort execution
// envelope: Transform has no ops.TransformResult twin yet (only Extract
// does, ops.ExtractResult), so Usage, Cost, Attempts, and the delivered
// contract are left at their zero values rather than guessed. Elapsed,
// Operation, RequestID, and CorrelationID are real.
func (r TransformRequest[T, U]) RunResult(ctx context.Context) (Result[U], error) {
	r.opts.CommonOptions = r.opts.CommonOptions.WithContext(ctx)
	if err := buildError(r.opts); err != nil {
		return Result[U]{}, err
	}
	return runEnvelope("transform", r.opts.GetRequestID(), r.opts.GetCorrelationID(), func() (U, error) {
		return Transform[T, U](r.input, r.opts)
	})
}

// GenerateRequest is a fluent builder for Generate.
type GenerateRequest[T any] struct {
	prompt string
	opts   GenerateOptions
}

// Generating starts a fluent Generate request.
func Generating[T any](prompt string) GenerateRequest[T] {
	return GenerateRequest[T]{prompt: prompt, opts: NewGenerateOptions()}
}

func (r GenerateRequest[T]) WithOptions(opts GenerateOptions) GenerateRequest[T] {
	r.opts = opts
	return r
}

func (r GenerateRequest[T]) Configure(fn func(GenerateOptions) GenerateOptions) GenerateRequest[T] {
	r.opts = fn(r.opts)
	return r
}

func (r GenerateRequest[T]) Steer(steering string) GenerateRequest[T] {
	r.opts = r.opts.WithSteering(steering)
	return r
}

func (r GenerateRequest[T]) Strict() GenerateRequest[T] {
	r.opts = r.opts.WithMode(Strict)
	return r
}

func (r GenerateRequest[T]) Creative() GenerateRequest[T] {
	r.opts = r.opts.WithMode(Creative)
	return r
}

func (r GenerateRequest[T]) Smart() GenerateRequest[T] {
	r.opts = r.opts.WithIntelligence(Smart)
	return r
}

func (r GenerateRequest[T]) Fast() GenerateRequest[T] {
	r.opts = r.opts.WithIntelligence(Fast)
	return r
}

func (r GenerateRequest[T]) Quick() GenerateRequest[T] {
	r.opts = r.opts.WithIntelligence(Quick)
	return r
}

func (r GenerateRequest[T]) Count(count int) GenerateRequest[T] {
	r.opts.Count = count
	return r
}

func (r GenerateRequest[T]) Style(style string) GenerateRequest[T] {
	r.opts.Style = style
	return r
}

func (r GenerateRequest[T]) Context(ctx context.Context) GenerateRequest[T] {
	r.opts.CommonOptions = r.opts.CommonOptions.WithContext(ctx)
	return r
}

func (r GenerateRequest[T]) RequestID(requestID string) GenerateRequest[T] {
	r.opts.CommonOptions = r.opts.CommonOptions.WithRequestID(requestID)
	return r
}

func (r GenerateRequest[T]) CorrelationID(correlationID string) GenerateRequest[T] {
	r.opts.CommonOptions = r.opts.CommonOptions.WithCorrelationID(correlationID)
	return r
}

// Run executes the request; see ExtractRequest.Run for the ctx contract.
func (r GenerateRequest[T]) Run(ctx ...context.Context) (T, error) {
	r.opts.CommonOptions = withRunContext(r.opts.CommonOptions, ctx)
	if err := buildError(r.opts); err != nil {
		var zero T
		return zero, err
	}
	return Generate[T](r.prompt, r.opts)
}

// RunResult executes identically to Run and returns a best-effort execution
// envelope; see TransformRequest.RunResult for what "best-effort" leaves at
// zero and why.
func (r GenerateRequest[T]) RunResult(ctx context.Context) (Result[T], error) {
	r.opts.CommonOptions = r.opts.CommonOptions.WithContext(ctx)
	if err := buildError(r.opts); err != nil {
		return Result[T]{}, err
	}
	return runEnvelope("generate", r.opts.GetRequestID(), r.opts.GetCorrelationID(), func() (T, error) {
		return Generate[T](r.prompt, r.opts)
	})
}

// ChooseRequest is a fluent builder for Choose.
type ChooseRequest[T any] struct {
	options []T
	opts    ChooseOptions
}

// Choosing starts a fluent Choose request.
func Choosing[T any](options []T) ChooseRequest[T] {
	return ChooseRequest[T]{options: options, opts: NewChooseOptions()}
}

func (r ChooseRequest[T]) WithOptions(opts ChooseOptions) ChooseRequest[T] {
	r.opts = opts
	return r
}

func (r ChooseRequest[T]) Configure(fn func(ChooseOptions) ChooseOptions) ChooseRequest[T] {
	r.opts = fn(r.opts)
	return r
}

func (r ChooseRequest[T]) By(criteria ...string) ChooseRequest[T] {
	r.opts = r.opts.WithCriteria(criteria)
	return r
}

func (r ChooseRequest[T]) Steer(steering string) ChooseRequest[T] {
	r.opts = r.opts.WithSteering(steering)
	return r
}

func (r ChooseRequest[T]) Smart() ChooseRequest[T] {
	r.opts = r.opts.WithIntelligence(Smart)
	return r
}

func (r ChooseRequest[T]) Fast() ChooseRequest[T] {
	r.opts = r.opts.WithIntelligence(Fast)
	return r
}

func (r ChooseRequest[T]) Quick() ChooseRequest[T] {
	r.opts = r.opts.WithIntelligence(Quick)
	return r
}

func (r ChooseRequest[T]) Context(ctx context.Context) ChooseRequest[T] {
	r.opts.CommonOptions = r.opts.CommonOptions.WithContext(ctx)
	return r
}

func (r ChooseRequest[T]) RequestID(requestID string) ChooseRequest[T] {
	r.opts.CommonOptions = r.opts.CommonOptions.WithRequestID(requestID)
	return r
}

func (r ChooseRequest[T]) CorrelationID(correlationID string) ChooseRequest[T] {
	r.opts.CommonOptions = r.opts.CommonOptions.WithCorrelationID(correlationID)
	return r
}

func (r ChooseRequest[T]) Top(n int) ChooseRequest[T] {
	r.opts = r.opts.WithTopN(n)
	return r
}

func (r ChooseRequest[T]) Reasoning(enabled bool) ChooseRequest[T] {
	r.opts = r.opts.WithRequireReasoning(enabled)
	return r
}

// Run executes the request; see ExtractRequest.Run for the ctx contract and
// withCollectionRunContext for why Choose needs its context set twice.
func (r ChooseRequest[T]) Run(ctx ...context.Context) (T, error) {
	withCollectionRunContext(&r.opts.OpOptions, &r.opts.CommonOptions, ctx)
	if err := buildError(r.opts); err != nil {
		var zero T
		return zero, err
	}
	return Choose[T](r.options, r.opts)
}

// RunResult executes identically to Run and returns a best-effort execution
// envelope; see TransformRequest.RunResult for what "best-effort" leaves at
// zero and why.
func (r ChooseRequest[T]) RunResult(ctx context.Context) (Result[T], error) {
	withCollectionRunContext(&r.opts.OpOptions, &r.opts.CommonOptions, []context.Context{ctx})
	if err := buildError(r.opts); err != nil {
		return Result[T]{}, err
	}
	return runEnvelope("choose", r.opts.GetRequestID(), r.opts.GetCorrelationID(), func() (T, error) {
		return Choose[T](r.options, r.opts)
	})
}

// FilterRequest is a fluent builder for Filter.
type FilterRequest[T any] struct {
	items []T
	opts  FilterOptions
}

// Filtering starts a fluent Filter request.
func Filtering[T any](items []T) FilterRequest[T] {
	return FilterRequest[T]{items: items, opts: NewFilterOptions()}
}

func (r FilterRequest[T]) WithOptions(opts FilterOptions) FilterRequest[T] {
	r.opts = opts
	return r
}

func (r FilterRequest[T]) Configure(fn func(FilterOptions) FilterOptions) FilterRequest[T] {
	r.opts = fn(r.opts)
	return r
}

func (r FilterRequest[T]) By(criteria string) FilterRequest[T] {
	r.opts = r.opts.WithCriteria(criteria)
	return r
}

func (r FilterRequest[T]) Steer(steering string) FilterRequest[T] {
	r.opts = r.opts.WithSteering(steering)
	return r
}

func (r FilterRequest[T]) Smart() FilterRequest[T] {
	r.opts = r.opts.WithIntelligence(Smart)
	return r
}

func (r FilterRequest[T]) Fast() FilterRequest[T] {
	r.opts = r.opts.WithIntelligence(Fast)
	return r
}

func (r FilterRequest[T]) Quick() FilterRequest[T] {
	r.opts = r.opts.WithIntelligence(Quick)
	return r
}

func (r FilterRequest[T]) Context(ctx context.Context) FilterRequest[T] {
	r.opts.CommonOptions = r.opts.CommonOptions.WithContext(ctx)
	return r
}

func (r FilterRequest[T]) RequestID(requestID string) FilterRequest[T] {
	r.opts.CommonOptions = r.opts.CommonOptions.WithRequestID(requestID)
	return r
}

func (r FilterRequest[T]) CorrelationID(correlationID string) FilterRequest[T] {
	r.opts.CommonOptions = r.opts.CommonOptions.WithCorrelationID(correlationID)
	return r
}

func (r FilterRequest[T]) KeepMatching(keep bool) FilterRequest[T] {
	r.opts.KeepMatching = keep
	return r
}

func (r FilterRequest[T]) MinConfidence(confidence float64) FilterRequest[T] {
	r.opts = r.opts.WithMinConfidence(confidence)
	return r
}

// Run executes the request; see ExtractRequest.Run for the ctx contract and
// withCollectionRunContext for why Filter needs its context set twice.
func (r FilterRequest[T]) Run(ctx ...context.Context) ([]T, error) {
	withCollectionRunContext(&r.opts.OpOptions, &r.opts.CommonOptions, ctx)
	if err := buildError(r.opts); err != nil {
		var zero []T
		return zero, err
	}
	return Filter[T](r.items, r.opts)
}

// RunResult executes identically to Run and returns a best-effort execution
// envelope; see TransformRequest.RunResult for what "best-effort" leaves at
// zero and why.
func (r FilterRequest[T]) RunResult(ctx context.Context) (Result[[]T], error) {
	withCollectionRunContext(&r.opts.OpOptions, &r.opts.CommonOptions, []context.Context{ctx})
	if err := buildError(r.opts); err != nil {
		return Result[[]T]{}, err
	}
	return runEnvelope("filter", r.opts.GetRequestID(), r.opts.GetCorrelationID(), func() ([]T, error) {
		return Filter[T](r.items, r.opts)
	})
}

// SortRequest is a fluent builder for Sort.
type SortRequest[T any] struct {
	items []T
	opts  SortOptions
}

// Sorting starts a fluent Sort request.
func Sorting[T any](items []T) SortRequest[T] {
	return SortRequest[T]{items: items, opts: NewSortOptions()}
}

func (r SortRequest[T]) WithOptions(opts SortOptions) SortRequest[T] {
	r.opts = opts
	return r
}

func (r SortRequest[T]) Configure(fn func(SortOptions) SortOptions) SortRequest[T] {
	r.opts = fn(r.opts)
	return r
}

func (r SortRequest[T]) By(criteria string) SortRequest[T] {
	r.opts = r.opts.WithCriteria(criteria)
	return r
}

func (r SortRequest[T]) Steer(steering string) SortRequest[T] {
	r.opts = r.opts.WithSteering(steering)
	return r
}

func (r SortRequest[T]) Smart() SortRequest[T] {
	r.opts = r.opts.WithIntelligence(Smart)
	return r
}

func (r SortRequest[T]) Fast() SortRequest[T] {
	r.opts = r.opts.WithIntelligence(Fast)
	return r
}

func (r SortRequest[T]) Quick() SortRequest[T] {
	r.opts = r.opts.WithIntelligence(Quick)
	return r
}

func (r SortRequest[T]) Context(ctx context.Context) SortRequest[T] {
	r.opts.CommonOptions = r.opts.CommonOptions.WithContext(ctx)
	return r
}

func (r SortRequest[T]) RequestID(requestID string) SortRequest[T] {
	r.opts.CommonOptions = r.opts.CommonOptions.WithRequestID(requestID)
	return r
}

func (r SortRequest[T]) CorrelationID(correlationID string) SortRequest[T] {
	r.opts.CommonOptions = r.opts.CommonOptions.WithCorrelationID(correlationID)
	return r
}

func (r SortRequest[T]) Asc() SortRequest[T] {
	r.opts = r.opts.WithDirection("ascending")
	return r
}

func (r SortRequest[T]) Desc() SortRequest[T] {
	r.opts = r.opts.WithDirection("descending")
	return r
}

// Run executes the request; see ExtractRequest.Run for the ctx contract and
// withCollectionRunContext for why Sort needs its context set twice.
func (r SortRequest[T]) Run(ctx ...context.Context) ([]T, error) {
	withCollectionRunContext(&r.opts.OpOptions, &r.opts.CommonOptions, ctx)
	if err := buildError(r.opts); err != nil {
		var zero []T
		return zero, err
	}
	return Sort[T](r.items, r.opts)
}

// RunResult executes identically to Run and returns a best-effort execution
// envelope; see TransformRequest.RunResult for what "best-effort" leaves at
// zero and why.
func (r SortRequest[T]) RunResult(ctx context.Context) (Result[[]T], error) {
	withCollectionRunContext(&r.opts.OpOptions, &r.opts.CommonOptions, []context.Context{ctx})
	if err := buildError(r.opts); err != nil {
		return Result[[]T]{}, err
	}
	return runEnvelope("sort", r.opts.GetRequestID(), r.opts.GetCorrelationID(), func() ([]T, error) {
		return Sort[T](r.items, r.opts)
	})
}

// ChooseBy is a compact entrypoint for selection-style tasks.
func ChooseBy[T any](options []T, criteria ...string) (T, error) {
	return Choosing(options).By(criteria...).Run()
}

// FilterBy is a compact entrypoint for natural-language filtering.
func FilterBy[T any](items []T, criteria string) ([]T, error) {
	return Filtering(items).By(criteria).Run()
}

// SortBy is a compact entrypoint for natural-language sorting.
func SortBy[T any](items []T, criteria string) ([]T, error) {
	return Sorting(items).By(criteria).Run()
}
