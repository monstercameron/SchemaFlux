package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/joho/godotenv"
	"github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
)

func loadEnv() {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, ".env")); err == nil {
			godotenv.Load(filepath.Join(dir, ".env"))
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
}

func main() {
	loadEnv()
	schemaflux.InitWithEnv()

	fmt.Println("=== Predict Example ===")
	fmt.Println("Forecasts future values based on historical data patterns")
	fmt.Println()

	// Business Use Case: Sales forecasting with business context
	fmt.Println("--- Business Use Case: Q1 2025 Sales Forecast ---")

	type QuarterlyData struct {
		Quarter        string  `json:"quarter"`
		Revenue        float64 `json:"revenue"`
		Units          int     `json:"units"`
		MarketingSpend float64 `json:"marketing_spend"`
		Headcount      int     `json:"sales_headcount"`
		NewProducts    int     `json:"new_products_launched"`
		ChurnRate      float64 `json:"customer_churn_rate"`
	}

	type MarketContext struct {
		IndustryGrowthRate float64 `json:"industry_growth_rate"`
		CompetitorCount    int     `json:"competitor_count"`
		SeasonalityIndex   float64 `json:"seasonality_index_q1"`
		EconomicOutlook    string  `json:"economic_outlook"`
	}

	type ForecastInput struct {
		Historical    []QuarterlyData `json:"historical_data"`
		MarketContext MarketContext   `json:"market_context"`
		Assumptions   []string        `json:"planning_assumptions"`
	}

	input := ForecastInput{
		Historical: []QuarterlyData{
			{Quarter: "Q1 2024", Revenue: 1000000, Units: 5000, MarketingSpend: 50000, Headcount: 10, NewProducts: 0, ChurnRate: 0.05},
			{Quarter: "Q2 2024", Revenue: 1150000, Units: 5500, MarketingSpend: 75000, Headcount: 12, NewProducts: 1, ChurnRate: 0.04},
			{Quarter: "Q3 2024", Revenue: 1100000, Units: 5200, MarketingSpend: 60000, Headcount: 12, NewProducts: 0, ChurnRate: 0.06},
			{Quarter: "Q4 2024", Revenue: 1400000, Units: 7000, MarketingSpend: 100000, Headcount: 15, NewProducts: 2, ChurnRate: 0.03},
		},
		MarketContext: MarketContext{
			IndustryGrowthRate: 0.08,
			CompetitorCount:    5,
			SeasonalityIndex:   0.85, // Q1 typically 15% below average
			EconomicOutlook:    "stable with mild recession risk",
		},
		Assumptions: []string{
			"Marketing budget Q1 2025: $80,000",
			"Hiring 2 additional sales reps",
			"No new product launches planned",
		},
	}

	fmt.Println("INPUT: ForecastInput struct")
	fmt.Println("  Historical: []QuarterlyData")
	for _, q := range input.Historical {
		fmt.Printf("    - %s: Rev=$%.0fK, Marketing=$%.0fK, Headcount=%d, Churn=%.0f%%\n",
			q.Quarter, q.Revenue/1000, q.MarketingSpend/1000, q.Headcount, q.ChurnRate*100)
	}
	fmt.Println("  MarketContext:")
	fmt.Printf("    IndustryGrowth: %.0f%%, Competitors: %d, SeasonalityIndex: %.2f\n",
		input.MarketContext.IndustryGrowthRate*100, input.MarketContext.CompetitorCount, input.MarketContext.SeasonalityIndex)
	fmt.Printf("    EconomicOutlook: %q\n", input.MarketContext.EconomicOutlook)
	fmt.Println("  Assumptions:")
	for _, a := range input.Assumptions {
		fmt.Printf("    - %s\n", a)
	}
	fmt.Println()

	opts := ops.NewPredictOptions().
		WithHorizon("Q1 2025").
		WithIntelligence(types.Smart)

	// Use float64 since prediction is a single revenue value
	result, err := ops.Predict[float64](input, opts)
	if err != nil {
		log.Fatalf("Prediction failed: %v", err)
	}

	fmt.Println("OUTPUT: PredictResult[float64]")
	fmt.Printf("  Prediction (Q1 2025 Revenue): $%.0f\n", result.Prediction)
	fmt.Printf("  Confidence:                   %.0f%%\n", result.Confidence*100)

	if result.Interval != nil {
		fmt.Println("  ConfidenceInterval:")
		fmt.Printf("    Lower:           $%.0f\n", result.Interval.Lower)
		fmt.Printf("    Upper:           $%.0f\n", result.Interval.Upper)
	}

	if result.Reasoning != "" {
		// Truncate reasoning for display
		reasoning := result.Reasoning
		if len(reasoning) > 300 {
			reasoning = reasoning[:300] + "..."
		}
		fmt.Printf("  Reasoning: %s\n", reasoning)
	}

	if len(result.Assumptions) > 0 {
		fmt.Println("  Assumptions:")
		for i, a := range result.Assumptions {
			if i >= 3 {
				fmt.Printf("    ... and %d more\n", len(result.Assumptions)-3)
				break
			}
			fmt.Printf("    - %s\n", a)
		}
	}

	if len(result.Risks) > 0 {
		fmt.Println("  Risks:")
		for i, r := range result.Risks {
			if i >= 3 {
				fmt.Printf("    ... and %d more\n", len(result.Risks)-3)
				break
			}
			fmt.Printf("    - %s\n", r)
		}
	}

	fmt.Println("\n=== Predict Example Complete ===")
}
