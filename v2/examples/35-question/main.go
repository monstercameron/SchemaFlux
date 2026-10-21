package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/monstercameron/schemaflux/v2"
)

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
type FinancialMetrics struct {
	DebtToEquity  float64 `json:"debt_to_equity_ratio"`
	CurrentRatio  float64 `json:"current_ratio"`
	QuickRatio    float64 `json:"quick_ratio"`
	InterestCover float64 `json:"interest_coverage"`
	ProfitMargin  float64 `json:"profit_margin_percent"`
	RevenueGrowth float64 `json:"revenue_growth_percent"`
}
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

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Question example failed: %v\nCheck your provider configuration and API credentials, then try again.\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "=== Question Example ===")
	fmt.Fprint(out, "Answers questions about data with typed responses\n\n")
	fmt.Fprintln(out, "--- Use Case 1: Sales Report Analysis ---")
	input1 := SalesReport{Quarter: "Q3", Year: 2024, TotalSales: 4850000, TopProducts: []ProductSale{{"Premium Widget", 1200000, 15000, "Hardware"}, {"Smart Gadget", 950000, 12000, "Electronics"}, {"Pro Service Plan", 800000, 8000, "Services"}}, Regions: []RegionSale{{"North America", 2100000, 15.5}, {"Europe", 1400000, 8.2}, {"Asia Pacific", 950000, 22.3}, {"Latin America", 400000, -3.1}}}
	fmt.Fprintln(out, "INPUT: SalesReport{")
	fmt.Fprintf(out, "  Quarter: %q, Year: %d,\n  TotalSales: $%.0f,\n", input1.Quarter, input1.Year, input1.TotalSales)
	fmt.Fprintln(out, "  TopProducts: []ProductSale{")
	for _, p := range input1.TopProducts {
		fmt.Fprintf(out, "    {Name: %q, Revenue: $%.0f, Units: %d},\n", p.Name, p.Revenue, p.Units)
	}
	fmt.Fprintln(out, "  },\n  Regions: []RegionSale{")
	for _, r := range input1.Regions {
		fmt.Fprintf(out, "    {Region: %q, Revenue: $%.0f, Growth: %.1f%%},\n", r.Region, r.Revenue, r.Growth)
	}
	fmt.Fprint(out, "  },\n}\n\n")
	// Question's answer type is supplied as a type argument; confidence is the model's claim, not a measurement.
	result1, err := schemaflux.Question[string](ctx, input1, "What is the best performing product by revenue and why?", schemaflux.QuestionOptions{Options: schemaflux.Options{Tier: schemaflux.TierSmart}})
	if err != nil {
		return fmt.Errorf("sales question: %w", err)
	}
	fmt.Fprintln(out, "OUTPUT: QuestionResult[string]{")
	fmt.Fprintf(out, "  Answer:     %q,\n  ModelConfidence: %.2f,\n  Reasoning:  %q,\n}\n\n", truncate(result1.Answer, 80), result1.ModelConfidence, truncate(result1.Reasoning, 60))

	fmt.Fprintln(out, "--- Use Case 2: Risk Assessment (Boolean Answer) ---")
	input2 := FinancialMetrics{2.8, 0.9, 0.6, 1.2, 3.5, -2.0}
	fmt.Fprintln(out, "INPUT: FinancialMetrics{")
	fmt.Fprintf(out, "  DebtToEquity:  %.1f,\n  CurrentRatio:  %.1f,\n  QuickRatio:    %.1f,\n  InterestCover: %.1f,\n  ProfitMargin:  %.1f%%,\n  RevenueGrowth: %.1f%%,\n}\n\n", input2.DebtToEquity, input2.CurrentRatio, input2.QuickRatio, input2.InterestCover, input2.ProfitMargin, input2.RevenueGrowth)
	result2, err := schemaflux.Question[bool](ctx, input2, "Is this company at risk of financial distress based on these metrics?", schemaflux.QuestionOptions{Options: schemaflux.Options{Tier: schemaflux.TierSmart}})
	if err != nil {
		return fmt.Errorf("risk assessment: %w", err)
	}
	fmt.Fprintln(out, "OUTPUT: QuestionResult[bool]{")
	fmt.Fprintf(out, "  Answer:     %v,\n  ModelConfidence: %.2f,\n", result2.Answer, result2.ModelConfidence)
	if len(result2.Evidence) > 0 {
		fmt.Fprintln(out, "  Evidence: []string{")
		for i, e := range result2.Evidence {
			if i >= 3 {
				fmt.Fprintf(out, "    ... and %d more,\n", len(result2.Evidence)-3)
				break
			}
			fmt.Fprintf(out, "    %q,\n", truncate(e, 60))
		}
		fmt.Fprintln(out, "  },")
	}
	fmt.Fprint(out, "}\n\n")

	fmt.Fprintln(out, "--- Use Case 3: Incident Triage (Structured Answer) ---")
	input3 := IncidentReport{"INC-2024-1847", "Payment processing failures, customers unable to complete checkout", []string{"payment-gateway", "checkout-service", "order-service"}, 45.2, 8500, 342, "2024-12-05T14:23:00Z"}
	fmt.Fprintln(out, "INPUT: IncidentReport{")
	fmt.Fprintf(out, "  IncidentID:   %q,\n  Description:  %q,\n  AffectedSvcs: %v,\n  ErrorRate:    %.1f%%,\n  Latency:      %dms,\n  UserReports:  %d,\n}\n\n", input3.IncidentID, input3.Description, input3.AffectedSvcs, input3.ErrorRate, input3.Latency, input3.UserReports)
	// Steering guides the model's wording; the requested response shape is still checked by the library.
	result3, err := schemaflux.Question[TriageResult](ctx, input3, "Triage this incident: determine severity, priority, likely root cause, and next steps", schemaflux.QuestionOptions{Options: schemaflux.Options{Steering: "Always respond in English", Tier: schemaflux.TierSmart}})
	if err != nil {
		return fmt.Errorf("incident triage: %w", err)
	}
	fmt.Fprintln(out, "OUTPUT: QuestionResult[TriageResult]{\n  Answer: TriageResult{")
	fmt.Fprintf(out, "    Severity:       %q,\n    Priority:       %d,\n    RootCauseGuess: %q,\n    NextSteps: []string{\n", result3.Answer.Severity, result3.Answer.Priority, truncate(result3.Answer.RootCauseGuess, 50))
	for i, step := range result3.Answer.NextSteps {
		if i >= 3 {
			fmt.Fprintf(out, "      ... and %d more,\n", len(result3.Answer.NextSteps)-3)
			break
		}
		fmt.Fprintf(out, "      %q,\n", truncate(step, 50))
	}
	fmt.Fprintf(out, "    },\n  },\n  ModelConfidence: %.2f,\n}\n", result3.ModelConfidence)
	fmt.Fprintln(out, "=== Question Example Complete ===")
	return nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
