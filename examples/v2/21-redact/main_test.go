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

func TestRunWithScriptedProvider(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(
		`{"spans":[{"text":"John Smith","category":"name"},{"text":"john.smith@company.com","category":"email"},{"text":"555-123-4567","category":"phone"}]}`,
		`{"spans":[{"text":"alice.johnson@example.org","category":"email"},{"text":"123-45-6789","category":"ssn"}]}`,
		`{"spans":[{"text":"4532-1234-5678-9012","category":"credit_card"}]}`,
		`{"spans":[{"text":"sk-abc123xyz789secret","category":"api_key"},{"text":"MyS3cretP@ss!","category":"password"}]}`,
		`{"spans":[{"text":"Bob Wilson","category":"name"},{"text":"bob.wilson@gmail.com","category":"email"},{"text":"(555) 987-6543","category":"phone"},{"text":"123 Main Street, New York, NY 10001","category":"address"},{"text":"03/15/1985","category":"date_of_birth"},{"text":"987-65-4321","category":"ssn"}]}`,
		`{"spans":[{"text":"admin@company.com","category":"email"},{"text":"support@help.org","category":"email"}]}`,
	)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, want := range []string{
		"1. Basic PII Detection",
		"2. Partial Reveal - Show First 2 and Last 2 Characters",
		"3. Custom Mask Character (|)",
		"4. Detect Secrets and API Keys",
		"5. Complex Text with Multiple PII Types",
		"6. Detect Only Email Addresses",
		"Spans detected:",
		"Sensitive text detection and masking completed.",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q", want)
		}
	}
	if got := strings.Count(out.String(), "Output:"); got != 6 {
		t.Errorf("output lines = %d, want 6", got)
	}
	if got := model.Calls(); got != 6 {
		t.Errorf("provider calls = %d, want 6", got)
	}
}

func TestRunReturnsProviderFailure(t *testing.T) {
	var model schemafluxtest.Provider
	model.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "provider unavailable"})
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	err := run(ctx, &out)
	if err == nil {
		t.Fatal("run returned nil after provider failure")
	}
	if !errors.Is(err, schemaflux.ErrUnavailable) {
		t.Fatalf("run error = %v, want ErrUnavailable", err)
	}
}
