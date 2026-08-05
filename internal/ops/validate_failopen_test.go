package ops

import (
	"context"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

type record struct {
	Email string `json:"email"`
	Age   int    `json:"age"`
}

// The regression this guards: Validate inferred its verdict from
// strings.Contains(response, "valid"), and "invalid" contains "valid". A model
// reporting a problem produced Valid: true with a nil error.
func TestValidateDoesNotReportInvalidAsValid(t *testing.T) {
	responses := []string{
		// Every one of these contains the substring "valid" and was therefore
		// read as a passing verdict by the removed fallback.
		"The data is invalid because the email is malformed.",
		"INVALID: age must be at least 18.",
		"This record is not valid.",
		"invalid",
		"The submission is invalidated by rule 3.",
		"Validation failed: two errors found.",
		"I could not validate this record.",
		// And these do not contain it, but still must not be verdicts.
		"I'm sorry, I can't help with that request.",
		"<html><body>503 Service Unavailable</body></html>",
		"{\"valid\": true",
		"[]",
		"",
	}

	for _, response := range responses {
		t.Run(strings.SplitN(response, " ", 2)[0], func(t *testing.T) {
			restore := stubLLM(response)
			defer restore()

			result, err := Validate(record{Email: "nope", Age: 3}, NewValidateOptions().
				WithRules("email must be valid, age must be at least 18"))

			if err == nil {
				t.Fatalf("an unparseable validation response must return an error; got Valid=%v", result.Valid)
			}
			if result.Valid {
				t.Fatal("a failed validation must never report Valid: true")
			}
		})
	}
}

// A well-formed negative verdict still parses and reports cleanly.
func TestValidateParsesWellFormedFailure(t *testing.T) {
	restore := stubLLM(`{"valid": false, "errors": [{"field": "age", "severity": "error", "message": "must be at least 18"}], "summary": "one error"}`)
	defer restore()

	result, err := Validate(record{Email: "a@b.com", Age: 3}, NewValidateOptions().WithRules("age >= 18"))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.Valid {
		t.Error("Valid must be false when the model reports an error")
	}
	if len(result.Errors) != 1 {
		t.Fatalf("expected one issue, got %d", len(result.Errors))
	}
}

// And a well-formed positive verdict is still accepted.
func TestValidateParsesWellFormedSuccess(t *testing.T) {
	restore := stubLLM(`{"valid": true, "summary": "ok"}`)
	defer restore()

	result, err := Validate(record{Email: "a@b.com", Age: 30}, NewValidateOptions().WithRules("age >= 18"))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !result.Valid {
		t.Error("Valid must be true when the model reports no issues")
	}
}

// stubLLM replaces the package LLM caller for the duration of a test.
func stubLLM(response string) func() {
	previous := customLLMCaller
	setLLMCaller(func(_ context.Context, _, _ string, _ types.OpOptions) (string, error) {
		return response, nil
	})
	return func() { customLLMCaller = previous }
}

// Validity must be derived from the issues, not taken from the model's own
// claim. A response asserting "valid": true alongside a populated errors array
// used to be reported as valid whenever FailOn was unset.
func TestValidateDerivesValidityFromIssues(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"claims_valid_but_has_errors", `{"valid":true,"errors":[{"field":"age","severity":"error","message":"too low"}]}`, false},
		{"claims_invalid_but_no_issues", `{"valid":false}`, true},
		{"clean", `{"valid":true}`, true},
		{"errors_only", `{"valid":false,"errors":[{"field":"a","severity":"error","message":"x"}]}`, false},
		{"warnings_do_not_fail_by_default", `{"valid":true,"warnings":[{"field":"a","severity":"warning","message":"x"}]}`, true},
		{"info_does_not_fail_by_default", `{"valid":true,"info":[{"severity":"info","message":"x"}]}`, true},
		{"multiple_errors", `{"valid":true,"errors":[{"message":"a"},{"message":"b"},{"message":"c"}]}`, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restore := stubLLM(tc.body)
			defer restore()

			result, err := Validate(record{}, NewValidateOptions().WithRules("any"))
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if result.Valid != tc.want {
				t.Errorf("Valid = %v, want %v (issues: %d errors, %d warnings, %d info)",
					result.Valid, tc.want, len(result.Errors), len(result.Warnings), len(result.Info))
			}
		})
	}
}

// A stricter FailOn escalates warnings and info into failures.
func TestValidateFailOnSeverities(t *testing.T) {
	const body = `{"valid":true,"warnings":[{"message":"w"}],"info":[{"message":"i"}]}`

	for _, tc := range []struct {
		failOn string
		want   bool
	}{
		{"error", true},
		{"warning", false},
		{"info", false},
	} {
		t.Run(tc.failOn, func(t *testing.T) {
			restore := stubLLM(body)
			defer restore()

			opts := NewValidateOptions().WithRules("any")
			opts.FailOn = tc.failOn

			result, err := Validate(record{}, opts)
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if result.Valid != tc.want {
				t.Errorf("FailOn=%s: Valid = %v, want %v", tc.failOn, result.Valid, tc.want)
			}
		})
	}
}

// An unrecognised FailOn is a configuration error, not a silent pass-through.
func TestValidateRejectsUnknownFailOn(t *testing.T) {
	restore := stubLLM(`{"valid":true}`)
	defer restore()

	opts := NewValidateOptions().WithRules("any")
	opts.FailOn = "catastrophic"

	if _, err := Validate(record{}, opts); err == nil {
		t.Fatal("an unknown FailOn severity must be reported")
	}
}

// A correction that does not fit the target type must be reported. Swallowing
// it left Corrected nil, which reads as "the model offered no correction".
func TestValidateReportsUnusableCorrection(t *testing.T) {
	restore := stubLLM(`{"valid":false,"errors":[{"message":"bad"}],"corrected":{"age":"not-a-number"}}`)
	defer restore()

	if _, err := Validate(record{}, NewValidateOptions().WithRules("any")); err == nil {
		t.Fatal("a correction that does not match the target type must be reported")
	}
}

// A usable correction is returned.
func TestValidateReturnsUsableCorrection(t *testing.T) {
	restore := stubLLM(`{"valid":false,"errors":[{"message":"bad"}],"corrected":{"email":"fixed@example.com","age":21}}`)
	defer restore()

	result, err := Validate(record{}, NewValidateOptions().WithRules("any"))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.Corrected == nil {
		t.Fatal("expected a correction")
	}
	if result.Corrected.Age != 21 {
		t.Errorf("Corrected.Age = %d, want 21", result.Corrected.Age)
	}
}

// An explicit null correction is simply absent, not an error.
func TestValidateNullCorrectionIsNotAnError(t *testing.T) {
	restore := stubLLM(`{"valid":true,"corrected":null}`)
	defer restore()

	result, err := Validate(record{}, NewValidateOptions().WithRules("any"))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.Corrected != nil {
		t.Error("a null correction must leave Corrected nil")
	}
}
