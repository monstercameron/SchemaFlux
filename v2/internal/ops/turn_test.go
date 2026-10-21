package ops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// The call path for a conversation. A turn goes to the model through the same
// request function an operation does, so its policy, budget, scheduler,
// deadline, retries, observers and call records are the operation's own. What a
// turn adds is the request it builds, and the one change to the path itself: a
// reply that is only tool calls is a complete answer.

// chatScript is a provider that answers each request from a script and keeps what
// it was sent. A step is a response or an error; the last step repeats.
type chatScript struct {
	mu       sync.Mutex
	name     string
	retries  int
	steps    []chatStep
	requests []llm.CompletionRequest
	contexts []context.Context
}

type chatStep struct {
	resp llm.CompletionResponse
	err  error
}

func (p *chatScript) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.mu.Lock()
	n := len(p.requests)
	p.requests = append(p.requests, req)
	p.contexts = append(p.contexts, ctx)
	step := p.steps[min(n, len(p.steps)-1)]
	p.mu.Unlock()
	if step.err != nil {
		return llm.CompletionResponse{}, step.err
	}
	resp := step.resp
	if resp.Model == "" {
		resp.Model = req.Model
	}
	if resp.Provider == "" {
		resp.Provider = p.Name()
	}
	return resp, nil
}

func (p *chatScript) Name() string {
	if p.name == "" {
		return "local"
	}
	return p.name
}
func (p *chatScript) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *chatScript) RetryPolicy() (int, time.Duration)          { return p.retries, time.Microsecond }

// script replaces what the provider answers and forgets what it was sent.
func (p *chatScript) script(steps ...chatStep) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.steps = steps
	p.requests = nil
	p.contexts = nil
}

func (p *chatScript) sent() []llm.CompletionRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]llm.CompletionRequest(nil), p.requests...)
}

func (p *chatScript) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

// toolCallsOnly is a reply that asks for tools and says nothing.
func toolCallsOnly(calls ...llm.ToolCall) llm.CompletionResponse {
	return llm.CompletionResponse{ToolCalls: calls, FinishReason: "tool_calls"}
}

func weatherCall(id string) llm.ToolCall {
	return llm.ToolCall{ID: id, Name: "get_weather", Arguments: json.RawMessage(`{"city":"Oslo"}`)}
}

// callOptions are the options of a call to a fixed model, so that no test of the
// call path depends on the environment's model.
func callOptions() types.OpOptions {
	return types.OpOptions{Model: "chat-model", Intelligence: types.Smart, Mode: types.TransformMode}
}

func TestValidateLLMCompletionAcceptsAnAnswerOfTextOrOfToolCalls(t *testing.T) {
	calls := []llm.ToolCall{weatherCall("call_1")}
	cases := []struct {
		name    string
		resp    llm.CompletionResponse
		refused bool
	}{
		{"text", llm.CompletionResponse{Content: "hello"}, false},
		{"text and calls", llm.CompletionResponse{Content: "let me look", ToolCalls: calls}, false},
		{"calls and no text", llm.CompletionResponse{ToolCalls: calls}, false},
		{"calls and only white space", llm.CompletionResponse{Content: " \n\t", ToolCalls: calls}, false},
		{"two calls and no text", llm.CompletionResponse{ToolCalls: []llm.ToolCall{weatherCall("a"), weatherCall("b")}}, false},
		{"calls with a finish reason of tool_calls", llm.CompletionResponse{ToolCalls: calls, FinishReason: "tool_calls"}, false},
		{"nothing at all", llm.CompletionResponse{}, true},
		{"only white space", llm.CompletionResponse{Content: "  \n"}, true},
		{"an empty list of calls is no calls", llm.CompletionResponse{ToolCalls: []llm.ToolCall{}}, true},
		{"a nil list of calls and only a tab", llm.CompletionResponse{Content: "\t"}, true},
		{"only usage", llm.CompletionResponse{Usage: types.TokenUsage{TotalTokens: 5}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateLLMCompletion(tc.resp)
			if (err != nil) != tc.refused {
				t.Fatalf("validateLLMCompletion = %v, refused should be %v", err, tc.refused)
			}
			if err != nil && !errors.Is(err, errEmptyCompletion) {
				t.Errorf("the refusal is %v, want errEmptyCompletion, which is what the retry reads", err)
			}
		})
	}
}

// An operation declares no tools, so a reply to one that is only tool calls is
// not an answer to it. It used to be refused as empty and sent again, up to
// 1+Retries times, and paid for each time. It is now refused once, as what it is.
func TestCallLLMRefusesAReplyOfOnlyToolCallsWithoutSendingItAgain(t *testing.T) {
	provider := &chatScript{retries: 3, steps: []chatStep{{resp: toolCallsOnly(weatherCall("call_1"))}}}

	answer, err := CallLLM(context.Background(), provider, "system", "user", callOptions())
	if err == nil {
		t.Fatalf("CallLLM answered %q for a request that declared no tools and was answered with a tool call", answer)
	}
	if answer != "" {
		t.Errorf("answer = %q beside an error", answer)
	}
	if got := provider.count(); got != 1 {
		t.Errorf("the provider was sent %d requests, want 1: the reply is not worth asking for again", got)
	}
	if kind := types.KindOf(err); kind != types.KindMalformedOutput {
		t.Errorf("kind = %v, want a malformed output", kind)
	}
	if isRetryableLLMError(err) {
		t.Error("the refusal is read as retryable")
	}
	if strings.Contains(err.Error(), "call_1") || strings.Contains(err.Error(), "get_weather") || strings.Contains(err.Error(), "Oslo") {
		t.Errorf("the error carries the model's call: %q", err.Error())
	}
}

func TestCallLLMStillAsksAgainForAReplyWithNeitherTextNorCalls(t *testing.T) {
	provider := &chatScript{retries: 2, steps: []chatStep{
		{resp: llm.CompletionResponse{Content: " "}},
		{resp: llm.CompletionResponse{}},
		{resp: llm.CompletionResponse{Content: "finally", FinishReason: "stop"}},
	}}
	answer, err := CallLLM(context.Background(), provider, "system", "user", callOptions())
	if err != nil || answer != "finally" {
		t.Fatalf("CallLLM = %q, %v", answer, err)
	}
	if got := provider.count(); got != 3 {
		t.Errorf("%d requests, want 3", got)
	}

	exhausted := &chatScript{retries: 1, steps: []chatStep{{resp: llm.CompletionResponse{}}}}
	_, err = CallLLM(context.Background(), exhausted, "system", "user", callOptions())
	if !errors.Is(err, errEmptyCompletion) && types.KindOf(err) != types.KindMalformedOutput {
		t.Fatalf("err = %v, want the empty-answer refusal", err)
	}
	if got := exhausted.count(); got != 2 {
		t.Errorf("%d requests, want 2: one and its retry", got)
	}
}
