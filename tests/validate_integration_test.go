package tests

import (
	"fmt"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/testfixtures"
)

type customer struct {
	Email   string `json:"email"`
	Country string `json:"country"`
	Age     int    `json:"age"`
}

// Validate is the operation where failing open is most dangerous: a caller uses
// it as a gate. These drive the exported API end to end through a provider.
func TestIntegrationValidateFailsClosedAtThePublicAPI(t *testing.T) {
	bodies := []struct {
		name string
		body string
	}{
		{"reports_invalid_in_prose", "The data is invalid: age is below 18."},
		{"refusal", "I'm sorry, I can't help with that."},
		{"error_page", "<html><body>502 Bad Gateway</body></html>"},
		{"truncated_json", `{"valid": tr`},
		{"array_not_object", `[{"valid": true}]`},
		{"empty", ""},
		{"whitespace", "   \n  "},
		{"the_word_valid_alone", "valid"},
	}

	for _, tc := range bodies {
		t.Run(tc.name, func(t *testing.T) {
			testfixtures.WithScriptedProvider(t, tc.body, nil)

			result, err := schemaflux.Validate(
				customer{Email: "nope", Country: "ZZ", Age: 3},
				schemaflux.NewValidateOptions().WithRules("email must be valid, age at least 18"))

			if err == nil {
				t.Fatalf("a gate must not pass on an unparseable response; got %+v", result)
			}
			if result.Valid {
				t.Fatal("Valid must never be true on a failed call")
			}
		})
	}
}

// A well-formed negative verdict is reported faithfully.
func TestIntegrationValidateReportsIssues(t *testing.T) {
	testfixtures.WithScriptedProvider(t, `{"valid": false, "errors": [
	  {"field":"age","severity":"error","message":"must be at least 18"},
	  {"field":"country","severity":"error","message":"must be ISO alpha-2"}
	], "summary":"two errors"}`, nil)

	result, err := schemaflux.Validate(
		customer{Email: "a@b.com", Country: "ZZZ", Age: 3},
		schemaflux.NewValidateOptions().WithRules("age >= 18"))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.Valid {
		t.Error("Valid must be false when errors are reported")
	}
	if len(result.Errors) != 2 {
		t.Fatalf("expected two issues, got %d", len(result.Errors))
	}
}

// And a clean record passes.
func TestIntegrationValidateAcceptsCleanRecord(t *testing.T) {
	testfixtures.WithScriptedProvider(t, `{"valid": true, "summary": "no issues"}`, nil)

	result, err := schemaflux.Validate(
		customer{Email: "a@b.com", Country: "US", Age: 30},
		schemaflux.NewValidateOptions().WithRules("age >= 18"))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !result.Valid {
		t.Error("a clean record must validate")
	}
}

// A provider failure must reach the caller rather than becoming a verdict.
func TestIntegrationValidatePropagatesProviderError(t *testing.T) {
	testfixtures.WithScriptedProvider(t, "", fmt.Errorf("provider unavailable"))

	result, err := schemaflux.Validate(customer{}, schemaflux.NewValidateOptions().WithRules("any"))
	if err == nil {
		t.Fatal("a provider failure must not produce a verdict")
	}
	if result.Valid {
		t.Fatal("Valid must be false when the provider failed")
	}
}

// Example_validateGate shows the intended shape of a validation gate, and that
// a response the operation cannot parse is an error rather than a pass. It runs
// under go test with a scripted provider: no credential, no spend.
func Example_validateGate() {
	schemaflux.NewClient("example-key").WithProviderInstance(testfixtures.NewScripted(`{"valid": false, "errors": [{"field":"age","severity":"error","message":"must be at least 18"}], "summary":"one error"}`))

	result, err := schemaflux.Validate(
		customer{Email: "ada@example.com", Country: "GB", Age: 12},
		schemaflux.NewValidateOptions().WithRules("age must be at least 18"))
	if err != nil {
		fmt.Println("validation could not be performed:", err)
		return
	}

	if !result.Valid {
		for _, issue := range result.Errors {
			fmt.Printf("%s: %s\n", issue.Field, issue.Message)
		}
	}

	// Output:
	// age: must be at least 18
}

// Example_validateFailsClosed is the counterpart: an unparseable response is an
// error, so a gate written as `if !result.Valid` cannot be silently bypassed.
func Example_validateFailsClosed() {
	schemaflux.NewClient("example-key").WithProviderInstance(testfixtures.NewScripted("The data is invalid because the age is below 18."))

	result, err := schemaflux.Validate(
		customer{Age: 12},
		schemaflux.NewValidateOptions().WithRules("age must be at least 18"))

	fmt.Println("error is nil:", err == nil)
	fmt.Println("valid:", result.Valid)

	// Output:
	// error is nil: false
	// valid: false
}

// Validity derivation must hold at the public boundary too: a response claiming
// valid alongside errors is not a pass.
func TestIntegrationValidateDerivesValidityFromIssues(t *testing.T) {
	testfixtures.WithScriptedProvider(t, `{"valid":true,"errors":[{"field":"age","severity":"error","message":"below 18"}]}`, nil)

	result, err := schemaflux.Validate(customer{Age: 3}, schemaflux.NewValidateOptions().WithRules("age >= 18"))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.Valid {
		t.Fatal("a response with errors must not validate, whatever it claims")
	}
}

// AutoCorrect surfaces a correction, and reports one that does not fit.
func TestIntegrationValidateCorrections(t *testing.T) {
	t.Run("usable", func(t *testing.T) {
		testfixtures.WithScriptedProvider(t, `{"valid":false,"errors":[{"message":"bad age"}],"corrected":{"email":"a@b.com","country":"US","age":21}}`, nil)
		result, err := schemaflux.Validate(customer{Age: 3}, schemaflux.NewValidateOptions().WithRules("age >= 18"))
		if err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if result.Corrected == nil || result.Corrected.Age != 21 {
			t.Fatalf("expected a usable correction, got %+v", result.Corrected)
		}
	})

	t.Run("unusable_is_reported", func(t *testing.T) {
		testfixtures.WithScriptedProvider(t, `{"valid":false,"errors":[{"message":"bad"}],"corrected":{"age":"twenty-one"}}`, nil)
		if _, err := schemaflux.Validate(customer{}, schemaflux.NewValidateOptions().WithRules("any")); err == nil {
			t.Fatal("a correction that does not fit the type must be reported")
		}
	})
}

// Example_validateAutoCorrect shows the correction path, which is the reason to
// reach for AutoCorrect at all.
func Example_validateAutoCorrect() {
	schemaflux.NewClient("example-key").WithProviderInstance(testfixtures.NewScripted(`{"valid":false,"errors":[{"field":"country","severity":"error","message":"must be ISO alpha-2"}],"corrected":{"email":"ada@example.com","country":"GB","age":36}}`))

	result, err := schemaflux.Validate(
		customer{Email: "ada@example.com", Country: "Great Britain", Age: 36},
		schemaflux.NewValidateOptions().WithRules("country must be ISO alpha-2").WithAutoCorrect(true))
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println("valid:", result.Valid)
	if result.Corrected != nil {
		fmt.Println("corrected country:", result.Corrected.Country)
	}

	// Output:
	// valid: false
	// corrected country: GB
}
