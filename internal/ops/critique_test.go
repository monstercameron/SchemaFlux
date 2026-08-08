package ops

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

const critiqueMockResponse = `{
	"overall_score": 0.55,
	"criteria_scores": {"clarity": 0.6, "evidence": 0.5},
	"issues": [
		{"criterion": "evidence", "severity": "critical", "description": "no citations", "location": "paragraph 2", "fix": "add a source"},
		{"criterion": "clarity", "severity": "minor", "description": "run-on sentence", "suggestion": "split into two sentences"}
	],
	"positives": [
		{"criterion": "structure", "description": "clear intro"}
	],
	"summary": "Needs citations, otherwise readable.",
	"top_priorities": ["add citations"]
}`

func installCritiqueResponse(t *testing.T) {
	t.Helper()
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return critiqueMockResponse, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})
}

// TestCritiqueWithModelMatchesLegacyCritique proves the collapse is
// behavior-preserving: for the same response, CritiqueWithModel's Issues
// describe the same findings the deprecated Critique's Issues did, and its
// Verdict reflects the critical issue present.
func TestCritiqueWithModelMatchesLegacyCritique(t *testing.T) {
	installCritiqueResponse(t)

	opts := NewCritiqueOptions().WithCriteria([]string{"clarity", "evidence"})

	legacy, err := Critique("some essay", opts)
	if err != nil {
		t.Fatalf("Critique failed: %v", err)
	}
	judged, err := CritiqueWithModel("some essay", opts)
	if err != nil {
		t.Fatalf("CritiqueWithModel failed: %v", err)
	}

	if len(judged.Issues) != len(legacy.Issues) {
		t.Fatalf("expected %d issues, got %d", len(legacy.Issues), len(judged.Issues))
	}
	if judged.Verdict != types.VerdictFail {
		t.Errorf("expected VerdictFail (a critical issue is present), got %v", judged.Verdict)
	}

	var foundFix, foundSuggestion bool
	for _, issue := range judged.Issues {
		switch issue.Subject {
		case "evidence":
			foundFix = true
			if issue.Suggestion != "add a source" {
				t.Errorf("expected the fix to win over an absent suggestion, got %q", issue.Suggestion)
			}
			if issue.Severity != "critical" {
				t.Errorf("expected severity 'critical', got %q", issue.Severity)
			}
		case "clarity":
			foundSuggestion = true
			if issue.Suggestion != "split into two sentences" {
				t.Errorf("expected the suggestion when no fix is present, got %q", issue.Suggestion)
			}
		}
	}
	if !foundFix || !foundSuggestion {
		t.Fatalf("expected both issues represented, got %+v", judged.Issues)
	}

	// ModelOverallScore is a claim, reachable but kept apart from Verdict.
	if judged.ModelConfidence != legacy.ModelOverallScore {
		t.Errorf("expected ModelConfidence %v, got %v", legacy.ModelOverallScore, judged.ModelConfidence)
	}
	if judged.ModelClaims["top_priorities"] == nil {
		t.Error("expected top_priorities to travel in ModelClaims")
	}
}

// TestCritiqueWithModelVerdictWithoutCritical proves Verdict is Mixed, not
// Fail, when issues exist but none is critical -- Fail is reserved for a
// critical finding, matching how a caller would actually want to gate on
// this.
func TestCritiqueWithModelVerdictWithoutCritical(t *testing.T) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return `{
			"overall_score": 0.8,
			"criteria_scores": {"clarity": 0.8},
			"issues": [{"criterion": "clarity", "severity": "minor", "description": "typo"}],
			"summary": "Mostly good."
		}`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	opts := NewCritiqueOptions().WithCriteria([]string{"clarity"})
	judged, err := CritiqueWithModel("text", opts)
	if err != nil {
		t.Fatalf("CritiqueWithModel failed: %v", err)
	}
	if judged.Verdict != types.VerdictMixed {
		t.Errorf("expected VerdictMixed, got %v", judged.Verdict)
	}
}

