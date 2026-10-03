package mw

import (
	"context"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// The cache's own conversions between the root's Response and the internal one
// allocate nothing for an answer that holds no slices: they were a provider built
// and called for each read and each write of the store.
func TestTheCacheStoreAdapterConvertsWithoutAllocating(t *testing.T) {
	store := &mapStore{}
	adapter := storeAdapter{store}
	ctx := context.Background()
	answer := llm.CompletionResponse{Content: "answer", Model: "m", Provider: "p"}
	adapter.SetContext(ctx, "k", answer, time.Hour)
	if got, ok := adapter.GetContext(ctx, "k"); !ok || got.Content != "answer" || got.Model != "m" {
		t.Fatalf("the answer did not come back: %+v, %v", got, ok)
	}
	// What is left is the store's own: the mutex-guarded map of the test double.
	if n := testing.AllocsPerRun(100, func() { adapter.GetContext(ctx, "k") }); n != 0 {
		t.Errorf("a read converted with %v allocations, want 0", n)
	}
	if n := testing.AllocsPerRun(100, func() { adapter.SetContext(ctx, "k", answer, time.Hour) }); n > 1 {
		t.Errorf("a write converted with %v allocations, want at most the 1 of the store's own append", n)
	}
}

// A request through a Cache costs the allocations of its key and its two
// conversions at the boundary of the wrapped package, and no more: 63 for a miss
// and for a hit with a small request, down from 65 and 63.
func TestTheCacheAllocatesWithinItsBudget(t *testing.T) {
	var base schemafluxtest.Provider
	base.Reply("answer")
	req := schemaflux.Request{Model: "m", System: "You answer.", User: "A question.", MaxTokens: 100}
	ctx := context.Background()

	hit := Cache()(&base)
	hit.Complete(ctx, req)
	if n := testing.AllocsPerRun(100, func() { hit.Complete(ctx, req) }); n > 63 {
		t.Errorf("a hit allocated %v times, want at most 63", n)
	}
	miss := Cache(CacheOptions{Store: nopCacheStore{}})(&base)
	if n := testing.AllocsPerRun(100, func() { miss.Complete(ctx, req) }); n > 64 {
		t.Errorf("a miss allocated %v times, want at most 64", n)
	}
}

// nopCacheStore never holds anything.
type nopCacheStore struct{}

func (nopCacheStore) Get(context.Context, string) (schemaflux.Response, bool) {
	return schemaflux.Response{}, false
}
func (nopCacheStore) Set(context.Context, string, schemaflux.Response, time.Duration) {}
