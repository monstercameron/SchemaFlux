package ops

import (
	"context"

	"github.com/monstercameron/schemaflux/internal/types"
)

// SEC-005 names diagnostic captures as a store a tenant deletion must
// reach. types.DiagnosticSink (internal/types/diagnostics.go) is the
// interface a caller implements to receive captured failure bodies -- this
// library ships no default implementation of it today, so "a tenant
// deletion removes its diagnostic entries" has never been true for anyone
// using only what this library provides. TenantDiagnosticStore is that
// missing default: a real, tenant-partitioned DiagnosticSink whose
// DeleteTenant genuinely empties a tenant's captured records.
//
// DiagnosticSink.Capture(record) takes no tenant parameter -- that
// interface is fixed by internal/types/diagnostics.go, outside this task's
// edit scope -- so a single TenantDiagnosticStore cannot tell which tenant
// a bare Capture call belongs to. ForTenant(tenantID) is the seam instead:
// it returns a DiagnosticSink bound to one tenant, meant to be attached per
// request with ops.WithDiagnosticSink(ctx, store.ForTenant(tenantID),
// policy) the same way any other sink is wired in today. Every record that
// arrives through that bound sink is filed under tenantID, which is what
// makes DeleteTenant(ctx, tenantID) able to remove it.

// TenantDiagnosticStore is a tenant-partitioned, in-process store of
// captured diagnostic records. Safe for concurrent use.
type TenantDiagnosticStore struct {
	store *tenantStore[types.DiagnosticRecord]
}

// NewTenantDiagnosticStore builds an empty TenantDiagnosticStore.
func NewTenantDiagnosticStore() *TenantDiagnosticStore {
	return &TenantDiagnosticStore{store: newTenantStore[types.DiagnosticRecord]()}
}

// ForTenant returns a types.DiagnosticSink that files every record it
// receives under tenantID.
//
// An empty tenantID returns a sink that discards everything instead of
// filing it under the empty-string partition. A record captured with no
// tenant attributed to it at write time could never be reached by
// DeleteTenant afterward -- DeleteTenant itself refuses an empty tenantID,
// per types.ErrTenantIDRequired -- so storing it anyway would create
// exactly the kind of undeletable resident data SEC-005 exists to prevent.
// Discarding is the "never fail open" choice at write time, mirroring the
// choice DeleteTenant already makes at delete time.
func (d *TenantDiagnosticStore) ForTenant(tenantID string) types.DiagnosticSink {
	return tenantDiagnosticSink{tenantID: tenantID, store: d}
}

// tenantDiagnosticSink is the bound DiagnosticSink ForTenant returns.
type tenantDiagnosticSink struct {
	tenantID string
	store    *TenantDiagnosticStore
}

// Capture implements types.DiagnosticSink.
func (s tenantDiagnosticSink) Capture(record types.DiagnosticRecord) {
	if s.tenantID == "" || record.ID == "" {
		return
	}
	s.store.store.set(s.tenantID, record.ID, record)
}

// DeleteTenant implements types.TenantScopedStore: every record filed under
// tenantID is removed, unconditionally, before this returns.
func (d *TenantDiagnosticStore) DeleteTenant(ctx context.Context, tenantID string) error {
	if tenantID == "" {
		return types.ErrTenantIDRequired
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	d.store.deleteTenant(tenantID)
	return nil
}

// EntryCount reports how many records remain for tenantID -- the checkable
// half of DeleteTenant's guarantee, read directly rather than inferred from
// a nil error. See TenantCacheStore.EntryCount's comment for why this
// exists: a DeleteTenant that returns nil while EntryCount stays nonzero is
// the exact failure this store's tests are written to catch.
func (d *TenantDiagnosticStore) EntryCount(tenantID string) int {
	return d.store.count(tenantID)
}

// Records returns a copy of every record currently filed under tenantID.
// Records, not just a count: an operator honoring a deletion request first
// needs to know what is there, and this is the one read path a caller has
// for that against this store.
func (d *TenantDiagnosticStore) Records(tenantID string) []types.DiagnosticRecord {
	entries := d.store.entries(tenantID)
	out := make([]types.DiagnosticRecord, 0, len(entries))
	for _, record := range entries {
		out = append(out, record)
	}
	return out
}
