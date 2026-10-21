package mw

import "github.com/monstercameron/schemaflux/v2/internal/llm"

// Every handler in this package implements llm.ModelSender by asking the
// provider it wraps. A middleware that did not would hide the wrapped
// provider's answer from llm.ModelSentBy, which reads only the outermost
// handler, and a data policy would then be checked against the model a request
// names and not the model the chain sends.

// ModelSentFor is the wrapped provider's answer.
func (p *budgetProvider) ModelSentFor(requested string) string {
	return llm.ModelSentBy(p.next, requested)
}

// ModelSentFor is the wrapped provider's answer.
func (p *bulkheadProvider) ModelSentFor(requested string) string {
	return llm.ModelSentBy(p.next, requested)
}

// ModelSentFor is the wrapped provider's answer.
func (h *cacheHandler) ModelSentFor(requested string) string {
	return llm.ModelSentBy(h.next, requested)
}

// ModelSentFor is the wrapped provider's answer.
func (p *circuitBreakerProvider) ModelSentFor(requested string) string {
	return llm.ModelSentBy(p.next, requested)
}

// ModelSentFor is the primary route's answer, the route a request is sent to
// first. An alternate is checked under its own model when it is reached.
func (p *fallbackProvider) ModelSentFor(requested string) string {
	return llm.ModelSentBy(p.primary.Handler, requested)
}

// ModelSentFor is the wrapped provider's answer.
func (h *hedged) ModelSentFor(requested string) string {
	return llm.ModelSentBy(h.next, requested)
}

// ModelSentFor is the wrapped provider's answer.
func (p *rateLimitedProvider) ModelSentFor(requested string) string {
	return llm.ModelSentBy(p.next, requested)
}

// ModelSentFor is the wrapped provider's answer.
func (p *redactingProvider) ModelSentFor(requested string) string {
	return llm.ModelSentBy(p.next, requested)
}

// ModelSentFor is the wrapped provider's answer.
func (p *retryProvider) ModelSentFor(requested string) string {
	return llm.ModelSentBy(p.next, requested)
}
