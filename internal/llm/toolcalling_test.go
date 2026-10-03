package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
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

// TestBuildResponsesRequestBodyWebSearchAlone proves the built-in web_search
// tool reaches the wire as a bare type marker even when no function tools are
// declared -- the declaration is what grants the model any web access at all.
func TestBuildResponsesRequestBodyWebSearchAlone(t *testing.T) {
	provider := &OpenAIProvider{config: ProviderConfig{}}
	body := provider.buildResponsesRequestBody(CompletionRequest{
		Model:      "gpt-5.6-luna",
		UserPrompt: "what happened today",
		WebSearch:  true,
	}, false)

	tools, ok := body["tools"].([]map[string]interface{})
	if !ok || len(tools) != 1 {
		t.Fatalf("expected exactly the web_search tool, got %#v", body["tools"])
	}
	if tools[0]["type"] != "web_search" {
		t.Errorf("tool.type = %v, want %q", tools[0]["type"], "web_search")
	}
	if _, ok := tools[0]["name"]; ok {
		t.Errorf("built-in tool must not carry a name, got %v", tools[0]["name"])
	}
	if _, ok := tools[0]["parameters"]; ok {
		t.Errorf("built-in tool must not carry parameters, got %v", tools[0]["parameters"])
	}
}

// TestBuildResponsesRequestBodyWebSearchAlongsideFunctionTools proves the two
// tool kinds share one tools array: function tools keep their shape and order,
// and web_search rides after them.
func TestBuildResponsesRequestBodyWebSearchAlongsideFunctionTools(t *testing.T) {
	provider := &OpenAIProvider{config: ProviderConfig{}}
	body := provider.buildResponsesRequestBody(CompletionRequest{
		Model:      "gpt-5.6-luna",
		UserPrompt: "what's the weather",
		WebSearch:  true,
		Tools:      []Tool{{Name: "get_weather", Description: "d"}},
	}, false)

	tools, ok := body["tools"].([]map[string]interface{})
	if !ok || len(tools) != 2 {
		t.Fatalf("expected function tool + web_search, got %#v", body["tools"])
	}
	if tools[0]["type"] != "function" || tools[0]["name"] != "get_weather" {
		t.Errorf("first tool should be the declared function, got %#v", tools[0])
	}
	if tools[1]["type"] != "web_search" {
		t.Errorf("second tool should be web_search, got %#v", tools[1])
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

// A forced ToolChoice that names none of the offered tools was sent as a forced
// function name and answered with a 400. It is refused before anything is sent.
func TestToolChoiceThatNamesNoOfferedToolIsRefusedBeforeSending(t *testing.T) {
	weather := Tool{Name: "get_weather"}
	cases := []struct {
		name    string
		choice  string
		tools   []Tool
		refused bool
	}{
		{"a misspelling", "gett_weather", []Tool{weather}, true},
		{"auto with a space", "auto ", []Tool{weather}, true},
		{"capitalised word", "Auto", []Tool{weather}, true},
		{"a name with no tools at all", "get_weather", nil, true},
		{"a different case of a tool", "GET_WEATHER", []Tool{weather}, true},
		{"the name of an offered tool", "get_weather", []Tool{weather}, false},
		{"one of several offered", "b", []Tool{{Name: "a"}, {Name: "b"}}, false},
		{"auto", "auto", []Tool{weather}, false},
		{"none", "none", nil, false},
		{"required", "required", []Tool{weather}, false},
		{"empty", "", []Tool{weather}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = w.Write([]byte(plainTextResponse))
			}))
			defer server.Close()
			provider := newTestOpenAIProvider(t, server.URL)
			req := CompletionRequest{Model: "gpt-5.6-luna", UserPrompt: "x", Tools: tc.tools, ToolChoice: tc.choice}

			_, err := provider.Complete(context.Background(), req)
			var streamErr error
			for _, e := range provider.CompleteStream(context.Background(), req) {
				if e != nil {
					streamErr = e
				}
				break
			}
			if tc.refused {
				for name, got := range map[string]error{"Complete": err, "CompleteStream": streamErr} {
					if Classify(got) != types.KindInvalidRequest {
						t.Errorf("%s: Classify = %v, want %v (%v)", name, Classify(got), types.KindInvalidRequest, got)
					}
					if tc.choice != "" && strings.Contains(got.Error(), tc.choice) && tc.choice != "get_weather" {
						t.Errorf("%s: the error repeats the choice: %v", name, got)
					}
				}
				if calls.Load() != 0 {
					t.Error("the request was sent")
				}
				return
			}
			if err != nil {
				t.Errorf("Complete: %v", err)
			}
		})
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
	// The name the model chose is the model's string: as long and as odd as it
	// pleased, and put in an error every caller logs. The error says how many
	// calls were refused and which tools the caller did offer, names that are the
	// caller's own.
	if strings.Contains(err.Error(), "delete_everything") {
		t.Errorf("error %q repeats the tool name the model chose", err.Error())
	}
	if !strings.Contains(err.Error(), `"get_weather"`) {
		t.Errorf("error %q does not list the offered tools", err.Error())
	}
	if !strings.Contains(err.Error(), "1 of 1") {
		t.Errorf("error %q does not count the refused calls", err.Error())
	}
}

