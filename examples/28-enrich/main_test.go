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

func TestRunEnrichesProductAndLead(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(
		`{"enriched":{"name":"Pro Gaming Keyboard RGB","description":"Mechanical keyboard with Cherry MX switches and RGB lighting","price":149.99,"category":"peripherals","keywords":["gaming","keyboard"],"target_market":"gamers"},"added_fields":["category","keywords","target_market"],"derivations":{}}`,
		`{"enriched":{"email":"john.smith@techcorp.io","company":"TechCorp Inc","title":"VP of Engineering","department":"engineering","seniority":"executive","industry":"technology"},"added_fields":["department","seniority","industry"],"derivations":{}}`,
	)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"peripherals", "gamers", "engineering", "executive"} {
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
	if err := run(ctx, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "enrich product") {
		t.Errorf("run error = %v, want product failure", err)
	}
}
