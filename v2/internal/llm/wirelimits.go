package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"unicode/utf8"
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
	if plainSchemaValue(schema, 0) {
		if len(schema) == 0 {
			return "$: the schema is empty"
		}
		if kind, _ := schema["type"].(string); kind != "object" {
			return `$: the root must be exactly "type": "object" (wrap an array, a scalar or an optional value in an object)`
		}
		// Successful schemas are the hot path. Walk without constructing a path
		// for every property; only a refusal needs those strings.
		if problem := strictProblemAt(schema, ""); problem == "" {
			return ""
		}
		return strictProblemAt(schema, "$")
	}

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

// plainSchemaValue recognises schemas whose Go representation is already the
// JSON shape strictProblemAt reads. Other values still take the JSON round trip:
// custom marshalers, typed maps and slices, numbers and invalid UTF-8 may change
// shape or spelling when encoded. The depth bound sends cyclic maps and unusually
// deep schemas to encoding/json, which reports its own error for either case.
func plainSchemaValue(value any, depth int) bool {
	if depth > 100 {
		return false
	}
	switch v := value.(type) {
	case nil, bool:
		return true
	case string:
		return utf8.ValidString(v)
	case []string:
		for _, item := range v {
			if !utf8.ValidString(item) {
				return false
			}
		}
		return true
	case []any:
		for _, item := range v {
			if !plainSchemaValue(item, depth+1) {
				return false
			}
		}
		return true
	case map[string]any:
		for key, item := range v {
			if !utf8.ValidString(key) || !plainSchemaValue(item, depth+1) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func strictProblemAt(node any, path string) string {
	switch typed := node.(type) {
	case map[string]any:
		properties, hasProperties := typed["properties"].(map[string]any)
		keys := sortedSchemaKeys(properties)
		if hasProperties || schemaTypeIncludes(typed["type"], "object") {
			if additional, isBool := typed["additionalProperties"].(bool); !isBool || additional {
				return fmt.Sprintf("%s: an object must set additionalProperties to false", path)
			}

			required := map[string]bool{}
			switch list := typed["required"].(type) {
			case []string:
				for _, name := range list {
					required[name] = true
				}
			case []any:
				for _, name := range list {
					if s, ok := name.(string); ok {
						required[s] = true
					}
				}
			}
			for _, name := range keys {
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

		for _, name := range keys {
			child, isSchema := properties[name].(map[string]any)
			if !isSchema || len(child) == 0 {
				return fmt.Sprintf("%s: property %q has no schema", path, name)
			}
			childPath := ""
			if path != "" {
				childPath = path + ".properties." + name
			}
			if problem := strictProblemAt(child, childPath); problem != "" {
				return problem
			}
		}
		if items, ok := typed["items"]; ok {
			child, isSchema := items.(map[string]any)
			if !isSchema || len(child) == 0 {
				return fmt.Sprintf("%s: items has no schema", path)
			}
			childPath := ""
			if path != "" {
				childPath = path + ".items"
			}
			if problem := strictProblemAt(child, childPath); problem != "" {
				return problem
			}
		}
		for _, keyword := range []string{"anyOf", "oneOf", "allOf"} {
			if members, ok := typed[keyword].([]string); ok {
				if len(members) > 0 {
					return fmt.Sprintf("%s.%s[0]: a union member has no schema", path, keyword)
				}
				continue
			}
			members, ok := typed[keyword].([]any)
			if !ok {
				continue
			}
			for i, member := range members {
				child, isSchema := member.(map[string]any)
				if !isSchema || len(child) == 0 {
					return fmt.Sprintf("%s.%s[%d]: a union member has no schema", path, keyword, i)
				}
				childPath := ""
				if path != "" {
					childPath = fmt.Sprintf("%s.%s[%d]", path, keyword, i)
				}
				if problem := strictProblemAt(child, childPath); problem != "" {
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
				childPath := ""
				if path != "" {
					childPath = path + "." + keyword + "." + name
				}
				if problem := strictProblemAt(defs[name], childPath); problem != "" {
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
	case []string:
		for _, name := range typed {
			if name == want {
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
