package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"sort"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// CodeSnippet represents a code submission.
type CodeSnippet struct {
	ID       int
	Author   string
	Language string
	Code     string
}

type scoredSnippet struct {
	snippet CodeSnippet
	result  schemaflux.ScoreResult
}

func main() {
	if err := run(context.Background(), io.Writer(log.Writer())); err != nil {
		log.Printf("Score example failed: %v. Check your provider configuration and credentials, then retry.", err)
	}
}

func run(ctx context.Context, out io.Writer) error {
	snippets := []CodeSnippet{
		{ID: 1, Author: "Alice", Language: "Go", Code: `func calculateTotal(prices []float64) float64 {
    total := 0.0
    for _, price := range prices {
        total += price
    }
    return total
}`},
		{ID: 2, Author: "Bob", Language: "Go", Code: `func calculateTotal(p []float64) float64 {
    var t float64
    for i:=0;i<len(p);i++ {
        t=t+p[i]
    }
    return t
}`},
		{ID: 3, Author: "Carol", Language: "Go", Code: `// CalculateTotal computes the sum of all prices in the slice.
// It returns 0.0 for an empty slice.
// Time complexity: O(n)
func CalculateTotal(prices []float64) float64 {
    if len(prices) == 0 {
        return 0.0
    }

    total := 0.0
    for _, price := range prices {
        if price < 0 {
            continue // Skip negative prices
        }
        total += price
    }
    return total
}`},
	}
	criteria := []string{"readability", "maintainability", "documentation", "error handling", "best practices"}
	if _, err := fmt.Fprintln(out, "Score Example - Code Quality Assessment"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(out, "============================================================"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(out, "Evaluation Criteria:", criteria); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(out, "Scale: 1-10 (10 = excellent)"); err != nil {
		return err
	}

	options := schemaflux.ScoreOptions{Criteria: criteria, ScaleMin: 1, ScaleMax: 10}
	scored := make([]scoredSnippet, 0, len(snippets))
	for _, snippet := range snippets {
		if _, err := fmt.Fprintf(out, "\nEvaluating Submission #%d by %s\n---\n%s\n---\n", snippet.ID, snippet.Author, snippet.Code); err != nil {
			return err
		}
		// Score each snippet separately so the provider can return an assessment for each submission.
		result, err := schemaflux.Score(ctx, snippet.Code, options)
		if err != nil {
			return fmt.Errorf("score submission #%d: %w", snippet.ID, err)
		}
		scored = append(scored, scoredSnippet{snippet: snippet, result: result})

		stars := int(result.Value)
		if stars < 0 {
			stars = 0
		} else if stars > 10 {
			stars = 10
		}
		bar := ""
		for i := 0; i < 10; i++ {
			if i < stars {
				bar += "*"
			} else {
				bar += "-"
			}
		}
		if _, err := fmt.Fprintf(out, "Score: %.1f/10 %s (%.0f%% normalized)\n", result.Value, bar, result.NormalizedValue*100); err != nil {
			return err
		}
		if len(result.Breakdown) > 0 {
			if _, err := fmt.Fprintln(out, "Score Breakdown:"); err != nil {
				return err
			}
			for _, criterion := range criteria {
				if score, ok := result.Breakdown[criterion]; ok {
					if _, err := fmt.Fprintf(out, "   - %s: %.1f\n", criterion, score); err != nil {
						return err
					}
				}
			}
		}
		if len(result.Strengths) > 0 {
			if _, err := fmt.Fprintln(out, "Strengths:"); err != nil {
				return err
			}
			for _, strength := range result.Strengths {
				if _, err := fmt.Fprintf(out, "   + %s\n", strength); err != nil {
					return err
				}
			}
		}
		if len(result.Weaknesses) > 0 {
			if _, err := fmt.Fprintln(out, "Areas for Improvement:"); err != nil {
				return err
			}
			for _, weakness := range result.Weaknesses {
				if _, err := fmt.Fprintf(out, "   - %s\n", weakness); err != nil {
					return err
				}
			}
		}
		if result.Reasoning != "" {
			if _, err := fmt.Fprintf(out, "Reasoning: %s\n", result.Reasoning); err != nil {
				return err
			}
		}
		switch {
		case result.Value >= 8.5:
			_, err = fmt.Fprintln(out, "Excellent! Production-ready code with best practices.")
		case result.Value >= 7:
			_, err = fmt.Fprintln(out, "Good code quality. Minor improvements possible.")
		case result.Value >= 5:
			_, err = fmt.Fprintln(out, "Acceptable but needs improvement.")
		default:
			_, err = fmt.Fprintln(out, "Needs significant refactoring.")
		}
		if err != nil {
			return err
		}
	}

	// Sort by score descending to show the final ranking.
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].result.Value > scored[j].result.Value })
	if _, err := fmt.Fprintln(out, "\nFinal Rankings:\n---"); err != nil {
		return err
	}
	for i, entry := range scored {
		if _, err := fmt.Fprintf(out, "%d. %s - Score: %.1f/10\n", i+1, entry.snippet.Author, entry.result.Value); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(out, "\nSuccess! All code submissions evaluated with detailed breakdowns")
	return err
}
