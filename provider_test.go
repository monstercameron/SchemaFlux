package schemaflux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// requestNotCarried are the fields of llm.CompletionRequest that a Request does
// not have, on purpose: no code of the library sets ToolChoice, so it is not an
// option a Provider is offered, and a provider seeing a request without it would
// answer a different question, so an adapter refuses a request that has one.
var requestNotCarried = map[string]bool{"ToolChoice": true}

// responseNotCarried are the fields of llm.CompletionResponse that a Response
// does not have, on purpose. None: a Response carries every one.
var responseNotCarried = map[string]bool{}

// usageRead are the fields of types.TokenUsage that a Usage reads only when the
// field it repeats is zero (a provider reports either spelling), and the field
// of Usage and the repeated field they come back as.
var usageRead = map[string]struct {
	field string
	back  string
}{
	"InputTokens":  {"Input", "PromptTokens"},
	"OutputTokens": {"Output", "CompletionTokens"},
}

// scriptedProvider is a Provider that answers with a function.
type scriptedProvider struct {
	name     string
	complete func(ctx context.Context, req Request) (Response, error)
}

func (p *scriptedProvider) Name() string { return p.name }

func (p *scriptedProvider) Complete(ctx context.Context, req Request) (Response, error) {
	return p.complete(ctx, req)
}

// retryingProvider is a scriptedProvider that has retry settings of its own.
type retryingProvider struct {
	scriptedProvider
	retries int
	backoff time.Duration
}

func (p *retryingProvider) RetryPolicy() (int, time.Duration) { return p.retries, p.backoff }

// scriptedInternalProvider is an llm.Provider that answers with a function.
type scriptedInternalProvider struct {
	name    string
	retries int
	backoff time.Duration
	answer  func(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error)
}

func (p *scriptedInternalProvider) Name() string                               { return p.name }
func (p *scriptedInternalProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *scriptedInternalProvider) RetryPolicy() (int, time.Duration)          { return p.retries, p.backoff }
func (p *scriptedInternalProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	return p.answer(ctx, req)
}

func failingProvider(err error) *scriptedProvider {
	return &scriptedProvider{name: "custom", complete: func(context.Context, Request) (Response, error) { return Response{}, err }}
}

func TestEveryFieldOfTheInternalRequestIsCarriedOrListed(t *testing.T) {
	t.Parallel()
	internalType := reflect.TypeOf(llm.CompletionRequest{})
	var carried int
	for _, name := range fieldNames(internalType) {
		t.Run(name, func(t *testing.T) {
			set := onlyField(t, internalType, name).Interface().(llm.CompletionRequest)
			converted, err := requestFromInternal(set)
			if requestNotCarried[name] {
				if err == nil {
					t.Fatalf("llm.CompletionRequest.%s is on the list of fields not carried, and a request that sets it was passed on", name)
				}
				var e *Error
				if !errors.As(err, &e) || e.Kind != ErrUnsupported {
					t.Errorf("got %v, want an ErrUnsupported error", err)
				}
				return
			}
			carried++
			if err != nil {
				t.Fatalf("requestFromInternal: %v", err)
			}
			if reflect.DeepEqual(converted, Request{}) {
				t.Fatalf("llm.CompletionRequest.%s is neither carried by requestFromInternal nor on the list of fields not carried: add it to Request and to both conversions, or to the list with the reason", name)
			}
			if back := requestToInternal(converted); !reflect.DeepEqual(back, set) {
				t.Errorf("llm.CompletionRequest.%s does not come back through Request:\n got %+v\nwant %+v", name, back, set)
			}
		})
	}
	t.Run("a Request has one field for each carried field", func(t *testing.T) {
		want := len(fieldNames(internalType)) - len(requestNotCarried)
		if got := reflect.TypeOf(Request{}).NumField(); got != want {
			t.Errorf("Request has %d fields and llm.CompletionRequest has %d carried", got, want)
		}
	})
}

func TestEveryFieldOfTheRequestReachesTheInternalOne(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeOf(Request{})
	for _, name := range fieldNames(typ) {
		t.Run(name, func(t *testing.T) {
			set := onlyField(t, typ, name).Interface().(Request)
			converted := requestToInternal(set)
			if reflect.DeepEqual(converted, llm.CompletionRequest{}) {
				t.Fatalf("Request.%s is not carried by requestToInternal", name)
			}
			back, err := requestFromInternal(converted)
			if err != nil || !reflect.DeepEqual(back, set) {
				t.Errorf("Request.%s does not come back: got %+v, %v", name, back, err)
			}
		})
	}
}