func TestValidateToolCallsReportsCountsAndOfferedNamesOnly(t *testing.T) {
	long := strings.Repeat("x", 5000)
	offered := []Tool{{Name: "zeta"}, {Name: "alpha"}, {Name: "mid"}}
	cases := []struct {
		name      string
		calls     []ToolCall
		offered   []Tool
		wantErr   bool
		wantParts []string
	}{
		{"all offered", []ToolCall{{Name: "alpha"}, {Name: "zeta"}}, offered, false, nil},
		{"no calls", nil, offered, false, nil},
		{"one refused", []ToolCall{{Name: "alpha"}, {Name: "rm"}}, offered, true, []string{"1 of 2", `"alpha", "mid", "zeta"`}},
		{"all refused", []ToolCall{{Name: "a"}, {Name: "b"}, {Name: "c"}}, offered, true, []string{"3 of 3"}},
		{"nothing offered", []ToolCall{{Name: "a"}}, nil, true, []string{"1 of 1", "offered: none"}},
		{"empty name refused", []ToolCall{{Name: ""}}, offered, true, []string{"1 of 1"}},
		{"case differs", []ToolCall{{Name: "Alpha"}}, offered, true, []string{"1 of 1"}},
		{"duplicate offers", []ToolCall{{Name: "q"}}, []Tool{{Name: "a"}, {Name: "a"}}, true, []string{`offered: "a")`}},
		{"very long name", []ToolCall{{Name: long}}, offered, true, []string{"1 of 1"}},
		{"arguments never read", []ToolCall{{Name: "x", Arguments: []byte(`{"ssn":"123-45-6789"}`)}}, offered, true, []string{"1 of 1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateToolCalls(tc.calls, tc.offered)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil {
				return
			}
			if !errors.Is(err, ErrUnrequestedTool) {
				t.Errorf("%v does not wrap ErrUnrequestedTool", err)
			}
			for _, part := range tc.wantParts {
				if !strings.Contains(err.Error(), part) {
					t.Errorf("error %q lacks %q", err.Error(), part)
				}
			}
			if len(err.Error()) > 300 {
				t.Errorf("the error is %d bytes; a model's string got into it", len(err.Error()))
			}
			if strings.Contains(err.Error(), "123-45-6789") {
				t.Errorf("arguments reached the error: %v", err)
			}
		})
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

// A request that mentions no tool and no message is written to the wire exactly
// as it was before tool calling and transcripts existed. These are the bytes
// the builder produced for each shape at that time; a change that moves one of
// them changes what every caller who never uses a tool sends.
func TestResponsesRequestWithNoToolsAndNoMessagesIsWrittenAsItAlwaysWas(t *testing.T) {
	schema := map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"a": map[string]any{"type": "string"}},
		"required":             []any{"a"},
		"additionalProperties": false,
	}
	cases := []struct {
		name string
		req  CompletionRequest
		want string
	}{
		{"user prompt only", CompletionRequest{Model: "gpt-6-luna", UserPrompt: "u"}, `{"input":"u","instructions":"","model":"gpt-6-luna","store":false}`},
		{"system and user", CompletionRequest{Model: "gpt-6-luna", SystemPrompt: "s", UserPrompt: "u"}, `{"input":"u","instructions":"s","model":"gpt-6-luna","store":false}`},
		{"json asked for in the prompt", CompletionRequest{Model: "gpt-6-luna", SystemPrompt: "s", UserPrompt: "u", ResponseFormat: "json"}, `{"input":"Return valid JSON only.\n\nu","instructions":"s","model":"gpt-6-luna","store":false,"text":{"format":{"type":"json_object"}}}`},
		{"a strict schema", CompletionRequest{Model: "gpt-6-luna", SystemPrompt: "s", UserPrompt: "u", ResponseFormat: "json", JSONSchema: schema, SchemaName: "answer"}, `{"input":"Return valid JSON only.\n\nu","instructions":"s","model":"gpt-6-luna","store":false,"text":{"format":{"name":"answer","schema":{"additionalProperties":false,"properties":{"a":{"type":"string"}},"required":["a"],"type":"object"},"strict":true,"type":"json_schema"}}}`},
		{"a ceiling and a cache key", CompletionRequest{Model: "gpt-6-luna", SystemPrompt: "s", UserPrompt: "u", MaxTokens: 50, PromptCacheKey: "k"}, `{"input":"u","instructions":"s","max_output_tokens":50,"model":"gpt-6-luna","prompt_cache_key":"k","store":false}`},
		{"an effort", CompletionRequest{Model: "gpt-6-luna", UserPrompt: "u", ReasoningEffort: "low"}, `{"input":"u","instructions":"","model":"gpt-6-luna","reasoning":{"effort":"low"},"store":false}`},
		{"web search", CompletionRequest{Model: "gpt-6-luna", UserPrompt: "u", WebSearch: true}, `{"input":"u","instructions":"","model":"gpt-6-luna","store":false,"tools":[{"type":"web_search"}]}`},
		{"a tool choice with no tools", CompletionRequest{Model: "gpt-6-luna", UserPrompt: "u", ToolChoice: "none"}, `{"input":"u","instructions":"","model":"gpt-6-luna","store":false,"tool_choice":"none"}`},
		{"an empty tools list and an empty transcript", CompletionRequest{Model: "gpt-6-luna", UserPrompt: "u", Tools: []Tool{}, Messages: []Message{}}, `{"input":"u","instructions":"","model":"gpt-6-luna","store":false}`},
		{"a plain transcript", CompletionRequest{Model: "gpt-6-luna", SystemPrompt: "s", Messages: []Message{{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"}, {Role: "user", Content: "c"}}}, `{"input":[{"content":"a","role":"user"},{"content":"b","role":"assistant"},{"content":"c","role":"user"}],"instructions":"s","model":"gpt-6-luna","store":false}`},
	}
	provider := &OpenAIProvider{config: ProviderConfig{}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := json.Marshal(provider.buildResponsesRequestBody(tc.req, false))
			if err != nil {
				t.Fatal(err)
			}
			if string(encoded) != tc.want {
				t.Errorf("body\n got %s\nwant %s", encoded, tc.want)
			}
		})
	}
}

// wireItems is the "input" a Responses request carries for a transcript, as the
// JSON the provider writes.
func wireItems(t *testing.T, messages []Message) string {
	t.Helper()
	provider := &OpenAIProvider{config: ProviderConfig{}}
	body := provider.buildResponsesRequestBody(CompletionRequest{Model: "gpt-6-luna", SystemPrompt: "s", Messages: messages}, false)
	encoded, err := json.Marshal(body["input"])
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// The Responses API takes a tool's answer as an input item of its own, a
// function_call_output carrying the call's id and the result, and not as a turn
// with a role. A tool message used to be written as {"role":"tool","content":...,
// "call_id":...}, which the API does not define, so a tool result sent back never
// reached the model as one.
func TestResponsesRequestWritesAToolResultAsAFunctionCallOutput(t *testing.T) {
	cases := []struct {
		name     string
		messages []Message
		want     string
	}{
		{"one result", []Message{{Role: "user", Content: "hi"}, {Role: "tool", Content: "7C", ToolCallID: "call_1"}},
			`[{"content":"hi","role":"user"},{"call_id":"call_1","output":"7C","type":"function_call_output"}]`},
		{"two results in the order they were given", []Message{{Role: "tool", Content: "a", ToolCallID: "call_2"}, {Role: "tool", Content: "b", ToolCallID: "call_1"}},
			`[{"call_id":"call_2","output":"a","type":"function_call_output"},{"call_id":"call_1","output":"b","type":"function_call_output"}]`},
		{"an empty result is still an output", []Message{{Role: "tool", Content: "", ToolCallID: "call_1"}},
			`[{"call_id":"call_1","output":"","type":"function_call_output"}]`},
		{"a result that is JSON stays text", []Message{{Role: "tool", Content: `{"temp":7}`, ToolCallID: "call_1"}},
			`[{"call_id":"call_1","output":"{\"temp\":7}","type":"function_call_output"}]`},
		{"a call id the API made up", []Message{{Role: "tool", Content: "x", ToolCallID: "call_AbC123xyz"}},
			`[{"call_id":"call_AbC123xyz","output":"x","type":"function_call_output"}]`},
		{"a system turn", []Message{{Role: "system", Content: "be brief"}, {Role: "user", Content: "hi"}},
			`[{"content":"be brief","role":"system"},{"content":"hi","role":"user"}]`},
		{"a developer turn", []Message{{Role: "developer", Content: "be brief"}, {Role: "user", Content: "hi"}},
			`[{"content":"be brief","role":"developer"},{"content":"hi","role":"user"}]`},
		{"an assistant turn of text", []Message{{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"}, {Role: "user", Content: "c"}},
			`[{"content":"a","role":"user"},{"content":"b","role":"assistant"},{"content":"c","role":"user"}]`},
		{"results between turns", []Message{{Role: "user", Content: "a"}, {Role: "tool", Content: "r", ToolCallID: "c1"}, {Role: "assistant", Content: "b"}},
			`[{"content":"a","role":"user"},{"call_id":"c1","output":"r","type":"function_call_output"},{"content":"b","role":"assistant"}]`},
		{"text with quotes and a multi-byte character", []Message{{Role: "tool", Content: `say "hi" caf` + "é", ToolCallID: "c1"}},
			`[{"call_id":"c1","output":"say \"hi\" caf` + "é" + `","type":"function_call_output"}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := wireItems(t, tc.messages); got != tc.want {
				t.Errorf("input\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

// The tools are declared with the strict flag the API reads: true when the
// parameters schema is one strict mode accepts, and written out as false when it
// is not, because the Responses API treats a function with no flag as strict and
// answers a schema that cannot be strict with a 400.
func TestResponsesRequestDeclaresEachToolWithItsStrictFlag(t *testing.T) {
	closed := map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"city": map[string]any{"type": "string"}},
		"required":             []any{"city"},
		"additionalProperties": false,
	}
	optional := map[string]any{
		"type":       "object",
		"properties": map[string]any{"city": map[string]any{"type": "string"}},
		"required":   []string{},
	}
	cases := []struct {
		name       string
		tool       Tool
		wantStrict bool
		wantParams string
	}{
		{"a closed schema", Tool{Name: "get_weather", Description: "d", Parameters: closed}, true,
			`{"additionalProperties":false,"properties":{"city":{"type":"string"}},"required":["city"],"type":"object"}`},
		{"a schema with an optional property", Tool{Name: "t", Parameters: optional}, false,
			`{"properties":{"city":{"type":"string"}},"required":[],"type":"object"}`},
		{"no parameters is a closed empty object", Tool{Name: "ping"}, true,
			`{"additionalProperties":false,"properties":{},"required":[],"type":"object"}`},
		{"an empty parameters map is no parameters", Tool{Name: "t", Parameters: map[string]any{}}, true,
			`{"additionalProperties":false,"properties":{},"required":[],"type":"object"}`},
		{"a root that is not an object", Tool{Name: "t", Parameters: map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}, false,
			`{"items":{"type":"string"},"type":"array"}`},
		{"a nested object left open", Tool{Name: "t", Parameters: map[string]any{
			"type":                 "object",
			"required":             []any{"inner"},
			"additionalProperties": false,
			"properties": map[string]any{"inner": map[string]any{
				"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}, "required": []any{"a"}}},
		}}, false, ""},
		{"a required name that is not a property", Tool{Name: "t", Parameters: map[string]any{
			"type": "object", "properties": map[string]any{}, "required": []any{"ghost"}, "additionalProperties": false}}, false, ""},
		{"a nullable optional", Tool{Name: "t", Parameters: map[string]any{
			"type": "object", "required": []any{"a"}, "additionalProperties": false,
			"properties": map[string]any{"a": map[string]any{"type": []any{"string", "null"}}}}}, true, ""},
		{"an array of closed objects", Tool{Name: "t", Parameters: map[string]any{
			"type": "object", "required": []any{"rows"}, "additionalProperties": false,
			"properties": map[string]any{"rows": map[string]any{"type": "array", "items": map[string]any{
				"type": "object", "required": []any{"n"}, "additionalProperties": false,
				"properties": map[string]any{"n": map[string]any{"type": "integer"}}}}}}}, true, ""},
		{"a name and a description with quotes", Tool{Name: "quote_tool", Description: `says "hi"`, Parameters: closed}, true, ""},
	}
	provider := &OpenAIProvider{config: ProviderConfig{}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := provider.buildResponsesRequestBody(CompletionRequest{Model: "gpt-6-luna", UserPrompt: "u", Tools: []Tool{tc.tool}}, false)
			tools, ok := body["tools"].([]map[string]any)
			if !ok || len(tools) != 1 {
				t.Fatalf("tools = %#v", body["tools"])
			}
			declared := tools[0]
			if declared["type"] != "function" || declared["name"] != tc.tool.Name || declared["description"] != tc.tool.Description {
				t.Errorf("declaration = %#v", declared)
			}
			strict, present := declared["strict"]
			if !present {
				t.Fatalf("the declaration carries no strict flag: %#v", declared)
			}
			if strict != tc.wantStrict {
				t.Errorf("strict = %v, want %v", strict, tc.wantStrict)
			}
			if tc.wantParams != "" {
				encoded, _ := json.Marshal(declared["parameters"])
				if string(encoded) != tc.wantParams {
					t.Errorf("parameters\n got %s\nwant %s", encoded, tc.wantParams)
				}
			}
		})
	}
}

// countingTransport answers a Responses call in place of OpenAI, so the request
// goes to OpenAI's own endpoint as far as the provider can tell, and counts what
// it was sent.
type countingTransport struct {
	calls atomic.Int32
	body  string
}

func (c *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.calls.Add(1)
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(c.body)),
		Request:    req,
	}, nil
}

// On OpenAI's own endpoint a tool whose schema cannot be strict is refused before
// anything is sent, as a response schema is: the API would answer it with a 400
// that names neither the operation nor the field. The error names the tool,
// which is the caller's own name, and the place in the schema, and never a value.
func TestOpenAIsOwnEndpointRefusesAToolThatCannotBeStrictBeforeSending(t *testing.T) {
	const secret = "CALLER-SECRET-6612"
	transport := &countingTransport{body: plainTextResponse}
	provider, err := NewOpenAIProvider(ProviderConfig{APIKey: "k", HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	open := Tool{Name: "lookup", Description: secret, Parameters: map[string]any{
		"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}, "required": []any{}}}
	closed := Tool{Name: "lookup", Parameters: map[string]any{
		"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}, "required": []any{"city"}, "additionalProperties": false}}

	cases := []struct {
		name    string
		tools   []Tool
		refused bool
	}{
		{"an open schema", []Tool{open}, true},
		{"an open schema after a closed one", []Tool{closed, {Name: "second", Description: secret, Parameters: open.Parameters}}, true},
		{"a closed schema", []Tool{closed}, false},
		{"no parameters", []Tool{{Name: "ping"}}, false},
		{"two closed schemas", []Tool{closed, {Name: "other", Parameters: closed.Parameters}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := transport.calls.Load()
			req := CompletionRequest{Model: "gpt-6-luna", UserPrompt: "u", Tools: tc.tools}
			_, completeErr := provider.Complete(context.Background(), req)
			var streamErr error
			for _, e := range provider.CompleteStream(context.Background(), req) {
				streamErr = e
				break
			}
			if !tc.refused {
				if completeErr != nil {
					t.Fatalf("Complete refused a tool it should send: %v", completeErr)
				}
				return
			}
			for name, got := range map[string]error{"Complete": completeErr, "CompleteStream": streamErr} {
				if got == nil || Classify(got) != types.KindInvalidRequest {
					t.Errorf("%s: err = %v, want an invalid request", name, got)
					continue
				}
				if !strings.Contains(got.Error(), `parameters of tool "`) || !strings.Contains(got.Error(), "additionalProperties") {
					t.Errorf("%s: the error names neither the tool nor the schema's problem: %v", name, got)
				}
				if strings.Contains(got.Error(), secret) {
					t.Errorf("%s: the error repeats the tool's description: %v", name, got)
				}
			}
			if transport.calls.Load() != before {
				t.Error("the request was sent")
			}
		})
	}
}

// A transcript is checked before anything is sent for what could not be written
// to the wire: a role the API does not define, a tool result that names no call,
// a call the model supposedly made that has no id or no name.
func TestResponsesRequestRefusesATranscriptItCannotWriteBeforeSending(t *testing.T) {
	const secret = "CALLER-SECRET-9047"
	cases := []struct {
		name     string
		messages []Message
	}{
		{"a role nobody defined", []Message{{Role: "narrator", Content: secret}}},
		{"no role", []Message{{Role: "", Content: secret}}},
		{"a role in capitals", []Message{{Role: "User", Content: secret}}},
		{"a role with a space", []Message{{Role: "user ", Content: secret}}},
		{"a function role", []Message{{Role: "function", Content: secret, ToolCallID: "c1"}}},
		{"a tool result with no call id", []Message{{Role: "user", Content: "hi"}, {Role: "tool", Content: secret}}},
		{"a tool result after a good turn with no call id", []Message{{Role: "user", Content: "hi"}, {Role: "tool", Content: "ok", ToolCallID: "c1"}, {Role: "tool", Content: secret}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = w.Write([]byte(plainTextResponse))
			}))
			defer server.Close()
			provider := newTestOpenAIProvider(t, server.URL)
			req := CompletionRequest{Model: "gpt-6-luna", Messages: tc.messages}

			_, completeErr := provider.Complete(context.Background(), req)
			var streamErr error
			for _, e := range provider.CompleteStream(context.Background(), req) {
				streamErr = e
				break
			}
			for name, got := range map[string]error{"Complete": completeErr, "CompleteStream": streamErr} {
				if got == nil || Classify(got) != types.KindInvalidRequest {
					t.Errorf("%s: err = %v, want an invalid request", name, got)
					continue
				}
				if strings.Contains(got.Error(), secret) {
					t.Errorf("%s: the error carries the caller's text: %v", name, got)
				}
			}
			if calls.Load() != 0 {
				t.Error("the request was sent")
			}
		})
	}
}

