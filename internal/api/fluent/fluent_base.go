package fluent

import (
	"context"

	"github.com/monstercameron/schemaflux/internal/types"
)

type commonRequest[Self any, Opt any] struct {
	opts   Opt
	lift   func(Opt) Self
	mutate func(Opt, func(CommonOptions) CommonOptions) Opt
}

func (r commonRequest[Self, Opt]) WithOptions(opts Opt) Self {
	return r.lift(opts)
}

func (r commonRequest[Self, Opt]) Configure(fn func(Opt) Opt) Self {
	return r.lift(fn(r.opts))
}

func (r commonRequest[Self, Opt]) Steer(steering string) Self {
	return r.lift(r.mutate(r.opts, func(common CommonOptions) CommonOptions {
		return common.WithSteering(steering)
	}))
}

func (r commonRequest[Self, Opt]) Threshold(threshold float64) Self {
	return r.lift(r.mutate(r.opts, func(common CommonOptions) CommonOptions {
		return common.WithThreshold(threshold)
	}))
}

func (r commonRequest[Self, Opt]) Mode(mode Mode) Self {
	return r.lift(r.mutate(r.opts, func(common CommonOptions) CommonOptions {
		return common.WithMode(mode)
	}))
}

func (r commonRequest[Self, Opt]) Strict() Self {
	return r.Mode(Strict)
}

func (r commonRequest[Self, Opt]) TransformMode() Self {
	return r.Mode(TransformMode)
}

func (r commonRequest[Self, Opt]) Creative() Self {
	return r.Mode(Creative)
}

func (r commonRequest[Self, Opt]) Intelligence(level Speed) Self {
	return r.lift(r.mutate(r.opts, func(common CommonOptions) CommonOptions {
		return common.WithIntelligence(level)
	}))
}

func (r commonRequest[Self, Opt]) Smart() Self {
	return r.Intelligence(Smart)
}

func (r commonRequest[Self, Opt]) Fast() Self {
	return r.Intelligence(Fast)
}

func (r commonRequest[Self, Opt]) Quick() Self {
	return r.Intelligence(Quick)
}

func (r commonRequest[Self, Opt]) Context(ctx context.Context) Self {
	return r.lift(r.mutate(r.opts, func(common CommonOptions) CommonOptions {
		return common.WithContext(ctx)
	}))
}

func (r commonRequest[Self, Opt]) RequestID(requestID string) Self {
	return r.lift(r.mutate(r.opts, func(common CommonOptions) CommonOptions {
		return common.WithRequestID(requestID)
	}))
}

func (r commonRequest[Self, Opt]) CorrelationID(correlationID string) Self {
	return r.lift(r.mutate(r.opts, func(common CommonOptions) CommonOptions {
		return common.WithCorrelationID(correlationID)
	}))
}

type opRequest[Self any, Opt any] struct {
	opts   Opt
	lift   func(Opt) Self
	mutate func(Opt, func(types.OpOptions) types.OpOptions) Opt
}

func (r opRequest[Self, Opt]) WithOptions(opts Opt) Self {
	return r.lift(opts)
}

func (r opRequest[Self, Opt]) Configure(fn func(Opt) Opt) Self {
	return r.lift(fn(r.opts))
}

func (r opRequest[Self, Opt]) Steer(steering string) Self {
	return r.lift(r.mutate(r.opts, func(op types.OpOptions) types.OpOptions {
		op.Steering = steering
		return op
	}))
}

func (r opRequest[Self, Opt]) Threshold(threshold float64) Self {
	return r.lift(r.mutate(r.opts, func(op types.OpOptions) types.OpOptions {
		op.Threshold = threshold
		return op
	}))
}

func (r opRequest[Self, Opt]) Mode(mode Mode) Self {
	return r.lift(r.mutate(r.opts, func(op types.OpOptions) types.OpOptions {
		op.Mode = mode
		return op
	}))
}

func (r opRequest[Self, Opt]) Strict() Self {
	return r.Mode(Strict)
}

