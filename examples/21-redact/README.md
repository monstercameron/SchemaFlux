# SchemaFlux Redact Operation Examples

The Redact operation provides intelligent data masking capabilities for removing or obscuring sensitive information from text and structured data.

## Features

- **Multiple Redaction Strategies**: Mask, nil, remove, or jumble/scramble sensitive data
- **Pattern-Based Detection**: Uses regex patterns to identify PII, secrets, and financial data
- **Type-Aware Processing**: Handles strings, structs, maps, and slices
- **Field-Level Control**: Uses struct tags and field names for targeted redaction
- **Immutable Operations**: Returns new objects, preserving originals

## Redaction Strategies

### 1. Mask (`RedactMask`)
Replaces sensitive data with a mask string (default: `***`)

```go
result, _ := ops.Redact("email: user@domain.com",
    ops.NewRedactOptions().WithStrategy(ops.RedactMask))
fmt.Println(result) // "email: ***"
```

### 2. Nil (`RedactNil`)
Replaces sensitive data with empty/null values

```go
result, _ := ops.Redact("phone: 555-123-4567",
    ops.NewRedactOptions().WithStrategy(ops.RedactNil))
fmt.Println(result) // "phone: "
```

### 3. Remove (`RedactRemove`)
Completely removes sensitive data (same as nil for text)

### 4. Jumble/Scramble (`RedactJumble`)
Scrambles characters while preserving length and format

```go
result, _ := ops.Redact("name@example.com",
    ops.NewRedactOptions().WithStrategy(ops.RedactJumble))
fmt.Println(result) // "aemn@aelpmxe.com" (scrambled but @ preserved)
```

## Detection Categories

### PII (Personally Identifiable Information)
- Email addresses
- Phone numbers
- Social Security Numbers
- Names
- Credit card numbers

### Secrets
- Passwords and tokens
- API keys
- Bearer tokens
- Database credentials

### Financial
- Credit card numbers
- Bank routing numbers
- Currency amounts

## Usage Examples

### Text Redaction
```go
text := "Contact john@example.com or call 555-123-4567"
redacted, _ := ops.Redact(text,
    ops.NewRedactOptions().WithCategories([]string{"PII"}))
// Result: "Contact *** or call ***"
```

### Struct Redaction
```go
type User struct {
    Name  string `redact:"PII"`
    Email string
    Age   int
}

user := User{Name: "John", Email: "john@example.com", Age: 30}
safeUser, _ := ops.Redact(user,
    ops.NewRedactOptions().WithCategories([]string{"PII"}))
// safeUser.Name == "***", safeUser.Email == "***", safeUser.Age == 30
```

### Field Name Detection
```go
type Config struct {
    DatabasePassword string  // Detected by "Password" in name
    APIToken         string  // Detected by "Token" in name
    NormalField      string  // Not redacted
}

config := Config{DatabasePassword: "secret", APIToken: "token123"}
safeConfig, _ := ops.Redact(config,
    ops.NewRedactOptions().WithCategories([]string{"secrets"}))
```

### Map Redaction
```go
data := map[string]interface{}{
    "email": "user@domain.com",
    "ssn": "123-45-6789",
    "safe": "normal data",
}
safeData, _ := ops.Redact(data,
    ops.NewRedactOptions().WithCategories([]string{"PII"}))
```

## Jumble Modes

### Basic (`JumbleBasic`)
Simple character scrambling - all characters randomized

### Smart (`JumbleSmart`)
Preserves some phonetic structure (future enhancement)

### Type-Aware (`JumbleTypeAware`)
Uses data type-specific scrambling rules:
- **Emails**: Scrambles username, preserves @ and domain
- **Phones**: Scrambles digits, preserves formatting
- **Names**: Scrambles within words
- **Other**: Falls back to basic scrambling

## Mask Configuration

### Custom Mask Characters and Length
Control the appearance of masked data with configurable characters and lengths:

```go
// Default: "***"
opts := ops.NewRedactOptions().WithCategories([]string{"PII"})

// Custom mask text
opts := ops.NewRedactOptions().
    WithCategories([]string{"PII"}).
    WithMaskText("[REDACTED]")

// Custom character and length
opts := ops.NewRedactOptions().
    WithCategories([]string{"PII"}).
    WithMaskChar('#').
    WithMaskLength(5)  // "#####"

// Use original data length
opts := ops.NewRedactOptions().
    WithCategories([]string{"PII"}).
    WithMaskChar('X').
    WithMaskLength(-1)  // Match original length
```

### Mask Text Priority
1. **MaskText** (highest priority) - Uses exact string if provided
2. **MaskChar + MaskLength** - Generates mask from character and length
3. **Default** - "***" (3 asterisks)

## Best Practices

1. **Choose Appropriate Strategies**:
   - Use `Mask` for general obfuscation
   - Use `Jumble` for testing with realistic data
   - Use `Nil` when data should be completely removed

2. **Combine Categories**:
   ```go
   opts.WithCategories([]string{"PII", "secrets", "financial"})
   ```

3. **Use Struct Tags for Precision**:
   ```go
   type User struct {
       Name  string `redact:"PII"`    // Explicitly mark for redaction
       ID    int                      // Not redacted
   }
   ```

4. **Test with Different Strategies**:
   - Ensure redaction doesn't break application logic
   - Verify that redacted data maintains required structure

5. **Handle Errors**:
   ```go
   result, err := ops.Redact(data, opts)
   if err != nil {
       // Handle validation errors
   }
   ```

## Performance Notes

- Pattern matching is performed on string content
- Struct reflection adds minimal overhead
- Jumbling operations are fast (O(n) character operations)
- Large datasets are processed efficiently

## Security Considerations

- Original data is never modified - only copies are redacted
- No sensitive data is logged or cached
- Patterns are applied client-side only
- Use appropriate strategies for your compliance requirements