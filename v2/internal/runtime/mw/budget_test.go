package mw

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// budgetFakeProvider is a minimal Handler whose cost estimate and behavior a
// test controls directly. It exists separately from fakeProvider (mw_test.go)
// because budget tests need a fixed EstimateCost independent of the scripted
// response sequence, which fakeProvider's hardcoded 0.01 does not give.
type budgetFakeProvider struct {
	estimate float64
	resp     llm.CompletionResponse
	err      error
	calls    int
	mu       sync.Mutex
}

func (f *budgetFakeProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.resp, f.err
}
func (f *budgetFakeProvider) Name() string                                   { return "budget-fake" }
func (f *budgetFakeProvider) EstimateCost(req llm.CompletionRequest) float64 { return f.estimate }
func (f *budgetFakeProvider) RetryPolicy() (int, time.Duration)              { return 0, 0 }
func (f *budgetFakeProvider) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// noCostFunc disables actual-cost reconciliation, so a test can reason about
// the ledger purely in terms of EstimateCost without depending on the real
// pricing table (which is package-global state this test package cannot
// register a fixture model into).
func noCostFunc(*types.TokenUsage, string, string) *types.CostInfo { return nil }

func TestBudgetAllowsACallUnderTheLimit(t *testing.T) {
	fake := &budgetFakeProvider{estimate: 1.0, resp: llm.CompletionResponse{Content: "ok"}}
	wrapped := Budget(10.0, WithBudgetCostFunc(noCostFunc))(fake)

	resp, err := wrapped.Complete(context.Background(), llm.CompletionRequest{})
	if err != nil {
		t.Fatalf("Complete returned an error: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("Content = %q, want %q", resp.Content, "ok")
	}
	if fake.Calls() != 1 {
		t.Fatalf("calls = %d, want 1", fake.Calls())
	}
}

func TestBudgetRefusesBeforeCallingTheProviderWhenEstimateExceedsLimit(t *testing.T) {
	fake := &budgetFakeProvider{estimate: 11.0, resp: llm.CompletionResponse{Content: "ok"}}
	wrapped := Budget(10.0, WithBudgetCostFunc(noCostFunc))(fake)

	_, err := wrapped.Complete(context.Background(), llm.CompletionRequest{})
	if err == nil {
		t.Fatal("Complete returned no error, want KindBudgetExceeded")
	}
	if fake.Calls() != 0 {
		t.Fatalf("calls = %d, want 0 -- the provider must never be called once the estimate refuses the request", fake.Calls())
	}
	var opErr *types.OperationError
	if !errors.As(err, &opErr) {
		t.Fatalf("error is not a *types.OperationError: %v", err)
	}
	if opErr.Kind != types.KindBudgetExceeded {
		t.Fatalf("Kind = %v, want KindBudgetExceeded", opErr.Kind)
	}
	if opErr.Op != "mw.Budget" {
		t.Fatalf("Op = %q, want %q", opErr.Op, "mw.Budget")
	}
}

func TestBudgetErrorSatisfiesErrorsIsBudgetExceeded(t *testing.T) {
	fake := &budgetFakeProvider{estimate: 5.0}
	wrapped := Budget(1.0, WithBudgetCostFunc(noCostFunc))(fake)

	_, err := wrapped.Complete(context.Background(), llm.CompletionRequest{})
	if !errors.Is(err, types.ErrBudgetExceeded) {
		t.Fatalf("errors.Is(err, types.ErrBudgetExceeded) = false, want true; err = %v", err)
	}
}

func TestBudgetAllowsExactlyAtTheLimit(t *testing.T) {
	fake := &budgetFakeProvider{estimate: 10.0, resp: llm.CompletionResponse{Content: "ok"}}
	wrapped := Budget(10.0, WithBudgetCostFunc(noCostFunc))(fake)

	if _, err := wrapped.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
		t.Fatalf("Complete returned an error at the exact limit: %v", err)
	}
}

func TestBudgetAccumulatesAcrossCalls(t *testing.T) {
	fake := &budgetFakeProvider{estimate: 4.0, resp: llm.CompletionResponse{Content: "ok"}}
	wrapped := Budget(10.0, WithBudgetCostFunc(noCostFunc))(fake)
	ctx := context.Background()

	// 4 + 4 = 8, fits.
	if _, err := wrapped.Complete(ctx, llm.CompletionRequest{}); err != nil {
		t.Fatalf("call 1: %v", err)
	}
	if _, err := wrapped.Complete(ctx, llm.CompletionRequest{}); err != nil {
		t.Fatalf("call 2: %v", err)
	}
	// 8 + 4 = 12, does not fit.
	if _, err := wrapped.Complete(ctx, llm.CompletionRequest{}); err == nil {
		t.Fatal("call 3: want KindBudgetExceeded, got nil")
	}
	if fake.Calls() != 2 {
		t.Fatalf("calls = %d, want 2 (the third must be refused before calling the provider)", fake.Calls())
	}
}

