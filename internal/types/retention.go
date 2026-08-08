package types

import (
	"context"
	"errors"
	"fmt"
)

// SEC-005. Retention and deletion.
//
// "Retention and deletion for every store the library owns -- result
// caches, diagnostic captures, pricing and usage records, replay fixtures
// ... Tenant-scoped deletion hooks where the adapter supports them."
//
// TenantScopedStore and DeleteTenantData are that hook, and only that hook:
// this file defines the contract a store implements and the fan-out that
// calls it across every store a caller wires in. It does NOT wire any real
// store into it. The stores SEC-005 names each live in a file outside this
// task's edit scope:
//
//   - mw/cache.go (the result cache) is not on the permitted file list
//     (only new files under mw/, not edits to the existing one).
//   - pricing/* is explicitly excluded.
//   - internal/types/diagnostics.go's DiagnosticSink is an interface a
//     CALLER implements, not a store this package owns; adding a
//     DeleteTenant method to it would mean editing diagnostics.go, which is
//     outside this task's edit scope (only policy.go and new files under
//     internal/types/ are permitted).
//   - schemafluxtest's replay fixtures live under schemafluxtest/*, also
//     explicitly excluded.
//
// So: a caller who owns a store that already tags its entries with a
// tenant ID can implement TenantScopedStore and pass it to
// DeleteTenantData today. Making mw.Cache, the pricing store, and
// DiagnosticSink actually satisfy that interface -- which is what "a
// tenant deletion removes its cache and diagnostic entries" (SEC-005's own
// verification line) asks for -- is not done, and is recorded here as a
// limitation rather than silently left out.
//
// "the pricing store contains no prompt text by construction" -- the
// other half of SEC-005's verification -- is already true independent of
// this file: pricing.go (outside this task's scope to change, and
// unchanged by it) records token counts, model IDs, request IDs, and money,
// per AGENTS.md's "never spend money by accident" section and PR-005's own
// design; nothing in this task altered that, and this file does not claim
// to have re-verified it against pricing/*'s current source.

// TenantScopedStore is a store this library owns that can delete everything
// it holds for one tenant. An adapter that cannot scope its data by tenant
// (e.g. a shared cache keyed only by request hash, with no tenant dimension
// at all) simply does not implement this interface -- there is no partial
// or best-effort deletion here, because a deletion hook that sometimes
// silently does nothing is worse than no hook, per AGENTS.md's "never fail
// open": a caller checking "did this delete the tenant's data" must get an
// honest answer, not a call that compiled and did nothing.
type TenantScopedStore interface {
	// DeleteTenant removes every entry belonging to tenantID from the
	// store. It returns an error if the deletion could not be completed or
	// verified; a store that cannot tell whether it succeeded must return an
	// error rather than reporting success.
	DeleteTenant(ctx context.Context, tenantID string) error
}

// ErrTenantIDRequired is returned by DeleteTenantData for an empty
// tenantID. Deleting "everything with no tenant filter" is not what an
// empty string should mean here -- a caller that wants a global wipe should
// say so explicitly per-store, not send a request this function could
// misinterpret as one. Refuse rather than guess, per AGENTS.md's "never
// fail open."
var ErrTenantIDRequired = errors.New("tenant id is required for a tenant-scoped deletion")

// DeleteTenantData calls DeleteTenant(ctx, tenantID) on every store, even
// after an earlier one fails -- a deletion request against three stores
// where the first fails must still attempt the other two, or a single
// unavailable store would leave a tenant's data resident in every store
// after it in the list, with the caller believing the whole request failed
// uniformly. Every store's error is collected and returned together via
// errors.Join, so a caller can inspect exactly which stores did not
// complete (errors.Is/errors.As reach each wrapped error) rather than only
// learning that "something" failed.
func DeleteTenantData(ctx context.Context, tenantID string, stores ...TenantScopedStore) error {
	if tenantID == "" {
		return ErrTenantIDRequired
	}

	var errs []error
	for i, store := range stores {
		if store == nil {
			continue
		}
		if err := store.DeleteTenant(ctx, tenantID); err != nil {
			errs = append(errs, fmt.Errorf("store %d: %w", i, err))
		}
	}
	return errors.Join(errs...)
}
