package tests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/mw"
)

// A Session through the exported API of the old root: it makes the requests,
// carries the conversation, and refuses what no conversation is. The wire test
// reads the bodies a server receives, turn by turn; the others run on a provider
// that answers from a script.

var orderTool = schemaflux.Tool{
	Name:        "order_status",
	Description: "Look up where an order is.",
	Parameters: map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"order": map[string]any{"type": "string"}},
		"required":             []string{"order"},
		"additionalProperties": false,
	},
}

// installChatProvider makes provider the one the default client sends to for the
// length of the test, and puts back what was there.
func installChatProvider(t *testing.T, provider schemaflux.Provider) {
	t.Helper()
	previous := schemaflux.GetDefaultClient()
	schemaflux.SetDefaultClient(schemaflux.NewClient("test-key").WithProviderInstance(provider))
	t.Cleanup(func() {
		ops.SetDefaultProvider(nil)
		schemaflux.SetDefaultClient(previous)
	})
}

// chatServer is a fake Responses endpoint that answers each request with the next
// body of its script, and keeps every request body it was sent.
type chatServer struct {
	mu      sync.Mutex
	replies []string
	bodies  []map[string]any
	server  *httptest.Server
	// hold, when set, makes the server wait for the client to go away before it
	// answers, which is how a request in flight looks to the server.
	hold    chan struct{}
	dropped chan struct{}
}

func newChatServer(t *testing.T, replies ...string) *chatServer {
	t.Helper()
	cs := &chatServer{replies: replies, dropped: make(chan struct{}, 4)}
	cs.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("the request body is not JSON: %v", err)
		}
		cs.mu.Lock()
		n := len(cs.bodies)
		cs.bodies = append(cs.bodies, body)
		hold := cs.hold
		cs.mu.Unlock()
		if hold != nil {
			select {
			case <-r.Context().Done():
				cs.dropped <- struct{}{}
				return
			case <-hold:
			}
		}
		if n >= len(cs.replies) {
			t.Errorf("the server was sent a request it had no answer for")
			http.Error(w, "no more answers", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(cs.replies[n]))
	}))
	t.Cleanup(cs.server.Close)

	previous := schemaflux.GetDefaultClient()
	schemaflux.SetDefaultClient(schemaflux.NewClient("test-key").WithProviderConfig("openai", schemaflux.ProviderConfig{
		APIKey: "test-key", BaseURL: cs.server.URL,
	}))
	t.Cleanup(func() {
		ops.SetDefaultProvider(nil)
		schemaflux.SetDefaultClient(previous)
	})
	return cs
}

func (cs *chatServer) sent() []map[string]any {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return append([]map[string]any(nil), cs.bodies...)
}

