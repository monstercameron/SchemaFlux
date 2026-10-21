package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// The roles a message of a conversation can have. The system prompt is the
// session's Instructions and is not a message, and a message with any other role
// is refused before anything is sent: a role the wire does not define reaches the
// API as it was spelled and is answered with an error that names none of this
// library's concepts.
const (
	sessionRoleUser      = "user"
	sessionRoleAssistant = "assistant"
	sessionRoleTool      = "tool"
)

// The operation names of a session's turns, which are what its record, its
// observers and its errors call them.
const (
	sessionSendOperation        = "chat.send"
	sessionSendResultsOperation = "chat.sendresults"
)

// ErrSessionInvariant is matched by the error a conversation returns for a
// history that no real conversation produces: a message in an order the model
// never produced it, or a tool result that does not answer a call of the message
// before it. The error is an invalid request as well, and says which message,
// never what it says.
var ErrSessionInvariant = errors.New("session transcript invariant violated")

// transcriptProblem is the refusal of a history. It names a position in the
// conversation and says what is wrong with it in words of this library's own: no
// text of a message, no tool name, no call id and no argument ever appears in it,
// because the history is the caller's data and the error will be logged.
func transcriptProblem(position int, format string, args ...any) error {
	return &types.OperationError{
		Kind:    types.KindInvalidRequest,
		Message: fmt.Sprintf("message %d %s", position, fmt.Sprintf(format, args...)),
		Cause:   ErrSessionInvariant,
	}
}

// withOperation is err with the operation that refused it as its Op, when err is
// one of this file's own refusals and does not have one yet.
func withOperation(err error, operation string) error {
	var refusal *types.OperationError
	if errors.As(err, &refusal) && refusal.Op == "" {
		stamped := *refusal
		stamped.Op = operation
		return &stamped
	}
	return err
}

// CheckTranscript refuses a conversation that no model could have had, before it
// is sent. It is the rules a history is held to, and they are checked over the
// whole of it every time, so a history a caller saved, edited or cut to fit the
// context window is checked as much as one the library kept:
//
//   - a message is a user message, an assistant message or a tool message;
//   - a user message says something and follows nothing, an assistant message
//     that asked for no tools, or a tool message;
//   - an assistant message follows a user message or a tool message, says
//     something or asks for tools, and every call it asks for has an id and a
//     name, arguments that are a JSON object, and an id no earlier call used;
//   - a tool message answers a call of the assistant message before it, once;
//   - no call is left unanswered before the next message.
//
// A history may end with calls nobody has answered yet: that is a conversation
// waiting for its tool results.
func CheckTranscript(messages []llm.Message) error {
	var (
		previous   string
		blockAt    int
		open       map[string]bool // the calls of the last assistant message: answered or not
		unanswered int
		used       = make(map[string]struct{})
	)
	for i, message := range messages {
		position := i + 1
		switch message.Role {
		case sessionRoleUser:
			switch {
			case len(message.ToolCalls) > 0 || message.ToolCallID != "":
				return transcriptProblem(position, "is a user message that carries tool data")
			case strings.TrimSpace(message.Content) == "":
				return transcriptProblem(position, "is a user message with no text")
			case previous == sessionRoleUser:
				return transcriptProblem(position, "is a user message after a user message, which nothing answered")
			case unanswered > 0:
				return transcriptProblem(position, "comes while %d tool calls of message %d are not answered", unanswered, blockAt)
			}
			open = nil

		case sessionRoleAssistant:
			switch {
			case message.ToolCallID != "":
				return transcriptProblem(position, "is an assistant message that names a call it answers")
			case previous == "":
				return transcriptProblem(position, "is an assistant message and no user message came before it")
			case previous == sessionRoleAssistant:
				return transcriptProblem(position, "is an assistant message after an assistant message")
			case unanswered > 0:
				return transcriptProblem(position, "comes while %d tool calls of message %d are not answered", unanswered, blockAt)
			case strings.TrimSpace(message.Content) == "" && len(message.ToolCalls) == 0:
				return transcriptProblem(position, "is an assistant message that says nothing and asks for nothing")
			}
			open = make(map[string]bool, len(message.ToolCalls))
			for j, call := range message.ToolCalls {
				switch {
				case call.ID == "" || call.Name == "":
					return transcriptProblem(position, "asks for tool call %d, which has no id or no name", j+1)
				case !jsonObject(call.Arguments):
					return transcriptProblem(position, "asks for tool call %d, whose arguments are not a JSON object", j+1)
				}
				if _, repeated := used[call.ID]; repeated {
					return transcriptProblem(position, "asks for tool call %d, whose id an earlier call already used", j+1)
				}
				used[call.ID] = struct{}{}
				open[call.ID] = false
			}
			blockAt, unanswered = position, len(message.ToolCalls)

		case sessionRoleTool:
			switch {
			case len(message.ToolCalls) > 0:
				return transcriptProblem(position, "is a tool message that asks for tools")
			case message.ToolCallID == "":
				return transcriptProblem(position, "is a tool message that names no call it answers")
			case len(open) == 0 || (previous != sessionRoleAssistant && previous != sessionRoleTool):
				return transcriptProblem(position, "is a tool message and no assistant message before it asked for tools")
			}
			answered, asked := open[message.ToolCallID]
			switch {
			case !asked:
				return transcriptProblem(position, "answers a call that the assistant message before it did not make")
			case answered:
				return transcriptProblem(position, "answers a call that was already answered")
			}
			open[message.ToolCallID] = true
			unanswered--

		default:
			return transcriptProblem(position, "has a role that is not user, assistant or tool")
		}
		previous = message.Role
	}
	return nil
}

