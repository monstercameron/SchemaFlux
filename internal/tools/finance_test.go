package tools

import (
	"context"
	"testing"
)

func TestTaxToolSales(t *testing.T) {
	result, _ := TaxTool.Execute(context.Background(), map[string]any{
		"type":   "sales",
		"amount": 100.0,
		"rate":   8.5,
	})

	if !result.Success {
		t.Fatalf("Expected success: %s", result.Error)
	}

	data := result.Data.(map[string]any)
	if data["amount"].(float64) != 100 {
		t.Errorf("Expected amount 100, got %v", data["amount"])
	}
	if data["tax_amount"].(float64) != 8.5 {
		t.Errorf("Expected tax_amount 8.5, got %v", data["tax_amount"])
	}
	if data["total"].(float64) != 108.5 {
		t.Errorf("Expected total 108.5, got %v", data["total"])
	}
}

func TestTaxToolVAT(t *testing.T) {
	result, _ := TaxTool.Execute(context.Background(), map[string]any{
		"type":   "vat",
		"amount": 100.0,
		"rate":   20.0,
	})

	if !result.Success {
		t.Fatalf("Expected success: %s", result.Error)
	}

	data := result.Data.(map[string]any)
	if data["tax_amount"].(float64) != 20 {
		t.Errorf("Expected tax_amount 20, got %v", data["tax_amount"])
	}
	if data["total"].(float64) != 120 {
		t.Errorf("Expected total 120, got %v", data["total"])
	}
}

func TestTaxToolIncome(t *testing.T) {
	result, _ := TaxTool.Execute(context.Background(), map[string]any{
		"type":   "income",
		"amount": 50000.0,
		"rate":   25.0,
	})

	if !result.Success {
		t.Fatalf("Expected success: %s", result.Error)
	}

	data := result.Data.(map[string]any)
	if data["amount"].(float64) != 50000 {
		t.Error("Expected amount 50000")
	}
	if data["tax_amount"].(float64) <= 0 {
		t.Error("Expected positive tax amount")
	}
	if data["total"].(float64) <= data["amount"].(float64) {
		t.Error("Expected total greater than amount")
	}
}

func TestTaxToolTip(t *testing.T) {
	result, _ := TaxTool.Execute(context.Background(), map[string]any{
		"type":   "tip",
		"amount": 50.0,
		"rate":   20.0,
	})

	if !result.Success {
		t.Fatalf("Expected success: %s", result.Error)
	}

	data := result.Data.(map[string]any)
	if data["tax_amount"].(float64) != 10 {
		t.Errorf("Expected tax_amount 10, got %v", data["tax_amount"])
	}
	if data["total"].(float64) != 60 {
		t.Errorf("Expected total 60, got %v", data["total"])
	}
}

func TestInterestToolSimple(t *testing.T) {
	result, _ := InterestTool.Execute(context.Background(), map[string]any{
		"type":      "simple",
		"principal": 1000.0,
		"rate":      5.0,
		"time":      2.0,
	})

	if !result.Success {
		t.Fatalf("Expected success: %s", result.Error)
	}

	data := result.Data.(map[string]any)
	// Simple interest: 1000 * 0.05 * 2 = 100
	if data["interest"].(float64) != 100 {
		t.Errorf("Expected interest 100, got %v", data["interest"])
	}
	if data["total"].(float64) != 1100 {
		t.Errorf("Expected total 1100, got %v", data["total"])
	}
}

func TestInterestToolCompound(t *testing.T) {
	result, _ := InterestTool.Execute(context.Background(), map[string]any{
		"type":      "compound",
		"principal": 1000.0,
		"rate":      12.0,
		"time":      1.0,
		"compounds": 12.0,
	})

	if !result.Success {
		t.Fatalf("Expected success: %s", result.Error)
	}

	data := result.Data.(map[string]any)
	// Compound interest should be slightly higher than simple
	// 1000 * (1 + 0.01)^12 ≈ 1126.83
	if data["total"].(float64) < 1126 || data["total"].(float64) > 1127 {
		t.Errorf("Expected total ~1126.83, got %v", data["total"])
	}
}

func TestInterestToolLoan(t *testing.T) {
	result, _ := InterestTool.Execute(context.Background(), map[string]any{
		"type":      "loan",
		"principal": 10000.0,
		"rate":      6.0,
		"time":      2.0,
	})

	if !result.Success {
		t.Fatalf("Expected success: %s", result.Error)
	}

	data := result.Data.(map[string]any)
	payment := data["monthly_payment"].(float64)
	if payment < 400 || payment > 500 {
		t.Errorf("Expected monthly payment ~443, got %v", payment)
	}
}

func TestInterestToolSavings(t *testing.T) {
	result, _ := InterestTool.Execute(context.Background(), map[string]any{
		"type":      "savings",
		"principal": 1000.0,
		"rate":      6.0,
		"time":      5.0,
		"payment":   100.0,
	})

	if !result.Success {
		t.Fatalf("Expected success: %s", result.Error)
	}

	data := result.Data.(map[string]any)
	// Should have future_value greater than deposits
	if data["future_value"].(float64) < data["total_deposits"].(float64) {
		t.Error("Expected future value > total deposits")
	}
}

func TestInterestToolMortgage(t *testing.T) {
	result, _ := InterestTool.Execute(context.Background(), map[string]any{
		"type":      "mortgage",
		"principal": 300000.0,
		"rate":      7.0,
		"time":      30.0,
	})

	if !result.Success {
		t.Fatalf("Expected success: %s", result.Error)
	}

	data := result.Data.(map[string]any)
	payment := data["monthly_payment"].(float64)
	if payment < 1900 || payment > 2100 {
		t.Errorf("Expected monthly payment ~1996, got %v", payment)
	}
}
