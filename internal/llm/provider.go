package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
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

// StreamChunk is one event from a streaming completion.
type StreamChunk struct {
	// Delta is the incremental text this event adds. Empty on the final
	// event and on any event that carries no text of its own.
	Delta string

	// Done marks the final event of a successful stream. Response is
	// populated only here -- a chunk is either an incremental Delta or the
	// terminal Done carrying the whole answer, never both, and a stream
	// that ends any other way (see StreamingProvider) yields an error
	// instead of a Done chunk, never a Done chunk built from whatever text
	// happened to arrive before it broke.
	Done bool

	// Response is the accumulated completion. Its Content is the full
	// answer, not just the last Delta, so a caller that only wants the
	// whole text never has to concatenate deltas by hand.
	Response CompletionResponse
}

// StreamingProvider is implemented by a Provider that can stream a
// completion.
//
// It is deliberately not a method on Provider itself. Adding a required
// method there breaks every existing implementation of Provider in this
// repository -- schemafluxtest's Provider and Player/Recorder, mw's
// wrappers, and every mock a test constructs -- none of which this change
// may touch. A caller that wants to stream type-asserts a Provider for this
// interface; a provider that does not implement it is a provider that
// cannot stream, and the caller is told so via KindUnsupportedCapability
// rather than being handed a buffered Complete dressed up as a stream of
// one chunk, which would hide the difference between "this provider streams"
// and "this provider doesn't" behind an identical-looking success.
type StreamingProvider interface {
	Provider

	// CompleteStream streams a completion.
	//
	// A stream that fails partway has delivered a partial answer, and a
	// partial answer is a failure, not a short success: the sequence never
	// yields a Done chunk built from an incomplete response, and any error
	// -- before the first byte, mid-stream, or a connection that closes
	// with no terminal event at all -- is reported as an error, not folded
	// into whatever text arrived first.
	CompleteStream(ctx context.Context, req CompletionRequest) iter.Seq2[StreamChunk, error]
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

	// Tools lists the functions the model may call instead of answering
	// directly. Omitted from the wire request entirely when empty -- a
	// caller who never mentions tools gets the exact request this library
	// sent before tool calling existed, not an empty tools array that some
	// vendor implementation might treat differently from no array at all.
	Tools []Tool

	// ToolChoice steers whether and which tool the model must use. Accepted
	// values mirror the Responses API: "auto" (the model decides, the
	// default when this is empty), "none" (never call a tool), "required"
	// (call some tool), or the exact Name of one of Tools to force that one.
	// Any other value that names one of Tools is sent as a forced choice;
	// forcing a name that is not in Tools is a caller bug caught by
	// buildResponsesRequestBody's caller before the request is ever sent --
	// see Complete.
	ToolChoice string

	// Messages, when non-empty, carries an explicit multi-turn transcript
	// and takes priority over SystemPrompt/UserPrompt for the "input" the
	// Responses API receives: SystemPrompt still becomes "instructions" (the
	// Responses API keeps that separate from the turn history), but the
	// turn-by-turn conversation is Messages, in order, oldest first.
	//
	// Left empty, a request behaves exactly as it did before this field
	// existed: SystemPrompt/UserPrompt build the single-turn request. This is
	// additive infrastructure for a caller building its own multi-turn loop
	// (see internal/ops/session.go) -- it is not wired into any operation in
	// this library today, so PromptCacheKeyFor's stable-prefix property for
	// system+template is unaffected by its mere existence.
	Messages []Message
}

// Tool describes one function the model may call. It is a declaration, not
// an invocation -- the library never executes it. A ToolCall the model
// returns is checked against the Tools a request actually offered (see
// ErrUnrequestedTool); a tool the caller never declared here can never be
// legitimately requested back.
type Tool struct {
	// Name identifies the tool. It is what a ToolCall.Name must match.
	Name string

	// Description tells the model when to use this tool. Prose, not a
	// contract -- the schema is what Parameters enforces.
	Description string

	// Parameters is the JSON Schema (as a map, exactly like
	// CompletionRequest.JSONSchema) describing the arguments the model must
	// supply. A nil value is sent as an empty-object schema, which is the
	// Responses API's spelling for "this tool takes no arguments."
	Parameters map[string]any
}

