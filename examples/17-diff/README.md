# 17-diff: Intelligent Difference Detection

This example demonstrates the `Diff[T]` operation, which intelligently compares two data instances and explains what changed between them.

## Overview

The Diff operation provides:
- **Structural Analysis**: Detects added, removed, and modified fields
- **Type-Aware Comparison**: Handles different data types appropriately
- **Intelligent Summaries**: Uses LLM to explain the significance of changes
- **Configurable Options**: Control comparison depth and ignore specific fields

## Use Cases

### 1. Customer Data Auditing
Track changes in customer records for compliance and fraud detection.

### 2. Product Catalog Management
Monitor product information updates and pricing changes.

### 3. Configuration Change Tracking
Audit system configuration changes while ignoring timestamps.

### 4. API Response Comparison
Detect breaking changes between API versions.

## Running the Example

```bash
cd examples/17-diff
go run main.go
```

Make sure you have a `.env` file with your OpenAI API key:

```env
SCHEMAFLUX_API_KEY=your_openai_api_key_here
```

## Example Output

The example shows three scenarios:

1. **Customer Record Changes**: Name refinement, email update, status change, and phone addition
2. **Product Catalog Changes**: Price increase, description enhancement, stock status change, and new tags
3. **Configuration Changes**: Version bump, port change, debug mode toggle (ignoring timestamps)

Each comparison includes:
- Detailed field-by-field changes
- LLM-generated summary explaining the significance
- Context-aware analysis

## Key Features Demonstrated

- **Type Safety**: Generic `Diff[T]` ensures compile-time type checking
- **Flexible Options**: Context provision, field ignoring, deep comparison control
- **Intelligent Analysis**: LLM provides business context for changes
- **Multiple Data Types**: Handles structs, primitives, slices, and complex nested data

## Options

- `WithContext(string)`: Provide domain context for better analysis
- `WithIgnoreFields([]string)`: Skip comparison of specified fields
- `WithDeepCompare(bool)`: Enable recursive struct comparison
- `WithIntelligence(Speed)`: Control analysis depth (Fast, Balanced, Thorough)