package mw

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// defaultMaxDelay bounds the backoff between retries when nothing else says
// otherwise. It matches llm.MaxRetryAfter: both exist to stop a single wait
// from parking a caller's goroutine indefinitely, and picking a different
// number here would mean "how long can a retry make you wait" has two
// different answers depending on whether the wait came from a server header
// or from this package's own backoff.
const defaultMaxDelay = llm.MaxRetryAfter

// Retry wraps a Handler so a retryable failure is retried instead of
// returned. It answers exactly one question -- "should this be retried" --
// and answers it by asking llm.Classify and types.OperationError.Retryable,
// the same functions every other layer in this module uses for the same
// question. Writing a second opinion here (matching on "timeout" in the
// error string, say) is exactly what A-007 removed from the classifier: two
// layers with two answers is a bug that only reproduces sometimes, because
// which layer wins depends on where the error came from.
//
// Without options, the number of attempts and the base delay come from the
// wrapped provider's own RetryPolicy(): a provider that already knows its
// rate limit windows and typical recovery time is a better default than one
// picked without knowing which provider it will run against. WithMaxAttempts
// and WithBaseDelay override that per call site.
//
// Waits between attempts use decorrelated jitter (the AWS backoff paper's
// term for it): each wait is a random value between the base delay and three
// times the previous wait, capped. Plain exponential backoff computed the
// same way by every caller means every caller retries at the same instants,
// so a provider recovering from an outage gets hit by a synchronized second
// wave the moment it comes back up; jitter spreads that wave out.
//
// A Retry-After the provider stated, or that mw.RateLimit attached to its
// own refusal, overrides the computed wait -- the server said exactly how
// long to wait, and guessing instead of using that number is the mistake
// RateLimitError exists to prevent for provider errors, extended here to any
// error that carries the same information.
func Retry(opts ...RetryOption) Middleware {
	cfg := retryConfig{}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	return func(next Handler) Handler {
		return &retryProvider{
			next:      next,
			cfg:       cfg,
			sleep:     realSleep,
			randFloat: rand.Float64,
		}
	}
}

// RetryOption configures Retry beyond the provider-supplied defaults.
type RetryOption func(*retryConfig)

type retryConfig struct {
	maxAttempts int // 0 means "ask the provider"
	baseDelay   time.Duration
	maxDelay    time.Duration
}

// WithMaxAttempts sets the total number of attempts (the first call plus its
// retries), overriding the wrapped provider's RetryPolicy(). n<=0 is treated
// as 1 -- a "retry" middleware configured to make zero attempts would never
// call the provider at all, which is a different feature (a circuit
// breaker's open state) wearing this one's name.
func WithMaxAttempts(n int) RetryOption {
	return func(c *retryConfig) {
		if n <= 0 {
			n = 1
		}
		c.maxAttempts = n
	}
}

// WithBaseDelay sets the minimum wait between attempts, overriding the
// wrapped provider's RetryPolicy().
func WithBaseDelay(d time.Duration) RetryOption {
	return func(c *retryConfig) {
		if d > 0 {
			c.baseDelay = d
		}
	}
}

// WithMaxDelay caps the computed backoff (it does not cap a server-stated
// Retry-After, which is honoured as given up to llm.MaxRetryAfter).
func WithMaxDelay(d time.Duration) RetryOption {
	return func(c *retryConfig) {
		if d > 0 {
			c.maxDelay = d
		}
	}
}

type retryProvider struct {
	next  Handler
	cfg   retryConfig
	sleep func(ctx context.Context, d time.Duration) error
	// randFloat returns a value in [0,1); it exists as a field, not a direct
	// call to math/rand/v2, so a test can make the jitter deterministic
	// instead of asserting on a range wide enough to hide a mistake.
	randFloat func() float64
}

func (p *retryProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	maxAttempts, baseDelay := p.effectivePolicy()
	maxDelay := p.cfg.maxDelay
	if maxDelay <= 0 {
		maxDelay = defaultMaxDelay
	}

	var lastErr error
	prevWait := baseDelay

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err := p.next.Complete(ctx, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err

		kind := llm.Classify(err)
		if !(&types.OperationError{Kind: kind}).Retryable() {
			return resp, err
		}
		if attempt == maxAttempts {
			break
		}

		wait, stated := statedRetryAfter(err)
		if !stated {
			wait = decorrelatedJitter(baseDelay, prevWait, maxDelay, p.randFloat)
			prevWait = wait
		}

		if serr := p.sleep(ctx, wait); serr != nil {
			return llm.CompletionResponse{}, serr
		}
	}

	return llm.CompletionResponse{}, lastErr
}

func (p *retryProvider) Name() string { return p.next.Name() }

func (p *retryProvider) EstimateCost(req llm.CompletionRequest) float64 {
	return p.next.EstimateCost(req)
}

func (p *retryProvider) RetryPolicy() (int, time.Duration) {
	return p.next.RetryPolicy()
}

// effectivePolicy resolves attempts and base delay: an explicit option wins,
// otherwise the wrapped provider's own RetryPolicy() supplies it, otherwise
// a provider that answers with nothing useful (maxRetries<=0, backoff<=0)
// still gets a working default rather than a middleware that silently never
// retries.
func (p *retryProvider) effectivePolicy() (maxAttempts int, baseDelay time.Duration) {
	maxAttempts = p.cfg.maxAttempts
	baseDelay = p.cfg.baseDelay

	if maxAttempts <= 0 || baseDelay <= 0 {
		providerRetries, providerBackoff := p.next.RetryPolicy()
		if maxAttempts <= 0 {
			if providerRetries > 0 {
				maxAttempts = providerRetries + 1
			} else {
				maxAttempts = 3
			}
		}
		if baseDelay <= 0 {
			if providerBackoff > 0 {
				baseDelay = providerBackoff
			} else {
				baseDelay = 500 * time.Millisecond
			}
		}
	}
	return maxAttempts, baseDelay
}

// statedRetryAfter extracts a wait the error itself demanded, checking both
// the provider-error path (llm.RetryAfterFrom, which reads *llm.RateLimitError)
// and the OperationError path (which is what mw.RateLimit's own refusal, and
// any other layer that has adopted the shared taxonomy, carries). It reports
// a wait only when one was actually stated, never a guess.
func statedRetryAfter(err error) (time.Duration, bool) {
	if wait, limited := llm.RetryAfterFrom(err); limited && wait > 0 {
		return wait, true
	}

	var opErr *types.OperationError
	if errors.As(err, &opErr) && opErr.RetryAfter > 0 {
		return opErr.RetryAfter, true
	}

	return 0, false
}

// decorrelatedJitter implements the AWS Architecture Blog's "decorrelated
// jitter" backoff: each wait is uniformly random between base and three
// times the previous wait, capped at max. Unlike "full jitter" (uniform
// between 0 and an exponentially growing cap), each wait here is correlated
// with the previous one rather than the attempt number, which is what stops
// a burst of concurrent callers -- all starting their backoff at the same
// moment -- from re-converging on the same retry instant a few attempts in.
func decorrelatedJitter(base, prev, max time.Duration, randFloat func() float64) time.Duration {
	if base <= 0 {
		base = time.Millisecond
	}
	upper := prev * 3
	if upper < base {
		upper = base
	}

	span := upper - base
	wait := base
	if span > 0 {
		wait += time.Duration(randFloat() * float64(span))
	}

	if max > 0 && wait > max {
		wait = max
	}
	return wait
}