// jsonObject reports whether arguments are one JSON object.
func jsonObject(arguments json.RawMessage) bool {
	trimmed := bytes.TrimSpace(arguments)
	return len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(trimmed)
}

// SessionSettings are what a conversation is run with besides its history.
type SessionSettings struct {
	// Model pins the exact model for every turn. Empty means the client's model
	// if it names one, else the model the tier maps to.
	Model string

	// Tier is the class of model the turn runs on and sets the default ceiling on
	// a reply. The zero value is Smart.
	Tier types.Speed

	// Effort is how much the model reasons before it replies. The zero value
	// leaves it to the client and the provider; a value is a requirement, and a
	// route that cannot take it refuses.
	Effort types.Effort

	// Mode sets the sampling temperature. The zero value is ModeTransform.
	Mode types.Mode

	// MaxOutputTokens is the ceiling on one reply. Zero means the tier's.
	MaxOutputTokens int

	// WebSearch declares the provider's web-search tool beside Tools.
	WebSearch bool

	// Instructions is the system prompt, sent as written.
	Instructions string

	// Tools are the functions the model may ask for. Their names are not empty and
	// not repeated.
	Tools []llm.Tool
}

// options checks the settings and returns the options a turn is called with.
// Defaults are the same ones a decision uses: a zero tier is Smart and a zero
// mode is Transform.
func (set SessionSettings) options(operation string) (types.OpOptions, error) {
	tier := set.Tier
	if tier == types.TierUnset {
		tier = types.Smart
	}
	switch tier {
	case types.Smart, types.Fast, types.Quick:
	default:
		return types.OpOptions{}, types.Newf(types.KindConfiguration, operation, "Tier is not one of the three tiers")
	}
	mode := set.Mode
	if mode == types.ModeUnset {
		mode = types.TransformMode
	}
	switch mode {
	case types.Strict, types.TransformMode, types.Creative:
	default:
		return types.OpOptions{}, types.Newf(types.KindConfiguration, operation, "Mode is not one of the three modes")
	}
	if set.MaxOutputTokens < 0 {
		return types.OpOptions{}, types.Newf(types.KindConfiguration, operation, "MaxOutputTokens cannot be negative")
	}
	names := make(map[string]struct{}, len(set.Tools))
	for i, tool := range set.Tools {
		if strings.TrimSpace(tool.Name) == "" {
			return types.OpOptions{}, types.Newf(types.KindConfiguration, operation, "tool %d has no name", i+1)
		}
		if _, repeated := names[tool.Name]; repeated {
			return types.OpOptions{}, types.Newf(types.KindConfiguration, operation, "tool %d has a name an earlier tool has", i+1)
		}
		names[tool.Name] = struct{}{}
	}
	return types.OpOptions{
		Model:           set.Model,
		Intelligence:    tier,
		Mode:            mode,
		Effort:          set.Effort,
		WebSearch:       set.WebSearch,
		MaxOutputTokens: set.MaxOutputTokens,
	}, nil
}

