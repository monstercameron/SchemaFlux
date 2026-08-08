package mw

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/pricing"
)

// Budget refuses a Complete call whose estimated cost would push spend past a
// fixed ceiling, checked BEFORE the provider is ever called.
//
// This is the difference between a budget and a receipt. pricing.CheckBudget
// (PR-005) already refuses an exhausted process-wide, periodic (daily/
// weekly/monthly) budget before CallLLM builds a request; this middleware is
// a different, narrower thing on purpose -- a fixed ceiling scoped to
// whichever *Client this wraps via WithProviderInstance, with no process-wide
// state and no period rollover. A caller wanting per-tenant or per-batch
// spend caps composes one mw.Budget per scope; a caller wanting the
// account-wide daily/weekly/monthly alerting keeps using pricing.SetBudget.
// The two are independent and may be composed in the same Chain.
//
// # Concurrency
//
// A budget two concurrent goroutines can both pass is not a budget: goroutine
// A checks "would this exceed the limit", sees no, and is preempted before
// it calls the provider; goroutine B runs the identical check against the
// same not-yet-updated total, also sees no, and now both calls proceed even
// though only one of them fit. Refusing AFTER the call returns has the same
// hole in the other direction -- the money is already spent by the time
// either goroutine finds out.
//
// This is closed by treating "check" and "spend" as one atomic step: the
// estimated cost is RESERVED against the ledger, under the same mutex that
// performs the check, before the provider is called at all. A second
// goroutine arriving concurrently sees the first goroutine's reservation
// already counted against the limit, so the two together cannot both pass a
// check that only one of them fits. Once the call returns, the reservation
// is corrected to the measured cost (or released entirely on failure --
// every provider in this codebase returns a zero-value response on every
// error path, so nothing was actually billed).
func Budget(limit float64, opts ...BudgetOption) Middleware {
	cfg := budgetConfig{calc: pricing.CalculateCost}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	return func(next Handler) Handler {
		return &budgetProvider{next: next, limit: limit, calc: cfg.calc}
	}
}

// BudgetOption configures Budget beyond the ceiling every caller supplies.
type BudgetOption func(*budgetConfig)

type budgetConfig struct {
	// calc measures the actual cost of a completed call, reconciling the
	// pre-call reservation to what was really spent. Nil disables
	// reconciliation: every call is charged exactly its pre-call estimate,
	// which is what a test wants when it is not exercising pricing's rate
	// table at all.
	calc func(usage *types.TokenUsage, model, provider string) *types.CostInfo
}

// WithBudgetCostFunc overrides how actual spend is measured after a call
// succeeds. The default is pricing.CalculateCost, the same measured-cost
// function the rest of the library uses -- so an unregistered model reports
// unpriced rather than a guessed figure (PR-001's rule), and the pre-call
// estimate is what the ledger keeps in that case. Pass nil to disable
// reconciliation entirely and always charge the pre-call estimate.
func WithBudgetCostFunc(calc func(usage *types.TokenUsage, model, provider string) *types.CostInfo) BudgetOption {
	return func(cfg *budgetConfig) { cfg.calc = calc }
}

type budgetProvider struct {
	next  Handler
	limit float64
	calc  func(usage *types.TokenUsage, model, provider string) *types.CostInfo

	mu    sync.Mutex
	spent float64
}

func (p *budgetProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	est := p.next.EstimateCost(req)
	if est < 0 {
		// A negative estimate would let a call spend down the ledger; treat
		// an implausible estimate as zero rather than as free money.
		est = 0
	}

	p.mu.Lock()
	if p.limit > 0 && p.spent+est > p.limit {
		remaining := p.limit - p.spent
		if remaining < 0 {
			remaining = 0
		}
		p.mu.Unlock()
		return llm.CompletionResponse{}, &types.OperationError{
			Kind: types.KindBudgetExceeded,
			Op:   "mw.Budget",
			Message: fmt.Sprintf(
				"estimated cost %.6f would exceed remaining budget %.6f of %.6f",
				est, remaining, p.limit),
		}
	}
	// Reserve before the call, inside the same critical section as the
	// check above -- see the concurrency note on Budget.
	p.spent += est
	p.mu.Unlock()

	resp, err := p.next.Complete(ctx, req)

	p.mu.Lock()
	switch {
	case err != nil:
		// Every provider in this package returns a zero-value
		// CompletionResponse on every error path (see internal/llm's
		// provider.go), so nothing was actually billed. Release the
		// reservation rather than leaving a phantom charge that a caller
		// retrying the same budget would never be able to recover from.
		p.spent -= est
	case p.calc != nil:
		if info := p.calc(&resp.Usage, resp.Model, resp.Provider); info != nil && info.Priced {
			p.spent += info.TotalCost - est
		}
		// An unpriced or nil result leaves the reservation exactly as it
		// was: an unpriced call is not a free one (PR-001), so the pre-call
		// estimate stands in for the unknown real figure rather than being
		// zeroed out.
	}
	if p.spent < 0 {
		p.spent = 0
	}
	p.mu.Unlock()

	return resp, err
}

func (p *budgetProvider) Name() string { return p.next.Name() }

func (p *budgetProvider) EstimateCost(req llm.CompletionRequest) float64 {
	return p.next.EstimateCost(req)
}

func (p *budgetProvider) RetryPolicy() (int, time.Duration) {
	return p.next.RetryPolicy()
}

// Spent reports the ledger's current total, reservations included. It exists
// for tests and for a caller that wants to surface remaining budget to an
// operator; it is not part of the llm.Provider interface.
func (p *budgetProvider) Spent() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.spent
}
