package ops

import (
	"context"
	"errors"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// SEC-005 names the result cache first among the stores a tenant deletion
// must reach. mw.Cache (mw/cache.go) is the result cache this library ships
// today, and it is not this file's DeleteTenant target: its CacheStore
// interface is Get(key)/Set(key, resp, ttl), and the key it is called with
// is already an opaque SHA-256 digest folding the tenant partition in with
// everything else (mw/cache.go's cacheKeyFor, axis 11) -- a hash cannot be
// reversed to recover which keys belonged to a tenant, so no store built
// only against that interface can implement real per-tenant deletion. That
// is an architectural gap in the calling seam, not something a smarter
// CacheStore implementation can route around, and internal/ops cannot close
// it from here: mw imports internal/ops already, so internal/ops importing
// mw back would cycle, and mw/cache.go itself is outside this task's edit
// scope regardless.
//
// Two things follow from that, and both are implemented below rather than
// left as a comment:
//
//  1. TenantCacheStore is a genuine, tenant-partitioned result cache this
//     package owns outright, with its own tenant-first API (Set/Get take
//     tenantID explicitly). A caller who wants tenant-scoped caching --
//     custom middleware, a per-request cache, anywhere this package's own
//     code wants to cache something --  uses this and gets a DeleteTenant
//     that actually empties the tenant's entries, checkable via EntryCount.
//  2. WrapExternalCacheStore covers the case AGENTS.md's "never fail open"
//     is most worried about: a caller who already has an mw.CacheStore (or
//     anything shaped like one) and wants to register it for tenant
//     deletion anyway. Wrapping it does not invent tenant tracking that
//     was never there -- it returns ErrCacheStoreNotTenantScoped from every
//     DeleteTenant call, honestly, instead of a nil error that deleted
//     nothing. A caller who passes an mw.Cache store into
//     types.DeleteTenantData via this wrapper gets a real error naming the
//     store, not a false "done."

// externalCacheStore restates the shape of mw.CacheStore (Get(key)
// (llm.CompletionResponse, bool); Set(key string, resp
// llm.CompletionResponse, ttl time.Duration)) locally, because internal/ops
// cannot import mw (mw imports internal/ops already; the reverse would be a
// cycle) to reference the type directly. Go's structural typing means an
// actual mw.CacheStore value -- including mw's own default in-process
// store -- satisfies this interface without either package knowing about
// the other's exact type.
type externalCacheStore interface {
	Get(key string) (llm.CompletionResponse, bool)
	Set(key string, resp llm.CompletionResponse, ttl time.Duration)
}

// ErrCacheStoreNotTenantScoped is what WrapExternalCacheStore's DeleteTenant
// always returns. It names the actual reason (the store's key carries no
// recoverable tenant identity) so a caller reading it back from
// types.DeleteTenantData's joined error knows this is a structural
// limitation, not a transient failure worth retrying.
var ErrCacheStoreNotTenantScoped = errors.New("cache store's key does not carry a recoverable tenant identity; delete the underlying store to remove this tenant's cached results")

// externalCacheStoreAdapter is the types.TenantScopedStore
// WrapExternalCacheStore returns. It never deletes anything -- there is
// nothing in the wrapped interface it could delete by tenant -- and never
// claims to.
type externalCacheStoreAdapter struct {
	store externalCacheStore
}

// WrapExternalCacheStore registers store (anything shaped like
// mw.CacheStore) for SEC-005's fan-out while being honest about what it can
// do: DeleteTenant on the result always fails with
// ErrCacheStoreNotTenantScoped. See this file's package comment for why
// that is correct behaviour rather than a gap -- a store whose Set/Get
// never receive a tenant has no per-tenant entries to find, and reporting
// success here would be exactly the "deletion API that appears to work and
// deletes nothing" SECURITY.md and TODOS.md's SEC-005 entry both call out.
// A nil store is accepted and produces the same error, so a caller that
// wires this in unconditionally (even when it never configured a cache)
// still gets an honest answer instead of a nil-pointer panic.
func WrapExternalCacheStore(store externalCacheStore) types.TenantScopedStore {
	return externalCacheStoreAdapter{store: store}
}

func (a externalCacheStoreAdapter) DeleteTenant(ctx context.Context, tenantID string) error {
	if tenantID == "" {
		return types.ErrTenantIDRequired
	}
	return ErrCacheStoreNotTenantScoped
}

// cacheEntry is one tenant-scoped cache slot: the response plus its expiry.
// Mirrors mw/cache.go's own cacheEntry shape (a zero expiresAt means "never
// expires") deliberately, so the two caches behave the same way about TTL
// even though they cannot share an implementation across the import
// boundary described above.
type cacheEntry struct {
	resp      llm.CompletionResponse
	expiresAt time.Time
}

// TenantCacheStore is a tenant-partitioned, in-process result cache this
// package owns. Unlike mw.Cache's default store, every write is tagged with
// the tenant it belongs to at the call site (Set takes tenantID directly,
// not folded into an opaque key), which is what makes DeleteTenant an exact
// operation instead of an impossible one. Safe for concurrent use.
type TenantCacheStore struct {
	store *tenantStore[cacheEntry]
	now   func() time.Time
}

// NewTenantCacheStore builds an empty TenantCacheStore.
func NewTenantCacheStore() *TenantCacheStore {
	return &TenantCacheStore{store: newTenantStore[cacheEntry](), now: time.Now}
}

// newTenantCacheStoreWithClock is for this file's own tests: it lets TTL
// expiry be driven by a fake clock instead of sleeping real time, the same
// reasoning mw/cache.go's cacheWithClock documents for the same problem.
func newTenantCacheStoreWithClock(now func() time.Time) *TenantCacheStore {
	return &TenantCacheStore{store: newTenantStore[cacheEntry](), now: now}
}

// Set stores resp under (tenantID, key). ttl of zero means the entry never
// expires on its own. An empty tenantID is refused rather than silently
// filed under the empty-string partition: a cache entry with no tenant
// attributed to it at write time can never be reached by DeleteTenant, which
// would make it exactly the kind of never-deletable resident data SEC-005
// exists to prevent.
func (c *TenantCacheStore) Set(tenantID, key string, resp llm.CompletionResponse, ttl time.Duration) error {
	if tenantID == "" {
		return types.ErrTenantIDRequired
	}
	if key == "" {
		return errors.New("tenant cache: empty key")
	}
	var expiresAt time.Time
	if ttl > 0 {
		expiresAt = c.now().Add(ttl)
	}
	c.store.set(tenantID, key, cacheEntry{resp: resp, expiresAt: expiresAt})
	return nil
}

// Get returns the response stored under (tenantID, key), and whether it was
// found and not expired. An expired entry is removed on this read (lazy
// expiry, matching mw/cache.go's own store) rather than left to be counted
// by EntryCount as though it were still live data.
func (c *TenantCacheStore) Get(tenantID, key string) (llm.CompletionResponse, bool) {
	entry, ok := c.store.get(tenantID, key)
	if !ok {
		return llm.CompletionResponse{}, false
	}
	if !entry.expiresAt.IsZero() && !c.now().Before(entry.expiresAt) {
		c.store.deleteKey(tenantID, key)
		return llm.CompletionResponse{}, false
	}
	return entry.resp, true
}

// DeleteTenant implements types.TenantScopedStore: every entry stored under
// tenantID is removed, unconditionally, before this returns.
func (c *TenantCacheStore) DeleteTenant(ctx context.Context, tenantID string) error {
	if tenantID == "" {
		return types.ErrTenantIDRequired
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.store.deleteTenant(tenantID)
	return nil
}

// EntryCount reports how many entries remain for tenantID. It is the
// checkable half of DeleteTenant's guarantee: a test (or an operator) reads
// this instead of trusting a nil error, which is exactly the check
// SEC-005's own verification line ("a tenant deletion removes its cache ...
// entries") asks for.
func (c *TenantCacheStore) EntryCount(tenantID string) int {
	return c.store.count(tenantID)
}
