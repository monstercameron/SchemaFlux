package mw

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/runtime/pricing"
	"github.com/monstercameron/schemaflux/v2/internal/types"
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
// # The limit
//
// limit is the ceiling, in the unit the wrapped provider's EstimateCost
// reports (dollars). A call is allowed when the spend so far plus its estimate
// is at most the limit. A limit of zero means no limit, as a zero limit does
// elsewhere in Go and in ops.ClientBudget: no ceiling is enforced and the
// ledger is not kept. A limit that is negative, NaN or infinite is not an
// amount a ceiling can be made of. Budget returns a Middleware and has no
// error to return, so such a limit refuses every call with
// types.KindBudgetExceeded, before the provider and whatever the call is
// estimated to cost; a limit that was not a number must not be read as no
// limit. A caller that computes what is left of a budget and passes it has to
// test for zero itself: zero left is no ceiling here, not an exhausted one.
//
// An estimate that is NaN, infinite or negative is not a cost, and the call is
// refused with types.KindBudgetExceeded without reaching the provider: a ledger
// that added it would be corrupted, since after a NaN every comparison is false
// and the ceiling never refuses again. The estimate stands in for a cost that
// is not known yet, so refusing it is the safe side of not knowing.
//
// # The estimate
//
// The provider's own EstimateCost is the estimate by default. It is a rough
// figure, zero for a provider that has none, and a reservation of zero holds
// nothing against the limit, so a budget over such a provider is a tripwire that
// trips after the money is spent. WithBudgetRequestEstimate makes the estimate a
// figure of the library's own, from the request alone: its prompt at four bytes
// to a token, and its answer at the ceiling the request names (500 tokens when it
// names none), each priced by the same cost function that measures the answer,
// which is the library's price table unless WithBudgetCostFunc says otherwise.
//
// With that estimate a call is reserved before it is sent, and a call whose
// reservation would cross the limit is refused. What the spend can be is bounded:
// the ledger holds the measured cost of the calls that have ended and the
// reservations of those in flight, and a call is admitted only when the ledger
// and its own reservation are within the limit. So the spend can pass the limit
// only by what calls cost beyond their reservations, and by at most the sum of
// those under-estimates over the calls that were in flight together when the
// limit was reached: a limit of 10, calls reserved at 1 and costing 3, and a
// thousand callers at once, admit 10 and spend 30. A call whose estimate is exact
// or too high never takes the spend past the limit.
//
// A model with no price cannot be estimated, and a response that reports no
// usage cannot be measured. Neither is counted as free: the first is refused
// before it is sent, and the second is counted at its reservation.
// AllowUnpricedCalls says that the caller would rather have such a call go
// through and count as nothing than have it refused.
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
//
// A panic in the wrapped provider releases the reservation, as a failure does,
// and then continues to unwind. The measured cost comes from a function that
// may be the caller's (WithBudgetCostFunc), which runs outside the ledger's
// lock; a panic in it is recovered and logged without its value, and the call is
// kept at its estimate, as an unpriced call is. A measured cost that is NaN,
// infinite or negative is ignored the same way.
//
// The ledger belongs to the Handler this middleware builds, not to the
// Middleware value: applying one Middleware to two providers gives each its own.
func Budget(limit float64, opts ...BudgetOption) Middleware {
	cfg := budgetConfig{calc: pricing.CalculateCost}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	// Zero, and negative zero, which compares equal to it, is no limit. Any
	// other value that is not a finite positive number cannot be enforced.
	unlimited := limit == 0
	unusable := !unlimited && (math.IsNaN(limit) || math.IsInf(limit, 0) || limit < 0)

	var estimate func(ctx context.Context, req llm.CompletionRequest, provider string) (float64, bool)
	if cfg.fromRequest {
		estimate = budgetRequestEstimate(cfg.calc)
	}

	return func(next Handler) Handler {
		return &budgetProvider{next: next, limit: limit, unlimited: unlimited, unusable: unusable, calc: cfg.calc,
			estimate: estimate, allowUnpriced: cfg.allowUnpriced}
	}
}

// BudgetOption configures Budget beyond the ceiling every caller supplies.
type BudgetOption func(*budgetConfig)

type budgetConfig struct {
	// fromRequest makes the estimate the library's own, from the request, and
	// not the provider's EstimateCost. See WithBudgetRequestEstimate.
	fromRequest bool

	// allowUnpriced lets a call that cannot be priced go through and count as
	// nothing. See AllowUnpricedCalls.
	allowUnpriced bool

	// calc measures the actual cost of a completed call, reconciling the
	// pre-call reservation to what was really spent. Nil disables
	// reconciliation: every call is charged exactly its pre-call estimate,
	// which is what a test wants when it is not exercising pricing's rate
	// table at all.
	calc func(usage *types.TokenUsage, model, provider string) *types.CostInfo
}