func TestBudgetReleasesTheReservationOnFailure(t *testing.T) {
	fake := &budgetFakeProvider{estimate: 6.0, err: errors.New("transport failed")}
	wrapped := Budget(10.0, WithBudgetCostFunc(noCostFunc))(fake)
	ctx := context.Background()

	if _, err := wrapped.Complete(ctx, llm.CompletionRequest{}); err == nil {
		t.Fatal("want the provider's own error, got nil")
	}
	// The reservation should have been released: a second 6.0 call still fits
	// under a 10.0 limit, which it would not if the first, failed call had
	// left its reservation on the ledger (6 + 6 = 12 > 10).
	fake.err = nil
	fake.resp = llm.CompletionResponse{Content: "ok"}
	if _, err := wrapped.Complete(ctx, llm.CompletionRequest{}); err != nil {
		t.Fatalf("second call refused, meaning the first call's failed reservation was never released: %v", err)
	}
}

// A limit of zero is no limit, as zero is for every limit in Go: whatever the
// call is estimated to cost, it is allowed, and the provider is reached.
func TestBudgetZeroLimitMeansUnlimited(t *testing.T) {
	cases := []struct {
		name     string
		estimate float64
	}{
		{"a free call", 0},
		{"a tiny estimate", 1e-12},
		{"a cent", 0.01},
		{"a dollar", 1},
		{"a thousand dollars", 1000},
		{"a million dollars", 1_000_000},
		{"an enormous estimate", 1e300},
		{"the largest finite estimate", math.MaxFloat64},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &budgetFakeProvider{estimate: tc.estimate, resp: llm.CompletionResponse{Content: "ok"}}
			wrapped := Budget(0, WithBudgetCostFunc(noCostFunc))(fake)
			// Twice: an estimate of the largest finite size would overflow a
			// ledger that kept adding it up, and still has to be allowed.
			for call := 1; call <= 2; call++ {
				if _, err := wrapped.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
					t.Fatalf("call %d with no limit was refused: %v", call, err)
				}
			}
			if fake.Calls() != 2 {
				t.Errorf("provider calls = %d, want 2", fake.Calls())
			}
		})
	}

	t.Run("negative zero is zero", func(t *testing.T) {
		fake := &budgetFakeProvider{estimate: 5, resp: llm.CompletionResponse{Content: "ok"}}
		wrapped := Budget(math.Copysign(0, -1), WithBudgetCostFunc(noCostFunc))(fake)
		if _, err := wrapped.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
			t.Fatalf("a limit of negative zero was refused: %v", err)
		}
	})

	t.Run("an estimate that is not an amount is still refused", func(t *testing.T) {
		for _, estimate := range []float64{math.NaN(), math.Inf(1), -1} {
			fake := &budgetFakeProvider{estimate: estimate, resp: llm.CompletionResponse{Content: "ok"}}
			wrapped := Budget(0, WithBudgetCostFunc(noCostFunc))(fake)
			_, err := wrapped.Complete(context.Background(), llm.CompletionRequest{})
			if types.KindOf(err) != types.KindBudgetExceeded || fake.Calls() != 0 {
				t.Errorf("estimate %v: err = %v after %d calls, want a refusal before the provider", estimate, err, fake.Calls())
			}
		}
	})
}

// A limit that is negative, NaN or infinite is not one a ceiling can be made
// of. Budget returns a Middleware, which has no error to return, so every call
// is refused, before the provider, whatever it is estimated to cost: a limit
// that was not a number used to make the ceiling allow everything.
func TestBudgetRefusesEveryCallUnderALimitThatIsNotAnAmount(t *testing.T) {
	limits := []struct {
		name  string
		limit float64
	}{
		{"negative", -1},
		{"a tiny negative", -1e-300},
		{"the most negative", -math.MaxFloat64},
		{"NaN", math.NaN()},
		{"positive infinity", math.Inf(1)},
		{"negative infinity", math.Inf(-1)},
	}
	for _, limit := range limits {
		for _, estimate := range []float64{0, 1e-12, 0.5} {
			t.Run(fmt.Sprintf("%s limit, estimate %v", limit.name, estimate), func(t *testing.T) {
				fake := &budgetFakeProvider{estimate: estimate, resp: llm.CompletionResponse{Content: "ok"}}
				wrapped := Budget(limit.limit, WithBudgetCostFunc(noCostFunc))(fake)
				_, err := wrapped.Complete(context.Background(), llm.CompletionRequest{})
				if types.KindOf(err) != types.KindBudgetExceeded {
					t.Fatalf("err = %v, want KindBudgetExceeded", err)
				}
				// A negative limit would be refused by the ledger comparison
				// anyway, with a message about spending that is not what is
				// wrong. The refusal names the limit.
				if !strings.Contains(err.Error(), "limit is not a usable amount") {
					t.Fatalf("err = %q, want it to say the limit is not a usable amount", err)
				}
				if fake.Calls() != 0 {
					t.Fatalf("provider calls = %d, want 0", fake.Calls())
				}
			})
		}
	}
}

