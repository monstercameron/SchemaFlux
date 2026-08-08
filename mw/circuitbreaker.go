package mw

import (
	"context"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
)

// CircuitBreaker wraps a Handler with a breaker keyed by provider name and,
// optionally, model -- SC-002's "circuit breakers keyed by endpoint and
// optionally model". The state machine itself (internal/ops.CircuitBreaker)
// is not duplicated here: this file is a thin adapter translating a
// Complete call into the keyed Allow/report protocol, the same shape
// mw.RateLimit and mw.Retry already use to sit in front of a Handler.
//
// It composes with mw.Retry exactly like a provider's own rate limit does:
// KindCircuitOpen is in types.OperationError.Retryable()'s set, so a chain
// ordered RateLimit, CircuitBreaker, Retry retries a refusal from either
// layer without the retry middleware needing to know which one produced it.
func CircuitBreaker(cfg ops.CircuitBreakerConfig, opts ...CircuitBreakerOption) Middleware {
	b := ops.NewCircuitBreaker(cfg)
	keyCfg := circuitBreakerKeyConfig{byModel: false}
	for _, opt := range opts {
		if opt != nil {
			opt(&keyCfg)
		}
	}

	return func(next Handler) Handler {
		return &circuitBreakerProvider{next: next, breaker: b, keyCfg: keyCfg}
	}
}

// CircuitBreakerOption configures how CircuitBreaker derives its key.
type CircuitBreakerOption func(*circuitBreakerKeyConfig)

type circuitBreakerKeyConfig struct {
	byModel bool
}

// KeyByModel makes the breaker key "provider/model" instead of just
// "provider" -- for a caller whose provider serves several models with
// independent failure characteristics (a broad outage on one model should
// not trip the breaker for every other model behind the same provider).
func KeyByModel() CircuitBreakerOption {
	return func(c *circuitBreakerKeyConfig) { c.byModel = true }
}

type circuitBreakerProvider struct {
	next    Handler
	breaker *ops.CircuitBreaker
	keyCfg  circuitBreakerKeyConfig
}

func (p *circuitBreakerProvider) key(req llm.CompletionRequest) string {
	name := p.next.Name()
	if !p.keyCfg.byModel || req.Model == "" {
		return name
	}
	return name + "/" + req.Model
}

func (p *circuitBreakerProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	report, err := p.breaker.Allow(p.key(req))
	if err != nil {
		return llm.CompletionResponse{}, err
	}
	resp, callErr := p.next.Complete(ctx, req)
	report(callErr == nil)
	return resp, callErr
}

func (p *circuitBreakerProvider) Name() string { return p.next.Name() }

func (p *circuitBreakerProvider) EstimateCost(req llm.CompletionRequest) float64 {
	return p.next.EstimateCost(req)
}

func (p *circuitBreakerProvider) RetryPolicy() (int, time.Duration) {
	return p.next.RetryPolicy()
}
