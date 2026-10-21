package main

import (
	"context"
	"fmt"
	"io"
	"os"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
)

type vendor struct {
	Name         string  `json:"vendor_name"`
	Price        float64 `json:"price_per_unit"`
	DeliveryDays int     `json:"delivery_days"`
	Certified    bool    `json:"has_iso9001"`
}
type cloudQuote struct {
	Provider    string  `json:"provider"`
	MonthlyCost float64 `json:"monthly_cost"`
	Support     string  `json:"support_tier"`
}
type claim struct {
	ID                     string  `json:"claim_id"`
	Amount                 float64 `json:"claim_amount"`
	FiledDaysAfterIncident int     `json:"filed_days_after_incident"`
	Documentation          string  `json:"documentation"`
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Arbitrate example: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "=== Arbitrate Example ===")
	vendors := []vendor{{Name: "Acme Manufacturing", Price: 11.50, DeliveryDays: 10, Certified: true}, {Name: "QuickParts Inc", Price: 10.75, DeliveryDays: 7, Certified: false}}
	v, err := exp.Arbitrate(ctx, vendors, []string{"Price per unit is under $12", "Vendor has ISO 9001 certification"}, exp.ArbitrateOptions{
		Weights: []float64{0.4, 0.6}, Options: schemaflux.Options{Tier: schemaflux.TierSmart},
	})
	if err != nil {
		return fmt.Errorf("arbitrate vendors: %w", err)
	}
	fmt.Fprintf(out, "Vendor to review: %s; weighted model score %.2f\n", v.Winner.Name, v.Scores[v.WinnerIndex])

	quotes := []cloudQuote{{Provider: "Cloud A", MonthlyCost: 45000, Support: "Enterprise"}, {Provider: "Cloud B", MonthlyCost: 42000, Support: "Business"}}
	c, err := exp.Arbitrate(ctx, quotes, []string{"Monthly cost is under $50,000", "Enterprise support is available"}, exp.ArbitrateOptions{
		RequireAllRules: true, Options: schemaflux.Options{Tier: schemaflux.TierSmart},
	})
	if err != nil {
		return fmt.Errorf("arbitrate cloud quotes: %w", err)
	}
	fmt.Fprintf(out, "Cloud candidate for verification: %s; quoted cost $%.0f\n", c.Winner.Provider, c.Winner.MonthlyCost)

	claims := []claim{{ID: "CLM-001", Amount: 15000, FiledDaysAfterIncident: 3, Documentation: "Police report and repair estimate"}, {ID: "CLM-002", Amount: 8500, FiledDaysAfterIncident: 55, Documentation: "Self report"}}
	r, err := exp.Arbitrate(ctx, claims, []string{"Filed within 30 days", "Has third-party documentation"}, exp.ArbitrateOptions{
		RequireAllRules: true, IncludeReasoning: true, Options: schemaflux.Options{Tier: schemaflux.TierSmart},
	})
	if err != nil {
		return fmt.Errorf("arbitrate claim review queue: %w", err)
	}
	// Arbitration is a model-assisted review priority, not a claim approval.
	fmt.Fprintf(out, "Priority claim for human review: %s\n", r.Winner.ID)
	fmt.Fprintln(out, "=== Arbitrate Example Complete ===")
	return nil
}
