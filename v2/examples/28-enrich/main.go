package main

import (
	"context"
	"fmt"
	"io"
	"os"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
)

type product struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Price       float64 `json:"price"`
}

type enrichedProduct struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Price        float64  `json:"price"`
	Category     string   `json:"category"`
	Keywords     []string `json:"keywords"`
	TargetMarket string   `json:"target_market"`
}

type lead struct {
	Email   string `json:"email"`
	Company string `json:"company"`
	Title   string `json:"title"`
}

type enrichedLead struct {
	Email      string `json:"email"`
	Company    string `json:"company"`
	Title      string `json:"title"`
	Department string `json:"department"`
	Seniority  string `json:"seniority"`
	Industry   string `json:"industry"`
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Enrich example: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "=== Enrich Example ===")
	fmt.Fprintln(out, "--- Product catalog enrichment ---")
	p := product{Name: "Pro Gaming Keyboard RGB", Description: "Mechanical keyboard with Cherry MX switches and RGB lighting", Price: 149.99}
	productResult, err := exp.Enrich[enrichedProduct](ctx, p, exp.EnrichOptions{
		Fields: []string{"category", "keywords", "target_market"}, Domain: "e-commerce",
		Options: schemaflux.Options{Tier: schemaflux.TierSmart},
	})
	if err != nil {
		return fmt.Errorf("enrich product: %w", err)
	}
	fmt.Fprintf(out, "Product: %s\nCategory: %s\nKeywords: %v\nTarget market: %s\n",
		productResult.Enriched.Name, productResult.Enriched.Category,
		productResult.Enriched.Keywords, productResult.Enriched.TargetMarket)

	fmt.Fprintln(out, "--- Sales lead enrichment ---")
	l := lead{Email: "john.smith@techcorp.io", Company: "TechCorp Inc", Title: "VP of Engineering"}
	leadResult, err := exp.Enrich[enrichedLead](ctx, l, exp.EnrichOptions{
		Fields:  []string{"department", "seniority", "industry"},
		Rules:   map[string]string{"seniority": "Infer from the job title"},
		Options: schemaflux.Options{Tier: schemaflux.TierSmart},
	})
	if err != nil {
		return fmt.Errorf("enrich lead: %w", err)
	}
	fmt.Fprintf(out, "Lead: %s\nDepartment: %s\nSeniority: %s\nIndustry: %s\n",
		leadResult.Enriched.Title, leadResult.Enriched.Department,
		leadResult.Enriched.Seniority, leadResult.Enriched.Industry)
	fmt.Fprintln(out, "=== Enrich Example Complete ===")
	return nil
}
