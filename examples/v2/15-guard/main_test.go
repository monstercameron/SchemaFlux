package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func TestRun(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(
		`{"is_safe":true,"violations":[],"risk_level":"low","explanation":"Routine order question.","action":"allow"}`,
		`{"is_safe":true,"violations":[],"risk_level":"medium","explanation":"Frustrated customer, no policy violation.","action":"allow"}`,
		`{"is_safe":false,"violations":["discriminatory language"],"risk_level":"high","explanation":"Targets a nationality.","action":"block"}`,
		`{"is_safe":false,"violations":["credential phishing"],"risk_level":"critical","explanation":"Requests passwords and payment data.","action":"block"}`,
	)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	for _, line := range []string{
		"1. Message from user_001 (support channel)", "SAFE - allow", "Risk Level: low", "Routine order question.",
		"2. Message from user_002 (support channel)", "SAFE - allow", "Risk Level: medium", "Frustrated customer, no policy violation.",
		"3. Message from user_003 (community channel)", "UNSAFE - block", "Risk Level: high", "discriminatory language",
		"4. Message from user_004 (support channel)", "UNSAFE - block", "Risk Level: critical", "credential phishing", "Total messages checked: 4",
		"Safe (allowed): 2", "Unsafe (blocked): 2", "Success! LLM content safety checks complete",
	} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("output missing %q", line)
		}
	}
	if got := model.Calls(); got != 4 {
		t.Errorf("provider calls = %d, want 4", got)
	}
}

func TestRunReturnsProviderFailure(t *testing.T) {
	var model schemafluxtest.Provider
	model.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable})
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err == nil {
		t.Fatal("run swallowed provider failure")
	}
}
