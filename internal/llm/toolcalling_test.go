package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newCapturingServer returns an httptest.Server that decodes every request
// body into capturedBody (last write wins, which every test here only
// calls once) and answers with the given raw JSON payload.
func newCapturingServer(t *testing.T, responseBody string, capturedBody *map[string]interface{}) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if capturedBody != nil {
			if err := json.NewDecoder(r.Body).Decode(capturedBody); err != nil {
				t.Errorf("failed to decode request body: %v", err)
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(responseBody))
	}))
	t.Cleanup(server.Close)
	return server
}

const plainTextResponse = `{
	"id": "resp_1",
	"status": "completed",
	"output": [
		{
			"type": "message",
			"content": [
				{"type": "output_text", "text": "hello there"}
			]
		}
	],
	"model": "gpt-5.6-luna",
	"usage": {"input_tokens": 5, "output_tokens": 3, "total_tokens": 8}
}`

// TestBuildResponsesRequestBodyOmitsToolsWhenUnset proves a request that
// never mentions tools produces the exact wire body it produced before tool
// calling existed -- no "tools" or "tool_choice" key at all, not an empty
// array or an implicit "auto".
func TestBuildResponsesRequestBodyOmitsToolsWhenUnset(t *testing.T) {
	provider := &OpenAIProvider{config: ProviderConfig{}}
	body := provider.buildResponsesRequestBody(CompletionRequest{
		Model:      "gpt-5.6-luna",
		UserPrompt: "hi",
	}, false)

	if _, ok := body["tools"]; ok {
		t.Errorf("expected no \"tools\" key, got %v", body["tools"])
	}
	if _, ok := body["tool_choice"]; ok {
		t.Errorf("expected no \"tool_choice\" key, got %v", body["tool_choice"])
	}
}

// TestBuildResponsesRequestBodySendsToolsInAPIShape proves a declared tool
// reaches the wire as the Responses API's function-tool shape: type,
// name, description, and parameters as the caller's schema.
func TestBuildResponsesRequestBodySendsToolsInAPIShape(t *testing.T) {
	provider := &OpenAIProvider{config: ProviderConfig{}}
	body := provider.buildResponsesRequestBody(CompletionRequest{
		Model:      "gpt-5.6-luna",
		UserPrompt: "what's the weather",
		Tools: []Tool{
			{
				Name:        "get_weather",
				Description: "Look up the current weather for a city.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"city": map[string]any{"type": "string"},
					},
					"required": []string{"city"},
				},
			},
		},
	}, false)

	tools, ok := body["tools"].([]map[string]interface{})
	if !ok || len(tools) != 1 {
		t.Fatalf("expected one tool in wire body, got %#v", body["tools"])
	}
	tool := tools[0]
	if tool["type"] != "function" {
		t.Errorf("tool.type = %v, want %q", tool["type"], "function")
	}
	if tool["name"] != "get_weather" {
		t.Errorf("tool.name = %v, want %q", tool["name"], "get_weather")
	}
	if tool["description"] != "Look up the current weather for a city." {
		t.Errorf("tool.description = %v", tool["description"])
	}
	params, ok := tool["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("tool.parameters is not a map: %#v", tool["parameters"])
	}
	if params["type"] != "object" {
		t.Errorf("tool.parameters.type = %v, want object", params["type"])
	}
}

// TestBuildResponsesRequestBodyDefaultsNilParametersToEmptyObjectSchema
// proves a tool with no declared arguments still gets a schema the API
// accepts, rather than a missing/nil "parameters" field.
func TestBuildResponsesRequestBodyDefaultsNilParametersToEmptyObjectSchema(t *testing.T) {
	provider := &OpenAIProvider{config: ProviderConfig{}}
	body := provider.buildResponsesRequestBody(CompletionRequest{
		Model:      "gpt-5.6-luna",
		UserPrompt: "ping",
		Tools:      []Tool{{Name: "ping"}},
	}, false)

	tools := body["tools"].([]map[string]interface{})
	params, ok := tools[0]["parameters"].(map[string]interface{})
	if !ok {
		t.Fatalf("parameters not a map: %#v", tools[0]["parameters"])
	}
	if params["type"] != "object" {
		t.Errorf("default parameters.type = %v, want object", params["type"])
	}
}

