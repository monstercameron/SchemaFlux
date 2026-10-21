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

func TestRunPrintsAllThreeComparisons(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply("Customer details changed.", "Product details changed.", "Configuration settings changed.")
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatalf("run returned an error: %v", err)
	}
	for _, want := range []string{
		"Customer Record Changes",
		"Name: John Smith -> John A. Smith",
		"Customer details changed.",
		"Product Catalog Changes",
		"Price: 79.99 -> 89.99",
		"Product details changed.",
		"Configuration Changes (ignoring timestamps)",
		"Version: 1.2.3 -> 1.3.0",
		"Configuration settings changed.",
		"Difference detection completed",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
	if model.Calls() != 3 {
		t.Errorf("provider received %d calls, want 3", model.Calls())
	}
}

func TestRunReturnsProviderFailure(t *testing.T) {
	var model schemafluxtest.Provider
	model.Fail(errors.New("offline test provider"))
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	err := run(ctx, &out)
	if err == nil {
		t.Fatal("run swallowed the failed summary")
	}
	if !strings.Contains(err.Error(), "customer comparison failed") {
		t.Errorf("error = %q, want actionable customer comparison context", err)
	}
	if model.Calls() != 1 {
		t.Errorf("provider received %d calls, want 1", model.Calls())
	}
}
