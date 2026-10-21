package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func TestRunPrintsMergedCustomer(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(`{"id":"CRM-001","name":"John A. Smith","email":"john.smith@example.com","phone":"+1-555-0123","address":"123 Main Street, Springfield, Illinois","vip":true,"notes":"Prefers email contact; Has premium support plan"}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	for _, want := range []string{
		"Source Records:",
		"1. Record CRM-001:",
		"3. Record SUPPORT-789:",
		"Merging records...",
		"Merged Result:",
		"Name:    John A. Smith",
		"Email:   john.smith@example.com",
		"Address: 123 Main Street, Springfield, Illinois",
		"VIP:     true",
		"Merge Analysis:",
		"Input: 3 duplicate records",
		"Success! Customer records merged",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q", want)
		}
	}
	if model.Calls() != 1 {
		t.Errorf("provider calls = %d, want 1", model.Calls())
	}
}

func TestRunReturnsProviderFailure(t *testing.T) {
	var model schemafluxtest.Provider
	model.Fail(nil)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err == nil {
		t.Fatal("run returned nil error after provider failure")
	}
	if strings.Contains(out.String(), "Success!") {
		t.Fatal("run printed success after provider failure")
	}
}
