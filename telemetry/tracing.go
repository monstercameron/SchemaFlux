//go:build !js

// package telemetry - OpenTelemetry integration for distributed tracing
package telemetry

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/types"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/jaeger"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
	"go.opentelemetry.io/otel/trace"
)

var (
	// Global tracer instance
	tracer trace.Tracer

	// Trace provider for shutdown
	traceProvider *sdktrace.TracerProvider

	// Sampling rate for traces
	traceSampleRate float64 = 0.1

	// Enable/disable tracing
	tracingEnabled bool = false
)

// InitTracing initializes OpenTelemetry tracing with configured exporters
func InitTracing(serviceName string) error {
	// Check if tracing is enabled
	if !tracingEnvEnabled() {
		logger.GetLogger().Info("Tracing disabled")
		return nil
	}

	tracingEnabled = true

	// Parse sample rate
	if rate := os.Getenv("SCHEMAFLUX_TRACE_SAMPLE_RATE"); rate != "" {
		var r float64
		if _, err := fmt.Sscanf(rate, "%f", &r); err == nil && r >= 0 && r <= 1 {
			traceSampleRate = r
		}
	}

	// Create resource with service information
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion("1.0.0"),
			attribute.String("library", "schemaflux"),
			attribute.String("environment", getEnvironment()),
		),
	)
	if err != nil {
		return fmt.Errorf("failed to create resource: %w", err)
	}

	// Create exporters based on configuration
	var exporters []sdktrace.SpanExporter

	// Stdout exporter (for debugging)
	if stdout := os.Getenv("SCHEMAFLUX_EXPORT_TRACES_STDOUT"); stdout == "true" || stdout == "1" {
		stdoutExporter, err := stdouttrace.New(
			stdouttrace.WithPrettyPrint(),
		)
		if err != nil {
			logger.GetLogger().Error("Failed to create stdout exporter", "error", err)
		} else {
			exporters = append(exporters, stdoutExporter)
			logger.GetLogger().Info("Stdout trace exporter enabled")
		}
	}

	// Jaeger exporter
	if endpoint := os.Getenv("SCHEMAFLUX_JAEGER_ENDPOINT"); endpoint != "" {
		jaegerExporter, err := jaeger.New(
			jaeger.WithCollectorEndpoint(jaeger.WithEndpoint(endpoint)),
		)
		if err != nil {
			logger.GetLogger().Error("Failed to create Jaeger exporter", "error", err, "endpoint", endpoint)
		} else {
			exporters = append(exporters, jaegerExporter)
			logger.GetLogger().Info("Jaeger trace exporter enabled", "endpoint", endpoint)
		}
	}

	// OTLP exporter
	if endpoint := os.Getenv("SCHEMAFLUX_OTLP_ENDPOINT"); endpoint != "" {
		ctx := context.Background()
		client := otlptracegrpc.NewClient(
			otlptracegrpc.WithEndpoint(endpoint),
			otlptracegrpc.WithInsecure(),
		)

		otlpExporter, err := otlptrace.New(ctx, client)
		if err != nil {
			logger.GetLogger().Error("Failed to create OTLP exporter", "error", err, "endpoint", endpoint)
		} else {
			exporters = append(exporters, otlpExporter)
			logger.GetLogger().Info("OTLP trace exporter enabled", "endpoint", endpoint)
		}
	}

	// Create trace provider with exporters
	var opts []sdktrace.TracerProviderOption
	opts = append(opts, sdktrace.WithResource(res))

	// Add batch span processors for each exporter
	for _, exp := range exporters {
		opts = append(opts, sdktrace.WithBatcher(exp))
	}

	// Add sampler
	opts = append(opts, sdktrace.WithSampler(
		sdktrace.TraceIDRatioBased(traceSampleRate),
	))

	traceProvider = sdktrace.NewTracerProvider(opts...)

	// Register as global provider
	otel.SetTracerProvider(traceProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// Create tracer
	tracer = otel.Tracer("github.com/monstercameron/schemaflux")

	logger.GetLogger().Info("Tracing initialized",
		"serviceName", serviceName,
		"sampleRate", traceSampleRate,
		"exporters", len(exporters),
	)

	return nil
}

// ShutdownTracing cleanly shuts down the trace provider
func ShutdownTracing(ctx context.Context) error {
	if traceProvider != nil {
		return traceProvider.Shutdown(ctx)
	}
	return nil
}

// StartSpan starts a new trace span for an operation
func StartSpan(ctx context.Context, operation string, opts types.OpOptions) (context.Context, trace.Span) {
	if !tracingEnabled || tracer == nil {
		// Return no-op span if tracing is disabled
		return ctx, trace.SpanFromContext(ctx)
	}

	// Start span with operation name
	ctx, span := tracer.Start(ctx, fmt.Sprintf("schemaflux.%s", operation),
		trace.WithSpanKind(trace.SpanKindClient),
	)

	// Add standard attributes
	span.SetAttributes(
		attribute.String("schemaflux.operation", operation),
		attribute.String("schemaflux.mode", opts.Mode.String()),
		attribute.String("schemaflux.intelligence", opts.Intelligence.String()),
		attribute.Float64("schemaflux.threshold", opts.Threshold),
	)

	// Add request ID if present
	if opts.RequestID != "" {
		span.SetAttributes(attribute.String("schemaflux.request_id", opts.RequestID))
	}
	if opts.CorrelationID != "" {
		span.SetAttributes(attribute.String("schemaflux.correlation_id", opts.CorrelationID))
	}

	// Add steering if present
	if opts.Steering != "" {
		span.SetAttributes(attribute.String("schemaflux.steering", truncateString(opts.Steering, 200)))
	}

	return ctx, span
}

