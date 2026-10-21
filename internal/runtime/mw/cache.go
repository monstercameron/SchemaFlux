package mw

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Cache is an exact-result cache: a Complete call whose full semantic
// identity matches a previous one is answered from memory instead of the
// provider. It exists so an exact-duplicate call costs zero, not so a
// *similar* call is answered approximately -- that is a different feature,
// deliberately not built here. Gap-07, MW-004.
//
// # Opt-in
//
// Nothing in this package or in schemaflux.Client wires Cache into a chain
// automatically. A caller adds it the same way as RateLimit or Retry:
//
//	client.WithProviderInstance(mw.Chain(base, mw.Cache()))
//
// A cache silently switched on by default is a cache silently serving a
// year-old answer to a question the caller thinks is fresh; opt-in is the
// whole safety property.
//
// # The key: TRU-10's axes
//
// TRU-10 revised MW-004 because "model, tier, mode, prompt bytes, schema" is
// not the complete identity of a call -- two calls that agree on all five can
// still disagree about which operation asked, which contract version it
// wanted, or whose data it was allowed to touch, and a cache that ignores
// that difference answers a question nobody asked. cacheKeyFor below carries,
// in order:
//
//  1. Operation, prompt version, and schema hash: req.PromptCacheKey. This is
//     NOT re-derived here. It is P-009's promptCacheKeyFor
//     (internal/ops/llm_helper.go), which already folds ops.SchemaCacheKey's
//     operation-name/operation-version/schema-hash identity together with the
//     resolved model and response format for operations that set
//     OpOptions.CacheIdentity. TRU-10 names ops.SchemaCacheKey as the thing
//     "MW-004 and P-009 both need... and neither should invent its own"; this
//     reuses it by reusing its output rather than importing internal/ops and
//     recomputing it, which would risk the two opinions drifting apart
//     exactly as the comment warns against. It is empty for operations that
//     do not set CacheIdentity -- axis 3 (the input digest) still keeps those
//     calls' keys apart from each other, just without the operation-name and
//     schema-version axis named explicitly.
//  2. Schema hash, independently: cacheDigest of req.SchemaName plus the
//     canonical JSON of req.JSONSchema. This duplicates part of what
//     PromptCacheKey already carries when CacheIdentity was set, on purpose:
//     req.JSONSchema is present on every call that has a schema at all, so
//     this axis holds even when CacheIdentity is unset, rather than a result
//     cache's schema-sensitivity depending on an upstream opt-in it doesn't
//     control.
//  3. Input digest: cacheDigest of req.SystemPrompt and req.UserPrompt AS
//     SENT -- the full prompt, steering included. This is where this key
//     deliberately diverges from promptCacheKeyFor rather than reusing it:
//     promptCacheKeyFor hashes only the STABLE prefix and leaves steering out
//     on purpose (CA-002), because a provider's prefix-routing hint tolerates
//     a miss -- the worst case is a slower first token. An exact-result
//     cache cannot tolerate that miss: steering changes what was asked, and
//     serving a steered call's answer to an unsteered one (or vice versa) is
//     exactly "a cache hit answers a question nobody asked." So this axis
//     hashes the request's actual SystemPrompt and UserPrompt fields, not the
//     stable-prefix precursor promptCacheKeyFor uses.
//  4. Provider: the wrapped Handler's Name().
//  5. Resolved model: req.Model. Called out separately from axis 1 because
//     PromptCacheKey is empty for CacheIdentity-less calls and this axis is
//     not.
//  6. Temperature: req.Temperature, formatted for byte-stable repetition.
//  7. Seed: cacheSeedPlaceholder (see below) -- absent from this codebase.
//  8. Max tokens: req.MaxTokens. Not named in TRU-10's list, included anyway:
//     two calls differing only in max_tokens can produce a genuinely
//     different (truncated versus complete) answer, and silently serving one
//     for a caller who asked for the other is the same failure mode TRU-10
//     is about, even though the axis list did not spell it out.
//  9. Response format: req.ResponseFormat.
//  10. Required contract level: cacheContractLevelPlaceholder -- see below.
//  11. Data-policy partition: tenant and data-policy, read from ctx via
//     WithCachePartition. See "Partitioning" below.
//  12. Decoder version: cacheDecoderVersionPlaceholder -- see below.
//  13. Reasoning effort: req.ReasoningEffort, and whether it is an operation's
//     preference rather than the caller's (req.ReasoningEffortPreferred). Not
//     in TRU-10's list. A preference is dropped on a model it was not
//     measured on, so the two are different requests there.
//  14. Web search: req.WebSearch.
//  15. Tools: req.Tools in the order offered, and req.ToolChoice.
//  16. Earlier messages: req.Messages, with the calls the model made in each of
//     them: each call's id, name and arguments, which are what a conversation
//     that differs only in what the model asked for earlier differs in. A
//     history with no call in it is keyed as it was before a message could carry
//     one.
//  17. The data policy the client declared, when it declared one. It is added
//     to the finished key (cacheDataPolicyKey) because it is read from the
//     context, not the request: two clients that both allow result caching but
//     differ in the regions, providers or retention they permit do not share an
//     entry.
//
// Each axis is hashed on its own before the axes are joined, so that a value
// holding the separator cannot move text from one axis into the next and make
// two different requests produce the same key.
//
// Three of TRU-10's axes are placeholders, not real values, because nothing
// in this codebase carries them yet:
//
//   - Seed: llm.CompletionRequest has no Seed field. No provider path here
//     requests deterministic sampling by seed. Adding it is a Seed field on
//     CompletionRequest threaded from OpOptions, the same way Temperature is
//     threaded today.
//   - Required contract level: types.ContractLevel exists
//     (internal/types/result.go) and is decided in internal/ops, well above
//     the Handler seam this middleware wraps -- llm.CompletionRequest carries
//     no contract-level field and nothing sets a context value for one.
//     Reaching it needs either a field on CompletionRequest or a context key
//     ops.CallLLM populates before calling into the chain; both are
//     internal/ changes out of scope for this file.
//   - Decoder version: internal/ops/strictdecode.go is a single, unversioned
//     implementation. There is nothing to name yet. Adding one means giving
//     strictdecode.go a version constant and threading it the way SchemaID is
//     threaded today.
//
// Each placeholder is a fixed, named string rather than an empty one, so a
// key missing that axis reads as "this axis does not exist yet," not as "this
// axis was silently dropped" -- an empty string and a real, deliberately-empty
// value hash identically, which is exactly the ambiguity a placeholder
// exists to remove.
//
// # A client that may not be cached
//
// A client that declared a data policy (Client.WithDataPolicy) is cached only
// when the policy sets AllowResultCaching. Otherwise its calls go round the
// cache: nothing is read for them and nothing of theirs is stored. A caller
// with no client, or a client that declared no policy, is cached as before.
//
// # What is stored
//
// Only a complete answer is stored. A response that llm.ClassifyCompletion
// does not classify as usable -- cut off at the token limit, withheld by a
// content filter, or empty with no tool call in it -- is returned to its caller
// and not kept: the failure that caused it can be transient, and a stored copy
// would hand it to every later identical request. A hit shares nothing with the
// stored value: the tool calls and their arguments are copied on the way in and
// on the way out, so a caller that edits a response in place changes nobody
// else's.
//
// The default store holds at most cacheDefaultMaxEntries responses and drops
// the oldest first when it is full. Entries past their TTL are dropped by a
// sweep that runs inside Set, every cacheSweepEvery sets and whenever the store
// is full, so memory is returned without a read of the entry and without a
// background goroutine.
//
// # Concurrent identical calls
//
// Identical calls that arrive while one is in flight wait for it and share its
// outcome. A follower never inherits the leader's own cancellation: when the
// leader's context ended and that is why the call failed, followers whose
// contexts are still live take the call up again, one of them as the new
// leader. If the wrapped provider panics, the leader's panic continues up its
// stack, the key is released, and the followers receive an error.
//
// # Where this stands in a chain
//
// The default store, and with it the cache, belongs to the Middleware value
// Cache returned, not to the Handler it wraps: applying that Middleware to two
// providers gives both the same store. Entries are keyed by provider name
// among other axes, so providers with different names do not read each other's
// answers, but two providers with the same name do.
//
// # Partitioning
//
// TRU-10 requires the cache be partitioned by tenant and data policy. Nothing
// upstream of mw.Cache passes either through llm.CompletionRequest today, so
// this middleware implements the partition as a context seam rather than
// inventing fields on a request struct it does not own: a caller wraps ctx
// with WithCachePartition(ctx, CachePartition{Tenant: ..., DataPolicy: ...})
// before calling into the chain, and Cache reads it back out. An unpartitioned
// caller (the common case today, since nothing yet supplies these values)
// gets the zero CachePartition, which is its own partition -- calls with no
// stated tenant or data policy are grouped together, not merged into calls
// that stated one, and not scattered across a policy nobody declared.
//
// # A hit is a hit, never a free call
//
// A hit must not report a cost of $0.00 the way a call that never happened
// would -- that indistinguishability is exactly what TRU-10 forbids. Two
// things this file does about it, and one gap it cannot close from here:
//
//  1. The response's TokenUsage is zeroed on a hit. This is not zero as in
//     "unknown" -- it is the true count of tokens THIS call spent talking to
//     a provider, which is genuinely zero. Replaying the ORIGINAL call's
//     usage instead would double-count spend across every hit, which is the
//     "invented number" AGENTS.md forbids in the other direction.
//  2. Every Complete call -- hit, miss, or a follower coalesced onto an
//     in-flight miss -- emits a CacheEvent through WithCacheStats, carrying
//     Hit, Coalesced, Key, and the partition. This is the actual "appears in
//     provenance" mechanism: a caller wires the callback into whatever
//     provenance or cost-accounting sink it already has, and that sink
//     records the hit as a hit with its own line item, distinct from a
//     genuinely free call.
//
// What this file cannot do: stamp the hit onto the object
// internal/ops/llm_helper.go turns into types.Meta. That function reads
// resp.Model, resp.Provider, and resp.Usage into types.ResultMetadata and
// already carries a Custom map for exactly this kind of extra fact (schema_id
// is threaded through it today) -- but nothing populates a "cache_hit" entry,
// and llm.CompletionResponse has no field this middleware could set that
// llm_helper.go already reads for that purpose. The one-line fix, out of
// scope here because it is under internal/: add `CacheHit bool` to
// llm.CompletionResponse, set it true in cacheHitResponse below, and add one
// line in llm_helper.go's Custom map literal (`"cache_hit": resp.CacheHit`)
// plus a matching read in types.MetaFrom next to the existing schema_id read.
// Until that lands, a caller who does not wire WithCacheStats sees a hit's
// cost as a genuinely-zero-tokens call in Meta -- correct as far as it goes,
// but not distinguishable there from an operation that happened to need zero
// tokens. WithCacheStats is the only channel this file has for closing that
// gap without touching internal/.
func Cache(opts ...CacheOption) Middleware {
	cfg := cacheConfig{now: time.Now}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if cfg.store == nil {
		cfg.store = cacheNewMemoryStore(cfg.now)
	}

	return func(next Handler) Handler {
		return &cacheHandler{
			next:   next,
			store:  cfg.store,
			ttl:    cfg.ttl,
			stats:  cfg.stats,
			onWait: cfg.onWait,
		}
	}
}

