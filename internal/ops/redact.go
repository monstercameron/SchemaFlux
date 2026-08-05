// package ops - Redact operation for intelligent data masking
package ops

import (
	"fmt"
	"math/rand"
	"reflect"
	"regexp"
	"strings"

	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/types"
)

// RedactResult contains detailed information about redaction operations
type RedactResult struct {
	Redacted map[string][]string `json:"redacted"` // category -> list of redacted values
	Count    int                 `json:"count"`    // total items redacted
	Metadata map[string]any      `json:"metadata"` // additional metadata
}

// RedactStrategy defines how sensitive data should be redacted
type RedactStrategy string

const (
	RedactNil      RedactStrategy = "nil"      // Replace with nil/null
	RedactMask     RedactStrategy = "mask"     // Replace with mask text
	RedactRemove   RedactStrategy = "remove"   // Remove property entirely
	RedactJumble   RedactStrategy = "jumble"   // Scramble characters
	RedactScramble RedactStrategy = "scramble" // Alias for jumble
)

// JumbleMode defines how jumbling/scrambling should work
type JumbleMode string

const (
	JumbleBasic     JumbleMode = "basic"     // Simple character scrambling
	JumbleSmart     JumbleMode = "smart"     // Preserve some structure
	JumbleTypeAware JumbleMode = "typeaware" // Data type-specific scrambling
)

// RedactOptions configures the Redact operation
type RedactOptions struct {
	types.OpOptions
	Categories     []string       // Sensitive data categories to redact
	Strategy       RedactStrategy // How to redact the data
	MaskText       string         // Custom mask text (overrides MaskChar/MaskLength)
	MaskChar       rune           // Character to use for masking (default: '*')
	MaskLength     int            // Length of mask (-1 = use original length, default: 3)
	JumbleSeed     int64          // Random seed for reproducible jumbling
	JumbleMode     JumbleMode     // How to perform jumbling
	PreserveFormat bool           // Keep special chars and formatting
	CustomPatterns []string       // Additional regex patterns to match
}

// NewRedactOptions creates RedactOptions with defaults
func NewRedactOptions() RedactOptions {
	return RedactOptions{
		OpOptions: types.OpOptions{
			Mode:         types.Strict,
			Intelligence: types.Smart,
		},
		Categories:     []string{"PII"},
		Strategy:       RedactMask,
		MaskText:       "",
		MaskChar:       '*',
		MaskLength:     3,
		JumbleMode:     JumbleTypeAware,
		PreserveFormat: true,
	}
}

// WithCategories sets the sensitive data categories to redact
func (opts RedactOptions) WithCategories(categories []string) RedactOptions {
	opts.Categories = categories
	return opts
}

// WithStrategy sets the redaction strategy
func (opts RedactOptions) WithStrategy(strategy RedactStrategy) RedactOptions {
	opts.Strategy = strategy
	return opts
}

// WithMaskText sets the text to use for masking
func (opts RedactOptions) WithMaskText(maskText string) RedactOptions {
	opts.MaskText = maskText
	return opts
}

// WithMaskChar sets the character to use for masking
func (opts RedactOptions) WithMaskChar(maskChar rune) RedactOptions {
	opts.MaskChar = maskChar
	return opts
}

// WithMaskLength sets the length of the mask (-1 to use original length)
func (opts RedactOptions) WithMaskLength(length int) RedactOptions {
	opts.MaskLength = length
	return opts
}

// WithJumbleSeed sets the random seed for reproducible jumbling
func (opts RedactOptions) WithJumbleSeed(seed int64) RedactOptions {
	opts.JumbleSeed = seed
	return opts
}

// WithJumbleMode sets how jumbling should work
func (opts RedactOptions) WithJumbleMode(mode JumbleMode) RedactOptions {
	opts.JumbleMode = mode
	return opts
}

// WithPreserveFormat sets whether to preserve formatting
func (opts RedactOptions) WithPreserveFormat(preserve bool) RedactOptions {
	opts.PreserveFormat = preserve
	return opts
}

