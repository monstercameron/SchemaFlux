package mw

import (
	"math"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"

	impl "github.com/monstercameron/schemaflux/mw"
)

// RetryOptions are the settings of Retry. The zero value is the default.
type RetryOptions struct {
	// Retries is how many times a request that failed with an error worth
	// repeating is sent again, after the first attempt: Retries of 2 sends a
	// request at most three times. Zero is the wrapped provider's own setting
	// when it states a positive count (a provider.Retrier that answers one), and
	// otherwise 2: Retry is an explicit act, so a provider that says it retries
	// nothing does not turn it off. A negative value is none: the request is sent
	// once and a failure is returned as it came, which makes Retry a
	// pass-through that still tells whatever calls the chain not to repeat it
	// either; use it to turn the middleware off without taking it out of a chain.
	Retries int

	// BaseDelay is the least wait before a repeat, and the first one when
	// nothing is stated. Zero is the wrapped provider's own backoff when it
	// states one, and otherwise 500 milliseconds. It must not be negative.
	// Later waits grow from it with random jitter, so that callers that failed
	// together do not repeat together.
	BaseDelay time.Duration

	// MaxDelay caps a computed wait. It does not cap a wait the provider asked
	// for with a Retry-After (a RetryAfter on the error), which is honoured as
	// stated up to two minutes. Zero is two minutes. It must not be negative.
	MaxDelay time.Duration
}

// Retry sends a request again when it failed with an error that repeating can
// cure. It answers the one question, whether to repeat, as the rest of the
// library does: by the Kind of the error. A request is repeated after
// ErrRateLimited, ErrUnavailable (an open circuit excepted) and ErrTimeout, and
// an error of another kind (a rejected key, a request the provider called
// malformed, a spent budget, a full bulkhead, a canceled context) is returned at
// once, since the same request would fail the same way or is not the provider's
// to cure. This is the wrapped package's rule, and it differs from
// Error.Retryable in one kind: ErrBusy, a limit the program set, is not repeated.
//
// An error that states how long to wait (RetryAfter, which a provider's 429
// carries and which RateLimit puts on its refusals) decides the wait. Other
// waits grow from BaseDelay with jitter, up to MaxDelay. The wait ends with the
// request's context, and then the context's error is the result.
//
// The repeats are made here and nowhere else. A chain that holds a Retry
// reports "never retry" as its retry settings (provider.Retrier), so a client
// that would repeat a failed request itself does not repeat the chain's repeats:
// the number of requests is this middleware's alone. Two things still multiply,
// and nothing refuses them: a Retry that wraps another, the outer one repeating
// the whole of the inner one's attempts, and a Retry around a provider that
// retries inside its own Complete. Of the two a chain gives one a negative count.
// The package comment says where Retry goes in a chain.
//
// A request is not repeated after an open circuit, which is a refusal that costs
// nothing and stays true for much longer than a wait, nor after a full bulkhead
// (ErrBusy), which is the program's own limit. A Fallback outside this Retry then
// goes on to its next route at once. A RateLimit that rejects states how long to
// wait and is repeated after it.
//
// A request that failed and was repeated successfully looks like any other
// success to the caller. Retry does not forward streaming: a wrapped provider
// cannot stream.
func Retry(opts ...RetryOptions) Middleware {
	o := mergeRetryOptions(opts)

	var problem string
	switch {
	case o.BaseDelay < 0:
		problem = "BaseDelay must not be negative"
	case o.MaxDelay < 0:
		problem = "MaxDelay must not be negative"
	}

	var settings []impl.RetryOption
	switch {
	case o.Retries > 0:
		settings = append(settings, impl.WithMaxAttempts(attemptsFor(o.Retries)))
	case o.Retries < 0:
		settings = append(settings, impl.WithMaxAttempts(1))
	}
	if o.BaseDelay > 0 {
		settings = append(settings, impl.WithBaseDelay(o.BaseDelay))
	}
	if o.MaxDelay > 0 {
		settings = append(settings, impl.WithMaxDelay(o.MaxDelay))
	}
	build := impl.Retry(settings...)

	return func(next schemaflux.Provider) schemaflux.Provider {
		return wrap("mw.retry", next, problem, build)
	}
}

// attemptsFor is the number of attempts that makes retries repeats.
func attemptsFor(retries int) int {
	if retries >= math.MaxInt {
		return math.MaxInt
	}
	return retries + 1
}

// mergeRetryOptions merges option values left to right, a later field that is
// not zero winning.
func mergeRetryOptions(opts []RetryOptions) RetryOptions {
	var merged RetryOptions
	for _, o := range opts {
		if o.Retries != 0 {
			merged.Retries = o.Retries
		}
		if o.BaseDelay != 0 {
			merged.BaseDelay = o.BaseDelay
		}
		if o.MaxDelay != 0 {
			merged.MaxDelay = o.MaxDelay
		}
	}
	return merged
}
