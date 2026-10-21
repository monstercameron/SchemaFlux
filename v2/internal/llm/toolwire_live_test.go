package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestLiveToolWireOpenAI checks the call/result round trip against OpenAI's own
// Responses endpoint. It uses the same paid-test gate as live conformance.
func TestLiveToolWireOpenAI(t *testing.T) {
	apiKey, model := requireLiveConformance(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	provider, err := NewOpenAIProvider(ProviderConfig{
		APIKey: apiKey, BaseURL: "https://api.openai.com/v1", Timeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	tool := Tool{Name: "lookup_code", Description: "Look up the fixed result for a code.", Parameters: map[string]any{
		"type": "object", "properties": map[string]any{"code": map[string]any{"type": "string"}},
		"required": []string{"code"}, "additionalProperties": false,
	}}
	first := CompletionRequest{
		Model: model, MaxTokens: 256, Tools: []Tool{tool},
		SystemPrompt: "Use lookup_code for the requested code. After its result arrives, answer with exactly that result.",
		Messages:     []Message{{Role: "user", Content: "Call lookup_code for code A7."}},
	}
	called, err := provider.Complete(ctx, first)
	if err != nil {
		t.Fatalf("first tool turn: %v", err)
	}
	if len(called.ToolCalls) != 1 || called.ToolCalls[0].Name != tool.Name {
		t.Fatalf("first turn returned %d calls; want one lookup_code call", len(called.ToolCalls))
	}
	call := called.ToolCalls[0]
	if call.ID == "" || !json.Valid(call.Arguments) {
		t.Fatal("tool call has no usable id or arguments")
	}
	first.Messages = append(first.Messages,
		Message{Role: "assistant", Content: called.Content, ToolCalls: called.ToolCalls},
		Message{Role: "tool", ToolCallID: call.ID, Content: "CHECK-4827"},
	)
	answered, err := provider.Complete(ctx, first)
	if err != nil {
		t.Fatalf("tool result turn: %v", err)
	}
	if !strings.Contains(answered.Content, "CHECK-4827") {
		t.Fatalf("tool result did not reach the final answer (content length %d, calls %d)", len(answered.Content), len(answered.ToolCalls))
	}
}

// TestLiveOpenAIRefusesNonStrictFunctionParameters confirms the endpoint's
// strict-schema rule directly. The library refuses this request locally, so
// this test sends only the deliberately invalid declaration over raw HTTP.
func TestLiveOpenAIRefusesNonStrictFunctionParameters(t *testing.T) {
	apiKey, model := requireLiveConformance(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	body, err := json.Marshal(map[string]any{
		"model": model, "input": "Call lookup_code for A7.", "store": false, "max_output_tokens": 64,
		"tools": []map[string]any{{
			"type": "function", "name": "lookup_code", "strict": true,
			"parameters": map[string]any{
				"type": "object", "properties": map[string]any{"code": map[string]any{"type": "string"}},
				"required": []string{}, "additionalProperties": false,
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/responses", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("strict-schema request: %v", err)
	}
	defer resp.Body.Close()
	reply, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("strict-schema request status = %d, want 400", resp.StatusCode)
	}
	if lower := strings.ToLower(string(reply)); !strings.Contains(lower, "schema") && !strings.Contains(lower, "parameters") {
		t.Fatal("the 400 response did not identify the function schema or parameters")
	}
}
