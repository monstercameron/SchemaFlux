package tools

import (
	"context"
	"testing"
)

// executeTax refuses a non-positive amount and a rate outside [0, 100].
func TestExecuteTaxRefusesInvalidInput(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]any
	}{
		{"zero amount", map[string]any{"type": "sales", "amount": 0.0, "rate": 5.0}},
		{"negative amount", map[string]any{"type": "sales", "amount": -10.0, "rate": 5.0}},
		{"negative rate", map[string]any{"type": "sales", "amount": 10.0, "rate": -1.0}},
		{"rate over 100", map[string]any{"type": "sales", "amount": 10.0, "rate": 101.0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := TaxTool.Execute(context.Background(), tc.params)
			if err != nil {
				t.Fatalf("Execute error: %v", err)
			}
			if result.Success {
				t.Error("expected refusal")
			}
		})
	}
}

// An unrecognised tax type still computes the arithmetic -- description is
// the only thing that depends on the type -- rather than being refused.
func TestExecuteTaxWithAnUnknownTypeStillComputes(t *testing.T) {
	result, err := TaxTool.Execute(context.Background(), map[string]any{
		"type": "luxury", "amount": 100.0, "rate": 10.0,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	data := result.Data.(map[string]any)
	if _, hasDescription := data["description"]; hasDescription {
		t.Errorf("an unrecognised tax type should not get a description: %+v", data)
	}
}

// executeInterest refuses a non-positive principal, a negative rate, a
// non-positive time, and an unrecognised calculation type.
func TestExecuteInterestRefusesInvalidInput(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]any
	}{
		{"zero principal", map[string]any{"type": "simple", "principal": 0.0, "rate": 5.0, "time": 1.0}},
		{"negative principal", map[string]any{"type": "simple", "principal": -100.0, "rate": 5.0, "time": 1.0}},
		{"negative rate", map[string]any{"type": "simple", "principal": 100.0, "rate": -1.0, "time": 1.0}},
		{"zero time", map[string]any{"type": "simple", "principal": 100.0, "rate": 5.0, "time": 0.0}},
		{"unknown type", map[string]any{"type": "bogus", "principal": 100.0, "rate": 5.0, "time": 1.0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := InterestTool.Execute(context.Background(), tc.params)
			if err != nil {
				t.Fatalf("Execute error: %v", err)
			}
			if result.Success {
				t.Error("expected refusal")
			}
		})
	}
}

// A zero-rate loan is a distinct arithmetic path (principal divided evenly,
// no interest accrues), not just the general formula with r=0 plugged in.
func TestExecuteInterestZeroRateLoan(t *testing.T) {
	result, err := InterestTool.Execute(context.Background(), map[string]any{
		"type": "loan", "principal": 1200.0, "rate": 0.0, "time": 1.0,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	data := result.Data.(map[string]any)
	if data["total_interest"] != 0.0 {
		t.Errorf("total_interest = %v, want 0 for a zero-rate loan", data["total_interest"])
	}
	if data["monthly_payment"] != 100.0 {
		t.Errorf("monthly_payment = %v, want 100 for $1200 over 12 months at 0%%", data["monthly_payment"])
	}
}

// Savings with no regular deposit is a valid input, not an error, and skips
// the payment-annuity term entirely.
func TestExecuteInterestSavingsWithNoDeposit(t *testing.T) {
	result, err := InterestTool.Execute(context.Background(), map[string]any{
		"type": "savings", "principal": 1000.0, "rate": 5.0, "time": 1.0,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	data := result.Data.(map[string]any)
	if data["monthly_deposit"] != 0.0 {
		t.Errorf("monthly_deposit = %v, want 0", data["monthly_deposit"])
	}
	if data["future_value"].(float64) <= 1000.0 {
		t.Errorf("future_value = %v, want more than the principal after interest", data["future_value"])
	}
}

// A custom compounding frequency is honoured rather than defaulting to 12.
func TestExecuteInterestCompoundWithCustomFrequency(t *testing.T) {
	result, err := InterestTool.Execute(context.Background(), map[string]any{
		"type": "compound", "principal": 1000.0, "rate": 6.0, "time": 1.0, "compounds": 4.0,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	data := result.Data.(map[string]any)
	if data["compounds_yearly"] != 4.0 {
		t.Errorf("compounds_yearly = %v, want 4", data["compounds_yearly"])
	}
}
