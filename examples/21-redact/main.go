// 21-redact: LLM-powered sensitive data detection and character-level masking
// Intelligence: Fast (Cerebras gpt-oss-120b)
// Expectations:
// - LLM identifies sensitive data (emails, phones, SSNs, names, etc.)
// - Returns exact character positions (spans) for each detection
// - Masks at character level with configurable mask char
// - Supports partial reveal (show first N, last N characters)

package main

import (
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/examples/internal/exampleutil"
)

func main() {

	fmt.Println("?? RedactLLM Example - AI-Powered Sensitive Data Masking")
	fmt.Println("=" + string(make([]byte, 60)))

	// Initialize SchemaFlux
	if err := exampleutil.Bootstrap(); err != nil {
		schemaflux.GetLogger().Error("Failed to initialize SchemaFlux", "error", err)
		return
	}

	// Example 1: Basic sensitive data detection
	fmt.Println("\n1??  Basic PII Detection")
	fmt.Println("-" + string(make([]byte, 60)))

	text1 := "Contact John Smith at john.smith@company.com or call 555-123-4567 for support."
	fmt.Printf("   Input:  %q\n", text1)

	result1, err := schemaflux.RedactLLM(text1,
		schemaflux.NewRedactLLMOptions().
			WithCategories([]string{"all"}).
			WithMaskChar('*').
			WithIntelligence(schemaflux.Fast))

	if err != nil {
		fmt.Printf("   ? Error: %v\n", err)
	} else {
		fmt.Printf("   Output: %q\n", result1.Text)
		fmt.Println("   Spans detected:")
		for _, span := range result1.Spans {
			fmt.Printf("      [%d:%d] %s ? %q\n", span.Start, span.End, span.Category, span.Original)
		}
	}

	// Example 2: Partial reveal (show first/last characters)
	fmt.Println("\n2??  Partial Reveal - Show First 2 and Last 2 Characters")
	fmt.Println("-" + string(make([]byte, 60)))

	text2 := "My email is alice.johnson@example.org and SSN is 123-45-6789."
	fmt.Printf("   Input:  %q\n", text2)

	result2, err := schemaflux.RedactLLM(text2,
		schemaflux.NewRedactLLMOptions().
			WithCategories([]string{"email", "ssn"}).
			WithMaskChar('*').
			WithShowFirst(2).
			WithShowLast(2).
			WithIntelligence(schemaflux.Fast))

	if err != nil {
		fmt.Printf("   ? Error: %v\n", err)
	} else {
		fmt.Printf("   Output: %q\n", result2.Text)
		fmt.Println("   Spans detected:")
		for _, span := range result2.Spans {
			fmt.Printf("      [%d:%d] %s ? %q\n", span.Start, span.End, span.Category, span.Original)
		}
	}

	// Example 3: Custom mask character
	fmt.Println("\n3??  Custom Mask Character (|)")
	fmt.Println("-" + string(make([]byte, 60)))

	text3 := "Credit card: 4532-1234-5678-9012, expires 12/25."
	fmt.Printf("   Input:  %q\n", text3)

	result3, err := schemaflux.RedactLLM(text3,
		schemaflux.NewRedactLLMOptions().
			WithCategories([]string{"credit_card"}).
			WithMaskChar('|').
			WithIntelligence(schemaflux.Fast))

	if err != nil {
		fmt.Printf("   ? Error: %v\n", err)
	} else {
		fmt.Printf("   Output: %q\n", result3.Text)
		fmt.Println("   Spans detected:")
		for _, span := range result3.Spans {
			fmt.Printf("      [%d:%d] %s ? %q\n", span.Start, span.End, span.Category, span.Original)
		}
	}

	// Example 4: Detect secrets and API keys
	fmt.Println("\n4??  Detect Secrets and API Keys")
	fmt.Println("-" + string(make([]byte, 60)))

	text4 := "Set API_KEY=sk-abc123xyz789secret and DB_PASSWORD=MyS3cretP@ss!"
	fmt.Printf("   Input:  %q\n", text4)

	result4, err := schemaflux.RedactLLM(text4,
		schemaflux.NewRedactLLMOptions().
			WithCategories([]string{"api_key", "password"}).
			WithMaskChar('#').
			WithShowFirst(3). // Show "sk-" prefix
			WithIntelligence(schemaflux.Fast))

	if err != nil {
		fmt.Printf("   ? Error: %v\n", err)
	} else {
		fmt.Printf("   Output: %q\n", result4.Text)
		fmt.Println("   Spans detected:")
		for _, span := range result4.Spans {
			fmt.Printf("      [%d:%d] %s ? %q\n", span.Start, span.End, span.Category, span.Original)
		}
	}

	// Example 5: Mixed content with multiple categories
	fmt.Println("\n5??  Complex Text with Multiple PII Types")
	fmt.Println("-" + string(make([]byte, 60)))

	text5 := `Customer: Bob Wilson
Email: bob.wilson@gmail.com
Phone: (555) 987-6543
Address: 123 Main Street, New York, NY 10001
DOB: 03/15/1985
SSN: 987-65-4321`

	fmt.Printf("   Input:\n%s\n\n", text5)

	result5, err := schemaflux.RedactLLM(text5,
		schemaflux.NewRedactLLMOptions().
			WithCategories([]string{"all"}).
			WithMaskChar('X').
			WithIntelligence(schemaflux.Fast))

	if err != nil {
		fmt.Printf("   ? Error: %v\n", err)
	} else {
		fmt.Printf("   Output:\n%s\n", result5.Text)
		fmt.Printf("\n   Categories found: %v\n", result5.Categories)
		fmt.Printf("   Total spans: %d\n", len(result5.Spans))
	}

	// Example 6: Specific category only
	fmt.Println("\n6??  Detect Only Email Addresses")
	fmt.Println("-" + string(make([]byte, 60)))

	text6 := "Contacts: admin@company.com, support@help.org, John Smith (555-1234)"
	fmt.Printf("   Input:  %q\n", text6)

	result6, err := schemaflux.RedactLLM(text6,
		schemaflux.NewRedactLLMOptions().
			WithCategories([]string{"email"}). // Only emails, ignore phone
			WithMaskChar('*').
			WithIntelligence(schemaflux.Fast))

	if err != nil {
		fmt.Printf("   ? Error: %v\n", err)
	} else {
		fmt.Printf("   Output: %q\n", result6.Text)
		fmt.Println("   Spans detected:")
		for _, span := range result6.Spans {
			fmt.Printf("      [%d:%d] %s ? %q\n", span.Start, span.End, span.Category, span.Original)
		}
	}

	fmt.Println()
	fmt.Println("? Success! Sensitive data detected and masked with LLM intelligence")
}
