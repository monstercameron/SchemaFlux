// Package mw wraps a provider so that requests to it are retried, rate limited,
// bounded, cached, hedged, redacted, kept within a budget, sent to a fallback, or
// routed between a decision model and a generating one.
//
// A Middleware takes a schemaflux.Provider and returns another, which does what
// the first does and one thing more. Chain composes them, and the result goes
// where a provider goes:
//
//	base, err := provider.New("openai", provider.Config{})
//	if err != nil {
//		return err
//	}
//	chain := mw.Chain(base,
//		mw.Redact(),
//		mw.Cache(mw.CacheOptions{TTL: time.Hour}),
//		mw.Budget(25),
//		mw.Retry(),
//		mw.Hedge(8*time.Second),
//		mw.RateLimit(60, time.Minute),
//		mw.CircuitBreaker(),
//	)
//	client := schemaflux.Client{Provider: chain}
//
// Every wrapper, in the same words:
//
//   - It takes and returns a schemaflux.Provider. The settings are a struct, and
//     its zero value is the documented default: mw.Retry() retries as the provider
//     says, mw.Cache() keeps answers for ever in memory. A wrapper that takes
//     settings takes them as options values, any number, merged left to right, and
//     a later value can add to what an earlier one asked for and cannot take it
//     away: a number or a duration is the last that is not zero, a map or a list is
//     the union (a later rule winning for a name both have), a boolean is on if any
//     value turns it on, and two functions both run, the earlier first. What
//     loosens a protection or a guard (RedactOptions.NoBuiltins,
//     BudgetOptions.AllowUnpriced, FallbackOptions.AllowSchemaDegradation) is
//     honoured in the first value only, and in a later one is a configuration error
//     that says so. There is no constructor for a setting and no chained setter.
//     Where "none" is a meaningful count it is a negative number, and the field
//     says so (RetryOptions.Retries).
//   - A setting that cannot be used, a negative duration, a limit below one, or a
//     rule with no expression, is not a panic and not ignored: every request
//     through the wrapper fails with an error of kind schemaflux.ErrConfiguration,
//     before the provider is asked, and the error names the wrapper ("mw.retry").
//     The numbers a wrapper takes by position are held to the same rule as the
//     fields of its options: RateLimit's n and per, Bulkhead's limit and Hedge's
//     after. Where zero is a default it is said (a per of zero is a second, an
//     after of zero hedges nothing), and below that it is an error.
//   - Name is the wrapped provider's, and Unwrap returns it. The library finds
//     what the provider underneath can do through Unwrap: that it can be warmed
//     (provider.Warmer), the model it means by a tier, the limits of a decision
//     provider (provider.Decider), and its retry settings (provider.Retrier,
//     answered by the wrapper itself: the provider's own, and "never" when the
//     chain holds a Retry, so that the repeats are that middleware's alone). A
//     middleware of a program's own written with Wrap keeps all of it; one written
//     with Func is a provider at the bottom of a chain and hides it.
//   - Streaming does not pass through. The wrappers act on a whole request and a
//     whole answer: a retry cannot repeat half a stream, a cache has nothing to
//     keep until it ends, a budget has nothing to count before the end. No wrapper
//     forwards a stream, and a Stream call on a wrapped provider fails with an
//     error of kind schemaflux.ErrUnsupported rather than being handed on to the
//     provider underneath, past every layer. The error names the layers that would
//     have been skipped: to stream, give the client the provider without them,
//     which means none of them applies to the stream.
//   - A provider's error keeps its Kind and its fields through every wrapper, and
//     is not changed. What a wrapper refuses itself is an error of the library's,
//     with the wrapper's name as its Op: a full bulkhead is ErrBusy ("mw.bulkhead"),
//     an open circuit is ErrUnavailable ("mw.breaker"), a spent budget is
//     ErrBudgetExceeded ("mw.budget"), and a rate limit that rejects is
//     ErrRateLimited with a RetryAfter ("mw.ratelimit"). Their messages carry
//     nothing of the request, and nothing prints a provider's own words or a key.
//   - A call that is over is not started: a nil context is an error of kind
//     schemaflux.ErrInvalidRequest and one that has ended fails with its own error,
//     before any layer or the provider is asked. A provider that panics is turned
//     into an error of kind schemaflux.ErrInternal where the library calls it, and
//     every layer then releases what it holds (a slot of a bulkhead, a reservation
//     of a budget, the claim on a key of a cache, the probe of a breaker) as it
//     does for any failure. Hedge, whose copies run on goroutines of their own, does
//     the same for them.
//   - A typed decision (Request.Decision), and the typed answers, the probabilities
//     and the escalation that come back (Response.Answers, Response.Escalation),
//     pass through every wrapper as they are. Redact is the one that reads a
//     decision, and it gives a label back as the caller's own option.
//   - Every wrapper is safe for concurrent use. State that belongs to a wrapper
//     (the bucket of a rate limit, the circuits, the answers of a cache) belongs to
//     the Middleware value and is shared by the providers it is applied to; the
//     ledger of a Budget belongs to each provider it is applied to.
//
// # The order
//
// Chain lists the outermost first: Chain(p, A, B) is A around B around p. The
// order that works is this, outermost to innermost, and a program uses the
// wrappers it needs in it:
//
//	Redact, Cache, Budget, Fallback or Decisions, Retry, Hedge, RateLimit, Bulkhead, CircuitBreaker, the provider
//
// The reasons, pair by pair. Each is held by a test of this package that builds
// the order and its reverse and shows the difference.
//
// Redact is outermost, so that nothing unredacted reaches anything. A wrapper
// outside it sees the original text, and so would a cache key, a second route of
// a Fallback, or the decision provider of a Decisions: a Redact around the primary
// route alone leaves the alternate sent the address. One consequence is to know
// of: the cache keys on what the provider is sent, so two requests that differ
// only in what was redacted are one request to it and share one answer, which is
// the answer it would have given either.
//
// Cache is outside Retry, Hedge, the limiters and the Budget, so that a hit
// enters none of them: it takes no retry, no slot of a bulkhead, no token of a
// rate limit and no budget, and a spent budget still serves what is already kept.
// A Hedge outside a Cache hedges nothing: the second copy joins the first as an
// identical request in flight, and the provider is asked once. Only a complete
// answer is kept, so a request that took three attempts is kept once and its
// repeats cost nothing.
//
// Budget is outside the router (Fallback or Decisions) and the retry. Around a
// Fallback it is one ceiling over every route and counts the route that answered;
// inside the primary route alone it never sees the alternates. Around a Hedge, or
// inside it, it counts the answer that was used and not the copy that was
// canceled, which the provider bills and no ledger sees: leave headroom. Around a
// Decisions pair it counts the generating provider's answer. A request whose
// decision provider was asked first and passed over is paid twice, and the first
// call is not in the answer's usage: to count it, put the ledger around the
// decision provider inside the pair, Decisions(generator, mw.Budget(limit)(decider),
// options). A budget reserves what it estimates for a request before it is sent,
// so it refuses before it spends; Budget says by how much the spend can still pass
// the limit.
//
// Fallback and Decisions are outside the retry and the breaker, which are each
// route's own: Chain the primary with its Retry and CircuitBreaker, give each
// alternate its own, and put the Fallback around the primary's. A route that is
// down is then left for the next at once: an open circuit is a refusal that
// costs nothing, a Retry between the Fallback and the breaker returns it without
// repeating it, and the Fallback fails over on it, where a breaker outside the
// Fallback sees only the alternate's successes and never opens. A Retry that
// repeated the refusal would hold every request back for a backoff while the
// primary is down.
//
// Retry is outside Hedge. A hedge answers for slowness within an attempt and a
// retry for failure between them, and Retry outside it hedges each attempt, so
// a request can send up to twice as many requests as it has attempts.
//
// RateLimit, Bulkhead and CircuitBreaker are innermost, next to the provider,
// with RateLimit outside Bulkhead and CircuitBreaker innermost. A limit that has
// to count what is sent goes inside Retry and Hedge. Inside a Retry each attempt
// takes a token, and a repeat waits for the bucket to refill, where a limiter
// outside it counts the call once and lets its repeats go past. Inside a Hedge
// each copy is counted, so a Bulkhead of 1 refuses the second copy as busy,
// where outside it the request is counted once and both copies are sent. A
// RateLimit goes outside the Bulkhead so that a request waiting for a token holds
// no slot. The CircuitBreaker is innermost because it counts a rate limit as a
// failure of the provider: a RateLimit that rejects, inside it, opens the circuit
// on a healthy provider, and one outside it never reaches it.
//
// # Where each is described
//
// Retry repeats a failed request after the wait it states or a jittered backoff.
// RateLimit holds requests to n per interval, waiting or rejecting. Bulkhead
// bounds how many are in flight. CircuitBreaker stops sending to a provider that
// is failing and tries it again after a while. Budget refuses a request that would
// take the spend past a ceiling. Cache answers an identical request from memory.
// Fallback sends a request to the next route when one fails. Hedge sends a second
// copy of a slow request. Redact removes what must not leave the process.
// Decisions pairs a decision provider with a generating one. Func writes a
// provider and Wrap a middleware of a program's own, and Chain composes them.
//
// A wrapper is a thin layer over the implementation in the package mw of the
// library, which holds the retrying, the caching and the rest: this package
// converts the provider, the settings and the errors, and does not repeat what
// the implementation does.
package mw