// CacheOption configures Cache beyond its always-on default: an in-memory
// store of at most cacheDefaultMaxEntries entries that never expires one on
// its own unless WithCacheTTL is set.
type CacheOption func(*cacheConfig)

type cacheConfig struct {
	ttl   time.Duration
	store CacheStore
	now   func() time.Time
	stats func(CacheEvent)

	// onWait is told each time a call starts waiting on another call's
	// in-flight request. It exists so a test can release the leader only once
	// every follower is actually waiting.
	onWait func()
}

// WithCacheTTL bounds how long a stored entry is served before it is treated
// as a miss. Zero (the default) means entries never expire on their own --
// right for a process-lifetime cache of a batch job's duplicate calls, wrong
// for a long-running server whose upstream data can change out from under a
// stale answer, which is what this option is for.
func WithCacheTTL(d time.Duration) CacheOption {
	return func(c *cacheConfig) {
		if d > 0 {
			c.ttl = d
		}
	}
}

// WithCacheStore swaps the default in-process map for another CacheStore
// implementation -- a distributed store shared across replicas, for
// instance. The default is process-local and gone on restart, which is
// correct for "exact-duplicate calls in this run cost zero" and wrong for
// anything meant to survive one.
func WithCacheStore(s CacheStore) CacheOption {
	return func(c *cacheConfig) {
		if s != nil {
			c.store = s
		}
	}
}

