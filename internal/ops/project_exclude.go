package ops

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// marshalWithoutExcluded serialises input with the named fields removed, and
// returns the values it removed so the output can be checked against them.
//
// Matching is on the JSON field name, case-insensitively, at every level of the
// document: a field named "ssn" nested inside an address is still an SSN.
func marshalWithoutExcluded(input any, exclude []string) ([]byte, map[string]string, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, nil, err
	}
	if len(exclude) == 0 {
		return raw, nil, nil
	}

	excluded := make(map[string]struct{}, len(exclude))
	for _, name := range exclude {
		name = strings.TrimSpace(strings.ToLower(name))
		if name != "" {
			excluded[name] = struct{}{}
		}
	}
	if len(excluded) == 0 {
		return raw, nil, nil
	}

	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		// Not an object or array: there are no field names to strip.
		return raw, nil, nil
	}

	removed := map[string]string{}
	stripped := stripExcluded(document, excluded, removed)

	cleaned, err := json.Marshal(stripped)
	if err != nil {
		return nil, nil, err
	}
	return cleaned, removed, nil
}

// stripExcluded walks a decoded JSON document and removes every key whose
// lower-cased name is excluded, recording the scalar values it removed.
func stripExcluded(node any, excluded map[string]struct{}, removed map[string]string) any {
	switch typed := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, value := range typed {
			if _, drop := excluded[strings.ToLower(key)]; drop {
				if text := valueText(value); text != "" {
					removed[key] = text
				}
				continue
			}
			out[key] = stripExcluded(value, excluded, removed)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, value := range typed {
			out[i] = stripExcluded(value, excluded, removed)
		}
		return out
	default:
		return node
	}
}

// valueText renders a removed value for later comparison. Only scalars are
// compared: a removed object is gone wholesale, and matching its parts
// individually would produce false positives on common substrings.
func valueText(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case float64, bool:
		return fmt.Sprintf("%v", typed)
	default:
		return ""
	}
}

// findLeakedValues reports the name of the first excluded field whose value
// appears in the projected output, or "" if none does.
//
// Short values are skipped: a removed boolean, or a two-character country code,
// would match by coincidence and turn every projection into an error.
func findLeakedValues(projected any, excludedValues map[string]string) string {
	if len(excludedValues) == 0 {
		return ""
	}

	rendered, err := json.Marshal(projected)
	if err != nil {
		return ""
	}
	haystack := strings.ToLower(string(rendered))

	// Sorted, so the reported field is the same one on every run.
	fields := make([]string, 0, len(excludedValues))
	for field := range excludedValues {
		fields = append(fields, field)
	}
	sort.Strings(fields)

	for _, field := range fields {
		value := excludedValues[field]
		if len(value) < minimumLeakLength {
			continue
		}
		if strings.Contains(haystack, strings.ToLower(value)) {
			return field
		}
	}
	return ""
}

// minimumLeakLength is the shortest excluded value worth scanning for. Below
// it, a match says more about coincidence than about a leak.
const minimumLeakLength = 6

// sortedStrings returns a copy in a stable order, so the prompt bytes do not
// depend on the caller's slice order.
func sortedStrings(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
