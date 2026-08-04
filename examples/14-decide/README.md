# Decide Example - Support Ticket Routing

## What This Does

Demonstrates the **Decide** operation: intelligently choosing the best option from multiple choices based on context.

This example routes:
- **Input**: Support tickets with various issues
- **Options**: 4 departments with different specialties
- **Output**: Best department with confidence and reasoning

## Use Case

**Real-World Applications**:
- Ticket routing to departments/agents
- Lead assignment to sales reps
- Task assignment to team members
- Service selection based on requirements
- Resource allocation
- Escalation path determination

## How It Works

```go
chosen, result, err := ops.Decide(ticket, departments)

fmt.Printf("Route to: %s\n", chosen.Name)
fmt.Printf("Confidence: %.0f%%\n", result.Confidence*100)
fmt.Printf("Reasoning: %s\n", result.Explanation)
```

The LLM intelligently:
1. Analyzes the context (ticket details)
2. Evaluates each option (department capabilities)
3. Considers multiple factors (priority, category, customer)
4. Provides confidence score
5. Explains the reasoning

## Running the Example

```bash
# Set your API key
export OPENAI_API_KEY='your-key-here'

# Run the example
go run main.go
```

## Expected Output

```
🎯 Decide Example - Support Ticket Routing
============================================================

1. Ticket #101: Application crashes on startup
---
   Description: After the latest update, the app crashes immediately...
   Priority: critical
   Customer: Enterprise Corp

   🔄 Routing ticket...

   ✅ Route to: Technical Support
   Confidence: 95%
   Reasoning: Critical bug report requiring immediate technical investigation

2. Ticket #102: Need help with advanced features
---
   Description: We'd like training on how to use the analytics dashboard...
   Priority: medium
   Customer: Small Business Inc

   🔄 Routing ticket...

   ✅ Route to: Customer Success
   Confidence: 88%
   Reasoning: Training and onboarding request, perfect for Customer Success team

3. Ticket #103: Invoice shows wrong amount
---
   Description: Our latest invoice has an incorrect charge...
   Priority: high
   Customer: ABC Company

   🔄 Routing ticket...

   ✅ Route to: Billing Support
   Confidence: 98%
   Reasoning: Billing inquiry requiring invoice review and correction

📊 Routing Summary:
   Total tickets: 3
   Technical Support: 1
   Billing Support: 1
   Customer Success: 1

✨ Success! Tickets routed intelligently
```

## Key Features Demonstrated

- ✅ **Context-Aware Decisions**: Considers full ticket context
- ✅ **Confidence Scoring**: Indicates decision certainty
- ✅ **Explainable AI**: Provides reasoning for each decision
- ✅ **Multi-Factor Analysis**: Priority, category, content all matter

## Advantages Over Rules-Based Routing

1. **Handles Ambiguity**: Works when rules aren't clear-cut
2. **Adapts to Context**: Considers nuances in ticket description
3. **No Complex Rule Trees**: Natural language decision logic
4. **Explainable**: Shows why each decision was made
5. **Flexible**: Easy to add new departments or criteria

## Learn More

- [SchemaFlux Documentation](../../README.md)
- [Decide API Reference](../../docs/reference/API.md#decide)
