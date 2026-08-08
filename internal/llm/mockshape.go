package llm

import (
	"encoding/json"
	"strings"
)

// A local provider that answers with the right shape.
//
// The old mock guessed from keywords in the prompt and answered
// `Mock response for: ...` when it did not recognise one -- which satisfies no
// JSON contract, so nineteen of the forty-five numbered examples failed under
// it. Those failures said nothing about the examples; they were the mock
// failing to be a provider.
//
// It does not need to guess any more. Every structured operation tells the
// provider what it wants, in one of two ways: a JSON Schema on the request
// (which the strict-output path has sent since P-005), or a JSON template
// embedded in the system prompt, which is how the prompt-only operations state
// their contract. Both are machine-readable, and a shape-correct answer is what
// an example needs -- an example is checking that the plumbing works, not that
// the model is clever.
//
// What this deliberately does not do is pretend to be a model. The values are
// obviously synthetic. Anything that needs a *particular* answer should script
// it with schemafluxtest, which exists for that.

// mockShapedResponse builds a response matching whatever the request declared,
// or returns "" when the request declared nothing.
func mockShapedResponse(req CompletionRequest) string {
	// A collection operation's contract is relational: Choose must return one of
	// the items it was given, Filter a subset, Sort a permutation, Cluster a
	// partition. A shape-correct answer is not enough -- invented items fail
	// those checks, correctly, which is what the invariants are for.
	//
	// So the mock answers with the identity transformation, which satisfies
	// every one of them by construction: the same items, in the same order, in
	// one group. That is a legitimate answer to "sort these" and "filter these",
	// and it is the right instinct for a double whose job is to exercise
	// plumbing rather than to be clever.
	if answer, ok := mockCollectionResponse(req); ok {
		return answer
	}

	if req.JSONSchema != nil {
		if value, ok := valueForSchema(req.JSONSchema, 0); ok {
			if encoded, err := json.Marshal(value); err == nil {
				return string(encoded)
			}
		}
	}

	// The prompt-only operations state their contract as a JSON template in the
	// system prompt: `Return a JSON object with: { "verdict": "...", ... }`.
	//
	// Every candidate object is tried, not just the first. These prompts
	// routinely embed the caller's *type schema* before the response template
	// -- "Each part should match this schema: {...}. Return a JSON object with:
	// {...}" -- so taking the first object answered with the shape of the
	// input instead of the shape of the answer, and taking only the last would
	// break the prompts that put their template first. Trying each and keeping
	// the first that parses and yields an object is what handles both.
	for _, template := range jsonObjectCandidates(req.SystemPrompt) {
		var shape any
		if err := json.Unmarshal([]byte(template), &shape); err != nil {
			continue
		}
		if _, isObject := shape.(map[string]any); !isObject {
			continue
		}
		if encoded, err := json.Marshal(fillTemplate(shape, 0)); err == nil {
			return string(encoded)
		}
	}

	return ""
}

// jsonObjectCandidates returns every balanced top-level {...} run in text, in
// the order they appear, last first.
//
// Last first because a response template is conventionally stated after the
// schema it refers to, so the later object is the likelier answer shape. The
// earlier ones are still tried, because some prompts state the template up
// front.
func jsonObjectCandidates(text string) []string {
	var found []string

	for offset := 0; offset < len(text); {
		start := strings.IndexByte(text[offset:], '{')
		if start < 0 {
			break
		}
		start += offset

		depth := 0
		end := -1
		for i := start; i < len(text); i++ {
			switch text[i] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					end = i
				}
			}
			if end >= 0 {
				break
			}
		}
		if end < 0 {
			break
		}

		found = append(found, text[start:end+1])
		offset = end + 1
	}

	// Reverse: later objects first.
	for i, j := 0, len(found)-1; i < j; i, j = i+1, j-1 {
		found[i], found[j] = found[j], found[i]
	}
	return found
}

const maxMockDepth = 8

