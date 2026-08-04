# Complete Operation Example

This example demonstrates the SchemaFlux Complete operation, which provides intelligent text completion using LLM capabilities.

## Overview

The Complete operation intelligently completes partial text by:
- Using context from previous messages/conversations
- Adapting to different content types (code, prose, technical writing)
- Controlling creativity and length parameters
- Stopping at appropriate boundaries

## Key Features Demonstrated

1. **Basic Text Completion** - Simple sentence completion
2. **Context-Aware Completion** - Using conversation history
3. **Code Completion** - Programming with stop sequences
4. **Creative Writing** - Higher temperature for imaginative content
5. **Email Completion** - Professional communication
6. **Mode Variations** - Conservative vs creative completion
7. **Length Control** - Strict completion limits
8. **API Documentation** - Technical writing completion
9. **Chat Completion** - Conversational AI responses
10. **Error Handling** - Input validation and error cases

## Running the Example

```bash
cd examples/20-complete
go run main.go
```

## Expected Output

The example shows various completion scenarios with different configurations:

```
=== SchemaFlux Complete Operation Examples ===

1. Basic Text Completion:
   Input: "The weather today is"
   Options: MaxLength=50, Temperature=0.7
   (Would complete with LLM-generated continuation)

2. Completion with Context:
   Input: "Please send me the"
   Context: 3 messages
     1. User: I need help with my order
     2. Assistant: I'd be happy to help! What seems to be the issue?
     3. User: I haven't received my package yet
   Options: MaxLength=100, Temperature=0.8

3. Code Completion with Stop Sequences:
   Input: "function calculateTotal(items) {"
   Stop Sequences: [} \n\n]
   Options: MaxLength=200, Temperature=0.3

4. Creative Writing Completion:
   Input: "Once upon a time, in a land far away,"
   Options: MaxLength=150, Temperature=1.2, TopP=0.95

5. Email Completion:
   Input: "Dear team,\n\nI hope this email finds you well."
   Context: [Subject: Project Update Meeting Previous email: Let's schedule...]
   Options: MaxLength=300

6. Sentence Completion (Different Modes):
   Input: "The new feature will"
   Conservative Mode: Temperature=0.1
   Balanced Mode: Temperature=0.7
   Creative Mode: Temperature=1.5

7. Completion with Length Constraints:
   Input: "In conclusion,"
   Options: MaxLength=50, StopSequences=[. !]

8. API Documentation Completion:
   Input: "// GET /api/users - Retrieve a list of users"
   Options: MaxLength=250, Temperature=0.4

9. Chat Message Completion:
   Input: "That sounds like a great idea! I think we should"
   Context: 3 messages
   Options: MaxLength=120, Temperature=0.9

10. Error Handling:
   Testing various error conditions...
   ✓ Empty input rejected: partial text cannot be empty
   ✓ Invalid options rejected: MaxLength must be positive

=== Complete Operation Examples Complete ===

Note: These examples show the API structure. In a real implementation,
the Complete function would call an LLM to generate intelligent completions
based on the partial text and context provided.
```

## Configuration Options

- `WithContext([]string)` - Previous messages for context
- `WithMaxLength(int)` - Maximum completion length
- `WithStopSequences([]string)` - Sequences that stop generation
- `WithTemperature(float32)` - Creativity level (0.0-2.0)
- `WithTopP(float32)` - Nucleus sampling (0.0-1.0)
- `WithTopK(int)` - Top-k sampling
- `WithIntelligence(core.Speed)` - Model intelligence level

## Use Cases

### Text Completion
```go
result, err := ops.Complete("The weather today is",
    ops.NewCompleteOptions().WithMaxLength(50))
```

### Context-Aware Chat
```go
result, err := ops.Complete("I think we should",
    ops.NewCompleteOptions().
        WithContext(previousMessages).
        WithMaxLength(100))
```

### Code Completion
```go
result, err := ops.Complete("function processData(data) {",
    ops.NewCompleteOptions().
        WithStopSequences([]string{"}", "\n\n"}).
        WithTemperature(0.3))
```

## Temperature Guidelines

- **0.0-0.3**: Conservative, predictable completions
- **0.4-0.7**: Balanced, natural completions
- **0.8-1.2**: Creative, varied completions
- **1.3-2.0**: Highly creative, unpredictable completions

## Error Handling

The operation validates:
- Non-empty input text
- Positive MaxLength
- Valid Temperature (0.0-2.0)
- Valid TopP (0.0-1.0)
- Positive TopK

## Performance Notes

- Context messages improve completion quality but increase token usage
- Lower temperature = faster, more predictable results
- Stop sequences prevent over-generation
- MaxLength prevents runaway completions