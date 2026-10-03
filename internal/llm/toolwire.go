package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/monstercameron/schemaflux/internal/types"
)

// This file holds what the providers that carry tools share: how a tool is
// declared, how a conversation that includes the model's earlier tool calls and
// their results is checked before it is written to a wire, and how the tool calls
// of a reply are checked before they reach a caller. It also holds the Responses
// API's way of writing a conversation; the chat dialect's is in
// chatcompletions.go.

// ErrMalformedToolCall reports a reply whose tool calls cannot be answered or
// run: a call with no id, an id two calls share, or arguments that are not a
// JSON object. The model's own text is never in the error. It is deterministic
// for the same reply and, like ErrUnrequestedTool, is classified as a malformed
// output, which the call path does not send again unchanged.
var ErrMalformedToolCall = errors.New("model returned a tool call that cannot be used")

// transcriptRoles are the roles a turn of Messages may have. They are the words
// both wire dialects define for the turns this library writes.
var transcriptRoles = map[string]bool{
	"system":    true,
	"developer": true,
	"user":      true,
	"assistant": true,
	"tool":      true,
}

// invalidRequest is the error for a request that cannot be written to the wire.
// The message is the library's own sentence: it may name a position or a tool the
// caller declared, and never a message's text or a call's arguments.
func invalidRequest(provider, model, message string) error {
	return &types.OperationError{
		Kind:     types.KindInvalidRequest,
		Op:       "llm.complete",
		Provider: provider,
		Model:    model,
		Message:  message,
	}
}

// checkTranscript refuses a conversation that cannot be written to a wire before
// anything is sent: a turn with a role that is none of the five the dialects
// define (it would reach the API as it was spelled), a tool result that names no
// call, which the API cannot line up with the call that asked for it, and a call
// of the model's in the history that has no id or no name, whose arguments are not
// a JSON object, that shares its id with another call, or that is on a turn that
// is not the assistant's. A message's position is named, never its text.
func checkTranscript(req CompletionRequest, provider string) error {
	called := make(map[string]bool)
	for i, message := range req.Messages {
		if !transcriptRoles[message.Role] {
			return invalidRequest(provider, req.Model,
				fmt.Sprintf("message %d has a role this API does not define (it takes system, developer, user, assistant and tool)", i+1))
		}
		if message.Role == "tool" && message.ToolCallID == "" {
			return invalidRequest(provider, req.Model, fmt.Sprintf("message %d is a tool result and names no call it answers", i+1))
		}
		if len(message.ToolCalls) > 0 && message.Role != "assistant" {
			return invalidRequest(provider, req.Model, fmt.Sprintf("message %d carries tool calls and is not an assistant turn", i+1))
		}
		for j, call := range message.ToolCalls {
			switch {
			case call.ID == "" || call.Name == "":
				return invalidRequest(provider, req.Model, fmt.Sprintf("tool call %d of message %d has no id or no name", j+1, i+1))
			case !argumentsAreAnObject(call.Arguments):
				return invalidRequest(provider, req.Model, fmt.Sprintf("the arguments of tool call %d of message %d are not a JSON object", j+1, i+1))
			case called[call.ID]:
				return invalidRequest(provider, req.Model, fmt.Sprintf("tool call %d of message %d has an id an earlier call already used", j+1, i+1))
			}
			called[call.ID] = true
		}
	}
	return nil
}

