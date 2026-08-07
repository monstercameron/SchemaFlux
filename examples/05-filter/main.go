// Example: 05-filter
//
// Operation: Filter[T] - Semantically filter items based on criteria
//
// Input: []SupportTicket (6 tickets with various urgency levels)
//
//	Tickets:
//	- #1: "Website is completely down" (urgent - service outage)
//	- #2: "How to change password?" (routine request)
//	- #3: "Payment processing failure" (urgent - blocking transactions)
//	- #4: "Feature request: dark mode" (low priority)
//	- #5: "Data breach suspected" (urgent - security issue)
//	- #6: "Invoice copy request" (routine request)
//
// Expected Output: 3 urgent tickets (IDs: 1, 3, 5)
//   - #1: Website outage (affects business)
//   - #3: Payment failure (blocking transactions)
//   - #5: Data breach (security critical)
//
// Provider: Cerebras (gpt-oss-120b via Fast intelligence)
// Expected Duration: ~500-1000ms
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
	ID          int    `json:"id"`          // Expected: Ticket identifier
	Customer    string `json:"customer"`    // Expected: Customer name
	Subject     string `json:"subject"`     // Expected: Brief issue summary
	Description string `json:"description"` // Expected: Detailed issue description
	Priority    string `json:"priority"`    // Expected: Priority level
	Status      string `json:"status"`      // Expected: Ticket status
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
		os.Exit(1)
	}

	// Support tickets
	tickets := []SupportTicket{
		{
			ID:          1,
			Customer:    "Alice Johnson",
			Subject:     "Website is completely down",
			Description: "Our entire e-commerce website has been offline for 2 hours. We're losing sales!",
			Priority:    "unknown",
			Status:      "open",
		},
		{
			ID:          2,
			Customer:    "Bob Smith",
			Subject:     "How to change password?",
			Description: "I forgot my password and need help resetting it.",
			Priority:    "unknown",
			Status:      "open",
		},
		{
			ID:          3,
			Customer:    "Carol White",
			Subject:     "Payment processing failure",
			Description: "Customer payments are failing with error code 500. This is blocking all transactions.",
			Priority:    "unknown",
			Status:      "open",
		},
		{
			ID:          4,
			Customer:    "David Brown",
			Subject:     "Feature request: dark mode",
			Description: "It would be nice to have a dark mode option in the settings.",
			Priority:    "unknown",
			Status:      "open",
		},
		{
			ID:          5,
			Customer:    "Eve Davis",
			Subject:     "Data breach suspected",
			Description: "We detected unusual activity and potential unauthorized access to customer data.",
			Priority:    "unknown",
			Status:      "open",
		},
		{
			ID:          6,
			Customer:    "Frank Miller",
			Subject:     "Invoice copy request",
			Description: "Can you send me a copy of invoice #12345 from last month?",
			Priority:    "unknown",
			Status:      "open",
		},
	}

	fmt.Println("🎫 Filter Example - Urgent Ticket Triage")
	fmt.Println("=" + string(make([]byte, 50)))
	fmt.Printf("\n📥 Total Tickets: %d\n", len(tickets))
	fmt.Println("\nAll Tickets:")
	for _, t := range tickets {
		fmt.Printf("  #%d - %s: %s\n", t.ID, t.Customer, t.Subject)
	}

	// Filter to find URGENT tickets only
	criteria := `Identify tickets that require immediate attention:
- Affects multiple users or critical business functions
- Security-related issues
- Service outages or payment failures
Exclude routine requests and feature requests.`

	filterOpts := schemaflux.NewFilterOptions().WithCriteria(criteria)
	filterOpts.OpOptions.Intelligence = schemaflux.Fast
	filterOpts.OpOptions.Steering = "Focus on business impact and urgency"

	urgentTickets, err := schemaflux.Filter(tickets, filterOpts)

	if err != nil {
		schemaflux.GetLogger().Error("Filtering failed", "error", err)
		os.Exit(1)
	}

	// Display urgent tickets
	fmt.Println("\n🚨 URGENT Tickets (require immediate attention):")
	fmt.Println("---")
	if len(urgentTickets) == 0 {
		fmt.Println("  No urgent tickets found")
	} else {
		for _, t := range urgentTickets {
			fmt.Printf("\n⚠️  Ticket #%d - %s\n", t.ID, t.Customer)
			fmt.Printf("   Subject: %s\n", t.Subject)
			fmt.Printf("   Issue: %s\n", t.Description)
		}
	}

	fmt.Printf("\n✨ Success! Filtered %d urgent tickets from %d total\n", len(urgentTickets), len(tickets))
}
