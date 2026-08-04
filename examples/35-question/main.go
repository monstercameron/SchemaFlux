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

	fmt.Println("=== Question Example ===")
	fmt.Println("Answers questions about data with typed responses")
	fmt.Println()

	// ==================== USE CASE 1: Sales Report Q&A ====================
	fmt.Println("--- Use Case 1: Sales Report Analysis ---")

	type ProductSale struct {
		Name     string  `json:"name"`
		Revenue  float64 `json:"revenue"`
		Units    int     `json:"units_sold"`
		Category string  `json:"category"`
	}

	type RegionSale struct {
		Region  string  `json:"region"`
		Revenue float64 `json:"revenue"`
		Growth  float64 `json:"growth_percent"`
	}

	type SalesReport struct {
		Quarter     string        `json:"quarter"`
		Year        int           `json:"year"`
		TotalSales  float64       `json:"total_sales"`
		TopProducts []ProductSale `json:"top_products"`
		Regions     []RegionSale  `json:"regions"`
	}

	input1 := SalesReport{
		Quarter:    "Q3",
		Year:       2024,
		TotalSales: 4850000,
		TopProducts: []ProductSale{
			{Name: "Premium Widget", Revenue: 1200000, Units: 15000, Category: "Hardware"},
			{Name: "Smart Gadget", Revenue: 950000, Units: 12000, Category: "Electronics"},
			{Name: "Pro Service Plan", Revenue: 800000, Units: 8000, Category: "Services"},
		},
		Regions: []RegionSale{
			{Region: "North America", Revenue: 2100000, Growth: 15.5},
			{Region: "Europe", Revenue: 1400000, Growth: 8.2},
			{Region: "Asia Pacific", Revenue: 950000, Growth: 22.3},
			{Region: "Latin America", Revenue: 400000, Growth: -3.1},
		},
	}

	fmt.Println("INPUT: SalesReport{")
	fmt.Printf("  Quarter: %q, Year: %d,\n", input1.Quarter, input1.Year)
	fmt.Printf("  TotalSales: $%.0f,\n", input1.TotalSales)
	fmt.Println("  TopProducts: []ProductSale{")
	for _, p := range input1.TopProducts {
		fmt.Printf("    {Name: %q, Revenue: $%.0f, Units: %d},\n", p.Name, p.Revenue, p.Units)
	}
	fmt.Println("  },")
	fmt.Println("  Regions: []RegionSale{")
	for _, r := range input1.Regions {
		fmt.Printf("    {Region: %q, Revenue: $%.0f, Growth: %.1f%%},\n", r.Region, r.Revenue, r.Growth)
	}
	fmt.Println("  },")
	fmt.Println("}")
	fmt.Println()

	// Answer type: simple string
	opts1 := ops.NewQuestionOptions("What is the best performing product by revenue and why?").
		WithIntelligence(types.Smart)

	result1, err := ops.Question[SalesReport, string](input1, opts1)
	if err != nil {
		log.Fatalf("Question failed: %v", err)
	}

	fmt.Println("OUTPUT: QuestionResult[string]{")
	fmt.Printf("  Answer:     %q,\n", truncate(result1.Answer, 80))
	fmt.Printf("  Confidence: %.2f,\n", result1.Confidence)
	fmt.Printf("  Reasoning:  %q,\n", truncate(result1.Reasoning, 60))
	fmt.Println("}")
	fmt.Println()

	// ==================== USE CASE 2: Boolean Question ====================
	fmt.Println("--- Use Case 2: Risk Assessment (Boolean Answer) ---")

	type FinancialMetrics struct {
		DebtToEquity  float64 `json:"debt_to_equity_ratio"`
		CurrentRatio  float64 `json:"current_ratio"`
		QuickRatio    float64 `json:"quick_ratio"`
		InterestCover float64 `json:"interest_coverage"`
		ProfitMargin  float64 `json:"profit_margin_percent"`
		RevenueGrowth float64 `json:"revenue_growth_percent"`
	}

	input2 := FinancialMetrics{
		DebtToEquity:  2.8, // High leverage
		CurrentRatio:  0.9, // Below 1 = liquidity concern
		QuickRatio:    0.6, // Low quick ratio
		InterestCover: 1.2, // Barely covering interest
		ProfitMargin:  3.5,
		RevenueGrowth: -2.0, // Declining revenue
	}

	fmt.Println("INPUT: FinancialMetrics{")
	fmt.Printf("  DebtToEquity:  %.1f,\n", input2.DebtToEquity)
	fmt.Printf("  CurrentRatio:  %.1f,\n", input2.CurrentRatio)
	fmt.Printf("  QuickRatio:    %.1f,\n", input2.QuickRatio)
	fmt.Printf("  InterestCover: %.1f,\n", input2.InterestCover)
	fmt.Printf("  ProfitMargin:  %.1f%%,\n", input2.ProfitMargin)
	fmt.Printf("  RevenueGrowth: %.1f%%,\n", input2.RevenueGrowth)
	fmt.Println("}")
	fmt.Println()

	opts2 := ops.NewQuestionOptions("Is this company at risk of financial distress based on these metrics?").
		WithIntelligence(types.Smart)

	result2, err := ops.Question[FinancialMetrics, bool](input2, opts2)
	if err != nil {
		log.Fatalf("Question failed: %v", err)
	}

	fmt.Println("OUTPUT: QuestionResult[bool]{")
	fmt.Printf("  Answer:     %v,\n", result2.Answer)
	fmt.Printf("  Confidence: %.2f,\n", result2.Confidence)
	if len(result2.Evidence) > 0 {
		fmt.Println("  Evidence: []string{")
		for i, e := range result2.Evidence {
			if i >= 3 {
				fmt.Printf("    ... and %d more,\n", len(result2.Evidence)-3)
				break
			}
			fmt.Printf("    %q,\n", truncate(e, 60))
		}
		fmt.Println("  },")
	}
	fmt.Println("}")
	fmt.Println()

	// ==================== USE CASE 3: Structured Answer ====================
	fmt.Println("--- Use Case 3: Incident Triage (Structured Answer) ---")

	type IncidentReport struct {
		IncidentID   string   `json:"incident_id"`
		Description  string   `json:"description"`
		AffectedSvcs []string `json:"affected_services"`
		ErrorRate    float64  `json:"error_rate_percent"`
		Latency      int      `json:"p99_latency_ms"`
		UserReports  int      `json:"user_reports"`
		StartTime    string   `json:"start_time"`
	}

	type TriageResult struct {
		Severity       string   `json:"severity"`
		Priority       int      `json:"priority"`
		RootCauseGuess string   `json:"likely_root_cause"`
		NextSteps      []string `json:"recommended_next_steps"`
	}

	input3 := IncidentReport{
		IncidentID:   "INC-2024-1847",
		Description:  "Payment processing failures, customers unable to complete checkout",
		AffectedSvcs: []string{"payment-gateway", "checkout-service", "order-service"},
		ErrorRate:    45.2,
		Latency:      8500,
		UserReports:  342,
		StartTime:    "2024-12-05T14:23:00Z",
	}

	fmt.Println("INPUT: IncidentReport{")
	fmt.Printf("  IncidentID:   %q,\n", input3.IncidentID)
	fmt.Printf("  Description:  %q,\n", input3.Description)
	fmt.Printf("  AffectedSvcs: %v,\n", input3.AffectedSvcs)
	fmt.Printf("  ErrorRate:    %.1f%%,\n", input3.ErrorRate)
	fmt.Printf("  Latency:      %dms,\n", input3.Latency)
	fmt.Printf("  UserReports:  %d,\n", input3.UserReports)
	fmt.Println("}")
	fmt.Println()

	opts3 := ops.NewQuestionOptions("Triage this incident: determine severity, priority, likely root cause, and next steps").
		WithSteering("Always respond in English").
		WithIntelligence(types.Smart)

	result3, err := ops.Question[IncidentReport, TriageResult](input3, opts3)
	if err != nil {
		log.Fatalf("Question failed: %v", err)
	}

	fmt.Println("OUTPUT: QuestionResult[TriageResult]{")
	fmt.Println("  Answer: TriageResult{")
	fmt.Printf("    Severity:       %q,\n", result3.Answer.Severity)
	fmt.Printf("    Priority:       %d,\n", result3.Answer.Priority)
	fmt.Printf("    RootCauseGuess: %q,\n", truncate(result3.Answer.RootCauseGuess, 50))
	fmt.Println("    NextSteps: []string{")
	for i, step := range result3.Answer.NextSteps {
		if i >= 3 {
			fmt.Printf("      ... and %d more,\n", len(result3.Answer.NextSteps)-3)
			break
		}
		fmt.Printf("      %q,\n", truncate(step, 50))
	}
	fmt.Println("    },")
	fmt.Println("  },")
	fmt.Printf("  Confidence: %.2f,\n", result3.Confidence)
	fmt.Println("}")

	fmt.Println("\n=== Question Example Complete ===")
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
