package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/types"
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
	Model        string
	SystemPrompt string
	UserPrompt   string
	// Temperature is the sampling temperature. Zero means unset: the field is
	// left off the request and the provider's own default applies, so a caller
	// cannot ask for temperature zero through it. A model that does not take
	// the parameter is sent none whatever this holds.
	Temperature    float64
	MaxTokens      int
	ResponseFormat string // "json" or "text"

	// JSONSchema, when set, is sent as the Responses API's json_schema strict
	// format. The library generates an exact schema for the target type and
	// used to render it into the prompt as prose and ask only for a json_object,
	// so the one artifact that could make the result structurally guaranteed
	// never reached the API that can enforce it.
	//
	// The map is read-only, and it is shared: the library hands every request
	// for the same Go type the one map it worked out for the type, to save
	// building it again for each call, and it may be read by several requests
	// at once. A Provider or middleware that needs a changed schema copies it
	// first. Changing it in place would alter the schema of every later request
	// for the type, and is a data race against the requests running now.
	JSONSchema map[string]any

	// SchemaName names the schema in the request. It is required by the API.
	SchemaName string

	// ReasoningEffort is how much the model should reason before answering:
	// "none", "low", "medium" and so on, as the model's family names them.
	// Empty leaves the request as it was before this field existed.
	//
	// ReasoningEffortPreferred says whose wish it is. False means the caller
	// asked for it: a provider that cannot honour it refuses the request. True
	// means the operation would like it: a provider that cannot honour it
	// ignores it. See resolveEffort.
	ReasoningEffort          string
	ReasoningEffortPreferred bool

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
	//
	// The Responses API path and the chat-completions providers send them. A
	// provider that cannot (Anthropic, the local provider, the codex transport and
	// the decision providers) refuses a request that sets Tools, ToolChoice or
	// Messages with KindUnsupportedCapability before anything is sent or run, and
	// never answers it as though they were not there. A reply that is only tool
	// calls is a complete answer: Content empty, CompletionResponse.ToolCalls set.
	Tools []Tool

	// ToolChoice steers whether and which tool the model must use. Accepted
	// values mirror the Responses API: "auto" (the model decides, the
	// default when this is empty), "none" (never call a tool), "required"
	// (call some tool), or the exact Name of one of Tools to force that one.
	// Any other value is a name that is not among Tools, which is a caller bug
	// (a misspelling, a stray space): newResponsesHTTPRequest refuses it with
	// KindInvalidRequest before the request is sent.
	ToolChoice string

	// WebSearch declares OpenAI's built-in web_search tool on the request,
	// letting the model search the live web before answering. Unlike Tools,
	// nothing comes back for the caller to execute -- the provider runs the
	// search itself and the answer arrives as an ordinary message (the
	// search-call output items are skipped by content(), exactly as
	// reasoning items are). Whether the model actually searches is its own
	// call unless ToolChoice forces it; declaring the tool is the only way
	// the model gets ANY web access at all.
	//
	// False produces the exact wire body this library sent before the field
	// existed. Providers without a built-in search tool (the Anthropic path, the
	// chat-completions providers, the local provider and the codex transport)
	// refuse a request that sets it.
	WebSearch bool

	// Messages, when non-empty, carries an explicit multi-turn transcript
	// and takes priority over SystemPrompt/UserPrompt for the "input" the
	// Responses API receives: SystemPrompt still becomes "instructions" (the
	// Responses API keeps that separate from the turn history), but the
	// turn-by-turn conversation is Messages, in order, oldest first.
	//
	// Left empty, a request behaves exactly as it did before this field
	// existed: SystemPrompt/UserPrompt build the single-turn request. A provider
	// that cannot carry a transcript refuses a request that sets it, as it does
	// for Tools. An assistant turn that called tools is written with those calls,
	// and a tool turn is written as the result of the call it names, in the shape
	// each wire defines.
	Messages []Message

	// Decision, when not nil, says what is being decided, typed: the subject and
	// the questions, each a yes or no, a choice among labels or a level on a
	// scale. An operation whose answer is bounded fills it, beside the prompts
	// it has always sent, which ask the same thing of a model that writes JSON.
	//
	// A generating provider does not read it and answers the prompts: nothing
	// it is sent changes with this field. A decision provider reads it, never
	// the prompts, and answers in CompletionResponse.DecisionAnswers; a request
	// that carries none asks it for text, which it refuses as an unsupported
	// capability before anything is sent (DecisionLimits.Check). What the
	// pointer leads to is read-only and is shared between calls.
	Decision *Decision
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

	// ToolCalls are the calls the model made in this turn, on an assistant turn
	// that asked for tools. The result of each is a later turn of the role "tool"
	// naming the call's ID. A provider writes them back into the conversation so
	// that a result is an answer to a call the model is shown it made; without
	// them a tool turn answers nothing. Empty on every other turn.
	ToolCalls []ToolCall
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

	// UpstreamProvider is the provider behind Provider, when Provider is a
	// gateway that says which one served the request: OpenRouter answers with
	// a top-level "provider" naming the vendor it sent the request to. It is
	// empty when the response names none, which is not the same as the request
	// having gone nowhere else. Provider stays the name of the provider this
	// library handed the request to, because pricing, call records and the
	// answered route are keyed by it; this field is what a data policy reads to
	// see where the request went from there.
	UpstreamProvider string

	// ToolCalls holds the tool calls the model asked for instead of (or
	// alongside) answering in Content. Empty on every response that did not
	// request a tool -- a plain text response is unaffected by this field's
	// existence, and a caller that never passes Tools in the request never
	// has to look at it. See ToolCall for why the arguments inside are raw.
	ToolCalls []ToolCall

	// Cached reports that this response was served from a response cache and
	// no provider was called for it. A cache sets it on a hit. Nothing was
	// spent on such a response, and the accounting reads this flag to know:
	// the usage on a hit is zero, and zero usage on its own is recorded as
	// spend of unknown size.
	Cached bool

	// ServiceTier is the service tier the provider says it served this answer
	// on, when it says. It can differ from the one that was asked for.
	ServiceTier string

	// Hedged reports that a second copy of the request was sent while the
	// first was still in flight, whichever copy answered. mw.Hedge sets it. A
	// hedged request can be billed twice, and the call record says that it
	// was hedged.
	Hedged bool

	// Timing is where the time of the HTTP call went (LAT-001): the lookup,
	// the connection, the handshake, the wait for the first byte and the
	// reading of the body. A provider that does not time its calls leaves it
	// at zero, which is not a measurement of an instant call (Measured).
	Timing types.Timing

	// DecisionAnswers are a decision provider's answers to the questions of
	// CompletionRequest.Decision: one for each question, in the questions'
	// order, each with the probability the model reported for it when it
	// reports one. A generating provider leaves it nil and answers in Content.
	// A decision provider leaves Content empty: the call path writes the form
	// the operations read from these (RenderDecisionAnswers), and refuses an
	// answer that is not one to its question.
	DecisionAnswers []DecisionAnswer

	// DecisionEscalation is set when a decision provider was asked first and
	// the answer here is not its own: its probability was below the floor, or
	// it failed or refused and the generating provider answered (mw.Decisions).
	// Provider and Model above name who did answer.
	DecisionEscalation *DecisionEscalation
}