// valueForSchema produces a value satisfying a JSON Schema fragment.
func valueForSchema(schema map[string]any, depth int) (any, bool) {
	if depth > maxMockDepth {
		return nil, false
	}

	switch schemaType(schema) {
	case "object":
		properties, _ := schema["properties"].(map[string]any)
		object := map[string]any{}
		for name, raw := range properties {
			property, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if value, ok := valueForSchema(property, depth+1); ok {
				object[name] = value
			}
		}
		return object, true

	case "array":
		items, ok := schema["items"].(map[string]any)
		if !ok {
			return []any{}, true
		}
		element, ok := valueForSchema(items, depth+1)
		if !ok {
			return []any{}, true
		}
		// Two elements rather than one: a single-element array hides an
		// off-by-one in anything that merges or indexes.
		return []any{element, element}, true

	case "string":
		if enum, ok := schema["enum"].([]any); ok && len(enum) > 0 {
			return enum[0], true
		}
		// A format annotation is a promise about the string's shape, and the
		// decoder on the other side enforces it: a time.Time field carries
		// `format: date-time`, and answering "mock" fails to parse. The
		// annotation is there precisely so this does not have to be guessed.
		if format, ok := schema["format"].(string); ok {
			if value, ok := valueForFormat(format); ok {
				return value, true
			}
		}
		return "mock", true

	case "integer":
		return 1, true

	case "number":
		return 0.5, true

	case "boolean":
		return true, true

	case "null":
		return nil, true
	}

	return nil, false
}

// schemaType reads the type, preferring the first non-null entry of a union --
// which is how an optional field is spelled in strict mode.
func schemaType(schema map[string]any) string {
	raw, ok := schema["type"]
	if !ok {
		return ""
	}

	switch typed := raw.(type) {
	case string:
		return typed
	case []any:
		for _, entry := range typed {
			if name, ok := entry.(string); ok && name != "null" {
				return name
			}
		}
	case []string:
		for _, name := range typed {
			if name != "null" {
				return name
			}
		}
	}
	return ""
}

// fillTemplate replaces the placeholder values in a prompt's JSON template with
// values of the same JSON type, keeping the structure.
//
// The templates in this library are written as examples -- `"confidence": 0.0`,
// `"issues": []` -- so the types are already right and the work is filling the
// empty containers, which is what an operation reading the response needs.
func fillTemplate(value any, depth int) any {
	if depth > maxMockDepth {
		return value
	}

	switch typed := value.(type) {
	case map[string]any:
		filled := make(map[string]any, len(typed))
		for key, entry := range typed {
			filled[key] = fillTemplate(entry, depth+1)
		}
		return filled

	case []any:
		if len(typed) == 0 {
			// An empty array in a template says nothing about its elements, and
			// an empty array is a valid answer for every operation that has
			// one. Leaving it empty is the honest choice.
			return []any{}
		}
		filled := make([]any, 0, len(typed))
		for _, entry := range typed {
			filled = append(filled, fillTemplate(entry, depth+1))
		}
		return filled

	case string:
		if typed == "" || strings.Contains(typed, "...") {
			return "mock"
		}
		return typed

	default:
		return value
	}
}

// firstJSONObject finds the first balanced JSON object in a prompt, string- and
// escape-aware so a brace inside a quoted example cannot end the scan early.
func firstJSONObject(text string) (string, bool) {
	start := strings.IndexByte(text, '{')
	if start < 0 {
		return "", false
	}

	depth := 0
	inString := false
	escaped := false

	for i := start; i < len(text); i++ {
		c := text[i]

		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && inString {
			escaped = true
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}

		switch c {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[start : i+1], true
			}
		}
	}

	return "", false
}

// valueForFormat answers a JSON Schema `format` annotation with something that
// parses as what it claims to be.
func valueForFormat(format string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "date-time":
		return "2026-01-02T15:04:05Z", true
	case "date":
		return "2026-01-02", true
	case "time":
		return "15:04:05Z", true
	case "duration":
		return "PT1H", true
	case "email", "idn-email":
		return "mock@example.com", true
	case "hostname", "idn-hostname":
		return "example.com", true
	case "uri", "iri", "uri-reference":
		return "https://example.com/mock", true
	case "uuid":
		return "3f2504e0-4f89-41d3-9a0c-0305e82c3301", true
	case "ipv4":
		return "192.0.2.1", true
	case "ipv6":
		return "2001:db8::1", true
	}
	return "", false
}