// Message is one turn of an explicit conversation transcript. Role follows
// the Responses API's own vocabulary ("system", "user", "assistant", or
// "tool"); Content is the turn's text. ToolCallID identifies which prior
// assistant ToolCall a "tool" role message is answering -- required by the
// API to line a tool result up with the call that asked for it.
type Message struct {
	Role       string
	Content    string
	ToolCallID string
}

// ToolCall is a request FROM the model to run one of the Tools a
// CompletionRequest offered. It is never executed by this library -- Complete
// returns it inside CompletionResponse.ToolCalls and it is the caller's job
// to run the tool (or refuse to) and feed the result back as a Message with
// Role "tool" and this call's ID as ToolCallID.
//
// Arguments is the model's raw JSON payload, untouched. It is built from
// whatever the model produced against the caller's Parameters schema, which
// makes it exactly as untrusted as any other model output -- this library
// decodes it into nothing narrower than raw bytes, on purpose, so the caller
// decodes it with the validation appropriate to their own tool rather than
// this library silently making that decision through a generic decode step
// that a malformed or adversarial payload could exploit differently than the
// caller expects.
type ToolCall struct {
	// ID identifies this specific call so a later tool-result Message can be
	// correlated back to it. The Responses API calls this call_id.
	ID string

	// Name is the tool the model wants to call. Checked against the Tools a
	// request offered before this ever reaches a caller -- see
	// ErrUnrequestedTool.
	Name string

	// Arguments is the model's raw, undecoded JSON argument payload. Never
	// logged, never embedded in an error: it is built from the caller's own
	// schema against the caller's own data, exactly as unlogged as any other
	// payload this library moves between the caller and the model.
	Arguments json.RawMessage
}

// CompletionResponse represents a unified response format
type CompletionResponse struct {
	Content      string
	Usage        types.TokenUsage
	Model        string
	Provider     string
	FinishReason string

	// ToolCalls holds the tool calls the model asked for instead of (or
	// alongside) answering in Content. Empty on every response that did not
	// request a tool -- a plain text response is unaffected by this field's
	// existence, and a caller that never passes Tools in the request never
	// has to look at it. See ToolCall for why the arguments inside are raw.
	ToolCalls []ToolCall
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

	// HTTPClient lets the caller supply their own *http.Client -- a proxy, an
	// mTLS transport, request instrumentation, or a transport that fails on
	// dial for a test. SC-007: before this field existed, every constructor
	// in this file built its own `&http.Client{Timeout: config.Timeout}`
	// with no injection point, so a caller who needed any of that had no way
	// to get it short of forking the library.
	//
	// The caller owns it: nil (the default) preserves exactly today's
	// behaviour -- this library builds one *http.Client per provider,
	// scoped to config.Timeout, precisely as it always did. A non-nil value
	// is used as-is; this library never mutates its Timeout, Jar, or
	// CheckRedirect, and never closes it. The one exception is ExtraHeaders:
	// if also set, the client actually used is a shallow copy with Transport
	// wrapped to add those headers, so the caller's Transport (and whatever
	// pooling or instrumentation it does) still runs underneath -- the
	// original *http.Client the caller passed in is never written to.
	HTTPClient *http.Client

	// EndpointPolicy, when non-nil, is checked against BaseURL before any
	// client is built (SEC-004's ValidateEndpoint, internal/types/
	// endpointpolicy.go). nil is the default and means no policy is
	// enforced -- today's behaviour, where BaseURL is used exactly as given.
	// SEC-004 documented this as the gap: the check existed with nothing
	// calling it. A caller who wants the loopback/link-local/cloud-metadata
	// refusal (or a host/scheme allowlist) opts in by setting this; nothing
	// is refused by default, because an EndpointPolicy left at its own zero
	// value refuses every scheme and host (see EndpointPolicy's doc
	// comment), and enforcing an implicit empty policy on every caller who
	// never mentioned one would reject every configured BaseURL that exists
	// in this codebase's own tests today.
	EndpointPolicy *types.EndpointPolicy
}

