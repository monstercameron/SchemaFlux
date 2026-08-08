package llm

import (
	"encoding/json"
	"testing"
)

// TestFillTemplateReplacesPlaceholders pins fillTemplate's per-JSON-type
// behaviour: empty/ellipsis strings become "mock", populated strings survive,
// nested maps and arrays recurse, an empty array stays empty (an honest
// answer -- it says nothing about its elements), and non-string scalars pass
// through unchanged.
func TestFillTemplateReplacesPlaceholders(t *testing.T) {
	input := map[string]any{
		"empty_string": "",
		"ellipsis":     "example...",
		"literal":      "keep me",
		"number":       float64(3),
		"boolean":      true,
		"null_value":   nil,
		"empty_array":  []any{},
		"filled_array": []any{"", "keep"},
		"nested": map[string]any{
			"inner": "",
		},
	}

	got := fillTemplate(input, 0).(map[string]any)

	if got["empty_string"] != "mock" {
		t.Errorf("empty_string = %v, want \"mock\"", got["empty_string"])
	}
	if got["ellipsis"] != "mock" {
		t.Errorf("ellipsis = %v, want \"mock\"", got["ellipsis"])
	}
	if got["literal"] != "keep me" {
		t.Errorf("literal = %v, want unchanged", got["literal"])
	}
	if got["number"] != float64(3) {
		t.Errorf("number = %v, want unchanged", got["number"])
	}
	if got["boolean"] != true {
		t.Errorf("boolean = %v, want unchanged", got["boolean"])
	}
	if got["null_value"] != nil {
		t.Errorf("null_value = %v, want nil", got["null_value"])
	}
	emptyArray, ok := got["empty_array"].([]any)
	if !ok || len(emptyArray) != 0 {
		t.Errorf("empty_array = %v, want an empty (not nil-typed) array", got["empty_array"])
	}
	filledArray, ok := got["filled_array"].([]any)
	if !ok || len(filledArray) != 2 || filledArray[0] != "mock" || filledArray[1] != "keep" {
		t.Errorf("filled_array = %v, want [mock keep]", got["filled_array"])
	}
	nested, ok := got["nested"].(map[string]any)
	if !ok || nested["inner"] != "mock" {
		t.Errorf("nested.inner = %v, want \"mock\"", got["nested"])
	}
}

// TestFillTemplateRespectsMaxDepth proves a template deeper than maxMockDepth
// is returned as-is at the point the recursion stops, rather than panicking
// or looping.
func TestFillTemplateRespectsMaxDepth(t *testing.T) {
	// Build a chain nested well past maxMockDepth.
	var deep any = ""
	for i := 0; i < maxMockDepth+5; i++ {
		deep = map[string]any{"next": deep}
	}
	// Must not panic, and must return something.
	got := fillTemplate(deep, 0)
	if got == nil {
		t.Error("fillTemplate returned nil for a deeply nested template")
	}
}

// TestValueForFormatKnownFormats proves every documented `format` annotation
// answers with a value that plausibly parses as that format, and that an
// unrecognised or blank format is declined rather than guessed at.
func TestValueForFormatKnownFormats(t *testing.T) {
	cases := []struct {
		format string
		want   string
	}{
		{"date-time", "2026-01-02T15:04:05Z"},
		{"date", "2026-01-02"},
		{"time", "15:04:05Z"},
		{"duration", "PT1H"},
		{"email", "mock@example.com"},
		{"idn-email", "mock@example.com"},
		{"hostname", "example.com"},
		{"idn-hostname", "example.com"},
		{"uri", "https://example.com/mock"},
		{"iri", "https://example.com/mock"},
		{"uri-reference", "https://example.com/mock"},
		{"uuid", "3f2504e0-4f89-41d3-9a0c-0305e82c3301"},
		{"ipv4", "192.0.2.1"},
		{"ipv6", "2001:db8::1"},
		// Case- and whitespace-insensitive, per the doc comment.
		{"  Date-Time  ", "2026-01-02T15:04:05Z"},
	}

	for _, tc := range cases {
		t.Run(tc.format, func(t *testing.T) {
			got, ok := valueForFormat(tc.format)
			if !ok {
				t.Fatalf("valueForFormat(%q) declined; want %q", tc.format, tc.want)
			}
			if got != tc.want {
				t.Errorf("valueForFormat(%q) = %q, want %q", tc.format, got, tc.want)
			}
		})
	}
}

