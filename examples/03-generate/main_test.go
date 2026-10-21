package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

const generatedUsersReply = `{"users":[{"id":1,"name":"Aisha Patel","email":"aisha.patel@example.com","age":27,"country":"India","role":"user","join_date":"2025-07-12","active":true}]}`

func TestRun(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(generatedUsersReply)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	for _, line := range []string{
		"🧪 Generate Example - Test Data Generator", "\"application\": \"social media\"",
		"✅ Generated 1 Test Users:", "Name:      Aisha Patel", "Email:     aisha.patel@example.com",
		"📦 JSON Output (ready for API):", "✨ Success! Generated 1 realistic test users from typed spec",
	} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("output missing %q", line)
		}
	}
	if model.Calls() != 1 {
		t.Fatalf("provider calls = %d, want 1", model.Calls())
	}
}

func TestRunProviderFailure(t *testing.T) {
	var model schemafluxtest.Provider
	model.Fail(nil)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err == nil {
		t.Fatal("run swallowed provider failure")
	}
}