// WithCacheStats registers a callback fired on every Complete call this
// middleware handles, hit or miss. It is the provenance seam described in
// Cache's doc comment: the callback is the only way outside this file to
// learn a response came from the cache rather than the provider, so a caller
// that cares about that distinction in its own cost accounting or logs wires
// it here.
//
// The callback runs on the goroutine of the call, before the call returns, and
// calls run at the same time, so it must be safe for that and must not block. A
// panic in it is recovered and logged without its value, and does not change
// the call's answer.
func WithCacheStats(fn func(CacheEvent)) CacheOption {
	return func(c *cacheConfig) { c.stats = fn }
}

// CachePartition is the tenant/data-policy seam TRU-10 requires. A caller
// that has this information sets it on ctx with WithCachePartition before
// calling into the chain; a caller that does not is grouped into the zero
// partition along with every other caller that also said nothing, which is a
// real partition (not a wildcard that could match a stated one) rather than a
// silent merge of "we did not say" into "we said public."
type CachePartition struct {
	Tenant     string
	DataPolicy string
}

// cachePartitionContextKey is unexported so nothing outside this file can
// forge a context value that collides with WithCachePartition's.
type cachePartitionContextKey struct{}

// WithCachePartition attaches a tenant/data-policy partition to ctx for
// mw.Cache to read. It has no effect unless the chain contains mw.Cache.
func WithCachePartition(ctx context.Context, partition CachePartition) context.Context {
	return context.WithValue(ctx, cachePartitionContextKey{}, partition)
}

// cachePartitionFromContext reads back what WithCachePartition attached, or
// the zero CachePartition when nothing did.
func cachePartitionFromContext(ctx context.Context) CachePartition {
	if ctx == nil {
		return CachePartition{}
	}
	if p, ok := ctx.Value(cachePartitionContextKey{}).(CachePartition); ok {
		return p
	}
	return CachePartition{}
}

// CacheEvent is what WithCacheStats' callback receives for every Complete
// call mw.Cache handles.
type CacheEvent struct {
	// Hit is true when the response came from the store (directly, or via a
	// follower coalesced onto an in-flight leader that succeeded) rather than
	// from the wrapped provider.
	Hit bool

	// Coalesced is true when this call did not itself reach the store or the
	// provider, but waited for another goroutine's identical in-flight call
	// and shared its outcome. A coalesced call with Hit true shared a
	// success; a coalesced call with Hit false shared a failure.
	Coalesced bool

	// Key is the cache key this call resolved to, already a digest -- never
	// the raw prompt bytes it was derived from. AGENTS.md's "never log the
	// caller's payload" applies to this event exactly as it does to an error
	// string.
	Key string

	Tenant     string
	DataPolicy string

	// Bypassed is true when the call went round the cache: nothing was read
	// for it and nothing of its answer was stored, because the client's data
	// policy does not allow its answers to be kept. Hit and Coalesced are
	// false on such an event.
	Bypassed bool
}

