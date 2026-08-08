package ops

import (
	"context"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

func TestValidate(t *testing.T) {
	setupMockClient()

	// Update mock to return validation results
	setLLMCaller(func(ctx context.Context, system, user string, opts types.OpOptions) (string, error) {
		if strings.Contains(system, "validation expert") {
			if strings.Contains(user, "age must be 18-100") {
				return `{
					"valid": true,
					"errors": [],
					"warnings": [],
					"info": [],
					"confidence": 0.95,
					"summary": "All validation rules passed"
				}`, nil
			}
			return `{
				"valid": false,
				"errors": [{"field": "age", "severity": "error", "message": "Age is outside valid range", "suggestion": "Set age between 18 and 100"}],
				"warnings": [],
				"info": [],
				"confidence": 0.9,
				"summary": "Age validation failed"
			}`, nil
		}
		return mockLLMResponse(ctx, system, user, opts)
	})

	t.Run("ValidData", func(t *testing.T) {
		person := Person{Name: "John", Age: 30}
		opts := NewValidateOptions().WithRules("age must be 18-100, email must be valid")
		result, err := Validate(person, opts)

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if !result.Valid {
			t.Errorf("Expected valid=true, got false")
		}

		if len(result.Errors) > 0 {
			t.Errorf("Expected no errors, got %v", result.Errors)
		}

		if result.ModelConfidence < 0.9 {
			t.Errorf("Expected high confidence, got %.2f", result.ModelConfidence)
		}
	})

	t.Run("InvalidData", func(t *testing.T) {
		person := Person{Name: "Jane", Age: 150}
		opts := NewValidateOptions().WithRules("age must be reasonable")
		result, err := Validate(person, opts)

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if result.Valid {
			t.Errorf("Expected valid=false, got true")
		}

		if len(result.Errors) == 0 {
			t.Errorf("Expected validation errors, got none")
		}

		// Check that the error has a suggestion
		if len(result.Errors) > 0 && result.Errors[0].Suggestion == "" {
			t.Errorf("Expected error suggestion, got empty")
		}
	})
}

// TestValidateDeterministicallyNoProviderCall proves the naming half of
// OP-206: ValidateDeterministically must never call a provider, not merely
// happen not to need one for a given input. A caller reading the old
// Validate name could not tell whether it was about to make a network call;
// this asserts the call count, not just the result, so a future change that
// silently sends a fully-decidable rule set to a model would fail this test
// even if the model happened to agree with the deterministic answer.
func TestValidateDeterministicallyNoProviderCall(t *testing.T) {
	calls := 0
	setLLMCaller(func(ctx context.Context, system, user string, opts types.OpOptions) (string, error) {
		calls++
		return mockLLMResponse(ctx, system, user, opts)
	})

	t.Run("AllRulesDecidable_NoIssues", func(t *testing.T) {
		calls = 0
		person := Person{Name: "John", Age: 30}
		opts := NewValidateOptions().WithFieldRules(map[string]string{"age": "min:18"})
		result, err := ValidateDeterministically(person, opts)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if calls != 0 {
			t.Fatalf("expected 0 provider calls, got %d", calls)
		}
		if result.Verdict != types.VerdictPass {
			t.Errorf("expected VerdictPass, got %v", result.Verdict)
		}
		if len(result.Issues) != 0 {
			t.Errorf("expected no issues, got %v", result.Issues)
		}
	})

	t.Run("AllRulesDecidable_WithIssue", func(t *testing.T) {
		calls = 0
		person := Person{Name: "Jane", Age: 10}
		opts := NewValidateOptions().WithFieldRules(map[string]string{"age": "min:18"})
		result, err := ValidateDeterministically(person, opts)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if calls != 0 {
			t.Fatalf("expected 0 provider calls, got %d", calls)
		}
		if result.Verdict != types.VerdictFail {
			t.Errorf("expected VerdictFail, got %v", result.Verdict)
		}
		if len(result.Issues) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(result.Issues))
		}
		if result.Issues[0].Subject != "age" {
			t.Errorf("expected issue subject 'age', got %q", result.Issues[0].Subject)
		}
	})

	t.Run("RejectsFreeTextRules", func(t *testing.T) {
		calls = 0
		person := Person{Name: "Jane", Age: 30}
		opts := NewValidateOptions().WithRules("must be a reasonable person")
		_, err := ValidateDeterministically(person, opts)
		if err == nil {
			t.Fatal("expected an error when Rules requires model judgment")
		}
		if calls != 0 {
			t.Fatalf("expected 0 provider calls even on rejection, got %d", calls)
		}
	})

	t.Run("RejectsUndecidableFieldRule", func(t *testing.T) {
		calls = 0
		person := Person{Name: "Jane", Age: 30}
		opts := NewValidateOptions().WithFieldRules(map[string]string{"name": "sounds professional"})
		_, err := ValidateDeterministically(person, opts)
		if err == nil {
			t.Fatal("expected an error for a field rule Go cannot decide")
		}
		if calls != 0 {
			t.Fatalf("expected 0 provider calls even on rejection, got %d", calls)
		}
	})
}

