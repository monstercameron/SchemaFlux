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

	fmt.Println("=== Normalize Example ===")
	fmt.Println("Standardizes data formats using LLM intelligence")
	fmt.Println("NOTE: The library is NOT opinionated - YOU define all normalization rules via WithRules() and WithCanonicalMappings()")
	fmt.Println()

	// Business Use Case: Normalize customer address data for CRM
	fmt.Println("--- Business Use Case: CRM Address Standardization ---")

	type Address struct {
		Street  string `json:"street"`
		City    string `json:"city"`
		State   string `json:"state"`
		Country string `json:"country"`
	}

	address := Address{
		Street:  "123 Main St.",
		City:    "new york city",
		State:   "NY",
		Country: "USA",
	}

	fmt.Println("INPUT: Address struct")
	fmt.Printf("  Street:  %q\n", address.Street)
	fmt.Printf("  City:    %q\n", address.City)
	fmt.Printf("  State:   %q\n", address.State)
	fmt.Printf("  Country: %q\n\n", address.Country)

	opts := ops.NewNormalizeOptions().
		WithRules(map[string]string{
			"street":  "Expand abbreviations (St. -> Street)",
			"city":    "Proper case, canonical name",
			"country": "Full country name",
		}).
		WithIntelligence(types.Smart)

	result, err := ops.Normalize(address, opts)
	if err != nil {
		log.Fatalf("Normalization failed: %v", err)
	}

	fmt.Println("OUTPUT: NormalizeResult[Address]")
	fmt.Printf("  Street:  %q\n", result.Normalized.Street)
	fmt.Printf("  City:    %q\n", result.Normalized.City)
	fmt.Printf("  State:   %q\n", result.Normalized.State)
	fmt.Printf("  Country: %q\n", result.Normalized.Country)

	if len(result.Changes) > 0 {
		fmt.Println("  --- Changes Applied ---")
		for _, change := range result.Changes {
			fmt.Printf("  %s: %q → %q (%s)\n", change.Field, change.Original, change.Normalized, change.Reason)
		}
	}
	fmt.Println()

	// Business Use Case: Normalize product categories for e-commerce
	fmt.Println("--- Business Use Case: Product Category Standardization ---")

	type Product struct {
		Name     string `json:"name"`
		Category string `json:"category"`
	}

	product := Product{
		Name:     "Wireless Mouse",
		Category: "comp accessories / mice",
	}

	fmt.Println("INPUT: Product struct")
	fmt.Printf("  Name:     %q\n", product.Name)
	fmt.Printf("  Category: %q\n\n", product.Category)

	prodOpts := ops.NewNormalizeOptions().
		WithRules(map[string]string{
			"category": "Standard e-commerce taxonomy (Electronics > Computer Accessories > Mice)",
		}).
		WithIntelligence(types.Smart)

	prodResult, err := ops.Normalize(product, prodOpts)
	if err != nil {
		log.Fatalf("Product normalization failed: %v", err)
	}

	fmt.Println("OUTPUT: NormalizeResult[Product]")
	fmt.Printf("  Name:     %q\n", prodResult.Normalized.Name)
	fmt.Printf("  Category: %q\n", prodResult.Normalized.Category)

	if len(prodResult.Changes) > 0 {
		fmt.Println("  --- Changes Applied ---")
		for _, change := range prodResult.Changes {
			fmt.Printf("  %s: %q → %q\n", change.Field, change.Original, change.Normalized)
		}
	}

	fmt.Println("\n=== Normalize Example Complete ===")
}
