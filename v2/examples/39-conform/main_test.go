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

func TestRunConformsThreeStandards(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(
		`{"conformed":{"name":"DR JOHN Q SMITH JR","street1":"123 N MAIN ST","city":"LOS ANGELES","state":"CA","postal_code":"90210","country":"USA"},"adjustments":[],"violations":[],"compliance":1,"standard":"USPS"}`,
		`{"conformed":{"transaction_id":"PAY123456","amount":15750,"currency":"USD","timestamp":"2024-12-15T20:45:00Z","sender_iban":"DE89370400440532013000"},"adjustments":[],"violations":[],"compliance":1,"standard":"ISO 20022"}`,
		`{"conformed":{"patient_id":"PAT-00123456","dob":"1985-03-15","gender":"female","ssn":"XXX-XX-6789","phone":"+15552345678"},"adjustments":[],"violations":[],"compliance":1,"standard":"HL7 FHIR R4"}`,
	)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"123 N MAIN ST", "USD at 2024-12-15", "birth date 1985-03-15"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q: %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "123-45-6789") {
		t.Error("output includes raw SSN")
	}
	if model.Calls() != 3 {
		t.Errorf("calls=%d, want 3", model.Calls())
	}
}

func TestRunReturnsProviderFailure(t *testing.T) {
	var model schemafluxtest.Provider
	model.Fail(errors.New("offline failure"))
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model, Retries: -1})
	if err := run(ctx, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "conform address") {
		t.Errorf("error=%v", err)
	}
}
