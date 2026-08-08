package mw

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
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

func TestBudgetZeroLimitMeansUnlimited(t *testing.T) {
	fake := &budgetFakeProvider{estimate: 1_000_000, resp: llm.CompletionResponse{Content: "ok"}}
	wrapped := Budget(0, WithBudgetCostFunc(noCostFunc))(fake)

	if _, err := wrapped.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
		t.Fatalf("a zero limit refused a call, want unlimited: %v", err)
	}
}

func TestBudgetNegativeEstimateIsTreatedAsZero(t *testing.T) {
	fake := &budgetFakeProvider{estimate: -5.0, resp: llm.CompletionResponse{Content: "ok"}}
	wrapped := Budget(1.0, WithBudgetCostFunc(noCostFunc))(fake)

	// A buggy provider returning a negative estimate must not let a caller
	// mine free budget out of the ledger.
	if _, err := wrapped.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
		t.Fatalf("Complete returned an error for a negative estimate: %v", err)
	}
	if got := wrapped.(*budgetProvider).Spent(); got != 0 {
		t.Fatalf("Spent() = %v, want 0 (a negative estimate must not credit the ledger)", got)
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
