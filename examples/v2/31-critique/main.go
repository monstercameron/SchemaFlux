package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		log.Printf("critique example failed: %v", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	var b strings.Builder
	fmt.Fprintln(&b, "=== Critique Example ===")
	fmt.Fprintln(&b, "Provides structured feedback with scores and actionable suggestions")
	fmt.Fprintln(&b)

	// Business Use Case: Executive presentation review
	fmt.Fprintln(&b, "--- Business Use Case: Executive Presentation Review ---")

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

	fmt.Fprintln(&b, "INPUT: Presentation struct")
	fmt.Fprintf(&b, "  Title:   %q\n", presentation.Title)
	fmt.Fprintf(&b, "  Content: %q\n", presentation.Content)
	fmt.Fprintf(&b, "  Author:  %q\n\n", presentation.Author)

	// Critique reports the model's opinion; the library works the verdict out from its issues.
	result, err := schemaflux.Critique(ctx, presentation, []string{"professionalism", "specificity", "impact"}, schemaflux.CritiqueOptions{
		Options: schemaflux.Options{Tier: schemaflux.TierSmart},
		Style:   "constructive",
	})
	if err != nil {
		return fmt.Errorf("review the presentation failed; check the request and provider configuration: %w", err)
	}

	fmt.Fprintln(&b, "OUTPUT: CritiqueResult")
	fmt.Fprintf(&b, "  Verdict: %s\n", result.Verdict)
	fmt.Fprintln(&b, "  Issues:")
	for i, issue := range result.Issues {
		fmt.Fprintf(&b, "    %d. [%s/%s] %s\n", i+1, issue.Category, issue.Severity, issue.Message)
		if issue.Suggestion != "" {
			fmt.Fprintf(&b, "       Suggestion: %s\n", issue.Suggestion)
		}
	}
	fmt.Fprintf(&b, "  Summary: %q\n", result.Summary)
	// The model's scores are claims and stay separate from the verdict.
	fmt.Fprintf(&b, "  Model-claimed confidence: %.2f\n", result.ModelConfidence)
	if len(result.CriteriaScores) > 0 {
		fmt.Fprintf(&b, "  Model-claimed criteria scores: %v\n", result.CriteriaScores)
	}
	fmt.Fprintln(&b)

	// Business Use Case: Code review
	fmt.Fprintln(&b, "--- Business Use Case: Automated Code Review ---")

	code := `func processData(data []string) {
    for i := 0; i < len(data); i++ {
        item := data[i]
        if item != "" {
            result := doSomething(item)
            fmt.Println(result)
        }
    }
}`

	fmt.Fprintln(&b, "INPUT: string (Go code)")
	fmt.Fprintf(&b, "  %s\n\n", code)

	codeResult, err := schemaflux.Critique(ctx, code, nil, schemaflux.CritiqueOptions{
		Options: schemaflux.Options{Tier: schemaflux.TierSmart},
		Domain:  "software",
		Rubric: map[string]string{
			"readability":    "Is the code easy to understand?",
			"best_practices": "Does it follow Go idioms?",
		},
	})
	if err != nil {
		return fmt.Errorf("review the code failed; check the request and provider configuration: %w", err)
	}

	fmt.Fprintln(&b, "OUTPUT: CritiqueResult")
	fmt.Fprintf(&b, "  Verdict: %s\n", codeResult.Verdict)
	fmt.Fprintln(&b, "  Issues:")
	for i, issue := range codeResult.Issues {
		fmt.Fprintf(&b, "    %d. [%s/%s] %s\n", i+1, issue.Category, issue.Severity, issue.Message)
		if issue.Suggestion != "" {
			fmt.Fprintf(&b, "       Suggestion: %s\n", issue.Suggestion)
		}
	}

	fmt.Fprintln(&b, "\n=== Critique Example Complete ===")
	_, err = io.WriteString(out, b.String())
	if err != nil {
		return fmt.Errorf("write example output: %w", err)
	}
	return nil
}