// input is the conversation of request n as the wire carried it, written back to
// JSON so that it compares as text.
func (cs *chatServer) input(t *testing.T, n int) string {
	t.Helper()
	bodies := cs.sent()
	if n >= len(bodies) {
		t.Fatalf("the server was sent %d requests, not %d", len(bodies), n+1)
	}
	encoded, err := json.Marshal(bodies[n]["input"])
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// responsesCall is a Responses body in which the model asks for tools.
func responsesCalls(tokens int, calls ...[3]string) string {
	items := make([]string, len(calls))
	for i, c := range calls {
		arguments, _ := json.Marshal(c[2])
		items[i] = fmt.Sprintf(`{"type":"function_call","call_id":%q,"name":%q,"arguments":%s}`, c[0], c[1], arguments)
	}
	return fmt.Sprintf(`{"id":"resp","status":"completed","model":"gpt-6-luna","output":[%s],"usage":{"input_tokens":%d,"output_tokens":5,"total_tokens":%d}}`,
		strings.Join(items, ","), tokens, tokens+5)
}

// responsesText is a Responses body in which the model answers in words.
func responsesText(tokens int, text string) string {
	payload, _ := json.Marshal(text)
	return fmt.Sprintf(`{"id":"resp","status":"completed","model":"gpt-6-luna","output":[{"type":"message","content":[{"type":"output_text","text":%s}]}],"usage":{"input_tokens":%d,"output_tokens":7,"total_tokens":%d}}`,
		payload, tokens, tokens+7)
}

func TestIntegrationASessionCarriesTwoTurnsAndAToolCallOverTheResponsesWire(t *testing.T) {
	server := newChatServer(t,
		responsesCalls(40, [3]string{"call_1", "order_status", `{"order":"A17"}`}),
		responsesText(80, "Order A17 shipped on Tuesday."),
		responsesText(120, "It arrives on Friday."),
	)
	ctx := context.Background()
	s := schemaflux.NewSession()
	s.Model = "gpt-6-luna"
	s.Instructions = "You answer questions about orders."
	s.Tools = []schemaflux.Tool{orderTool}

	first, err := s.Send(ctx, "Where is order A17?")
	if err != nil {
		t.Fatalf("first Send: %v", err)
	}
	if first.Value.Content != "" || len(first.Value.ToolCalls) != 1 || first.Value.ToolCalls[0].ID != "call_1" ||
		first.Value.ToolCalls[0].Name != "order_status" || string(first.Value.ToolCalls[0].Arguments) != `{"order":"A17"}` {
		t.Fatalf("the model's first reply is %+v, want its call of order_status", first.Value)
	}

	second, err := s.SendResults(ctx, map[string]string{"call_1": `{"status":"shipped","day":"Tuesday"}`})
	if err != nil {
		t.Fatalf("SendResults: %v", err)
	}
	if second.Value.Content != "Order A17 shipped on Tuesday." {
		t.Fatalf("second reply = %q", second.Value.Content)
	}

	third, err := s.Send(ctx, "When does it arrive?")
	if err != nil {
		t.Fatalf("second Send: %v", err)
	}
	if third.Value.Content != "It arrives on Friday." {
		t.Errorf("the last reply is %q", third.Value.Content)
	}

	// What the server was sent, turn by turn.
	bodies := server.sent()
	if len(bodies) != 3 {
		t.Fatalf("the server was sent %d requests, want 3", len(bodies))
	}
	for i, body := range bodies {
		if body["instructions"] != "You answer questions about orders." {
			t.Errorf("request %d: instructions = %v", i+1, body["instructions"])
		}
		tools, _ := body["tools"].([]any)
		if len(tools) != 1 || tools[0].(map[string]any)["name"] != "order_status" || tools[0].(map[string]any)["type"] != "function" {
			t.Errorf("request %d: tools = %v", i+1, body["tools"])
		}
	}
	const (
		user1 = `{"content":"Where is order A17?","role":"user"}`
		call1 = `{"arguments":"{\"order\":\"A17\"}","call_id":"call_1","name":"order_status","type":"function_call"}`
		out1  = `{"call_id":"call_1","output":"{\"status\":\"shipped\",\"day\":\"Tuesday\"}","type":"function_call_output"}`
		asst1 = `{"content":"Order A17 shipped on Tuesday.","role":"assistant"}`
		user2 = `{"content":"When does it arrive?","role":"user"}`
	)
	want := []string{
		"[" + user1 + "]",
		"[" + user1 + "," + call1 + "," + out1 + "]",
		"[" + user1 + "," + call1 + "," + out1 + "," + asst1 + "," + user2 + "]",
	}
	for i, w := range want {
		if got := server.input(t, i); got != w {
			t.Errorf("request %d carried\n  %s\nwant\n  %s", i+1, got, w)
		}
	}

	// The record of each turn, and the total.
	if first.Meta.Usage.PromptTokens != 40 || second.Meta.Usage.PromptTokens != 80 || third.Meta.Usage.PromptTokens != 120 {
		t.Errorf("prompt tokens per turn: %d, %d, %d", first.Meta.Usage.PromptTokens, second.Meta.Usage.PromptTokens, third.Meta.Usage.PromptTokens)
	}
	if first.Meta.Operation != "chat.send" || second.Meta.Operation != "chat.sendresults" {
		t.Errorf("operations %q and %q", first.Meta.Operation, second.Meta.Operation)
	}
	total := s.Total()
	if total.Usage.PromptTokens != 240 || total.Attempts != 3 {
		t.Errorf("total = %d prompt tokens in %d attempts, want 240 in 3", total.Usage.PromptTokens, total.Attempts)
	}
	if len(s.History) != 6 {
		t.Errorf("History has %d messages, want 6", len(s.History))
	}
}

func TestIntegrationAModelThatAsksForTwoToolsAtOnceIsAnsweredForBoth(t *testing.T) {
	server := newChatServer(t,
		responsesCalls(40, [3]string{"call_a", "order_status", `{"order":"A17"}`}, [3]string{"call_b", "order_status", `{"order":"B02"}`}),
		responsesText(90, "A17 shipped and B02 is packing."),
	)
	s := schemaflux.NewSession()
	s.Model = "gpt-6-luna"
	s.Tools = []schemaflux.Tool{orderTool}

	first, err := s.Send(context.Background(), "A17 and B02?")
	if err != nil || len(first.Value.ToolCalls) != 2 {
		t.Fatalf("first = %+v, %v", first.Value, err)
	}
	// Not the same results in the same call: a result for one is refused.
	if _, err := s.SendResults(context.Background(), map[string]string{"call_a": "shipped"}); schemaflux.KindOf(err) != types.KindInvalidRequest {
		t.Fatalf("a result for one of two calls: err = %v, want an invalid request", err)
	}
	if got := len(server.sent()); got != 1 {
		t.Fatalf("the refused results were sent: %d requests", got)
	}
	last, err := s.SendResults(context.Background(), map[string]string{"call_b": "packing", "call_a": "shipped"})
	if err != nil || last.Value.Content != "A17 shipped and B02 is packing." {
		t.Fatalf("last = %+v, %v", last.Value, err)
	}
	const want = `[{"content":"A17 and B02?","role":"user"},` +
		`{"arguments":"{\"order\":\"A17\"}","call_id":"call_a","name":"order_status","type":"function_call"},` +
		`{"arguments":"{\"order\":\"B02\"}","call_id":"call_b","name":"order_status","type":"function_call"},` +
		`{"call_id":"call_a","output":"shipped","type":"function_call_output"},` +
		`{"call_id":"call_b","output":"packing","type":"function_call_output"}]`
	if got := server.input(t, 1); got != want {
		t.Errorf("the second request carried\n  %s\nwant\n  %s", got, want)
	}
}

// chatModel is a provider that plays a model for the weather and the orders: it
// asks for the tool when the last message is a user turn that mentions an order,
// answers from the result when the last message is a result, and says so
// otherwise. It keeps what it was sent.
type chatModel struct {
	mu       sync.Mutex
	name     string
	requests []schemaflux.CompletionRequest
	// call overrides what it asks for: the tool, and how many calls at once.
	tool   string
	nCalls int
}

func (p *chatModel) Complete(_ context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	p.mu.Lock()
	n := len(p.requests)
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	if len(req.Messages) == 0 {
		return schemaflux.CompletionResponse{}, errors.New("this provider answers a conversation")
	}
	usage := types.TokenUsage{PromptTokens: 10 + n, CompletionTokens: 3, TotalTokens: 13 + n}
	last := req.Messages[len(req.Messages)-1]
	switch {
	case last.Role == "user" && strings.Contains(last.Content, "order"):
		tool := p.tool
		if tool == "" {
			tool = "order_status"
		}
		count := max(p.nCalls, 1)
		calls := make([]llm.ToolCall, count)
		for i := range calls {
			calls[i] = llm.ToolCall{ID: fmt.Sprintf("call_%d_%d", n, i), Name: tool, Arguments: json.RawMessage(`{"order":"A17"}`)}
		}
		return schemaflux.CompletionResponse{ToolCalls: calls, FinishReason: "tool_calls", Usage: usage}, nil
	case last.Role == "tool":
		return schemaflux.CompletionResponse{Content: "Order A17 shipped on Tuesday.", FinishReason: "stop", Usage: usage}, nil
	}
	return schemaflux.CompletionResponse{Content: "It arrives on Friday.", FinishReason: "stop", Usage: usage}, nil
}

func (p *chatModel) Name() string {
	if p.name == "" {
		return "local"
	}
	return p.name
}
func (p *chatModel) EstimateCost(schemaflux.CompletionRequest) float64 { return 0 }
func (p *chatModel) RetryPolicy() (int, time.Duration)                 { return 0, 0 }
func (p *chatModel) sent() []schemaflux.CompletionRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]schemaflux.CompletionRequest(nil), p.requests...)
}