// Above zero the limit is a ceiling: a call over it is refused, a call at or
// under it is allowed.
func TestBudgetPositiveLimitIsACeiling(t *testing.T) {
	cases := []struct {
		name     string
		limit    float64
		estimate float64
		refused  bool
	}{
		{"a call over it", 1, 2, true},
		{"a call just over it", 1, 1.0000001, true},
		{"a call at it", 1, 1, false},
		{"a call under it", 1, 0.25, false},
		{"a free call", 1, 0, false},
		{"a tiny limit and a call over it", 1e-9, 1e-6, true},
		{"a huge limit and a huge call under it", 1e300, 1e299, false},
		{"a huge limit and a call over it", 1e300, 2e300, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &budgetFakeProvider{estimate: tc.estimate, resp: llm.CompletionResponse{Content: "ok"}}
			wrapped := Budget(tc.limit, WithBudgetCostFunc(noCostFunc))(fake)
			_, err := wrapped.Complete(context.Background(), llm.CompletionRequest{})
			if tc.refused {
				if types.KindOf(err) != types.KindBudgetExceeded || fake.Calls() != 0 {
					t.Fatalf("err = %v after %d calls, want a refusal before the provider", err, fake.Calls())
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want the call allowed", err)
			}
		})
	}
}

// An estimate that is not an amount is refused, and the ledger it would have
// corrupted stays usable: after a NaN every comparison is false, so the
// ceiling would never refuse again.
func TestBudgetRefusesAnEstimateThatIsNotAnAmount(t *testing.T) {
	for _, est := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -5, -1e-9} {
		t.Run(fmt.Sprint(est), func(t *testing.T) {
			fake := &budgetFakeProvider{estimate: est, resp: llm.CompletionResponse{Content: "ok"}}
			wrapped := Budget(1.0, WithBudgetCostFunc(noCostFunc))(fake)
			_, err := wrapped.Complete(context.Background(), llm.CompletionRequest{})
			if types.KindOf(err) != types.KindBudgetExceeded {
				t.Fatalf("err = %v, want KindBudgetExceeded", err)
			}
			if fake.Calls() != 0 {
				t.Fatalf("provider calls = %d, want 0", fake.Calls())
			}
			if got := wrapped.(*budgetProvider).Spent(); got != 0 {
				t.Fatalf("Spent() = %v, want 0", got)
			}
			// The ledger still refuses what does not fit.
			fake.estimate = 5
			if _, err := wrapped.Complete(context.Background(), llm.CompletionRequest{}); types.KindOf(err) != types.KindBudgetExceeded {
				t.Fatalf("after the refused estimate, err = %v, want the ceiling to still hold", err)
			}
		})
	}
}

// A measured cost that is not an amount is ignored: the call stays at its
// estimate and the ledger is not corrupted.
func TestBudgetIgnoresAMeasuredCostThatIsNotAnAmount(t *testing.T) {
	for _, cost := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -3} {
		t.Run(fmt.Sprint(cost), func(t *testing.T) {
			fake := &budgetFakeProvider{estimate: 4.0, resp: llm.CompletionResponse{Content: "ok"}}
			calc := func(*types.TokenUsage, string, string) *types.CostInfo {
				return &types.CostInfo{TotalCost: cost, Priced: true}
			}
			wrapped := Budget(10.0, WithBudgetCostFunc(calc))(fake)
			if _, err := wrapped.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if got := wrapped.(*budgetProvider).Spent(); got != 4.0 {
				t.Fatalf("Spent() = %v, want the estimate 4.0 kept", got)
			}
			wrapped.Complete(context.Background(), llm.CompletionRequest{})
			if _, err := wrapped.Complete(context.Background(), llm.CompletionRequest{}); types.KindOf(err) != types.KindBudgetExceeded {
				t.Fatalf("third 4.0 call against a 10.0 limit: err = %v, want the ceiling to hold", err)
			}
		})
	}
}

// A panic in the provider must release the reservation: the estimate was left
// on the ledger for good, so a budget could be used up by panics.
func TestBudgetPanicReleasesTheReservationAndStillPanics(t *testing.T) {
	provider := &panickingBudgetProvider{estimate: 6.0, panics: true}
	wrapped := Budget(10.0, WithBudgetCostFunc(noCostFunc))(provider)

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the panic was swallowed")
			}
		}()
		wrapped.Complete(context.Background(), llm.CompletionRequest{})
	}()
	if got := wrapped.(*budgetProvider).Spent(); got != 0 {
		t.Fatalf("Spent() = %v after a panic, want the reservation released", got)
	}
	provider.panics = false
	if _, err := wrapped.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
		t.Fatalf("a call after the panic was refused: %v", err)
	}
}