// mockCollectionResponse answers the operations whose contract refers back to
// their input, using the input itself.
func mockCollectionResponse(req CompletionRequest) (string, bool) {
	items, ok := lastJSONArray(req.UserPrompt)
	if !ok || len(items) == 0 {
		return "", false
	}

	// Id-based contracts first, because they are the most specific thing this
	// function can recognise: OP-101 moved Choose, Filter, and Sort onto
	// invocation-local ids, so the items arrive tagged and the answer names ids
	// instead of echoing items back. This layer answered the old value-based
	// protocol and kept passing its own tests while every example that runs
	// without a credential failed, which is why the examples gate is separate
	// from `go test`.
	if answer, ok := idAnswer(req, items); ok {
		return answer, true
	}

	// Index-based contracts: the prompt's template names an indices field, and
	// one group holding every index is a valid partition and a valid selection.
	if template, ok := firstJSONObject(req.SystemPrompt); ok {
		var shape map[string]any
		if err := json.Unmarshal([]byte(template), &shape); err == nil {
			if answer, ok := indexAnswer(shape, len(items)); ok {
				return answer, true
			}
		}
	}

	// Value-based contracts. An array response means "these items"; the
	// identity is a permutation and a subset at once.
	if expectsArray(req) {
		if encoded, err := json.Marshal(items); err == nil {
			return string(encoded), true
		}
	}

	// A single-object response from a list of items means "one of these".
	if expectsObject(req) && looksLikeItemShape(req, items[0]) {
		if encoded, err := json.Marshal(items[0]); err == nil {
			return string(encoded), true
		}
	}

	// The prompt carried items and declared nothing this layer could read. One
	// of the items is still a better guess than the keyword fallback below,
	// which answers `{"result": ..., "success": true}` -- a shape no operation
	// in this library asks for. If the operation wanted a verdict rather than an
	// item, this fails its field check exactly as the keyword answer does, so
	// the guess costs nothing and sometimes wins.
	if !expectsArray(req) {
		if _, ok := firstJSONObject(req.SystemPrompt); !ok {
			if encoded, err := json.Marshal(items[0]); err == nil {
				return string(encoded), true
			}
		}
	}

	return "", false
}

// idAnswer answers the id-based collection contract. Every id is a valid
// answer for Filter (a subset may be the whole set) and for Sort (every id
// exactly once is a permutation); the first id is a valid answer for Choose.
// The point is a shaped answer that satisfies the Go-side invariant, not a
// good one -- a mock that produced a semantically clever answer would hide
// the invariant checks these operations exist to run.
func idAnswer(req CompletionRequest, items []any) (string, bool) {
	ids, ok := taggedIDs(items)
	if !ok {
		return "", false
	}

	// Which field is asked for is read off the prompt rather than a template,
	// because the first JSON object in these prompts is a worked example, not
	// the response shape.
	switch {
	case strings.Contains(req.SystemPrompt, `"ids"`):
		encoded, err := json.Marshal(map[string]any{"ids": ids})
		return string(encoded), err == nil
	case strings.Contains(req.SystemPrompt, `"id"`):
		encoded, err := json.Marshal(map[string]any{"id": ids[0]})
		return string(encoded), err == nil
	}
	return "", false
}

// taggedIDs reports the ids of a tagged item list, and false for anything that
// is not one. Every element has to be tagged: a partially tagged array is not
// this protocol, and guessing at it would answer with ids that do not cover
// the input.
func taggedIDs(items []any) ([]string, bool) {
	ids := make([]string, 0, len(items))
	for _, raw := range items {
		object, isObject := raw.(map[string]any)
		if !isObject {
			return nil, false
		}
		id, isString := object["id"].(string)
		if !isString || id == "" {
			return nil, false
		}
		if _, hasItem := object["item"]; !hasItem {
			return nil, false
		}
		ids = append(ids, id)
	}
	return ids, len(ids) > 0
}