func roleNames(messages []schemaflux.Message) []string {
	out := make([]string, len(messages))
	for i, m := range messages {
		out[i] = m.Role
	}
	return out
}

func TestIntegrationASessionCarriesTwoTurnsAndAToolCallThroughAProvider(t *testing.T) {
	provider := &chatModel{}
	installChatProvider(t, provider)
	ctx := context.Background()
	s := &schemaflux.Session{Model: "chat-model", Instructions: "You answer questions about orders.", Tools: []schemaflux.Tool{orderTool}}

	first, err := s.Send(ctx, "Where is order A17?")
	if err != nil || len(first.Value.ToolCalls) != 1 || first.Value.ToolCalls[0].Name != "order_status" {
		t.Fatalf("first = %+v, %v", first.Value, err)
	}
	second, err := s.SendResults(ctx, map[string]string{first.Value.ToolCalls[0].ID: `{"status":"shipped","day":"Tuesday"}`})
	if err != nil || second.Value.Content != "Order A17 shipped on Tuesday." {
		t.Fatalf("second = %+v, %v", second.Value, err)
	}
	third, err := s.Send(ctx, "When does it arrive?")
	if err != nil || third.Value.Content != "It arrives on Friday." {
		t.Fatalf("third = %+v, %v", third.Value, err)
	}

	wantRoles := []string{"user", "assistant", "tool", "assistant", "user", "assistant"}
	got := roleNames(s.History)
	if fmt.Sprint(got) != fmt.Sprint(wantRoles) {
		t.Fatalf("History roles %v, want %v", got, wantRoles)
	}

	// Each request carried the conversation as it was then, and the second
	// carries the result under the id the model asked with.
	sent := provider.sent()
	if len(sent) != 3 {
		t.Fatalf("%d requests, want 3", len(sent))
	}
	for i, want := range []int{1, 3, 5} {
		if len(sent[i].Messages) != want {
			t.Errorf("request %d carried %d messages, want %d", i+1, len(sent[i].Messages), want)
		}
		if sent[i].SystemPrompt != "You answer questions about orders." || sent[i].UserPrompt != "" || len(sent[i].Tools) != 1 {
			t.Errorf("request %d: system %q, user prompt %q, %d tools", i+1, sent[i].SystemPrompt, sent[i].UserPrompt, len(sent[i].Tools))
		}
	}
	tool := sent[1].Messages[2]
	if tool.Role != "tool" || tool.ToolCallID != first.Value.ToolCalls[0].ID || sent[1].Messages[1].ToolCalls[0].ID != tool.ToolCallID {
		t.Errorf("the result is %+v beside the call %+v", tool, sent[1].Messages[1].ToolCalls)
	}

	// The history is the session's: a copy of a reply cannot rewrite it.
	first.Value.ToolCalls[0].Name = "rewritten"
	if s.History[1].ToolCalls[0].Name != "order_status" {
		t.Error("editing a reply changed the session's history")
	}
}