func TestEveryFieldOfTheInternalResponseIsCarriedOrListed(t *testing.T) {
	t.Parallel()
	internalType := reflect.TypeOf(llm.CompletionResponse{})
	for _, name := range fieldNames(internalType) {
		t.Run(name, func(t *testing.T) {
			set := onlyField(t, internalType, name).Interface().(llm.CompletionResponse)
			// The two duplicates of the token count are not carried, on purpose.
			set.Usage.InputTokens, set.Usage.OutputTokens = 0, 0
			converted, err := responseFromInternal(set)
			if responseNotCarried[name] {
				if err == nil {
					t.Fatalf("llm.CompletionResponse.%s is on the list of fields not carried, and a response that sets it was passed on", name)
				}
				var e *Error
				if !errors.As(err, &e) || e.Kind != ErrUnsupported {
					t.Errorf("got %v, want an ErrUnsupported error", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("responseFromInternal: %v", err)
			}
			if reflect.DeepEqual(converted, Response{}) {
				t.Fatalf("llm.CompletionResponse.%s is neither carried by responseFromInternal nor on the list of fields not carried: add it to Response and to both conversions, or to the list with the reason", name)
			}
			if back := responseToInternal(converted); !reflect.DeepEqual(back, set) {
				t.Errorf("llm.CompletionResponse.%s does not come back through Response:\n got %+v\nwant %+v", name, back, set)
			}
		})
	}
}

func TestEveryFieldOfTheResponseReachesTheInternalOne(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeOf(Response{})
	for _, name := range fieldNames(typ) {
		t.Run(name, func(t *testing.T) {
			set := onlyField(t, typ, name).Interface().(Response)
			converted := responseToInternal(set)
			if reflect.DeepEqual(converted, llm.CompletionResponse{}) {
				t.Fatalf("Response.%s is not carried by responseToInternal", name)
			}
			back, err := responseFromInternal(converted)
			if err != nil || !reflect.DeepEqual(back, set) {
				t.Errorf("Response.%s does not come back: got %+v, %v", name, back, err)
			}
		})
	}
}

func TestAFullRequestAndResponseRoundTrip(t *testing.T) {
	t.Parallel()
	t.Run("a request with every field set", func(t *testing.T) {
		var req Request
		counter := 0
		fillValue(t, reflect.ValueOf(&req).Elem(), "r", &counter)
		// A request that decides and also carries tools is refused by the
		// adapter (TestADecisionRequestThatCarriesToolsIsRefused), so the one
		// that sets every other field has no decision.
		req.Decision = nil
		back, err := requestFromInternal(requestToInternal(req))
		if err != nil || !reflect.DeepEqual(back, req) {
			t.Errorf("got %+v, %v", back, err)
		}
		internal := requestToInternal(req)
		if internal.SystemPrompt != req.System || internal.UserPrompt != req.User || internal.ResponseFormat != req.Format ||
			!reflect.DeepEqual(internal.JSONSchema, req.Schema) || internal.ReasoningEffort != string(req.Effort) ||
			internal.ReasoningEffortPreferred != req.EffortPreferred || internal.PromptCacheKey != req.CacheKey {
			t.Errorf("a field went to the wrong place: %+v", internal)
		}
	})
	t.Run("a response with every field set", func(t *testing.T) {
		var resp Response
		counter := 0
		fillValue(t, reflect.ValueOf(&resp).Elem(), "r", &counter)
		back, err := responseFromInternal(responseToInternal(resp))
		if err != nil || !reflect.DeepEqual(back, resp) {
			t.Errorf("got %+v, %v", back, err)
		}
	})
	t.Run("the zero values convert to the zero values", func(t *testing.T) {
		if got := requestToInternal(Request{}); !reflect.DeepEqual(got, llm.CompletionRequest{}) {
			t.Errorf("got %+v", got)
		}
		if got := responseToInternal(Response{}); !reflect.DeepEqual(got, llm.CompletionResponse{}) {
			t.Errorf("got %+v", got)
		}
		if got, err := requestFromInternal(llm.CompletionRequest{}); err != nil || !reflect.DeepEqual(got, Request{}) {
			t.Errorf("got %+v, %v", got, err)
		}
		if got, err := responseFromInternal(llm.CompletionResponse{}); err != nil || !reflect.DeepEqual(got, Response{}) {
			t.Errorf("got %+v, %v", got, err)
		}
	})
	t.Run("the decision fields reach the other side", func(t *testing.T) {
		req := Request{Decision: &Decision{State: `"s"`, Guidance: "g", Questions: []DecisionQuestion{{Kind: DecisionChoice, Ask: "q", Options: []string{"a", "b"}, Criteria: []string{"x", "y"}}, {Kind: DecisionScale, Ask: "r", Low: 1, High: 5}}}}
		internal := requestToInternal(req)
		if internal.Decision == nil || internal.Decision.State != `"s"` || internal.Decision.Guidance != "g" || len(internal.Decision.Questions) != 2 ||
			internal.Decision.Questions[0].Kind != llm.DecisionChoice || internal.Decision.Questions[1].High != 5 {
			t.Errorf("got %+v", internal.Decision)
		}
		resp := Response{
			Answers:    []DecisionAnswer{{Kind: DecisionYesNo, Yes: true, ModelConfidence: 0.9, ConfidenceReported: true}, {Kind: DecisionScale, Level: 4}},
			Escalation: &DecisionEscalation{Provider: "jev", Model: "j1", Reason: llm.DecisionEscalatedBelowFloor, ModelConfidence: 0.3, ConfidenceReported: true},
		}
		internalResponse := responseToInternal(resp)
		if len(internalResponse.DecisionAnswers) != 2 || !internalResponse.DecisionAnswers[0].Yes || internalResponse.DecisionAnswers[1].Level != 4 ||
			internalResponse.DecisionEscalation == nil || internalResponse.DecisionEscalation.Reason != "below the floor" {
			t.Errorf("got %+v", internalResponse)
		}
	})
	t.Run("a request is not shared with its copy's decision", func(t *testing.T) {
		req := Request{Decision: &Decision{State: "s"}}
		internal := requestToInternal(req)
		internal.Decision.State = "changed"
		if req.Decision.State != "s" {
			t.Error("the conversion shares the Decision struct")
		}
	})
}

func TestEveryFieldOfTheTokenCountIsCarriedOrListed(t *testing.T) {
	t.Parallel()
	internalType := reflect.TypeOf(types.TokenUsage{})
	for _, name := range fieldNames(internalType) {
		t.Run(name, func(t *testing.T) {
			set := onlyField(t, internalType, name).Interface().(types.TokenUsage)
			// The total is the one field a conversion may derive: give it a value of its own.
			if name != "TotalTokens" {
				set.TotalTokens = 99
			}
			got := usageFromInternal(set)
			if got == (Usage{Total: 99}) || got == (Usage{}) {
				t.Fatalf("types.TokenUsage.%s is not carried by usageFromInternal", name)
			}
			want := set
			if read, ok := usageRead[name]; ok {
				// Read in place of the field it repeats, so it comes back as that one.
				want = types.TokenUsage{TotalTokens: 99}
				reflect.ValueOf(&want).Elem().FieldByName(read.back).Set(reflect.ValueOf(set).FieldByName(name))
			}
			if back := usageToInternal(got); !reflect.DeepEqual(back, want) {
				t.Errorf("types.TokenUsage.%s does not come back: got %+v, want %+v", name, back, want)
			}
		})
	}
}

func TestUsageConversion(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   Usage
		want types.TokenUsage
	}{
		{"nothing reported", Usage{}, types.TokenUsage{}},
		{"every field", Usage{Input: 10, Output: 20, Total: 30, Cached: 4, CacheWrite: 3, Reasoning: 5}, types.TokenUsage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30, CachedTokens: 4, CacheWriteTokens: 3, ReasoningTokens: 5}},
		{"a total left at zero is the sum", Usage{Input: 10, Output: 20}, types.TokenUsage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30}},
		{"a total that was given is kept", Usage{Input: 10, Output: 20, Total: 99}, types.TokenUsage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 99}},
		{"input only", Usage{Input: 7}, types.TokenUsage{PromptTokens: 7, TotalTokens: 7}},
		{"output only", Usage{Output: 7}, types.TokenUsage{CompletionTokens: 7, TotalTokens: 7}},
		{"a cache count alone does not invent a total", Usage{Cached: 5}, types.TokenUsage{CachedTokens: 5}},
		{"reasoning alone does not invent a total", Usage{Reasoning: 5}, types.TokenUsage{ReasoningTokens: 5}},
		{"a large count", Usage{Input: 1 << 30, Output: 1 << 30}, types.TokenUsage{PromptTokens: 1 << 30, CompletionTokens: 1 << 30, TotalTokens: 1 << 31}},
		{"cache writes", Usage{CacheWrite: 9}, types.TokenUsage{CacheWriteTokens: 9}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := usageToInternal(c.in); got != c.want {
				t.Errorf("usageToInternal = %+v, want %+v", got, c.want)
			}
		})
	}
}

