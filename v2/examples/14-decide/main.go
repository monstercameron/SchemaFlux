// 14-decide: Make decisions with explanations.
// Intelligence: the v2 default tier is Smart.
// Expectations:
// - Routes support tickets to appropriate departments.
// - Reports the model's confidence claim and reasoning for each decision.
// - Technical issues go to Technical Support.
// - Training requests go to Customer Success.
// - Invoice issues go to Billing Support.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/monstercameron/schemaflux/v2/flow"
)

// SupportTicket represents a customer support ticket.
type SupportTicket struct {
	ID          int
	Subject     string
	Description string
	Priority    string
	Customer    string
	Category    string
}

// Department represents a support department.
type Department struct {
	Name        string
	Handles     []string
	MaxPriority string
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Could not route support tickets: %v\nCheck the SchemaFlux provider configuration and try again.\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "Decide Example - Support Ticket Routing")
	fmt.Fprintln(out, "============================================================")

	// Available departments.
	departments := []flow.Decision[Department]{
		{
			Value:       Department{Name: "Technical Support", Handles: []string{"bugs", "errors", "crashes", "performance"}, MaxPriority: "critical"},
			Description: "Handles technical issues, bugs, and system problems",
		},
		{
			Value:       Department{Name: "Billing Support", Handles: []string{"payments", "invoices", "refunds", "subscriptions"}, MaxPriority: "high"},
			Description: "Handles all payment and billing related issues",
		},
		{
			Value:       Department{Name: "Customer Success", Handles: []string{"training", "onboarding", "best practices"}, MaxPriority: "medium"},
			Description: "Helps customers get the most out of the product",
		},
		{
			Value:       Department{Name: "Sales", Handles: []string{"upgrades", "new features", "enterprise"}, MaxPriority: "high"},
			Description: "Handles upgrade requests and enterprise inquiries",
		},
	}

	// Test tickets.
	tickets := []SupportTicket{
		{ID: 101, Subject: "Application crashes on startup", Description: "After the latest update, the app crashes immediately when I try to launch it. This is blocking my work!", Priority: "critical", Customer: "Enterprise Corp", Category: "unknown"},
		{ID: 102, Subject: "Need help with advanced features", Description: "We'd like training on how to use the analytics dashboard and reporting features effectively.", Priority: "medium", Customer: "Small Business Inc", Category: "unknown"},
		{ID: 103, Subject: "Invoice shows wrong amount", Description: "Our latest invoice has an incorrect charge. Can you review and send a corrected invoice?", Priority: "high", Customer: "ABC Company", Category: "unknown"},
	}

	// Route each ticket.
	counts := make(map[string]int)
	for i, ticket := range tickets {
		fmt.Fprintf(out, "\n%d. Ticket #%d: %s\n", i+1, ticket.ID, ticket.Subject)
		fmt.Fprintln(out, "---")
		fmt.Fprintf(out, "   Description: %s\n", ticket.Description)
		fmt.Fprintf(out, "   Priority: %s\n", ticket.Priority)
		fmt.Fprintf(out, "   Customer: %s\n\n", ticket.Customer)
		fmt.Fprintln(out, "   Routing ticket...")

		// Decide returns a checked selection from the supplied decisions. The
		// confidence is the model's claim, not a measured probability.
		result, err := flow.Decide(ctx, ticket, departments)
		if err != nil {
			return fmt.Errorf("route ticket #%d: %w", ticket.ID, err)
		}

		chosen := result.Value
		counts[chosen.Name]++
		fmt.Fprintf(out, "\n   Route to: %s\n", chosen.Name)
		fmt.Fprintf(out, "   Model confidence: %.0f%%\n", result.ModelConfidence*100)
		fmt.Fprintf(out, "   Reasoning: %s\n", result.Explanation)
	}

	fmt.Fprintln(out, "\nRouting Summary:")
	fmt.Fprintf(out, "   Total tickets: %d\n", len(tickets))
	for _, name := range []string{"Technical Support", "Billing Support", "Customer Success"} {
		fmt.Fprintf(out, "   %s: %d\n", name, counts[name])
	}
	fmt.Fprintln(out, "\nSuccess! Tickets routed intelligently")
	return nil
}
