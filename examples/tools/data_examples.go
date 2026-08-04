package main

import (
	"context"
	"fmt"

	"github.com/monstercameron/schemaflux/internal/tools"
)

func runDataExamples(ctx context.Context) {
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("📊 DATA TOOLS EXAMPLES")
	fmt.Println(strings.Repeat("=", 60))

	// Example 1: Parse CSV
	csvData := `name,age,city
Alice,30,New York
Bob,25,Los Angeles
Charlie,35,Chicago`

	result, err := tools.Execute(ctx, "csv", map[string]any{
		"action": "parse",
		"data":   csvData,
	})
	printResult("CSV Parse", result, err)

	// Example 2: Format CSV from data
	result, err = tools.Execute(ctx, "csv", map[string]any{
		"action": "format",
		"rows": []any{
			map[string]any{"product": "Widget", "price": 9.99, "qty": 100},
			map[string]any{"product": "Gadget", "price": 19.99, "qty": 50},
		},
		"headers": []any{"product", "price", "qty"},
	})
	printResult("CSV Format", result, err)

	// Example 3: Parse JSON
	jsonData := `{"users": [{"name": "Alice", "age": 30}, {"name": "Bob", "age": 25}]}`
	result, err = tools.Execute(ctx, "json", map[string]any{
		"action": "parse",
		"data":   jsonData,
	})
	printResult("JSON Parse", result, err)

	// Example 4: Extract JSON path
	result, err = tools.Execute(ctx, "json", map[string]any{
		"action": "extract",
		"data":   jsonData,
		"path":   "users.0.name",
	})
	printResult("JSON Extract: users.0.name", result, err)

	// Example 5: Validate JSON
	result, err = tools.Execute(ctx, "json", map[string]any{
		"action": "validate",
		"data":   `{"valid": true}`,
	})
	printResult("JSON Validate (valid)", result, err)

	result, err = tools.Execute(ctx, "json", map[string]any{
		"action": "validate",
		"data":   `{invalid json}`,
	})
	printResult("JSON Validate (invalid)", result, err)

	// Example 6: Format JSON (pretty print)
	result, err = tools.Execute(ctx, "json", map[string]any{
		"action": "format",
		"object": map[string]any{
			"name":  "SchemaFlux",
			"type":  "library",
			"tools": 80,
		},
		"pretty": true,
	})
	printResult("JSON Format (pretty)", result, err)

	// Example 7: Parse XML
	xmlData := `<book><title>Go Programming</title><author>John Doe</author></book>`
	result, err = tools.Execute(ctx, "xml", map[string]any{
		"action": "parse",
		"data":   xmlData,
	})
	printResult("XML Parse", result, err)

	// Example 8: Format XML
	result, err = tools.Execute(ctx, "xml", map[string]any{
		"action": "format",
		"object": map[string]any{
			"item":  "Widget",
			"price": 9.99,
		},
		"root": "product",
	})
	printResult("XML Format", result, err)

	// Example 9: Create text table
	result, err = tools.Execute(ctx, "table", map[string]any{
		"data": []any{
			map[string]any{"name": "Alice", "score": 95},
			map[string]any{"name": "Bob", "score": 87},
			map[string]any{"name": "Charlie", "score": 92},
		},
		"headers": []any{"name", "score"},
		"format":  "text",
	})
	printResult("Table (text)", result, err)

	// Example 10: Create markdown table
	result, err = tools.Execute(ctx, "table", map[string]any{
		"data": []any{
			map[string]any{"name": "Alice", "score": 95},
			map[string]any{"name": "Bob", "score": 87},
		},
		"headers": []any{"name", "score"},
		"format":  "markdown",
	})
	printResult("Table (markdown)", result, err)

	// Example 11: Create HTML table
	result, err = tools.Execute(ctx, "table", map[string]any{
		"data": []any{
			map[string]any{"name": "Alice", "score": 95},
		},
		"headers": []any{"name", "score"},
		"format":  "html",
	})
	printResult("Table (HTML)", result, err)

	// Example 12: Diff two objects
	result, err = tools.Execute(ctx, "diff", map[string]any{
		"left": map[string]any{
			"name":    "Alice",
			"age":     30,
			"city":    "New York",
			"deleted": "old value",
		},
		"right": map[string]any{
			"name":  "Alice",
			"age":   31,
			"city":  "New York",
			"added": "new value",
		},
	})
	printResult("Diff Objects", result, err)

	// Example 13: Diff identical objects
	result, err = tools.Execute(ctx, "diff", map[string]any{
		"left":  map[string]any{"a": 1, "b": 2},
		"right": map[string]any{"a": 1, "b": 2},
	})
	printResult("Diff Identical Objects", result, err)
}
