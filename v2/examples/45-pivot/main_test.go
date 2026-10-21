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

func TestRunPivotsAndChecksThreeShapes(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(
		`{"pivoted":[{"sales_rep":"Alice","jan_revenue":45000,"feb_revenue":52000,"q1_total":97000}],"mappings":[],"stats":{"source_fields":3,"target_fields":4,"expansions":0,"compressions":1,"depth_change":0},"data_loss":[]}`,
		`{"pivoted":{"order_id":"ORD-2024-78421","customer_id":"CUST-1234","payment_amount":299.99},"mappings":[],"stats":{"source_fields":3,"target_fields":3,"expansions":0,"compressions":0,"depth_change":-1},"data_loss":[]}`,
		`{"pivoted":[{"respondent_id":"R001","satisfaction":"Very satisfied","would_recommend":"Yes"}],"mappings":[],"stats":{"source_fields":3,"target_fields":3,"expansions":0,"compressions":1,"depth_change":0},"data_loss":[]}`,
	)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"$97000", "customer CUST-1234", "recommends Yes"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q: %s", want, out.String())
		}
	}
	if model.Calls() != 3 {
		t.Errorf("calls=%d, want 3", model.Calls())
	}
}

func TestRunRefusesAWrongSalesTotal(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(`{"pivoted":[{"sales_rep":"Alice","jan_revenue":45000,"feb_revenue":52000,"q1_total":100000}],"mappings":[],"stats":{"source_fields":3,"target_fields":4,"expansions":0,"compressions":1,"depth_change":0},"data_loss":[]}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	if err := run(ctx, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "model totals did not match") {
		t.Errorf("error=%v", err)
	}
}

func TestRunReturnsProviderFailure(t *testing.T) {
	var model schemafluxtest.Provider
	model.Fail(errors.New("offline failure"))
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model, Retries: -1})
	if err := run(ctx, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "pivot sales") {
		t.Errorf("error=%v", err)
	}
}
