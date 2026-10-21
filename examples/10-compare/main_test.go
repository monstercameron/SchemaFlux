package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

const compareAnswer = `{"similarity_score":0.65,"similarities":[{"aspect":"battery","description":"both support all-day use"}],"differences":[{"aspect":"camera","description":"UltraPhone has more resolution","severity":"major"}],"verdict":"both are premium phones with different strengths","aspect_scores":{"camera":0.6,"battery":0.8,"display":0.7,"performance":0.75,"value":0.65}}`

func TestRunPrintsComparison(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(compareAnswer)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out strings.Builder
	if err := run(ctx, &out); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"🔍 Compare Example - Product Comparison", "============================================================",
		"UltraPhone Pro Max ($1299.99)", "SmartPhone Elite ($1099.99)",
		"Overall Similarity: 65%", "Verdict: both are premium phones with different strengths",
		"battery: ████████░░ 80%", "[battery] both support all-day use",
		"[camera] UltraPhone has more resolution", "Choose UltraPhone Pro Max for:",
		"Choose SmartPhone Elite for:", "Success! Typed product comparison with detailed analysis",
	} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("output is missing %q", line)
		}
	}
	if model.Calls() != 1 {
		t.Errorf("provider calls = %d, want 1", model.Calls())
	}
}

func TestRunReturnsProviderFailure(t *testing.T) {
	var model schemafluxtest.Provider
	model.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "offline"})
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	err := run(ctx, &strings.Builder{})
	if err == nil {
		t.Fatal("run returned nil after provider failure")
	}
	if !errors.Is(err, schemaflux.ErrUnavailable) {
		t.Errorf("error = %v, want ErrUnavailable", err)
	}
	if !strings.Contains(err.Error(), "check your provider configuration and try again") {
		t.Errorf("error = %v, want user guidance", err)
	}
	if model.Calls() != 1 {
		t.Errorf("provider calls = %d, want 1", model.Calls())
	}
}