type panickingBudgetProvider struct {
	estimate float64
	panics   bool
}

func (p *panickingBudgetProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	if p.panics {
		panic("provider bug")
	}
	return llm.CompletionResponse{Content: "ok"}, nil
}
func (p *panickingBudgetProvider) Name() string                                   { return "panicking" }
func (p *panickingBudgetProvider) EstimateCost(req llm.CompletionRequest) float64 { return p.estimate }
func (p *panickingBudgetProvider) RetryPolicy() (int, time.Duration)              { return 0, 0 }

// A cost function that panics is the caller's. The panic is recovered, the call
// keeps its estimate, and the ledger's lock is not left held: the next call must
// not deadlock.
func TestBudgetSurvivesACostFunctionThatPanics(t *testing.T) {
	fake := &budgetFakeProvider{estimate: 4.0, resp: llm.CompletionResponse{Content: "ok"}}
	calc := func(*types.TokenUsage, string, string) *types.CostInfo { panic("cost function bug") }
	wrapped := Budget(10.0, WithBudgetCostFunc(calc))(fake)

	done := make(chan error, 2)
	go func() {
		_, err := wrapped.Complete(context.Background(), llm.CompletionRequest{})
		done <- err
		_, err = wrapped.Complete(context.Background(), llm.CompletionRequest{})
		done <- err
	}()
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("call %d: %v", i+1, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("call %d did not return: the ledger's lock is held", i+1)
		}
	}
	if got := wrapped.(*budgetProvider).Spent(); got != 8.0 {
		t.Fatalf("Spent() = %v, want both calls kept at their 4.0 estimates", got)
	}
}

func TestBudgetReconcilesToActualPricedCost(t *testing.T) {
	fake := &budgetFakeProvider{
		estimate: 5.0,
		resp: llm.CompletionResponse{
			Content: "ok",
			Model:   "priced-model",
			Usage:   types.TokenUsage{PromptTokens: 100, CompletionTokens: 100},
		},
	}
	calc := func(usage *types.TokenUsage, model, provider string) *types.CostInfo {
		return &types.CostInfo{TotalCost: 2.0, Priced: true}
	}
	wrapped := Budget(10.0, WithBudgetCostFunc(calc))(fake)

	if _, err := wrapped.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
		t.Fatalf("Complete returned an error: %v", err)
	}
	// Reserved 5.0 pre-call, then corrected down to the measured 2.0.
	if got := wrapped.(*budgetProvider).Spent(); got != 2.0 {
		t.Fatalf("Spent() = %v, want 2.0 (reconciled to the measured cost, not the estimate)", got)
	}
}

func TestBudgetKeepsTheEstimateWhenCostIsUnpriced(t *testing.T) {
	fake := &budgetFakeProvider{
		estimate: 3.0,
		resp:     llm.CompletionResponse{Content: "ok", Model: "unknown-model"},
	}
	calc := func(usage *types.TokenUsage, model, provider string) *types.CostInfo {
		return &types.CostInfo{Priced: false} // unpriced, per pricing.CalculateCost's own contract
	}
	wrapped := Budget(10.0, WithBudgetCostFunc(calc))(fake)

	if _, err := wrapped.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
		t.Fatalf("Complete returned an error: %v", err)
	}
	// PR-001's rule: unpriced is not free. The 3.0 reservation must survive
	// rather than being zeroed or replaced by a bogus 0.0 "measured" figure.
	if got := wrapped.(*budgetProvider).Spent(); got != 3.0 {
		t.Fatalf("Spent() = %v, want 3.0 (unpriced keeps the pre-call estimate)", got)
	}
}

// TestBudgetConcurrentCallsCannotBothPassAnExceedingCheck is the concurrency
// case the task calls out explicitly: two goroutines, each estimated at just
// over half the limit, must not both be allowed through. If the check and
// the reservation were not atomic, both could observe "under the limit" at
// the same instant and both proceed, together exceeding it.
func TestBudgetConcurrentCallsCannotBothPassAnExceedingCheck(t *testing.T) {
	fake := &budgetFakeProvider{estimate: 6.0, resp: llm.CompletionResponse{Content: "ok"}}
	wrapped := Budget(10.0, WithBudgetCostFunc(noCostFunc))(fake)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = wrapped.Complete(context.Background(), llm.CompletionRequest{})
		}(i)
	}
	wg.Wait()

	succeeded := 0
	for _, err := range errs {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("succeeded = %d of 2 concurrent 6.0 calls against a 10.0 limit, want exactly 1", succeeded)
	}
	if fake.Calls() != 1 {
		t.Fatalf("provider calls = %d, want 1 -- the refused goroutine must never reach the provider", fake.Calls())
	}
}

