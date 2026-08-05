package ops

import (
	"testing"
)

func TestVerifyOptions(t *testing.T) {
	t.Run("NewVerifyOptions creates valid defaults", func(t *testing.T) {
		opts := NewVerifyOptions()
		if err := opts.Validate(); err != nil {
			t.Errorf("default options should be valid: %v", err)
		}
	})

	t.Run("WithSources sets sources", func(t *testing.T) {
		sources := []any{"source1", "source2"}
		opts := NewVerifyOptions().WithSources(sources)
		if len(opts.Sources) != 2 {
			t.Errorf("expected 2 sources, got %d", len(opts.Sources))
		}
	})

	t.Run("WithStrictness sets strictness", func(t *testing.T) {
		opts := NewVerifyOptions().WithStrictness("strict")
		if opts.Strictness != "strict" {
			t.Errorf("expected strict, got %s", opts.Strictness)
		}
	})

	t.Run("WithIncludeEvidence sets evidence flag", func(t *testing.T) {
		opts := NewVerifyOptions().WithIncludeEvidence(false)
		if opts.IncludeEvidence {
			t.Error("expected IncludeEvidence to be false")
		}
	})

	t.Run("WithExplainReasoning sets reasoning flag", func(t *testing.T) {
		opts := NewVerifyOptions().WithExplainReasoning(false)
		if opts.ExplainReasoning {
			t.Error("expected ExplainReasoning to be false")
		}
	})

	t.Run("WithCheckLogic sets logic flag", func(t *testing.T) {
		opts := NewVerifyOptions().WithCheckLogic(false)
		if opts.CheckLogic {
			t.Error("expected CheckLogic to be false")
		}
	})

	t.Run("WithCheckFacts sets facts flag", func(t *testing.T) {
		opts := NewVerifyOptions().WithCheckFacts(false)
		if opts.CheckFacts {
			t.Error("expected CheckFacts to be false")
		}
	})

	t.Run("WithCheckConsistency sets consistency flag", func(t *testing.T) {
		opts := NewVerifyOptions().WithCheckConsistency(false)
		if opts.CheckConsistency {
			t.Error("expected CheckConsistency to be false")
		}
	})

	t.Run("WithDomain sets domain", func(t *testing.T) {
		opts := NewVerifyOptions().WithDomain("science")
		if opts.Domain != "science" {
			t.Errorf("expected science, got %s", opts.Domain)
		}
	})

	t.Run("WithTrustedSources sets trusted sources", func(t *testing.T) {
		opts := NewVerifyOptions().WithTrustedSources([]string{"PubMed", "WHO"})
		if len(opts.TrustedSources) != 2 {
			t.Errorf("expected 2 trusted sources, got %d", len(opts.TrustedSources))
		}
	})

	t.Run("WithMinConfidence sets min confidence", func(t *testing.T) {
		opts := NewVerifyOptions().WithMinConfidence(0.9)
		if opts.MinConfidence != 0.9 {
			t.Errorf("expected 0.9, got %f", opts.MinConfidence)
		}
	})

	t.Run("Validate rejects invalid strictness", func(t *testing.T) {
		opts := NewVerifyOptions()
		opts.Strictness = "invalid"
		if err := opts.Validate(); err == nil {
			t.Error("expected error for invalid strictness")
		}
	})

	t.Run("Validate rejects invalid min confidence", func(t *testing.T) {
		opts := NewVerifyOptions().WithMinConfidence(1.5)
		if err := opts.Validate(); err == nil {
			t.Error("expected error for confidence > 1")
		}
	})
}

func TestVerify(t *testing.T) {
	// Skip integration tests without LLM
	t.Skip("Integration test requires LLM provider")

	t.Run("Verify fact-checks claims", func(t *testing.T) {
		claims := `
		The Earth is approximately 4.5 billion years old.
		Water boils at 100 degrees Celsius at sea level.
		The capital of France is London.
		`

		opts := NewVerifyOptions().
			WithCheckFacts(true).
			WithIncludeEvidence(true).
			WithExplainReasoning(true)

		result, err := Verify(claims, opts)
		if err != nil {
			t.Fatalf("Verify failed: %v", err)
		}

		if len(result.Claims) == 0 {
			t.Error("expected verified claims")
		}

		// Should find the false claim about Paris
		foundFalse := false
		for _, claim := range result.Claims {
			if claim.Verdict == "false" {
				foundFalse = true
				break
			}
		}
		if !foundFalse {
			t.Error("expected to find a false claim")
		}
	})

	t.Run("Verify checks logic", func(t *testing.T) {
		argument := `
		All birds can fly.
		Penguins are birds.
		Therefore, penguins can fly.
		`

		opts := NewVerifyOptions().
			WithCheckLogic(true).
			WithCheckFacts(true)

		result, err := Verify(argument, opts)
		if err != nil {
			t.Fatalf("Verify failed: %v", err)
		}

		if result.ModelTrustScore >= 1.0 {
			t.Error("expected trust score < 1.0 for flawed argument")
		}
	})
}

func TestVerifyClaim(t *testing.T) {
	// Skip integration tests without LLM
	t.Skip("Integration test requires LLM provider")

	t.Run("VerifyClaim checks single claim", func(t *testing.T) {
		claim := "The speed of light in a vacuum is approximately 300,000 km/s"
		opts := NewVerifyOptions().WithExplainReasoning(true)

		result, err := VerifyClaim(claim, opts)
		if err != nil {
			t.Fatalf("VerifyClaim failed: %v", err)
		}

		if result.Verdict != "verified" && result.Verdict != "partially_true" {
			t.Errorf("expected verified or partially_true, got %s", result.Verdict)
		}
	})
}