// CacheStore is what mw.Cache reads and writes. The default (cacheMemoryStore)
// is process-local; CacheStore exists as an interface so WithCacheStore can
// swap in something else without this file needing to know what.
//
// Get and Set are called from many goroutines at once. A store that panics in
// either is not allowed to take a call down with it: a Get that panics is a
// miss, and a Set that panics leaves the answer to the call that paid for it,
// which is returned as it would have been. Neither can be cancelled, since they
// take no context; a store that waits on a network implements ContextCacheStore.
type CacheStore interface {
	Get(key string) (llm.CompletionResponse, bool)
	Set(key string, resp llm.CompletionResponse, ttl time.Duration)
}

// ContextCacheStore is a CacheStore that is handed the context of the call it is
// read or written for, so that a store behind a network can give up when the
// call's deadline passes. When the store given to WithCacheStore is one, the
// cache calls GetContext and SetContext and never Get and Set.
type ContextCacheStore interface {
	CacheStore

	// GetContext is Get for the call whose context is ctx.
	GetContext(ctx context.Context, key string) (llm.CompletionResponse, bool)

	// SetContext is Set for the call whose context is ctx.
	SetContext(ctx context.Context, key string, resp llm.CompletionResponse, ttl time.Duration)
}

const (
	// cacheDefaultMaxEntries is how many responses the default store keeps. A
	// response is a few kilobytes of text, so the bound holds the default store
	// to the order of tens of megabytes however many distinct requests are made.
	// A caller who needs more, or a different eviction rule, supplies a store
	// with WithCacheStore.
	cacheDefaultMaxEntries = 4096

	// cacheSweepEvery is how many sets pass between sweeps of expired entries.
	// A sweep visits every entry, so it is spread over many sets; a store that
	// has reached its bound sweeps at once, to make room with expired entries
	// before it evicts a live one.
	cacheSweepEvery = 128
)

// cacheEntry is one stored response plus its expiry.
type cacheEntry struct {
	resp      llm.CompletionResponse
	expiresAt time.Time // zero means "never expires"
	order     uint64    // the set that wrote it; the lowest is the oldest
}

// cacheMemoryStore is the default CacheStore: an in-process map guarded by a
// mutex, bounded by maxEntries. Simple on purpose -- a distributed cache is
// WithCacheStore's job, not this type's.
type cacheMemoryStore struct {
	mu         sync.Mutex
	entries    map[string]cacheEntry
	now        func() time.Time
	maxEntries int
	sets       uint64
}

func cacheNewMemoryStore(now func() time.Time) *cacheMemoryStore {
	if now == nil {
		now = time.Now
	}
	return &cacheMemoryStore{entries: make(map[string]cacheEntry), now: now, maxEntries: cacheDefaultMaxEntries}
}

func (s *cacheMemoryStore) Get(key string) (llm.CompletionResponse, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[key]
	if !ok {
		return llm.CompletionResponse{}, false
	}
	if !entry.expiresAt.IsZero() && !s.now().Before(entry.expiresAt) {
		delete(s.entries, key)
		return llm.CompletionResponse{}, false
	}
	return entry.resp, true
}

func (s *cacheMemoryStore) Set(key string, resp llm.CompletionResponse, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.sets++
	if s.sets%cacheSweepEvery == 0 || len(s.entries) >= s.maxEntries {
		s.removeExpiredLocked()
	}
	if _, replacing := s.entries[key]; !replacing {
		for len(s.entries) >= s.maxEntries && len(s.entries) > 0 {
			s.evictOldestLocked()
		}
	}

	var expiresAt time.Time
	if ttl > 0 {
		expiresAt = s.now().Add(ttl)
	}
	s.entries[key] = cacheEntry{resp: resp, expiresAt: expiresAt, order: s.sets}
}

// removeExpiredLocked drops every entry past its expiry. The caller holds mu.
func (s *cacheMemoryStore) removeExpiredLocked() {
	now := s.now()
	for key, entry := range s.entries {
		if !entry.expiresAt.IsZero() && !now.Before(entry.expiresAt) {
			delete(s.entries, key)
		}
	}
}

// evictOldestLocked drops the entry that was written longest ago. The caller
// holds mu.
func (s *cacheMemoryStore) evictOldestLocked() {
	var oldestKey string
	var oldest uint64
	found := false
	for key, entry := range s.entries {
		if !found || entry.order < oldest {
			oldestKey, oldest, found = key, entry.order, true
		}
	}
	if found {
		delete(s.entries, oldestKey)
	}
}

// cacheCall tracks one in-flight miss so concurrent identical callers
// coalesce onto it instead of each paying for their own provider call.
// "Exact-duplicate calls cost zero" has to hold for calls that arrive at the
// same instant, not only for calls that arrive after a previous one already
// finished and got stored -- a store-based cache alone only catches the
// second kind.
//
// resp, err and abandoned are written by the leader before done is closed and
// read by followers after it, and by nobody else.
type cacheCall struct {
	done chan struct{}
	resp llm.CompletionResponse
	err  error

	// abandoned is true when the leader failed while its own context was over:
	// the failure is the leader's cancellation or deadline, not an answer to the
	// request, and a follower with a live context must not be given it.
	abandoned bool
}

