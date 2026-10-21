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

func TestRunPrintsForecastAndUncertainty(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(`{"prediction":1250000,"confidence":0.72,"interval":{"lower":1100000,"upper":1400000,"confidence_level":0.8},"scenarios":[],"factors":[],"reasoning":"seasonal adjustment","assumptions":[],"risks":["Demand may weaken"]}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Q1 2024 revenue", "Predicted revenue: $1250000", "Model confidence claim: 72%", "Model interval: $1100000 to $1400000", "Demand may weaken"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q: %s", want, out.String())
		}
	}
	if model.Calls() != 1 {
		t.Errorf("provider calls = %d, want 1", model.Calls())
	}
}

func TestRunReturnsProviderFailure(t *testing.T) {
	var model schemafluxtest.Provider
	model.Fail(errors.New("offline failure"))
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model, Retries: -1})
	if err := run(ctx, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "predict revenue") {
		t.Errorf("run error = %v, want forecast failure", err)
	}
}
