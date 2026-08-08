package ops

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

func TestTenantCacheStoreSetGetRoundTrips(t *testing.T) {
	c := NewTenantCacheStore()
	if err := c.Set("tenant-a", "key-1", llm.CompletionResponse{Content: "hello"}, 0); err != nil {
		t.Fatalf("Set = %v, want nil", err)
	}

	resp, ok := c.Get("tenant-a", "key-1")
	if !ok || resp.Content != "hello" {
		t.Fatalf("Get = (%+v, %v), want (Content: hello, true)", resp, ok)
	}
}

func TestTenantCacheStoreSetRefusesEmptyTenant(t *testing.T) {
	c := NewTenantCacheStore()
	err := c.Set("", "key-1", llm.CompletionResponse{}, 0)
	if !errors.Is(err, types.ErrTenantIDRequired) {
		t.Fatalf("Set with empty tenant = %v, want ErrTenantIDRequired", err)
	}
	if c.EntryCount("") != 0 {
		t.Fatalf("a refused Set still stored an entry under the empty tenant")
	}
}

func TestTenantCacheStoreSetRefusesEmptyKey(t *testing.T) {
	c := NewTenantCacheStore()
	if err := c.Set("tenant-a", "", llm.CompletionResponse{}, 0); err == nil {
		t.Fatalf("Set with an empty key was accepted")
	}
}

func TestTenantCacheStoreGetIsolatesTenants(t *testing.T) {
	c := NewTenantCacheStore()
	c.Set("tenant-a", "key-1", llm.CompletionResponse{Content: "a"}, 0)
	c.Set("tenant-b", "key-1", llm.CompletionResponse{Content: "b"}, 0)

	if resp, _ := c.Get("tenant-a", "key-1"); resp.Content != "a" {
		t.Fatalf("tenant-a's entry leaked tenant-b's content: got %q", resp.Content)
	}
}

func TestTenantCacheStoreExpiresOnTTL(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := newTenantCacheStoreWithClock(func() time.Time { return now })

	if err := c.Set("tenant-a", "key-1", llm.CompletionResponse{Content: "hi"}, time.Minute); err != nil {
		t.Fatalf("Set = %v", err)
	}
	if _, ok := c.Get("tenant-a", "key-1"); !ok {
		t.Fatalf("entry missing before TTL elapsed")
	}

	now = now.Add(2 * time.Minute)
	if _, ok := c.Get("tenant-a", "key-1"); ok {
		t.Fatalf("entry still returned after TTL elapsed")
	}
	if n := c.EntryCount("tenant-a"); n != 0 {
		t.Fatalf("expired entry still counted: EntryCount = %d, want 0", n)
	}
}

func TestTenantCacheStoreDeleteTenantRemovesEntries(t *testing.T) {
	c := NewTenantCacheStore()
	c.Set("tenant-a", "key-1", llm.CompletionResponse{Content: "a1"}, 0)
	c.Set("tenant-a", "key-2", llm.CompletionResponse{Content: "a2"}, 0)
	c.Set("tenant-b", "key-1", llm.CompletionResponse{Content: "b1"}, 0)

	if err := c.DeleteTenant(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("DeleteTenant = %v, want nil", err)
	}

	// The failure mode this test exists to catch: DeleteTenant returning nil
	// while the data is still there. Check the store's actual contents, not
	// the error.
	if n := c.EntryCount("tenant-a"); n != 0 {
		t.Fatalf("EntryCount(tenant-a) after DeleteTenant = %d, want 0", n)
	}
	if _, ok := c.Get("tenant-a", "key-1"); ok {
		t.Fatalf("tenant-a/key-1 still readable after DeleteTenant")
	}
	if _, ok := c.Get("tenant-a", "key-2"); ok {
		t.Fatalf("tenant-a/key-2 still readable after DeleteTenant")
	}
	if n := c.EntryCount("tenant-b"); n != 1 {
		t.Fatalf("EntryCount(tenant-b) after deleting tenant-a = %d, want 1 (untouched)", n)
	}
}

func TestTenantCacheStoreDeleteTenantRefusesEmptyTenant(t *testing.T) {
	c := NewTenantCacheStore()
	if err := c.DeleteTenant(context.Background(), ""); !errors.Is(err, types.ErrTenantIDRequired) {
		t.Fatalf("DeleteTenant(\"\") = %v, want ErrTenantIDRequired", err)
	}
}

func TestTenantCacheStoreDeleteTenantRespectsCancelledContext(t *testing.T) {
	c := NewTenantCacheStore()
	c.Set("tenant-a", "key-1", llm.CompletionResponse{}, 0)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := c.DeleteTenant(ctx, "tenant-a"); err == nil {
		t.Fatalf("DeleteTenant with a cancelled context returned nil")
	}
	// A cancelled-context deletion must not have silently deleted anyway --
	// an error return that happened to still do the work would just be a
	// different kind of dishonesty.
	if n := c.EntryCount("tenant-a"); n != 1 {
		t.Fatalf("EntryCount(tenant-a) after a refused deletion = %d, want 1 (untouched)", n)
	}
}

func TestTenantCacheStoreDeleteTenantOnEmptyTenantIsNotAnError(t *testing.T) {
	c := NewTenantCacheStore()
	if err := c.DeleteTenant(context.Background(), "tenant-with-nothing-cached"); err != nil {
		t.Fatalf("DeleteTenant for a tenant with no entries = %v, want nil", err)
	}
}

func TestWrapExternalCacheStoreReportsUnsupportedRatherThanSucceeding(t *testing.T) {
	store := WrapExternalCacheStore(fakeExternalCacheStore{})
	err := store.DeleteTenant(context.Background(), "tenant-a")
	if err == nil {
		t.Fatalf("DeleteTenant on a wrapped non-tenant-scoped cache store returned nil -- exactly the fail-open case this wrapper exists to prevent")
	}
	if !errors.Is(err, ErrCacheStoreNotTenantScoped) {
		t.Fatalf("DeleteTenant error = %v, want ErrCacheStoreNotTenantScoped", err)
	}
}

func TestWrapExternalCacheStoreRefusesEmptyTenantBeforeClaimingUnsupported(t *testing.T) {
	store := WrapExternalCacheStore(fakeExternalCacheStore{})
	err := store.DeleteTenant(context.Background(), "")
	if !errors.Is(err, types.ErrTenantIDRequired) {
		t.Fatalf("DeleteTenant(\"\") = %v, want ErrTenantIDRequired (checked before the unsupported case)", err)
	}
}

func TestWrapExternalCacheStoreHandlesNilStore(t *testing.T) {
	store := WrapExternalCacheStore(nil)
	if err := store.DeleteTenant(context.Background(), "tenant-a"); !errors.Is(err, ErrCacheStoreNotTenantScoped) {
		t.Fatalf("DeleteTenant on a nil wrapped store = %v, want ErrCacheStoreNotTenantScoped, not a panic or a nil", err)
	}
}

// fakeExternalCacheStore stands in for mw.CacheStore's shape: Get/Set only,
// no tenant dimension anywhere in the signature.
type fakeExternalCacheStore struct{}

func (fakeExternalCacheStore) Get(key string) (llm.CompletionResponse, bool) {
	return llm.CompletionResponse{}, false
}

func (fakeExternalCacheStore) Set(key string, resp llm.CompletionResponse, ttl time.Duration) {}
