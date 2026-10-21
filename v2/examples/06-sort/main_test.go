package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func TestRunSortsTasks(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(`{"ids":["i-000002","i-000004","i-000001","i-000003","i-000005"]}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	got := out.String()
	lines := []string{
		"Sort Example - Task Prioritization",
		"1. Update documentation (Deadline: Next week)",
		"2. Fix critical security vulnerability (Deadline: Today)",
		"3. Add dark mode feature (Deadline: Next month)",
		"4. Database backup failing (Deadline: ASAP)",
		"5. Refactor payment module (Deadline: Q2 2025)",
		"\n1. Fix critical security vulnerability",
		"\n2. Database backup failing",
		"\n3. Update documentation",
		"\n4. Add dark mode feature",
		"\n5. Refactor payment module",
		"Success! Tasks intelligently prioritized",
	}
	last := -1
	for _, line := range lines {
		next := strings.Index(got, line)
		if next <= last {
			t.Fatalf("expected line %q after offset %d in output:\n%s", line, last, got)
		}
		last = next
	}
	if model.Calls() != 1 {
		t.Fatalf("provider calls = %d, want 1", model.Calls())
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
		t.Fatalf("run printed success after provider failure:\n%s", out.String())
	}
}