// TestValidateHybridMatchesLegacyValidate proves the collapse is
// behavior-preserving: for the same response, ValidateHybrid's Verdict and
// Issues describe the same finding the deprecated Validate's Valid and
// Errors did, just in the shared shape.
func TestValidateHybridMatchesLegacyValidate(t *testing.T) {
	setLLMCaller(func(ctx context.Context, system, user string, opts types.OpOptions) (string, error) {
		return `{
			"valid": false,
			"errors": [{"field": "age", "severity": "error", "message": "Age is outside valid range", "suggestion": "Set age between 18 and 100"}],
			"warnings": [],
			"info": [],
			"confidence": 0.9,
			"summary": "Age validation failed"
		}`, nil
	})

	person := Person{Name: "Jane", Age: 150}
	opts := NewValidateOptions().WithRules("age must be reasonable")

	legacy, err := Validate(person, opts)
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}

	hybrid, err := ValidateHybrid(person, opts)
	if err != nil {
		t.Fatalf("ValidateHybrid failed: %v", err)
	}

	if legacy.Valid {
		t.Fatal("expected legacy Valid=false")
	}
	if hybrid.Verdict != types.VerdictFail {
		t.Errorf("expected VerdictFail, got %v", hybrid.Verdict)
	}
	if len(hybrid.Issues) != len(legacy.Errors) {
		t.Fatalf("expected %d issues, got %d", len(legacy.Errors), len(hybrid.Issues))
	}
	if hybrid.Issues[0].Subject != legacy.Errors[0].Field {
		t.Errorf("expected subject %q, got %q", legacy.Errors[0].Field, hybrid.Issues[0].Subject)
	}
	if hybrid.Issues[0].Message != legacy.Errors[0].Message {
		t.Errorf("expected message %q, got %q", legacy.Errors[0].Message, hybrid.Issues[0].Message)
	}
	if hybrid.ModelConfidence != legacy.ModelConfidence {
		t.Errorf("expected ModelConfidence %v, got %v", legacy.ModelConfidence, hybrid.ModelConfidence)
	}
}

func TestFormat(t *testing.T) {
	setupMockClient()

	// Update mock for formatting
	setLLMCaller(func(ctx context.Context, system, user string, opts types.OpOptions) (string, error) {
		if strings.Contains(system, "formatting expert") {
			if strings.Contains(user, "markdown table") {
				return "| Name | Age |\n|------|-----|\n| John | 30 |", nil
			}
			if strings.Contains(user, "professional bio") {
				return "John is a 30-year-old professional with extensive experience.", nil
			}
		}
		return mockLLMResponse(ctx, system, user, opts)
	})

	t.Run("FormatAsTable", func(t *testing.T) {
		person := Person{Name: "John", Age: 30}
		formatted, err := Format(person, "markdown table with headers")

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if !strings.Contains(formatted, "|") {
			t.Errorf("Expected markdown table format, got: %s", formatted)
		}

		if !strings.Contains(formatted, "Name") || !strings.Contains(formatted, "Age") {
			t.Errorf("Expected headers in table, got: %s", formatted)
		}
	})

	t.Run("FormatAsBio", func(t *testing.T) {
		person := Person{Name: "John", Age: 30}
		bio, err := Format(person, "professional bio in third person")

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if !strings.Contains(bio, "John") {
			t.Errorf("Expected name in bio, got: %s", bio)
		}

		if strings.Contains(bio, "|") {
			t.Errorf("Bio should not be in table format, got: %s", bio)
		}
	})
}

func TestMerge(t *testing.T) {
	setupMockClient()

	// Update mock for merging
	setLLMCaller(func(ctx context.Context, system, user string, opts types.OpOptions) (string, error) {
		if strings.Contains(system, "merging expert") {
			// Return a merged person with combined data
			return `{"name": "John Doe", "age": 30}`, nil
		}
		return mockLLMResponse(ctx, system, user, opts)
	})

	t.Run("MergeMultipleSources", func(t *testing.T) {
		sources := []Person{
			{Name: "John", Age: 30},
			{Name: "John Doe", Age: 30},
			{Name: "John Doe", Age: 30},
		}

		merged, err := Merge(sources, "prefer newest, combine names")

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if merged.Name == "" {
			t.Errorf("Expected merged name, got empty")
		}

	})

	t.Run("MergeSingleSource", func(t *testing.T) {
		sources := []Person{
			{Name: "Jane", Age: 25},
		}

		merged, err := Merge(sources, "any strategy")

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		// Should return the single source unchanged
		if merged.Name != "Jane" || merged.Age != 25 {
			t.Errorf("Single source should be returned unchanged, got %+v", merged)
		}
	})

	t.Run("MergeEmptySources", func(t *testing.T) {
		sources := []Person{}

		_, err := Merge(sources, "any strategy")

		if err == nil {
			t.Errorf("Expected error for empty sources")
		}
	})
}

