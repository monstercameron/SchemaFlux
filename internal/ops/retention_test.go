package ops

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

func TestTenantStoresSkipsNilArguments(t *testing.T) {
	stores := TenantStores(nil, nil)
	if len(stores) != 0 {
		t.Fatalf("TenantStores(nil, nil) = %d stores, want 0", len(stores))
	}

	cache := NewTenantCacheStore()
	stores = TenantStores(cache, nil)
	if len(stores) != 1 {
		t.Fatalf("TenantStores(cache, nil) = %d stores, want 1", len(stores))
	}
}

func TestTenantStoresIncludesBothWhenBothGiven(t *testing.T) {
	cache := NewTenantCacheStore()
	diag := NewTenantDiagnosticStore()
	stores := TenantStores(cache, diag)
	if len(stores) != 2 {
		t.Fatalf("TenantStores(cache, diag) = %d stores, want 2", len(stores))
	}
}

// TestDeleteTenantDataThroughTheExportedAPIRemovesRealData is the
// integration test AGENTS.md's standard-of-done asks for: the whole stack,
// types.DeleteTenantData calling into two real ops-owned stores through the
// interface it is typed against, not one function tested in isolation.
func TestDeleteTenantDataThroughTheExportedAPIRemovesRealData(t *testing.T) {
	cache := NewTenantCacheStore()
	diag := NewTenantDiagnosticStore()

	if err := cache.Set("tenant-1", "key-1", llm.CompletionResponse{Content: "cached answer"}, 0); err != nil {
		t.Fatalf("cache.Set = %v", err)
	}
	diag.ForTenant("tenant-1").Capture(types.DiagnosticRecord{ID: "diag-1", Body: "captured failure"})

	// A second tenant's data must survive tenant-1's deletion.
	if err := cache.Set("tenant-2", "key-1", llm.CompletionResponse{Content: "other tenant"}, 0); err != nil {
		t.Fatalf("cache.Set = %v", err)
	}
	diag.ForTenant("tenant-2").Capture(types.DiagnosticRecord{ID: "diag-2", Body: "other tenant"})

	err := types.DeleteTenantData(context.Background(), "tenant-1", TenantStores(cache, diag)...)
	if err != nil {
		t.Fatalf("DeleteTenantData = %v, want nil", err)
	}

	if n := cache.EntryCount("tenant-1"); n != 0 {
		t.Fatalf("cache still holds %d entries for tenant-1 after DeleteTenantData", n)
	}
	if n := diag.EntryCount("tenant-1"); n != 0 {
		t.Fatalf("diagnostic store still holds %d records for tenant-1 after DeleteTenantData", n)
	}
	if _, ok := cache.Get("tenant-1", "key-1"); ok {
		t.Fatalf("tenant-1's cached entry is still readable after DeleteTenantData")
	}

	if n := cache.EntryCount("tenant-2"); n != 1 {
		t.Fatalf("tenant-2's cache entry was disturbed by tenant-1's deletion: EntryCount = %d, want 1", n)
	}
	if n := diag.EntryCount("tenant-2"); n != 1 {
		t.Fatalf("tenant-2's diagnostic record was disturbed by tenant-1's deletion: EntryCount = %d, want 1", n)
	}
}

// TestDeleteTenantDataWithUnsupportedCacheStoreReportsFailure covers the
// mixed fan-out: a real store that can delete, alongside a wrapped
// external cache store that honestly cannot. The combined call must report
// the failure (via errors.Join), not swallow it because another store in
// the list succeeded, and the store that could delete must still have done
// so.
func TestDeleteTenantDataWithUnsupportedCacheStoreReportsFailure(t *testing.T) {
	diag := NewTenantDiagnosticStore()
	diag.ForTenant("tenant-1").Capture(types.DiagnosticRecord{ID: "diag-1"})

	unsupported := WrapExternalCacheStore(fakeExternalCacheStore{})

	err := types.DeleteTenantData(context.Background(), "tenant-1", diag, unsupported)
	if err == nil {
		t.Fatalf("DeleteTenantData with an unsupported store in the list returned nil, want an error naming that store")
	}
	if n := diag.EntryCount("tenant-1"); n != 0 {
		t.Fatalf("the store that COULD delete did not: EntryCount = %d, want 0", n)
	}
}
