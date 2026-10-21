package schemaflux

import (
	"context"
)

// A Provider sends one request to a model and returns its answer. The built-in
// ones come from the provider package; the mw package wraps one in retry,
// caching, rate limiting and fallback; schemafluxtest has one for tests.
//
// Complete must be safe for concurrent use. It returns the model's answer as it
// came: the library parses and checks it. To report a failure the library can
// act on, return an *Error with its Kind set (ErrRateLimited with RetryAfter,
// ErrUnauthorized, ErrUnavailable); any other error is taken to be the provider
// being unavailable, except a context error, which is a cancellation or a
// timeout, and an error of the library's own packages, which already says what
// it is.
//
// A Provider written outside this module has no model for a tier unless it says
// which: a call on it names one with Client.Model or Options.Model, or fails with
// ErrConfiguration, and a Provider that has a ModelFor(Tier) method is asked for
// the model of the tier the call is at.
//
// A provider may also implement any of the optional interfaces of the provider
// package: Streamer (answers as they arrive), Warmer (open the connection ahead
// of the first call), Retrier (its own retry settings; without them a failed
// request is sent again as the client's Retries says, or 3 times by default),
// Decider (the decision limits of a decision provider, DecisionLimits), and
// Unwrapper (what it wraps, so that a chain can be warmed through it). The
// library looks for each optional interface, and for ModelFor, on the provider
// and then on what it wraps (Unwrapper); a wrapper that hides its inner provider
// hides its streaming, warming, retry settings, decision limits and models.
//
// A panic in Complete, in Stream or in Warm is recovered by the library and
// returned as an error of kind ErrInternal that names the type of what was
// thrown and nothing of its value.
type Provider interface {
	// Complete sends req and returns the answer.
	Complete(ctx context.Context, req Request) (Response, error)

	// Name is the provider's name, in lower case, as a DataPolicy and a price
	// table know it: "openai", "anthropic".
	Name() string
}

// A Request is one request to a model, as the library built it. Tools and
// Messages are set by chat sessions only; no operation sets them.
//
// The Schema map is shared with every other request for the same type and is
// read-only. A Provider or a middleware that needs a changed schema copies it
// first. The same holds for Tools and Messages and what they hold.
type Request struct {
	// Model is the model asked for. Empty means the provider's own default.
	Model string

	// System is the system prompt: the library's instructions, the same for
	// every call of one kind. Empty means none.
	System string

	// User is the user message: the caller's steering and the caller's data.
	// Empty means none.
	User string

	// Temperature is the sampling temperature. Zero leaves the provider's
	// default, so a temperature of exactly zero cannot be asked for.
	Temperature float64

	// MaxTokens is the output ceiling. Zero leaves the provider's default.
	MaxTokens int

	// Format is "json" or "text". Empty leaves it to the provider.
	Format string

	// Schema is the JSON Schema the answer must fit. Nil means the answer is
	// not held to a declared schema.
	Schema map[string]any

	// SchemaName is the schema's name on the wire. Empty when Schema is nil.
	SchemaName string

	// Effort is the reasoning effort. The zero value is the provider's default.
	Effort Effort

	// EffortPreferred says whose wish Effort is. True means the operation would
	// like it: drop it if the model does not take one. False means the caller
	// asked for it: refuse the request instead.
	EffortPreferred bool

	// CacheKey is a hint for the provider's prompt cache. It names the stable
	// prefix of the request, never the data. Empty means none.
	CacheKey string

	// WebSearch lets the model search the web. False sends the request without
	// the tool.
	WebSearch bool

	// Decision is what the operation is deciding, typed, when it is deciding
	// something a decision model can answer: a yes-or-no, a choice among
	// labels, a level on a scale. A provider that generates text ignores it. A
	// decision provider answers it and never reads the prompts. Nil means the
	// request is not a decision.
	Decision *Decision

	// Tools are the functions the model may call instead of answering. Nil means
	// none. A provider that cannot take tools returns an *Error of kind
	// ErrUnsupported before anything is sent. Read-only and shared.
	//
	// The library cannot tell that a provider written outside it ignored this
	// field: it would answer in words a request that asked for a call, and the
	// answer would look like any other. A provider that cannot take tools makes
	// this check first, which is one line:
	//
	//	if len(req.Tools) > 0 || len(req.Messages) > 0 {
	//		return schemaflux.Response{}, &schemaflux.Error{Kind: schemaflux.ErrUnsupported, Message: "this provider takes no tools or message lists"}
	//	}
	Tools []Tool

	// Messages is the conversation so far, oldest first. When it is not empty it
	// replaces User as the input of the request; System is still the system
	// prompt. Nil means the request is the single user message in User. A provider
	// that cannot take a message list returns ErrUnsupported before anything is
	// sent, with the check given at Tools: one that read only User would answer
	// the empty prompt of a request whose conversation is here. Read-only and
	// shared.
	Messages []Message
}

