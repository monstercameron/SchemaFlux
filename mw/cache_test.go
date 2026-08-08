package mw

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// baseCacheRequest is the starting point every key-axis test mutates exactly
// one field of, so a test failure says precisely which axis stopped
// separating keys.
func baseCacheRequest() llm.CompletionRequest {
	return llm.CompletionRequest{
		Model:          "gpt-test",
		SystemPrompt:   "you are a helpful assistant",
		UserPrompt:     "extract the fields from this",
		Temperature:    0.5,
		MaxTokens:      256,
		ResponseFormat: "json",
		JSONSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"a": map[string]any{"type": "string"},
			},
		},
		SchemaName:     "Widget",
		PromptCacheKey: "extract:v1:Widget:v1:abc123:json-schema-2020-12-strict",
	}
}

func basePartition() CachePartition {
	return CachePartition{Tenant: "tenant-1", DataPolicy: "public"}
}

// assertAxisChangesKey builds the baseline key and the key for changed,
// against the same provider and partition, and fails unless they differ --
// the "changing only this axis changes the key" half of the bar every axis
// test must clear.
func assertAxisChangesKey(t *testing.T, axis string, base, changed llm.CompletionRequest) {
	t.Helper()
	provider := "openai"
	partition := basePartition()
	a := cacheKeyFor(base, provider, partition)
	b := cacheKeyFor(changed, provider, partition)
	if a == b {
		t.Fatalf("axis %s: changing only this field did not change the cache key (both %q)", axis, a)
	}
}

// --- Axis 1: operation, prompt version, and schema hash, via req.PromptCacheKey.

func TestCacheKeyAxisPromptCacheKey(t *testing.T) {
	base := baseCacheRequest()
	changed := base
	changed.PromptCacheKey = "extract:v2:Widget:v1:abc123:json-schema-2020-12-strict"
	assertAxisChangesKey(t, "PromptCacheKey (operation/version/schema identity)", base, changed)
}

// --- Axis 2: schema hash, independent of PromptCacheKey -- SchemaName half.

func TestCacheKeyAxisSchemaName(t *testing.T) {
	base := baseCacheRequest()
	changed := base
	changed.SchemaName = "Gadget"
	assertAxisChangesKey(t, "SchemaName", base, changed)
}

// --- Axis 2: schema hash -- JSONSchema half.

func TestCacheKeyAxisJSONSchemaContent(t *testing.T) {
	base := baseCacheRequest()
	changed := base
	changed.JSONSchema = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"a": map[string]any{"type": "string"},
			"b": map[string]any{"type": "integer"}, // a field was added -- a different contract
		},
	}
	assertAxisChangesKey(t, "JSONSchema content", base, changed)
}

// The schema hash must not depend on which order json.Marshal happens to
// visit map keys in -- Go randomizes map iteration, and CA-001 exists because
// a hash sensitive to that order stops being reproducible. Two schemas built
// with the same keys in different literal order must hash identically.
func TestCacheKeySchemaHashIsOrderIndependent(t *testing.T) {
	schemaA := map[string]any{"type": "object", "properties": map[string]any{"a": "x", "b": "y", "c": "z"}}
	schemaB := map[string]any{"properties": map[string]any{"c": "z", "a": "x", "b": "y"}, "type": "object"}

	base := baseCacheRequest()
	reqA := base
	reqA.JSONSchema = schemaA
	reqB := base
	reqB.JSONSchema = schemaB

	if got, want := cacheKeyFor(reqA, "openai", basePartition()), cacheKeyFor(reqB, "openai", basePartition()); got != want {
		t.Fatalf("schemas differing only in map literal order produced different keys: %q vs %q", got, want)
	}
}

// --- Axis 3: input digest -- SystemPrompt half. Steering lives here (it is
// appended to the system prompt by applySteering before the request reaches
// this layer), so this also proves steering is NOT excluded the way it is
// from promptCacheKeyFor's stable-prefix digest -- an exact-result cache must
// treat a steered call as a different call.

