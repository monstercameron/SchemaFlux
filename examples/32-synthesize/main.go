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

	fmt.Println("=== Synthesize Example ===")
	fmt.Println("Combines multiple sources into a unified output with conflict resolution")
	fmt.Println()

	// Business Use Case: Research synthesis from multiple studies
	fmt.Println("--- Business Use Case: Research Synthesis ---")

	type Study struct {
		Title   string `json:"title"`
		Year    int    `json:"year"`
		Finding string `json:"finding"`
	}

	// Simple string output - let the LLM write a synthesis paragraph
	studies := []Study{
		{Title: "Remote Work Study A", Year: 2023, Finding: "Remote workers showed 13% higher productivity"},
		{Title: "Hybrid Work Study B", Year: 2023, Finding: "Hybrid workers reported better work-life balance"},
		{Title: "Office Study C", Year: 2024, Finding: "In-office teams showed stronger collaboration"},
	}

	fmt.Println("INPUT: []Study")
	for _, s := range studies {
		fmt.Printf("  - %s (%d): %q\n", s.Title, s.Year, s.Finding)
	}
	fmt.Println()

	// Convert to []any for Synthesize
	studySources := make([]any, len(studies))
	for i, s := range studies {
		studySources[i] = s
	}

	opts := ops.NewSynthesizeOptions().
		WithStrategy("integrate").
		WithGenerateInsights(true).
		WithIntelligence(types.Smart)

	result, err := ops.Synthesize[string](studySources, opts)
	if err != nil {
		log.Fatalf("Synthesis failed: %v", err)
	}

	fmt.Println("OUTPUT: SynthesizeResult[string]")
	fmt.Printf("  Synthesized: %q\n", result.Synthesized)

	if len(result.Insights) > 0 {
		fmt.Println("  Insights:")
		for _, insight := range result.Insights {
			fmt.Printf("    - [%s] %s\n", insight.Type, insight.Insight)
		}
	}

	fmt.Println("\n=== Synthesize Example Complete ===")
}