func TestIntegrationASessionRefusesAReplyThatAsksForAToolNobodyDeclared(t *testing.T) {
	t.Run("a provider that does not check, which the session does", func(t *testing.T) {
		provider := &chatModel{tool: "delete_everything"}
		installChatProvider(t, provider)
		s := &schemaflux.Session{Model: "chat-model", Tools: []schemaflux.Tool{orderTool}}
		_, err := s.Send(context.Background(), "Where is order A17?")
		if schemaflux.KindOf(err) != types.KindMalformedOutput {
			t.Fatalf("err = %v, want a malformed output", err)
		}
		if s.History != nil {
			t.Errorf("History = %+v after a refused reply", s.History)
		}
		if strings.Contains(err.Error(), "delete_everything") {
			t.Errorf("the error names the model's tool: %q", err.Error())
		}
	})

	t.Run("the Responses provider, which refuses it itself", func(t *testing.T) {
		newChatServer(t, responsesCalls(40, [3]string{"call_1", "delete_everything", `{}`}))
		s := &schemaflux.Session{Model: "gpt-6-luna", Tools: []schemaflux.Tool{orderTool}}
		_, err := s.Send(context.Background(), "Where is order A17?")
		if err == nil || s.History != nil {
			t.Fatalf("err = %v, History %+v: the call was accepted", err, s.History)
		}
		if strings.Contains(err.Error(), "delete_everything") && !strings.Contains(err.Error(), "offered") {
			t.Errorf("the error carries the model's tool name: %q", err.Error())
		}
	})
}

