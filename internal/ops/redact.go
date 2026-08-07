// package ops - Redact operation for intelligent data masking
package ops

import (
	cryptorand "crypto/rand"
	"encoding/binary"
	"fmt"
	"math/rand"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/types"
)

// RedactResult contains detailed information about redaction operations
type RedactResult struct {
	Redacted map[string][]string `json:"redacted"` // category -> list of redacted values
	Count    int                 `json:"count"`    // total items redacted

	// FieldsRedacted names the struct fields that were replaced because of
	// their name or a redact tag, which the category map cannot express: a
	// field redacted by name matched no pattern.
	FieldsRedacted []string `json:"fields_redacted,omitempty"`

	Metadata map[string]any `json:"metadata"` // additional metadata
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
	CustomPatterns []string       // Additional regex patterns to match

	// report accumulates what was redacted, so RedactWithResult can say. It is
	// a pointer because RedactOptions is copied by value through the recursive
	// traversal, and every copy has to record into the same place.
	report *redactionReport
}

// redactionReport accumulates what a redaction pass replaced.
type redactionReport struct {
	byCategory map[string][]string
	fields     []string
}

func newRedactionReport() *redactionReport {
	return &redactionReport{byCategory: map[string][]string{}}
}

func (r *redactionReport) addValues(found map[string][]string) {
	if r == nil {
		return
	}
	for category, values := range found {
		r.byCategory[category] = append(r.byCategory[category], values...)
	}
}

func (r *redactionReport) addField(name string) {
	if r == nil {
		return
	}
	r.fields = append(r.fields, name)
}

