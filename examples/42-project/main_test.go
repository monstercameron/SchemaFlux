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

func TestRunProjectsThreeSchemasWithoutExcludedFields(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(
		`{"projected":{"order_number":"ORD-2024-88921","item_count":3,"total":172.71,"status":"shipped","order_date":"2024-12-01"},"mappings":[],"lost":[],"inferred":[],"confidence":0.8}`,
		`{"projected":{"event_id":"TXN-2024120588821","amount_usd":156.5,"event_date":"2024-12-05","merchant_type":"books"},"mappings":[],"lost":[],"inferred":[],"confidence":0.8}`,
		`{"projected":{"employee_id":"E00421","full_name":"Sarah Johnson","department":"Engineering","start_date":"2019-03-15"},"mappings":[],"lost":[],"inferred":[],"confidence":0.8}`,
	)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Public order: ORD-2024-88921", "Analytics event: TXN-2024120588821", "Sarah Johnson"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q: %s", want, out.String())
		}
	}
	for _, req := range model.Requests() {
		if strings.Contains(req.User, "SSN-CANARY") || strings.Contains(req.User, "CARD-CANARY") {
			t.Errorf("excluded field reached provider: %s", req.User)
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
	if err := run(ctx, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "project public order") {
		t.Errorf("error=%v", err)
	}
}
