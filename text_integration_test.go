package schemaflux_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
)

// scriptedProvider is a Provider that returns canned bodies. It is the seam a
// consumer needs in order to test code built on this library without reaching a
// paid endpoint, and it exercises the whole stack: public API -> ops -> llm.
type scriptedProvider struct {
	body string
	err  error

	// requests records what the stack actually sent.
	requests []schemaflux.CompletionRequest
}

func (p *scriptedProvider) Complete(_ context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	p.requests = append(p.requests, req)
	if p.err != nil {
		return schemaflux.CompletionResponse{}, p.err
	}
	return schemaflux.CompletionResponse{
		Content:      p.body,
		Provider:     p.Name(),
		Model:        req.Model,
		FinishReason: "stop",
	}, nil
}

func (p *scriptedProvider) Name() string                                      { return "scripted" }
func (p *scriptedProvider) EstimateCost(schemaflux.CompletionRequest) float64 { return 0 }
func (p *scriptedProvider) RetryPolicy() (int, time.Duration)                 { return 0, 0 }

func withScriptedProvider(t *testing.T, body string, err error) *scriptedProvider {
	t.Helper()
	provider := &scriptedProvider{body: body, err: err}
	schemaflux.NewClient("test-key").WithProviderInstance(provider)
	return provider
}

// End to end through the exported API: a well-formed body produces a populated
// result.
func TestIntegrationSummarizeWithMetadataSucceeds(t *testing.T) {
	withScriptedProvider(t, `{"text":"A short summary.","key_points":["first","second"],"confidence":0.88}`, nil)

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
			withScriptedProvider(t, body, nil)
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
	withScriptedProvider(t, "", fmt.Errorf("upstream exploded"))

	if _, err := schemaflux.SummarizeWithMetadata("input", schemaflux.NewSummarizeOptions()); err == nil {
		t.Fatal("a provider error must reach the caller")
	}
}

// The stack must actually send the prompt and honour the model tier. This is
// the assertion that catches a request being built from the wrong options.
func TestIntegrationRequestCarriesPromptAndModel(t *testing.T) {
	provider := withScriptedProvider(t, `{"text":"s","confidence":0.5}`, nil)

	const marker = "UNIQUE-MARKER-STRING"
	if _, err := schemaflux.SummarizeWithMetadata(marker, schemaflux.NewSummarizeOptions()); err != nil {
		t.Fatalf("SummarizeWithMetadata: %v", err)
	}
	if len(provider.requests) == 0 {
		t.Fatal("the provider received no request")
	}
	req := provider.requests[0]
	if !strings.Contains(req.UserPrompt, marker) {
		t.Error("the user prompt does not contain the input")
	}
	if req.Model == "" {
		t.Error("no model was selected")
	}
	if !strings.HasPrefix(req.Model, "gpt-5.6-") {
		t.Errorf("Model = %q, want a gpt-5.6 default", req.Model)
	}
}

// Every operation in the family behaves the same way at the boundary.
func TestIntegrationWholeTextFamilyFailsClosed(t *testing.T) {
	withScriptedProvider(t, "not json at all", nil)

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
	schemaflux.NewClient("example-key").WithProviderInstance(&scriptedProvider{
		body: `{"text":"Costs rose 12% on higher freight.","key_points":["freight up","margin down"],"confidence":0.91}`,
	})

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
	schemaflux.NewClient("example-key").WithProviderInstance(&scriptedProvider{
		body: "I'm sorry, I can't summarise that.",
	})

	result, err := schemaflux.SummarizeWithMetadata("input", schemaflux.NewSummarizeOptions())
	fmt.Println("error is nil:", err == nil)
	fmt.Printf("confidence: %.1f\n", result.ModelConfidence)

	// Output:
	// error is nil: false
	// confidence: 0.0
}