// WithCustomPatterns adds custom regex patterns to match
func (opts RedactOptions) WithCustomPatterns(patterns []string) RedactOptions {
	opts.CustomPatterns = patterns
	return opts
}

// Validate checks if the options are valid
func (opts RedactOptions) Validate() error {
	if len(opts.Categories) == 0 {
		return fmt.Errorf("at least one category must be specified")
	}

	validStrategies := []RedactStrategy{RedactNil, RedactMask, RedactRemove, RedactJumble, RedactScramble}
	strategyValid := false
	for _, s := range validStrategies {
		if opts.Strategy == s {
			strategyValid = true
			break
		}
	}
	if !strategyValid {
		return fmt.Errorf("invalid strategy: %s", opts.Strategy)
	}

	return nil
}

// Redact removes or masks sensitive information from data.
// Returns a new object of the same type with sensitive data redacted.
//
// # NOT PRODUCTION READY
//
// Do not use this to remove sensitive data from anything that leaves your
// control. The known defects are recorded as T-07 through T-13 in
// docs/engineering/reviews/ADVERSARIAL_API_REVIEW.md, and the fixes are
// scheduled in TODOS.md under M05. In summary:
//
//   - Field matching is a substring test, so a field named "password_reset_url"
//     is redacted because it contains "pass", and a field named "taxpayer_id" is
//     not, because it does not contain "ssn" (T-07).
//   - The built-in patterns miss the formats they name: the SSN pattern does not
//     match an unhyphenated SSN, and the card pattern does not match a card
//     number written without separators (T-08).
//   - RedactWithResult returns an empty map and a nil error whatever it did, so
//     an audit of a redaction pass reads as "nothing was redacted" (T-09).
//   - Jumble is a character permutation with a seed derived from the input, so
//     it is reversible by anyone who wants to reverse it. It is obfuscation, not
//     redaction (T-11).
//   - RedactLLM applies character offsets the model reports without checking
//     them against the text, so an off-by-one shifts every subsequent span
//     (T-13).
//
// What it is usable for today: reducing the incidental visibility of fields in
// logs and demos, where a missed value is an annoyance rather than a breach.
func Redact[T any](input T, opts ...interface{}) (T, error) {
	log := logger.GetLogger()
	log.Debug("Starting redact operation", "requestID", "unknown", "inputType", fmt.Sprintf("%T", input))

	var options RedactOptions
	if len(opts) == 0 {
		options = NewRedactOptions()
	} else {
		switch opt := opts[0].(type) {
		case RedactOptions:
			options = opt
		case types.OpOptions:
			options = NewRedactOptions()
			options.OpOptions = opt
		default:
			options = NewRedactOptions()
		}
	}

	if err := options.Validate(); err != nil {
		log.Error("Redact operation validation failed", "requestID", "unknown", "error", err)
		var zero T
		return zero, fmt.Errorf("invalid options: %w", err)
	}

	result, err := redactValue(input, options)
	if err != nil {
		log.Error("Redact operation failed", "requestID", "unknown", "error", err)
		return result, err
	}

	log.Debug("Redact operation succeeded", "requestID", "unknown")
	return result, nil
}

// RedactWithResult provides detailed information about what was redacted.
//
// # NOT PRODUCTION READY
//
// It reports nothing by construction: the returned map is empty whatever the
// operation did, so an audit of a redaction pass reads as "nothing was
// redacted" with a nil error (T-09). See Redact for the rest, and
// docs/engineering/reviews/ADVERSARIAL_API_REVIEW.md for the detail.
func RedactWithResult[T any](input T, opts ...interface{}) (T, RedactResult, error) {
	result := RedactResult{
		Redacted: make(map[string][]string),
		Metadata: make(map[string]any),
	}

	redacted, err := Redact(input, opts...)
	if err != nil {
		return redacted, result, err
	}

	// For now, return empty result - full implementation would track what was redacted
	// This would require more complex logic to compare original vs redacted
	return redacted, result, nil
}