// RunTurn is one turn of a conversation: it checks the history with the new
// messages added to it, asks the model, checks the reply and returns it with the
// record of the call. history is read and never written, and add is what the
// turn would append before the reply (the user's message, or the results of the
// tools), so that a turn that fails has changed nothing and the caller appends
// only on success.
//
// The turn is one operation named operation, run under a record as every call of
// an operation is: its observers see one operation with its request as a child,
// the record says what the turn used and who answered, and a data policy that
// requires more of an answer than a model's own text refuses it. The reply is
// the model's text or its tool calls or both, as an assistant message, copied so
// that nothing the caller does to it reaches the history. An error comes with an
// empty reply and the record of what was done up to it.
func RunTurn(ctx context.Context, operation string, set SessionSettings, history, add []llm.Message) (types.Result[llm.Message], error) {
	if ctx == nil {
		return types.Result[llm.Message]{}, types.Newf(types.KindInvalidRequest, operation, "the context is nil")
	}
	var check *types.Check
	result, err := Recorded(ctx, operation, types.ContractPromptOnly, func(ctx context.Context) (llm.Message, error) {
		reply, replyCheck, err := runTurn(ctx, operation, set, history, add)
		check = replyCheck
		return reply, err
	})
	if check != nil {
		result.Meta.Checks = append(result.Meta.Checks, *check)
	}
	return result, err
}

func runTurn(ctx context.Context, operation string, set SessionSettings, history, add []llm.Message) (llm.Message, *types.Check, error) {
	opts, err := set.options(operation)
	if err != nil {
		return llm.Message{}, nil, err
	}
	conversation := make([]llm.Message, 0, len(history)+len(add))
	conversation = append(append(conversation, history...), add...)
	if err := CheckTranscript(conversation); err != nil {
		return llm.Message{}, nil, withOperation(err, operation)
	}

	resp, err := CallTurn(ctx, Turn{Instructions: set.Instructions, Tools: set.Tools, Messages: conversation}, opts)
	if err != nil {
		return llm.Message{}, nil, err
	}
	return checkTurnReply(operation, resp, set.Tools, conversation)
}

// checkTurnReply makes the checks of a reply that the call path does not make for
// every provider, and returns it as the assistant message to keep. A reply that
// fails them is an invalid answer and is not kept, and is the same for every
// provider: the providers of this library make most of these checks themselves,
// and a provider a caller wrote may not.
//
//   - every call names a tool the turn declared;
//   - every call has an id, and no two calls of the reply share one, and none is an
//     id an earlier call of the conversation used, which the next request would be
//     refused for;
//   - every call's arguments are a JSON object;
//   - the reply says something or asks for something.
//
// The check that comes back is the one the turn's record keeps: that the calls
// were usable, or why they were not, in counts and never in the model's words.
func checkTurnReply(operation string, resp llm.CompletionResponse, tools []llm.Tool, conversation []llm.Message) (llm.Message, *types.Check, error) {
	if len(resp.ToolCalls) == 0 {
		if strings.TrimSpace(resp.Content) == "" {
			return llm.Message{}, &types.Check{Name: "reply", Passed: false, Detail: "the reply has no text and no tool calls"},
				types.Newf(types.KindMalformedOutput, operation, "the model's reply has no text and asks for no tools")
		}
		return llm.Message{Role: sessionRoleAssistant, Content: resp.Content}, nil, nil
	}

	declared := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		declared[tool.Name] = struct{}{}
	}
	earlier := make(map[string]struct{})
	for _, message := range conversation {
		for _, call := range message.ToolCalls {
			earlier[call.ID] = struct{}{}
		}
	}
	var undeclared, noID, repeated, notObject int
	ids := make(map[string]struct{}, len(resp.ToolCalls))
	for _, call := range resp.ToolCalls {
		if _, ok := declared[call.Name]; !ok {
			undeclared++
		}
		_, seenEarlier := earlier[call.ID]
		_, seenNow := ids[call.ID]
		switch {
		case call.ID == "":
			noID++
		case seenNow || seenEarlier:
			repeated++
		}
		ids[call.ID] = struct{}{}
		if !jsonObject(call.Arguments) {
			notObject++
		}
	}
	if undeclared+noID+repeated+notObject > 0 {
		var reasons []string
		for _, reason := range []struct {
			count int
			text  string
		}{
			{undeclared, "name a tool that was not declared"},
			{noID, "have no id"},
			{repeated, "have an id that was already used"},
			{notObject, "have arguments that are not a JSON object"},
		} {
			if reason.count > 0 {
				reasons = append(reasons, fmt.Sprintf("%d %s", reason.count, reason.text))
			}
		}
		detail := fmt.Sprintf("%d calls: %s", len(resp.ToolCalls), strings.Join(reasons, ", "))
		return llm.Message{}, &types.Check{Name: "tool calls", Passed: false, Detail: detail},
			types.Newf(types.KindMalformedOutput, operation, "the model's reply asks for tools that cannot be used: %s", detail)
	}

	calls := make([]llm.ToolCall, len(resp.ToolCalls))
	for i, call := range resp.ToolCalls {
		calls[i] = llm.ToolCall{ID: call.ID, Name: call.Name, Arguments: bytes.Clone(call.Arguments)}
	}
	detail := fmt.Sprintf("%d calls, all declared, arguments are JSON objects", len(calls))
	return llm.Message{Role: sessionRoleAssistant, Content: resp.Content, ToolCalls: calls},
		&types.Check{Name: "tool calls", Passed: true, Detail: detail}, nil
}

