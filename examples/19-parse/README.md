# Parse Operation Example

This example demonstrates the SchemaFlux Parse operation, which intelligently parses data from various formats into strongly-typed Go structs.

## Overview

The Parse operation uses a hybrid approach:
- **Traditional algorithms** for standard formats (JSON, XML, CSV, YAML)
- **LLM fallback** for malformed data recovery and custom format parsing

## Key Features Demonstrated

1. **Standard Format Parsing**: JSON, XML, YAML, CSV
2. **Custom Delimited Data**: Pipe-delimited, tab-separated, custom delimiters
3. **Format Hints**: Guide parsing with field mapping hints
4. **Type Conversion**: Automatic conversion to appropriate Go types
5. **Mixed Format Handling**: Parse data containing multiple formats
6. **Error Handling**: Graceful handling of malformed data

## Running the Example

```bash
cd examples/19-parse
go run main.go
```

## Expected Output

The example will parse various data formats and display the results:

```
=== SchemaFlux Parse Operation Examples ===

1. Parsing Standard JSON:
   Result: {Name:Alice Age:28 Job:Engineer} (Format: json)

2. Parsing XML:
   Result: {Name:Bob Age:35 Job:Manager} (Format: xml)

3. Parsing YAML:
   Result: {Name:Charlie Age:42 Job:Director} (Format: yaml)

4. Parsing CSV:
   Result: 3 employees parsed (Format: csv)
     Employee 1: {Name:John Age:30 Salary:75000 Active:true}
     ...

5. Parsing Pipe-Delimited Data:
   Result: 2 employees parsed (Format: pipe-delimited)
     ...

6. Parsing with Format Hints:
   Result: {Name:Alice Age:29 Job:Senior Developer} (Format: pipe-delimited)

7. Parsing Mixed Format Data:
   Result: Database config parsed (Format: json)
     Database: "host=localhost\nport=5432\nuser=admin"
     ...

8. Parsing with Custom Delimiters:
   Result: {Name:Frank Age:45 Job:Architect} (Format: pipe-delimited)

9. Error Handling for Malformed Data:
   Expected error for malformed JSON: parsing failed: invalid character '}' after object key:value pair (consider enabling AllowLLMFallback)
   (This would succeed with AllowLLMFallback=true)

10. Type Conversion:
   Result: {Name:Helen Age:33 Salary:0 Active:true} (Format: csv)
   Types: Name=string, Age=int, Salary=float64, Active=bool

=== Parse Operation Examples Complete ===
```

## Use Cases

### Malformed Data Recovery
```go
// Enable LLM fallback for broken JSON/XML
result, err := ops.Parse[MyStruct](malformedData,
    ops.NewParseOptions().WithAllowLLMFallback(true).WithAutoFix(true))
```

### Custom Format Parsing
```go
// Parse proprietary formats with hints
result, err := ops.Parse[MyStruct](customData,
    ops.NewParseOptions().WithFormatHints([]string{"field1|field2|field3"}))
```

### Mixed Format Handling
```go
// Parse JSON containing other formats
type Config struct {
    Database string `json:"database"` // Contains connection string
    Settings map[string]string `json:"settings"`
}
```

## Configuration Options

- `AllowLLMFallback`: Enable LLM for complex parsing cases
- `AutoFix`: Attempt to fix malformed data
- `FormatHints`: Provide hints about expected formats
- `CustomDelimiters`: Specify custom field delimiters
- `Intelligence`: Set LLM intelligence level for fallback

## Error Handling

The Parse operation provides clear error messages:
- Algorithm failures suggest enabling `AllowLLMFallback`
- Type conversion errors specify problematic fields
- Format detection failures indicate unsupported formats

## Performance Notes

- Traditional parsing is fast and doesn't require API calls
- LLM fallback is used only when algorithms fail
- Format detection is optimized for common cases
- Type conversion handles standard Go types automatically