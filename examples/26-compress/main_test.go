package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func TestRunPrintsBothCompressionScenarios(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(
		`{"compressed":{"title":"AI in Healthcare","content":"AI aids diagnosis and drug discovery; ethics require attention.","author":"Dr. Smith"},"preserved_info":["key facts","conclusions"],"removed_info":[]}`,
		`{"compressed":"Project Alpha: Sarah documents auth blockers; John schedules sync; Mike presents designs; Lisa prioritizes payment bugs. Friday, 10 AM.","preserved_info":[],"removed_info":[]}`,
	)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	for _, want := range []string{
		"=== Compress Example ===",
		"--- Example 1: Document Compression ---",
		`Compressed: {"AI in Healthcare" "AI aids diagnosis and drug discovery; ethics require attention." "Dr. Smith"}`,
		"--- Example 2: Meeting Notes Compression ---",
		`Compressed: "Project Alpha: Sarah documents auth blockers; John schedules sync; Mike presents designs; Lisa prioritizes payment bugs. Friday, 10 AM."`,
		"=== Compress Example Complete ===",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, out.String())
		}
	}
	if got := model.Calls(); got != 2 {
		t.Errorf("provider calls = %d, want 2", got)
	}
}

func TestRunReturnsProviderFailure(t *testing.T) {
	var model schemafluxtest.Provider
	model.Fail(fmt.Errorf("provider unavailable"))
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	err := run(ctx, &out)
	if err == nil {
		t.Fatal("run swallowed provider failure")
	}
	if !strings.Contains(err.Error(), "compress the document") {
		t.Errorf("error = %v, want actionable document compression context", err)
	}
	if got := model.Calls(); got != 1 {
		t.Errorf("provider calls = %d, want 1 after first scenario fails", got)
	}
}
