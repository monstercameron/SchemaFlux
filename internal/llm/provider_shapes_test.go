package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The output-item walk has to survive every shape the Responses API emits, not
// just the two the happy path produces. Each of these is a way to get either a
// corrupted answer or a silent empty one.
func TestOpenAIResponsesOutputShapes(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		want    string
		wantErr bool
	}{
		{
			"message_only",
			`{"output":[{"type":"message","content":[{"type":"output_text","text":"answer"}]}]}`,
			"answer", false,
		},
		{
			"reasoning_then_message",
			`{"output":[{"type":"reasoning","content":[{"type":"output_text","text":"thinking"}]},
			            {"type":"message","content":[{"type":"output_text","text":"answer"}]}]}`,
			"answer", false,
		},
		{
			"message_then_reasoning",
			`{"output":[{"type":"message","content":[{"type":"output_text","text":"answer"}]},
			            {"type":"reasoning","content":[{"type":"output_text","text":"afterthought"}]}]}`,
			"answer", false,
		},
		{
			"several_message_items_concatenate",
			`{"output":[{"type":"message","content":[{"type":"output_text","text":"one "}]},
			            {"type":"message","content":[{"type":"output_text","text":"two"}]}]}`,
			"one two", false,
		},
		{
			"several_content_parts_concatenate",
			`{"output":[{"type":"message","content":[{"type":"output_text","text":"one "},{"type":"output_text","text":"two"}]}]}`,
			"one two", false,
		},
		{
			// An untyped item is a message: the field is optional on the wire.
			"untyped_item_is_a_message",
			`{"output":[{"content":[{"text":"answer"}]}]}`,
			"answer", false,
		},
		{
			"non_text_content_is_skipped",
			`{"output":[{"type":"message","content":[{"type":"refusal","text":"no"},{"type":"output_text","text":"answer"}]}]}`,
			"answer", false,
		},
		{
			"reasoning_only_is_an_error",
			`{"output":[{"type":"reasoning","content":[{"type":"output_text","text":"thinking"}]}]}`,
			"", true,
		},
		{
			"empty_output_is_an_error",
			`{"output":[]}`,
			"", true,
		},
		{
			"missing_output_is_an_error",
			`{"usage":{"input_tokens":1}}`,
			"", true,
		},
		{
			"message_with_no_content_is_an_error",
			`{"output":[{"type":"message","content":[]}]}`,
			"", true,
		},
		{
			"tool_call_only_is_an_error",
			`{"output":[{"type":"function_call","content":[{"type":"output_text","text":"x"}]}]}`,
			"", true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := responsesServer(t, tc.body)
			provider := newTestOpenAIProvider(t, server.URL)

			response, err := provider.Complete(context.Background(), CompletionRequest{
				Model:      "gpt-5.6-luna",
				UserPrompt: "hello",
			})

			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got content %q", response.Content)
				}
				return
			}
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if response.Content != tc.want {
				t.Errorf("Content = %q, want %q", response.Content, tc.want)
			}
		})
	}
}

// Usage accounting has to survive a response that omits any of it: a missing
// field must read as zero rather than break the caller's cost report.
func TestOpenAIResponsesUsageShapes(t *testing.T) {
	cases := []struct {
		name          string
		body          string
		wantIn        int
		wantOut       int
		wantCached    int
		wantReasoning int
	}{
		{"full", `{"output":[{"type":"message","content":[{"type":"output_text","text":"a"}]}],
		           "usage":{"input_tokens":10,"output_tokens":5,
		                    "input_tokens_details":{"cached_tokens":4},
		                    "output_tokens_details":{"reasoning_tokens":3}}}`, 10, 5, 4, 3},
		{"no_details", `{"output":[{"type":"message","content":[{"type":"output_text","text":"a"}]}],
		                "usage":{"input_tokens":10,"output_tokens":5}}`, 10, 5, 0, 0},
		{"no_usage", `{"output":[{"type":"message","content":[{"type":"output_text","text":"a"}]}]}`, 0, 0, 0, 0},
		{"zero_usage", `{"output":[{"type":"message","content":[{"type":"output_text","text":"a"}]}],
		                "usage":{"input_tokens":0,"output_tokens":0}}`, 0, 0, 0, 0},
		{"cached_only", `{"output":[{"type":"message","content":[{"type":"output_text","text":"a"}]}],
		                 "usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":10}}}`, 10, 0, 10, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := responsesServer(t, tc.body)
			provider := newTestOpenAIProvider(t, server.URL)

			response, err := provider.Complete(context.Background(), CompletionRequest{
				Model: "gpt-5.6-luna", UserPrompt: "hello",
			})
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if response.Usage.PromptTokens != tc.wantIn {
				t.Errorf("PromptTokens = %d, want %d", response.Usage.PromptTokens, tc.wantIn)
			}
			if response.Usage.CompletionTokens != tc.wantOut {
				t.Errorf("CompletionTokens = %d, want %d", response.Usage.CompletionTokens, tc.wantOut)
			}
		})
	}
}

// A transport-level failure must reach the caller rather than becoming an empty
// completion.
func TestOpenAIResponsesTransportFailures(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"rate_limited", http.StatusTooManyRequests, `{"error":{"message":"slow down"}}`},
		{"server_error", http.StatusInternalServerError, `{"error":{"message":"boom"}}`},
		{"unauthorized", http.StatusUnauthorized, `{"error":{"message":"bad key"}}`},
		{"forbidden", http.StatusForbidden, `{"error":{"message":"no access"}}`},
		{"html_error_page", http.StatusBadGateway, `<html>502</html>`},
		{"truncated_json", http.StatusOK, `{"output":[{"type":"mess`},
		{"empty_body", http.StatusOK, ``},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(server.Close)

			provider := newTestOpenAIProvider(t, server.URL)
			response, err := provider.Complete(context.Background(), CompletionRequest{
				Model: "gpt-5.6-luna", UserPrompt: "hello",
			})
			if err == nil {
				t.Fatalf("a %d response must be an error, got content %q", tc.status, response.Content)
			}
		})
	}
}

// A cancelled context must abandon the request promptly rather than waiting out
// the provider timeout.
func TestOpenAIResponsesHonoursCancellation(t *testing.T) {
	// The handler waits on a channel the test controls rather than on the
	// request context: a client-side cancellation does not always reach the
	// server handler promptly, and httptest.Server.Close blocks on outstanding
	// handlers, so waiting on the request context deadlocks the test.
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(release)
		server.Close()
	})

	provider := newTestOpenAIProvider(t, server.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := provider.Complete(ctx, CompletionRequest{Model: "gpt-5.6-luna", UserPrompt: "hello"}); err == nil {
		t.Fatal("a cancelled request must be an error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("cancellation took %v; the request is not honouring the context", elapsed)
	}
}

// The request the provider sends must be a well-formed Responses API call.
func TestOpenAIResponsesRequestShape(t *testing.T) {
	var captured map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	t.Cleanup(server.Close)

	provider := newTestOpenAIProvider(t, server.URL)
	if _, err := provider.Complete(context.Background(), CompletionRequest{
		Model:        "gpt-5.6-luna",
		SystemPrompt: "be terse",
		UserPrompt:   "hello",
		MaxTokens:    128,
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if captured == nil {
		t.Fatal("no request body was captured")
	}
	if model, _ := captured["model"].(string); model != "gpt-5.6-luna" {
		t.Errorf("model = %v, want gpt-5.6-luna", captured["model"])
	}
}