// WithBudgetRequestEstimate makes the budget estimate a call from the request
// itself, with the cost function that measures an answer, and refuse a call
// whose model it cannot price. See the estimate section of Budget.
func WithBudgetRequestEstimate() BudgetOption {
	return func(cfg *budgetConfig) { cfg.fromRequest = true }
}

// AllowUnpricedCalls lets a call whose cost cannot be known go through: a model
// the cost function has no price for, and a response that reports no usage. Such
// a call counts as nothing against the limit. Without it a call that cannot be
// estimated is refused before it is sent, and one that cannot be measured is
// counted at its reservation. It only changes what a budget made with
// WithBudgetRequestEstimate does; a provider's own estimate is used as it is.
func AllowUnpricedCalls() BudgetOption {
	return func(cfg *budgetConfig) { cfg.allowUnpriced = true }
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
	// unlimited is a limit of zero: nothing is checked against a ceiling and
	// nothing is added to the ledger.
	unlimited bool
	// unusable is a limit that is negative, NaN or infinite: every call is
	// refused.
	unusable bool
	calc     func(usage *types.TokenUsage, model, provider string) *types.CostInfo

	// estimate prices a call from its request, and reports false for one it
	// cannot price. Nil is the wrapped provider's EstimateCost.
	estimate      func(ctx context.Context, req llm.CompletionRequest, provider string) (float64, bool)
	allowUnpriced bool

	mu    sync.Mutex
	spent float64
}

// budgetRefusal is the error a call the ledger will not take is refused with.
func budgetRefusal(message string) error {
	return &types.OperationError{Kind: types.KindBudgetExceeded, Op: "mw.Budget", Message: message}
}

// validCost reports whether x can be added to a ledger: a finite number that is
// not below zero.
func validCost(x float64) bool {
	return !math.IsNaN(x) && !math.IsInf(x, 0) && x >= 0
}

func (p *budgetProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	if p.unusable {
		return llm.CompletionResponse{}, budgetRefusal("the budget's limit is not a usable amount: it is negative, not a number or infinite")
	}
	est, priced := p.estimateOf(ctx, req)
	if !validCost(est) {
		return llm.CompletionResponse{}, budgetRefusal("the estimated cost of the call is not a usable amount")
	}
	if p.unlimited {
		return p.next.Complete(ctx, req)
	}
	if !priced && !p.allowUnpriced {
		// A call nobody can price is not a free one. Sending it would let a
		// budget be spent without its ledger ever moving.
		return llm.CompletionResponse{}, budgetRefusal("the cost of the call cannot be estimated: the model has no price, so it is not sent under a limit")
	}

	p.mu.Lock()
	if p.spent+est > p.limit {
		remaining := p.limit - p.spent
		if remaining < 0 {
			remaining = 0
		}
		p.mu.Unlock()
		return llm.CompletionResponse{}, budgetRefusal(fmt.Sprintf(
			"estimated cost %.6f would exceed remaining budget %.6f of %.6f",
			est, remaining, p.limit))
	}
	// Reserve before the call, inside the same critical section as the
	// check above -- see the concurrency note on Budget.
	p.spent += est
	p.mu.Unlock()

	returned := false
	defer func() {
		if !returned {
			// The provider panicked, so nothing is known to have been billed and
			// the reservation goes back, as it does on an error.
			p.settle(est, 0, false)
		}
	}()
	resp, err := p.next.Complete(ctx, req)
	returned = true

	switch {
	case err != nil:
		// Every provider in this package returns a zero-value
		// CompletionResponse on every error path (see internal/llm's
		// provider.go), so nothing was actually billed. Release the
		// reservation rather than leaving a phantom charge that a caller
		// retrying the same budget would never be able to recover from.
		p.settle(est, 0, false)
		return resp, err
	case p.calc != nil:
		if measured, ok := p.measure(ctx, req, resp); ok {
			p.settle(est, measured, true)
			return resp, nil
		}
		if p.allowUnpriced {
			// The caller said a call that cannot be measured is to count as
			// nothing.
			p.settle(est, 0, true)
			return resp, nil
		}
	}
	// An unpriced or nil result leaves the reservation exactly as it was: an
	// unpriced call is not a free one (PR-001), so the pre-call estimate stands
	// in for the unknown real figure rather than being zeroed out.
	return resp, nil
}

// budgetDefaultOutputTokens is the answer a request that names no ceiling is
// estimated at. It is the figure the providers' own estimate uses.
const budgetDefaultOutputTokens = 500

// budgetBytesPerToken is how many bytes of text the estimate counts as a token:
// the figure the library's size guard uses for the same purpose.
const budgetBytesPerToken = 4

