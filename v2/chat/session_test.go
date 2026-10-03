package chat_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/types"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/chat"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func TestATwoTurnConversationWithOneToolCall(t *testing.T) {
	t.Parallel()
	model := scripted(asking(toolCall("call_1", "lookup", `{"id":"A17"}`)), saying("Order A17 shipped on Tuesday."))
	ctx := onClient(model)
	s := chat.Session{Instructions: "You answer questions about orders.", Tools: []schemaflux.Tool{lookupTool, clockTool}}

	first, err := s.Send(ctx, "Where is order A17?")
	if err != nil {
		t.Fatal(err)
	}
	wantAsk := schemaflux.Message{Role: "assistant", ToolCalls: []schemaflux.ToolCall{toolCall("call_1", "lookup", `{"id":"A17"}`)}}
	if !reflect.DeepEqual(first, wantAsk) {
		t.Fatalf("the first reply = %+v, want %+v", first, wantAsk)
	}
	requireHistory(t, s.History, []schemaflux.Message{{Role: "user", Content: "Where is order A17?"}, wantAsk})

	req := model.Last()
	if req.System != "You answer questions about orders." || req.User != "" || req.Format != "text" || req.Schema != nil || req.SchemaName != "" || req.Decision != nil {
		t.Errorf("the first request = %+v", req)
	}
	if !reflect.DeepEqual(req.Tools, []schemaflux.Tool{lookupTool, clockTool}) {
		t.Errorf("the tools sent = %+v", req.Tools)
	}
	if want := []schemaflux.Message{{Role: "user", Content: "Where is order A17?"}}; !reflect.DeepEqual(req.Messages, want) {
		t.Errorf("the conversation sent = %+v", req.Messages)
	}
	firstKey := req.CacheKey

	second, err := s.SendResults(ctx, map[string]string{"call_1": `{"status":"shipped"}`})
	if err != nil {
		t.Fatal(err)
	}
	if second.Role != "assistant" || second.Content != "Order A17 shipped on Tuesday." || second.ToolCalls != nil {
		t.Errorf("the second reply = %+v", second)
	}
	wantHistory := []schemaflux.Message{
		{Role: "user", Content: "Where is order A17?"},
		wantAsk,
		{Role: "tool", ToolCallID: "call_1", Content: `{"status":"shipped"}`},
		second,
	}
	requireHistory(t, s.History, wantHistory)

	req = model.Last()
	if !reflect.DeepEqual(req.Messages, wantHistory[:3]) {
		t.Errorf("the conversation of the second request = %+v", req.Messages)
	}
	if req.System != "You answer questions about orders." || len(req.Tools) != 2 {
		t.Errorf("the second request = %+v", req)
	}
	if req.CacheKey == "" || req.CacheKey != firstKey {
		t.Errorf("the cache key of turn two is %q and of turn one %q: the part that is stable across a conversation is one key", req.CacheKey, firstKey)
	}
	if strings.Contains(req.CacheKey, "A17") || strings.Contains(req.CacheKey, "orders") {
		t.Errorf("the cache key holds the conversation: %q", req.CacheKey)
	}
	if model.Calls() != 2 {
		t.Errorf("%d requests were sent, want 2", model.Calls())
	}

	// A third turn, a question after the answer, goes on from the history.
	if _, err := s.Send(ctx, "Thanks."); err != nil {
		t.Fatal(err)
	}
	if got := model.Last().Messages; len(got) != 5 || got[4].Content != "Thanks." {
		t.Errorf("the third request carried %+v", got)
	}
}

func TestTwoToolsAskedForAtOnceAreAnsweredInTheOrderOfTheCalls(t *testing.T) {
	t.Parallel()
	model := scripted(
		asking(toolCall("b-call", "clock", `{}`), toolCall("a-call", "lookup", `{"id":"1"}`), toolCall("c-call", "lookup", `{"id":"2"}`)),
		saying("done"),
	)
	ctx := onClient(model)
	s := chat.Session{Tools: []schemaflux.Tool{lookupTool, clockTool}}
	first, err := s.Send(ctx, "go")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.ToolCalls) != 3 || first.ToolCalls[0].ID != "b-call" || first.ToolCalls[1].ID != "a-call" || first.ToolCalls[2].ID != "c-call" {
		t.Fatalf("the calls = %+v", first.ToolCalls)
	}
	// A map has no order; the messages follow the calls, not the keys.
	reply, err := s.SendResults(ctx, map[string]string{"c-call": "three", "a-call": "two", "b-call": "one"})
	if err != nil {
		t.Fatal(err)
	}
	sent := model.Last().Messages
	if len(sent) != 5 || sent[2].ToolCallID != "b-call" || sent[2].Content != "one" || sent[3].ToolCallID != "a-call" || sent[3].Content != "two" ||
		sent[4].ToolCallID != "c-call" || sent[4].Content != "three" {
		t.Errorf("the tool messages sent = %+v", sent[2:])
	}
	if reply.Content != "done" || len(s.History) != 6 {
		t.Errorf("reply %+v, History of %d", reply, len(s.History))
	}
}