// functionCallItem is one function_call item of a Responses payload.
func functionCallItem(callID, itemID, name, arguments string) map[string]any {
	item := map[string]any{"type": "function_call", "name": name, "arguments": arguments}
	if callID != "" {
		item["call_id"] = callID
	}
	if itemID != "" {
		item["id"] = itemID
	}
	return item
}

func messageItem(text string) map[string]any {
	return map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": text}}}
}

// responsesPayload is a Responses API payload with the given output items.
func responsesPayload(t *testing.T, status string, items ...any) string {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"id": "resp_1", "status": status, "model": "gpt-6-luna", "output": items,
		"usage": map[string]any{"input_tokens": 5, "output_tokens": 3, "total_tokens": 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// streamedPayload is the same payload as the terminal event of a stream.
func streamedPayload(t *testing.T, payload string) string {
	t.Helper()
	return "event: response.completed\ndata: " + `{"type":"response.completed","response":` + payload + "}\n\n"
}

// completeAndStream sends one request to a server answering payload, as a
// buffered call and as a stream, and returns what each ended with.
func completeAndStream(t *testing.T, payload string, req CompletionRequest) (CompletionResponse, error, CompletionResponse, error) {
	t.Helper()
	buffered := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer buffered.Close()
	streamed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(streamedPayload(t, payload)))
	}))
	defer streamed.Close()

	resp, err := newTestOpenAIProvider(t, buffered.URL).Complete(context.Background(), req)
	var streamResp CompletionResponse
	var streamErr error
	for chunk, e := range newTestOpenAIProvider(t, streamed.URL).CompleteStream(context.Background(), req) {
		if e != nil {
			streamErr = e
			break
		}
		if chunk.Done {
			streamResp = chunk.Response
		}
	}
	return resp, err, streamResp, streamErr
}