// A Response is a model's answer to one Request.
type Response struct {
	// Content is the answer's text. Empty from a decision provider, whose
	// answers are in Answers.
	Content string

	// Usage is what the provider says it used. The zero value means it said
	// nothing, which is not a count of zero.
	Usage Usage

	// Model is the model that answered, when the provider says. Empty means it
	// did not, and the model asked for is taken to have answered.
	Model string

	// Provider is the provider that answered, when it is not the one that was
	// asked (a router, a fallback). Empty means the one that was asked.
	Provider string

	// Upstream is the provider a gateway passed the request to, when it says.
	// Empty means it did not.
	Upstream string

	// FinishReason is the provider's own word for why the answer ended: "stop",
	// "length", "tool_calls", ... Empty means it gave none.
	FinishReason string

	// ServiceTier is the service tier the provider says it answered on. Empty
	// means it said none.
	ServiceTier string

	// Cached is true when a response cache answered and no provider was called.
	// Such an answer cost nothing.
	Cached bool

	// Hedged is true when a second copy of the request was sent because the
	// first was slow, whichever answered.
	Hedged bool

	// Timing is where the time of the HTTP call went. The zero value means the
	// provider does not time its calls, which is not a measurement of an
	// instant call.
	Timing Timing

	// Answers are a decision provider's typed answers, one per question, in
	// order. Empty from a provider that answered with text.
	Answers []DecisionAnswer

	// Escalation says that a decision provider was asked first and its answer
	// was passed over, by whom, and why. Nil means nothing was passed over.
	Escalation *DecisionEscalation

	// ToolCalls are the calls the model asked for, instead of or beside Content.
	// When there are any, an empty Content is a complete answer and not a refusal
	// to answer. Nil means the model answered in words. A provider that returns
	// calls sets FinishReason to "tool_calls" when its vendor says so. The calls
	// are the model's words and are not run by the library.
	ToolCalls []ToolCall
}

// A Chunk is one event of a stream: a piece of the answer as it is written. A
// stream is a sequence of chunks that carry text and a last chunk that has Done
// set and holds the whole answer; a stream that breaks is an error and never a
// short answer.
type Chunk struct {
	// Delta is the text this event adds to the answer. Empty on the last event
	// and on an event that carries no text.
	Delta string

	// Done is true on the last event of a stream that completed.
	Done bool

	// Response is the whole answer, its text in Response.Content, with the
	// usage and the other facts of a Response. It is set only when Done is true,
	// and is the zero Response before that.
	Response Response
}

// Usage is the token count of one answer, as the provider reports it. The zero
// value means the provider reported none.
type Usage struct {
	// Input is the tokens in the request.
	Input int `json:"input"`

	// Output is the tokens in the answer, reasoning included.
	Output int `json:"output"`

	// Total is Input plus Output.
	Total int `json:"total"`

	// Cached is, of Input, the tokens the provider read from its prompt cache.
	Cached int `json:"cached,omitempty"`

	// CacheWrite is, of Input, the tokens the provider wrote to its prompt
	// cache.
	CacheWrite int `json:"cache_write,omitempty"`

	// Reasoning is, of Output, the tokens spent reasoning.
	Reasoning int `json:"reasoning,omitempty"`
}
