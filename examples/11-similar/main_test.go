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

func TestRunPrintsPairwiseSimilarityResults(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(
		`{"score":0.18,"explanation":"One concerns billing and the other account access.","aspects":{"a1":{"score":0.2,"reason":"Different problems."},"a2":{"score":0.1,"reason":"Different goals."},"a3":{"score":0.1,"reason":"Different intent."}}}`,
		`{"score":0.91,"explanation":"Both tickets describe trouble signing in.","aspects":{"a1":{"score":0.9,"reason":"Both concern login trouble."},"a2":{"score":0.95,"reason":"The underlying issue is access."},"a3":{"score":0.95,"reason":"Both users want account access."}}}`,
		`{"score":0.12,"explanation":"An account login issue differs from an app crash.","aspects":{"a1":{"score":0.1,"reason":"The problems differ."},"a2":{"score":0.1,"reason":"The underlying issues differ."},"a3":{"score":0.2,"reason":"The goals differ."}}}`,
		`{"score":0.15,"explanation":"A billing issue differs from a startup crash.","aspects":{"a1":{"score":0.1,"reason":"The problems differ."},"a2":{"score":0.1,"reason":"The underlying issues differ."},"a3":{"score":0.2,"reason":"The goals differ."}}}`,
		`{"score":0.2,"explanation":"A billing issue differs from login trouble.","aspects":{"a1":{"score":0.2,"reason":"The problems differ."},"a2":{"score":0.1,"reason":"The underlying issues differ."},"a3":{"score":0.2,"reason":"The goals differ."}}}`,
		`{"score":0.1,"explanation":"An account login issue differs from an app crash.","aspects":{"a1":{"score":0.1,"reason":"The problems differ."},"a2":{"score":0.1,"reason":"The underlying issues differ."},"a3":{"score":0.2,"reason":"The goals differ."}}}`,
	)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	got := out.String()
	wants := []string{
		"Ticket #1001 vs Ticket #1002",
		"Ticket #1001 vs Ticket #1003",
		"Ticket #1001 vs Ticket #1004",
		"Ticket #1002 vs Ticket #1003",
		"Ticket #1002 vs Ticket #1004",
		"Ticket #1003 vs Ticket #1004",
		"91%",
		"Both tickets describe trouble signing in.",
		"Differing aspects:",
		"Summary:",
		"Success! Semantic similarity analysis complete",
	}
	last := -1
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q", want)
		}
		if strings.HasPrefix(want, "Ticket #") {
			at := strings.Index(got, want)
			if at <= last {
				t.Errorf("comparison %q appeared out of order", want)
			}
			last = at
		}
	}
	if calls := model.Calls(); calls != 6 {
		t.Errorf("provider received %d calls, want 6", calls)
	}
}

func TestRunReturnsProviderFailure(t *testing.T) {
	var model schemafluxtest.Provider
	model.Fail(errors.New("scripted provider failure"))
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err == nil {
		t.Fatal("run returned nil error after provider failure")
	}
	if model.Calls() != 1 {
		t.Fatalf("provider received %d calls, want 1", model.Calls())
	}
}
