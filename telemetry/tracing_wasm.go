//go:build js && wasm

package telemetry

import (
	"context"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
	"go.opentelemetry.io/otel/trace"
)

func InitTracing(serviceName string) error {
	_ = serviceName
	return nil
}

func ShutdownTracing(ctx context.Context) error {
	_ = ctx
	return nil
}

func StartSpan(ctx context.Context, operation string, opts types.OpOptions) (context.Context, trace.Span) {
	_ = operation
	_ = opts
	return ctx, trace.SpanFromContext(ctx)
}

func RecordLLMCall(span trace.Span, model string, provider string, usage *types.TokenUsage, cost *types.CostInfo, duration time.Duration, err error) {
	_, _, _, _, _, _, _ = span, model, provider, usage, cost, duration, err
}

func AddSpanTags(ctx context.Context, tags map[string]string) {
	_, _ = ctx, tags
}

func RecordSpanEvent(ctx context.Context, name string, attributes map[string]any) {
	_, _, _ = ctx, name, attributes
}

func ExtractTraceContext(ctx context.Context, carrier map[string]string) context.Context {
	_ = carrier
	return ctx
}

func InjectTraceContext(ctx context.Context, carrier map[string]string) {
	_, _ = ctx, carrier
}

func GetTraceID(ctx context.Context) string {
	_ = ctx
	return ""
}

func GetSpanID(ctx context.Context) string {
	_ = ctx
	return ""
}
