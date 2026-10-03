package schemaflux

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/monstercameron/schemaflux/internal/llm"
)

func TestToolsConvertBothWaysWithoutLoss(t *testing.T) {
	t.Parallel()
	object := map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}}
	cases := []struct {
		name  string
		tools []Tool
	}{
		{"nil", nil},
		{"empty", []Tool{}},
		{"one with everything", []Tool{{Name: "weather", Description: "the weather in a city", Parameters: object}}},
		{"no description", []Tool{{Name: "ping", Parameters: object}}},
		{"no parameters", []Tool{{Name: "now", Description: "the time"}}},
		{"an empty schema", []Tool{{Name: "x", Parameters: map[string]any{}}}},
		{"several in order", []Tool{{Name: "a"}, {Name: "b"}, {Name: "c", Description: "third"}}},
		{"unicode", []Tool{{Name: "météo", Description: "température en °C, λ"}}},
		{"a long description", []Tool{{Name: "long", Description: string(make([]byte, 5000))}}},
		{"an empty name is carried, not repaired", []Tool{{Name: "", Description: "nameless"}}},
		{"a repeated name is carried, not repaired", []Tool{{Name: "a"}, {Name: "a"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			internal := toolsToInternal(c.tools)
			if (c.tools == nil) != (internal == nil) || len(internal) != len(c.tools) {
				t.Fatalf("the internal list is %#v for %#v", internal, c.tools)
			}
			for i, tool := range c.tools {
				if internal[i].Name != tool.Name || internal[i].Description != tool.Description || !reflect.DeepEqual(internal[i].Parameters, tool.Parameters) {
					t.Errorf("tool %d went to %#v", i, internal[i])
				}
			}
			if back := toolsFromInternal(internal); !reflect.DeepEqual(back, c.tools) {
				t.Errorf("came back as %#v, was %#v", back, c.tools)
			}
		})
	}
}

func TestToolCallsConvertBothWaysWithoutLoss(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		calls []ToolCall
	}{
		{"nil", nil},
		{"empty", []ToolCall{}},
		{"one", []ToolCall{{ID: "call_1", Name: "weather", Arguments: json.RawMessage(`{"city":"Oslo"}`)}}},
		{"no arguments at all", []ToolCall{{ID: "c", Name: "n"}}},
		{"empty object", []ToolCall{{ID: "c", Name: "n", Arguments: json.RawMessage(`{}`)}}},
		{"two", []ToolCall{{ID: "a", Name: "x", Arguments: json.RawMessage(`{"n":1}`)}, {ID: "b", Name: "x", Arguments: json.RawMessage(`{"n":2}`)}}},
		{"whitespace is kept as the model wrote it", []ToolCall{{ID: "c", Name: "n", Arguments: json.RawMessage(" { \"a\" : 1 }\n")}}},
		{"not an object is carried, not repaired", []ToolCall{{ID: "c", Name: "n", Arguments: json.RawMessage(`[1,2]`)}}},
		{"no id is carried, not repaired", []ToolCall{{Name: "n", Arguments: json.RawMessage(`{}`)}}},
		{"unicode", []ToolCall{{ID: "é", Name: "ñ", Arguments: json.RawMessage(`{"λ":"☃"}`)}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			internal := callsToInternal(c.calls)
			if (c.calls == nil) != (internal == nil) || len(internal) != len(c.calls) {
				t.Fatalf("the internal list is %#v for %#v", internal, c.calls)
			}
			for i, call := range c.calls {
				if internal[i].ID != call.ID || internal[i].Name != call.Name || !reflect.DeepEqual(internal[i].Arguments, call.Arguments) {
					t.Errorf("call %d went to %#v", i, internal[i])
				}
			}
			if back := callsFromInternal(internal); !reflect.DeepEqual(back, c.calls) {
				t.Errorf("came back as %#v, was %#v", back, c.calls)
			}
		})
	}
}

