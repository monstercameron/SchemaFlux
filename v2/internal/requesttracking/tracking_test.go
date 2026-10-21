package requesttracking

import (
	"context"
	"testing"
)

func TestResolveUsesExplicitIDs(t *testing.T) {
	Configure(Config{Enabled: true, RequestIDStrategy: IDStrategyAuto, CorrelationIDStrategy: CorrelationStrategyInherit})

	metadata := Resolve(context.Background(), "req-explicit", "corr-explicit")
	if metadata.RequestID != "req-explicit" {
		t.Fatalf("expected explicit request id, got %q", metadata.RequestID)
	}
	if metadata.CorrelationID != "corr-explicit" {
		t.Fatalf("expected explicit correlation id, got %q", metadata.CorrelationID)
	}
}

func TestResolveInheritsContextMetadata(t *testing.T) {
	Configure(Config{Enabled: true, RequestIDStrategy: IDStrategyAuto, CorrelationIDStrategy: CorrelationStrategyInherit})

	ctx := WithMetadata(context.Background(), Metadata{RequestID: "req-parent", CorrelationID: "corr-parent"})
	metadata := Resolve(ctx, "", "")

	if metadata.RequestID != "req-parent" {
		t.Fatalf("expected context request id, got %q", metadata.RequestID)
	}
	if metadata.CorrelationID != "corr-parent" {
		t.Fatalf("expected context correlation id, got %q", metadata.CorrelationID)
	}
}

func TestResolveGeneratesCorrelationFromRequestByDefault(t *testing.T) {
	Configure(Config{Enabled: true, RequestIDStrategy: IDStrategyNone, CorrelationIDStrategy: CorrelationStrategyRequest})

	metadata := Resolve(context.Background(), "req-123", "")
	if metadata.CorrelationID != "req-123" {
		t.Fatalf("expected correlation to reuse request id, got %q", metadata.CorrelationID)
	}
}

func TestResolveDisabledDoesNotGenerateIDs(t *testing.T) {
	Configure(Config{Enabled: false, RequestIDStrategy: IDStrategyAuto, CorrelationIDStrategy: CorrelationStrategyGenerate})

	metadata := Resolve(context.Background(), "", "")
	if metadata.RequestID != "" || metadata.CorrelationID != "" {
		t.Fatalf("expected empty metadata when disabled, got %#v", metadata)
	}
}

func TestInjectAndExtractHeaders(t *testing.T) {
	Configure(Config{Enabled: true, RequestIDHeader: "X-Test-Request-ID", CorrelationIDHeader: "X-Test-Correlation-ID"})

	carrier := map[string]string{}
	ctx := WithMetadata(context.Background(), Metadata{RequestID: "req-1", CorrelationID: "corr-1"})
	Inject(ctx, carrier)

	if carrier["X-Test-Request-ID"] != "req-1" {
		t.Fatalf("unexpected request header: %#v", carrier)
	}
	if carrier["X-Test-Correlation-ID"] != "corr-1" {
		t.Fatalf("unexpected correlation header: %#v", carrier)
	}

	extracted := FromContext(Extract(context.Background(), carrier))
	if extracted.RequestID != "req-1" || extracted.CorrelationID != "corr-1" {
		t.Fatalf("unexpected extracted metadata: %#v", extracted)
	}
}