// ProviderConfig contains provider-specific configuration
type ProviderConfig struct {
	APIKey  string
	BaseURL string
	OrgID   string

	// Models are the models the tiers mean for this provider: smart, fast and
	// quick, in that order. An empty entry means the built-in one for the
	// provider.
	Models [3]string

	// Timeout bounds one call, from the request to the end of the response body.
	// Zero is no bound of the configuration's own. With no HTTPClient the
	// client the library builds enforces it. With an HTTPClient of the caller's
	// it is applied to each call's context, since that client is used as it is.
	// The earliest of the call context's deadline, the HTTPClient's own Timeout
	// and this one is the one that fires, so a caller's client with a shorter
	// Timeout of its own keeps it.
	Timeout      time.Duration
	MaxRetries   int
	RetryBackoff time.Duration
	Debug        bool
	ExtraHeaders map[string]string

	// Store asks the provider to retain the response server-side. It defaults
	// to false: the zero value is the private one, so a caller who never
	// thinks about this gets retention off rather than on.
	Store bool

	// ServiceTier is the provider's service tier for every request this
	// configures: "priority" for a faster answer at a higher price, "flex" for
	// a slower one at a lower price, "default", or "auto". Empty sends none and
	// leaves the choice to the account's own setting. It is sent to OpenAI's own
	// Responses API endpoint and to nothing else, so a tier set on any other
	// provider, Anthropic or a chat-completions provider, or on an OpenAI
	// provider whose base URL is another host, is a configuration error when the
	// provider is built. The tier the provider says it served the answer on is
	// CompletionResponse.ServiceTier. LAT-008.
	//
	// The cost this library reports is at its price table's rates, which are
	// the default tier's: on another tier the provider's bill differs.
	ServiceTier string

	// OmitTemperature leaves `temperature` off every request, whatever the
	// model and whichever provider this configures. The model table decides
	// this for a name it knows, and for an
	// unknown name it goes by the endpoint; neither can see through an alias,
	// such as an Azure deployment or a proxy route for a model that rejects the
	// parameter. Set this for such an endpoint. The cost is that Mode no longer
	// changes the sampling temperature there.
	OmitTemperature bool

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
		wrapped.Transport = newCustomTransport(base, config.ExtraHeaders)
		return &wrapped
	}

	// LAT-002: with no client of the caller's, the transport is this
	// library's shared one, tuned for many small calls at once. It was Go's
	// default, which keeps two idle connections to a host.
	client := &http.Client{Timeout: config.Timeout, Transport: sharedTransport()}
	if len(config.ExtraHeaders) > 0 {
		client.Transport = newCustomTransport(sharedTransport(), config.ExtraHeaders)
	}
	return client
}

