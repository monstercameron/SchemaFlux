// Example: 09-score
//
// Operation: Score[T] - Rate items on a numeric scale with breakdown
//
// Input: 3 Go code snippets (same function, different quality)
//   - Alice: Clean, readable code with range loop
//   - Bob: Terse code, poor naming, index-based loop
//   - Carol: Well-documented, handles edge cases, best practices
//
// Criteria: readability, maintainability, documentation, error handling, best practices
// Scale: 1-10 (10 = excellent)
//
// Expected Output:
//   - Carol: ~8-9/10 (best - documented, handles edge cases)
//   - Alice: ~6-7/10 (good - clean but no docs/error handling)
//   - Bob: ~4-5/10 (poor - cryptic names, no docs)
//
// Provider: Cerebras (gpt-oss-120b via Fast intelligence)
// Expected Duration: ~1-2s per snippet
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	schemaflux "github.com/monstercameron/schemaflux"
)

// CodeSnippet represents a code submission
type CodeSnippet struct {
	ID       int
	Author   string
	Language string
	Code     string
}

// loadEnv loads environment variables from a .env file
func loadEnv(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			os.Setenv(strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]))
		}
	}
	return scanner.Err()
}

func main() {
	// Load .env file from project root
	if err := loadEnv("../../.env"); err != nil {
		fmt.Printf("Warning: Could not load .env file: %v\n", err)
	}

	// Initialize SchemaFlux
	if err := schemaflux.InitWithEnv(); err != nil {
		schemaflux.GetLogger().Error("Failed to initialize SchemaFlux", "error", err)
		return
	}

	// Code snippets to evaluate
	snippets := []CodeSnippet{
		{
			ID:       1,
			Author:   "Alice",
			Language: "Go",
			Code: `func calculateTotal(prices []float64) float64 {
    total := 0.0
    for _, price := range prices {
        total += price
    }
    return total
}`,
		},
		{
			ID:       2,
			Author:   "Bob",
			Language: "Go",
			Code: `func calculateTotal(p []float64) float64 {
    var t float64
    for i:=0;i<len(p);i++ {
        t=t+p[i]
    }
    return t
}`,
		},
		{
			ID:       3,
			Author:   "Carol",
			Language: "Go",
			Code: `// CalculateTotal computes the sum of all prices in the slice.
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
}`,
		},
	}

	// Scoring criteria
	criteria := []string{
		"readability",
		"maintainability",
		"documentation",
		"error handling",
		"best practices",
	}

	fmt.Println("📊 Score Example - Code Quality Assessment")
	fmt.Println("=" + string(make([]byte, 60)))
	fmt.Println("🎯 Evaluation Criteria:", criteria)
	fmt.Println("📏 Scale: 1-10 (10 = excellent)")

	// Score each snippet with full results
	type ScoredSnippet struct {
		Snippet CodeSnippet
		Result  schemaflux.ScoreResult
	}
	var scored []ScoredSnippet

	for _, snippet := range snippets {
		fmt.Printf("\n📝 Evaluating Submission #%d by %s\n", snippet.ID, snippet.Author)
		fmt.Println("---")
		fmt.Println(snippet.Code)
		fmt.Println("---")

		// Score the code using the new generic signature
		scoreOpts := schemaflux.NewScoreOptions().
			WithScaleMin(1).
			WithScaleMax(10).
			WithCriteria(criteria)
		scoreOpts.OpOptions.Intelligence = schemaflux.Fast

		result, err := schemaflux.Score[string](snippet.Code, scoreOpts)
		if err != nil {
			schemaflux.GetLogger().Error("Failed to score snippet", "snippetID", snippet.ID, "error", err)
			continue
		}

		scored = append(scored, ScoredSnippet{snippet, result})

		// Display score with visualization
		stars := int(result.Value)
		starBar := ""
		for i := 0; i < 10; i++ {
			if i < stars {
				starBar += "⭐"
			} else {
				starBar += "☆"
			}
		}

		fmt.Printf("\n✅ Score: %.1f/10 %s (%.0f%% normalized)\n", result.Value, starBar, result.NormalizedValue*100)

		// Show breakdown if available
		if len(result.Breakdown) > 0 {
			fmt.Println("📋 Score Breakdown:")
			for criterion, score := range result.Breakdown {
				fmt.Printf("   - %s: %.1f\n", criterion, score)
			}
		}

		// Show strengths and weaknesses
		if len(result.Strengths) > 0 {
			fmt.Println("💪 Strengths:")
			for _, s := range result.Strengths {
				fmt.Printf("   ✓ %s\n", s)
			}
		}
		if len(result.Weaknesses) > 0 {
			fmt.Println("⚠️  Areas for Improvement:")
			for _, w := range result.Weaknesses {
				fmt.Printf("   ✗ %s\n", w)
			}
		}

		// Show reasoning if available
		if result.Reasoning != "" {
			fmt.Printf("📝 Reasoning: %s\n", result.Reasoning)
		}

		// Provide feedback based on score
		if result.Value >= 8.5 {
			fmt.Println("💎 Excellent! Production-ready code with best practices.")
		} else if result.Value >= 7.0 {
			fmt.Println("👍 Good code quality. Minor improvements possible.")
		} else if result.Value >= 5.0 {
			fmt.Println("⚠️  Acceptable but needs improvement.")
		} else {
			fmt.Println("❌ Needs significant refactoring.")
		}
	}

	// Show ranking
	fmt.Println("\n🏆 Final Rankings:")
	fmt.Println("---")
	// Sort by score descending
	for i := len(scored) - 1; i >= 0; i-- {
		for j := 0; j < len(scored)-i-1; j++ {
			if scored[j].Result.Value < scored[j+1].Result.Value {
				scored[j], scored[j+1] = scored[j+1], scored[j]
			}
		}
	}

	for i, s := range scored {
		medal := "🥉"
		if i == 0 {
			medal = "🥇"
		} else if i == 1 {
			medal = "🥈"
		}
		fmt.Printf("%s %d. %s - Score: %.1f/10\n", medal, i+1, s.Snippet.Author, s.Result.Value)
	}

	fmt.Println("\n✨ Success! All code submissions evaluated with detailed breakdowns")
}
