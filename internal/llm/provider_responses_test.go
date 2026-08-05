package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// responsesServer stands up a fake Responses API endpoint returning body verbatim.
func responsesServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			t.Errorf("expected the Responses API path, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func newTestOpenAIProvider(t *testing.T, serverURL string) *OpenAIProvider {
	t.Helper()
	provider, err := NewOpenAIProvider(ProviderConfig{
		APIKey:  "test-key",
		BaseURL: serverURL,
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}
	return provider
}

// A reasoning model returns reasoning items alongside the message item. Only the
// message text is the answer; concatenating every item corrupts a JSON payload.
func TestOpenAIResponsesIgnoresReasoningOutputItems(t *testing.T) {
	body := `{
	  "id": "resp_1",
	  "status": "completed",
	  "model": "gpt-5.6-luna",
	  "output": [
	    {"type": "reasoning", "content": [{"type": "reasoning_text", "text": "First I identify the name, then the age."}]},
	    {"type": "message", "content": [{"type": "output_text", "text": "{\"name\":\"John\",\"age\":30}"}]}
	  ],
	  "usage": {"input_tokens": 11, "output_tokens": 7, "total_tokens": 18}
	}`

	provider := newTestOpenAIProvider(t, responsesServer(t, body).URL)
	resp, err := provider.Complete(context.Background(), CompletionRequest{
		Model:      "gpt-5.6-luna",
		UserPrompt: "John Smith is 30 years old.",
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	const want = `{"name":"John","age":30}`
	if resp.Content != want {
		t.Fatalf("content must contain only the message text.\n got: %q\nwant: %q", resp.Content, want)
	}

	// The decisive assertion: the result must be parseable. Concatenating the
	// reasoning item leaves leading prose and json.Unmarshal fails.
	var parsed struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}
	if err := json.Unmarshal([]byte(resp.Content), &parsed); err != nil {
		t.Fatalf("content is not valid JSON, reasoning text leaked in: %v", err)
	}
	if parsed.Name != "John" || parsed.Age != 30 {
		t.Fatalf("unexpected parse result: %+v", parsed)
	}
}

// Reasoning tokens are billed. Not reading them makes every cost figure on a
// reasoning model wrong, and cached tokens are reported by CostSummary but never
// populated by any provider.
func TestOpenAIResponsesParsesTokenDetails(t *testing.T) {
	body := `{
	  "id": "resp_2",
	  "status": "completed",
	  "model": "gpt-5.6-luna",
	  "output": [{"type": "message", "content": [{"type": "output_text", "text": "ok"}]}],
	  "usage": {
	    "input_tokens": 1200,
	    "output_tokens": 340,
	    "total_tokens": 1540,
	    "input_tokens_details": {"cached_tokens": 1024},
	    "output_tokens_details": {"reasoning_tokens": 256}
	  }
	}`

	provider := newTestOpenAIProvider(t, responsesServer(t, body).URL)
	resp, err := provider.Complete(context.Background(), CompletionRequest{Model: "gpt-5.6-luna"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if resp.Usage.CachedTokens != 1024 {
		t.Errorf("CachedTokens = %d, want 1024", resp.Usage.CachedTokens)
	}
	if resp.Usage.ReasoningTokens != 256 {
		t.Errorf("ReasoningTokens = %d, want 256", resp.Usage.ReasoningTokens)
	}
	if resp.Usage.PromptTokens != 1200 || resp.Usage.CompletionTokens != 340 {
		t.Errorf("base token counts wrong: %+v", resp.Usage)
	}
}

// A truncated response must be distinguishable from a complete one. Hardcoding
// "stop" hides the one failure a caller can actually act on.
func TestOpenAIResponsesReportsTruncationFinishReason(t *testing.T) {
	body := `{
	  "id": "resp_3",
	  "status": "incomplete",
	  "incomplete_details": {"reason": "max_output_tokens"},
	  "model": "gpt-5.6-luna",
	  "output": [{"type": "message", "content": [{"type": "output_text", "text": "{\"name\":\"Jo"}]}],
	  "usage": {"input_tokens": 10, "output_tokens": 4, "total_tokens": 14}
	}`

	provider := newTestOpenAIProvider(t, responsesServer(t, body).URL)
	resp, err := provider.Complete(context.Background(), CompletionRequest{Model: "gpt-5.6-luna"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.FinishReason == "stop" {
		t.Fatalf("FinishReason must reflect truncation, got %q", resp.FinishReason)
	}
	if resp.FinishReason != "max_output_tokens" {
		t.Errorf("FinishReason = %q, want %q", resp.FinishReason, "max_output_tokens")
	}
}

// Output items with no message content must not be reported as a successful
// empty completion.
func TestOpenAIResponsesReasoningOnlyIsAnError(t *testing.T) {
	body := `{
	  "id": "resp_4",
	  "status": "completed",
	  "model": "gpt-5.6-luna",
	  "output": [{"type": "reasoning", "content": [{"type": "reasoning_text", "text": "thinking"}]}],
	  "usage": {"input_tokens": 5, "output_tokens": 0, "total_tokens": 5}
	}`

	provider := newTestOpenAIProvider(t, responsesServer(t, body).URL)
	if _, err := provider.Complete(context.Background(), CompletionRequest{Model: "gpt-5.6-luna"}); err == nil {
		t.Fatal("a response carrying no message item must return an error, not empty content")
	}
}

// One http.Client per provider, not one per request: a fresh client per call
// defeats connection reuse.
func TestOpenAIProviderReusesHTTPClient(t *testing.T) {
	body := `{"id":"r","status":"completed","model":"gpt-5.6-luna",
	  "output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],
	  "usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`

	provider := newTestOpenAIProvider(t, responsesServer(t, body).URL)
	if provider.httpClient == nil {
		t.Fatal("provider must hold a reusable http.Client")
	}
	first := provider.httpClient
	for i := 0; i < 3; i++ {
		if _, err := provider.Complete(context.Background(), CompletionRequest{Model: "gpt-5.6-luna"}); err != nil {
			t.Fatalf("Complete: %v", err)
		}
	}
	if provider.httpClient != first {
		t.Error("http.Client must not be replaced between requests")
	}
}
