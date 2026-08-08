package ops

import (
	"context"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

type applicant struct {
	Email   string `json:"email"`
	Country string `json:"country"`
	Age     int    `json:"age"`
	Website string `json:"website"`
	Tier    string `json:"tier"`
}

// OP-205. Every rule in this operation's own documented example -- a valid
// email, an ISO country code, a minimum age -- is exact in Go and a judgement
// call in a model.
func TestDeterministicRulesDecideWhatTheyCan(t *testing.T) {
	cases := []struct {
		name    string
		rule    string
		value   any
		wantBad bool
	}{
		{"valid email", "email", "ada@example.com", false},
		{"malformed email", "email", "ada@@example", true},
		{"email with no domain", "email", "ada@", true},
		{"non-string email", "email", 42, true},

		{"valid country", "iso3166-alpha2", "US", false},
		{"lowercase country", "iso3166-alpha2", "us", true},
		{"three-letter country", "iso3166-alpha2", "USA", true},

		{"age above the floor", "min:18", 21, false},
		{"age at the floor", "min:18", 18, false},
		{"age below the floor", "min:18", 17, true},
		{"age as a string", "min:18", "21", false},
		{"age that is not a number", "min:18", "twenty-one", true},

		{"under the ceiling", "max:100", 99, false},
		{"over the ceiling", "max:100", 101, true},

		{"valid url", "url", "https://example.com/path", false},
		{"relative url", "url", "/path", true},

		{"valid uuid", "uuid", "3f2504e0-4f89-41d3-9a0c-0305e82c3301", false},
		{"not a uuid", "uuid", "3f2504e0", true},

		{"one of the allowed", "oneof:gold|silver|bronze", "silver", false},
		{"case-folded", "oneof:gold|silver|bronze", "SILVER", false},
		{"not one of them", "oneof:gold|silver|bronze", "platinum", true},

		{"long enough", "minlen:3", "abc", false},
		{"too short", "minlen:3", "ab", true},
		{"short enough", "maxlen:3", "abc", false},
		{"too long", "maxlen:3", "abcd", true},
		{"length counts runes", "maxlen:3", "東京都", false},

		{"matches the pattern", `regex:^INV-\d+$`, "INV-4417", false},
		{"does not match", `regex:^INV-\d+$`, "4417", true},

		{"non-empty", "nonempty", "something", false},
		{"empty string", "nonempty", "   ", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rule, ok := resolveDeterministicRule(tc.rule)
			if !ok {
				t.Fatalf("resolveDeterministicRule(%q) did not recognise the rule", tc.rule)
			}
			complaint := rule.Check(tc.value)
			if tc.wantBad && complaint == "" {
				t.Errorf("%v passed %q", tc.value, tc.rule)
			}
			if !tc.wantBad && complaint != "" {
				t.Errorf("%v failed %q: %s", tc.value, tc.rule, complaint)
			}
		})
	}
}

// Natural language is what the operation is for, so an expression this layer
// does not understand is passed through rather than refused.
func TestUnrecognisedRulesFallThroughToTheModel(t *testing.T) {
	for _, expression := range []string{
		"must look professional",
		"should be consistent with the other fields",
		"min:not-a-number",
		"regex:[unclosed",
		"",
	} {
		if _, ok := resolveDeterministicRule(expression); ok {
			t.Errorf("resolveDeterministicRule(%q) claimed to handle it", expression)
		}
	}
}

// When every rule is decidable, the operation answers without a provider call.
func TestValidateAnswersDeterministicallyWithoutCallingTheModel(t *testing.T) {
	var calls int
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		calls++
		return `{"valid":true}`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	opts := NewValidateOptions().WithFieldRules(map[string]string{
		"email":   "email",
		"country": "iso3166-alpha2",
		"age":     "min:18",
	})

	bad := applicant{Email: "not-an-email", Country: "usa", Age: 16}
	result, err := Validate(bad, opts)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if calls != 0 {
		t.Errorf("the model was called %d times for rules a machine can decide", calls)
	}
	if result.Valid {
		t.Error("Validate accepted a record failing all three rules")
	}
	if len(result.Errors) != 3 {
		t.Fatalf("Errors = %+v, want one per failing rule", result.Errors)
	}
	// Stable order, because Go randomizes map iteration and an issue list a
	// caller cannot diff is a worse list.
	for i, field := range []string{"age", "country", "email"} {
		if result.Errors[i].Field != field {
			t.Errorf("issue %d is about %q, want %q -- the order is not stable", i, result.Errors[i].Field, field)
		}
	}

	good := applicant{Email: "ada@example.com", Country: "US", Age: 36}
	result, err = Validate(good, opts)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !result.Valid {
		t.Errorf("a record satisfying every rule was rejected: %+v", result.Errors)
	}
	if calls != 0 {
		t.Errorf("the model was called %d times", calls)
	}
}

// A mix goes to the model, with only the rules the model still has to judge --
// and the deterministic findings survive whatever the model says.
func TestDeterministicFindingsSurviveTheModelCall(t *testing.T) {
	var sawPrompt string
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(_ context.Context, _, userPrompt string, _ types.OpOptions) (string, error) {
		sawPrompt = userPrompt
		// The model says everything is fine, and is wrong about the email.
		return `{"valid":true,"errors":[]}`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	opts := NewValidateOptions().WithFieldRules(map[string]string{
		"email": "email",
		"tier":  "must sound premium",
	})

	result, err := Validate(applicant{Email: "not-an-email", Tier: "budget"}, opts)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.Valid {
		t.Error("a model saying `valid` overruled mail.ParseAddress")
	}
	if len(result.Errors) == 0 || result.Errors[0].Field != "email" {
		t.Errorf("Errors = %+v, want the deterministic email failure first", result.Errors)
	}

	// The model was asked about the rule it can judge, and not about the one
	// already decided.
	if !strings.Contains(sawPrompt, "must sound premium") {
		t.Error("the prompt does not carry the rule the model was needed for")
	}
	if strings.Contains(sawPrompt, "- email:") {
		t.Error("the prompt still asks the model about a rule already decided exactly")
	}
}

// The issue message names the field and what is wrong with it, never the value.
func TestRuleIssuesCarryNoValues(t *testing.T) {
	issues, _ := applyDeterministicRules(
		applicant{Email: "secret-address@private.example"},
		map[string]string{"email": "minlen:200"})

	if len(issues) == 0 {
		t.Fatal("expected an issue")
	}
	for _, issue := range issues {
		if strings.Contains(issue.Message, "secret-address") {
			t.Errorf("the issue carries the value: %s", issue.Message)
		}
	}
}

// A rule naming a field the data does not have is an error, not a pass.
func TestARuleForAMissingFieldFails(t *testing.T) {
	issues, _ := applyDeterministicRules(
		applicant{Email: "ada@example.com"},
		map[string]string{"nickname": "nonempty"})

	if len(issues) != 1 {
		t.Fatalf("issues = %+v, want one", issues)
	}
	if !strings.Contains(issues[0].Message, "not present") {
		t.Errorf("the issue does not say the field is missing: %s", issues[0].Message)
	}
}