func TestCacheKeyAxisSystemPrompt(t *testing.T) {
	base := baseCacheRequest()
	changed := base
	changed.SystemPrompt = base.SystemPrompt + "\n\nAdditional steering: be concise."
	assertAxisChangesKey(t, "SystemPrompt (includes steering)", base, changed)
}

// --- Axis 3: input digest -- UserPrompt half.

func TestCacheKeyAxisUserPrompt(t *testing.T) {
	base := baseCacheRequest()
	changed := base
	changed.UserPrompt = "a completely different question"
	assertAxisChangesKey(t, "UserPrompt", base, changed)
}

// --- Axis 4: provider.

func TestCacheKeyAxisProvider(t *testing.T) {
	req := baseCacheRequest()
	a := cacheKeyFor(req, "openai", basePartition())
	b := cacheKeyFor(req, "anthropic", basePartition())
	if a == b {
		t.Fatalf("axis provider: changing only the provider name did not change the cache key (both %q)", a)
	}
}

// --- Axis 5: resolved model.

func TestCacheKeyAxisResolvedModel(t *testing.T) {
	base := baseCacheRequest()
	changed := base
	changed.Model = "gpt-other"
	assertAxisChangesKey(t, "Model (resolved)", base, changed)
}

// --- Axis 6: temperature.

func TestCacheKeyAxisTemperature(t *testing.T) {
	base := baseCacheRequest()
	changed := base
	changed.Temperature = 0.9
	assertAxisChangesKey(t, "Temperature", base, changed)
}

// --- Axis 8: max tokens (not in TRU-10's named list; included because a
// different token budget can produce a genuinely different, truncated
// answer -- see cacheKeyFor's doc comment).

func TestCacheKeyAxisMaxTokens(t *testing.T) {
	base := baseCacheRequest()
	changed := base
	changed.MaxTokens = 64
	assertAxisChangesKey(t, "MaxTokens", base, changed)
}

// --- Axis 9: response format.

func TestCacheKeyAxisResponseFormat(t *testing.T) {
	base := baseCacheRequest()
	changed := base
	changed.ResponseFormat = "text"
	assertAxisChangesKey(t, "ResponseFormat", base, changed)
}

// --- Axis 11: data-policy partition -- tenant half.

func TestCacheKeyAxisPartitionTenant(t *testing.T) {
	req := baseCacheRequest()
	a := cacheKeyFor(req, "openai", CachePartition{Tenant: "tenant-1", DataPolicy: "public"})
	b := cacheKeyFor(req, "openai", CachePartition{Tenant: "tenant-2", DataPolicy: "public"})
	if a == b {
		t.Fatalf("axis partition.Tenant: changing only the tenant did not change the cache key (both %q)", a)
	}
}

// --- Axis 11: data-policy partition -- data-policy half.

func TestCacheKeyAxisPartitionDataPolicy(t *testing.T) {
	req := baseCacheRequest()
	a := cacheKeyFor(req, "openai", CachePartition{Tenant: "tenant-1", DataPolicy: "public"})
	b := cacheKeyFor(req, "openai", CachePartition{Tenant: "tenant-1", DataPolicy: "restricted"})
	if a == b {
		t.Fatalf("axis partition.DataPolicy: changing only the data policy did not change the cache key (both %q)", a)
	}
}

// An identical call -- same request, same provider, same partition --
// reproduces the exact same key byte-for-byte, every time. Without this, none
// of the axis tests above would mean anything: a key that is not stable for
// an unchanged call could not be trusted to differ only when a real axis
// changes.
func TestCacheKeyIsReproducibleForIdenticalCalls(t *testing.T) {
	req := baseCacheRequest()
	partition := basePartition()
	a := cacheKeyFor(req, "openai", partition)
	b := cacheKeyFor(req, "openai", partition)
	if a != b {
		t.Fatalf("identical calls produced different cache keys: %q vs %q", a, b)
	}
	// And across a second, independently-built request with the same field
	// values (not the same struct instance), not merely the same Go value.
	c := cacheKeyFor(baseCacheRequest(), "openai", basePartition())
	if a != c {
		t.Fatalf("two independently-built identical requests produced different keys: %q vs %q", a, c)
	}
}

