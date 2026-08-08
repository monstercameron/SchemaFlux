package ops

import (
	"strings"
	"testing"
)

// gap_conform_test.go exercises Conform's actual behaviour: the empty-standard
// guard, the marshal-error path, strict-mode's violation refusal, and the
// parse failures ParseJSONStrict produces. It does not assert on prompt text.

// unmarshalableConformInput carries a channel field, which encoding/json
// refuses to marshal -- Conform's own marshal-error path (conform.go, the
// json.Marshal(input) call before the type schema is built).
type unmarshalableConformInput struct {
	Ch chan int `json:"ch"`
}

type conformAddress struct {
	Street string `json:"street"`
	City   string `json:"city"`
}

func TestConform_EmptyStandardIsRefusedBeforeAnyCall(t *testing.T) {
	_, err := Conform(conformAddress{Street: "1 main st"}, "")
	if err == nil {
		t.Fatal("expected an error for an empty standard")
	}
	if !strings.Contains(err.Error(), "standard") {
		t.Fatalf("error does not name the missing standard: %v", err)
	}
}

func TestConform_MarshalErrorOnAnUnmarshalableInput(t *testing.T) {
	_, err := Conform(unmarshalableConformInput{Ch: make(chan int)}, "USPS")
	if err == nil {
		t.Fatal("expected a marshal error for a channel-bearing input")
	}
	if !strings.Contains(err.Error(), "marshal") {
		t.Fatalf("error does not name the marshal failure: %v", err)
	}
}

func TestConform_HappyPathParsesConformedDataAndAdjustments(t *testing.T) {
	withResponse(t, `{
		"conformed": {"street": "1 MAIN ST", "city": "SPRINGFIELD"},
		"adjustments": [
			{"field": "street", "original_value": "1 main st", "conformed_value": "1 MAIN ST", "rule": "uppercase", "description": "USPS uses uppercase"}
		],
		"violations": [],
		"compliance": 0.95
	}`)

	result, err := Conform(conformAddress{Street: "1 main st", City: "springfield"}, "USPS")
	if err != nil {
		t.Fatalf("Conform failed: %v", err)
	}
	if result.Conformed.Street != "1 MAIN ST" || result.Conformed.City != "SPRINGFIELD" {
		t.Fatalf("Conformed = %+v, want the uppercased fields from the response", result.Conformed)
	}
	if len(result.Adjustments) != 1 {
		t.Fatalf("Adjustments = %+v, want exactly one", result.Adjustments)
	}
	if result.Compliance != 0.95 {
		t.Fatalf("Compliance = %v, want 0.95", result.Compliance)
	}
	if result.Standard != "USPS" {
		t.Fatalf("Standard = %q, want %q", result.Standard, "USPS")
	}
}

// TestConform_StrictModeRefusesAnyViolation is the one enforced refusal this
// operation has: opt.Strict combined with a non-empty Violations list fails
// the call outright rather than handing back a partially-conformed value.
func TestConform_StrictModeRefusesAnyViolation(t *testing.T) {
	withResponse(t, `{
		"conformed": {"street": "1 main st", "city": "springfield"},
		"adjustments": [],
		"violations": ["could not determine a valid ZIP+4 extension"],
		"compliance": 0.4
	}`)

	_, err := Conform(conformAddress{Street: "1 main st"}, "USPS", ConformOptions{Strict: true})
	if err == nil {
		t.Fatal("expected strict mode to refuse a response carrying violations")
	}
	if !strings.Contains(err.Error(), "strict") {
		t.Fatalf("error does not name strict-mode refusal: %v", err)
	}
}

// TestConform_NonStrictModeToleratesViolations is the companion case: the
// same violations list is accepted when Strict is not set, which is what
// makes the strict test above a real behavioural switch rather than an
// always-on rejection.
func TestConform_NonStrictModeToleratesViolations(t *testing.T) {
	withResponse(t, `{
		"conformed": {"street": "1 main st", "city": "springfield"},
		"adjustments": [],
		"violations": ["could not determine a valid ZIP+4 extension"],
		"compliance": 0.4
	}`)

	result, err := Conform(conformAddress{Street: "1 main st"}, "USPS")
	if err != nil {
		t.Fatalf("non-strict Conform should tolerate violations, got: %v", err)
	}
	if len(result.Violations) != 1 {
		t.Fatalf("Violations = %+v, want the one reported violation surfaced to the caller", result.Violations)
	}
}

func TestConform_RefusesMalformedJSON(t *testing.T) {
	withResponse(t, `{"conformed": {"street": "1 main`) // truncated
	_, err := Conform(conformAddress{Street: "1 main st"}, "USPS")
	if err == nil {
		t.Fatal("expected a parse error for truncated JSON")
	}
}

// TestConform_RefusesAnOffTopicResponse pins ParseJSONStrict's field check:
// a well-formed JSON object sharing none of "conformed", "adjustments",
// "violations", or "compliance" is a response about something else.
func TestConform_RefusesAnOffTopicResponse(t *testing.T) {
	withResponse(t, `{"unrelated_field": "some other answer entirely"}`)
	_, err := Conform(conformAddress{Street: "1 main st"}, "USPS")
	if err == nil {
		t.Fatal("expected a schema-violation error for an off-topic response")
	}
}

// TestConform_ConformedFieldDoesNotUnmarshalIntoT covers the case where
// "conformed" is present but shaped for a different type than T -- e.g. an
// array where T is a struct. json.Unmarshal must fail loudly rather than
// silently leaving the zero value.
func TestConform_ConformedFieldDoesNotUnmarshalIntoT(t *testing.T) {
	withResponse(t, `{
		"conformed": ["not", "an", "object"],
		"adjustments": [],
		"violations": [],
		"compliance": 0.9
	}`)

	_, err := Conform(conformAddress{Street: "1 main st"}, "USPS")
	if err == nil {
		t.Fatal("expected an error: \"conformed\" was a JSON array, not the expected object shape")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Fatalf("error does not describe a parse failure: %v", err)
	}
}

// TestConform_CustomRulesAndOptionMergingDoNotPanic exercises the
// mergeConformOptions branches (CustomRules, Steering, Mode, Intelligence,
// Model, Context) and PreserveUnknown/Validate's explicit-overwrite path
// documented in conform.go: passing a ConformOptions value always sets
// Strict, PreserveUnknown, and Validate to exactly what that value carries,
// including their zero values, because a bool cannot distinguish "unset"
// from "false" the way a string or pointer can.
func TestConform_CustomRulesAndOptionMergingDoNotPanic(t *testing.T) {
	withResponse(t, `{
		"conformed": {"street": "1 MAIN ST", "city": "SPRINGFIELD"},
		"adjustments": [],
		"violations": [],
		"compliance": 1.0
	}`)

	opts := ConformOptions{
		CustomRules:     map[string]string{"street": "must be uppercase"},
		Steering:        "prefer USPS abbreviations",
		PreserveUnknown: false, // explicit: overrides the true default even though it is the zero value
	}
	result, err := Conform(conformAddress{Street: "1 main st", City: "springfield"}, "USPS", opts)
	if err != nil {
		t.Fatalf("Conform with custom rules failed: %v", err)
	}
	if result.Compliance != 1.0 {
		t.Fatalf("Compliance = %v, want 1.0", result.Compliance)
	}
}
