package wirecontract

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
)

// The wire shapes of a tool loop: a tool declared, the model's call written back
// into the conversation, and the tool's result. Each is checked, without a
// network call, against what the API it is sent to defines, for both dialects
// the library writes. The rules below are restated here and do not call the
// library's own validators, as the rules in wire_contract_test.go are: the
// provider uses those to decide what to refuse, so a rule it did not know would
// pass both it and a test built on it.
//
// What each rule rests on is noted where it is stated. None of them was
// observed against the live API by this test, which cannot call it; they are
// the API's documented shapes.

// toolRecorder keeps the raw body of every request, and answers each with a
// reply that is only a tool call, in the dialect it speaks.
type toolRecorder struct {
	mu     sync.Mutex
	bodies []map[string]any
	reply  string
}

func (r *toolRecorder) take() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.bodies
	r.bodies = nil
	return out
}

func (r *toolRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	raw, _ := io.ReadAll(req.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	r.mu.Lock()
	r.bodies = append(r.bodies, body)
	r.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(r.reply)),
		Request:    req,
	}, nil
}

const responsesToolReply = `{"status":"completed","model":"gpt-6-luna","output":[{"type":"function_call","call_id":"call_9","name":"get_weather","arguments":"{\"city\":\"Oslo\"}"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`

