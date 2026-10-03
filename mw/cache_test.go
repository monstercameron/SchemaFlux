package mw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
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

// Axis 13: the reasoning effort. The same prompt asked at none and then at high
// must not be answered from the cache with the none answer.
func TestCacheKeyAxisReasoningEffort(t *testing.T) {
	efforts := []string{"", "none", "low", "medium", "high", "xhigh", "max", "minimal"}
	seen := map[string]string{}
	for _, effort := range efforts {
		req := baseCacheRequest()
		req.ReasoningEffort = effort
		key := cacheKeyFor(req, "openai", basePartition())
		if other, clash := seen[key]; clash {
			t.Errorf("efforts %q and %q share a cache key", other, effort)
		}
		seen[key] = effort
	}

	// Whose wish it is changes what is sent. A caller's none goes to any model
	// that takes the word; an operation's preference for none is dropped
	// everywhere but the one model it was measured on, and the request runs at
	// the default. On gpt-5.6-luna the two are different requests with
	// different answers, and one key for both served the caller's none answer
	// to a call that asked for nothing.
	for _, effort := range efforts[1:] {
		preferred, callers := baseCacheRequest(), baseCacheRequest()
		preferred.ReasoningEffort, preferred.ReasoningEffortPreferred = effort, true
		callers.ReasoningEffort = effort
		assertAxisChangesKey(t, "whose reasoning effort "+effort+" is", callers, preferred)
	}

	// A preference with no word is no preference.
	plain, flagged := baseCacheRequest(), baseCacheRequest()
	flagged.ReasoningEffortPreferred = true
	if cacheKeyFor(plain, "openai", basePartition()) != cacheKeyFor(flagged, "openai", basePartition()) {
		t.Error("the preferred flag alone, with no effort, changed the key")
	}

	base, changed := baseCacheRequest(), baseCacheRequest()
	changed.ReasoningEffort = "high"
	assertAxisChangesKey(t, "reasoning effort", base, changed)
}