// A provider reports its counts as prompt and completion tokens or as input and
// output tokens (a codex-style provider reports only the second), and the Usage
// holds whichever the provider reported.
func TestUsageFromInternalReadsEitherSpellingOfTheCounts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   types.TokenUsage
		want Usage
	}{
		{"all four zero", types.TokenUsage{}, Usage{}},
		{"prompt and completion", types.TokenUsage{PromptTokens: 3, CompletionTokens: 4, TotalTokens: 7}, Usage{Input: 3, Output: 4, Total: 7}},
		{"input and output", types.TokenUsage{InputTokens: 5, OutputTokens: 6, TotalTokens: 11}, Usage{Input: 5, Output: 6, Total: 11}},
		{"input and output with no total", types.TokenUsage{InputTokens: 5, OutputTokens: 6}, Usage{Input: 5, Output: 6}},
		{"both spellings that agree", types.TokenUsage{PromptTokens: 5, CompletionTokens: 6, InputTokens: 5, OutputTokens: 6}, Usage{Input: 5, Output: 6}},
		{"prompt and completion win when both are given and differ", types.TokenUsage{PromptTokens: 1, CompletionTokens: 2, InputTokens: 50, OutputTokens: 60}, Usage{Input: 1, Output: 2}},
		{"input alone", types.TokenUsage{InputTokens: 5}, Usage{Input: 5}},
		{"output alone", types.TokenUsage{OutputTokens: 6}, Usage{Output: 6}},
		{"prompt with an output", types.TokenUsage{PromptTokens: 3, OutputTokens: 6}, Usage{Input: 3, Output: 6}},
		{"an input with a completion", types.TokenUsage{InputTokens: 5, CompletionTokens: 4}, Usage{Input: 5, Output: 4}},
		{"each count is read from its own pair", types.TokenUsage{PromptTokens: 3, OutputTokens: 6, InputTokens: 50, CompletionTokens: 0}, Usage{Input: 3, Output: 6}},
		{"the rest is carried beside input and output", types.TokenUsage{InputTokens: 5, OutputTokens: 6, CachedTokens: 2, ReasoningTokens: 1, CacheWriteTokens: 3}, Usage{Input: 5, Output: 6, Cached: 2, Reasoning: 1, CacheWrite: 3}},
		{"a count of zero in both spellings is zero", types.TokenUsage{CachedTokens: 2}, Usage{Cached: 2}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := usageFromInternal(c.in); got != c.want {
				t.Errorf("usageFromInternal(%+v) = %+v, want %+v", c.in, got, c.want)
			}
		})
	}
}

func TestAToolChoiceIsRefusedNotDropped(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		req  llm.CompletionRequest
	}{
		{"a tool choice", llm.CompletionRequest{ToolChoice: "required"}},
		{"a tool choice beside tools", llm.CompletionRequest{ToolChoice: "auto", Tools: []llm.Tool{{Name: "t"}}}},
		{"a tool choice beside messages", llm.CompletionRequest{ToolChoice: "none", Messages: []llm.Message{{Role: "user", Content: "hi"}}}},
		{"a tool choice that names a tool", llm.CompletionRequest{ToolChoice: "t", Tools: []llm.Tool{{Name: "t"}}, UserPrompt: "u"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			called := false
			p := &scriptedProvider{name: "x", complete: func(context.Context, Request) (Response, error) { called = true; return Response{}, nil }}
			_, err := toInternalProvider(p).Complete(context.Background(), c.req)
			if err == nil || called {
				t.Fatalf("the provider was called (%v) or no error was returned (%v)", called, err)
			}
			var e *Error
			if !errors.As(convertError("op", err), &e) || e.Kind != ErrUnsupported {
				t.Errorf("got %v", err)
			}
		})
	}
}

func TestToolsAndMessagesReachAProviderAndItsToolCallsComeBack(t *testing.T) {
	t.Parallel()
	args := json.RawMessage(`{"city":"Oslo"}`)
	var seen Request
	p := &scriptedProvider{name: "custom", complete: func(_ context.Context, req Request) (Response, error) {
		seen = req
		return Response{
			FinishReason: "tool_calls",
			ToolCalls:    []ToolCall{{ID: "call_1", Name: "weather", Arguments: args}, {ID: "call_2", Name: "clock", Arguments: json.RawMessage(`{}`)}},
		}, nil
	}}
	internal := llm.CompletionRequest{
		Model:        "m",
		SystemPrompt: "instructions",
		Tools: []llm.Tool{
			{Name: "weather", Description: "the weather", Parameters: map[string]any{"type": "object"}},
			{Name: "clock"},
		},
		Messages: []llm.Message{
			{Role: "user", Content: "q"},
			{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "earlier", Name: "clock", Arguments: json.RawMessage(`{}`)}}},
			{Role: "tool", ToolCallID: "earlier", Content: "noon"},
		},
	}
	resp, err := toInternalProvider(p).Complete(context.Background(), internal)
	if err != nil {
		t.Fatal(err)
	}
	if seen.System != "instructions" || seen.User != "" || len(seen.Tools) != 2 || seen.Tools[0].Name != "weather" || seen.Tools[0].Description != "the weather" ||
		!reflect.DeepEqual(seen.Tools[0].Parameters, map[string]any{"type": "object"}) || seen.Tools[1].Name != "clock" {
		t.Errorf("the provider saw the tools %+v and the system %q", seen.Tools, seen.System)
	}
	if len(seen.Messages) != 3 || seen.Messages[1].ToolCalls[0].ID != "earlier" || string(seen.Messages[1].ToolCalls[0].Arguments) != `{}` ||
		seen.Messages[2].Role != "tool" || seen.Messages[2].ToolCallID != "earlier" || seen.Messages[2].Content != "noon" {
		t.Errorf("the provider saw the messages %+v", seen.Messages)
	}
	if len(resp.ToolCalls) != 2 || resp.ToolCalls[0].ID != "call_1" || resp.ToolCalls[0].Name != "weather" || string(resp.ToolCalls[0].Arguments) != `{"city":"Oslo"}` ||
		resp.ToolCalls[1].ID != "call_2" || resp.Content != "" || resp.FinishReason != "tool_calls" {
		t.Errorf("the answer came back as %+v", resp)
	}
}