const chatToolReply = `{"model":"m","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_9","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Oslo\"}"}}]}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

// openAIResponses is the real OpenAI provider at its own endpoint, with a
// recording transport in place of the network.
func openAIResponses(t *testing.T) (llm.Provider, *toolRecorder) {
	t.Helper()
	t.Setenv("SCHEMAFLUX_OPENAI_BASE_URL", "")
	t.Setenv("OPENAI_BASE_URL", "")
	recorder := &toolRecorder{reply: responsesToolReply}
	provider, err := llm.NewOpenAIProvider(llm.ProviderConfig{APIKey: "test-key", HTTPClient: &http.Client{Transport: recorder}})
	if err != nil {
		t.Fatal(err)
	}
	return provider, recorder
}

// chatDialect is the real chat-completions provider, with a recording transport.
func chatDialect(t *testing.T) (llm.Provider, *toolRecorder) {
	t.Helper()
	recorder := &toolRecorder{reply: chatToolReply}
	provider, err := llm.NewOpenAICompatibleProvider("testvendor", llm.ProviderConfig{
		APIKey: "test-key", BaseURL: "https://vendor.example/v1", HTTPClient: &http.Client{Transport: recorder}})
	if err != nil {
		t.Fatal(err)
	}
	return provider, recorder
}

// closedSchema is a function's parameters in the subset strict mode accepts.
func closedSchema(property string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{property: map[string]any{"type": "string"}},
		"required":             []any{property},
		"additionalProperties": false,
	}
}

// assistantTurn is the model's turn in a conversation: what it said, if it said
// anything, and the calls it made.
func assistantTurn(text string, calls ...llm.ToolCall) llm.Message {
	return llm.Message{Role: "assistant", Content: text, ToolCalls: calls}
}

func weatherCall(id string) llm.ToolCall {
	return llm.ToolCall{ID: id, Name: "get_weather", Arguments: json.RawMessage(`{"city":"Oslo"}`)}
}

// toolConversation is one request in the middle of a tool loop.
type toolConversation struct {
	name string
	req  llm.CompletionRequest
}

func toolConversations() []toolConversation {
	weather := llm.Tool{Name: "get_weather", Description: "Look up the weather for a city.", Parameters: closedSchema("city")}
	clock := llm.Tool{Name: "get_time", Description: "The time in a city.", Parameters: closedSchema("city")}
	ping := llm.Tool{Name: "ping", Description: "Checks the line."}
	base := func(tools []llm.Tool, messages ...llm.Message) llm.CompletionRequest {
		return llm.CompletionRequest{Model: "gpt-6-luna", SystemPrompt: "You answer questions about weather.", Tools: tools, Messages: messages}
	}
	return []toolConversation{
		{"a tool declared on the first turn", base([]llm.Tool{weather}, llm.Message{Role: "user", Content: "How warm is Oslo?"})},
		{"two tools declared", base([]llm.Tool{weather, clock}, llm.Message{Role: "user", Content: "How warm is Oslo?"})},
		{"a tool that takes no arguments", base([]llm.Tool{weather, ping}, llm.Message{Role: "user", Content: "Is the line up?"})},
		{"a tool declared on a request with no transcript", llm.CompletionRequest{Model: "gpt-6-luna", UserPrompt: "How warm is Oslo?", Tools: []llm.Tool{weather}}},
		{"a call and its result", base([]llm.Tool{weather},
			llm.Message{Role: "user", Content: "How warm is Oslo?"},
			assistantTurn("", weatherCall("call_1")),
			llm.Message{Role: "tool", Content: "7C and clear", ToolCallID: "call_1"})},
		{"a call after some words and its result", base([]llm.Tool{weather},
			llm.Message{Role: "user", Content: "How warm is Oslo?"},
			assistantTurn("Let me look.", weatherCall("call_1")),
			llm.Message{Role: "tool", Content: "7C and clear", ToolCallID: "call_1"})},
		{"two calls in one turn and both results", base([]llm.Tool{weather, clock},
			llm.Message{Role: "user", Content: "How warm is Oslo, and what time is it?"},
			assistantTurn("", weatherCall("call_1"), llm.ToolCall{ID: "call_2", Name: "get_time", Arguments: json.RawMessage(`{"city":"Oslo"}`)}),
			llm.Message{Role: "tool", Content: "7C", ToolCallID: "call_1"},
			llm.Message{Role: "tool", Content: "12:05", ToolCallID: "call_2"})},
		{"two calls in one turn, answered in the other order", base([]llm.Tool{weather, clock},
			llm.Message{Role: "user", Content: "How warm is Oslo, and what time is it?"},
			assistantTurn("", weatherCall("call_1"), llm.ToolCall{ID: "call_2", Name: "get_time", Arguments: json.RawMessage(`{"city":"Oslo"}`)}),
			llm.Message{Role: "tool", Content: "12:05", ToolCallID: "call_2"},
			llm.Message{Role: "tool", Content: "7C", ToolCallID: "call_1"})},
		{"a call with no arguments", base([]llm.Tool{weather, ping},
			llm.Message{Role: "user", Content: "Is the line up?"},
			assistantTurn("", llm.ToolCall{ID: "call_1", Name: "ping", Arguments: json.RawMessage(`{}`)}),
			llm.Message{Role: "tool", Content: "pong", ToolCallID: "call_1"})},
		{"a second loop after an answer", base([]llm.Tool{weather},
			llm.Message{Role: "user", Content: "How warm is Oslo?"},
			assistantTurn("", weatherCall("call_1")),
			llm.Message{Role: "tool", Content: "7C", ToolCallID: "call_1"},
			assistantTurn("It is 7C."),
			llm.Message{Role: "user", Content: "And now?"},
			assistantTurn("", weatherCall("call_2")),
			llm.Message{Role: "tool", Content: "8C", ToolCallID: "call_2"})},
		{"a result that is JSON text", base([]llm.Tool{weather},
			llm.Message{Role: "user", Content: "How warm is Oslo?"},
			assistantTurn("", weatherCall("call_1")),
			llm.Message{Role: "tool", Content: `{"temp":7,"sky":"clear"}`, ToolCallID: "call_1"})},
		{"a result that is empty", base([]llm.Tool{weather},
			llm.Message{Role: "user", Content: "How warm is Oslo?"},
			assistantTurn("", weatherCall("call_1")),
			llm.Message{Role: "tool", Content: "", ToolCallID: "call_1"})},
		{"text with quotes and a multi-byte character", base([]llm.Tool{weather},
			llm.Message{Role: "user", Content: "Say \"hi\" from Tromsø"},
			assistantTurn("", llm.ToolCall{ID: "call_1", Name: "get_weather", Arguments: json.RawMessage(`{"city":"Tromsø \"north\""}`)}),
			llm.Message{Role: "tool", Content: "-3C", ToolCallID: "call_1"})},
	}
}

var functionName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// jsonObject reports whether text is one JSON object.
func jsonObject(text string) bool {
	var decoded map[string]any
	return strings.HasPrefix(strings.TrimSpace(text), "{") && json.Unmarshal([]byte(text), &decoded) == nil
}

// keysOf lists the keys of an item, sorted, as one string.
func keysOf(item map[string]any) string {
	keys := make([]string, 0, len(item))
	for key := range item {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// responsesToolProblems applies what the Responses API defines for tools and for
// the items that carry a tool loop to one request body.
//
//   - A function tool is {type, name, description, parameters, strict}; the
//     name is letters, digits, underscores and dashes, up to 64; the parameters
//     are a JSON Schema object. A function with no strict flag is strict, so a
//     schema that is not a closed object (additionalProperties false, every
//     property required) is only accepted with the flag written false.
//   - The model's call is an input item {type: "function_call", call_id, name,
//     arguments}, with the arguments as a string of JSON text.
//   - A tool's result is an input item {type: "function_call_output", call_id,
//     output}, with the output as a string, after the call it answers. It has no
//     role.
//   - A turn is {role, content} with a role of system, developer, user or
//     assistant.
func responsesToolProblems(body map[string]any) []string {
	var problems []string

	if tools, ok := body["tools"].([]any); ok {
		names := map[string]bool{}
		for i, raw := range tools {
			tool, _ := raw.(map[string]any)
			where := fmt.Sprintf("tools[%d]", i)
			if tool["type"] != "function" {
				problems = append(problems, where+": type is not function")
				continue
			}
			name, _ := tool["name"].(string)
			if !functionName.MatchString(name) {
				problems = append(problems, where+": the name is not one the API accepts")
			}
			if names[name] {
				problems = append(problems, where+": the name is used twice")
			}
			names[name] = true
			parameters, _ := tool["parameters"].(map[string]any)
			if parameters["type"] != "object" {
				problems = append(problems, where+": the parameters are not an object schema")
			}
			strict, hasStrict := tool["strict"].(bool)
			if !hasStrict {
				problems = append(problems, where+": no strict flag is written, and the API reads that as strict")
			}
			if strict {
				for _, problem := range openObjects(parameters, where+".parameters") {
					problems = append(problems, where+" is strict: "+problem)
				}
			}
			if keys := keysOf(tool); keys != "description,name,parameters,strict,type" {
				problems = append(problems, where+": has the keys "+keys)
			}
		}
	}

	items, isArray := body["input"].([]any)
	if !isArray {
		return problems
	}
	called := map[string]bool{}
	answered := map[string]bool{}
	for i, raw := range items {
		item, _ := raw.(map[string]any)
		where := fmt.Sprintf("input[%d]", i)
		switch item["type"] {
		case "function_call":
			id, _ := item["call_id"].(string)
			name, _ := item["name"].(string)
			arguments, isText := item["arguments"].(string)
			switch {
			case id == "":
				problems = append(problems, where+": a function_call with no call_id")
			case called[id]:
				problems = append(problems, where+": the call_id "+id+" is used by two calls")
			}
			called[id] = true
			if name == "" {
				problems = append(problems, where+": a function_call with no name")
			}
			if !isText || !jsonObject(arguments) {
				problems = append(problems, where+": the arguments are not a string of JSON object text")
			}
			if keys := keysOf(item); keys != "arguments,call_id,name,type" {
				problems = append(problems, where+": a function_call has the keys "+keys)
			}
		case "function_call_output":
			id, _ := item["call_id"].(string)
			if !called[id] {
				problems = append(problems, where+": a function_call_output answers "+id+", which no earlier function_call made")
			}
			if answered[id] {
				problems = append(problems, where+": the call "+id+" is answered twice")
			}
			answered[id] = true
			if _, isText := item["output"].(string); !isText {
				problems = append(problems, where+": the output is not a string")
			}
			if keys := keysOf(item); keys != "call_id,output,type" {
				problems = append(problems, where+": a function_call_output has the keys "+keys)
			}
		case nil:
			role, _ := item["role"].(string)
			if role != "system" && role != "developer" && role != "user" && role != "assistant" {
				problems = append(problems, where+": a turn with the role "+role)
			}
			if _, isText := item["content"].(string); !isText {
				problems = append(problems, where+": the content of a turn is not a string")
			}
		default:
			problems = append(problems, where+": an item of a type nobody defined here")
		}
	}
	for id := range called {
		if !answered[id] {
			problems = append(problems, "the call "+id+" is never answered")
		}
	}
	return problems
}

// chatToolProblems applies what the chat-completions dialect defines.
//
//   - A tool is {type: "function", function: {name, description, parameters,
//     strict}}; strict is optional and false by default, and a strict function's
//     schema is a closed object.
//   - An assistant message that called tools carries tool_calls, each {id, type:
//     "function", function: {name, arguments}} with the arguments as a string of
//     JSON text, and a content that is null or text.
//   - A tool's result is a message with the role "tool", the tool_call_id of the
//     call it answers and its content as text, after the assistant message.
func chatToolProblems(body map[string]any) []string {
	var problems []string

	if tools, ok := body["tools"].([]any); ok {
		for i, raw := range tools {
			tool, _ := raw.(map[string]any)
			where := fmt.Sprintf("tools[%d]", i)
			function, _ := tool["function"].(map[string]any)
			if tool["type"] != "function" || len(tool) != 2 {
				problems = append(problems, where+": not {type: function, function}")
			}
			name, _ := function["name"].(string)
			if !functionName.MatchString(name) {
				problems = append(problems, where+": the name is not one the API accepts")
			}
			parameters, _ := function["parameters"].(map[string]any)
			if parameters["type"] != "object" {
				problems = append(problems, where+": the parameters are not an object schema")
			}
			if strict, present := function["strict"]; present {
				if strict != true {
					problems = append(problems, where+": strict is written and is not true")
				}
				for _, problem := range openObjects(parameters, where+".parameters") {
					problems = append(problems, where+" is strict: "+problem)
				}
			}
		}
	}
	switch choice := body["tool_choice"].(type) {
	case nil:
	case string:
		if choice != "auto" && choice != "none" && choice != "required" {
			problems = append(problems, "tool_choice is the string "+choice)
		}
	case map[string]any:
		function, _ := choice["function"].(map[string]any)
		if choice["type"] != "function" || function["name"] == nil || len(function) != 1 {
			problems = append(problems, "tool_choice is an object that is not {type: function, function: {name}}")
		}
	}

	messages, _ := body["messages"].([]any)
	called := map[string]bool{}
	answered := map[string]bool{}
	for i, raw := range messages {
		message, _ := raw.(map[string]any)
		where := fmt.Sprintf("messages[%d]", i)
		role, _ := message["role"].(string)
		switch role {
		case "system", "developer", "user":
			if _, isText := message["content"].(string); !isText {
				problems = append(problems, where+": the content is not a string")
			}
		case "assistant":
			content := message["content"]
			if _, isText := content.(string); !isText && content != nil {
				problems = append(problems, where+": the content is neither text nor null")
			}
			calls, hasCalls := message["tool_calls"].([]any)
			if _, present := message["tool_calls"]; present && (!hasCalls || len(calls) == 0) {
				problems = append(problems, where+": tool_calls is present and empty")
			}
			if content == nil && !hasCalls {
				problems = append(problems, where+": an assistant message with neither content nor calls")
			}
			for j, rawCall := range calls {
				call, _ := rawCall.(map[string]any)
				function, _ := call["function"].(map[string]any)
				id, _ := call["id"].(string)
				arguments, isText := function["arguments"].(string)
				callWhere := fmt.Sprintf("%s.tool_calls[%d]", where, j)
				switch {
				case id == "":
					problems = append(problems, callWhere+": no id")
				case called[id]:
					problems = append(problems, callWhere+": the id "+id+" is used twice")
				}
				called[id] = true
				if call["type"] != "function" || function["name"] == nil || function["name"] == "" {
					problems = append(problems, callWhere+": not {id, type: function, function: {name, arguments}}")
				}
				if !isText || !jsonObject(arguments) {
					problems = append(problems, callWhere+": the arguments are not a string of JSON object text")
				}
			}
		case "tool":
			id, _ := message["tool_call_id"].(string)
			if !called[id] {
				problems = append(problems, where+": a tool message answers "+id+", which no earlier assistant message called")
			}
			if answered[id] {
				problems = append(problems, where+": the call "+id+" is answered twice")
			}
			answered[id] = true
			if _, isText := message["content"].(string); !isText {
				problems = append(problems, where+": the content is not a string")
			}
		default:
			problems = append(problems, where+": the role "+role)
		}
	}
	for id := range called {
		if !answered[id] {
			problems = append(problems, "the call "+id+" is never answered")
		}
	}
	return problems
}

func TestResponsesRequestsOfAToolLoopAreWhatTheAPIDefines(t *testing.T) {
	provider, recorder := openAIResponses(t)
	for _, tc := range toolConversations() {
		t.Run(tc.name, func(t *testing.T) {
			recorder.take()
			resp, err := provider.Complete(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "call_9" || resp.Content != "" {
				t.Errorf("the reply was not read as a call: %+v", resp)
			}
			bodies := recorder.take()
			if len(bodies) != 1 {
				t.Fatalf("%d requests were sent, want 1", len(bodies))
			}
			for _, problem := range responsesToolProblems(bodies[0]) {
				t.Error(problem)
			}
			if len(tc.req.Tools) > 0 {
				if tools, _ := bodies[0]["tools"].([]any); len(tools) != len(tc.req.Tools) {
					t.Errorf("%d tools were declared, want %d", len(tools), len(tc.req.Tools))
				}
			}
			for _, problem := range bodyProblems(bodies[0]) {
				t.Error(problem)
			}
		})
	}
}

func TestChatDialectRequestsOfAToolLoopAreWhatTheAPIDefines(t *testing.T) {
	provider, recorder := chatDialect(t)
	for _, tc := range toolConversations() {
		t.Run(tc.name, func(t *testing.T) {
			recorder.take()
			resp, err := provider.Complete(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "call_9" || resp.Content != "" {
				t.Errorf("the reply was not read as a call: %+v", resp)
			}
			bodies := recorder.take()
			if len(bodies) != 1 {
				t.Fatalf("%d requests were sent, want 1", len(bodies))
			}
			for _, problem := range chatToolProblems(bodies[0]) {
				t.Error(problem)
			}
			if len(tc.req.Tools) > 0 {
				if tools, _ := bodies[0]["tools"].([]any); len(tools) != len(tc.req.Tools) {
					t.Errorf("%d tools were declared, want %d", len(tools), len(tc.req.Tools))
				}
			}
		})
	}
}

// The helpers are only as good as their own judgement of a body, so they are
// checked against bodies whose verdict is known.
func TestToolBodyProblems(t *testing.T) {
	closed := map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}, "required": []any{"a"}, "additionalProperties": false}
	open := map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}, "required": []any{}}
	responsesTool := func(parameters map[string]any, strict any) map[string]any {
		tool := map[string]any{"type": "function", "name": "t", "description": "d", "parameters": parameters}
		if strict != nil {
			tool["strict"] = strict
		}
		return map[string]any{"tools": []any{tool}}
	}
	call := map[string]any{"type": "function_call", "call_id": "c1", "name": "t", "arguments": `{"a":"b"}`}
	output := map[string]any{"type": "function_call_output", "call_id": "c1", "output": "x"}
	oldStyleResult := map[string]any{"role": "tool", "content": "x", "call_id": "c1"}
	input := func(items ...any) map[string]any { return map[string]any{"input": items} }

	responsesCases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"nothing", map[string]any{}, 0},
		{"a closed strict tool", responsesTool(closed, true), 0},
		{"an open tool written non-strict", responsesTool(open, false), 0},
		{"an open tool written strict", responsesTool(open, true), 2},
		{"a tool with no strict flag", responsesTool(closed, nil), 2},
		{"a call and its result", input(call, output), 0},
		{"a result with no call", input(output), 1},
		{"a call never answered", input(call), 1},
		{"a result before its call", input(output, call), 1},
		{"a result written as a turn with a role", input(call, oldStyleResult), 2},
		{"a call answered twice", input(call, output, output), 1},
		{"a call whose arguments are not an object", input(map[string]any{"type": "function_call", "call_id": "c1", "name": "t", "arguments": "[1]"}, output), 1},
		{"a call whose arguments are an object and not text", input(map[string]any{"type": "function_call", "call_id": "c1", "name": "t", "arguments": map[string]any{"a": "b"}}, output), 1},
		{"a turn with a role nobody defined", input(map[string]any{"role": "tool", "content": "x"}), 1},
		{"plain turns", input(map[string]any{"role": "user", "content": "x"}, map[string]any{"role": "assistant", "content": "y"}), 0},
	}
	for _, tc := range responsesCases {
		t.Run("responses/"+tc.name, func(t *testing.T) {
			if got := responsesToolProblems(tc.body); len(got) != tc.want {
				t.Fatalf("found %d problem(s) %v, want %d", len(got), got, tc.want)
			}
		})
	}

	chatTool := func(parameters map[string]any, strict any) map[string]any {
		function := map[string]any{"name": "t", "parameters": parameters}
		if strict != nil {
			function["strict"] = strict
		}
		return map[string]any{"tools": []any{map[string]any{"type": "function", "function": function}}}
	}
	assistant := map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{
		map[string]any{"id": "c1", "type": "function", "function": map[string]any{"name": "t", "arguments": `{"a":"b"}`}}}}
	result := map[string]any{"role": "tool", "tool_call_id": "c1", "content": "x"}
	messages := func(items ...any) map[string]any { return map[string]any{"messages": items} }

	chatCases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"a closed strict tool", chatTool(closed, true), 0},
		{"an open tool with no flag", chatTool(open, nil), 0},
		{"an open tool written strict", chatTool(open, true), 2},
		{"a call and its result", messages(assistant, result), 0},
		{"a result with no call", messages(result), 1},
		{"a call never answered", messages(assistant), 1},
		{"a result before its call", messages(result, assistant), 1},
		{"a result with the wrong role", messages(assistant, map[string]any{"role": "function", "content": "x"}), 2},
		{"an assistant message with nothing in it", messages(map[string]any{"role": "assistant", "content": nil}), 1},
		{"a tool choice that is a word nobody defined", map[string]any{"tool_choice": "any"}, 1},
		{"a tool choice naming a function", map[string]any{"tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": "t"}}}, 0},
	}
	for _, tc := range chatCases {
		t.Run("chat/"+tc.name, func(t *testing.T) {
			if got := chatToolProblems(tc.body); len(got) != tc.want {
				t.Fatalf("found %d problem(s) %v, want %d", len(got), got, tc.want)
			}
		})
	}
}