// Axes 14 to 16: what else the request asks the model to do. None of the three
// was in the key, so a request with web search on was answered from the cache
// with the answer to the same prompt asked without it, and a turn of a tool
// conversation with the answer to another turn.
func TestCacheKeyAxesForSearchToolsAndMessages(t *testing.T) {
	weather := llm.Tool{Name: "weather", Description: "current weather", Parameters: map[string]any{"type": "object"}}
	cases := []struct {
		name   string
		change func(*llm.CompletionRequest)
	}{
		{"web search on", func(r *llm.CompletionRequest) { r.WebSearch = true }},
		{"one tool", func(r *llm.CompletionRequest) { r.Tools = []llm.Tool{weather} }},
		{"the tool under another name", func(r *llm.CompletionRequest) {
			r.Tools = []llm.Tool{{Name: "forecast", Description: weather.Description, Parameters: weather.Parameters}}
		}},
		{"the tool described differently", func(r *llm.CompletionRequest) {
			r.Tools = []llm.Tool{{Name: "weather", Description: "tomorrow's weather", Parameters: weather.Parameters}}
		}},
		{"the tool with other parameters", func(r *llm.CompletionRequest) {
			r.Tools = []llm.Tool{{Name: "weather", Description: weather.Description, Parameters: map[string]any{"type": "object", "required": []any{"city"}}}}
		}},
		{"two tools", func(r *llm.CompletionRequest) { r.Tools = []llm.Tool{weather, {Name: "time"}} }},
		{"the two tools in the other order", func(r *llm.CompletionRequest) { r.Tools = []llm.Tool{{Name: "time"}, weather} }},
		{"a tool choice", func(r *llm.CompletionRequest) { r.Tools = []llm.Tool{weather}; r.ToolChoice = "required" }},
		{"another tool choice", func(r *llm.CompletionRequest) { r.Tools = []llm.Tool{weather}; r.ToolChoice = "weather" }},
		{"a tool choice with no tools", func(r *llm.CompletionRequest) { r.ToolChoice = "none" }},
		{"one earlier message", func(r *llm.CompletionRequest) {
			r.Messages = []llm.Message{{Role: "user", Content: "and tomorrow?"}}
		}},
		{"the message from another role", func(r *llm.CompletionRequest) {
			r.Messages = []llm.Message{{Role: "assistant", Content: "and tomorrow?"}}
		}},
		{"a tool result", func(r *llm.CompletionRequest) {
			r.Messages = []llm.Message{{Role: "tool", Content: "18C", ToolCallID: "call_1"}}
		}},
		{"the result of another call", func(r *llm.CompletionRequest) {
			r.Messages = []llm.Message{{Role: "tool", Content: "18C", ToolCallID: "call_2"}}
		}},
		{"two messages", func(r *llm.CompletionRequest) {
			r.Messages = []llm.Message{{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"}}
		}},
		{"the two messages split differently", func(r *llm.CompletionRequest) {
			r.Messages = []llm.Message{{Role: "user", Content: "a\",\"Role\":\"assistant"}, {Role: "assistant", Content: "b"}}
		}},
		{"web search and a tool", func(r *llm.CompletionRequest) { r.WebSearch = true; r.Tools = []llm.Tool{weather} }},
	}

	seen := map[string]string{cacheKeyFor(baseCacheRequest(), "openai", basePartition()): "the plain request"}
	for _, tc := range cases {
		req := baseCacheRequest()
		tc.change(&req)
		key := cacheKeyFor(req, "openai", basePartition())
		if other, clash := seen[key]; clash {
			t.Errorf("%q shares a cache key with %q", tc.name, other)
		}
		seen[key] = tc.name

		// And the same request built twice has one key.
		again := baseCacheRequest()
		tc.change(&again)
		if cacheKeyFor(again, "openai", basePartition()) != key {
			t.Errorf("%q: the same request built twice has two keys", tc.name)
		}
	}

	// Empty and absent are the same request.
	empty := baseCacheRequest()
	empty.Tools, empty.Messages = []llm.Tool{}, []llm.Message{}
	if cacheKeyFor(empty, "openai", basePartition()) != cacheKeyFor(baseCacheRequest(), "openai", basePartition()) {
		t.Error("an empty tool list and an empty message list changed the key")
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
		cacheDigest(req.PromptCacheKey) + "\x1f" +
			cacheDigest(cacheDigest(cacheLengthPrefixed([]string{req.SchemaName, cacheCanonicalSchemaJSON(req.JSONSchema)}))) + "\x1f" +
			cacheDigest(cacheDigest(cacheLengthPrefixed([]string{req.SystemPrompt, req.UserPrompt}))) + "\x1f" +
			cacheDigest("openai") + "\x1f" +
			cacheDigest(req.Model) + "\x1f" +
			cacheDigest(cacheFloatAxis(req.Temperature)) + "\x1f" +
			cacheDigest(cacheSeedPlaceholder) + "\x1f" +
			cacheDigest("256") + "\x1f" +
			cacheDigest(req.ResponseFormat) + "\x1f" +
			cacheDigest(cacheContractLevelPlaceholder) + "\x1f" +
			cacheDigest("tenant-1") + "\x1f" +
			cacheDigest("public") + "\x1f" +
			cacheDigest(cacheDecoderVersionPlaceholder) + "\x1f" +
			"-" + "\x1f" + // axis 13: the reasoning effort, unset on the base request
			"-" + "\x1f" + // axis 14: web search, off
			"-" + "\x1f" + // axis 15: tools and tool choice, none
			"-", // axis 16: earlier messages, none
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

	// A hit says that it is one. Zero usage alone reads as spend of unknown
	// size to the accounting above this, which is what a provider that reports
	// no usage looks like, so the two have to be told apart outright.
	if first.Cached {
		t.Error("the miss, which called the provider, is marked as served from the cache")
	}
	if !second.Cached {
		t.Error("the hit is not marked as served from the cache")
	}
	// And the mark is on what is returned, not on what is stored: a third call
	// is a hit of the same stored response and reads the same.
	third, _ := wrapped.Complete(context.Background(), req)
	if !third.Cached || third.Usage != (types.TokenUsage{}) {
		t.Errorf("the second hit: Cached = %v, usage %+v", third.Cached, third.Usage)
	}
}

// A response that is not a complete answer is returned and not kept: a transient
// truncation or a filtered answer would otherwise be handed to every identical
// request for as long as the entry lived.
func TestCacheKeepsOnlyCompleteAnswers(t *testing.T) {
	toolCall := []llm.ToolCall{{ID: "c1", Name: "weather", Arguments: json.RawMessage(`{"city":"Oslo"}`)}}
	cases := []struct {
		name   string
		resp   llm.CompletionResponse
		stored bool
	}{
		{"a finished answer", llm.CompletionResponse{Content: "ok", FinishReason: "stop"}, true},
		{"an answer with no finish reason", llm.CompletionResponse{Content: "ok"}, true},
		{"cut off at the limit (length)", llm.CompletionResponse{Content: "par", FinishReason: "length"}, false},
		{"cut off at the limit (max_tokens)", llm.CompletionResponse{Content: "par", FinishReason: "max_tokens"}, false},
		{"cut off at the limit (max_output_tokens)", llm.CompletionResponse{Content: "par", FinishReason: "max_output_tokens"}, false},
		{"incomplete", llm.CompletionResponse{Content: "par", FinishReason: "incomplete"}, false},
		{"truncated", llm.CompletionResponse{Content: "par", FinishReason: "truncated"}, false},
		{"truncated, in capitals", llm.CompletionResponse{Content: "par", FinishReason: "LENGTH"}, false},
		{"withheld by a content filter", llm.CompletionResponse{Content: "x", FinishReason: "content_filter"}, false},
		{"empty", llm.CompletionResponse{FinishReason: "stop"}, false},
		{"blank", llm.CompletionResponse{Content: "  \n", FinishReason: "stop"}, false},
		{"a tool call and no content", llm.CompletionResponse{FinishReason: "tool_calls", ToolCalls: toolCall}, true},
		{"a tool call cut off at the limit", llm.CompletionResponse{FinishReason: "length", ToolCalls: toolCall}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeProvider{responses: []fakeResponse{{resp: tc.resp}}}
			wrapped := Cache()(fake)
			req := llm.CompletionRequest{Model: "m"}
			for i := 0; i < 2; i++ {
				if _, err := wrapped.Complete(context.Background(), req); err != nil {
					t.Fatalf("call %d: %v", i+1, err)
				}
			}
			want := 2
			if tc.stored {
				want = 1
			}
			if fake.calls != want {
				t.Fatalf("provider calls = %d, want %d", fake.calls, want)
			}
		})
	}
}

// A store a caller supplied may hold anything. A stored answer that is not a
// complete one (cut off at the token limit, withheld, empty, or typed answers
// the questions did not ask for) is a miss, as it would not have been written in
// the first place: serving it as a hit would hand a truncation to every caller
// until the entry expired. The call goes to the provider, and its complete answer
// replaces the entry.
func TestCacheTreatsAStoredAnswerThatIsNotCompleteAsAMiss(t *testing.T) {
	question := &llm.Decision{Questions: []llm.DecisionQuestion{{Kind: llm.DecisionChoice, Ask: "which", Options: []string{"red", "green"}}}}
	cases := []struct {
		name   string
		stored llm.CompletionResponse
		req    llm.CompletionRequest
	}{
		{"cut off at the token limit", llm.CompletionResponse{Content: "partial", FinishReason: "length"}, llm.CompletionRequest{Model: "m"}},
		{"withheld by a content filter", llm.CompletionResponse{Content: "partial", FinishReason: "content_filter"}, llm.CompletionRequest{Model: "m"}},
		{"empty", llm.CompletionResponse{}, llm.CompletionRequest{Model: "m"}},
		{"empty and cut off", llm.CompletionResponse{FinishReason: "length"}, llm.CompletionRequest{Model: "m"}},
		{"a label the question never offered", llm.CompletionResponse{DecisionAnswers: []llm.DecisionAnswer{{Kind: llm.DecisionChoice, Label: "purple", ModelConfidence: 0.9, ConfidenceReported: true}}}, llm.CompletionRequest{Model: "m", Decision: question}},
		{"typed answers of the wrong count", llm.CompletionResponse{DecisionAnswers: []llm.DecisionAnswer{{Kind: llm.DecisionChoice, Label: "red"}, {Kind: llm.DecisionChoice, Label: "red"}}}, llm.CompletionRequest{Model: "m", Decision: question}},
		{"typed answers of the wrong kind", llm.CompletionResponse{DecisionAnswers: []llm.DecisionAnswer{{Kind: llm.DecisionYesNo, Yes: true}}}, llm.CompletionRequest{Model: "m", Decision: question}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := cacheNewMemoryStore(nil)
			complete := llm.CompletionResponse{Content: "fresh"}
			if tc.req.Decision != nil {
				complete = llm.CompletionResponse{DecisionAnswers: []llm.DecisionAnswer{{Kind: llm.DecisionChoice, Label: "green", ModelConfidence: 0.9, ConfidenceReported: true}}}
			}
			fake := &fakeProvider{responses: []fakeResponse{{resp: complete}}}
			wrapped := Cache(WithCacheStore(store))(fake)
			key := cacheKeyFor(tc.req, fake.Name(), CachePartition{})
			store.Set(key, tc.stored, 0)

			resp, err := wrapped.Complete(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.Cached || fake.calls != 1 {
				t.Errorf("Cached = %v, provider calls = %d, want a miss and one call", resp.Cached, fake.calls)
			}
			if kept, _ := store.Get(key); !reflect.DeepEqual(kept, complete) {
				t.Errorf("the store holds %+v, want the complete answer in place of the one that was not", kept)
			}
			if again, err := wrapped.Complete(context.Background(), tc.req); err != nil || !again.Cached || fake.calls != 1 {
				t.Errorf("the second call: cached %v, err %v, provider calls %d, want a hit", again.Cached, err, fake.calls)
			}
		})
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

type coalescedResult struct {
	resp llm.CompletionResponse
	err  error
}

// runCoalescedCalls makes n identical calls at once through a cache whose
// provider answers with outcome. The provider is held until n-1 of the calls
// are waiting on the one in flight, so the calls provably overlapped: without
// that, a call that happened to start after the first had finished would be
// served from the store and the test would pass with coalescing deleted. It
// returns how many times the provider was called, what each caller got, and how
// many calls the cache reported as coalesced.
func runCoalescedCalls(t *testing.T, n int, outcome func() (llm.CompletionResponse, error)) (providerCalls int, results []coalescedResult, coalesced int) {
	t.Helper()
	var calls, waiting, coalescedEvents atomic.Int32
	release := make(chan struct{})
	allWaiting := make(chan struct{})
	provider := completeFunc(func(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
		calls.Add(1)
		<-release
		return outcome()
	})
	wrapped := Cache(
		cacheWithWaitHook(func() {
			if waiting.Add(1) == int32(n-1) {
				close(allWaiting)
			}
		}),
		WithCacheStats(func(evt CacheEvent) {
			if evt.Coalesced {
				coalescedEvents.Add(1)
			}
		}),
	)(provider)

	results = make([]coalescedResult, n)
	var done sync.WaitGroup
	for i := 0; i < n; i++ {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			resp, err := wrapped.Complete(context.Background(), llm.CompletionRequest{Model: "m", UserPrompt: "same"})
			results[i] = coalescedResult{resp: resp, err: err}
		}(i)
	}
	select {
	case <-allWaiting:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatalf("%d of %d callers were waiting on the call in flight; the rest did not coalesce", waiting.Load(), n-1)
	}
	close(release)
	done.Wait()
	return int(calls.Load()), results, int(coalescedEvents.Load())
}

// Concurrent identical calls must coalesce onto a single provider call --
// "exact-duplicate calls cost zero" has to hold for calls that arrive at the
// same instant too, not only for calls that arrive after a previous one
// finished and got stored.
func TestCacheConcurrentIdenticalCallsCoalesceToOneProviderCall(t *testing.T) {
	const n = 8
	calls, results, coalesced := runCoalescedCalls(t, n, func() (llm.CompletionResponse, error) {
		return llm.CompletionResponse{Content: "ok"}, nil
	})
	if calls != 1 {
		t.Fatalf("provider called %d times for %d concurrent identical requests, want 1", calls, n)
	}
	if coalesced != n-1 {
		t.Fatalf("%d calls reported as coalesced, want %d", coalesced, n-1)
	}
	for i, result := range results {
		if result.err != nil || result.resp.Content != "ok" {
			t.Fatalf("goroutine %d: Content = %q, err = %v", i, result.resp.Content, result.err)
		}
	}
}

// A follower coalesced onto a leader that failed shares the leader's error --
// it is not silently retried into a second provider call, and it is not handed
// a fabricated success.
func TestCacheCoalescedFollowerSharesLeaderError(t *testing.T) {
	wantErr := errors.New("boom")
	const n = 4
	calls, results, coalesced := runCoalescedCalls(t, n, func() (llm.CompletionResponse, error) {
		return llm.CompletionResponse{}, wantErr
	})
	if calls != 1 {
		t.Fatalf("provider called %d times for %d concurrent identical requests, want 1", calls, n)
	}
	if coalesced != n-1 {
		t.Fatalf("%d calls reported as coalesced, want %d", coalesced, n-1)
	}
	for i, result := range results {
		if !errors.Is(result.err, wantErr) {
			t.Fatalf("goroutine %d: error = %v, want %v", i, result.err, wantErr)
		}
		if result.resp.Content != "" {
			t.Fatalf("goroutine %d: content %q came back with the failure", i, result.resp.Content)
		}
	}
}

// waitOrFail receives from ch, or fails the test if nothing arrives.
func waitOrFail(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// A follower whose own context is live must not receive the cancellation of the
// call it was waiting on: that is the leader's context ending, not an answer.
func TestCacheFollowerIsNotHandedTheLeadersCancellation(t *testing.T) {
	entered := make(chan struct{})
	waiting := make(chan struct{})
	var calls atomic.Int32
	var once sync.Once
	provider := completeFunc(func(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-ctx.Done()
			return llm.CompletionResponse{}, ctx.Err()
		}
		return llm.CompletionResponse{Content: "answered by the second leader"}, nil
	})
	wrapped := Cache(cacheWithWaitHook(func() { once.Do(func() { close(waiting) }) }))(provider)
	req := llm.CompletionRequest{Model: "m", UserPrompt: "same"}

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderErr := make(chan error, 1)
	go func() {
		_, err := wrapped.Complete(leaderCtx, req)
		leaderErr <- err
	}()
	waitOrFail(t, entered, "the leader to reach the provider")

	followerDone := make(chan coalescedResult, 1)
	go func() {
		resp, err := wrapped.Complete(context.Background(), req)
		followerDone <- coalescedResult{resp: resp, err: err}
	}()
	waitOrFail(t, waiting, "the follower to wait on the leader")

	cancelLeader()
	if err := <-leaderErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader err = %v, want its own cancellation", err)
	}
	select {
	case got := <-followerDone:
		if got.err != nil || got.resp.Content != "answered by the second leader" {
			t.Fatalf("follower got %q, err %v; want it to take the call up itself", got.resp.Content, got.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the follower never returned")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("provider calls = %d, want 2 (the cancelled leader and the follower that took over)", got)
	}
}

// A follower woken by an abandoned call whose own context is over has nothing
// to ask again for: it returns its own error and does not become the next
// leader, whose provider call would be made on a context that is dead. The
// follower's select sees the leader's completion and its own context at once, so
// either branch may be the one taken; the situation is repeated until the
// branch that would go wrong has been taken with near certainty (each round
// takes it with probability one half: 80 rounds miss it one time in 2^80).
func TestCacheFollowerWithADeadContextDoesNotBecomeTheNextLeader(t *testing.T) {
	for round := 0; round < 80; round++ {
		var calls atomic.Int32
		entered := make(chan struct{})
		provider := completeFunc(func(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
			if calls.Add(1) == 1 {
				close(entered)
				<-ctx.Done()
				return llm.CompletionResponse{}, ctx.Err()
			}
			return llm.CompletionResponse{Content: "a call made on a dead context"}, nil
		})
		leaderCtx, cancelLeader := context.WithCancel(context.Background())
		followerCtx, cancelFollower := context.WithCancel(context.Background())
		leaderErr := make(chan error, 1)
		var once sync.Once
		// The wait hook runs on the follower just before it waits: it ends both
		// contexts and lets the leader finish, so that when the follower selects,
		// the leader's completion and its own context are both ready.
		wrapped := Cache(cacheWithWaitHook(func() {
			once.Do(func() {
				cancelFollower()
				cancelLeader()
				<-leaderErr
			})
		}))(provider)
		req := llm.CompletionRequest{Model: "m", UserPrompt: "same"}

		go func() {
			_, err := wrapped.Complete(leaderCtx, req)
			leaderErr <- err
		}()
		waitOrFail(t, entered, "the leader to reach the provider")

		_, err := wrapped.Complete(followerCtx, req)
		cancelLeader()
		cancelFollower()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("round %d: follower err = %v, want its own context's error", round, err)
		}
		if got := calls.Load(); got != 1 {
			t.Fatalf("round %d: provider calls = %d, want 1: the follower took the call up on a dead context", round, got)
		}
	}
}

// countingStore counts how often it is read and written, and keeps what it is
// given.
type countingStore struct {
	mu        sync.Mutex
	gets, set int
	held      map[string]llm.CompletionResponse
}

func (s *countingStore) Get(key string) (llm.CompletionResponse, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gets++
	resp, ok := s.held[key]
	return resp, ok
}

func (s *countingStore) Set(key string, resp llm.CompletionResponse, _ time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.set++
	if s.held == nil {
		s.held = map[string]llm.CompletionResponse{}
	}
	s.held[key] = resp
}

// A miss costs a store behind a network one read and one write. It was two reads:
// one before the claim for the key and one after, to catch an answer stored in
// between.
func TestCacheReadsTheStoreOnceForAMissAndWritesItOnce(t *testing.T) {
	var calls atomic.Int32
	provider := completeFunc(func(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
		calls.Add(1)
		return llm.CompletionResponse{Content: "paid for"}, nil
	})
	store := &countingStore{}
	wrapped := Cache(WithCacheStore(store))(provider)
	req := llm.CompletionRequest{Model: "m", UserPrompt: "q"}

	if resp, err := wrapped.Complete(context.Background(), req); err != nil || resp.Cached {
		t.Fatalf("first call: cached %v, err %v", resp.Cached, err)
	}
	if store.gets != 1 || store.set != 1 {
		t.Errorf("a miss read the store %d times and wrote it %d, want 1 and 1", store.gets, store.set)
	}
	if resp, err := wrapped.Complete(context.Background(), req); err != nil || !resp.Cached {
		t.Fatalf("second call: cached %v, err %v", resp.Cached, err)
	}
	if store.gets != 2 || store.set != 1 || calls.Load() != 1 {
		t.Errorf("a hit read the store %d times in all, wrote it %d, and called the provider %d times, want 2, 1 and 1", store.gets, store.set, calls.Load())
	}
}

// A provider failure the leader's context did not cause is still shared, even
// when the follower retries on cancellation: a real failure must not be
// retried by every follower.
func TestCacheFollowerStillSharesAFailureThatIsNotACancellation(t *testing.T) {
	wantErr := llm.NewAPIError("p", "m", 503, "")
	calls, results, _ := runCoalescedCalls(t, 3, func() (llm.CompletionResponse, error) {
		return llm.CompletionResponse{}, wantErr
	})
	if calls != 1 {
		t.Fatalf("provider calls = %d, want 1", calls)
	}
	for i, result := range results {
		if !errors.Is(result.err, wantErr) {
			t.Fatalf("caller %d: err = %v", i, result.err)
		}
	}
}

// A follower whose own context ends while it waits returns its own error and
// leaves the leader alone.
func TestCacheFollowerReturnsItsOwnContextErrorWhileWaiting(t *testing.T) {
	entered := make(chan struct{})
	waiting := make(chan struct{})
	release := make(chan struct{})
	provider := completeFunc(func(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
		close(entered)
		<-release
		return llm.CompletionResponse{Content: "ok"}, nil
	})
	wrapped := Cache(cacheWithWaitHook(func() { close(waiting) }))(provider)
	req := llm.CompletionRequest{Model: "m", UserPrompt: "same"}

	leaderDone := make(chan coalescedResult, 1)
	go func() {
		resp, err := wrapped.Complete(context.Background(), req)
		leaderDone <- coalescedResult{resp: resp, err: err}
	}()
	waitOrFail(t, entered, "the leader to reach the provider")

	followerCtx, cancelFollower := context.WithCancel(context.Background())
	followerErr := make(chan error, 1)
	go func() {
		_, err := wrapped.Complete(followerCtx, req)
		followerErr <- err
	}()
	waitOrFail(t, waiting, "the follower to wait on the leader")
	cancelFollower()
	if err := <-followerErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("follower err = %v, want its own cancellation", err)
	}
	close(release)
	if got := <-leaderDone; got.err != nil || got.resp.Content != "ok" {
		t.Fatalf("leader got %q, err %v", got.resp.Content, got.err)
	}
}

// A provider that panics must not leave its key claimed: the followers get an
// error, and the next caller is not made to wait on a call that will never end.
func TestCacheProviderPanicFreesTheKeyAndFailsFollowers(t *testing.T) {
	entered := make(chan struct{})
	waiting := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	provider := completeFunc(func(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
			panic("provider bug with the caller's text in it")
		}
		return llm.CompletionResponse{Content: "ok"}, nil
	})
	wrapped := Cache(cacheWithWaitHook(func() { close(waiting) }))(provider)
	req := llm.CompletionRequest{Model: "m", UserPrompt: "same"}

	leaderPanic := make(chan any, 1)
	go func() {
		defer func() { leaderPanic <- recover() }()
		wrapped.Complete(context.Background(), req)
	}()
	waitOrFail(t, entered, "the leader to reach the provider")

	followerDone := make(chan coalescedResult, 1)
	go func() {
		resp, err := wrapped.Complete(context.Background(), req)
		followerDone <- coalescedResult{resp: resp, err: err}
	}()
	waitOrFail(t, waiting, "the follower to wait on the leader")
	close(release)

	if got := <-leaderPanic; got == nil {
		t.Fatal("the leader's panic was swallowed")
	}
	select {
	case got := <-followerDone:
		if types.KindOf(got.err) != types.KindInvariantViolation {
			t.Fatalf("follower err = %v, want an invariant violation", got.err)
		}
		if strings.Contains(got.err.Error(), "caller's text") {
			t.Fatalf("the panic's value is in the error: %v", got.err)
		}
		// A recovered panic is marked as one, for a boundary that tells it from the
		// other invariant violations; the text is what it was.
		if types.NarrowingOf(got.err) != types.NarrowPanic {
			t.Errorf("follower err narrows to %v, want a panic", types.NarrowingOf(got.err))
		}
		if want := "mw.Cache: invariant violation: the identical request this call was waiting on panicked"; got.err.Error() != want {
			t.Errorf("follower err reads %q, want %q", got.err.Error(), want)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the follower is still waiting on a call that panicked")
	}

	next := make(chan coalescedResult, 1)
	go func() {
		resp, err := wrapped.Complete(context.Background(), req)
		next <- coalescedResult{resp: resp, err: err}
	}()
	select {
	case got := <-next:
		if got.err != nil || got.resp.Content != "ok" {
			t.Fatalf("the call after the panic got %q, err %v", got.resp.Content, got.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a call after the panic is waiting on the key the panic left claimed")
	}
}

// A hit shares nothing with the stored response, and neither does the response
// the leader returned: editing a tool call in place changes nobody else's.
func TestCacheSharesNoMemoryBetweenResponses(t *testing.T) {
	provider := completeFunc(func(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
		return llm.CompletionResponse{
			FinishReason: "tool_calls",
			ToolCalls:    []llm.ToolCall{{ID: "c1", Name: "weather", Arguments: json.RawMessage(`{"city":"Oslo"}`)}},
		}, nil
	})
	wrapped := Cache()(provider)
	req := llm.CompletionRequest{Model: "m", UserPrompt: "same"}

	mutate := func(resp llm.CompletionResponse) {
		resp.ToolCalls[0].Arguments[2] = 'X'
		resp.ToolCalls[0].Name = "mutated"
		resp.ToolCalls[0] = llm.ToolCall{ID: "mutated", Arguments: json.RawMessage(`{}`)}
	}
	check := func(label string, resp llm.CompletionResponse) {
		t.Helper()
		call := resp.ToolCalls[0]
		if call.ID != "c1" || call.Name != "weather" || string(call.Arguments) != `{"city":"Oslo"}` {
			t.Fatalf("%s: tool call = %+v %s, want the original", label, call, call.Arguments)
		}
	}

	leader, err := wrapped.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("leader: %v", err)
	}
	mutate(leader)
	hit, err := wrapped.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("first hit: %v", err)
	}
	check("the first hit, after the leader's response was edited", hit)
	mutate(hit)
	again, err := wrapped.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("second hit: %v", err)
	}
	check("the second hit, after the first hit was edited", again)
}

func TestCacheCloneResponse(t *testing.T) {
	args := func(s string) json.RawMessage { return json.RawMessage(s) }
	cases := []struct {
		name string
		resp llm.CompletionResponse
	}{
		{"no tool calls", llm.CompletionResponse{Content: "x"}},
		{"nil tool calls", llm.CompletionResponse{ToolCalls: nil}},
		{"empty tool calls", llm.CompletionResponse{ToolCalls: []llm.ToolCall{}}},
		{"one call", llm.CompletionResponse{ToolCalls: []llm.ToolCall{{ID: "1", Name: "a", Arguments: args(`{"k":1}`)}}}},
		{"two calls", llm.CompletionResponse{ToolCalls: []llm.ToolCall{{ID: "1", Name: "a", Arguments: args(`{}`)}, {ID: "2", Name: "b", Arguments: args(`[1]`)}}}},
		{"a call with nil arguments", llm.CompletionResponse{ToolCalls: []llm.ToolCall{{ID: "1", Name: "a"}}}},
		{"a call with empty arguments", llm.CompletionResponse{ToolCalls: []llm.ToolCall{{ID: "1", Name: "a", Arguments: args("")}}}},
		{"content and a call", llm.CompletionResponse{Content: "x", ToolCalls: []llm.ToolCall{{ID: "1", Name: "a", Arguments: args(`{}`)}}}},
		{"usage is carried", llm.CompletionResponse{Usage: types.TokenUsage{TotalTokens: 5}, ToolCalls: []llm.ToolCall{{ID: "1"}}}},
		{"a long argument", llm.CompletionResponse{ToolCalls: []llm.ToolCall{{ID: "1", Arguments: args(strings.Repeat("a", 4096))}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clone := cacheCloneResponse(tc.resp)
			if !reflect.DeepEqual(clone, tc.resp) {
				t.Fatalf("clone = %+v, want %+v", clone, tc.resp)
			}
			for i := range tc.resp.ToolCalls {
				if len(tc.resp.ToolCalls[i].Arguments) > 0 && &clone.ToolCalls[i].Arguments[0] == &tc.resp.ToolCalls[i].Arguments[0] {
					t.Fatalf("call %d: the clone shares its argument bytes", i)
				}
			}
			if len(tc.resp.ToolCalls) > 0 && &clone.ToolCalls[0] == &tc.resp.ToolCalls[0] {
				t.Fatal("the clone shares the slice of calls")
			}
		})
	}
}

// Two requests that differ must not produce one key because a value holds the
// separator that joins the axes.
func TestCacheKeyAxesCannotBleedIntoEachOther(t *testing.T) {
	base := baseCacheRequest()
	with := func(edit func(*llm.CompletionRequest)) llm.CompletionRequest {
		req := base
		edit(&req)
		return req
	}
	cases := []struct {
		name   string
		a, b   llm.CompletionRequest
		pa, pb CachePartition
	}{
		{name: "tenant and data policy", a: base, b: base, pa: CachePartition{Tenant: "a\x1fb", DataPolicy: "c"}, pb: CachePartition{Tenant: "a", DataPolicy: "b\x1fc"}},
		{name: "tenant and data policy, the separator moved", a: base, b: base, pa: CachePartition{Tenant: "a\x1f", DataPolicy: "b"}, pb: CachePartition{Tenant: "a", DataPolicy: "\x1fb"}},
		{name: "system and user prompt", a: with(func(r *llm.CompletionRequest) { r.SystemPrompt, r.UserPrompt = "a\x1fb", "c" }), b: with(func(r *llm.CompletionRequest) { r.SystemPrompt, r.UserPrompt = "a", "b\x1fc" })},
		{name: "system and user prompt, text moved across", a: with(func(r *llm.CompletionRequest) { r.SystemPrompt, r.UserPrompt = "ab", "" }), b: with(func(r *llm.CompletionRequest) { r.SystemPrompt, r.UserPrompt = "a", "b" })},
		{name: "schema name and schema", a: with(func(r *llm.CompletionRequest) { r.SchemaName, r.JSONSchema = "a\x1f{}", nil }), b: with(func(r *llm.CompletionRequest) { r.SchemaName, r.JSONSchema = "a", map[string]any{} })},
		{name: "model and response format", a: with(func(r *llm.CompletionRequest) { r.Model, r.ResponseFormat = "m\x1fjson", "" }), b: with(func(r *llm.CompletionRequest) { r.Model, r.ResponseFormat = "m", "json" })},
		{name: "prompt cache key and schema name", a: with(func(r *llm.CompletionRequest) { r.PromptCacheKey, r.SchemaName = "k\x1fv", "" }), b: with(func(r *llm.CompletionRequest) { r.PromptCacheKey, r.SchemaName = "k", "v" })},
		{name: "model and effort", a: with(func(r *llm.CompletionRequest) { r.Model, r.ReasoningEffort = "m\x1fhigh", "" }), b: with(func(r *llm.CompletionRequest) { r.Model, r.ReasoningEffort = "m", "high" })},
		{name: "tenant against model", a: base, b: with(func(r *llm.CompletionRequest) { r.Model = "gpt-test\x1ftenant-1" }), pa: CachePartition{Tenant: "tenant-1"}, pb: CachePartition{}},
		{name: "a blank tenant against one named dash", a: base, b: base, pa: CachePartition{Tenant: ""}, pb: CachePartition{Tenant: "-"}},
		{name: "a blank tenant against whitespace", a: base, b: base, pa: CachePartition{Tenant: ""}, pb: CachePartition{Tenant: " "}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if a, b := cacheKeyFor(tc.a, "openai", tc.pa), cacheKeyFor(tc.b, "openai", tc.pb); a == b {
				t.Fatalf("both requests produce the key %s", a)
			}
		})
	}
}

// Two clients that both allow their answers to be kept but permit different
// regions are different requesters: neither is served what the other's call
// stored.
func TestCacheSeparatesClientsByTheDataPolicyTheyDeclared(t *testing.T) {
	policy := func(regions ...string) context.Context {
		return ops.WithExecConfig(context.Background(), &ops.ExecConfig{
			DataPolicy:         types.DataPolicy{AllowResultCaching: true, AllowedRegions: regions},
			DataPolicyDeclared: true,
		})
	}
	fake := &fakeProvider{responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "ok"}}}}
	wrapped := Cache()(fake)
	req := llm.CompletionRequest{Model: "m", UserPrompt: "same"}

	for _, ctx := range []context.Context{policy("eu"), policy("us"), policy("eu"), policy("us"), policy("eu", "us")} {
		if _, err := wrapped.Complete(ctx, req); err != nil {
			t.Fatalf("Complete: %v", err)
		}
	}
	if fake.calls != 3 {
		t.Fatalf("provider calls = %d, want 3: one for each distinct policy, and the repeats served from the cache", fake.calls)
	}
}

