// Example 27: Decompose Operation
// Breaks down complex items into smaller, manageable parts using LLM

package main

import (
	"fmt"
	"github.com/monstercameron/schemaflux/examples/internal/exampleutil"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
	"log"
)

func main() {

	// Initialize SchemaFlux
	if err := exampleutil.Bootstrap(); err != nil {
		log.Fatalf("Failed to initialize SchemaFlux: %v", err)
	}

	fmt.Println("=== Decompose Example ===")
	fmt.Println("Breaks down complex items into smaller parts")
	fmt.Println()

	// Business Use Case: Sprint Planning - Break Epic into User Stories
	fmt.Println("--- Business Use Case: Epic ? Sprint Backlog ---")

	type Epic struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Team        string `json:"team"`
	}

	epic := Epic{
		Title:       "Multi-Tenant Billing System",
		Description: "Build billing for SaaS platform with usage tracking and invoicing",
		Team:        "Platform Engineering",
	}

	fmt.Println("INPUT: Epic struct")
	fmt.Printf("  Title:       %q\n", epic.Title)
	fmt.Printf("  Description: %q\n", epic.Description)
	fmt.Printf("  Team:        %q\n\n", epic.Team)

	opts := ops.NewDecomposeOptions().
		WithStrategy("sequential").
		WithIncludeDependencies(true).
		WithTargetParts(5).
		WithIntelligence(types.Smart)

	result, err := ops.Decompose(epic, opts)
	if err != nil {
		log.Fatalf("Decomposition failed: %v", err)
	}

	fmt.Println("OUTPUT: DecomposeResult[Epic] ? Sprint-Ready User Stories")
	fmt.Printf("  Total Stories: %d\n", len(result.Parts))
	fmt.Println("  +-------------------------------------------------------------")
	for i, part := range result.Parts {
		fmt.Printf("  Â¦ Story %d: %s\n", i+1, part.Name)
		fmt.Printf("  Â¦   Description: %s\n", part.Description)
		if len(part.Dependencies) > 0 {
			fmt.Printf("  Â¦   Blocked By: %v\n", part.Dependencies)
		}
		if i < len(result.Parts)-1 {
			fmt.Println("  Â¦")
		}
	}
	fmt.Println("  +-------------------------------------------------------------")

	fmt.Println("\n--- Business Use Case: Incident ? Runbook Steps ---")

	incident := "P1: Checkout failures during peak traffic"

	fmt.Println("INPUT: string")
	fmt.Printf("  Incident: %q\n\n", incident)

	incidentOpts := ops.NewDecomposeOptions().
		WithStrategy("sequential").
		WithTargetParts(4).
		WithIntelligence(types.Smart)

	incidentResult, err := ops.Decompose(incident, incidentOpts)
	if err != nil {
		log.Fatalf("Incident decomposition failed: %v", err)
	}

	fmt.Println("OUTPUT: DecomposeResult[string] ? Troubleshooting Runbook")
	fmt.Printf("  Total Steps: %d\n", len(incidentResult.Parts))
	fmt.Println("  +-------------------------------------------------------------")
	for i, part := range incidentResult.Parts {
		fmt.Printf("  Â¦ Step %d: %s\n", i+1, part.Name)
		fmt.Printf("  Â¦   Action: %s\n", part.Description)
		if i < len(incidentResult.Parts)-1 {
			fmt.Println("  Â¦")
		}
	}
	fmt.Println("  +-------------------------------------------------------------")

	fmt.Println("\n=== Decompose Example Complete ===")
}
