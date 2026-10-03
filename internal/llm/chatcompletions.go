package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// The OpenAI-compatible providers went through go-openai's chat completion
// client, and that client's ChatCompletionResponseFormat carries a Type and
// nothing else. So a request that arrived here with a strict JSON schema had
// the schema dropped and was sent as `response_format: {"type":"json_object"}`
// -- "please emit some JSON" rather than "emit something this schema accepts".
//
// That is a silent downgrade, and it is invisible at the call site: the same
// Extract[T] gets constrained decoding on OpenAI and an unconstrained guess on
// Cerebras, DeepSeek, Qwen, ZAI, and OpenRouter. The schema is the contract
// this library is built on, so the transport is hand-rolled here for the same
// reason the Responses API path is: the SDK cannot express the shape.

// chatCompletionsEndpoint is the path appended to a provider's base URL.
const chatCompletionsEndpoint = "/chat/completions"

// chatCompletionRequest is the wire shape of an OpenAI-compatible request. It
// is written out rather than reused from the SDK because the SDK's type cannot
// carry a schema.
type chatCompletionRequest struct {
	Model          string              `json:"model"`
	Messages       []chatMessage       `json:"messages"`
	Temperature    *float64            `json:"temperature,omitempty"`
	MaxTokens      int                 `json:"max_tokens,omitempty"`
	ResponseFormat *chatResponseFormat `json:"response_format,omitempty"`
	Tools          []chatTool          `json:"tools,omitempty"`
	ToolChoice     any                 `json:"tool_choice,omitempty"`
}

