package mw

import (
	"context"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/llm"

	impl "github.com/monstercameron/schemaflux/v2/internal/runtime/mw"
)

// CacheOptions are the settings of Cache. The zero value is the default: an
// in-memory store of at most 4096 answers, whose entries do not expire, and no
// callback.
type CacheOptions struct {
	// TTL is how long a stored answer is served before it is a miss again.
	// Zero is for ever, which suits a batch job's duplicate requests and not a
	// long-running server whose upstream data changes. It must not be
	// negative.
	TTL time.Duration

	// Store keeps the answers. Nil is a store in this process's memory: it
	// holds at most 4096 answers, drops the oldest first when it is full, and is
	// gone when the process ends. A store of your own (a shared one, one that
	// survives a restart) takes its place; Get and Set are called from many
	// goroutines at once, with the context of the request they are for, which a
	// store behind a network gives up on when it ends. A store is handed the keys
	// the cache makes, which are digests of the request and never its text, and
	// the answers.
	Store CacheStore

	// OnEvent is called with an event for every request the cache handles, a
	// hit or a miss, once the request is over. It is how a program counts hits
	// and tells a free answer from one that was paid for. It runs on the
	// request's goroutine, from many at once, and must not block. A panic in it
	// is recovered and does not change the answer. Nil emits no events.
	OnEvent func(CacheEvent)
}

// A CacheStore keeps the answers of a Cache. The cache's own store is a map in
// memory; a program supplies another to share answers between processes or to
// keep them across restarts.
//
// A key is an opaque digest of everything that makes one request different from
// another. An answer is a whole Response; a decision provider's typed Answers
// are in it, and a store that keeps only Content loses them. Get returns an answer
// the cache may hand to a caller: it does not share memory with the store, so a
// store that holds the Response's slices returns copies.
//
// Both methods are given the context of the request they are for, so that a
// store that waits on a network can stop when the request's deadline passes: a
// Get that gives up is a miss, and the request then ends with the context's
// error and is not sent. The cache reads the store once for a miss and writes it
// once, and trusts what it reads no further than it trusts what it writes: an
// answer that is not complete (cut off, withheld, empty, or typed answers that do
// not answer the decision) is a miss. A store that panics is not allowed to take
// a request down with it: a Get that panics is a miss, and a Set that panics
// loses the entry and leaves the answer, which was paid for, with the request that
// paid for it.
type CacheStore interface {
	// Get returns the answer kept under key, and false when there is none or
	// it has expired.
	Get(ctx context.Context, key string) (schemaflux.Response, bool)

	// Set keeps resp under key for ttl. A ttl of zero or less means for
	// ever.
	Set(ctx context.Context, key string, resp schemaflux.Response, ttl time.Duration)
}

// A CacheEvent says what Cache did with one request.
type CacheEvent struct {
	// Hit is true when the answer came from the store, directly or by waiting
	// for an identical request already in flight that succeeded, and false when
	// the provider was asked.
	Hit bool

	// Coalesced is true when this request did not reach the store or the
	// provider itself but waited for an identical request that was in flight and
	// shared its outcome: a Hit if that request succeeded, and not if it failed.
	Coalesced bool

	// Key is the digest this request resolved to. It is never the text of the
	// request, and it is not safe to log or send for a request whose text is
	// short or guessable: it is an unsalted SHA-256 of everything the request
	// holds, so a program that logs it lets anyone who can guess the request
	// confirm the guess.
	Key string

	// Tenant and DataPolicy are the partition the request was cached in, as
	// WithCachePartition set them. Empty for a request with none.
	Tenant, DataPolicy string

	// Bypassed is true when the request went round the cache, nothing read for
	// it and nothing of its answer stored, because the client's data policy does
	// not allow its answers to be kept. Hit and Coalesced are false then.
	Bypassed bool
}

// A CachePartition keeps the answers of one tenant and one data policy apart
// from the others'. A request in no partition is in the zero partition, which is
// a partition of its own and is not merged into one that was named.
type CachePartition struct {
	// Tenant names whose request this is: a customer, a user, a workspace.
	Tenant string

	// DataPolicy names the policy the request was made under, when two
	// policies must not share answers.
	DataPolicy string
}

// WithCachePartition returns a context that carries p, and the cache puts the
// requests made with it in that partition: an answer stored for one partition
// is never served to another. It has no effect on a chain that has no Cache.
func WithCachePartition(ctx context.Context, p CachePartition) context.Context {
	return impl.WithCachePartition(ctx, impl.CachePartition{Tenant: p.Tenant, DataPolicy: p.DataPolicy})
}