// callContext applies ProviderConfig.Timeout to one call's context when the
// caller supplied their own HTTPClient. A client the library builds carries the
// timeout itself (see resolveHTTPClient), but a caller's client is used as it is
// and may have none, in which case a call under a context with no deadline could
// wait for ever, with Timeout set and ignored.
//
// The deadlines that apply are all of them, and the earliest one wins: the
// context's own, the supplied client's Timeout, and ProviderConfig.Timeout. The
// returned cancel function releases the timer and must be called once the body
// has been read.
func callContext(ctx context.Context, config ProviderConfig) (context.Context, context.CancelFunc) {
	if config.HTTPClient == nil || config.Timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, config.Timeout)
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
		return nil, missingKeyError("OpenAI")
	}
	if err := validateConfiguredEndpoint("openai", config); err != nil {
		return nil, err
	}
	if err := validateCostEnvironment(); err != nil {
		return nil, err
	}
	tier, err := serviceTier(config.ServiceTier)
	if err != nil {
		return nil, err
	}
	config.ServiceTier = tier
	if err := requireOpenAIEndpointForTier(config); err != nil {
		return nil, err
	}

	config = settleBaseURL(config, "")

	return &OpenAIProvider{
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

// serviceTiers are the tiers the Responses API names.
var serviceTiers = []string{"auto", "default", "flex", "priority"}

// serviceTier reads a configured tier: one of the provider's, in lower case,
// or none.
func serviceTier(configured string) (string, error) {
	tier := strings.ToLower(strings.TrimSpace(configured))
	if tier == "" {
		return "", nil
	}
	for _, known := range serviceTiers {
		if tier == known {
			return tier, nil
		}
	}
	return "", types.Newf(types.KindConfiguration, "llm.configure", "OpenAI service tier is not one the API names; it takes %s", strings.Join(serviceTiers, ", "))
}

// responsesAPIResponse is the shape of a POST /v1/responses payload -- the
// body of a buffered call, and the payload the "response.completed" /
// "response.incomplete" SSE events carry in a streamed one. Both paths
// decode into this one type instead of two independently-maintained shapes,
// so a field the streaming event forgets to mirror is a compile error
// instead of a silent gap between "streamed" and "buffered" behaviour.
type responsesAPIResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	// Error is the vendor's account of a response whose status is "failed". The
	// API nests it in the response, and not at the top level of the event or body
	// that carries the response.
	Error *struct {
		Code    any    `json:"code"`
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
	// ServiceTier is the tier the provider served the request on.
	ServiceTier      string `json:"service_tier"`
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

// refused reports whether a message item carries a "refusal" content part: the
// model declining to answer. What the part holds is the model's own words, and
// is not read.
func (r *responsesAPIResponse) refused() bool {
	for _, output := range r.Output {
		if output.Type != "" && output.Type != "message" {
			continue
		}
		for _, item := range output.Content {
			if item.Type == "refusal" {
				return true
			}
		}
	}
	return false
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
// be distinguishable from a complete one in both. It reads only the two
// statuses that carry an answer; classifyResponsesResult refuses every other
// before this is reached, so a status that did not finish is never "stop".
func (r *responsesAPIResponse) finishReason() string {
	if r.Status == "incomplete" {
		if r.IncompleteReason.Reason != "" {
			return r.IncompleteReason.Reason
		}
		return "incomplete"
	}
	return "stop"
}

func (r *responsesAPIResponse) toCompletionResponse(providerName string) CompletionResponse {
	content := r.content()
	calls := r.toolCalls()

	finishReason := r.finishReason()
	if len(calls) > 0 && finishReason == "stop" {
		// "stop" is technically accurate (the response completed normally)
		// but indistinguishable from a genuine empty text answer, or from an
		// answer that has nothing more to say. A caller switching on
		// FinishReason -- as this library's own ClassifyCompletion does for
		// truncation -- needs to be able to tell "the model answered" apart
		// from "the model asked for a tool", whether or not it said something
		// first. The chat dialect's own reason for the same thing is
		// "tool_calls", and this is the same word.
		finishReason = "tool_calls"
	}

	return CompletionResponse{
		Content:      content,
		ToolCalls:    calls,
		Provider:     providerName,
		Model:        r.Model,
		ServiceTier:  r.ServiceTier,
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
//
// Only a status that carries an answer is read as one. "completed" is a whole
// answer and "incomplete" one that was cut short, which the finish reason says.
// "failed" is an error, with the code and message the vendor put in the
// response, and any other status, or none, is a response that is not a finished
// answer, so it is refused and not read as one that stopped normally.
//
// requestModel is the model the request named, for the errors this returns; the
// response's own model field is not known to be there on a failure.
func classifyResponsesResult(r *responsesAPIResponse, providerName, requestModel string, offeredTools []Tool, config ProviderConfig) (CompletionResponse, error) {
	switch r.Status {
	case "completed", "incomplete":
	case "failed":
		return CompletionResponse{}, responsesFailedError(providerName, requestModel, r, config)
	case "cancelled", "queued", "in_progress":
		return CompletionResponse{}, &types.OperationError{
			Kind:     types.KindProviderUnavailable,
			Op:       "llm.complete",
			Provider: providerName,
			Model:    requestModel,
			Message:  fmt.Sprintf("the response did not finish (status %s)", r.Status),
		}
	default:
		return CompletionResponse{}, &types.OperationError{
			Kind:     types.KindMalformedOutput,
			Op:       "llm.complete",
			Provider: providerName,
			Model:    requestModel,
			Message:  "the response carried no status this library recognises, so it is not known to be a finished answer",
		}
	}

	// A refusal part is the model declining. It is not an answer, and any text
	// beside it is not trusted to be one: the part was dropped and the rest
	// returned, which handed a caller the words of a model that had said no.
	if r.refused() {
		return CompletionResponse{}, refusedAnswerError(providerName, requestModel)
	}

	calls := r.toolCalls()

	if r.content() == "" && len(calls) == 0 {
		// The answer was stopped before any text. The same request would be
		// stopped the same way, and was billed for what it spent getting there.
		if r.Status == "incomplete" {
			return CompletionResponse{}, incompleteAnswerError(providerName, requestModel, r.IncompleteReason.Reason)
		}
		return CompletionResponse{}, fmt.Errorf("openai: %w", ErrNoMessageOutput)
	}

	if len(calls) > 0 {
		// A reply that stopped at a limit with tool calls in it has calls that were
		// cut, whatever they look like.
		if r.Status == "incomplete" {
			return CompletionResponse{}, truncatedToolCallsError(providerName, requestModel, r.IncompleteReason.Reason)
		}
		if err := checkToolCalls(providerName, calls, offeredTools); err != nil {
			return CompletionResponse{}, err
		}
	}

	return r.toCompletionResponse(providerName), nil
}

// responsesFailedError is the error for a response whose status is "failed",
// built from the error object the vendor nested in it.
func responsesFailedError(providerName, requestModel string, r *responsesAPIResponse, config ProviderConfig) error {
	if r.Error == nil {
		return NewAPIError(providerName, requestModel, statusForVendorCode(""), "")
	}
	return vendorFailureError(providerName, requestModel, r.Error.Type, scalarString(r.Error.Code), r.Error.Message, config)
}

// vendorFailureError builds the typed error for a failure the vendor reported
// inside a 200 answer or a stream, where there is no status line of its own. The
// status is the one an HTTP failure for the same code carries (see
// statusForVendorCode), so the failure is classified, and retried or not, the
// way the same failure would be had it arrived as a status.
func vendorFailureError(provider, model, errType, code, message string, config ProviderConfig) *APIError {
	scrub := newScrubber(configSecrets(config))
	apiErr := &APIError{
		Provider:   provider,
		Model:      model,
		StatusCode: statusForVendorCode(code),
	}
	apiErr.fill(scrub, message, errType, code, "")
	return apiErr
}

// statusForVendorCode maps the code a vendor puts in a failure that has no
// status line to the status that code stands for. A number from 400 to 599 is a
// status already; a code this does not know is a 502, the provider failing to
// produce an answer, which is retried and is not the caller's request being
// wrong.
func statusForVendorCode(code string) int {
	if status, err := strconv.Atoi(code); err == nil && status >= 400 && status <= 599 {
		return status
	}
	switch {
	case code == "rate_limit_exceeded":
		return http.StatusTooManyRequests
	case code == "server_error":
		return http.StatusInternalServerError
	case code == "vector_store_timeout":
		return http.StatusGatewayTimeout
	case strings.HasPrefix(code, "invalid_"), strings.HasPrefix(code, "image_"),
		strings.HasPrefix(code, "unsupported_"), strings.HasPrefix(code, "empty_"),
		strings.HasPrefix(code, "failed_to_download"):
		return http.StatusBadRequest
	}
	return http.StatusBadGateway
}

// validateToolCalls refuses any call naming a tool the request never
// offered, with the Responses API's name on the error. It is checkToolNames; the
// reply of a call is held to more than this (checkToolCalls).
func validateToolCalls(calls []ToolCall, offeredTools []Tool) error {
	return checkToolNames("openai", calls, offeredTools)
}

// buildResponsesRequestBody constructs the JSON body shared by the buffered
// and streamed calls to the Responses API. `stream` adds the one field that
// turns a normal response into an SSE one; everything else -- schema,
// reasoning controls, cache key -- is built identically for both, because a
// caller streaming the exact same request as a buffered call must produce
// the exact same request on the wire.
func (provider *OpenAIProvider) buildResponsesRequestBody(req CompletionRequest, stream bool) map[string]any {
	// input is either the single UserPrompt string (unchanged from before
	// Messages existed) or, when the caller supplied an explicit transcript,
	// an ordered array of role/content turns. SystemPrompt still becomes
	// "instructions" either way -- the Responses API keeps steering separate
	// from the turn history, so a caller building a transcript is not also
	// responsible for re-injecting the system prompt as message zero.
	var input any
	if len(req.Messages) > 0 {
		input = responsesInputItems(req.Messages)
	} else {
		userInput := req.UserPrompt
		if req.ResponseFormat == "json" && !strings.Contains(strings.ToLower(userInput), "json") {
			userInput = "Return valid JSON only.\n\n" + userInput
		}
		input = userInput
	}

	requestBody := map[string]any{
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
	// Only to OpenAI's own endpoint: the constructor refuses a tier for any
	// other, and this holds for a provider that was put together without it.
	if provider.config.ServiceTier != "" && isOpenAIEndpoint(provider.config.BaseURL) {
		requestBody["service_tier"] = provider.config.ServiceTier
	}
	if stream {
		requestBody["stream"] = true
	}

	if req.Temperature > 0 && !provider.config.OmitTemperature &&
		supportsTemperature(req.Model, isOpenAIEndpoint(provider.config.BaseURL)) {
		requestBody["temperature"] = req.Temperature
	}

	if req.MaxTokens > 0 {
		requestBody["max_output_tokens"] = req.MaxTokens
	}

	if req.PromptCacheKey != "" {
		// The API refuses a key over 64 characters, and the key an operation
		// builds from its schema identity is longer than that (LX-002).
		requestBody["prompt_cache_key"] = boundPromptCacheKey(req.PromptCacheKey)
	}

	// Tools/ToolChoice are omitted entirely rather than sent empty/"auto" by
	// default, so a request that never mentions tools produces the exact
	// same wire body it produced before tool calling existed.
	if len(req.Tools) > 0 || req.WebSearch {
		tools := make([]map[string]any, 0, len(req.Tools)+1)
		for _, tool := range req.Tools {
			parameters := toolParameters(tool)
			tools = append(tools, map[string]any{
				"type":        "function",
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  parameters,
				// The API reads a function with no flag as strict, and answers a
				// schema strict mode does not accept with a 400. The flag is
				// written out, true when the schema is one strict mode takes.
				"strict": toolIsStrict(parameters),
			})
		}
		if req.WebSearch {
			// The built-in tool is a bare type marker: the provider hosts
			// the implementation, so there is no name/parameters half.
			tools = append(tools, map[string]any{"type": "web_search"})
		}
		requestBody["tools"] = tools
	}
	if req.ToolChoice != "" {
		switch req.ToolChoice {
		case "auto", "none", "required":
			requestBody["tool_choice"] = req.ToolChoice
		default:
			// Anything else names one specific tool to force.
			requestBody["tool_choice"] = map[string]any{
				"type": "function",
				"name": req.ToolChoice,
			}
		}
	}

	textConfig := map[string]any{}
	if req.ResponseFormat == "json" {
		if len(req.JSONSchema) > 0 {
			name := req.SchemaName
			if name == "" {
				name = "result"
			}
			// strict: the model must produce something the schema accepts, which
			// is the difference between a contract and a request.
			textConfig["format"] = map[string]any{
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
	// A refusal is newResponsesHTTPRequest's to make, before this is called;
	// here an effort that cannot be sent is left off.
	if effort, _ := resolveEffort(req.Model, isOpenAIEndpoint(provider.config.BaseURL), req.ReasoningEffort, req.ReasoningEffortPreferred); effort != "" {
		requestBody["reasoning"] = map[string]string{"effort": effort}
		// The families that are sent a default effort are also asked to be
		// brief. That stays with the family and not with the effort, so a
		// request with no effort set is byte for byte what it was.
		if supportsReasoningControls(req.Model) {
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

	// A schema is always sent as strict, and OpenAI answers a schema that is
	// not strict-valid with a 400 that names neither the operation nor the
	// field (LX-003). Refusing it here costs no network call, says where the
	// schema is wrong, and is classified as an invalid request so the retry
	// loop does not send the same body four times.
	//
	// Only for OpenAI's own endpoint. The rules checked are the ones that API
	// was observed to enforce; another server behind an OpenAI-shaped URL may
	// accept a schema this would refuse, and a caller whose own schema worked
	// there should not have it taken away on OpenAI's behalf.
	if err := checkToolChoice(req, provider.Name()); err != nil {
		return nil, err
	}
	if err := checkTools(req, provider.Name(), isOpenAIEndpoint(provider.config.BaseURL)); err != nil {
		return nil, err
	}
	if err := checkTranscript(req, provider.Name()); err != nil {
		return nil, err
	}

	if req.ResponseFormat == "json" && len(req.JSONSchema) > 0 && isOpenAIEndpoint(provider.config.BaseURL) {
		if problem := StrictSchemaProblem(req.JSONSchema); problem != "" {
			name := req.SchemaName
			if name == "" {
				name = "result"
			}
			return nil, &types.OperationError{
				Kind:     types.KindInvalidRequest,
				Provider: provider.Name(),
				Model:    req.Model,
				Message:  fmt.Sprintf("the response schema %q is not valid for strict structured output: %s", name, problem),
			}
		}
	}

	// LX-105. An effort the caller asked for and the model does not take is
	// refused here, with the values it does take.
	if _, problem := resolveEffort(req.Model, isOpenAIEndpoint(provider.config.BaseURL), req.ReasoningEffort, req.ReasoningEffortPreferred); problem != "" {
		return nil, &types.OperationError{
			Kind:     types.KindInvalidRequest,
			Provider: provider.Name(),
			Model:    req.Model,
			Message:  problem,
		}
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

// Warm opens the connection the provider's calls will use (LAT-002).
func (provider *OpenAIProvider) Warm(ctx context.Context) error {
	endpoint := "https://api.openai.com/v1"
	if provider.config.BaseURL != "" {
		endpoint = strings.TrimRight(provider.config.BaseURL, "/")
	}
	return warmConnection(ctx, provider.httpClient, provider.Name(), endpoint)
}

// Complete sends a completion request to OpenAI using the Responses API
func (provider *OpenAIProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	ctx, cancel := callContext(ctx, provider.config)
	defer cancel()

	// POST /v1/responses, written out here because go-openai does not speak the
	// Responses API.
	httpReq, err := provider.newResponsesHTTPRequest(ctx, req, false)
	if err != nil {
		return CompletionResponse{}, err
	}

	resp, clock, err := doTimed(provider.httpClient, httpReq)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("OpenAI request failed: %w", err)
	}
	// Read to its end, so that the connection is kept (drainAndClose).
	defer drainAndClose(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return CompletionResponse{}, statusError("openai", req.Model, resp, provider.config)
	}

	raw, err := readBounded(resp.Body, maxResponseBodyBytes)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("failed to read response: %w", err)
	}
	var response responsesAPIResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return CompletionResponse{}, fmt.Errorf("failed to decode response: %w", err)
	}

	// Taken once the body has been read and decoded, so that the reading is in
	// it. It travels only on a result: a call that failed returns no result, and
	// the error type has nowhere to carry it.
	timing := clock.timing()
	result, err := classifyResponsesResult(&response, provider.Name(), req.Model, req.Tools, provider.config)
	if err != nil {
		return CompletionResponse{}, err
	}
	result.Timing = timing
	return result, nil
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
		ctx, cancel := callContext(ctx, provider.config)
		defer cancel()

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
			yield(StreamChunk{}, statusError("openai", req.Model, resp, provider.config))
			return
		}

		scanner := bufio.NewScanner(resp.Body)
		// SSE frames are small individually, but a "response.completed"
		// event carries the whole answer as one line; the default 64KiB
		// token limit truncates any answer longer than that silently inside
		// bufio, which would look like a parse error three layers away. The
		// ceiling is the one an event as a whole is held to, below.
		scanner.Buffer(make([]byte, 64*1024), maxStreamEventBytes)

		var dataLines []string
		// eventBytes totals the data lines collected for the event being read. A
		// stream that never sends the blank line that ends an event would
		// otherwise grow dataLines without limit, each line within the per-line
		// ceiling.
		eventBytes := 0
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
			eventBytes = 0
			if payload == "[DONE]" {
				return true
			}

			// An "error" event carries its code and message at the top level of
			// the event; a "response.failed" event carries them inside its
			// response, as Response.Error. Neither is under a top-level "error".
			var envelope struct {
				Type     string                `json:"type"`
				Delta    string                `json:"delta"`
				Response *responsesAPIResponse `json:"response"`
				Code     any                   `json:"code"`
				Message  string                `json:"message"`
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
				completion, err := classifyResponsesResult(envelope.Response, provider.Name(), req.Model, req.Tools, provider.config)
				if err != nil {
					return yield(StreamChunk{}, err)
				}
				return yield(StreamChunk{Done: true, Response: completion}, nil)

			case "response.failed":
				terminal = true
				if envelope.Response == nil {
					return yield(StreamChunk{}, vendorFailureError(provider.Name(), req.Model, envelope.Type, "", "", provider.config))
				}
				return yield(StreamChunk{}, responsesFailedError(provider.Name(), req.Model, envelope.Response, provider.config))

			case "error":
				terminal = true
				return yield(StreamChunk{}, vendorFailureError(provider.Name(), req.Model, envelope.Type, scalarString(envelope.Code), envelope.Message, provider.config))

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
				eventBytes += len(after) + 1
				if eventBytes > maxStreamEventBytes {
					yield(StreamChunk{}, streamEventTooLongError())
					return
				}
				dataLines = append(dataLines, strings.TrimPrefix(after, " "))
			}
			// Any other field (event:, id:, retry:, or a comment line
			// starting with ':') is SSE framing this library does not need
			// -- dispatch reads the event's own "type" from the JSON
			// payload, not the SSE "event:" line, so the two cannot disagree.
		}

		if err := scanner.Err(); err != nil {
			if errors.Is(err, bufio.ErrTooLong) {
				yield(StreamChunk{}, streamEventTooLongError())
				return
			}
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
	return estimateCost("openai", req)
}

// estimateCost is the estimate every provider makes: the prompt at four
// characters to a token, and a completion of the request's ceiling, or 500 tokens
// when it names none, each at the provider's rate for the model. The prompt is
// everything the request sends: the system and user prompts, the turns of a
// conversation, and the tools declared.
func estimateCost(provider string, req CompletionRequest) float64 {
	inputRate, outputRate := getModelRates(provider, req.Model)

	estimatedPromptTokens := requestPromptBytes(req) / 4
	estimatedCompletionTokens := 500
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
//
// The three predicates read one table, modelFamilies in models.go, which is
// also where the measurements behind each row are recorded. They used to carry
// a prefix comparison each, and all three answered for a model none of them
// named, which is how a new family got `temperature` and failed every request
// (LX-001).

// supportsTemperature reports whether `temperature` is sent for the model.
// openAIEndpoint says whether the request goes to OpenAI's own API, which
// decides the answer for a name the table does not know; see traitsFor.
func supportsTemperature(model string, openAIEndpoint bool) bool {
	return traitsFor(model, openAIEndpoint).temperature
}

// supportsReasoningControls reports whether a reasoning block is sent when the
// caller has not asked for a particular effort. A name the table does not know
// gets no block on any endpoint, so the endpoint does not enter into it.
func supportsReasoningControls(model string) bool {
	return traitsFor(model, true).reasoningBlock
}

// NormalizeProviderName is the one spelling of a provider's name: trimmed of
// space and in lower case. The client package matches names the same way when it
// picks a provider by name, and two copies of the rule would let " OpenAI" be
// one provider in one place and an unknown one in the other.
func NormalizeProviderName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// settleBaseURL returns config with its base URL trimmed of space and of a
// trailing slash, or defaultBaseURL when it names none. A provider with neither
// keeps an empty one, which the Responses path reads as OpenAI's own endpoint.
func settleBaseURL(config ProviderConfig, defaultBaseURL string) ProviderConfig {
	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if baseURL == "" {
		baseURL = strings.TrimRight(defaultBaseURL, "/")
	}
	if baseURL != "" {
		config.BaseURL = baseURL
	}
	return config
}

// missingKeyError is the configuration error for a provider built with no API
// key, which would otherwise be built and answer every call with a 401.
func missingKeyError(provider string) error {
	return types.Newf(types.KindConfiguration, "llm.configure", "%s API key is required", provider)
}

func newOpenAICompatibleProvider(name string, config ProviderConfig, defaultBaseURL string) (*OpenAICompatibleProvider, error) {
	if err := validateConfiguredEndpoint(name, config); err != nil {
		return nil, err
	}
	if err := validateCostEnvironment(); err != nil {
		return nil, err
	}
	if err := refuseServiceTier(NormalizeProviderName(name), config); err != nil {
		return nil, err
	}

	if config.APIKey == "" {
		return nil, missingKeyError(name)
	}
	config = settleBaseURL(config, defaultBaseURL)

	return &OpenAICompatibleProvider{
		name: NormalizeProviderName(name),
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
		return nil, missingKeyError("anthropic")
	}
	if err := validateConfiguredEndpoint("anthropic", config); err != nil {
		return nil, err
	}
	if err := validateCostEnvironment(); err != nil {
		return nil, err
	}
	if err := refuseServiceTier("anthropic", config); err != nil {
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

// anthropicDefaultModel is what this provider sends when the request names no
// Claude model.
const anthropicDefaultModel = "claude-3-5-sonnet-20240620"

// ModelSentFor returns the model this provider sends for a requested one. An
// empty model, and a "gpt" one, which the library's default model settings
// produce for every provider and Anthropic cannot serve, are sent as
// anthropicDefaultModel; any other name is sent as it is.
//
// It is exported so that the route can be known before the call. The data
// policy checked "anthropic/<the requested name>" while the request went to the
// default model, so content was sent over a route that was never checked, and
// the answer was then refused for naming one (see ModelSentBy).
func (provider *AnthropicProvider) ModelSentFor(requested string) string {
	name := strings.TrimSpace(requested)
	if name == "" || strings.HasPrefix(strings.ToLower(name), "gpt") {
		return anthropicDefaultModel
	}
	return name
}

// Complete sends a completion request to Anthropic
func (provider *AnthropicProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	// This provider sends a system prompt, one user prompt, a ceiling and a
	// temperature, and nothing else. A request that asks for more is refused
	// before anything is sent: a tool the model was never offered, or a transcript
	// replaced by an empty prompt, is an answer to a different question.
	if err := unsupportedRequestFields(req, provider.Name(), "Tools", "ToolChoice", "WebSearch", "Messages"); err != nil {
		return CompletionResponse{}, err
	}

	ctx, cancel := callContext(ctx, provider.config)
	defer cancel()

	url := strings.TrimRight(provider.baseURL, "/") + "/v1/messages"

	model := provider.ModelSentFor(req.Model)

	// anthropicFallbackMaxTokens is the ceiling sent when the request names none.
	// A ceiling the caller set always wins; this is only the floor for a request
	// built outside the configuration path, which would otherwise be sent no
	// max_tokens at all, and the API requires one.
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
	cacheBreakpoint := map[string]any{"type": "ephemeral"}

	// Construct messages. When there is a system prompt, it is the last stable
	// block and gets the breakpoint. When there is not, the user message is
	// the only block there is, so the breakpoint goes there instead -- an
	// Anthropic request with no cache_control anywhere never reads or writes
	// the cache at all, silently.
	var messages []map[string]any
	if req.SystemPrompt != "" {
		messages = []map[string]any{
			{
				"role":    "user",
				"content": req.UserPrompt,
			},
		}
	} else {
		messages = []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{
						"type":          "text",
						"text":          req.UserPrompt,
						"cache_control": cacheBreakpoint,
					},
				},
			},
		}
	}

	// LX-105. This provider does not set a reasoning effort. A caller who asked
	// for one is told so; an operation's preference is not a request and is
	// left out.
	if err := effortNotSupported(req, provider.Name()); err != nil {
		return CompletionResponse{}, err
	}

	// Construct request body
	requestBody := map[string]any{
		"model":      model,
		"messages":   messages,
		"max_tokens": maxTokens,
	}

	if req.SystemPrompt != "" {
		requestBody["system"] = []map[string]any{
			{
				"type":          "text",
				"text":          req.SystemPrompt,
				"cache_control": cacheBreakpoint,
			},
		}
	}

	if req.Temperature > 0 && !provider.config.OmitTemperature {
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

	// provider.httpClient is resolved once, at construction (resolveHTTPClient):
	// the caller's own *http.Client when supplied, else the library's, so that
	// connections are reused between calls.
	resp, clock, err := doTimed(provider.httpClient, httpReq)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("anthropic request failed: %w", err)
	}
	// Read to its end, so that the connection is kept (drainAndClose).
	defer drainAndClose(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return CompletionResponse{}, statusError(provider.Name(), model, resp, provider.config)
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

	raw, err := readBounded(resp.Body, maxResponseBodyBytes)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("failed to read response: %w", err)
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return CompletionResponse{}, fmt.Errorf("failed to decode response: %w", err)
	}

	// A stop reason says whether the text is the whole answer, and only the ones
	// below are known to. "refusal" is the model declining, which is an answer
	// that is not an answer; a stop reason that is missing, or one this library
	// does not know, is not read as a normal end.
	switch response.StopReason {
	case "end_turn", "stop_sequence", "max_tokens":
	case "refusal":
		return CompletionResponse{}, refusedAnswerError(provider.Name(), model)
	default:
		return CompletionResponse{}, &types.OperationError{
			Kind:     types.KindMalformedOutput,
			Op:       "llm.complete",
			Provider: provider.Name(),
			Model:    model,
			Message:  "the response carried no stop reason this library recognises, so it is not known to be a finished answer",
		}
	}

	// Extract text content
	var content strings.Builder
	for _, item := range response.Content {
		if item.Type == "text" {
			content.WriteString(item.Text)
		}
	}
	if content.Len() == 0 {
		if response.StopReason == "max_tokens" {
			return CompletionResponse{}, incompleteAnswerError(provider.Name(), model, "max_tokens")
		}
		return CompletionResponse{}, fmt.Errorf("anthropic: %w", ErrNoMessageOutput)
	}

	return CompletionResponse{
		Timing:       clock.timing(),
		Content:      content.String(),
		Provider:     provider.Name(),
		Model:        response.Model,
		FinishReason: response.StopReason,
		Usage: types.TokenUsage{
			PromptTokens:     response.Usage.InputTokens,
			CompletionTokens: response.Usage.OutputTokens,
			TotalTokens:      response.Usage.InputTokens + response.Usage.OutputTokens,
		},
	}, nil
}

// Warm opens the connection the provider's calls will use (LAT-002).
func (provider *AnthropicProvider) Warm(ctx context.Context) error {
	return warmConnection(ctx, provider.httpClient, provider.Name(), strings.TrimRight(provider.baseURL, "/"))
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
	return estimateCost("anthropic", req)
}

// OpenRouterProvider implements Provider for OpenRouter
type OpenRouterProvider struct {
	*OpenAICompatibleProvider
}

// customTransport is a http.RoundTripper that adds custom headers.
type customTransport struct {
	transport http.RoundTripper
	headers   map[string]string
}

// newCustomTransport wraps transport so that every request carries headers. The
// headers are copied: the map is the caller's, and a write to it after the
// provider was built would race with the reads here.
func newCustomTransport(transport http.RoundTripper, headers map[string]string) *customTransport {
	copied := make(map[string]string, len(headers))
	for name, value := range headers {
		copied[name] = value
	}
	return &customTransport{transport: transport, headers: copied}
}

// RoundTrip adds the headers to a clone of the request. A RoundTripper must not
// modify the request it is given, and the transport underneath may retry or
// inspect it afterwards.
func (t *customTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	for name, value := range t.headers {
		clone.Header.Set(name, value)
	}
	return t.transport.RoundTrip(clone)
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

// Warm opens the connection the provider's calls will use (LAT-002).
func (provider *OpenAICompatibleProvider) Warm(ctx context.Context) error {
	return warmConnection(ctx, provider.httpClient, provider.name, strings.TrimRight(strings.TrimSpace(provider.config.BaseURL), "/"))
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
	return estimateCost(provider.name, req)
}

// NewOpenRouterProvider creates a new OpenRouter provider
func NewOpenRouterProvider(config ProviderConfig) (*OpenRouterProvider, error) {
	if config.APIKey == "" {
		return nil, missingKeyError("OpenRouter")
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
		return nil, missingKeyError("cerebras")
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
		return nil, missingKeyError("DeepSeek")
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
		return nil, missingKeyError("qwen")
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
		return nil, missingKeyError("z.ai")
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
	// The handler returns text, so it cannot ask for a tool, and the shape the
	// default answer is built from is read from the prompts, so it cannot read a
	// conversation. A request that sets either was answered as if it had not: a
	// transcript in Messages as the empty user prompt. It is refused before the
	// handler runs, as a provider that speaks a wire refuses what it cannot send.
	if err := unsupportedRequestFields(req, provider.Name(), "Tools", "ToolChoice", "WebSearch", "Messages"); err != nil {
		return CompletionResponse{}, err
	}

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

	// The answer names the model that was asked for, when one was. It named
	// "local-model" whatever was asked, which a client's data policy now reads
	// as an answer from a route other than the one it allowed.
	model := req.Model
	if model == "" {
		model = "local-model"
	}

	return CompletionResponse{
		Content:      content,
		Provider:     provider.Name(),
		Model:        model,
		FinishReason: "stop",
		Usage: types.TokenUsage{
			PromptTokens:     len(req.SystemPrompt+req.UserPrompt) / 4,
			CompletionTokens: len(content) / 4,
			TotalTokens:      (len(req.SystemPrompt+req.UserPrompt) + len(content)) / 4,
		},
	}, nil
}

// unscriptedAnswerText is what the local provider answers when nothing scripted
// it and the request declared no shape to answer in. It is plainly not an answer:
// it names no field any operation reads and carries no verdict, so an operation
// that asked for a structure fails its own checks on it, and one that asked for
// text can see where the text came from. The provider used to guess from words in
// the prompt and answer `{"valid": true, "confidence": 0.95}` to "validate", which
// is a judgment made by nobody and reported as one.
const unscriptedAnswerText = "[the local provider has no scripted answer for this request; script one with WithHandler or schemafluxtest]"

// mockResponse answers a request that no handler scripted.
//
// The shape comes from what the request declared -- a JSON Schema, or the JSON
// template the prompt-only operations embed in their system prompt -- because
// guessing from keywords is what made this provider answer
// `Mock response for: ...` to nineteen of the forty-five numbered examples. See
// mockshape.go: those values are obviously synthetic placeholders and only make
// the plumbing run. A request that declared nothing is answered with
// unscriptedAnswerText, in JSON when JSON was asked for.
func (provider *LocalProvider) mockResponse(req CompletionRequest) string {
	if shaped := mockShapedResponse(req); shaped != "" {
		return shaped
	}
	if req.ResponseFormat == "json" {
		encoded, _ := json.Marshal(map[string]string{"unscripted_local_provider_answer": unscriptedAnswerText})
		return string(encoded)
	}
	return unscriptedAnswerText
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

	name = NormalizeProviderName(name)
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

// RegisterFactory adds a provider factory to the registry. The first provider or
// factory a caller registers becomes the registry's default until SetDefault says
// otherwise.
func (registry *ProviderRegistry) RegisterFactory(name string, factory ProviderFactory) error {
	return registry.registerFactory(name, factory, true)
}

// registerFactory is RegisterFactory with the choice of whether the factory may
// become the default. The built-in providers are registered without it: which one
// is registered first is an accident of start-up order and not a choice, and
// being the default is a choice.
func (registry *ProviderRegistry) registerFactory(name string, factory ProviderFactory, mayBeDefault bool) error {
	registry.mu.Lock()
	defer registry.mu.Unlock()

	name = NormalizeProviderName(name)
	if factory == nil {
		return fmt.Errorf("provider factory cannot be nil")
	}
	if name == "" {
		return fmt.Errorf("provider name cannot be empty")
	}
	registry.factories[name] = factory
	if mayBeDefault && registry.defaultProvider == "" {
		registry.defaultProvider = name
	}
	return nil
}

// Get retrieves a provider by name
func (registry *ProviderRegistry) Get(name string) (Provider, error) {
	return registry.Create(name, ProviderConfig{})
}

// Create creates or retrieves a provider by name and configuration. An empty
// name asks for the registry's default, and a registry with none is a
// configuration error that says what to configure: guessing a provider from the
// ones that happen to be registered would send the caller's data to a vendor they
// did not choose, or answer from the local mock.
func (registry *ProviderRegistry) Create(name string, config ProviderConfig) (Provider, error) {
	registry.mu.RLock()
	name = NormalizeProviderName(name)
	if name == "" {
		name = registry.defaultProvider
	}
	if name == "" {
		registry.mu.RUnlock()
		return nil, types.Newf(types.KindConfiguration, "llm.provider",
			"no LLM provider configured: name one when creating the provider, or choose a default with SetDefaultProvider")
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

	name = NormalizeProviderName(name)
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

// registerBuiltInProviderFactories registers the providers this library ships.
// None of them becomes the registry's default: the order they are registered in
// is not a choice anyone made, and a default picked by it was a different vendor,
// or the local mock, on each start. The names are walked in sorted order so that
// registration is deterministic whatever else is done with the result.
func registerBuiltInProviderFactories(registry *ProviderRegistry) {
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

		// The decision providers: one protocol, at the three places it is served
		// (systemone.go). They answer a request that carries a Decision and refuse
		// any other.
		SystemOneTypeSafe.Name:   systemOneFactory(SystemOneTypeSafe),
		SystemOneOpenRouter.Name: systemOneFactory(SystemOneOpenRouter),
		SystemOneLiteLLM.Name:    systemOneFactory(SystemOneLiteLLM),
	}

	names := make([]string, 0, len(builtIns))
	for name := range builtIns {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := registry.registerFactory(name, builtIns[name], false); err != nil {
			panic(fmt.Sprintf("failed to register built-in provider %s: %v", name, err))
		}
	}
}

func init() {
	registerBuiltInProviderFactories(globalRegistry)
}

// costEnvPrefixes are the prefixes of the environment variables that override a
// price: SCHEMAFLUX_COST_INPUT_<NAME> and SCHEMAFLUX_COST_OUTPUT_<NAME>, where
// <NAME> is a model name with its dashes and dots written as underscores, in
// capitals, or SMART, FAST or QUICK for the model mapped to that level by
// SCHEMAFLUX_MODEL_<LEVEL>. The value is a price in dollars per million tokens.
var costEnvPrefixes = []string{"SCHEMAFLUX_COST_INPUT_", "SCHEMAFLUX_COST_OUTPUT_"}

// priceFromEnv reads one price variable. set is false for a variable that is not
// set or is empty. A value that is set must be a finite number that is not
// negative: NaN and infinity make every estimate NaN or infinite, and a negative
// price makes a call spend the budget down. The error names the variable and not
// its value.
func priceFromEnv(name string) (perToken float64, set bool, err error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0, false, nil
	}
	perMillion, parseErr := strconv.ParseFloat(raw, 64)
	if parseErr != nil || math.IsNaN(perMillion) || math.IsInf(perMillion, 0) || perMillion < 0 {
		return 0, false, fmt.Errorf("%s is not a finite price per million tokens of zero or more", name)
	}
	return perMillion / 1_000_000, true, nil
}

// validateCostEnvironment refuses a price variable whose value is not a usable
// price. It runs when a provider is built, so that the mistake is reported as a
// configuration error where it was made. EstimateCost cannot report one, since it
// returns a number, so a variable that is invalid by the time an estimate is made
// is not used.
func validateCostEnvironment() error {
	for _, entry := range os.Environ() {
		name, _, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		for _, prefix := range costEnvPrefixes {
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			if _, _, err := priceFromEnv(name); err != nil {
				return types.Newf(types.KindConfiguration, "llm.configure", "%s", err.Error())
			}
		}
	}
	return nil
}

// getModelRates returns the input and output cost per token for a model.
//
// A price the environment sets wins: for the model's own name, and for the level
// (SMART, FAST or QUICK) the model is mapped to. A price it does not set is the
// provider's table price (see getDefaultRates). A variable that is set to
// something that is not a price is not used: validateCostEnvironment reports it
// when the provider is built.
func getModelRates(provider, model string) (float64, float64) {
	if suffix := levelOf(model); suffix != "" {
		input, inputSet, _ := priceFromEnv("SCHEMAFLUX_COST_INPUT_" + suffix)
		output, outputSet, _ := priceFromEnv("SCHEMAFLUX_COST_OUTPUT_" + suffix)
		if inputSet && outputSet {
			return input, output
		}
	}

	sanitizedModel := strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(model, "-", "_"), ".", "_"))
	inputRate, inputFound, _ := priceFromEnv("SCHEMAFLUX_COST_INPUT_" + sanitizedModel)
	outputRate, outputFound, _ := priceFromEnv("SCHEMAFLUX_COST_OUTPUT_" + sanitizedModel)
	if inputFound && outputFound {
		return inputRate, outputRate
	}

	defaultInput, defaultOutput := getDefaultRates(provider)
	if !inputFound {
		inputRate = defaultInput
	}
	if !outputFound {
		outputRate = defaultOutput
	}
	return inputRate, outputRate
}

// levelOf names the level, SMART, FAST or QUICK, that the environment maps the
// model to, or "" for a model that is mapped to none.
func levelOf(model string) string {
	for _, level := range []string{"SMART", "FAST", "QUICK"} {
		if mapped := os.Getenv("SCHEMAFLUX_MODEL_" + level); mapped != "" && model == mapped {
			return level
		}
	}
	return ""
}

// getDefaultRates is the price table: dollars per token for a provider, whatever
// the model. It is a table of the provider and not of the model, so it is a rough
// figure at best, and a provider that is not in it is priced at zero, which says
// nothing about what it costs. EstimateCost returns a number and has no way to say
// "unknown".
func getDefaultRates(provider string) (float64, float64) {
	switch provider {
	case "openai":
		// GPT-4 pricing ($30/1M input, $60/1M output).
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

// checkToolChoice refuses a ToolChoice that is none of the three words the API
// takes and names none of the tools the request offers. It would be sent as a
// forced function name, and the API answers a name it does not know with a 400
// that costs a round trip. The error lists the tools the request offers, which
// are the caller's own names, and not the choice.
func checkToolChoice(req CompletionRequest, provider string) error {
	switch req.ToolChoice {
	case "", "auto", "none", "required":
		return nil
	}
	offered := make([]string, 0, len(req.Tools))
	for _, tool := range req.Tools {
		if tool.Name == req.ToolChoice {
			return nil
		}
		offered = append(offered, fmt.Sprintf("%q", tool.Name))
	}
	sort.Strings(offered)
	list := "none"
	if len(offered) > 0 {
		list = strings.Join(offered, ", ")
	}
	return &types.OperationError{
		Kind:     types.KindInvalidRequest,
		Op:       "llm.complete",
		Provider: provider,
		Model:    req.Model,
		Message:  fmt.Sprintf("ToolChoice is not auto, none or required, and names no tool the request offers (offered: %s)", list),
	}
}

// unsupportedRequestFields refuses a request that sets a field the provider
// path has no way to send. A field that is dropped without a word changes what
// was asked: the caller who offered Tools gets a plain answer with no calls in
// it, and one who passed a transcript has an empty prompt sent in its place. The
// error names the field, and never its value, which is the caller's data.
//
// fields lists the CompletionRequest fields the provider cannot send; only those
// that the request actually sets are reported.
func unsupportedRequestFields(req CompletionRequest, provider string, fields ...string) error {
	set := map[string]bool{
		"Tools":      len(req.Tools) > 0,
		"ToolChoice": req.ToolChoice != "",
		"WebSearch":  req.WebSearch,
		"Messages":   len(req.Messages) > 0,
	}
	var named []string
	for _, field := range fields {
		if set[field] {
			named = append(named, field)
		}
	}
	if len(named) == 0 {
		return nil
	}
	return &types.OperationError{
		Kind:     types.KindUnsupportedCapability,
		Op:       "llm.complete",
		Provider: provider,
		Model:    req.Model,
		Message:  fmt.Sprintf("%s cannot send %s, which the request sets; it would be dropped and the call answered without it", provider, strings.Join(named, ", ")),
	}
}

// RefuseUnsupportedFields is unsupportedRequestFields for a provider that lives
// outside this package, such as the codex transport: it refuses with
// KindUnsupportedCapability, before anything is sent or run, a request that sets
// any of fields ("Tools", "ToolChoice", "WebSearch", "Messages") that the
// provider has no way to carry. A provider that cannot carry a field says so
// here, in the one wording every provider uses, and never drops it.
func RefuseUnsupportedFields(req CompletionRequest, provider string, fields ...string) error {
	return unsupportedRequestFields(req, provider, fields...)
}

// effortNotSupported refuses a request that asks for a reasoning effort on a
// provider path that cannot set one. An operation's preference is not a
// request, and passes.
func effortNotSupported(req CompletionRequest, provider string) error {
	if req.ReasoningEffort == "" || req.ReasoningEffortPreferred {
		return nil
	}
	return &types.OperationError{
		Kind:     types.KindUnsupportedCapability,
		Provider: provider,
		Model:    req.Model,
		Message:  "reasoning effort \"" + req.ReasoningEffort + "\" was asked for, and this provider cannot set one",
	}
}
