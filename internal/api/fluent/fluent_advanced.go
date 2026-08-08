package fluent

import "context"

// NegotiateRequest is a fluent builder for Negotiate.
type NegotiateRequest[T any] struct {
	requestBase[NegotiateRequest[T], NegotiateOptions]
	constraints any
}

func newNegotiateRequest[T any](constraints any, opts NegotiateOptions) NegotiateRequest[T] {
	return NegotiateRequest[T]{
		requestBase: requestBase[NegotiateRequest[T], NegotiateOptions]{
			opts: opts,
			lift: func(next NegotiateOptions) NegotiateRequest[T] {
				return newNegotiateRequest[T](constraints, next)
			},
			setSteering: func(current NegotiateOptions, steering string) NegotiateOptions {
				current.Steering = accumulateSteering(current.Steering, steering)
				return current
			},
			setMode: func(current NegotiateOptions, mode Mode) NegotiateOptions {
				current.Mode = mode
				return current
			},
			setIntelligence: func(current NegotiateOptions, level Speed) NegotiateOptions {
				current.Intelligence = level
				return current
			},
			setContext: func(current NegotiateOptions, ctx context.Context) NegotiateOptions {
				current.Context = ctx
				return current
			},
			setRequestID: func(current NegotiateOptions, requestID string) NegotiateOptions {
				current.RequestID = requestID
				return current
			},
			setCorrelationID: func(current NegotiateOptions, correlationID string) NegotiateOptions {
				current.CorrelationID = correlationID
				return current
			},
		},
		constraints: constraints,
	}
}

// Negotiating starts a fluent Negotiate request.
//
// Despite the name, Run resolves it in a single model round trip -- see
// ops.Negotiate's doc comment for what that means and where the real
// multi-turn primitive (ops.Session) lives (PS-005, Revised ARC-20).
func Negotiating[T any](constraints any) NegotiateRequest[T] {
	return newNegotiateRequest[T](constraints, NewNegotiateOptions())
}

func (r NegotiateRequest[T]) Strategy(strategy string) NegotiateRequest[T] {
	opts := r.opts
	opts.Strategy = strategy
	return r.WithOptions(opts)
}

func (r NegotiateRequest[T]) MinimumSatisfaction(minimum float64) NegotiateRequest[T] {
	opts := r.opts
	opts.MinSatisfaction = minimum
	return r.WithOptions(opts)
}

func (r NegotiateRequest[T]) Run() (NegotiateResult[T], error) {
	if err := buildError(r.opts); err != nil {
		var zero NegotiateResult[T]
		return zero, err
	}
	return Negotiate[T](r.constraints, r.opts)
}

// AdversarialNegotiationRequest is a fluent builder for NegotiateAdversarial.
type AdversarialNegotiationRequest[T any] struct {
	requestBase[AdversarialNegotiationRequest[T], AdversarialOptions]
	context AdversarialContext[T]
}

func newAdversarialNegotiationRequest[T any](ctx AdversarialContext[T], opts AdversarialOptions) AdversarialNegotiationRequest[T] {
	return AdversarialNegotiationRequest[T]{
		requestBase: requestBase[AdversarialNegotiationRequest[T], AdversarialOptions]{
			opts: opts,
			lift: func(next AdversarialOptions) AdversarialNegotiationRequest[T] {
				return newAdversarialNegotiationRequest(ctx, next)
			},
			setSteering: func(current AdversarialOptions, steering string) AdversarialOptions {
				current.Steering = accumulateSteering(current.Steering, steering)
				return current
			},
			setMode: func(current AdversarialOptions, mode Mode) AdversarialOptions {
				current.Mode = mode
				return current
			},
			setIntelligence: func(current AdversarialOptions, level Speed) AdversarialOptions {
				current.Intelligence = level
				return current
			},
			setContext: func(current AdversarialOptions, context context.Context) AdversarialOptions {
				current.Context = context
				return current
			},
			setRequestID: func(current AdversarialOptions, requestID string) AdversarialOptions {
				current.RequestID = requestID
				return current
			},
			setCorrelationID: func(current AdversarialOptions, correlationID string) AdversarialOptions {
				current.CorrelationID = correlationID
				return current
			},
		},
		context: ctx,
	}
}

