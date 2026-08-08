package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ST-003 / I-09. Anthropic's Messages API reports truncation as
// `"stop_reason":"max_tokens"`, exactly the way OpenAI reports "length" -- but
// AnthropicProvider.Complete used to hardcode FinishReason to "stop"
// unconditionally, discarding the field entirely. That made a truncated
// Anthropic answer indistinguishable from a complete one all the way to
// ParseJSON, which is the same bug I-09 describes for OpenAI, just on the
// other provider.
// newTestAnthropicProvider is declared in provider_cache_test.go and reused
// here.

func anthropicFinishReasonServer(t *testing.T, body string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func TestAnthropicProviderReportsMaxTokensStopReason(t *testing.T) {
	body := `{
	  "id": "msg_1",
	  "content": [{"type": "text", "text": "{\"name\":\"Jo"}],
	  "stop_reason": "max_tokens",
	  "model": "claude-3-5-sonnet-20240620",
	  "usage": {"input_tokens": 10, "output_tokens": 4}
	}`

	provider := newTestAnthropicProvider(t, anthropicFinishReasonServer(t, body))
	resp, err := provider.Complete(context.Background(), CompletionRequest{Model: "claude-3-5-sonnet-20240620"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.FinishReason != "max_tokens" {
		t.Fatalf("FinishReason = %q, want %q", resp.FinishReason, "max_tokens")
	}

	// The decisive assertion for I-09: the classifier this library actually
	// uses must recognize the reason Complete reported.
	if kind := ClassifyCompletion(resp); kind.String() != "output truncated" {
		t.Errorf("ClassifyCompletion(%+v) = %v, want output truncated", resp, kind)
	}
}

// A normal completion's stop_reason ("end_turn") must pass through rather than
// being discarded in favor of the old hardcoded "stop" -- and must not be
// mistaken for truncation.
func TestAnthropicProviderReportsEndTurnStopReason(t *testing.T) {
	body := `{
	  "id": "msg_2",
	  "content": [{"type": "text", "text": "a complete answer"}],
	  "stop_reason": "end_turn",
	  "model": "claude-3-5-sonnet-20240620",
	  "usage": {"input_tokens": 10, "output_tokens": 4}
	}`

	provider := newTestAnthropicProvider(t, anthropicFinishReasonServer(t, body))
	resp, err := provider.Complete(context.Background(), CompletionRequest{Model: "claude-3-5-sonnet-20240620"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.FinishReason != "end_turn" {
		t.Fatalf("FinishReason = %q, want %q", resp.FinishReason, "end_turn")
	}
	if kind := ClassifyCompletion(resp); kind.String() != "unknown" {
		t.Errorf("ClassifyCompletion(%+v) = %v, want unknown (not a failure)", resp, kind)
	}
}

// A response carrying no stop_reason at all (an older API version, a mock)
// must still default to "stop" rather than an empty string, so existing
// callers that never populated the field keep the behaviour they had.
func TestAnthropicProviderDefaultsToStopWhenReasonAbsent(t *testing.T) {
	body := `{
	  "id": "msg_3",
	  "content": [{"type": "text", "text": "ok"}],
	  "model": "claude-3-5-sonnet-20240620",
	  "usage": {"input_tokens": 1, "output_tokens": 1}
	}`

	provider := newTestAnthropicProvider(t, anthropicFinishReasonServer(t, body))
	resp, err := provider.Complete(context.Background(), CompletionRequest{Model: "claude-3-5-sonnet-20240620"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.FinishReason != "stop" {
		t.Fatalf("FinishReason = %q, want %q", resp.FinishReason, "stop")
	}
}
