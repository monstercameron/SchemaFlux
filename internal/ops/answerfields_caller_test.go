package ops

// The unexported fields and the `json:"-,"` tag in this file are the subject,
// not an oversight: they exist to prove the caller reader and the schema
// generator follow the encoding/json grammar. See F-015 and F-035.
//lint:file-ignore U1000 fixtures are unexported on purpose
//lint:file-ignore SA5008 `json:"-,"` names the field "-"; that is the case under test

import (
	"errors"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

type verdict struct {
	Valid           bool     `json:"valid"`
	Reasons         []string `json:"reasons"`
	ModelConfidence float64  `json:"confidence,omitempty"`
	Internal        string   `json:"-"`
	unexported      string
}

// encoding/json ignores fields it does not recognise, so a well-formed object
// of entirely the wrong shape unmarshals into a zero value with no error. Every
// operation that only called ParseJSON therefore reported success with every
// field empty.
func TestDecodeCallerAnswerRejectsAnAnswerAboutSomethingElse(t *testing.T) {
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
			if err := decodeCallerAnswer("probe", tc.body, &target); err == nil {
				t.Fatalf("a body carrying none of the expected fields must be reported; parsed into %+v", target)
			}
			// The error names shapes, not values.
			var alsoTarget verdict
			err := decodeCallerAnswer("probe", tc.body, &alsoTarget)
			if strings.Contains(err.Error(), "the original text") {
				t.Errorf("the error reproduces the payload: %v", err)
			}
		})
	}
}

// The keys of an answer are the model's text, and a model can write anything
// as a key, a caller's own data included. The error says how many fields the
// answer carried and which ones were wanted, which are the library's own names,
// and none of the keys the model sent.
func TestDecodeCallerAnswerErrorNamesNoneOfTheResponsesKeys(t *testing.T) {
	cases := []struct {
		name string
		body string
		keys []string
	}{
		{"one key", `{"nonsense": true}`, []string{"nonsense"}},
		{"an email address as a key", `{"jane.doe@example.com": 1}`, []string{"jane.doe@example.com", "jane.doe"}},
		{"a number as a key", `{"4417-0099-1234": "x"}`, []string{"4417-0099-1234"}},
		{"two keys", `{"alpha-secret": 1, "beta-secret": 2}`, []string{"alpha-secret", "beta-secret"}},
		{"a key with spaces", `{"unpaid invoice for Jane": []}`, []string{"unpaid invoice", "Jane"}},
		{"a key in capitals", `{"CLUSTERS": []}`, []string{"CLUSTERS", "clusters"}},
		{"a key with unicode", `{"zażółć-gęślą": 1}`, []string{"zażółć"}},
		{"a nested object under an unknown key", `{"data-secret": {"valid": true}}`, []string{"data-secret"}},
		{"an error envelope", `{"error": "rate limited", "retry_after": 30}`, []string{"retry_after", "rate limited"}},
		{"a long key", `{"` + strings.Repeat("k", 300) + `": 1}`, []string{strings.Repeat("k", 50)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var target verdict
			err := decodeCallerAnswer("probe", tc.body, &target)
			if err == nil {
				t.Fatalf("an answer with none of the expected fields was accepted: %+v", target)
			}
			if kind := types.KindOf(err); kind != types.KindSchemaViolation {
				t.Errorf("kind = %v, want schema violation", kind)
			}
			for _, key := range tc.keys {
				if strings.Contains(err.Error(), key) {
					t.Errorf("the error carries the answer's key %q: %v", key, err)
				}
			}
			for _, wanted := range []string{"valid", "reasons", "confidence"} {
				if !strings.Contains(err.Error(), wanted) {
					t.Errorf("the error does not name the wanted field %q: %v", wanted, err)
				}
			}
		})
	}
}

// One recognised field is enough. The rule has to be weak, or a model that
// omits an optional field is treated as a failure.
func TestDecodeCallerAnswerAcceptsAPartialAnswer(t *testing.T) {
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
			if err := decodeCallerAnswer("probe", tc.body, &target); err != nil {
				t.Fatalf("a body carrying an expected field must be accepted: %v", err)
			}
		})
	}
}

// A body that is not JSON at all is refused as malformed output.
func TestDecodeCallerAnswerKeepsTheParseError(t *testing.T) {
	for _, body := range []string{
		"I'm sorry, I can't help with that.",
		"<html>502</html>",
		`{"valid": tr`,
		"",
		"   ",
	} {
		t.Run(body, func(t *testing.T) {
			var target verdict
			err := decodeCallerAnswer("probe", body, &target)
			if err == nil {
				t.Fatal("an unparseable body must be reported")
			}
			// The kind, not the wording. Asserting on the message is what
			// A-007 exists to stop callers doing, and a test doing it is the
			// same mistake with a shorter blast radius.
			if kind := types.KindOf(err); kind != types.KindMalformedOutput {
				t.Errorf("a body that is not JSON classified as %v, want malformed output", kind)
			}
		})
	}
}

