package mw

import (
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"

	impl "github.com/monstercameron/schemaflux/mw"
)

// RateLimitOptions are the settings of RateLimit. The zero value is the
// default.
type RateLimitOptions struct {
	// Burst is how many requests may go at once when the limiter has been
	// idle: the capacity of the bucket, which starts full. Zero is n, the
	// number of requests per interval. A larger Burst allows a big first
	// batch with a slow refill. It must not be negative.
	Burst int

	// Reject makes a request that would exceed the limit fail at once with an
	// error of kind schemaflux.ErrRateLimited whose RetryAfter says how long
	// until a slot is due, instead of waiting for the slot. The default is to
	// wait, bounded by the request's context.
	Reject bool
}

// RateLimit holds requests to at most n per per, on the client's side. It is
// not the provider's limit, which protects the provider and is learned by
// being refused: it is a ceiling the program sets on purpose, so that a batch
// that could fire 500 requests at once stays inside its own quota.
//
// By default a request that would exceed the limit waits for a slot, and the
// wait ends with its context. With Reject it fails at once with ErrRateLimited
// and a RetryAfter, which Retry reads as the wait the way it reads a provider's
// 429. An n below one, and a per below zero, are unusable settings, and fail every
// request with an error of kind schemaflux.ErrConfiguration, as a negative Burst
// does; a per of zero is a second.
//
// Waiting requests are not served in the order they arrived. The limit holds
// and none is refused, but under steady load one request's wait is not bounded
// by its place in a queue; a program that needs first come, first served puts
// its own queue in front of the chain.
//
// The bucket belongs to the Middleware this returns, not to a provider: the
// same Middleware applied to two providers limits them together. Where it goes
// in a chain decides what it counts; the package comment says. RateLimit does
// not forward streaming: a wrapped provider cannot stream.
func RateLimit(n int, per time.Duration, opts ...RateLimitOptions) Middleware {
	o := mergeRateLimitOptions(opts)

	var problem string
	switch {
	case n < 1:
		problem = "n must be at least one request"
	case per < 0:
		problem = "per must not be negative"
	case o.Burst < 0:
		problem = "Burst must not be negative"
	}

	var settings []impl.RateLimitOption
	if o.Burst > 0 {
		settings = append(settings, impl.WithBurst(o.Burst))
	}
	if o.Reject {
		settings = append(settings, impl.Reject())
	}
	build := impl.RateLimit(n, per, settings...)

	return func(next schemaflux.Provider) schemaflux.Provider {
		return wrap("mw.ratelimit", next, problem, build)
	}
}

// mergeRateLimitOptions merges option values left to right, a later field that
// is not zero winning.
func mergeRateLimitOptions(opts []RateLimitOptions) RateLimitOptions {
	var merged RateLimitOptions
	for _, o := range opts {
		if o.Burst != 0 {
			merged.Burst = o.Burst
		}
		if o.Reject {
			merged.Reject = true
		}
	}
	return merged
}