// redactValue performs the actual redaction using reflection
func redactValue[T any](input T, opts RedactOptions) (T, error) {
	v := reflect.ValueOf(input)
	if !v.IsValid() {
		var zero T
		return zero, fmt.Errorf("invalid input")
	}

	// Handle different types
	switch v.Kind() {
	case reflect.String:
		redactedStr := redactString(v.String(), opts)
		return reflect.ValueOf(redactedStr).Interface().(T), nil

	case reflect.Struct:
		return redactStruct(input, opts)

	case reflect.Slice, reflect.Array:
		return redactSlice(input, opts)

	case reflect.Map:
		return redactMap(input, opts)

	case reflect.Ptr:
		if v.IsNil() {
			var zero T
			return zero, nil
		}
		elem := v.Elem()
		redactedElem, err := redactValue(elem.Interface(), opts)
		if err != nil {
			var zero T
			return zero, err
		}
		// Create new pointer with redacted value
		newPtr := reflect.New(elem.Type())
		newPtr.Elem().Set(reflect.ValueOf(redactedElem))
		return newPtr.Interface().(T), nil

	default:
		// For other types, return as-is
		return input, nil
	}
}

// redactString redacts sensitive information from a string
func redactString(input string, opts RedactOptions) string {
	if input == "" {
		return input
	}

	// For now, use simple pattern matching
	// In a full implementation, this would use LLM for intelligent detection
	patterns := getPatternsForCategories(opts.Categories)
	patterns = append(patterns, opts.CustomPatterns...)

	result := input
	for _, pattern := range patterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			continue // Skip invalid patterns
		}

		result = re.ReplaceAllStringFunc(result, func(match string) string {
			return applyRedactionStrategy(match, opts)
		})
	}

	return result
}

// redactStruct redacts sensitive fields from a struct
func redactStruct[T any](input T, opts RedactOptions) (T, error) {
	v := reflect.ValueOf(input)
	t := reflect.TypeOf(input)

	if v.Kind() != reflect.Struct {
		return input, fmt.Errorf("input must be a struct")
	}

	// Create a new struct of the same type
	newStruct := reflect.New(t).Elem()

	// Copy all fields, redacting sensitive ones
	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		fieldType := t.Field(i)
		fieldName := fieldType.Name

		// Check if field should be redacted based on name, tags, or content
		if shouldRedactField(fieldName, fieldType, field, opts) {
			redactedValue := applyRedactionToValue(field, opts)
			newStruct.Field(i).Set(redactedValue)
		} else {
			// Recursively redact nested structs/slices/maps
			redactedValue, err := redactValue(field.Interface(), opts)
			if err != nil {
				// If redaction fails, keep original value
				newStruct.Field(i).Set(field)
			} else {
				newStruct.Field(i).Set(reflect.ValueOf(redactedValue))
			}
		}
	}

	return newStruct.Interface().(T), nil
}

// redactSlice redacts elements in a slice
func redactSlice[T any](input T, opts RedactOptions) (T, error) {
	v := reflect.ValueOf(input)
	if v.Kind() != reflect.Slice && v.Kind() != reflect.Array {
		return input, fmt.Errorf("input must be a slice or array")
	}

	// Create new slice
	newSlice := reflect.MakeSlice(v.Type(), v.Len(), v.Len())

	for i := 0; i < v.Len(); i++ {
		elem := v.Index(i)
		redactedElem, err := redactValue(elem.Interface(), opts)
		if err != nil {
			// Keep original if redaction fails
			newSlice.Index(i).Set(elem)
		} else {
			newSlice.Index(i).Set(reflect.ValueOf(redactedElem))
		}
	}

	return newSlice.Interface().(T), nil
}

