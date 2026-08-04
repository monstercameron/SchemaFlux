# SchemaFlux Suggest Operation Example

This example demonstrates the **Suggest** operation, which generates context-aware suggestions to optimize workflows, solve problems, and guide decision-making using LLM intelligence.

## Overview

The Suggest operation analyzes input data and current context to provide intelligent recommendations. It's designed to help with:

- **Workflow Optimization**: Suggest next steps in complex processes
- **Problem Solving**: Generate solutions for issues and challenges
- **Decision Support**: Provide ranked options with reasoning
- **Configuration Guidance**: Recommend optimal settings and parameters

## Key Features

### Type-Safe Generic API
```go
// Suggest strings
suggestions, err := ops.Suggest[string](context, options)

// Suggest custom types
suggestions, err := ops.Suggest[Action](workflowState, options)
```

### Flexible Configuration
```go
opts := ops.NewSuggestOptions().
    WithStrategy(ops.SuggestContextual).  // Strategy: contextual, pattern, goal, hybrid
    WithTopN(5).                          // Limit number of suggestions
    WithRanked(true).                     // Rank by relevance
    WithDomain("data-processing").        // Domain context
    WithConstraints([]string{"efficient", "scalable"}). // Specific requirements
    WithCategories([]string{"optimization"}).           // Suggestion categories
    WithIncludeReasons(true)               // Include reasoning
```

### Strategies

- **`SuggestContextual`**: Analyzes current state and context
- **`SuggestPattern`**: Identifies patterns and recommends based on similar scenarios
- **`SuggestGoal`**: Focuses on achieving stated objectives
- **`SuggestHybrid`**: Combines multiple approaches for comprehensive suggestions

## Usage Examples

### 1. Data Processing Workflow
```go
currentState := map[string]any{
    "task": "ETL optimization",
    "issues": []string{"slow processing", "memory usage"},
}

suggestions, err := ops.Suggest[string](currentState,
    ops.NewSuggestOptions().WithDomain("data-engineering"))
```

### 2. API Design Guidance
```go
apiContext := map[string]any{
    "resource": "user profiles",
    "operations": []string{"create", "read", "update", "delete"},
}

suggestions, err := ops.Suggest[string](apiContext,
    ops.NewSuggestOptions().WithStrategy(ops.SuggestPattern))
```

### 3. Configuration Optimization
```go
configContext := map[string]any{
    "system": "web application",
    "issues": []string{"high latency", "memory leaks"},
}

suggestions, err := ops.Suggest[string](configContext,
    ops.NewSuggestOptions().WithStrategy(ops.SuggestHybrid))
```

### 4. Custom Types
```go
type Action struct {
    Name        string `json:"name"`
    Description string `json:"description"`
    Priority    string `json:"priority"`
}

workflowContext := map[string]any{
    "phase": "data validation",
    "issues": []string{"inconsistent formats"},
}

actions, err := ops.Suggest[Action](workflowContext, options)
```

## Running the Example

```bash
cd examples/22-suggest
go run main.go
```

## Expected Output

The example demonstrates 5 different suggestion scenarios:

1. **Data Processing Workflow**: ETL pipeline optimization suggestions
2. **API Design**: RESTful endpoint recommendations
3. **Configuration**: System optimization advice
4. **Custom Types**: Structured action recommendations
5. **Error Recovery**: Solutions for system issues

## Business Value

### Decision Support
- Reduces cognitive load in complex decision-making
- Provides expert-level recommendations instantly
- Ensures consistency across team decisions

### Productivity Enhancement
- Accelerates problem-solving workflows
- Suggests optimizations proactively
- Guides users through best practices

### Quality Assurance
- Standardized recommendation patterns
- Context-aware suggestions prevent common mistakes
- Domain-specific expertise built into suggestions

### Autonomous Systems
- Foundation for self-optimizing workflows
- Enables AI-assisted automation
- Supports intelligent agent behaviors

## Configuration Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `Strategy` | `SuggestStrategy` | `SuggestContextual` | Suggestion generation approach |
| `TopN` | `int` | `5` | Maximum suggestions to return |
| `Ranked` | `bool` | `true` | Rank suggestions by relevance |
| `IncludeScores` | `bool` | `false` | Include confidence scores |
| `IncludeReasons` | `bool` | `false` | Include reasoning for suggestions |
| `Domain` | `string` | `""` | Domain context (e.g., "data-engineering") |
| `Constraints` | `[]string` | `nil` | Specific requirements/constraints |
| `Categories` | `[]string` | `nil` | Suggestion categories to focus on |

## Error Handling

The Suggest operation gracefully handles:
- Invalid options (validation errors)
- LLM unavailability (falls back to basic suggestions)
- Malformed responses (parsing fallbacks)
- Context timeouts (configurable via core options)

## Integration

The Suggest operation integrates seamlessly with other SchemaFlux operations:

```go
// Use suggestions to guide transformations
suggestions := Suggest[TransformStep](data, opts)
for _, step := range suggestions {
    result, err := Transform[Output](data, step.Config)
    // Apply suggested transformation
}
```

This operation transforms SchemaFlux from a **data processing toolkit** into a **decision intelligence platform** that actively guides users toward optimal outcomes.