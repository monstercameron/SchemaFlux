// Example: 11-similar
//
// Operation: Similar[T] - Check semantic similarity between two items
//
// Input: 4 Support Tickets (pairwise comparison)
//   - #1001: "Can't login to my account" (Authentication)
//   - #1002: "Payment declined" (Billing)
//   - #1003: "Unable to sign in" (Authentication) ← Similar to #1001!
//   - #1004: "App crashes on startup" (Technical)
//
// Expected Output:
//   - #1001 vs #1003: ~80-90% similar (both login issues) → DUPLICATE
//   - #1001 vs #1002: ~10-20% similar (different problems)
//   - #1001 vs #1004: ~10-20% similar (different problems)
//   - etc.
//
// Provider: Cerebras (gpt-oss-120b via Fast intelligence)
// Expected Duration: ~500ms per comparison
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	schemaflux "github.com/monstercameron/schemaflux"
)

// SupportTicket represents a customer support ticket
type SupportTicket struct {
	ID          int
	Title       string
	Description string
	Category    string
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

	fmt.Println("🔎 Similar Example - Duplicate Ticket Detection")
	fmt.Println("=" + string(make([]byte, 60)))

	// Support tickets to check for duplicates
	tickets := []SupportTicket{
		{
			ID:          1001,
			Title:       "Can't login to my account",
			Description: "I'm trying to log in but keep getting an error message saying my password is wrong. I've reset it twice.",
			Category:    "Authentication",
		},
		{
			ID:          1002,
			Title:       "Payment declined",
			Description: "My credit card was declined when trying to upgrade to premium. Card works fine on other sites.",
			Category:    "Billing",
		},
		{
			ID:          1003,
			Title:       "Unable to sign in",
			Description: "Having trouble accessing my account. Says invalid credentials but I know my password is correct.",
			Category:    "Authentication",
		},
		{
			ID:          1004,
			Title:       "App crashes on startup",
			Description: "Every time I open the app it closes immediately. Happens on my iPhone 14.",
			Category:    "Technical",
		},
	}

	fmt.Println("\n📝 Checking tickets for similarity:")
	fmt.Println()

	// Compare tickets pairwise to find duplicates
	for i := 0; i < len(tickets); i++ {
		for j := i + 1; j < len(tickets); j++ {
			ticketA := tickets[i]
			ticketB := tickets[j]

			// Use Similar to check semantic similarity
			opts := schemaflux.NewSimilarOptions().
				WithSimilarityThreshold(0.7).
				WithAspects([]string{"problem description", "underlying issue", "user intent"})
			opts.OpOptions.Intelligence = schemaflux.Fast

			result, err := schemaflux.Similar[SupportTicket](ticketA, ticketB, opts)
			if err != nil {
				schemaflux.GetLogger().Warn("Similarity check failed",
					"ticketA", ticketA.ID, "ticketB", ticketB.ID, "error", err)
				continue
			}

			// Display results
			fmt.Printf("🎫 Ticket #%d vs Ticket #%d\n", ticketA.ID, ticketB.ID)
			fmt.Printf("   A: \"%s\"\n", ticketA.Title)
			fmt.Printf("   B: \"%s\"\n", ticketB.Title)

			// Show similarity score with visualization
			bar := ""
			filled := int(result.Score * 10)
			for k := 0; k < 10; k++ {
				if k < filled {
					bar += "█"
				} else {
					bar += "░"
				}
			}
			fmt.Printf("   Similarity: %s %.0f%%\n", bar, result.Score*100)

			if result.IsSimilar {
				fmt.Println("   ⚠️  POTENTIAL DUPLICATE DETECTED!")
			} else {
				fmt.Println("   ✅ Not similar")
			}

			// Show matched aspects
			if len(result.MatchedAspects) > 0 {
				fmt.Println("   Matched aspects:")
				for _, match := range result.MatchedAspects {
					fmt.Printf("     ✓ %s (%.0f%%): %s\n", match.Aspect, match.Score*100, match.Reason)
				}
			}

			// Show differing aspects
			if len(result.DifferingAspects) > 0 {
				fmt.Println("   Differing aspects:")
				for _, diff := range result.DifferingAspects {
					fmt.Printf("     ✗ %s (%.0f%%): %s\n", diff.Aspect, diff.Score*100, diff.Reason)
				}
			}

			// Show explanation
			if result.Explanation != "" {
				fmt.Printf("   📝 Explanation: %s\n", result.Explanation)
			}
			fmt.Println()
		}
	}

	// Summary
	fmt.Println("📊 Summary:")
	fmt.Println("   The Similar operation helps identify:")
	fmt.Println("   • Duplicate tickets that should be merged")
	fmt.Println("   • Related issues that may have the same root cause")
	fmt.Println("   • Patterns in customer problems")

	fmt.Println("\n✨ Success! Semantic similarity analysis complete")
}
