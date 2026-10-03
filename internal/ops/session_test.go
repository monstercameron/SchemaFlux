package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// The rules a history is held to, and the conversation that runs on them. The
// first half is what the transcript validator of the session code always
// promised; the second is the session calling a model, which it never did.

func userMsg(text string) llm.Message { return llm.Message{Role: "user", Content: text} }
func asstMsg(text string) llm.Message { return llm.Message{Role: "assistant", Content: text} }
func asstCalls(calls ...llm.ToolCall) llm.Message {
	return llm.Message{Role: "assistant", ToolCalls: calls}
}
func toolMsg(id, text string) llm.Message {
	return llm.Message{Role: "tool", ToolCallID: id, Content: text}
}
func callOf(id, name string) llm.ToolCall {
	return llm.ToolCall{ID: id, Name: name, Arguments: json.RawMessage(`{}`)}
}

func TestCheckTranscriptAcceptsAConversation(t *testing.T) {
	a, b, c := callOf("a", "get_weather"), callOf("b", "get_weather"), callOf("c", "get_time")
	cases := []struct {
		name    string
		history []llm.Message
	}{
		{"nothing yet", nil},
		{"an empty history", []llm.Message{}},
		{"one user message", []llm.Message{userMsg("hi")}},
		{"a question and its answer", []llm.Message{userMsg("hi"), asstMsg("hello")}},
		{"two exchanges", []llm.Message{userMsg("hi"), asstMsg("hello"), userMsg("and?"), asstMsg("fine")}},
		{"a call, its result and the answer", []llm.Message{userMsg("hi"), asstCalls(a), toolMsg("a", "7C"), asstMsg("7C")}},
		{"text with a call", []llm.Message{userMsg("hi"), {Role: "assistant", Content: "one moment", ToolCalls: []llm.ToolCall{a}}, toolMsg("a", "7C"), asstMsg("7C")}},
		{"two calls answered in the order asked", []llm.Message{userMsg("hi"), asstCalls(a, b), toolMsg("a", "1"), toolMsg("b", "2"), asstMsg("done")}},
		{"two calls answered in the other order", []llm.Message{userMsg("hi"), asstCalls(a, b), toolMsg("b", "2"), toolMsg("a", "1"), asstMsg("done")}},
		{"three calls", []llm.Message{userMsg("hi"), asstCalls(a, b, c), toolMsg("a", "1"), toolMsg("b", "2"), toolMsg("c", "3"), asstMsg("done")}},
		{"waiting for the results of a call", []llm.Message{userMsg("hi"), asstCalls(a)}},
		{"waiting for the results of two calls", []llm.Message{userMsg("hi"), asstCalls(a, b)}},
		{"a result that is empty text", []llm.Message{userMsg("hi"), asstCalls(a), toolMsg("a", ""), asstMsg("ok")}},
		{"a second round of calls", []llm.Message{userMsg("hi"), asstCalls(a), toolMsg("a", "1"), asstCalls(c), toolMsg("c", "2"), asstMsg("done")}},
		{"a user message after the results", []llm.Message{userMsg("hi"), asstCalls(a), toolMsg("a", "1"), userMsg("never mind")}},
		{"arguments with a body", []llm.Message{userMsg("hi"), asstCalls(llm.ToolCall{ID: "a", Name: "t", Arguments: json.RawMessage(` {"city": "Oslo"} `)}), toolMsg("a", "1")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := CheckTranscript(tc.history); err != nil {
				t.Fatalf("CheckTranscript refused it: %v", err)
			}
		})
	}
}