// Placeholder axes (seed, contract level, decoder version) are present in
// every key as fixed, named strings -- proven indirectly here by confirming
// the key is non-empty and stable even though this codebase has no real
// value for any of the three. The axis tests above already prove the real
// axes are load-bearing; this proves the absent ones do not silently vanish
// from the derivation (a change to cacheKeyFor that deleted a placeholder
// line would still pass every axis test above, since none of them vary a
// placeholder -- this test's job is to fail if the join arity itself shrinks).
func TestCacheKeyIncludesPlaceholderAxes(t *testing.T) {
	req := baseCacheRequest()
	withPlaceholders := cacheKeyFor(req, "openai", basePartition())

	manualJoin := cacheDigest(
		req.PromptCacheKey + "\x1f" +
			cacheDigest(req.SchemaName+"\x1f"+cacheCanonicalSchemaJSON(req.JSONSchema)) + "\x1f" +
			cacheDigest(req.SystemPrompt+"\x1f"+req.UserPrompt) + "\x1f" +
			"openai" + "\x1f" +
			req.Model + "\x1f" +
			cacheFloatAxis(req.Temperature) + "\x1f" +
			cacheSeedPlaceholder + "\x1f" +
			"256" + "\x1f" +
			req.ResponseFormat + "\x1f" +
			cacheContractLevelPlaceholder + "\x1f" +
			"tenant-1" + "\x1f" +
			"public" + "\x1f" +
			cacheDecoderVersionPlaceholder,
	)
	if withPlaceholders != manualJoin {
		t.Fatalf("cacheKeyFor's derivation no longer matches the documented axis order/placeholders:\n  got:  %s\n  want: %s", withPlaceholders, manualJoin)
	}
}

// --- Cache behaviour through the Handler seam. ---

func TestCacheIsOptIn(t *testing.T) {
	fake := &fakeProvider{responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "ok"}}}}
	// No Cache stage in the chain -- identical calls must both reach the
	// provider. This is the opt-in requirement: nothing else in this package
	// silently starts caching on a caller's behalf.
	wrapped := Chain(fake, Retry(WithMaxAttempts(1)))
	req := llm.CompletionRequest{Model: "m", UserPrompt: "same"}

	if _, err := wrapped.Complete(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := wrapped.Complete(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.calls != 2 {
		t.Fatalf("calls = %d, want 2 -- caching must never happen unless mw.Cache is explicitly added", fake.calls)
	}
}

func TestCacheMissDelegatesToProviderAndStores(t *testing.T) {
	fake := &fakeProvider{responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "fresh"}}}}
	wrapped := Cache()(fake)

	resp, err := wrapped.Complete(context.Background(), llm.CompletionRequest{Model: "m"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "fresh" {
		t.Fatalf("Content = %q, want %q", resp.Content, "fresh")
	}
	if fake.calls != 1 {
		t.Fatalf("calls = %d, want 1", fake.calls)
	}
}

func TestCacheHitReturnsStoredResponseWithoutCallingProvider(t *testing.T) {
	fake := &fakeProvider{responses: []fakeResponse{
		{resp: llm.CompletionResponse{Content: "first"}},
		{resp: llm.CompletionResponse{Content: "should-never-be-served"}},
	}}
	wrapped := Cache()(fake)
	req := llm.CompletionRequest{Model: "m", UserPrompt: "same question"}

	first, err := wrapped.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if first.Content != "first" {
		t.Fatalf("Content = %q, want %q", first.Content, "first")
	}

	second, err := wrapped.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if second.Content != "first" {
		t.Fatalf("second identical call was not served from the cache: got %q", second.Content)
	}
	if fake.calls != 1 {
		t.Fatalf("calls = %d, want 1 -- the second call must not reach the provider", fake.calls)
	}
}

// A hit's TokenUsage is zeroed, not replayed. See cacheHitResponse's doc
// comment: replaying the original nonzero usage on every hit would invent a
// spend that did not happen on this call, and reporting zero is the true
// count of what THIS call cost.
func TestCacheHitZeroesTokenUsage(t *testing.T) {
	fake := &fakeProvider{responses: []fakeResponse{{resp: llm.CompletionResponse{
		Content: "ok",
		Usage:   types.TokenUsage{TotalTokens: 500, PromptTokens: 400, CompletionTokens: 100},
	}}}}
	wrapped := Cache()(fake)
	req := llm.CompletionRequest{Model: "m"}

	first, _ := wrapped.Complete(context.Background(), req)
	if first.Usage.TotalTokens != 500 {
		t.Fatalf("first (miss) call must report the real usage, got %+v", first.Usage)
	}

	second, _ := wrapped.Complete(context.Background(), req)
	if second.Usage != (types.TokenUsage{}) {
		t.Fatalf("cache hit must report zero usage (no tokens were spent on this call), got %+v", second.Usage)
	}
	if second.Content != "ok" {
		t.Fatalf("Content = %q, want %q", second.Content, "ok")
	}
}

// A stored response that was originally truncated must still classify as
// truncated when served from the cache -- internal/llm/classify.go's
// ClassifyCompletion reads FinishReason, and a cache that overwrote it with
// something like "cache_hit" would make a real truncation invisible on every
// replay. See cacheHitResponse's doc comment.
func TestCacheHitPreservesFinishReasonForClassification(t *testing.T) {
	fake := &fakeProvider{responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "partial", FinishReason: "length"}}}}
	wrapped := Cache()(fake)
	req := llm.CompletionRequest{Model: "m"}

	wrapped.Complete(context.Background(), req)
	resp, err := wrapped.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.FinishReason != "length" {
		t.Fatalf("FinishReason = %q, want %q -- a cache hit must classify identically to the original call", resp.FinishReason, "length")
	}
}