// TestBudgetThroughChainWithScriptedProvider is the composition case: Budget
// wired the way a caller actually wires it, through mw.Chain, with a second
// middleware (RateLimit, effectively unlimited here) alongside it so the
// seam is exercised and not just the bare middleware function.
func TestBudgetThroughChainWithScriptedProvider(t *testing.T) {
	fake := &fakeProvider{
		responses: []fakeResponse{
			{resp: llm.CompletionResponse{Content: "first"}},
			{resp: llm.CompletionResponse{Content: "second"}},
		},
	}
	// fakeProvider.EstimateCost always returns 0.01 (mw_test.go); a limit of
	// 0.015 admits exactly one call and refuses the second.
	wrapped := Chain(fake, RateLimit(100, time.Second), Budget(0.015, WithBudgetCostFunc(noCostFunc)))

	ctx := context.Background()
	if _, err := wrapped.Complete(ctx, llm.CompletionRequest{}); err != nil {
		t.Fatalf("first call: %v", err)
	}
	_, err := wrapped.Complete(ctx, llm.CompletionRequest{})
	if err == nil {
		t.Fatal("second call: want KindBudgetExceeded through the chain, got nil")
	}
	if types.KindOf(err) != types.KindBudgetExceeded {
		t.Fatalf("Kind = %v, want KindBudgetExceeded", types.KindOf(err))
	}
	if fake.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", fake.calls)
	}
}

func TestValidCost(t *testing.T) {
	cases := []struct {
		x    float64
		want bool
	}{
		{0, true}, {1e-300, true}, {0.000001, true}, {1, true}, {1e300, true}, {math.MaxFloat64, true},
		{-1e-300, false}, {-1, false}, {math.Inf(1), false}, {math.Inf(-1), false}, {math.NaN(), false},
		{math.Copysign(0, -1), true},
	}
	for _, tc := range cases {
		if got := validCost(tc.x); got != tc.want {
			t.Errorf("validCost(%v) = %v, want %v", tc.x, got, tc.want)
		}
	}
}

func TestBudgetSettle(t *testing.T) {
	cases := []struct {
		name     string
		spent    float64
		est      float64
		measured float64
		known    bool
		want     float64
	}{
		{"a failed call releases its estimate", 5, 5, 0, false, 0},
		{"a failed call releases only its own estimate", 8, 3, 0, false, 5},
		{"a measured cost replaces the estimate", 5, 5, 2, true, 2},
		{"a measured cost above the estimate", 5, 5, 7, true, 7},
		{"a measured cost of nothing", 5, 5, 0, true, 0},
		{"other reservations are kept", 9, 4, 1, true, 6},
		{"the ledger never goes below zero", 1, 3, 0, false, 0},
		{"a measured cost below the ledger stays at zero", 1, 3, 0, true, 0},
		{"an unknown cost keeps nothing of the call", 2.5, 2.5, 99, false, 0},
		{"fractions", 0.3, 0.1, 0.25, true, 0.45},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &budgetProvider{spent: tc.spent}
			p.settle(tc.est, tc.measured, tc.known)
			if math.Abs(p.spent-tc.want) > 1e-12 {
				t.Fatalf("spent = %v, want %v", p.spent, tc.want)
			}
		})
	}
}

// Spent reports the ledger's current total, reservations included. It is
// reachable from this package's tests and not through llm.Provider, which has
// no place for it.
func (p *budgetProvider) Spent() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.spent
}

// budgetPriced is a cost function that prices the models of prices at a rate
// per token, prompt and completion alike, and knows no other.
func budgetPriced(prices map[string]float64) func(*types.TokenUsage, string, string) *types.CostInfo {
	return func(usage *types.TokenUsage, model, _ string) *types.CostInfo {
		price, known := prices[model]
		if !known {
			return &types.CostInfo{}
		}
		return &types.CostInfo{TotalCost: float64(usage.PromptTokens+usage.CompletionTokens) * price, Priced: true}
	}
}

// budgetHundredTokens is a request estimated at 100 tokens: 10 for a prompt of
// 40 bytes and 90 for the ceiling it names.
func budgetHundredTokens(model string) llm.CompletionRequest {
	return llm.CompletionRequest{Model: model, UserPrompt: strings.Repeat("x", 40), MaxTokens: 90}
}

