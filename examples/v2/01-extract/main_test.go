package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func TestRunPrintsExtractedEmail(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(`{"from":"john.smith@example.com","to":"sarah.jones@company.com","subject":"Project Update - Q4 Results","date":"2024-12-15T00:00:00Z","body":"I wanted to share the Q4 results with you.","tags":["Project","Update","Q4","Results"]}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	var out strings.Builder
	if err := run(ctx, &out); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	for _, expected := range []string{
		"📧 Email Parser Example",
		strings.Repeat("=", 50),
		"📥 Raw Input:",
		"✅ Extracted Email:",
		"From:    john.smith@example.com",
		"To:      sarah.jones@company.com",
		"Subject: Project Update - Q4 Results",
		"Date:    2024-12-15",
		"Tags:    [Project Update Q4 Results]",
		"Body:\n  I wanted to share the Q4 results with you.",
	} {
		if !strings.Contains(out.String(), expected) {
			t.Errorf("output missing %q", expected)
		}
	}
}

func TestRunReturnsProviderFailure(t *testing.T) {
	var model schemafluxtest.Provider
	model.Fail(errors.New("provider unavailable"))
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	var out strings.Builder
	if err := run(ctx, &out); err == nil {
		t.Fatal("run returned nil error after provider failure")
	}
}
