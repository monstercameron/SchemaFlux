package wirecontract

import (
	"fmt"
	"strings"
	"testing"
)

// bodyProblems applies the constraints learned live to one request body.
//
// The schema rules are restated here on purpose and do not call the library's
// own validator. The provider uses that validator to decide what to refuse, so
// a rule it does not know would pass both it and a test built on it.
func bodyProblems(body map[string]any) []string {
	var problems []string

	if _, has := body["temperature"]; has {
		problems = append(problems, fmt.Sprintf("sends temperature to %v, which rejects it", body["model"]))
	}

	if key, ok := body["prompt_cache_key"].(string); ok && len(key) > 64 {
		problems = append(problems, fmt.Sprintf("prompt_cache_key is %d bytes; the API limit is 64", len(key)))
	}

	// A reasoning effort is not negotiated either: the API fails the request
	// on a word it does not know. These are the words it named when it
	// rejected one, on 2026-10-01. None of these cases sets an effort of its
	// own, so any that is on the wire is an operation's preference, and a
	// preference is sent only to the model it was measured on. A caller's own
	// value would be passed to an unknown model as given (LX-105).
	if reasoning, ok := body["reasoning"].(map[string]any); ok {
		effort, _ := reasoning["effort"].(string)
		model, _ := body["model"].(string)
		accepted := map[string]bool{"none": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true}
		switch {
		case !strings.HasPrefix(model, "gpt-6"):
			problems = append(problems, fmt.Sprintf("sends reasoning effort %q to %v, whose accepted efforts are not known", effort, model))
		case !accepted[effort]:
			problems = append(problems, fmt.Sprintf("sends reasoning effort %q, which %v rejects", effort, model))
		}
	}

	if text, ok := body["text"].(map[string]any); ok {
		if format, ok := text["format"].(map[string]any); ok && format["type"] == "json_schema" {
			schema, _ := format["schema"].(map[string]any)
			if schema["type"] != "object" {
				problems = append(problems, fmt.Sprintf("schema %v has root type %v; the API requires an object", format["name"], schema["type"]))
			}
			for _, problem := range openObjects(schema, "$") {
				problems = append(problems, fmt.Sprintf("schema %v is sent as strict: %s", format["name"], problem))
			}
		}
	}
	return problems
}

// openObjects walks a decoded JSON schema and reports every object that is not
// closed, every property missing from required, every required name that is not
// a property, and every place a schema should be and is not.
func openObjects(node any, path string) []string {
	var problems []string
	schema, ok := node.(map[string]any)
	if !ok || len(schema) == 0 {
		return []string{path + " is not a schema"}
	}

	isObject := schema["type"] == "object"
	if list, ok := schema["type"].([]any); ok {
		for _, name := range list {
			if name == "object" {
				isObject = true
			}
		}
	}
	properties, hasProperties := schema["properties"].(map[string]any)
	if isObject || hasProperties {
		if schema["additionalProperties"] != false {
			problems = append(problems, path+" does not set additionalProperties to false")
		}
		required := map[string]bool{}
		list, _ := schema["required"].([]any)
		for _, name := range list {
			required[fmt.Sprint(name)] = true
		}
		for name := range properties {
			if !required[name] {
				problems = append(problems, path+" does not list "+name+" in required")
			}
		}
		for name := range required {
			if _, declared := properties[name]; !declared {
				problems = append(problems, path+" requires "+name+", which is not a property")
			}
		}
	}

	for name, child := range properties {
		problems = append(problems, openObjects(child, path+"."+name)...)
	}
	if items, ok := schema["items"]; ok {
		problems = append(problems, openObjects(items, path+"[]")...)
	}
	for _, keyword := range []string{"anyOf", "oneOf", "allOf"} {
		if members, ok := schema[keyword].([]any); ok {
			for i, member := range members {
				problems = append(problems, openObjects(member, fmt.Sprintf("%s.%s[%d]", path, keyword, i))...)
			}
		}
	}
	return problems
}

// The helper is only as good as its own judgement of a body, so it is checked
// against bodies whose verdict is known.
func TestBodyProblems(t *testing.T) {
	strict := func(schema map[string]any) map[string]any {
		return map[string]any{"format": map[string]any{"type": "json_schema", "name": "x", "strict": true, "schema": schema}}
	}
	closed := map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}, "required": []any{"a"}, "additionalProperties": false}
	open := map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}, "required": []any{"a"}}

	array := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	ghost := map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}, "required": []any{"a", "ghost"}, "additionalProperties": false}
	nullProperty := map[string]any{"type": "object", "properties": map[string]any{"a": nil}, "required": []any{"a"}, "additionalProperties": false}
	nested := map[string]any{"type": "object", "properties": map[string]any{"inner": open}, "required": []any{"inner"}, "additionalProperties": false}

	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"clean", map[string]any{"model": "gpt-6-luna"}, 0},
		{"temperature", map[string]any{"model": "gpt-6-luna", "temperature": 0.3}, 1},
		{"temperature zero is still the key", map[string]any{"temperature": 0.0}, 1},
		{"key at the limit", map[string]any{"prompt_cache_key": strings.Repeat("k", 64)}, 0},
		{"key over the limit", map[string]any{"prompt_cache_key": strings.Repeat("k", 65)}, 1},
		{"an effort gpt-6 takes", map[string]any{"model": "gpt-6-luna", "reasoning": map[string]any{"effort": "none"}}, 0},
		{"another effort gpt-6 takes", map[string]any{"model": "gpt-6.1-sol", "reasoning": map[string]any{"effort": "xhigh"}}, 0},
		{"an effort gpt-6 rejects", map[string]any{"model": "gpt-6-luna", "reasoning": map[string]any{"effort": "minimal"}}, 1},
		{"an empty effort", map[string]any{"model": "gpt-6-luna", "reasoning": map[string]any{}}, 1},
		{"an effort sent to a model nothing is known about", map[string]any{"model": "a-new-model", "reasoning": map[string]any{"effort": "low"}}, 1},
		{"strict-valid schema", map[string]any{"text": strict(closed)}, 0},
		{"open schema", map[string]any{"text": strict(open)}, 1},
		{"open schema nested in a property", map[string]any{"text": strict(nested)}, 1},
		{"array root", map[string]any{"text": strict(array)}, 1},
		{"required names a property that does not exist", map[string]any{"text": strict(ghost)}, 1},
		{"a property with no schema", map[string]any{"text": strict(nullProperty)}, 1},
		{"json_object carries no schema", map[string]any{"text": map[string]any{"format": map[string]any{"type": "json_object"}}}, 0},
		{"all three at once", map[string]any{"temperature": 0.1, "prompt_cache_key": strings.Repeat("k", 100), "text": strict(open)}, 3},
		{"text block without a format", map[string]any{"text": map[string]any{"verbosity": "low"}}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := bodyProblems(tc.body); len(got) != tc.want {
				t.Fatalf("found %d problem(s) %v, want %d", len(got), got, tc.want)
			}
		})
	}
}
