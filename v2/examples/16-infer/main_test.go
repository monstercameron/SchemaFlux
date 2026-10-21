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

func TestRun(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(`{"name":"John","age":30,"email":"john.doe@example.com","city":"San Francisco"}`, `{"name":"iPhone 15","price":999,"category":"smartphone","brand":"Apple"}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	want := []string{
		"Smart Data Inference Example", "Partial Person Data:", "Starting person inference...",
		"Email: john.doe@example.com", "City:  San Francisco", "Partial Product Data:",
		"Starting product inference...", "Price:    $999.00", "Category: smartphone", "Brand:    Apple",
		"Success! Partial data → Complete records",
	}
	for _, line := range want {
		if !strings.Contains(out.String(), line) {
			t.Errorf("output does not contain %q:\n%s", line, out.String())
		}
	}
	if model.Calls() != 2 {
		t.Fatalf("provider got %d calls, want 2", model.Calls())
	}
}

func TestRunReturnsProviderFailure(t *testing.T) {
	var model schemafluxtest.Provider
	model.Fail(errors.New("provider unavailable"))
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	err := run(ctx, &out)
	if err == nil {
		t.Fatal("run swallowed provider failure")
	}
	if !strings.Contains(err.Error(), "person inference failed") {
		t.Fatalf("error = %v, want person inference context", err)
	}
}