func TestIntegrationASessionRefusesAHistoryNoConversationHadBeforeSendingIt(t *testing.T) {
	provider := &chatModel{}
	installChatProvider(t, provider)
	s := &schemaflux.Session{Model: "chat-model", History: []schemaflux.Message{
		{Role: "user", Content: "hi"}, {Role: "user", Content: "hello?"},
	}}
	_, err := s.Send(context.Background(), "again")
	if !errors.Is(err, schemaflux.ErrSessionInvariant) || schemaflux.KindOf(err) != types.KindInvalidRequest {
		t.Fatalf("err = %v, want an invalid history", err)
	}
	if len(provider.sent()) != 0 || len(s.History) != 2 {
		t.Errorf("%d requests were sent and History has %d messages", len(provider.sent()), len(s.History))
	}
}

// Twenty rounds of asking for a tool: no goroutine outlives its turn, and the
// total is the sum of the twenty-one turns.
func TestIntegrationATwentyRoundToolLoopLeavesNoGoroutinesAndTotalsEveryTurn(t *testing.T) {
	loop := &loopModel{rounds: 20}
	installChatProvider(t, loop)
	s := &schemaflux.Session{Model: "chat-model", Tools: []schemaflux.Tool{orderTool}}
	goroutines := runtime.NumGoroutine()

	reply, err := s.Send(context.Background(), "Keep checking the order.")
	rounds := 0
	for err == nil && len(reply.Value.ToolCalls) > 0 {
		results := map[string]string{}
		for _, call := range reply.Value.ToolCalls {
			results[call.ID] = "still packing"
		}
		rounds++
		reply, err = s.SendResults(context.Background(), results)
	}
	if err != nil || rounds != 20 || reply.Value.Content != "Done after 20 rounds." {
		t.Fatalf("%d rounds, reply %+v, err %v", rounds, reply.Value, err)
	}
	total := s.Total()
	if total.Attempts != 21 || total.Usage.PromptTokens != 21*10 || total.Usage.CompletionTokens != 21*2 {
		t.Errorf("total = %d attempts, %+v: want 21 turns of 10 and 2 tokens", total.Attempts, total.Usage)
	}
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > goroutines && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > goroutines {
		t.Errorf("%d goroutines after twenty rounds, %d before them", got, goroutines)
	}
	if want := 1 + 20*2 + 1; len(s.History) != want {
		t.Errorf("History has %d messages, want %d", len(s.History), want)
	}
}

// loopModel asks for the tool until it has been given rounds results.
type loopModel struct {
	mu     sync.Mutex
	rounds int
	asked  int
}

