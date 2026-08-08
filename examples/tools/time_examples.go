package main

import (
	"context"
	"fmt"
	"time"

	"github.com/monstercameron/schemaflux/internal/tools"
)

func runTimeExamples(ctx context.Context) {
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("⏰ TIME/DATE TOOLS EXAMPLES")
	fmt.Println(strings.Repeat("=", 60))

	// Example 1: Get current time
	result, err := tools.Execute(ctx, "now", map[string]any{})
	printResult("Now: Current time", result, err)

	// Example 2: Get time in different timezone
	result, err = tools.Execute(ctx, "now", map[string]any{
		"timezone": "America/New_York",
	})
	printResult("Now: New York time", result, err)

	result, err = tools.Execute(ctx, "now", map[string]any{
		"timezone": "Europe/London",
	})
	printResult("Now: London time", result, err)

	result, err = tools.Execute(ctx, "now", map[string]any{
		"timezone": "Asia/Tokyo",
	})
	printResult("Now: Tokyo time", result, err)

	// Example 3: Different output formats
	result, err = tools.Execute(ctx, "now", map[string]any{
		"format": "unix",
	})
	printResult("Now: Unix timestamp", result, err)

	result, err = tools.Execute(ctx, "now", map[string]any{
		"format": "date",
	})
	printResult("Now: Date only", result, err)

	result, err = tools.Execute(ctx, "now", map[string]any{
		"format": "time",
	})
	printResult("Now: Time only", result, err)

	// Example 4: Parse time string
	result, err = tools.Execute(ctx, "parse_time", map[string]any{
		"time": "2024-12-25T10:30:00Z",
	})
	printResult("Parse Time: RFC3339", result, err)

	result, err = tools.Execute(ctx, "parse_time", map[string]any{
		"time": "2024-12-25",
	})
	printResult("Parse Time: Date", result, err)

	// Example 5: Parse Unix timestamp
	result, err = tools.Execute(ctx, "parse_time", map[string]any{
		"time":   "1735123800",
		"format": "unix",
	})
	printResult("Parse Time: Unix timestamp", result, err)

	// Example 6: Calculate duration between times
	result, err = tools.Execute(ctx, "duration", map[string]any{
		"action": "between",
		"from":   "2024-01-01T00:00:00Z",
		"to":     "2024-12-31T23:59:59Z",
	})
	printResult("Duration: Year 2024", result, err)

	// Example 7: Add duration to time
	now := time.Now().Format(time.RFC3339)
	result, err = tools.Execute(ctx, "duration", map[string]any{
		"action": "add",
		"from":   now,
		"amount": "7d",
	})
	printResult("Duration: Add 7 days", result, err)

	result, err = tools.Execute(ctx, "duration", map[string]any{
		"action": "add",
		"from":   now,
		"amount": "2h30m",
	})
	printResult("Duration: Add 2h30m", result, err)

	// Example 8: Subtract duration
	result, err = tools.Execute(ctx, "duration", map[string]any{
		"action": "subtract",
		"from":   now,
		"amount": "30d",
	})
	printResult("Duration: Subtract 30 days", result, err)

	// Example 9: Natural language schedule
	result, err = tools.Execute(ctx, "schedule", map[string]any{
		"query": "next Monday",
	})
	printResult("Schedule: next Monday", result, err)

	result, err = tools.Execute(ctx, "schedule", map[string]any{
		"query": "in 2 hours",
	})
	printResult("Schedule: in 2 hours", result, err)

	result, err = tools.Execute(ctx, "schedule", map[string]any{
		"query": "tomorrow",
	})
	printResult("Schedule: tomorrow", result, err)

	result, err = tools.Execute(ctx, "schedule", map[string]any{
		"query": "in 1 week",
	})
	printResult("Schedule: in 1 week", result, err)

	// Example 10: Check holidays
	result, err = tools.Execute(ctx, "holiday", map[string]any{
		"date":    "2024-12-25",
		"country": "US",
	})
	printResult("Holiday: Christmas 2024", result, err)

	result, err = tools.Execute(ctx, "holiday", map[string]any{
		"date":    "2024-07-04",
		"country": "US",
	})
	printResult("Holiday: July 4th 2024", result, err)

	result, err = tools.Execute(ctx, "holiday", map[string]any{
		"date":    "2024-12-02",
		"country": "US",
	})
	printResult("Holiday: Regular Monday", result, err)
}
