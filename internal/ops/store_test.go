package ops

import (
	"sync"
	"testing"
)

func TestTenantStoreSetGetRoundTrips(t *testing.T) {
	s := newTenantStore[string]()
	s.set("tenant-a", "key-1", "value-1")

	got, ok := s.get("tenant-a", "key-1")
	if !ok || got != "value-1" {
		t.Fatalf("get = (%q, %v), want (value-1, true)", got, ok)
	}
}

func TestTenantStoreGetMissingKeyReportsFalse(t *testing.T) {
	s := newTenantStore[string]()
	s.set("tenant-a", "key-1", "value-1")

	if _, ok := s.get("tenant-a", "no-such-key"); ok {
		t.Fatalf("get for a key never set reported true")
	}
}

func TestTenantStoreGetMissingTenantReportsFalse(t *testing.T) {
	s := newTenantStore[string]()
	if _, ok := s.get("no-such-tenant", "key-1"); ok {
		t.Fatalf("get for a tenant that never wrote anything reported true")
	}
}

func TestTenantStoreIsolatesTenants(t *testing.T) {
	s := newTenantStore[string]()
	s.set("tenant-a", "key-1", "a-value")
	s.set("tenant-b", "key-1", "b-value")

	got, ok := s.get("tenant-a", "key-1")
	if !ok || got != "a-value" {
		t.Fatalf("tenant-a's key-1 = (%q, %v), want (a-value, true)", got, ok)
	}
	got, ok = s.get("tenant-b", "key-1")
	if !ok || got != "b-value" {
		t.Fatalf("tenant-b's key-1 = (%q, %v), want (b-value, true)", got, ok)
	}
}

func TestTenantStoreDeleteTenantEmptiesThatTenantOnly(t *testing.T) {
	s := newTenantStore[string]()
	s.set("tenant-a", "key-1", "a-value")
	s.set("tenant-a", "key-2", "a-value-2")
	s.set("tenant-b", "key-1", "b-value")

	s.deleteTenant("tenant-a")

	if n := s.count("tenant-a"); n != 0 {
		t.Fatalf("tenant-a count after delete = %d, want 0", n)
	}
	if _, ok := s.get("tenant-a", "key-1"); ok {
		t.Fatalf("tenant-a/key-1 still readable after DeleteTenant")
	}
	if _, ok := s.get("tenant-a", "key-2"); ok {
		t.Fatalf("tenant-a/key-2 still readable after DeleteTenant")
	}
	if n := s.count("tenant-b"); n != 1 {
		t.Fatalf("tenant-b count after deleting tenant-a = %d, want 1 (untouched)", n)
	}
}

func TestTenantStoreDeleteKeyRemovesEmptyBucket(t *testing.T) {
	s := newTenantStore[string]()
	s.set("tenant-a", "key-1", "a-value")
	s.deleteKey("tenant-a", "key-1")

	if n := s.tenantCount(); n != 0 {
		t.Fatalf("tenantCount after deleting the only key = %d, want 0 (empty bucket dropped)", n)
	}
}

func TestTenantStoreEntriesReturnsACopy(t *testing.T) {
	s := newTenantStore[string]()
	s.set("tenant-a", "key-1", "a-value")

	entries := s.entries("tenant-a")
	entries["key-1"] = "mutated"

	got, _ := s.get("tenant-a", "key-1")
	if got != "a-value" {
		t.Fatalf("mutating the returned map affected the store: got %q, want a-value", got)
	}
}

func TestTenantStoreConcurrentAccessDoesNotRace(t *testing.T) {
	s := newTenantStore[int]()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s.set("tenant-a", "k", i)
			s.get("tenant-a", "k")
			if i%10 == 0 {
				s.deleteTenant("tenant-a")
			}
		}(i)
	}
	wg.Wait()
}
