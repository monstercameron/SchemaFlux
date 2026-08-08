package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
	openai "github.com/sashabaranov/go-openai"
)

// Provider defines the interface for LLM providers
type Provider interface {
	// Complete sends a completion request to the LLM
	Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)

	// Name returns the provider name
	Name() string

	// EstimateCost estimates the cost for a request
	EstimateCost(req CompletionRequest) float64

	// RetryPolicy returns provider-specific retry settings.
	RetryPolicy() (maxRetries int, backoff time.Duration)
}

// CompletionRequest represents a unified request format
type CompletionRequest struct {
	Model          string
	SystemPrompt   string
	UserPrompt     string
	Temperature    float64
	MaxTokens      int
	ResponseFormat string // "json" or "text"

	// JSONSchema, when set, is sent as the Responses API's json_schema strict
	// format. The library generates an exact schema for the target type and
	// used to render it into the prompt as prose and ask only for a json_object,
	// so the one artifact that could make the result structurally guaranteed
	// never reached the API that can enforce it.
	JSONSchema map[string]any

	// SchemaName names the schema in the request. It is required by the API.
	SchemaName string

	// PromptCacheKey is sent as the Responses API's `prompt_cache_key`, a hint
	// that tells OpenAI's routing layer which backend replica is likely to
	// already hold this request's stable prefix in its prompt cache.
	//
	// It has to identify more than "which operation, at which tier" -- that
	// tuple collides across every prompt revision and every schema change,
	// because neither is part of it, and a collision here does not fail the
	// request: it silently routes to a replica holding a DIFFERENT prefix, so
	// the cache read misses and the call pays full price while looking like it
	// should have been cheap. internal/ops computes this (CacheIdentity plus
	// the resolved model and the rendered template) because internal/llm
	// cannot import internal/ops -- ops already imports llm. P-009.
	PromptCacheKey string
}

// CompletionResponse represents a unified response format
type CompletionResponse struct {
	Content      string
	Usage        types.TokenUsage
	Model        string
	Provider     string
	FinishReason string
}

// ProviderConfig contains provider-specific configuration
type ProviderConfig struct {
	APIKey       string
	BaseURL      string
	OrgID        string
	Timeout      time.Duration
	MaxRetries   int
	RetryBackoff time.Duration
	Debug        bool
	ExtraHeaders map[string]string

	// Store asks the provider to retain the response server-side. It defaults
	// to false: the zero value is the private one, so a caller who never
	// thinks about this gets retention off rather than on.
	Store bool
}

// ProviderFactory creates a provider from configuration.
type ProviderFactory func(config ProviderConfig) (Provider, error)

// ErrNoMessageOutput reports a response that carried no assistant message.
// It is deterministic, not transient: a reasoning-only response or an empty
// output list will be identical on retry, so the retry classifier must not
// spend a backoff cycle on it.
var ErrNoMessageOutput = errors.New("response carried no message output")

// OpenAIProvider implements Provider for OpenAI
type OpenAIProvider struct {
	client     *openai.Client
	httpClient *http.Client
	config     ProviderConfig
}

// OpenAICompatibleProvider implements Provider for vendors with an OpenAI-compatible chat API.
type OpenAICompatibleProvider struct {
	name string
	// The transport is hand-rolled rather than go-openai's: see
	// chatcompletions.go for why the SDK's response format cannot carry a
	// schema. One client per provider, so connections are reused.
	httpClient *http.Client
	config     ProviderConfig
}

// NewOpenAIProvider creates a new OpenAI provider
func NewOpenAIProvider(config ProviderConfig) (*OpenAIProvider, error) {
	if config.APIKey == "" {
		return nil, fmt.Errorf("OpenAI API key is required")
	}

	client, config, err := newOpenAIClient(config, "")
	if err != nil {
		return nil, fmt.Errorf("OpenAI %w", err)
	}

	return &OpenAIProvider{
		client: client,
		// One client per provider. A fresh http.Client per request defeats
		// connection reuse and HTTP/2 multiplexing; the per-request deadline
		// rides on the context instead.
		httpClient: &http.Client{Timeout: config.Timeout},
		config:     config,
	}, nil
}

