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

func TestRunScoresAndRanksSubmissions(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(
		`{"value":7.5,"normalized_value":0.722,"breakdown":{"readability":8},"reasoning":"Clear loop","strengths":["Readable"],"weaknesses":["No documentation"]}`,
		`{"value":4.5,"normalized_value":0.389,"breakdown":{"readability":4},"reasoning":"Names are terse","strengths":[],"weaknesses":["Use clearer names"]}`,
		`{"value":9.2,"normalized_value":0.911,"breakdown":{"readability":9},"reasoning":"Well documented","strengths":["Handles edge cases"],"weaknesses":[]}`,
	)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"Evaluating Submission #1 by Alice", "Score: 7.5/10", "Good code quality. Minor improvements possible.",
		"Evaluating Submission #2 by Bob", "Score: 4.5/10", "Needs significant refactoring.",
		"Evaluating Submission #3 by Carol", "Score: 9.2/10", "Excellent! Production-ready code with best practices.",
		"1. Carol - Score: 9.2/10", "2. Alice - Score: 7.5/10", "3. Bob - Score: 4.5/10",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q", want)
		}
	}
	if strings.Index(got, "Evaluating Submission #1") > strings.Index(got, "Evaluating Submission #2") || strings.Index(got, "Evaluating Submission #2") > strings.Index(got, "Evaluating Submission #3") {
		t.Errorf("submission order changed:\n%s", got)
	}
}

func TestRunReturnsProviderFailure(t *testing.T) {
	providerErr := errors.New("provider unavailable")
	var model schemafluxtest.Provider
	model.Fail(providerErr)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err == nil {
		t.Fatal("run swallowed provider failure")
	}
}
