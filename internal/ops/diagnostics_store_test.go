package ops

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

func TestTenantDiagnosticStoreForTenantFilesUnderTenant(t *testing.T) {
	d := NewTenantDiagnosticStore()
	sink := d.ForTenant("tenant-a")
	sink.Capture(types.DiagnosticRecord{ID: "diag-1", Body: "captured"})

	if n := d.EntryCount("tenant-a"); n != 1 {
		t.Fatalf("EntryCount(tenant-a) after one Capture = %d, want 1", n)
	}
	records := d.Records("tenant-a")
	if len(records) != 1 || records[0].ID != "diag-1" {
		t.Fatalf("Records(tenant-a) = %+v, want one record with ID diag-1", records)
	}
}

func TestTenantDiagnosticStoreIsolatesTenants(t *testing.T) {
	d := NewTenantDiagnosticStore()
	d.ForTenant("tenant-a").Capture(types.DiagnosticRecord{ID: "diag-a"})
	d.ForTenant("tenant-b").Capture(types.DiagnosticRecord{ID: "diag-b"})

	if n := d.EntryCount("tenant-a"); n != 1 {
		t.Fatalf("EntryCount(tenant-a) = %d, want 1", n)
	}
	if n := d.EntryCount("tenant-b"); n != 1 {
		t.Fatalf("EntryCount(tenant-b) = %d, want 1", n)
	}
}

func TestTenantDiagnosticStoreForTenantEmptyDiscardsRatherThanFiling(t *testing.T) {
	d := NewTenantDiagnosticStore()
	sink := d.ForTenant("")
	sink.Capture(types.DiagnosticRecord{ID: "diag-orphan"})

	// Filing this under the empty-string tenant would create a record
	// DeleteTenant("") can never reach (DeleteTenant refuses an empty
	// tenantID) -- it must be discarded instead, not stored anywhere.
	if n := d.EntryCount(""); n != 0 {
		t.Fatalf("EntryCount(\"\") = %d, want 0 (record must be discarded, not filed under the empty tenant)", n)
	}
}

func TestTenantDiagnosticStoreCaptureWithNoIDIsDiscarded(t *testing.T) {
	d := NewTenantDiagnosticStore()
	d.ForTenant("tenant-a").Capture(types.DiagnosticRecord{})
	if n := d.EntryCount("tenant-a"); n != 0 {
		t.Fatalf("EntryCount(tenant-a) after capturing a record with no ID = %d, want 0", n)
	}
}

func TestTenantDiagnosticStoreDeleteTenantRemovesRecords(t *testing.T) {
	d := NewTenantDiagnosticStore()
	d.ForTenant("tenant-a").Capture(types.DiagnosticRecord{ID: "diag-1", Body: "one"})
	d.ForTenant("tenant-a").Capture(types.DiagnosticRecord{ID: "diag-2", Body: "two"})
	d.ForTenant("tenant-b").Capture(types.DiagnosticRecord{ID: "diag-3", Body: "three"})

	if err := d.DeleteTenant(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("DeleteTenant = %v, want nil", err)
	}

	// Check the store's actual contents, not just the error -- a
	// DeleteTenant returning nil while the records are still readable is
	// the failure this test exists to catch.
	if n := d.EntryCount("tenant-a"); n != 0 {
		t.Fatalf("EntryCount(tenant-a) after DeleteTenant = %d, want 0", n)
	}
	if records := d.Records("tenant-a"); len(records) != 0 {
		t.Fatalf("Records(tenant-a) after DeleteTenant = %+v, want none", records)
	}
	if n := d.EntryCount("tenant-b"); n != 1 {
		t.Fatalf("EntryCount(tenant-b) after deleting tenant-a = %d, want 1 (untouched)", n)
	}
}

func TestTenantDiagnosticStoreDeleteTenantRefusesEmptyTenant(t *testing.T) {
	d := NewTenantDiagnosticStore()
	if err := d.DeleteTenant(context.Background(), ""); !errors.Is(err, types.ErrTenantIDRequired) {
		t.Fatalf("DeleteTenant(\"\") = %v, want ErrTenantIDRequired", err)
	}
}

func TestTenantDiagnosticStoreDeleteTenantRespectsCancelledContext(t *testing.T) {
	d := NewTenantDiagnosticStore()
	d.ForTenant("tenant-a").Capture(types.DiagnosticRecord{ID: "diag-1"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := d.DeleteTenant(ctx, "tenant-a"); err == nil {
		t.Fatalf("DeleteTenant with a cancelled context returned nil")
	}
	if n := d.EntryCount("tenant-a"); n != 1 {
		t.Fatalf("EntryCount(tenant-a) after a refused deletion = %d, want 1 (untouched)", n)
	}
}

func TestTenantDiagnosticStoreConcurrentCaptureAndDeleteDoesNotRace(t *testing.T) {
	d := NewTenantDiagnosticStore()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			d.ForTenant("tenant-a").Capture(types.DiagnosticRecord{ID: "diag-" + string(rune('a'+i%26))})
			if i%10 == 0 {
				_ = d.DeleteTenant(context.Background(), "tenant-a")
			}
		}(i)
	}
	wg.Wait()
}
