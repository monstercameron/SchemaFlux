# Generate Example - Test Data Generator

## What This Does

Demonstrates the **Generate** operation: creating structured data from natural language prompts.

This example generates:
- **Input**: Natural language description of desired data
- **Output**: Array of realistic test users (structured data)

## Use Case

**Real-World Application**: Generate test data for:
- API development and testing
- Database seeding
- UI prototyping
- Load testing
- Demo environments

## How It Works

```go
batch, err := schemaflux.Generate[TestUserBatch](
    "Generate 5 realistic test users...",
    ops.NewGenerateOptions(),
)
```

The LLM intelligently:
1. Understands the data requirements
2. Creates diverse, realistic data
3. Follows the struct schema
4. Ensures data consistency
5. Applies constraints (age ranges, date formats, etc.)

## Running the Example

```bash
# Set your API key
export OPENAI_API_KEY='your-key-here'

# Run the example
go run main.go
```

## Expected Output

```
🧪 Generate Example - Test Data Generator
==================================================

📝 Prompt:
Generate 5 realistic test users for a social media application...

✅ Generated Test Users:
---

👤 User 1:
   ID:        1
   Name:      Akira Tanaka
   Email:     akira.t@example.jp
   Age:       28
   Country:   Japan
   Role:      user
   Join Date: 2023-05-12
   Active:    true

👤 User 2:
   ...

📦 JSON Output (ready for API):
{
  "users": [...],
  "count": 5
}

✨ Success! Generated 5 realistic test users
```

## Key Features Demonstrated

- ✅ **Data Generation**: From text prompt to structured data
- ✅ **Schema Compliance**: Follows Go struct definition
- ✅ **Realistic Data**: Culturally diverse, logical values
- ✅ **Ready to Use**: JSON output for immediate API consumption

## Use Cases

1. **API Testing**: Generate test payloads
2. **Database Seeding**: Populate dev/staging databases
3. **UI Mockups**: Create demo data for presentations
4. **Load Testing**: Generate large datasets
5. **Documentation**: Create example data for API docs

## Learn More

- [SchemaFlux Documentation](../../README.md)
- [Generate API Reference](../../docs/reference/API.md#generate)
