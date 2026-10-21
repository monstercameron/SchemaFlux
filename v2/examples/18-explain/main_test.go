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

func TestRunPrintsAllAudienceExplanations(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(
		`{"explanation":"The profile stores identity and account details for application use.","summary":"A technical profile record.","key_points":["JSON tags define serialized field names"]}`,
		`{"explanation":"The profile helps the business understand and serve a verified premium customer.","summary":"A premium customer profile.","key_points":["Interests can guide engagement"]}`,
		`{"explanation":"This is a set of details about Alice.","summary":"Alice's profile.","key_points":["It includes her name and interests"]}`,
	)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatalf("run returned an error: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"1. Technical Audience Explanation", "A technical profile record.",
		"2. Executive Audience Explanation", "A premium customer profile.",
		"3. Beginner-Friendly Explanation", "Alice's profile.",
		"Success! Data explained for different audiences",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output is missing %q", want)
		}
	}
	if model.Calls() != 3 {
		t.Errorf("provider calls = %d, want 3", model.Calls())
	}
}

func TestRunReturnsProviderFailure(t *testing.T) {
	var model schemafluxtest.Provider
	model.Fail(errors.New("scripted provider failure"))
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err == nil {
		t.Fatal("run swallowed the provider failure")
	}
	if strings.Contains(out.String(), "Success!") {
		t.Fatal("run printed success after a provider failure")
	}
}