// resolveHTTPClient returns the *http.Client a provider should use for
// config, applying SC-007's ownership rule: a caller-supplied client is
// never replaced, and a caller who supplies nothing gets exactly what every
// provider built before this field existed -- one *http.Client scoped to
// config.Timeout.
//
// ExtraHeaders is layered on top either way. When config.HTTPClient is set,
// this returns a shallow copy with Transport wrapped around whatever
// Transport the caller configured (http.DefaultTransport if they left it
// nil) rather than discarding it -- the caller's own *http.Client value is
// never mutated, only read.
func resolveHTTPClient(config ProviderConfig) *http.Client {
	if config.HTTPClient != nil {
		if len(config.ExtraHeaders) == 0 {
			return config.HTTPClient
		}
		wrapped := *config.HTTPClient
		base := wrapped.Transport
		if base == nil {
			base = http.DefaultTransport
		}
		wrapped.Transport = &customTransport{transport: base, headers: config.ExtraHeaders}
		return &wrapped
	}

	client := &http.Client{Timeout: config.Timeout}
	if len(config.ExtraHeaders) > 0 {
		client.Transport = &customTransport{transport: http.DefaultTransport, headers: config.ExtraHeaders}
	}
	return client
}

// validateConfiguredEndpoint enforces config.EndpointPolicy against
// config.BaseURL, when a policy was supplied. Called once per provider
// construction, before any client or request is built, so a refused
// endpoint fails configuration rather than the first request. See
// ProviderConfig.EndpointPolicy for why nil means unenforced rather than
// "refuse everything."
func validateConfiguredEndpoint(providerName string, config ProviderConfig) error {
	if config.EndpointPolicy == nil {
		return nil
	}
	if err := config.EndpointPolicy.ValidateEndpoint(config.BaseURL); err != nil {
		return fmt.Errorf("configuring provider %q: %w", providerName, err)
	}
	return nil
}

// ProviderFactory creates a provider from configuration.
type ProviderFactory func(config ProviderConfig) (Provider, error)

// ErrNoMessageOutput reports a response that carried no assistant message.
// It is deterministic, not transient: a reasoning-only response or an empty
// output list will be identical on retry, so the retry classifier must not
// spend a backoff cycle on it.
var ErrNoMessageOutput = errors.New("response carried no message output")

// ErrUnrequestedTool reports a tool call naming something the caller never
// declared in CompletionRequest.Tools. A model asking for a tool it was
// never offered is a failure the library catches in Go, not something that
// reaches the caller looking like a legitimate request to act on -- the
// caller's Tools list is the only source of truth for what may be called,
// and this is deterministic: the same offered/requested mismatch fails the
// same way on retry, so it is not worth spending a backoff cycle on.
var ErrUnrequestedTool = errors.New("model requested a tool that was not offered")

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
	if err := validateConfiguredEndpoint("openai", config); err != nil {
		return nil, err
	}

	client, config, err := newOpenAIClient(config, "")
	if err != nil {
		return nil, fmt.Errorf("OpenAI %w", err)
	}

	return &OpenAIProvider{
		client: client,
		// One client per provider, resolved by resolveHTTPClient: the
		// caller's own *http.Client (SC-007) when config.HTTPClient is set,
		// or the same `&http.Client{Timeout: config.Timeout}` this always
		// built otherwise. A fresh http.Client per request defeats
		// connection reuse and HTTP/2 multiplexing; the per-request deadline
		// rides on the context instead.
		httpClient: resolveHTTPClient(config),
		config:     config,
	}, nil
}

