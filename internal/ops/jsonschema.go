package ops

import (
	"reflect"
	"strings"
	"time"
)

// The library generates an exact schema for T and renders it into the system
// prompt as prose. The provider then asks only for `json_object` — free-form
// JSON — so the one artifact that could make Extract[T] structurally guaranteed
// was never sent to the API that can enforce it. The central claim of the
// library, that the type is the contract, was enforced by persuasion.
//
// GenerateJSONSchema produces the machine-readable form, for the Responses API's
// json_schema strict mode.

// GenerateJSONSchema renders a Go type as a JSON Schema suitable for
// structured-output enforcement, or nil when the type cannot be expressed.
//
// Strict mode has rules the schema has to satisfy: every object needs
// "additionalProperties": false and must list every property in "required".
// Optional fields are therefore expressed as a union with null rather than by
// omission, which is what the API expects.
func GenerateJSONSchema(targetType reflect.Type) map[string]any {
	schema := jsonSchemaFor(targetType, map[reflect.Type]bool{}, 0)
	if schema == nil {
		return nil
	}
	return schema
}

// maxJSONSchemaDepth bounds the expansion, as the prose schema is bounded.
const maxJSONSchemaDepth = 6

func jsonSchemaFor(targetType reflect.Type, seen map[reflect.Type]bool, depth int) map[string]any {
	optional := false
	for targetType != nil && targetType.Kind() == reflect.Ptr {
		optional = true
		targetType = targetType.Elem()
	}
	if targetType == nil || depth > maxJSONSchemaDepth {
		return nil
	}

	var schema map[string]any

	switch targetType.Kind() {
	case reflect.String:
		schema = map[string]any{"type": "string"}
	case reflect.Bool:
		schema = map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		schema = map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		schema = map[string]any{"type": "number"}

	case reflect.Struct:
		if targetType == reflect.TypeOf(time.Time{}) {
			schema = map[string]any{"type": "string", "format": "date-time"}
			break
		}
		if seen[targetType] {
			// A recursive type cannot be expressed in strict mode without $ref
			// support, so the whole schema is abandoned rather than silently
			// truncated: a partial contract enforced as if complete is worse
			// than no contract.
			return nil
		}
		seen[targetType] = true
		defer delete(seen, targetType)

		properties := map[string]any{}
		var required []string

		if !collectStructProperties(targetType, seen, depth, properties, &required) {
			return nil
		}
		if len(properties) == 0 {
			return nil
		}

		schema = map[string]any{
			"type":                 "object",
			"properties":           properties,
			"required":             required,
			"additionalProperties": false,
		}

	case reflect.Slice, reflect.Array:
		items := jsonSchemaFor(targetType.Elem(), seen, depth+1)
		if items == nil {
			return nil
		}
		schema = map[string]any{"type": "array", "items": items}

	case reflect.Map:
		// Strict mode cannot express an open-ended object, so a map means the
		// type is not enforceable and the caller falls back to prompt-only.
		return nil

	default:
		return nil
	}

	if optional {
		schema = nullable(schema)
	}
	return schema
}

// collectStructProperties fills properties and required for a struct, following
// embedded structs the way encoding/json promotes them. It reports false when
// any field cannot be expressed.
func collectStructProperties(structType reflect.Type, seen map[reflect.Type]bool, depth int,
	properties map[string]any, required *[]string) bool {

	for i := 0; i < structType.NumField(); i++ {
		field := structType.Field(i)
		tag := field.Tag.Get("json")

		if field.Anonymous && tag == "" {
			embedded := field.Type
			for embedded.Kind() == reflect.Ptr {
				embedded = embedded.Elem()
			}
			if embedded.Kind() == reflect.Struct {
				if !collectStructProperties(embedded, seen, depth, properties, required) {
					return false
				}
				continue
			}
		}

		if !field.IsExported() {
			continue
		}

		name := field.Name
		if tag != "" {
			parts := strings.Split(tag, ",")
			if parts[0] == "-" && len(parts) == 1 {
				continue
			}
			if parts[0] != "" {
				name = parts[0]
			}
		}

		fieldSchema := jsonSchemaFor(field.Type, seen, depth+1)
		if fieldSchema == nil {
			return false
		}

		// Strict mode requires every property to appear in "required", so an
		// optional field is expressed as a union with null instead.
		//
		// Optionality comes from the same resolver the prompt schema and the
		// validator use, so the JSON Schema sent to the provider says the same
		// thing as the prose schema in the prompt and the check applied to the
		// answer. Three descriptions of one contract that could disagree is
		// three chances to disagree.
		if !IsRequired(field) {
			fieldSchema = nullable(fieldSchema)
		}

		properties[name] = fieldSchema
		*required = append(*required, name)
	}

	return true
}

// nullable widens a schema to permit null, which is how strict mode expresses
// an optional value.
func nullable(schema map[string]any) map[string]any {
	raw, ok := schema["type"]
	if !ok {
		return schema
	}

	switch typed := raw.(type) {
	case string:
		if typed == "null" {
			return schema
		}
		widened := map[string]any{}
		for k, v := range schema {
			widened[k] = v
		}
		widened["type"] = []string{typed, "null"}
		return widened
	default:
		return schema
	}
}

// schemaNameFor produces a name the API accepts: letters, digits, and
// underscores only.
func schemaNameFor(targetType reflect.Type) string {
	for targetType != nil && targetType.Kind() == reflect.Ptr {
		targetType = targetType.Elem()
	}
	if targetType == nil {
		return "result"
	}

	var builder strings.Builder
	for _, r := range targetType.Name() {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			builder.WriteRune(r)
		default:
			builder.WriteRune('_')
		}
	}

	name := builder.String()
	if name == "" {
		return "result"
	}
	return name
}
