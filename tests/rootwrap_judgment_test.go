package tests

import (
	"errors"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/testfixtures"
)

// This file covers the OP-206 judgment wrappers: ValidateDeterministically,
// ValidateHybrid, VerifyWithModel, AuditWithModel, and CritiqueWithModel.

type judgmentPerson struct {
	Age int
}

// critiqueOptionsWithCriteria fills in the one required field CritiqueOptions
// has no builder method for.
func critiqueOptionsWithCriteria(criteria ...string) schemaflux.CritiqueOptions {
	opts := schemaflux.NewCritiqueOptions()
	opts.Criteria = criteria
	return opts
}

// ValidateDeterministically must decide a Go-checkable rule without ever
// calling the provider.
func TestValidateDeterministicallyMakesNoProviderCall(t *testing.T) {
	testfixtures.WithScriptedProvider(t, "", errors.New("must not be called"))

	result, err := schemaflux.ValidateDeterministically(judgmentPerson{Age: 5},
		schemaflux.NewValidateOptions().WithFieldRules(map[string]string{"Age": "min:18"}))
	if err != nil {
		t.Fatalf("ValidateDeterministically: %v", err)
	}
	if result.Verdict != schemaflux.VerdictFail {
		t.Errorf("Verdict = %q, want fail (age 5 violates min:18)", result.Verdict)
	}
	if len(result.Issues) != 1 || result.Issues[0].Subject != "Age" {
		t.Errorf("Issues = %+v, want exactly one issue on Age", result.Issues)
	}
}

func TestValidateDeterministicallyPassesWhenRuleSatisfied(t *testing.T) {
	testfixtures.WithScriptedProvider(t, "", errors.New("must not be called"))

	result, err := schemaflux.ValidateDeterministically(judgmentPerson{Age: 30},
		schemaflux.NewValidateOptions().WithFieldRules(map[string]string{"Age": "min:18"}))
	if err != nil {
		t.Fatalf("ValidateDeterministically: %v", err)
	}
	if result.Verdict != schemaflux.VerdictPass {
		t.Errorf("Verdict = %q, want pass", result.Verdict)
	}
}

// Free-text Rules cannot be decided in Go, and ValidateDeterministically must
// refuse rather than silently sending it to a model anyway.
func TestValidateDeterministicallyRefusesFreeTextRules(t *testing.T) {
	testfixtures.WithScriptedProvider(t, "", errors.New("must not be called"))

	_, err := schemaflux.ValidateDeterministically(judgmentPerson{Age: 30},
		schemaflux.NewValidateOptions().WithRules("age should feel about right"))
	if err == nil {
		t.Fatal("expected an error: free-text Rules requires model judgment")
	}
}

// ValidateHybrid decides the same rule deterministically and, with nothing
// left for a model, also makes no provider call.
func TestValidateHybridShortcutsWhenEverythingIsDecidable(t *testing.T) {
	testfixtures.WithScriptedProvider(t, "", errors.New("must not be called"))

	result, err := schemaflux.ValidateHybrid(judgmentPerson{Age: 5},
		schemaflux.NewValidateOptions().WithFieldRules(map[string]string{"Age": "min:18"}))
	if err != nil {
		t.Fatalf("ValidateHybrid: %v", err)
	}
	if result.Verdict != schemaflux.VerdictFail {
		t.Errorf("Verdict = %q, want fail", result.Verdict)
	}
}

// With free-text Rules left over, ValidateHybrid must reach the model rather
// than erroring the way ValidateDeterministically does.
func TestValidateHybridCallsModelForFreeTextRules(t *testing.T) {
	testfixtures.WithScriptedProvider(t, `{"valid":true,"errors":[],"confidence":0.9}`, nil)

	result, err := schemaflux.ValidateHybrid(judgmentPerson{Age: 30},
		schemaflux.NewValidateOptions().WithRules("age should feel about right"))
	if err != nil {
		t.Fatalf("ValidateHybrid: %v", err)
	}
	if result.Verdict != schemaflux.VerdictPass {
		t.Errorf("Verdict = %q, want the scripted pass", result.Verdict)
	}
}

