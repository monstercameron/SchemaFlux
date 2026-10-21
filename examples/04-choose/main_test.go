package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func TestRunPrintsRecommendation(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(`{"id":"i-000001"}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	for _, line := range []string{
		"Choose Example - Product Recommender",
		"I'm a college student studying computer science. I need something portable for coding and note-taking. My budget is around $500-600.",
		"1. BudgetBook - $499.99 (Laptop)",
		"Recommended Product:",
		"Product: BudgetBook",
		"Price: $499.99",
		"Category: Laptop",
		"Features: [8GB RAM 256GB SSD 14-inch display 8hr battery]",
		"Affordable laptop for students and basic computing",
		"Success! Intelligently selected best match",
	} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("output does not contain %q", line)
		}
	}
	if model.Calls() != 1 {
		t.Errorf("provider received %d calls, want 1", model.Calls())
	}
}

func TestRunReturnsProviderFailure(t *testing.T) {
	var model schemafluxtest.Provider
	model.Fail(errors.New("scripted provider failure"))
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err == nil {
		t.Fatal("run swallowed provider failure")
	} else if !strings.Contains(err.Error(), "could not choose a product") {
		t.Errorf("error %q does not give recovery guidance", err)
	}
	if strings.Contains(out.String(), "Recommended Product:") {
		t.Error("printed a recommendation after provider failure")
	}
}