// NegotiatingAdversarially starts a fluent NegotiateAdversarial request.
//
// Despite the two-party framing, Run resolves it in a single model round
// trip -- see ops.NegotiateAdversarial's doc comment for what that means and
// where the real multi-turn primitive (ops.Session) lives (PS-005, Revised
// ARC-20).
func NegotiatingAdversarially[T any](ctx AdversarialContext[T]) AdversarialNegotiationRequest[T] {
	return newAdversarialNegotiationRequest(ctx, NewAdversarialOptions())
}

func (r AdversarialNegotiationRequest[T]) Strategy(strategy string) AdversarialNegotiationRequest[T] {
	opts := r.opts
	opts.Strategy = strategy
	return r.WithOptions(opts)
}

func (r AdversarialNegotiationRequest[T]) Run() (AdversarialResult[T], error) {
	if err := buildError(r.opts); err != nil {
		var zero AdversarialResult[T]
		return zero, err
	}
	return NegotiateAdversarial[T](r.context, r.opts)
}

// ResolveRequest is a fluent builder for Resolve.
type ResolveRequest[T any] struct {
	requestBase[ResolveRequest[T], ResolveOptions]
	sources []T
}

func newResolveRequest[T any](sources []T, opts ResolveOptions) ResolveRequest[T] {
	return ResolveRequest[T]{
		requestBase: requestBase[ResolveRequest[T], ResolveOptions]{
			opts: opts,
			lift: func(next ResolveOptions) ResolveRequest[T] {
				return newResolveRequest(sources, next)
			},
			setSteering: func(current ResolveOptions, steering string) ResolveOptions {
				current.Steering = accumulateSteering(current.Steering, steering)
				return current
			},
			setMode: func(current ResolveOptions, mode Mode) ResolveOptions {
				current.Mode = mode
				return current
			},
			setIntelligence: func(current ResolveOptions, level Speed) ResolveOptions {
				current.Intelligence = level
				return current
			},
			setContext: func(current ResolveOptions, ctx context.Context) ResolveOptions {
				current.Context = ctx
				return current
			},
			setRequestID: func(current ResolveOptions, requestID string) ResolveOptions {
				current.RequestID = requestID
				return current
			},
			setCorrelationID: func(current ResolveOptions, correlationID string) ResolveOptions {
				current.CorrelationID = correlationID
				return current
			},
		},
		sources: sources,
	}
}

// Resolving starts a fluent Resolve request.
func Resolving[T any](sources []T) ResolveRequest[T] {
	return newResolveRequest(sources, NewResolveOptions())
}

func (r ResolveRequest[T]) Strategy(strategy string) ResolveRequest[T] {
	opts := r.opts
	opts.Strategy = strategy
	return r.WithOptions(opts)
}

func (r ResolveRequest[T]) Run() (ResolveResult[T], error) {
	if err := buildError(r.opts); err != nil {
		var zero ResolveResult[T]
		return zero, err
	}
	return Resolve[T](r.sources, r.opts)
}

// DeriveRequest is a fluent builder for Derive.
type DeriveRequest[T any, U any] struct {
	requestBase[DeriveRequest[T, U], DeriveOptions]
	input T
}

