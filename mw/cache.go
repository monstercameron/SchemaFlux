package mw

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
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
//  7. Seed: CACHE_SEED_PLACEHOLDER (see below) -- absent from this codebase.
//  8. Max tokens: req.MaxTokens. Not named in TRU-10's list, included anyway:
//     two calls differing only in max_tokens can produce a genuinely
//     different (truncated versus complete) answer, and silently serving one
//     for a caller who asked for the other is the same failure mode TRU-10
//     is about, even though the axis list did not spell it out.
//  9. Response format: req.ResponseFormat.
//  10. Required contract level: CACHE_CONTRACT_LEVEL_PLACEHOLDER -- see below.
//  11. Data-policy partition: tenant and data-policy, read from ctx via
//     WithCachePartition. See "Partitioning" below.
//  12. Decoder version: CACHE_DECODER_VERSION_PLACEHOLDER -- see below.
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
			next:  next,
			store: cfg.store,
			ttl:   cfg.ttl,
			stats: cfg.stats,
		}
	}
}

// CacheOption configures Cache beyond its always-on default: an unbounded,
// never-expiring in-memory store.
type CacheOption func(*cacheConfig)

type cacheConfig struct {
	ttl   time.Duration
	store CacheStore
	now   func() time.Time
	stats func(CacheEvent)
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
func WithCacheStats(fn func(CacheEvent)) CacheOption {
	return func(c *cacheConfig) { c.stats = fn }
}

// cacheWithClock overrides the store's clock. Unexported on purpose: it
// exists so this package's own tests can drive TTL expiry without sleeping
// real time, not as something a caller should ever need to override -- a
// cache whose notion of "now" disagrees with the rest of the process is a bug,
// not a feature.
func cacheWithClock(now func() time.Time) CacheOption {
	return func(c *cacheConfig) {
		if now != nil {
			c.now = now
		}
	}
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
}

// CacheStore is what mw.Cache reads and writes. The default (cacheMemoryStore)
// is process-local; CacheStore exists as an interface so WithCacheStore can
// swap in something else without this file needing to know what.
type CacheStore interface {
	Get(key string) (llm.CompletionResponse, bool)
	Set(key string, resp llm.CompletionResponse, ttl time.Duration)
}

// cacheEntry is one stored response plus its expiry.
type cacheEntry struct {
	resp      llm.CompletionResponse
	expiresAt time.Time // zero means "never expires"
}

// cacheMemoryStore is the default CacheStore: an in-process map guarded by a
// mutex. Simple on purpose -- a distributed cache is WithCacheStore's job,
// not this type's.
type cacheMemoryStore struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
	now     func() time.Time
}

func cacheNewMemoryStore(now func() time.Time) *cacheMemoryStore {
	if now == nil {
		now = time.Now
	}
	return &cacheMemoryStore{entries: make(map[string]cacheEntry), now: now}
}

func (s *cacheMemoryStore) Get(key string) (llm.CompletionResponse, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[key]
	if !ok {
		return llm.CompletionResponse{}, false
	}
	if !entry.expiresAt.IsZero() && !s.now().Before(entry.expiresAt) {
		// Expired entries are removed on read rather than swept on a timer:
		// a cache with no background goroutine cannot leak one, and the only
		// cost is that an entry nobody asks for again outlives its TTL in
		// memory, which is the same tradeoff every lazy-expiry cache makes.
		delete(s.entries, key)
		return llm.CompletionResponse{}, false
	}
	return entry.resp, true
}

func (s *cacheMemoryStore) Set(key string, resp llm.CompletionResponse, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var expiresAt time.Time
	if ttl > 0 {
		expiresAt = s.now().Add(ttl)
	}
	s.entries[key] = cacheEntry{resp: resp, expiresAt: expiresAt}
}

// cacheCall tracks one in-flight miss so concurrent identical callers
// coalesce onto it instead of each paying for their own provider call.
// "Exact-duplicate calls cost zero" has to hold for calls that arrive at the
// same instant, not only for calls that arrive after a previous one already
// finished and got stored -- a store-based cache alone only catches the
// second kind.
type cacheCall struct {
	done chan struct{}
	resp llm.CompletionResponse
	err  error
}

