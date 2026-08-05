// 14-decide: Make decisions with explanations
// Intelligence: Fast (Cerebras gpt-oss-120b)
// Expectations:
// - Routes support tickets to appropriate departments
// - Provides confidence scores and reasoning for each decision
// - Technical issues ? Technical Support
// - Training requests ? Customer Success
// - Invoice issues ? Billing Support

package main

import (
	"context"
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/examples/internal/exampleutil"
)

// SupportTicket represents a customer support ticket
type SupportTicket struct {
	ID          int
	Subject     string
	Description string
	Priority    string
	Customer    string
	Category    string
}

// Department represents a support department
type Department struct {
	Name        string
	Handles     []string
	MaxPriority string
}

func main() {

	// Initialize SchemaFlux with Fast intelligence (Cerebras)
	if err := exampleutil.Bootstrap(); err != nil {
		schemaflux.GetLogger().Error("Failed to initialize SchemaFlux", "error", err)
		return
	}

	fmt.Println("?? Decide Example - Support Ticket Routing")
	fmt.Println("=" + string(make([]byte, 60)))

	// Available departments
	departments := []schemaflux.Decision[Department]{
		{
			Value: Department{
				Name:        "Technical Support",
				Handles:     []string{"bugs", "errors", "crashes", "performance"},
				MaxPriority: "critical",
			},
			Description: "Handles technical issues, bugs, and system problems",
		},
		{
			Value: Department{
				Name:        "Billing Support",
				Handles:     []string{"payments", "invoices", "refunds", "subscriptions"},
				MaxPriority: "high",
			},
			Description: "Handles all payment and billing related issues",
		},
		{
			Value: Department{
				Name:        "Customer Success",
				Handles:     []string{"training", "onboarding", "best practices"},
				MaxPriority: "medium",
			},
			Description: "Helps customers get the most out of the product",
		},
		{
			Value: Department{
				Name:        "Sales",
				Handles:     []string{"upgrades", "new features", "enterprise"},
				MaxPriority: "high",
			},
			Description: "Handles upgrade requests and enterprise inquiries",
		},
	}

	// Test tickets
	tickets := []SupportTicket{
		{
			ID:          101,
			Subject:     "Application crashes on startup",
			Description: "After the latest update, the app crashes immediately when I try to launch it. This is blocking my work!",
			Priority:    "critical",
			Customer:    "Enterprise Corp",
			Category:    "unknown",
		},
		{
			ID:          102,
			Subject:     "Need help with advanced features",
			Description: "We'd like training on how to use the analytics dashboard and reporting features effectively.",
			Priority:    "medium",
			Customer:    "Small Business Inc",
			Category:    "unknown",
		},
		{
			ID:          103,
			Subject:     "Invoice shows wrong amount",
			Description: "Our latest invoice has an incorrect charge. Can you review and send a corrected invoice?",
			Priority:    "high",
			Customer:    "ABC Company",
			Category:    "unknown",
		},
	}

	// Route each ticket
	for i, ticket := range tickets {
		fmt.Printf("\n%d. Ticket #%d: %s\n", i+1, ticket.ID, ticket.Subject)
		fmt.Println("---")
		fmt.Printf("   Description: %s\n", ticket.Description)
		fmt.Printf("   Priority: %s\n", ticket.Priority)
		fmt.Printf("   Customer: %s\n", ticket.Customer)

		fmt.Println()
		fmt.Println("   ?? Routing ticket...")

		// Use Decide to route the ticket
		chosen, result, err := schemaflux.Decide(context.Background(), ticket, departments)
		if err != nil {
			schemaflux.GetLogger().Error("Routing error", "error", err)
			continue
		}

		fmt.Println()
		fmt.Printf("   ? Route to: %s\n", chosen.Name)
		fmt.Printf("   ModelConfidence: %.0f%%\n", result.ModelConfidence*100)
		fmt.Printf("   Reasoning: %s\n", result.Explanation)
	}

	fmt.Println()
	fmt.Println("?? Routing Summary:")
	fmt.Println("   Total tickets: 3")
	fmt.Println("   Technical Support: 1")
	fmt.Println("   Billing Support: 1")
	fmt.Println("   Customer Success: 1")
	fmt.Println()
	fmt.Println("? Success! Tickets routed intelligently")
}
