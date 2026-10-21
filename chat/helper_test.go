package chat_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// lookupTool and clockTool are the two tools the conversations declare.
var (
	lookupTool = schemaflux.Tool{
		Name:        "lookup",
		Description: "Look a record up by its id.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"id": map[string]any{"type": "string"}},
			"required":   []string{"id"},
		},
	}
	clockTool = schemaflux.Tool{Name: "clock", Description: "The time."}
)

// toolCall is a call a scripted model asks for.
func toolCall(id, name, arguments string) schemaflux.ToolCall {
	return schemaflux.ToolCall{ID: id, Name: name, Arguments: json.RawMessage(arguments)}
}

// asking is a reply that is only tool calls.
func asking(calls ...schemaflux.ToolCall) schemaflux.Response {
	return schemaflux.Response{ToolCalls: calls, FinishReason: "tool_calls"}
}

// saying is a reply that is words.
func saying(text string) schemaflux.Response {
	return schemaflux.Response{Content: text, FinishReason: "stop"}
}

// scripted is a fake model that answers its n-th request with the n-th reply,
// the last repeating.
func scripted(replies ...schemaflux.Response) *schemafluxtest.Provider {
	model := &schemafluxtest.Provider{}
	model.Respond(func(call int, _ schemaflux.Request) (schemaflux.Response, error) {
		return replies[min(call, len(replies)-1)], nil
	})
	return model
}

// onClient is a context whose client sends to p, with the settings applied.
func onClient(p schemaflux.Provider, settings ...func(*schemaflux.Client)) context.Context {
	client := schemaflux.Client{Provider: p}
	for _, set := range settings {
		set(&client)
	}
	return schemaflux.WithClient(context.Background(), client)
}

// recorded is ctx with a new recorder on it.
func recorded(ctx context.Context) (context.Context, *schemaflux.Recorder) {
	var recorder schemaflux.Recorder
	return schemaflux.WithRecorder(ctx, &recorder), &recorder
}

// copyHistory is a copy of a history that shares nothing with it, so that a
// later comparison finds what changed and not what moved with it.
func copyHistory(history []schemaflux.Message) []schemaflux.Message {
	if history == nil {
		return nil
	}
	out := make([]schemaflux.Message, len(history))
	for i, m := range history {
		if m.ToolCalls != nil {
			m.ToolCalls = make([]schemaflux.ToolCall, len(history[i].ToolCalls))
			for j, c := range history[i].ToolCalls {
				c.Arguments = append(json.RawMessage(nil), c.Arguments...)
				m.ToolCalls[j] = c
			}
		}
		out[i] = m
	}
	return out
}

// requireHistory fails the test unless got is want, element by element, down to
// the bytes of a call's arguments, and says which element is not.
func requireHistory(t *testing.T, got, want []schemaflux.Message) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("History has %d messages, want %d:\n got %+v\nwant %+v", len(got), len(want), got, want)
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("History[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	if (got == nil) != (want == nil) {
		t.Errorf("History is nil: %v, want nil: %v", got == nil, want == nil)
	}
}

// requireKind fails the test unless err is an *Error of kind, and returns it.
func requireKind(t *testing.T, err error, kind schemaflux.Kind) *schemaflux.Error {
	t.Helper()
	var got *schemaflux.Error
	if !errors.As(err, &got) {
		t.Fatalf("the error is not a *schemaflux.Error: %T %v", err, err)
	}
	if got.Kind != kind {
		t.Fatalf("the error is of kind %v, want %v: %v", got.Kind, kind, err)
	}
	return got
}

// chain is every text an error holds: its own message and that of each error it
// wraps, however it wraps them, and its Kind and operation. A payload in any of
// them is a payload in a log line the caller writes.
func chain(err error) []string {
	var texts []string
	var walk func(error)
	walk = func(err error) {
		if err == nil {
			return
		}
		texts = append(texts, err.Error())
		var e *schemaflux.Error
		if errors.As(err, &e) {
			texts = append(texts, e.Op, e.Message, e.Provider, e.Model)
		}
		switch wrapped := err.(type) {
		case interface{ Unwrap() error }:
			walk(wrapped.Unwrap())
		case interface{ Unwrap() []error }:
			for _, inner := range wrapped.Unwrap() {
				walk(inner)
			}
		}
	}
	walk(err)
	return texts
}

// requireNone fails the test if any of texts holds any of the canaries.
func requireNone(t *testing.T, where string, texts []string, canaries ...string) {
	t.Helper()
	for _, text := range texts {
		for _, canary := range canaries {
			if strings.Contains(text, canary) {
				t.Errorf("%s holds %q: %s", where, canary, text)
			}
		}
	}
}
