package main

import (
	"context"
	"fmt"

	"github.com/monstercameron/schemaflux/internal/tools"
)

func runComputeExamples(ctx context.Context) {
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("📐 COMPUTATION EXAMPLES")
	fmt.Println(strings.Repeat("=", 60))

	// Example 1: Basic arithmetic
	result, err := tools.Execute(ctx, "calculate", map[string]any{
		"expression": "2 + 2 * 3",
	})
	printResult("Calculate: 2 + 2 * 3", result, err)

	// Example 2: Percentage calculation
	result, err = tools.Execute(ctx, "calculate", map[string]any{
		"expression": "15% of 200",
	})
	printResult("Calculate: 15% of 200", result, err)

	// Example 3: Math functions
	result, err = tools.Execute(ctx, "calculate", map[string]any{
		"expression": "sqrt(16) + pow(2, 3)",
	})
	printResult("Calculate: sqrt(16) + pow(2, 3)", result, err)

	// Example 4: Trigonometry
	result, err = tools.Execute(ctx, "calculate", map[string]any{
		"expression": "sin(0) + cos(0)",
	})
	printResult("Calculate: sin(0) + cos(0)", result, err)

	// Example 5: Unit conversion - length
	result, err = tools.Execute(ctx, "convert", map[string]any{
		"value": 100.0,
		"from":  "cm",
		"to":    "inch",
	})
	printResult("Convert: 100 cm to inches", result, err)

	// Example 6: Unit conversion - temperature
	result, err = tools.Execute(ctx, "convert", map[string]any{
		"value": 32.0,
		"from":  "fahrenheit",
		"to":    "celsius",
	})
	printResult("Convert: 32°F to Celsius", result, err)

	// Example 7: Unit conversion - data size
	result, err = tools.Execute(ctx, "convert", map[string]any{
		"value": 1.0,
		"from":  "gb",
		"to":    "mb",
	})
	printResult("Convert: 1 GB to MB", result, err)

	// Example 8: Regex match
	result, err = tools.Execute(ctx, "regex", map[string]any{
		"action":  "match",
		"pattern": `^\d{3}-\d{4}$`,
		"text":    "555-1234",
	})
	printResult("Regex: Match phone number pattern", result, err)

	// Example 9: Regex find all
	result, err = tools.Execute(ctx, "regex", map[string]any{
		"action":  "findall",
		"pattern": `\b\w+@\w+\.\w+\b`,
		"text":    "Contact us at hello@example.com or support@test.org",
	})
	printResult("Regex: Find all email addresses", result, err)

	// Example 10: Regex replace
	result, err = tools.Execute(ctx, "regex", map[string]any{
		"action":  "replace",
		"pattern": `\d{4}`,
		"text":    "Credit card: 1234-5678-9012-3456",
		"replace": "****",
	})
	printResult("Regex: Mask credit card numbers", result, err)

	// Example 11: Using Calculate helper function directly
	val, err := tools.Calculate("(10 + 5) * 2 / 3")
	if err != nil {
		fmt.Printf("\n❌ Calculate helper error: %v\n", err)
	} else {
		fmt.Printf("\n✅ Calculate helper: (10 + 5) * 2 / 3 = %.2f\n", val)
	}

	// Example 12: Using Convert helper function directly
	converted, err := tools.Convert(100, "km", "mi")
	if err != nil {
		fmt.Printf("❌ Convert helper error: %v\n", err)
	} else {
		fmt.Printf("✅ Convert helper: 100 km = %.2f miles\n", converted)
	}
}
