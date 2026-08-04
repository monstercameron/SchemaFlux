# Extract Example - Email Parser

## What This Does

Demonstrates the **Extract** operation: converting unstructured text into strongly-typed Go structs.

This example takes a raw email (plain text) and extracts structured fields:
- Sender and recipient
- Subject line
- Date
- Body content
- Auto-generated tags

## Use Case

**Real-World Application**: Parse incoming emails from various sources (customer support, newsletters, notifications) into a structured database.

## How It Works

```go
email, err := schemaflux.Extract[Email](rawEmail, ops.NewExtractOptions())
```

The LLM intelligently:
1. Identifies email components
2. Maps them to struct fields
3. Infers data types (string, time.Time, []string)
4. Extracts implicit information (tags based on content)

## Running the Example

```bash
# Set your API key
export OPENAI_API_KEY='your-key-here'

# Run the example
go run main.go
```

## Expected Output

```
📧 Email Parser Example
==================================================

📥 Raw Input:
[raw email text]

✅ Extracted Email:
  From:    john.smith@example.com
  To:      sarah.jones@company.com
  Subject: Project Update - Q4 Results
  Date:    2024-12-15
  Tags:    [project, update, results]

  Body:
  [email body content]

✨ Success! Unstructured text → Structured data
```

## Key Features Demonstrated

- ✅ **Type Inference**: Automatically parses dates, arrays
- ✅ **Smart Extraction**: Finds fields even with varied formatting
- ✅ **Semantic Understanding**: Generates tags based on content
- ✅ **Flexible Input**: Works with various email formats

## Learn More

- [SchemaFlux Documentation](../../README.md)
- [Extract API Reference](../../docs/reference/API.md#extract)
