// 18-explain demonstrates explanations tailored to different audiences.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// UserProfile represents a user's profile data.
type UserProfile struct {
	ID         int      `json:"id"`
	Name       string   `json:"name"`
	Email      string   `json:"email"`
	Age        int      `json:"age"`
	Role       string   `json:"role"`
	IsVerified bool     `json:"is_verified"`
	Interests  []string `json:"interests"`
	JoinedAt   string   `json:"joined_at"`
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Explain example failed: %v\nCheck your provider configuration and connection, then try again.\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "Explain Example - Generate Human-Readable Explanations")
	fmt.Fprintln(out, "============================================================")

	// Sample user data to explain.
	userData := UserProfile{
		ID: 12345, Name: "Alice Johnson", Email: "alice.johnson@example.com", Age: 28,
		Role: "premium", IsVerified: true,
		Interests: []string{"technology", "data science", "machine learning"}, JoinedAt: "2023-01-15",
	}
	fmt.Fprintln(out, "\nData to Explain:")
	fmt.Fprintf(out, "   %+v\n", userData)

	cases := []struct {
		title   string
		options schemaflux.ExplainOptions
	}{
		{"Technical Audience Explanation", schemaflux.ExplainOptions{Audience: "technical", Depth: 2, Format: "structured", Focus: "implementation", Tier: schemaflux.TierFast}},
		{"Executive Audience Explanation", schemaflux.ExplainOptions{Audience: "executive", Depth: 1, Format: "bullet-points", Focus: "benefits", Background: "Business intelligence and user analytics data", Tier: schemaflux.TierFast}},
		{"Beginner-Friendly Explanation", schemaflux.ExplainOptions{Audience: "beginner", Depth: 1, Format: "step-by-step", Focus: "overview", Tier: schemaflux.TierFast}},
	}
	for i, c := range cases {
		fmt.Fprintf(out, "\n------------------------------------------------------------\n%d. %s\n------------------------------------------------------------\n", i+1, c.title)
		result, err := schemaflux.Explain(ctx, userData, c.options)
		if err != nil {
			return fmt.Errorf("%s: %w", c.title, err)
		}
		fmt.Fprintf(out, "\nSummary: %s\n\nExplanation:\n%s\n", result.Summary, result.Explanation)
		if len(result.KeyPoints) > 0 {
			fmt.Fprintln(out, "\nKey Points:")
			for _, point := range result.KeyPoints {
				fmt.Fprintf(out, "   - %s\n", point)
			}
		}
	}
	fmt.Fprintln(out, "\nSuccess! Data explained for different audiences")
	return nil
}
