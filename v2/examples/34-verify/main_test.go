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

func TestRunPrintsAllVerificationScenarios(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(
		`{"summary":"uptime fell short","overall_confidence":0.7,"trust_score":0.7,"claims":[{"claim":"Vendor met all uptime requirements for Q4 2024","verdict":"false","confidence":0.9,"corrections":"Actual uptime was 99.7%.","sources":[0],"evidence":["Q4 report"],"reasoning":"Below the guarantee."}],"logic_issues":[],"consistency_issues":[]}`,
		`{"summary":"some resume details differ","overall_confidence":0.6,"trust_score":0.6,"claims":[{"claim":"BS in Computer Science from MIT, graduated 2018","verdict":"partially_true","confidence":0.8,"corrections":"Graduated in 2019.","sources":[0],"evidence":["Background check"],"reasoning":"The year differs."}],"logic_issues":[],"consistency_issues":[]}`,
		`{"summary":"marketing claims exceed the test results","overall_confidence":0.4,"trust_score":0.4,"claims":[{"claim":"Clinically proven to boost energy by 50%","verdict":"false","confidence":0.95,"corrections":"The measured improvement was 28%.","sources":[0],"evidence":["Clinical test data"],"reasoning":"The result is below 50%."}],"logic_issues":[],"consistency_issues":[]}`,
	)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"--- Use Case 1: Contract Compliance Audit ---", "--- Use Case 2: Resume Verification ---", "--- Use Case 3: Marketing Claims Compliance ---",
		"ModelTrustScore: 0.70", "ModelTrustScore: 0.60", "ModelTrustScore: 0.40", "=== Verify Example Complete ===",
		`Summary: "uptime fell short"`, `Summary: "some resume details differ"`, `Summary: "marketing claims exceed the test results"`,
		`{"Vendor met all uptime requirements for Q4 2024", "fact:false",`,
		`{"BS in Computer Science from MIT, graduated 2018", "fact:partially_true",`,
		`{"Clinically proven to boost energy by 50%", "fact:false",`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q", want)
		}
	}
	if model.Calls() != 3 {
		t.Errorf("provider calls = %d, want 3", model.Calls())
	}
}

func TestRunReturnsProviderFailure(t *testing.T) {
	var model schemafluxtest.Provider
	model.Fail(errors.New("offline"))
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err == nil {
		t.Fatal("run swallowed provider failure")
	}
}
