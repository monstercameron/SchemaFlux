package schemafluxtest_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func weatherTools() []schemaflux.Tool {
	return []schemaflux.Tool{
		{Name: "weather", Description: "the weather in a city", Parameters: map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}}},
		{Name: "clock"},
	}
}

func askedForWeather() []schemaflux.Message {
	return []schemaflux.Message{
		{Role: "user", Content: "How warm is Oslo?"},
		{Role: "assistant", ToolCalls: []schemaflux.ToolCall{{ID: "call_1", Name: "weather", Arguments: json.RawMessage(`{"city":"Oslo"}`)}}},
		{Role: "tool", ToolCallID: "call_1", Content: "8 degrees"},
	}
}

func TestAProviderScriptsAReplyThatIsOnlyToolCallsAndOneThatIsText(t *testing.T) {
	t.Parallel()
	var p schemafluxtest.Provider
	p.Respond(func(call int, req schemaflux.Request) (schemaflux.Response, error) {
		if call == 0 {
			return schemaflux.Response{
				FinishReason: "tool_calls",
				ToolCalls: []schemaflux.ToolCall{
					{ID: "a", Name: "weather", Arguments: json.RawMessage(`{"city":"Oslo"}`)},
					{ID: "b", Name: "clock", Arguments: json.RawMessage(`{}`)},
				},
			}, nil
		}
		return schemaflux.Response{Content: "It is 8 degrees.", FinishReason: "stop"}, nil
	})
	first, err := p.Complete(context.Background(), schemaflux.Request{Tools: weatherTools(), Messages: askedForWeather()[:1]})
	if err != nil {
		t.Fatal(err)
	}
	if first.Content != "" || len(first.ToolCalls) != 2 || first.ToolCalls[0].ID != "a" || first.ToolCalls[1].Name != "clock" ||
		string(first.ToolCalls[0].Arguments) != `{"city":"Oslo"}` || first.FinishReason != "tool_calls" {
		t.Errorf("the reply of calls = %+v", first)
	}
	second, err := p.Complete(context.Background(), schemaflux.Request{Tools: weatherTools(), Messages: askedForWeather()})
	if err != nil || second.Content != "It is 8 degrees." || len(second.ToolCalls) != 0 {
		t.Errorf("the reply of text = %+v, %v", second, err)
	}
	// The ordinary scripts still answer in words, whatever the request holds.
	var plain schemafluxtest.Provider
	plain.Reply("words")
	if resp, err := plain.Complete(context.Background(), schemaflux.Request{Tools: weatherTools()}); err != nil || resp.Content != "words" || resp.ToolCalls != nil {
		t.Errorf("Reply = %+v, %v", resp, err)
	}
}

func TestAProviderRecordsTheToolsAndMessagesItReceived(t *testing.T) {
	t.Parallel()
	var p schemafluxtest.Provider
	tools, messages := weatherTools(), askedForWeather()
	want := schemaflux.Request{System: "s", Tools: weatherTools(), Messages: askedForWeather()}
	if _, err := p.Complete(context.Background(), schemaflux.Request{System: "s", Tools: tools, Messages: messages}); err != nil {
		t.Fatal(err)
	}
	if got := p.Last(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Last = %+v, want %+v", got, want)
	}
	if got := p.Requests(); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("Requests = %+v", got)
	}

	// What the caller does to its own lists after the call changes nothing the
	// provider recorded: a history that is edited between turns is not a
	// rewrite of what the model was sent.
	tools[0].Name, messages[0].Content = "changed", "changed"
	messages[1].ToolCalls[0].Name = "changed"
	messages[1].ToolCalls[0].Arguments[2] = 'X'
	messages = append(messages, schemaflux.Message{Role: "user", Content: "later"})
	if got := p.Last(); !reflect.DeepEqual(got, want) {
		t.Errorf("an edit after the call reached the record: %+v", got)
	}

	// And what the test does to what it read changes nothing the next read sees.
	read := p.Last()
	read.Tools[1].Name, read.Messages[2].Content = "changed", "changed"
	read.Messages[1].ToolCalls[0].Arguments[2] = 'Y'
	if got := p.Requests()[0]; !reflect.DeepEqual(got, want) {
		t.Errorf("an edit of what Last returned reached the record: %+v", got)
	}
}

func TestAProviderRecordsNilAsNilAndEmptyAsEmpty(t *testing.T) {
	t.Parallel()
	var p schemafluxtest.Provider
	_, _ = p.Complete(context.Background(), schemaflux.Request{})
	_, _ = p.Complete(context.Background(), schemaflux.Request{Tools: []schemaflux.Tool{}, Messages: []schemaflux.Message{{Role: "user", ToolCalls: []schemaflux.ToolCall{}}}})
	got := p.Requests()
	if got[0].Tools != nil || got[0].Messages != nil {
		t.Errorf("a request with none recorded %+v", got[0])
	}
	if got[1].Tools == nil || len(got[1].Tools) != 0 || got[1].Messages[0].ToolCalls == nil {
		t.Errorf("empty lists were recorded as nil: %+v", got[1])
	}
}

func TestAProviderGivesTheToolsAndMessagesOfEveryRequestInOrder(t *testing.T) {
	t.Parallel()
	var p schemafluxtest.Provider
	history := askedForWeather()
	for i := 1; i <= len(history); i++ {
		if _, err := p.Complete(context.Background(), schemaflux.Request{Tools: weatherTools(), Messages: history[:i]}); err != nil {
			t.Fatal(err)
		}
	}
	requests := p.Requests()
	if len(requests) != 3 {
		t.Fatalf("%d requests", len(requests))
	}
	for i, req := range requests {
		if len(req.Messages) != i+1 || len(req.Tools) != 2 {
			t.Errorf("request %d holds %d messages and %d tools", i+1, len(req.Messages), len(req.Tools))
		}
	}
}
