// Example: 05-filter
//
// Filter support tickets that need immediate attention: business-impacting
// outages, payment failures, and security concerns.
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
	ID          int    `json:"id"`
	Customer    string `json:"customer"`
	Subject     string `json:"subject"`
	Description string `json:"description"`
	Priority    string `json:"priority"`
	Status      string `json:"status"`
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Filter example failed: %v\nCheck your provider configuration and credentials, then try again.\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	tickets := []SupportTicket{
		{ID: 1, Customer: "Alice Johnson", Subject: "Website is completely down", Description: "Our entire e-commerce website has been offline for 2 hours. We're losing sales!", Priority: "unknown", Status: "open"},
		{ID: 2, Customer: "Bob Smith", Subject: "How to change password?", Description: "I forgot my password and need help resetting it.", Priority: "unknown", Status: "open"},
		{ID: 3, Customer: "Carol White", Subject: "Payment processing failure", Description: "Customer payments are failing with error code 500. This is blocking all transactions.", Priority: "unknown", Status: "open"},
		{ID: 4, Customer: "David Brown", Subject: "Feature request: dark mode", Description: "It would be nice to have a dark mode option in the settings.", Priority: "unknown", Status: "open"},
		{ID: 5, Customer: "Eve Davis", Subject: "Data breach suspected", Description: "We detected unusual activity and potential unauthorized access to customer data.", Priority: "unknown", Status: "open"},
		{ID: 6, Customer: "Frank Miller", Subject: "Invoice copy request", Description: "Can you send me a copy of invoice #12345 from last month?", Priority: "unknown", Status: "open"},
	}

	fmt.Fprintln(out, "🎫 Filter Example - Urgent Ticket Triage")
	fmt.Fprintln(out, "==================================================")
	fmt.Fprintf(out, "\n📥 Total Tickets: %d\n", len(tickets))
	fmt.Fprintln(out, "\nAll Tickets:")
	for _, ticket := range tickets {
		fmt.Fprintf(out, "  #%d - %s: %s\n", ticket.ID, ticket.Customer, ticket.Subject)
	}

	// Filter for issues requiring immediate attention. The criteria guide the
	// model; Filter also checks that every returned item came from this list.
	criteria := `Identify tickets that require immediate attention:
- Affects multiple users or critical business functions
- Security-related issues
- Service outages or payment failures
Exclude routine requests and feature requests.`

	urgentTickets, err := schemaflux.Filter(ctx, tickets, criteria, schemaflux.FilterOptions{
		Options: schemaflux.Options{
			Tier:     schemaflux.TierFast,
			Steering: "Focus on business impact and urgency",
		},
	})
	if err != nil {
		return fmt.Errorf("filter urgent tickets: %w", err)
	}

	fmt.Fprintln(out, "\n🚨 URGENT Tickets (require immediate attention):")
	fmt.Fprintln(out, "---")
	if len(urgentTickets) == 0 {
		fmt.Fprintln(out, "  No urgent tickets found")
	} else {
		for _, ticket := range urgentTickets {
			fmt.Fprintf(out, "\n⚠️  Ticket #%d - %s\n", ticket.ID, ticket.Customer)
			fmt.Fprintf(out, "   Subject: %s\n", ticket.Subject)
			fmt.Fprintf(out, "   Issue: %s\n", ticket.Description)
		}
	}

	fmt.Fprintf(out, "\n✨ Success! Filtered %d urgent tickets from %d total\n", len(urgentTickets), len(tickets))
	return nil
}
