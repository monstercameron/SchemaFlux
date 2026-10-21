package main

import (
	"context"
	"fmt"
	"io"
	"os"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
)

type customerProfile struct {
	ID             string   `json:"customer_id"`
	Name           string   `json:"full_name"`
	TotalSpend     float64  `json:"total_spend"`
	SupportTickets int      `json:"support_tickets"`
	Tags           []string `json:"tags"`
}
type researchBundle struct {
	Ticker  string `json:"ticker"`
	Company string `json:"company_name"`
	Sector  string `json:"sector"`
	Thesis  string `json:"investment_thesis"`
	Risks   string `json:"key_risks"`
}
type catalogEntry struct {
	SKU      string  `json:"sku"`
	Name     string  `json:"name"`
	Price    float64 `json:"price"`
	Cost     float64 `json:"cost"`
	Stock    int     `json:"stock_level"`
	Supplier string  `json:"supplier"`
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Compose example: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "=== Compose Example ===")
	crm := map[string]any{"customer_id": "CUST-88421", "full_name": "Sarah Johnson"}
	orders := map[string]any{"customer_id": "CUST-88421", "total_spend": 12450.0}
	support := map[string]any{"customer_id": "CUST-88421", "support_tickets": 3, "tags": []string{"loyal"}}
	profile, err := exp.Assemble[customerProfile](ctx, []any{crm, orders, support}, exp.AssembleOptions{
		MergeStrategy: "smart", Options: schemaflux.Options{Tier: schemaflux.TierSmart},
	})
	if err != nil {
		return fmt.Errorf("compose customer profile: %w", err)
	}
	fmt.Fprintf(out, "Customer 360: %s; spend $%.2f; support tickets %d\n", profile.Composed.Name, profile.Composed.TotalSpend, profile.Composed.SupportTickets)

	market := map[string]any{"ticker": "EXAMPLE", "company_name": "Example Corp", "sector": "Technology"}
	analyst := map[string]any{"ticker": "EXAMPLE", "investment_thesis": "Growing demand", "key_risks": "Competition"}
	research, err := exp.Assemble[researchBundle](ctx, []any{market, analyst}, exp.AssembleOptions{
		MergeStrategy: "smart", Options: schemaflux.Options{Tier: schemaflux.TierSmart},
	})
	if err != nil {
		return fmt.Errorf("compose research bundle: %w", err)
	}
	fmt.Fprintf(out, "Research bundle: %s; thesis %s; risks %s\n", research.Composed.Company, research.Composed.Thesis, research.Composed.Risks)

	pim := map[string]any{"sku": "ELEC-LAPTOP-001", "name": "ProBook 15 Laptop"}
	inventory := map[string]any{"sku": "ELEC-LAPTOP-001", "stock_level": 145}
	procurement := map[string]any{"sku": "ELEC-LAPTOP-001", "price": 899.99, "cost": 650.0, "supplier": "TechDistributor Inc."}
	entry, err := exp.Assemble[catalogEntry](ctx, []any{pim, inventory, procurement}, exp.AssembleOptions{
		MergeStrategy: "smart", Options: schemaflux.Options{Tier: schemaflux.TierSmart},
	})
	if err != nil {
		return fmt.Errorf("compose catalog entry: %w", err)
	}
	if entry.Composed.Price <= 0 {
		return fmt.Errorf("compose catalog entry: price must be positive before computing margin")
	}
	// A margin derived from prices belongs in deterministic Go code.
	margin := (entry.Composed.Price - entry.Composed.Cost) / entry.Composed.Price * 100
	fmt.Fprintf(out, "Catalog entry: %s; stock %d; margin %.1f%%\n", entry.Composed.Name, entry.Composed.Stock, margin)
	fmt.Fprintln(out, "=== Compose Example Complete ===")
	return nil
}
