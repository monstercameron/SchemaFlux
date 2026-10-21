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
	model.Reply(
		"AI improves healthcare diagnostics, drug discovery, and patient care, while privacy, bias, and regulation remain challenges.",
		`{"text":"AI is transforming healthcare through diagnostics, drug discovery, and patient care. Privacy, bias, and regulatory challenges remain. Experts expect ethical AI to save lives and reduce costs.","key_points":["AI advances diagnostics","AI speeds drug discovery","Privacy and bias need attention"],"confidence":0.9}`,
	)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatalf("run returned an error: %v", err)
	}
	for _, want := range []string{
		"Example 1: Simple Summary (string to string)",
		"AI improves healthcare diagnostics, drug discovery, and patient care",
		"Example 2: Summary with Metadata (SummarizeDetail)",
		"AI is transforming healthcare through diagnostics, drug discovery, and patient care.",
		"Compression Ratio:",
		"Model Confidence (model-reported): 90%",
		"1. AI advances diagnostics",
		"Success! SummarizeDetail provides rich insights beyond just text",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output does not contain %q", want)
		}
	}
	if got := model.Calls(); got != 2 {
		t.Errorf("provider received %d calls, want 2", got)
	}
}

func TestRunReturnsProviderFailure(t *testing.T) {
	var model schemafluxtest.Provider
	model.Fail(errors.New("scripted provider failure"))
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	err := run(ctx, &out)
	if err == nil || !strings.Contains(err.Error(), "summarize the article") {
		t.Fatalf("run error = %v, want a summary failure", err)
	}
	if strings.Contains(out.String(), "Summary:") {
		t.Fatalf("run printed a successful summary after provider failure: %q", out.String())
	}
	if got := model.Calls(); got != 1 {
		t.Errorf("provider received %d calls, want 1", got)
	}
}
