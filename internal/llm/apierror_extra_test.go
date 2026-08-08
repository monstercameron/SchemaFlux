package llm

import (
	"strings"
	"testing"
)

// TestScalarStringEveryBranch pins every branch of scalarString's type
// switch: nil, string, an integer-valued float64, a non-integer float64, a
// bool, and a value of a type the vendor should never actually send (the
// default case).
func TestScalarStringEveryBranch(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"nil", nil, ""},
		{"string", "already-a-string", "already-a-string"},
		{"integer_float", float64(429), "429"},
		{"fractional_float", float64(3.5), "3.5"},
		{"true", true, "true"},
		{"false", false, "false"},
		{"unexpected_slice", []any{"nope"}, ""},
		{"unexpected_map", map[string]any{"x": 1}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := scalarString(tc.value); got != tc.want {
				t.Errorf("scalarString(%#v) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

// TestDetailWithNoBodyFallsBackToError proves Detail()'s early return: a
// body of only whitespace (or none at all) means Detail is exactly Error(),
// with no "\nbody:" suffix appended.
func TestDetailWithNoBodyFallsBackToError(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"empty", ""},
		{"whitespace_only", "   \n\t  "},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := &APIError{Provider: "openai", StatusCode: 500, Body: tc.body}
			if got := err.Detail(); got != err.Error() {
				t.Errorf("Detail() = %q, want exactly Error() = %q when Body has nothing to add", got, err.Error())
			}
			if strings.Contains(err.Detail(), "\nbody:") {
				t.Errorf("Detail() appended a body section despite an empty body: %q", err.Detail())
			}
		})
	}
}

// TestDetailWithABodyAppendsIt proves the non-empty branch appends the
// trimmed body after Error(), under the "body:" label.
func TestDetailWithABodyAppendsIt(t *testing.T) {
	err := &APIError{Provider: "openai", StatusCode: 500, Body: "  raw vendor text  "}
	got := err.Detail()
	if !strings.HasPrefix(got, err.Error()) {
		t.Errorf("Detail() = %q, want it to start with Error()", got)
	}
	if !strings.Contains(got, "body: raw vendor text") {
		t.Errorf("Detail() = %q, want the trimmed body appended under \"body:\"", got)
	}
}
