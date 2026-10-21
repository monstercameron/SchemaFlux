package mw

import (
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/ops"

	impl "github.com/monstercameron/schemaflux/v2/internal/runtime/mw"
)

// BreakerOptions are the settings of CircuitBreaker. The zero value is the
// default: five failures open it for thirty seconds, and one clean probe closes
// it again.
type BreakerOptions struct {
	// Failures is how many failures in a row, for one provider, open the
	// circuit. Zero is 5. It must not be negative.
	Failures int

	// Successes is how many probes in a row must succeed, once the circuit is
	// half open, before it closes. Zero is 1. It must not be negative.
	Successes int

	// HalfOpenCalls is how many probes may be in flight at once while the
	// circuit is half open; the requests beyond that are refused as they are
	// while it is open. Zero is 1. It must not be negative.
	HalfOpenCalls int

	// OpenFor is how long the circuit stays open before it lets a probe
	// through. Zero is 30 seconds. It must not be negative.
	OpenFor time.Duration

	// PerModel keeps a circuit for each model of a provider and not one for
	// the provider, for a provider whose models fail independently: an outage
	// of one model then does not open the circuit for the others. The default
	// is one circuit for a provider name. At most 256 models are tracked; the
	// ones after that share their provider's circuit.
	PerModel bool
}

// CircuitBreaker stops sending requests to a provider that is failing, and
// tries it again after a while. After Failures failures in a row the circuit
// opens: requests are refused at once, without being sent, with an error of kind
// schemaflux.ErrUnavailable. After OpenFor one probe is let through; if it
// succeeds the circuit closes, and if it fails the circuit opens again.
//
// Only failures that say something about the provider count: a rate limit (429),
// a 5xx, a timeout of the provider's own, a network failure, a panic below
// (ErrInternal), and an error that is not classified at all. These do not count,
// because they say nothing about the endpoint: a cancellation or a deadline of the
// caller's, a request the provider rejected as the caller's fault (a rejected key,
// a malformed request), a refusal by the data policy, a budget or a bulkhead, a
// refusal by another breaker, and an answer that came back and was no good (cut
// off, invalid, or held for review), since a model's bad answer is not an outage. A rate limit counts, so a RateLimit in Reject mode must
// not be inside a CircuitBreaker: its own refusals would open the circuit; the
// package comment's order has it outside.
//
// A request that finds the circuit open is a refusal that Retry does not repeat,
// and that Fallback reads as a reason to use the next route: listed outside the
// breaker, a Retry returns the refusal at once and a Fallback outside both fails
// over at once, as the circuit is already known to be open.
//
// The circuits belong to the Middleware this returns: applied to two providers
// it shares its state between them, keyed by provider name, so two providers
// with the same name trip together. CircuitBreaker does not forward streaming:
// a wrapped provider cannot stream.
func CircuitBreaker(opts ...BreakerOptions) Middleware {
	o := mergeBreakerOptions(opts)

	var problem string
	switch {
	case o.Failures < 0:
		problem = "Failures must not be negative"
	case o.Successes < 0:
		problem = "Successes must not be negative"
	case o.HalfOpenCalls < 0:
		problem = "HalfOpenCalls must not be negative"
	case o.OpenFor < 0:
		problem = "OpenFor must not be negative"
	}

	var settings []impl.CircuitBreakerOption
	if o.PerModel {
		settings = append(settings, impl.KeyByModel())
	}
	build := impl.CircuitBreaker(ops.CircuitBreakerConfig{
		FailureThreshold: o.Failures,
		SuccessThreshold: o.Successes,
		HalfOpenMaxCalls: o.HalfOpenCalls,
		OpenDuration:     o.OpenFor,
	}, settings...)

	return func(next schemaflux.Provider) schemaflux.Provider {
		return wrap("mw.breaker", next, problem, build)
	}
}

// mergeBreakerOptions merges option values left to right, a later field that
// is not zero winning.
func mergeBreakerOptions(opts []BreakerOptions) BreakerOptions {
	var merged BreakerOptions
	for _, o := range opts {
		if o.Failures != 0 {
			merged.Failures = o.Failures
		}
		if o.Successes != 0 {
			merged.Successes = o.Successes
		}
		if o.HalfOpenCalls != 0 {
			merged.HalfOpenCalls = o.HalfOpenCalls
		}
		if o.OpenFor != 0 {
			merged.OpenFor = o.OpenFor
		}
		if o.PerModel {
			merged.PerModel = true
		}
	}
	return merged
}