// RecordLLMCall records details of an LLM API call in the span
func RecordLLMCall(span trace.Span, model string, provider string, usage *types.TokenUsage, cost *types.CostInfo, duration time.Duration, err error) {
	if span == nil || !span.IsRecording() {
		return
	}

	// Record model and provider
	span.SetAttributes(
		attribute.String("llm.model", model),
		attribute.String("llm.provider", provider),
		attribute.Int64("llm.duration_ms", duration.Milliseconds()),
	)

	// Record token usage
	if usage != nil {
		span.SetAttributes(
			attribute.Int("llm.tokens.prompt", usage.PromptTokens),
			attribute.Int("llm.tokens.completion", usage.CompletionTokens),
			attribute.Int("llm.tokens.total", usage.TotalTokens),
		)

		if usage.CachedTokens > 0 {
			span.SetAttributes(attribute.Int("llm.tokens.cached", usage.CachedTokens))
		}
		if usage.ReasoningTokens > 0 {
			span.SetAttributes(attribute.Int("llm.tokens.reasoning", usage.ReasoningTokens))
		}
	}

	// Record cost information
	if cost != nil {
		span.SetAttributes(
			attribute.Float64("llm.cost.total_usd", cost.TotalCost),
			attribute.Float64("llm.cost.prompt_usd", cost.PromptCost),
			attribute.Float64("llm.cost.completion_usd", cost.CompletionCost),
			attribute.String("llm.cost.currency", cost.Currency),
		)

		if cost.CachedCost > 0 {
			span.SetAttributes(attribute.Float64("llm.cost.cached_usd", cost.CachedCost))
		}
		if cost.ReasoningCost > 0 {
			span.SetAttributes(attribute.Float64("llm.cost.reasoning_usd", cost.ReasoningCost))
		}
	}

	// Record error if present
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		span.SetAttributes(attribute.String("llm.error", err.Error()))
	} else {
		span.SetStatus(codes.Ok, "success")
	}
}

// AddSpanTags adds custom tags to the current span
func AddSpanTags(ctx context.Context, tags map[string]string) {
	span := trace.SpanFromContext(ctx)
	if span == nil || !span.IsRecording() {
		return
	}

	for k, v := range tags {
		span.SetAttributes(attribute.String(k, v))
	}
}

// RecordSpanEvent records a custom event in the span
func RecordSpanEvent(ctx context.Context, name string, attributes map[string]any) {
	span := trace.SpanFromContext(ctx)
	if span == nil || !span.IsRecording() {
		return
	}

	var attrs []attribute.KeyValue
	for k, v := range attributes {
		switch val := v.(type) {
		case string:
			attrs = append(attrs, attribute.String(k, val))
		case int:
			attrs = append(attrs, attribute.Int(k, val))
		case int64:
			attrs = append(attrs, attribute.Int64(k, val))
		case float64:
			attrs = append(attrs, attribute.Float64(k, val))
		case bool:
			attrs = append(attrs, attribute.Bool(k, val))
		default:
			attrs = append(attrs, attribute.String(k, fmt.Sprintf("%v", val)))
		}
	}

	span.AddEvent(name, trace.WithAttributes(attrs...))
}

// ExtractTraceContext extracts trace context from a carrier (for distributed tracing)
func ExtractTraceContext(ctx context.Context, carrier map[string]string) context.Context {
	if !tracingEnabled {
		return ctx
	}

	propagator := otel.GetTextMapPropagator()
	return propagator.Extract(ctx, propagation.MapCarrier(carrier))
}

// InjectTraceContext injects trace context into a carrier (for distributed tracing)
func InjectTraceContext(ctx context.Context, carrier map[string]string) {
	if !tracingEnabled {
		return
	}

	propagator := otel.GetTextMapPropagator()
	propagator.Inject(ctx, propagation.MapCarrier(carrier))
}

// GetTraceID returns the current trace ID from context
func GetTraceID(ctx context.Context) string {
	span := trace.SpanFromContext(ctx)
	if span != nil {
		return span.SpanContext().TraceID().String()
	}
	return ""
}

// GetSpanID returns the current span ID from context
func GetSpanID(ctx context.Context) string {
	span := trace.SpanFromContext(ctx)
	if span != nil {
		return span.SpanContext().SpanID().String()
	}
	return ""
}

// Helper functions

func getEnvironment() string {
	if env := os.Getenv("SCHEMAFLUX_ENVIRONMENT"); env != "" {
		return env
	}
	if env := os.Getenv("ENVIRONMENT"); env != "" {
		return env
	}
	if env := os.Getenv("ENV"); env != "" {
		return env
	}
	return "development"
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func tracingEnvEnabled() bool {
	for _, key := range []string{"SCHEMAFLUX_ENABLE_TRACING", "SCHEMAFLUX_TRACE"} {
		switch os.Getenv(key) {
		case "true", "1":
			return true
		}
	}
	return false
}
