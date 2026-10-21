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

func TestRunDecomposesBothInputs(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(
		`{"parts":[{"id":"story","name":"Meter usage","description":"Record per-tenant usage","content":{"title":"Meter usage","description":"Record per-tenant usage","team":"Platform Engineering"},"depth":1,"order":0}],"root_parts":["story"]}`,
		`{"parts":[{"id":"step","name":"Check traffic","description":"Inspect the checkout error rate","content":"Check traffic","depth":1,"order":0}],"root_parts":["step"]}`,
	)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Meter usage", "Check traffic", "Decompose Example Complete"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q: %s", want, out.String())
		}
	}
	if model.Calls() != 2 {
		t.Errorf("provider calls = %d, want 2", model.Calls())
	}
}

func TestRunReturnsProviderFailure(t *testing.T) {
	var model schemafluxtest.Provider
	model.Fail(errors.New("offline failure"))
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model, Retries: -1})
	if err := run(ctx, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "decompose epic") {
		t.Errorf("run error = %v, want epic failure", err)
	}
}
