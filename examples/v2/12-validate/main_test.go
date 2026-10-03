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

func TestRunPrintsAllValidationScenarios(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(
		`{"errors":[],"warnings":[],"info":[],"confidence":0.99,"summary":"Registration meets the rules."}`,
		`{"errors":[{"field":"email","message":"Email format is invalid","suggestion":"Use a valid email address."}],"warnings":[],"info":[],"confidence":0.97,"summary":"Email needs correction."}`,
		`{"errors":[{"field":"password","message":"Password is too short and misses required characters."}],"warnings":[],"info":[],"confidence":0.98,"summary":"Password needs strengthening."}`,
		`{"errors":[{"field":"age","message":"Age must be at least 18."}],"warnings":[],"info":[],"confidence":0.99,"summary":"Registrant is underage."}`,
	)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"1. Valid Registration", "✅ VALID - Registration accepted", "99%",
		"2. Invalid Email", "[email] Email format is invalid", "Use a valid email address.",
		"3. Weak Password", "[password] Password is too short and misses required characters.",
		"4. Underage User", "[age] Age must be at least 18.",
		"Total tested: 4 registrations", "Success! Validation complete",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output does not contain %q", want)
		}
	}
	if calls := model.Calls(); calls != 4 {
		t.Errorf("provider received %d calls, want 4", calls)
	}
}

func TestRunReturnsProviderFailure(t *testing.T) {
	var model schemafluxtest.Provider
	model.Fail(errors.New("scripted provider failure"))
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err == nil {
		t.Fatal("run swallowed provider failure")
	}
	if calls := model.Calls(); calls != 1 {
		t.Errorf("provider received %d calls, want 1", calls)
	}
}