// TestValueForFormatUnknownIsDeclined proves an annotation this layer does
// not recognise is declined (ok=false) rather than answered with a guess --
// the caller (valueForSchema) falls back to the generic "mock" string.
func TestValueForFormatUnknownIsDeclined(t *testing.T) {
	for _, format := range []string{"", "regex", "json-pointer", "byte"} {
		if _, ok := valueForFormat(format); ok {
			t.Errorf("valueForFormat(%q) was accepted; want it declined", format)
		}
	}
}

// TestValueForSchemaUsesFormatAnnotation proves a string schema carrying a
// recognised format is answered with a value of that shape, not the generic
// "mock" -- this is the branch valueForFormat feeds.
func TestValueForSchemaUsesFormatAnnotation(t *testing.T) {
	schema := map[string]any{"type": "string", "format": "email"}
	value, ok := valueForSchema(schema, 0)
	if !ok {
		t.Fatal("valueForSchema declined a well-formed string+format schema")
	}
	if value != "mock@example.com" {
		t.Errorf("value = %v, want the email-shaped mock value", value)
	}
}

// TestValueForSchemaEveryType exercises every case of valueForSchema's type
// switch, including the array (two-element, off-by-one-catching) and object
// (properties) recursion, and the depth guard.
func TestValueForSchemaEveryType(t *testing.T) {
	cases := []struct {
		name   string
		schema map[string]any
		check  func(t *testing.T, value any, ok bool)
	}{
		// Zero rather than one: several operations bounds-check an answer's
		// index fields against the caller's list, and one is out of range for
		// any single-element list. See valueForSchema's "integer" case.
		{"integer", map[string]any{"type": "integer"}, func(t *testing.T, v any, ok bool) {
			if !ok || v != 0 {
				t.Errorf("integer -> %v, %v; want 0, true", v, ok)
			}
		}},
		{"integer_with_minimum", map[string]any{"type": "integer", "minimum": 3.0}, func(t *testing.T, v any, ok bool) {
			if !ok || v != 3 {
				t.Errorf("integer with minimum 3 -> %v, %v; want 3, true", v, ok)
			}
		}},
		{"number", map[string]any{"type": "number"}, func(t *testing.T, v any, ok bool) {
			if !ok || v != 0.5 {
				t.Errorf("number -> %v, %v; want 0.5, true", v, ok)
			}
		}},
		{"boolean", map[string]any{"type": "boolean"}, func(t *testing.T, v any, ok bool) {
			if !ok || v != true {
				t.Errorf("boolean -> %v, %v; want true, true", v, ok)
			}
		}},
		{"null", map[string]any{"type": "null"}, func(t *testing.T, v any, ok bool) {
			if !ok || v != nil {
				t.Errorf("null -> %v, %v; want nil, true", v, ok)
			}
		}},
		{"string_enum", map[string]any{"type": "string", "enum": []any{"blue", "green"}}, func(t *testing.T, v any, ok bool) {
			if !ok || v != "blue" {
				t.Errorf("enum -> %v, %v; want the first enum value", v, ok)
			}
		}},
		{"string_plain", map[string]any{"type": "string"}, func(t *testing.T, v any, ok bool) {
			if !ok || v != "mock" {
				t.Errorf("string -> %v, %v; want \"mock\", true", v, ok)
			}
		}},
		{"array_of_strings", map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, func(t *testing.T, v any, ok bool) {
			arr, isArr := v.([]any)
			if !ok || !isArr || len(arr) != 2 {
				t.Fatalf("array -> %v, %v; want a two-element array", v, ok)
			}
			if arr[0] != "mock" || arr[1] != "mock" {
				t.Errorf("array elements = %v, want both \"mock\"", arr)
			}
		}},
		{"array_no_items", map[string]any{"type": "array"}, func(t *testing.T, v any, ok bool) {
			arr, isArr := v.([]any)
			if !ok || !isArr || len(arr) != 0 {
				t.Errorf("array with no items -> %v, %v; want an empty array", v, ok)
			}
		}},
		{"object", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
				"age":  map[string]any{"type": "integer"},
			},
		}, func(t *testing.T, v any, ok bool) {
			obj, isObj := v.(map[string]any)
			if !ok || !isObj {
				t.Fatalf("object -> %v, %v; want a map", v, ok)
			}
			if obj["name"] != "mock" || obj["age"] != 0 {
				t.Errorf("object properties = %v", obj)
			}
		}},
		{"object_bad_property_shape", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"weird": "not a schema map",
			},
		}, func(t *testing.T, v any, ok bool) {
			obj, isObj := v.(map[string]any)
			if !ok || !isObj {
				t.Fatalf("object -> %v, %v; want a map even with a malformed property", v, ok)
			}
			if _, present := obj["weird"]; present {
				t.Errorf("a non-map property schema produced a value: %v", obj)
			}
		}},
		{"unrecognised_type", map[string]any{"type": "unknown-type"}, func(t *testing.T, v any, ok bool) {
			if ok {
				t.Errorf("unrecognised type -> %v, %v; want declined", v, ok)
			}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value, ok := valueForSchema(tc.schema, 0)
			tc.check(t, value, ok)
		})
	}
}