func (r opRequest[Self, Opt]) TransformMode() Self {
	return r.Mode(TransformMode)
}

func (r opRequest[Self, Opt]) Creative() Self {
	return r.Mode(Creative)
}

func (r opRequest[Self, Opt]) Intelligence(level Speed) Self {
	return r.lift(r.mutate(r.opts, func(op types.OpOptions) types.OpOptions {
		op.Intelligence = level
		return op
	}))
}

func (r opRequest[Self, Opt]) Smart() Self {
	return r.Intelligence(Smart)
}

func (r opRequest[Self, Opt]) Fast() Self {
	return r.Intelligence(Fast)
}

func (r opRequest[Self, Opt]) Quick() Self {
	return r.Intelligence(Quick)
}

func (r opRequest[Self, Opt]) Context(ctx context.Context) Self {
	return r.lift(r.mutate(r.opts, func(op types.OpOptions) types.OpOptions {
		op.Context = ctx
		return op
	}))
}

func (r opRequest[Self, Opt]) RequestID(requestID string) Self {
	return r.lift(r.mutate(r.opts, func(op types.OpOptions) types.OpOptions {
		op.RequestID = requestID
		return op
	}))
}

func (r opRequest[Self, Opt]) CorrelationID(correlationID string) Self {
	return r.lift(r.mutate(r.opts, func(op types.OpOptions) types.OpOptions {
		op.CorrelationID = correlationID
		return op
	}))
}

type directRequest[Self any, Opt any] struct {
	opts             Opt
	lift             func(Opt) Self
	setSteering      func(Opt, string) Opt
	setMode          func(Opt, Mode) Opt
	setIntelligence  func(Opt, Speed) Opt
	setContext       func(Opt, context.Context) Opt
	setRequestID     func(Opt, string) Opt
	setCorrelationID func(Opt, string) Opt
}

func (r directRequest[Self, Opt]) WithOptions(opts Opt) Self {
	return r.lift(opts)
}

func (r directRequest[Self, Opt]) Configure(fn func(Opt) Opt) Self {
	return r.lift(fn(r.opts))
}

func (r directRequest[Self, Opt]) Steer(steering string) Self {
	if r.setSteering == nil {
		return r.lift(r.opts)
	}
	return r.lift(r.setSteering(r.opts, steering))
}

func (r directRequest[Self, Opt]) Mode(mode Mode) Self {
	if r.setMode == nil {
		return r.lift(r.opts)
	}
	return r.lift(r.setMode(r.opts, mode))
}

func (r directRequest[Self, Opt]) Strict() Self {
	return r.Mode(Strict)
}

func (r directRequest[Self, Opt]) TransformMode() Self {
	return r.Mode(TransformMode)
}

func (r directRequest[Self, Opt]) Creative() Self {
	return r.Mode(Creative)
}

func (r directRequest[Self, Opt]) Intelligence(level Speed) Self {
	if r.setIntelligence == nil {
		return r.lift(r.opts)
	}
	return r.lift(r.setIntelligence(r.opts, level))
}

func (r directRequest[Self, Opt]) Smart() Self {
	return r.Intelligence(Smart)
}

func (r directRequest[Self, Opt]) Fast() Self {
	return r.Intelligence(Fast)
}

func (r directRequest[Self, Opt]) Quick() Self {
	return r.Intelligence(Quick)
}

func (r directRequest[Self, Opt]) Context(ctx context.Context) Self {
	if r.setContext == nil {
		return r.lift(r.opts)
	}
	return r.lift(r.setContext(r.opts, ctx))
}

func (r directRequest[Self, Opt]) RequestID(requestID string) Self {
	if r.setRequestID == nil {
		return r.lift(r.opts)
	}
	return r.lift(r.setRequestID(r.opts, requestID))
}

func (r directRequest[Self, Opt]) CorrelationID(correlationID string) Self {
	if r.setCorrelationID == nil {
		return r.lift(r.opts)
	}
	return r.lift(r.setCorrelationID(r.opts, correlationID))
}