func TestCheckTranscriptRefusesAConversationNoModelHad(t *testing.T) {
	a, b := callOf("SECRET-ID-A", "SECRET-NAME"), callOf("SECRET-ID-B", "SECRET-NAME")
	secretArgs := json.RawMessage(`{"q":"SECRET-ARG"`)
	cases := []struct {
		name    string
		history []llm.Message
		wording string
	}{
		{"a system message", []llm.Message{{Role: "system", Content: "SECRET-TEXT"}, userMsg("hi")}, "a role that is not user, assistant or tool"},
		{"a developer message", []llm.Message{{Role: "developer", Content: "SECRET-TEXT"}}, "a role that is not"},
		{"a role in other capitals", []llm.Message{{Role: "User", Content: "SECRET-TEXT"}}, "a role that is not"},
		{"no role", []llm.Message{{Content: "SECRET-TEXT"}}, "a role that is not"},
		{"two user messages in a row", []llm.Message{userMsg("SECRET-TEXT"), userMsg("SECRET-TEXT")}, "after a user message"},
		{"an assistant message first", []llm.Message{asstMsg("SECRET-TEXT")}, "no user message came before it"},
		{"two assistant messages in a row", []llm.Message{userMsg("hi"), asstMsg("SECRET-TEXT"), asstMsg("SECRET-TEXT")}, "after an assistant message"},
		{"a tool result first", []llm.Message{toolMsg("SECRET-ID-A", "SECRET-TEXT")}, "no assistant message before it asked"},
		{"a tool result after a user message", []llm.Message{userMsg("hi"), toolMsg("SECRET-ID-A", "SECRET-TEXT")}, "no assistant message before it asked"},
		{"a tool result after an answer in words", []llm.Message{userMsg("hi"), asstMsg("SECRET-TEXT"), toolMsg("SECRET-ID-A", "SECRET-TEXT")}, "no assistant message before it asked"},
		{"a tool result that names no call", []llm.Message{userMsg("hi"), asstCalls(a), toolMsg("", "SECRET-TEXT")}, "names no call"},
		{"a tool result for a call nobody made", []llm.Message{userMsg("hi"), asstCalls(a), toolMsg("SECRET-ID-Z", "SECRET-TEXT")}, "did not make"},
		{"a call answered twice", []llm.Message{userMsg("hi"), asstCalls(a, b), toolMsg("SECRET-ID-A", "1"), toolMsg("SECRET-ID-A", "2")}, "already answered"},
		{"a result for a call of an earlier turn", []llm.Message{userMsg("hi"), asstCalls(a), toolMsg("SECRET-ID-A", "1"), asstMsg("ok"), userMsg("again"), toolMsg("SECRET-ID-A", "2")}, "no assistant message before it asked"},
		{"a call left unanswered before a user message", []llm.Message{userMsg("hi"), asstCalls(a), userMsg("SECRET-TEXT")}, "tool calls of message 2 are not answered"},
		{"one of two calls left unanswered before a user message", []llm.Message{userMsg("hi"), asstCalls(a, b), toolMsg("SECRET-ID-A", "1"), userMsg("SECRET-TEXT")}, "1 tool calls of message 2 are not answered"},
		{"a call left unanswered before an assistant message", []llm.Message{userMsg("hi"), asstCalls(a, b), toolMsg("SECRET-ID-A", "1"), asstMsg("SECRET-TEXT")}, "tool calls of message 2 are not answered"},
		{"a user message with no text", []llm.Message{userMsg("")}, "no text"},
		{"a user message of white space", []llm.Message{userMsg(" \n\t")}, "no text"},
		{"an assistant message that says nothing", []llm.Message{userMsg("hi"), {Role: "assistant"}}, "says nothing"},
		{"a call with no id", []llm.Message{userMsg("hi"), asstCalls(llm.ToolCall{Name: "SECRET-NAME", Arguments: json.RawMessage(`{}`)})}, "no id or no name"},
		{"a call with no name", []llm.Message{userMsg("hi"), asstCalls(llm.ToolCall{ID: "SECRET-ID-A", Arguments: json.RawMessage(`{}`)})}, "no id or no name"},
		{"arguments that are cut off", []llm.Message{userMsg("hi"), asstCalls(llm.ToolCall{ID: "SECRET-ID-A", Name: "SECRET-NAME", Arguments: secretArgs})}, "not a JSON object"},
		{"arguments that are a string", []llm.Message{userMsg("hi"), asstCalls(llm.ToolCall{ID: "SECRET-ID-A", Name: "SECRET-NAME", Arguments: json.RawMessage(`"SECRET-ARG"`)})}, "not a JSON object"},
		{"arguments that are a list", []llm.Message{userMsg("hi"), asstCalls(llm.ToolCall{ID: "SECRET-ID-A", Name: "SECRET-NAME", Arguments: json.RawMessage(`["SECRET-ARG"]`)})}, "not a JSON object"},
		{"no arguments at all", []llm.Message{userMsg("hi"), asstCalls(llm.ToolCall{ID: "SECRET-ID-A", Name: "SECRET-NAME"})}, "not a JSON object"},
		{"two calls with one id", []llm.Message{userMsg("hi"), asstCalls(a, callOf("SECRET-ID-A", "SECRET-NAME"))}, "an earlier call already used"},
		{"an id an earlier turn used", []llm.Message{userMsg("hi"), asstCalls(a), toolMsg("SECRET-ID-A", "1"), asstCalls(a)}, "an earlier call already used"},
		{"a tool message that asks for tools", []llm.Message{userMsg("hi"), asstCalls(a), {Role: "tool", ToolCallID: "SECRET-ID-A", ToolCalls: []llm.ToolCall{b}}}, "asks for tools"},
		{"a user message that carries a call", []llm.Message{{Role: "user", Content: "SECRET-TEXT", ToolCalls: []llm.ToolCall{a}}}, "carries tool data"},
		{"a user message that names a call", []llm.Message{{Role: "user", Content: "SECRET-TEXT", ToolCallID: "SECRET-ID-A"}}, "carries tool data"},
		{"an assistant message that names a call it answers", []llm.Message{userMsg("hi"), {Role: "assistant", Content: "SECRET-TEXT", ToolCallID: "SECRET-ID-A"}}, "names a call it answers"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckTranscript(tc.history)
			if err == nil {
				t.Fatal("the history was accepted")
			}
			if !errors.Is(err, ErrSessionInvariant) {
				t.Errorf("%v does not match ErrSessionInvariant", err)
			}
			if !errors.Is(err, types.ErrInvalidRequest) || types.KindOf(err) != types.KindInvalidRequest {
				t.Errorf("kind = %v, want an invalid request", types.KindOf(err))
			}
			if !strings.Contains(err.Error(), tc.wording) {
				t.Errorf("error %q lost its wording %q", err.Error(), tc.wording)
			}
			if !strings.Contains(err.Error(), "message ") {
				t.Errorf("error %q names no position", err.Error())
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Errorf("error %q carries the conversation", err.Error())
			}
		})
	}
}