func TestAnInternalProviderAnswersToolCallsThroughTheRootsAdapter(t *testing.T) {
	t.Parallel()
	var seen llm.CompletionRequest
	inner := &scriptedInternalProvider{name: "x", answer: func(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
		seen = req
		return llm.CompletionResponse{ToolCalls: []llm.ToolCall{{ID: "1", Name: "t", Arguments: json.RawMessage(`{"a":1}`)}}}, nil
	}}
	resp, err := fromInternalProvider(inner).(Provider).Complete(context.Background(), Request{
		Tools:    []Tool{{Name: "t", Parameters: map[string]any{"type": "object"}}},
		Messages: []Message{{Role: "user", Content: "q"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen.Tools) != 1 || seen.Tools[0].Name != "t" || len(seen.Messages) != 1 || seen.Messages[0].Content != "q" {
		t.Errorf("the internal provider saw %+v", seen)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "1" || resp.ToolCalls[0].Name != "t" || string(resp.ToolCalls[0].Arguments) != `{"a":1}` || resp.Content != "" {
		t.Errorf("the answer came back as %+v", resp)
	}
}

// A request that decides cannot carry tools or messages: a decision provider
// would answer the decision and drop them, which is a different question from
// the one asked.
func TestADecisionRequestThatCarriesToolsIsRefused(t *testing.T) {
	t.Parallel()
	decision := &llm.Decision{State: "s", Questions: []llm.DecisionQuestion{{Kind: llm.DecisionYesNo, Ask: "q"}}}
	cases := []struct {
		name string
		req  llm.CompletionRequest
	}{
		{"tools", llm.CompletionRequest{Decision: decision, Tools: []llm.Tool{{Name: "t"}}}},
		{"messages", llm.CompletionRequest{Decision: decision, Messages: []llm.Message{{Role: "user", Content: "q"}}}},
		{"both", llm.CompletionRequest{Decision: decision, Tools: []llm.Tool{{Name: "t"}}, Messages: []llm.Message{{Role: "user", Content: "q"}}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			called := false
			p := &scriptedProvider{name: "x", complete: func(context.Context, Request) (Response, error) { called = true; return Response{}, nil }}
			_, err := toInternalProvider(p).Complete(context.Background(), c.req)
			var e *Error
			if err == nil || called || !errors.As(convertError("op", err), &e) || e.Kind != ErrInvalidRequest {
				t.Errorf("called %v, err %v", called, err)
			}
			if _, err := requestFromInternal(c.req); err == nil {
				t.Error("requestFromInternal passed the request on")
			}
		})
	}
	t.Run("through the root's adapter of an internal provider", func(t *testing.T) {
		called := false
		inner := &scriptedInternalProvider{name: "x", answer: func(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
			called = true
			return llm.CompletionResponse{}, nil
		}}
		_, err := fromInternalProvider(inner).(Provider).Complete(context.Background(), Request{
			Decision: &Decision{State: "s", Questions: []DecisionQuestion{{Kind: DecisionYesNo, Ask: "q"}}},
			Tools:    []Tool{{Name: "t"}},
		})
		var e *Error
		if err == nil || called || !errors.As(err, &e) || e.Kind != ErrInvalidRequest {
			t.Errorf("called %v, err %v", called, err)
		}
	})
	t.Run("a decision alone is passed on, and so are tools alone", func(t *testing.T) {
		if _, err := requestFromInternal(llm.CompletionRequest{Decision: decision}); err != nil {
			t.Errorf("a decision alone: %v", err)
		}
		if _, err := requestFromInternal(llm.CompletionRequest{Tools: []llm.Tool{{Name: "t"}}, Messages: []llm.Message{{Role: "user"}}}); err != nil {
			t.Errorf("tools and messages: %v", err)
		}
	})
	t.Run("the message does not name what was asked", func(t *testing.T) {
		_, err := requestFromInternal(llm.CompletionRequest{Decision: decision, Tools: []llm.Tool{{Name: "secret-tool-name"}}})
		if err == nil || strings.Contains(err.Error(), "secret-tool-name") {
			t.Errorf("got %v", err)
		}
	})
}

// randomText, randomCalls, randomRequest and randomResponse draw content of every
// kind from rng: they are the inputs of the round-trip property, which says that
// whatever the content, a conversion loses nothing.
func randomText(rng *rand.Rand) string {
	words := []string{"", "a", "weather", "Oslo", "λ", "☃", "line\nbreak", `"quoted"`, "{}", "secret-canary", strings.Repeat("x", 300)}
	return words[rng.IntN(len(words))] + words[rng.IntN(len(words))]
}

func randomCalls(rng *rand.Rand) []ToolCall {
	switch rng.IntN(4) {
	case 0:
		return nil
	case 1:
		return []ToolCall{}
	}
	calls := make([]ToolCall, 1+rng.IntN(3))
	for i := range calls {
		calls[i] = ToolCall{ID: randomText(rng), Name: randomText(rng)}
		switch rng.IntN(3) {
		case 0:
			calls[i].Arguments = json.RawMessage(`{}`)
		case 1:
			calls[i].Arguments = json.RawMessage(`{"k":"v","n":` + string(rune('0'+rng.IntN(10))) + `}`)
		}
	}
	return calls
}

func randomRequest(rng *rand.Rand) Request {
	req := Request{
		Model: randomText(rng), System: randomText(rng), User: randomText(rng), Temperature: rng.Float64(), MaxTokens: rng.IntN(5000),
		Format: []string{"", "json", "text"}[rng.IntN(3)], SchemaName: randomText(rng), CacheKey: randomText(rng),
		Effort: Effort([]string{"", "low", "high"}[rng.IntN(3)]), EffortPreferred: rng.IntN(2) == 0, WebSearch: rng.IntN(2) == 0,
	}
	if rng.IntN(2) == 0 {
		req.Schema = map[string]any{"type": "object", "n": float64(rng.IntN(9))}
	}
	switch rng.IntN(4) {
	case 0:
	case 1:
		req.Tools = []Tool{}
	default:
		req.Tools = make([]Tool, 1+rng.IntN(3))
		for i := range req.Tools {
			req.Tools[i] = Tool{Name: randomText(rng), Description: randomText(rng)}
			if rng.IntN(2) == 0 {
				req.Tools[i].Parameters = map[string]any{"type": "object", "required": []any{randomText(rng)}}
			}
		}
	}
	switch rng.IntN(4) {
	case 0:
	case 1:
		req.Messages = []Message{}
	default:
		req.Messages = make([]Message, 1+rng.IntN(5))
		for i := range req.Messages {
			req.Messages[i] = Message{
				Role: []string{"user", "assistant", "tool", ""}[rng.IntN(4)], Content: randomText(rng),
				ToolCalls: randomCalls(rng), ToolCallID: randomText(rng),
			}
		}
	}
	if len(req.Tools) == 0 && len(req.Messages) == 0 && rng.IntN(2) == 0 {
		req.Decision = &Decision{State: randomText(rng), Questions: []DecisionQuestion{{Kind: DecisionYesNo, Ask: randomText(rng)}}}
	}
	return req
}

func randomResponse(rng *rand.Rand) Response {
	resp := Response{
		Content: randomText(rng), Usage: Usage{Input: rng.IntN(900), Output: rng.IntN(900), Cached: rng.IntN(9)},
		Model: randomText(rng), Provider: randomText(rng), Upstream: randomText(rng), FinishReason: randomText(rng),
		ServiceTier: randomText(rng), Cached: rng.IntN(2) == 0, Hedged: rng.IntN(2) == 0, ToolCalls: randomCalls(rng),
	}
	resp.Usage.Total = resp.Usage.Input + resp.Usage.Output
	return resp
}

func TestRequestsAndResponsesWithToolsRoundTripWithoutLoss(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(20261003, 1))
	for i := range 500 {
		req := randomRequest(rng)
		internal := requestToInternal(req)
		back, err := requestFromInternal(internal)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if !reflect.DeepEqual(back, req) {
			t.Fatalf("request %d did not come back:\n got %+v\nwant %+v", i, back, req)
		}
		// And from the internal side: what a root request became is what the
		// internal request was.
		if again := requestToInternal(back); !reflect.DeepEqual(again, internal) {
			t.Fatalf("request %d: the internal request changed on the way back:\n got %+v\nwant %+v", i, again, internal)
		}

		resp := randomResponse(rng)
		internalResponse := responseToInternal(resp)
		backResponse, err := responseFromInternal(internalResponse)
		if err != nil {
			t.Fatalf("response %d: %v", i, err)
		}
		if !reflect.DeepEqual(backResponse, resp) {
			t.Fatalf("response %d did not come back:\n got %+v\nwant %+v", i, backResponse, resp)
		}
		if len(resp.ToolCalls) != len(internalResponse.ToolCalls) {
			t.Fatalf("response %d: %d calls went in and %d came out", i, len(resp.ToolCalls), len(internalResponse.ToolCalls))
		}
		// A stream's last chunk carries the same answer.
		chunk, err := chunkFromInternal(chunkToInternal(Chunk{Done: true, Response: resp}))
		if err != nil || !reflect.DeepEqual(chunk.Response, resp) {
			t.Fatalf("chunk %d: %+v, %v", i, chunk, err)
		}
	}
}

func TestTheConvertedListsAreNotTheCallersOwn(t *testing.T) {
	t.Parallel()
	req := Request{
		Tools:    []Tool{{Name: "a"}},
		Messages: []Message{{Role: "assistant", ToolCalls: []ToolCall{{ID: "1", Name: "a", Arguments: json.RawMessage(`{}`)}}}},
	}
	internal := requestToInternal(req)
	internal.Tools[0].Name = "changed"
	internal.Messages[0].ToolCalls[0].Name = "changed"
	internal.Messages[0].Content = "changed"
	if req.Tools[0].Name != "a" || req.Messages[0].ToolCalls[0].Name != "a" || req.Messages[0].Content != "" {
		t.Errorf("an edit of the internal request reached the caller's: %+v", req)
	}
	resp := Response{ToolCalls: []ToolCall{{ID: "1", Name: "a"}}}
	internalResponse := responseToInternal(resp)
	internalResponse.ToolCalls[0].Name = "changed"
	if resp.ToolCalls[0].Name != "a" {
		t.Errorf("an edit of the internal response reached the caller's: %+v", resp)
	}
}

func TestAV2ProviderSeenAsAnInternalOne(t *testing.T) {
	t.Parallel()
	t.Run("the request reaches the provider and the answer comes back", func(t *testing.T) {
		var seen Request
		p := &scriptedProvider{name: "custom", complete: func(_ context.Context, req Request) (Response, error) {
			seen = req
			return Response{Content: "answer", Model: "m", Usage: Usage{Input: 1, Output: 2, Total: 3}, ServiceTier: "flex", Upstream: "u"}, nil
		}}
		internal := toInternalProvider(p)
		resp, err := internal.Complete(context.Background(), llm.CompletionRequest{Model: "m", SystemPrompt: "sys", UserPrompt: "usr", MaxTokens: 5, ResponseFormat: "json", ReasoningEffort: "low", PromptCacheKey: "k"})
		if err != nil {
			t.Fatal(err)
		}
		if seen.Model != "m" || seen.System != "sys" || seen.User != "usr" || seen.MaxTokens != 5 || seen.Format != "json" || seen.Effort != EffortLow || seen.CacheKey != "k" {
			t.Errorf("the provider saw %+v", seen)
		}
		if resp.Content != "answer" || resp.Model != "m" || resp.Usage.TotalTokens != 3 || resp.ServiceTier != "flex" || resp.UpstreamProvider != "u" {
			t.Errorf("got %+v", resp)
		}
	})
	t.Run("the context reaches the provider", func(t *testing.T) {
		type key struct{}
		var got any
		p := &scriptedProvider{name: "custom", complete: func(ctx context.Context, _ Request) (Response, error) { got = ctx.Value(key{}); return Response{}, nil }}
		toInternalProvider(p).Complete(context.WithValue(context.Background(), key{}, "v"), llm.CompletionRequest{})
		if got != "v" {
			t.Errorf("got %v", got)
		}
	})
	t.Run("the name is the provider's", func(t *testing.T) {
		if got := toInternalProvider(&scriptedProvider{name: "custom"}).Name(); got != "custom" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("the estimate is zero", func(t *testing.T) {
		if got := toInternalProvider(&scriptedProvider{name: "custom"}).EstimateCost(llm.CompletionRequest{UserPrompt: "x"}); got != 0 {
			t.Errorf("got %v", got)
		}
	})
	t.Run("a failed answer is an empty response beside an error", func(t *testing.T) {
		p := failingProvider(&Error{Kind: ErrUnavailable})
		resp, err := toInternalProvider(p).Complete(context.Background(), llm.CompletionRequest{})
		if err == nil || !reflect.DeepEqual(resp, llm.CompletionResponse{}) {
			t.Errorf("got %+v, %v", resp, err)
		}
	})
	t.Run("a decision and its answers go through", func(t *testing.T) {
		p := &scriptedProvider{name: "jev", complete: func(_ context.Context, req Request) (Response, error) {
			if req.Decision == nil || len(req.Decision.Questions) != 1 {
				return Response{}, errors.New("no decision")
			}
			return Response{Answers: []DecisionAnswer{{Kind: DecisionYesNo, Yes: true, ModelConfidence: 0.8, ConfidenceReported: true}}}, nil
		}}
		resp, err := toInternalProvider(p).Complete(context.Background(), llm.CompletionRequest{Decision: &llm.Decision{State: "s", Questions: []llm.DecisionQuestion{{Kind: llm.DecisionYesNo, Ask: "q"}}}})
		if err != nil || len(resp.DecisionAnswers) != 1 || !resp.DecisionAnswers[0].Yes || resp.DecisionAnswers[0].ModelConfidence != 0.8 {
			t.Errorf("got %+v, %v", resp, err)
		}
	})
}

func TestRetryPolicyIsInTheCallPathsConvention(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		provider    Provider
		wantRetries int
		wantBackoff time.Duration
	}{
		{"a provider without settings is not configured", &scriptedProvider{name: "x"}, -1, 0},
		{"a retrier answering zero means the library's default", &retryingProvider{retries: 0, backoff: time.Second}, -1, time.Second},
		{"a retrier answering a negative number means never", &retryingProvider{retries: -1}, 0, 0},
		{"a retrier answering a very negative number means never", &retryingProvider{retries: -9}, 0, 0},
		{"a retrier answering one", &retryingProvider{retries: 1, backoff: 2 * time.Second}, 1, 2 * time.Second},
		{"a retrier answering three", &retryingProvider{retries: 3}, 3, 0},
		{"a retrier answering ten", &retryingProvider{retries: 10, backoff: time.Millisecond}, 10, time.Millisecond},
		{"a backoff is kept whatever the retries", &retryingProvider{retries: -1, backoff: time.Minute}, 0, time.Minute},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			retries, backoff := toInternalProvider(c.provider).RetryPolicy()
			if retries != c.wantRetries || backoff != c.wantBackoff {
				t.Errorf("RetryPolicy() = %d, %v, want %d, %v", retries, backoff, c.wantRetries, c.wantBackoff)
			}
		})
	}
	external := []struct {
		name        string
		internal    int
		wantRetries int
	}{
		{"an internal provider not configured is the default", -1, 0},
		{"an internal provider that never retries is a negative number", 0, -1},
		{"an internal provider that retries once", 1, 1},
		{"an internal provider that retries three times", 3, 3},
		{"an internal provider that retries five times", 5, 5},
		{"an internal provider with a very negative setting is the default", -7, 0},
	}
	for _, c := range external {
		t.Run(c.name, func(t *testing.T) {
			p := externalProvider{provider: &scriptedInternalProvider{retries: c.internal, backoff: time.Second}}
			retries, backoff := p.RetryPolicy()
			if retries != c.wantRetries || backoff != time.Second {
				t.Errorf("RetryPolicy() = %d, %v, want %d", retries, backoff, c.wantRetries)
			}
		})
	}
	t.Run("the two conventions are inverse", func(t *testing.T) {
		for _, retries := range []int{-5, -1, 0, 1, 2, 3, 8} {
			there, _ := toInternalProvider(&retryingProvider{retries: retries}).RetryPolicy()
			back, _ := (&externalProvider{provider: &scriptedInternalProvider{retries: there}}).RetryPolicy()
			if (retries < 0) != (back < 0) || (retries > 0 && back != retries) || (retries == 0 && back != 0) {
				t.Errorf("%d went to %d and came back as %d", retries, there, back)
			}
		}
	})
}

func TestABuiltInProviderSeenAsAProvider(t *testing.T) {
	t.Parallel()
	t.Run("the request and the answer are converted", func(t *testing.T) {
		var seen llm.CompletionRequest
		p := &scriptedInternalProvider{name: "openai", answer: func(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
			seen = req
			return llm.CompletionResponse{Content: "hi", Usage: types.TokenUsage{PromptTokens: 3, CompletionTokens: 4, TotalTokens: 7}, UpstreamProvider: "up", Cached: true}, nil
		}}
		resp, err := fromInternalProvider(p).(Provider).Complete(context.Background(), Request{Model: "m", System: "s", User: "u", Schema: map[string]any{"type": "object"}, SchemaName: "n", WebSearch: true, Temperature: 0.5})
		if err != nil {
			t.Fatal(err)
		}
		if seen.Model != "m" || seen.SystemPrompt != "s" || seen.UserPrompt != "u" || seen.SchemaName != "n" || !seen.WebSearch || seen.Temperature != 0.5 || seen.JSONSchema["type"] != "object" {
			t.Errorf("the provider saw %+v", seen)
		}
		if resp.Content != "hi" || resp.Usage != (Usage{Input: 3, Output: 4, Total: 7}) || resp.Upstream != "up" || !resp.Cached {
			t.Errorf("got %+v", resp)
		}
	})
	t.Run("the name is the provider's", func(t *testing.T) {
		if got := fromInternalProvider(&scriptedInternalProvider{name: "openai"}).(Provider).Name(); got != "openai" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("a failure is an *Error with its kind", func(t *testing.T) {
		p := &scriptedInternalProvider{name: "openai", answer: func(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
			return llm.CompletionResponse{}, &llm.APIError{Provider: "openai", StatusCode: 401, Message: canary}
		}}
		resp, err := fromInternalProvider(p).(Provider).Complete(context.Background(), Request{})
		var e *Error
		if !errors.As(err, &e) || e.Kind != ErrUnauthorized || e.Status != 401 || e.Provider != "openai" {
			t.Fatalf("got %v", err)
		}
		if strings.Contains(e.Error(), canary) || resp.Content != "" {
			t.Errorf("got %q, %+v", e.Error(), resp)
		}
	})
	t.Run("a context error keeps its cause", func(t *testing.T) {
		p := &scriptedInternalProvider{name: "openai", answer: func(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
			return llm.CompletionResponse{}, context.DeadlineExceeded
		}}
		_, err := fromInternalProvider(p).(Provider).Complete(context.Background(), Request{})
		if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrTimeout) {
			t.Errorf("got %v", err)
		}
	})
}

func TestTheAdaptersGiveBackWhatTheyWereMadeFrom(t *testing.T) {
	t.Parallel()
	t.Run("a built-in provider keeps being itself on the way through", func(t *testing.T) {
		builtIn := &scriptedInternalProvider{name: "openai"}
		as := fromInternalProvider(builtIn)
		if back := toInternalProvider(as); back != llm.Provider(builtIn) {
			t.Errorf("got %v, want the original provider", back)
		}
	})
	t.Run("a v2 provider keeps being itself on the way through", func(t *testing.T) {
		custom := &scriptedProvider{name: "custom"}
		as := toInternalProvider(custom)
		if back := fromInternalProvider(as); back != any(custom) {
			t.Errorf("got %v, want the original provider", back)
		}
	})
	t.Run("nil is nil", func(t *testing.T) {
		if toInternalProvider(nil) != nil {
			t.Error("toInternalProvider(nil) is not nil")
		}
		if fromInternalProvider(nil) != nil {
			t.Error("fromInternalProvider(nil) is not nil")
		}
	})
	t.Run("a value that is not a provider is nil", func(t *testing.T) {
		for _, v := range []any{"a string", 42, struct{}{}, &scriptedInternalProvider{}} {
			if toInternalProvider(v) != nil {
				t.Errorf("toInternalProvider(%T) is not nil", v)
			}
		}
	})
	t.Run("two conversions of one provider are two adapters", func(t *testing.T) {
		custom := &scriptedProvider{name: "custom"}
		if toInternalProvider(custom) == toInternalProvider(custom) {
			t.Log("the adapter is shared")
		}
		if toInternalProvider(custom).Name() != "custom" {
			t.Error("the name was lost")
		}
	})
}

func TestAnErrorFromAV2ProviderIsHonouredForItsKind(t *testing.T) {
	t.Parallel()
	for _, kind := range allKinds {
		t.Run("the kind "+kind.Error()+" survives the call path", func(t *testing.T) {
			original := &Error{Kind: kind, Message: "the provider's account"}
			err := providerFailure(original, "custom", "m1")
			internalKind, narrow := kindToInternal(kind)
			if got := types.KindOf(err); got != internalKind {
				t.Errorf("the call path reads the kind %v, want %v", got, internalKind)
			}
			if got := types.NarrowingOf(err); got != narrow {
				t.Errorf("the call path reads the mark %v, want %v", got, narrow)
			}
			var e *Error
			if !errors.As(convertError("classify", err), &e) || e.Kind != kind {
				t.Fatalf("the caller reads %v, want %v", e, kind)
			}
			if e.Op != "classify" || e.Provider != "custom" || e.Model != "m1" || e.Message != "the provider's account" {
				t.Errorf("got %+v", e)
			}
			if !errors.Is(convertError("classify", err), original) {
				t.Errorf("the provider's own error is no longer in the chain")
			}
		})
	}
	t.Run("a rate limit with a wait is retried after the wait", func(t *testing.T) {
		original := &Error{Kind: ErrRateLimited, RetryAfter: 12 * time.Second, Status: 429}
		_, err := toInternalProvider(failingProvider(original)).Complete(context.Background(), llm.CompletionRequest{Model: "m1"})
		if wait, ok := llm.RetryAfterFrom(err); !ok || wait != 12*time.Second {
			t.Errorf("the retry loop reads %v, %v", wait, ok)
		}
		if !(&types.OperationError{Kind: types.KindOf(err)}).Retryable() {
			t.Errorf("the call path does not retry it")
		}
		var e *Error
		if !errors.As(convertError("classify", err), &e) || e.Kind != ErrRateLimited || e.RetryAfter != 12*time.Second || e.Status != 429 || e.Op != "classify" {
			t.Errorf("the caller reads %+v", e)
		}
		if !errors.Is(convertError("classify", err), original) {
			t.Errorf("the provider's own error is not the one the caller gets")
		}
	})
	t.Run("a rate limit with no wait adds none and invents no status", func(t *testing.T) {
		err := providerFailure(&Error{Kind: ErrRateLimited}, "custom", "m")
		if _, ok := llm.RetryAfterFrom(err); ok {
			t.Error("a wait was made up")
		}
		var e *Error
		errors.As(convertError("x", err), &e)
		if e.Status != 0 || e.RetryAfter != 0 {
			t.Errorf("got %+v", e)
		}
	})
	t.Run("the wait is not added twice when the chain already has one", func(t *testing.T) {
		inner := &llm.RateLimitError{APIError: &llm.APIError{StatusCode: 429}, RetryAfter: time.Second}
		err := providerFailure(&Error{Kind: ErrRateLimited, RetryAfter: time.Second, Err: inner}, "p", "m")
		var count int
		walkChain(err, func(e error) bool {
			if _, ok := e.(*llm.RateLimitError); ok {
				count++
			}
			return true
		})
		if count != 1 {
			t.Errorf("%d rate-limit errors in the chain", count)
		}
	})
	t.Run("a kind anywhere in the chain is honoured", func(t *testing.T) {
		shapes := map[string]error{
			"a bare kind":           ErrRateLimited,
			"a kind under fmt":      fmt.Errorf("sending: %w", ErrRateLimited),
			"an *Error under fmt":   fmt.Errorf("sending: %w", &Error{Kind: ErrRateLimited}),
			"a kind in a join":      errors.Join(errors.New("x"), ErrRateLimited),
			"a kind two levels in":  fmt.Errorf("a: %w", fmt.Errorf("b: %w", ErrRateLimited)),
			"an *Error in a join":   errors.Join(&Error{Kind: ErrRateLimited}, errors.New("x")),
			"the outermost decides": fmt.Errorf("x: %w", &Error{Kind: ErrRateLimited, Err: ErrUnauthorized}),
		}
		for name, in := range shapes {
			err := providerFailure(in, "p", "m")
			if got := types.KindOf(err); got != types.KindRateLimited {
				t.Errorf("%s: the call path reads %v", name, got)
			}
		}
	})
	t.Run("a bare error is the provider being unavailable", func(t *testing.T) {
		cases := []error{errors.New("boom"), fmt.Errorf("sending: %w", errors.New("boom")), errors.New(""), errors.New("connection reset"), errors.Join(errors.New("a"), errors.New("b"))}
		for _, in := range cases {
			err := providerFailure(in, "p", "m")
			if got := types.KindOf(err); got != types.KindProviderUnavailable {
				t.Errorf("%v: the call path reads %v", in, got)
			}
			var e *Error
			errors.As(convertError("x", err), &e)
			if e.Kind != ErrUnavailable || !e.Retryable() {
				t.Errorf("%v: the caller reads %+v", in, e)
			}
		}
	})
	t.Run("what the internal classifier knows is not overruled", func(t *testing.T) {
		cases := []struct {
			in   error
			want types.ErrorKind
		}{
			{context.Canceled, types.KindCanceled},
			{context.DeadlineExceeded, types.KindTimeout},
			{&llm.APIError{StatusCode: 401}, types.KindAuthentication},
			{&llm.APIError{StatusCode: 429}, types.KindRateLimited},
			{&net.DNSError{IsTimeout: true}, types.KindTimeout},
		}
		for _, c := range cases {
			err := providerFailure(c.in, "p", "m")
			if got := types.KindOf(err); got != c.want {
				t.Errorf("%T: the call path reads %v, want %v", c.in, got, c.want)
			}
		}
	})
	t.Run("an error of the call path's own is returned as it is", func(t *testing.T) {
		own := []error{
			types.Newf(types.KindBudgetExceeded, "budget", "spent"),
			types.Classified(types.KindConfiguration, errors.New("x")),
			fmt.Errorf("w: %w", types.Newf(types.KindTimeout, "x", "y")),
		}
		for _, in := range own {
			if got := providerFailure(in, "p", "m"); got != in {
				t.Errorf("the error was wrapped: %v", got)
			}
		}
	})
	t.Run("nil is nil", func(t *testing.T) {
		if providerFailure(nil, "p", "m") != nil {
			t.Error("not nil")
		}
	})
	t.Run("the route is the provider's name and the request's model when the error gave none", func(t *testing.T) {
		err := providerFailure(&Error{Kind: ErrUnauthorized}, "custom", "m9")
		var op *types.OperationError
		if !errors.As(err, &op) || op.Provider != "custom" || op.Model != "m9" {
			t.Errorf("got %+v", op)
		}
		err = providerFailure(&Error{Kind: ErrUnauthorized, Provider: "theirs", Model: "m1"}, "custom", "m9")
		errors.As(err, &op)
		if op.Provider != "theirs" || op.Model != "m1" {
			t.Errorf("got %+v", op)
		}
	})
	t.Run("the marker in a provider's own message stays out of the error text", func(t *testing.T) {
		err := providerFailure(&Error{Kind: ErrUnavailable, Err: errors.New(canary)}, "p", "m")
		var e *Error
		errors.As(convertError("x", err), &e)
		if strings.Contains(e.Error(), canary) {
			t.Errorf("got %q", e.Error())
		}
	})
}

func TestProviderErrorRoundTrip(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		err    error
		kind   Kind
		after  time.Duration
		status int
	}{
		{"a rate limit with a wait", &Error{Kind: ErrRateLimited, RetryAfter: 5 * time.Second}, ErrRateLimited, 5 * time.Second, 0},
		{"a bare rate limit kind", ErrRateLimited, ErrRateLimited, 0, 0},
		{"a wrapped rate limit kind", fmt.Errorf("provider: %w", ErrRateLimited), ErrRateLimited, 0, 0},
		{"an unauthorized provider", &Error{Kind: ErrUnauthorized, Status: 401}, ErrUnauthorized, 0, 401},
		{"an unavailable provider", &Error{Kind: ErrUnavailable, Status: 503, RetryAfter: time.Second}, ErrUnavailable, time.Second, 503},
		{"a bare error", errors.New("down"), ErrUnavailable, 0, 0},
		{"a refusal", &Error{Kind: ErrRefused}, ErrRefused, 0, 0},
		{"a panic", &Error{Kind: ErrInternal}, ErrInternal, 0, 0},
		{"a canceled context", context.Canceled, ErrCanceled, 0, 0},
		{"a provider that is unsupported", &Error{Kind: ErrUnsupported}, ErrUnsupported, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := toInternalProvider(failingProvider(c.err)).Complete(context.Background(), llm.CompletionRequest{Model: "m"})
			got := convertError("classify", err)
			var e *Error
			if !errors.As(got, &e) {
				t.Fatalf("got %T", got)
			}
			if e.Kind != c.kind || e.RetryAfter != c.after || e.Status != c.status || e.Op != "classify" {
				t.Errorf("got %+v, want kind %v after %v status %d", e, c.kind, c.after, c.status)
			}
			if !errors.Is(got, c.err) {
				t.Errorf("the provider's error is not in the chain of %v", got)
			}
		})
	}
}
