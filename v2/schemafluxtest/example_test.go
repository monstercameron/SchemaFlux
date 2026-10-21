package schemafluxtest_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// A test scripts what the fake answers, makes its calls, and reads what the fake
// was sent. Here the fake is called directly; in a test of code that uses the
// library it is the Provider of the client on the context.
func Example() {
	var model schemafluxtest.Provider
	model.Reply(`{"number":"A-17","total":120.5}`)

	resp, err := model.Complete(context.Background(), schemaflux.Request{
		System:     "Extract the invoice.",
		User:       "Invoice A-17 for 120.50",
		SchemaName: "Invoice",
	})
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(resp.Content)
	fmt.Println(model.Calls(), "request, about", model.Last().SchemaName)
	// Output:
	// {"number":"A-17","total":120.5}
	// 1 request, about Invoice
}

// ReplyWhen answers by the operation a request is about, so a test of code that
// makes several kinds of call does not write a string match of its own.
func ExampleProvider_ReplyWhen() {
	var model schemafluxtest.Provider
	model.ReplyWhen("invoice", `{"total":120.5}`)
	model.ReplyWhen("summar", "A short summary.")
	model.Reply("{}") // every other request

	for _, req := range []schemaflux.Request{
		{SchemaName: "Invoice"},
		{System: "You summarize the text."},
		{System: "You do something else."},
	} {
		resp, _ := model.Complete(context.Background(), req)
		fmt.Println(resp.Content)
	}
	// Output:
	// {"total":120.5}
	// A short summary.
	// {}
}

// Fail scripts an error, and a kind scripts what the library does about it.
// The fake retries nothing, so one failure is one request.
func ExampleProvider_Fail() {
	var model schemafluxtest.Provider
	model.Fail(&schemaflux.Error{Kind: schemaflux.ErrRateLimited, Status: 429, RetryAfter: time.Second})

	_, err := model.Complete(context.Background(), schemaflux.Request{})
	fmt.Println(errors.Is(err, schemaflux.ErrRateLimited), model.Calls())
	// Output:
	// true 1
}

// FailThen fails the first requests and then answers, to show what a retry
// does with a failure that passes.
func ExampleProvider_FailThen() {
	var model schemafluxtest.Provider
	model.FailThen(2, schemaflux.ErrUnavailable, "ok")

	for range 3 {
		resp, err := model.Complete(context.Background(), schemaflux.Request{})
		fmt.Printf("%q %v\n", resp.Content, err != nil)
	}
	// Output:
	// "" true
	// "" true
	// "ok" false
}

// Respond is the general form: the whole Response, chosen by the request. Here
// an answer is cut short, which no body of a Reply can say.
func ExampleProvider_Respond() {
	var model schemafluxtest.Provider
	model.Respond(func(call int, req schemaflux.Request) (schemaflux.Response, error) {
		return schemaflux.Response{
			Content:      `{"summary":"The invoice is`,
			FinishReason: "length",
			Usage:        schemaflux.Usage{Input: 120, Output: 16, Total: 136},
		}, nil
	})

	resp, _ := model.Complete(context.Background(), schemaflux.Request{})
	fmt.Println(resp.FinishReason, resp.Usage.Total)
	// Output:
	// length 136
}

// Shape answers with a value that fits the request's own schema, for a test
// about what was sent that has no opinion on the answer.
func ExampleProvider_Shape() {
	var model schemafluxtest.Provider
	model.Shape()

	resp, _ := model.Complete(context.Background(), schemaflux.Request{
		SchemaName: "Invoice",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"number": map[string]any{"type": "string"},
				"total":  map[string]any{"type": "number"},
			},
			"required":             []any{"number", "total"},
			"additionalProperties": false,
		},
	})
	var answer map[string]any
	_ = json.Unmarshal([]byte(resp.Content), &answer)
	keys := make([]string, 0, len(answer))
	for key := range answer {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	fmt.Println(keys)
	// Output:
	// [number total]
}

// A Decider answers the typed questions of a decision, one answer for each
// question in order, with the probability the model reports when the test gives
// one.
func ExampleDecider() {
	decider := schemafluxtest.NewDecider(
		schemafluxtest.Yes(0.97),
		schemafluxtest.Chose("billing"),
		schemafluxtest.Level(4, 0.6),
	)
	resp, err := decider.Complete(context.Background(), schemaflux.Request{
		Decision: &schemaflux.Decision{
			State: `"My invoice is wrong"`,
			Questions: []schemaflux.DecisionQuestion{
				{Kind: schemaflux.DecisionYesNo, Ask: "Is it urgent?"},
				{Kind: schemaflux.DecisionChoice, Ask: "Which team?", Options: []string{"billing", "support"}},
				{Kind: schemaflux.DecisionScale, Ask: "How upset?", Low: 1, High: 5},
			},
		},
	})
	fmt.Println(err)
	for _, a := range resp.Answers {
		fmt.Println(a.Kind, a.Yes, a.Label, a.Level, a.ModelConfidence, a.ConfidenceReported)
	}
	// Output:
	// <nil>
	// 1 true  0 0.97 true
	// 2 false billing 0 0 false
	// 3 false  4 0.6 true
}

// A question the Decider was not scripted for is a failure that says what was
// asked and what was scripted, never a zero answer.
func ExampleDecider_Complete() {
	decider := schemafluxtest.NewDecider(schemafluxtest.Yes())
	_, err := decider.Complete(context.Background(), schemaflux.Request{
		Decision: &schemaflux.Decision{
			Questions: []schemaflux.DecisionQuestion{
				{Kind: schemaflux.DecisionYesNo, Ask: "First?"},
				{Kind: schemaflux.DecisionChoice, Ask: "Second?", Options: []string{"a", "b", "c"}},
			},
		},
	})
	var e *schemaflux.Error
	if errors.As(err, &e) {
		fmt.Println(e.Kind)
		fmt.Println(e.Message)
	}
	// Output:
	// configuration
	// no answer is scripted for question 2 of request 1 (a choice among 3 options; the request asks 2 questions); scripted: 1 answer (yes), 0 already used
}

// Stream delivers the scripted answer in pieces and then a last chunk that
// holds the whole response, so code that reads a streamed answer is tested
// without a stub of its own.
func ExampleProvider_Stream() {
	var model schemafluxtest.Provider
	model.Reply("Hello, world")

	for chunk, err := range model.Stream(context.Background(), schemaflux.Request{}) {
		if err != nil {
			fmt.Println("error:", err)
			return
		}
		if chunk.Done {
			fmt.Printf("done: %q after %s\n", chunk.Response.Content, chunk.Response.FinishReason)
			continue
		}
		fmt.Printf("%q\n", chunk.Delta)
	}
	// Output:
	// "Hell"
	// "o, w"
	// "orld"
	// done: "Hello, world" after stop
}
