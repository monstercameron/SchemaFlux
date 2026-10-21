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

func TestRunPrintsUrgentTickets(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(`{"ids":["i-000001","i-000003","i-000005"]}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatalf("run returned an error: %v", err)
	}

	for _, line := range []string{
		"🎫 Filter Example - Urgent Ticket Triage",
		"📥 Total Tickets: 6",
		"#1 - Alice Johnson: Website is completely down",
		"#6 - Frank Miller: Invoice copy request",
		"🚨 URGENT Tickets (require immediate attention):",
		"⚠️  Ticket #1 - Alice Johnson",
		"⚠️  Ticket #3 - Carol White",
		"⚠️  Ticket #5 - Eve Davis",
		"✨ Success! Filtered 3 urgent tickets from 6 total",
	} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("output missing %q", line)
		}
	}
}

func TestRunReturnsProviderFailure(t *testing.T) {
	var model schemafluxtest.Provider
	model.Fail(errors.New("provider unavailable"))
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	var out bytes.Buffer
	err := run(ctx, &out)
	if err == nil {
		t.Fatal("run returned nil error after provider failure")
	}
	if !strings.Contains(err.Error(), "filter urgent tickets") {
		t.Errorf("error = %q, want filter context", err)
	}
}