// Every sequence of up to five turns over the roles is checked and none panics,
// and a history that is accepted starts with a user message and never holds a
// message that answers nothing.
func TestCheckTranscriptOverEveryShortSequenceOfRoles(t *testing.T) {
	roles := []string{"user", "assistant", "tool", "system"}
	accepted, refused := 0, 0
	var walk func(prefix []llm.Message, depth int)
	walk = func(prefix []llm.Message, depth int) {
		if err := CheckTranscript(prefix); err != nil {
			refused++
		} else {
			accepted++
			if len(prefix) > 0 && prefix[0].Role != "user" {
				t.Fatalf("a history that starts with %q was accepted", prefix[0].Role)
			}
			for _, m := range prefix {
				if m.Role == "system" {
					t.Fatal("a system message was accepted")
				}
			}
		}
		if depth == 5 {
			return
		}
		for _, role := range roles {
			message := llm.Message{Role: role, Content: "x"}
			switch role {
			case "assistant":
				message = asstCalls(callOf(fmt.Sprintf("c%d", len(prefix)), "t"))
				if depth%2 == 0 {
					message = asstMsg("x")
				}
			case "tool":
				message.ToolCallID = fmt.Sprintf("c%d", len(prefix)-1)
			}
			walk(append(append([]llm.Message(nil), prefix...), message), depth+1)
		}
	}
	walk(nil, 0)
	if accepted < 5 || refused < 100 {
		t.Errorf("%d histories accepted and %d refused: the walk did not reach both", accepted, refused)
	}
}