// Targets with no field names to match are passed through: the field check has
// nothing to say about a slice, a map, or a scalar.
func TestDecodeCallerAnswerIgnoresTargetsWithoutFields(t *testing.T) {
	t.Run("slice", func(t *testing.T) {
		var target []verdict
		if err := decodeCallerAnswer("probe", `[{"valid": true}]`, &target); err != nil {
			t.Fatalf("decodeCallerAnswer: %v", err)
		}
		if len(target) != 1 {
			t.Errorf("target = %+v", target)
		}
	})

	t.Run("map", func(t *testing.T) {
		var target map[string]any
		if err := decodeCallerAnswer("probe", `{"anything": 1}`, &target); err != nil {
			t.Fatalf("a map has no field names to check: %v", err)
		}
	})

	t.Run("scalar", func(t *testing.T) {
		var target string
		if err := decodeCallerAnswer("probe", `"just a string"`, &target); err != nil {
			t.Fatalf("decodeCallerAnswer: %v", err)
		}
	})

	t.Run("array_body_into_slice_of_structs", func(t *testing.T) {
		// A top-level array is not an object, so there are no keys to compare.
		var target []struct {
			Index int `json:"index"`
		}
		if err := decodeCallerAnswer("probe", `[{"index": 0}]`, &target); err != nil {
			t.Fatalf("decodeCallerAnswer: %v", err)
		}
	})
}

// Embedded structs contribute their field names.
func TestDecodeCallerAnswerFollowsEmbeddedStructs(t *testing.T) {
	type base struct {
		RequestID string `json:"request_id"`
	}
	type wrapper struct {
		base
		Answer string `json:"answer"`
	}

	var target wrapper
	if err := decodeCallerAnswer("probe", `{"request_id": "abc"}`, &target); err != nil {
		t.Fatalf("an embedded field name must count: %v", err)
	}
	if target.RequestID != "abc" {
		t.Errorf("RequestID = %q", target.RequestID)
	}
}

// strictRefusingField decodes from nothing: its decoder reports a plain error
// that no one classified.
type strictRefusingField struct{}

func (*strictRefusingField) UnmarshalJSON([]byte) error { return errors.New("the decoder gave up") }

// The operations that read an answer through decodeCallerAnswer take the kind of
// its error as the kind of their own refusal and have no default for an error
// that carries none, so every error it returns has to carry one.
func TestDecodeCallerAnswerErrorsAreAlwaysClassified(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		target any
		want   types.ErrorKind
	}{
		{"not JSON", `sorry`, &verdict{}, types.KindMalformedOutput},
		{"an empty answer", ``, &verdict{}, types.KindMalformedOutput},
		{"a truncated object", `{"valid":tr`, &verdict{}, types.KindMalformedOutput},
		{"a field of the wrong type", `{"valid":"yes"}`, &verdict{}, types.KindSchemaViolation},
		{"an array for an object", `[1]`, &verdict{}, types.KindMalformedOutput},
		{"a scalar for an object", `7`, &verdict{}, types.KindMalformedOutput},
		{"markup", `<html>error</html>`, &verdict{}, types.KindMalformedOutput},
		{"a null", `null`, &verdict{}, types.KindMalformedOutput},
		{"a decoder that fails with an unclassified error", `{"inner":1}`, &struct {
			Inner strictRefusingField `json:"inner"`
		}{}, types.KindMalformedOutput},
		{"an object with none of the fields", `{"other":1}`, &verdict{}, types.KindSchemaViolation},
		{"an empty object", `{}`, &verdict{}, types.KindSchemaViolation},
		{"an object of another shape inside a fence", "```json\n{\"other\":1}\n```", &verdict{}, types.KindSchemaViolation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := decodeCallerAnswer("probe", tc.input, tc.target)
			if err == nil {
				t.Fatalf("accepted %q", tc.input)
			}
			if got := types.KindOf(err); got != tc.want {
				t.Errorf("kind = %v, want %v (%v)", got, tc.want, err)
			}
		})
	}
}

// The plan's names give the answers the same verdicts the type's own do.
func TestDecodeCallerAnswerVerdictsFromThePlan(t *testing.T) {
	type inner struct {
		Depth string `json:"depth"`
	}
	type outer struct {
		inner
		Name    string `json:"name"`
		Renamed string `json:"other_name,omitempty"`
		hidden  string
		Skipped string `json:"-"`
	}
	cases := []struct {
		name    string
		body    string
		target  any
		refused bool
	}{
		{"a field of the struct", `{"name":"x"}`, new(outer), false},
		{"a field by its tag", `{"other_name":"x"}`, new(outer), false},
		{"a promoted field", `{"depth":"x"}`, new(outer), false},
		{"upper case is the same field", `{"NAME":"x"}`, new(outer), false},
		{"one wanted field among strangers", `{"zzz":1,"name":"x","yyy":2}`, new(outer), false},
		{"nothing wanted", `{"zzz":1}`, new(outer), true},
		{"an empty object", `{}`, new(outer), true},
		{"an unexported field is not a name", `{"hidden":"x"}`, new(outer), true},
		{"a field hidden from JSON is not a name", `{"skipped":"x"}`, new(outer), true},
		{"a target that is not a struct needs no name", `{"zzz":1}`, new(map[string]any), false},
		{"a body that is not an object", `[1,2]`, new(outer), true},
		{"a target that is a list", `[{"name":"x"}]`, new([]outer), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := decodeCallerAnswer("probe", tc.body, tc.target)
			if (err != nil) != tc.refused {
				t.Errorf("decodeCallerAnswer(%s) = %v, refused should be %v", tc.body, err, tc.refused)
			}
			if err != nil && tc.body != `[1,2]` && types.KindOf(err) != types.KindSchemaViolation {
				t.Errorf("kind = %v, want KindSchemaViolation", types.KindOf(err))
			}
		})
	}
}