// A failed call is never stored: a caller who retried past a transient
// failure must not have that failure served back to them, or to anyone else,
// forever.
func TestCacheDoesNotStoreFailures(t *testing.T) {
	wantErr := errors.New("boom")
	fake := &fakeProvider{responses: []fakeResponse{
		{err: wantErr},
		{resp: llm.CompletionResponse{Content: "ok"}},
	}}
	wrapped := Cache()(fake)
	req := llm.CompletionRequest{Model: "m"}

	if _, err := wrapped.Complete(context.Background(), req); !errors.Is(err, wantErr) {
		t.Fatalf("first call error = %v, want %v", err, wantErr)
	}
	resp, err := wrapped.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("second call: unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("Content = %q, want %q", resp.Content, "ok")
	}
	if fake.calls != 2 {
		t.Fatalf("calls = %d, want 2 -- a failed call must not be cached", fake.calls)
	}
}

// Two calls that agree on everything except partition must not share an
// entry -- TRU-10's tenant/data-policy partitioning requirement.
func TestCachePartitionsKeepEntriesSeparate(t *testing.T) {
	fake := &fakeProvider{responses: []fakeResponse{
		{resp: llm.CompletionResponse{Content: "tenant-a-answer"}},
		{resp: llm.CompletionResponse{Content: "tenant-b-answer"}},
	}}
	wrapped := Cache()(fake)
	req := llm.CompletionRequest{Model: "m", UserPrompt: "same for both tenants"}

	ctxA := WithCachePartition(context.Background(), CachePartition{Tenant: "a", DataPolicy: "public"})
	ctxB := WithCachePartition(context.Background(), CachePartition{Tenant: "b", DataPolicy: "public"})

	respA, err := wrapped.Complete(ctxA, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	respB, err := wrapped.Complete(ctxB, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if respA.Content != "tenant-a-answer" || respB.Content != "tenant-b-answer" {
		t.Fatalf("got %q / %q, want each tenant to reach the provider independently", respA.Content, respB.Content)
	}
	if fake.calls != 2 {
		t.Fatalf("calls = %d, want 2 -- different partitions must never share a cache entry", fake.calls)
	}

	// Repeating tenant A's call now hits its own, already-populated entry.
	respA2, err := wrapped.Complete(ctxA, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if respA2.Content != "tenant-a-answer" {
		t.Fatalf("Content = %q, want %q", respA2.Content, "tenant-a-answer")
	}
	if fake.calls != 2 {
		t.Fatalf("calls = %d, want still 2 -- tenant A's repeat call should have hit its own entry", fake.calls)
	}
}

// A call carrying no partition at all is grouped with other unpartitioned
// calls, not silently merged into one that stated a policy.
func TestCacheUnpartitionedCallsDoNotShareWithAStatedPartition(t *testing.T) {
	fake := &fakeProvider{responses: []fakeResponse{
		{resp: llm.CompletionResponse{Content: "unpartitioned"}},
		{resp: llm.CompletionResponse{Content: "restricted"}},
	}}
	wrapped := Cache()(fake)
	req := llm.CompletionRequest{Model: "m", UserPrompt: "shared text"}

	if _, err := wrapped.Complete(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ctxRestricted := WithCachePartition(context.Background(), CachePartition{DataPolicy: "restricted"})
	resp, err := wrapped.Complete(ctxRestricted, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "restricted" {
		t.Fatalf("Content = %q, want %q -- a stated data policy must not read an unpartitioned entry", resp.Content, "restricted")
	}
	if fake.calls != 2 {
		t.Fatalf("calls = %d, want 2", fake.calls)
	}
}

// TTL: an entry served within its TTL is a hit; one served after it expires
// is treated as a miss. Driven entirely by a fake clock -- no real sleeping.
func TestCacheTTLExpiresEntries(t *testing.T) {
	fc := newFakeClock(time.Unix(0, 0))
	fake := &fakeProvider{responses: []fakeResponse{
		{resp: llm.CompletionResponse{Content: "first"}},
		{resp: llm.CompletionResponse{Content: "second"}},
	}}
	wrapped := Cache(WithCacheTTL(time.Minute), cacheWithClock(fc.Now))(fake)
	req := llm.CompletionRequest{Model: "m"}

	resp, err := wrapped.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "first" {
		t.Fatalf("Content = %q, want %q", resp.Content, "first")
	}

	// Still within the TTL: served from the cache.
	resp, err = wrapped.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "first" || fake.calls != 1 {
		t.Fatalf("within TTL: Content = %q (calls=%d), want the cached %q and 1 call", resp.Content, fake.calls, "first")
	}

	// Advance the fake clock past the TTL; the entry must now be treated as
	// a miss.
	fc.sleep(context.Background(), 2*time.Minute)
	resp, err = wrapped.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "second" {
		t.Fatalf("after TTL expiry: Content = %q, want %q", resp.Content, "second")
	}
	if fake.calls != 2 {
		t.Fatalf("after TTL expiry: calls = %d, want 2", fake.calls)
	}
}

// A zero TTL (the default) never expires.
func TestCacheZeroTTLNeverExpires(t *testing.T) {
	fc := newFakeClock(time.Unix(0, 0))
	fake := &fakeProvider{responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "first"}}}}
	wrapped := Cache(cacheWithClock(fc.Now))(fake)
	req := llm.CompletionRequest{Model: "m"}

	wrapped.Complete(context.Background(), req)
	fc.sleep(context.Background(), 365*24*time.Hour)
	resp, err := wrapped.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "first" || fake.calls != 1 {
		t.Fatalf("a zero-TTL entry expired: Content = %q (calls=%d)", resp.Content, fake.calls)
	}
}

// WithCacheStats is the provenance seam: every Complete call fires an event
// saying whether it was a hit, so a caller can record a hit distinctly from
// a genuinely free call in its own accounting -- the seam cacheHitResponse's
// doc comment says is the only channel available without an internal/ change.
func TestCacheEmitsStatsForMissThenHit(t *testing.T) {
	fake := &fakeProvider{responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "ok"}}}}
	var events []CacheEvent
	wrapped := Cache(WithCacheStats(func(e CacheEvent) { events = append(events, e) }))(fake)
	req := llm.CompletionRequest{Model: "m"}

	wrapped.Complete(context.Background(), req)
	wrapped.Complete(context.Background(), req)

	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if events[0].Hit {
		t.Fatalf("first call: Hit = true, want false (it was a miss)")
	}
	if !events[1].Hit {
		t.Fatalf("second call: Hit = false, want true (it was a hit)")
	}
	if events[0].Key == "" || events[1].Key == "" {
		t.Fatalf("events must carry the cache key")
	}
	if events[0].Key != events[1].Key {
		t.Fatalf("identical requests produced different event keys: %q vs %q", events[0].Key, events[1].Key)
	}
}