// TestValueForSchemaRespectsMaxDepth proves recursion into a deeply nested
// object schema stops rather than overflowing the stack.
func TestValueForSchemaRespectsMaxDepth(t *testing.T) {
	var schema map[string]any = map[string]any{"type": "string"}
	for i := 0; i < maxMockDepth+5; i++ {
		schema = map[string]any{
			"type":       "object",
			"properties": map[string]any{"next": schema},
		}
	}
	// Must not panic.
	if _, ok := valueForSchema(schema, 0); !ok {
		t.Error("valueForSchema declined at the top level; want at least an empty object")
	}
}

// TestSchemaTypeVariants exercises schemaType's three representations of a
// type -- a plain string, a []any union (preferring the first non-null
// entry), and a []string union -- plus the no-type-key and unrecognised-value
// cases.
func TestSchemaTypeVariants(t *testing.T) {
	cases := []struct {
		name   string
		schema map[string]any
		want   string
	}{
		{"plain_string", map[string]any{"type": "string"}, "string"},
		{"any_union_null_first", map[string]any{"type": []any{"null", "integer"}}, "integer"},
		{"any_union_no_null", map[string]any{"type": []any{"boolean", "string"}}, "boolean"},
		{"string_slice_union", map[string]any{"type": []string{"null", "number"}}, "number"},
		{"all_null_any_union", map[string]any{"type": []any{"null"}}, ""},
		{"no_type_key", map[string]any{}, ""},
		{"unrecognised_value_shape", map[string]any{"type": 5}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := schemaType(tc.schema); got != tc.want {
				t.Errorf("schemaType(%v) = %q, want %q", tc.schema, got, tc.want)
			}
		})
	}
}

