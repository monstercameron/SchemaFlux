package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// Email is the structured form of the example email.
type Email struct {
	From    string    `json:"from"`
	To      string    `json:"to"`
	Subject string    `json:"subject"`
	Date    time.Time `json:"date"`
	Body    string    `json:"body"`
	Tags    []string  `json:"tags"`
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Email extraction failed: %v\nCheck your provider configuration and try again.\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	rawEmail := `
From: john.smith@example.com
To: sarah.jones@company.com
Subject: Project Update - Q4 Results

Hi Sarah,

I wanted to share the Q4 results with you. The project exceeded expectations
with a 25% increase in productivity. The team did an excellent job meeting
all deadlines.

Let me know if you need any additional details.

Best regards,
John Smith
Sent: December 15, 2024
`

	fmt.Fprintln(out, "📧 Email Parser Example")
	fmt.Fprintln(out, strings.Repeat("=", 50))
	fmt.Fprintln(out, "\n📥 Raw Input:")
	fmt.Fprintln(out, rawEmail)

	// Extract maps the unstructured email to the fields and types in Email.
	email, err := schemaflux.Extract[Email](ctx, rawEmail, schemaflux.ExtractOptions{
		Tier:     schemaflux.TierFast,
		Steering: "Extract all email fields including metadata and categorize by tags",
	})
	if err != nil {
		return fmt.Errorf("extract email: %w", err)
	}

	fmt.Fprintln(out, "\n✅ Extracted Email:")
	fmt.Fprintf(out, "  From:    %s\n", email.From)
	fmt.Fprintf(out, "  To:      %s\n", email.To)
	fmt.Fprintf(out, "  Subject: %s\n", email.Subject)
	fmt.Fprintf(out, "  Date:    %s\n", email.Date.Format("2006-01-02"))
	fmt.Fprintf(out, "  Tags:    %v\n", email.Tags)
	fmt.Fprintf(out, "\n  Body:\n  %s\n", email.Body)
	fmt.Fprintln(out, "\n✨ Success! Unstructured text → Structured data")
	return nil
}