func newDeriveRequest[T any, U any](input T, opts DeriveOptions) DeriveRequest[T, U] {
	return DeriveRequest[T, U]{
		requestBase: requestBase[DeriveRequest[T, U], DeriveOptions]{
			opts: opts,
			lift: func(next DeriveOptions) DeriveRequest[T, U] {
				return newDeriveRequest[T, U](input, next)
			},
			setSteering: func(current DeriveOptions, steering string) DeriveOptions {
				current.Steering = accumulateSteering(current.Steering, steering)
				return current
			},
			setMode: func(current DeriveOptions, mode Mode) DeriveOptions {
				current.Mode = mode
				return current
			},
			setIntelligence: func(current DeriveOptions, level Speed) DeriveOptions {
				current.Intelligence = level
				return current
			},
			setContext: func(current DeriveOptions, ctx context.Context) DeriveOptions {
				current.Context = ctx
				return current
			},
			setRequestID: func(current DeriveOptions, requestID string) DeriveOptions {
				current.RequestID = requestID
				return current
			},
			setCorrelationID: func(current DeriveOptions, correlationID string) DeriveOptions {
				current.CorrelationID = correlationID
				return current
			},
		},
		input: input,
	}
}

// Deriving starts a fluent Derive request.
func Deriving[T any, U any](input T) DeriveRequest[T, U] {
	return newDeriveRequest[T, U](input, NewDeriveOptions())
}

func (r DeriveRequest[T, U]) Fields(fields ...string) DeriveRequest[T, U] {
	opts := r.opts
	opts.Fields = append([]string(nil), fields...)
	return r.WithOptions(opts)
}

func (r DeriveRequest[T, U]) Run() (DeriveResult[U], error) {
	if err := buildError(r.opts); err != nil {
		var zero DeriveResult[U]
		return zero, err
	}
	return Derive[T, U](r.input, r.opts)
}

// ConformRequest is a fluent builder for Conform.
type ConformRequest[T any] struct {
	requestBase[ConformRequest[T], ConformOptions]
	input    T
	standard string
}

func newConformRequest[T any](input T, standard string, opts ConformOptions) ConformRequest[T] {
	return ConformRequest[T]{
		requestBase: requestBase[ConformRequest[T], ConformOptions]{
			opts: opts,
			lift: func(next ConformOptions) ConformRequest[T] {
				return newConformRequest(input, standard, next)
			},
			setSteering: func(current ConformOptions, steering string) ConformOptions {
				current.Steering = accumulateSteering(current.Steering, steering)
				return current
			},
			setMode: func(current ConformOptions, mode Mode) ConformOptions {
				current.Mode = mode
				return current
			},
			setIntelligence: func(current ConformOptions, level Speed) ConformOptions {
				current.Intelligence = level
				return current
			},
			setContext: func(current ConformOptions, ctx context.Context) ConformOptions {
				current.Context = ctx
				return current
			},
			setRequestID: func(current ConformOptions, requestID string) ConformOptions {
				current.RequestID = requestID
				return current
			},
			setCorrelationID: func(current ConformOptions, correlationID string) ConformOptions {
				current.CorrelationID = correlationID
				return current
			},
		},
		input:    input,
		standard: standard,
	}
}

// Conforming starts a fluent Conform request.
func Conforming[T any](input T, standard string) ConformRequest[T] {
	return newConformRequest(input, standard, NewConformOptions())
}

func (r ConformRequest[T]) Strictly(strict bool) ConformRequest[T] {
	opts := r.opts
	opts.Strict = strict
	return r.WithOptions(opts)
}

func (r ConformRequest[T]) Run() (ConformResult[T], error) {
	if err := buildError(r.opts); err != nil {
		var zero ConformResult[T]
		return zero, err
	}
	return Conform[T](r.input, r.standard, r.opts)
}

// InterpolateRequest is a fluent builder for Interpolate.
type InterpolateRequest[T any] struct {
	requestBase[InterpolateRequest[T], InterpolateOptions]
	items []T
}