func TestAReplyWithWordsAndCallsIsKeptWithBoth(t *testing.T) {
	t.Parallel()
	model := scripted(schemaflux.Response{Content: "Let me check.", ToolCalls: []schemaflux.ToolCall{toolCall("c1", "clock", `{}`)}}, saying("It is noon."))
	ctx := onClient(model)
	s := chat.Session{Tools: []schemaflux.Tool{clockTool}}
	first, err := s.Send(ctx, "time?")
	if err != nil || first.Content != "Let me check." || len(first.ToolCalls) != 1 {
		t.Fatalf("%+v, %v", first, err)
	}
	if _, err := s.SendResults(ctx, map[string]string{"c1": "12:00"}); err != nil {
		t.Fatal(err)
	}
	if got := model.Last().Messages[1]; got.Content != "Let me check." || len(got.ToolCalls) != 1 {
		t.Errorf("the assistant message was sent back as %+v", got)
	}
}

// What a caller does to the Message it was given changes nothing in History, and
// what it does to History's slice after a turn changes nothing the next turn
// sends that it did not mean to.
func TestTheReturnedReplyIsACopyOfTheLastMessageOfHistory(t *testing.T) {
	t.Parallel()
	model := scripted(asking(toolCall("c1", "lookup", `{"id":"1"}`)), saying("ok"))
	ctx := onClient(model)
	s := chat.Session{Tools: []schemaflux.Tool{lookupTool}}
	reply, err := s.Send(ctx, "q")
	if err != nil {
		t.Fatal(err)
	}
	reply.ToolCalls[0].Name = "changed"
	reply.ToolCalls[0].Arguments[2] = 'X'
	reply.ToolCalls = append(reply.ToolCalls, toolCall("c2", "x", `{}`))
	reply.Role = "user"
	last := s.History[len(s.History)-1]
	if last.Role != "assistant" || len(last.ToolCalls) != 1 || last.ToolCalls[0].Name != "lookup" || string(last.ToolCalls[0].Arguments) != `{"id":"1"}` {
		t.Fatalf("a change to the returned Message reached History: %+v", last)
	}
	if _, err := s.SendResults(ctx, map[string]string{"c1": "r"}); err != nil {
		t.Fatalf("the conversation no longer goes on: %v", err)
	}

	// A slice the caller took before a turn is not written to by it.
	before := s.History
	snapshot := copyHistory(before)
	if _, err := s.Send(ctx, "again"); err != nil {
		t.Fatal(err)
	}
	requireHistory(t, before, snapshot)
	if len(s.History) != len(before)+2 {
		t.Errorf("History has %d messages after %d", len(s.History), len(before))
	}
}

