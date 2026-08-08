package tests

import (
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/testfixtures"
)

// OP-201 at the public boundary. MinConfidence has a non-zero default on these
// operations, so a caller who never touched it still believed a floor was
// active; it reached the prompt and was never read back.
func TestIntegrationClassifyEnforcesItsConfidenceFloor(t *testing.T) {
	opts := schemaflux.NewClassifyOptions().
		WithCategories([]string{"billing", "technical", "account"}).
		WithMinConfidence(0.8)

	t.Run("below the floor is refused", func(t *testing.T) {
		testfixtures.WithScriptedProvider(t, `{"category":"billing","confidence":0.2,"reasoning":"a guess"}`, nil)

		result, err := schemaflux.Classify[string, string]("my card was declined", opts)
		if err == nil {
			t.Fatalf("a result the model scored at 0.2 was accepted against a 0.8 floor: %+v", result)
		}
		if !strings.Contains(err.Error(), "0.80") && !strings.Contains(err.Error(), "0.8") {
			t.Errorf("the error should name the floor: %v", err)
		}
	})

	t.Run("above the floor is accepted", func(t *testing.T) {
		testfixtures.WithScriptedProvider(t, `{"category":"billing","confidence":0.95,"reasoning":"clear"}`, nil)

		result, err := schemaflux.Classify[string, string]("my card was declined", opts)
		if err != nil {
			t.Fatalf("Classify: %v", err)
		}
		if result.Category != "billing" {
			t.Errorf("Category = %q", result.Category)
		}
	})

	t.Run("a zero floor accepts anything", func(t *testing.T) {
		testfixtures.WithScriptedProvider(t, `{"category":"billing","confidence":0.01,"reasoning":"barely"}`, nil)

		zeroFloor := schemaflux.NewClassifyOptions().
			WithCategories([]string{"billing", "technical"}).
			WithMinConfidence(0)

		if _, err := schemaflux.Classify[string, string]("my card was declined", zeroFloor); err != nil {
			t.Errorf("Classify with no floor = %v, want nil", err)
		}
	})
}

// OP-202: the membership check, at the boundary. A category outside the offered
// set is refused whatever its confidence.
func TestIntegrationClassifyRefusesACategoryItWasNotOffered(t *testing.T) {
	opts := schemaflux.NewClassifyOptions().
		WithCategories([]string{"billing", "technical"}).
		WithMinConfidence(0)

	for _, body := range []string{
		`{"category":"sales","confidence":0.99}`,
		`{"category":"billings","confidence":0.99}`,
		`{"category":"","confidence":0.99}`,
		`{"category":"I think this is billing","confidence":0.99}`,
	} {
		testfixtures.WithScriptedProvider(t, body, nil)

		if _, err := schemaflux.Classify[string, string]("my card was declined", opts); err == nil {
			t.Errorf("Classify accepted a category it never offered: %s", body)
		}
	}
}

// Case is normalised rather than rejected, because a caller comparing against
// their own constants needs "Billing" and "billing" to be one answer.
func TestIntegrationClassifyNormalisesCase(t *testing.T) {
	testfixtures.WithScriptedProvider(t, `{"category":"BILLING","confidence":0.99}`, nil)

	result, err := schemaflux.Classify[string, string]("my card was declined",
		schemaflux.NewClassifyOptions().
			WithCategories([]string{"billing", "technical"}).
			WithMinConfidence(0))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if result.Category != "billing" {
		t.Errorf("Category = %q, want the canonical billing", result.Category)
	}
}

// Verify's floor defaults to 0.7 and was prompt-only too.
func TestIntegrationVerifyEnforcesItsConfidenceFloor(t *testing.T) {
	testfixtures.WithScriptedProvider(t,
		`{"overall_verdict":"verified","overall_confidence":0.3,"trust_score":0.3,"summary":"weak"}`, nil)

	if _, err := schemaflux.Verify("the sky is green", schemaflux.NewVerifyOptions()); err == nil {
		t.Error("Verify accepted a verification the model scored at 0.3 against its 0.7 default floor")
	}

	testfixtures.WithScriptedProvider(t,
		`{"overall_verdict":"verified","overall_confidence":0.9,"trust_score":0.9,"summary":"strong"}`, nil)

	if _, err := schemaflux.Verify("the sky is blue", schemaflux.NewVerifyOptions()); err != nil {
		t.Errorf("Verify = %v, want nil for a confident verification", err)
	}
}