func TestBudgetRequestEstimate(t *testing.T) {
	prices := budgetPriced(map[string]float64{"m": 0.5, "free": 0})
	nothing := func(*types.TokenUsage, string, string) *types.CostInfo { return nil }
	notANumber := func(*types.TokenUsage, string, string) *types.CostInfo {
		return &types.CostInfo{TotalCost: math.NaN(), Priced: true}
	}
	belowZero := func(*types.TokenUsage, string, string) *types.CostInfo {
		return &types.CostInfo{TotalCost: -1, Priced: true}
	}
	panics := func(*types.TokenUsage, string, string) *types.CostInfo { panic("boom") }
	cases := []struct {
		name   string
		calc   func(*types.TokenUsage, string, string) *types.CostInfo
		req    llm.CompletionRequest
		want   float64
		priced bool
	}{
		{"a prompt and a ceiling", prices, llm.CompletionRequest{Model: "m", UserPrompt: strings.Repeat("x", 40), MaxTokens: 90}, 50, true},
		{"the prompts are both counted", prices, llm.CompletionRequest{Model: "m", SystemPrompt: strings.Repeat("x", 20), UserPrompt: strings.Repeat("x", 20), MaxTokens: 90}, 50, true},
		{"a partial token is a token", prices, llm.CompletionRequest{Model: "m", UserPrompt: "xxxxx", MaxTokens: 8}, 5, true},
		{"no ceiling is 500 tokens", prices, llm.CompletionRequest{Model: "m"}, 250, true},
		{"a negative ceiling is no ceiling", prices, llm.CompletionRequest{Model: "m", MaxTokens: -1}, 250, true},
		{"the turns of a conversation", prices, llm.CompletionRequest{Model: "m", MaxTokens: 1, Messages: []llm.Message{{Content: strings.Repeat("x", 40)}, {Content: strings.Repeat("x", 40)}}}, 0.5 * 21, true},
		{"the calls in a turn", prices, llm.CompletionRequest{Model: "m", MaxTokens: 1, Messages: []llm.Message{{ToolCalls: []llm.ToolCall{{Name: "abcd", Arguments: []byte(`{"a":1}`)}}}}}, 0.5 * 4, true},
		{"the tools offered", prices, llm.CompletionRequest{Model: "m", MaxTokens: 1, Tools: []llm.Tool{{Name: "abcd", Description: "efgh"}}}, 0.5 * 3, true},
		{"the text of a decision", prices, llm.CompletionRequest{Model: "m", MaxTokens: 1, Decision: &llm.Decision{State: "abcd", Guidance: "efgh", Questions: []llm.DecisionQuestion{{Ask: "ijkl", Options: []string{"mnop"}, Criteria: []string{"qrst"}}}}}, 0.5 * 6, true},
		{"a model that costs nothing is priced", prices, llm.CompletionRequest{Model: "free", MaxTokens: 10}, 0, true},
		{"a model with no price", prices, llm.CompletionRequest{Model: "other", MaxTokens: 10}, 0, false},
		{"no model", prices, llm.CompletionRequest{MaxTokens: 10}, 0, false},
		{"a cost function with no answer", nothing, llm.CompletionRequest{Model: "m"}, 0, false},
		{"a cost that is not a number", notANumber, llm.CompletionRequest{Model: "m"}, 0, false},
		{"a cost that is below zero", belowZero, llm.CompletionRequest{Model: "m"}, 0, false},
		{"a cost function that panics", panics, llm.CompletionRequest{Model: "m"}, 0, false},
		{"the library's price table knows no model of this name", nil, llm.CompletionRequest{Model: "no-such-model-in-the-table", MaxTokens: 10}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, priced := budgetRequestEstimate(tc.calc)(context.Background(), tc.req, "p")
			if got != tc.want || priced != tc.priced {
				t.Errorf("estimate = (%v, %v), want (%v, %v)", got, priced, tc.want, tc.priced)
			}
		})
	}
}

// A budget that estimates from the request refuses the call that would cross the
// limit before it is sent, where one that learns the cost afterwards serves it.
func TestBudgetWithARequestEstimateRefusesTheCallThatWouldCrossTheLimit(t *testing.T) {
	prices := budgetPriced(map[string]float64{"m": 0.01})
	reply := llm.CompletionResponse{Content: "ok", Model: "m", Usage: types.TokenUsage{PromptTokens: 10, CompletionTokens: 90}}
	fake := &budgetFakeProvider{resp: reply}
	budget := Budget(2.5, WithBudgetCostFunc(prices), WithBudgetRequestEstimate())(fake)

	served := 0
	var refusal error
	for range 5 {
		if _, err := budget.Complete(context.Background(), budgetHundredTokens("m")); err != nil {
			refusal = err
			break
		}
		served++
	}
	// A call is estimated and measured at 1.0: two fit under 2.5, and the third
	// would take the ledger to 3.0.
	if served != 2 || fake.Calls() != 2 {
		t.Errorf("served %d, provider asked %d times, want 2 and 2", served, fake.Calls())
	}
	if !errors.Is(refusal, types.ErrBudgetExceeded) {
		t.Errorf("the refusal is %v, want ErrBudgetExceeded", refusal)
	}
	if refusal != nil && strings.Contains(refusal.Error(), "0.000000 would exceed remaining budget 0.000000") {
		t.Errorf("the refusal reports an estimate that does not exist: %v", refusal)
	}
}