func TestSendResultsRefusesResultsThatDoNotAnswerEveryPendingCallOnce(t *testing.T) {
	t.Parallel()
	pending := func() []schemaflux.Message {
		return []schemaflux.Message{
			{Role: "user", Content: "q"},
			{Role: "assistant", ToolCalls: []schemaflux.ToolCall{toolCall("a", "lookup", `{}`), toolCall("b", "lookup", `{}`)}},
		}
	}
	answered := append(pending(), schemaflux.Message{Role: "tool", ToolCallID: "a", Content: "x"}, schemaflux.Message{Role: "tool", ToolCallID: "b", Content: "y"})
	cases := []struct {
		name    string
		history []schemaflux.Message
		results map[string]string
	}{
		{"missing both", pending(), map[string]string{}},
		{"nil results", pending(), nil},
		{"missing the second", pending(), map[string]string{"a": "one"}},
		{"missing the first", pending(), map[string]string{"b": "two"}},
		{"an unknown id beside both", pending(), map[string]string{"a": "one", "b": "two", "x": "extra"}},
		{"an unknown id alone", pending(), map[string]string{"x": "extra"}},
		{"missing one and unknown one", pending(), map[string]string{"b": "two", "x": "extra"}},
		{"an empty id", pending(), map[string]string{"": "empty", "a": "one", "b": "two"}},
		{"the case differs", pending(), map[string]string{"A": "one", "b": "two"}},
		{"surrounding space on an id", pending(), map[string]string{" a": "one", "b ": "two"}},
		{"nothing is pending in an empty history", nil, map[string]string{"a": "one"}},
		{"the last message is the user's", pending()[:1], map[string]string{"a": "one"}},
		{"the last reply asked for nothing", []schemaflux.Message{{Role: "user", Content: "q"}, {Role: "assistant", Content: "words"}}, map[string]string{"a": "one"}},
		{"the results were already given", answered, map[string]string{"a": "one", "b": "two"}},
		{"the results were already given and none are sent", answered, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			model := scripted(saying("never"))
			ctx, recorder := recorded(onClient(model))
			s := chat.Session{Tools: []schemaflux.Tool{lookupTool}, History: copyHistory(c.history)}
			before := copyHistory(s.History)
			reply, err := s.SendResults(ctx, c.results)
			if err == nil {
				t.Fatal("results that do not match were accepted")
			}
			e := requireKind(t, err, schemaflux.ErrInvalidRequest)
			if !strings.HasPrefix(e.Error(), "chat.sendresults:") {
				t.Errorf("the message does not begin with the operation: %q", e.Error())
			}
			if !reflect.DeepEqual(reply, schemaflux.Message{}) {
				t.Errorf("a reply came back beside the error: %+v", reply)
			}
			if model.Calls() != 0 {
				t.Errorf("%d requests were sent", model.Calls())
			}
			requireHistory(t, s.History, before)
			records := recorder.Records()
			if len(records) != 1 || records[0].Op != "chat.sendresults" || records[0].Requests != 0 || records[0].Err == nil {
				t.Errorf("records = %+v, want one refused record with no request", records)
			}
		})
	}
}

// A refusal says how many, never which: an id is the model's text and a result is
// the caller's.
func TestSendResultsRefusalNamesCountsAndNoIdOrText(t *testing.T) {
	t.Parallel()
	s := chat.Session{History: []schemaflux.Message{
		{Role: "user", Content: "q"},
		{Role: "assistant", ToolCalls: []schemaflux.ToolCall{toolCall("id-canary", "lookup", `{"x":"args-canary"}`)}},
	}}
	_, err := s.SendResults(context.Background(), map[string]string{"other-canary": "result-canary"})
	e := requireKind(t, err, schemaflux.ErrInvalidRequest)
	requireNone(t, "the error", chain(e), "id-canary", "other-canary", "result-canary", "args-canary", "lookup")
	if !strings.Contains(e.Error(), "1 tool calls") || !strings.Contains(e.Error(), "1 have no result") || !strings.Contains(e.Error(), "1 results answer no call") {
		t.Errorf("the message does not say how many: %q", e.Error())
	}
}

func TestAnEmptyResultIsAnAnswer(t *testing.T) {
	t.Parallel()
	model := scripted(saying("done"))
	s := chat.Session{History: []schemaflux.Message{
		{Role: "user", Content: "q"},
		{Role: "assistant", ToolCalls: []schemaflux.ToolCall{toolCall("a", "lookup", `{}`), toolCall("b", "lookup", `{}`)}},
	}, Tools: []schemaflux.Tool{lookupTool}}
	if _, err := s.SendResults(onClient(model), map[string]string{"a": "", "b": ""}); err != nil {
		t.Fatal(err)
	}
	if len(s.History) != 5 || s.History[2].Content != "" || s.History[3].Content != "" || s.History[4].Content != "done" {
		t.Errorf("History = %+v", s.History)
	}
}

func TestSendRefusesEmptyTextAndATurnWhileResultsAreOwed(t *testing.T) {
	t.Parallel()
	owing := []schemaflux.Message{
		{Role: "user", Content: "q"},
		{Role: "assistant", ToolCalls: []schemaflux.ToolCall{toolCall("a", "lookup", `{}`)}},
	}
	cases := []struct {
		name    string
		history []schemaflux.Message
		text    string
	}{
		{"empty text", nil, ""},
		{"spaces", nil, "   "},
		{"a new line", nil, "\n\t"},
		{"empty text mid conversation", []schemaflux.Message{{Role: "user", Content: "q"}, {Role: "assistant", Content: "a"}}, ""},
		{"results are owed", owing, "and another thing"},
		{"results are owed and the text is empty", owing, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			model := scripted(saying("never"))
			s := chat.Session{Tools: []schemaflux.Tool{lookupTool}, History: copyHistory(c.history)}
			before := copyHistory(s.History)
			_, err := s.Send(onClient(model), c.text)
			e := requireKind(t, err, schemaflux.ErrInvalidRequest)
			if !strings.HasPrefix(e.Error(), "chat.send:") || model.Calls() != 0 {
				t.Errorf("error %q, %d requests", e.Error(), model.Calls())
			}
			requireHistory(t, s.History, before)
		})
	}
}