// TestIndexAnswerFillsIndicesAndGroups exercises indexAnswer's three branches:
// an "indices"/"indexes" field (all indices, or empty for an "outlier" field),
// a group-list field (isGroupList/groupWithIndices), and a plain field that
// falls through to fillTemplate. A template with none of these "touched"
// fields is declined.
func TestIndexAnswerFillsIndicesAndGroups(t *testing.T) {
	t.Run("plain indices field", func(t *testing.T) {
		shape := map[string]any{"kept_indices": []any{}}
		answer, ok := indexAnswer(shape, 3)
		if !ok {
			t.Fatal("indexAnswer declined a template with an indices field")
		}
		var decoded struct {
			KeptIndices []int `json:"kept_indices"`
		}
		if err := json.Unmarshal([]byte(answer), &decoded); err != nil {
			t.Fatalf("answer is not the declared shape: %v (%s)", err, answer)
		}
		if len(decoded.KeptIndices) != 3 {
			t.Errorf("kept_indices = %v, want all 3 indices", decoded.KeptIndices)
		}
	})

	t.Run("outlier indices field is left empty", func(t *testing.T) {
		shape := map[string]any{"outlier_indexes": []any{}}
		answer, ok := indexAnswer(shape, 3)
		if !ok {
			t.Fatal("indexAnswer declined a template with an outlier indices field")
		}
		var decoded struct {
			OutlierIndexes []int `json:"outlier_indexes"`
		}
		if err := json.Unmarshal([]byte(answer), &decoded); err != nil {
			t.Fatalf("answer is not the declared shape: %v (%s)", err, answer)
		}
		if len(decoded.OutlierIndexes) != 0 {
			t.Errorf("outlier_indexes = %v, want empty (an item is never both kept and an outlier)", decoded.OutlierIndexes)
		}
	})

	t.Run("group list field", func(t *testing.T) {
		shape := map[string]any{
			"groups": []any{
				map[string]any{"label": "", "member_indices": []any{}},
			},
		}
		answer, ok := indexAnswer(shape, 2)
		if !ok {
			t.Fatal("indexAnswer declined a template with a group-list field")
		}
		var decoded struct {
			Groups []struct {
				Label         string `json:"label"`
				MemberIndices []int  `json:"member_indices"`
			} `json:"groups"`
		}
		if err := json.Unmarshal([]byte(answer), &decoded); err != nil {
			t.Fatalf("answer is not the declared shape: %v (%s)", err, answer)
		}
		if len(decoded.Groups) != 1 {
			t.Fatalf("groups = %v, want exactly one group", decoded.Groups)
		}
		if decoded.Groups[0].Label != "mock" {
			t.Errorf("label = %q, want the filled template value", decoded.Groups[0].Label)
		}
		if len(decoded.Groups[0].MemberIndices) != 2 {
			t.Errorf("member_indices = %v, want both indices", decoded.Groups[0].MemberIndices)
		}
	})

	t.Run("a plain field alone is not touched, so the template is declined", func(t *testing.T) {
		// No key here matches "indices"/"indexes" and no value is a group
		// list, so nothing sets touched -- the whole point of indexAnswer is
		// filling index fields, and a template with none is not this
		// protocol.
		if _, ok := indexAnswer(map[string]any{"summary": ""}, 1); ok {
			t.Error("indexAnswer accepted a template with only a plain, non-index field")
		}
	})

	t.Run("a plain field alongside an indices field is still filled via fillTemplate", func(t *testing.T) {
		shape := map[string]any{"summary": "", "kept_indices": []any{}}
		answer, ok := indexAnswer(shape, 2)
		if !ok {
			t.Fatal("indexAnswer declined a template with both an indices field and a plain field")
		}
		var decoded struct {
			Summary     string `json:"summary"`
			KeptIndices []int  `json:"kept_indices"`
		}
		if err := json.Unmarshal([]byte(answer), &decoded); err != nil {
			t.Fatalf("answer is not the declared shape: %v (%s)", err, answer)
		}
		if decoded.Summary != "mock" {
			t.Errorf("summary = %q, want it filled via fillTemplate", decoded.Summary)
		}
		if len(decoded.KeptIndices) != 2 {
			t.Errorf("kept_indices = %v, want both indices", decoded.KeptIndices)
		}
	})

	t.Run("empty template is declined", func(t *testing.T) {
		if _, ok := indexAnswer(map[string]any{}, 1); ok {
			t.Error("indexAnswer accepted an empty template")
		}
	})
}

