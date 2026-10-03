package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func TestRunRoutesTickets(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(
		`{"selected":0,"explanation":"The application is crashing after an update.","confidence":0.94,"alternatives":[]}`,
		`{"selected":2,"explanation":"They are asking for product training.","confidence":0.88,"alternatives":[]}`,
		`{"selected":1,"explanation":"The invoice charge is incorrect.","confidence":0.97,"alternatives":[]}`,
	)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatalf("run returned an error: %v", err)
	}
	for _, line := range []string{
		"1. Ticket #101: Application crashes on startup",
		"Route to: Technical Support",
		"Model confidence: 94%",
		"Reasoning: The application is crashing after an update.",
		"2. Ticket #102: Need help with advanced features",
		"Route to: Customer Success",
		"Reasoning: They are asking for product training.",
		"3. Ticket #103: Invoice shows wrong amount",
		"Route to: Billing Support",
		"Reasoning: The invoice charge is incorrect.",
		"Total tickets: 3",
		"Technical Support: 1",
		"Billing Support: 1",
		"Customer Success: 1",
	} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("output is missing %q", line)
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
	if err := run(ctx, &bytes.Buffer{}); err == nil {
		t.Fatal("run swallowed the provider failure")
	}
}