// checkTools refuses a declaration that cannot be sent: a tool with no name, and
// two tools with one name, which the model could not tell apart. A tool's name is
// the caller's own word and is quoted.
//
// strictOnly is set for OpenAI's own endpoint, where a function is strict unless
// it says otherwise and a schema that strict mode does not accept is answered with
// a 400 that names neither the tool nor the field. There it is refused before the
// request is sent, and the error names the tool and the place in its schema. It is
// the same rule newResponsesHTTPRequest applies to a response schema, and on the
// same ground: the rules checked are the ones that API was observed to enforce,
// so another server behind the same dialect is not held to them.
func checkTools(req CompletionRequest, provider string, strictOnly bool) error {
	seen := make(map[string]bool, len(req.Tools))
	for i, tool := range req.Tools {
		if tool.Name == "" {
			return invalidRequest(provider, req.Model, fmt.Sprintf("tool %d has no name", i+1))
		}
		if seen[tool.Name] {
			return invalidRequest(provider, req.Model, fmt.Sprintf("two tools are named %q, which the model could not tell apart", tool.Name))
		}
		seen[tool.Name] = true
		if !strictOnly {
			continue
		}
		if problem := StrictSchemaProblem(toolParameters(tool)); problem != "" {
			return invalidRequest(provider, req.Model,
				fmt.Sprintf("the parameters of tool %q cannot be sent as strict function parameters: %s", tool.Name, problem))
		}
	}
	return nil
}

// toolParameters is the schema a tool is declared with. A tool that takes no
// arguments is the closed empty object, which is the schema strict mode accepts
// for "no arguments": an object with no properties, nothing required, and no
// others allowed.
func toolParameters(tool Tool) map[string]any {
	if len(tool.Parameters) == 0 {
		return map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"required":             []any{},
			"additionalProperties": false,
		}
	}
	return tool.Parameters
}

// toolIsStrict reports whether a tool's schema is one strict mode accepts, which
// is the flag the tool is declared with.
func toolIsStrict(parameters map[string]any) bool {
	return StrictSchemaProblem(parameters) == ""
}

// argumentsAreAnObject reports whether a call's arguments are one JSON object.
// The Responses API sends them as text, and a model can send text that is not
// JSON at all, a bare value, or an object cut off before its end.
func argumentsAreAnObject(arguments json.RawMessage) bool {
	trimmed := bytes.TrimSpace(arguments)
	return len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(trimmed)
}

// checkToolNames refuses any call naming a tool the request never offered. It
// compares by Name only and never reads a call's arguments. The error does not
// carry the model's name for the tool, which is as long and as odd as the model
// chose: it says how many calls were refused and which tools were offered, and
// those names are the caller's own. A call naming a tool that was not offered is
// ErrUnrequestedTool: the caller's Tools list is the only source of truth for what
// may be called. It is classified as a malformed output.
func checkToolNames(provider string, calls []ToolCall, offeredTools []Tool) error {
	offered := make(map[string]struct{}, len(offeredTools))
	for _, tool := range offeredTools {
		offered[tool.Name] = struct{}{}
	}
	refused := 0
	for _, call := range calls {
		if _, ok := offered[call.Name]; !ok {
			refused++
		}
	}
	if refused == 0 {
		return nil
	}
	names := make([]string, 0, len(offered))
	for name := range offered {
		names = append(names, fmt.Sprintf("%q", name))
	}
	sort.Strings(names)
	list := "none"
	if len(names) > 0 {
		list = strings.Join(names, ", ")
	}
	return types.Classified(types.KindMalformedOutput,
		fmt.Errorf("%s: %w: %d of %d tool calls named a tool that was not offered (offered: %s)",
			provider, ErrUnrequestedTool, refused, len(calls), list))
}

