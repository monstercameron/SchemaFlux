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
		"The data is invalid because the email is malformed.",
		"INVALID: age must be at least 18.",
		"This record is not valid.",
		"invalid",
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