// chatSession is a session for the weather, over a provider that answers from a
// script, and the context its turns run on.
func chatSession(t *testing.T, steps ...chatStep) (*Session, *chatScript, context.Context) {
	t.Helper()
	provider := &chatScript{steps: steps}
	s := &Session{
		Instructions: "You answer questions about the weather. Use get_weather.",
		Tools: []llm.Tool{{
			Name: "get_weather", Description: "Look up the weather.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}},
		}},
	}
	ctx := WithSharedExecConfig(context.Background(), &ExecConfig{Provider: provider, Model: "chat-model"})
	return s, provider, ctx
}

func said(text string) chatStep {
	return chatStep{resp: llm.CompletionResponse{Content: text, FinishReason: "stop"}}
}
func asked(calls ...llm.ToolCall) chatStep {
	return chatStep{resp: toolCallsOnly(calls...)}
}

func cloneHistory(history []llm.Message) []llm.Message {
	if history == nil {
		return nil
	}
	out := make([]llm.Message, len(history))
	for i, m := range history {
		out[i] = m
		if m.ToolCalls != nil {
			out[i].ToolCalls = make([]llm.ToolCall, len(m.ToolCalls))
			for j, c := range m.ToolCalls {
				out[i].ToolCalls[j] = llm.ToolCall{ID: c.ID, Name: c.Name, Arguments: append(json.RawMessage(nil), c.Arguments...)}
			}
		}
	}
	return out
}

func TestASessionAnswersFromTheModel(t *testing.T) {
	s, provider, ctx := chatSession(t, chatStep{resp: llm.CompletionResponse{
		Content: "It is 7C in Oslo.", FinishReason: "stop",
		Usage: types.TokenUsage{PromptTokens: 30, CompletionTokens: 8, TotalTokens: 38},
	}})
	result, err := s.Send(ctx, "How warm is Oslo?")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if result.Value.Role != "assistant" || result.Value.Content != "It is 7C in Oslo." || len(result.Value.ToolCalls) != 0 {
		t.Errorf("reply = %+v", result.Value)
	}
	want := []llm.Message{userMsg("How warm is Oslo?"), asstMsg("It is 7C in Oslo.")}
	if !reflect.DeepEqual(s.History, want) {
		t.Errorf("History = %+v, want %+v", s.History, want)
	}
	sent := provider.sent()
	if len(sent) != 1 {
		t.Fatalf("%d requests", len(sent))
	}
	req := sent[0]
	if req.SystemPrompt != s.Instructions || len(req.Tools) != 1 || len(req.Messages) != 1 || req.Messages[0].Content != "How warm is Oslo?" || req.UserPrompt != "" {
		t.Errorf("request = %+v", req)
	}
	meta := result.Meta
	if meta.Operation != "chat.send" || meta.Attempts != 1 || meta.Usage.TotalTokens != 38 || meta.Model != "chat-model" || meta.Provider != "local" || meta.RequestID == "" {
		t.Errorf("meta = %+v, want a record of one request", meta)
	}
}