func TestANilContextIsRefusedByBothMethods(t *testing.T) {
	t.Parallel()
	var nilContext context.Context
	s := chat.Session{History: []schemaflux.Message{{Role: "user", Content: "q"}, {Role: "assistant", ToolCalls: []schemaflux.ToolCall{toolCall("a", "lookup", `{}`)}}}}
	before := copyHistory(s.History)
	if _, err := s.Send(nilContext, "x"); err == nil || requireKind(t, err, schemaflux.ErrInvalidRequest) == nil {
		t.Errorf("Send: %v", err)
	}
	if _, err := s.SendResults(nilContext, map[string]string{"a": "r"}); err == nil || requireKind(t, err, schemaflux.ErrInvalidRequest) == nil {
		t.Errorf("SendResults: %v", err)
	}
	requireHistory(t, s.History, before)
	// A refused call leaves the session usable.
	if _, err := s.SendResults(onClient(scripted(saying("ok"))), map[string]string{"a": "r"}); err != nil {
		t.Errorf("the session is stuck after a nil context: %v", err)
	}
}

// What a failed turn leaves behind is the history it was given, the same call can
// be made again, and no reply comes back beside the error.
func TestAFailedTurnLeavesHistoryExactlyAsItWasAndCanBeMadeAgain(t *testing.T) {
	t.Parallel()
	owing := []schemaflux.Message{
		{Role: "user", Content: "q"},
		{Role: "assistant", ToolCalls: []schemaflux.ToolCall{toolCall("earlier", "lookup", `{"id":"1"}`)}},
	}
	failures := []struct {
		name  string
		reply func(*schemafluxtest.Provider)
		kind  schemaflux.Kind
	}{
		{"the provider is unavailable", func(p *schemafluxtest.Provider) {
			p.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "vendor"})
		}, schemaflux.ErrUnavailable},
		{"the provider is rate limited", func(p *schemafluxtest.Provider) {
			p.Fail(&schemaflux.Error{Kind: schemaflux.ErrRateLimited, Status: 429, RetryAfter: time.Millisecond})
		}, schemaflux.ErrRateLimited},
		{"the key is rejected", func(p *schemafluxtest.Provider) {
			p.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnauthorized, Status: 401})
		}, schemaflux.ErrUnauthorized},
		{"the provider fails with an error of its own", func(p *schemafluxtest.Provider) { p.Fail(errors.New("plain failure")) }, schemaflux.ErrUnavailable},
		{"the model asks for a tool that was not declared", func(p *schemafluxtest.Provider) {
			p.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) {
				return asking(toolCall("n1", "undeclared", `{}`)), nil
			})
		}, schemaflux.ErrInvalidAnswer},
		{"two calls share an id", func(p *schemafluxtest.Provider) {
			p.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) {
				return asking(toolCall("same", "lookup", `{}`), toolCall("same", "lookup", `{}`)), nil
			})
		}, schemaflux.ErrInvalidAnswer},
		{"a call has no id", func(p *schemafluxtest.Provider) {
			p.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) {
				return asking(toolCall("", "lookup", `{}`)), nil
			})
		}, schemaflux.ErrInvalidAnswer},
		{"a call reuses the id of an earlier one", func(p *schemafluxtest.Provider) {
			p.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) {
				return asking(toolCall("earlier", "lookup", `{}`)), nil
			})
		}, schemaflux.ErrInvalidAnswer},
		{"the arguments are an array", func(p *schemafluxtest.Provider) {
			p.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) {
				return asking(toolCall("n1", "lookup", `[1,2]`)), nil
			})
		}, schemaflux.ErrInvalidAnswer},
		{"the arguments are not JSON", func(p *schemafluxtest.Provider) {
			p.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) {
				return asking(toolCall("n1", "lookup", `{"id":`)), nil
			})
		}, schemaflux.ErrInvalidAnswer},
		{"the arguments are missing", func(p *schemafluxtest.Provider) {
			p.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) {
				return asking(schemaflux.ToolCall{ID: "n1", Name: "lookup"}), nil
			})
		}, schemaflux.ErrInvalidAnswer},
		{"the reply has neither words nor calls", func(p *schemafluxtest.Provider) { p.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) { return schemaflux.Response{}, nil }) }, schemaflux.ErrInvalidAnswer},
		{"the reply is only white space", func(p *schemafluxtest.Provider) { p.Reply(" \n\t") }, schemaflux.ErrInvalidAnswer},
		{"the reply was cut at the ceiling", func(p *schemafluxtest.Provider) {
			p.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) {
				return schemaflux.Response{Content: "partial", FinishReason: "length"}, nil
			})
		}, schemaflux.ErrTruncated},
	}
	for _, f := range failures {
		for _, turn := range []string{"Send", "SendResults"} {
			t.Run(f.name+"/"+turn, func(t *testing.T) {
				t.Parallel()
				model := &schemafluxtest.Provider{}
				f.reply(model)
				ctx := onClient(model)
				s := chat.Session{Tools: []schemaflux.Tool{lookupTool}}
				var run func() (schemaflux.Message, error)
				switch turn {
				case "Send":
					// A conversation that went through a tool call, so that an id is
					// already used when the model asks for another.
					s.History = []schemaflux.Message{
						{Role: "user", Content: "q0"},
						{Role: "assistant", ToolCalls: []schemaflux.ToolCall{toolCall("earlier", "lookup", `{"id":"1"}`)}},
						{Role: "tool", ToolCallID: "earlier", Content: "r0"},
						{Role: "assistant", Content: "a0"},
					}
					run = func() (schemaflux.Message, error) { return s.Send(ctx, "next") }
				default:
					s.History = copyHistory(owing)
					run = func() (schemaflux.Message, error) { return s.SendResults(ctx, map[string]string{"earlier": "r"}) }
				}
				before := copyHistory(s.History)
				reply, err := run()
				if err == nil {
					t.Fatalf("the turn succeeded: %+v", reply)
				}
				requireKind(t, err, f.kind)
				if !reflect.DeepEqual(reply, schemaflux.Message{}) {
					t.Errorf("a reply came back beside the error: %+v", reply)
				}
				requireHistory(t, s.History, before)

				// The same call can be made again, and now it goes through.
				model.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) { return saying("fine"), nil })
				again, err := run()
				if err != nil || again.Content != "fine" {
					t.Fatalf("the repeated turn = %+v, %v", again, err)
				}
				if len(s.History) != len(before)+2 {
					t.Errorf("History has %d messages after the repeated turn", len(s.History))
				}
			})
		}
	}
}

