package schemaflux

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
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

// requestToInternal is the internal request for a Request. Every field of a
// Request has a field of the internal one; the one internal field that has no
// field of a Request, ToolChoice, is left zero. A request that decides and also
// carries tools or messages converts as it is: refusing it is the work of
// requestFromInternal and externalProvider, which can return an error.
func requestToInternal(r Request) llm.CompletionRequest {
	return llm.CompletionRequest{
		Model:                    r.Model,
		SystemPrompt:             r.System,
		UserPrompt:               r.User,
		Temperature:              r.Temperature,
		MaxTokens:                r.MaxTokens,
		ResponseFormat:           r.Format,
		JSONSchema:               r.Schema,
		SchemaName:               r.SchemaName,
		ReasoningEffort:          string(r.Effort),
		ReasoningEffortPreferred: r.EffortPreferred,
		PromptCacheKey:           r.CacheKey,
		WebSearch:                r.WebSearch,
		Decision:                 r.Decision.internal(),
		Tools:                    toolsToInternal(r.Tools),
		Messages:                 messagesToInternal(r.Messages),
	}
}

// requestFromInternal is the Request for an internal request. A request that
// sets a tool choice has nothing in a Request to carry it, and is refused with
// ErrUnsupported and not passed on with it dropped: the provider would answer a
// different question from the one asked. No code of the library sets it. A
// request that decides and also carries tools or a message list is refused with
// ErrInvalidRequest: a decision provider answers the decision and cannot honour
// the rest, and answering one while dropping the other is the failure the library
// does not do. Neither message names what the request holds.
func requestFromInternal(r llm.CompletionRequest) (Request, error) {
	if r.ToolChoice != "" {
		return Request{}, &Error{Kind: ErrUnsupported, Message: "a tool choice is not carried by a Provider"}
	}
	if err := refuseDecisionWithTools(r.Decision != nil, len(r.Tools) > 0 || len(r.Messages) > 0); err != nil {
		return Request{}, err
	}
	return Request{
		Model:           r.Model,
		System:          r.SystemPrompt,
		User:            r.UserPrompt,
		Temperature:     r.Temperature,
		MaxTokens:       r.MaxTokens,
		Format:          r.ResponseFormat,
		Schema:          r.JSONSchema,
		SchemaName:      r.SchemaName,
		Effort:          Effort(r.ReasoningEffort),
		EffortPreferred: r.ReasoningEffortPreferred,
		CacheKey:        r.PromptCacheKey,
		WebSearch:       r.WebSearch,
		Decision:        decisionFromInternal(r.Decision),
		Tools:           toolsFromInternal(r.Tools),
		Messages:        messagesFromInternal(r.Messages),
	}, nil
}

// refuseDecisionWithTools is the refusal of a request that is a decision and
// also has tools or messages, or nil.
func refuseDecisionWithTools(decides, hasTools bool) error {
	if decides && hasTools {
		return &Error{Kind: ErrInvalidRequest, Message: "a decision carries no tools and no message list"}
	}
	return nil
}

// responseToInternal is the internal response for a Response.
func responseToInternal(r Response) llm.CompletionResponse {
	return llm.CompletionResponse{
		Content:            r.Content,
		Usage:              usageToInternal(r.Usage),
		Model:              r.Model,
		Provider:           r.Provider,
		FinishReason:       r.FinishReason,
		UpstreamProvider:   r.Upstream,
		Cached:             r.Cached,
		Hedged:             r.Hedged,
		ServiceTier:        r.ServiceTier,
		Timing:             timingToInternal(r.Timing),
		DecisionAnswers:    answersToInternal(r.Answers),
		DecisionEscalation: escalationToInternal(r.Escalation),
		ToolCalls:          callsToInternal(r.ToolCalls),
	}
}

