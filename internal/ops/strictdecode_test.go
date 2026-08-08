package ops

import (
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

type exactTarget struct {
	Name  string  `json:"name"`
	Total float64 `json:"total"`
	Items []struct {
		ID    int     `json:"id"`
		Price float64 `json:"price"`
	} `json:"items"`
}

// S-008. encoding/json ignores properties it does not recognise, takes the last
// of a duplicate key, and stops at the end of the first value. Reasonable for a
// config file written by a person; wrong for an answer from a model, where an
// unrecognised property is the model producing a field nobody asked for.
func TestDecodeExactRejectsWhatEncodingJSONForgives(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string
		kind    types.ErrorKind
	}{
		{
			"a field nobody asked for",
			`{"name":"Ada","total":1,"items":[],"invented":"value"}`,
			"invented",
			types.KindSchemaViolation,
		},
		{
			"a repeated key",
			`{"name":"Ada","name":"Grace","total":1,"items":[]}`,
			"repeats the key",
			types.KindSchemaViolation,
		},
		{
			"a second value after the first",
			`{"name":"Ada","total":1,"items":[]} {"name":"Grace","total":2,"items":[]}`,
			"more than one JSON value",
			types.KindSchemaViolation,
		},
		{
			"a value of the wrong type",
			`{"name":"Ada","total":"free","items":[]}`,
			"total",
			types.KindSchemaViolation,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var target exactTarget
			err := DecodeExact(tc.body, &target, DecodeLimits{})
			if err == nil {
				t.Fatal("DecodeExact accepted it")
			}
			if kind := types.KindOf(err); kind != tc.kind {
				t.Errorf("kind = %v, want %v", kind, tc.kind)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("the error does not mention %q: %v", tc.wantErr, err)
			}
		})
	}
}

// A well-formed answer that fits still decodes, including nested structure.
func TestDecodeExactAcceptsAFaithfulAnswer(t *testing.T) {
	body := `{"name":"Ada","total":12.5,"items":[{"id":1,"price":2.5},{"id":2,"price":10}]}`

	var target exactTarget
	if err := DecodeExact(body, &target, DecodeLimits{}); err != nil {
		t.Fatalf("DecodeExact: %v", err)
	}
	if target.Name != "Ada" || target.Total != 12.5 || len(target.Items) != 2 {
		t.Errorf("decoded %+v", target)
	}
}

// The packaging models put around answers still comes off, because the strict
// contract is about the JSON, not about whether the model said "here you go".
func TestDecodeExactSeesThroughFences(t *testing.T) {
	body := "Here you go:\n```json\n{\"name\":\"Ada\",\"total\":1,\"items\":[]}\n```"

	var target exactTarget
	if err := DecodeExact(body, &target, DecodeLimits{}); err != nil {
		t.Fatalf("DecodeExact: %v", err)
	}
	if target.Name != "Ada" {
		t.Errorf("decoded %+v", target)
	}
}

// The limits exist because the input is bytes from a remote service. A response
// that would cost memory before turning out to be useless is refused.
func TestDecodeLimitsRefusePathologicalInput(t *testing.T) {
	deep := strings.Repeat(`{"a":`, 40) + "1" + strings.Repeat("}", 40)
	var target map[string]any
	if err := DecodeExact(deep, &target, DecodeLimits{MaxDepth: 8}); err == nil {
		t.Error("a 40-level document was accepted against an 8-level limit")
	} else if !strings.Contains(err.Error(), "nests deeper") {
		t.Errorf("the error does not say what was wrong: %v", err)
	}

	long := `{"name":"` + strings.Repeat("x", 500) + `","total":1,"items":[]}`
	var value exactTarget
	if err := DecodeExact(long, &value, DecodeLimits{MaxStringBytes: 100}); err == nil {
		t.Error("a 500-byte string was accepted against a 100-byte limit")
	}

	big := `{"name":"Ada","total":1,"items":[]}`
	if err := DecodeExact(big, &value, DecodeLimits{MaxBytes: 10}); err == nil {
		t.Error("a 35-byte body was accepted against a 10-byte limit")
	}

	items := "[" + strings.TrimSuffix(strings.Repeat("1,", 50), ",") + "]"
	var list []int
	if err := DecodeExact(items, &list, DecodeLimits{MaxArrayItems: 10}); err == nil {
		t.Error("a 50-item array was accepted against a 10-item limit")
	}
}

// A failure names where it happened, because "the response did not fit" is not
// actionable and a pointer is.
func TestDecodeExactNamesTheLocation(t *testing.T) {
	body := `{"name":"Ada","total":1,"items":[{"id":1,"price":1,"price":2}]}`

	var target exactTarget
	err := DecodeExact(body, &target, DecodeLimits{})
	if err == nil {
		t.Fatal("a repeated key inside an array element was accepted")
	}
	if !strings.Contains(err.Error(), "price") {
		t.Errorf("the error does not name the repeated key: %v", err)
	}
	if !strings.Contains(err.Error(), "/items") {
		t.Errorf("the error does not point into the document: %v", err)
	}
}

// The message carries the field name, which describes the shape, and never the
// value, which is the caller's data.
func TestDecodeExactErrorsCarryNoValues(t *testing.T) {
	const secret = "SSN-123-45-6789"
	body := `{"name":"` + secret + `","total":"free","items":[]}`

	var target exactTarget
	err := DecodeExact(body, &target, DecodeLimits{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("the error carries the payload: %v", err)
	}
}

// Malformed and schema-violating are different kinds, because they are repaired
// differently: one by quoting the parse error, one by regenerating.
func TestDecodeExactDistinguishesMalformedFromMisfitting(t *testing.T) {
	var target exactTarget

	err := DecodeExact("this is not JSON at all", &target, DecodeLimits{})
	if kind := types.KindOf(err); kind != types.KindMalformedOutput {
		t.Errorf("prose classified as %v, want malformed output", kind)
	}

	err = DecodeExact(`{"name":"Ada","total":1,"items":[],"extra":1}`, &target, DecodeLimits{})
	if kind := types.KindOf(err); kind != types.KindSchemaViolation {
		t.Errorf("an extra field classified as %v, want schema violation", kind)
	}
}