func TestCacheDataPolicyKey(t *testing.T) {
	policies := []types.DataPolicy{
		{},
		{AllowResultCaching: true},
		{AllowResultCaching: true, Classification: "pii"},
		{AllowResultCaching: true, Classification: "public"},
		{AllowResultCaching: true, AllowedRegions: []string{"eu"}},
		{AllowResultCaching: true, AllowedRegions: []string{"us"}},
		{AllowResultCaching: true, AllowedRegions: []string{"eu", "us"}},
		{AllowResultCaching: true, AllowedProviders: []string{"openai"}},
		{AllowResultCaching: true, AllowedProviders: []string{"openai"}, AllowedRegions: []string{"eu"}},
		{AllowResultCaching: true, AllowTrainingUse: true},
		{AllowResultCaching: true, MaxRetentionDays: 30},
		{AllowResultCaching: true, ContentLogging: 1},
		{AllowResultCaching: true, MinimumContract: 1},
	}
	seen := map[string]int{}
	for i, policy := range policies {
		key := cacheDataPolicyKey("base", policy)
		if other, clash := seen[key]; clash {
			t.Errorf("policies %d and %d share a key", other, i)
		}
		seen[key] = i
		if cacheDataPolicyKey("base", policy) != key {
			t.Errorf("policy %d: the key is not stable", i)
		}
		if cacheDataPolicyKey("other", policy) == key {
			t.Errorf("policy %d: the base key is not part of the result", i)
		}
	}
}

// A stats callback is the caller's, and a panic in it does not take the call
// down or change its answer.
func TestCacheSurvivesAStatsCallbackThatPanics(t *testing.T) {
	fake := &fakeProvider{responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "ok"}}}}
	wrapped := Cache(WithCacheStats(func(CacheEvent) { panic("callback bug") }))(fake)
	req := llm.CompletionRequest{Model: "m", UserPrompt: "q"}

	for i := 0; i < 2; i++ {
		resp, err := wrapped.Complete(context.Background(), req)
		if err != nil || resp.Content != "ok" {
			t.Fatalf("call %d: %q, %v", i+1, resp.Content, err)
		}
	}
	if fake.calls != 1 {
		t.Fatalf("provider calls = %d, want 1: the miss was stored although its callback panicked", fake.calls)
	}
}

type cacheStoreStep struct {
	key     string
	ttl     time.Duration
	advance time.Duration
}