func TestQuestion(t *testing.T) {
	setupMockClient()

	// Update mock for questions
	setLLMCaller(func(ctx context.Context, system, user string, opts types.OpOptions) (string, error) {
		if strings.Contains(system, "data analysis expert") {
			if strings.Contains(user, "What is the average age") {
				return `{
					"answer": "The average age is 30 years.",
					"confidence": 0.95,
					"reasoning": "Calculated by adding all ages and dividing by count.",
					"evidence": ["John is 30", "Jane is 25", "Bob is 35"]
				}`, nil
			}
			if strings.Contains(user, "How many people") {
				return `{
					"answer": "There are 3 people in the data.",
					"confidence": 0.99,
					"reasoning": "Counted the number of records.",
					"evidence": ["John", "Jane", "Bob"]
				}`, nil
			}
		}
		return mockLLMResponse(ctx, system, user, opts)
	})

	t.Run("QuestionAboutData", func(t *testing.T) {
		data := []Person{
			{Name: "John", Age: 30},
			{Name: "Jane", Age: 25},
			{Name: "Bob", Age: 35},
		}

		opts := NewQuestionOptions("What is the average age?")
		result, err := Question[[]Person, string](data, opts)

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if !strings.Contains(result.Answer, "30") || !strings.Contains(result.Answer, "average") {
			t.Errorf("Expected answer about average age, got: %s", result.Answer)
		}

		if result.ModelConfidence < 0.9 {
			t.Errorf("Expected high confidence, got: %.2f", result.ModelConfidence)
		}

		if result.Reasoning == "" {
			t.Errorf("Expected reasoning, got empty")
		}
	})

	t.Run("CountQuestion", func(t *testing.T) {
		data := []Person{
			{Name: "John", Age: 30},
			{Name: "Jane", Age: 25},
			{Name: "Bob", Age: 35},
		}

		opts := NewQuestionOptions("How many people are there?")
		result, err := Question[[]Person, string](data, opts)

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if !strings.Contains(result.Answer, "3") || !strings.Contains(result.Answer, "people") {
			t.Errorf("Expected answer about count, got: %s", result.Answer)
		}
	})

	t.Run("QuestionLegacy", func(t *testing.T) {
		data := []Person{
			{Name: "John", Age: 30},
			{Name: "Jane", Age: 25},
			{Name: "Bob", Age: 35},
		}

		// Test the legacy interface still works
		answer, err := QuestionLegacy(data, "What is the average age?")

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if answer == "" {
			t.Errorf("Expected non-empty answer")
		}
	})
}

func TestDeduplicate(t *testing.T) {
	setupMockClient()

	// Update mock for deduplication
	setLLMCaller(func(ctx context.Context, system, user string, opts types.OpOptions) (string, error) {
		if strings.Contains(system, "deduplication expert") {
			// Return groups where items 0 and 2 are duplicates
			return `{
				"groups": [
					[0, 2],
					[1],
					[3]
				]
			}`, nil
		}
		return mockLLMResponse(ctx, system, user, opts)
	})

	t.Run("DeduplicateWithDuplicates", func(t *testing.T) {
		items := []Person{
			{Name: "John Doe", Age: 30},
			{Name: "Jane Smith", Age: 25},
			{Name: "John D.", Age: 30}, // Duplicate of first
			{Name: "Bob Johnson", Age: 35},
		}

		result, err := Deduplicate(items, 0.85)

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		// Should have 3 unique items (removed 1 duplicate)
		if len(result.Unique) != 3 {
			t.Errorf("Expected 3 unique items, got %d", len(result.Unique))
		}

		// Should have 1 removed
		if result.TotalRemoved != 1 {
			t.Errorf("Expected 1 removed, got %d", result.TotalRemoved)
		}

		// Should have 1 duplicate group
		if len(result.Duplicates) != 1 {
			t.Errorf("Expected 1 duplicate group, got %d", len(result.Duplicates))
		}
	})

	t.Run("DeduplicateNoDuplicates", func(t *testing.T) {
		// Update mock for no duplicates case
		setLLMCaller(func(ctx context.Context, system, user string, opts types.OpOptions) (string, error) {
			if strings.Contains(system, "deduplication expert") {
				return `{
					"groups": [
						[0],
						[1]
					]
				}`, nil
			}
			return mockLLMResponse(ctx, system, user, opts)
		})

		items := []Person{
			{Name: "Alice", Age: 20},
			{Name: "Bob", Age: 30},
		}

		result, err := Deduplicate(items, 0.85)

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		// All items should be unique
		if len(result.Unique) != 2 {
			t.Errorf("Expected 2 unique items, got %d", len(result.Unique))
		}

		if result.TotalRemoved != 0 {
			t.Errorf("Expected 0 removed, got %d", result.TotalRemoved)
		}
	})

	t.Run("DeduplicateEmptyList", func(t *testing.T) {
		items := []Person{}

		result, err := Deduplicate(items, 0.85)

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if len(result.Unique) != 0 {
			t.Errorf("Expected 0 unique items for empty input, got %d", len(result.Unique))
		}
	})
}
