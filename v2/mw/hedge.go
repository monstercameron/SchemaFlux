package mw

import (
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"

	impl "github.com/monstercameron/schemaflux/mw"
)

// Hedge sends a second copy of a request that has not answered within after, and
// uses whichever of the two answers first, canceling the other. It is for the
// slow tail: a model that usually answers in a second will now and then take
// twenty, for reasons that have nothing to do with the request, and a second copy
// sent to the same place usually comes back in the usual second. Set after a
// little above what a normal answer takes, twice the median say, and nine
// requests in ten are sent once and the tenth is over in after plus a normal
// answer.
//
// Whether it happened is in the answer: Response.Hedged is true when a second
// copy was sent, whichever copy answered. A Record counts it. A Hedge outside a
// Cache sets it too, though the cache joins the copies into one request to the
// provider: the mark says that a second copy was sent, not that it was billed.
//
// What it costs: the second copy is a second request, and the provider bills for
// what the copy that was canceled had generated before it was canceled. A request
// the provider stores is stored twice. The library records the cost of the answer
// that was used; the abandoned copy's share is on no ledger, so no budget sees it,
// and a program that hedges leaves headroom. A request that fails before the delay
// is returned as it is: trying a failure again is what Retry is for. After the
// second copy has gone out the first to succeed is the answer, and when both fail
// the first copy's error is returned. If the caller's context ends while a copy
// has answered already, that answer is returned; while a copy has failed already,
// that copy's error is, not the context's.
//
// Every other middleware of this package waits for the provider, and a provider
// that ignores its context holds the call past its deadline. Hedge is the one
// that returns when the caller's context ends while a copy is still running, so
// the copy that is left outlives the call: it ends when the provider honours the
// cancellation Hedge gives it, and not before. A provider that does not honour its
// context leaves a goroutine behind for as long as it blocks, for each call that
// was given up on, and no layer can stop it.
//
// A copy that panics is an error of kind schemaflux.ErrInternal, with no value of
// the panic in it, and the stack is logged: each copy runs on a goroutine of its
// own, where the caller could not recover it.
//
// The timing of an answer is that of the copy that won, measured from that
// copy's start; when the second copy wins, the delay before it was sent is not
// in it.
//
// A delay of zero sends every request once; one below zero is an unusable
// setting, and fails every request with an error of kind
// schemaflux.ErrConfiguration. A request costs a goroutine, a channel and a timer
// whether or not a second copy is sent, which is the price of being able to send
// it. Where Hedge goes in a chain
// decides what is counted twice: a RateLimit or a Bulkhead that must count both
// copies goes inside Hedge, where it sees each copy as a request, and a Cache
// goes outside, so that a hit is answered without being hedged; the package
// comment gives the whole order. Hedge does not forward streaming: a wrapped
// provider cannot stream.
func Hedge(after time.Duration) Middleware {
	var problem string
	if after < 0 {
		problem = "after must not be negative"
	}
	build := impl.Hedge(after)
	return func(next schemaflux.Provider) schemaflux.Provider {
		return wrap("mw.hedge", next, problem, build)
	}
}
