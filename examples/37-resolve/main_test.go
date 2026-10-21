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

func TestRunResolvesThreeSources(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(
		`{"resolved":{"customer_id":"CUST-5521","name":"John A. Smith","email":"jsmith@work.com","address":"123 Main Street, Boston","status":"active"},"conflicts":[],"source_contributions":{"0":[],"1":["name","address"],"2":[]},"confidence":0.7}`,
		`{"resolved":{"sku":"PROD-7890","name":"Wireless Headphones Pro","price":79.99,"stock":200},"conflicts":[],"source_contributions":{"0":["price"],"1":["name"],"2":["stock"]},"confidence":0.7}`,
		`{"resolved":{"employee_id":"EMP-1234","full_name":"Sarah M. Johnson","department":"Platform Engineering","title":"Staff Engineer"},"conflicts":[],"source_contributions":{"0":[],"1":["full_name","department","title"],"2":[]},"confidence":0.7}`,
	)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"John A. Smith", "stock: 200", "Staff Engineer"} {
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
	if err := run(ctx, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "resolve customer") {
		t.Errorf("error=%v", err)
	}
}