// responseFromInternal is the Response for an internal response. It carries
// every field of the internal one, tool calls included, so it cannot fail; the
// error beside the Response is the shape the registered conversion has, which a
// response that could not be carried would use.
func responseFromInternal(r llm.CompletionResponse) (Response, error) {
	return Response{
		Content:      r.Content,
		Usage:        usageFromInternal(r.Usage),
		Model:        r.Model,
		Provider:     r.Provider,
		Upstream:     r.UpstreamProvider,
		FinishReason: r.FinishReason,
		ServiceTier:  r.ServiceTier,
		Cached:       r.Cached,
		Hedged:       r.Hedged,
		Timing:       timingFromInternal(r.Timing),
		Answers:      answersFromInternal(r.DecisionAnswers),
		Escalation:   escalationFromInternal(r.DecisionEscalation),
		ToolCalls:    callsFromInternal(r.ToolCalls),
	}, nil
}

// chunkToInternal is the internal stream event for a Chunk.
func chunkToInternal(c Chunk) llm.StreamChunk {
	out := llm.StreamChunk{Delta: c.Delta, Done: c.Done}
	if c.Done {
		out.Response = responseToInternal(c.Response)
	}
	return out
}

// chunkFromInternal is the Chunk for an internal stream event.
func chunkFromInternal(c llm.StreamChunk) (Chunk, error) {
	out := Chunk{Delta: c.Delta, Done: c.Done}
	if c.Done {
		resp, err := responseFromInternal(c.Response)
		if err != nil {
			return Chunk{}, err
		}
		out.Response = resp
	}
	return out, nil
}

// usageToInternal is the internal token count for a Usage. A Total the
// provider left at zero beside an Input or an Output is their sum, which is
// what Total is defined to be; a Total it gave is kept as it is.
func usageToInternal(u Usage) types.TokenUsage {
	total := u.Total
	if total == 0 {
		total = types.SumInt(u.Input, u.Output)
	}
	return types.TokenUsage{
		PromptTokens:     u.Input,
		CompletionTokens: u.Output,
		TotalTokens:      total,
		CachedTokens:     u.Cached,
		CacheWriteTokens: u.CacheWrite,
		ReasoningTokens:  u.Reasoning,
	}
}

// usageFromInternal is the Usage for an internal token count. A provider
// reports its counts as PromptTokens and CompletionTokens or as InputTokens and
// OutputTokens (a codex-style provider reports only the second), so each of
// Input and Output is read from the first spelling and, when that is zero, from
// the second. A provider that reports both and differs is taken at its prompt
// and completion counts, which are the ones the call path itself fills.
func usageFromInternal(u types.TokenUsage) Usage {
	input, output := u.PromptTokens, u.CompletionTokens
	if input == 0 {
		input = u.InputTokens
	}
	if output == 0 {
		output = u.OutputTokens
	}
	return Usage{
		Input:      input,
		Output:     output,
		Total:      u.TotalTokens,
		Cached:     u.CachedTokens,
		CacheWrite: u.CacheWriteTokens,
		Reasoning:  u.ReasoningTokens,
	}
}

// The optional interfaces of a Provider as the root names them. The provider
// package declares the same methods under the names Warmer, Retrier, Decider
// and Streamer; the root cannot import it, and an interface is satisfied by its
// method set, so a provider written against those is found here.
type (
	optionalWarmer interface {
		Warm(ctx context.Context) error
	}
	optionalRetrier interface {
		RetryPolicy() (retries int, backoff time.Duration)
	}
	optionalDecider  interface{ DecisionLimits() DecisionLimits }
	optionalStreamer interface {
		Stream(ctx context.Context, req Request) iter.Seq2[Chunk, error]
	}
	optionalTierModels interface{ ModelFor(tier Tier) string }

	// internalSeam is what the provider package's adapter of a built-in provider
	// has: the internal provider it was made from.
	internalSeam interface{ InternalProvider() llm.Provider }
)

// internalProvider is a Provider seen as an llm.Provider. It answers each
// optional interface of the call path (warming, streaming, the model of a tier,
// the model sent for a request) from the provider, and then from what the
// provider wraps, so that a wrapper which does not hide what is under it hides
// none of it. under is the built-in provider at the bottom of a chain of
// wrappers, when there is one: a built-in made by the provider package carries
// its internal provider, and that is the one that knows its tier models, its
// streaming and the model it sends.
type internalProvider struct {
	provider Provider
	under    llm.Provider
}

