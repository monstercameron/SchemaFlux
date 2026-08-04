# Choose Example - Product Recommender

## What This Does

Demonstrates the **Choose** operation: intelligently selecting the best option from a list based on criteria.

This example selects:
- **Input**: Customer query + product catalog
- **Output**: Best matching product with reasoning

## Use Case

**Real-World Applications**:
- E-commerce product recommendations
- Support ticket routing
- Content recommendation engines
- Service plan selection
- Best candidate selection from resumes

## How It Works

```go
chosen, err := schemaflux.Choose(
    products,
    ops.NewChooseOptions().
        WithCriteria("I need something portable for coding...").
        WithSteering("Consider budget, portability, and use case"),
)
```

The LLM intelligently:
1. Analyzes each option against criteria
2. Considers explicit requirements (budget, portability)
3. Infers implicit needs (student-friendly, value)
4. Ranks options by relevance
5. Selects the best match

## Running the Example

```bash
# Set your API key
export OPENAI_API_KEY='your-key-here'

# Run the example
go run main.go
```

## Expected Output

```
🛍️ Choose Example - Product Recommender
==================================================

💬 Customer Query:
I'm a college student studying computer science. I need something 
portable for coding and note-taking. My budget is around $500-600.

📦 Available Products:
  1. UltraBook Pro - $1299.99 (Laptop)
  2. PowerStation Desktop - $1899.99 (Desktop)
  3. BudgetBook - $499.99 (Laptop)
  4. CreatorPro Workstation - $2499.99 (Desktop)

✅ Recommended Product:
---
🎯 BudgetBook
💰 Price: $499.99
📱 Category: Laptop
✨ Features: [8GB RAM 256GB SSD 14-inch display 8hr battery]

📝 Description:
   Affordable laptop for students and basic computing

💡 Why this choice?
   - Within budget ($500-600)
   - Portable laptop (not desktop)
   - Suitable for coding and note-taking
   - Good value for students

✨ Success! Intelligently selected best match
```

## Key Features Demonstrated

- ✅ **Context-Aware Selection**: Considers multiple factors
- ✅ **Budget Awareness**: Respects price constraints
- ✅ **Use Case Matching**: Aligns features with needs
- ✅ **Implicit Understanding**: Infers student needs

## Use Cases

1. **E-commerce**: Product recommendations
2. **Customer Service**: Route tickets to right department
3. **HR**: Select best candidate from pool
4. **Content**: Recommend articles/videos
5. **Pricing**: Suggest appropriate service tier

## Learn More

- [SchemaFlux Documentation](../../README.md)
- [Choose API Reference](../../docs/reference/API.md#choose)