func TestBudgetWithARequestEstimateRefusesAModelItCannotPrice(t *testing.T) {
	prices := budgetPriced(map[string]float64{"m": 0.01})
	reply := llm.CompletionResponse{Content: "ok", Model: "unknown", Usage: types.TokenUsage{PromptTokens: 5_000_000, CompletionTokens: 5_000_000}}

	t.Run("it is refused before it is sent", func(t *testing.T) {
		fake := &budgetFakeProvider{resp: reply}
		budget := Budget(1, WithBudgetCostFunc(prices), WithBudgetRequestEstimate())(fake)
		_, err := budget.Complete(context.Background(), budgetHundredTokens("unknown"))
		if !errors.Is(err, types.ErrBudgetExceeded) {
			t.Fatalf("err = %v, want ErrBudgetExceeded", err)
		}
		if fake.Calls() != 0 {
			t.Errorf("the provider was asked %d times, want 0", fake.Calls())
		}
		if strings.Contains(err.Error(), "unknown") {
			t.Errorf("the refusal names the model: %v", err)
		}
	})

	t.Run("with unpriced calls allowed it goes through and counts nothing", func(t *testing.T) {
		fake := &budgetFakeProvider{resp: reply}
		budget := Budget(1, WithBudgetCostFunc(prices), WithBudgetRequestEstimate(), AllowUnpricedCalls())(fake)
		for range 20 {
			if _, err := budget.Complete(context.Background(), budgetHundredTokens("unknown")); err != nil {
				t.Fatalf("err = %v, want none", err)
			}
		}
		if spent := budget.(*budgetProvider).Spent(); spent != 0 {
			t.Errorf("the ledger holds %v, want nothing for calls that cannot be priced", spent)
		}
	})

	t.Run("a limit of zero is no limit and asks nothing of the price", func(t *testing.T) {
		fake := &budgetFakeProvider{resp: reply}
		budget := Budget(0, WithBudgetCostFunc(prices), WithBudgetRequestEstimate())(fake)
		if _, err := budget.Complete(context.Background(), budgetHundredTokens("unknown")); err != nil {
			t.Errorf("err = %v, want none", err)
		}
	})

	t.Run("without a request estimate the provider's own is used as it was", func(t *testing.T) {
		fake := &budgetFakeProvider{resp: reply}
		budget := Budget(1, WithBudgetCostFunc(prices))(fake)
		if _, err := budget.Complete(context.Background(), budgetHundredTokens("unknown")); err != nil {
			t.Errorf("err = %v, want none: an estimate of the provider's own is a price whatever it is", err)
		}
	})
}

// A response that reports no usage has not said what it cost. A cost function
// answers zero for a model it knows when it is given no tokens, which is not a
// measurement.
func TestBudgetCountsAResponseThatReportsNoUsageAtItsReservation(t *testing.T) {
	prices := budgetPriced(map[string]float64{"m": 0.01})
	noUsage := llm.CompletionResponse{Content: "ok", Model: "m"}

	t.Run("it counts at the reservation", func(t *testing.T) {
		fake := &budgetFakeProvider{resp: noUsage}
		budget := Budget(10, WithBudgetCostFunc(prices), WithBudgetRequestEstimate())(fake)
		served := 0
		for range 30 {
			if _, err := budget.Complete(context.Background(), budgetHundredTokens("m")); err != nil {
				break
			}
			served++
		}
		if served != 10 {
			t.Errorf("served %d calls of 1.0 under a limit of 10, want 10: a call with no usage was counted as free", served)
		}
	})

	t.Run("with unpriced calls allowed it counts nothing", func(t *testing.T) {
		fake := &budgetFakeProvider{resp: noUsage}
		budget := Budget(10, WithBudgetCostFunc(prices), WithBudgetRequestEstimate(), AllowUnpricedCalls())(fake)
		for range 30 {
			if _, err := budget.Complete(context.Background(), budgetHundredTokens("m")); err != nil {
				t.Fatalf("err = %v, want none", err)
			}
		}
	})

	t.Run("an answer from a cache used no tokens and counts as nothing", func(t *testing.T) {
		fake := &budgetFakeProvider{resp: llm.CompletionResponse{Content: "ok", Model: "m", Cached: true}}
		budget := Budget(10, WithBudgetCostFunc(prices), WithBudgetRequestEstimate())(fake)
		for range 30 {
			if _, err := budget.Complete(context.Background(), budgetHundredTokens("m")); err != nil {
				t.Fatalf("err = %v, want none", err)
			}
		}
		if spent := budget.(*budgetProvider).Spent(); spent != 0 {
			t.Errorf("the ledger holds %v, want nothing", spent)
		}
	})
}

