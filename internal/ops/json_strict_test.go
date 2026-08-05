package ops

import (
	"reflect"
	"strings"
	"testing"
)

type verdict struct {
	Valid      bool     `json:"valid"`
	Reasons    []string `json:"reasons"`
	Confidence float64  `json:"confidence,omitempty"`
	Internal   string   `json:"-"`
	unexported string
}

// encoding/json ignores fields it does not recognise, so a well-formed object
// of entirely the wrong shape unmarshals into a zero value with no error. Every
// operation that only called ParseJSON therefore reported success with every
// field empty.
func TestParseJSONStrictRejectsAnAnswerAboutSomethingElse(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"unrelated_object", `{"unrelated": [1,2,3], "shape": "wrong"}`},
		{"empty_object", `{}`},
		{"error_envelope", `{"error": "rate limited", "retry_after": 30}`},
		{"different_operation", `{"clusters": [], "cluster_count": 0}`},
		{"echo_of_the_request", `{"input": "the original text", "model": "gpt-5.6-luna"}`},
		{"provider_metadata", `{"id": "resp_123", "object": "response", "created": 1}`},
		{"nested_only", `{"data": {"valid": true}}`},
		{"near_miss_name", `{"validity": true}`},
		{"excluded_field_only", `{"Internal": "x"}`},
		{"unexported_field_only", `{"unexported": "x"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var target verdict
			if err := ParseJSONStrict(tc.body, &target); err == nil {
				t.Fatalf("a body carrying none of the expected fields must be reported; parsed into %+v", target)
			}
			// The error names shapes, not values.
			var alsoTarget verdict
			err := ParseJSONStrict(tc.body, &alsoTarget)
			if strings.Contains(err.Error(), "the original text") {
				t.Errorf("the error reproduces the payload: %v", err)
			}
		})
	}
}

// One recognised field is enough. The rule has to be weak, or a model that
// omits an optional field is treated as a failure.
func TestParseJSONStrictAcceptsAPartialAnswer(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"all_fields", `{"valid": true, "reasons": ["a"], "confidence": 0.9}`},
		{"one_field", `{"valid": false}`},
		{"one_field_plus_extras", `{"valid": true, "model": "gpt-5.6-luna", "latency_ms": 12}`},
		{"optional_field_only", `{"confidence": 0.5}`},
		{"case_insensitive", `{"VALID": true}`},
		{"fenced", "```json\n{\"valid\": true}\n```"},
		{"fenced_no_language", "```\n{\"valid\": true}\n```"},
		{"whitespace_around", "  \n {\"valid\": true}  \n "},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var target verdict
			if err := ParseJSONStrict(tc.body, &target); err != nil {
				t.Fatalf("a body carrying an expected field must be accepted: %v", err)
			}
		})
	}
}

// A body that is not JSON at all is still ParseJSON's error, unchanged.
func TestParseJSONStrictKeepsTheParseError(t *testing.T) {
	for _, body := range []string{
		"I'm sorry, I can't help with that.",
		"<html>502</html>",
		`{"valid": tr`,
		"",
		"   ",
	} {
		t.Run(body, func(t *testing.T) {
			var target verdict
			err := ParseJSONStrict(body, &target)
			if err == nil {
				t.Fatal("an unparseable body must be reported")
			}
			if !strings.Contains(err.Error(), "unmarshal") {
				t.Errorf("the parse error should be reported as such, got %v", err)
			}
		})
	}
}

// Targets with no field names to match are passed through: the field check has
// nothing to say about a slice, a map, or a scalar.
func TestParseJSONStrictIgnoresTargetsWithoutFields(t *testing.T) {
	t.Run("slice", func(t *testing.T) {
		var target []verdict
		if err := ParseJSONStrict(`[{"valid": true}]`, &target); err != nil {
			t.Fatalf("ParseJSONStrict: %v", err)
		}
		if len(target) != 1 {
			t.Errorf("target = %+v", target)
		}
	})

	t.Run("map", func(t *testing.T) {
		var target map[string]any
		if err := ParseJSONStrict(`{"anything": 1}`, &target); err != nil {
			t.Fatalf("a map has no field names to check: %v", err)
		}
	})

	t.Run("scalar", func(t *testing.T) {
		var target string
		if err := ParseJSONStrict(`"just a string"`, &target); err != nil {
			t.Fatalf("ParseJSONStrict: %v", err)
		}
	})

	t.Run("array_body_into_slice_of_structs", func(t *testing.T) {
		// A top-level array is not an object, so there are no keys to compare.
		var target []struct {
			Index int `json:"index"`
		}
		if err := ParseJSONStrict(`[{"index": 0}]`, &target); err != nil {
			t.Fatalf("ParseJSONStrict: %v", err)
		}
	})
}

// Embedded structs contribute their field names.
func TestParseJSONStrictFollowsEmbeddedStructs(t *testing.T) {
	type base struct {
		RequestID string `json:"request_id"`
	}
	type wrapper struct {
		base
		Answer string `json:"answer"`
	}

	var target wrapper
	if err := ParseJSONStrict(`{"request_id": "abc"}`, &target); err != nil {
		t.Fatalf("an embedded field name must count: %v", err)
	}
	if target.RequestID != "abc" {
		t.Errorf("RequestID = %q", target.RequestID)
	}
}

// jsonFieldNames is the mechanism; check its tag handling directly.
func TestJSONFieldNames(t *testing.T) {
	type sample struct {
		Renamed  string `json:"renamed"`
		Excluded string `json:"-"`
		DashName string `json:"-,"`
		Untagged string
		Optional string `json:",omitempty"`
		hidden   string
	}

	names := jsonFieldNames(reflect.TypeOf(sample{}))

	for _, want := range []string{"renamed", "-", "untagged", "optional"} {
		if _, ok := names[want]; !ok {
			t.Errorf("missing %q from %v", want, names)
		}
	}
	for _, notWant := range []string{"excluded", "hidden"} {
		if _, ok := names[notWant]; ok {
			t.Errorf("%q should not be there: %v", notWant, names)
		}
	}
}
