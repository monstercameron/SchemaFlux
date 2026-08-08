package types

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// fakeTenantStore is an in-memory TenantScopedStore for testing
// DeleteTenantData's fan-out. It is not one of the library's real stores
// (see retention.go's package doc for why none of those are wired in here)
// -- it exists only to prove DeleteTenantData's own contract: call every
// store, collect every error, refuse an empty tenant ID.
type fakeTenantStore struct {
	mu      sync.Mutex
	deleted map[string]bool
	failFor string // tenant ID this store fails to delete, "" means never
	calls   int
}

func newFakeTenantStore() *fakeTenantStore {
	return &fakeTenantStore{deleted: map[string]bool{}}
}

func (s *fakeTenantStore) DeleteTenant(ctx context.Context, tenantID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.failFor != "" && tenantID == s.failFor {
		return fmt.Errorf("fakeTenantStore: simulated failure for %s", tenantID)
	}
	s.deleted[tenantID] = true
	return nil
}

func TestDeleteTenantDataEmptyTenantIDRefuses(t *testing.T) {
	store := newFakeTenantStore()
	err := DeleteTenantData(context.Background(), "", store)
	if !errors.Is(err, ErrTenantIDRequired) {
		t.Fatalf("DeleteTenantData with empty tenantID = %v, want ErrTenantIDRequired", err)
	}
	if store.calls != 0 {
		t.Fatalf("store was called %d times for a refused request, want 0", store.calls)
	}
}

func TestDeleteTenantDataSingleStoreSucceeds(t *testing.T) {
	store := newFakeTenantStore()
	if err := DeleteTenantData(context.Background(), "tenant-1", store); err != nil {
		t.Fatalf("DeleteTenantData = %v, want nil", err)
	}
	if !store.deleted["tenant-1"] {
		t.Fatalf("tenant-1 was not deleted from the store")
	}
}

func TestDeleteTenantDataCallsEveryStore(t *testing.T) {
	stores := []*fakeTenantStore{newFakeTenantStore(), newFakeTenantStore(), newFakeTenantStore()}
	scoped := make([]TenantScopedStore, len(stores))
	for i, s := range stores {
		scoped[i] = s
	}
	if err := DeleteTenantData(context.Background(), "tenant-2", scoped...); err != nil {
		t.Fatalf("DeleteTenantData = %v, want nil", err)
	}
	for i, s := range stores {
		if !s.deleted["tenant-2"] {
			t.Errorf("store %d did not delete tenant-2", i)
		}
	}
}

func TestDeleteTenantDataNoStoresIsNotAnError(t *testing.T) {
	if err := DeleteTenantData(context.Background(), "tenant-3"); err != nil {
		t.Fatalf("DeleteTenantData with no stores = %v, want nil (nothing to delete from)", err)
	}
}

func TestDeleteTenantDataNilStoreIsSkipped(t *testing.T) {
	store := newFakeTenantStore()
	if err := DeleteTenantData(context.Background(), "tenant-4", nil, store, nil); err != nil {
		t.Fatalf("DeleteTenantData with nil entries = %v, want nil", err)
	}
	if !store.deleted["tenant-4"] {
		t.Fatalf("the non-nil store was not called")
	}
}

// The core "even after an earlier one fails" property: a failing FIRST
// store must not prevent a later store from being called.
func TestDeleteTenantDataContinuesAfterAnEarlierFailure(t *testing.T) {
	failing := newFakeTenantStore()
	failing.failFor = "tenant-5"
	succeeding := newFakeTenantStore()

	err := DeleteTenantData(context.Background(), "tenant-5", failing, succeeding)
	if err == nil {
		t.Fatalf("expected an error since the first store failed")
	}
	if !succeeding.deleted["tenant-5"] {
		t.Fatalf("the second store was never called after the first failed -- a failing store must not block the rest")
	}
}

func TestDeleteTenantDataCollectsAllFailures(t *testing.T) {
	first := newFakeTenantStore()
	first.failFor = "tenant-6"
	second := newFakeTenantStore()
	second.failFor = "tenant-6"
	third := newFakeTenantStore() // succeeds

	err := DeleteTenantData(context.Background(), "tenant-6", first, second, third)
	if err == nil {
		t.Fatalf("expected a combined error")
	}
	// errors.Join produces a multi-error; both underlying failures must be
	// reachable, not just the first one swallowing the second.
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		t.Fatalf("error does not support multi-unwrap (errors.Join contract): %v", err)
	}
	if len(joined.Unwrap()) != 2 {
		t.Fatalf("expected 2 collected errors (stores 0 and 1), got %d: %v", len(joined.Unwrap()), err)
	}
	if !third.deleted["tenant-6"] {
		t.Fatalf("the third (succeeding) store was not reached")
	}
}

func TestDeleteTenantDataDoesNotCrossContaminateTenants(t *testing.T) {
	store := newFakeTenantStore()
	if err := DeleteTenantData(context.Background(), "tenant-a", store); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.deleted["tenant-b"] {
		t.Fatalf("deleting tenant-a marked tenant-b as deleted too")
	}
}

func TestDeleteTenantDataRespectsContext(t *testing.T) {
	// DeleteTenant receives whatever ctx DeleteTenantData was given; a store
	// that checks ctx.Err() itself should see the same context, not a
	// derived or dropped one.
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "marker")

	var sawMarker bool
	store := ctxCheckingStore{fn: func(ctx context.Context) {
		if v, _ := ctx.Value(ctxKey{}).(string); v == "marker" {
			sawMarker = true
		}
	}}

	if err := DeleteTenantData(ctx, "tenant-7", store); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !sawMarker {
		t.Fatalf("DeleteTenantData did not pass its context through to the store")
	}
}

type ctxCheckingStore struct {
	fn func(context.Context)
}

func (s ctxCheckingStore) DeleteTenant(ctx context.Context, tenantID string) error {
	s.fn(ctx)
	return nil
}

func TestDeleteTenantDataErrorNamesTheStoreIndex(t *testing.T) {
	ok1 := newFakeTenantStore()
	failing := newFakeTenantStore()
	failing.failFor = "tenant-8"

	err := DeleteTenantData(context.Background(), "tenant-8", ok1, failing)
	if err == nil {
		t.Fatalf("expected an error")
	}
	if got := err.Error(); !strings.Contains(got, "store 1") {
		t.Fatalf("error %q does not identify which store (index 1) failed", got)
	}
}
