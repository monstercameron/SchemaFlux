package main

import (
	"context"
	"fmt"
	"io"
	"os"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// CustomerRecord represents customer information from different sources.
type CustomerRecord struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
	Email   string `json:"email,omitempty"`
	Phone   string `json:"phone,omitempty"`
	Address string `json:"address,omitempty"`
	VIP     bool   `json:"vip,omitempty"`
	Notes   string `json:"notes,omitempty"`
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Merge failed: %v\nCheck your provider configuration and try again.\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "Merge Example - Customer Record Deduplication")
	fmt.Fprintln(out, "============================================================")

	// Multiple records for the same customer from different systems.
	records := []CustomerRecord{
		{ID: "CRM-001", Name: "John Smith", Email: "john.smith@example.com", Phone: "+1-555-0123", Notes: "Prefers email contact"},
		{ID: "SALES-456", Name: "J. Smith", Phone: "+1-555-0123", Address: "123 Main St, Springfield, IL 62701", VIP: true},
		{ID: "SUPPORT-789", Name: "John A. Smith", Email: "john.smith@example.com", Address: "123 Main Street, Springfield, Illinois", Notes: "Has premium support plan"},
	}

	fmt.Fprintln(out, "\nSource Records:")
	for i, record := range records {
		fmt.Fprintf(out, "\n%d. Record %s:\n", i+1, record.ID)
		printRecord(out, record, "   ")
	}

	// The strategy tells the model how to resolve conflicting fields.
	strategy := `
Merge Strategy:
1. Keep the most complete name variant
2. Prefer email if present in any record
3. Use the most detailed address
4. Set VIP=true if ANY record has it
5. Combine notes from all records
6. Choose ID from CRM system if present
`

	fmt.Fprintln(out, "\nMerging records...")
	merged, err := schemaflux.Merge(ctx, records, strategy)
	if err != nil {
		return fmt.Errorf("merge customer records: %w", err)
	}

	fmt.Fprintln(out, "\nMerged Result:")
	printRecord(out, merged, "   ")

	fmt.Fprintln(out, "\nMerge Analysis:")
	fmt.Fprintln(out, "   Input: 3 duplicate records")
	fmt.Fprintln(out, "   Output: 1 unified record")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "   ✓ Name: Selected most complete variant")
	fmt.Fprintln(out, "   ✓ Email: Preserved from CRM")
	fmt.Fprintln(out, "   ✓ Phone: Common across records")
	fmt.Fprintln(out, "   ✓ Address: Used most detailed version")
	fmt.Fprintln(out, "   ✓ VIP: Upgraded to true")
	fmt.Fprintln(out, "   ✓ Notes: Combined all information")
	fmt.Fprintln(out, "\nSuccess! Customer records merged")
	return nil
}

func printRecord(out io.Writer, r CustomerRecord, indent string) {
	fmt.Fprintf(out, "%sName:    %s\n", indent, valueOrEmpty(r.Name))
	fmt.Fprintf(out, "%sEmail:   %s\n", indent, valueOrEmpty(r.Email))
	fmt.Fprintf(out, "%sPhone:   %s\n", indent, valueOrEmpty(r.Phone))
	fmt.Fprintf(out, "%sAddress: %s\n", indent, valueOrEmpty(r.Address))
	fmt.Fprintf(out, "%sVIP:     %v\n", indent, r.VIP)
	if r.Notes != "" {
		fmt.Fprintf(out, "%sNotes:   %s\n", indent, r.Notes)
	}
}

func valueOrEmpty(s string) string {
	if s == "" {
		return "(empty)"
	}
	return s
}
