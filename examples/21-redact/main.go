// 21-redact demonstrates model-assisted sensitive text detection and masking.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/monstercameron/schemaflux/v2/redact"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "Redact Example - Sensitive Data Masking")
	fmt.Fprintln(out, "============================================================")

	cases := []struct {
		title, text string
		options     redact.TextOptions
	}{
		{"1. Basic PII Detection", "Contact John Smith at john.smith@company.com or call 555-123-4567 for support.", redact.TextOptions{Categories: []string{"all"}, MaskChar: '*'}},
		{"2. Partial Reveal - Show First 2 and Last 2 Characters", "My email is alice.johnson@example.org and SSN is 123-45-6789.", redact.TextOptions{Categories: []string{"email", "ssn"}, MaskChar: '*', ShowFirst: 2, ShowLast: 2}},
		{"3. Custom Mask Character (|)", "Credit card: 4532-1234-5678-9012, expires 12/25.", redact.TextOptions{Categories: []string{"credit_card"}, MaskChar: '|'}},
		{"4. Detect Secrets and API Keys", "Set API_KEY=sk-abc123xyz789secret and DB_PASSWORD=MyS3cretP@ss!", redact.TextOptions{Categories: []string{"api_key", "password"}, MaskChar: '#', ShowFirst: 3}},
		{"5. Complex Text with Multiple PII Types", "Customer: Bob Wilson\nEmail: bob.wilson@gmail.com\nPhone: (555) 987-6543\nAddress: 123 Main Street, New York, NY 10001\nDOB: 03/15/1985\nSSN: 987-65-4321", redact.TextOptions{Categories: []string{"all"}, MaskChar: 'X'}},
		{"6. Detect Only Email Addresses", "Contacts: admin@company.com, support@help.org, John Smith (555-1234)", redact.TextOptions{Categories: []string{"email"}, MaskChar: '*'}},
	}

	for _, example := range cases {
		fmt.Fprintf(out, "\n%s\n", example.title)
		fmt.Fprintln(out, "------------------------------------------------------------")
		fmt.Fprintf(out, "   Input:  %q\n", example.text)
		result, err := redact.Text(ctx, example.text, example.options)
		if err != nil {
			return fmt.Errorf("%s failed; check the configured provider and retry: %w", example.title, err)
		}
		fmt.Fprintf(out, "   Output: %q\n", result.Text)
		fmt.Fprintln(out, "   Spans detected:")
		for _, span := range result.Spans {
			fmt.Fprintf(out, "      [%d:%d] %s -> %q\n", span.Start, span.End, span.Category, span.Original)
		}
		if example.title == "5. Complex Text with Multiple PII Types" {
			fmt.Fprintf(out, "\n   Categories found: %v\n", result.Categories)
			fmt.Fprintf(out, "   Total spans: %d\n", len(result.Spans))
		}
	}

	fmt.Fprintln(out, "\nSensitive text detection and masking completed.")
	return nil
}
