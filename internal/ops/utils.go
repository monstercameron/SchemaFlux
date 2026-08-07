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

// GenerateTypeSchema creates a human-readable schema description for a Go type.
//
// It recurses. It used to expand only the top level and describe every field
// with its Go type name, so
//
//	type Order struct {
//	    Customer Person      `json:"customer"`
//	    Items    []OrderItem `json:"items"`
//	}
//
// produced `customer: main.Person` and `items: []main.OrderItem` — names that
// mean nothing to a model, which then had to invent the shape of the thing it
// was being asked to produce.
func GenerateTypeSchema(targetType reflect.Type) string {
	return describeType(targetType, map[reflect.Type]bool{}, 0)
}

// maxSchemaDepth bounds the expansion. A schema deeper than this is more prompt
// than it is worth, and the bound is also what stops a self-referential type
// from expanding forever.
const maxSchemaDepth = 6

// describeType renders a type, expanding structs, slices, and maps. seen holds
// the types on the current path so a cycle -- a Node with a Parent *Node -- is
// named rather than followed.
func describeType(targetType reflect.Type, seen map[reflect.Type]bool, depth int) string {
	for targetType != nil && targetType.Kind() == reflect.Ptr {
		targetType = targetType.Elem()
	}
	if targetType == nil {
		return "unknown"
	}

	switch targetType.Kind() {
	case reflect.Struct:
		if targetType.String() == "time.Time" {
			return "datetime (RFC3339)"
		}
		if depth >= maxSchemaDepth {
			return targetType.String() + " (nested, not expanded)"
		}
		if seen[targetType] {
			// A cycle. Naming it is honest; following it does not terminate.
			return targetType.String() + " (recursive)"
		}

		seen[targetType] = true
		defer delete(seen, targetType)

		var fields []string
		for i := 0; i < targetType.NumField(); i++ {
			field := targetType.Field(i)

			tag := field.Tag.Get("json")
			if field.Anonymous && tag == "" {
				// An embedded struct promotes its fields, so it is flattened
				// here as encoding/json would flatten it.
				embedded := field.Type
				if embedded.Kind() == reflect.Ptr {
					embedded = embedded.Elem()
				}
				if embedded.Kind() == reflect.Struct {
					inner := describeType(embedded, seen, depth)
					fields = append(fields, indentSchemaBody(inner))
					continue
				}
			}

			if !field.IsExported() {
				continue
			}

			fieldName := field.Name
			if tag != "" {
				parts := strings.Split(tag, ",")
				if parts[0] == "-" && len(parts) == 1 {
					continue
				}
				if parts[0] != "" {
					fieldName = parts[0]
				}
			}

			requiredStr := ""
			if !hasJSONOption(tag, "omitempty") {
				requiredStr = " (required)"
			}

			fields = append(fields, fmt.Sprintf("  %s: %s%s",
				fieldName, indentNested(describeType(field.Type, seen, depth+1)), requiredStr))
		}

		if len(fields) == 0 {
			return "{}"
		}
		return fmt.Sprintf("{\n%s\n}", strings.Join(fields, "\n"))

	case reflect.Slice, reflect.Array:
		return "[" + describeType(targetType.Elem(), seen, depth+1) + "]"

	case reflect.Map:
		return fmt.Sprintf("map[%s]%s",
			GetTypeDescription(targetType.Key()),
			describeType(targetType.Elem(), seen, depth+1))

	default:
		return GetTypeDescription(targetType)
	}
}

// indentNested pushes a nested block in one level so the shape stays readable.
func indentNested(body string) string {
	if !strings.Contains(body, "\n") {
		return body
	}
	lines := strings.Split(body, "\n")
	for i := 1; i < len(lines); i++ {
		lines[i] = "  " + lines[i]
	}
	return strings.Join(lines, "\n")
}

// indentSchemaBody strips the braces from an embedded struct's rendering so its
// fields sit alongside the outer ones, as encoding/json promotes them.
func indentSchemaBody(body string) string {
	body = strings.TrimPrefix(body, "{\n")
	body = strings.TrimSuffix(body, "\n}")
	return body
}