func TestCacheMemoryStoreSetBoundsAndExpiresEntries(t *testing.T) {
	minute := time.Minute
	cases := []struct {
		name  string
		max   int
		steps []cacheStoreStep
		want  []string
	}{
		{"under the bound everything is kept", 3, []cacheStoreStep{{key: "a"}, {key: "b"}}, []string{"a", "b"}},
		{"at the bound nothing is lost", 3, []cacheStoreStep{{key: "a"}, {key: "b"}, {key: "c"}}, []string{"a", "b", "c"}},
		{"one over drops the oldest", 3, []cacheStoreStep{{key: "a"}, {key: "b"}, {key: "c"}, {key: "d"}}, []string{"b", "c", "d"}},
		{"two over drops the two oldest", 3, []cacheStoreStep{{key: "a"}, {key: "b"}, {key: "c"}, {key: "d"}, {key: "e"}}, []string{"c", "d", "e"}},
		{"replacing a key at the bound drops nothing", 3, []cacheStoreStep{{key: "a"}, {key: "b"}, {key: "c"}, {key: "b"}}, []string{"a", "b", "c"}},
		{"a replaced key is the newest", 3, []cacheStoreStep{{key: "a"}, {key: "b"}, {key: "c"}, {key: "a"}, {key: "d"}}, []string{"a", "c", "d"}},
		{"a bound of one keeps the last", 1, []cacheStoreStep{{key: "a"}, {key: "b"}, {key: "c"}}, []string{"c"}},
		{"an expired entry goes before a live one is dropped", 2, []cacheStoreStep{{key: "a", ttl: minute}, {key: "b"}, {key: "c", advance: 2 * minute}}, []string{"b", "c"}},
		{"every expired entry goes before an older live one is dropped", 3, []cacheStoreStep{{key: "live-and-oldest"}, {key: "short-1", ttl: time.Second}, {key: "short-2", ttl: time.Second}, {key: "new", advance: minute}}, []string{"live-and-oldest", "new"}},
		{"an entry with no ttl never expires", 3, []cacheStoreStep{{key: "a"}, {key: "b", advance: 1000 * 24 * time.Hour}, {key: "c"}}, []string{"a", "b", "c"}},
		{"an entry within its ttl is kept", 2, []cacheStoreStep{{key: "a", ttl: minute}, {key: "b", advance: 30 * time.Second}, {key: "c"}}, []string{"b", "c"}},
		{"a long run stays at the bound", 5, func() []cacheStoreStep {
			var steps []cacheStoreStep
			for i := 0; i < 100; i++ {
				steps = append(steps, cacheStoreStep{key: fmt.Sprintf("k%03d", i)})
			}
			return steps
		}(), []string{"k095", "k096", "k097", "k098", "k099"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := newFakeClock(time.Unix(0, 0))
			store := cacheNewMemoryStore(fc.Now)
			store.maxEntries = tc.max
			for _, step := range tc.steps {
				fc.sleep(context.Background(), step.advance)
				store.Set(step.key, llm.CompletionResponse{Content: step.key}, step.ttl)
			}
			var got []string
			for key := range store.entries {
				got = append(got, key)
			}
			sort.Strings(got)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("entries = %v, want %v", got, tc.want)
			}
		})
	}
}

// Expired entries are removed by Set without anything reading them.
func TestCacheMemoryStoreSweepsExpiredEntriesWithoutAReadOfThem(t *testing.T) {
	fc := newFakeClock(time.Unix(0, 0))
	store := cacheNewMemoryStore(fc.Now)
	for i := 0; i < 100; i++ {
		store.Set(fmt.Sprintf("old%d", i), llm.CompletionResponse{Content: "x"}, time.Minute)
	}
	fc.sleep(context.Background(), 2*time.Minute)
	for i := 0; len(store.entries) > 50 && i < 2*cacheSweepEvery; i++ {
		store.Set(fmt.Sprintf("new%d", i), llm.CompletionResponse{Content: "x"}, 0)
	}
	for key := range store.entries {
		if strings.HasPrefix(key, "old") {
			t.Fatalf("%s is past its ttl and was never read, and a sweep inside Set did not remove it", key)
		}
	}
}

func TestCacheDefaultStoreIsBounded(t *testing.T) {
	store := cacheNewMemoryStore(nil)
	if store.maxEntries != cacheDefaultMaxEntries {
		t.Fatalf("maxEntries = %d, want %d", store.maxEntries, cacheDefaultMaxEntries)
	}
	for i := 0; i < 2*cacheDefaultMaxEntries; i++ {
		store.Set(fmt.Sprintf("k%d", i), llm.CompletionResponse{Content: "x"}, 0)
	}
	if got := len(store.entries); got != cacheDefaultMaxEntries {
		t.Fatalf("the default store holds %d entries after %d sets, want %d", got, 2*cacheDefaultMaxEntries, cacheDefaultMaxEntries)
	}
	if _, ok := store.Get(fmt.Sprintf("k%d", 2*cacheDefaultMaxEntries-1)); !ok {
		t.Fatal("the newest entry was dropped")
	}
	if _, ok := store.Get("k0"); ok {
		t.Fatal("the oldest entry was kept past the bound")
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

// measuredTiming is the timing of a call that was made: 800 ms of network, the
// provider's wait, a connection that was paid for.
func measuredTiming() types.Timing {
	return types.Timing{
		Calls: 1, Lookup: 5 * time.Millisecond, Connect: 20 * time.Millisecond, Handshake: 40 * time.Millisecond,
		Send: time.Millisecond, Wait: 700 * time.Millisecond, Read: 30 * time.Millisecond, Network: 800 * time.Millisecond,
	}
}

// A hit is answered from memory in microseconds. It replayed the timing of the
// call that filled the cache, so a hit in 20 microseconds reported 800 ms on
// the network, the original wait, and a measured call.
func TestACacheHitMeasuredNothing(t *testing.T) {
	stored := llm.CompletionResponse{
		Content: "answer", Model: "m", Provider: "fake", FinishReason: "stop",
		Usage:  types.TokenUsage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
		Timing: measuredTiming(),
	}
	cases := []struct {
		name   string
		mutate func(*llm.CompletionResponse)
	}{
		{"as stored", func(*llm.CompletionResponse) {}},
		{"a reused connection", func(r *llm.CompletionResponse) { r.Timing.Reused = true }},
		{"a queue wait", func(r *llm.CompletionResponse) { r.Timing.Queued = time.Second }},
		{"no timing at all", func(r *llm.CompletionResponse) { r.Timing = types.Timing{} }},
		{"a sum of calls", func(r *llm.CompletionResponse) { r.Timing = r.Timing.Add(r.Timing) }},
		{"only a network figure", func(r *llm.CompletionResponse) { r.Timing = types.Timing{Network: time.Hour} }},
		{"a tier", func(r *llm.CompletionResponse) { r.ServiceTier = "flex" }},
		{"a truncated answer", func(r *llm.CompletionResponse) { r.FinishReason = "length" }},
		{"already marked cached", func(r *llm.CompletionResponse) { r.Cached = true }},
		{"tool calls", func(r *llm.CompletionResponse) { r.ToolCalls = []llm.ToolCall{{Name: "t"}} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := stored
			tc.mutate(&in)
			before := in
			hit := cacheHitResponse(in)
			if hit.Timing != (types.Timing{}) || hit.Timing.Measured() {
				t.Errorf("a hit carries timing %+v", hit.Timing)
			}
			if !hit.Cached || hit.Usage != (types.TokenUsage{}) {
				t.Errorf("a hit is cached = %v with usage %+v", hit.Cached, hit.Usage)
			}
			if hit.Content != in.Content || hit.FinishReason != in.FinishReason || hit.Model != in.Model {
				t.Errorf("the hit's content, finish reason or model changed: %+v", hit)
			}
			if in.Timing != before.Timing {
				t.Error("the stored response was written to")
			}
		})
	}
}

// The same through the middleware: the call that made the request keeps what
// was measured, and a hit and a follower that shared its answer measured
// nothing.
func TestOnlyTheCallThatWasMadeIsTimed(t *testing.T) {
	t.Run("a miss, then hits", func(t *testing.T) {
		provider := &fakeProvider{name: "fake", responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "answer", Timing: measuredTiming()}}}}
		chain := Chain(provider, Cache())
		request := llm.CompletionRequest{Model: "m", UserPrompt: "u"}

		first, err := chain.Complete(context.Background(), request)
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if first.Timing != measuredTiming() || first.Cached {
			t.Errorf("the call that was made carries timing %+v (cached %v)", first.Timing, first.Cached)
		}
		for range 3 {
			hit, err := chain.Complete(context.Background(), request)
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if !hit.Cached || hit.Timing.Measured() || hit.Timing != (types.Timing{}) {
				t.Errorf("a hit carries timing %+v (cached %v)", hit.Timing, hit.Cached)
			}
		}
		if provider.calls != 1 {
			t.Errorf("%d provider calls", provider.calls)
		}
	})

	t.Run("followers that share a leader's answer", func(t *testing.T) {
		release := make(chan struct{})
		provider := newScript(func(ctx context.Context) (llm.CompletionResponse, error) {
			<-release
			return llm.CompletionResponse{Content: "answer", Timing: measuredTiming()}, nil
		})
		const callers = 6
		var waiting atomic.Int32
		allWaiting := make(chan struct{})
		chain := Chain(provider, Cache(cacheWithWaitHook(func() {
			if waiting.Add(1) == callers-1 {
				close(allWaiting)
			}
		})))
		request := llm.CompletionRequest{Model: "m", UserPrompt: "u"}

		results := make([]llm.CompletionResponse, callers)
		var wait sync.WaitGroup
		for i := range callers {
			wait.Add(1)
			go func() {
				defer wait.Done()
				resp, err := chain.Complete(context.Background(), request)
				if err != nil {
					t.Errorf("Complete: %v", err)
				}
				results[i] = resp
			}()
		}
		// The leader is held until every other caller is waiting on it.
		waitOrFail(t, allWaiting, "the other callers to wait on the leader")
		close(release)
		wait.Wait()

		measured := 0
		for _, resp := range results {
			if resp.Timing.Measured() {
				measured++
				if resp.Cached || resp.Timing != measuredTiming() {
					t.Errorf("the leader's response is %+v", resp)
				}
				continue
			}
			if !resp.Cached || resp.Timing != (types.Timing{}) {
				t.Errorf("a follower carries timing %+v (cached %v)", resp.Timing, resp.Cached)
			}
		}
		if measured != 1 || provider.made() != 1 {
			t.Errorf("%d responses were timed from %d provider calls, want one of each", measured, provider.made())
		}
	})
}

func TestCachePartitionFromContextHandlesNilAndUnpartitionedContexts(t *testing.T) {
	if got := cachePartitionFromContext(nil); got != (CachePartition{}) {
		t.Errorf("cachePartitionFromContext(nil) = %+v, want the zero value", got)
	}
	if got := cachePartitionFromContext(context.Background()); got != (CachePartition{}) {
		t.Errorf("cachePartitionFromContext(no partition set) = %+v, want the zero value", got)
	}

	// A context value under this key of the wrong type must not panic the
	// type assertion; it reads back as "no partition" instead.
	type wrongType struct{}
	ctx := context.WithValue(context.Background(), cachePartitionContextKey{}, wrongType{})
	if got := cachePartitionFromContext(ctx); got != (CachePartition{}) {
		t.Errorf("cachePartitionFromContext(wrong type) = %+v, want the zero value", got)
	}
}

func TestCacheNewMemoryStoreDefaultsANilClock(t *testing.T) {
	store := cacheNewMemoryStore(nil)
	if store.now == nil {
		t.Fatal("cacheNewMemoryStore(nil) left now nil; it must default to time.Now")
	}
	// Prove it is actually usable: Set then Get round-trips without a
	// clock-related panic.
	store.Set("k", llm.CompletionResponse{Content: "v"}, 0)
	resp, ok := store.Get("k")
	if !ok || resp.Content != "v" {
		t.Errorf("Get after Set = (%+v, %v), want (\"v\", true)", resp, ok)
	}
}

// cacheCanonicalSchemaJSON's error fallback is unreachable through this
// library's own schema generator (it only ever emits marshalable maps), but
// the function's contract is "never panic on a caller-shaped input" -- so an
// unmarshalable value (a channel) must still produce a stable string rather
// than propagating an error a middleware has nowhere to return.
func TestCacheCanonicalSchemaJSONFallsBackRatherThanPanickingOnUnmarshalableInput(t *testing.T) {
	schema := map[string]any{"bad": make(chan int)}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("cacheCanonicalSchemaJSON panicked on unmarshalable input: %v", r)
		}
	}()

	out := cacheCanonicalSchemaJSON(schema)
	if out == "" {
		t.Error("expected a non-empty fallback string for unmarshalable input")
	}
	// Confirm it really did take the fallback path, not a lucky marshal.
	if _, err := json.Marshal(schema); err == nil {
		t.Fatal("test setup is broken: a channel value marshaled successfully")
	}
}

