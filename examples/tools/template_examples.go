package main

import (
	"context"
	"fmt"

	"github.com/monstercameron/schemaflux/internal/tools"
)

func runTemplateExamples(ctx context.Context) {
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("📝 TEMPLATE TOOLS EXAMPLES")
	fmt.Println(strings.Repeat("=", 60))

	// Example 1: Go template with simple substitution
	result, err := tools.Execute(ctx, "template", map[string]any{
		"template": "Hello, {{.Name}}! You have {{.Count}} messages.",
		"data": map[string]any{
			"Name":  "Alice",
			"Count": 5,
		},
	})
	printResult("Template: Simple substitution", result, err)

	// Example 2: Go template with conditionals
	result, err = tools.Execute(ctx, "template", map[string]any{
		"template": `{{if .Premium}}Welcome, Premium Member!{{else}}Welcome, Guest!{{end}}`,
		"data": map[string]any{
			"Premium": true,
		},
	})
	printResult("Template: Conditional (Premium=true)", result, err)

	result, err = tools.Execute(ctx, "template", map[string]any{
		"template": `{{if .Premium}}Welcome, Premium Member!{{else}}Welcome, Guest!{{end}}`,
		"data": map[string]any{
			"Premium": false,
		},
	})
	printResult("Template: Conditional (Premium=false)", result, err)

	// Example 3: Go template with range/loop
	result, err = tools.Execute(ctx, "template", map[string]any{
		"template": `Shopping List:
{{range .Items}}- {{.}}
{{end}}`,
		"data": map[string]any{
			"Items": []any{"Apples", "Bananas", "Oranges", "Milk"},
		},
	})
	printResult("Template: Loop over items", result, err)

	// Example 4: HTML template (with escaping)
	result, err = tools.Execute(ctx, "template", map[string]any{
		"template": `<h1>{{.Title}}</h1><p>{{.Content}}</p>`,
		"data": map[string]any{
			"Title":   "Welcome",
			"Content": "This is <b>safe</b> content",
		},
		"html": true,
	})
	printResult("Template: HTML (escaped)", result, err)

	// Example 5: Simple string template with {{key}} syntax
	result, err = tools.Execute(ctx, "string_template", map[string]any{
		"template": "Dear {{name}}, your order #{{order_id}} is ready!",
		"values": map[string]any{
			"name":     "Bob",
			"order_id": "12345",
		},
	})
	printResult("String Template: Simple interpolation", result, err)

	// Example 6: String template with missing values
	result, err = tools.Execute(ctx, "string_template", map[string]any{
		"template": "Hello {{name}}, your balance is {{balance}}.",
		"values": map[string]any{
			"name": "Charlie",
			// balance is missing
		},
	})
	printResult("String Template: Missing value", result, err)

	// Example 7: Markdown to HTML
	result, err = tools.Execute(ctx, "markdown", map[string]any{
		"markdown": `# Hello World

This is **bold** and this is *italic*.

- Item 1
- Item 2
- Item 3

Check out [SchemaFlux](https://github.com/schemaflux)!`,
		"format": "html",
	})
	printResult("Markdown: Convert to HTML", result, err)

	// Example 8: Markdown to plain text
	result, err = tools.Execute(ctx, "markdown", map[string]any{
		"markdown": `# Title
This is **bold** and [a link](https://example.com).
- List item`,
		"format": "text",
	})
	printResult("Markdown: Convert to plain text", result, err)

	// Example 9: Markdown with code blocks
	result, err = tools.Execute(ctx, "markdown", map[string]any{
		"markdown": "# Code Example\n\n```go\nfunc main() {\n    fmt.Println(\"Hello!\")\n}\n```\n\nAnd inline `code` here.",
		"format":   "html",
	})
	printResult("Markdown: Code blocks", result, err)

	// Example 10: Complex template for email
	result, err = tools.Execute(ctx, "template", map[string]any{
		"template": `Dear {{.Customer.Name}},

Thank you for your order on {{.Order.Date}}.

Order Summary:
{{range .Order.Items}}- {{.Name}}: ${{.Price}}
{{end}}
Total: ${{.Order.Total}}

Best regards,
The Team`,
		"data": map[string]any{
			"Customer": map[string]any{
				"Name": "Jane Doe",
			},
			"Order": map[string]any{
				"Date": "December 3, 2025",
				"Items": []any{
					map[string]any{"Name": "Widget", "Price": "9.99"},
					map[string]any{"Name": "Gadget", "Price": "19.99"},
				},
				"Total": "29.98",
			},
		},
	})
	printResult("Template: Complex email template", result, err)
}
