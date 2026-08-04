// Example: 01-extract
//
// Operation: Extract[T] - Converts unstructured data into strongly-typed Go structs
//
// Input: Raw email text (unstructured string)
//   From: john.smith@example.com
//   To: sarah.jones@company.com
//   Subject: Project Update - Q4 Results
//   ...body text...
//   Sent: December 15, 2024
//
// Expected Output:
//   Email{
//       From:    "john.smith@example.com",
//       To:      "sarah.jones@company.com",
//       Subject: "Project Update - Q4 Results",
//       Date:    2024-12-15T00:00:00Z,
//       Tags:    ["Project", "Update", "Q4", "Results", "productivity", ...],
//       Body:    "Hi Sarah, I wanted to share the Q4 results...",
//   }
//
// Provider: Cerebras (gpt-oss-120b via Fast intelligence)
// Expected Duration: ~500-800ms
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
)

// Email represents a structured email extracted from unstructured text
type Email struct {
	From    string    `json:"from"`    // Expected: "john.smith@example.com"
	To      string    `json:"to"`      // Expected: "sarah.jones@company.com"
	Subject string    `json:"subject"` // Expected: "Project Update - Q4 Results"
	Date    time.Time `json:"date"`    // Expected: 2024-12-15
	Body    string    `json:"body"`    // Expected: Email body text
	Tags    []string  `json:"tags"`    // Expected: LLM-inferred tags like ["Project", "Update", "Q4"]
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
		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Parse KEY=VALUE
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])
			os.Setenv(key, value)
		}
	}
	return scanner.Err()
}

func main() {
	// Load .env file from project root
	if err := loadEnv("../../.env"); err != nil {
		fmt.Printf("Warning: Could not load .env file: %v\n", err)
	}

	// Initialize SchemaFlux (reads from environment variables)
	if err := schemaflux.InitWithEnv(); err != nil {
		schemaflux.GetLogger().Error("Failed to initialize SchemaFlux", "error", err)
		os.Exit(1)
	}

	// Raw email text (unstructured)
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

	fmt.Println("📧 Email Parser Example")
	fmt.Println("=" + string(make([]byte, 50)))
	fmt.Println("\n📥 Raw Input:")
	fmt.Println(rawEmail)

	// Extract structured email from unstructured text
	email, err := schemaflux.Extract[Email](rawEmail, schemaflux.NewExtractOptions().
		WithIntelligence(schemaflux.Fast).
		WithSteering("Extract all email fields including metadata and categorize by tags"))

	if err != nil {
		schemaflux.GetLogger().Error("Extraction failed", "error", err)
		os.Exit(1)
	}

	// Display structured output
	fmt.Println("\n✅ Extracted Email:")
	fmt.Printf("  From:    %s\n", email.From)
	fmt.Printf("  To:      %s\n", email.To)
	fmt.Printf("  Subject: %s\n", email.Subject)
	fmt.Printf("  Date:    %s\n", email.Date.Format("2006-01-02"))
	fmt.Printf("  Tags:    %v\n", email.Tags)
	fmt.Printf("\n  Body:\n  %s\n", email.Body)

	fmt.Println("\n✨ Success! Unstructured text → Structured data")
}
