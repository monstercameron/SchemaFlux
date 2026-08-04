# Sort Example - Task Prioritization

## What This Does

Demonstrates the **Sort** operation: ordering items using natural language criteria and semantic understanding.

This example sorts:
- **Input**: Array of tasks with deadlines, impact, and effort
- **Criteria**: Urgency, business impact, effort (Eisenhower Matrix-style)
- **Output**: Tasks ordered by intelligent priority

## Use Case

**Real-World Applications**:
- Task management and prioritization
- Issue tracking systems
- Content curation (by relevance/quality)
- Product backlogs
- Search result ranking

## How It Works

```go
sortedTasks, err := schemaflux.Sort(
    tasks,
    ops.NewSortOptions().
        WithCriteria("Priority by urgency, impact, and effort"),
)
```

The LLM intelligently:
1. Evaluates multiple factors simultaneously
2. Applies prioritization frameworks
3. Balances competing criteria
4. Identifies quick wins
5. Considers deadlines and impact

## Running the Example

```bash
# Set your API key
export OPENAI_API_KEY='your-key-here'

# Run the example
go run main.go
```

## Expected Output

```
📋 Sort Example - Task Prioritization
==================================================

📥 Unsorted Tasks:
  1. Update documentation (Deadline: Next week)
  2. Fix critical security vulnerability (Deadline: Today)
  3. Add dark mode feature (Deadline: Next month)
  4. Database backup failing (Deadline: ASAP)
  5. Refactor payment module (Deadline: Q2 2025)

✅ Prioritized Tasks (Highest Priority First):
---

1. 🎯 Fix critical security vulnerability
   Deadline: Today
   Impact:   Critical - affects all users, security risk
   Effort:   4 hours
   Why:      Critical security issue with immediate deadline

2. 🎯 Database backup failing
   Deadline: ASAP
   Impact:   High - data loss risk
   Effort:   1 hour
   Why:      High impact with minimal effort - quick win

3. 🎯 Update documentation
   Deadline: Next week
   Impact:   Low - only affects developers
   Effort:   2 hours
   Why:      Quick task before moving to longer projects

✨ Success! Tasks intelligently prioritized
```

## Key Features Demonstrated

- ✅ **Multi-Factor Sorting**: Considers urgency, impact, effort
- ✅ **Context-Aware**: Understands business priorities
- ✅ **Quick Wins**: Identifies high-value, low-effort tasks
- ✅ **Semantic Understanding**: Not just numerical sorting

## Use Cases

1. **Project Management**: Prioritize backlog items
2. **Support Systems**: Order tickets by importance
3. **Content**: Rank articles by quality/relevance
4. **E-commerce**: Sort products by customer fit
5. **Hiring**: Rank candidates by suitability

## Learn More

- [SchemaFlux Documentation](../../README.md)
- [Sort API Reference](../../docs/reference/API.md#sort)