// cacheHandler is the Handler mw.Cache wraps a provider in.
type cacheHandler struct {
	next   Handler
	store  CacheStore
	ttl    time.Duration
	stats  func(CacheEvent)
	onWait func()

	inflight sync.Map // key (string) -> *cacheCall
}

func (h *cacheHandler) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	// A client whose data policy does not allow its answers to be kept for reuse
	// goes round the cache altogether. Nothing is read for it, so it is not
	// served an answer somebody else stored, and nothing is stored, so its
	// answer is not served to anybody else.
	partition := cachePartitionFromContext(ctx)
	key := cacheKeyFor(req, h.next.Name(), partition)
	policy, declared := ops.ExecDataPolicy(ctx)
	if declared {
		key = cacheDataPolicyKey(key, policy)
	}
	event := CacheEvent{Key: key, Tenant: partition.Tenant, DataPolicy: partition.DataPolicy}

	if declared && !policy.AllowResultCaching {
		event.Bypassed = true
		h.emit(ctx, event)
		return h.next.Complete(ctx, req)
	}

	for {
		// The store is read once, by whoever holds the claim. Reading it before
		// claiming, and again after, was two round trips to a store behind a
		// network for every miss; claiming first costs a hit nothing the store
		// would have charged it, and identical calls at once share the one read.
		call, leader := h.claim(key)
		if leader {
			return h.lead(ctx, req, key, call, event)
		}

		if h.onWait != nil {
			h.onWait()
		}
		select {
		case <-call.done:
			if call.abandoned {
				// The leader's cancellation is the leader's. A caller that is
				// still live asks again: it finds the answer in the store, or
				// takes the call up as the new leader, or waits on whoever did.
				if err := ctx.Err(); err != nil {
					return llm.CompletionResponse{}, err
				}
				continue
			}
			event.Hit = call.err == nil
			event.Coalesced = true
			h.emit(ctx, event)
			if call.err != nil {
				return llm.CompletionResponse{}, call.err
			}
			return servedFromCache(ctx, call.resp), nil
		case <-ctx.Done():
			return llm.CompletionResponse{}, ctx.Err()
		}
	}
}

// lead makes the call for key on behalf of itself and every follower waiting
// on call, then releases the key.
//
// The key is released by a deferred function so that a panic in the wrapped
// provider cannot leave it claimed: a claim nobody ever completes makes every
// later caller of the key a follower that waits until its own context ends,
// and for a caller with no deadline that is for ever. On a panic the followers
// get an error and the panic carries on up this goroutine's stack.
func (h *cacheHandler) lead(ctx context.Context, req llm.CompletionRequest, key string, call *cacheCall, event CacheEvent) (resp llm.CompletionResponse, err error) {
	returned := false
	defer func() {
		if !returned {
			panicked := types.Newf(types.KindInvariantViolation, "mw.Cache", "the identical request this call was waiting on panicked")
			panicked.Narrow = types.NarrowPanic
			call.err = panicked
		}
		// Released before the followers are woken, so that one that retries finds
		// the key free or held by a newer call, never this finished one.
		h.inflight.Delete(key)
		close(call.done)
	}()

	// Read the store now that this call holds the claim. An earlier leader may
	// have finished, stored its answer and released the key just before: this
	// call finds that answer here, where a read made before claiming would have
	// missed it and made a second paid call. The answer is checked as a stored
	// answer is on the way in: one that is not complete is a miss, since a store
	// of the caller's can hold anything, and the call path above would refuse it.
	if cached, ok := h.get(ctx, key); ok && cacheable(req, cached) {
		call.resp = cached
		returned = true
		event.Hit = true
		h.emit(ctx, event)
		return servedFromCache(ctx, cached), nil
	}
	// A call that was given up on while the store was read is not sent.
	if err := ctx.Err(); err != nil {
		call.err = err
		call.abandoned = true
		returned = true
		h.emit(ctx, event)
		return llm.CompletionResponse{}, err
	}

	resp, err = h.next.Complete(ctx, req)
	returned = true
	if err != nil {
		call.err = err
		call.abandoned = ctx.Err() != nil
	} else {
		// The leader keeps the response it was given; the store and the followers
		// get a copy, so that editing one's response in place does not change
		// the other's.
		shared := cacheCloneResponse(resp)
		call.resp = shared
		if cacheable(req, resp) {
			h.set(ctx, key, shared)
		}
	}
	h.emit(ctx, event)
	return resp, err
}

// get reads the store, through its context form when it has one. A store that
// panics is a miss: the call goes to the provider, which is the answer it would
// have had if the store had held nothing.
func (h *cacheHandler) get(ctx context.Context, key string) (resp llm.CompletionResponse, ok bool) {
	defer func() {
		if thrown := recover(); thrown != nil {
			logPanic(ctx, "A cache store panicked reading", thrown)
			resp, ok = llm.CompletionResponse{}, false
		}
	}()
	if store, ok := h.store.(ContextCacheStore); ok {
		return store.GetContext(ctx, key)
	}
	return h.store.Get(key)
}