func TestVerifyWithModelReportsAFalseClaim(t *testing.T) {
	testfixtures.WithScriptedProvider(t,
		`{"overall_verdict":"false","overall_confidence":0.9,"claims":[{"claim":"The Earth is flat.","verdict":"false","confidence":0.95,"reasoning":"contradicts basic geodesy"}],"summary":"false claim"}`, nil)

	result, err := schemaflux.VerifyWithModel("The Earth is flat.", schemaflux.NewVerifyOptions())
	if err != nil {
		t.Fatalf("VerifyWithModel: %v", err)
	}
	if result.Verdict != schemaflux.VerdictFail {
		t.Errorf("Verdict = %q, want fail", result.Verdict)
	}
	if len(result.Issues) != 1 || result.Issues[0].Subject != "The Earth is flat." {
		t.Errorf("Issues = %+v, want the false claim reported", result.Issues)
	}
}

func TestVerifyWithModelProviderFailureIsAnError(t *testing.T) {
	testfixtures.WithScriptedProvider(t, "", errors.New("provider unavailable"))

	_, err := schemaflux.VerifyWithModel("The Earth is flat.", schemaflux.NewVerifyOptions())
	if err == nil {
		t.Fatal("expected an error when the provider is unreachable")
	}
}

func TestAuditWithModelReportsFindings(t *testing.T) {
	testfixtures.WithScriptedProvider(t,
		`{"findings":[{"category":"security","severity":0.95,"field":"ssn","issue":"stored in plaintext"}]}`, nil)

	type record struct{ SSN string }
	result, err := schemaflux.AuditWithModel(record{SSN: "123-45-6789"})
	if err != nil {
		t.Fatalf("AuditWithModel: %v", err)
	}
	if result.Verdict != schemaflux.VerdictFail {
		t.Errorf("Verdict = %q, want fail for a critical finding", result.Verdict)
	}
	if len(result.Issues) != 1 || result.Issues[0].Subject != "ssn" {
		t.Errorf("Issues = %+v, want the ssn finding", result.Issues)
	}
}

func TestAuditWithModelCleanRecordPasses(t *testing.T) {
	testfixtures.WithScriptedProvider(t, `{"findings":[]}`, nil)

	type record struct{ SSN string }
	result, err := schemaflux.AuditWithModel(record{SSN: "redacted"})
	if err != nil {
		t.Fatalf("AuditWithModel: %v", err)
	}
	if result.Verdict != schemaflux.VerdictPass {
		t.Errorf("Verdict = %q, want pass with no findings", result.Verdict)
	}
}

func TestCritiqueWithModelReportsIssues(t *testing.T) {
	testfixtures.WithScriptedProvider(t,
		`{"overall_score":0.4,"criteria_scores":{"clarity":0.4},"issues":[{"criterion":"clarity","severity":"major","description":"unclear thesis"}],"summary":"needs work"}`, nil)

	result, err := schemaflux.CritiqueWithModel("a rough draft essay",
		critiqueOptionsWithCriteria("clarity"))
	if err != nil {
		t.Fatalf("CritiqueWithModel: %v", err)
	}
	if len(result.Issues) != 1 || result.Issues[0].Subject != "clarity" {
		t.Errorf("Issues = %+v, want the clarity issue", result.Issues)
	}
}

func TestCritiqueWithModelProviderFailureIsAnError(t *testing.T) {
	testfixtures.WithScriptedProvider(t, "", errors.New("provider unavailable"))

	_, err := schemaflux.CritiqueWithModel("a rough draft essay",
		critiqueOptionsWithCriteria("clarity"))
	if err == nil {
		t.Fatal("expected an error when the provider is unreachable")
	}
}