func (p *loopModel) Complete(_ context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	usage := types.TokenUsage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}
	if p.asked < p.rounds {
		p.asked++
		call := llm.ToolCall{ID: fmt.Sprintf("call_%d", p.asked), Name: "order_status", Arguments: json.RawMessage(`{"order":"A17"}`)}
		return schemaflux.CompletionResponse{ToolCalls: []llm.ToolCall{call}, FinishReason: "tool_calls", Usage: usage}, nil
	}
	return schemaflux.CompletionResponse{Content: fmt.Sprintf("Done after %d rounds.", p.rounds), FinishReason: "stop", Usage: usage}, nil
}
func (p *loopModel) Name() string                                      { return "local" }
func (p *loopModel) EstimateCost(schemaflux.CompletionRequest) float64 { return 0 }
func (p *loopModel) RetryPolicy() (int, time.Duration)                 { return 0, 0 }

// A turn cancelled while the request is with the provider returns, leaves the
// history as it was, and the same turn can be made again.
func TestIntegrationACancelledTurnLeavesTheSessionAsItWas(t *testing.T) {
	server := newChatServer(t, responsesText(40, "never read"), responsesText(40, "Order A17 shipped."))
	server.hold = make(chan struct{})
	s := &schemaflux.Session{Model: "gpt-6-luna", Tools: []schemaflux.Tool{orderTool}}

	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	go func() {
		_, err := s.Send(ctx, "Where is order A17?")
		errs <- err
	}()
	for len(server.sent()) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()

	select {
	case err := <-errs:
		if err == nil || (schemaflux.KindOf(err) != types.KindCanceled && !errors.Is(err, context.Canceled)) {
			t.Errorf("err = %v, want the turn cancelled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the turn did not return after it was cancelled")
	}
	select {
	case <-server.dropped:
	case <-time.After(5 * time.Second):
		t.Error("the server never saw the request end: the cancellation did not reach the wire")
	}
	if s.History != nil {
		t.Errorf("History = %+v after a cancelled turn", s.History)
	}

	// The same call, on a server that answers.
	server.mu.Lock()
	server.hold = nil
	server.replies = []string{"", responsesText(40, "Order A17 shipped.")}
	server.mu.Unlock()
	reply, err := s.Send(context.Background(), "Where is order A17?")
	if err != nil || reply.Value.Content != "Order A17 shipped." || len(s.History) != 2 {
		t.Fatalf("the turn could not be made again: %+v, %v, History %d", reply.Value, err, len(s.History))
	}
}

// A secret in a tool's result does not reach the provider when the redaction
// middleware is in front of it, and the session works behind it. The caller's own
// history keeps what the caller wrote: the middleware rewrites the request it
// forwards and nothing else.
func TestIntegrationASessionWorksBehindTheRedactionMiddleware(t *testing.T) {
	inner := &chatModel{}
	installChatProvider(t, mw.Chain(inner, mw.RedactEgress()))
	s := &schemaflux.Session{Model: "chat-model", Tools: []schemaflux.Tool{orderTool}}
	const (
		email = "alice@example.com"
		key   = "sk-proj-0123456789abcdefghijklmnop"
	)
	ctx := context.Background()

	first, err := s.Send(ctx, "Where is order A17? My address is "+email)
	if err != nil || len(first.Value.ToolCalls) != 1 {
		t.Fatalf("first = %+v, %v", first.Value, err)
	}
	if _, err := s.SendResults(ctx, map[string]string{first.Value.ToolCalls[0].ID: "shipped to " + email + " using " + key}); err != nil {
		t.Fatalf("SendResults: %v", err)
	}
	if _, err := s.Send(ctx, "When does it arrive?"); err != nil {
		t.Fatalf("second Send: %v", err)
	}

	for i, req := range inner.sent() {
		encoded, err := json.Marshal(req.Messages)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), email) || strings.Contains(string(encoded), key) {
			t.Errorf("request %d carried a secret to the provider: %s", i+1, encoded)
		}
	}
	// The conversation and its tool call arrived whole behind the redaction.
	sent := inner.sent()
	if len(sent) != 3 || len(sent[1].Messages) != 3 || sent[1].Messages[2].ToolCallID != first.Value.ToolCalls[0].ID {
		t.Fatalf("the provider was sent %d requests: %+v", len(sent), sent)
	}
	if !strings.Contains(sent[1].Messages[2].Content, "shipped to") {
		t.Errorf("the result was not sent at all: %q", sent[1].Messages[2].Content)
	}
	// The caller's history is as the caller wrote it.
	if !strings.Contains(s.History[2].Content, email) || !strings.Contains(s.History[0].Content, email) {
		t.Errorf("the middleware rewrote the caller's history: %+v", s.History)
	}
}

