package ops

import "github.com/monstercameron/schemaflux/internal/types"

// SEC-005's fan-out, from this package's side of the boundary.
//
// types.DeleteTenantData (internal/types/retention.go) is the generic
// caller: call DeleteTenant on every types.TenantScopedStore given to it,
// even after an earlier one fails, and join every error. What was missing
// -- recorded in internal/types/retention.go's own package comment -- was
// anything to pass it that actually deletes something. cache.go's
// TenantCacheStore and diagnostics_store.go's TenantDiagnosticStore are
// this package's answer for the two SEC-005-named stores it owns outright.
//
// Two of SEC-005's four named categories are not answered here, and are
// recorded here rather than left silent:
//
//   - Pricing and usage records: pricing/pricing.go owns that store, and
//     pricing/* is explicitly outside this task's edit scope. SEC-005's own
//     verification line's other half -- "the pricing store contains no
//     prompt text by construction" -- does not depend on a deletion hook at
//     all, and internal/types/retention.go's package comment already
//     records that this task did not re-verify it against pricing/*'s
//     current source.
//   - Replay fixtures: schemafluxtest/cassette.go is the only cassette-like
//     store in this repository, and schemafluxtest/* is explicitly outside
//     this task's edit scope. It is also test-support code a production
//     tenant-deletion path has no business depending on -- nothing under
//     internal/ops imports schemafluxtest today, and adding that import
//     only to reach a deletion hook would make production code depend on
//     test fixtures to build. No cassette-shaped store exists inside
//     internal/ops for this file to wire in; inventing one with nothing
//     that writes to it would be exactly the "hook contract exists, nothing
//     real behind it" gap this task exists to close, not a way to close it.
//
// A caller who also uses mw.Cache's default store, the pricing store, or
// schemafluxtest's cassettes is responsible for that store's own deletion
// story -- WrapExternalCacheStore (cache.go) covers the mw.Cache case
// honestly (an explicit unsupported error, not a silent no-op) for a caller
// who wants it registered in the fan-out list anyway.

// TenantStores collects this package's own tenant-scoped stores into the
// slice types.DeleteTenantData takes. A nil cache or diag is skipped rather
// than passed through as a nil types.TenantScopedStore -- DeleteTenantData
// already tolerates a nil entry in the slice, but building the slice here
// means a caller who only constructed one of the two stores does not have
// to know that.
func TenantStores(cache *TenantCacheStore, diag *TenantDiagnosticStore) []types.TenantScopedStore {
	var stores []types.TenantScopedStore
	if cache != nil {
		stores = append(stores, cache)
	}
	if diag != nil {
		stores = append(stores, diag)
	}
	return stores
}
