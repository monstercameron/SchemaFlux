package main

import (
	"context"
	"fmt"
	"io"
	"os"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Matching failed: %v\nCheck your provider configuration and try again.\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "=== SemanticMatch Example ===")
	fmt.Fprintln(out, "Finds semantic matches between two sets of data")
	fmt.Fprintln(out)

	// Business Use Case: Match customer search queries to products
	fmt.Fprintln(out, "--- Business Use Case: E-commerce Search Matching ---")
	type Product struct {
		ID          int    `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	products := []Product{
		{ID: 1, Name: "Wireless Bluetooth Headphones", Description: "Over-ear noise cancelling"},
		{ID: 2, Name: "USB-C Charging Cable", Description: "Fast charging for phones"},
		{ID: 3, Name: "Mechanical Gaming Keyboard", Description: "RGB with Cherry MX switches"},
	}
	query := "bluetooth audio device"
	fmt.Fprintln(out, "INPUT:")
	fmt.Fprintf(out, "  Query: %q\n", query)
	fmt.Fprintln(out, "  Products: []Product")
	for _, p := range products {
		fmt.Fprintf(out, "    - ID:%d %q\n", p.ID, p.Name)
	}
	fmt.Fprintln(out)

	// SemanticMatch accepts a single source in a one-element slice. Threshold
	// values below zero disable the floor; 0.3 here means a 0.3 minimum score.
	result, err := schemaflux.SemanticMatch(ctx, []string{query}, products, schemaflux.SemanticMatchOptions{
		Strategy:  "best-fit",
		Threshold: 0.3,
		Options:   schemaflux.Options{Tier: schemaflux.TierSmart},
	})
	if err != nil {
		return fmt.Errorf("match products: %w", err)
	}
	fmt.Fprintln(out, "OUTPUT: MatchResult")
	fmt.Fprintf(out, "  TotalMatches: %d\n", len(result.Matches))
	for _, match := range result.Matches {
		fmt.Fprintln(out, "  Match:")
		fmt.Fprintf(out, "    Product:     %q\n", products[match.TargetIndex].Name)
		fmt.Fprintf(out, "    Score:       %.2f\n", match.Score)
		fmt.Fprintf(out, "    Explanation: %q\n", match.Explanation)
	}
	fmt.Fprintln(out)

	// Business Use Case: Match candidates to job requirements
	fmt.Fprintln(out, "--- Business Use Case: Recruiting - Candidate Matching ---")
	type JobReq struct {
		Title  string   `json:"title"`
		Skills []string `json:"skills"`
	}
	type Candidate struct {
		Name   string   `json:"name"`
		Skills []string `json:"skills"`
	}
	job := JobReq{Title: "Senior Go Developer", Skills: []string{"Go", "Kubernetes", "gRPC"}}
	candidates := []Candidate{
		{Name: "Alice", Skills: []string{"Go", "Docker", "Kubernetes"}},
		{Name: "Bob", Skills: []string{"JavaScript", "React", "Node.js"}},
		{Name: "Diana", Skills: []string{"Go", "gRPC", "Microservices"}},
	}
	fmt.Fprintln(out, "INPUT:")
	fmt.Fprintf(out, "  Job: %q with skills %v\n", job.Title, job.Skills)
	fmt.Fprintln(out, "  Candidates:")
	for _, c := range candidates {
		fmt.Fprintf(out, "    - %s: %v\n", c.Name, c.Skills)
	}
	fmt.Fprintln(out)

	jobResult, err := schemaflux.SemanticMatch(ctx, []JobReq{job}, candidates, schemaflux.SemanticMatchOptions{
		Strategy:  "best-fit",
		Threshold: 0.3,
		Options:   schemaflux.Options{Tier: schemaflux.TierSmart},
	})
	if err != nil {
		return fmt.Errorf("match candidates: %w", err)
	}
	fmt.Fprintln(out, "OUTPUT: MatchResult")
	fmt.Fprintf(out, "  TotalMatches: %d\n", len(jobResult.Matches))
	for _, match := range jobResult.Matches {
		fmt.Fprintln(out, "  Match:")
		fmt.Fprintf(out, "    Candidate:   %q\n", candidates[match.TargetIndex].Name)
		fmt.Fprintf(out, "    Skills:      %v\n", candidates[match.TargetIndex].Skills)
		fmt.Fprintf(out, "    Score:       %.2f\n", match.Score)
		fmt.Fprintf(out, "    Explanation: %q\n", match.Explanation)
	}
	fmt.Fprintln(out, "\n=== SemanticMatch Example Complete ===")
	return nil
}