func newInterpolateRequest[T any](items []T, opts InterpolateOptions) InterpolateRequest[T] {
	return InterpolateRequest[T]{
		requestBase: requestBase[InterpolateRequest[T], InterpolateOptions]{
			opts: opts,
			lift: func(next InterpolateOptions) InterpolateRequest[T] {
				return newInterpolateRequest(items, next)
			},
			setSteering: func(current InterpolateOptions, steering string) InterpolateOptions {
				current.Steering = accumulateSteering(current.Steering, steering)
				return current
			},
			setMode: func(current InterpolateOptions, mode Mode) InterpolateOptions {
				current.Mode = mode
				return current
			},
			setIntelligence: func(current InterpolateOptions, level Speed) InterpolateOptions {
				current.Intelligence = level
				return current
			},
			setContext: func(current InterpolateOptions, ctx context.Context) InterpolateOptions {
				current.Context = ctx
				return current
			},
			setRequestID: func(current InterpolateOptions, requestID string) InterpolateOptions {
				current.RequestID = requestID
				return current
			},
			setCorrelationID: func(current InterpolateOptions, correlationID string) InterpolateOptions {
				current.CorrelationID = correlationID
				return current
			},
		},
		items: items,
	}
}

// Interpolating starts a fluent Interpolate request.
func Interpolating[T any](items []T) InterpolateRequest[T] {
	return newInterpolateRequest(items, NewInterpolateOptions())
}

func (r InterpolateRequest[T]) Run() (InterpolateResult[T], error) {
	if err := buildError(r.opts); err != nil {
		var zero InterpolateResult[T]
		return zero, err
	}
	return Interpolate[T](r.items, r.opts)
}

// ArbitrateRequest is a fluent builder for Arbitrate.
type ArbitrateRequest[T any] struct {
	requestBase[ArbitrateRequest[T], ArbitrateOptions]
	options []T
}

func newArbitrateRequest[T any](options []T, opts ArbitrateOptions) ArbitrateRequest[T] {
	return ArbitrateRequest[T]{
		requestBase: requestBase[ArbitrateRequest[T], ArbitrateOptions]{
			opts: opts,
			lift: func(next ArbitrateOptions) ArbitrateRequest[T] {
				return newArbitrateRequest(options, next)
			},
			setSteering: func(current ArbitrateOptions, steering string) ArbitrateOptions {
				current.Steering = accumulateSteering(current.Steering, steering)
				return current
			},
			setMode: func(current ArbitrateOptions, mode Mode) ArbitrateOptions {
				current.Mode = mode
				return current
			},
			setIntelligence: func(current ArbitrateOptions, level Speed) ArbitrateOptions {
				current.Intelligence = level
				return current
			},
			setContext: func(current ArbitrateOptions, ctx context.Context) ArbitrateOptions {
				current.Context = ctx
				return current
			},
			setRequestID: func(current ArbitrateOptions, requestID string) ArbitrateOptions {
				current.RequestID = requestID
				return current
			},
			setCorrelationID: func(current ArbitrateOptions, correlationID string) ArbitrateOptions {
				current.CorrelationID = correlationID
				return current
			},
		},
		options: options,
	}
}

// Arbitrating starts a fluent Arbitrate request.
func Arbitrating[T any](options []T) ArbitrateRequest[T] {
	return newArbitrateRequest(options, NewArbitrateOptions())
}

func (r ArbitrateRequest[T]) Rules(rules ...string) ArbitrateRequest[T] {
	opts := r.opts
	opts.Rules = append([]string(nil), rules...)
	return r.WithOptions(opts)
}

func (r ArbitrateRequest[T]) Run() (ArbitrateResult[T], error) {
	if err := buildError(r.opts); err != nil {
		var zero ArbitrateResult[T]
		return zero, err
	}
	return Arbitrate[T](r.options, r.opts)
}

// ProjectRequest is a fluent builder for Project.
type ProjectRequest[T any, U any] struct {
	requestBase[ProjectRequest[T, U], ProjectOptions]
	input T
}

