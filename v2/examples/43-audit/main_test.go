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

func TestRunPrintsThreeAuditsWithScriptedProvider(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(
		`{"findings":[],"policies":{"p1":[{"category":"security","severity":0.95,"field":"card_number","issue":"full PAN stored","evidence":"field present","recommendation":"mask it"}],"p2":[{"category":"security","severity":0.95,"field":"cvv","issue":"CVV stored","evidence":"field present","recommendation":"remove it"}],"p3":[{"category":"security","severity":0.8,"field":"storage_type","issue":"not encrypted","evidence":"plain text","recommendation":"encrypt it"}],"p4":[]}}`,
		`{"findings":[],"policies":{"p1":[{"category":"compliance","severity":0.95,"field":"consent_given","issue":"consent missing","evidence":"false","recommendation":"obtain consent"}],"p2":[{"category":"privacy","severity":0.8,"field":"consent_date","issue":"date missing","evidence":"empty","recommendation":"record date"}],"p3":[{"category":"privacy","severity":0.8,"field":"data_retention","issue":"retention indefinite","evidence":"indefinite","recommendation":"set limit"}],"p4":[{"category":"compliance","severity":0.8,"field":"access_log","issue":"access not logged","evidence":"empty","recommendation":"log access"}],"p5":[]}}`,
		`{"findings":[],"policies":{"p1":[{"category":"consistency","severity":0.95,"field":"gross_profit","issue":"gross profit is inconsistent","evidence":"expected 6000000","recommendation":"correct it"}],"p2":[{"category":"accuracy","severity":0.9,"field":"net_income","issue":"net income is inconsistent","evidence":"expected 4000000","recommendation":"correct it"}],"p3":[{"category":"accuracy","severity":0.9,"field":"tax_paid","issue":"tax paid is inconsistent","evidence":"expected 1000000","recommendation":"correct it"}],"p4":[]}}`,
	)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	for _, want := range []string{
		"=== Audit Example ===",
		"--- Use Case 1: PCI-DSS Compliance Audit ---",
		"PCI-DSS Audit Results:",
		"[card_number] full PAN stored",
		"--- Use Case 2: GDPR Data Audit ---",
		"GDPR Audit Results:",
		"  compliance: 2",
		"  privacy: 2",
		"--- Use Case 3: Financial Consistency Audit ---",
		"Financial Consistency Audit (Q4 2024):",
		"[gross_profit] gross profit is inconsistent",
		"=== Audit Example Complete ===",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output does not contain %q\n%s", want, out.String())
		}
	}
	if model.Calls() != 3 {
		t.Errorf("provider calls = %d, want 3", model.Calls())
	}
}

func TestRunReturnsProviderFailure(t *testing.T) {
	var model schemafluxtest.Provider
	model.Fail(errors.New("scripted offline failure"))
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	err := run(ctx, &out)
	if err == nil || !strings.Contains(err.Error(), "PCI audit failed") {
		t.Fatalf("run error = %v, want returned PCI audit failure", err)
	}
	if model.Calls() != 1 {
		t.Errorf("provider calls = %d, want 1", model.Calls())
	}
}
