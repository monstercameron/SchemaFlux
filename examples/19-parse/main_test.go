package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func TestRunPrintsAllParseScenarios(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(`{"name":"Grace","age":32,"job":"Analyst"}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out strings.Builder
	if err := run(ctx, &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"1. Parsing Standard JSON:", "Alice", "Format: json",
		"2. Parsing XML:", "Bob", "Format: xml",
		"3. Parsing YAML:", "Charlie", "Format: yaml",
		"4. Parsing CSV:", "3 employees parsed (Format: csv)", "John", "Jane",
		"5. Parsing Pipe-Delimited Data:", "2 employees parsed (Format: pipe-delimited)", "David", "Eva",
		"6. Parsing with Format Hints:", "Senior Developer", "pipe-delimited",
		"7. Parsing Mixed Format Data:", "Database config parsed (Format: json)", "host=localhost", "timeout:30s",
		"8. Parsing with Custom Delimiters:", "Frank", "custom-delimited",
		"9. Error Handling for Malformed Data:", "Grace", "LLM-assisted",
		"10. Type Conversion:", "Helen", "Name=string, Age=int, Salary=float64, Active=bool",
		"=== Parse Operation Examples Complete ===",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q", want)
		}
	}
	if model.Calls() != 1 {
		t.Errorf("provider calls = %d, want 1", model.Calls())
	}
}

func TestRunReturnsProviderFailure(t *testing.T) {
	providerErr := errors.New("scripted provider failure")
	var model schemafluxtest.Provider
	model.Fail(providerErr)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out strings.Builder
	if err := run(ctx, &out); !errors.Is(err, providerErr) {
		t.Fatalf("run error = %v, want wrapped provider failure", err)
	}
}
