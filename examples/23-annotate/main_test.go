package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func TestRunPrintsAllAnnotationScenarios(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(
		`{"annotations":[{"type":"entities","value":"Tim Cook","text":"Tim Cook","start":10,"end":18,"confidence":0.9}],"summary":{"overall_sentiment":"neutral"}}`,
		`{"annotations":[{"type":"sentiment","value":"mixed","start":0,"end":1,"confidence":0.8}],"summary":{"overall_sentiment":"mixed"}}`,
		`{"annotations":[{"type":"topics","value":"medical AI","start":0,"end":1,"confidence":0.9},{"type":"keywords","value":"diagnostics","start":0,"end":1,"confidence":0.8}],"summary":{"overall_sentiment":"neutral"}}`,
	)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	for _, line := range []string{
		"=== Annotate Example ===", "--- Example 1: Named Entity Recognition ---",
		"Found 1 annotations:", "Tim Cook (entities): confidence 0.90",
		"--- Example 2: Sentiment Annotation ---", "sentiment: mixed (confidence 0.80)",
		"--- Example 3: Topic Annotation ---", "topics: medical AI (confidence 0.90)",
		"keywords: diagnostics (confidence 0.80)", "=== Annotate Example Complete ===",
	} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("output missing %q", line)
		}
	}
	if got := model.Calls(); got != 3 {
		t.Errorf("provider calls = %d, want 3", got)
	}
}

func TestRunReturnsProviderFailure(t *testing.T) {
	var model schemafluxtest.Provider
	model.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "offline"})
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err == nil {
		t.Fatal("run swallowed provider failure")
	}
}
