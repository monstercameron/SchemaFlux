package ops

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// ParseJSON parses a JSON string into a target struct, handling common LLM output issues
// like markdown code blocks and whitespace.
func ParseJSON(input string, target any) error {
	// Clean up the input
	cleaned := cleanJSON(input)

	// Try to unmarshal
	err := json.Unmarshal([]byte(cleaned), target)
	if err != nil {
		// Deliberately no payload in the error. This error is logged by every
		// caller, and the payload is the caller's data -- often the very record
		// being extracted or redacted -- so embedding it copies user content
		// into every log aggregator the operator runs. Report the shape instead:
		// enough to diagnose, nothing to leak.
		return fmt.Errorf("failed to unmarshal JSON: %w (response was %d bytes, shape: %s)",
			err, len(cleaned), describeShape(cleaned))
	}

	return nil
}

// ParseJSONStrict parses like ParseJSON and additionally requires that the body
// answered the question asked: at least one of the target's JSON field names
// must appear in the response.
//
// encoding/json ignores fields it does not recognise, so a well-formed object of
// entirely the wrong shape unmarshals into a zero value and returns no error.
// An operation that only called ParseJSON therefore reported success with every
// field empty — a cluster operation returning no clusters, a verification
// returning no verdict — and the caller had nothing to check.
//
// The rule is deliberately weak: one recognised field is enough. It catches the
// answer that is about something else, without rejecting a model that omits an
// optional field or adds one of its own.
func ParseJSONStrict(input string, target any) error {
	if err := ParseJSON(input, target); err != nil {
		return err
	}
	return requireRecognisedField(input, target)
}

// requireRecognisedField reports an error when a JSON object shares no field
// name with the target struct. It returns nil for targets that have no field
// names to match — slices, maps, scalars — and for non-object bodies, which
// ParseJSON has already accepted against a matching target.
func requireRecognisedField(input string, target any) error {
	expected := jsonFieldNames(reflect.TypeOf(target))
	if len(expected) == 0 {
		return nil
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(cleanJSON(input)), &raw); err != nil {
		// Not an object: ParseJSON accepted it against this target, so the
		// shapes already agree.
		return nil
	}

	for key := range raw {
		if _, ok := expected[strings.ToLower(key)]; ok {
			return nil
		}
	}

	want := make([]string, 0, len(expected))
	for name := range expected {
		want = append(want, name)
	}
	sort.Strings(want)

	if len(raw) == 0 {
		return fmt.Errorf("the response was an empty object, which answers nothing (wanted one of %v)", want)
	}

	present := make([]string, 0, len(raw))
	for key := range raw {
		present = append(present, key)
	}
	sort.Strings(present)

	// Field names, not values: the keys describe the shape, not the payload.
	return fmt.Errorf("the response carried none of the expected fields; it has %v, wanted one of %v", present, want)
}

// jsonFieldNames returns the lower-cased JSON names of a struct's exported
// fields, following embedded structs. It returns nil for anything that is not a
// struct.
func jsonFieldNames(targetType reflect.Type) map[string]struct{} {
	for targetType != nil && (targetType.Kind() == reflect.Ptr || targetType.Kind() == reflect.Interface) {
		if targetType.Kind() == reflect.Interface {
			return nil
		}
		targetType = targetType.Elem()
	}
	if targetType == nil || targetType.Kind() != reflect.Struct {
		return nil
	}

	names := map[string]struct{}{}
	var collect func(reflect.Type, int)
	collect = func(structType reflect.Type, depth int) {
		if depth > 3 {
			return
		}
		for i := 0; i < structType.NumField(); i++ {
			field := structType.Field(i)

			// An embedded struct promotes its exported fields even when the
			// embedded type itself is unexported, so the Anonymous case has to
			// be handled before the IsExported gate.
			tag := field.Tag.Get("json")
			if field.Anonymous && tag == "" {
				embedded := field.Type
				if embedded.Kind() == reflect.Ptr {
					embedded = embedded.Elem()
				}
				if embedded.Kind() == reflect.Struct {
					collect(embedded, depth+1)
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

			names[strings.ToLower(name)] = struct{}{}
		}
	}
	collect(targetType, 0)

	return names
}

// describeShape classifies a response without reproducing any of it. A prefix
// was the obvious choice and the wrong one: the first 24 characters of a JSON
// body are usually the first field and its value, so a payload beginning
// {"ssn": "123-45-6789" leaks the value it was meant to summarise. The
// classification below distinguishes the cases an operator actually needs to
// tell apart -- an error page, prose, a truncated object -- and carries no
// caller data at all.
func describeShape(s string) string {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return "empty"
	case strings.HasPrefix(s, "```"):
		return "fenced block"
	case strings.HasPrefix(s, "{"):
		return "json object"
	case strings.HasPrefix(s, "["):
		return "json array"
	case strings.HasPrefix(s, "<"):
		return "markup, likely an error page"
	default:
		return "prose"
	}
}

// cleanJSON removes markdown code blocks and extra whitespace
func cleanJSON(input string) string {
	input = strings.TrimSpace(input)

	// Remove markdown code blocks if present
	if strings.HasPrefix(input, "```json") {
		input = strings.TrimPrefix(input, "```json")
		input = strings.TrimSuffix(input, "```")
	} else if strings.HasPrefix(input, "```") {
		input = strings.TrimPrefix(input, "```")
		input = strings.TrimSuffix(input, "```")
	}

	return strings.TrimSpace(input)
}