// A reply that is only tool calls is an answer from the provider's Complete:
// Content empty, the calls in order with their ids, names and argument text, and
// the finish reason saying why it stopped. A reply the calls of which cannot be
// answered, or were not offered, is refused as a malformed output and names
// nothing the model wrote.
func TestResponsesReplyToolCallsAreReadOrRefused(t *testing.T) {
	const marker = "MODEL-WROTE-THIS-5521"
	offered := []Tool{{Name: "get_weather"}, {Name: "get_time"}}

	cases := []struct {
		name      string
		status    string
		items     []any
		wantCalls []ToolCall
		wantText  string
		wantKind  types.ErrorKind // KindUnknown for an answer
		wantIs    error
	}{
		{name: "only one call", status: "completed",
			items:     []any{functionCallItem("call_1", "fc_1", "get_weather", `{"city":"Oslo"}`)},
			wantCalls: []ToolCall{{ID: "call_1", Name: "get_weather", Arguments: json.RawMessage(`{"city":"Oslo"}`)}}},
		{name: "two calls in the order the model made them", status: "completed",
			items: []any{functionCallItem("call_2", "", "get_time", `{}`), functionCallItem("call_1", "", "get_weather", `{"city":"Oslo"}`)},
			wantCalls: []ToolCall{
				{ID: "call_2", Name: "get_time", Arguments: json.RawMessage(`{}`)},
				{ID: "call_1", Name: "get_weather", Arguments: json.RawMessage(`{"city":"Oslo"}`)}}},
		{name: "text beside a call", status: "completed",
			items:     []any{messageItem("Let me check."), functionCallItem("call_1", "", "get_weather", `{"a":1}`)},
			wantCalls: []ToolCall{{ID: "call_1", Name: "get_weather", Arguments: json.RawMessage(`{"a":1}`)}}, wantText: "Let me check."},
		{name: "the item id when there is no call id", status: "completed",
			items:     []any{functionCallItem("", "fc_9", "get_weather", `{}`)},
			wantCalls: []ToolCall{{ID: "fc_9", Name: "get_weather", Arguments: json.RawMessage(`{}`)}}},
		{name: "arguments with white space and nesting", status: "completed",
			items:     []any{functionCallItem("call_1", "", "get_weather", " {\n \"a\": {\"b\": [1, 2]} } ")},
			wantCalls: []ToolCall{{ID: "call_1", Name: "get_weather", Arguments: json.RawMessage(" {\n \"a\": {\"b\": [1, 2]} } ")}}},

		{name: "no id at all", status: "completed", items: []any{functionCallItem("", "", "get_weather", `{}`)}, wantKind: types.KindMalformedOutput},
		{name: "no name", status: "completed", items: []any{functionCallItem("call_1", "", "", `{}`)}, wantKind: types.KindMalformedOutput, wantIs: ErrUnrequestedTool},
		{name: "one call without an id among good ones", status: "completed",
			items:    []any{functionCallItem("call_1", "", "get_weather", `{}`), functionCallItem("", "", "get_time", `{}`)},
			wantKind: types.KindMalformedOutput},
		{name: "an id used twice", status: "completed",
			items:    []any{functionCallItem("call_1", "", "get_weather", `{}`), functionCallItem("call_1", "", "get_time", `{}`)},
			wantKind: types.KindMalformedOutput},
		{name: "arguments that are an array", status: "completed", items: []any{functionCallItem("c1", "", "get_weather", `[1]`)}, wantKind: types.KindMalformedOutput},
		{name: "arguments that are null", status: "completed", items: []any{functionCallItem("c1", "", "get_weather", `null`)}, wantKind: types.KindMalformedOutput},
		{name: "arguments that are a number", status: "completed", items: []any{functionCallItem("c1", "", "get_weather", `7`)}, wantKind: types.KindMalformedOutput},
		{name: "arguments that are a string", status: "completed", items: []any{functionCallItem("c1", "", "get_weather", `"`+marker+`"`)}, wantKind: types.KindMalformedOutput},
		{name: "arguments that are empty", status: "completed", items: []any{functionCallItem("c1", "", "get_weather", ``)}, wantKind: types.KindMalformedOutput},
		{name: "arguments cut short", status: "completed", items: []any{functionCallItem("c1", "", "get_weather", `{"city":"`+marker)}, wantKind: types.KindMalformedOutput},
		{name: "arguments with text after the object", status: "completed", items: []any{functionCallItem("c1", "", "get_weather", `{} `+marker)}, wantKind: types.KindMalformedOutput},
		{name: "a tool nobody offered", status: "completed", items: []any{functionCallItem("c1", "", marker, `{}`)}, wantKind: types.KindMalformedOutput, wantIs: ErrUnrequestedTool},
		{name: "a tool offered in another case", status: "completed", items: []any{functionCallItem("c1", "", "Get_Weather", `{}`)}, wantKind: types.KindMalformedOutput, wantIs: ErrUnrequestedTool},
		{name: "calls cut off at the ceiling", status: "incomplete",
			items:    []any{functionCallItem("c1", "", "get_weather", `{"city":"`)},
			wantKind: types.KindOutputTruncated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := responsesPayload(t, tc.status, tc.items...)
			if tc.status == "incomplete" {
				payload = strings.Replace(payload, `"status":"incomplete"`, `"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}`, 1)
			}
			req := CompletionRequest{Model: "gpt-6-luna", UserPrompt: "u", Tools: offered}
			resp, err, streamResp, streamErr := completeAndStream(t, payload, req)

			for name, got := range map[string][2]any{"Complete": {resp, err}, "CompleteStream": {streamResp, streamErr}} {
				result, failure := got[0].(CompletionResponse), got[1]
				if tc.wantKind != types.KindUnknown {
					gotErr, _ := failure.(error)
					if gotErr == nil {
						t.Errorf("%s: answered %+v, want a refusal", name, result)
						continue
					}
					if kind := Classify(gotErr); kind != tc.wantKind {
						t.Errorf("%s: kind = %v, want %v (%v)", name, kind, tc.wantKind, gotErr)
					}
					if tc.wantIs != nil && !errors.Is(gotErr, tc.wantIs) {
						t.Errorf("%s: %v does not wrap %v", name, gotErr, tc.wantIs)
					}
					if strings.Contains(gotErr.Error(), marker) {
						t.Errorf("%s: the error repeats what the model wrote: %v", name, gotErr)
					}
					if !isEmptyResponse(result) {
						t.Errorf("%s: a result came back beside the error", name)
					}
					continue
				}
				if failure != nil {
					t.Errorf("%s: %v", name, failure)
					continue
				}
				if len(result.ToolCalls) != len(tc.wantCalls) {
					t.Errorf("%s: %d calls, want %d", name, len(result.ToolCalls), len(tc.wantCalls))
					continue
				}
				for i, want := range tc.wantCalls {
					call := result.ToolCalls[i]
					if call.ID != want.ID || call.Name != want.Name || string(call.Arguments) != string(want.Arguments) {
						t.Errorf("%s: call %d = %+v, want %+v", name, i, call, want)
					}
				}
				if result.Content != tc.wantText {
					t.Errorf("%s: Content = %q, want %q", name, result.Content, tc.wantText)
				}
				if result.FinishReason != "tool_calls" {
					t.Errorf("%s: FinishReason = %q, want tool_calls", name, result.FinishReason)
				}
			}
		})
	}
}

