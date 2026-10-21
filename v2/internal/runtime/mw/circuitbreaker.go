package mw

import (
	"context"
	"sync"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// circuitBreakerMaxModelKeys bounds how many distinct "provider/model" keys
// KeyByModel creates. The model comes from the request, which is the caller's
// string, and the underlying breaker never forgets a key. A caller that varies
// the model without limit would otherwise grow that map for the life of the
// process; past this many keys a new model is tracked under its provider's key
// alone.
const circuitBreakerMaxModelKeys = 256

// CircuitBreaker wraps a Handler with a breaker keyed by provider name and,
// optionally, model -- SC-002's "circuit breakers keyed by endpoint and
// optionally model". The state machine itself (internal/ops.CircuitBreaker)
// is not duplicated here: this file is a thin adapter translating a
// Complete call into the keyed Allow/report protocol, the same shape
// mw.RateLimit and mw.Retry already use to sit in front of a Handler.
//
// Only failures of the provider count toward opening the breaker. These say
// nothing about the endpoint and are not counted: a cancellation by the caller
// and a deadline the caller set, a request the provider rejected as the
// caller's fault (400, 401, 403, 404, 422 and the other classified
// authentication, permission, invalid-request, configuration and size kinds),
// a refusal by the data policy or the budget, a refusal by an inner breaker or
// scheduler, and an answer that came back and was no good (cut off, malformed,
// failing its checks, or held for review), since a model's bad answer is not an
// outage. A rate limit (429), a 5xx, a timeout of the provider's
// own and a network failure do count, and so does an error that carries no
// classification, since nothing says it was the caller's doing. A panic in the
// wrapped provider is reported as a failure and then continues to unwind, and so
// is an error that says a request panicked below (an invariant violation marked
// types.NarrowPanic).
//
// A call that is not counted and was admitted as a half-open probe has no
// outcome to report, and internal/ops offers no way to give a probe slot back
// without one. The slot is released by reporting the call as a success, which
// is the outcome that does not hold the endpoint against itself; a breaker
// that is half-open therefore closes on a probe the caller cancelled. A call
// admitted while the breaker was closed reports nothing at all, so a
// cancellation neither adds to the count of consecutive failures nor clears it.
//
// KeyByModel keys are bounded (see circuitBreakerMaxModelKeys): models seen
// after the bound share their provider's key.
//
// The breaker belongs to the Middleware this returns, not to a Handler: the one
// Middleware applied to two providers shares its state between them, keyed by
// provider name, so two providers with the same name trip together.
//
// It composes with mw.Retry as a refusal that is not repeated. An open circuit
// is a refusal made without sending anything, and a breaker stays open far longer
// than a backoff, so a Retry outside the breaker returns the refusal at once
// (see retryRepeats), and a Fallback outside both fails over on it at once. A
// Retry inside the breaker never sees the refusal, since the breaker refuses
// before it is reached.
func CircuitBreaker(cfg ops.CircuitBreakerConfig, opts ...CircuitBreakerOption) Middleware {
	b := ops.NewCircuitBreaker(cfg)
	keyCfg := circuitBreakerKeyConfig{byModel: false}
	for _, opt := range opts {
		if opt != nil {
			opt(&keyCfg)
		}
	}
	models := &circuitBreakerModelKeys{seen: make(map[string]struct{})}

	return func(next Handler) Handler {
		return &circuitBreakerProvider{next: next, breaker: b, keyCfg: keyCfg, models: models}
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

// circuitBreakerModelKeys remembers which "provider/model" keys have been
// handed out, so their number can be bounded.
type circuitBreakerModelKeys struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

// admit reports whether key may be used as a breaker key: it already is one, or
// there is still room for another.
func (k *circuitBreakerModelKeys) admit(key string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, ok := k.seen[key]; ok {
		return true
	}
	if len(k.seen) >= circuitBreakerMaxModelKeys {
		return false
	}
	k.seen[key] = struct{}{}
	return true
}

type circuitBreakerProvider struct {
	next    Handler
	breaker *ops.CircuitBreaker
	keyCfg  circuitBreakerKeyConfig
	models  *circuitBreakerModelKeys
}

func (p *circuitBreakerProvider) key(req llm.CompletionRequest) string {
	name := p.next.Name()
	if !p.keyCfg.byModel || req.Model == "" {
		return name
	}
	full := name + "/" + req.Model
	if !p.models.admit(full) {
		return name
	}
	return full
}

// circuitOutcome is what a finished call says about the endpoint.
type circuitOutcome int

const (
	circuitSuccess circuitOutcome = iota
	circuitFailure
	// circuitNoVerdict is a failure that says nothing about the endpoint.
	circuitNoVerdict
)

// circuitOutcomeOf decides what err, returned for a call made under ctx, says
// about the endpoint. Classification is llm.Classify's: the one answer the rest
// of the library also uses.
func circuitOutcomeOf(ctx context.Context, err error) circuitOutcome {
	if err == nil {
		return circuitSuccess
	}
	kind := llm.Classify(err)
	if ctx.Err() != nil && (kind == types.KindCanceled || kind == types.KindTimeout) {
		// The caller's own context ended, so a cancellation or a deadline is the
		// caller's doing. A timeout with the context still live is the provider's.
		return circuitNoVerdict
	}
	switch kind {
	case types.KindCanceled, types.KindConfiguration, types.KindAuthentication,
		types.KindPermission, types.KindInvalidRequest, types.KindPolicyViolation,
		types.KindBudgetExceeded, types.KindContextTooLarge,
		types.KindUnsupportedCapability, types.KindAdmissionRejected,
		types.KindCircuitOpen, types.KindShutdown:
		return circuitNoVerdict
	case types.KindOutputTruncated, types.KindMalformedOutput, types.KindSchemaViolation,
		types.KindBatchProtocolViolation, types.KindEvidenceViolation,
		types.KindRepairExhausted, types.KindReviewRequired:
		// What a model wrote, or a person declined to approve, says nothing of
		// whether the endpoint is up: the call went through and came back.
		return circuitNoVerdict
	case types.KindInvariantViolation:
		// A wrong answer is the model's. A panic is a defect in the chain below,
		// and the doc says a panic is a failure.
		if types.NarrowingOf(err) != types.NarrowPanic {
			return circuitNoVerdict
		}
	}
	return circuitFailure
}

func (p *circuitBreakerProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	key := p.key(req)
	// Read before Allow, which is what moves an elapsed open breaker to
	// half-open: a call admitted while the breaker was anything but closed holds
	// a probe slot that has to be given back.
	mayHoldProbe := p.breaker.State(key) != ops.CircuitClosed
	report, err := p.breaker.Allow(key)
	if err != nil {
		return llm.CompletionResponse{}, err
	}

	returned := false
	defer func() {
		if !returned {
			// The provider panicked. The slot a probe holds must not be kept for
			// good, and a panic is a failure of the endpoint; the panic itself
			// continues past this function.
			report(false)
		}
	}()
	resp, callErr := p.next.Complete(ctx, req)
	returned = true

	switch circuitOutcomeOf(ctx, callErr) {
	case circuitSuccess:
		report(true)
	case circuitFailure:
		report(false)
	case circuitNoVerdict:
		if mayHoldProbe {
			report(true)
		}
	}
	return resp, callErr
}

func (p *circuitBreakerProvider) Name() string { return p.next.Name() }

func (p *circuitBreakerProvider) EstimateCost(req llm.CompletionRequest) float64 {
	return p.next.EstimateCost(req)
}

func (p *circuitBreakerProvider) RetryPolicy() (int, time.Duration) {
	return p.next.RetryPolicy()
}