// chatMessage is one message of a request. Content is a pointer because the
// dialect lets an assistant message that only calls tools carry none, which is
// written as null, while every other message carries its text, an empty string
// included.
type chatMessage struct {
	Role       string         `json:"role"`
	Content    *string        `json:"content"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

// chatText is the content of a message that has text.
func chatText(text string) *string { return &text }

// chatTool declares one function the model may call.
type chatTool struct {
	Type     string           `json:"type"`
	Function chatToolFunction `json:"function"`
}

type chatToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
	// Strict is written only when it is true: the dialect's own default is that a
	// function is not strict, so leaving it off says the same as false, and a
	// vendor that has no such flag is not sent one it did not ask for.
	Strict bool `json:"strict,omitempty"`
}

// chatToolCall is a call the model made, as the dialect writes it in a response
// and as it is written back in the assistant message that made it. The arguments
// are text, as the dialect defines them.
type chatToolCall struct {
	ID       string               `json:"id"`
	Type     string               `json:"type"`
	Function chatToolCallFunction `json:"function"`
}

type chatToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatResponseFormat struct {
	Type       string          `json:"type"`
	JSONSchema *chatJSONSchema `json:"json_schema,omitempty"`
}

type chatJSONSchema struct {
	Name string `json:"name"`
	// Strict turns the schema from a suggestion into constrained decoding.
	// Without it a provider is free to return something the schema rejects,
	// which is the failure this library exists to remove.
	Strict bool           `json:"strict"`
	Schema map[string]any `json:"schema"`
}

// chatReplyToolCall is a tool call as a reply carries it.
type chatReplyToolCall struct {
	ID       string `json:"id"`
	Function struct {
		Name string `json:"name"`
		// Arguments is text in the dialect. A server that sends an object instead
		// is read as the object it sent: what the caller decodes is the same
		// either way.
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

// chatCompletionResponse is the subset of the reply this library uses.
type chatCompletionResponse struct {
	Model string `json:"model"`
	// Provider is the vendor a gateway says served the request. OpenRouter
	// sends it as a string at the top level. It is decoded as any because a
	// server that sends something else under this key (an object) must not make
	// the whole answer undecodable: only a string is read.
	Provider any `json:"provider"`
	Choices  []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content   string              `json:"content"`
			ToolCalls []chatReplyToolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		TotalTokens         int `json:"total_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error"`
}

// completeViaChatCompletions posts an OpenAI-compatible chat completion and
// returns the answer. It is shared by every provider that speaks that dialect.
func completeViaChatCompletions(
	ctx context.Context,
	client *http.Client,
	name string,
	config ProviderConfig,
	req CompletionRequest,
) (CompletionResponse, error) {

	// LX-105. The chat dialect is spoken by servers this library knows nothing
	// about, and no effort parameter is common to them. A caller who asked for
	// one is told so; an operation's preference is left out.
	if err := effortNotSupported(req, name); err != nil {
		return CompletionResponse{}, err
	}
	// The dialect carries a system prompt, a conversation, tools and a response
	// format. It has no search, and a request that asks for one is refused and not
	// sent without it.
	if err := unsupportedRequestFields(req, name, "WebSearch"); err != nil {
		return CompletionResponse{}, err
	}
	// What cannot be written is refused before anything is sent.
	if err := checkToolChoice(req, name); err != nil {
		return CompletionResponse{}, err
	}
	if err := checkTools(req, name, false); err != nil {
		return CompletionResponse{}, err
	}
	if err := checkTranscript(req, name); err != nil {
		return CompletionResponse{}, err
	}

	endpoint := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if endpoint == "" {
		return CompletionResponse{}, types.Newf(types.KindConfiguration, "llm.configure", "%s: no base URL configured", name)
	}
	endpoint += chatCompletionsEndpoint

	ctx, cancel := callContext(ctx, config)
	defer cancel()

	body := chatCompletionRequest{
		Model:    req.Model,
		Messages: chatMessages(req),
		Tools:    chatTools(req.Tools),
	}
	if req.ToolChoice != "" {
		body.ToolChoice = chatToolChoice(req.ToolChoice)
	}

	// The chat dialect is spoken by other vendors' servers, and by gateways in
	// front of OpenAI's models. A name the model table knows gets its row, so a
	// gpt-6 model reached through a gateway is not sent the `temperature` it
	// rejects (LX-001); any other name is sent it, as it always was.
	if req.Temperature > 0 && !config.OmitTemperature &&
		supportsTemperature(req.Model, isOpenAIEndpoint(config.BaseURL)) {
		temperature := req.Temperature
		body.Temperature = &temperature
	}
	if req.MaxTokens > 0 {
		body.MaxTokens = req.MaxTokens
	}

	if req.ResponseFormat == "json" {
		if schema := chatDialectSchema(req.JSONSchema); len(schema) > 0 {
			schemaName := req.SchemaName
			if schemaName == "" {
				schemaName = "result"
			}
			body.ResponseFormat = &chatResponseFormat{
				Type: "json_schema",
				JSONSchema: &chatJSONSchema{
					Name:   schemaName,
					Strict: true,
					Schema: schema,
				},
			}
		} else {
			body.ResponseFormat = &chatResponseFormat{Type: "json_object"}
		}
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("%s: failed to marshal request: %w", name, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("%s: failed to create request: %w", name, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	// ProviderConfig.ExtraHeaders are added by the transport resolveHTTPClient
	// wraps around the client, so they are not set here a second time.
	httpReq.Header.Set("Authorization", "Bearer "+config.APIKey)

	resp, clock, err := doTimed(client, httpReq)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("%s request failed: %w", name, err)
	}
	defer drainAndClose(resp.Body)

	if resp.StatusCode != http.StatusOK {
		// Typed, so a caller can branch on the status rather than substring-
		// matching an English sentence, with the wait a throttled answer asks for
		// kept, and the raw body retained without being printed into their logs by
		// default.
		return CompletionResponse{}, statusError(name, req.Model, resp, config)
	}

	raw, err := readBounded(resp.Body, maxResponseBodyBytes)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("%s: failed to read response: %w", name, err)
	}

	var decoded chatCompletionResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return CompletionResponse{}, fmt.Errorf("%s: failed to decode response: %w", name, err)
	}

	// Some compatible vendors answer 200 with an error object in the body. It is
	// the same failure as one that arrived as a status, so it is typed as one:
	// the message is bounded, the code is kept, and a throttle is retried as one.
	if decoded.Error != nil && strings.TrimSpace(decoded.Error.Message) != "" {
		failure := vendorFailureError(name, req.Model, decoded.Error.Type, scalarString(decoded.Error.Code), decoded.Error.Message, config)
		if carriesRetryAfter(failure.StatusCode) {
			return CompletionResponse{}, &RateLimitError{
				APIError:   failure,
				RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
			}
		}
		return CompletionResponse{}, failure
	}

	if len(decoded.Choices) == 0 {
		return CompletionResponse{}, fmt.Errorf("%s: %w", name, ErrNoMessageOutput)
	}

	choice := decoded.Choices[0]
	calls := chatReplyCalls(choice.Message.ToolCalls)
	// Only the reasons that say how an answer ended are read. A missing reason is
	// not "stop", and one this library does not know is not known to be an answer
	// at all. "tool_calls" is a reply that asked for tools, and is one only when
	// the reply carries the calls it says it does.
	switch choice.FinishReason {
	case "stop", "length", "content_filter":
	case "tool_calls":
		if len(calls) == 0 {
			return CompletionResponse{}, unknownFinishReasonError(name, req.Model)
		}
	default:
		return CompletionResponse{}, unknownFinishReasonError(name, req.Model)
	}
	if len(calls) > 0 {
		// Calls that were cut off, or withheld, are not returned: the arguments of
		// a call that stopped at a limit are text that stops somewhere.
		switch choice.FinishReason {
		case "length":
			return CompletionResponse{}, truncatedToolCallsError(name, req.Model, choice.FinishReason)
		case "content_filter":
			return CompletionResponse{}, incompleteAnswerError(name, req.Model, choice.FinishReason)
		}
		if err := checkToolCalls(name, calls, req.Tools); err != nil {
			return CompletionResponse{}, err
		}
	} else if strings.TrimSpace(choice.Message.Content) == "" {
		// Stopped before any text: the same request would be stopped the same way.
		if choice.FinishReason != "stop" {
			return CompletionResponse{}, incompleteAnswerError(name, req.Model, choice.FinishReason)
		}
		return CompletionResponse{}, fmt.Errorf("%s: %w", name, ErrNoMessageOutput)
	}

	model := decoded.Model
	if model == "" {
		model = req.Model
	}

	finishReason := choice.FinishReason

	// Where the request went from here, when the response says. It was never
	// read, so for the repo's own router the answer-route check could not see
	// which vendor served a request it had allowed to be sent to the router.
	upstream, _ := decoded.Provider.(string)

	return CompletionResponse{
		Timing:           clock.timing(),
		Content:          choice.Message.Content,
		ToolCalls:        calls,
		Provider:         name,
		UpstreamProvider: strings.TrimSpace(upstream),
		Model:            model,
		FinishReason:     finishReason,
		Usage: types.TokenUsage{
			PromptTokens:     decoded.Usage.PromptTokens,
			CompletionTokens: decoded.Usage.CompletionTokens,
			TotalTokens:      decoded.Usage.TotalTokens,
			CachedTokens:     decoded.Usage.PromptTokensDetails.CachedTokens,
			ReasoningTokens:  decoded.Usage.CompletionTokensDetails.ReasoningTokens,
		},
	}, nil
}

// unsupportedInChatDialect lists the JSON Schema keywords the compatible
// vendors reject under strict mode. Cerebras documents the set explicitly;
// sending any of them fails the whole request with a 400 rather than being
// ignored, so a `time.Time` field -- which the generator annotates with
// `format: date-time` -- would take an otherwise valid extraction down.
//
// These are annotations or constraints on top of a type, never the type itself.
// Dropping them is a downgrade all the same: a Go type does not enforce a
// pattern, a length or a range on unmarshal, so a constraint stripped here is
// one nothing on this path checks. ProviderCapabilities.SchemaKeywords is where
// a route says which keywords it honours, and a request that depends on one of
// these is negotiated against it.
var unsupportedInChatDialect = map[string]bool{
	"format":    true,
	"pattern":   true,
	"minItems":  true,
	"maxItems":  true,
	"minLength": true,
	"maxLength": true,
	"minimum":   true,
	"maximum":   true,
	"default":   true,
	"examples":  true,
}

// chatDialectSchema returns a copy of schema with the keywords the chat dialect
// rejects removed. It copies rather than editing in place: the caller's schema
// is reused across providers, and a Cerebras call must not quietly strip
// annotations from the next OpenAI one.
func chatDialectSchema(schema map[string]any) map[string]any {
	if len(schema) == 0 {
		return nil
	}
	return sanitiseSchema(schema)
}

// schemaMapKeywords are the keywords whose value is a map from a name to a
// schema. The names in it are the schema's own data, a property called "format"
// among them, and are never keywords.
var schemaMapKeywords = map[string]bool{
	"properties": true, "$defs": true, "definitions": true,
	"patternProperties": true, "dependentSchemas": true,
}

// schemaListKeywords are the keywords whose value is a list of schemas.
var schemaListKeywords = map[string]bool{
	"anyOf": true, "oneOf": true, "allOf": true, "prefixItems": true,
}

// schemaKeywords are the keywords whose value is one schema.
var schemaKeywords = map[string]bool{
	"items": true, "additionalProperties": true, "not": true, "if": true,
	"then": true, "else": true, "contains": true, "propertyNames": true,
	"unevaluatedItems": true, "unevaluatedProperties": true,
}

// sanitiseSchema copies one schema with the unsupported keywords removed from
// it, and from every schema inside it. A keyword is removed only where a schema
// stands: a property named "format", a definition named "default", and the values
// of "enum", "const" and "required" are the schema's data, not keywords, and are
// copied as they are. Stripping by name wherever it appeared deleted a property
// from `properties` while `required` still named it.
func sanitiseSchema(schema map[string]any) map[string]any {
	copied := make(map[string]any, len(schema))
	for keyword, value := range schema {
		if unsupportedInChatDialect[keyword] {
			continue
		}
		switch {
		case schemaMapKeywords[keyword]:
			if named, ok := value.(map[string]any); ok {
				members := make(map[string]any, len(named))
				for name, member := range named {
					members[name] = sanitiseSchemaValue(member)
				}
				copied[keyword] = members
				continue
			}
		case schemaListKeywords[keyword]:
			if list, ok := value.([]any); ok {
				members := make([]any, len(list))
				for i, member := range list {
					members[i] = sanitiseSchemaValue(member)
				}
				copied[keyword] = members
				continue
			}
		case schemaKeywords[keyword]:
			copied[keyword] = sanitiseSchemaValue(value)
			continue
		}
		copied[keyword] = copySchemaData(value)
	}
	return copied
}

// sanitiseSchemaValue sanitises a value that is a schema, and passes on any that
// is not one: a boolean schema, such as `additionalProperties: false`, has no
// keywords.
func sanitiseSchemaValue(value any) any {
	if schema, ok := value.(map[string]any); ok {
		return sanitiseSchema(schema)
	}
	return copySchemaData(value)
}

// copySchemaData deep-copies maps and lists, so that the sanitised schema shares
// nothing with the caller's and editing one cannot change the other.
func copySchemaData(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		copied := make(map[string]any, len(typed))
		for key, nested := range typed {
			copied[key] = copySchemaData(nested)
		}
		return copied
	case []any:
		copied := make([]any, len(typed))
		for i, nested := range typed {
			copied[i] = copySchemaData(nested)
		}
		return copied
	default:
		return value
	}
}

// unknownFinishReasonError is the refusal of a reply whose finish reason is not one
// that says how an answer ended.
func unknownFinishReasonError(provider, model string) error {
	return &types.OperationError{
		Kind:     types.KindMalformedOutput,
		Op:       "llm.complete",
		Provider: provider,
		Model:    model,
		Message:  "the response carried no finish reason this library recognises, so it is not known to be a finished answer",
	}
}

// chatReplyCalls reads the tool calls of a reply, in the order the model made
// them. Their arguments are the text the dialect defines them as; a server that
// sends an object is read as the object it sent, and anything else is carried as
// the text it is, for checkToolCalls to refuse.
func chatReplyCalls(wire []chatReplyToolCall) []ToolCall {
	if len(wire) == 0 {
		return nil
	}
	calls := make([]ToolCall, len(wire))
	for i, call := range wire {
		arguments := call.Function.Arguments
		var text string
		if len(arguments) > 0 && arguments[0] == '"' && json.Unmarshal(arguments, &text) == nil {
			arguments = json.RawMessage(text)
		}
		calls[i] = ToolCall{ID: call.ID, Name: call.Function.Name, Arguments: arguments}
	}
	return calls
}

// chatMessages writes the conversation of a request: the system prompt, then
// either the single user prompt or, when the request carries a transcript, its
// turns in order. An assistant turn that called tools is written with those calls
// and a tool's result as a message of the role "tool" naming the call it answers.
func chatMessages(req CompletionRequest) []chatMessage {
	messages := []chatMessage{{Role: "system", Content: chatText(req.SystemPrompt)}}
	if len(req.Messages) == 0 {
		return append(messages, chatMessage{Role: "user", Content: chatText(req.UserPrompt)})
	}
	for _, message := range req.Messages {
		wire := chatMessage{Role: message.Role, Content: chatText(message.Content), ToolCallID: message.ToolCallID}
		if message.Role == "assistant" && len(message.ToolCalls) > 0 {
			// An assistant turn that only called tools has no text, which the
			// dialect writes as null.
			if message.Content == "" {
				wire.Content = nil
			}
			wire.ToolCalls = make([]chatToolCall, len(message.ToolCalls))
			for i, call := range message.ToolCalls {
				wire.ToolCalls[i] = chatToolCall{ID: call.ID, Type: "function", Function: chatToolCallFunction{Name: call.Name, Arguments: string(call.Arguments)}}
			}
		}
		messages = append(messages, wire)
	}
	return messages
}

// chatTools declares the request's tools. A schema is written as a response
// schema is: without the keywords the compatible vendors reject, and as a copy,
// since the caller's schema is reused across providers. A tool is flagged strict
// when what is left is a schema strict mode takes.
func chatTools(tools []Tool) []chatTool {
	if len(tools) == 0 {
		return nil
	}
	declared := make([]chatTool, len(tools))
	for i, tool := range tools {
		parameters := chatDialectSchema(toolParameters(tool))
		declared[i] = chatTool{Type: "function", Function: chatToolFunction{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  parameters,
			Strict:      toolIsStrict(parameters),
		}}
	}
	return declared
}

// chatToolChoice is the dialect's spelling of a choice: the three words as they
// are, and the name of a tool as the function the model must call.
func chatToolChoice(choice string) any {
	switch choice {
	case "auto", "none", "required":
		return choice
	}
	return map[string]any{"type": "function", "function": map[string]any{"name": choice}}
}