// GetTypeDescription returns a one-line description of a leaf type. Structs,
// slices, and maps are rendered by describeType, which recurses; this is what
// it calls at the leaves and what map keys use.
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
	default:
		// JSON before Stringer, which is the reverse of what this used to do.
		//
		// Any type with a String() method took the Stringer branch, and
		// time.Time has one -- so a timestamp was sent to the model as
		// "2026-08-07 18:04:05.999 -0400 EDT" while the generated schema told
		// it, in the same request, that the format was RFC3339. The model was
		// asked to reconcile two descriptions of the same value and the library
		// had given it no way to.
		//
		// A json.Marshaler is a type's own statement about its wire form, and
		// that is the form the schema describes, so it wins. Stringer is a
		// display format and stays as the fallback for types JSON cannot
		// render.
		if b, err := json.Marshal(input); err == nil {
			return string(b), nil
		}
		if stringer, ok := input.(fmt.Stringer); ok {
			return stringer.String(), nil
		}
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
		// Creative on an extraction used to say "Generate plausible values for
		// missing fields" and "Prioritize completeness over strict accuracy" --
		// which is an instruction to fabricate, on the one operation whose
		// entire purpose is faithfulness to a source. A caller reaching for
		// Creative wants flexible *interpretation* of what is there, not
		// invention of what is not, and nothing in the name told them the
		// difference.
		//
		// It now means the most permissive reading of the source. Inference is
		// still allowed and still marked; invention is not.
		return base + `
- Interpret ambiguous or loosely-worded data generously
- Use context elsewhere in the input to resolve what a field refers to
- Accept values that are implied rather than stated, where the implication is clear
- Leave a field null when the input does not support a value: do NOT invent one`

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
// ValidateExtractedData is what Strict mode enforces. It checks that every
// required field — every exported field without `omitempty` — was actually
// populated, at every level of the structure.
//
// It used to take a threshold it never read and check only the top level, so
// Strict() on a struct with a nested address whose every field came back empty
// reported success. Strict is the most confidence-inspiring word in this API;
// it should mean something.
//
// A field is "populated" if it is not the zero value of its type. That is a
// blunt rule and it is the honest one available: a model that returns 0 for an
// int it could not determine is indistinguishable from one that determined 0.
// Mark such fields `omitempty` to say they are optional.
func ValidateExtractedData(data any) error {
	if data == nil {
		return fmt.Errorf("data cannot be nil")
	}

	value := reflect.ValueOf(data)
	if !value.IsValid() {
		return fmt.Errorf("invalid data")
	}

	return validateRequiredFields(value, "", 0)
}

// maxValidationDepth bounds the walk, matching the schema's own bound: a
// structure the schema did not describe is one Strict cannot meaningfully
// enforce.
const maxValidationDepth = maxSchemaDepth

func validateRequiredFields(value reflect.Value, path string, depth int) error {
	if depth > maxValidationDepth {
		return nil
	}

	for value.Kind() == reflect.Ptr || value.Kind() == reflect.Interface {
		if value.IsNil() {
			if path == "" {
				return fmt.Errorf("data cannot be nil pointer")
			}
			return nil // a nil pointer field is checked by its own zero test
		}
		value = value.Elem()
	}

	switch value.Kind() {
	case reflect.Struct:
		structType := value.Type()
		for i := 0; i < structType.NumField(); i++ {
			field := structType.Field(i)
			if !field.IsExported() {
				continue
			}

			tag := field.Tag.Get("json")
			parts := strings.Split(tag, ",")
			if tag != "" && parts[0] == "-" && len(parts) == 1 {
				continue
			}

			name := field.Name
			if tag != "" && parts[0] != "" {
				name = parts[0]
			}
			fieldPath := name
			if path != "" {
				fieldPath = path + "." + name
			}

			fieldValue := value.Field(i)
			required := !hasJSONOption(tag, "omitempty")

			if required && fieldValue.IsZero() {
				return fmt.Errorf("required field %s is empty", fieldPath)
			}

			// Recurse into what was populated. An optional field that is
			// present still has to be internally consistent.
			if !fieldValue.IsZero() {
				if err := validateRequiredFields(fieldValue, fieldPath, depth+1); err != nil {
					return err
				}
			}
		}
		return nil

	case reflect.Slice, reflect.Array:
		for i := 0; i < value.Len(); i++ {
			element := value.Index(i)
			if element.Kind() == reflect.Struct || element.Kind() == reflect.Ptr || element.Kind() == reflect.Interface {
				if err := validateRequiredFields(element, fmt.Sprintf("%s[%d]", path, i), depth+1); err != nil {
					return err
				}
			}
		}
		return nil

	case reflect.Map:
		for _, key := range value.MapKeys() {
			element := value.MapIndex(key)
			if element.Kind() == reflect.Struct || element.Kind() == reflect.Ptr || element.Kind() == reflect.Interface {
				if err := validateRequiredFields(element, fmt.Sprintf("%s[%v]", path, key.Interface()), depth+1); err != nil {
					return err
				}
			}
		}
		return nil

	default:
		return nil
	}
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
