package main

import (
	"context"
	"fmt"
	"io"
	"os"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// SupportTicket represents a customer support ticket.
type SupportTicket struct {
	ID          int
	Title       string
	Description string
	Category    string
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "similar example: %v\nCheck your provider configuration and try again.\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "Similar Example - Duplicate Ticket Detection")
	fmt.Fprintln(out, "============================================")

	tickets := []SupportTicket{
		{ID: 1001, Title: "Can't login to my account", Description: "I'm trying to log in but keep getting an error message saying my password is wrong. I've reset it twice.", Category: "Authentication"},
		{ID: 1002, Title: "Payment declined", Description: "My credit card was declined when trying to upgrade to premium. Card works fine on other sites.", Category: "Billing"},
		{ID: 1003, Title: "Unable to sign in", Description: "Having trouble accessing my account. Says invalid credentials but I know my password is correct.", Category: "Authentication"},
		{ID: 1004, Title: "App crashes on startup", Description: "Every time I open the app it closes immediately. Happens on my iPhone 14.", Category: "Technical"},
	}

	fmt.Fprint(out, "\nChecking tickets for similarity:\n\n")
	// Compare tickets pairwise to find duplicates. Similarity scores are model
	// claims; the operation applies the threshold in Go to decide IsSimilar.
	for i := 0; i < len(tickets); i++ {
		for j := i + 1; j < len(tickets); j++ {
			ticketA, ticketB := tickets[i], tickets[j]
			result, err := schemaflux.Similar(ctx, ticketA, ticketB, schemaflux.SimilarOptions{
				Threshold: 0.7,
				Aspects:   []string{"problem description", "underlying issue", "user intent"},
			})
			if err != nil {
				return fmt.Errorf("compare tickets #%d and #%d: %w", ticketA.ID, ticketB.ID, err)
			}

			fmt.Fprintf(out, "Ticket #%d vs Ticket #%d\n", ticketA.ID, ticketB.ID)
			fmt.Fprintf(out, "   A: %q\n", ticketA.Title)
			fmt.Fprintf(out, "   B: %q\n", ticketB.Title)

			bar := ""
			filled := int(result.Score * 10)
			for k := 0; k < 10; k++ {
				if k < filled {
					bar += "#"
				} else {
					bar += "."
				}
			}
			fmt.Fprintf(out, "   Similarity: %s %.0f%%\n", bar, result.Score*100)
			if result.IsSimilar {
				fmt.Fprintln(out, "   Potential duplicate detected!")
			} else {
				fmt.Fprintln(out, "   Not similar")
			}

			printAspects(out, "Matched", result.MatchedAspects)
			printAspects(out, "Differing", result.DifferingAspects)
			if result.Explanation != "" {
				fmt.Fprintf(out, "   Explanation: %s\n", result.Explanation)
			}
			fmt.Fprintln(out)
		}
	}

	fmt.Fprintln(out, "Summary:")
	fmt.Fprintln(out, "   The Similar operation helps identify:")
	fmt.Fprintln(out, "   - Duplicate tickets that should be merged")
	fmt.Fprintln(out, "   - Related issues that may have the same root cause")
	fmt.Fprintln(out, "   - Patterns in customer problems")
	fmt.Fprintln(out, "\nSuccess! Semantic similarity analysis complete")
	return nil
}

func printAspects(out io.Writer, heading string, aspects []schemaflux.AspectMatch) {
	if len(aspects) == 0 {
		return
	}
	fmt.Fprintf(out, "   %s aspects:\n", heading)
	for _, aspect := range aspects {
		fmt.Fprintf(out, "     - %s (%.0f%%): %s\n", aspect.Aspect, aspect.Score*100, aspect.Reason)
	}
}
