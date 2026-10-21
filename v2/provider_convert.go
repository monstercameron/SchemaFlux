package schemaflux

import (
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

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
