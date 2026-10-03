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

func TestRunPrintsAllQuestionScenarios(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(
		`{"answer":"Premium Widget leads with $1.2 million in revenue.","confidence":0.94,"reasoning":"It has the highest revenue.","evidence":["Premium Widget: $1,200,000"]}`,
		`{"answer":true,"confidence":0.88,"reasoning":"Leverage and liquidity are concerning.","evidence":["Debt-to-equity is 2.8","Current ratio is 0.9"]}`,
		`{"answer":{"severity":"critical","priority":1,"likely_root_cause":"Payment gateway failure","recommended_next_steps":["Check gateway health","Review recent deploys"]},"confidence":0.97,"reasoning":"Checkout failures are widespread.","evidence":["45.2% error rate"]}`,
	)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"--- Use Case 1: Sales Report Analysis ---", `Answer:     "Premium Widget leads with $1.2 million in revenue."`,
		"--- Use Case 2: Risk Assessment (Boolean Answer) ---", "Answer:     true,", `"Debt-to-equity is 2.8"`,
		"--- Use Case 3: Incident Triage (Structured Answer) ---", `Severity:       "critical"`, `RootCauseGuess: "Payment gateway failure"`,
		"=== Question Example Complete ===",
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
	want := errors.New("offline")
	var model schemafluxtest.Provider
	model.Fail(want)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	err := run(ctx, &out)
	if err == nil || !strings.Contains(err.Error(), "sales question") {
		t.Fatalf("run error = %v, want sales question failure", err)
	}
	if model.Calls() != 1 {
		t.Errorf("provider calls = %d, want 1", model.Calls())
	}
}
