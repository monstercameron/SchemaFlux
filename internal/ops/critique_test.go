package ops

import (
	"testing"
)

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