func newProjectRequest[T any, U any](input T, opts ProjectOptions) ProjectRequest[T, U] {
	return ProjectRequest[T, U]{
		requestBase: requestBase[ProjectRequest[T, U], ProjectOptions]{
			opts: opts,
			lift: func(next ProjectOptions) ProjectRequest[T, U] {
				return newProjectRequest[T, U](input, next)
			},
			setSteering: func(current ProjectOptions, steering string) ProjectOptions {
				current.Steering = accumulateSteering(current.Steering, steering)
				return current
			},
			setMode: func(current ProjectOptions, mode Mode) ProjectOptions {
				current.Mode = mode
				return current
			},
			setIntelligence: func(current ProjectOptions, level Speed) ProjectOptions {
				current.Intelligence = level
				return current
			},
			setContext: func(current ProjectOptions, ctx context.Context) ProjectOptions {
				current.Context = ctx
				return current
			},
			setRequestID: func(current ProjectOptions, requestID string) ProjectOptions {
				current.RequestID = requestID
				return current
			},
			setCorrelationID: func(current ProjectOptions, correlationID string) ProjectOptions {
				current.CorrelationID = correlationID
				return current
			},
		},
		input: input,
	}
}

// Projecting starts a fluent Project request.
func Projecting[T any, U any](input T) ProjectRequest[T, U] {
	return newProjectRequest[T, U](input, NewProjectOptions())
}

func (r ProjectRequest[T, U]) Exclude(fields ...string) ProjectRequest[T, U] {
	opts := r.opts
	opts.Exclude = append([]string(nil), fields...)
	return r.WithOptions(opts)
}

func (r ProjectRequest[T, U]) Run() (ProjectResult[U], error) {
	if err := buildError(r.opts); err != nil {
		var zero ProjectResult[U]
		return zero, err
	}
	return Project[T, U](r.input, r.opts)
}

// AuditRequest is a fluent builder for Audit.
type AuditRequest[T any] struct {
	requestBase[AuditRequest[T], AuditOptions]
	input T
}

func newAuditRequest[T any](input T, opts AuditOptions) AuditRequest[T] {
	return AuditRequest[T]{
		requestBase: requestBase[AuditRequest[T], AuditOptions]{
			opts: opts,
			lift: func(next AuditOptions) AuditRequest[T] {
				return newAuditRequest(input, next)
			},
			setSteering: func(current AuditOptions, steering string) AuditOptions {
				current.Steering = accumulateSteering(current.Steering, steering)
				return current
			},
			// AuditOptions is the one type among fluent_advanced.go's eleven
			// that has a Threshold field (minimum severity to report,
			// internal/ops/audit.go) -- .Threshold() was previously
			// unreachable on AuditRequest because directRequest had no
			// Threshold setter slot at all. requestBase adding one closes
			// that gap for free; the other ten leave setThreshold nil and
			// .Threshold() stays a documented no-op for them, same as before.
			setThreshold: func(current AuditOptions, threshold float64) AuditOptions {
				current.Threshold = threshold
				return current
			},
			setMode: func(current AuditOptions, mode Mode) AuditOptions {
				current.Mode = mode
				return current
			},
			setIntelligence: func(current AuditOptions, level Speed) AuditOptions {
				current.Intelligence = level
				return current
			},
			setContext: func(current AuditOptions, ctx context.Context) AuditOptions {
				current.Context = ctx
				return current
			},
			setRequestID: func(current AuditOptions, requestID string) AuditOptions {
				current.RequestID = requestID
				return current
			},
			setCorrelationID: func(current AuditOptions, correlationID string) AuditOptions {
				current.CorrelationID = correlationID
				return current
			},
		},
		input: input,
	}
}

// Auditing starts a fluent Audit request.
func Auditing[T any](input T) AuditRequest[T] {
	return newAuditRequest(input, NewAuditOptions())
}

func (r AuditRequest[T]) Policies(policies ...string) AuditRequest[T] {
	opts := r.opts
	opts.Policies = append([]string(nil), policies...)
	return r.WithOptions(opts)
}

