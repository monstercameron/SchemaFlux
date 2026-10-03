package main

import (
	"context"
	"fmt"
	"io"
	"os"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// Product represents a product in the catalog.
type Product struct {
	ID          int      `json:"id"`
	Name        string   `json:"name"`
	Category    string   `json:"category"`
	Price       float64  `json:"price"`
	Features    []string `json:"features"`
	Description string   `json:"description"`
}

func run(ctx context.Context, out io.Writer) error {
	products := []Product{
		{ID: 1, Name: "BudgetBook", Category: "Laptop", Price: 499.99,
			Features:    []string{"8GB RAM", "256GB SSD", "14-inch display", "8hr battery"},
			Description: "Affordable laptop for students and basic computing"},
		{ID: 2, Name: "UltraBook Pro", Category: "Laptop", Price: 1299.99,
			Features:    []string{"16GB RAM", "512GB SSD", "13-inch display", "10hr battery"},
			Description: "Lightweight laptop perfect for professionals on the go"},
		{ID: 3, Name: "PowerStation Desktop", Category: "Desktop", Price: 1899.99,
			Features:    []string{"32GB RAM", "1TB SSD", "RTX 4070", "27-inch monitor"},
			Description: "High-performance desktop for gaming and content creation"},
		{ID: 4, Name: "CreatorPro Workstation", Category: "Desktop", Price: 2499.99,
			Features:    []string{"64GB RAM", "2TB SSD", "RTX 4090", "Dual 4K monitors"},
			Description: "Professional workstation for video editing and 3D rendering"},
	}
	customerQuery := "I'm a college student studying computer science. I need something portable for coding and note-taking. My budget is around $500-600."

	fmt.Fprintln(out, "Choose Example - Product Recommender")
	fmt.Fprintln(out, "==================================================")
	fmt.Fprintln(out, "\nCustomer Query:")
	fmt.Fprintln(out, customerQuery)
	fmt.Fprintln(out, "\nAvailable Products:")
	for _, p := range products {
		fmt.Fprintf(out, "  %d. %s - $%.2f (%s)\n", p.ID, p.Name, p.Price, p.Category)
	}

	// Criteria describe the requirements; steering emphasizes the hard budget and portability constraint.
	chosen, err := schemaflux.Choose(ctx, products, schemaflux.ChooseOptions{
		Criteria: []string{
			"Budget: Maximum $600",
			"Must be portable (laptop)",
			"For coding and note-taking",
		},
		Options: schemaflux.Options{Tier: schemaflux.TierFast, Steering: "Select the product that is under $600 budget AND is a laptop. BudgetBook at $499.99 is the ONLY option within budget."},
	})
	if err != nil {
		return fmt.Errorf("could not choose a product; check the provider configuration and retry: %w", err)
	}

	fmt.Fprintln(out, "\nRecommended Product:")
	fmt.Fprintln(out, "---")
	fmt.Fprintf(out, "Product: %s\n", chosen.Name)
	fmt.Fprintf(out, "Price: $%.2f\n", chosen.Price)
	fmt.Fprintf(out, "Category: %s\n", chosen.Category)
	fmt.Fprintf(out, "Features: %v\n", chosen.Features)
	fmt.Fprintf(out, "\nDescription:\n   %s\n", chosen.Description)
	fmt.Fprintln(out, "\nWhy this choice?")
	fmt.Fprintln(out, "   - Within budget ($500-600)")
	fmt.Fprintln(out, "   - Portable laptop (not desktop)")
	fmt.Fprintln(out, "   - Suitable for coding and note-taking")
	fmt.Fprintln(out, "   - Good value for students")
	fmt.Fprintln(out, "\nSuccess! Intelligently selected best match")
	return nil
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