// set writes the store, through its context form when it has one. A store that
// panics loses the entry and nothing else: the answer was paid for, and the call
// that paid for it gets it.
func (h *cacheHandler) set(ctx context.Context, key string, resp llm.CompletionResponse) {
	defer func() {
		if thrown := recover(); thrown != nil {
			logPanic(ctx, "A cache store panicked writing", thrown)
		}
	}()
	if store, ok := h.store.(ContextCacheStore); ok {
		store.SetContext(ctx, key, resp, h.ttl)
		return
	}
	h.store.Set(key, resp, h.ttl)
}

// claim registers this call as the leader for key if no other call is
// currently in flight for it, or returns the existing leader's cacheCall for
// this call to wait on.
func (h *cacheHandler) claim(key string) (call *cacheCall, leader bool) {
	actual, loaded := h.inflight.LoadOrStore(key, &cacheCall{done: make(chan struct{})})
	return actual.(*cacheCall), !loaded
}

// emit tells the WithCacheStats callback about a call. The callback is the
// caller's, and it does not get to take the call down with it.
func (h *cacheHandler) emit(ctx context.Context, evt CacheEvent) {
	if h.stats == nil {
		return
	}
	defer func() {
		if thrown := recover(); thrown != nil {
			logPanic(ctx, "A cache stats callback panicked", thrown)
		}
	}()
	h.stats(evt)
}

// cacheable reports whether resp, the answer to req, is a complete answer worth
// keeping. llm.ClassifyCompletion names the responses that are not: one cut off
// at the token limit, one withheld by a content filter, one with no content. A
// response with no content that asks for a tool call is complete, and is kept.
// So is one with no content whose answers are typed (a decision provider writes
// none): provided they are answers to the questions of req's decision. A typed
// answer the call path will refuse is not kept, since the copy would be refused
// again, for as long as the entry lived.
func cacheable(req llm.CompletionRequest, resp llm.CompletionResponse) bool {
	if len(resp.DecisionAnswers) > 0 {
		if _, err := llm.RenderDecisionAnswers(req.Decision, resp.DecisionAnswers); err != nil {
			return false
		}
	}
	switch llm.ClassifyCompletion(resp) {
	case types.KindUnknown:
		return true
	case types.KindMalformedOutput:
		return len(resp.ToolCalls) > 0 || len(resp.DecisionAnswers) > 0
	}
	return false
}

// cacheCloneResponse copies the parts of resp that hold memory a second copy
// would otherwise share: the tool calls and each call's argument bytes, the typed
// answers of a decision, and the record of an escalation.
func cacheCloneResponse(resp llm.CompletionResponse) llm.CompletionResponse {
	if resp.ToolCalls != nil {
		calls := make([]llm.ToolCall, len(resp.ToolCalls))
		for i, call := range resp.ToolCalls {
			if call.Arguments != nil {
				call.Arguments = append(json.RawMessage{}, call.Arguments...)
			}
			calls[i] = call
		}
		resp.ToolCalls = calls
	}
	if resp.DecisionAnswers != nil {
		resp.DecisionAnswers = append(make([]llm.DecisionAnswer, 0, len(resp.DecisionAnswers)), resp.DecisionAnswers...)
	}
	if resp.DecisionEscalation != nil {
		escalation := *resp.DecisionEscalation
		resp.DecisionEscalation = &escalation
	}
	return resp
}

func (h *cacheHandler) Name() string { return h.next.Name() }

func (h *cacheHandler) EstimateCost(req llm.CompletionRequest) float64 {
	return h.next.EstimateCost(req)
}

func (h *cacheHandler) RetryPolicy() (int, time.Duration) {
	return h.next.RetryPolicy()
}

// servedFromCache is the response of a hit, after telling the observers of the
// request it answers that a cache did, so that its span says no provider was
// called and nothing was bought.
func servedFromCache(ctx context.Context, resp llm.CompletionResponse) llm.CompletionResponse {
	ops.NoteCacheHit(ctx)
	return cacheHitResponse(resp)
}

// cacheHitResponse is what a hit (a genuine store hit, or a follower sharing
// a leader's successful outcome) returns: the recorded content, model, and
// finish reason unchanged, but token usage zeroed. See the "A hit is a hit,
// never a free call" section of Cache's doc comment for why the usage is
// zeroed rather than replayed, and what that does and does not accomplish.
//
// FinishReason is deliberately left as recorded rather than overwritten with
// something like "cache_hit": internal/llm/classify.go's ClassifyCompletion
// reads FinishReason to decide whether a successful-looking response is
// actually a truncation or a policy refusal, and a response served from a
// store a caller supplied, which may hold anything, must classify as it did
// when it was made. The default path never stores such a response (see
// cacheable).
//
// The tool calls are copied, so the caller that receives a hit can edit them
// without changing the stored response or any other hit.
func cacheHitResponse(resp llm.CompletionResponse) llm.CompletionResponse {
	hit := cacheCloneResponse(resp)
	hit.Usage = types.TokenUsage{}
	// Said outright, so the accounting above does not have to guess it from
	// the zeroed usage, which it reads as spend of unknown size.
	hit.Cached = true
	// What the original call measured is the original call's: a hit is answered
	// from memory, and replaying the network time of the request that filled the
	// cache said that a hit in microseconds had spent hundreds of milliseconds
	// on the network. A hit, and a follower sharing a leader's answer, measured
	// nothing.
	hit.Timing = types.Timing{}
	// The request that filled the cache may have been hedged. A hit sent no copy
	// of anything, and the mark would say that it may have been billed twice.
	hit.Hedged = false
	return hit
}

