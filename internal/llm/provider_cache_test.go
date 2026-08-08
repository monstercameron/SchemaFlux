package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// captureOpenAIRequest stands up a fake Responses endpoint that decodes and
// returns every request body it receives, so a test can assert on the exact
// field the provider sent -- the recorded-request boundary the P-009 verify
// line is about.
func captureOpenAIRequest(t *testing.T) (*httptest.Server, *map[string]any) {
	t.Helper()
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	t.Cleanup(server.Close)
	return server, &captured
}

// TestOpenAIResponsesSendsPromptCacheKey is P-009's provider-layer half: a
// CompletionRequest carrying PromptCacheKey must reach the wire as
// prompt_cache_key. Before this field existed, the Responses request had no
// way to hint which backend replica already held the request's stable
// prefix, so a repeat request could route to a cold one and pay full price.
func TestOpenAIResponsesSendsPromptCacheKey(t *testing.T) {
	server, captured := captureOpenAIRequest(t)
	provider := newTestOpenAIProvider(t, server.URL)

	if _, err := provider.Complete(context.Background(), CompletionRequest{
		Model:          "gpt-5.6-luna",
		SystemPrompt:   "be terse",
		UserPrompt:     "hello",
		PromptCacheKey: "extract:v1:Person:v1:abc123:json-schema-2020-12-strict:gpt-5.6-luna:deadbeef:strict:json",
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	got, _ := (*captured)["prompt_cache_key"].(string)
	if got != "extract:v1:Person:v1:abc123:json-schema-2020-12-strict:gpt-5.6-luna:deadbeef:strict:json" {
		t.Fatalf("prompt_cache_key = %q, want the key to reach the request verbatim", got)
	}
}

// TestOpenAIResponsesOmitsPromptCacheKeyWhenUnset must not send an empty or
// placeholder value: a caller that never set PromptCacheKey (schemafluxtest's
// fake provider, a hand-built CompletionRequest, an operation that has not
// been wired to compute one yet) should produce a request identical to
// before this field existed, not a request carrying a hint the provider has
// to specially treat as meaningless.
func TestOpenAIResponsesOmitsPromptCacheKeyWhenUnset(t *testing.T) {
	server, captured := captureOpenAIRequest(t)
	provider := newTestOpenAIProvider(t, server.URL)

	if _, err := provider.Complete(context.Background(), CompletionRequest{
		Model:        "gpt-5.6-luna",
		SystemPrompt: "be terse",
		UserPrompt:   "hello",
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if _, present := (*captured)["prompt_cache_key"]; present {
		t.Fatalf("expected prompt_cache_key to be omitted, request had: %v", *captured)
	}
}

// TestOpenAIResponsesPromptCacheKeyDiffersAcrossRequests: two Complete calls
// with different keys must produce different wire values -- a sanity check
// that nothing normalizes or truncates the key on the way out.
func TestOpenAIResponsesPromptCacheKeyDiffersAcrossRequests(t *testing.T) {
	server, captured := captureOpenAIRequest(t)
	provider := newTestOpenAIProvider(t, server.URL)

	if _, err := provider.Complete(context.Background(), CompletionRequest{
		Model: "gpt-5.6-luna", UserPrompt: "hello", PromptCacheKey: "key-one",
	}); err != nil {
		t.Fatalf("Complete (first): %v", err)
	}
	first, _ := (*captured)["prompt_cache_key"].(string)

	if _, err := provider.Complete(context.Background(), CompletionRequest{
		Model: "gpt-5.6-luna", UserPrompt: "hello", PromptCacheKey: "key-two",
	}); err != nil {
		t.Fatalf("Complete (second): %v", err)
	}
	second, _ := (*captured)["prompt_cache_key"].(string)

	if first == second {
		t.Fatalf("expected different keys to reach the wire differently, both were %q", first)
	}
}

// captureAnthropicRequest is captureOpenAIRequest's counterpart for the
// Anthropic Messages API.
func captureAnthropicRequest(t *testing.T) (*httptest.Server, *map[string]any) {
	t.Helper()
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	t.Cleanup(server.Close)
	return server, &captured
}

func newTestAnthropicProvider(t *testing.T, serverURL string) *AnthropicProvider {
	t.Helper()
	provider, err := NewAnthropicProvider(ProviderConfig{APIKey: "test-key", BaseURL: serverURL})
	if err != nil {
		t.Fatalf("NewAnthropicProvider: %v", err)
	}
	return provider
}

// TestAnthropicSendsConfiguredMaxTokensCeiling is half of P-016's verify
// line. The provider used to hardcode max_tokens to 1024 and only override it
// when the request set one; this asserts the ceiling the request actually
// carries reaches the wire, for several values including ones the old
// hardcoded default would not have coincidentally matched.
func TestAnthropicSendsConfiguredMaxTokensCeiling(t *testing.T) {
	cases := []struct {
		name      string
		reqTokens int
		want      float64
	}{
		{"tier default smart", 4000, 4000},
		{"tier default fast", 2000, 2000},
		{"tier default quick", 1000, 1000},
		{"arbitrary caller ceiling", 8192, 8192},
		{"small ceiling", 16, 16},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, captured := captureAnthropicRequest(t)
			provider := newTestAnthropicProvider(t, server.URL)

			if _, err := provider.Complete(context.Background(), CompletionRequest{
				SystemPrompt: "be terse",
				UserPrompt:   "hello",
				MaxTokens:    tc.reqTokens,
			}); err != nil {
				t.Fatalf("Complete: %v", err)
			}

			got, _ := (*captured)["max_tokens"].(float64)
			if got != tc.want {
				t.Fatalf("max_tokens = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAnthropicFallsBackToDefaultMaxTokensWhenUnset: a request that never set
// a ceiling at all (MaxTokens <= 0) must still produce a valid request rather
// than sending zero, which Anthropic rejects.
func TestAnthropicFallsBackToDefaultMaxTokensWhenUnset(t *testing.T) {
	server, captured := captureAnthropicRequest(t)
	provider := newTestAnthropicProvider(t, server.URL)

	if _, err := provider.Complete(context.Background(), CompletionRequest{
		SystemPrompt: "be terse",
		UserPrompt:   "hello",
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	got, _ := (*captured)["max_tokens"].(float64)
	if got != 1024 {
		t.Fatalf("max_tokens = %v, want the documented fallback of 1024", got)
	}
}

// TestAnthropicMarksSystemPromptAsCacheBreakpoint is the other half of
// P-016's verify line: when a system prompt is present it is the last stable
// block, and it must carry a cache_control breakpoint so a repeat call reads
// from the cache instead of paying full price for an identical prefix every
// time. Before this fix the provider never sent cache_control anywhere, so
// this assertion fails against the old behaviour unconditionally.
func TestAnthropicMarksSystemPromptAsCacheBreakpoint(t *testing.T) {
	server, captured := captureAnthropicRequest(t)
	provider := newTestAnthropicProvider(t, server.URL)

	if _, err := provider.Complete(context.Background(), CompletionRequest{
		SystemPrompt: "You are a helpful extraction assistant.",
		UserPrompt:   "hello",
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	system, ok := (*captured)["system"].([]any)
	if !ok || len(system) == 0 {
		t.Fatalf("expected system to be a non-empty content block array, got %v", (*captured)["system"])
	}
	last, ok := system[len(system)-1].(map[string]any)
	if !ok {
		t.Fatalf("expected the last system block to be an object, got %v", system[len(system)-1])
	}
	if last["text"] != "You are a helpful extraction assistant." {
		t.Fatalf("expected the system text to be preserved, got %v", last["text"])
	}
	cacheControl, ok := last["cache_control"].(map[string]any)
	if !ok || cacheControl["type"] != "ephemeral" {
		t.Fatalf("expected an ephemeral cache_control breakpoint on the last system block, got %v", last["cache_control"])
	}
}

// TestAnthropicMarksUserMessageAsCacheBreakpointWithoutSystemPrompt: when
// there is no system prompt, the user message is the only block there is, so
// the breakpoint has to move there instead of being silently dropped.
func TestAnthropicMarksUserMessageAsCacheBreakpointWithoutSystemPrompt(t *testing.T) {
	server, captured := captureAnthropicRequest(t)
	provider := newTestAnthropicProvider(t, server.URL)

	if _, err := provider.Complete(context.Background(), CompletionRequest{
		UserPrompt: "hello, no system prompt here",
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if _, present := (*captured)["system"]; present {
		t.Fatalf("expected no system field when SystemPrompt is empty, got %v", (*captured)["system"])
	}

	messages, ok := (*captured)["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("expected exactly one message, got %v", (*captured)["messages"])
	}
	msg, ok := messages[0].(map[string]any)
	if !ok {
		t.Fatalf("expected message to be an object, got %v", messages[0])
	}
	content, ok := msg["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("expected message content to be a non-empty block array, got %v", msg["content"])
	}
	last, ok := content[len(content)-1].(map[string]any)
	if !ok {
		t.Fatalf("expected the last content block to be an object, got %v", content[len(content)-1])
	}
	cacheControl, ok := last["cache_control"].(map[string]any)
	if !ok || cacheControl["type"] != "ephemeral" {
		t.Fatalf("expected an ephemeral cache_control breakpoint on the last user block, got %v", last["cache_control"])
	}
}

// TestAnthropicUserMessageStaysPlainStringWhenSystemPromptCarriesTheBreakpoint
// guards against a regression that would put the breakpoint on both blocks:
// when a system prompt is present, the user message content stays a plain
// string, matching the shape every existing Anthropic test already asserts.
func TestAnthropicUserMessageStaysPlainStringWhenSystemPromptCarriesTheBreakpoint(t *testing.T) {
	server, captured := captureAnthropicRequest(t)
	provider := newTestAnthropicProvider(t, server.URL)

	if _, err := provider.Complete(context.Background(), CompletionRequest{
		SystemPrompt: "be terse",
		UserPrompt:   "hello",
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	messages, ok := (*captured)["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("expected exactly one message, got %v", (*captured)["messages"])
	}
	msg, ok := messages[0].(map[string]any)
	if !ok {
		t.Fatalf("expected message to be an object, got %v", messages[0])
	}
	content, ok := msg["content"].(string)
	if !ok || content != "hello" {
		t.Fatalf("expected plain string user content, got %v", msg["content"])
	}
}