// A client's data policy says whether its answers may be kept for reuse. A
// client whose policy forbids caching is not served an answer somebody else
// stored, and its own answer is not stored for the next caller.
func TestCacheHonoursAClientsDataPolicy(t *testing.T) {
	policy := func(allow bool) context.Context {
		return ops.WithExecConfig(context.Background(), &ops.ExecConfig{
			DataPolicy:         types.DataPolicy{AllowResultCaching: allow},
			DataPolicyDeclared: true,
		})
	}
	undeclared := ops.WithExecConfig(context.Background(), &ops.ExecConfig{
		DataPolicy: types.DataPolicy{AllowResultCaching: false},
	})

	cases := []struct {
		name      string
		first     context.Context
		second    context.Context
		wantCalls int  // provider calls after the two
		secondHit bool // whether the second is served from the cache
	}{
		{"no client", context.Background(), context.Background(), 1, true},
		{"a client with no policy declared", undeclared, undeclared, 1, true},
		{"a policy that allows caching", policy(true), policy(true), 1, true},
		{"a policy that forbids caching", policy(false), policy(false), 2, false},
		// Forbidden means not stored, and not only not read: a later caller
		// who may use the cache does not get this client's answer.
		{"forbidden first, then a caller with no policy", policy(false), context.Background(), 2, false},
		// And not read: an answer somebody else stored is not served to a
		// client whose policy forbids caching.
		{"a caller with no policy first, then forbidden", context.Background(), policy(false), 2, false},
		{"allowed first, then forbidden", policy(true), policy(false), 2, false},
		{"forbidden first, then allowed", policy(false), policy(true), 2, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeProvider{responses: []fakeResponse{{resp: llm.CompletionResponse{
				Content: "ok",
				Usage:   types.TokenUsage{TotalTokens: 30, PromptTokens: 20, CompletionTokens: 10},
			}}}}
			var events []CacheEvent
			wrapped := Cache(WithCacheStats(func(evt CacheEvent) { events = append(events, evt) }))(fake)
			req := llm.CompletionRequest{Model: "m", UserPrompt: "the same question"}

			first, err := wrapped.Complete(tc.first, req)
			if err != nil || first.Cached {
				t.Fatalf("the first call: err = %v, cached = %v", err, first.Cached)
			}
			second, err := wrapped.Complete(tc.second, req)
			if err != nil {
				t.Fatalf("the second call: %v", err)
			}
			if second.Cached != tc.secondHit {
				t.Errorf("the second call was served from the cache: %v, want %v", second.Cached, tc.secondHit)
			}
			if fake.calls != tc.wantCalls {
				t.Errorf("the provider was called %d times, want %d", fake.calls, tc.wantCalls)
			}
			// A call that goes round the cache still pays, and says so.
			if !tc.secondHit && second.Usage.TotalTokens != 30 {
				t.Errorf("a call that reached the provider reports usage %+v", second.Usage)
			}
		})
	}
}

// A call that goes round the cache says so, where a caller watching the
// cache's events can see it. It emitted nothing, so a client whose policy
// forbade caching looked like a client that never called.
func TestCacheBypassIsAnEvent(t *testing.T) {
	var events []CacheEvent
	fake := &fakeProvider{responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "ok"}}}}
	wrapped := Cache(WithCacheStats(func(evt CacheEvent) { events = append(events, evt) }))(fake)
	forbidden := ops.WithExecConfig(context.Background(), &ops.ExecConfig{DataPolicyDeclared: true})

	if _, err := wrapped.Complete(forbidden, llm.CompletionRequest{Model: "m", UserPrompt: "q"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(events) != 1 || !events[0].Bypassed || events[0].Hit || events[0].Coalesced {
		t.Fatalf("events = %+v, want one bypass", events)
	}
	if events[0].Key == "" {
		t.Error("the bypass event has no key")
	}

	// An ordinary miss and hit are not bypasses.
	events = nil
	for range 2 {
		if _, err := wrapped.Complete(context.Background(), llm.CompletionRequest{Model: "m", UserPrompt: "q"}); err != nil {
			t.Fatalf("Complete: %v", err)
		}
	}
	if len(events) != 2 || events[0].Bypassed || events[1].Bypassed || events[0].Hit || !events[1].Hit {
		t.Errorf("events = %+v, want a miss and then a hit", events)
	}
}

// The parts of a key are joined so that no two lists of parts join to the same
// string. A separator alone lets a part that contains the separator stand in
// for two parts.
func TestCacheLengthPrefixedIsInjective(t *testing.T) {
	lists := [][]string{
		nil,
		{""},
		{"", ""},
		{"a"},
		{"a", ""},
		{"", "a"},
		{"a", "b"},
		{"ab"},
		{"a\x1fb"},
		{"a", "\x1fb"},
		{"a\x1f", "b"},
		{"1:a"},
		{"1", "a"},
		{"11:a"},
		{"1", "1:a"},
		{"a:1"},
		{"a", "1:"},
	}
	seen := map[string]int{}
	for i, parts := range lists {
		joined := cacheLengthPrefixed(parts)
		if other, clash := seen[joined]; clash {
			t.Errorf("%q and %q join to the same string %q", lists[other], parts, joined)
		}
		seen[joined] = i
	}
}

// The cache key's axes for effort, search, tools and messages, one at a time.
func TestCacheAxisHelpers(t *testing.T) {
	effort := []struct {
		word      string
		preferred bool
		want      string
	}{
		{"", false, ""}, {"", true, ""}, {"none", false, "none"}, {"none", true, "preferred:none"},
		{"low", false, "low"}, {"low", true, "preferred:low"}, {"high", false, "high"}, {"max", true, "preferred:max"},
		{"preferred:none", false, "preferred:none"}, {"xhigh", false, "xhigh"},
	}
	for _, tc := range effort {
		got := cacheEffortAxis(llm.CompletionRequest{ReasoningEffort: tc.word, ReasoningEffortPreferred: tc.preferred})
		if got != tc.want {
			t.Errorf("cacheEffortAxis(%q, preferred %v) = %q, want %q", tc.word, tc.preferred, got, tc.want)
		}
	}

	if cacheFlagAxis(false, "web-search") != "" || cacheFlagAxis(true, "web-search") != "web-search" {
		t.Error("cacheFlagAxis does not read as its flag")
	}

	weather := llm.Tool{Name: "weather", Description: "d", Parameters: map[string]any{"type": "object"}}
	tools := []struct {
		name   string
		tools  []llm.Tool
		choice string
	}{
		{"nothing", nil, ""},
		{"one tool", []llm.Tool{weather}, ""},
		{"a choice with no tools", nil, "none"},
		{"a tool and a choice", []llm.Tool{weather}, "required"},
		{"a tool and another choice", []llm.Tool{weather}, "weather"},
		{"a tool with no parameters", []llm.Tool{{Name: "weather", Description: "d"}}, ""},
		{"a tool with no description", []llm.Tool{{Name: "weather", Parameters: weather.Parameters}}, ""},
		{"the description where the name was", []llm.Tool{{Name: "d", Description: "weather", Parameters: weather.Parameters}}, ""},
		{"two tools", []llm.Tool{weather, {Name: "time"}}, ""},
		{"the two the other way", []llm.Tool{{Name: "time"}, weather}, ""},
		{"a tool whose name is the choice", []llm.Tool{{Name: "required"}}, ""},
		{"a choice that is a tool's name", nil, "required"},
	}
	seen := map[string]string{}
	for _, tc := range tools {
		axis := cacheToolsAxis(tc.tools, tc.choice)
		if (axis == "") != (tc.name == "nothing") {
			t.Errorf("%s: axis %q", tc.name, axis)
		}
		if other, clash := seen[axis]; clash {
			t.Errorf("%q and %q share a tools axis", tc.name, other)
		}
		seen[axis] = tc.name
		if cacheToolsAxis(tc.tools, tc.choice) != axis {
			t.Errorf("%s: the axis is not stable", tc.name)
		}
	}

	messages := []struct {
		name string
		list []llm.Message
	}{
		{"nothing", nil},
		{"one", []llm.Message{{Role: "user", Content: "a"}}},
		{"another role", []llm.Message{{Role: "assistant", Content: "a"}}},
		{"other content", []llm.Message{{Role: "user", Content: "b"}}},
		{"a tool call id", []llm.Message{{Role: "tool", Content: "a", ToolCallID: "1"}}},
		{"another tool call id", []llm.Message{{Role: "tool", Content: "a", ToolCallID: "2"}}},
		{"two", []llm.Message{{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"}}},
		{"the two the other way", []llm.Message{{Role: "assistant", Content: "b"}, {Role: "user", Content: "a"}}},
		{"the content where the role was", []llm.Message{{Role: "a", Content: "user"}}},
		{"an empty message", []llm.Message{{}}},
		{"two empty messages", []llm.Message{{}, {}}},
	}
	seen = map[string]string{}
	for _, tc := range messages {
		axis := cacheMessagesAxis(tc.list)
		if (axis == "") != (tc.name == "nothing") {
			t.Errorf("%s: axis %q", tc.name, axis)
		}
		if other, clash := seen[axis]; clash {
			t.Errorf("%q and %q share a messages axis", tc.name, other)
		}
		seen[axis] = tc.name
	}
}

// A stats callback is the caller's: whatever it does, emit returns, and a
// callback that does not panic is told the event.
func TestCacheEmit(t *testing.T) {
	var nilMap map[string]int
	cases := []struct {
		name     string
		callback func(CacheEvent)
		told     bool
	}{
		{"no callback", nil, false},
		{"a callback", func(CacheEvent) {}, true},
		{"a panic with a string", func(CacheEvent) { panic("callback bug") }, true},
		{"a panic with an error", func(CacheEvent) { panic(errors.New("callback bug")) }, true},
		{"a panic with nil", func(CacheEvent) { panic(nil) }, true},
		{"a panic with a number", func(CacheEvent) { panic(42) }, true},
		{"a nil map write", func(CacheEvent) { nilMap["x"] = 1 }, true},
		{"an index out of range", func(CacheEvent) { _ = []int{}[len(nilMap)+1] }, true},
		{"a panic with the key in it", func(e CacheEvent) { panic(e.Key) }, true},
		{"a callback that panics after reading", func(e CacheEvent) { _ = e.Hit; panic("late") }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var told bool
			handler := &cacheHandler{}
			if tc.callback != nil {
				callback := tc.callback
				handler.stats = func(e CacheEvent) {
					told = true
					callback(e)
				}
			}
			handler.emit(context.Background(), CacheEvent{Key: "k", Hit: true})
			if told != tc.told {
				t.Fatalf("told = %v, want %v", told, tc.told)
			}
		})
	}
}

// --- The decision a request carries.

// cacheDecisionRequest is a request as an operation builds it when it decides
// something: the prompts for a generating model, and the same question typed.
func cacheDecisionRequest() llm.CompletionRequest {
	req := baseCacheRequest()
	req.Decision = &llm.Decision{
		State:    `"Please refund my order."`,
		Guidance: "Refunds are allowed for thirty days.",
		Questions: []llm.DecisionQuestion{
			{Kind: llm.DecisionYesNo, Ask: "Is the customer asking for a refund?"},
			{
				Kind:     llm.DecisionChoice,
				Ask:      "Which team should answer?",
				Options:  []string{"billing", "shipping", "support"},
				Criteria: []string{"money questions", "delivery questions", ""},
			},
			{Kind: llm.DecisionScale, Ask: "How urgent is it?", Low: 1, High: 5},
		},
	}
	return req
}

// A request that carries no decision is keyed as it was before the field
// existed, so an entry a store holds from then is still found.
func TestCacheKeyOfARequestWithNoDecisionIsUnchanged(t *testing.T) {
	const want = "0b113d18d279d56d8a503d2fda34dc287e6903221656ec954a5cb53490bb81e9"
	if got := cacheKeyFor(baseCacheRequest(), "openai", basePartition()); got != want {
		t.Fatalf("the key of a request with no decision is %q, want %q", got, want)
	}
}

// Every part of a decision is part of the identity of the call: the same prompts
// asked as a different decision are a different request.
func TestCacheKeyAxisDecision(t *testing.T) {
	edit := func(change func(*llm.Decision)) llm.CompletionRequest {
		req := cacheDecisionRequest()
		// A copy of the whole decision, so that the edit leaves the base alone.
		copied := *req.Decision
		copied.Questions = append([]llm.DecisionQuestion(nil), req.Decision.Questions...)
		for i := range copied.Questions {
			copied.Questions[i].Options = append([]string(nil), copied.Questions[i].Options...)
			if copied.Questions[i].Criteria != nil {
				copied.Questions[i].Criteria = append([]string(nil), copied.Questions[i].Criteria...)
			}
		}
		change(&copied)
		req.Decision = &copied
		return req
	}
	cases := []struct {
		name    string
		changed llm.CompletionRequest
	}{
		{"the state", edit(func(d *llm.Decision) { d.State = `"Please cancel my order."` })},
		{"the guidance", edit(func(d *llm.Decision) { d.Guidance = "Refunds are allowed for sixty days." })},
		{"no guidance", edit(func(d *llm.Decision) { d.Guidance = "" })},
		{"a question's text", edit(func(d *llm.Decision) { d.Questions[0].Ask = "Is the customer asking for a replacement?" })},
		{"a question's kind", edit(func(d *llm.Decision) {
			d.Questions[0] = llm.DecisionQuestion{Kind: llm.DecisionScale, Ask: d.Questions[0].Ask, Low: 0, High: 1}
		})},
		{"an option's text", edit(func(d *llm.Decision) { d.Questions[1].Options[2] = "sales" })},
		{"the order of the options", edit(func(d *llm.Decision) {
			d.Questions[1].Options[0], d.Questions[1].Options[1] = d.Questions[1].Options[1], d.Questions[1].Options[0]
		})},
		{"another option", edit(func(d *llm.Decision) {
			d.Questions[1].Options = append(d.Questions[1].Options, "legal")
			d.Questions[1].Criteria = append(d.Questions[1].Criteria, "")
		})},
		{"a criterion", edit(func(d *llm.Decision) { d.Questions[1].Criteria[1] = "parcel questions" })},
		{"criteria that are blank, against none", edit(func(d *llm.Decision) { d.Questions[1].Criteria = nil })},
		{"the low end of a scale", edit(func(d *llm.Decision) { d.Questions[2].Low = 0 })},
		{"the high end of a scale", edit(func(d *llm.Decision) { d.Questions[2].High = 10 })},
		{"another question", edit(func(d *llm.Decision) {
			d.Questions = append(d.Questions, llm.DecisionQuestion{Kind: llm.DecisionYesNo, Ask: "Is it a gift?"})
		})},
		{"a question fewer", edit(func(d *llm.Decision) { d.Questions = d.Questions[:2] })},
		{"the order of the questions", edit(func(d *llm.Decision) { d.Questions[0], d.Questions[2] = d.Questions[2], d.Questions[0] })},
		{"no questions", edit(func(d *llm.Decision) { d.Questions = nil })},
		{"no decision at all", func() llm.CompletionRequest { req := cacheDecisionRequest(); req.Decision = nil; return req }()},
		{"options that run together", edit(func(d *llm.Decision) {
			d.Questions[1].Options = []string{"billingshipping", "support"}
			d.Questions[1].Criteria = []string{"", ""}
		})},
	}
	base := cacheDecisionRequest()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertAxisChangesKey(t, "decision", base, tc.changed)
		})
	}

	t.Run("a text boundary moved from one field to the next is another request", func(t *testing.T) {
		a := edit(func(d *llm.Decision) { d.State, d.Guidance = `"ab"`, "c" })
		b := edit(func(d *llm.Decision) { d.State, d.Guidance = `"a`, `b"c` })
		assertAxisChangesKey(t, "decision", a, b)
		c := edit(func(d *llm.Decision) { d.Questions[1].Options = []string{"ab", "c"}; d.Questions[1].Criteria = nil })
		e := edit(func(d *llm.Decision) { d.Questions[1].Options = []string{"a", "bc"}; d.Questions[1].Criteria = nil })
		assertAxisChangesKey(t, "decision", c, e)
	})

	t.Run("the same decision at another address is the same request", func(t *testing.T) {
		a, b := cacheDecisionRequest(), cacheDecisionRequest()
		if a.Decision == b.Decision {
			t.Fatal("the two requests share a decision")
		}
		if cacheKeyFor(a, "openai", basePartition()) != cacheKeyFor(b, "openai", basePartition()) {
			t.Error("two equal decisions are two keys")
		}
	})
}