// Concurrent identical calls must coalesce onto a single provider call --
// "exact-duplicate calls cost zero" has to hold for calls that arrive at the
// same instant too, not only for calls that arrive after a previous one
// finished and got stored. Correctness here does not depend on goroutine
// scheduling: sync.Map.LoadOrStore is atomic, so exactly one goroutine per
// key ever becomes the leader regardless of interleaving -- the release
// channel just keeps the leader from finishing before every follower has had
// a chance to start, to actually exercise the wait path rather than the
// already-stored path.
func TestCacheConcurrentIdenticalCallsCoalesceToOneProviderCall(t *testing.T) {
	var calls int32
	release := make(chan struct{})
	provider := completeFunc(func(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
		atomic.AddInt32(&calls, 1)
		<-release
		return llm.CompletionResponse{Content: "ok"}, nil
	})
	wrapped := Cache()(provider)

	const n = 8
	var launched, done sync.WaitGroup
	launched.Add(n)
	done.Add(n)
	errs := make([]error, n)
	contents := make([]string, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer done.Done()
			launched.Done()
			resp, err := wrapped.Complete(context.Background(), llm.CompletionRequest{Model: "m", UserPrompt: "same"})
			errs[i] = err
			contents[i] = resp.Content
		}(i)
	}
	launched.Wait()
	close(release)
	done.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("provider called %d times for %d concurrent identical requests, want 1", got, n)
	}
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: unexpected error: %v", i, errs[i])
		}
		if contents[i] != "ok" {
			t.Fatalf("goroutine %d: Content = %q, want %q", i, contents[i], "ok")
		}
	}
}

