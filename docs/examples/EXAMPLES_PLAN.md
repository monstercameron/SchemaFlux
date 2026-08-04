# SchemaFlux Examples Plan

## Goal
Create simple, focused examples that demonstrate **ALL** SchemaFlux LLM operations.
Each example should be:
- **Self-contained** - runs independently
- **Simple** - < 100 lines of code
- **Clear** - demonstrates one concept well
- **Practical** - solves a real use case

## Core Operations (Primary)

### 1. Extract Example (`01-extract/`)
**Purpose**: Convert unstructured text into structured data
**File**: `ops/core.go` - `Extract[T any]()`

**Use Case**: Parse a plain text email into structured fields
- Input: Raw email text
- Output: Email struct (from, to, subject, date, body)
- Demonstrates: Basic extraction with type inference

---

### 2. Transform Example (`02-transform/`)
**Purpose**: Convert data from one type to another
**File**: `ops/core.go` - `Transform[T any, U any]()`

**Use Case**: Convert JSON resume to Markdown CV
- Input: Resume struct (JSON format)
- Output: Professional markdown CV
- Demonstrates: Structured transformation with formatting

---

### 3. Generate Example (`03-generate/`)
**Purpose**: Create structured data from natural language prompts
**File**: `ops/core.go` - `Generate[T any]()`

**Use Case**: Generate test data for an API
- Input: Data schema description
- Output: Array of realistic test users
- Demonstrates: Content generation with constraints

---

## Collection Operations

### 4. Choose Example (`04-choose/`)
**Purpose**: Select best option from a list
**File**: `ops/collection.go` - `Choose[T any]()`

**Use Case**: Pick the most relevant product for a customer query
- Input: Customer question + product catalog
- Output: Best matching product with reasoning
- Demonstrates: Intelligent selection with criteria

---

### 5. Filter Example (`05-filter/`)
**Purpose**: Filter items based on natural language criteria
**File**: `ops/collection.go` - `Filter[T any]()`

**Use Case**: Filter customer support tickets by urgency
- Input: Array of support tickets
- Output: Filtered urgent tickets only
- Demonstrates: Natural language filtering

---

### 6. Sort Example (`06-sort/`)
**Purpose**: Sort items using natural language criteria
**File**: `ops/collection.go` - `Sort[T any]()`

**Use Case**: Prioritize tasks by importance and urgency
- Input: Array of tasks
- Output: Sorted tasks (most important first)
- Demonstrates: Intelligent prioritization

---

## Text Operations

### 7. Summarize Example (`07-summarize/`)
**Purpose**: Condense text while preserving key information
**File**: `ops/text.go` - `Summarize()`

**Use Case**: Summarize long article for newsletter
- Input: Long article text
- Output: Concise summary (3-5 sentences)
- Demonstrates: Text compression with quality

---

## Analysis Operations

### 8. Classify Example (`08-classify/`)
**Purpose**: Categorize text into predefined categories
**File**: `ops/analysis.go` - `Classify()`

**Use Case**: Classify customer feedback sentiment
- Input: Customer review text
- Categories: ["positive", "negative", "neutral"]
- Output: Category + confidence
- Demonstrates: Text classification

---

### 9. Score Example (`09-score/`)
**Purpose**: Rate content based on specified criteria
**File**: `ops/analysis.go` - `Score()`

**Use Case**: Score code quality from 1-10
- Input: Code snippet
- Criteria: ["readability", "performance", "maintainability"]
- Output: Numeric score (1-10)
- Demonstrates: Numeric scoring

---

### 10. Compare Example (`10-compare/`)
**Purpose**: Analyze similarities and differences
**File**: `ops/analysis.go` - `Compare()`

**Use Case**: Compare two product descriptions
- Input: Product A description, Product B description
- Output: Structured comparison (similarities & differences)
- Demonstrates: Comparative analysis

---

### 11. Similar Example (`11-similar/`)
**Purpose**: Check semantic similarity between items
**File**: `ops/analysis.go` - `Similar()`

