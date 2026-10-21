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

func TestRunNegotiatesThreeCases(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(
		`{"deal":{"base_salary":145000,"remote_days":5,"bonus":12000},"deal_reached":true,"term_movements":[],"who_conceded_more":"theirs","our_satisfaction":0.8,"their_satisfaction":0.6,"reasoning":"split","confidence":0.7}`,
		`{"deal":{"price_per_unit":50,"quantity":700,"payment_days":45},"deal_reached":true,"term_movements":[],"who_conceded_more":"neither","our_satisfaction":0.7,"their_satisfaction":0.7,"reasoning":"split","confidence":0.7}`,
		`{"deal":{"valuation_millions":105,"earnout_percent":20,"retention_months":12},"deal_reached":true,"term_movements":[],"who_conceded_more":"ours","our_satisfaction":0.6,"their_satisfaction":0.8,"reasoning":"split","confidence":0.7}`,
	)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"base $145000", "unit price $50.00", "valuation $105M"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q: %s", want, out.String())
		}
	}
	if model.Calls() != 3 {
		t.Errorf("calls = %d, want 3", model.Calls())
	}
}

func TestRunReturnsProviderFailure(t *testing.T) {
	var model schemafluxtest.Provider
	model.Fail(errors.New("offline failure"))
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model, Retries: -1})
	if err := run(ctx, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "salary negotiation") {
		t.Errorf("error = %v", err)
	}
}