// NewRedactOptions creates RedactOptions with defaults
func NewRedactOptions() RedactOptions {
	return RedactOptions{
		OpOptions: types.OpOptions{
			Mode:         types.Strict,
			Intelligence: types.Smart,
		},
		Categories: []string{"PII"},
		Strategy:   RedactMask,
		MaskText:   "",
		MaskChar:   '*',
		MaskLength: 3,
		JumbleMode: JumbleTypeAware,
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

// resolveRedactOptions turns the variadic any into options. The signature
// takes `...interface{}`, so an argument of an unexpected type is silently
// replaced by the defaults (T-10); resolveRedactOptions is where that will be
// fixed when the signature is tightened.
func resolveRedactOptions(opts ...interface{}) RedactOptions {
	if len(opts) == 0 {
		return NewRedactOptions()
	}
	switch opt := opts[0].(type) {
	case RedactOptions:
		return opt
	case types.OpOptions:
		options := NewRedactOptions()
		options.OpOptions = opt
		return options
	default:
		return NewRedactOptions()
	}
}

// Redact removes or masks sensitive information from data.
// Returns a new object of the same type with sensitive data redacted.
//
// # What it detects, and what it does not
//
// Field names are matched as whole names, not substrings: FirstName and APIKey
// are redacted; Filename, Username, Keywords, KeyMetrics, FirstSeen,
// LastUpdated, and CardCount are not. The `redact` struct tag is the precise
// mechanism and should be preferred when you know the shape of your data.
//
// Values are matched by pattern per category. Card numbers are validated with
// Luhn rather than recognised by shape, so an unformatted 16-digit PAN is
// caught and a 16-digit order number is not. SSNs are matched in the forms they
// are written in; a bare nine-digit run is deliberately NOT matched, because it
// is indistinguishable from an order ID and matching it destroyed ordinary
// data.
//
// It is a pattern matcher, not a classifier. It finds what its categories
// describe and nothing else: a card number spelled out in words, or a name
// inside a free-text note, is not detected. Tagging the fields is the reliable
// path; the patterns are a safety net under it.
//
// # Jumbling is obfuscation, not anonymization
//
// RedactJumble and RedactScramble permute characters, which preserves length,
// alphabet, and frequency. Use them for demo data that has to look realistic,
// not for anything where re-identification matters.
func Redact[T any](input T, opts ...interface{}) (T, error) {
	log := logger.GetLogger()
	log.Debug("Starting redact operation", "requestID", "unknown", "inputType", fmt.Sprintf("%T", input))

	options := resolveRedactOptions(opts...)

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

	options := resolveRedactOptions(opts...)
	options.report = newRedactionReport()

	if err := options.Validate(); err != nil {
		var zero T
		return zero, result, fmt.Errorf("invalid options: %w", err)
	}

	redacted, err := redactValue(input, options)
	if err != nil {
		var zero T
		return zero, result, err
	}

	// This used to return an empty map with a nil error whatever it had done,
	// so a caller auditing a redaction pass was told nothing was redacted and
	// treated that as success.
	result.Redacted = options.report.byCategory
	if result.Redacted == nil {
		result.Redacted = map[string][]string{}
	}
	result.FieldsRedacted = options.report.fields
	result.Metadata["fields_redacted"] = len(options.report.fields)

	total := 0
	for _, values := range result.Redacted {
		total += len(values)
	}
	result.Metadata["values_redacted"] = total
	result.Count = total + len(options.report.fields)

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

// redactString redacts sensitive information from a string.
func redactString(input string, opts RedactOptions) string {
	redacted, _ := redactStringWithReport(input, opts)
	return redacted
}

// redactStringWithReport redacts and reports what it replaced, keyed by
// category. The report is what RedactWithResult returns; it used to be empty by
// construction, so an audit of a redaction pass read as "nothing was redacted".
func redactStringWithReport(input string, opts RedactOptions) (string, map[string][]string) {
	report := map[string][]string{}
	if input == "" {
		return input, report
	}

	result := input
	defer func() { opts.report.addValues(report) }()

	for _, category := range opts.Categories {
		for _, pattern := range categoryPatterns(category) {
			result = pattern.ReplaceAllStringFunc(result, func(match string) string {
				report[category] = append(report[category], match)
				return applyRedactionStrategy(match, opts)
			})
		}

		if categoryUsesCardDetection(category) {
			result = cardCandidatePattern.ReplaceAllStringFunc(result, func(match string) string {
				// Shape is not enough: an order number is 16 digits too. Luhn
				// is what separates a payment card from a sequence number, and
				// it is why an unformatted PAN is caught now and "New York"
				// never was a card in the first place.
				if !luhn(digitsOnly(match)) {
					return match
				}
				report[category] = append(report[category], match)
				return applyRedactionStrategy(match, opts)
			})
		}
	}

	for _, raw := range opts.CustomPatterns {
		pattern, err := regexp.Compile(raw)
		if err != nil {
			continue // Skip invalid patterns
		}
		result = pattern.ReplaceAllStringFunc(result, func(match string) string {
			report["custom"] = append(report["custom"], match)
			return applyRedactionStrategy(match, opts)
		})
	}

	return result, report
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

		// A field named as sensitive is replaced wholesale; a field that merely
		// contains something sensitive has that part replaced, so a notes field
		// with a phone number in it keeps the note. Recording which of the two
		// happened is what makes RedactWithResult an audit rather than a count.
		byName := fieldNameIsSensitive(fieldName) || fieldType.Tag.Get("redact") != ""

		if byName {
			opts.report.addField(fieldName)
			newStruct.Field(i).Set(applyRedactionToValue(field, opts))
		} else if field.Kind() == reflect.String &&
			valueIsSensitive(field.String(), opts.Categories, opts.CustomPatterns) {
			redacted, found := redactStringWithReport(field.String(), opts)
			opts.report.addField(fieldName)
			_ = found // redactStringWithReport already recorded into opts.report
			newStruct.Field(i).SetString(redacted)
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

		// A map key names its value exactly as a struct field name does, and
		// this used to ignore it entirely: map[string]any{"ssn": ...} was
		// redacted only if the value happened to match a pattern.
		if key.Kind() == reflect.String && fieldNameIsSensitive(key.String()) {
			opts.report.addField(key.String())
			newMap.SetMapIndex(key, applyRedactionToValue(value, opts))
			continue
		}

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
	// A map[string]any holds interface values, so the string inside has to be
	// unwrapped or nothing is ever redacted by key name.
	if value.Kind() == reflect.Interface && !value.IsNil() {
		redacted := applyRedactionToValue(value.Elem(), opts)
		wrapper := reflect.New(value.Type()).Elem()
		if redacted.Type().AssignableTo(value.Type()) {
			wrapper.Set(redacted)
			return wrapper
		}
		return value
	}

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
// jumbleString permutes the characters of the input.
//
// # This is obfuscation, not anonymization
//
// A permutation preserves length, alphabet, and character frequency. Those
// three facts are often enough to re-identify a value, so jumbling is suitable
// for demo data and screenshots that need to look realistic, and is not
// suitable for anything where re-identification matters. Use RedactMask or
// RedactNil for that.
//
// It used to be worse than that: JumbleSeed defaults to zero and the RNG was
// then seeded with the input's LENGTH -- a number an attacker reads off the
// output. The permutation was fully determined by a seed anyone could
// reproduce with this library, which made RedactJumble and RedactScramble
// exactly invertible. The default now draws from crypto/rand and is not
// reproducible.
//
// Setting JumbleSeed explicitly restores determinism, and with it
// invertibility by anyone who knows the seed. That is a reasonable thing to
// want for reproducible test fixtures and an unreasonable thing to rely on for
// privacy.
func jumbleString(input string, opts RedactOptions) string {
	if input == "" {
		return input
	}

	seed := opts.JumbleSeed
	if seed == 0 {
		seed = unpredictableSeed()
	}
	r := rand.New(rand.NewSource(seed))

	switch opts.JumbleMode {
	case JumbleTypeAware:
		return jumbleTypeAware(input, r)
	case JumbleSmart:
		return jumbleSmart(input, r)
	default: // JumbleBasic
		return jumbleBasic(input, r)
	}
}

// unpredictableSeed draws a seed the caller cannot reconstruct from the output.
// A failure to read the system source falls back to the clock, which is weaker
// but still not a function of the input.
func unpredictableSeed() int64 {
	var buf [8]byte
	if _, err := cryptorand.Read(buf[:]); err == nil {
		return int64(binary.LittleEndian.Uint64(buf[:]))
	}
	return time.Now().UnixNano()
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
