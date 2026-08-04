# Summarize Example - Article Condensation

## What This Does

Demonstrates the **Summarize** operation: condensing long text while preserving key information.

This example summarizes:
- **Input**: Long article (~1800 characters)
- **Output**: Concise summary (~400 characters, ~78% reduction)
- **Preserves**: Main points, key facts, conclusion

## Use Case

**Real-World Applications**:
- News aggregation and newsletters
- Document management systems
- Research paper abstracts
- Meeting notes and transcripts
- Email thread summarization

## How It Works

```go
summary, err := ops.Summarize(
    article,
    ops.NewSummarizeOptions().WithMaxLength(200),
)
```

The LLM intelligently:
1. Identifies main themes and key points
2. Removes redundant information
3. Preserves critical facts and data
4. Maintains logical flow
5. Respects length constraints

## Running the Example

```bash
# Set your API key
export OPENAI_API_KEY='your-key-here'

# Run the example
go run main.go
```

## Expected Output

```
📰 Summarize Example - Article Condensation
============================================================

📄 Original Article Length: 1847 characters

📥 Original Article:
---
[Full article about AI in healthcare...]
---

✅ Summary (Condensed):
---
AI is transforming healthcare through advanced diagnostics, drug discovery, 
and patient care. Machine learning achieves 94.5% accuracy in detecting 
breast cancer, surpassing human radiologists at 88%. Drug discovery, 
traditionally taking 10-15 years, now completes in weeks with AI. Virtual 
health assistants provide real-time monitoring and personalized treatment. 
However, challenges include data privacy, algorithmic bias, and regulatory 
gaps. Despite these issues, AI is expected to reduce healthcare costs by 
30% and save millions of lives within a decade.
---

📊 Summary Statistics:
   Original:    1847 characters
   Summary:     423 characters
   Compression: 22.9% of original
   Reduction:   77.1% smaller

✨ Success! Article condensed while preserving key information
```

## Key Features Demonstrated

- ✅ **Length Control**: Target length enforcement
- ✅ **Key Point Extraction**: Preserves critical information
- ✅ **Fact Preservation**: Keeps important data (94.5%, 88%, etc.)
- ✅ **Coherent Output**: Maintains readability

## Use Cases

1. **News Feeds**: Generate article previews
2. **Email**: Summarize long email threads
3. **Documents**: Create executive summaries
4. **Transcripts**: Condense meeting recordings
5. **Research**: Generate paper abstracts

## Learn More

- [SchemaFlux Documentation](../../README.md)
- [Summarize API Reference](../../docs/reference/API.md#summarize)