// Cache answers a request from memory when an identical one was answered
// before, so that an exact repeat costs nothing. It is an exact-match cache and
// not a similarity search: a request that is merely like an earlier one is a
// miss.
//
// # Opt in
//
// Nothing puts a Cache in a chain for a program. A cache that is on by default
// serves last year's answer to a question the caller thinks is fresh.
//
// # What makes two requests the same
//
// Two requests are the same when everything that could change the answer is: the
// provider's name, the model, the system prompt and the user message, as they are
// sent (the steering included), the temperature, the output ceiling, the format,
// the schema and its name, the reasoning effort, whether web search is on, the
// prompt-cache key, the partition, and the client's data policy where it declares
// one. For a typed decision, the decision: its state and guidance, and each
// question's kind, text, scale, options and criteria. The same prompts asked as
// a different decision, or as none, are different requests and are never served
// each other's answers. Each part is hashed on its own before they are joined,
// so that a value holding a separator cannot move text from one part to the next.
//
// # What is kept
//
// Only a complete answer is kept. One cut off at the output ceiling, withheld by
// a content filter, or empty is returned to its caller and not stored, since the
// cause can be passing and a stored copy would hand it to every later request. A
// decision provider's typed answers are kept, and are checked against their
// questions first: an answer the call path would refuse is not stored. A hit
// shares nothing with the stored answer; a caller that edits what it was given
// changes nobody else's.
//
// # What a hit reports
//
// A hit has Cached set, is not marked Hedged whatever the request that filled the
// cache did, and reports zero usage and no timing: it is not a free
// call dressed as a measured one, and it does not repeat the original's tokens,
// which would double-count spend. OnEvent is how a program tells hits from
// misses in its own accounting.
//
// # Identical requests at once
//
// Identical requests that arrive while one is in flight wait for it and share its
// outcome. A waiting request never inherits the first one's cancellation: when
// the first request's context ended and that is why it failed, those whose
// contexts are live ask again, and one of them becomes the new first. If the
// provider panics, the panic continues up the first request's stack and the
// others receive an error of kind schemaflux.ErrInternal.
//
// # A client that may not be cached
//
// A client that declared a data policy (Client.Policy) is cached only when the
// policy sets AllowCaching: otherwise it goes round the cache, and nothing is read
// for it and nothing of its answers is stored. A client that declared no policy is
// cached.
//
// The store belongs to the Middleware this returns, not to a provider: applied
// to two providers it gives both the same store. Keys include the provider's
// name, so providers with different names never read each other's answers, and
// two with the same name do. Where Cache goes in a chain matters, and the
// package comment says. Cache does not forward streaming: a wrapped provider
// cannot stream.
func Cache(opts ...CacheOptions) Middleware {
	o := mergeCacheOptions(opts)

	var problem string
	if o.TTL < 0 {
		problem = "TTL must not be negative"
	}

	var settings []impl.CacheOption
	if o.TTL > 0 {
		settings = append(settings, impl.WithCacheTTL(o.TTL))
	}
	if o.Store != nil {
		settings = append(settings, impl.WithCacheStore(storeAdapter{o.Store}))
	}
	if o.OnEvent != nil {
		onEvent := o.OnEvent
		settings = append(settings, impl.WithCacheStats(func(e impl.CacheEvent) {
			onEvent(CacheEvent{
				Hit:        e.Hit,
				Coalesced:  e.Coalesced,
				Key:        e.Key,
				Tenant:     e.Tenant,
				DataPolicy: e.DataPolicy,
				Bypassed:   e.Bypassed,
			})
		}))
	}
	build := impl.Cache(settings...)

	return func(next schemaflux.Provider) schemaflux.Provider {
		return wrap("mw.cache", next, problem, build)
	}
}

// mergeCacheOptions merges option values left to right, a later TTL that is not
// zero and a later Store that is not nil winning. The callbacks are all kept and
// called in order: a later value that watches the cache does not take the
// watching an earlier one asked for away.
func mergeCacheOptions(opts []CacheOptions) CacheOptions {
	var merged CacheOptions
	for _, o := range opts {
		if o.TTL != 0 {
			merged.TTL = o.TTL
		}
		if o.Store != nil {
			merged.Store = o.Store
		}
		if o.OnEvent != nil {
			earlier, later := merged.OnEvent, o.OnEvent
			if earlier == nil {
				merged.OnEvent = later
				continue
			}
			merged.OnEvent = func(e CacheEvent) {
				earlier(e)
				later(e)
			}
		}
	}
	return merged
}

// storeAdapter is a CacheStore as the cache of the wrapped package takes one: one
// that is given the context of the request, so that the store of the program is.
type storeAdapter struct{ store CacheStore }

// Get is GetContext with no context. The cache calls GetContext and never this.
func (s storeAdapter) Get(key string) (llm.CompletionResponse, bool) {
	return s.GetContext(context.Background(), key)
}

// Set is SetContext with no context. The cache calls SetContext and never this.
func (s storeAdapter) Set(key string, resp llm.CompletionResponse, ttl time.Duration) {
	s.SetContext(context.Background(), key, resp, ttl)
}

// GetContext reads the store and converts the answer. An answer that does not
// convert is a miss.
func (s storeAdapter) GetContext(ctx context.Context, key string) (llm.CompletionResponse, bool) {
	resp, ok := s.store.Get(ctx, key)
	if !ok {
		return llm.CompletionResponse{}, false
	}
	return bind.ResponseToInternal(resp), true
}

// SetContext converts the answer and writes it. An answer that does not convert
// is not kept.
func (s storeAdapter) SetContext(ctx context.Context, key string, resp llm.CompletionResponse, ttl time.Duration) {
	converted, err := bind.ResponseFromInternal[schemaflux.Response](resp)
	if err != nil {
		return
	}
	s.store.Set(ctx, key, converted, ttl)
}
