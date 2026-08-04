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

	fmt.Println("=== Enrich Example ===")
	fmt.Println("Adds derived fields to data using LLM inference")
	fmt.Println()

	// Business Use Case: Enrich product listings with marketing metadata
	fmt.Println("--- Business Use Case: Product Catalog Enrichment ---")

	type Product struct {
		Name        string  `json:"name"`
		Description string  `json:"description"`
		Price       float64 `json:"price"`
	}

	type EnrichedProduct struct {
		Name         string   `json:"name"`
		Description  string   `json:"description"`
		Price        float64  `json:"price"`
		Category     string   `json:"category"`
		Keywords     []string `json:"keywords"`
		TargetMarket string   `json:"target_market"`
	}

	product := Product{
		Name:        "Pro Gaming Keyboard RGB",
		Description: "Mechanical keyboard with Cherry MX switches and RGB lighting",
		Price:       149.99,
	}

	fmt.Println("INPUT: Product struct")
	fmt.Printf("  Name:        %q\n", product.Name)
	fmt.Printf("  Description: %q\n", product.Description)
	fmt.Printf("  Price:       $%.2f\n\n", product.Price)

	opts := ops.NewEnrichOptions().
		WithDeriveFields([]string{"category", "keywords", "target_market"}).
		WithDomain("e-commerce").
		WithIntelligence(types.Smart)

	result, err := ops.Enrich[Product, EnrichedProduct](product, opts)
	if err != nil {
		log.Fatalf("Enrichment failed: %v", err)
	}

	fmt.Println("OUTPUT: EnrichedProduct struct")
	fmt.Printf("  Name:        %q\n", result.Enriched.Name)
	fmt.Printf("  Description: %q\n", result.Enriched.Description)
	fmt.Printf("  Price:       $%.2f\n", result.Enriched.Price)
	fmt.Println("  --- Derived Fields ---")
	fmt.Printf("  Category:     %q\n", result.Enriched.Category)
	fmt.Printf("  Keywords:     %v\n", result.Enriched.Keywords)
	fmt.Printf("  TargetMarket: %q\n", result.Enriched.TargetMarket)
	fmt.Println()

	// Business Use Case: Enrich sales lead with firmographic data
	fmt.Println("--- Business Use Case: Sales Lead Enrichment ---")

	type Lead struct {
		Email   string `json:"email"`
		Company string `json:"company"`
		Title   string `json:"title"`
	}

	type EnrichedLead struct {
		Email      string `json:"email"`
		Company    string `json:"company"`
		Title      string `json:"title"`
		Department string `json:"department"`
		Seniority  string `json:"seniority"`
		Industry   string `json:"industry"`
	}

	lead := Lead{
		Email:   "john.smith@techcorp.io",
		Company: "TechCorp Inc",
		Title:   "VP of Engineering",
	}

	fmt.Println("INPUT: Lead struct")
	fmt.Printf("  Email:   %q\n", lead.Email)
	fmt.Printf("  Company: %q\n", lead.Company)
	fmt.Printf("  Title:   %q\n\n", lead.Title)

	leadOpts := ops.NewEnrichOptions().
		WithDeriveFields([]string{"department", "seniority", "industry"}).
		WithDerivationRules(map[string]string{
			"seniority": "Infer from job title (executive, senior, mid, junior)",
		}).
		WithIntelligence(types.Smart)

	leadResult, err := ops.Enrich[Lead, EnrichedLead](lead, leadOpts)
	if err != nil {
		log.Fatalf("Lead enrichment failed: %v", err)
	}

	fmt.Println("OUTPUT: EnrichedLead struct")
	fmt.Printf("  Email:   %q\n", leadResult.Enriched.Email)
	fmt.Printf("  Company: %q\n", leadResult.Enriched.Company)
	fmt.Printf("  Title:   %q\n", leadResult.Enriched.Title)
	fmt.Println("  --- Derived Fields ---")
	fmt.Printf("  Department: %q\n", leadResult.Enriched.Department)
	fmt.Printf("  Seniority:  %q\n", leadResult.Enriched.Seniority)
	fmt.Printf("  Industry:   %q\n", leadResult.Enriched.Industry)

	fmt.Println("\n=== Enrich Example Complete ===")
}
