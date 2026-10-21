package main

import (
	"context"
	"fmt"
	"io"
	"os"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
)

type epic struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Team        string `json:"team"`
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Decompose example: %v\n", err)
		os.Exit(1)
	}
}

// run shows the two useful shapes: a typed planning tree and a text runbook.
func run(ctx context.Context, out io.Writer) error {
	story := epic{
		Title:       "Multi-Tenant Billing System",
		Description: "Build usage tracking and invoicing for a SaaS platform",
		Team:        "Platform Engineering",
	}
	fmt.Fprintln(out, "=== Decompose Example ===")
	fmt.Fprintln(out, "--- Epic to sprint stories ---")
	fmt.Fprintf(out, "Epic: %s\n", story.Title)
	planned, err := exp.Decompose(ctx, story, exp.DecomposeOptions{
		Strategy: "sequential", TargetParts: 5,
		Options: schemaflux.Options{Tier: schemaflux.TierSmart},
	})
	if err != nil {
		return fmt.Errorf("decompose epic: %w", err)
	}
	for _, part := range planned.Parts {
		fmt.Fprintf(out, "Story: %s — %s\n", part.Name, part.Description)
		if len(part.Dependencies) > 0 {
			fmt.Fprintf(out, "  Blocked by: %v\n", part.Dependencies)
		}
	}

	fmt.Fprintln(out, "--- Incident to runbook steps ---")
	incident := "P1: Checkout failures during peak traffic"
	steps, err := exp.Decompose(ctx, incident, exp.DecomposeOptions{
		Strategy: "sequential", TargetParts: 4,
		Options: schemaflux.Options{Tier: schemaflux.TierSmart},
	})
	if err != nil {
		return fmt.Errorf("decompose incident: %w", err)
	}
	for _, part := range steps.Parts {
		fmt.Fprintf(out, "Step: %s — %s\n", part.Name, part.Description)
	}
	fmt.Fprintln(out, "=== Decompose Example Complete ===")
	return nil
}
