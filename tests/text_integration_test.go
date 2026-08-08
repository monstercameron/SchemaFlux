package tests

import (
	"fmt"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/testfixtures"
)

// End to end through the exported API: a well-formed body produces a populated
// result.
func TestIntegrationSummarizeWithMetadataSucceeds(t *testing.T) {
	testfixtures.WithScriptedProvider(t, `{"text":"A short summary.","key_points":["first","second"],"confidence":0.88}`, nil)

	result, err := schemaflux.SummarizeWithMetadata(
		"A considerably longer piece of source material that needs condensing.",
		schemaflux.NewSummarizeOptions())
	if err != nil {
		t.Fatalf("SummarizeWithMetadata: %v", err)
	}
	if result.Text != "A short summary." {
		t.Errorf("Text = %q", result.Text)
	}
	if len(result.KeyPoints) != 2 {
		t.Errorf("KeyPoints = %v, want two", result.KeyPoints)
	}
	if result.ModelConfidence != 0.88 {
		t.Errorf("ModelConfidence = %v, want the reported 0.88", result.ModelConfidence)
	}
}

// End to end: a malformed body is an error at the public boundary too, not a
// result carrying an invented confidence.
func TestIntegrationMalformedBodyIsAnErrorAtThePublicAPI(t *testing.T) {
	bodies := []string{
		"I'm sorry, I can't help with that.",
		"{",
		"",
		"<html>503</html>",
	}
	for _, body := range bodies {
		t.Run(strings.TrimSpace(body[:min(len(body), 12)]), func(t *testing.T) {
			testfixtures.WithScriptedProvider(t, body, nil)
			result, err := schemaflux.SummarizeWithMetadata("input", schemaflux.NewSummarizeOptions())
			if err == nil {
				t.Fatalf("expected an error, got %+v", result)
			}
			if result.ModelConfidence != 0 {
				t.Errorf("failed call reported confidence %v", result.ModelConfidence)
			}
		})
	}
}

// A provider error must surface at the public boundary.
func TestIntegrationProviderErrorSurfaces(t *testing.T) {
	testfixtures.WithScriptedProvider(t, "", fmt.Errorf("upstream exploded"))

	if _, err := schemaflux.SummarizeWithMetadata("input", schemaflux.NewSummarizeOptions()); err == nil {
		t.Fatal("a provider error must reach the caller")
	}
}

// The stack must actually send the prompt and honour the model tier. This is
// the assertion that catches a request being built from the wrong options.
func TestIntegrationRequestCarriesPromptAndModel(t *testing.T) {
	provider := testfixtures.WithScriptedProvider(t, `{"text":"s","confidence":0.5}`, nil)

	const marker = "UNIQUE-MARKER-STRING"
	if _, err := schemaflux.SummarizeWithMetadata(marker, schemaflux.NewSummarizeOptions()); err != nil {
		t.Fatalf("SummarizeWithMetadata: %v", err)
	}
	if len(provider.Requests()) == 0 {
		t.Fatal("the provider received no request")
	}
	req := provider.Requests()[0]
	if !strings.Contains(req.UserPrompt, marker) {
		t.Error("the user prompt does not contain the input")
	}
	if req.Model == "" {
		t.Error("no model was selected")
	}
	// The model must belong to the provider that was asked for. Asserting a
	// gpt-5.6 prefix here was asserting the bug FL-001 fixed: every provider
	// used to receive OpenAI model IDs regardless of which provider it was.
	if req.Model != "local-mock" {
		t.Errorf("Model = %q, want the local provider's own model", req.Model)
	}
}

// Every operation in the family behaves the same way at the boundary.
func TestIntegrationWholeTextFamilyFailsClosed(t *testing.T) {
	testfixtures.WithScriptedProvider(t, "not json at all", nil)

	if _, err := schemaflux.SummarizeWithMetadata("x", schemaflux.NewSummarizeOptions()); err == nil {
		t.Error("summarize")
	}
	if _, err := schemaflux.RewriteWithMetadata("x", schemaflux.NewRewriteOptions()); err == nil {
		t.Error("rewrite")
	}
	if _, err := schemaflux.TranslateWithMetadata("x", schemaflux.NewTranslateOptions()); err == nil {
		t.Error("translate")
	}
	if _, err := schemaflux.ExpandWithMetadata("x", schemaflux.NewExpandOptions()); err == nil {
		t.Error("expand")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Example_summarizeWithMetadata is a runnable example for an LLM-backed
// operation. It uses a scripted provider so it executes in CI with no
// credential and no spend, and `go test` verifies its output.
func Example_summarizeWithMetadata() {
	schemaflux.NewClient("example-key").WithProviderInstance(testfixtures.NewScripted(`{"text":"Costs rose 12% on higher freight.","key_points":["freight up","margin down"],"confidence":0.91}`))

	result, err := schemaflux.SummarizeWithMetadata(
		"Quarterly report: freight costs increased sharply, compressing margin...",
		schemaflux.NewSummarizeOptions())
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println(result.Text)
	fmt.Println("key points:", len(result.KeyPoints))
	fmt.Printf("confidence: %.2f\n", result.ModelConfidence)

	// Output:
	// Costs rose 12% on higher freight.
	// key points: 2
	// confidence: 0.91
}

// Example_summarizeFailsClosed shows the behaviour that used to be silent: a
// response the operation cannot parse is an error, not a result carrying an
// invented confidence.
func Example_summarizeFailsClosed() {
	schemaflux.NewClient("example-key").WithProviderInstance(testfixtures.NewScripted("I'm sorry, I can't summarise that."))

	result, err := schemaflux.SummarizeWithMetadata("input", schemaflux.NewSummarizeOptions())
	fmt.Println("error is nil:", err == nil)
	fmt.Printf("confidence: %.1f\n", result.ModelConfidence)

	// Output:
	// error is nil: false
	// confidence: 0.0
}