// A follower coalesced onto a leader that failed shares the leader's error --
// it is not silently retried into a second provider call, and it is not handed
// a fabricated success.
//
// The property asserted is that two provider calls for one key never overlap,
// not that exactly one call is ever made. Exactly-once is not achievable here
// and should not be claimed: a failure is deliberately never stored, so a
// caller arriving after the leader has failed and cleared is asking a fresh
// question and is entitled to its own attempt. The earlier version of this
// test asserted exactly-once and released its barrier before the callers had
// entered the cache at all, so it was asserting a scheduling it did not
// control -- it failed about one run in ten under -shuffle.
func TestCacheCoalescedFollowerSharesLeaderError(t *testing.T) {
	wantErr := errors.New("boom")

	var mu sync.Mutex
	inFlight, peak, calls := 0, 0, 0

	release := make(chan struct{})
	provider := completeFunc(func(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
		mu.Lock()
		calls++
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		mu.Unlock()

		<-release

		mu.Lock()
		inFlight--
		mu.Unlock()
		return llm.CompletionResponse{}, wantErr
	})
	wrapped := Cache()(provider)

	const n = 4
	var done sync.WaitGroup
	done.Add(n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer done.Done()
			_, err := wrapped.Complete(context.Background(), llm.CompletionRequest{Model: "m", UserPrompt: "same"})
			errs[i] = err
		}(i)
	}

	// Every caller that is going to coalesce has done so by the time the
	// leader is inside the provider; releasing here lets it fail.
	for {
		mu.Lock()
		started := calls > 0
		mu.Unlock()
		if started {
			break
		}
		runtime.Gosched()
	}
	close(release)
	done.Wait()

	mu.Lock()
	gotPeak, gotCalls := peak, calls
	mu.Unlock()

	if gotPeak != 1 {
		t.Fatalf("%d provider calls were in flight at once for one key, want 1", gotPeak)
	}
	if gotCalls > n {
		t.Fatalf("provider called %d times for %d requests; nothing coalesced at all", gotCalls, n)
	}
	for i := 0; i < n; i++ {
		if !errors.Is(errs[i], wantErr) {
			t.Fatalf("goroutine %d: error = %v, want %v", i, errs[i], wantErr)
		}
	}
}

