package main

import (
	"context"
	"fmt"
	"io"
	"os"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
)

type study struct {
	Title   string `json:"title"`
	Year    int    `json:"year"`
	Finding string `json:"finding"`
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Synthesize example: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	studies := []study{
		{Title: "Remote Work Study A", Year: 2023, Finding: "Remote workers showed 13% higher productivity"},
		{Title: "Hybrid Work Study B", Year: 2023, Finding: "Hybrid workers reported better work-life balance"},
		{Title: "Office Study C", Year: 2024, Finding: "In-office teams showed stronger collaboration"},
	}
	fmt.Fprintln(out, "=== Synthesize Example ===")
	fmt.Fprintln(out, "--- Research synthesis ---")
	sources := make([]any, len(studies))
	for i, item := range studies {
		sources[i] = item
		fmt.Fprintf(out, "Source: %s (%d)\n", item.Title, item.Year)
	}
	result, err := exp.Synthesize[string](ctx, sources, exp.SynthesizeOptions{
		Strategy: "integrate", Options: schemaflux.Options{Tier: schemaflux.TierSmart},
	})
	if err != nil {
		return fmt.Errorf("synthesize studies: %w", err)
	}
	fmt.Fprintf(out, "Synthesis: %s\n", result.Synthesized)
	for _, insight := range result.Insights {
		fmt.Fprintf(out, "Insight [%s]: %s\n", insight.Type, insight.Insight)
	}
	fmt.Fprintln(out, "=== Synthesize Example Complete ===")
	return nil
}
