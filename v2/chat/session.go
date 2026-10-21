package chat

import (
	"bytes"
	"context"
	"strings"
	"sync/atomic"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// The names of the two operations of a session. They are what a Record's Op
// says, what the error of a turn begins with, and the names the contract table
// knows (internal/ops/contracttable.go): the function's name after its package.
const (
	sendOperation        = "chat.send"
	sendResultsOperation = "chat.sendresults"
)

// A Session is a conversation with a model that may ask for tools. The zero
// value is ready to use: set Instructions and Tools if the conversation has
// them, then Send. A Session holds the history, so each turn sends everything
// said so far again; a long conversation costs more with every turn, and a
// caller who needs it shorter edits History between turns.
//
// The library never runs a tool. The model asks, the caller's code runs the
// tool, and SendResults gives the result back.
//
// A Session is for one conversation and is not safe for concurrent use: a
// method called while another is running returns ErrInvalidRequest at once and
// does not wait. Do not copy a Session after its first use. The client a turn
// runs on is the one on the turn's context (WithClient), or the default; a
// Session holds none, so a conversation can move between clients.
type Session struct {
	// Model pins the exact model for every turn. Empty means the client's model
	// if it names one, else the model Tier maps to.
	Model string

	// Tier is the class the model is chosen from when Model is empty, and it
	// sets the default ceiling on a reply's length. The zero value means
	// TierSmart. A value that is not one of the three tiers is a configuration
	// error.
	Tier schemaflux.Tier

	// Effort is how much the model reasons before it replies. The zero value
	// leaves it to the client and the provider. A value the route does not take
	// is ErrUnsupported before anything is sent.
	Effort schemaflux.Effort

	// Mode sets the sampling temperature. The zero value means ModeTransform. A
	// value that is not one of the three modes is a configuration error.
	Mode schemaflux.Mode

	// MaxOutputTokens is the ceiling on one reply, in tokens. Zero means the
	// Tier's ceiling. A negative value is a configuration error.
	MaxOutputTokens int

	// WebSearch declares the provider's web-search tool beside Tools. False
	// sends the request without it. A route that has none refuses with
	// ErrUnsupported before anything is sent.
	WebSearch bool

	// Instructions is the system prompt: what the conversation is for and how
	// the model should behave. It is the program's own text, sent as written and
	// as the system message; never put a user's or a document's text in it, that
	// belongs in a message. Empty means no system prompt. The library adds
	// nothing to it, and the client's OmitReinforcement setting does not apply.
	Instructions string

	// Tools are the functions the model may ask for. Nil means none, and the
	// model only answers in words. Names must be unique and not empty: a tool
	// with no name or a name an earlier tool has is a configuration error.
	Tools []schemaflux.Tool

	// History is the conversation so far, oldest first: user messages, the
	// model's replies, and the results of its tool calls. Send and SendResults
	// append to it. It is checked before every request: it may be empty or start
	// with a user message; a user message follows nothing, an assistant message
	// that asked for no tools, or a tool message; an assistant message follows a
	// user or tool message; every tool message answers a call of the assistant
	// message before it, once; and no call is left unanswered before the next
	// message. A history that breaks that is ErrInvalidRequest, and says which
	// position, never what it says. Nil means a conversation not yet started.
	// A caller who saves a conversation, resumes it or cuts old turns assigns or
	// edits History; the library never writes into a slice it was given, and a
	// turn that fails leaves History as it was.
	History []schemaflux.Message

	// busy is set while a turn runs, so that a second one is refused and not
	// queued behind it.
	busy atomic.Bool
}

// Send adds text as a user message to History, asks the model, adds its reply
// and returns it. When the reply's ToolCalls are not empty the model is waiting
// for their results: run them and call SendResults. The returned Message is a
// copy of the last element of History: changing it changes nothing in History.
//
// If the last message of History is an assistant message that asked for tools,
// Send is ErrInvalidRequest: give the results first. Empty text is
// ErrInvalidRequest. A turn that fails, for any reason, leaves History exactly as
// it was, so the same call can be made again, and returns the zero Message and an
// *Error. Each turn adds one Record, named "chat.send", whether it succeeded,
// failed or was refused before anything was sent.
func (s *Session) Send(ctx context.Context, text string) (schemaflux.Message, error) {
	return s.turn(ctx, sendOperation, func(history []schemaflux.Message) ([]schemaflux.Message, error) {
		if strings.TrimSpace(text) == "" {
			return nil, types.Newf(types.KindInvalidRequest, sendOperation, "the text is empty")
		}
		return []schemaflux.Message{{Role: "user", Content: text}}, nil
	})
}

// SendResults gives the model the results of the tool calls of the last reply
// and asks it to continue. results maps each call's ID to the text of its result,
// and has to answer every call of the last message of History and no other: a
// missing or unknown ID is ErrInvalidRequest naming how many, never which text. A
// tool that failed sends its failure as its result. The messages are added in the
// order of the calls, then the reply, which is returned as Send returns it. A
// turn that fails leaves History as it was. Each turn adds one Record, named
// "chat.sendresults".
//
// A call is answered once: when History already holds the results of the last
// reply's calls, there is nothing to answer and SendResults is ErrInvalidRequest.
func (s *Session) SendResults(ctx context.Context, results map[string]string) (schemaflux.Message, error) {
	return s.turn(ctx, sendResultsOperation, func(history []schemaflux.Message) ([]schemaflux.Message, error) {
		if len(history) == 0 || history[len(history)-1].Role != "assistant" || len(history[len(history)-1].ToolCalls) == 0 {
			return nil, types.Newf(types.KindInvalidRequest, sendResultsOperation,
				"the last message of the history is not a request for tools that are waiting for results")
		}
		calls := history[len(history)-1].ToolCalls
		asked := make(map[string]struct{}, len(calls))
		answers := make([]schemaflux.Message, 0, len(calls))
		missing := 0
		for _, call := range calls {
			asked[call.ID] = struct{}{}
			text, ok := results[call.ID]
			if !ok {
				missing++
				continue
			}
			answers = append(answers, schemaflux.Message{Role: "tool", ToolCallID: call.ID, Content: text})
		}
		unknown := 0
		for id := range results {
			if _, ok := asked[id]; !ok {
				unknown++
			}
		}
		if missing > 0 || unknown > 0 {
			return nil, types.Newf(types.KindInvalidRequest, sendResultsOperation,
				"the results do not match the %d tool calls waiting: %d have no result and %d results answer no call", len(calls), missing, unknown)
		}
		return answers, nil
	})
}

// turn is what Send and SendResults share. It makes the steps of a turn in the
// order the specification gives them: the context, the guard against a second
// turn, the messages the turn adds, the settings, then the history with the new
// messages checked and sent, the reply checked, and only now the history
// assigned. A turn that fails before the last step has changed nothing.
func (s *Session) turn(ctx context.Context, operation string, additions func(history []schemaflux.Message) ([]schemaflux.Message, error)) (schemaflux.Message, error) {
	var none schemaflux.Message
	call, err := bind.Begin(ctx, operation)
	if err != nil {
		return none, err
	}
	// One atomic flag and no lock held while the request is in flight: a second
	// call is told at once, it does not wait for the first and does not
	// interleave with it.
	if !s.busy.CompareAndSwap(false, true) {
		return none, call.Refuse(types.KindInvalidRequest,
			"a turn is already running on this session, which is for one conversation and not for concurrent use")
	}
	defer s.busy.Store(false)

	add, err := additions(s.History)
	if err != nil {
		return none, call.End(nil, err)
	}
	// The one check of the seven common settings (a negative ceiling, a tier or a
	// mode that is not one of the constants) is the root's, and so is the one
	// conversion of the root's types to the internal ones.
	common, err := bind.CommonOf(schemaflux.Options{
		Model: s.Model, Tier: s.Tier, Effort: s.Effort, Mode: s.Mode, MaxOutputTokens: s.MaxOutputTokens, WebSearch: s.WebSearch,
	})
	if err != nil {
		return none, call.End(nil, err)
	}
	sent := bind.RequestToInternal(schemaflux.Request{Tools: s.Tools, Messages: s.History})
	added := bind.RequestToInternal(schemaflux.Request{Messages: add})

	result, err := ops.RunTurn(call.Context(), operation, ops.SessionSettings{
		Model: common.Model, Tier: common.Tier, Effort: common.Effort, Mode: common.Mode,
		MaxOutputTokens: common.MaxOutputTokens, WebSearch: common.WebSearch,
		Instructions: s.Instructions, Tools: sent.Tools,
	}, sent.Messages, added.Messages)
	if err != nil {
		return none, call.End(&result.Meta, err)
	}
	reply, err := replyOf(result.Value)
	if err != nil {
		return none, call.End(&result.Meta, err)
	}

	// A new slice, so that the one the caller gave, or took earlier, is never
	// written to. The reply is kept as it is and a copy is returned: what the
	// caller does to its Message does not reach History.
	next := make([]schemaflux.Message, 0, len(s.History)+len(add)+1)
	next = append(append(append(next, s.History...), add...), reply)
	s.History = next
	return copyOf(reply), call.End(&result.Meta, nil)
}

// replyOf is the root's Message for the assistant message a turn produced. It
// goes through the conversion of a request, which is the one the root has for
// messages.
func replyOf(m llm.Message) (schemaflux.Message, error) {
	converted, err := bind.RequestFromInternal[schemaflux.Request](llm.CompletionRequest{Messages: []llm.Message{m}})
	if err != nil {
		return schemaflux.Message{}, err
	}
	if len(converted.Messages) != 1 {
		return schemaflux.Message{}, &schemaflux.Error{Kind: schemaflux.ErrInternal, Message: "the reply did not convert to one message"}
	}
	return converted.Messages[0], nil
}

// copyOf is m with its call list and the bytes of each call's arguments copied,
// so that it shares nothing with the message it came from.
func copyOf(m schemaflux.Message) schemaflux.Message {
	if m.ToolCalls == nil {
		return m
	}
	calls := make([]schemaflux.ToolCall, len(m.ToolCalls))
	for i, c := range m.ToolCalls {
		c.Arguments = bytes.Clone(c.Arguments)
		calls[i] = c
	}
	m.ToolCalls = calls
	return m
}
