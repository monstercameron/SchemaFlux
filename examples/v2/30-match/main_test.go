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

func TestRunPrintsBothMatchingScenarios(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(
		`{"matches":[{"source_index":0,"target_index":0,"score":0.91,"explanation":"Bluetooth headphones are an audio device","is_partial":false}],"unmatched_sources":[],"unmatched_targets":[1,2]}`,
		`{"matches":[{"source_index":0,"target_index":2,"score":0.88,"explanation":"Go and gRPC match the role","is_partial":false}],"unmatched_sources":[],"unmatched_targets":[0,1]}`,
	)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatalf("run returned an error: %v", err)
	}
	want := []string{
		"--- Business Use Case: E-commerce Search Matching ---",
		`Product:     "Wireless Bluetooth Headphones"`,
		"Score:       0.91",
		`Explanation: "Bluetooth headphones are an audio device"`,
		"--- Business Use Case: Recruiting - Candidate Matching ---",
		`Candidate:   "Diana"`,
		"Skills:      [Go gRPC Microservices]",
		"Score:       0.88",
		`Explanation: "Go and gRPC match the role"`,
		"=== SemanticMatch Example Complete ===",
	}
	for _, line := range want {
		if !strings.Contains(out.String(), line) {
			t.Errorf("output does not contain %q:\n%s", line, out.String())
		}
	}
	if model.Calls() != 2 {
		t.Errorf("provider got %d calls, want 2", model.Calls())
	}
}

func TestRunReturnsProviderFailure(t *testing.T) {
	var model schemafluxtest.Provider
	model.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "provider unavailable"})
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	err := run(ctx, &out)
	if err == nil {
		t.Fatal("run swallowed the provider failure")
	}
	if !errors.Is(err, schemaflux.ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
}