// A Session is a conversation with a model that may ask for tools. The zero
// value is ready to use: set Instructions and Tools if the conversation has
// them, then Send. A Session holds the history, so each turn sends everything
// said so far again; a long conversation costs more with every turn, and a
// caller who needs it shorter edits History between turns. A conversation that
// outgrows the model's context window, when the route declares one, is refused
// before it is sent with the context-too-large kind.
//
// The library never runs a tool. The model asks, the caller runs the tool, and
// SendResults gives the results back.
//
// A Session is for one conversation and is not safe for concurrent use: a method
// called while another is running returns an invalid-request error at once and
// does not wait. Do not copy a Session after its first use. The client a turn runs
// on is the one on the turn's context, or the default.
type Session struct {
	// Model pins the exact model for every turn. Empty means the client's model
	// if it names one, else the model Tier maps to.
	Model string

	// Tier is the class of model a turn runs on, and sets the default ceiling on a
	// reply. The zero value is Smart.
	Tier types.Speed

	// Effort is how much the model reasons before it replies. The zero value
	// leaves it to the client and the provider.
	Effort types.Effort

	// Mode sets the sampling temperature. The zero value is ModeTransform.
	Mode types.Mode

	// MaxOutputTokens is the ceiling on one reply, in tokens. Zero means the
	// tier's.
	MaxOutputTokens int

	// WebSearch declares the provider's web-search tool beside Tools.
	WebSearch bool

	// Instructions is the system prompt: what the conversation is for and how the
	// model should behave. It is the program's own text, sent as written; never put
	// a user's or a document's text in it, that belongs in a message.
	Instructions string

	// Tools are the functions the model may ask for. Names are not empty and not
	// repeated.
	Tools []llm.Tool

	// History is the conversation so far, oldest first: user messages, the model's
	// replies, and the results of its tool calls. Send and SendResults append to
	// it, and they check it, whole, before every request, so one a caller saved,
	// edited or cut is held to the same rules as one the session built (see
	// CheckTranscript). A history that breaks them is an invalid request. The
	// session never writes into the slice it was given: a turn that succeeds
	// assigns a new one, and one that fails leaves this one as it was.
	History []llm.Message

	busy  atomic.Bool
	mu    sync.Mutex // guards total, and is never held while a request is made
	total types.Meta
}

// NewSession returns a Session. The zero value is as ready, and the function is
// kept for the callers that wrote it.
func NewSession() *Session {
	return &Session{}
}

func (s *Session) settings() SessionSettings {
	return SessionSettings{
		Model: s.Model, Tier: s.Tier, Effort: s.Effort, Mode: s.Mode, MaxOutputTokens: s.MaxOutputTokens,
		WebSearch: s.WebSearch, Instructions: s.Instructions, Tools: s.Tools,
	}
}

// Send adds text as a user message, asks the model and returns its reply with
// the record of the turn. The reply's Value is the assistant message that was
// added to History: when it carries tool calls the model is waiting for their
// results, so run them and call SendResults. It is a copy, so changing it does not
// change the history.
//
// Text with nothing in it, a history that is not valid, and a history whose last
// message asks for tools that have not been answered are invalid requests. A turn
// that fails, for any reason, and a turn that is cancelled leave History exactly as
// it was, so the same call can be made again. The record of a failed turn is still
// returned, since what it cost is what a caller reads.
func (s *Session) Send(ctx context.Context, text string) (types.Result[llm.Message], error) {
	return s.turn(ctx, sessionSendOperation, func(history []llm.Message) ([]llm.Message, error) {
		if strings.TrimSpace(text) == "" {
			return nil, types.Newf(types.KindInvalidRequest, sessionSendOperation, "the text is empty")
		}
		return []llm.Message{{Role: sessionRoleUser, Content: text}}, nil
	})
}

