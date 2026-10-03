package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// Limits the Responses API enforces on the request itself.
//
// Each of these was learned as an HTTP 400 against the live API on 2026-10-01
// and 2026-10-02 (TODOS.md LX-002, LX-003, LX-004). A request that breaks one
// is refused whole, so they are checked where the request is built instead of
// being left for the vendor to report.

// maxPromptCacheKeyLength is the API's bound on `prompt_cache_key`: "Expected a
// string with maximum length 64."
const maxPromptCacheKeyLength = 64

// boundPromptCacheKey returns a key the API will accept.
//
// A key within the limit is sent unchanged, so a request that worked before
// this bound existed produces the same body. A longer one is replaced by the
// SHA-256 of the whole key, which is exactly 64 hex characters. The digest
// covers every part of the original, so two prefixes that differed before
// still differ -- the property the key exists for -- and the mapping is stable
// across calls and processes, which is what makes it a cache key at all.
//
// The length is counted in bytes. The API's error does not say which it
// counts, and bytes is the reading that cannot be too generous.
func boundPromptCacheKey(key string) string {
	if len(key) <= maxPromptCacheKeyLength {
		return key
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// StrictSchemaProblem reports the first way a schema breaks a rule OpenAI's
// strict structured-output mode was observed to enforce, or "" when it breaks
// none of them.
//
// It checks these rules and no others, each seen as a 400 from the live API:
//
//   - the root is exactly `"type": "object"`. An array, a scalar, a union and a
//     nullable object are all refused: "schema must be a JSON Schema of 'type:
//     "object"'".
//   - every object sets `"additionalProperties": false`.
//   - `required` lists every property, and nothing that is not a property:
//     "Extra required key ... supplied". An optional field is a union with
//     null, not an omission from required.
//
// It also refuses a property whose schema is missing or empty, which is what a
// type the schema generator cannot express turns into if nobody checks.
//
// An empty answer is not a promise that the API will accept the schema: the
// API has rules this does not know. It reads only the schema's own keywords,
// and names a JSON path and property names, never a value from caller data.
// The path walked is fixed, so one schema always yields one message.
func StrictSchemaProblem(schema map[string]any) string {
	// A schema built in Go can hold []string, []map[string]any or a typed nil
	// map where JSON has only arrays and objects. Walking the JSON form means
	// what is checked is what is sent.
	raw, err := json.Marshal(schema)
	if err != nil {
		// The error names a Go type or a JSON path, never a value.
		return "$: the schema cannot be encoded as JSON: " + err.Error()
	}
	var normalised any
	if err := json.Unmarshal(raw, &normalised); err != nil {
		return "$: the schema cannot be encoded as JSON"
	}

	root, ok := normalised.(map[string]any)
	if !ok || len(root) == 0 {
		return "$: the schema is empty"
	}
	if kind, _ := root["type"].(string); kind != "object" {
		return `$: the root must be exactly "type": "object" (wrap an array, a scalar or an optional value in an object)`
	}
	return strictProblemAt(root, "$")
}

func strictProblemAt(node any, path string) string {
	switch typed := node.(type) {
	case map[string]any:
		properties, hasProperties := typed["properties"].(map[string]any)
		if hasProperties || schemaTypeIncludes(typed["type"], "object") {
			if additional, isBool := typed["additionalProperties"].(bool); !isBool || additional {
				return fmt.Sprintf("%s: an object must set additionalProperties to false", path)
			}

			required := map[string]bool{}
			list, _ := typed["required"].([]any)
			for _, name := range list {
				if s, ok := name.(string); ok {
					required[s] = true
				}
			}
			for _, name := range sortedSchemaKeys(properties) {
				if !required[name] {
					return fmt.Sprintf("%s: property %q must be listed in required (declare an optional field as a union with null)", path, name)
				}
			}
			extra := make([]string, 0)
			for name := range required {
				if _, declared := properties[name]; !declared {
					extra = append(extra, name)
				}
			}
			if len(extra) > 0 {
				sort.Strings(extra)
				return fmt.Sprintf("%s: required names %q, which is not a property", path, extra[0])
			}
		}

		for _, name := range sortedSchemaKeys(properties) {
			child, isSchema := properties[name].(map[string]any)
			if !isSchema || len(child) == 0 {
				return fmt.Sprintf("%s: property %q has no schema", path, name)
			}
			if problem := strictProblemAt(child, path+".properties."+name); problem != "" {
				return problem
			}
		}
		if items, ok := typed["items"]; ok {
			child, isSchema := items.(map[string]any)
			if !isSchema || len(child) == 0 {
				return fmt.Sprintf("%s: items has no schema", path)
			}
			if problem := strictProblemAt(child, path+".items"); problem != "" {
				return problem
			}
		}
		for _, keyword := range []string{"anyOf", "oneOf", "allOf"} {
			members, ok := typed[keyword].([]any)
			if !ok {
				continue
			}
			for i, member := range members {
				child, isSchema := member.(map[string]any)
				if !isSchema || len(child) == 0 {
					return fmt.Sprintf("%s.%s[%d]: a union member has no schema", path, keyword, i)
				}
				if problem := strictProblemAt(child, fmt.Sprintf("%s.%s[%d]", path, keyword, i)); problem != "" {
					return problem
				}
			}
		}
		for _, keyword := range []string{"$defs", "definitions"} {
			defs, ok := typed[keyword].(map[string]any)
			if !ok {
				continue
			}
			for _, name := range sortedSchemaKeys(defs) {
				if problem := strictProblemAt(defs[name], path+"."+keyword+"."+name); problem != "" {
					return problem
				}
			}
		}
	}
	return ""
}

// schemaTypeIncludes reports whether a schema's "type" keyword, which may be a
// string or a list, names the given type.
func schemaTypeIncludes(raw any, want string) bool {
	switch typed := raw.(type) {
	case string:
		return typed == want
	case []any:
		for _, name := range typed {
			if s, ok := name.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}

func sortedSchemaKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