// The estimate of a request is the estimate of what it sends. A conversation held
// in Messages and the tools it declares were not counted, so a Budget in front of
// a tool loop priced every turn as the system prompt alone, however long the
// history had grown.
func TestEstimateCostCountsTheConversationAndTheTools(t *testing.T) {
	text := strings.Repeat("a", 4000)
	providers := map[string]Provider{
		"openai":       &OpenAIProvider{config: ProviderConfig{}},
		"chat dialect": &OpenAICompatibleProvider{name: "openrouter", config: ProviderConfig{}},
	}
	for name, provider := range providers {
		t.Run(name, func(t *testing.T) {
			plain := provider.EstimateCost(CompletionRequest{Model: "gpt-6-luna", UserPrompt: text, MaxTokens: 1})

			cases := []struct {
				name string
				req  CompletionRequest
			}{
				{"one message", CompletionRequest{Messages: []Message{{Role: "user", Content: text}}}},
				{"the same text in two messages", CompletionRequest{Messages: []Message{{Role: "user", Content: text[:2000]}, {Role: "assistant", Content: text[2000:]}}}},
				{"a tool result", CompletionRequest{Messages: []Message{{Role: "tool", Content: text, ToolCallID: ""}}}},
				{"a tool description", CompletionRequest{Tools: []Tool{{Description: text}}}},
				{"a tool's parameters", CompletionRequest{Tools: []Tool{{Parameters: map[string]any{"description": text}}}}},
			}
			for _, tc := range cases {
				req := tc.req
				req.Model, req.MaxTokens = "gpt-6-luna", 1
				got := provider.EstimateCost(req)
				// The same bytes sent another way cost about the same: the tool
				// declaration adds its own few bytes of structure.
				if got < plain*0.99 {
					t.Errorf("%s: estimate %v is less than the %v of the same text as a prompt", tc.name, got, plain)
				}
				if got > plain*1.5 {
					t.Errorf("%s: estimate %v is far more than the %v of the same text as a prompt", tc.name, got, plain)
				}
			}

			empty := provider.EstimateCost(CompletionRequest{Model: "gpt-6-luna", MaxTokens: 1})
			if empty >= plain {
				t.Fatalf("an empty request estimates %v, no less than %v for 4000 bytes", empty, plain)
			}
		})
	}
}