// redactMap redacts values in a map
func redactMap[T any](input T, opts RedactOptions) (T, error) {
	v := reflect.ValueOf(input)
	if v.Kind() != reflect.Map {
		return input, fmt.Errorf("input must be a map")
	}

	// Create new map
	newMap := reflect.MakeMap(v.Type())

	for _, key := range v.MapKeys() {
		value := v.MapIndex(key)
		redactedValue, err := redactValue(value.Interface(), opts)
		if err != nil {
			// Keep original if redaction fails
			newMap.SetMapIndex(key, value)
		} else {
			newMap.SetMapIndex(key, reflect.ValueOf(redactedValue))
		}
	}

	return newMap.Interface().(T), nil
}

// shouldRedactField determines if a struct field should be redacted
func shouldRedactField(fieldName string, fieldType reflect.StructField, fieldValue reflect.Value, opts RedactOptions) bool {
	// Check struct tags
	if tag := fieldType.Tag.Get("redact"); tag != "" {
		return stringSliceContains(opts.Categories, tag)
	}

	// Check field name patterns
	lowerName := strings.ToLower(fieldName)
	sensitivePatterns := []string{
		"password", "secret", "token", "key", "ssn", "social", "credit", "card",
		"email", "phone", "address", "name", "first", "last", "full",
	}

	for _, pattern := range sensitivePatterns {
		if strings.Contains(lowerName, pattern) {
			return true
		}
	}

	// Check content patterns for string fields
	if fieldValue.Kind() == reflect.String {
		strValue := fieldValue.String()
		if matchesSensitivePattern(strValue, opts.Categories) {
			return true
		}
	}

	return false
}

// matchesSensitivePattern checks if a string matches sensitive data patterns
func matchesSensitivePattern(value string, categories []string) bool {
	patterns := getPatternsForCategories(categories)

	for _, pattern := range patterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			continue
		}
		if re.MatchString(value) {
			return true
		}
	}

	return false
}

// getPatternsForCategories returns regex patterns for given categories
func getPatternsForCategories(categories []string) []string {
	patterns := make(map[string][]string)

	patterns["PII"] = []string{
		`\b\d{3}-\d{2}-\d{4}\b`,       // SSN
		`\b\d{3}-\d{3}-\d{4}\b`,       // Phone
		`\S+@\S+\.\S+`,                // Email
		`\b\d{4} \d{4} \d{4} \d{4}\b`, // Credit card
		`\b[A-Z][a-z]+ [A-Z][a-z]+\b`, // Names (First Last)
	}

	patterns["secrets"] = []string{
		`(?i)(password|passwd|pwd)\s*[:=]\s*\S+`,
		`(?i)(secret|token|key)\s*[:=]\s*\S+`,
		`Bearer\s+[A-Za-z0-9+/=]{20,}`,
	}

	patterns["financial"] = []string{
		`\b\d{4}[- ]\d{4}[- ]\d{4}[- ]\d{4}\b`, // Credit cards
		`\b\d{9}\b`,                            // Bank routing numbers
		`\$\d+(?:\.\d{2})?`,                    // Currency amounts
	}

	var result []string
	for _, category := range categories {
		if categoryPatterns, exists := patterns[category]; exists {
			result = append(result, categoryPatterns...)
		}
	}

	return result
}

// generateMaskText creates mask text based on the options
func generateMaskText(originalValue string, opts RedactOptions) string {
	// If custom mask text is provided, use it
	if opts.MaskText != "" {
		return opts.MaskText
	}

	// Generate mask based on MaskChar and MaskLength
	var length int
	if opts.MaskLength == -1 {
		// Use original length
		length = len(originalValue)
	} else {
		length = opts.MaskLength
	}

	// Create mask string
	mask := make([]rune, length)
	for i := range mask {
		mask[i] = opts.MaskChar
	}
	return string(mask)
}

// applyRedactionStrategy applies the chosen redaction strategy to a value
func applyRedactionStrategy(value string, opts RedactOptions) string {
	switch opts.Strategy {
	case RedactNil:
		return ""
	case RedactMask:
		return generateMaskText(value, opts)
	case RedactRemove:
		return ""
	case RedactJumble, RedactScramble:
		return jumbleString(value, opts)
	default:
		return generateMaskText(value, opts)
	}
}