// cacheHandler is the Handler mw.Cache wraps a provider in.
type cacheHandler struct {
	next  Handler
	store CacheStore
	ttl   time.Duration
	stats func(CacheEvent)

	inflight sync.Map // key (string) -> *cacheCall
}

func (h *cacheHandler) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	partition := cachePartitionFromContext(ctx)
	key := cacheKeyFor(req, h.next.Name(), partition)

	if resp, ok := h.store.Get(key); ok {
		h.emit(CacheEvent{Hit: true, Key: key, Tenant: partition.Tenant, DataPolicy: partition.DataPolicy})
		return cacheHitResponse(resp), nil
	}

	call, leader := h.claim(key)
	if !leader {
		select {
		case <-call.done:
			h.emit(CacheEvent{
				Hit:        call.err == nil,
				Coalesced:  true,
				Key:        key,
				Tenant:     partition.Tenant,
				DataPolicy: partition.DataPolicy,
			})
			if call.err != nil {
				return llm.CompletionResponse{}, call.err
			}
			return cacheHitResponse(call.resp), nil
		case <-ctx.Done():
			return llm.CompletionResponse{}, ctx.Err()
		}
	}

	// Re-check the store now that this call holds the claim. Between the miss
	// above and winning the claim, an earlier leader may have finished, stored
	// its answer, and deleted its in-flight entry -- so a caller that missed
	// the store, was descheduled, and woke up after all of that becomes a
	// second leader and makes a second paid call for an answer already sitting
	// in the cache. It reproduces roughly one run in ten under -shuffle, which
	// is why the single-run verification did not see it.
	//
	// Followers waiting on this claim are still released below, so returning
	// here cannot strand them.
	if cached, ok := h.store.Get(key); ok {
		call.resp = cached
		close(call.done)
		h.inflight.Delete(key)

		h.emit(CacheEvent{Hit: true, Key: key, Tenant: partition.Tenant, DataPolicy: partition.DataPolicy})
		return cacheHitResponse(cached), nil
	}

	resp, err := h.next.Complete(ctx, req)
	if err == nil {
		h.store.Set(key, resp, h.ttl)
	}
	call.resp, call.err = resp, err
	close(call.done)
	h.inflight.Delete(key)

	h.emit(CacheEvent{Hit: false, Key: key, Tenant: partition.Tenant, DataPolicy: partition.DataPolicy})
	return resp, err
}

// claim registers this call as the leader for key if no other call is
// currently in flight for it, or returns the existing leader's cacheCall for
// this call to wait on.
func (h *cacheHandler) claim(key string) (call *cacheCall, leader bool) {
	actual, loaded := h.inflight.LoadOrStore(key, &cacheCall{done: make(chan struct{})})
	return actual.(*cacheCall), !loaded
}

func (h *cacheHandler) emit(evt CacheEvent) {
	if h.stats != nil {
		h.stats(evt)
	}
}

func (h *cacheHandler) Name() string { return h.next.Name() }

func (h *cacheHandler) EstimateCost(req llm.CompletionRequest) float64 {
	return h.next.EstimateCost(req)
}

func (h *cacheHandler) RetryPolicy() (int, time.Duration) {
	return h.next.RetryPolicy()
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
// actually a truncation or a policy refusal. A stored response that was
// originally truncated must still classify as truncated when served from
// cache -- overwriting FinishReason would turn a real failure invisible,
// which is a worse bug than the cost-accounting gap this function cannot
// close.
func cacheHitResponse(resp llm.CompletionResponse) llm.CompletionResponse {
	hit := resp
	hit.Usage = types.TokenUsage{}
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
		cacheDigest(req.SchemaName + "\x1f" + cacheCanonicalSchemaJSON(req.JSONSchema)), // axis 2: schema hash, independent of CacheIdentity
		cacheDigest(req.SystemPrompt + "\x1f" + req.UserPrompt),                         // axis 3: input digest, full prompt as sent -- NOT the stable-only prefix promptCacheKeyFor uses
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
	}
	for i, axis := range axes {
		if strings.TrimSpace(axis) == "" {
			axes[i] = "-"
		}
	}
	return cacheDigest(strings.Join(axes, "\x1f"))
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
