# Merge Example - Customer Record Deduplication

## What This Does

Demonstrates the **Merge** operation: intelligently combining multiple records into a unified result.

This example merges:
- **Input**: 3 customer records from different systems (CRM, Sales, Support)
- **Strategy**: Complex merge rules (most complete name, combine notes, upgrade VIP status)
- **Output**: Single unified customer record

## Use Case

**Real-World Applications**:
- Customer data deduplication
- Multi-source data consolidation
- Master data management
- Contact merging
- Profile unification
- System migration data cleanup

## How It Works

```go
merged, err := ops.Merge(records, strategy)
```

The LLM intelligently:
1. Identifies corresponding fields across records
2. Selects the most complete/accurate values
3. Combines complementary information
4. Resolves conflicts using strategy rules
5. Produces a unified result

## Running the Example

```bash
# Set your API key
export OPENAI_API_KEY='your-key-here'

# Run the example
go run main.go
```

## Expected Output

```
🔀 Merge Example - Customer Record Deduplication
============================================================

📋 Source Records:

1. Record CRM-001:
   Name:    John Smith
   Email:   john.smith@example.com
   Phone:   +1-555-0123
   Address: (empty)
   VIP:     false
   Notes:   Prefers email contact

2. Record SALES-456:
   Name:    J. Smith
   Email:   (empty)
   Phone:   +1-555-0123
   Address: 123 Main St, Springfield, IL 62701
   VIP:     true

3. Record SUPPORT-789:
   Name:    John A. Smith
   Email:   john.smith@example.com
   Phone:   (empty)
   Address: 123 Main Street, Springfield, Illinois
   VIP:     false
   Notes:   Has premium support plan

🔄 Merging records...

✅ Merged Result:
   Name:    John A. Smith
   Email:   john.smith@example.com
   Phone:   +1-555-0123
   Address: 123 Main Street, Springfield, Illinois
   VIP:     true
   Notes:   Prefers email contact. Has premium support plan

📊 Merge Analysis:
   Input: 3 duplicate records
   Output: 1 unified record

   ✓ Name: Selected most complete variant
   ✓ Email: Preserved from CRM
   ✓ Phone: Common across records
   ✓ Address: Used most detailed version
   ✓ VIP: Upgraded to true
   ✓ Notes: Combined all information

✨ Success! Customer records merged
```

## Key Features Demonstrated

- ✅ **Smart Field Selection**: Chooses best value for each field
- ✅ **Information Preservation**: Combines non-conflicting data
- ✅ **Conflict Resolution**: Follows strategy rules
- ✅ **Data Enrichment**: Fills missing fields from other sources

## Merge Strategies

The operation supports various strategies:
- **Most Complete**: Prefer records with more filled fields
- **Most Recent**: Use timestamps to prefer newer data
- **Priority Source**: Trust certain systems more
- **Custom Rules**: Define field-specific logic
- **Consensus**: Choose values that appear in multiple records

## Learn More

- [SchemaFlux Documentation](../../README.md)
- [Merge API Reference](../../docs/reference/API.md#merge)