// internalDecider is an internalProvider whose provider, or what it wraps,
// declares decision limits. It is a type of its own because the call path finds
// limits by the method, and a provider that declares none must not have it.
type internalDecider struct{ internalProvider }

// Complete converts the request, calls the provider and converts the answer. An
// error the provider returned is given the kind the call path reads, see
// providerFailure.
//
// A panic in the provider is recovered here and returned as an error of kind
// ErrInternal: the provider is the caller's code, it runs on the goroutine of the
// call, and a panic that crossed the API would take the caller's program down
// from inside an operation. The error names the type of what was thrown and
// nothing of its value, which can hold the request.
func (a *internalProvider) Complete(ctx context.Context, req llm.CompletionRequest) (_ llm.CompletionResponse, failed error) {
	converted, err := requestFromInternal(req)
	if err != nil {
		return llm.CompletionResponse{}, providerFailure(err, a.provider.Name(), req.Model)
	}
	defer func() {
		if thrown := recover(); thrown != nil {
			failed = providerPanic(thrown, a.provider.Name(), req.Model)
		}
	}()
	resp, err := a.provider.Complete(ctx, converted)
	if err != nil {
		return llm.CompletionResponse{}, providerFailure(err, a.provider.Name(), req.Model)
	}
	return responseToInternal(resp), nil
}

// providerPanic is the error for a panic in a Provider: ErrInternal, carrying
// the type of what was thrown. The value is never read, since it can hold the
// request or a vendor's text, and nothing but its type goes into the message.
func providerPanic(thrown any, provider, model string) error {
	return providerFailure(&Error{
		Kind:    ErrInternal,
		Message: "the provider panicked with a value of type " + fmt.Sprintf("%T", thrown),
	}, provider, model)
}

// Name is the provider's own name.
func (a *internalProvider) Name() string { return a.provider.Name() }

// EstimateCost is zero: a Provider has no estimate, and the call path no longer
// asks for one.
func (a *internalProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }

// RetryPolicy is the provider's own settings when it implements
// provider.Retrier, in the convention of the call path: a Retrier answers zero
// retries for "the library's default" and a negative number for "never", and
// the call path takes a negative number for "not configured" and zero for
// "never". A provider without settings is "not configured".
func (a *internalProvider) RetryPolicy() (int, time.Duration) {
	retrier, ok := bind.Optional[optionalRetrier](a.provider)
	if !ok {
		return -1, 0
	}
	retries, backoff := retrier.RetryPolicy()
	switch {
	case retries == 0:
		retries = -1
	case retries < 0:
		retries = 0
	}
	return retries, backoff
}

// Warm opens the connection of the provider, or of what it wraps, when one of
// them can; a chain with nothing to warm is not an error.
func (a *internalProvider) Warm(ctx context.Context) (failed error) {
	warmer, ok := bind.Optional[optionalWarmer](a.provider)
	if !ok {
		return nil
	}
	defer func() {
		if thrown := recover(); thrown != nil {
			failed = providerPanic(thrown, a.provider.Name(), "")
		}
	}()
	if err := warmer.Warm(ctx); err != nil {
		return providerFailure(err, a.provider.Name(), "")
	}
	return nil
}

// ModelFor is the model the provider, or what it wraps, means by a tier, and
// the empty string when none of them names one: the call path reads that as
// "ask what this wraps" and then as "no model of the provider's own".
func (a *internalProvider) ModelFor(tier types.Speed) string {
	if models, ok := bind.Optional[optionalTierModels](a.provider); ok {
		return models.ModelFor(tierFromInternal(tier))
	}
	if a.under != nil {
		model, _ := llm.ModelFor(a.under, tier)
		return model
	}
	return ""
}

// ModelSentFor is the model the built-in provider under the chain sends for a
// requested one, which is what a data policy is checked against. A chain with
// no built-in at the bottom sends the model as it was asked.
func (a *internalProvider) ModelSentFor(requested string) string {
	if a.under != nil {
		return llm.ModelSentBy(a.under, requested)
	}
	return requested
}

