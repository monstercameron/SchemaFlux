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

	fmt.Println("=== Critique Example ===")
	fmt.Println("Provides structured feedback with scores and actionable suggestions")
	fmt.Println()

	// Business Use Case: Executive presentation review
	fmt.Println("--- Business Use Case: Executive Presentation Review ---")

	type Presentation struct {
		Title   string `json:"title"`
		Content string `json:"content"`
		Author  string `json:"author"`
	}

	presentation := Presentation{
		Title:   "Q4 Results",
		Content: "We did stuff this quarter. Sales were okay. Next quarter will be better probably.",
		Author:  "Sales Team",
	}

	fmt.Println("INPUT: Presentation struct")
	fmt.Printf("  Title:   %q\n", presentation.Title)
	fmt.Printf("  Content: %q\n", presentation.Content)
	fmt.Printf("  Author:  %q\n\n", presentation.Author)

	opts := ops.NewCritiqueOptions().
		WithCriteria([]string{"professionalism", "specificity", "impact"}).
		WithIncludeSuggestions(true).
		WithIncludeFixes(true).
		WithStyle("constructive").
		WithIntelligence(types.Smart)

	result, err := ops.Critique(presentation, opts)
	if err != nil {
		log.Fatalf("Critique failed: %v", err)
	}

	fmt.Println("OUTPUT: CritiqueResult")
	fmt.Printf("  ModelOverallScore: %.2f/1.00\n", result.ModelOverallScore)
	fmt.Println("  CriteriaScores:")
	for criterion, score := range result.CriteriaScores {
		fmt.Printf("    %s: %.2f\n", criterion, score)
	}
	fmt.Println("  Issues:")
	for i, issue := range result.Issues {
		fmt.Printf("    %d. [%s] %s\n", i+1, issue.Severity, issue.Description)
		if issue.Suggestion != "" {
			fmt.Printf("       Suggestion: %s\n", issue.Suggestion)
		}
	}
	fmt.Printf("  Summary: %q\n", result.Summary)
	fmt.Println()

	// Business Use Case: Code review
	fmt.Println("--- Business Use Case: Automated Code Review ---")

	code := `func processData(data []string) {
    for i := 0; i < len(data); i++ {
        item := data[i]
        if item != "" {
            result := doSomething(item)
            fmt.Println(result)
        }
    }
}`

	fmt.Println("INPUT: string (Go code)")
	fmt.Printf("  %s\n\n", code)

	codeOpts := ops.NewCritiqueOptions().
		WithDomain("software").
		WithRubric(map[string]string{
			"readability":    "Is the code easy to understand?",
			"best_practices": "Does it follow Go idioms?",
		}).
		WithIncludeFixes(true).
		WithIntelligence(types.Smart)

	codeResult, err := ops.Critique(code, codeOpts)
	if err != nil {
		log.Fatalf("Code critique failed: %v", err)
	}

	fmt.Println("OUTPUT: CritiqueResult")
	fmt.Printf("  ModelOverallScore: %.2f\n", codeResult.ModelOverallScore)
	fmt.Println("  Issues:")
	for i, issue := range codeResult.Issues {
		fmt.Printf("    %d. [%s] %s\n", i+1, issue.Severity, issue.Description)
		if issue.Fix != "" {
			fmt.Printf("       Fix: %s\n", issue.Fix)
		}
	}

	fmt.Println("\n=== Critique Example Complete ===")
}
