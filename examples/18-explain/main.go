// 18-explain: Generate human-readable explanations of data structures
// Intelligence: Fast (Cerebras gpt-oss-120b)
// Expectations:
// - Explains user profile data for different audiences
// - Technical: focuses on struct design, serialization, patterns
// - Executive: focuses on business value, engagement metrics
// - Beginner: uses simple language and step-by-step format

package main

import (
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/examples/internal/exampleutil"
)

// UserProfile represents a user's profile data
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

	fmt.Println("?? Explain Example - Generate Human-Readable Explanations")
	fmt.Println("=" + string(make([]byte, 60)))

	// Initialize SchemaFlux with Fast intelligence (Cerebras)
	if err := exampleutil.Bootstrap(); err != nil {
		schemaflux.GetLogger().Error("Failed to initialize SchemaFlux", "error", err)
		return
	}

	// Sample user data to explain
	userData := UserProfile{
		ID:         12345,
		Name:       "Alice Johnson",
		Email:      "alice.johnson@example.com",
		Age:        28,
		Role:       "premium",
		IsVerified: true,
		Interests:  []string{"technology", "data science", "machine learning"},
		JoinedAt:   "2023-01-15",
	}

	fmt.Println("\n?? Data to Explain:")
	fmt.Printf("   %+v\n", userData)

	// Example 1: Technical audience explanation
	fmt.Println("\n" + "-" + string(make([]byte, 60)))
	fmt.Println("1??  Technical Audience Explanation")
	fmt.Println("-" + string(make([]byte, 60)))

	techResult, err := schemaflux.Explain(userData,
		schemaflux.NewExplainOptions().
			WithAudience("technical").
			WithDepth(2).
			WithFormat("structured").
			WithFocus("implementation").
			WithIntelligence(schemaflux.Fast))

	if err != nil {
		fmt.Printf("   ? Error: %v\n", err)
	} else {
		fmt.Printf("\n   ?? Summary: %s\n", techResult.Summary)
		fmt.Printf("\n   ?? Explanation:\n   %s\n", techResult.Explanation)
		if len(techResult.KeyPoints) > 0 {
			fmt.Println("\n   ?? Key Points:")
			for _, point := range techResult.KeyPoints {
				fmt.Printf("      â€¢ %s\n", point)
			}
		}
	}

	// Example 2: Executive audience explanation
	fmt.Println("\n" + "-" + string(make([]byte, 60)))
	fmt.Println("2??  Executive Audience Explanation")
	fmt.Println("-" + string(make([]byte, 60)))

	execResult, err := schemaflux.Explain(userData,
		schemaflux.NewExplainOptions().
			WithAudience("executive").
			WithDepth(1).
			WithFormat("bullet-points").
			WithFocus("benefits").
			WithBackground("Business intelligence and user analytics data").
			WithIntelligence(schemaflux.Fast))

	if err != nil {
		fmt.Printf("   ? Error: %v\n", err)
	} else {
		fmt.Printf("\n   ?? Summary: %s\n", execResult.Summary)
		fmt.Printf("\n   ?? Explanation:\n   %s\n", execResult.Explanation)
		if len(execResult.KeyPoints) > 0 {
			fmt.Println("\n   ?? Key Points:")
			for _, point := range execResult.KeyPoints {
				fmt.Printf("      â€¢ %s\n", point)
			}
		}
	}

	// Example 3: Beginner-friendly explanation
	fmt.Println("\n" + "-" + string(make([]byte, 60)))
	fmt.Println("3??  Beginner-Friendly Explanation")
	fmt.Println("-" + string(make([]byte, 60)))

	beginnerResult, err := schemaflux.Explain(userData,
		schemaflux.NewExplainOptions().
			WithAudience("beginner").
			WithDepth(1).
			WithFormat("step-by-step").
			WithFocus("overview").
			WithIntelligence(schemaflux.Fast))

	if err != nil {
		fmt.Printf("   ? Error: %v\n", err)
	} else {
		fmt.Printf("\n   ?? Summary: %s\n", beginnerResult.Summary)
		fmt.Printf("\n   ?? Explanation:\n   %s\n", beginnerResult.Explanation)
		if len(beginnerResult.KeyPoints) > 0 {
			fmt.Println("\n   ?? Key Points:")
			for _, point := range beginnerResult.KeyPoints {
				fmt.Printf("      â€¢ %s\n", point)
			}
		}
	}

	fmt.Println()
	fmt.Println("? Success! Data explained for different audiences")
}