// indexAnswer fills a template whose fields are index lists, putting every
// index in the first group so the answer covers the input exactly once.
func indexAnswer(shape map[string]any, count int) (string, bool) {
	all := make([]int, count)
	for i := range all {
		all[i] = i
	}

	filled := map[string]any{}
	touched := false

	for key, value := range shape {
		lower := strings.ToLower(key)
		switch {
		case strings.Contains(lower, "indices") || strings.Contains(lower, "indexes"):
			// Outliers get none of them: an item is in a cluster or it is an
			// outlier, and putting it in both is the overlap the partition
			// check exists to reject.
			if strings.Contains(lower, "outlier") {
				filled[key] = []int{}
			} else {
				filled[key] = all
			}
			touched = true
		case isGroupList(value):
			filled[key] = []any{groupWithIndices(value, all)}
			touched = true
		default:
			filled[key] = fillTemplate(value, 0)
		}
	}

	if !touched {
		return "", false
	}
	encoded, err := json.Marshal(filled)
	return string(encoded), err == nil
}

// isGroupList reports whether a template value is a list of objects carrying an
// indices field -- the shape a clustering answer has.
func isGroupList(value any) bool {
	list, ok := value.([]any)
	if !ok || len(list) == 0 {
		return false
	}
	object, ok := list[0].(map[string]any)
	if !ok {
		return false
	}
	for key := range object {
		if strings.Contains(strings.ToLower(key), "indices") {
			return true
		}
	}
	return false
}

func groupWithIndices(value any, all []int) map[string]any {
	list, _ := value.([]any)
	template, _ := list[0].(map[string]any)

	group := map[string]any{}
	for key, entry := range template {
		if strings.Contains(strings.ToLower(key), "indices") {
			group[key] = all
			continue
		}
		group[key] = fillTemplate(entry, 0)
	}
	return group
}

// expectsArray reports whether the declared response is a JSON array.
func expectsArray(req CompletionRequest) bool {
	if req.JSONSchema != nil {
		return schemaType(req.JSONSchema) == "array"
	}
	// A prompt that shows its example as an array rather than an object.
	return strings.Contains(req.SystemPrompt, "Return a JSON array") ||
		strings.Contains(req.SystemPrompt, "return a JSON array")
}

func expectsObject(req CompletionRequest) bool {
	if req.JSONSchema != nil {
		return schemaType(req.JSONSchema) == "object"
	}
	_, ok := firstJSONObject(req.SystemPrompt)
	return !ok
}

// looksLikeItemShape reports whether the declared response resembles one of the
// input items, which is what distinguishes "return one of these" from "return a
// verdict about these".
func looksLikeItemShape(req CompletionRequest, item any) bool {
	object, ok := item.(map[string]any)
	if !ok {
		return false
	}

	if req.JSONSchema != nil {
		properties, ok := req.JSONSchema["properties"].(map[string]any)
		if !ok {
			return false
		}
		for name := range properties {
			if _, present := object[name]; !present {
				return false
			}
		}
		return len(properties) > 0
	}

	return false
}

// lastJSONArray finds the last balanced JSON array in a prompt and decodes it.
//
// The last one, because the operations put their instructions and any example
// first and the caller's data last -- so the final array is the items, not an
// illustration of the format.
func lastJSONArray(text string) ([]any, bool) {
	for start := len(text) - 1; start >= 0; start-- {
		if text[start] != '[' {
			continue
		}
		candidate, ok := balancedFrom(text, start, '[', ']')
		if !ok {
			continue
		}
		var items []any
		if err := json.Unmarshal([]byte(candidate), &items); err == nil && len(items) > 0 {
			return items, true
		}
	}
	return nil, false
}

func balancedFrom(text string, start int, open, close byte) (string, bool) {
	depth := 0
	inString := false
	escaped := false

	for i := start; i < len(text); i++ {
		c := text[i]

		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && inString {
			escaped = true
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}

		switch c {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return text[start : i+1], true
			}
		}
	}
	return "", false
}

// ShapedMockResponse exposes the shaping to schemafluxtest, whose Shaped mode
// answers from the request's declared shape when a test has scripted nothing.
//
// It is exported from an internal package, so it reaches the test double inside
// this module and no further.
func ShapedMockResponse(req CompletionRequest) string {
	return mockShapedResponse(req)
}