// TestBuildResponsesRequestBodyToolChoice proves the three bare-string
// choices ("auto"/"none"/"required") are sent verbatim, and any other value
// is treated as a specific tool name to force.
func TestBuildResponsesRequestBodyToolChoice(t *testing.T) {
	provider := &OpenAIProvider{config: ProviderConfig{}}

	for _, choice := range []string{"auto", "none", "required"} {
		body := provider.buildResponsesRequestBody(CompletionRequest{
			Model:      "gpt-5.6-luna",
			UserPrompt: "x",
			ToolChoice: choice,
		}, false)
		if body["tool_choice"] != choice {
			t.Errorf("ToolChoice %q: tool_choice = %v, want %q", choice, body["tool_choice"], choice)
		}
	}

	body := provider.buildResponsesRequestBody(CompletionRequest{
		Model:      "gpt-5.6-luna",
		UserPrompt: "x",
		ToolChoice: "get_weather",
	}, false)
	forced, ok := body["tool_choice"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected a forced-tool object, got %#v", body["tool_choice"])
	}
	if forced["type"] != "function" || forced["name"] != "get_weather" {
		t.Errorf("forced tool_choice = %#v", forced)
	}
}

// TestCompleteSurfacesToolCallNotAsParseFailure proves a response whose
// output is a function_call item, not a message item, comes back as a
// successful CompletionResponse carrying ToolCalls -- not as
// ErrNoMessageOutput, which is what "empty content" meant before tool
// calling existed.
func TestCompleteSurfacesToolCallNotAsParseFailure(t *testing.T) {
	server := newCapturingServer(t, `{
		"id": "resp_1",
		"status": "completed",
		"output": [
			{
				"type": "function_call",
				"call_id": "call_abc",
				"name": "get_weather",
				"arguments": "{\"city\":\"NYC\"}"
			}
		],
		"model": "gpt-5.6-luna",
		"usage": {"input_tokens": 5, "output_tokens": 3, "total_tokens": 8}
	}`, nil)

	provider := newTestOpenAIProvider(t, server.URL)
	resp, err := provider.Complete(context.Background(), CompletionRequest{
		Model:      "gpt-5.6-luna",
		UserPrompt: "what's the weather in NYC?",
		Tools:      []Tool{{Name: "get_weather"}},
	})
	if err != nil {
		t.Fatalf("Complete returned an error for a valid tool call: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected one tool call, got %d", len(resp.ToolCalls))
	}
	call := resp.ToolCalls[0]
	if call.Name != "get_weather" {
		t.Errorf("call.Name = %q, want get_weather", call.Name)
	}
	if call.ID != "call_abc" {
		t.Errorf("call.ID = %q, want call_abc", call.ID)
	}
	if string(call.Arguments) != `{"city":"NYC"}` {
		t.Errorf("call.Arguments = %s, want raw passthrough", call.Arguments)
	}
	if resp.Content != "" {
		t.Errorf("Content = %q, want empty for a tool-only response", resp.Content)
	}
	if resp.FinishReason != "tool_calls" {
		t.Errorf("FinishReason = %q, want tool_calls", resp.FinishReason)
	}
}

// TestCompleteRefusesUnrequestedTool proves a model naming a tool the
// caller never declared in Tools is refused in Go, via errors.Is against
// ErrUnrequestedTool, rather than handed to the caller as something to act
// on.
func TestCompleteRefusesUnrequestedTool(t *testing.T) {
	server := newCapturingServer(t, `{
		"id": "resp_1",
		"status": "completed",
		"output": [
			{
				"type": "function_call",
				"call_id": "call_xyz",
				"name": "delete_everything",
				"arguments": "{}"
			}
		],
		"model": "gpt-5.6-luna",
		"usage": {"input_tokens": 5, "output_tokens": 3, "total_tokens": 8}
	}`, nil)

	provider := newTestOpenAIProvider(t, server.URL)
	_, err := provider.Complete(context.Background(), CompletionRequest{
		Model:      "gpt-5.6-luna",
		UserPrompt: "do something",
		Tools:      []Tool{{Name: "get_weather"}},
	})
	if err == nil {
		t.Fatal("expected an error for an unoffered tool, got nil")
	}
	if !errors.Is(err, ErrUnrequestedTool) {
		t.Errorf("error %v does not wrap ErrUnrequestedTool", err)
	}
	if !strings.Contains(err.Error(), "delete_everything") {
		t.Errorf("error %q does not name the offending tool", err.Error())
	}
}

// TestToolArgumentsNeverAppearInErrorMessage proves the refusal path names
// the offending tool but never reproduces its arguments, even though the
// arguments are attacker/model-controlled and could contain anything.
func TestToolArgumentsNeverAppearInErrorMessage(t *testing.T) {
	const secretMarker = "SSN-should-never-be-logged-471828"
	server := newCapturingServer(t, `{
		"id": "resp_1",
		"status": "completed",
		"output": [
			{
				"type": "function_call",
				"call_id": "call_xyz",
				"name": "not_offered",
				"arguments": "{\"note\":\"`+secretMarker+`\"}"
			}
		],
		"model": "gpt-5.6-luna",
		"usage": {"input_tokens": 5, "output_tokens": 3, "total_tokens": 8}
	}`, nil)

	provider := newTestOpenAIProvider(t, server.URL)
	_, err := provider.Complete(context.Background(), CompletionRequest{
		Model:      "gpt-5.6-luna",
		UserPrompt: "do something",
		Tools:      []Tool{{Name: "get_weather"}},
	})
	if err == nil {
		t.Fatal("expected an error for an unoffered tool, got nil")
	}
	if strings.Contains(err.Error(), secretMarker) {
		t.Errorf("error leaked tool arguments: %q", err.Error())
	}
}