// TestIsGroupList proves the group-list shape detector accepts only a
// non-empty array whose first element is an object carrying an "indices"-like
// key, and rejects everything else.
func TestIsGroupList(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  bool
	}{
		{"well_formed", []any{map[string]any{"cluster_indices": []any{}}}, true},
		{"case_insensitive_key", []any{map[string]any{"ClusterIndices": []any{}}}, true},
		{"empty_list", []any{}, false},
		{"not_a_list", "nope", false},
		{"list_of_non_objects", []any{"a", "b"}, false},
		{"object_with_no_indices_key", []any{map[string]any{"label": "x"}}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isGroupList(tc.value); got != tc.want {
				t.Errorf("isGroupList(%v) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

// TestGroupWithIndices proves the indices-like key of the group template gets
// every index, and every other key is filled via fillTemplate.
func TestGroupWithIndices(t *testing.T) {
	value := []any{map[string]any{
		"item_indices": []any{},
		"label":        "",
		"score":        float64(0),
	}}
	group := groupWithIndices(value, []int{0, 1, 2})

	indices, ok := group["item_indices"].([]int)
	if !ok || len(indices) != 3 {
		t.Errorf("item_indices = %v, want all 3 indices", group["item_indices"])
	}
	if group["label"] != "mock" {
		t.Errorf("label = %v, want filled via fillTemplate", group["label"])
	}
	if group["score"] != float64(0) {
		t.Errorf("score = %v, want unchanged", group["score"])
	}
}

// TestLooksLikeItemShape proves the detector requires a JSONSchema whose
// every declared property is present on the candidate item, and declines a
// prompt-only request (no JSONSchema) or a non-object item outright.
func TestLooksLikeItemShape(t *testing.T) {
	schemaReq := CompletionRequest{JSONSchema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"subject": map[string]any{"type": "string"},
		},
	}}

	if !looksLikeItemShape(schemaReq, map[string]any{"subject": "outage", "extra": 1}) {
		t.Error("declined an item that carries every declared property")
	}
	if looksLikeItemShape(schemaReq, map[string]any{"unrelated": true}) {
		t.Error("accepted an item missing the declared property")
	}
	if looksLikeItemShape(schemaReq, "not an object") {
		t.Error("accepted a non-object item")
	}
	if looksLikeItemShape(CompletionRequest{}, map[string]any{"subject": "outage"}) {
		t.Error("accepted a request with no JSONSchema at all")
	}

	noPropsSchema := CompletionRequest{JSONSchema: map[string]any{"type": "object"}}
	if looksLikeItemShape(noPropsSchema, map[string]any{"subject": "outage"}) {
		t.Error("accepted a schema with no properties key")
	}
}

// TestMockCollectionResponseValueBasedContracts covers mockCollectionResponse's
// value-based branches: an array-declared response answers with the whole
// item list (identity), and an object-declared response whose shape matches
// an item answers with one item.
func TestMockCollectionResponseValueBasedContracts(t *testing.T) {
	t.Run("array declared via JSONSchema", func(t *testing.T) {
		req := CompletionRequest{
			JSONSchema: map[string]any{"type": "array"},
			UserPrompt: `Items:` + "\n" + `[{"subject":"a"},{"subject":"b"}]`,
		}
		answer, ok := mockCollectionResponse(req)
		if !ok {
			t.Fatal("declined an array-declared collection request")
		}
		var items []map[string]any
		if err := json.Unmarshal([]byte(answer), &items); err != nil {
			t.Fatalf("answer is not a JSON array: %v (%s)", err, answer)
		}
		if len(items) != 2 {
			t.Errorf("items = %v, want both input items back (identity)", items)
		}
	})

	t.Run("object shape matching one item", func(t *testing.T) {
		req := CompletionRequest{
			JSONSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"subject": map[string]any{"type": "string"}},
			},
			UserPrompt: `Items:` + "\n" + `[{"subject":"a"},{"subject":"b"}]`,
		}
		answer, ok := mockCollectionResponse(req)
		if !ok {
			t.Fatal("declined an object-declared choose-shaped request")
		}
		var item map[string]any
		if err := json.Unmarshal([]byte(answer), &item); err != nil {
			t.Fatalf("answer is not a JSON object: %v (%s)", err, answer)
		}
		if item["subject"] != "a" {
			t.Errorf("item = %v, want the first offered item", item)
		}
	})

	t.Run("declares nothing this layer can read, still answers one item", func(t *testing.T) {
		req := CompletionRequest{
			UserPrompt: `Items:` + "\n" + `[{"subject":"a"},{"subject":"b"}]`,
		}
		answer, ok := mockCollectionResponse(req)
		if !ok {
			t.Fatal("declined a request with no schema and no template")
		}
		var item map[string]any
		if err := json.Unmarshal([]byte(answer), &item); err != nil {
			t.Fatalf("answer is not a JSON object: %v (%s)", err, answer)
		}
		if item["subject"] != "a" {
			t.Errorf("item = %v, want the first offered item as the last-resort guess", item)
		}
	})

	t.Run("no array in the prompt at all", func(t *testing.T) {
		if _, ok := mockCollectionResponse(CompletionRequest{UserPrompt: "no items here"}); ok {
			t.Error("accepted a prompt with no JSON array")
		}
	})

	t.Run("empty array in the prompt", func(t *testing.T) {
		if _, ok := mockCollectionResponse(CompletionRequest{UserPrompt: "Items:\n[]"}); ok {
			t.Error("accepted a prompt whose array is empty")
		}
	})
}

