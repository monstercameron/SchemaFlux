package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

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
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
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

// chatCompletionResponse is the subset of the reply this library uses.
type chatCompletionResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content string `json:"content"`
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

	endpoint := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if endpoint == "" {
		return CompletionResponse{}, fmt.Errorf("%s: no base URL configured", name)
	}
	endpoint += chatCompletionsEndpoint

	body := chatCompletionRequest{
		Model: req.Model,
		Messages: []chatMessage{
			{Role: "system", Content: req.SystemPrompt},
			{Role: "user", Content: req.UserPrompt},
		},
	}

	if req.Temperature > 0 {
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
	httpReq.Header.Set("Authorization", "Bearer "+config.APIKey)
	for header, value := range config.ExtraHeaders {
		httpReq.Header.Set(header, value)
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("%s request failed: %w", name, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("%s: failed to read response: %w", name, err)
	}

	if resp.StatusCode != http.StatusOK {
		// A throttled request carries the wait the server wants in a header.
		// Reporting it as a plain error throws that away and leaves the retry
		// loop guessing at a window it cannot see.
		if isRateLimited(resp.StatusCode) {
			return CompletionResponse{}, rateLimitError(name, resp, string(raw))
		}
		// The body is included because a schema rejection is reported there and
		// nowhere else; a bare status code sends the caller guessing.
		return CompletionResponse{}, fmt.Errorf("%s API error (status %d): %s",
			name, resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var decoded chatCompletionResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return CompletionResponse{}, fmt.Errorf("%s: failed to decode response: %w", name, err)
	}

	// Some compatible vendors answer 200 with an error object in the body.
	if decoded.Error != nil && strings.TrimSpace(decoded.Error.Message) != "" {
		return CompletionResponse{}, fmt.Errorf("%s API error: %s", name, decoded.Error.Message)
	}

	if len(decoded.Choices) == 0 {
		return CompletionResponse{}, fmt.Errorf("%s: %w", name, ErrNoMessageOutput)
	}

	choice := decoded.Choices[0]
	if strings.TrimSpace(choice.Message.Content) == "" {
		return CompletionResponse{}, fmt.Errorf("%s: %w", name, ErrNoMessageOutput)
	}

	model := decoded.Model
	if model == "" {
		model = req.Model
	}

	finishReason := choice.FinishReason
	if finishReason == "" {
		finishReason = "stop"
	}

	return CompletionResponse{
		Content:      choice.Message.Content,
		Provider:     name,
		Model:        model,
		FinishReason: finishReason,
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
// These are all annotations or constraints on top of a type, never the type
// itself, so dropping them narrows nothing that the caller's Go type does not
// already enforce on unmarshal.
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
	sanitised, _ := sanitiseSchemaValue(schema).(map[string]any)
	return sanitised
}

func sanitiseSchemaValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		copied := make(map[string]any, len(typed))
		for key, nested := range typed {
			if unsupportedInChatDialect[key] {
				continue
			}
			copied[key] = sanitiseSchemaValue(nested)
		}
		return copied
	case []any:
		copied := make([]any, len(typed))
		for i, nested := range typed {
			copied[i] = sanitiseSchemaValue(nested)
		}
		return copied
	default:
		return value
	}
}