func TestACancelledTurnLeavesHistoryAsItWas(t *testing.T) {
	t.Parallel()
	for _, how := range []string{"cancelled while the request is in flight", "deadline while the request is in flight", "cancelled before the call"} {
		t.Run(how, func(t *testing.T) {
			t.Parallel()
			model := &schemafluxtest.Provider{}
			entered := make(chan struct{}, 1)
			model.RespondContext(func(ctx context.Context, _ int, _ schemaflux.Request) (schemaflux.Response, error) {
				entered <- struct{}{}
				<-ctx.Done()
				return schemaflux.Response{}, ctx.Err()
			})
			base := onClient(model)
			var ctx context.Context
			var cancel context.CancelFunc
			switch how {
			case "deadline while the request is in flight":
				ctx, cancel = context.WithTimeout(base, 40*time.Millisecond)
			default:
				ctx, cancel = context.WithCancel(base)
			}
			defer cancel()
			ctx, recorder := recorded(ctx)
			s := chat.Session{History: []schemaflux.Message{{Role: "user", Content: "q"}, {Role: "assistant", Content: "a"}}}
			before := copyHistory(s.History)

			done := make(chan error, 1)
			if how == "cancelled before the call" {
				cancel()
			}
			go func() { _, err := s.Send(ctx, "next"); done <- err }()
			if how == "cancelled while the request is in flight" {
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("the request was not sent")
				}
				cancel()
			}
			var err error
			select {
			case err = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("the turn did not end")
			}
			if err == nil {
				t.Fatal("the turn succeeded")
			}
			want := schemaflux.ErrCanceled
			if how == "deadline while the request is in flight" {
				want = schemaflux.ErrTimeout
			}
			requireKind(t, err, want)
			requireHistory(t, s.History, before)
			if how == "cancelled before the call" && model.Calls() != 0 {
				t.Errorf("a context that had ended sent %d requests", model.Calls())
			}
			if records := recorder.Records(); len(records) != 1 || records[0].Op != "chat.send" || records[0].Err == nil {
				t.Errorf("records = %+v", records)
			}
			// And the session is free again.
			model.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) { return saying("ok"), nil })
			if _, err := s.Send(onClient(model), "again"); err != nil {
				t.Errorf("the session is stuck after a cancelled turn: %v", err)
			}
		})
	}
}

