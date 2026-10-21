package main

import (
	"context"
	"fmt"
	"io"
	"os"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
)

type quarter struct {
	Name           string  `json:"quarter"`
	Revenue        float64 `json:"revenue"`
	Units          int     `json:"units"`
	MarketingSpend float64 `json:"marketing_spend"`
	Headcount      int     `json:"sales_headcount"`
	ChurnRate      float64 `json:"customer_churn_rate"`
}

type forecastInput struct {
	Historical   []quarter `json:"historical_data"`
	MarketGrowth float64   `json:"industry_growth_rate"`
	Seasonality  float64   `json:"seasonality_index_q1"`
	Assumptions  []string  `json:"planning_assumptions"`
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Predict example: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	input := forecastInput{
		Historical: []quarter{
			{Name: "Q1 2024", Revenue: 1000000, Units: 5000, MarketingSpend: 50000, Headcount: 10, ChurnRate: 0.05},
			{Name: "Q2 2024", Revenue: 1150000, Units: 5500, MarketingSpend: 75000, Headcount: 12, ChurnRate: 0.04},
			{Name: "Q3 2024", Revenue: 1100000, Units: 5200, MarketingSpend: 60000, Headcount: 12, ChurnRate: 0.06},
			{Name: "Q4 2024", Revenue: 1400000, Units: 7000, MarketingSpend: 100000, Headcount: 15, ChurnRate: 0.03},
		},
		MarketGrowth: 0.08, Seasonality: 0.85,
		Assumptions: []string{"Marketing budget: $80,000", "Hire two sales reps", "No new product launch"},
	}
	fmt.Fprintln(out, "=== Predict Example ===")
	fmt.Fprintln(out, "--- Q1 2025 sales forecast ---")
	for _, q := range input.Historical {
		fmt.Fprintf(out, "%s revenue: $%.0f\n", q.Name, q.Revenue)
	}
	result, err := exp.Predict[float64](ctx, input, exp.PredictOptions{
		Horizon: "Q1 2025", Options: schemaflux.Options{Tier: schemaflux.TierSmart},
	})
	if err != nil {
		return fmt.Errorf("predict revenue: %w", err)
	}
	fmt.Fprintf(out, "Predicted revenue: $%.0f\n", result.Prediction)
	fmt.Fprintf(out, "Model confidence claim: %.0f%%\n", result.ModelConfidence*100)
	if result.Interval != nil {
		fmt.Fprintf(out, "Model interval: $%.0f to $%.0f at %.0f%%\n",
			result.Interval.Lower, result.Interval.Upper, result.Interval.ConfidenceLevel*100)
	}
	for _, risk := range result.Risks {
		fmt.Fprintf(out, "Risk: %s\n", risk)
	}
	fmt.Fprintln(out, "=== Predict Example Complete ===")
	return nil
}