func TestMessagesConvertBothWaysWithoutLoss(t *testing.T) {
	t.Parallel()
	call := ToolCall{ID: "a", Name: "lookup", Arguments: json.RawMessage(`{"x":1}`)}
	cases := []struct {
		name     string
		messages []Message
	}{
		{"nil", nil},
		{"empty", []Message{}},
		{"a user message", []Message{{Role: "user", Content: "hello"}}},
		{"an assistant answer", []Message{{Role: "assistant", Content: "done"}}},
		{"an assistant that asks for tools and says nothing", []Message{{Role: "assistant", ToolCalls: []ToolCall{call}}}},
		{"an assistant that asks for tools and says something", []Message{{Role: "assistant", Content: "looking", ToolCalls: []ToolCall{call}}}},
		{"an empty call list stays empty", []Message{{Role: "assistant", ToolCalls: []ToolCall{}}}},
		{"a tool result", []Message{{Role: "tool", ToolCallID: "a", Content: "result"}}},
		{"an empty tool result", []Message{{Role: "tool", ToolCallID: "b"}}},
		{"a whole exchange", []Message{
			{Role: "user", Content: "q"},
			{Role: "assistant", ToolCalls: []ToolCall{call, {ID: "b", Name: "lookup", Arguments: json.RawMessage(`{}`)}}},
			{Role: "tool", ToolCallID: "a", Content: "one"},
			{Role: "tool", ToolCallID: "b", Content: "two"},
			{Role: "assistant", Content: "answer"},
		}},
		{"a role that is no role is carried, not repaired", []Message{{Role: "system", Content: "x"}}},
		{"unicode", []Message{{Role: "user", Content: "unicode λ ☃"}}},
		{"the zero message", []Message{{}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			internal := messagesToInternal(c.messages)
			if (c.messages == nil) != (internal == nil) || len(internal) != len(c.messages) {
				t.Fatalf("the internal list is %#v for %#v", internal, c.messages)
			}
			for i, m := range c.messages {
				if internal[i].Role != m.Role || internal[i].Content != m.Content || internal[i].ToolCallID != m.ToolCallID || len(internal[i].ToolCalls) != len(m.ToolCalls) {
					t.Errorf("message %d went to %#v", i, internal[i])
				}
			}
			if back := messagesFromInternal(internal); !reflect.DeepEqual(back, c.messages) {
				t.Errorf("came back as %#v, was %#v", back, c.messages)
			}
		})
	}
}

// A provider that edits the list it was handed edits a copy: the lists, the
// messages and the call lists are new on every conversion. Only the bytes of an
// argument and the map of a schema are shared, as the types say.
func TestConvertedListsAreCopiesOneLevelDeep(t *testing.T) {
	t.Parallel()
	tools := []Tool{{Name: "a", Parameters: map[string]any{"type": "object"}}}
	calls := []ToolCall{{ID: "1", Name: "a", Arguments: json.RawMessage(`{}`)}}
	messages := []Message{{Role: "assistant", ToolCalls: calls}, {Role: "tool", ToolCallID: "1", Content: "r"}}

	internalTools := toolsToInternal(tools)
	internalTools[0].Name = "changed"
	if tools[0].Name != "a" {
		t.Error("an edit of a converted tool reached the caller's list")
	}
	internalMessages := messagesToInternal(messages)
	internalMessages[0].Content, internalMessages[0].ToolCalls[0].Name = "changed", "changed"
	internalMessages[1].Content = "changed"
	if messages[0].Content != "" || messages[0].ToolCalls[0].Name != "a" || messages[1].Content != "r" || calls[0].Name != "a" {
		t.Errorf("an edit of a converted message reached the caller's: %#v", messages)
	}

	// And the other way: what a provider returned is not shared with the copy
	// the library keeps.
	source := []llm.Message{{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "1", Name: "a"}}}}
	back := messagesFromInternal(source)
	back[0].ToolCalls[0].Name, back[0].Content = "changed", "changed"
	if source[0].ToolCalls[0].Name != "a" || source[0].Content != "" {
		t.Errorf("an edit of a converted internal message reached its source: %#v", source)
	}
	sourceCalls := []llm.ToolCall{{ID: "1", Name: "a"}}
	callsFromInternal(sourceCalls)[0].ID = "changed"
	sourceTools := []llm.Tool{{Name: "a"}}
	toolsFromInternal(sourceTools)[0].Name = "changed"
	if sourceCalls[0].ID != "1" || sourceTools[0].Name != "a" {
		t.Errorf("an edit of a converted list reached its source: %#v %#v", sourceCalls, sourceTools)
	}
}
