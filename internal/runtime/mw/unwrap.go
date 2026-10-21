package mw

// Every handler that wraps one provider says which, so that a call that needs
// the provider itself, llm.Warm opening its connection, can reach it through a
// chain. Without this a chain is opaque to such a call: warming a provider
// built with Chain would do nothing.

// Unwrap is the wrapped provider.
func (p *budgetProvider) Unwrap() Handler { return p.next }

// Unwrap is the wrapped provider.
func (p *bulkheadProvider) Unwrap() Handler { return p.next }

// Unwrap is the wrapped provider.
func (h *cacheHandler) Unwrap() Handler { return h.next }

// Unwrap is the wrapped provider.
func (p *circuitBreakerProvider) Unwrap() Handler { return p.next }

// Unwrap is the primary route, the one a request is sent to first and the one
// that is warmed. An alternate's connection is opened by its own first use.
func (p *fallbackProvider) Unwrap() Handler { return p.primary.Handler }

// Unwrap is the wrapped provider.
func (p *rateLimitedProvider) Unwrap() Handler { return p.next }

// Unwrap is the wrapped provider.
func (p *redactingProvider) Unwrap() Handler { return p.next }

// Unwrap is the wrapped provider.
func (p *retryProvider) Unwrap() Handler { return p.next }