// The loop of a conversation with a tool: ask, run the tool, give the result back.
func TestASessionRunsAToolLoop(t *testing.T) {
	call := llm.ToolCall{ID: "call_1", Name: "get_weather", Arguments: json.RawMessage(`{"city":"Oslo"}`)}
	s, provider, ctx := chatSession(t, asked(call), said("It is 7C in Oslo."), said("Tomorrow will be colder."))

	first, err := s.Send(ctx, "How warm is Oslo?")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if first.Value.Content != "" || len(first.Value.ToolCalls) != 1 || first.Value.ToolCalls[0].ID != "call_1" || string(first.Value.ToolCalls[0].Arguments) != `{"city":"Oslo"}` {
		t.Fatalf("the first reply is %+v, want one call and no text", first.Value)
	}
	if first.Meta.Operation != "chat.send" {
		t.Errorf("operation = %q", first.Meta.Operation)
	}
	var tool *types.Check
	for i := range first.Meta.Checks {
		if first.Meta.Checks[i].Name == "tool calls" {
			tool = &first.Meta.Checks[i]
		}
	}
	if tool == nil || !tool.Passed || tool.Detail != "1 calls, all declared, arguments are JSON objects" {
		t.Errorf("checks = %+v, want the tool calls check, passed", first.Meta.Checks)
	}

	second, err := s.SendResults(ctx, map[string]string{"call_1": `{"temp":7}`})
	if err != nil {
		t.Fatalf("SendResults: %v", err)
	}
	if second.Value.Content != "It is 7C in Oslo." || second.Meta.Operation != "chat.sendresults" {
		t.Errorf("second = %+v (%q)", second.Value, second.Meta.Operation)
	}
	third, err := s.Send(ctx, "And tomorrow?")
	if err != nil || third.Value.Content != "Tomorrow will be colder." {
		t.Fatalf("third = %+v, %v", third.Value, err)
	}

	wantRoles := []string{"user", "assistant", "tool", "assistant", "user", "assistant"}
	if len(s.History) != len(wantRoles) {
		t.Fatalf("History has %d messages, want %d", len(s.History), len(wantRoles))
	}
	for i, role := range wantRoles {
		if s.History[i].Role != role {
			t.Errorf("message %d is %q, want %q", i+1, s.History[i].Role, role)
		}
	}
	if err := CheckTranscript(s.History); err != nil {
		t.Errorf("the history the session built is not valid: %v", err)
	}

	// The second request carries the model's own call, and the result under the
	// same id, in the order they happened.
	sent := provider.sent()
	if len(sent) != 3 {
		t.Fatalf("%d requests, want 3", len(sent))
	}
	got := sent[1].Messages
	if len(got) != 3 || got[0].Role != "user" || got[1].Role != "assistant" || len(got[1].ToolCalls) != 1 ||
		got[1].ToolCalls[0].ID != "call_1" || got[2].Role != "tool" || got[2].ToolCallID != "call_1" || got[2].Content != `{"temp":7}` {
		t.Errorf("the second request carried %+v", got)
	}
	if len(sent[2].Messages) != 5 {
		t.Errorf("the third request carried %d messages, want everything said so far", len(sent[2].Messages))
	}
	for i, req := range sent {
		if req.SystemPrompt != s.Instructions || len(req.Tools) != 1 {
			t.Errorf("request %d lost the instructions or the tools", i+1)
		}
	}
}

func TestASessionAnswersTwoToolsAskedForAtOnce(t *testing.T) {
	calls := []llm.ToolCall{
		{ID: "a", Name: "get_weather", Arguments: json.RawMessage(`{"city":"Oslo"}`)},
		{ID: "b", Name: "get_weather", Arguments: json.RawMessage(`{"city":"Rome"}`)},
		{ID: "c", Name: "get_weather", Arguments: json.RawMessage(`{"city":"Lima"}`)},
	}
	for round := 0; round < 20; round++ { // a map's order is random
		s, provider, ctx := chatSession(t, asked(calls...), said("Oslo 7, Rome 19, Lima 22."))
		first, err := s.Send(ctx, "Three cities?")
		if err != nil || len(first.Value.ToolCalls) != 3 {
			t.Fatalf("first = %+v, %v", first.Value, err)
		}
		if _, err := s.SendResults(ctx, map[string]string{"c": "22", "a": "7", "b": "19"}); err != nil {
			t.Fatalf("SendResults: %v", err)
		}
		got := provider.sent()[1].Messages
		if len(got) != 5 || got[2].ToolCallID != "a" || got[3].ToolCallID != "b" || got[4].ToolCallID != "c" ||
			got[2].Content != "7" || got[3].Content != "19" || got[4].Content != "22" {
			t.Fatalf("the results were sent as %+v, want them in the order of the calls", got[2:])
		}
	}
}

