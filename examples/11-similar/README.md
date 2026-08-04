# Similar Example - Duplicate Ticket Detection

## What This Does

Demonstrates the **Similar** operation: checking semantic similarity between items.

This example detects:
- **Input**: Support tickets
- **Threshold**: 70% similarity
- **Output**: Duplicate groups with recommendations

## Use Case

**Real-World Applications**:
- Duplicate detection (tickets, bugs, content)
- Plagiarism detection
- Content deduplication
- Similar product recommendations
- Near-duplicate document finding

## How It Works

```go
similar, err := ops.Similar(
    ticket1.Text, ticket2.Text,
    ops.NewSimilarOptions().WithSimilarityThreshold(0.7),
)
```

The LLM intelligently:
1. Understands semantic meaning
2. Ignores superficial differences
3. Detects paraphrasing
4. Handles different wording
5. Identifies core similarity

## Running the Example

```bash
# Set your API key
export OPENAI_API_KEY='your-key-here'

# Run the example
go run main.go
```

## Key Features Demonstrated

- ✅ **Semantic Similarity**: Beyond exact matching
- ✅ **Duplicate Detection**: Groups related items
- ✅ **Threshold Control**: Adjustable sensitivity
- ✅ **Actionable Output**: Merge recommendations

## Use Cases

1. **Support**: Merge duplicate tickets
2. **Content**: Find duplicate articles/posts
3. **E-commerce**: Detect duplicate listings
4. **Education**: Plagiarism detection
5. **QA**: Find duplicate bug reports

## Learn More

- [SchemaFlux Documentation](../../README.md)
- [Similar API Reference](../../docs/reference/API.md#similar)