// Complete sends a completion request to OpenAI using the Responses API
func (provider *OpenAIProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	// Use the new Responses API (POST /v1/responses)
	// Since go-openai v1.20.4 doesn't support this endpoint, we implement it manually.

	url := "https://api.openai.com/v1/responses"
	if provider.config.BaseURL != "" {
		url = strings.TrimRight(provider.config.BaseURL, "/") + "/responses"
	}

	input := req.UserPrompt
	if req.ResponseFormat == "json" && !strings.Contains(strings.ToLower(input), "json") {
		input = "Return valid JSON only.\n\n" + input
	}

	// Construct request body
	requestBody := map[string]interface{}{
		"model":        req.Model,
		"input":        input,
		"instructions": req.SystemPrompt,
		// The Responses API retains responses server-side unless told
		// otherwise. That is a surprising default for a library whose whole
		// job is running arbitrary user records -- invoices, tickets, medical
		// notes -- through a model: the caller opted into an extraction, not
		// into retention. Off by default; Store(true) is opt-in.
		"store": provider.config.Store,
	}

	if req.Temperature > 0 && supportsTemperature(req.Model) {
		requestBody["temperature"] = req.Temperature
	}

	if req.MaxTokens > 0 {
		requestBody["max_output_tokens"] = req.MaxTokens
	}

	if req.PromptCacheKey != "" {
		requestBody["prompt_cache_key"] = req.PromptCacheKey
	}

	textConfig := map[string]interface{}{}
	if req.ResponseFormat == "json" {
		if len(req.JSONSchema) > 0 {
			name := req.SchemaName
			if name == "" {
				name = "result"
			}
			// strict: the model must produce something the schema accepts, which
			// is the difference between a contract and a request.
			textConfig["format"] = map[string]interface{}{
				"type":   "json_schema",
				"name":   name,
				"strict": true,
				"schema": req.JSONSchema,
			}
		} else {
			textConfig["format"] = map[string]string{
				"type": "json_object",
			}
		}
	}
	if supportsReasoningControls(req.Model) {
		// An empty effort means "this family's accepted values are unknown".
		// Sending the block anyway would fail the whole request, so omit it.
		if effort := reasoningEffort(req.Model); effort != "" {
			requestBody["reasoning"] = map[string]string{"effort": effort}
			textConfig["verbosity"] = "low"
		}
	}
	if len(textConfig) > 0 {
		requestBody["text"] = textConfig
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+provider.config.APIKey)

	if provider.config.OrgID != "" {
		httpReq.Header.Set("OpenAI-Organization", provider.config.OrgID)
	}

	resp, err := provider.httpClient.Do(httpReq)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("OpenAI request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		if isRateLimited(resp.StatusCode) {
			return CompletionResponse{}, rateLimitError("openai", req.Model, resp, string(bodyBytes))
		}
		return CompletionResponse{}, NewAPIError("openai", req.Model, resp.StatusCode, string(bodyBytes))
	}

	// Parse response
	var response struct {
		ID               string `json:"id"`
		Status           string `json:"status"`
		IncompleteReason struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			InputTokens        int `json:"input_tokens"`
			OutputTokens       int `json:"output_tokens"`
			TotalTokens        int `json:"total_tokens"`
			InputTokensDetails struct {
				CachedTokens     int `json:"cached_tokens"`
				CacheWriteTokens int `json:"cache_write_tokens"`
			} `json:"input_tokens_details"`
			OutputTokensDetails struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
		Model string `json:"model"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return CompletionResponse{}, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(response.Output) == 0 {
		return CompletionResponse{}, fmt.Errorf("openai: %w", ErrNoMessageOutput)
	}

	// Extract the answer only. A reasoning model returns `reasoning` items
	// alongside the `message` item; concatenating every item prefixes the
	// answer with reasoning prose, which corrupts a JSON payload.
	var builder strings.Builder
	for _, output := range response.Output {
		if output.Type != "" && output.Type != "message" {
			continue
		}
		for _, item := range output.Content {
			// Older payloads omit the content type; treat that as text.
			if item.Type != "" && item.Type != "output_text" {
				continue
			}
			builder.WriteString(item.Text)
		}
	}
	content := builder.String()

	if content == "" {
		if response.Status == "incomplete" && response.IncompleteReason.Reason != "" {
			return CompletionResponse{}, fmt.Errorf("OpenAI response incomplete: %s", response.IncompleteReason.Reason)
		}
		return CompletionResponse{}, fmt.Errorf("openai: %w", ErrNoMessageOutput)
	}

	// A truncated answer must be distinguishable from a complete one.
	finishReason := "stop"
	if response.Status == "incomplete" {
		finishReason = response.IncompleteReason.Reason
		if finishReason == "" {
			finishReason = "incomplete"
		}
	}

	return CompletionResponse{
		Content:      content,
		Provider:     provider.Name(),
		Model:        response.Model,
		FinishReason: finishReason,
		Usage: types.TokenUsage{
			PromptTokens:     response.Usage.InputTokens,
			CompletionTokens: response.Usage.OutputTokens,
			TotalTokens:      response.Usage.TotalTokens,
			CachedTokens:     response.Usage.InputTokensDetails.CachedTokens,
			CacheWriteTokens: response.Usage.InputTokensDetails.CacheWriteTokens,
			ReasoningTokens:  response.Usage.OutputTokensDetails.ReasoningTokens,
		},
	}, nil
}

// Name returns the provider name
func (provider *OpenAIProvider) Name() string {
	return "openai"
}

// RetryPolicy returns provider retry settings.
func (provider *OpenAIProvider) RetryPolicy() (int, time.Duration) {
	return provider.config.MaxRetries, provider.config.RetryBackoff
}

// EstimateCost estimates the cost for OpenAI
func (provider *OpenAIProvider) EstimateCost(req CompletionRequest) float64 {
	inputRate, outputRate := getModelRates("openai", req.Model)

	estimatedPromptTokens := len(req.SystemPrompt+req.UserPrompt) / 4
	estimatedCompletionTokens := 500 // Default estimate
	if req.MaxTokens > 0 {
		estimatedCompletionTokens = req.MaxTokens
	}

	promptCost := float64(estimatedPromptTokens) * inputRate
	completionCost := float64(estimatedCompletionTokens) * outputRate

	return promptCost + completionCost
}

// The capability predicates below are measured, not inferred from the model
// name. The evidence is .audit/live/capabilities.py (one parameter at a time
// against each 5.6 model) and .audit/live/bench3.py (whether sending the
// reasoning block costs accuracy). Both are re-runnable.
//
// This matters more than a normal default would, because these parameters are
// not negotiated: an unaccepted one fails the WHOLE request with a 400, so a
// wrong guess is not a degraded call, it is no call at all.

func supportsTemperature(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	// Measured: all three 5.6 models reject `temperature` outright, including
	// a temperature of zero -- "Unsupported parameter: 'temperature' is not
	// supported with this model."
	if strings.HasPrefix(model, "gpt-5") {
		return false
	}
	return true
}

func supportsReasoningControls(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))

	// The 5.6 family accepts a reasoning block, so this is not about what the
	// API allows. It is about what the answer is worth afterwards.
	//
	// Measured on a proration with a distractor figure, four runs per arm:
	//
	//	                 omitted        effort=none      effort=low
	//	luna             4/4  2304ms    0/4  3682ms      4/4  2339ms
	//	sol              4/4  6808ms    0/4  2959ms      4/4  3375ms
	//	terra            4/4  2174ms    4/4  1884ms      4/4  2232ms
	//
	// `effort: none` takes luna and sol from four correct to none correct --
	// answers of 650 and 2600 against a correct 500. `effort: low` matches the
	// omitted case on accuracy and beats it on latency nowhere that matters.
	// So the block buys nothing and can cost everything: omit it, and let the
	// server's own default stand.
	if strings.HasPrefix(model, "gpt-5.6") {
		return false
	}
	return strings.HasPrefix(model, "gpt-5")
}

// reasoningEffort returns an effort value the model accepts, or "" to omit the
// block entirely.
//
// It used to return "minimal" for everything that was not gpt-5.4. The 5.6
// family rejects "minimal" -- accepted values are none/low/medium/high/xhigh/
// max -- so the pair of functions was one flag away from 400ing every request,
// and supportsReasoningControls returning false was the only thing hiding it.
// Returning "" for a family whose accepted values are not known keeps the
// failure mode at "no reasoning control" rather than "no requests".
func reasoningEffort(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasPrefix(model, "gpt-5.6"):
		// Accepted, but measured to be harmful at "none" and neutral at "low";
		// see supportsReasoningControls. Named here so that flipping that
		// predicate produces a valid request rather than a 400.
		return "low"
	case strings.HasPrefix(model, "gpt-5.4"):
		return "none"
	case strings.HasPrefix(model, "gpt-5"):
		return "minimal"
	default:
		return ""
	}
}

func normalizeProviderName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func newOpenAIClient(config ProviderConfig, defaultBaseURL string) (*openai.Client, ProviderConfig, error) {
	if config.APIKey == "" {
		return nil, config, fmt.Errorf("API key is required")
	}

	clientConfig := openai.DefaultConfig(config.APIKey)

	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if baseURL == "" {
		baseURL = strings.TrimRight(defaultBaseURL, "/")
	}
	if baseURL != "" {
		clientConfig.BaseURL = baseURL
		config.BaseURL = baseURL
	}

	if config.OrgID != "" {
		clientConfig.OrgID = config.OrgID
	}

	if len(config.ExtraHeaders) > 0 {
		clientConfig.HTTPClient = &http.Client{
			Transport: &customTransport{
				transport: http.DefaultTransport,
				headers:   config.ExtraHeaders,
			},
			Timeout: config.Timeout,
		}
	}

	return openai.NewClientWithConfig(clientConfig), config, nil
}

func newOpenAICompatibleProvider(name string, config ProviderConfig, defaultBaseURL string) (*OpenAICompatibleProvider, error) {
	// newOpenAIClient is still what validates the key and settles the base URL,
	// even though the SDK client it builds is no longer used to send the call.
	_, config, err := newOpenAIClient(config, defaultBaseURL)
	if err != nil {
		return nil, fmt.Errorf("%s %w", name, err)
	}

	return &OpenAICompatibleProvider{
		name:       normalizeProviderName(name),
		httpClient: &http.Client{Timeout: config.Timeout},
		config:     config,
	}, nil
}

// AnthropicProvider implements Provider for Anthropic Claude
type AnthropicProvider struct {
	config  ProviderConfig
	apiKey  string
	baseURL string
}

// NewAnthropicProvider creates a new Anthropic provider
func NewAnthropicProvider(config ProviderConfig) (*AnthropicProvider, error) {
	if config.APIKey == "" {
		return nil, fmt.Errorf("anthropic API key is required")
	}

	baseURL := config.BaseURL
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}

	return &AnthropicProvider{
		config:  config,
		apiKey:  config.APIKey,
		baseURL: baseURL,
	}, nil
}

// Complete sends a completion request to Anthropic
func (provider *AnthropicProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	url := strings.TrimRight(provider.baseURL, "/") + "/v1/messages"

	model := req.Model
	if model == "" || strings.HasPrefix(model, "gpt") {
		// Default to Sonnet 3.5 if no valid model specified
		model = "claude-3-5-sonnet-20240620"
	}

	// anthropicFallbackMaxTokens is used only when the caller never set a
	// ceiling at all. It used to be sent unconditionally and then
	// conditionally overridden -- which happened to work today because
	// config.GetMaxTokens always returns a tier default, but it meant a
	// request built outside that path (a direct CompletionRequest, a test, a
	// future caller) silently got 1024 with no link to anything the caller
	// configured. The real ceiling now always wins when it is set; this is
	// only the floor for when it is not.
	const anthropicFallbackMaxTokens = 1024

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = anthropicFallbackMaxTokens
	}

	// cacheBreakpoint marks the last block of the stable prefix so Anthropic's
	// prompt cache can be written and read on repeat calls. Anthropic charges
	// for a cache write on the block it is attached to and reads everything
	// before it for free on a hit, so the mark has to sit at the END of
	// whatever the caller will keep sending byte-for-byte -- putting it
	// earlier caches less than could be cached, putting it on volatile content
	// (the CA-002 split has not landed, so the system prompt today can still
	// carry per-call steering) caches nothing because the block never repeats.
	// Anthropic allows up to four of these; sending one is well inside that.
	cacheBreakpoint := map[string]interface{}{"type": "ephemeral"}

	// Construct messages. When there is a system prompt, it is the last stable
	// block and gets the breakpoint. When there is not, the user message is
	// the only block there is, so the breakpoint goes there instead -- an
	// Anthropic request with no cache_control anywhere never reads or writes
	// the cache at all, silently.
	var messages []map[string]interface{}
	if req.SystemPrompt != "" {
		messages = []map[string]interface{}{
			{
				"role":    "user",
				"content": req.UserPrompt,
			},
		}
	} else {
		messages = []map[string]interface{}{
			{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type":          "text",
						"text":          req.UserPrompt,
						"cache_control": cacheBreakpoint,
					},
				},
			},
		}
	}

	// Construct request body
	requestBody := map[string]interface{}{
		"model":      model,
		"messages":   messages,
		"max_tokens": maxTokens,
	}

	if req.SystemPrompt != "" {
		requestBody["system"] = []map[string]interface{}{
			{
				"type":          "text",
				"text":          req.SystemPrompt,
				"cache_control": cacheBreakpoint,
			},
		}
	}

	if req.Temperature > 0 {
		requestBody["temperature"] = req.Temperature
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", provider.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	// Use a custom HTTP client or default
	client := &http.Client{
		Timeout: provider.config.Timeout,
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("anthropic request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return CompletionResponse{}, fmt.Errorf("anthropic API error (status %d): %s", resp.StatusCode, string(bodyBytes))
	}

	// Parse response
	var response struct {
		ID      string `json:"id"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		// StopReason is Anthropic's finish-reason field. "max_tokens" means the
		// response was cut off at the ceiling, exactly like OpenAI's "length" --
		// it used to be discarded and every response reported "stop"
		// unconditionally, which made a truncated answer indistinguishable from
		// a complete one until it failed to parse three layers away (I-09).
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
		Model string `json:"model"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return CompletionResponse{}, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(response.Content) == 0 {
		return CompletionResponse{}, fmt.Errorf("empty response from Anthropic")
	}

	// Extract text content
	content := ""
	for _, item := range response.Content {
		if item.Type == "text" {
			content += item.Text
		}
	}

	finishReason := response.StopReason
	if finishReason == "" {
		finishReason = "stop"
	}

	return CompletionResponse{
		Content:      content,
		Provider:     provider.Name(),
		Model:        response.Model,
		FinishReason: finishReason,
		Usage: types.TokenUsage{
			PromptTokens:     response.Usage.InputTokens,
			CompletionTokens: response.Usage.OutputTokens,
			TotalTokens:      response.Usage.InputTokens + response.Usage.OutputTokens,
		},
	}, nil
}

// Name returns the provider name
func (provider *AnthropicProvider) Name() string {
	return "anthropic"
}

// RetryPolicy returns provider retry settings.
func (provider *AnthropicProvider) RetryPolicy() (int, time.Duration) {
	return provider.config.MaxRetries, provider.config.RetryBackoff
}

// EstimateCost estimates the cost for Anthropic
func (provider *AnthropicProvider) EstimateCost(req CompletionRequest) float64 {
	inputRate, outputRate := getModelRates("anthropic", req.Model)

	estimatedPromptTokens := len(req.SystemPrompt+req.UserPrompt) / 4
	estimatedCompletionTokens := 500
	if req.MaxTokens > 0 {
		estimatedCompletionTokens = req.MaxTokens
	}

	promptCost := float64(estimatedPromptTokens) * inputRate
	completionCost := float64(estimatedCompletionTokens) * outputRate

	return promptCost + completionCost
}

// OpenRouterProvider implements Provider for OpenRouter
type OpenRouterProvider struct {
	*OpenAICompatibleProvider
}

// customTransport is a http.RoundTripper that adds custom headers
type customTransport struct {
	transport http.RoundTripper
	headers   map[string]string
}

func (t *customTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	return t.transport.RoundTrip(req)
}

// NewOpenAICompatibleProvider creates a provider for OpenAI-compatible chat completion APIs.
func NewOpenAICompatibleProvider(name string, config ProviderConfig) (*OpenAICompatibleProvider, error) {
	if strings.TrimSpace(config.BaseURL) == "" {
		return nil, fmt.Errorf("%s base URL is required", name)
	}
	return newOpenAICompatibleProvider(name, config, "")
}

// Complete sends a completion request to an OpenAI-compatible API.
//
// A typed operation arrives here with a strict JSON schema, and it is sent as
// one. The SDK path this replaced could only ask for "some JSON", so every
// provider other than OpenAI silently lost the contract.
func (provider *OpenAICompatibleProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	return completeViaChatCompletions(ctx, provider.httpClient, provider.name, provider.config, req)
}

// Name returns the provider name.
func (provider *OpenAICompatibleProvider) Name() string {
	return provider.name
}

// RetryPolicy returns provider retry settings.
func (provider *OpenAICompatibleProvider) RetryPolicy() (int, time.Duration) {
	return provider.config.MaxRetries, provider.config.RetryBackoff
}

// EstimateCost estimates the cost for an OpenAI-compatible provider.
func (provider *OpenAICompatibleProvider) EstimateCost(req CompletionRequest) float64 {
	inputRate, outputRate := getModelRates(provider.name, req.Model)

	estimatedPromptTokens := len(req.SystemPrompt+req.UserPrompt) / 4
	estimatedCompletionTokens := 500
	if req.MaxTokens > 0 {
		estimatedCompletionTokens = req.MaxTokens
	}

	promptCost := float64(estimatedPromptTokens) * inputRate
	completionCost := float64(estimatedCompletionTokens) * outputRate

	return promptCost + completionCost
}

// NewOpenRouterProvider creates a new OpenRouter provider
func NewOpenRouterProvider(config ProviderConfig) (*OpenRouterProvider, error) {
	if config.APIKey == "" {
		return nil, fmt.Errorf("OpenRouter API key is required")
	}
	compatible, err := newOpenAICompatibleProvider("openrouter", config, "https://openrouter.ai/api/v1")
	if err != nil {
		return nil, err
	}
	return &OpenRouterProvider{OpenAICompatibleProvider: compatible}, nil
}

// CerebrasProvider implements Provider for Cerebras
type CerebrasProvider struct {
	*OpenAICompatibleProvider
}

// NewCerebrasProvider creates a new Cerebras provider
func NewCerebrasProvider(config ProviderConfig) (*CerebrasProvider, error) {
	if config.APIKey == "" {
		return nil, errors.New("cerebras API key is required")
	}
	compatible, err := newOpenAICompatibleProvider("cerebras", config, "https://api.cerebras.ai/v1")
	if err != nil {
		return nil, err
	}
	return &CerebrasProvider{OpenAICompatibleProvider: compatible}, nil
}

// DeepSeekProvider implements Provider for DeepSeek's OpenAI-compatible API.
type DeepSeekProvider struct {
	*OpenAICompatibleProvider
}

// NewDeepSeekProvider creates a new DeepSeek provider.
func NewDeepSeekProvider(config ProviderConfig) (*DeepSeekProvider, error) {
	if config.APIKey == "" {
		return nil, fmt.Errorf("DeepSeek API key is required")
	}
	compatible, err := newOpenAICompatibleProvider("deepseek", config, "https://api.deepseek.com/v1")
	if err != nil {
		return nil, err
	}
	return &DeepSeekProvider{OpenAICompatibleProvider: compatible}, nil
}

// QwenProvider implements Provider for Qwen via DashScope compatible mode.
type QwenProvider struct {
	*OpenAICompatibleProvider
}

// NewQwenProvider creates a new Qwen provider.
func NewQwenProvider(config ProviderConfig) (*QwenProvider, error) {
	if config.APIKey == "" {
		return nil, errors.New("qwen API key is required")
	}
	compatible, err := newOpenAICompatibleProvider("qwen", config, "https://dashscope-intl.aliyuncs.com/compatible-mode/v1")
	if err != nil {
		return nil, err
	}
	return &QwenProvider{OpenAICompatibleProvider: compatible}, nil
}

// ZAIProvider implements Provider for Z.ai's OpenAI-compatible API.
type ZAIProvider struct {
	*OpenAICompatibleProvider
}

// NewZAIProvider creates a new Z.ai provider.
func NewZAIProvider(config ProviderConfig) (*ZAIProvider, error) {
	if config.APIKey == "" {
		return nil, errors.New("z.ai API key is required")
	}
	compatible, err := newOpenAICompatibleProvider("zai", config, "https://api.z.ai/api/paas/v4")
	if err != nil {
		return nil, err
	}
	return &ZAIProvider{OpenAICompatibleProvider: compatible}, nil
}

// LocalProvider implements Provider for local/mock models
type LocalProvider struct {
	config  ProviderConfig
	handler func(context.Context, CompletionRequest) (string, error)
}

// NewLocalProvider creates a new local/mock provider
func NewLocalProvider(config ProviderConfig) (*LocalProvider, error) {
	return &LocalProvider{
		config: config,
	}, nil
}

// WithHandler sets a custom handler for the local provider
func (provider *LocalProvider) WithHandler(handler func(context.Context, CompletionRequest) (string, error)) *LocalProvider {
	provider.handler = handler
	return provider
}

// Complete processes a request locally
func (provider *LocalProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	var content string
	var err error

	if provider.handler != nil {
		content, err = provider.handler(ctx, req)
		if err != nil {
			return CompletionResponse{}, err
		}
	} else {
		// Default mock behavior for testing
		content = provider.mockResponse(req)
	}

	return CompletionResponse{
		Content:      content,
		Provider:     provider.Name(),
		Model:        "local-model",
		FinishReason: "stop",
		Usage: types.TokenUsage{
			PromptTokens:     len(req.SystemPrompt+req.UserPrompt) / 4,
			CompletionTokens: len(content) / 4,
			TotalTokens:      (len(req.SystemPrompt+req.UserPrompt) + len(content)) / 4,
		},
	}, nil
}

// mockResponse generates a mock response for testing.
//
// The shape comes from what the request declared -- a JSON Schema, or the JSON
// template the prompt-only operations embed in their system prompt -- because
// guessing from keywords is what made this provider answer
// `Mock response for: ...` to nineteen of the forty-five numbered examples. See
// mockshape.go. The keyword branches below remain as the fallback for requests
// that declare neither.
func (provider *LocalProvider) mockResponse(req CompletionRequest) string {
	if shaped := mockShapedResponse(req); shaped != "" {
		return shaped
	}

	// Analyze the prompt to generate appropriate mock responses
	userPrompt := strings.ToLower(req.UserPrompt)

	// Check for extraction patterns
	if strings.Contains(userPrompt, "extract") || strings.Contains(req.SystemPrompt, "extraction") {
		if req.ResponseFormat == "json" {
			return `{"name": "John Doe", "age": 30, "email": "john@example.com"}`
		}
		return "Extracted data: John Doe, 30 years old"
	}

	// Check for validation patterns
	if strings.Contains(userPrompt, "validate") || strings.Contains(req.SystemPrompt, "validation") {
		return `{"valid": true, "issues": [], "confidence": 0.95}`
	}

	// Check for transformation patterns
	if strings.Contains(userPrompt, "transform") || strings.Contains(req.SystemPrompt, "transform") {
		return `{"result": "transformed data", "success": true}`
	}

	// Default response
	if req.ResponseFormat == "json" {
		return `{"response": "Mock response for testing", "success": true}`
	}

	return "Mock response for: " + req.UserPrompt
}

// Name returns the provider name
func (provider *LocalProvider) Name() string {
	return "local"
}

// RetryPolicy returns provider retry settings.
func (provider *LocalProvider) RetryPolicy() (int, time.Duration) {
	return provider.config.MaxRetries, provider.config.RetryBackoff
}

// EstimateCost returns 0 for local provider
func (provider *LocalProvider) EstimateCost(req CompletionRequest) float64 {
	return 0.0
}

// ProviderRegistry manages available providers
type ProviderRegistry struct {
	mu              sync.RWMutex
	providers       map[string]Provider
	factories       map[string]ProviderFactory
	defaultProvider string
}

// NewProviderRegistry creates a new provider registry
func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{
		providers: make(map[string]Provider),
		factories: make(map[string]ProviderFactory),
	}
}

// Register adds a provider to the registry
func (registry *ProviderRegistry) Register(name string, provider Provider) error {
	registry.mu.Lock()
	defer registry.mu.Unlock()

	name = normalizeProviderName(name)
	if provider == nil {
		return fmt.Errorf("provider cannot be nil")
	}
	if name == "" {
		return fmt.Errorf("provider name cannot be empty")
	}
	registry.providers[name] = provider
	if registry.defaultProvider == "" {
		registry.defaultProvider = name
	}
	return nil
}

// RegisterFactory adds a provider factory to the registry.
func (registry *ProviderRegistry) RegisterFactory(name string, factory ProviderFactory) error {
	registry.mu.Lock()
	defer registry.mu.Unlock()

	name = normalizeProviderName(name)
	if factory == nil {
		return fmt.Errorf("provider factory cannot be nil")
	}
	if name == "" {
		return fmt.Errorf("provider name cannot be empty")
	}
	registry.factories[name] = factory
	if registry.defaultProvider == "" {
		registry.defaultProvider = name
	}
	return nil
}

// Get retrieves a provider by name
func (registry *ProviderRegistry) Get(name string) (Provider, error) {
	return registry.Create(name, ProviderConfig{})
}

// Create creates or retrieves a provider by name and configuration.
func (registry *ProviderRegistry) Create(name string, config ProviderConfig) (Provider, error) {
	registry.mu.RLock()
	name = normalizeProviderName(name)
	if name == "" {
		name = registry.defaultProvider
	}

	provider, ok := registry.providers[name]
	factory := registry.factories[name]
	registry.mu.RUnlock()

	if ok {
		return provider, nil
	}
	if factory == nil {
		return nil, fmt.Errorf("provider %s not found", name)
	}
	return factory(config)
}

// SetDefault sets the default provider
func (registry *ProviderRegistry) SetDefault(name string) error {
	registry.mu.Lock()
	defer registry.mu.Unlock()

	name = normalizeProviderName(name)
	if name == "" {
		return fmt.Errorf("provider name cannot be empty")
	}
	if _, ok := registry.providers[name]; !ok {
		if _, ok := registry.factories[name]; !ok {
			return fmt.Errorf("provider %s not found", name)
		}
	}
	registry.defaultProvider = name
	return nil
}

// List returns all registered provider names
func (registry *ProviderRegistry) List() []string {
	registry.mu.RLock()
	defer registry.mu.RUnlock()

	names := make([]string, 0, len(registry.providers)+len(registry.factories))
	for name := range registry.providers {
		names = append(names, name)
	}
	for name := range registry.factories {
		if _, exists := registry.providers[name]; !exists {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// Global provider registry
var globalRegistry = NewProviderRegistry()

// RegisterProvider registers a provider globally
func RegisterProvider(name string, provider Provider) error {
	return globalRegistry.Register(name, provider)
}

// RegisterProviderFactory registers a provider factory globally.
func RegisterProviderFactory(name string, factory ProviderFactory) error {
	return globalRegistry.RegisterFactory(name, factory)
}

// GetProviderFromRegistry gets a provider from the global registry
func GetProviderFromRegistry(name string) (Provider, error) {
	return globalRegistry.Get(name)
}

// CreateProvider creates a provider from the global registry using the supplied configuration.
func CreateProvider(name string, config ProviderConfig) (Provider, error) {
	return globalRegistry.Create(name, config)
}

// SetDefaultProvider sets the global default provider
func SetDefaultProvider(name string) error {
	return globalRegistry.SetDefault(name)
}

// ListProviders returns all globally registered provider names.
func ListProviders() []string {
	return globalRegistry.List()
}

func registerBuiltInProviderFactories() {
	builtIns := map[string]ProviderFactory{
		"openai":     func(config ProviderConfig) (Provider, error) { return NewOpenAIProvider(config) },
		"anthropic":  func(config ProviderConfig) (Provider, error) { return NewAnthropicProvider(config) },
		"openrouter": func(config ProviderConfig) (Provider, error) { return NewOpenRouterProvider(config) },
		"cerebras":   func(config ProviderConfig) (Provider, error) { return NewCerebrasProvider(config) },
		"deepseek":   func(config ProviderConfig) (Provider, error) { return NewDeepSeekProvider(config) },
		"qwen":       func(config ProviderConfig) (Provider, error) { return NewQwenProvider(config) },
		"zai":        func(config ProviderConfig) (Provider, error) { return NewZAIProvider(config) },
		"local":      func(config ProviderConfig) (Provider, error) { return NewLocalProvider(config) },
		"mock":       func(config ProviderConfig) (Provider, error) { return NewLocalProvider(config) },
	}

	for name, factory := range builtIns {
		if err := globalRegistry.RegisterFactory(name, factory); err != nil {
			panic(fmt.Sprintf("failed to register built-in provider %s: %v", name, err))
		}
	}
}

func init() {
	registerBuiltInProviderFactories()
}

// getModelRates returns the input and output cost per token for a given model
// It checks environment variables first, then falls back to provider defaults.
// Environment variables format: SCHEMAFLUX_COST_INPUT_<MODEL> and SCHEMAFLUX_COST_OUTPUT_<MODEL>
// where <MODEL> is the sanitized model name (uppercase, dots/dashes replaced with underscores).
// The value should be the cost per 1,000,000 (1M) tokens.
func getModelRates(provider, model string) (float64, float64) {
	// Check for intelligence level overrides first if the model matches a known level env var
	// Note: This is tricky because we don't know the intelligence level here, only the model name.
	// However, if the user set SCHEMAFLUX_MODEL_SMART="gpt-4", they might also set SCHEMAFLUX_COST_INPUT_SMART.
	// But the request was "map the models and cost/token at the levels in the env vars".
	// This implies we should check SCHEMAFLUX_COST_INPUT_SMART if the current model IS the smart model.
	// Since we don't have the level context here easily without circular dependency or API change,
	// we will stick to model-based lookup which is robust.
	// If the user sets SCHEMAFLUX_MODEL_SMART="my-model", they can set SCHEMAFLUX_COST_INPUT_MY_MODEL.

	// Wait, the user specifically asked: "map the models and cost/token at the levels in the env vars"
	// This could mean: SCHEMAFLUX_COST_INPUT_SMART=30.0
	// If we can't know if "model" is "Smart", we can't apply "Smart" pricing.
	// But wait, the caller of EstimateCost knows the model, but not necessarily the level used to select it.
	// Let's support looking up by the exact model name first (as implemented),
	// AND also support looking up by "SMART", "FAST", "QUICK" if the model name matches one of those keywords (unlikely)
	// OR, we can just rely on the user setting the cost for the *model* they mapped to the level.

	// However, if the user wants to say "Smart level costs X", and they mapped Smart -> GPT-4,
	// they probably want to set cost for GPT-4.
	// BUT, if they want to abstract it:
	// SCHEMAFLUX_MODEL_SMART=gpt-4
	// SCHEMAFLUX_COST_INPUT_SMART=30.0

	// To support this, we'd need to know if 'model' == os.Getenv("SCHEMAFLUX_MODEL_SMART").

	smartModel := os.Getenv("SCHEMAFLUX_MODEL_SMART")
	fastModel := os.Getenv("SCHEMAFLUX_MODEL_FAST")
	quickModel := os.Getenv("SCHEMAFLUX_MODEL_QUICK")

	var levelSuffix string
	if model == smartModel && smartModel != "" {
		levelSuffix = "SMART"
	} else if model == fastModel && fastModel != "" {
		levelSuffix = "FAST"
	} else if model == quickModel && quickModel != "" {
		levelSuffix = "QUICK"
	}

	if levelSuffix != "" {
		inputEnv := os.Getenv(fmt.Sprintf("SCHEMAFLUX_COST_INPUT_%s", levelSuffix))
		outputEnv := os.Getenv(fmt.Sprintf("SCHEMAFLUX_COST_OUTPUT_%s", levelSuffix))

		if inputEnv != "" && outputEnv != "" {
			inputVal, err1 := strconv.ParseFloat(inputEnv, 64)
			outputVal, err2 := strconv.ParseFloat(outputEnv, 64)
			if err1 == nil && err2 == nil {
				return inputVal / 1_000_000, outputVal / 1_000_000
			}
		}
	}

	sanitizedModel := strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(model, "-", "_"), ".", "_"))

	inputEnv := os.Getenv(fmt.Sprintf("SCHEMAFLUX_COST_INPUT_%s", sanitizedModel))
	outputEnv := os.Getenv(fmt.Sprintf("SCHEMAFLUX_COST_OUTPUT_%s", sanitizedModel))

	var inputRate, outputRate float64
	var inputFound, outputFound bool

	if inputEnv != "" {
		if val, err := strconv.ParseFloat(inputEnv, 64); err == nil {
			inputRate = val / 1_000_000
			inputFound = true
		}
	}

	if outputEnv != "" {
		if val, err := strconv.ParseFloat(outputEnv, 64); err == nil {
			outputRate = val / 1_000_000
			outputFound = true
		}
	}

	if inputFound && outputFound {
		return inputRate, outputRate
	}

	// Defaults if not fully specified in env vars
	defaultInput, defaultOutput := getDefaultRates(provider, model)

	if !inputFound {
		inputRate = defaultInput
	}
	if !outputFound {
		outputRate = defaultOutput
	}

	return inputRate, outputRate
}

func getDefaultRates(provider, _ string) (float64, float64) {
	switch provider {
	case "openai":
		// Default to GPT-4 pricing ($30/1M input, $60/1M output)
		return 30.0 / 1_000_000, 60.0 / 1_000_000
	case "anthropic":
		// Claude 3 Opus: $15/1M, $75/1M
		return 15.0 / 1_000_000, 75.0 / 1_000_000
	case "openrouter":
		// Generic: $1/1M, $2/1M
		return 1.0 / 1_000_000, 2.0 / 1_000_000
	case "cerebras":
		// Cheap: $0.10/1M
		return 0.10 / 1_000_000, 0.10 / 1_000_000
	default:
		return 0, 0
	}
}
