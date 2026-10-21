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

func TestRunDerivesThreeProfiles(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(
		`{"derived":{"customer_id":"CUST-8847","tenure_days":1361,"avg_order_value":181.76,"churn_risk":"low"},"derivations":[],"field_confidence":{},"overall_confidence":0.9}`,
		`{"derived":{"address":"1842 Maple Drive","price_per_sq_ft":307.02,"property_class":"luxury"},"derivations":[],"field_confidence":{},"overall_confidence":0.9}`,
		`{"derived":{"name":"Alex Rivera","seniority_level":"director","skill_categories":["distributed systems","leadership"]},"derivations":[],"field_confidence":{},"overall_confidence":0.9}`,
	)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"average order $181.76", "class luxury", "director"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q: %s", want, out.String())
		}
	}
	if model.Calls() != 3 {
		t.Errorf("calls=%d, want 3", model.Calls())
	}
}

func TestRunReturnsProviderFailure(t *testing.T) {
	var model schemafluxtest.Provider
	model.Fail(errors.New("offline failure"))
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model, Retries: -1})
	if err := run(ctx, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "derive customer features") {
		t.Errorf("error=%v", err)
	}
}