// Unwrap is the built-in provider under the chain, or nil. It is how the call
// path looks through this adapter to what a wrapper hides: the optional
// interfaces of the built-in at the bottom.
func (a *internalProvider) Unwrap() llm.Provider { return a.under }

// CompleteStream streams through the provider, or through what it wraps, when
// one of them is a Streamer, and then through the built-in provider under the
// chain when that streams. With neither it yields the unsupported-capability
// error the call path gives a provider that cannot stream.
func (a *internalProvider) CompleteStream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamChunk, error] {
	if streamer, ok := bind.Optional[optionalStreamer](a.provider); ok {
		return func(yield func(llm.StreamChunk, error) bool) {
			converted, err := requestFromInternal(req)
			if err != nil {
				yield(llm.StreamChunk{}, providerFailure(err, a.provider.Name(), req.Model))
				return
			}
			// A panic in the provider's sequence is recovered as one in Complete
			// is. A panic in yield is the consumer's and is not: yielding again
			// from inside a loop body that has panicked is itself a panic.
			inYield := false
			defer func() {
				if thrown := recover(); thrown != nil {
					if inYield {
						panic(thrown)
					}
					yield(llm.StreamChunk{}, providerPanic(thrown, a.provider.Name(), req.Model))
				}
			}()
			for chunk, err := range streamer.Stream(ctx, converted) {
				if err != nil {
					inYield = true
					yield(llm.StreamChunk{}, providerFailure(err, a.provider.Name(), req.Model))
					return
				}
				inYield = true
				if !yield(chunkToInternal(chunk), nil) {
					return
				}
				inYield = false
			}
		}
	}
	if streaming, ok := a.under.(llm.StreamingProvider); ok {
		return streaming.CompleteStream(ctx, req)
	}
	name := a.provider.Name()
	return func(yield func(llm.StreamChunk, error) bool) {
		yield(llm.StreamChunk{}, types.Newf(types.KindUnsupportedCapability, "llm.stream", "%s cannot stream", name))
	}
}

// DecisionLimits is what the provider, or what it wraps, declares, and failing
// that the limits of the built-in provider under the chain.
func (d *internalDecider) DecisionLimits() llm.DecisionLimits {
	if decider, ok := bind.Optional[optionalDecider](d.provider); ok {
		return decider.DecisionLimits().internal()
	}
	limits, _ := llm.DecisionLimitsOf(d.under)
	return limits
}

// externalProvider is an llm.Provider seen as a Provider.
type externalProvider struct{ provider llm.Provider }

// Complete converts the request, calls the provider and converts the answer. An
// error the provider returned becomes an *Error, so that a caller of a
// built-in provider reads its failure the way it reads any other. A request that
// decides and also carries tools or messages is refused before the provider is
// called, as requestFromInternal refuses it on the way to a Provider.
func (a *externalProvider) Complete(ctx context.Context, req Request) (Response, error) {
	if err := refuseDecisionWithTools(req.Decision != nil, len(req.Tools) > 0 || len(req.Messages) > 0); err != nil {
		return Response{}, err
	}
	resp, err := a.provider.Complete(ctx, requestToInternal(req))
	if err != nil {
		return Response{}, fromProviderError(err)
	}
	return responseFromInternal(resp)
}

// InternalProvider is the internal provider this one adapts. It is the seam the
// conversion of a Provider to an internal one looks for, so that a built-in
// provider is not wrapped a second time and keeps what it can do beyond Complete.
func (a *externalProvider) InternalProvider() llm.Provider { return a.provider }

// Name is the provider's own name.
func (a *externalProvider) Name() string { return a.provider.Name() }

// RetryPolicy is the provider's own settings, in the convention of
// provider.Retrier: the call path's "not configured" is zero, which means the
// library's default, and its zero is a negative number, which means never.
func (a *externalProvider) RetryPolicy() (int, time.Duration) {
	retries, backoff := a.provider.RetryPolicy()
	switch {
	case retries < 0:
		retries = 0
	case retries == 0:
		retries = -1
	}
	return retries, backoff
}