func TestASecondTurnWhileOneIsInFlightIsRefusedAtOnce(t *testing.T) {
	t.Parallel()
	model := &schemafluxtest.Provider{}
	entered := make(chan struct{})
	release := make(chan struct{})
	var inFlight atomic.Int32
	model.Respond(func(call int, _ schemaflux.Request) (schemaflux.Response, error) {
		if inFlight.Add(1) == 1 {
			close(entered)
			<-release
		}
		return saying("answer"), nil
	})
	ctx, recorder := recorded(onClient(model))
	s := chat.Session{Tools: []schemaflux.Tool{lookupTool}}
	done := make(chan error, 1)
	go func() { _, err := s.Send(ctx, "first"); done <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first request did not start")
	}

	for name, call := range map[string]func() error{
		"Send":        func() error { _, err := s.Send(ctx, "second"); return err },
		"SendResults": func() error { _, err := s.SendResults(ctx, map[string]string{"a": "b"}); return err },
	} {
		started := time.Now()
		err := call()
		if err == nil {
			t.Fatalf("%s: a second turn was accepted while one was in flight", name)
		}
		requireKind(t, err, schemaflux.ErrInvalidRequest)
		if time.Since(started) > 2*time.Second {
			t.Errorf("%s: the refusal waited for the first turn", name)
		}
	}
	if model.Calls() != 1 {
		t.Errorf("%d requests were sent while the first was in flight", model.Calls())
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(s.History) != 2 || s.History[0].Content != "first" {
		t.Errorf("History = %+v: the refused turns changed it", s.History)
	}
	if records := recorder.Records(); len(records) != 3 {
		t.Errorf("%d records, want one for each of the three calls", len(records))
	}
	if _, err := s.Send(ctx, "third"); err != nil {
		t.Errorf("the session is stuck after the refusal: %v", err)
	}
}

// A zero Session is usable, and what its zero fields mean is what the
// specification says: tier Smart, mode Transform, the client's model, no system
// prompt and no tools.
func TestTheZeroSessionsDefaults(t *testing.T) {
	t.Parallel()
	model := scripted(saying("hello"))
	var s chat.Session
	if _, err := s.Send(onClient(model), "hi"); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	if req.Model != "test-model" || req.System != "" || req.User != "" || req.Tools != nil || req.WebSearch || req.Effort != "" {
		t.Errorf("the request = %+v", req)
	}
	if want := float64(config.GetTemperature(types.TransformMode)); req.Temperature != want {
		t.Errorf("Temperature = %v, want the transform mode's %v", req.Temperature, want)
	}
	if want := config.GetMaxTokens(types.Smart); req.MaxTokens != want {
		t.Errorf("MaxTokens = %d, want the smart tier's %d", req.MaxTokens, want)
	}
	if len(req.Messages) != 1 || req.Messages[0].Role != "user" || req.Messages[0].Content != "hi" {
		t.Errorf("Messages = %+v", req.Messages)
	}
	if req.Tools != nil {
		t.Errorf("a session with no tools sent Tools = %#v: a provider that checks for nil would think some were declared", req.Tools)
	}
}

func TestEachSettingOfASessionReachesTheRequest(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		s     func() chat.Session
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"Model pins the model", func() chat.Session { return chat.Session{Model: "pinned-model"} }, func(t *testing.T, r schemaflux.Request) {
			if r.Model != "pinned-model" {
				t.Errorf("Model = %q", r.Model)
			}
		}},
		{"Tier fast has the fast ceiling", func() chat.Session { return chat.Session{Tier: schemaflux.TierFast} }, func(t *testing.T, r schemaflux.Request) {
			if r.MaxTokens != config.GetMaxTokens(types.Fast) {
				t.Errorf("MaxTokens = %d, want %d", r.MaxTokens, config.GetMaxTokens(types.Fast))
			}
		}},
		{"Tier quick has the quick ceiling", func() chat.Session { return chat.Session{Tier: schemaflux.TierQuick} }, func(t *testing.T, r schemaflux.Request) {
			if r.MaxTokens != config.GetMaxTokens(types.Quick) {
				t.Errorf("MaxTokens = %d, want %d", r.MaxTokens, config.GetMaxTokens(types.Quick))
			}
		}},
		{"Tier smart is the default", func() chat.Session { return chat.Session{Tier: schemaflux.TierSmart} }, func(t *testing.T, r schemaflux.Request) {
			if r.MaxTokens != config.GetMaxTokens(types.Smart) {
				t.Errorf("MaxTokens = %d", r.MaxTokens)
			}
		}},
		{"Mode strict", func() chat.Session { return chat.Session{Mode: schemaflux.ModeStrict} }, func(t *testing.T, r schemaflux.Request) {
			if want := float64(config.GetTemperature(types.Strict)); r.Temperature != want {
				t.Errorf("Temperature = %v, want %v", r.Temperature, want)
			}
		}},
		{"Mode creative", func() chat.Session { return chat.Session{Mode: schemaflux.ModeCreative} }, func(t *testing.T, r schemaflux.Request) {
			if want := float64(config.GetTemperature(types.Creative)); r.Temperature != want {
				t.Errorf("Temperature = %v, want %v", r.Temperature, want)
			}
		}},
		{"MaxOutputTokens overrides the tier", func() chat.Session { return chat.Session{Tier: schemaflux.TierFast, MaxOutputTokens: 77} }, func(t *testing.T, r schemaflux.Request) {
			if r.MaxTokens != 77 {
				t.Errorf("MaxTokens = %d", r.MaxTokens)
			}
		}},
		{"WebSearch declares the search tool", func() chat.Session { return chat.Session{WebSearch: true} }, func(t *testing.T, r schemaflux.Request) {
			if !r.WebSearch {
				t.Error("WebSearch was not sent")
			}
		}},
		{"Effort is asked for, not preferred", func() chat.Session { return chat.Session{Effort: schemaflux.EffortLow} }, func(t *testing.T, r schemaflux.Request) {
			if r.Effort != schemaflux.EffortLow || r.EffortPreferred {
				t.Errorf("Effort = %q preferred %v", r.Effort, r.EffortPreferred)
			}
		}},
		{"Instructions are sent as written", func() chat.Session { return chat.Session{Instructions: "  Keep {{this}} <b>as it is</b>.\n\nTwo paragraphs.\n"} }, func(t *testing.T, r schemaflux.Request) {
			if r.System != "  Keep {{this}} <b>as it is</b>.\n\nTwo paragraphs.\n" {
				t.Errorf("System = %q", r.System)
			}
		}},
		{"Tools are sent in order", func() chat.Session { return chat.Session{Tools: []schemaflux.Tool{clockTool, lookupTool}} }, func(t *testing.T, r schemaflux.Request) {
			if !reflect.DeepEqual(r.Tools, []schemaflux.Tool{clockTool, lookupTool}) {
				t.Errorf("Tools = %+v", r.Tools)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			model := scripted(saying("ok"))
			s := c.s()
			if _, err := s.Send(onClient(model), "hi"); err != nil {
				t.Fatal(err)
			}
			c.check(t, model.Last())
		})
	}
	t.Run("the client's model is used when the session names none, and the session's wins", func(t *testing.T) {
		t.Parallel()
		model := scripted(saying("ok"))
		ctx := onClient(model, func(c *schemaflux.Client) { c.Model = "client-model" })
		var s chat.Session
		if _, err := s.Send(ctx, "hi"); err != nil || model.Last().Model != "client-model" {
			t.Errorf("model %q, %v", model.Last().Model, err)
		}
		s.Model = "session-model"
		if _, err := s.Send(ctx, "again"); err != nil || model.Last().Model != "session-model" {
			t.Errorf("model %q, %v", model.Last().Model, err)
		}
	})
	t.Run("the library's reinforcement block is never added, with the client's setting either way", func(t *testing.T) {
		t.Parallel()
		for _, omit := range []bool{false, true} {
			model := scripted(saying("ok"))
			s := chat.Session{Instructions: "Only these."}
			if _, err := s.Send(onClient(model, func(c *schemaflux.Client) { c.OmitReinforcement = omit }), "hi"); err != nil {
				t.Fatal(err)
			}
			if got := model.Last().System; got != "Only these." {
				t.Errorf("OmitReinforcement %v: System = %q", omit, got)
			}
		}
	})
}

