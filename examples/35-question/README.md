# Question Example

This example demonstrates the `Question` operation, which allows you to ask natural language questions about your data and receive typed answers.

## Features

- **Type-Safe Answers**: Get answers in the exact type you specify (string, bool, struct, etc.)
- **Confidence Scoring**: Know how confident the model is in its answer
- **Reasoning**: Understand how the answer was derived
- **Evidence**: See supporting facts from the data

## Usage

```go
// Simple string answer
result, err := schemaflux.Question[MyData, string](data, 
    schemaflux.NewQuestionOptions("What is the main finding?"))
fmt.Println(result.Answer, result.Confidence)

// Boolean answer
result, err := schemaflux.Question[MyData, bool](data,
    schemaflux.NewQuestionOptions("Is the data valid?"))
if result.Answer {
    fmt.Println("Data is valid")
}

// Typed struct answer
type Findings struct {
    TopItem    string `json:"top_item"`
    Summary    string `json:"summary"`
    ActionItem string `json:"action_item"`
}
result, err := schemaflux.Question[Report, Findings](report,
    schemaflux.NewQuestionOptions("What are the key findings?"))
fmt.Println(result.Answer.TopItem)

// Legacy interface for quick string answers
answer, err := schemaflux.QuestionLegacy(data, "What is the answer?")
```

## Options

- `WithIncludeEvidence(bool)` - Include supporting evidence from the data
- `WithIncludeConfidence(bool)` - Include confidence score
- `WithIncludeReasoning(bool)` - Include reasoning explanation
- `WithSteering(string)` - Provide additional guidance
- `WithMode(mode)` - Set the reasoning mode
- `WithIntelligence(speed)` - Set the model quality/speed

## Running the Example

```bash
export SCHEMAFLUX_API_KEY=your-api-key
cd examples/35-question
go run main.go
```
