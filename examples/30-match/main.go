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

	fmt.Println("=== SemanticMatch Example ===")
	fmt.Println("Finds semantic matches between two sets of data")
	fmt.Println()

	// Business Use Case: Match customer search queries to products
	fmt.Println("--- Business Use Case: E-commerce Search Matching ---")

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

	fmt.Println("INPUT:")
	fmt.Printf("  Query: %q\n", query)
	fmt.Println("  Products: []Product")
	for _, p := range products {
		fmt.Printf("    - ID:%d %q\n", p.ID, p.Name)
	}
	fmt.Println()

	opts := ops.NewMatchOptions().
		WithStrategy("best-fit").
		WithThreshold(0.3).
		WithIntelligence(types.Smart)

	result, err := ops.SemanticMatch([]string{query}, products, opts)
	if err != nil {
		log.Fatalf("Matching failed: %v", err)
	}

	fmt.Println("OUTPUT: MatchResult")
	fmt.Printf("  TotalMatches: %d\n", len(result.Matches))
	for _, match := range result.Matches {
		fmt.Printf("  Match:\n")
		fmt.Printf("    Product:     %q\n", products[match.TargetIndex].Name)
		fmt.Printf("    Score:       %.2f\n", match.Score)
		fmt.Printf("    Explanation: %q\n", match.Explanation)
	}
	fmt.Println()

	// Business Use Case: Match candidates to job requirements
	fmt.Println("--- Business Use Case: Recruiting - Candidate Matching ---")

	type JobReq struct {
		Title  string   `json:"title"`
		Skills []string `json:"skills"`
	}

	type Candidate struct {
		Name   string   `json:"name"`
		Skills []string `json:"skills"`
	}

	job := JobReq{
		Title:  "Senior Go Developer",
		Skills: []string{"Go", "Kubernetes", "gRPC"},
	}

	candidates := []Candidate{
		{Name: "Alice", Skills: []string{"Go", "Docker", "Kubernetes"}},
		{Name: "Bob", Skills: []string{"JavaScript", "React", "Node.js"}},
		{Name: "Diana", Skills: []string{"Go", "gRPC", "Microservices"}},
	}

	fmt.Println("INPUT:")
	fmt.Printf("  Job: %q with skills %v\n", job.Title, job.Skills)
	fmt.Println("  Candidates:")
	for _, c := range candidates {
		fmt.Printf("    - %s: %v\n", c.Name, c.Skills)
	}
	fmt.Println()

	jobOpts := ops.NewMatchOptions().
		WithStrategy("best-fit").
		WithThreshold(0.3).
		WithIntelligence(types.Smart)

	jobResult, err := ops.SemanticMatch([]JobReq{job}, candidates, jobOpts)
	if err != nil {
		log.Fatalf("Job matching failed: %v", err)
	}

	fmt.Println("OUTPUT: MatchResult")
	fmt.Printf("  TotalMatches: %d\n", len(jobResult.Matches))
	for _, match := range jobResult.Matches {
		fmt.Printf("  Match:\n")
		fmt.Printf("    Candidate:   %q\n", candidates[match.TargetIndex].Name)
		fmt.Printf("    Skills:      %v\n", candidates[match.TargetIndex].Skills)
		fmt.Printf("    Score:       %.2f\n", match.Score)
		fmt.Printf("    Explanation: %q\n", match.Explanation)
	}

	fmt.Println("\n=== SemanticMatch Example Complete ===")
}
