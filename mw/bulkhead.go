package mw

import (
	"context"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
)

// Bulkhead wraps a Handler with a hard concurrency wall around a single
// provider -- SC-002's "per-provider bulkheads" at the seam where a raw
// Complete call actually happens, independent of whether the caller also
// adopts internal/ops.Scheduler's queueing and fairness. The two compose:
// a caller using both gets Scheduler's PerProviderConcurrency as the
// admission-time ceiling and this as a second, narrower wall directly in
// front of the transport, which is a legitimate defense-in-depth choice a
// caller under this library's control, not something this file has to
// decide for them.
//
// limit is fixed at construction, matching the intent of
// SchedulerLimits.PerProviderConcurrency: a bulkhead whose capacity moves
// under callers already waiting on it is not a wall.
func Bulkhead(limit int, opts ...BulkheadOption) Middleware {
	if limit <= 0 {
		limit = 1
	}
	cfg := bulkheadConfig{bh: ops.NewBulkhead(), limit: limit}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	return func(next Handler) Handler {
		return &bulkheadProvider{next: next, cfg: cfg}
	}
}

// BulkheadOption configures Bulkhead beyond the fixed capacity every caller
// supplies.
type BulkheadOption func(*bulkheadConfig)

type bulkheadConfig struct {
	bh     *ops.Bulkhead
	limit  int
	reject bool
}

// RejectWhenFull makes Bulkhead fail a call immediately instead of waiting
// for a slot -- the same choice mw.RateLimit offers via Reject(), for a
// caller who would rather shed load than queue behind it.
func RejectWhenFull() BulkheadOption {
	return func(c *bulkheadConfig) { c.reject = true }
}

type bulkheadProvider struct {
	next Handler
	cfg  bulkheadConfig
}

func (p *bulkheadProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	key := p.next.Name()

	var release func()
	var err error
	if p.cfg.reject {
		release, err = p.cfg.bh.TryAcquire(key, p.cfg.limit)
	} else {
		release, err = p.cfg.bh.Acquire(ctx, key, p.cfg.limit)
	}
	if err != nil {
		return llm.CompletionResponse{}, err
	}
	defer release()

	return p.next.Complete(ctx, req)
}

func (p *bulkheadProvider) Name() string { return p.next.Name() }

func (p *bulkheadProvider) EstimateCost(req llm.CompletionRequest) float64 {
	return p.next.EstimateCost(req)
}

func (p *bulkheadProvider) RetryPolicy() (int, time.Duration) {
	return p.next.RetryPolicy()
}