// Placeholders for the TRU-10 axes this codebase has no real value for yet.
// See Cache's doc comment for what each would take to fill in. Fixed, named
// strings rather than "" so a key missing the axis is visibly missing it
// rather than indistinguishable from a key that computed an empty value.
const (
	cacheSeedPlaceholder           = "seed:absent"
	cacheContractLevelPlaceholder  = "contract:absent"
	cacheDecoderVersionPlaceholder = "decoder:absent"
)

// cacheKeyFor builds the exact-result cache key for req, resolved against
// provider and partition. See Cache's doc comment for the full axis list and
// which of TRU-10's axes are real versus placeholder here.
func cacheKeyFor(req llm.CompletionRequest, provider string, partition CachePartition) string {
	axes := []string{
		req.PromptCacheKey, // axis 1: operation, prompt version, schema hash (via P-009's promptCacheKeyFor / S-002's SchemaCacheKey) -- empty when the caller has no CacheIdentity
		cacheDigest(cacheLengthPrefixed([]string{req.SchemaName, cacheCanonicalSchemaJSON(req.JSONSchema)})), // axis 2: schema hash, independent of CacheIdentity
		cacheDigest(cacheLengthPrefixed([]string{req.SystemPrompt, req.UserPrompt})),                         // axis 3: input digest, full prompt as sent -- NOT the stable-only prefix promptCacheKeyFor uses
		provider,                        // axis 4
		req.Model,                       // axis 5: resolved model
		cacheFloatAxis(req.Temperature), // axis 6
		cacheSeedPlaceholder,            // axis 7: placeholder, no Seed field exists
		strconv.Itoa(req.MaxTokens),     // axis 8: not in TRU-10's list, added anyway -- see doc comment
		req.ResponseFormat,              // axis 9
		cacheContractLevelPlaceholder,   // axis 10: placeholder, not reachable at this seam
		partition.Tenant,                // axis 11a
		partition.DataPolicy,            // axis 11b
		cacheDecoderVersionPlaceholder,  // axis 12: placeholder, no versioned decoder exists
		// axis 13: the reasoning effort. Without it the same prompt asked at none
		// and then at high would be answered from the cache with the none answer,
		// which is the one a caller raising the effort is trying to get away from.
		// Whose wish it is is keyed with the word: a preference is dropped on a
		// model it was not measured on and the request runs at the default, so a
		// caller's none and an operation's preference for none are different
		// requests there.
		cacheEffortAxis(req),
		// axes 14 to 16: what else the request asks the model to do. A request
		// with web search on, with tools, or with earlier messages is not the
		// request without them.
		cacheFlagAxis(req.WebSearch, "web-search"),
		cacheToolsAxis(req.Tools, req.ToolChoice),
		cacheMessagesAxis(req.Messages),
	}
	// axis 17: the decision a request carries. It is added only to a request that
	// has one, so that a request without keeps the key it had before the field
	// existed, and an entry a store holds from then is still found. The same
	// prompts asked as another decision, or as none, are another request, and
	// would otherwise be served each other's answers.
	if req.Decision != nil {
		axes = append(axes, cacheDecisionAxis(req.Decision))
	}
	// Each axis is hashed before the axes are joined. A raw join lets a value that
	// holds the separator (a tenant of "a\x1fb" with a data policy of "c", against
	// a tenant of "a" with a data policy of "b\x1fc") produce the same string as
	// another request's two axes. A blank axis is written as "-" so that it is
	// distinct from the hash of anything.
	for i, axis := range axes {
		if axis == "" {
			axes[i] = "-"
			continue
		}
		axes[i] = cacheDigest(axis)
	}
	return cacheDigest(strings.Join(axes, "\x1f"))
}

// cacheDataPolicyKey folds the data policy a client declared into key. The
// policy is read from the context, not the request, so cacheKeyFor does not
// see it. Every field is part of it, whether or not it concerns caching: two
// clients that permit different regions, providers or retention are different
// requesters even when both allow their results to be kept.
func cacheDataPolicyKey(key string, policy types.DataPolicy) string {
	return cacheDigest(cacheLengthPrefixed([]string{key, fmt.Sprintf("%#v", policy)}))
}

// cacheEffortAxis is the reasoning effort and whether it is only a preference.
// With no effort there is nothing to prefer, and the flag is not keyed.
func cacheEffortAxis(req llm.CompletionRequest) string {
	if req.ReasoningEffort == "" || !req.ReasoningEffortPreferred {
		return req.ReasoningEffort
	}
	return "preferred:" + req.ReasoningEffort
}

func cacheFlagAxis(on bool, name string) string {
	if on {
		return name
	}
	return ""
}

