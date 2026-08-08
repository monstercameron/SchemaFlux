package otel

import (
	"context"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

func TestStartSpan(t *testing.T) {
	// Test span creation
	ctx := context.Background()
	opts := types.OpOptions{
		Mode:         types.TransformMode,
		Intelligence: types.Fast,
	}

	newCtx, span := StartSpan(ctx, "test-operation", opts)
	if span == nil {
		t.Error("Expected span to be created")
	}
	defer span.End()

	if newCtx == nil {
		t.Error("Expected context with span")
	}
}

func TestRecordLLMCall(t *testing.T) {
	ctx := context.Background()
	opts := types.OpOptions{
		Mode:         types.TransformMode,
		Intelligence: types.Fast,
	}

	_, span := StartSpan(ctx, "test-operation", opts)
	defer span.End()

	// Test recording LLM call
	usage := &types.TokenUsage{
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
	}

	cost := &types.CostInfo{
		TotalCost: 0.05,
		Currency:  "USD",
	}

	RecordLLMCall(span, "gpt-5-nano-2025-08-07", "openai", usage, cost, 1*time.Second, nil)

	// No error expected, just ensuring it doesn't panic
}

func TestAddSpanTags(t *testing.T) {
	ctx := context.Background()
	opts := types.OpOptions{
		Mode:         types.TransformMode,
		Intelligence: types.Fast,
	}

	newCtx, span := StartSpan(ctx, "test-operation", opts)
	defer span.End()

	// Test adding span tags
	tags := map[string]string{
		"user":      "test-user",
		"operation": "test",
	}

	AddSpanTags(newCtx, tags)

	// No error expected, just ensuring it doesn't panic
}

func TestGetSpanID(t *testing.T) {
	// StartSpan is the LEGACY path: it reads the package-level `tracer` and
	// `tracingEnabled`, which only InitTracing sets. Install() wires the
	// Observer seam instead and deliberately does not touch those globals, so a
	// span started here is non-recording however the provider is configured.
	//
	// This test previously asserted GetSpanID returned something, and passed --
	// not because a span existed, but because GetSpanID returned the all-zero
	// placeholder for an untraced context. Non-empty, so the assertion held
	// while measuring nothing at all.
	//
	// With that fixed, the honest assertion is the one that is actually true on
	// this path: with tracing uninitialised, there is no span and therefore no
	// ID, and both accessors say so rather than inventing a plausible-looking
	// one. TestAnUntracedContextYieldsNoIDRatherThanAZeroOne covers the same
	// property from the other direction, and otel_more_test.go covers the
	// Install()/Observer path where IDs are real.
	ctx := context.Background()
	opts := types.OpOptions{
		Mode:         types.TransformMode,
		Intelligence: types.Fast,
	}

	newCtx, span := StartSpan(ctx, "test-operation", opts)
	defer span.End()

	if spanID := GetSpanID(newCtx); spanID != "" {
		t.Errorf("GetSpanID = %q with tracing uninitialised; StartSpan returned a non-recording span, so there is no ID to report", spanID)
	}
	if traceID := GetTraceID(newCtx); traceID != "" {
		t.Errorf("GetTraceID = %q with tracing uninitialised; want the empty string", traceID)
	}
}

func TestTracingEnvEnabled(t *testing.T) {
	t.Setenv("SCHEMAFLUX_ENABLE_TRACING", "")
	t.Setenv("SCHEMAFLUX_TRACE", "")
	if tracingEnvEnabled() {
		t.Fatal("expected tracing to be disabled when env vars are unset")
	}

	t.Setenv("SCHEMAFLUX_TRACE", "true")
	if !tracingEnvEnabled() {
		t.Fatal("expected SCHEMAFLUX_TRACE=true to enable tracing")
	}

	t.Setenv("SCHEMAFLUX_TRACE", "")
	t.Setenv("SCHEMAFLUX_ENABLE_TRACING", "1")
	if !tracingEnvEnabled() {
		t.Fatal("expected SCHEMAFLUX_ENABLE_TRACING=1 to enable tracing")
	}
}