// A reply with text beside its calls keeps both.
func TestASessionKeepsTheTextAndTheCallsOfOneReply(t *testing.T) {
	step := chatStep{resp: llm.CompletionResponse{Content: "Let me look.", ToolCalls: []llm.ToolCall{weatherCall("call_1")}, FinishReason: "tool_calls"}}
	s, provider, ctx := chatSession(t, step, said("7C."))
	first, err := s.Send(ctx, "Oslo?")
	if err != nil || first.Value.Content != "Let me look." || len(first.Value.ToolCalls) != 1 {
		t.Fatalf("first = %+v, %v", first.Value, err)
	}
	if _, err := s.SendResults(ctx, map[string]string{"call_1": "7C"}); err != nil {
		t.Fatal(err)
	}
	if got := provider.sent()[1].Messages[1]; got.Content != "Let me look." || len(got.ToolCalls) != 1 {
		t.Errorf("the assistant message was sent back as %+v", got)
	}
}

// A request that is refused before it is sent changes nothing and sends nothing.
func TestASessionRefusesARequestThatIsNotValidBeforeSendingIt(t *testing.T) {
	call := weatherCall("call_1")
	waiting := []llm.Message{userMsg("Oslo?"), asstCalls(call)}
	answered := []llm.Message{userMsg("Oslo?"), asstCalls(call), toolMsg("call_1", "7C"), asstMsg("7C")}
	cases := []struct {
		name    string
		history []llm.Message
		do      func(s *Session, ctx context.Context) error
		kind    types.ErrorKind
	}{
		{"Send with no text", nil, func(s *Session, ctx context.Context) error { _, err := s.Send(ctx, ""); return err }, types.KindInvalidRequest},
		{"Send with white space", nil, func(s *Session, ctx context.Context) error { _, err := s.Send(ctx, " \n "); return err }, types.KindInvalidRequest},
		{"Send while the model waits for results", waiting, func(s *Session, ctx context.Context) error { _, err := s.Send(ctx, "hello?"); return err }, types.KindInvalidRequest},
		{"Send on a history that starts with the assistant", []llm.Message{asstMsg("hello")}, func(s *Session, ctx context.Context) error { _, err := s.Send(ctx, "hi"); return err }, types.KindInvalidRequest},
		{"Send on a history with a system message", []llm.Message{{Role: "system", Content: "x"}}, func(s *Session, ctx context.Context) error { _, err := s.Send(ctx, "hi"); return err }, types.KindInvalidRequest},
		{"Send on a history that ends with a user message", []llm.Message{userMsg("a")}, func(s *Session, ctx context.Context) error { _, err := s.Send(ctx, "b"); return err }, types.KindInvalidRequest},
		{"SendResults on no history", nil, func(s *Session, ctx context.Context) error {
			_, err := s.SendResults(ctx, map[string]string{"call_1": "x"})
			return err
		}, types.KindInvalidRequest},
		{"SendResults when the model asked for nothing", answered, func(s *Session, ctx context.Context) error {
			_, err := s.SendResults(ctx, map[string]string{"call_1": "x"})
			return err
		}, types.KindInvalidRequest},
		{"SendResults after a user message", []llm.Message{userMsg("hi")}, func(s *Session, ctx context.Context) error {
			_, err := s.SendResults(ctx, map[string]string{"call_1": "x"})
			return err
		}, types.KindInvalidRequest},
		{"SendResults with no results", waiting, func(s *Session, ctx context.Context) error { _, err := s.SendResults(ctx, nil); return err }, types.KindInvalidRequest},
		{"SendResults with an empty map", waiting, func(s *Session, ctx context.Context) error {
			_, err := s.SendResults(ctx, map[string]string{})
			return err
		}, types.KindInvalidRequest},
		{"SendResults with the wrong id", waiting, func(s *Session, ctx context.Context) error {
			_, err := s.SendResults(ctx, map[string]string{"SECRET-ID-Z": "x"})
			return err
		}, types.KindInvalidRequest},
		{"SendResults with a result too many", waiting, func(s *Session, ctx context.Context) error {
			_, err := s.SendResults(ctx, map[string]string{"call_1": "x", "SECRET-ID-Z": "y"})
			return err
		}, types.KindInvalidRequest},
		{"SendResults with a result missing", []llm.Message{userMsg("hi"), asstCalls(call, weatherCall("call_2"))}, func(s *Session, ctx context.Context) error {
			_, err := s.SendResults(ctx, map[string]string{"call_1": "x"})
			return err
		}, types.KindInvalidRequest},
		{"SendResults for calls that have a result already", []llm.Message{userMsg("hi"), asstCalls(call), toolMsg("call_1", "x")}, func(s *Session, ctx context.Context) error {
			_, err := s.SendResults(ctx, map[string]string{"call_1": "y"})
			return err
		}, types.KindInvalidRequest},
		{"Send on a history with two calls that share an id", []llm.Message{userMsg("hi"), asstCalls(call, call)}, func(s *Session, ctx context.Context) error {
			_, err := s.SendResults(ctx, map[string]string{"call_1": "y"})
			return err
		}, types.KindInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, provider, ctx := chatSession(t, said("never sent"))
			s.History = cloneHistory(tc.history)
			before := cloneHistory(s.History)

			err := tc.do(s, ctx)
			if err == nil {
				t.Fatal("the request was accepted")
			}
			if got := types.KindOf(err); got != tc.kind {
				t.Errorf("kind = %v (%v), want %v", got, err, tc.kind)
			}
			if provider.count() != 0 {
				t.Errorf("%d requests were sent", provider.count())
			}
			if !reflect.DeepEqual(s.History, before) {
				t.Errorf("History changed: %+v", s.History)
			}
			if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "call_1") || strings.Contains(err.Error(), "Oslo") {
				t.Errorf("the error carries the conversation: %q", err.Error())
			}
			if !strings.HasPrefix(err.Error(), "chat.send") {
				t.Errorf("error %q does not start with the operation", err.Error())
			}
		})
	}
}