// TestExpectsArrayAndExpectsObject cover the prompt-text fallback branches
// (no JSONSchema) that TestMockCollectionResponseValueBasedContracts's
// schema-driven cases do not reach.
func TestExpectsArrayAndExpectsObject(t *testing.T) {
	if !expectsArray(CompletionRequest{SystemPrompt: "Return a JSON array of ids."}) {
		t.Error("expectsArray declined a prompt saying \"Return a JSON array\"")
	}
	if !expectsArray(CompletionRequest{SystemPrompt: "please return a JSON array here"}) {
		t.Error("expectsArray declined the lowercase phrasing")
	}
	if expectsArray(CompletionRequest{SystemPrompt: "Return a JSON object."}) {
		t.Error("expectsArray accepted a prompt with no array phrasing")
	}

	if !expectsObject(CompletionRequest{SystemPrompt: "no template here"}) {
		t.Error("expectsObject declined a prompt with no JSON object template at all")
	}
	if expectsObject(CompletionRequest{SystemPrompt: `Return {"verdict":"..."}`}) {
		t.Error("expectsObject accepted a prompt that does carry an object template")
	}
}

// TestFirstJSONObjectStringAndEscapeAware proves firstJSONObject does not end
// its scan on a brace that appears inside a quoted string, including an
// escaped quote.
func TestFirstJSONObjectStringAndEscapeAware(t *testing.T) {
	text := `prose {"note": "a brace \"}\" inside a string", "n": 1} trailing`
	obj, ok := firstJSONObject(text)
	if !ok {
		t.Fatal("firstJSONObject found nothing")
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(obj), &decoded); err != nil {
		t.Fatalf("the extracted object is not valid JSON: %v (%s)", err, obj)
	}
	if decoded["n"] != float64(1) {
		t.Errorf("decoded = %v, want n=1 -- the scan ended too early", decoded)
	}
}

// TestFirstJSONObjectNoBrace proves a prompt without any '{' is declined.
func TestFirstJSONObjectNoBrace(t *testing.T) {
	if _, ok := firstJSONObject("nothing to see here"); ok {
		t.Error("firstJSONObject found an object in text with no brace")
	}
}

// TestMockShapedResponseDeclaringNothingReturnsEmpty proves a request with no
// JSONSchema, no JSON template in its system prompt, and no array in its
// user prompt is answered with "" -- the signal mockResponse's own caller
// uses to fall back to the keyword-guessing behaviour.
func TestMockShapedResponseDeclaringNothingReturnsEmpty(t *testing.T) {
	got := mockShapedResponse(CompletionRequest{UserPrompt: "just some prose", SystemPrompt: "just some prose"})
	if got != "" {
		t.Errorf("mockShapedResponse(nothing declared) = %q, want \"\"", got)
	}
}

// TestMockShapedResponseSkipsAnUnparsableCandidateAndUsesTheNextOne proves
// jsonObjectCandidates' brace scanner is purely syntactic (unlike
// firstJSONObject, it is not string/escape aware), so a balanced-but-invalid
// JSON run is skipped rather than aborting the whole search -- a later,
// well-formed candidate is still used.
func TestMockShapedResponseSkipsAnUnparsableCandidateAndUsesTheNextOne(t *testing.T) {
	system := `Example shape: {not valid json at all} ` +
		`Return a JSON object with: {"verdict": "...", "confidence": 0.0}`

	got := mockShapedResponse(CompletionRequest{SystemPrompt: system})
	if got == "" {
		t.Fatal("mockShapedResponse found nothing despite a well-formed later candidate")
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("answer is not valid JSON: %v (%s)", err, got)
	}
	if _, present := decoded["verdict"]; !present {
		t.Errorf("decoded = %v, want the verdict template's shape", decoded)
	}
}

// TestMockShapedResponseUsesJSONSchemaOverTemplate proves a request that
// declares a JSONSchema is answered from the schema, never falling through
// to a JSON template in the system prompt.
func TestMockShapedResponseUsesJSONSchemaOverTemplate(t *testing.T) {
	req := CompletionRequest{
		JSONSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"queue": map[string]any{"type": "string"}},
		},
		SystemPrompt: `Return a JSON object with: {"unrelated_template_field": "..."}`,
	}
	got := mockShapedResponse(req)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("answer is not valid JSON: %v (%s)", err, got)
	}
	if _, present := decoded["queue"]; !present {
		t.Errorf("decoded = %v, want the schema's shape, not the template's", decoded)
	}
	if _, present := decoded["unrelated_template_field"]; present {
		t.Errorf("decoded = %v, the template was used despite a JSONSchema being present", decoded)
	}
}