// cachingDecider is a decision provider that counts its calls and answers each
// request by its state, so that a test can tell whose answer it was served.
type cachingDecider struct {
	calls atomic.Int32
	reply func(req llm.CompletionRequest) llm.CompletionResponse
}

func (d *cachingDecider) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	d.calls.Add(1)
	return d.reply(req), nil
}
func (d *cachingDecider) Name() string                                   { return "jev" }
func (d *cachingDecider) EstimateCost(req llm.CompletionRequest) float64 { return 0 }
func (d *cachingDecider) RetryPolicy() (int, time.Duration)              { return 0, 0 }

func typedReply(answers ...llm.DecisionAnswer) func(llm.CompletionRequest) llm.CompletionResponse {
	return func(llm.CompletionRequest) llm.CompletionResponse {
		return llm.CompletionResponse{
			Provider: "jev", Model: "jev-1.13.0",
			Usage:           types.TokenUsage{PromptTokens: 300, TotalTokens: 300},
			DecisionAnswers: append([]llm.DecisionAnswer(nil), answers...),
		}
	}
}

func TestCacheKeepsATypedAnswerAndServesItEqual(t *testing.T) {
	answers := []llm.DecisionAnswer{
		{Kind: llm.DecisionYesNo, Yes: true, ModelConfidence: 0.91, ConfidenceReported: true},
		{Kind: llm.DecisionChoice, Label: "shipping", ModelConfidence: 0.62, ConfidenceReported: true},
		{Kind: llm.DecisionScale, Level: 4},
	}
	decider := &cachingDecider{reply: typedReply(answers...)}
	wrapped := Cache()(decider)
	req := cacheDecisionRequest()

	first, err := wrapped.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := wrapped.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if decider.calls.Load() != 1 {
		t.Fatalf("the provider was called %d times, want 1: a typed answer is an answer", decider.calls.Load())
	}
	if !second.Cached || second.Usage != (types.TokenUsage{}) {
		t.Errorf("the second is cached %v with usage %+v", second.Cached, second.Usage)
	}
	if !reflect.DeepEqual(second.DecisionAnswers, answers) {
		t.Errorf("the hit's answers are\n%+v\nwant\n%+v", second.DecisionAnswers, answers)
	}
	if second.Provider != first.Provider || second.Model != first.Model || second.Content != first.Content {
		t.Errorf("the hit names %q %q %q, want %q %q %q", second.Provider, second.Model, second.Content, first.Provider, first.Model, first.Content)
	}

	t.Run("a caller that edits a response changes nobody else's", func(t *testing.T) {
		scribble := func(resp llm.CompletionResponse) {
			for i := range resp.DecisionAnswers {
				resp.DecisionAnswers[i] = llm.DecisionAnswer{Kind: llm.DecisionScale, Level: 99}
			}
		}
		scribble(first)
		hit, err := wrapped.Complete(context.Background(), req)
		if err != nil {
			t.Fatalf("hit: %v", err)
		}
		if !reflect.DeepEqual(hit.DecisionAnswers, answers) {
			t.Fatalf("editing the first response changed the stored answers: %+v", hit.DecisionAnswers)
		}
		scribble(hit)
		again, err := wrapped.Complete(context.Background(), req)
		if err != nil {
			t.Fatalf("again: %v", err)
		}
		if !reflect.DeepEqual(again.DecisionAnswers, answers) {
			t.Fatalf("editing a hit changed the stored answers: %+v", again.DecisionAnswers)
		}
		if decider.calls.Load() != 1 {
			t.Errorf("the provider was called %d times", decider.calls.Load())
		}
	})
}

func TestCacheServesAnEscalatedAnswerWithItsRecordCopied(t *testing.T) {
	escalation := &llm.DecisionEscalation{Provider: "jev", Model: "jev-1.13.0", Reason: llm.DecisionEscalatedBelowFloor, ModelConfidence: 0.4, ConfidenceReported: true}
	calls := 0
	generator := completeFunc(func(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
		calls++
		return llm.CompletionResponse{Content: `{"q1":true}`, Provider: "openai", Model: "gpt-6-luna", FinishReason: "stop", DecisionEscalation: escalation}, nil
	})
	wrapped := Cache()(generator)
	req := cacheDecisionRequest()

	if _, err := wrapped.Complete(context.Background(), req); err != nil {
		t.Fatalf("first: %v", err)
	}
	hit, err := wrapped.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("hit: %v", err)
	}
	if calls != 1 || !reflect.DeepEqual(hit.DecisionEscalation, escalation) {
		t.Fatalf("%d calls, escalation %+v, want one call and %+v", calls, hit.DecisionEscalation, escalation)
	}
	if hit.DecisionEscalation == escalation {
		t.Fatal("the hit shares its escalation record with the stored response")
	}
	hit.DecisionEscalation.Reason = "scribbled"
	again, err := wrapped.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("again: %v", err)
	}
	if again.DecisionEscalation.Reason != llm.DecisionEscalatedBelowFloor {
		t.Fatalf("editing a hit's record changed the stored one: %+v", again.DecisionEscalation)
	}
}

// What is not an answer to the question is not kept: the call path refuses it,
// and a copy kept would refuse it again for as long as the entry lived.
func TestCacheDoesNotKeepATypedAnswerThatIsNotAnAnswer(t *testing.T) {
	question := func(q llm.DecisionQuestion) *llm.Decision {
		return &llm.Decision{State: `"s"`, Questions: []llm.DecisionQuestion{q}}
	}
	choice := llm.DecisionQuestion{Kind: llm.DecisionChoice, Ask: "Which?", Options: []string{"a", "b"}}
	scale := llm.DecisionQuestion{Kind: llm.DecisionScale, Ask: "How much?", Low: 1, High: 5}
	yesNo := llm.DecisionQuestion{Kind: llm.DecisionYesNo, Ask: "Is it?"}
	good := func(kind llm.DecisionKind) llm.DecisionAnswer {
		switch kind {
		case llm.DecisionChoice:
			return llm.DecisionAnswer{Kind: kind, Label: "a"}
		case llm.DecisionScale:
			return llm.DecisionAnswer{Kind: kind, Level: 3}
		}
		return llm.DecisionAnswer{Kind: kind, Yes: true}
	}

	cases := []struct {
		name     string
		decision *llm.Decision
		resp     llm.CompletionResponse
		stored   bool
	}{
		{"a yes or no answered", question(yesNo), llm.CompletionResponse{DecisionAnswers: []llm.DecisionAnswer{good(llm.DecisionYesNo)}}, true},
		{"a choice answered", question(choice), llm.CompletionResponse{DecisionAnswers: []llm.DecisionAnswer{good(llm.DecisionChoice)}}, true},
		{"a scale answered", question(scale), llm.CompletionResponse{DecisionAnswers: []llm.DecisionAnswer{good(llm.DecisionScale)}}, true},
		{"an answer with a probability", question(yesNo), llm.CompletionResponse{DecisionAnswers: []llm.DecisionAnswer{{Kind: llm.DecisionYesNo, ModelConfidence: 0.5, ConfidenceReported: true}}}, true},
		{"no answers", question(yesNo), llm.CompletionResponse{}, false},
		{"too many answers", question(yesNo), llm.CompletionResponse{DecisionAnswers: []llm.DecisionAnswer{good(llm.DecisionYesNo), good(llm.DecisionYesNo)}}, false},
		{"an answer of another kind", question(yesNo), llm.CompletionResponse{DecisionAnswers: []llm.DecisionAnswer{good(llm.DecisionScale)}}, false},
		{"an answer with no kind", question(yesNo), llm.CompletionResponse{DecisionAnswers: []llm.DecisionAnswer{{}}}, false},
		{"a label nobody offered", question(choice), llm.CompletionResponse{DecisionAnswers: []llm.DecisionAnswer{{Kind: llm.DecisionChoice, Label: "c"}}}, false},
		{"a level off the scale", question(scale), llm.CompletionResponse{DecisionAnswers: []llm.DecisionAnswer{{Kind: llm.DecisionScale, Level: 6}}}, false},
		{"a probability above one", question(yesNo), llm.CompletionResponse{DecisionAnswers: []llm.DecisionAnswer{{Kind: llm.DecisionYesNo, ModelConfidence: 1.5, ConfidenceReported: true}}}, false},
		{"answers to a request that decided nothing", nil, llm.CompletionResponse{DecisionAnswers: []llm.DecisionAnswer{good(llm.DecisionYesNo)}}, false},
		{"typed answers cut off", question(yesNo), llm.CompletionResponse{FinishReason: "length", DecisionAnswers: []llm.DecisionAnswer{good(llm.DecisionYesNo)}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := tc.resp
			decider := &cachingDecider{reply: func(llm.CompletionRequest) llm.CompletionResponse { return resp }}
			wrapped := Cache()(decider)
			req := llm.CompletionRequest{Model: "jev-latest", UserPrompt: "p", Decision: tc.decision}
			for i := 0; i < 2; i++ {
				if _, err := wrapped.Complete(context.Background(), req); err != nil {
					t.Fatalf("call %d: %v", i+1, err)
				}
			}
			want := int32(2)
			if tc.stored {
				want = 1
			}
			if decider.calls.Load() != want {
				t.Fatalf("the provider was called %d times, want %d", decider.calls.Load(), want)
			}
		})
	}
}

// The same prompts asked as different decisions, and as no decision, are
// different calls, each with its own answer.
func TestCacheDoesNotAnswerOneDecisionWithAnotherOrWithText(t *testing.T) {
	decider := &cachingDecider{reply: func(req llm.CompletionRequest) llm.CompletionResponse {
		if req.Decision == nil {
			return llm.CompletionResponse{Content: "text", FinishReason: "stop"}
		}
		return llm.CompletionResponse{DecisionAnswers: []llm.DecisionAnswer{{Kind: llm.DecisionYesNo, Yes: req.Decision.State == `"yes"`}}}
	}}
	wrapped := Cache()(decider)
	withState := func(state string) llm.CompletionRequest {
		req := baseCacheRequest()
		req.Decision = &llm.Decision{State: state, Questions: []llm.DecisionQuestion{{Kind: llm.DecisionYesNo, Ask: "Is it?"}}}
		return req
	}
	text := baseCacheRequest()

	for round := 0; round < 2; round++ {
		yesReply, err := wrapped.Complete(context.Background(), withState(`"yes"`))
		if err != nil || len(yesReply.DecisionAnswers) != 1 || !yesReply.DecisionAnswers[0].Yes {
			t.Fatalf("round %d: the yes decision was answered %+v, %v", round, yesReply.DecisionAnswers, err)
		}
		noReply, err := wrapped.Complete(context.Background(), withState(`"no"`))
		if err != nil || len(noReply.DecisionAnswers) != 1 || noReply.DecisionAnswers[0].Yes {
			t.Fatalf("round %d: the no decision was answered %+v, %v", round, noReply.DecisionAnswers, err)
		}
		plain, err := wrapped.Complete(context.Background(), text)
		if err != nil || plain.Content != "text" || len(plain.DecisionAnswers) != 0 {
			t.Fatalf("round %d: the text request was answered %+v, %v", round, plain, err)
		}
	}
	if decider.calls.Load() != 3 {
		t.Fatalf("the provider was called %d times, want 3: one for each of three different requests, and none for their repeats", decider.calls.Load())
	}
}