func TestASessionRefusesSettingsThatAreNotUsable(t *testing.T) {
	cases := []struct {
		name  string
		apply func(s *Session)
	}{
		{"a tier that is none of the three", func(s *Session) { s.Tier = types.Speed(9) }},
		{"a negative tier", func(s *Session) { s.Tier = types.Speed(-1) }},
		{"a mode that is none of the three", func(s *Session) { s.Mode = types.Mode(9) }},
		{"a negative ceiling", func(s *Session) { s.MaxOutputTokens = -1 }},
		{"a tool with no name", func(s *Session) { s.Tools = []llm.Tool{{Name: ""}} }},
		{"a tool with a name of white space", func(s *Session) { s.Tools = []llm.Tool{{Name: "  "}} }},
		{"two tools with one name", func(s *Session) { s.Tools = []llm.Tool{{Name: "a"}, {Name: "b"}, {Name: "a"}} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, provider, ctx := chatSession(t, said("never sent"))
			tc.apply(s)
			_, err := s.Send(ctx, "hi")
			if types.KindOf(err) != types.KindConfiguration {
				t.Fatalf("err = %v, want a configuration error", err)
			}
			if provider.count() != 0 || s.History != nil {
				t.Errorf("%d requests were sent and History is %+v", provider.count(), s.History)
			}
		})
	}
	t.Run("a nil context", func(t *testing.T) {
		s, provider, _ := chatSession(t, said("never sent"))
		//lint:ignore SA1012 the point is the refusal of a nil context
		_, err := s.Send(nil, "hi") //nolint:staticcheck
		if types.KindOf(err) != types.KindInvalidRequest || provider.count() != 0 {
			t.Errorf("err = %v after %d requests", err, provider.count())
		}
		_, err = s.SendResults(nil, nil) //nolint:staticcheck
		if types.KindOf(err) != types.KindInvalidRequest {
			t.Errorf("SendResults err = %v", err)
		}
	})
}

