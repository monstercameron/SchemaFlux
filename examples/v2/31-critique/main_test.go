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

func TestRunPrintsBothCritiqueScenarios(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(
		`{"overall_score":0.6,"criteria_scores":{"professionalism":0.4,"specificity":0.5,"impact":0.7},"issues":[{"criterion":"specificity","severity":"major","description":"The forecast is vague.","suggestion":"Name a measurable target.","fix":"Replace the forecast with a quantified goal."}],"summary":"Add concrete measures.","top_priorities":["Name a target"]}`,
		`{"overall_score":0.8,"criteria_scores":{"readability":0.8,"best_practices":0.8},"issues":[{"criterion":"best_practices","severity":"minor","description":"The loop can use a range clause.","suggestion":"Use range to iterate over data.","fix":"Rewrite the loop as for _, item := range data."}],"summary":"Clear, with a small idiom improvement.","top_priorities":["Use range"]}`,
	)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	for _, want := range []string{
		"--- Business Use Case: Executive Presentation Review ---",
		"Verdict: mixed",
		"[specificity/error] The forecast is vague.",
		"Suggestion: Replace the forecast with a quantified goal.",
		"Summary: \"Add concrete measures.\"",
		"--- Business Use Case: Automated Code Review ---",
		"[best_practices/warning] The loop can use a range clause.",
		"Suggestion: Rewrite the loop as for _, item := range data.",
		"=== Critique Example Complete ===",
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
	wantErr := errors.New("scripted provider failure")
	model.Fail(wantErr)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	var out bytes.Buffer
	err := run(ctx, &out)
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("run error = %v, want wrapped provider failure", err)
	}
	if out.Len() != 0 {
		t.Errorf("run printed partial output after failure: %q", out.String())
	}
}
