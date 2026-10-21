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

func TestRunComposesThreeKindsOfRecords(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(
		`{"composed":{"customer_id":"CUST-88421","full_name":"Sarah Johnson","total_spend":12450,"support_tickets":3,"tags":["loyal"]},"field_sources":[],"conflicts_resolved":0,"gaps_filled":[],"unused_parts":[],"completeness":1}`,
		`{"composed":{"ticker":"EXAMPLE","company_name":"Example Corp","sector":"Technology","investment_thesis":"Growing demand","key_risks":"Competition"},"field_sources":[],"conflicts_resolved":0,"gaps_filled":[],"unused_parts":[],"completeness":1}`,
		`{"composed":{"sku":"ELEC-LAPTOP-001","name":"ProBook 15 Laptop","price":899.99,"cost":650,"stock_level":145,"supplier":"TechDistributor Inc."},"field_sources":[],"conflicts_resolved":0,"gaps_filled":[],"unused_parts":[],"completeness":1}`,
	)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Sarah Johnson", "Example Corp", "ProBook 15 Laptop", "margin 27.8%"} {
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
	if err := run(ctx, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "compose customer profile") {
		t.Errorf("error=%v", err)
	}
}
