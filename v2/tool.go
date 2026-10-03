package schemaflux

import (
	"encoding/json"

	"github.com/monstercameron/schemaflux/internal/llm"
)

// A Tool declares one function a model may ask to have called instead of
// answering in words. It is a declaration and never an invocation: the library
// does not run tools. The model asks, the caller's code runs the tool, and the
// result goes back in a Message.
type Tool struct {
	// Name identifies the tool. It is what a ToolCall's Name has to match, and
	// it is unique among the tools of one request. Empty is not a tool.
	Name string

	// Description tells the model when to use the tool. Empty means none given;
	// a model chooses worse without one.
	Description string

	// Parameters is the JSON Schema of the tool's arguments, an object schema.
	// Nil means the tool takes no arguments. The map is read-only and may be
	// shared between requests: a Provider or middleware that needs a changed
	// schema copies it first.
	Parameters map[string]any
}

// A ToolCall is a model's request to have one of the declared tools run. The
// library has checked that Name is a tool the request declared, that ID is not
// empty and not repeated within the answer, and that Arguments is a JSON object;
// it has not checked Arguments against the tool's Parameters unless the provider
// enforces the schema. The arguments are model output: as untrusted as any, never
// written to an error or a log line.
type ToolCall struct {
	// ID identifies the call so that its result can be matched to it. Never
	// empty in a call the library returns.
	ID string

	// Name is the tool the model asked for.
	Name string

	// Arguments is the JSON object the model wrote for the tool, as it wrote
	// it. "{}" for a tool with no parameters. The bytes are read-only and may be
	// shared with the answer they came from.
	Arguments json.RawMessage
}

// A Message is one turn of a conversation.
type Message struct {
	// Role is "user", "assistant" or "tool". The system prompt is Request.System
	// and is not a message. Any other word is refused with ErrInvalidRequest
	// before anything is sent.
	Role string

	// Content is the message's text. For an assistant message that asks for
	// tools it may be empty. For a tool message it is the tool's result as text;
	// a tool that failed sends its failure as the text.
	Content string

	// ToolCalls are the calls an assistant message asked for. Nil for any other
	// role and for an assistant message that answered in words. They are kept in
	// the history because the provider needs them back beside their results.
	ToolCalls []ToolCall

	// ToolCallID is, for a tool message, the ID of the call it answers. Empty for
	// any other role.
	ToolCallID string
}

// The conversions below are the only place a Tool, a ToolCall and a Message
// become their internal twins and back; the request and response conversions of
// provider.go use them, and internal/bind hands them to the other packages. A
// slice is copied one level, so that a provider that edits what it was given
// edits its own copy of the list; a tool's parameters and a call's argument
// bytes are shared, read-only, as the types document. A nil slice stays nil and
// an empty one stays empty, so that nothing is added to a request that carried
// nothing.

// toolsToInternal is the internal tool list for a list of Tools.
func toolsToInternal(tools []Tool) []llm.Tool {
	if tools == nil {
		return nil
	}
	out := make([]llm.Tool, len(tools))
	for i, t := range tools {
		out[i] = llm.Tool{Name: t.Name, Description: t.Description, Parameters: t.Parameters}
	}
	return out
}

// toolsFromInternal is the list of Tools for an internal tool list.
func toolsFromInternal(tools []llm.Tool) []Tool {
	if tools == nil {
		return nil
	}
	out := make([]Tool, len(tools))
	for i, t := range tools {
		out[i] = Tool{Name: t.Name, Description: t.Description, Parameters: t.Parameters}
	}
	return out
}

// callsToInternal is the internal call list for a list of ToolCalls.
func callsToInternal(calls []ToolCall) []llm.ToolCall {
	if calls == nil {
		return nil
	}
	out := make([]llm.ToolCall, len(calls))
	for i, c := range calls {
		out[i] = llm.ToolCall{ID: c.ID, Name: c.Name, Arguments: c.Arguments}
	}
	return out
}

// callsFromInternal is the list of ToolCalls for an internal call list.
func callsFromInternal(calls []llm.ToolCall) []ToolCall {
	if calls == nil {
		return nil
	}
	out := make([]ToolCall, len(calls))
	for i, c := range calls {
		out[i] = ToolCall{ID: c.ID, Name: c.Name, Arguments: c.Arguments}
	}
	return out
}

// messagesToInternal is the internal transcript for a list of Messages.
func messagesToInternal(messages []Message) []llm.Message {
	if messages == nil {
		return nil
	}
	out := make([]llm.Message, len(messages))
	for i, m := range messages {
		out[i] = llm.Message{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID, ToolCalls: callsToInternal(m.ToolCalls)}
	}
	return out
}

// messagesFromInternal is the list of Messages for an internal transcript.
func messagesFromInternal(messages []llm.Message) []Message {
	if messages == nil {
		return nil
	}
	out := make([]Message, len(messages))
	for i, m := range messages {
		out[i] = Message{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID, ToolCalls: callsFromInternal(m.ToolCalls)}
	}
	return out
}
