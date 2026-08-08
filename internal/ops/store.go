package ops

import "sync"

// SEC-005's own stores need the same shape twice: a value cached per
// (tenant, key), and "delete every entry for one tenant" as an operation the
// store can actually perform rather than a hook that compiles and does
// nothing. tenantStore is that shape, built once here instead of once per
// store (cache.go, diagnostics_store.go) so the locking and the deletion
// guarantee are proven in one place rather than reimplemented -- and
// possibly gotten subtly wrong -- twice.
//
// Partitioning by tenant at the top level of the map (tenant -> key -> V),
// rather than folding the tenant into a single composite key the way
// mw/cache.go's cacheKeyFor folds it into a hash, is what makes DeleteTenant
// possible at all: a hash cannot be reversed to find which keys belonged to
// a tenant, but deleting map[tenantID] is exact and immediate.
type tenantStore[V any] struct {
	mu   sync.Mutex
	data map[string]map[string]V // tenantID -> key -> value
}

func newTenantStore[V any]() *tenantStore[V] {
	return &tenantStore[V]{data: make(map[string]map[string]V)}
}

// set stores value under (tenantID, key), replacing whatever was there.
func (s *tenantStore[V]) set(tenantID, key string, value V) {
	s.mu.Lock()
	defer s.mu.Unlock()
	bucket, ok := s.data[tenantID]
	if !ok {
		bucket = make(map[string]V)
		s.data[tenantID] = bucket
	}
	bucket[key] = value
}

// get returns the value stored under (tenantID, key), and whether one
// exists. A tenant with no bucket at all and a tenant whose bucket exists
// but lacks key both report ok == false; the caller does not need to tell
// the two apart.
func (s *tenantStore[V]) get(tenantID, key string) (V, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	bucket, ok := s.data[tenantID]
	if !ok {
		var zero V
		return zero, false
	}
	v, ok := bucket[key]
	return v, ok
}

// deleteKey removes one entry, and the tenant's bucket entirely once it is
// empty, so a tenant that only ever had expired or explicitly-removed
// entries does not linger in tenantCount() forever.
func (s *tenantStore[V]) deleteKey(tenantID, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	bucket, ok := s.data[tenantID]
	if !ok {
		return
	}
	delete(bucket, key)
	if len(bucket) == 0 {
		delete(s.data, tenantID)
	}
}

// deleteTenant removes every entry for tenantID. This is the operation
// SEC-005 is about: not marking entries stale, not scheduling a sweep --
// the map entry is gone before this returns, and a subsequent get for any
// key under tenantID reports ok == false, which the test in this file's
// _test.go companion checks directly rather than trusting the absence of an
// error.
func (s *tenantStore[V]) deleteTenant(tenantID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, tenantID)
}

// count reports how many entries tenantID currently holds. It exists so a
// test -- or an operator -- can check the guarantee "DeleteTenant leaves
// zero" directly against the store's actual contents, rather than inferring
// success from DeleteTenant having returned a nil error. A DeleteTenantData
// that returns nil while count stays nonzero is exactly the failure mode
// this store's tests are written to catch.
func (s *tenantStore[V]) count(tenantID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.data[tenantID])
}

// entries returns a copy of everything stored for tenantID, keyed the same
// way the store keys it internally. A copy, not the live map: a caller
// iterating this must never be able to race the store's own mutex-guarded
// access, and map iteration order is unspecified per AGENTS.md, so a caller
// that needs a stable order sorts the keys itself.
func (s *tenantStore[V]) entries(tenantID string) map[string]V {
	s.mu.Lock()
	defer s.mu.Unlock()
	bucket := s.data[tenantID]
	out := make(map[string]V, len(bucket))
	for k, v := range bucket {
		out[k] = v
	}
	return out
}

// tenantCount reports how many distinct tenants currently have at least one
// entry. Used by tests to confirm DeleteTenant("a") does not disturb
// tenant "b"'s bucket at all, not merely that "a" ends up empty.
func (s *tenantStore[V]) tenantCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.data)
}