// responsesAPIResponse is the shape of a POST /v1/responses payload -- the
// body of a buffered call, and the payload the "response.completed" /
// "response.incomplete" SSE events carry in a streamed one. Both paths
// decode into this one type instead of two independently-maintained shapes,
// so a field the streaming event forgets to mirror is a compile error
// instead of a silent gap between "streamed" and "buffered" behaviour.
type responsesAPIResponse struct {
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
		// The fields below are populated only on a "function_call" output
		// item; a "message" item leaves them at their zero value. CallID is
		// the Responses API's name for what ToolCall.ID must carry so a
		// later tool-result message correlates to this exact call -- ID is
		// kept too because some payload shapes carry the item's own id there
		// instead, and preferring CallID with ID as a fallback (see
		// toolCalls) means a payload variance in this field is not a parse
		// failure.
		ID        string `json:"id"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
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

// content extracts the answer only. A reasoning model returns `reasoning`
// items alongside the `message` item; concatenating every item prefixes the
// answer with reasoning prose, which corrupts a JSON payload.
func (r *responsesAPIResponse) content() string {
	var builder strings.Builder
	for _, output := range r.Output {
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
	return builder.String()
}

// toolCalls extracts every "function_call" output item. A reasoning model
// or a tool-calling model can emit these alongside or instead of a
// "message" item, and unlike content() this does not require the payload
// well-formed enough to decode arguments -- Arguments is carried through
// exactly as the API sent it, because decoding it is the caller's job (see
// ToolCall).
func (r *responsesAPIResponse) toolCalls() []ToolCall {
	var calls []ToolCall
	for _, output := range r.Output {
		if output.Type != "function_call" {
			continue
		}
		id := output.CallID
		if id == "" {
			id = output.ID
		}
		calls = append(calls, ToolCall{
			ID:        id,
			Name:      output.Name,
			Arguments: json.RawMessage(output.Arguments),
		})
	}
	return calls
}

// finishReason derives the finish reason the same way for a buffered
// response and a streamed one's terminal event -- a truncated answer must
// be distinguishable from a complete one in both.
func (r *responsesAPIResponse) finishReason() string {
	if r.Status != "incomplete" {
		return "stop"
	}
	if r.IncompleteReason.Reason != "" {
		return r.IncompleteReason.Reason
	}
	return "incomplete"
}

func (r *responsesAPIResponse) toCompletionResponse(providerName string) CompletionResponse {
	content := r.content()
	calls := r.toolCalls()

	finishReason := r.finishReason()
	if content == "" && len(calls) > 0 && finishReason == "stop" {
		// "stop" is technically accurate (the response completed normally)
		// but indistinguishable from a genuine empty text answer. A caller
		// switching on FinishReason -- as this library's own
		// ClassifyCompletion does for truncation -- needs to be able to
		// tell "the model answered with nothing" apart from "the model
		// asked for a tool instead of answering."
		finishReason = "tool_calls"
	}

	return CompletionResponse{
		Content:      content,
		ToolCalls:    calls,
		Provider:     providerName,
		Model:        r.Model,
		FinishReason: finishReason,
		Usage: types.TokenUsage{
			PromptTokens:     r.Usage.InputTokens,
			CompletionTokens: r.Usage.OutputTokens,
			TotalTokens:      r.Usage.TotalTokens,
			CachedTokens:     r.Usage.InputTokensDetails.CachedTokens,
			CacheWriteTokens: r.Usage.InputTokensDetails.CacheWriteTokens,
			ReasoningTokens:  r.Usage.OutputTokensDetails.ReasoningTokens,
		},
	}
}

// classifyResponsesResult turns a decoded Responses API payload into either a
// usable CompletionResponse or the error describing why it is not one.
//
// Both Complete and CompleteStream call this for the identical payload
// shape -- a buffered response body and a streamed one's terminal event --
// which is what keeps a streamed failure classified the same way as a
// buffered one for the same cause: there is only one function that decides,
// not a second opinion that only runs on the streaming path.
//
// offeredTools is the Tools list from the request that produced r. A
// response that carries a tool call is not a parse failure -- content()
// being empty because the answer is a function_call item, not a message
// item, used to fall straight into ErrNoMessageOutput below, which
// misreported a legitimate tool-call response as a malformed one. It is
// still checked against offeredTools before it reaches the caller: a call
// naming something the caller never declared is refused here, in Go,
// instead of being handed to a caller who might mistake "the model
// mentioned a tool" for "the model may run one."
func classifyResponsesResult(r *responsesAPIResponse, providerName string, offeredTools []Tool) (CompletionResponse, error) {
	if len(r.Output) == 0 {
		return CompletionResponse{}, fmt.Errorf("openai: %w", ErrNoMessageOutput)
	}

	calls := r.toolCalls()

	if r.content() == "" && len(calls) == 0 {
		if r.Status == "incomplete" && r.IncompleteReason.Reason != "" {
			return CompletionResponse{}, fmt.Errorf("OpenAI response incomplete: %s", r.IncompleteReason.Reason)
		}
		return CompletionResponse{}, fmt.Errorf("openai: %w", ErrNoMessageOutput)
	}

	if len(calls) > 0 {
		if err := validateToolCalls(calls, offeredTools); err != nil {
			return CompletionResponse{}, err
		}
	}

	return r.toCompletionResponse(providerName), nil
}

// validateToolCalls refuses any call naming a tool the request never
// offered. It compares by Name only -- offeredTools is what CompletionRequest
// declared, calls is what the model sent back -- and never touches or
// mentions a call's Arguments, which are the caller's data.
func validateToolCalls(calls []ToolCall, offeredTools []Tool) error {
	offered := make(map[string]struct{}, len(offeredTools))
	for _, tool := range offeredTools {
		offered[tool.Name] = struct{}{}
	}
	for _, call := range calls {
		if _, ok := offered[call.Name]; !ok {
			return fmt.Errorf("openai: %w: %q", ErrUnrequestedTool, call.Name)
		}
	}
	return nil
}

// buildResponsesRequestBody constructs the JSON body shared by the buffered
// and streamed calls to the Responses API. `stream` adds the one field that
// turns a normal response into an SSE one; everything else -- schema,
// reasoning controls, cache key -- is built identically for both, because a
// caller streaming the exact same request as a buffered call must produce
// the exact same request on the wire.
func (provider *OpenAIProvider) buildResponsesRequestBody(req CompletionRequest, stream bool) map[string]interface{} {
	// input is either the single UserPrompt string (unchanged from before
	// Messages existed) or, when the caller supplied an explicit transcript,
	// an ordered array of role/content turns. SystemPrompt still becomes
	// "instructions" either way -- the Responses API keeps steering separate
	// from the turn history, so a caller building a transcript is not also
	// responsible for re-injecting the system prompt as message zero.
	var input interface{}
	if len(req.Messages) > 0 {
		turns := make([]map[string]interface{}, 0, len(req.Messages))
		for _, msg := range req.Messages {
			turn := map[string]interface{}{
				"role":    msg.Role,
				"content": msg.Content,
			}
			if msg.ToolCallID != "" {
				turn["call_id"] = msg.ToolCallID
			}
			turns = append(turns, turn)
		}
		input = turns
	} else {
		userInput := req.UserPrompt
		if req.ResponseFormat == "json" && !strings.Contains(strings.ToLower(userInput), "json") {
			userInput = "Return valid JSON only.\n\n" + userInput
		}
		input = userInput
	}

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
	if stream {
		requestBody["stream"] = true
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

	// Tools/ToolChoice are omitted entirely rather than sent empty/"auto" by
	// default, so a request that never mentions tools produces the exact
	// same wire body it produced before tool calling existed.
	if len(req.Tools) > 0 {
		tools := make([]map[string]interface{}, 0, len(req.Tools))
		for _, tool := range req.Tools {
			parameters := tool.Parameters
			if parameters == nil {
				parameters = map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				}
			}
			tools = append(tools, map[string]interface{}{
				"type":        "function",
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  parameters,
			})
		}
		requestBody["tools"] = tools
	}
	if req.ToolChoice != "" {
		switch req.ToolChoice {
		case "auto", "none", "required":
			requestBody["tool_choice"] = req.ToolChoice
		default:
			// Anything else names one specific tool to force.
			requestBody["tool_choice"] = map[string]interface{}{
				"type": "function",
				"name": req.ToolChoice,
			}
		}
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
	return requestBody
}

// newResponsesHTTPRequest builds the outgoing HTTP request for either call
// shape, sharing header and URL construction so the two cannot drift.
func (provider *OpenAIProvider) newResponsesHTTPRequest(ctx context.Context, req CompletionRequest, stream bool) (*http.Request, error) {
	url := "https://api.openai.com/v1/responses"
	if provider.config.BaseURL != "" {
		url = strings.TrimRight(provider.config.BaseURL, "/") + "/responses"
	}

	jsonData, err := json.Marshal(provider.buildResponsesRequestBody(req, stream))
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+provider.config.APIKey)
	if provider.config.OrgID != "" {
		httpReq.Header.Set("OpenAI-Organization", provider.config.OrgID)
	}
	if stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}
	return httpReq, nil
}

// Complete sends a completion request to OpenAI using the Responses API
func (provider *OpenAIProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	// Use the new Responses API (POST /v1/responses)
	// Since go-openai v1.20.4 doesn't support this endpoint, we implement it manually.
	httpReq, err := provider.newResponsesHTTPRequest(ctx, req, false)
	if err != nil {
		return CompletionResponse{}, err
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

	var response responsesAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return CompletionResponse{}, fmt.Errorf("failed to decode response: %w", err)
	}

	return classifyResponsesResult(&response, provider.Name(), req.Tools)
}

// ErrStreamIncomplete reports a stream that ended without either a terminal
// event or a transport error -- the connection simply stopped. It is not a
// truncation (that arrives as a normal terminal event carrying
// finish_reason "length"/"incomplete", classified by ClassifyCompletion
// exactly like a buffered truncated response) and it is not a decodable
// provider error either: something between here and the provider dropped
// the rest of the answer, and the partial text collected so far must never
// be handed back as if it were the complete one.
var ErrStreamIncomplete = errors.New("stream ended before a completion event arrived")

// CompleteStream implements StreamingProvider for the Responses API.
//
// The returned sequence is pull-based end to end: nothing is read from the
// socket ahead of the consumer's next iteration of the range loop, because
// each SSE frame is decoded and handed to `yield` synchronously from inside
// the same call stack that is blocked reading the socket. A consumer that
// stops ranging (a `break`) makes the next `yield` call return false, which
// this stops on immediately -- there is no separate goroutine here to leak
// and no read-ahead buffer to drain.
func (provider *OpenAIProvider) CompleteStream(ctx context.Context, req CompletionRequest) iter.Seq2[StreamChunk, error] {
	return func(yield func(StreamChunk, error) bool) {
		httpReq, err := provider.newResponsesHTTPRequest(ctx, req, true)
		if err != nil {
			yield(StreamChunk{}, err)
			return
		}

		resp, err := provider.httpClient.Do(httpReq)
		if err != nil {
			yield(StreamChunk{}, fmt.Errorf("OpenAI stream request failed: %w", err))
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			// Classified exactly like Complete's non-200 path -- the status
			// line arrives before any SSE framing, so a rejected request
			// looks identical to a caller whether or not they asked to
			// stream it.
			bodyBytes, _ := io.ReadAll(resp.Body)
			if isRateLimited(resp.StatusCode) {
				yield(StreamChunk{}, rateLimitError("openai", req.Model, resp, string(bodyBytes)))
				return
			}
			yield(StreamChunk{}, NewAPIError("openai", req.Model, resp.StatusCode, string(bodyBytes)))
			return
		}

		scanner := bufio.NewScanner(resp.Body)
		// SSE frames are small individually, but a "response.completed"
		// event carries the whole answer as one line; the default 64KiB
		// token limit truncates any answer longer than that silently inside
		// bufio, which would look like a parse error three layers away. 4MiB
		// matches the largest answer this library's own tier ceilings can
		// produce.
		scanner.Buffer(make([]byte, 64*1024), 4<<20)

		var dataLines []string
		terminal := false

		// flush decodes one complete SSE event (the data: lines collected
		// since the last blank line) and dispatches it. It returns false
		// when the consumer asked to stop (yield returned false) so the
		// caller can unwind without reading further.
		flush := func() bool {
			if len(dataLines) == 0 {
				return true
			}
			payload := strings.Join(dataLines, "\n")
			dataLines = nil
			if payload == "[DONE]" {
				return true
			}

			var envelope struct {
				Type     string                `json:"type"`
				Delta    string                `json:"delta"`
				Response *responsesAPIResponse `json:"response"`
				Error    *struct {
					Message string `json:"message"`
					Type    string `json:"type"`
					Code    any    `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
				// A frame that is not JSON at all is a framing bug, not a
				// content decision -- report it rather than skip it.
				terminal = true
				return yield(StreamChunk{}, fmt.Errorf("openai: could not parse stream event: %w", err))
			}

			switch envelope.Type {
			case "response.output_text.delta":
				if envelope.Delta == "" {
					return true
				}
				return yield(StreamChunk{Delta: envelope.Delta}, nil)

			case "response.completed", "response.incomplete":
				terminal = true
				if envelope.Response == nil {
					return yield(StreamChunk{}, fmt.Errorf("openai: %s carried no response", envelope.Type))
				}
				completion, err := classifyResponsesResult(envelope.Response, provider.Name(), req.Tools)
				if err != nil {
					return yield(StreamChunk{}, err)
				}
				return yield(StreamChunk{Done: true, Response: completion}, nil)

			case "response.failed", "error":
				terminal = true
				apiErr := NewAPIError("openai", req.Model, 0, payload)
				if envelope.Error != nil && apiErr.Message == "" {
					apiErr.Message = truncateMessage(envelope.Error.Message)
					apiErr.Type = envelope.Error.Type
					apiErr.Code = scalarString(envelope.Error.Code)
				}
				return yield(StreamChunk{}, apiErr)

			default:
				// Every other event type (response.created,
				// response.output_item.added, reasoning deltas, ...) carries
				// nothing this library surfaces yet. Skipping it is not the
				// same as skipping a delta or a terminal event -- those two
				// are the only kinds that carry the answer.
				return true
			}
		}

		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				if !flush() {
					return
				}
				if terminal {
					return
				}
				continue
			}
			if after, ok := strings.CutPrefix(line, "data:"); ok {
				dataLines = append(dataLines, strings.TrimPrefix(after, " "))
			}
			// Any other field (event:, id:, retry:, or a comment line
			// starting with ':') is SSE framing this library does not need
			// -- dispatch reads the event's own "type" from the JSON
			// payload, not the SSE "event:" line, so the two cannot disagree.
		}

		if err := scanner.Err(); err != nil {
			yield(StreamChunk{}, fmt.Errorf("openai stream read failed: %w", err))
			return
		}

		// The body ended; flush whatever the final blank-line-less frame
		// held (a well-formed SSE stream always ends its last event with a
		// blank line, but this guards a server that does not).
		if !flush() {
			return
		}
		if !terminal {
			// The connection closed cleanly with neither a terminal event
			// nor a transport error. Any deltas already yielded were real,
			// but there is no Done chunk to hand back -- that is exactly the
			// "partial answer presented as a short success" this interface
			// exists to prevent.
			yield(StreamChunk{}, fmt.Errorf("openai: %w", ErrStreamIncomplete))
		}
	}
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
	if err := validateConfiguredEndpoint(name, config); err != nil {
		return nil, err
	}

	// newOpenAIClient is still what validates the key and settles the base URL,
	// even though the SDK client it builds is no longer used to send the call.
	_, config, err := newOpenAIClient(config, defaultBaseURL)
	if err != nil {
		return nil, fmt.Errorf("%s %w", name, err)
	}

	return &OpenAICompatibleProvider{
		name: normalizeProviderName(name),
		// resolveHTTPClient (SC-007): the caller's own *http.Client when
		// config.HTTPClient is set, otherwise the same
		// `&http.Client{Timeout: config.Timeout}` this always built.
		httpClient: resolveHTTPClient(config),
		config:     config,
	}, nil
}