**Use Case**: Detect duplicate support tickets
- Input: Two ticket descriptions
- Output: Boolean (similar or not) + confidence
- Demonstrates: Similarity detection

---

## Extended Operations

### 12. Validate Example (`12-validate/`)
**Purpose**: Check if data meets specified criteria
**File**: `ops/extended.go` - `Validate[T any]()`

**Use Case**: Validate user registration data
- Input: User struct
- Rules: "age must be 18-100, email must be valid"
- Output: ValidationResult (valid/invalid + issues)
- Demonstrates: Data validation

---

### 13. Merge Example (`13-merge/`)
**Purpose**: Intelligently combine multiple data sources
**File**: `ops/extended.go` - `Merge[T any]()`

**Use Case**: Merge duplicate customer records
- Input: Array of customer records
- Strategy: "prefer most recent non-null values"
- Output: Single merged record
- Demonstrates: Data deduplication

---

## Procedural Operations

### 14. Decide Example (`14-decide/`)
**Purpose**: Make decisions based on context
**File**: `ops/procedural.go` - `Decide[T any]()`

**Use Case**: Route support ticket to correct department
- Input: Ticket description
- Decisions: ["technical", "billing", "sales"]
- Output: Best department + reasoning
- Demonstrates: Context-based routing

---

### 15. Guard Example (`15-guard/`)
**Purpose**: Validate state transitions
**File**: `ops/procedural.go` - `Guard[T any]()`

**Use Case**: Check if order can be processed
- Input: Order state
- Checks: Inventory available, payment valid
- Output: GuardResult (pass/fail + suggestions)
- Demonstrates: Business rule validation

---

## Directory Structure
```
examples/
├── EXAMPLES_PLAN.md (this file)
│
├── Core Operations/
│   ├── 01-extract/
│   ├── 02-transform/
│   └── 03-generate/
│
├── Collection Operations/
│   ├── 04-choose/
│   ├── 05-filter/
│   └── 06-sort/
│
├── Text Operations/
│   └── 07-summarize/
│
├── Analysis Operations/
│   ├── 08-classify/
│   ├── 09-score/
│   ├── 10-compare/
│   └── 11-similar/
│
├── Extended Operations/
│   ├── 12-validate/
│   └── 13-merge/
│
├── Procedural Operations/
│   ├── 14-decide/
│   └── 15-guard/
│
└── Advanced Example/
    └── smarttodo/ (existing TUI app)
```

## Common File Structure

Each example folder contains:
- `main.go` - The example code
- `README.md` - Documentation with use case and instructions
- `*.json` / `*.txt` - Sample input data (if needed)

## Common Code Pattern

Each example follows this structure:

```go
package main

import (
    "fmt"
    "log"
    
    "github.com/monstercameron/schemaflux"
)

func main() {
    // 1. Initialize SchemaFlux
    if err := schemaflux.InitWithEnv(); err != nil {
        log.Fatal(err)
    }
    
    // 2. Prepare input data
    // ...
    
    // 3. Call ONE SchemaFlux operation
    result, err := schemaflux.OperationName(input, opts)
    if err != nil {
        log.Fatal(err)
    }
    
    // 4. Display results
    fmt.Printf("Result: %+v\n", result)
}
```

## Implementation Priority

**Phase 1 - Core Operations** (Most Important)
1. Extract
2. Transform  
3. Generate

**Phase 2 - Collection Operations**
4. Choose
5. Filter
6. Sort

**Phase 3 - Analysis Operations**
7. Summarize
8. Classify
9. Score
10. Compare
11. Similar

**Phase 4 - Extended Operations**
12. Validate
13. Merge

**Phase 5 - Procedural Operations**
14. Decide
15. Guard

## Summary

**Total Operations**: 15 LLM-powered operations
**Total Examples**: 15 simple + 1 complex (SmartTodo)

Each example demonstrates:
- ✅ One operation clearly
- ✅ Practical real-world use case
- ✅ Simple, copy-pasteable code
- ✅ Clear documentation