// TestPlainTextResponseUnaffectedByToolFields proves declaring Tools on a
// request that gets a normal text answer back changes nothing about that
// answer: Content is populated exactly as before, ToolCalls is empty, and
// there is no error.
func TestPlainTextResponseUnaffectedByToolFields(t *testing.T) {
	server := newCapturingServer(t, plainTextResponse, nil)

	provider := newTestOpenAIProvider(t, server.URL)
	resp, err := provider.Complete(context.Background(), CompletionRequest{
		Model:      "gpt-5.6-luna",
		UserPrompt: "say hi",
		Tools:      []Tool{{Name: "get_weather"}},
		ToolChoice: "auto",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "hello there" {
		t.Errorf("Content = %q, want %q", resp.Content, "hello there")
	}
	if len(resp.ToolCalls) != 0 {
		t.Errorf("expected no tool calls, got %#v", resp.ToolCalls)
	}
	if resp.FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want stop", resp.FinishReason)
	}
}

// TestCompleteStreamSurfacesToolCall proves the streaming path classifies a
// terminal event carrying a function_call output the same way the buffered
// path does -- a successful Done chunk with ToolCalls populated, not an
// error.
func TestCompleteStreamSurfacesToolCall(t *testing.T) {
	const sse = "event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"NYC\"}"}],"model":"gpt-5.6-luna","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` +
		"\n\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sse))
	}))

	provider := newTestOpenAIProvider(t, server.URL)
	var lastChunk StreamChunk
	var streamErr error
	for chunk, err := range provider.CompleteStream(context.Background(), CompletionRequest{
		Model:      "gpt-5.6-luna",
		UserPrompt: "weather?",
		Tools:      []Tool{{Name: "get_weather"}},
	}) {
		if err != nil {
			streamErr = err
			break
		}
		lastChunk = chunk
	}
	if streamErr != nil {
		t.Fatalf("unexpected stream error: %v", streamErr)
	}
	if !lastChunk.Done {
		t.Fatal("expected a terminal Done chunk")
	}
	if len(lastChunk.Response.ToolCalls) != 1 || lastChunk.Response.ToolCalls[0].Name != "get_weather" {
		t.Errorf("Response.ToolCalls = %#v", lastChunk.Response.ToolCalls)
	}
}

// TestMessagesTranscriptSentAsArrayInput proves an explicit Messages
// transcript is sent as an ordered array of role/content turns, with
// SystemPrompt still carried separately as "instructions" -- not folded
// into the transcript itself.
func TestMessagesTranscriptSentAsArrayInput(t *testing.T) {
	var captured map[string]interface{}
	server := newCapturingServer(t, plainTextResponse, &captured)

	provider := newTestOpenAIProvider(t, server.URL)
	_, err := provider.Complete(context.Background(), CompletionRequest{
		Model:        "gpt-5.6-luna",
		SystemPrompt: "You are terse.",
		Messages: []Message{
			{Role: "user", Content: "What is 2+2?"},
			{Role: "assistant", Content: "4"},
			{Role: "user", Content: "And 3+3?"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if captured["instructions"] != "You are terse." {
		t.Errorf("instructions = %v, want the system prompt", captured["instructions"])
	}

	turns, ok := captured["input"].([]interface{})
	if !ok || len(turns) != 3 {
		t.Fatalf("expected a 3-turn array input, got %#v", captured["input"])
	}
	first, ok := turns[0].(map[string]interface{})
	if !ok || first["role"] != "user" || first["content"] != "What is 2+2?" {
		t.Errorf("turns[0] = %#v", turns[0])
	}
	third, ok := turns[2].(map[string]interface{})
	if !ok || third["content"] != "And 3+3?" {
		t.Errorf("turns[2] = %#v", turns[2])
	}
}

// TestMessagesEmptyFallsBackToUserPrompt proves a request with no Messages
// produces the same plain-string "input" it always did -- Messages being a
// new field does not change behaviour for every caller that never sets it.
func TestMessagesEmptyFallsBackToUserPrompt(t *testing.T) {
	var captured map[string]interface{}
	server := newCapturingServer(t, plainTextResponse, &captured)

	provider := newTestOpenAIProvider(t, server.URL)
	_, err := provider.Complete(context.Background(), CompletionRequest{
		Model:      "gpt-5.6-luna",
		UserPrompt: "hello",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured["input"] != "hello" {
		t.Errorf("input = %#v, want plain string %q", captured["input"], "hello")
	}
}
