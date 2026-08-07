package ops

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/pricing"
)

// The budget check has to happen before the provider call, not after the
// invoice. This is the integration half of PR-005: the pricing package can
// answer the question, and CallLLM has to ask it.
func TestBudgetIsCheckedBeforeTheProviderIsCalled(t *testing.T) {
	pricing.ResetCostTracking()
	pricing.ResetBudget()
	t.Cleanup(func() {
		pricing.ResetCostTracking()
		pricing.ResetBudget()
	})

	pricing.SetBudget(0.10, 0, 0, nil)
	pricing.SetBudgetEnforcement(true)

	// Put the daily total past the limit.
	pricing.TrackCost(
		&types.CostInfo{TotalCost: 0.25, Currency: "USD", Priced: true},
		&types.ResultMetadata{RequestID: "req-earlier", EndTime: time.Now()},
	)

	provider := &captureProvider{}
	_, err := CallLLM(context.Background(), provider, "system", "user", types.OpOptions{})

	if err == nil {
		t.Fatal("CallLLM proceeded with an exhausted budget")
	}
	if !errors.Is(err, pricing.ErrBudgetExceeded) {
		t.Errorf("errors.Is(err, ErrBudgetExceeded) = false; err = %v", err)
	}
	if provider.attempts != 0 {
		t.Errorf("the provider was called %d times; the point of a budget is to refuse before spending", provider.attempts)
	}
}

// Under the limit, nothing changes.
func TestCallProceedsWhileUnderBudget(t *testing.T) {
	pricing.ResetCostTracking()
	pricing.ResetBudget()
	t.Cleanup(func() {
		pricing.ResetCostTracking()
		pricing.ResetBudget()
	})

	pricing.SetBudget(10.0, 0, 0, nil)
	pricing.SetBudgetEnforcement(true)
	pricing.TrackCost(
		&types.CostInfo{TotalCost: 0.25, Currency: "USD", Priced: true},
		&types.ResultMetadata{RequestID: "req-earlier", EndTime: time.Now()},
	)

	provider := &captureProvider{}
	if _, err := CallLLM(context.Background(), provider, "system", "user", types.OpOptions{}); err != nil {
		t.Fatalf("CallLLM = %v, want nil while under budget", err)
	}
	if provider.attempts != 1 {
		t.Errorf("provider attempts = %d, want 1", provider.attempts)
	}
}

// Enforcement off is the default, and an over-budget deployment that never
// asked for enforcement keeps working exactly as it did.
func TestAnExhaustedBudgetDoesNotBlockWithoutEnforcement(t *testing.T) {
	pricing.ResetCostTracking()
	pricing.ResetBudget()
	t.Cleanup(func() {
		pricing.ResetCostTracking()
		pricing.ResetBudget()
	})

	pricing.SetBudget(0.10, 0, 0, nil)
	pricing.TrackCost(
		&types.CostInfo{TotalCost: 5.00, Currency: "USD", Priced: true},
		&types.ResultMetadata{RequestID: "req-earlier", EndTime: time.Now()},
	)

	provider := &captureProvider{}
	if _, err := CallLLM(context.Background(), provider, "system", "user", types.OpOptions{}); err != nil {
		t.Fatalf("CallLLM = %v, want nil with enforcement off", err)
	}
	if provider.attempts != 1 {
		t.Errorf("provider attempts = %d, want 1", provider.attempts)
	}
}
