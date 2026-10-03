// Package mw provides a middleware chain for llm.Provider.
//
// A provider is one method worth caring about -- Complete -- so the seam a
// cross-cutting concern hangs on is small on purpose: a Middleware takes a
// Handler and returns a Handler, exactly like net/http's middleware shape.
// Composing them is just function composition, which is why rate limiting,
// retry, caching (MW-004), budget enforcement (MW-005), egress redaction
// (MW-006), metrics (MW-007), and fallback routing (MW-008) can all be
// written as independent, unexported provider wrappers behind this same
// public Chain -- none of them need to know the others exist, and none of
// them need this file to change to be added.
//
// # Composing at client construction
//
// SchemaFlux's *Client already has the seam this package needs:
// WithProviderInstance(provider llm.Provider) accepts anything satisfying the
// interface. A caller wires the chain once, at construction, and every
// operation the client performs goes through it:
//
//	base, _ := llm.CreateProvider("openai", cfg)
//	client := schemaflux.NewClient(apiKey).
//	    WithProviderInstance(mw.Chain(base,
//	        mw.RateLimit(5, time.Second),
//	        mw.Retry(),
//	    ))
//
// Middlewares run in the order listed: the first one wraps every other, so
// it sees a request first and a response or error last. In the example
// above, RateLimit is outside Retry, so it counts a call once and the repeats
// Retry makes go past it: the limiter paces calls and not attempts. A limiter
// that has to count every attempt, a repeat included, is listed after Retry,
// inside it, where each attempt takes a token and a repeat waits for the bucket
// to refill.
package mw

import (
	"context"
	"fmt"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
)

// Handler is the thing a middleware wraps: anything that can complete a
// request. It is llm.Provider under another name -- the alias exists so this
// package's exported signatures read as "handler in, handler out" rather than
// naming the internal package at every call site, without introducing a
// second interface a provider would have to satisfy twice.
type Handler = llm.Provider

// Middleware adapts one Handler into another. A middleware that does nothing
// but call through is the identity function on this type, which is the
// contract every concrete middleware in this package (and any added later)
// has to honour: nothing here may change what a downstream operation
// receives on success, only decide whether and when the call happens and
// what to do with a failure.
type Middleware func(next Handler) Handler

// Chain wraps base with mws, applied in the order listed: mws[0] is the
// outermost wrapper, so it is the first to see a call and the last to see its
// result. That ordering matters for how the pieces interact -- a rate limiter
// listed before a retry counts the call once, and the repeats of the retry go
// past it without taking a token; listed after, inside the retry, it counts each
// attempt, the first and every repeat, and a repeat waits for the bucket. A limit
// that is the provider's own quota, to be kept whatever is sent, goes inside; a
// limit on calls the program makes goes outside.
//
// Chain with no middlewares returns base unchanged, so wiring in an empty or
// nil-filtered options slice at a call site never has to special-case "no
// middleware wanted."
func Chain(base Handler, mws ...Middleware) Handler {
	wrapped := base
	for i := len(mws) - 1; i >= 0; i-- {
		if mws[i] == nil {
			continue
		}
		wrapped = mws[i](wrapped)
	}
	return wrapped
}

// logPanic writes the line for a panic that was recovered in code of the
// caller's, to the logger of the client the call was made with, and holds only the
// type of what was thrown: its value can hold the text it was handed.
func logPanic(ctx context.Context, message string, thrown any) {
	ops.ExecLogger(ctx).Error(message, "thrown", fmt.Sprintf("%T", thrown))
}

// kindIsTerminal reports whether nothing this library can do will help for a
// failure of the given kind, which is the answer types.OperationError.Terminal
// gives. types.ErrorKind carries no such method, so the question is asked of an
// error holding only the kind.
func kindIsTerminal(kind types.ErrorKind) bool {
	return (&types.OperationError{Kind: kind}).Terminal()
}

// kindIsRetryable reports whether repeating the same request could succeed for
// a failure of the given kind, which is the answer types.OperationError.Retryable
// gives.
func kindIsRetryable(kind types.ErrorKind) bool {
	return (&types.OperationError{Kind: kind}).Retryable()
}

// realSleep waits for d or until ctx is done, whichever comes first. It is
// the production implementation of the sleep hook every timing-based
// middleware in this package uses; tests substitute a fake that advances a
// fake clock instead of the wall clock, so a rate-limit or backoff test does
// not have to actually wait out real seconds to be correct.
func realSleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
