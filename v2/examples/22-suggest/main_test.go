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

func TestRunPrintsEachSuggestionScenario(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(
		`["Partition the input by date","Use streaming transforms","Profile memory allocations","Add retry handling","Monitor pipeline latency"]`,
		`["Version the API","Validate request bodies","Document error responses","Add pagination"]`,
		`["Add bounded connection retries","Use a connection pool","Expose timeout metrics"]`,
		`["Handle every returned error","Move configuration to environment","Add focused tests","Review authorization checks"]`,
	)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	for _, line := range []string{
		"1. ETL Pipeline Optimization Suggestions", "Suggestions (5):", "1. Partition the input by date",
		"2. REST API Design Suggestions", "Suggestions (4):", "1. Version the API",
		"3. Error Recovery Suggestions (with constraints)", "Suggestions (3):", "1. Add bounded connection retries",
		"4. Code Review Suggestions", "Suggestions (4):", "1. Handle every returned error",
		"Success! Context-aware suggestions generated with LLM intelligence",
	} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("output is missing %q", line)
		}
	}
	if got := model.Calls(); got != 4 {
		t.Errorf("provider calls = %d, want 4", got)
	}
}

func TestRunReturnsProviderFailure(t *testing.T) {
	var model schemafluxtest.Provider
	failure := &schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "provider unavailable"}
	model.Fail(failure)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	err := run(ctx, &out)
	if err == nil {
		t.Fatal("run swallowed provider failure")
	}
	var got *schemaflux.Error
	if !errors.As(err, &got) && !strings.Contains(err.Error(), "provider") {
		t.Errorf("error %v does not preserve provider failure", err)
	}
	if model.Calls() != 1 {
		t.Errorf("provider calls = %d, want 1", model.Calls())
	}
}