// A turn that fails for any reason leaves History exactly as it was, and the same
// call can be made again.
func TestAFailedTurnLeavesTheHistoryAsItWas(t *testing.T) {
	good := weatherCall("call_1")
	cases := []struct {
		name string
		step chatStep
		kind types.ErrorKind
	}{
		{"the provider failed", chatStep{err: types.Newf(types.KindInvalidRequest, "llm", "rejected")}, types.KindInvalidRequest},
		{"the provider is unavailable", chatStep{err: types.Newf(types.KindProviderUnavailable, "llm", "down")}, types.KindProviderUnavailable},
		{"the reply is cut at the ceiling", chatStep{resp: llm.CompletionResponse{Content: "It is", FinishReason: "length"}}, types.KindOutputTruncated},
		{"the reply asks for a tool nobody declared", asked(llm.ToolCall{ID: "x", Name: "SECRET-NAME", Arguments: json.RawMessage(`{}`)}), types.KindMalformedOutput},
		{"the reply's call has no id", asked(llm.ToolCall{Name: "get_weather", Arguments: json.RawMessage(`{}`)}), types.KindMalformedOutput},
		{"the reply's calls share an id", asked(good, good), types.KindMalformedOutput},
		{"the reply's arguments are not an object", asked(llm.ToolCall{ID: "x", Name: "get_weather", Arguments: json.RawMessage(`[1]`)}), types.KindMalformedOutput},
		{"the reply's arguments are cut", asked(llm.ToolCall{ID: "x", Name: "get_weather", Arguments: json.RawMessage(`{"city":"SECRET-ARG`)}), types.KindMalformedOutput},
		{"the reply says nothing", chatStep{resp: llm.CompletionResponse{}}, types.KindMalformedOutput},
		{"the reply reuses an id of an earlier turn", asked(weatherCall("call_0")), types.KindMalformedOutput},
	}
	for _, tc := range cases {
		for _, entry := range []string{"Send", "SendResults"} {
			t.Run(tc.name+", "+entry, func(t *testing.T) {
				s, provider, ctx := chatSession(t, asked(weatherCall("call_0")), said("7C."))
				if _, err := s.Send(ctx, "Oslo?"); err != nil {
					t.Fatal(err)
				}
				if _, err := s.SendResults(ctx, map[string]string{"call_0": "7C"}); err != nil {
					t.Fatal(err)
				}
				// A second exchange whose first turn is the one that fails.
				if entry == "SendResults" {
					provider.script(asked(weatherCall("call_2")), tc.step, said("recovered"))
					if _, err := s.Send(ctx, "Rome?"); err != nil {
						t.Fatal(err)
					}
				} else {
					provider.script(tc.step, said("recovered"))
				}
				before := cloneHistory(s.History)
				total := s.Total()

				var err error
				if entry == "Send" {
					_, err = s.Send(ctx, "Lima?")
				} else {
					_, err = s.SendResults(ctx, map[string]string{"call_2": "19C"})
				}
				if err == nil {
					t.Fatal("the turn succeeded")
				}
				if got := types.KindOf(err); got != tc.kind {
					t.Errorf("kind = %v (%v), want %v", got, err, tc.kind)
				}
				if !reflect.DeepEqual(s.History, before) {
					t.Errorf("History changed to %+v", s.History)
				}
				if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "call_") {
					t.Errorf("the error carries the model's call: %q", err.Error())
				}
				if got := s.Total(); got.Attempts <= total.Attempts {
					t.Errorf("the total counts %d attempts after %d: a request that was paid for is missing from it", got.Attempts, total.Attempts)
				}
			})
		}
	}
}
