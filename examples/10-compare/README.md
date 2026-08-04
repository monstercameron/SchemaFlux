# Compare Example - Product Comparison

## What This Does

Demonstrates the **Compare** operation: analyzing similarities and differences between items.

This example compares:
- **Input**: Two smartphone products
- **Aspects**: Camera, battery, display, performance, value
- **Output**: Structured comparison with recommendations

## Use Case

**Real-World Applications**:
- E-commerce product comparisons
- Competitive analysis
- Document diff and review
- A/B test result comparison
- Resume screening

## How It Works

```go
comparison, err := ops.Compare(
    productA, productB,
    ops.NewCompareOptions().
        WithComparisonAspects([]string{"camera", "battery", ...}),
)
```

The LLM intelligently:
1. Identifies key differences
2. Highlights similarities
3. Provides context for trade-offs
4. Considers multiple dimensions
5. Generates actionable insights

## Running the Example

```bash
# Set your API key
export OPENAI_API_KEY='your-key-here'

# Run the example
go run main.go
```

## Key Features Demonstrated

- ✅ **Multi-Aspect Comparison**: Multiple evaluation dimensions
- ✅ **Structured Output**: Clear similarities/differences
- ✅ **Contextual Analysis**: Trade-offs and recommendations
- ✅ **Decision Support**: Helps users choose

## Use Cases

1. **E-commerce**: Compare products side-by-side
2. **Business**: Competitive analysis
3. **HR**: Compare candidate qualifications
4. **Technology**: Feature comparison matrices
5. **Finance**: Investment option comparison

## Learn More

- [SchemaFlux Documentation](../../README.md)
- [Compare API Reference](../../docs/reference/API.md#compare)