// SendResults gives the model the results of the tool calls of the last reply and
// asks it to continue. results maps each call's id to the text of its result, and
// has to answer every call of the last message of History and no other: a missing
// result, or an id that is not one of the calls, is an invalid request that says
// how many, and never which. A tool that failed sends its failure as its result.
// The results are added in the order of the calls, then the reply, which is
// returned as Send returns it. A turn that fails leaves History as it was.
//
// A call is answered once: when History already holds the results of the last
// reply's calls, SendResults has nothing to answer and is an invalid request.
func (s *Session) SendResults(ctx context.Context, results map[string]string) (types.Result[llm.Message], error) {
	return s.turn(ctx, sessionSendResultsOperation, func(history []llm.Message) ([]llm.Message, error) {
		calls := pendingCalls(history)
		if len(calls) == 0 {
			return nil, types.Newf(types.KindInvalidRequest, sessionSendResultsOperation,
				"the last message of the history is not a request for tools that are waiting for results")
		}
		answers := make([]llm.Message, 0, len(calls))
		missing := 0
		asked := make(map[string]struct{}, len(calls))
		for _, call := range calls {
			asked[call.ID] = struct{}{}
			text, ok := results[call.ID]
			if !ok {
				missing++
				continue
			}
			answers = append(answers, llm.Message{Role: sessionRoleTool, ToolCallID: call.ID, Content: text})
		}
		unknown := 0
		for id := range results {
			if _, ok := asked[id]; !ok {
				unknown++
			}
		}
		if missing > 0 || unknown > 0 {
			return nil, types.Newf(types.KindInvalidRequest, sessionSendResultsOperation,
				"the results do not match the %d tool calls waiting: %d have no result and %d results answer no call", len(calls), missing, unknown)
		}
		return answers, nil
	})
}

// pendingCalls returns the calls of the last message of history when it is an
// assistant message that asked for tools and nothing has answered them.
func pendingCalls(history []llm.Message) []llm.ToolCall {
	if len(history) == 0 {
		return nil
	}
	last := history[len(history)-1]
	if last.Role != sessionRoleAssistant {
		return nil
	}
	return last.ToolCalls
}

// turn is the part Send and SendResults share: the guard, the messages to add,
// the turn itself, and the history assigned only after it succeeded.
func (s *Session) turn(ctx context.Context, operation string, messages func(history []llm.Message) ([]llm.Message, error)) (types.Result[llm.Message], error) {
	if ctx == nil {
		return types.Result[llm.Message]{}, types.Newf(types.KindInvalidRequest, operation, "the context is nil")
	}
	// One atomic flag, and no lock held while the request is in flight: a second
	// call is told at once, it does not wait for the first and it does not
	// interleave with it.
	if !s.busy.CompareAndSwap(false, true) {
		return types.Result[llm.Message]{}, types.Newf(types.KindInvalidRequest, operation,
			"a turn is already running on this session, which is for one conversation and not for concurrent use")
	}
	defer s.busy.Store(false)

	add, err := messages(s.History)
	if err != nil {
		return types.Result[llm.Message]{}, err
	}
	result, err := RunTurn(ctx, operation, s.settings(), s.History, add)
	s.note(result.Meta)
	if err != nil {
		return types.Result[llm.Message]{Meta: result.Meta}, err
	}
	// A new slice: the one the caller gave, or took earlier, is never written to.
	next := make([]llm.Message, 0, len(s.History)+len(add)+1)
	next = append(append(append(next, s.History...), add...), result.Value)
	s.History = next

	// What the caller gets is not the history's own message.
	reply := result.Value
	reply.ToolCalls = slices.Clone(reply.ToolCalls)
	for i := range reply.ToolCalls {
		reply.ToolCalls[i].Arguments = bytes.Clone(reply.ToolCalls[i].Arguments)
	}
	result.Value = reply
	return result, nil
}

// note adds a turn's record to the running total.
func (s *Session) note(meta types.Meta) {
	if meta.Attempts == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.total = sumMetas("chat", s.total, meta)
}

// Total is what every turn of the session has used so far, summed: the tokens,
// the cost where the requests were priced, the attempts and retries, and the time
// spent in them. A turn that failed after a request was sent is in it, since the
// request was paid for. The model and provider are the turns' when they all
// agree and empty when they do not, and the request id is never set: it is one
// request's.
func (s *Session) Total() types.Meta {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total
}
