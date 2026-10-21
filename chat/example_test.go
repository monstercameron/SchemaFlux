package chat_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/chat"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// weatherModel is a scripted model for the examples: it asks for the weather
// tool on the first turn and answers in words on the second.
func weatherModel() *schemafluxtest.Provider {
	model := &schemafluxtest.Provider{}
	model.Respond(func(call int, req schemaflux.Request) (schemaflux.Response, error) {
		if call == 0 {
			return schemaflux.Response{ToolCalls: []schemaflux.ToolCall{
				{ID: "call_1", Name: "get_weather", Arguments: json.RawMessage(`{"city":"Oslo"}`)},
			}}, nil
		}
		return schemaflux.Response{Content: "It is " + req.Messages[len(req.Messages)-1].Content + " in Oslo."}, nil
	})
	return model
}

var weatherTool = schemaflux.Tool{
	Name:        "get_weather",
	Description: "The current temperature in a city.",
	Parameters: map[string]any{
		"type":       "object",
		"properties": map[string]any{"city": map[string]any{"type": "string"}},
		"required":   []string{"city"},
	},
}

// A conversation in which the model asks for a tool: the caller runs it and
// gives the result back, and the loop ends when the model answers in words.
func Example() {
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: weatherModel()})

	s := chat.Session{
		Instructions: "You answer questions about the weather. Use get_weather.",
		Tools:        []schemaflux.Tool{weatherTool},
	}
	reply, err := s.Send(ctx, "How warm is Oslo?")
	for err == nil && len(reply.ToolCalls) > 0 {
		results := map[string]string{}
		for _, call := range reply.ToolCalls {
			results[call.ID] = "8 degrees" // the caller's code runs the tool here
		}
		reply, err = s.SendResults(ctx, results)
	}
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(reply.Content)
	fmt.Println(len(s.History), "messages in the history")
	// Output:
	// It is 8 degrees in Oslo.
	// 4 messages in the history
}

// Send returns the model's reply, which is also the last message of History. A
// reply that asks for tools has calls and may have no words.
func ExampleSession_Send() {
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: weatherModel()})

	s := chat.Session{Tools: []schemaflux.Tool{weatherTool}}
	reply, err := s.Send(ctx, "How warm is Oslo?")
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Printf("%q asked for %s with %s\n", reply.Role, reply.ToolCalls[0].Name, reply.ToolCalls[0].Arguments)
	fmt.Println(len(reply.Content), "words")
	// Output:
	// "assistant" asked for get_weather with {"city":"Oslo"}
	// 0 words
}

// SendResults answers every call of the last reply, by id, and asks the model to
// continue. A result that is missing or that answers no call is refused before
// anything is sent, and History stays as it was.
func ExampleSession_SendResults() {
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: weatherModel()})

	s := chat.Session{Tools: []schemaflux.Tool{weatherTool}}
	if _, err := s.Send(ctx, "How warm is Oslo?"); err != nil {
		fmt.Println("error:", err)
		return
	}
	_, err := s.SendResults(ctx, map[string]string{"call_9": "8 degrees"})
	fmt.Println(errors.Is(err, schemaflux.ErrInvalidRequest), len(s.History))

	reply, err := s.SendResults(ctx, map[string]string{"call_1": "8 degrees"})
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(reply.Content)
	// Output:
	// true 2
	// It is 8 degrees in Oslo.
}
