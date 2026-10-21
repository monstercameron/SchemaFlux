package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func TestRunPrintsAllRankScenarios(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(
		`{"rankings":[{"index":0,"score":0.2,"explanation":"general introduction"},{"index":1,"score":0.95,"explanation":"Go patterns"},{"index":2,"score":0.8,"explanation":"Go concurrency"},{"index":3,"score":0.1,"explanation":"different framework"},{"index":4,"score":0.75,"explanation":"Go performance"},{"index":5,"score":0.15,"explanation":"Python data science"}]}`,
		`{"rankings":[{"index":0,"score":0.8,"explanation":"Go and cloud experience"},{"index":1,"score":0.2,"explanation":"different stack"},{"index":2,"score":0.95,"explanation":"strong infrastructure fit"},{"index":3,"score":0.1,"explanation":"different stack"},{"index":4,"score":0.9,"explanation":"Go services experience"}]}`,
		`{"rankings":[{"index":0,"score":0.9},{"index":1,"score":0.5},{"index":2,"score":0.8},{"index":3,"score":0.7}]}`,
	)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"--- Example 1: Article Relevance Ranking ---", `Item: Article{Title: "Advanced Go Patterns", Author: "Bob"}`,
		"Explanation: Go patterns", "--- Example 2: Candidate Ranking ---", `Candidate{Name: "Charlie", Experience: 7`,
		"--- Example 3: Ranking with Boost/Penalize ---", "Premium Widget A - $99.99", "=== Rank Example Complete ===",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output does not contain %q", want)
		}
	}
	if model.Calls() != 3 {
		t.Errorf("provider received %d calls, want 3", model.Calls())
	}
}

func TestRunReturnsProviderFailure(t *testing.T) {
	var model schemafluxtest.Provider
	model.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable})
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err == nil || !strings.Contains(err.Error(), "ranking articles failed") {
		t.Fatalf("run error = %v, want an article ranking error", err)
	}
	if model.Calls() != 1 {
		t.Errorf("provider received %d calls, want 1", model.Calls())
	}
}