// The data policy's rule for caching holds for a typed answer as for a completion.
func TestCacheHonoursAClientsDataPolicyForATypedAnswer(t *testing.T) {
	policy := func(allow bool) context.Context {
		return ops.WithExecConfig(context.Background(), &ops.ExecConfig{
			DataPolicy:         types.DataPolicy{AllowResultCaching: allow},
			DataPolicyDeclared: true,
		})
	}
	cases := []struct {
		name      string
		ctx       context.Context
		wantCalls int32
	}{
		{"no client", context.Background(), 1},
		{"a policy that allows caching", policy(true), 1},
		{"a policy that forbids caching", policy(false), 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decider := &cachingDecider{reply: typedReply(llm.DecisionAnswer{Kind: llm.DecisionYesNo, Yes: true})}
			req := llm.CompletionRequest{Model: "jev-latest", Decision: &llm.Decision{State: `"s"`, Questions: []llm.DecisionQuestion{{Kind: llm.DecisionYesNo, Ask: "Is it?"}}}}
			wrapped := Cache()(decider)
			for i := 0; i < 2; i++ {
				if _, err := wrapped.Complete(tc.ctx, req); err != nil {
					t.Fatalf("call %d: %v", i+1, err)
				}
			}
			if decider.calls.Load() != tc.wantCalls {
				t.Errorf("the provider was called %d times, want %d", decider.calls.Load(), tc.wantCalls)
			}
		})
	}
}

func TestCacheCloneResponseCopiesTypedAnswersAndTheEscalation(t *testing.T) {
	answers := []llm.DecisionAnswer{
		{Kind: llm.DecisionYesNo, Yes: true, ModelConfidence: 0.9, ConfidenceReported: true},
		{Kind: llm.DecisionChoice, Label: "a"},
	}
	cases := []struct {
		name string
		resp llm.CompletionResponse
	}{
		{"no typed answers", llm.CompletionResponse{Content: "x"}},
		{"nil answers", llm.CompletionResponse{DecisionAnswers: nil}},
		{"empty answers", llm.CompletionResponse{DecisionAnswers: []llm.DecisionAnswer{}}},
		{"one answer", llm.CompletionResponse{DecisionAnswers: answers[:1]}},
		{"two answers", llm.CompletionResponse{DecisionAnswers: answers}},
		{"an escalation", llm.CompletionResponse{Content: "x", DecisionEscalation: &llm.DecisionEscalation{Provider: "jev", Reason: llm.DecisionEscalatedUnavailable}}},
		{"an escalation with a probability", llm.CompletionResponse{DecisionEscalation: &llm.DecisionEscalation{Provider: "jev", Model: "m", Reason: llm.DecisionEscalatedBelowFloor, ModelConfidence: 0.3, ConfidenceReported: true}}},
		{"answers and an escalation", llm.CompletionResponse{DecisionAnswers: answers, DecisionEscalation: &llm.DecisionEscalation{Provider: "jev"}}},
		{"answers and tool calls", llm.CompletionResponse{DecisionAnswers: answers, ToolCalls: []llm.ToolCall{{ID: "1", Name: "a", Arguments: json.RawMessage(`{}`)}}}},
		{"usage is carried", llm.CompletionResponse{Usage: types.TokenUsage{TotalTokens: 5}, DecisionAnswers: answers}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clone := cacheCloneResponse(tc.resp)
			if !reflect.DeepEqual(clone, tc.resp) {
				t.Fatalf("clone = %+v, want %+v", clone, tc.resp)
			}
			if len(tc.resp.DecisionAnswers) > 0 && &clone.DecisionAnswers[0] == &tc.resp.DecisionAnswers[0] {
				t.Error("the clone shares the slice of answers")
			}
			if tc.resp.DecisionEscalation != nil && clone.DecisionEscalation == tc.resp.DecisionEscalation {
				t.Error("the clone shares the escalation record")
			}
			if (tc.resp.DecisionAnswers == nil) != (clone.DecisionAnswers == nil) {
				t.Error("nil and empty answers were confused")
			}
		})
	}
}

// cacheWithWaitHook sets the callback told when a call starts waiting on an
// identical in-flight call. Unexported for the same reason as cacheWithClock.
func cacheWithWaitHook(fn func()) CacheOption {
	return func(c *cacheConfig) { c.onWait = fn }
}

// cacheWithClock overrides the store's clock. Unexported on purpose: it
// exists so this package's own tests can drive TTL expiry without sleeping
// real time, not as something a caller should ever need to override -- a
// cache whose notion of "now" disagrees with the rest of the process is a bug,
// not a feature.
func cacheWithClock(now func() time.Time) CacheOption {
	return func(c *cacheConfig) {
		if now != nil {
			c.now = now
		}
	}
}

// conversationCacheRequest is a request in the middle of a tool loop whose
// history holds no tool call of the model's: tools declared, a user turn, an
// assistant turn of text and a tool result.
func conversationCacheRequest() llm.CompletionRequest {
	req := baseCacheRequest()
	req.Tools = []llm.Tool{{Name: "weather", Description: "current weather", Parameters: map[string]any{"type": "object"}}}
	req.ToolChoice = "auto"
	req.Messages = []llm.Message{
		{Role: "user", Content: "How warm is Oslo?"},
		{Role: "assistant", Content: "Let me look."},
		{Role: "tool", Content: "7C", ToolCallID: "call_1"},
	}
	return req
}

// A conversation whose history holds no tool call of the model's is keyed as it
// was before a message could carry one, so an entry a store holds from then is
// still found.
func TestCacheKeyOfAConversationWithNoToolCallsIsUnchanged(t *testing.T) {
	const want = "d5476431612b59d1ca813b151f3ea9da2539e8c5f3d04c193f831394397d0f37"
	if got := cacheKeyFor(conversationCacheRequest(), "openai", basePartition()); got != want {
		t.Fatalf("the key is %q, want %q", got, want)
	}
}

// The model's earlier tool calls are part of what a request is: two
// conversations that have the same prompts, tools and results and differ only in
// what the model asked for in an earlier turn are different requests, and one is
// not served the other's answer.
func TestCacheKeyDiffersWhenOnlyAnEarlierToolCallDiffers(t *testing.T) {
	withCalls := func(calls ...llm.ToolCall) llm.CompletionRequest {
		req := conversationCacheRequest()
		req.Messages = []llm.Message{
			{Role: "user", Content: "How warm is Oslo?"},
			{Role: "assistant", Content: "Let me look.", ToolCalls: calls},
			{Role: "tool", Content: "7C", ToolCallID: "call_1"},
		}
		return req
	}
	call := llm.ToolCall{ID: "call_1", Name: "weather", Arguments: json.RawMessage(`{"city":"Oslo"}`)}
	other := llm.ToolCall{ID: "call_2", Name: "time", Arguments: json.RawMessage(`{}`)}

	cases := []struct {
		name string
		req  llm.CompletionRequest
	}{
		{"the call", withCalls(call)},
		{"another id", withCalls(llm.ToolCall{ID: "call_7", Name: "weather", Arguments: call.Arguments})},
		{"another name", withCalls(llm.ToolCall{ID: "call_1", Name: "forecast", Arguments: call.Arguments})},
		{"another value", withCalls(llm.ToolCall{ID: "call_1", Name: "weather", Arguments: json.RawMessage(`{"city":"Bergen"}`)})},
		{"another argument name", withCalls(llm.ToolCall{ID: "call_1", Name: "weather", Arguments: json.RawMessage(`{"town":"Oslo"}`)})},
		{"an extra argument", withCalls(llm.ToolCall{ID: "call_1", Name: "weather", Arguments: json.RawMessage(`{"city":"Oslo","days":2}`)})},
		{"arguments that are not JSON", withCalls(llm.ToolCall{ID: "call_1", Name: "weather", Arguments: json.RawMessage(`city=Oslo`)})},
		{"empty arguments", withCalls(llm.ToolCall{ID: "call_1", Name: "weather"})},
		{"two calls", withCalls(call, other)},
		{"the two calls the other way round", withCalls(other, call)},
		{"the second call alone", withCalls(other)},
		{"a call and a repeat of it", withCalls(call, call)},
		{"no call at all", withCalls()},
	}
	seen := map[string]string{}
	for _, tc := range cases {
		key := cacheKeyFor(tc.req, "openai", basePartition())
		if other, clash := seen[key]; clash {
			t.Errorf("%q shares a cache key with %q", tc.name, other)
		}
		seen[key] = tc.name
	}

	t.Run("a call moved to another turn", func(t *testing.T) {
		first := conversationCacheRequest()
		first.Messages = []llm.Message{
			{Role: "assistant", ToolCalls: []llm.ToolCall{call}},
			{Role: "assistant", Content: "x"},
		}
		second := conversationCacheRequest()
		second.Messages = []llm.Message{
			{Role: "assistant"},
			{Role: "assistant", Content: "x", ToolCalls: []llm.ToolCall{call}},
		}
		if cacheKeyFor(first, "openai", basePartition()) == cacheKeyFor(second, "openai", basePartition()) {
			t.Error("a call in the first turn and the same call in the second share a key")
		}
	})

	t.Run("the same call written differently is the same call", func(t *testing.T) {
		spaced := withCalls(llm.ToolCall{ID: "call_1", Name: "weather", Arguments: json.RawMessage("{ \"city\" :\n \"Oslo\" }")})
		reordered := withCalls(llm.ToolCall{ID: "call_1", Name: "weather", Arguments: json.RawMessage(`{"city":"Oslo"}`)})
		if cacheKeyFor(spaced, "openai", basePartition()) != cacheKeyFor(reordered, "openai", basePartition()) {
			t.Error("white space in the arguments changed the key")
		}
		two := withCalls(llm.ToolCall{ID: "c", Name: "n", Arguments: json.RawMessage(`{"a":1,"b":2}`)})
		swapped := withCalls(llm.ToolCall{ID: "c", Name: "n", Arguments: json.RawMessage(`{"b":2,"a":1}`)})
		if cacheKeyFor(two, "openai", basePartition()) != cacheKeyFor(swapped, "openai", basePartition()) {
			t.Error("the order of the arguments changed the key")
		}
	})

	t.Run("the same request built twice has one key", func(t *testing.T) {
		if cacheKeyFor(withCalls(call, other), "openai", basePartition()) != cacheKeyFor(withCalls(call, other), "openai", basePartition()) {
			t.Error("two keys for one request")
		}
	})
}

// Two conversations that differ only in an earlier tool call are two requests to
// the provider, and the same conversation asked twice is one.
func TestCacheDoesNotAnswerOneConversationWithAnother(t *testing.T) {
	reply := llm.CompletionResponse{
		FinishReason: "tool_calls",
		ToolCalls:    []llm.ToolCall{{ID: "call_9", Name: "weather", Arguments: json.RawMessage(`{"city":"Oslo"}`)}},
		Usage:        types.TokenUsage{PromptTokens: 20, CompletionTokens: 10, TotalTokens: 30},
	}
	conversation := func(argument string) llm.CompletionRequest {
		req := conversationCacheRequest()
		req.Messages = []llm.Message{
			{Role: "user", Content: "How warm is Oslo?"},
			{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "call_1", Name: "weather", Arguments: json.RawMessage(argument)}}},
			{Role: "tool", Content: "7C", ToolCallID: "call_1"},
		}
		return req
	}

	fake := &fakeProvider{responses: []fakeResponse{{resp: reply}}}
	wrapped := Cache()(fake)

	first, err := wrapped.Complete(context.Background(), conversation(`{"city":"Oslo"}`))
	if err != nil || first.Cached {
		t.Fatalf("the first call: err %v, cached %v", err, first.Cached)
	}
	other, err := wrapped.Complete(context.Background(), conversation(`{"city":"Bergen"}`))
	if err != nil || other.Cached {
		t.Fatalf("a conversation that differs in an earlier call was served from the cache: err %v, cached %v", err, other.Cached)
	}
	if fake.calls != 2 {
		t.Fatalf("the provider was called %d times, want 2", fake.calls)
	}
	again, err := wrapped.Complete(context.Background(), conversation(`{"city":"Oslo"}`))
	if err != nil || !again.Cached || fake.calls != 2 {
		t.Fatalf("the same conversation again: err %v, cached %v, provider calls %d; want a hit", err, again.Cached, fake.calls)
	}
}

