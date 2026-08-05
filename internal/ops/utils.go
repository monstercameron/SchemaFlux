package ops

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/monstercameron/schemaflux/internal/types"
)

// hasJSONOption reports whether a struct tag carries the named option, testing
// the comma-separated options rather than the tag as a whole.
func hasJSONOption(tag, option string) bool {
	parts := strings.Split(tag, ",")
	for _, part := range parts[1:] {
		if part == option {
			return true
		}
	}
	return false
}

// sortedKeys returns a map's keys in a stable order. Go randomizes map
// iteration, so rendering a map into a prompt unsorted produces different bytes
// on every call: no prefix cache can hit, and no run is reproducible.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// GenerateTypeSchema creates a human-readable schema description for a Go type
func GenerateTypeSchema(targetType reflect.Type) string {
	if targetType.Kind() == reflect.Ptr {
		targetType = targetType.Elem()
	}

	switch targetType.Kind() {
	case reflect.Struct:
		var fields []string
		for i := 0; i < targetType.NumField(); i++ {
			field := targetType.Field(i)

			// Skip unexported fields
			if !field.IsExported() {
				continue
			}

			// Get JSON tag or use field name. `json:"-"` means the field is
			// not serialised at all, so describing it to the model under its Go
			// name asks for a value that will be discarded — and, for a field
			// excluded because it is sensitive, sends its name out of process.
			jsonTag := field.Tag.Get("json")
			fieldName := field.Name
			if jsonTag != "" {
				parts := strings.Split(jsonTag, ",")
				if parts[0] == "-" && len(parts) == 1 {
					continue
				}
				if parts[0] != "" {
					fieldName = parts[0]
				}
			}

			// Get field type description
			fieldType := GetTypeDescription(field.Type)

			// Check if field is required (no omitempty option). A substring
			// test would also match a field literally named "omitempty_flag".
			required := !hasJSONOption(jsonTag, "omitempty")
			requiredStr := ""
			if required {
				requiredStr = " (required)"
			}

			// Add field description
			fields = append(fields, fmt.Sprintf("  %s: %s%s", fieldName, fieldType, requiredStr))
		}
		return fmt.Sprintf("{\n%s\n}", strings.Join(fields, "\n"))

	case reflect.Slice:
		elemType := targetType.Elem()
		return fmt.Sprintf("[]%s", GenerateTypeSchema(elemType))

	case reflect.Map:
		keyType := targetType.Key()
		valueType := targetType.Elem()
		return fmt.Sprintf("map[%s]%s", keyType.String(), GenerateTypeSchema(valueType))

	default:
		return GetTypeDescription(targetType)
	}
}

// GetTypeDescription returns a simple description of a type
func GetTypeDescription(targetType reflect.Type) string {
	switch targetType.Kind() {
	case reflect.String:
		return "string"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return "integer"
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "unsigned integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Bool:
		return "boolean"
	case reflect.Struct:
		if targetType.String() == "time.Time" {
			return "datetime (RFC3339)"
		}
		return targetType.String()
	case reflect.Ptr:
		return GetTypeDescription(targetType.Elem()) + " (optional)"
	default:
		return targetType.String()
	}
}

// NormalizeInput converts various input types to string for LLM processing
func NormalizeInput(input any) (string, error) {
	if input == nil {
		return "", fmt.Errorf("input is nil")
	}

	switch inputValue := input.(type) {
	case string:
		return inputValue, nil
	case []byte:
		return string(inputValue), nil
	case fmt.Stringer:
		return inputValue.String(), nil
	default:
		// Try JSON marshaling for complex types
		if b, err := json.Marshal(input); err == nil {
			return string(b), nil
		}
		// Fallback to fmt.Sprint
		return fmt.Sprint(input), nil
	}
}

// BuildExtractSystemPrompt creates the system prompt for extraction based on mode
func BuildExtractSystemPrompt(typeSchema string, mode types.Mode) string {
	base := fmt.Sprintf(`You are a data extraction expert. Extract structured data from the input and return it as JSON.
Target schema:
%s

Rules:
- Extract all relevant information that maps to the schema
- Return ONLY valid JSON, no explanations or markdown`, typeSchema)

	switch mode {
	case types.Strict:
		return base + `
- All required fields MUST be present and valid
- Fail if any required field cannot be extracted
- Use null only for explicitly optional fields
- Validate data types strictly`

	case types.TransformMode:
		return base + `
- Infer missing fields intelligently when possible
- Use reasonable defaults for missing data
- Be flexible with type conversions
- Preserve as much information as possible`

	case types.Creative:
		return base + `
- Creatively interpret ambiguous data
- Generate plausible values for missing fields
- Use context to enrich extracted data
- Prioritize completeness over strict accuracy`

	default:
		return base
	}
}

// BuildGenerateStringPrompt creates the system prompt for string generation
func BuildGenerateStringPrompt(mode types.Mode) string {
	switch mode {
	case types.Strict:
		return "You are a precise content generator. Generate exactly what is requested, following all specifications strictly."
	case types.TransformMode:
		return "You are a creative content generator. Generate the requested content while maintaining quality and relevance."
	case types.Creative:
		return "You are a highly creative content generator. Generate engaging, original content based on the prompt."
	default:
		return "You are a content generator. Generate the requested content based on the prompt."
	}
}

// ValidateExtractedData validates extracted data meets requirements
func ValidateExtractedData(data any, threshold float64) error {
	// Basic validation - can be extended based on needs
	if data == nil {
		return fmt.Errorf("data cannot be nil")
	}

	value := reflect.ValueOf(data)
	if !value.IsValid() {
		return fmt.Errorf("invalid data")
	}

	if value.Kind() == reflect.Ptr {
		if value.IsNil() {
			return fmt.Errorf("data cannot be nil pointer")
		}
		value = value.Elem()
	}

	if value.Kind() != reflect.Struct {
		return nil // Only validate structs for now
	}

	// Check for zero values in required fields
	t := value.Type()
	for i := 0; i < value.NumField(); i++ {
		field := t.Field(i)
		fieldValue := value.Field(i)

		// Skip unexported fields
		if !field.IsExported() {
			continue
		}

		// Check if field is required (no omitempty tag)
		jsonTag := field.Tag.Get("json")
		if !strings.Contains(jsonTag, "omitempty") && fieldValue.IsZero() {
			return fmt.Errorf("required field %s is empty", field.Name)
		}
	}

	return nil
}

func Min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// contains checks if a slice contains a string
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// applyDefaults applies default values to OpOptions
func applyDefaults(opts ...types.OpOptions) types.OpOptions {
	result := types.OpOptions{
		Mode:         types.TransformMode,
		Intelligence: types.Smart,
	}

	for _, opt := range opts {
		// Mode is an int enum, 0 is Strict which is valid
		// Only override if the mode appears set (checking for non-zero threshold is a proxy)
		// Actually, we should just always copy since 0 (Strict) is a valid value
		// Let's check if any field is set by looking at Steering
		if opt.Steering != "" {
			result.Steering = opt.Steering
		}
		if opt.Threshold > 0 {
			result.Threshold = opt.Threshold
		}
		if opt.RequestID != "" {
			result.RequestID = opt.RequestID
		}
		if opt.Context != nil {
			result.Context = opt.Context
		}
		// For enums, we need a different approach - check if explicitly set
		// Since we can't tell if they're explicitly set, we'll assume any value is intentional
		// This means callers must always set these explicitly if they differ from defaults
		result.Mode = opt.Mode
		result.Intelligence = opt.Intelligence
	}

	return result
}