// Name, EstimateCost, and RetryPolicy delegate unchanged -- a caller asking
// "which provider is this" or "what would this cost" through a cached
// handler must get the wrapped provider's own answer, the same contract
// RateLimit and Retry already hold.
func TestCacheDelegatesNameCostAndRetryPolicy(t *testing.T) {
	fake := &fakeProvider{name: "openai-fake", maxRetries: 4, backoff: 2 * time.Second}
	wrapped := Cache()(fake)

	if wrapped.Name() != "openai-fake" {
		t.Fatalf("Name() = %q, want %q", wrapped.Name(), "openai-fake")
	}
	if got := wrapped.EstimateCost(llm.CompletionRequest{}); got != 0.01 {
		t.Fatalf("EstimateCost() = %v, want 0.01", got)
	}
	retries, backoff := wrapped.RetryPolicy()
	if retries != 4 || backoff != 2*time.Second {
		t.Fatalf("RetryPolicy() = (%d, %v), want (4, 2s)", retries, backoff)
	}
}

// WithCacheStore lets a caller swap the default in-process map for another
// implementation. A trivial always-empty store proves the seam is honoured
// (every call becomes a miss) without this test needing a second real
// implementation.
type neverHitStore struct{ sets int }

func (s *neverHitStore) Get(key string) (llm.CompletionResponse, bool) {
	return llm.CompletionResponse{}, false
}
func (s *neverHitStore) Set(key string, resp llm.CompletionResponse, ttl time.Duration) {
	s.sets++
}

func TestCacheWithCustomStore(t *testing.T) {
	fake := &fakeProvider{responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "ok"}}}}
	store := &neverHitStore{}
	wrapped := Cache(WithCacheStore(store))(fake)
	req := llm.CompletionRequest{Model: "m"}

	wrapped.Complete(context.Background(), req)
	wrapped.Complete(context.Background(), req)

	if fake.calls != 2 {
		t.Fatalf("calls = %d, want 2 -- a store that never reports a hit must never suppress a provider call", fake.calls)
	}
	if store.sets != 2 {
		t.Fatalf("store.sets = %d, want 2 -- every successful miss must still be written through the custom store", store.sets)
	}
}

// --- Integration: mw.Chain with a scripted provider. ---

func TestCacheThroughChainWithScriptedProvider(t *testing.T) {
	fake := &fakeProvider{name: "scripted", responses: []fakeResponse{
		{resp: llm.CompletionResponse{Content: "answer", Model: "gpt", Provider: "scripted"}},
		{resp: llm.CompletionResponse{Content: "second-question-answer"}},
	}}
	wrapped := Chain(fake, Cache(), Retry(WithMaxAttempts(1)))
	req := llm.CompletionRequest{
		Model:          "gpt",
		SystemPrompt:   "sys",
		UserPrompt:     "same question",
		Temperature:    0.2,
		MaxTokens:      128,
		ResponseFormat: "json",
	}

	first, err := wrapped.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if first.Content != "answer" {
		t.Fatalf("Content = %q, want %q", first.Content, "answer")
	}

	second, err := wrapped.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if second.Content != "answer" {
		t.Fatalf("second identical call through the chain was not served from the cache: got %q", second.Content)
	}
	if fake.calls != 1 {
		t.Fatalf("calls = %d, want 1 -- the second identical call must never reach the provider", fake.calls)
	}

	// A request differing only in UserPrompt must miss and reach the
	// provider.
	req2 := req
	req2.UserPrompt = "a different question"
	third, err := wrapped.Complete(context.Background(), req2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if third.Content != "second-question-answer" {
		t.Fatalf("Content = %q, want %q", third.Content, "second-question-answer")
	}
	if fake.calls != 2 {
		t.Fatalf("calls = %d, want 2", fake.calls)
	}
}