func TestASettingNoCallCanUseIsAConfigurationErrorAndNothingIsSent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		s    func() chat.Session
	}{
		{"a tier that is not one", func() chat.Session { return chat.Session{Tier: 9} }},
		{"a negative tier", func() chat.Session { return chat.Session{Tier: -1} }},
		{"a mode that is not one", func() chat.Session { return chat.Session{Mode: 9} }},
		{"a negative mode", func() chat.Session { return chat.Session{Mode: -3} }},
		{"a negative ceiling", func() chat.Session { return chat.Session{MaxOutputTokens: -1} }},
		{"a tool with no name", func() chat.Session { return chat.Session{Tools: []schemaflux.Tool{{Description: "nameless"}}} }},
		{"a tool named with spaces", func() chat.Session { return chat.Session{Tools: []schemaflux.Tool{{Name: "  "}}} }},
		{"a repeated tool name", func() chat.Session { return chat.Session{Tools: []schemaflux.Tool{lookupTool, clockTool, lookupTool}} }},
		{"a repeated tool name with another description", func() chat.Session { return chat.Session{Tools: []schemaflux.Tool{{Name: "x"}, {Name: "x", Description: "again"}}} }},
	}
	for _, c := range cases {
		for _, turn := range []string{"Send", "SendResults"} {
			t.Run(c.name+"/"+turn, func(t *testing.T) {
				t.Parallel()
				model := scripted(saying("never"))
				ctx, recorder := recorded(onClient(model))
				s := c.s()
				var err error
				if turn == "Send" {
					_, err = s.Send(ctx, "hi")
				} else {
					s.History = []schemaflux.Message{{Role: "user", Content: "q"}, {Role: "assistant", ToolCalls: []schemaflux.ToolCall{toolCall("a", "lookup", `{}`)}}}
					_, err = s.SendResults(ctx, map[string]string{"a": "r"})
				}
				e := requireKind(t, err, schemaflux.ErrConfiguration)
				if !strings.HasPrefix(e.Error(), "chat.send") || model.Calls() != 0 {
					t.Errorf("error %q, %d requests", e.Error(), model.Calls())
				}
				if records := recorder.Records(); len(records) != 1 || records[0].Requests != 0 || records[0].Err == nil {
					t.Errorf("records = %+v", records)
				}
				if turn == "Send" && s.History != nil {
					t.Errorf("History = %+v after a refused turn", s.History)
				}
			})
		}
	}
}