func TestBudgetMeasure(t *testing.T) {
	prices := budgetPriced(map[string]float64{"m": 0.5})
	cases := []struct {
		name   string
		req    llm.CompletionRequest
		resp   llm.CompletionResponse
		want   float64
		wantOK bool
	}{
		{"prompt and completion tokens", llm.CompletionRequest{}, llm.CompletionResponse{Model: "m", Usage: types.TokenUsage{PromptTokens: 2, CompletionTokens: 2}}, 2, true},
		{"input and output tokens", llm.CompletionRequest{}, llm.CompletionResponse{Model: "m", Usage: types.TokenUsage{InputTokens: 2, OutputTokens: 2}}, 2, true},
		{"prompt tokens beside output tokens", llm.CompletionRequest{}, llm.CompletionResponse{Model: "m", Usage: types.TokenUsage{PromptTokens: 2, OutputTokens: 2}}, 2, true},
		{"no usage at all", llm.CompletionRequest{}, llm.CompletionResponse{Model: "m"}, 0, false},
		{"a total and nothing else", llm.CompletionRequest{}, llm.CompletionResponse{Model: "m", Usage: types.TokenUsage{TotalTokens: 4}}, 0, true},
		{"no usage from a cache", llm.CompletionRequest{}, llm.CompletionResponse{Model: "m", Cached: true}, 0, true},
		{"the request's model when the response names none", llm.CompletionRequest{Model: "m"}, llm.CompletionResponse{Usage: types.TokenUsage{PromptTokens: 2}}, 1, true},
		{"the response's model over the request's", llm.CompletionRequest{Model: "other"}, llm.CompletionResponse{Model: "m", Usage: types.TokenUsage{PromptTokens: 2}}, 1, true},
		{"a model with no price", llm.CompletionRequest{}, llm.CompletionResponse{Model: "other", Usage: types.TokenUsage{PromptTokens: 2}}, 0, false},
		{"no model at all", llm.CompletionRequest{}, llm.CompletionResponse{Usage: types.TokenUsage{PromptTokens: 2}}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &budgetProvider{next: &budgetFakeProvider{}, calc: prices}
			got, ok := p.measure(context.Background(), tc.req, tc.resp)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("measure = (%v, %v), want (%v, %v)", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// Under a thousand callers at once the ledger holds what is in flight, so the
// limit admits as many calls as it has room to reserve, and the spend passes it
// by no more than the calls in flight cost beyond their estimates.
func TestBudgetBoundsTheSpendUnderAThousandConcurrentCallers(t *testing.T) {
	const callers, limit = 1000, 10.0
	prices := budgetPriced(map[string]float64{"m": 0.01})
	cases := []struct {
		name string
		// usage is what each answer reports. The request is estimated at 100
		// tokens, which costs 1.0.
		usage types.TokenUsage
		// span is the most the spend can be, and reached is whether it is reached.
		span    float64
		reached bool
	}{
		{"estimates that are exact", types.TokenUsage{PromptTokens: 10, CompletionTokens: 90}, limit, true},
		{"estimates that are too high", types.TokenUsage{PromptTokens: 10, CompletionTokens: 40}, limit, false},
		// Ten calls are in flight, each reserved at 1.0 and costing 3.0.
		{"estimates that are too low", types.TokenUsage{PromptTokens: 100, CompletionTokens: 200}, limit + 10*(3-1), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			release := make(chan struct{})
			var inProvider, refusedCount, servedCount atomic.Int64
			provider := completeFunc(func(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
				inProvider.Add(1)
				<-release
				return llm.CompletionResponse{Content: "ok", Model: "m", Usage: tc.usage}, nil
			})
			budget := Budget(limit, WithBudgetCostFunc(prices), WithBudgetRequestEstimate())(provider)

			var wg sync.WaitGroup
			for range callers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if _, err := budget.Complete(context.Background(), budgetHundredTokens("m")); err != nil {
						refusedCount.Add(1)
						return
					}
					servedCount.Add(1)
				}()
			}
			// Every caller is refused or inside the provider; let those inside go
			// together, once all of them are.
			deadline := time.Now().Add(10 * time.Second)
			for inProvider.Load()+refusedCount.Load() < callers {
				if time.Now().After(deadline) {
					t.Fatalf("%d in the provider and %d refused of %d callers", inProvider.Load(), refusedCount.Load(), callers)
				}
				time.Sleep(time.Millisecond)
			}
			close(release)
			wg.Wait()

			if got := inProvider.Load(); got > int64(limit) {
				t.Errorf("%d calls were in the provider at once, want at most %d: that is all the ledger had room to reserve", got, int(limit))
			}
			if got := servedCount.Load(); got > int64(limit) {
				t.Errorf("served %d calls, want at most %d", got, int(limit))
			}
			spent := budget.(*budgetProvider).Spent()
			if spent > tc.span+1e-9 {
				t.Errorf("spent %v, want at most %v", spent, tc.span)
			}
			if tc.reached && spent < tc.span-1e-9 {
				t.Errorf("spent %v, want the bound %v reached", spent, tc.span)
			}
		})
	}
}