// The model's call is written back into the conversation as the Responses API's
// function_call item, with the id, the name and the arguments as text, in the
// place in the order the turns were given where the model made it; the words it
// said first, if any, are a turn of their own before its calls. The request used
// to carry only the text of an assistant turn, so a result sent back answered a
// call the API had never been shown.
func TestResponsesRequestWritesTheModelsCallsAsFunctionCalls(t *testing.T) {
	weather := ToolCall{ID: "call_1", Name: "get_weather", Arguments: json.RawMessage(`{"city":"Oslo"}`)}
	clock := ToolCall{ID: "call_2", Name: "get_time", Arguments: json.RawMessage(`{}`)}
	cases := []struct {
		name     string
		messages []Message
		want     string
	}{
		{"a call and its result", []Message{
			{Role: "user", Content: "How warm is Oslo?"},
			{Role: "assistant", ToolCalls: []ToolCall{weather}},
			{Role: "tool", Content: "7C", ToolCallID: "call_1"}},
			`[{"content":"How warm is Oslo?","role":"user"},{"arguments":"{\"city\":\"Oslo\"}","call_id":"call_1","name":"get_weather","type":"function_call"},{"call_id":"call_1","output":"7C","type":"function_call_output"}]`},
		{"words before the call", []Message{
			{Role: "user", Content: "How warm is Oslo?"},
			{Role: "assistant", Content: "Let me look.", ToolCalls: []ToolCall{weather}},
			{Role: "tool", Content: "7C", ToolCallID: "call_1"}},
			`[{"content":"How warm is Oslo?","role":"user"},{"content":"Let me look.","role":"assistant"},{"arguments":"{\"city\":\"Oslo\"}","call_id":"call_1","name":"get_weather","type":"function_call"},{"call_id":"call_1","output":"7C","type":"function_call_output"}]`},
		{"two calls and two results", []Message{
			{Role: "assistant", ToolCalls: []ToolCall{weather, clock}},
			{Role: "tool", Content: "7C", ToolCallID: "call_1"},
			{Role: "tool", Content: "12:05", ToolCallID: "call_2"}},
			`[{"arguments":"{\"city\":\"Oslo\"}","call_id":"call_1","name":"get_weather","type":"function_call"},{"arguments":"{}","call_id":"call_2","name":"get_time","type":"function_call"},{"call_id":"call_1","output":"7C","type":"function_call_output"},{"call_id":"call_2","output":"12:05","type":"function_call_output"}]`},
		{"the arguments are written as they were", []Message{
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "c", Name: "n", Arguments: json.RawMessage(" { \"a\" : 1 } ")}}}},
			`[{"arguments":" { \"a\" : 1 } ","call_id":"c","name":"n","type":"function_call"}]`},
		{"an assistant turn of words only is a turn", []Message{
			{Role: "assistant", Content: "It is 7C."}},
			`[{"content":"It is 7C.","role":"assistant"}]`},
		{"an assistant turn with an empty list of calls is a turn", []Message{
			{Role: "assistant", Content: "hi", ToolCalls: []ToolCall{}}},
			`[{"content":"hi","role":"assistant"}]`},
		{"a second loop", []Message{
			{Role: "user", Content: "a"},
			{Role: "assistant", ToolCalls: []ToolCall{weather}},
			{Role: "tool", Content: "r1", ToolCallID: "call_1"},
			{Role: "assistant", Content: "b"},
			{Role: "user", Content: "c"},
			{Role: "assistant", ToolCalls: []ToolCall{clock}},
			{Role: "tool", Content: "r2", ToolCallID: "call_2"}},
			`[{"content":"a","role":"user"},{"arguments":"{\"city\":\"Oslo\"}","call_id":"call_1","name":"get_weather","type":"function_call"},{"call_id":"call_1","output":"r1","type":"function_call_output"},{"content":"b","role":"assistant"},{"content":"c","role":"user"},{"arguments":"{}","call_id":"call_2","name":"get_time","type":"function_call"},{"call_id":"call_2","output":"r2","type":"function_call_output"}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := wireItems(t, tc.messages); got != tc.want {
				t.Errorf("input\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

// The chat dialect carries the same history as an assistant message with the
// calls in tool_calls, whose content is null when the model said nothing, and a
// message of the role tool for each result.
func TestChatDialectWritesTheModelsCallsAsToolCalls(t *testing.T) {
	weather := ToolCall{ID: "call_1", Name: "get_weather", Arguments: json.RawMessage(`{"city":"Oslo"}`)}
	clock := ToolCall{ID: "call_2", Name: "get_time", Arguments: json.RawMessage(`{}`)}
	cases := []struct {
		name     string
		messages []Message
		want     string
	}{
		{"a call and its result", []Message{
			{Role: "user", Content: "How warm is Oslo?"},
			{Role: "assistant", ToolCalls: []ToolCall{weather}},
			{Role: "tool", Content: "7C", ToolCallID: "call_1"}},
			`[{"content":"s","role":"system"},{"content":"How warm is Oslo?","role":"user"},{"content":null,"role":"assistant","tool_calls":[{"function":{"arguments":"{\"city\":\"Oslo\"}","name":"get_weather"},"id":"call_1","type":"function"}]},{"content":"7C","role":"tool","tool_call_id":"call_1"}]`},
		{"words with the call", []Message{
			{Role: "assistant", Content: "Let me look.", ToolCalls: []ToolCall{weather}},
			{Role: "tool", Content: "7C", ToolCallID: "call_1"}},
			`[{"content":"s","role":"system"},{"content":"Let me look.","role":"assistant","tool_calls":[{"function":{"arguments":"{\"city\":\"Oslo\"}","name":"get_weather"},"id":"call_1","type":"function"}]},{"content":"7C","role":"tool","tool_call_id":"call_1"}]`},
		{"two calls in one message, in order", []Message{
			{Role: "assistant", ToolCalls: []ToolCall{clock, weather}},
			{Role: "tool", Content: "12:05", ToolCallID: "call_2"},
			{Role: "tool", Content: "7C", ToolCallID: "call_1"}},
			`[{"content":"s","role":"system"},{"content":null,"role":"assistant","tool_calls":[{"function":{"arguments":"{}","name":"get_time"},"id":"call_2","type":"function"},{"function":{"arguments":"{\"city\":\"Oslo\"}","name":"get_weather"},"id":"call_1","type":"function"}]},{"content":"12:05","role":"tool","tool_call_id":"call_2"},{"content":"7C","role":"tool","tool_call_id":"call_1"}]`},
		{"an assistant turn of words only", []Message{{Role: "assistant", Content: "It is 7C."}},
			`[{"content":"s","role":"system"},{"content":"It is 7C.","role":"assistant"}]`},
		{"an empty assistant turn keeps its empty content", []Message{{Role: "assistant", Content: ""}},
			`[{"content":"s","role":"system"},{"content":"","role":"assistant"}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := messagesOnTheWire(t, CompletionRequest{Model: "m", SystemPrompt: "s", Messages: tc.messages})
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("messages\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

// A call in the history that could not be written to the wire is refused before
// anything is sent, by both dialects.
func TestATranscriptWithACallItCannotWriteIsRefusedBeforeSending(t *testing.T) {
	const secret = "CALLER-SECRET-2205"
	good := ToolCall{ID: "call_1", Name: "get_weather", Arguments: json.RawMessage(`{}`)}
	cases := []struct {
		name     string
		messages []Message
	}{
		{"a call with no id", []Message{{Role: "assistant", ToolCalls: []ToolCall{{Name: "n", Arguments: json.RawMessage(`{}`)}}}}},
		{"a call with no name", []Message{{Role: "assistant", ToolCalls: []ToolCall{{ID: "c", Arguments: json.RawMessage(`{}`)}}}}},
		{"arguments that are empty", []Message{{Role: "assistant", ToolCalls: []ToolCall{{ID: "c", Name: "n"}}}}},
		{"arguments that are an array", []Message{{Role: "assistant", ToolCalls: []ToolCall{{ID: "c", Name: "n", Arguments: json.RawMessage(`[1]`)}}}}},
		{"arguments that are not JSON", []Message{{Role: "assistant", ToolCalls: []ToolCall{{ID: "c", Name: "n", Arguments: json.RawMessage(secret)}}}}},
		{"an id two calls share", []Message{{Role: "assistant", ToolCalls: []ToolCall{good, good}}}},
		{"an id two turns share", []Message{{Role: "assistant", ToolCalls: []ToolCall{good}}, {Role: "tool", Content: "r", ToolCallID: "call_1"}, {Role: "assistant", ToolCalls: []ToolCall{good}}}},
		{"calls on a user turn", []Message{{Role: "user", Content: secret, ToolCalls: []ToolCall{good}}}},
		{"calls on a tool turn", []Message{{Role: "tool", Content: secret, ToolCallID: "call_1", ToolCalls: []ToolCall{good}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = w.Write([]byte(plainTextResponse))
			}))
			defer server.Close()
			req := CompletionRequest{Model: "gpt-6-luna", Messages: tc.messages}

			responses := newTestOpenAIProvider(t, server.URL)
			chat, err := NewOpenAICompatibleProvider("testvendor", ProviderConfig{APIKey: "k", BaseURL: server.URL, Timeout: 10 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			_, responsesErr := responses.Complete(context.Background(), req)
			_, chatErr := chat.Complete(context.Background(), req)
			for name, got := range map[string]error{"Responses": responsesErr, "chat": chatErr} {
				if got == nil || Classify(got) != types.KindInvalidRequest {
					t.Errorf("%s: err = %v, want an invalid request", name, got)
					continue
				}
				if strings.Contains(got.Error(), secret) {
					t.Errorf("%s: the error carries the caller's text: %v", name, got)
				}
			}
			if calls.Load() != 0 {
				t.Error("the request was sent")
			}
		})
	}
}

// The estimate counts the arguments of the calls in the history, which are sent
// again with every turn.
func TestEstimateCostCountsTheCallsOfTheHistory(t *testing.T) {
	provider := &OpenAIProvider{config: ProviderConfig{}}
	text := strings.Repeat("a", 4000)
	plain := provider.EstimateCost(CompletionRequest{Model: "gpt-6-luna", UserPrompt: text, MaxTokens: 1})
	withCall := provider.EstimateCost(CompletionRequest{Model: "gpt-6-luna", MaxTokens: 1, Messages: []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c", Name: "n", Arguments: json.RawMessage(`{"a":"` + text + `"}`)}}}}})
	if withCall < plain*0.99 {
		t.Errorf("estimate %v for the arguments of a call is less than the %v of the same text as a prompt", withCall, plain)
	}
}