// applyRedactionToValue applies redaction to a reflect.Value
func applyRedactionToValue(value reflect.Value, opts RedactOptions) reflect.Value {
	switch value.Kind() {
	case reflect.String:
		redacted := applyRedactionStrategy(value.String(), opts)
		return reflect.ValueOf(redacted)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if opts.Strategy == RedactNil {
			return reflect.Zero(value.Type())
		}
		return value // Keep numbers as-is for now
	case reflect.Float32, reflect.Float64:
		if opts.Strategy == RedactNil {
			return reflect.Zero(value.Type())
		}
		return value // Keep floats as-is for now
	default:
		if opts.Strategy == RedactNil {
			return reflect.Zero(value.Type())
		}
		return value
	}
}

// jumbleString scrambles a string according to the jumble mode
func jumbleString(input string, opts RedactOptions) string {
	if input == "" {
		return input
	}

	r := rand.New(rand.NewSource(opts.JumbleSeed))
	if opts.JumbleSeed == 0 {
		r = rand.New(rand.NewSource(int64(len(input))))
	}

	switch opts.JumbleMode {
	case JumbleTypeAware:
		return jumbleTypeAware(input, r)
	case JumbleSmart:
		return jumbleSmart(input, r)
	default: // JumbleBasic
		return jumbleBasic(input, r)
	}
}

// jumbleBasic performs simple character scrambling
func jumbleBasic(input string, r *rand.Rand) string {
	runes := []rune(input)
	for i := len(runes) - 1; i > 0; i-- {
		j := r.Intn(i + 1)
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

// jumbleSmart preserves some structure (vowels, consonants)
func jumbleSmart(input string, r *rand.Rand) string {
	// Simple implementation - could be more sophisticated
	return jumbleBasic(input, r)
}

// jumbleTypeAware uses data type-specific scrambling rules
func jumbleTypeAware(input string, r *rand.Rand) string {
	// Detect data type and apply appropriate scrambling
	if matched, _ := regexp.MatchString(`\S+@\S+\.\S+`, input); matched {
		return jumbleEmail(input, r)
	}
	if matched, _ := regexp.MatchString(`\b\d{3}-\d{3}-\d{4}\b`, input); matched {
		return jumblePhone(input, r)
	}
	if matched, _ := regexp.MatchString(`\b[A-Z][a-z]+ [A-Z][a-z]+\b`, input); matched {
		return jumbleName(input, r)
	}

	// Default to basic jumbling
	return jumbleBasic(input, r)
}

// jumbleEmail scrambles an email address
func jumbleEmail(email string, r *rand.Rand) string {
	parts := strings.Split(email, "@")
	if len(parts) != 2 {
		return jumbleBasic(email, r)
	}

	username := jumbleBasic(parts[0], r)
	domainParts := strings.Split(parts[1], ".")
	if len(domainParts) > 0 {
		// Keep domain extension, jumble domain name
		domainParts[0] = jumbleBasic(domainParts[0], r)
	}

	return username + "@" + strings.Join(domainParts, ".")
}

// jumblePhone scrambles a phone number while preserving format
func jumblePhone(phone string, r *rand.Rand) string {
	// Simple digit scrambling while preserving hyphens
	digits := regexp.MustCompile(`\d`).FindAllString(phone, -1)

	// Scramble digits
	for i := len(digits) - 1; i > 0; i-- {
		j := r.Intn(i + 1)
		digits[i], digits[j] = digits[j], digits[i]
	}

	// Reconstruct with non-digits
	result := ""
	digitIdx := 0
	for _, char := range phone {
		if char >= '0' && char <= '9' {
			result += digits[digitIdx]
			digitIdx++
		} else {
			result += string(char)
		}
	}

	return result
}

// jumbleName scrambles a name
func jumbleName(name string, r *rand.Rand) string {
	parts := strings.Fields(name)
	for i, part := range parts {
		parts[i] = jumbleBasic(part, r)
	}
	return strings.Join(parts, " ")
}

// stringSliceContains checks if a slice contains a string
func stringSliceContains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