func TestATurnOnAProviderThatTakesNoToolsFailsAsUnsupportedAndSendsNothing(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		tools []schemaflux.Tool
	}{
		{"with tools", []schemaflux.Tool{lookupTool}},
		{"with none, since the message list is enough", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			model := &noToolsProvider{}
			ctx, recorder := recorded(onClient(model))
			s := chat.Session{Tools: c.tools, History: []schemaflux.Message{{Role: "user", Content: "q"}, {Role: "assistant", Content: "a"}}}
			before := copyHistory(s.History)
			_, err := s.Send(ctx, "next")
			e := requireKind(t, err, schemaflux.ErrUnsupported)
			if e.Op != "chat.send" {
				t.Errorf("Op = %q", e.Op)
			}
			if model.sent.Load() != 0 || model.asked.Load() != 1 {
				t.Errorf("the provider was asked %d times and sent %d", model.asked.Load(), model.sent.Load())
			}
			requireHistory(t, s.History, before)
			if records := recorder.Records(); len(records) != 1 || records[0].Op != "chat.send" || records[0].Err == nil {
				t.Errorf("records = %+v", records)
			}
		})
	}
}

// noToolsProvider is a provider that cannot take tools or a message list and
// says so, with the one-line check Request.Tools documents.
type noToolsProvider struct{ asked, sent atomic.Int32 }

func (p *noToolsProvider) Name() string { return "no-tools" }

func (p *noToolsProvider) Complete(_ context.Context, req schemaflux.Request) (schemaflux.Response, error) {
	p.asked.Add(1)
	if len(req.Tools) > 0 || len(req.Messages) > 0 {
		return schemaflux.Response{}, &schemaflux.Error{Kind: schemaflux.ErrUnsupported, Message: "this provider takes no tools or message lists"}
	}
	p.sent.Add(1)
	return schemaflux.Response{Content: "answered the empty prompt"}, nil
}

func (p *noToolsProvider) ModelFor(schemaflux.Tier) string { return "m" }