// estimateOf is what the call is expected to cost, and whether that is a price.
// The provider's own estimate is a price whatever it is; the request estimate is
// one only when the cost function knows the model.
func (p *budgetProvider) estimateOf(ctx context.Context, req llm.CompletionRequest) (float64, bool) {
	if p.estimate == nil {
		return p.next.EstimateCost(req), true
	}
	return p.estimate(ctx, req, p.next.Name())
}

// budgetRequestEstimate is the estimate of a call from its request: the prompt
// at budgetBytesPerToken, the answer at the ceiling the request names, priced by
// calc, or by the library's price table when calc is nil. It reports false for a
// model calc has no price for, and for a figure that is not an amount. The cost
// function is the caller's, and a panic in it is a call that cannot be priced.
func budgetRequestEstimate(calc func(usage *types.TokenUsage, model, provider string) *types.CostInfo) func(context.Context, llm.CompletionRequest, string) (float64, bool) {
	if calc == nil {
		calc = pricing.CalculateCost
	}
	return func(ctx context.Context, req llm.CompletionRequest, provider string) (cost float64, ok bool) {
		defer func() {
			if thrown := recover(); thrown != nil {
				logPanic(ctx, "A budget cost function panicked", thrown)
				cost, ok = 0, false
			}
		}()
		prompt := (budgetRequestBytes(req) + budgetBytesPerToken - 1) / budgetBytesPerToken
		output := req.MaxTokens
		if output <= 0 {
			output = budgetDefaultOutputTokens
		}
		info := calc(&types.TokenUsage{PromptTokens: prompt, CompletionTokens: output, TotalTokens: prompt + output}, req.Model, provider)
		if info == nil || !info.Priced || !validCost(info.TotalCost) {
			return 0, false
		}
		return info.TotalCost, true
	}
}

// budgetRequestBytes is the size of what a request sends as text: the prompts,
// the turns of a conversation and the calls in them, the tools offered, and the
// text of a typed decision.
func budgetRequestBytes(req llm.CompletionRequest) int {
	size := len(req.SystemPrompt) + len(req.UserPrompt)
	for _, message := range req.Messages {
		size += len(message.Content)
		for _, call := range message.ToolCalls {
			size += len(call.Name) + len(call.Arguments)
		}
	}
	for _, tool := range req.Tools {
		size += len(tool.Name) + len(tool.Description)
		if len(tool.Parameters) > 0 {
			if encoded, err := json.Marshal(tool.Parameters); err == nil {
				size += len(encoded)
			}
		}
	}
	if decision := req.Decision; decision != nil {
		size += len(decision.State) + len(decision.Guidance)
		for _, question := range decision.Questions {
			size += len(question.Ask)
			for _, option := range question.Options {
				size += len(option)
			}
			for _, criterion := range question.Criteria {
				size += len(criterion)
			}
		}
	}
	return size
}

// measure asks the cost function what the call cost. It reports false when
// there is no usable figure: the function declined to price the call, returned
// something that is not an amount, or panicked. It runs outside the lock, so a
// function that is slow or that panics cannot hold the ledger.
func (p *budgetProvider) measure(ctx context.Context, req llm.CompletionRequest, resp llm.CompletionResponse) (cost float64, ok bool) {
	defer func() {
		if thrown := recover(); thrown != nil {
			logPanic(ctx, "A budget cost function panicked", thrown)
			cost, ok = 0, false
		}
	}()
	usage := resp.Usage
	// A provider reports its counts as prompt and completion tokens or as input
	// and output tokens, and the cost function reads the first.
	if usage.PromptTokens == 0 {
		usage.PromptTokens = usage.InputTokens
	}
	if usage.CompletionTokens == 0 {
		usage.CompletionTokens = usage.OutputTokens
	}
	// A response that reports no usage has not told what it cost, and a cost
	// function given zero tokens answers zero for a model it knows: that is a
	// figure for a call nobody measured. An answer served from a cache is the one
	// that really used none.
	if usage == (types.TokenUsage{}) && !resp.Cached {
		return 0, false
	}
	model, provider := resp.Model, resp.Provider
	if model == "" {
		model = req.Model
	}
	if provider == "" {
		provider = p.next.Name()
	}
	info := p.calc(&usage, model, provider)
	if info == nil || !info.Priced || !validCost(info.TotalCost) {
		return 0, false
	}
	return info.TotalCost, true
}

// settle replaces a reservation of est with what the call turned out to cost:
// measured when known, else nothing at all (a failed call, whose reservation is
// released).
func (p *budgetProvider) settle(est, measured float64, known bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.spent -= est
	if known {
		p.spent += measured
	}
	if p.spent < 0 {
		p.spent = 0
	}
}

func (p *budgetProvider) Name() string { return p.next.Name() }

func (p *budgetProvider) EstimateCost(req llm.CompletionRequest) float64 {
	return p.next.EstimateCost(req)
}

func (p *budgetProvider) RetryPolicy() (int, time.Duration) {
	return p.next.RetryPolicy()
}