func (r AuditRequest[T]) Categories(categories ...string) AuditRequest[T] {
	opts := r.opts
	opts.Categories = append([]string(nil), categories...)
	return r.WithOptions(opts)
}

func (r AuditRequest[T]) Run() (AuditResult[T], error) {
	if err := buildError(r.opts); err != nil {
		var zero AuditResult[T]
		return zero, err
	}
	return Audit[T](r.input, r.opts)
}

// AssembleRequest is a fluent builder for Assemble.
type AssembleRequest[T any] struct {
	requestBase[AssembleRequest[T], ComposeOptions]
	parts []any
}

func newAssembleRequest[T any](parts []any, opts ComposeOptions) AssembleRequest[T] {
	return AssembleRequest[T]{
		requestBase: requestBase[AssembleRequest[T], ComposeOptions]{
			opts: opts,
			lift: func(next ComposeOptions) AssembleRequest[T] {
				return newAssembleRequest[T](parts, next)
			},
			setSteering: func(current ComposeOptions, steering string) ComposeOptions {
				current.Steering = accumulateSteering(current.Steering, steering)
				return current
			},
			setMode: func(current ComposeOptions, mode Mode) ComposeOptions {
				current.Mode = mode
				return current
			},
			setIntelligence: func(current ComposeOptions, level Speed) ComposeOptions {
				current.Intelligence = level
				return current
			},
			setContext: func(current ComposeOptions, ctx context.Context) ComposeOptions {
				current.Context = ctx
				return current
			},
			setRequestID: func(current ComposeOptions, requestID string) ComposeOptions {
				current.RequestID = requestID
				return current
			},
			setCorrelationID: func(current ComposeOptions, correlationID string) ComposeOptions {
				current.CorrelationID = correlationID
				return current
			},
		},
		parts: parts,
	}
}

// Assembling starts a fluent Assemble request.
func Assembling[T any](parts []any) AssembleRequest[T] {
	return newAssembleRequest[T](parts, NewComposeOptions())
}

func (r AssembleRequest[T]) Run() (ComposeResult[T], error) {
	if err := buildError(r.opts); err != nil {
		var zero ComposeResult[T]
		return zero, err
	}
	return Assemble[T](r.parts, r.opts)
}

// PivotRequest is a fluent builder for Pivot.
type PivotRequest[T any, U any] struct {
	requestBase[PivotRequest[T, U], PivotOptions]
	input T
}

func newPivotRequest[T any, U any](input T, opts PivotOptions) PivotRequest[T, U] {
	return PivotRequest[T, U]{
		requestBase: requestBase[PivotRequest[T, U], PivotOptions]{
			opts: opts,
			lift: func(next PivotOptions) PivotRequest[T, U] {
				return newPivotRequest[T, U](input, next)
			},
			setSteering: func(current PivotOptions, steering string) PivotOptions {
				current.Steering = accumulateSteering(current.Steering, steering)
				return current
			},
			setMode: func(current PivotOptions, mode Mode) PivotOptions {
				current.Mode = mode
				return current
			},
			setIntelligence: func(current PivotOptions, level Speed) PivotOptions {
				current.Intelligence = level
				return current
			},
			setContext: func(current PivotOptions, ctx context.Context) PivotOptions {
				current.Context = ctx
				return current
			},
			setRequestID: func(current PivotOptions, requestID string) PivotOptions {
				current.RequestID = requestID
				return current
			},
			setCorrelationID: func(current PivotOptions, correlationID string) PivotOptions {
				current.CorrelationID = correlationID
				return current
			},
		},
		input: input,
	}
}

// Pivoting starts a fluent Pivot request.
func Pivoting[T any, U any](input T) PivotRequest[T, U] {
	return newPivotRequest[T, U](input, NewPivotOptions())
}

func (r PivotRequest[T, U]) Run() (PivotResult[U], error) {
	if err := buildError(r.opts); err != nil {
		var zero PivotResult[U]
		return zero, err
	}
	return Pivot[T, U](r.input, r.opts)
}