// A reply that is only tool calls is stored, comes back equal with nothing the
// caller could mutate shared with what is stored or with another hit, and is held
// to the data policy the client declared as any completion is.
func TestCacheStoresAReplyThatIsOnlyToolCalls(t *testing.T) {
	newReply := func() llm.CompletionResponse {
		return llm.CompletionResponse{
			Provider: "openai", Model: "m", FinishReason: "tool_calls",
			ToolCalls: []llm.ToolCall{
				{ID: "call_1", Name: "weather", Arguments: json.RawMessage(`{"city":"Oslo"}`)},
				{ID: "call_2", Name: "time", Arguments: json.RawMessage(`{}`)},
			},
			Usage: types.TokenUsage{PromptTokens: 20, CompletionTokens: 10, TotalTokens: 30},
		}
	}
	req := conversationCacheRequest()

	t.Run("it comes back equal and shares nothing", func(t *testing.T) {
		fake := &fakeProvider{responses: []fakeResponse{{resp: newReply()}}}
		wrapped := Cache()(fake)

		first, err := wrapped.Complete(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		// The leader edits what it was given in place.
		first.ToolCalls[0].Arguments[2] = 'X'
		first.ToolCalls[1].Name = "edited"

		hit, err := wrapped.Complete(context.Background(), req)
		if err != nil || !hit.Cached || fake.calls != 1 {
			t.Fatalf("err %v, cached %v, provider calls %d; want a hit", err, hit.Cached, fake.calls)
		}
		want := newReply()
		if !reflect.DeepEqual(hit.ToolCalls, want.ToolCalls) || hit.Content != "" || hit.FinishReason != "tool_calls" {
			t.Errorf("the hit is not the stored reply: %+v", hit)
		}
		if hit.Usage != (types.TokenUsage{}) {
			t.Errorf("a hit reports usage %+v", hit.Usage)
		}
		// And a hit edits what it was given in place.
		hit.ToolCalls[0].Arguments[2] = 'Y'
		hit.ToolCalls = append(hit.ToolCalls[:1], llm.ToolCall{ID: "x"})
		second, _ := wrapped.Complete(context.Background(), req)
		if !reflect.DeepEqual(second.ToolCalls, want.ToolCalls) {
			t.Errorf("one hit's edit reached the next: %+v", second.ToolCalls)
		}
	})

	t.Run("the client's data policy decides", func(t *testing.T) {
		policy := func(allow bool) context.Context {
			return ops.WithExecConfig(context.Background(), &ops.ExecConfig{
				DataPolicy: types.DataPolicy{AllowResultCaching: allow}, DataPolicyDeclared: true})
		}
		for _, tc := range []struct {
			name      string
			ctx       context.Context
			wantCalls int
		}{
			{"a policy that allows caching", policy(true), 1},
			{"a policy that forbids caching", policy(false), 2},
			{"no policy", context.Background(), 1},
		} {
			t.Run(tc.name, func(t *testing.T) {
				fake := &fakeProvider{responses: []fakeResponse{{resp: newReply()}}}
				wrapped := Cache()(fake)
				for i := 0; i < 2; i++ {
					if _, err := wrapped.Complete(tc.ctx, req); err != nil {
						t.Fatal(err)
					}
				}
				if fake.calls != tc.wantCalls {
					t.Errorf("the provider was called %d times, want %d", fake.calls, tc.wantCalls)
				}
			})
		}
	})

	t.Run("a reply that was cut off or has nothing in it is not stored", func(t *testing.T) {
		for name, reply := range map[string]llm.CompletionResponse{
			"calls cut off at the ceiling": {FinishReason: "length", ToolCalls: newReply().ToolCalls},
			"calls withheld by a filter":   {FinishReason: "content_filter", ToolCalls: newReply().ToolCalls},
			"no calls and no text":         {FinishReason: "tool_calls"},
		} {
			fake := &fakeProvider{responses: []fakeResponse{{resp: reply}}}
			wrapped := Cache()(fake)
			for i := 0; i < 2; i++ {
				if _, err := wrapped.Complete(context.Background(), req); err != nil {
					t.Fatal(err)
				}
			}
			if fake.calls != 2 {
				t.Errorf("%s: the provider was called %d times, want 2: the reply was stored", name, fake.calls)
			}
		}
	})
}

// contextStore is a ContextCacheStore that records the context it is given and
// takes as long as it is told to over a read, unless that context ends first.
type contextStore struct {
	countingStore
	readTime time.Duration
	getCtx   context.Context
	setCtx   context.Context
	plain    int // calls of Get and Set, which a context store is not to be given
}

type contextStoreKey struct{}

func (s *contextStore) Get(key string) (llm.CompletionResponse, bool) {
	s.mu.Lock()
	s.plain++
	s.mu.Unlock()
	return s.countingStore.Get(key)
}

func (s *contextStore) Set(key string, resp llm.CompletionResponse, ttl time.Duration) {
	s.mu.Lock()
	s.plain++
	s.mu.Unlock()
	s.countingStore.Set(key, resp, ttl)
}

func (s *contextStore) GetContext(ctx context.Context, key string) (llm.CompletionResponse, bool) {
	s.mu.Lock()
	s.getCtx = ctx
	s.mu.Unlock()
	select {
	case <-time.After(s.readTime):
	case <-ctx.Done():
		return llm.CompletionResponse{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gets++
	resp, ok := s.held[key]
	return resp, ok
}

func (s *contextStore) SetContext(ctx context.Context, key string, resp llm.CompletionResponse, ttl time.Duration) {
	s.mu.Lock()
	s.setCtx = ctx
	s.mu.Unlock()
	s.countingStore.Set(key, resp, ttl)
}

func TestCacheGivesAContextStoreTheContextOfTheCall(t *testing.T) {
	store := &contextStore{}
	provider := completeFunc(func(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
		return llm.CompletionResponse{Content: "paid for"}, nil
	})
	wrapped := Cache(WithCacheStore(store))(provider)
	ctx := context.WithValue(context.Background(), contextStoreKey{}, "this call")

	for range 2 {
		if _, err := wrapped.Complete(ctx, llm.CompletionRequest{Model: "m", UserPrompt: "q"}); err != nil {
			t.Fatal(err)
		}
	}
	if store.getCtx == nil || store.getCtx.Value(contextStoreKey{}) != "this call" {
		t.Errorf("GetContext was given %v, want the context of the call", store.getCtx)
	}
	if store.setCtx == nil || store.setCtx.Value(contextStoreKey{}) != "this call" {
		t.Errorf("SetContext was given %v, want the context of the call", store.setCtx)
	}
	if store.plain != 0 {
		t.Errorf("the plain Get and Set were called %d times beside the context forms, want 0", store.plain)
	}
}

// A store behind a network that takes 300 ms, and a call that has 50: the call
// ends when its deadline does, with the context's error, and nothing is sent.
func TestCacheStopsWaitingForAContextStoreWhenTheDeadlinePasses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &contextStore{readTime: 300 * time.Millisecond}
		var calls atomic.Int32
		provider := completeFunc(func(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
			calls.Add(1)
			return llm.CompletionResponse{Content: "paid for"}, nil
		})
		wrapped := Cache(WithCacheStore(store))(provider)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		start := time.Now()
		_, err := wrapped.Complete(ctx, llm.CompletionRequest{Model: "m", UserPrompt: "q"})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want the deadline", err)
		}
		if took := time.Since(start); took != 50*time.Millisecond {
			t.Errorf("the call took %v, want the 50 ms it had", took)
		}
		if calls.Load() != 0 {
			t.Errorf("the provider was asked %d times after the deadline, want 0", calls.Load())
		}
	})
}

// A plain store cannot be given up on, but the answer it gives is not used when
// the call's context ended while it was read.
func TestCacheDoesNotSendACallWhoseContextEndedWhileTheStoreWasRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	provider := completeFunc(func(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
		calls.Add(1)
		return llm.CompletionResponse{Content: "paid for"}, nil
	})
	store := &slowStore{onGet: cancel}
	wrapped := Cache(WithCacheStore(store))(provider)

	_, err := wrapped.Complete(ctx, llm.CompletionRequest{Model: "m", UserPrompt: "q"})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want the cancellation", err)
	}
	if calls.Load() != 0 {
		t.Errorf("the provider was asked %d times for a call that was over, want 0", calls.Load())
	}
}

// slowStore misses, and runs onGet while it does.
type slowStore struct{ onGet func() }

func (s *slowStore) Get(string) (llm.CompletionResponse, bool) {
	s.onGet()
	return llm.CompletionResponse{}, false
}
func (s *slowStore) Set(string, llm.CompletionResponse, time.Duration) {}

// panickingStore panics in the methods named.
type panickingStore struct {
	countingStore
	onGet, onSet bool
}

func (s *panickingStore) Get(key string) (llm.CompletionResponse, bool) {
	if s.onGet {
		panic("store down")
	}
	return s.countingStore.Get(key)
}

func (s *panickingStore) Set(key string, resp llm.CompletionResponse, ttl time.Duration) {
	if s.onSet {
		panic("store down")
	}
	s.countingStore.Set(key, resp, ttl)
}

// A store of the caller's is the caller's code. A read that panics is a miss, and
// a write that panics loses the entry and leaves the answer, which was paid for,
// with the call that paid for it.
func TestCacheSurvivesAStoreThatPanics(t *testing.T) {
	cases := []struct {
		name         string
		onGet, onSet bool
	}{
		{"a read that panics", true, false},
		{"a write that panics", false, true},
		{"both", true, true},
		{"neither", false, false},
	}
	for _, tc := range cases {
		for _, repeats := range []int{1, 3} {
			t.Run(fmt.Sprintf("%s, %d calls", tc.name, repeats), func(t *testing.T) {
				var calls atomic.Int32
				provider := completeFunc(func(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
					calls.Add(1)
					return llm.CompletionResponse{Content: "paid for"}, nil
				})
				store := &panickingStore{onGet: tc.onGet, onSet: tc.onSet}
				wrapped := Cache(WithCacheStore(store))(provider)
				for i := range repeats {
					resp, err := wrapped.Complete(context.Background(), llm.CompletionRequest{Model: "m", UserPrompt: "q"})
					if err != nil || resp.Content != "paid for" {
						t.Fatalf("call %d: %q, %v, want the answer that was paid for", i, resp.Content, err)
					}
				}
				// With nothing that works, every call is a miss and pays.
				wantCalls := repeats
				if !tc.onGet && !tc.onSet {
					wantCalls = 1
				}
				if tc.onSet && !tc.onGet {
					wantCalls = repeats
				}
				if int(calls.Load()) != wantCalls {
					t.Errorf("the provider was asked %d times, want %d", calls.Load(), wantCalls)
				}
			})
		}
	}
}

// A hit sent no copy of anything. The request that filled the cache may have been
// hedged, and the answer it stored says so; a hit does not.
func TestCacheAHitIsNotMarkedHedged(t *testing.T) {
	provider := completeFunc(func(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
		return llm.CompletionResponse{Content: "answered", Hedged: true}, nil
	})
	wrapped := Cache()(provider)
	req := llm.CompletionRequest{Model: "m", UserPrompt: "q"}

	first, err := wrapped.Complete(context.Background(), req)
	if err != nil || !first.Hedged || first.Cached {
		t.Fatalf("the call that paid: hedged %v, cached %v, err %v, want hedged and not cached", first.Hedged, first.Cached, err)
	}
	hit, err := wrapped.Complete(context.Background(), req)
	if err != nil || !hit.Cached || hit.Hedged {
		t.Errorf("the hit: hedged %v, cached %v, err %v, want cached and not hedged", hit.Hedged, hit.Cached, err)
	}
}

func TestCacheCloneResponseKeepsNoHedgedMarkOnAHit(t *testing.T) {
	cases := []llm.CompletionResponse{
		{Content: "a", Hedged: true},
		{Content: "a", Hedged: false},
		{Content: "a", Hedged: true, Usage: types.TokenUsage{PromptTokens: 3}},
		{Content: "a", Hedged: true, Timing: types.Timing{Network: time.Second}},
		{Hedged: true, ToolCalls: []llm.ToolCall{{ID: "1", Name: "n"}}},
		{Hedged: true, DecisionAnswers: []llm.DecisionAnswer{{Kind: llm.DecisionYesNo, Yes: true}}},
		{Hedged: true, DecisionEscalation: &llm.DecisionEscalation{Provider: "p"}},
		{Content: "a", Hedged: true, Cached: true},
		{Content: "a", Hedged: true, FinishReason: "stop"},
		{Content: "a", Hedged: true, Model: "m", Provider: "p"},
	}
	for i, stored := range cases {
		hit := cacheHitResponse(stored)
		if hit.Hedged || !hit.Cached || hit.Usage != (types.TokenUsage{}) || hit.Timing != (types.Timing{}) {
			t.Errorf("case %d: a hit is %+v, want cached, not hedged, with no usage or timing", i, hit)
		}
		if hit.Content != stored.Content || hit.Model != stored.Model {
			t.Errorf("case %d: a hit changed the content or the model: %+v", i, hit)
		}
	}
}