// cacheToolsAxis is a digest of the tools offered, in the order offered, and of
// which one the model was told to use. The order is kept because it is sent.
func cacheToolsAxis(tools []llm.Tool, choice string) string {
	if len(tools) == 0 && choice == "" {
		return ""
	}
	parts := make([]string, 0, 3*len(tools)+1)
	for _, tool := range tools {
		parts = append(parts, tool.Name, tool.Description, cacheCanonicalSchemaJSON(tool.Parameters))
	}
	parts = append(parts, choice)
	return cacheDigest(cacheLengthPrefixed(parts))
}

// cacheMessagesAxis is a digest of the earlier turns of the conversation and of
// the tool calls the model made in them. The turns are digested as they always
// were, so a history with no call in it has the digest it had before a message
// could carry one. When any turn carries calls, the digest of the calls joins it:
// for each call the number of the turn it is in, its id, its name and its
// arguments, so that a call moved to another turn, a call that is not the same
// call, and calls in another order are other histories.
func cacheMessagesAxis(messages []llm.Message) string {
	if len(messages) == 0 {
		return ""
	}
	parts := make([]string, 0, 3*len(messages))
	var calls []string
	for turn, message := range messages {
		parts = append(parts, message.Role, message.Content, message.ToolCallID)
		for _, call := range message.ToolCalls {
			calls = append(calls, strconv.Itoa(turn), call.ID, call.Name, cacheCanonicalArguments(call.Arguments))
		}
	}
	turns := cacheDigest(cacheLengthPrefixed(parts))
	if len(calls) == 0 {
		return turns
	}
	return cacheDigest(cacheLengthPrefixed([]string{turns, "tool calls", cacheDigest(cacheLengthPrefixed(calls))}))
}

// cacheCanonicalArguments renders a call's arguments so that the same arguments
// written with other white space or in another key order are one text: JSON is
// decoded and written again with its keys sorted, numbers as they were written.
// Arguments that are not JSON are the text they are, marked as such so that they
// cannot stand for a JSON value.
func cacheCanonicalArguments(arguments json.RawMessage) string {
	if !json.Valid(arguments) {
		return "text:" + string(arguments)
	}
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return "text:" + string(arguments)
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return "text:" + string(arguments)
	}
	return "json:" + string(encoded)
}

// cacheDecisionAxis is a digest of a decision: its state and guidance, and for
// each question in order its kind, text, ends, options and criteria. Each list is
// written after its length, and criteria that are absent are told from criteria
// that are blank, since the two are different requests to a decision provider.
func cacheDecisionAxis(decision *llm.Decision) string {
	parts := make([]string, 0, 3+8*len(decision.Questions))
	parts = append(parts, decision.State, decision.Guidance, strconv.Itoa(len(decision.Questions)))
	for _, question := range decision.Questions {
		parts = append(parts, strconv.Itoa(int(question.Kind)), question.Ask,
			strconv.Itoa(question.Low), strconv.Itoa(question.High), strconv.Itoa(len(question.Options)))
		parts = append(parts, question.Options...)
		if question.Criteria == nil {
			parts = append(parts, "criteria:none")
			continue
		}
		parts = append(parts, "criteria:"+strconv.Itoa(len(question.Criteria)))
		parts = append(parts, question.Criteria...)
	}
	return cacheDigest(cacheLengthPrefixed(parts))
}

// cacheLengthPrefixed joins parts so that no two different lists join to the
// same string: each part is written after its own length. A separator alone
// lets a part that contains the separator stand in for two.
func cacheLengthPrefixed(parts []string) string {
	var joined strings.Builder
	for _, part := range parts {
		joined.WriteString(strconv.Itoa(len(part)))
		joined.WriteByte(':')
		joined.WriteString(part)
	}
	return joined.String()
}

// cacheCanonicalSchemaJSON renders req.JSONSchema deterministically.
// encoding/json.Marshal already sorts every map[string]any's keys, at every
// nesting level, which is the same guarantee internal/ops/schemaid.go's
// canonicalJSON documents for the reason CA-001 exists: an unsorted render
// defeats a hash the instant Go's map iteration order picks a different
// sequence on two runs of the identical schema.
func cacheCanonicalSchemaJSON(schema map[string]any) string {
	if schema == nil {
		return ""
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		// Unreachable for a schema this library generated -- GenerateJSONSchema
		// only emits marshalable maps -- but a key derivation must never panic
		// on a caller-shaped input, so this falls back to a stable-enough
		// (if uglier) string rather than propagating the error into a
		// middleware whose contract has no error return.
		return fmt.Sprintf("%v", schema)
	}
	return string(encoded)
}

// cacheFloatAxis formats a float64 for a cache key: the shortest
// round-tripping representation, so the identical value always produces the
// identical byte sequence and two distinct values that print alike (which
// 'g' does not permit) are never confused.
func cacheFloatAxis(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// cacheDigest is the fixed-length identity used inside a cache key. Hashing
// -- rather than concatenating raw bytes -- keeps a logged key from ever
// containing the prompt text it was derived from, which is what
// AGENTS.md's "never log or embed the caller's payload" requires of
// anything built from the caller's data.
func cacheDigest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