// AnthropicProvider implements Provider for Anthropic Claude
type AnthropicProvider struct {
	config     ProviderConfig
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

// NewAnthropicProvider creates a new Anthropic provider
func NewAnthropicProvider(config ProviderConfig) (*AnthropicProvider, error) {
	if config.APIKey == "" {
		return nil, fmt.Errorf("anthropic API key is required")
	}
	if err := validateConfiguredEndpoint("anthropic", config); err != nil {
		return nil, err
	}

	baseURL := config.BaseURL
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}

	return &AnthropicProvider{
		config:  config,
		apiKey:  config.APIKey,
		baseURL: baseURL,
		// resolveHTTPClient (SC-007). This used to be built fresh inside
		// Complete on every call -- `client := &http.Client{Timeout:
		// provider.config.Timeout}` -- which defeated connection reuse the
		// same way OpenAIProvider's own doc comment warns against, and gave
		// a caller-supplied client (once this field existed to accept one)
		// nowhere to land, since a fresh internal client was built
		// regardless of what the caller configured.
		httpClient: resolveHTTPClient(config),
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

	// provider.httpClient (SC-007, resolveHTTPClient at construction): the
	// caller's own *http.Client when supplied, otherwise the same
	// `&http.Client{Timeout: provider.config.Timeout}` this used to build
	// fresh on every call here -- which defeated connection reuse and gave a
	// caller-supplied client nowhere to land.
	resp, err := provider.httpClient.Do(httpReq)
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