// checkToolCalls refuses a reply whose tool calls cannot be used, and says how
// many of them and why, never what the model wrote. The names are checked first
// (checkToolNames). A call that names a tool that was offered, but has no id,
// repeats an id an earlier call of the same reply has, or whose arguments are not
// a JSON object, is ErrMalformedToolCall: its result could not be sent back
// against the id, or its arguments would reach the caller's own decoder as text
// that is not an object. Both are classified as a malformed output.
func checkToolCalls(provider string, calls []ToolCall, offeredTools []Tool) error {
	if err := checkToolNames(provider, calls, offeredTools); err != nil {
		return err
	}

	var noID, repeated, notObject int
	ids := make(map[string]struct{}, len(calls))
	for _, call := range calls {
		switch _, seen := ids[call.ID]; {
		case call.ID == "":
			noID++
		case seen:
			repeated++
		}
		ids[call.ID] = struct{}{}
		if !argumentsAreAnObject(call.Arguments) {
			notObject++
		}
	}
	if noID+repeated+notObject == 0 {
		return nil
	}
	var reasons []string
	if noID > 0 {
		reasons = append(reasons, fmt.Sprintf("%d with no id", noID))
	}
	if repeated > 0 {
		reasons = append(reasons, fmt.Sprintf("%d with an id an earlier call already used", repeated))
	}
	if notObject > 0 {
		reasons = append(reasons, fmt.Sprintf("%d whose arguments are not a JSON object", notObject))
	}
	return types.Classified(types.KindMalformedOutput,
		fmt.Errorf("%s: %w: %d tool calls (%s)", provider, ErrMalformedToolCall, len(calls), strings.Join(reasons, ", ")))
}

// truncatedToolCallsError is the error for a reply that stopped at a limit with
// tool calls in it. The arguments of a call that was cut are text that happens to
// stop somewhere, and one that stops at a closing brace would pass as an object,
// so none of the calls of a reply that did not finish is returned.
func truncatedToolCallsError(provider, model, reason string) error {
	switch reason {
	case "max_output_tokens", "max_tokens", "length":
		return &types.OperationError{
			Kind:     types.KindOutputTruncated,
			Op:       "llm.complete",
			Provider: provider,
			Model:    model,
			Message:  "the answer was cut off at the token ceiling while it held tool calls, so their arguments are not known to be whole; raise MaxTokens",
		}
	}
	return incompleteAnswerError(provider, model, reason)
}

// responsesInputItems writes a conversation as the Responses API's input items,
// in the order the turns were given. A turn is an item with a role and its
// content, as it always was. A tool's result is not a turn: the API takes it as a
// function_call_output item carrying the id of the call it answers and the
// result as text, and does not define a "tool" role. The model's call is not a
// turn either: it is a function_call item with the call's id, name and arguments
// as text, one for each call, after the words the model said before it, if any.
func responsesInputItems(messages []Message) []map[string]any {
	items := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		if message.Role == "assistant" && len(message.ToolCalls) > 0 {
			if message.Content != "" {
				items = append(items, map[string]any{"role": message.Role, "content": message.Content})
			}
			for _, call := range message.ToolCalls {
				items = append(items, map[string]any{
					"type":      "function_call",
					"call_id":   call.ID,
					"name":      call.Name,
					"arguments": string(call.Arguments),
				})
			}
			continue
		}
		if message.Role == "tool" {
			items = append(items, map[string]any{
				"type":    "function_call_output",
				"call_id": message.ToolCallID,
				"output":  message.Content,
			})
			continue
		}
		item := map[string]any{
			"role":    message.Role,
			"content": message.Content,
		}
		if message.ToolCallID != "" {
			item["call_id"] = message.ToolCallID
		}
		items = append(items, item)
	}
	return items
}

// requestPromptBytes is the size in bytes of the text a request sends the model:
// the two prompts, the content of every turn of a conversation, and each tool's
// name, description and parameters. It is what the cost estimate counts, so that
// a conversation and its tools are priced and not only the system prompt.
func requestPromptBytes(req CompletionRequest) int {
	size := len(req.SystemPrompt) + len(req.UserPrompt)
	for _, message := range req.Messages {
		size += len(message.Content)
		for _, call := range message.ToolCalls {
			size += len(call.Name) + len(call.Arguments)
		}
	}
	for _, tool := range req.Tools {
		size += len(tool.Name) + len(tool.Description)
		if len(tool.Parameters) > 0 {
			if encoded, err := json.Marshal(tool.Parameters); err == nil {
				size += len(encoded)
			}
		}
	}
	return size
}
