package provider

import (
	"context"
	"iter"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// A Streamer is a Provider that can deliver an answer as it is written, in
// pieces, and not only whole. Stream sends req and returns a sequence of
// chunks: each carries the text that arrived since the last, and the last has
// Done set and Response holding the whole answer.
//
// A stream that breaks is an error, never a short answer: the sequence yields
// the failure and ends, and no Done chunk is built from the text that arrived
// before it broke. Stopping the iteration early abandons the request.
type Streamer interface {
	Stream(ctx context.Context, req schemaflux.Request) iter.Seq2[schemaflux.Chunk, error]
}

// A Warmer is a Provider that can open its connection ahead of the first
// request, so that the first request does not pay for the name lookup and the
// handshake. Warm sends no prompt and costs nothing.
type Warmer interface {
	Warm(ctx context.Context) error
}

// A Retrier is a Provider that has retry settings of its own. retries is how
// many times a request that failed in transit is sent again: zero means the
// library's default, and a negative value means never. backoff is the wait
// before the first of those repeats; zero means the library's default.
//
// A provider that is not a Retrier is retried as the client says (the client's
// Retries). A provider built by New is one, and answers the settings of its
// Config.
type Retrier interface {
	RetryPolicy() (retries int, backoff time.Duration)
}

// An Unwrapper is a Provider that wraps another, such as a retry, a cache or a
// logging layer. Unwrap returns the wrapped provider, so that the library can
// find the optional interfaces of the provider underneath: a client that is
// asked to warm a provider, or to stream, looks at the provider it holds and
// then at what Unwrap returns, and so on down the chain. A wrapper that does not
// implement Unwrapper hides everything its provider can do beyond Complete.
type Unwrapper interface {
	Unwrap() schemaflux.Provider
}

// A Decider is a decision provider: one that answers the typed questions of a
// Request.Decision, with Response.Answers, instead of writing text. "jev",
// "jev-openrouter" and "jev-litellm" are Deciders.
//
// DecisionLimits says what it can take, and a request past a limit, or with no
// decision at all, is refused before anything is sent, with an error of kind
// schemaflux.ErrUnsupported: a layer that holds a decision provider and a
// provider that generates text reads that kind as "ask the other one". A
// decision provider is usually wrapped with a generating one by the mw package.
type Decider interface {
	DecisionLimits() schemaflux.DecisionLimits
}
