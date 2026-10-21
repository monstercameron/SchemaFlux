package provider

import (
	"context"
	"iter"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// This file turns the providers of internal/llm into schemaflux.Providers. The
// root package decides every conversion (the request, the response, the usage,
// the kind of a failure) and hands it to the packages beside it through
// internal/bind; this file calls those and keeps one thing of its own: a
// provider this package builds keeps what it can do beyond Complete, as the
// optional interfaces of optional.go, which the root's adapter does not offer.

// core is what every adapted provider is: Complete, Name and the retry settings
// of the provider underneath.
type core struct{ inner llm.Provider }

// Complete converts the request, calls the provider and converts the answer. A
// failure is a *schemaflux.Error.
func (c core) Complete(ctx context.Context, req schemaflux.Request) (schemaflux.Response, error) {
	resp, err := c.inner.Complete(ctx, bind.RequestToInternal(req))
	if err != nil {
		return schemaflux.Response{}, failure(err)
	}
	return bind.ResponseFromInternal[schemaflux.Response](resp)
}

// Name is the provider's own name.
func (c core) Name() string { return c.inner.Name() }

// RetryPolicy is the provider's retry settings in the convention of Retrier: the
// internal "not configured", a negative count, is zero here, which means the
// library's default, and the internal zero, which is never, is negative.
func (c core) RetryPolicy() (int, time.Duration) {
	retries, backoff := c.inner.RetryPolicy()
	switch {
	case retries < 0:
		retries = 0
	case retries == 0:
		retries = -1
	}
	return retries, backoff
}

// InternalProvider is the internal provider this one adapts. It is a seam for
// this module and not a part of the package's surface: the type that has it is
// not exported, and the root's conversion of a Provider to the internal one is
// to look for it, so that a built-in provider is not wrapped a second time and
// keeps its streaming, its warming and its model table on the way through.
func (c core) InternalProvider() llm.Provider { return c.inner }

// warmer adds Warm to an adapted provider whose provider can warm.
type warmer struct{ inner llm.Provider }

// Warm opens the provider's connection. A failure is a *schemaflux.Error.
func (w warmer) Warm(ctx context.Context) error {
	_, err := llm.Warm(ctx, w.inner)
	return failure(err)
}

// streamer adds Stream to an adapted provider whose provider can stream.
type streamer struct{ inner llm.StreamingProvider }

// Stream sends req and yields the chunks of the answer. A failure, before or
// during the stream, is yielded as a *schemaflux.Error and ends the sequence.
func (s streamer) Stream(ctx context.Context, req schemaflux.Request) iter.Seq2[schemaflux.Chunk, error] {
	return func(yield func(schemaflux.Chunk, error) bool) {
		for chunk, err := range s.inner.CompleteStream(ctx, bind.RequestToInternal(req)) {
			if err != nil {
				yield(schemaflux.Chunk{}, failure(err))
				return
			}
			converted, err := bind.ChunkFromInternal[schemaflux.Chunk](chunk)
			if err != nil {
				yield(schemaflux.Chunk{}, err)
				return
			}
			if !yield(converted, nil) {
				return
			}
		}
	}
}

// decider adds DecisionLimits to an adapted provider that declares limits.
type decider struct{ inner llm.Provider }

// DecisionLimits is what the provider can take.
func (d decider) DecisionLimits() schemaflux.DecisionLimits {
	limits, _ := llm.DecisionLimitsOf(d.inner)
	return bind.LimitsFromInternal[schemaflux.DecisionLimits](limits)
}

// The eight adapted providers, one for each set of optional interfaces. A type
// implements an interface by having its methods, so a provider that cannot warm
// must be a type that has no Warm: one type with every method would make every
// provider look as if it could do everything.
type (
	adapted  struct{ core }
	adaptedW struct {
		core
		warmer
	}
	adaptedS struct {
		core
		streamer
	}
	adaptedD struct {
		core
		decider
	}
	adaptedWS struct {
		core
		warmer
		streamer
	}
	adaptedWD struct {
		core
		warmer
		decider
	}
	adaptedSD struct {
		core
		streamer
		decider
	}
	adaptedWSD struct {
		core
		warmer
		streamer
		decider
	}
)

// adapt is the schemaflux.Provider for an internal provider. It implements
// Warmer when the internal provider can warm, Streamer when it can stream, and
// Decider when it declares decision limits, and Retrier always. A provider that
// is the root's adapter of a schemaflux.Provider (bind.ToInternal) is given back
// as the Provider it was made from.
func adapt(p llm.Provider) schemaflux.Provider {
	if root, ok := bind.FromInternal(p).(schemaflux.Provider); ok {
		if _, generic := root.(interface{ InternalProvider() llm.Provider }); !generic {
			return root
		}
	}
	_, warms := p.(llm.Warmer)
	streams, canStream := p.(llm.StreamingProvider)
	_, decides := llm.DecisionLimitsOf(p)

	c, w, d := core{p}, warmer{p}, decider{p}
	s := streamer{streams}
	switch {
	case warms && canStream && decides:
		return &adaptedWSD{c, w, s, d}
	case warms && canStream:
		return &adaptedWS{c, w, s}
	case warms && decides:
		return &adaptedWD{c, w, d}
	case canStream && decides:
		return &adaptedSD{c, s, d}
	case warms:
		return &adaptedW{c, w}
	case canStream:
		return &adaptedS{c, s}
	case decides:
		return &adaptedD{c, d}
	}
	return &adapted{c}
}

// failure is the *schemaflux.Error for an error of an internal provider, read as
// the root reads every error of a provider (bind.FromProvider): the kind is that
// of the first classified error of the chain, from the outside in, and then what
// the internal classifier reads (a context error, an HTTP status, a network
// failure); a failure that says nothing of its kind is the provider being
// unavailable. The fields are the route, the status, the provider's code and the
// wait the provider asked for. The error that was given stays in Err, so
// errors.Is reaches a context error and Detail reaches what the provider said;
// Error prints none of it.
func failure(err error) error { return bind.FromProvider(err) }

// buildFailure is the *schemaflux.Error for an error of building a provider. An
// error that is classified keeps its kind, and one that is not is a
// configuration error: nothing was sent, so nothing can have been unavailable.
func buildFailure(op, name string, err error) error {
	if _, known := bind.KindOfError(err); !known {
		// A cause and no message: what the builder wrote is not carried into the
		// error's words.
		err = &types.OperationError{Kind: types.KindConfiguration, Op: op, Cause: err}
	}
	converted := bind.ConvertError(op, err)
	if e, ok := converted.(*schemaflux.Error); ok && e.Provider == "" {
		named := *e
		named.Provider = name
		return &named
	}
	return converted
}