// toInternalProvider is the llm.Provider for a Provider, or nil for something
// that is not one.
//
// A Provider that is a built-in one seen as a Provider, whether by this package
// (*externalProvider) or by the provider package, both of which give their
// adapters an InternalProvider method, is given back as the llm.Provider it was
// made from, so a built-in provider keeps everything it can do on the way through
// and is never wrapped a second time.
//
// A Provider that wraps another (it has Unwrap) is wrapped as any other
// Provider is, so that its Complete runs: it is the middleware. What the
// middleware does not do itself is looked for on what it wraps, and the
// built-in provider at the bottom of the chain, if there is one, is reachable
// through the adapter's own Unwrap, so that warming, streaming, the model of a
// tier and the model a request is sent as all reach it.
func toInternalProvider(provider any) llm.Provider {
	p, ok := provider.(Provider)
	if !ok || p == nil {
		return nil
	}
	if seam, ok := p.(internalSeam); ok {
		if inner := seam.InternalProvider(); inner != nil {
			return inner
		}
	}
	adapted := internalProvider{provider: p}
	if seam, ok := bind.Optional[internalSeam](p); ok {
		adapted.under = seam.InternalProvider()
	}
	if _, decides := bind.Optional[optionalDecider](p); decides {
		return &internalDecider{adapted}
	}
	if _, decides := llm.DecisionLimitsOf(adapted.under); decides {
		return &internalDecider{adapted}
	}
	return &adapted
}

// fromInternalProvider is the Provider for an llm.Provider, as an any, or nil
// for nil. An llm.Provider that is a Provider seen as one is given back as the
// Provider it was made from.
func fromInternalProvider(provider llm.Provider) any {
	switch internal := provider.(type) {
	case nil:
		return nil
	case *internalProvider:
		return internal.provider
	case *internalDecider:
		return internal.provider
	}
	return &externalProvider{provider: provider}
}

// unwrapProvider is the provider a Provider wraps, as an any, or nil: the step
// the registered Unwrap hook gives bind.Optional. The method it looks for is
// the one the provider package names Unwrapper.
func unwrapProvider(provider any) any {
	wrapper, ok := provider.(interface{ Unwrap() Provider })
	if !ok {
		return nil
	}
	if inner := wrapper.Unwrap(); inner != nil {
		return inner
	}
	return nil
}

// providerFailure is the error the call path is given for a failure of a
// Provider. An error that already carries an internal kind is the call path's
// own and is returned as it is. An *Error, or a bare Kind, anywhere in the
// chain decides the kind, the outermost first; with neither, the internal
// classifier reads what it knows (a context error, an HTTP status, a network
// timeout), and what it does not know is the provider being unavailable.
//
// The error returned has the kind, the route and the wait the call path reads
// to retry, and the provider's error in its chain, so that what the caller
// finally receives is that error with the operation filled in. A wait the
// provider asked for reaches the retry loop as a rate-limit error would carry
// it.
func providerFailure(err error, provider, model string) error {
	if err == nil {
		return nil
	}
	decided, classified := classifiedBy(err)
	if classified && decided.source != fromLibrary {
		return err
	}

	var kind Kind
	switch {
	case classified:
		kind = decided.kind
	default:
		kind = ErrUnavailable
		if known, ok := kindFromInternal(llm.Classify(err), types.NarrowNone); ok {
			kind = known
		}
	}

	f := factsOf(err, verdict{})
	internalKind, narrow := kindToInternal(kind)
	cause := err
	if f.retryAfter > 0 {
		if _, already := llm.RetryAfterFrom(err); !already {
			cause = errors.Join(err, &llm.RateLimitError{
				APIError:   &llm.APIError{Provider: provider, Model: model, StatusCode: f.status},
				RetryAfter: f.retryAfter,
			})
		}
	}
	if f.provider != "" {
		provider = f.provider
	}
	if f.model != "" {
		model = f.model
	}
	return &types.OperationError{
		Kind:       internalKind,
		Narrow:     narrow,
		Provider:   provider,
		Model:      model,
		RequestID:  f.requestID,
		RetryAfter: f.retryAfter,
		Cause:      cause,
	}
}