// refusingModel is a provider that cannot carry tools or a conversation and says
// so before it sends anything, as the built-in providers that cannot do.
type refusingModel struct {
	mu       sync.Mutex
	attempts int
}

func (p *refusingModel) Complete(_ context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	p.mu.Lock()
	p.attempts++
	p.mu.Unlock()
	if err := llm.RefuseUnsupportedFields(req, "refuser", "Tools", "Messages"); err != nil {
		return schemaflux.CompletionResponse{}, err
	}
	return schemaflux.CompletionResponse{Content: "answered the empty prompt", FinishReason: "stop"}, nil
}
func (p *refusingModel) Name() string                                      { return "refuser" }
func (p *refusingModel) EstimateCost(schemaflux.CompletionRequest) float64 { return 0 }
func (p *refusingModel) RetryPolicy() (int, time.Duration)                 { return 0, 0 }

// A provider that cannot do tools fails the turn as unsupported, and with the
// fallback middleware the same conversation is served by an alternate that can.
func TestIntegrationASessionFallsBackFromAProviderThatCannotDoToolsToOneThatCan(t *testing.T) {
	t.Run("without a fallback the turn is unsupported", func(t *testing.T) {
		primary := &refusingModel{}
		installChatProvider(t, primary)
		s := &schemaflux.Session{Model: "chat-model", Tools: []schemaflux.Tool{orderTool}}
		_, err := s.Send(context.Background(), "Where is order A17?")
		if !errors.Is(err, types.ErrUnsupportedCapability) {
			t.Fatalf("err = %v, want the unsupported kind", err)
		}
		if s.History != nil {
			t.Errorf("History = %+v", s.History)
		}
	})

	t.Run("with a fallback the alternate serves the conversation", func(t *testing.T) {
		primary := &refusingModel{}
		alternate := &chatModel{name: "alternate"}
		installChatProvider(t, mw.Chain(primary, mw.Fallback([]mw.FallbackRoute{{Handler: alternate}})))
		s := &schemaflux.Session{Model: "chat-model", Tools: []schemaflux.Tool{orderTool}}
		ctx := context.Background()

		first, err := s.Send(ctx, "Where is order A17?")
		if err != nil || len(first.Value.ToolCalls) != 1 {
			t.Fatalf("first = %+v, %v", first.Value, err)
		}
		second, err := s.SendResults(ctx, map[string]string{first.Value.ToolCalls[0].ID: "shipped"})
		if err != nil || second.Value.Content != "Order A17 shipped on Tuesday." {
			t.Fatalf("second = %+v, %v", second.Value, err)
		}
		if primary.attempts != 2 {
			t.Errorf("the primary was asked %d times, want once for each turn", primary.attempts)
		}
		sent := alternate.sent()
		if len(sent) != 2 || len(sent[1].Messages) != 3 || len(sent[1].Tools) != 1 {
			t.Fatalf("the alternate was sent %d requests: %+v", len(sent), sent)
		}
		if second.Meta.Provider != "alternate" {
			t.Errorf("the record says %q answered, want the alternate", second.Meta.Provider)
		}
	})
}
