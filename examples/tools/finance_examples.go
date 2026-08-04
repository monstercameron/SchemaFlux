package main

import (
	"context"
	"fmt"

	"github.com/monstercameron/schemaflux/internal/tools"
)

func runFinanceExamples(ctx context.Context) {
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("💰 FINANCE TOOLS EXAMPLES")
	fmt.Println(strings.Repeat("=", 60))

	// Example 1: Sales tax calculation
	result, err := tools.Execute(ctx, "tax", map[string]any{
		"type":   "sales",
		"amount": 100.0,
		"rate":   8.25,
	})
	printResult("Tax: Sales tax (8.25% on $100)", result, err)

	// Example 2: VAT calculation
	result, err = tools.Execute(ctx, "tax", map[string]any{
		"type":   "vat",
		"amount": 50.0,
		"rate":   20.0,
	})
	printResult("Tax: VAT (20% on $50)", result, err)

	// Example 3: Tip calculation
	result, err = tools.Execute(ctx, "tax", map[string]any{
		"type":   "tip",
		"amount": 75.50,
		"rate":   18.0,
	})
	printResult("Tax: Tip (18% on $75.50)", result, err)

	// Example 4: Income tax calculation
	result, err = tools.Execute(ctx, "tax", map[string]any{
		"type":   "income",
		"amount": 50000.0,
		"rate":   22.0,
	})
	printResult("Tax: Income tax (22% on $50,000)", result, err)

	// Example 5: Simple interest
	result, err = tools.Execute(ctx, "interest", map[string]any{
		"type":      "simple",
		"principal": 10000.0,
		"rate":      5.0,
		"time":      3.0,
	})
	printResult("Interest: Simple (5% for 3 years)", result, err)

	// Example 6: Compound interest
	result, err = tools.Execute(ctx, "interest", map[string]any{
		"type":      "compound",
		"principal": 10000.0,
		"rate":      5.0,
		"time":      3.0,
		"compounds": 12.0,
	})
	printResult("Interest: Compound monthly (5% for 3 years)", result, err)

	// Example 7: Loan/Mortgage calculation
	result, err = tools.Execute(ctx, "interest", map[string]any{
		"type":      "mortgage",
		"principal": 300000.0,
		"rate":      6.5,
		"time":      30.0,
	})
	printResult("Interest: 30-year mortgage ($300k at 6.5%)", result, err)

	// Example 8: Shorter mortgage comparison
	result, err = tools.Execute(ctx, "interest", map[string]any{
		"type":      "mortgage",
		"principal": 300000.0,
		"rate":      6.5,
		"time":      15.0,
	})
	printResult("Interest: 15-year mortgage ($300k at 6.5%)", result, err)

	// Example 9: Savings with regular deposits
	result, err = tools.Execute(ctx, "interest", map[string]any{
		"type":      "savings",
		"principal": 5000.0,
		"rate":      4.5,
		"time":      10.0,
		"payment":   200.0,
		"compounds": 12.0,
	})
	printResult("Interest: Savings ($5k initial + $200/mo for 10 years)", result, err)

	// Example 10: Currency conversion (stub)
	result, err = tools.Execute(ctx, "currency", map[string]any{
		"amount": 100.0,
		"from":   "USD",
		"to":     "EUR",
	})
	printResult("Currency: USD to EUR (stub)", result, err)

	// Example 11: Stock quote (stub)
	result, err = tools.Execute(ctx, "stock", map[string]any{
		"symbol": "AAPL",
		"action": "quote",
	})
	printResult("Stock: AAPL quote (stub)", result, err)

	// Example 12: Chart configuration (stub)
	result, err = tools.Execute(ctx, "chart", map[string]any{
		"type":   "line",
		"data":   []any{10, 20, 15, 25, 30, 22},
		"title":  "Sales Trend",
		"labels": []any{"Jan", "Feb", "Mar", "Apr", "May", "Jun"},
	})
	printResult("Chart: Line chart config (stub)", result, err)

	// Example 13: Compare investment options
	fmt.Println("\n📊 Investment Comparison: $10,000 for 5 years")
	fmt.Println(strings.Repeat("-", 50))

	rates := []float64{3.0, 5.0, 7.0, 10.0}
	for _, rate := range rates {
		result, _ = tools.Execute(ctx, "interest", map[string]any{
			"type":      "compound",
			"principal": 10000.0,
			"rate":      rate,
			"time":      5.0,
			"compounds": 12.0,
		})
		if result.Success {
			data := result.Data.(map[string]any)
			fmt.Printf("   %.1f%% APY: $%.2f (earned $%.2f)\n",
				rate, data["total"], data["interest"])
		}
	}
}
