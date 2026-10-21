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

func TestRunPrintsAllCompletionScenarios(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(
		"improved reliability and clearer interfaces.",
		"    return strings.Contains(email, \"@\")\n}",
		"the laptop arrived damaged, and I would appreciate a refund.",
		"would row out to the reef and listen to the waves whisper.",
		"providing clean power while protecting the planet.",
		"crystal-clear sound, comfortable fit, and all-day battery life.",
	)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	for _, want := range []string{
		"1. Basic Sentence Completion", "improved reliability and clearer interfaces.",
		"2. Code Completion", "3. Email Completion with Context", "4. Creative Story Completion",
		"CompleteField - Complete a Struct Field", "5. Complete Blog Post Body", "6. Complete Product Description",
		"Updated BlogPost.Body:", "Updated ProductDescription.Description:", "Success! Partial text and struct fields completed.",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output does not contain %q", want)
		}
	}
	if got := model.Calls(); got != 6 {
		t.Errorf("provider calls = %d, want 6", got)
	}
}

func TestRunReturnsProviderFailure(t *testing.T) {
	var model schemafluxtest.Provider
	model.Fail(errors.New("scripted provider failure"))
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	err := run(ctx, &out)
	if err == nil {
		t.Fatal("run returned nil after provider failure")
	}
	if !strings.Contains(err.Error(), "complete the basic sentence") {
		t.Fatalf("error = %q, want failing scenario context", err)
	}
}
