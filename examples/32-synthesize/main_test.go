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

func TestRunSynthesizesStudies(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(`{"synthesized":"Remote work improves focus while offices aid collaboration.","facts":[],"insights":[{"insight":"Different settings help different tasks","supporting_sources":[0,2],"type":"pattern"}],"conflicts":[],"source_coverage":{}}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Remote Work Study A", "Remote work improves focus", "Different settings help different tasks"} {
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
	if err := run(ctx, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "synthesize studies") {
		t.Errorf("run error = %v, want synthesis failure", err)
	}
}
