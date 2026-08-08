package mw

import (
	"context"
	"sync"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// RateLimit throttles Complete calls to at most n requests per interval,
// enforced client-side rather than by whatever the provider does with a 429.
//
// The two exist for different reasons. A provider's rate limit protects the
// provider and is discovered by being hit; this one protects the caller's
// own budget and quota headroom and is set on purpose, before the first
// request -- a batch job that fires 500 requests at once still wants to stay
// under a self-imposed ceiling even on a provider that would have accepted
// them all.
//
// By default a call that would exceed the limit blocks until a slot opens,
// bounded by ctx: a caller who wants to smooth a burst rather than shed it
// gets that without writing their own queue. Reject() switches to failing
// immediately instead, for a caller who would rather drop a request than
// wait for one.
func RateLimit(n int, interval time.Duration, opts ...RateLimitOption) Middleware {
	if n <= 0 {
		n = 1
	}
	if interval <= 0 {
		interval = time.Second
	}

	cfg := rateLimiterConfig{burst: n}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if cfg.burst <= 0 {
		cfg.burst = n
	}

	bucket := newTokenBucket(n, interval, cfg.burst, time.Now)

	return func(next Handler) Handler {
		return &rateLimitedProvider{next: next, bucket: bucket, reject: cfg.reject}
	}
}

// RateLimitOption configures RateLimit beyond the requests-per-interval pair
// every caller has to supply.
type RateLimitOption func(*rateLimiterConfig)

type rateLimiterConfig struct {
	burst  int
	reject bool
}

// WithBurst sets the bucket's capacity separately from its refill rate, so a
// caller can allow an initial burst of b requests even though the sustained
// rate is n per interval. Without this, burst always equals n, which is
// right for most callers and wrong for one that wants a large initial
// allowance with a slow refill.
func WithBurst(b int) RateLimitOption {
	return func(c *rateLimiterConfig) {
		if b > 0 {
			c.burst = b
		}
	}
}

// Reject makes RateLimit fail a call that would exceed the limit instead of
// waiting for a slot. The error is a *types.OperationError with
// types.KindRateLimited, so it flows into mw.Retry and any other consumer of
// llm.Classify exactly like a provider's own 429 would -- a client-side
// throttle and a server-side one are the same kind of failure to a caller
// deciding whether to retry, and answering that question in two different
// ways depending on which layer refused the request is the bug A-007
// already closed for provider errors.
func Reject() RateLimitOption {
	return func(c *rateLimiterConfig) { c.reject = true }
}

// rateLimitedProvider wraps a Handler with client-side pacing. It delegates
// everything except Complete unchanged: Name, EstimateCost, and RetryPolicy
// describe the underlying provider, and a middleware that pretended
// otherwise would answer a caller's "which provider is this" or "what would
// this cost" incorrectly.
type rateLimitedProvider struct {
	next   Handler
	bucket *tokenBucket
	reject bool
}

func (p *rateLimitedProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	if err := p.bucket.wait(ctx, p.reject); err != nil {
		return llm.CompletionResponse{}, err
	}
	return p.next.Complete(ctx, req)
}

func (p *rateLimitedProvider) Name() string { return p.next.Name() }

func (p *rateLimitedProvider) EstimateCost(req llm.CompletionRequest) float64 {
	return p.next.EstimateCost(req)
}

func (p *rateLimitedProvider) RetryPolicy() (int, time.Duration) {
	return p.next.RetryPolicy()
}

// tokenBucket is a standard token bucket: capacity tokens available at
// start, refilled continuously at rate tokens per second, each Complete call
// spending one.
//
// now and sleep are hooks rather than direct calls to the time package so a
// test can replace both with a fake clock. A rate limiter test that uses the
// real clock either waits out real seconds -- slow, and still capable of
// flaking under a loaded CI runner -- or asserts on wall-clock timestamps
// with a tolerance window wide enough to be nearly meaningless. Faking the
// clock keeps the test exact and instant.
type tokenBucket struct {
	mu       sync.Mutex
	rate     float64 // tokens per second
	capacity float64
	tokens   float64
	updated  time.Time

	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
}

func newTokenBucket(n int, interval time.Duration, capacity int, now func() time.Time) *tokenBucket {
	return &tokenBucket{
		rate:     float64(n) / interval.Seconds(),
		capacity: float64(capacity),
		tokens:   float64(capacity), // starts full, so the first burst up to capacity is immediate
		updated:  now(),
		now:      now,
		sleep:    realSleep,
	}
}

// tryAcquire refreshes the bucket for elapsed time and, if a token is
// available, spends it. It never spends a token it does not have -- a reject
// caller whose request was refused must not have paid for a slot nobody
// used, or every refusal would silently push back everyone waiting behind
// it.
func (b *tokenBucket) tryAcquire() (ok bool, wait time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	if elapsed := now.Sub(b.updated).Seconds(); elapsed > 0 {
		b.tokens += elapsed * b.rate
		if b.tokens > b.capacity {
			b.tokens = b.capacity
		}
		b.updated = now
	}

	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}

	needed := 1 - b.tokens
	return false, time.Duration(needed / b.rate * float64(time.Second))
}

// wait blocks (or, in reject mode, fails) until a token is available or ctx
// ends. The loop re-checks after every sleep rather than trusting the wait it
// computed, because under concurrent callers another goroutine can spend the
// token this one was waiting for in between the computation and the wake-up.
func (b *tokenBucket) wait(ctx context.Context, reject bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for {
		ok, waitFor := b.tryAcquire()
		if ok {
			return nil
		}
		if reject {
			return &types.OperationError{
				Kind:       types.KindRateLimited,
				Op:         "mw.RateLimit",
				RetryAfter: waitFor,
				Message:    "client-side rate limit exceeded",
			}
		}
		if err := b.sleep(ctx, waitFor); err != nil {
			return err
		}
	}
}
