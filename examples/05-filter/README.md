# Filter Example - Urgent Ticket Triage

## What This Does

Demonstrates the **Filter** operation: selecting items from a list based on natural language criteria.

This example filters:
- **Input**: Array of support tickets
- **Criteria**: Business impact, security concerns, service outages
- **Output**: Only urgent tickets requiring immediate attention

## Use Case

**Real-World Applications**:
- Support ticket triage and prioritization
- Email filtering and categorization
- Content moderation
- Lead qualification
- Inventory management (find low stock items)

## How It Works

```go
urgentTickets, err := schemaflux.Filter(
    tickets,
    ops.NewFilterOptions().
        WithCriteria([]string{
            "Affects multiple users",
            "Security-related",
            "Service outages",
        }),
)
```

The LLM intelligently:
1. Evaluates each ticket against criteria
2. Understands business impact
3. Identifies security concerns
4. Recognizes service disruptions
5. Filters out routine requests

## Running the Example

```bash
# Set your API key
export OPENAI_API_KEY='your-key-here'

# Run the example
go run main.go
```

## Expected Output

```
🎫 Filter Example - Urgent Ticket Triage
==================================================

📥 Total Tickets: 6

All Tickets:
  #1 - Alice Johnson: Website is completely down
  #2 - Bob Smith: How to change password?
  #3 - Carol White: Payment processing failure
  #4 - David Brown: Feature request: dark mode
  #5 - Eve Davis: Data breach suspected
  #6 - Frank Miller: Invoice copy request

🚨 URGENT Tickets (require immediate attention):
---

⚠️  Ticket #1 - Alice Johnson
   Subject: Website is completely down
   Issue: Our entire e-commerce website has been offline...

⚠️  Ticket #3 - Carol White
   Subject: Payment processing failure
   Issue: Customer payments are failing...

⚠️  Ticket #5 - Eve Davis
   Subject: Data breach suspected
   Issue: We detected unusual activity...

✨ Success! Filtered 3 urgent tickets from 6 total
```

## Key Features Demonstrated

- ✅ **Intelligent Filtering**: Understands business context
- ✅ **Multi-Criteria**: Combines multiple filter rules
- ✅ **Semantic Understanding**: Not just keyword matching
- ✅ **Priority Detection**: Identifies critical issues

## Use Cases

1. **Customer Support**: Auto-triage tickets
2. **Email Management**: Filter important emails
3. **Content Moderation**: Flag inappropriate content
4. **Sales**: Qualify leads by potential
5. **Security**: Detect anomalies and threats

## Learn More

- [SchemaFlux Documentation](../../README.md)
- [Filter API Reference](../../docs/reference/API.md#filter)