func TestCritiqueOptions(t *testing.T) {
	t.Run("NewCritiqueOptions creates valid defaults", func(t *testing.T) {
		opts := NewCritiqueOptions().WithCriteria([]string{"clarity"})
		if err := opts.Validate(); err != nil {
			t.Errorf("default options with criteria should be valid: %v", err)
		}
	})

	t.Run("WithCriteria sets criteria", func(t *testing.T) {
		opts := NewCritiqueOptions().WithCriteria([]string{"accuracy", "clarity", "completeness"})
		if len(opts.Criteria) != 3 {
			t.Errorf("expected 3 criteria, got %d", len(opts.Criteria))
		}
	})

	t.Run("WithRubric sets rubric", func(t *testing.T) {
		rubric := map[string]string{
			"grammar": "Check for grammatical errors",
		}
		opts := NewCritiqueOptions().WithRubric(rubric)
		if len(opts.Rubric) != 1 {
			t.Errorf("expected 1 rubric entry, got %d", len(opts.Rubric))
		}
	})

	t.Run("WithIncludeSuggestions sets suggestions flag", func(t *testing.T) {
		opts := NewCritiqueOptions().WithIncludeSuggestions(false)
		if opts.IncludeSuggestions {
			t.Error("expected IncludeSuggestions to be false")
		}
	})

	t.Run("WithIncludeFixes sets fixes flag", func(t *testing.T) {
		opts := NewCritiqueOptions().WithIncludeFixes(false)
		if opts.IncludeFixes {
			t.Error("expected IncludeFixes to be false")
		}
	})

	t.Run("WithSeverityFilter sets filter", func(t *testing.T) {
		opts := NewCritiqueOptions().WithSeverityFilter("major")
		if opts.SeverityFilter != "major" {
			t.Errorf("expected major, got %s", opts.SeverityFilter)
		}
	})

	t.Run("WithMaxIssues sets limit", func(t *testing.T) {
		opts := NewCritiqueOptions().WithMaxIssues(10)
		if opts.MaxIssues != 10 {
			t.Errorf("expected 10, got %d", opts.MaxIssues)
		}
	})

	t.Run("WithStyle sets style", func(t *testing.T) {
		opts := NewCritiqueOptions().WithStyle("harsh")
		if opts.Style != "harsh" {
			t.Errorf("expected harsh, got %s", opts.Style)
		}
	})

	t.Run("WithIncludePositives sets positives flag", func(t *testing.T) {
		opts := NewCritiqueOptions().WithIncludePositives(false)
		if opts.IncludePositives {
			t.Error("expected IncludePositives to be false")
		}
	})

	t.Run("Validate requires criteria or rubric", func(t *testing.T) {
		opts := CritiqueOptions{}
		if err := opts.Validate(); err == nil {
			t.Error("expected error for empty criteria and rubric")
		}
	})

	t.Run("Validate rejects invalid severity filter", func(t *testing.T) {
		opts := NewCritiqueOptions().
			WithCriteria([]string{"test"}).
			WithSeverityFilter("invalid")
		if err := opts.Validate(); err == nil {
			t.Error("expected error for invalid severity filter")
		}
	})

	t.Run("Validate rejects invalid style", func(t *testing.T) {
		opts := NewCritiqueOptions().
			WithCriteria([]string{"test"}).
			WithStyle("invalid")
		if err := opts.Validate(); err == nil {
			t.Error("expected error for invalid style")
		}
	})
}

func TestCritique(t *testing.T) {
	// Skip integration tests without LLM
	t.Skip("Integration test requires LLM provider")

	t.Run("Critique provides feedback", func(t *testing.T) {
		essay := `Climate change is a big problem. We should do something about it.
		Scientists say it's getting warmer. This is bad for polar bears.`

		opts := NewCritiqueOptions().
			WithCriteria([]string{"argument_strength", "evidence", "clarity"}).
			WithIncludeSuggestions(true).
			WithIncludeFixes(true)

		result, err := Critique(essay, opts)
		if err != nil {
			t.Fatalf("Critique failed: %v", err)
		}

		if result.ModelOverallScore <= 0 {
			t.Error("expected positive overall score")
		}

		if len(result.Issues) == 0 {
			t.Error("expected issues to be identified")
		}
	})

	t.Run("Critique with rubric", func(t *testing.T) {
		code := `func add(a, b int) int {
			return a + b
		}`

		opts := NewCritiqueOptions().
			WithDomain("software").
			WithRubric(map[string]string{
				"readability": "Is the code easy to read?",
				"naming":      "Are variable names clear?",
				"comments":    "Are there appropriate comments?",
			})

		result, err := Critique(code, opts)
		if err != nil {
			t.Fatalf("Critique failed: %v", err)
		}

		if len(result.CriteriaScores) == 0 {
			t.Error("expected criteria scores")
		}
	})
}
