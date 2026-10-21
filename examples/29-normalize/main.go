// Example 29: Normalize customer and product data with caller-owned rules.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

type address struct {
	Street  string `json:"street"`
	City    string `json:"city"`
	State   string `json:"state"`
	Country string `json:"country"`
}

type product struct {
	Name     string `json:"name"`
	Category string `json:"category"`
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Normalize example:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "=== Normalize Example ===")
	fmt.Fprintln(out, "Caller rules determine the desired canonical form.")

	addr := address{Street: "123 Main St.", City: "new york city", State: "NY", Country: "USA"}
	cleanAddress, err := schemaflux.Normalize(ctx, addr, schemaflux.NormalizeOptions{
		Rules: map[string]string{
			"street":  "Expand street abbreviations",
			"city":    "Use the canonical city name",
			"country": "Use the full country name",
		},
		CanonicalMappings: map[string]string{"USA": "United States"},
		Options:           schemaflux.Options{Tier: schemaflux.TierSmart},
	})
	if err != nil {
		return fmt.Errorf("normalize address: %w", err)
	}
	fmt.Fprintf(out, "Address: %s, %s, %s, %s\n", cleanAddress.Normalized.Street, cleanAddress.Normalized.City, cleanAddress.Normalized.State, cleanAddress.Normalized.Country)
	fmt.Fprintf(out, "Changed address fields: %v\n", cleanAddress.ChangedFields)

	item := product{Name: "Wireless Mouse", Category: "comp accessories / mice"}
	cleanProduct, err := schemaflux.Normalize(ctx, item, schemaflux.NormalizeOptions{
		Rules: map[string]string{"category": "Use the standard e-commerce taxonomy"},
	})
	if err != nil {
		return fmt.Errorf("normalize product: %w", err)
	}
	fmt.Fprintf(out, "Product: %s (%s)\n", cleanProduct.Normalized.Name, cleanProduct.Normalized.Category)
	fmt.Fprintf(out, "Changed product fields: %v\n", cleanProduct.ChangedFields)
	fmt.Fprintln(out, "=== Normalize Example Complete ===")
	return nil
}
