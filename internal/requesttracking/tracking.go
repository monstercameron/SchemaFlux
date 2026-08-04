package requesttracking

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	oteltrace "go.opentelemetry.io/otel/trace"
)

type IDStrategy string

type CorrelationStrategy string

type Config struct {
	Enabled               bool
	RequestIDStrategy     IDStrategy
	CorrelationIDStrategy CorrelationStrategy
	RequestIDHeader       string
	CorrelationIDHeader   string
}

type Metadata struct {
	RequestID     string
	CorrelationID string
}

const (
	IDStrategyAuto      IDStrategy = "auto"
	IDStrategyUUID      IDStrategy = "uuid"
	IDStrategyTimestamp IDStrategy = "timestamp"
	IDStrategyTrace     IDStrategy = "trace"
	IDStrategyNone      IDStrategy = "none"

	CorrelationStrategyInherit  CorrelationStrategy = "inherit"
	CorrelationStrategyRequest  CorrelationStrategy = "request"
	CorrelationStrategyGenerate CorrelationStrategy = "generate"
	CorrelationStrategyNone     CorrelationStrategy = "none"
)

var (
	configMu      sync.RWMutex
	configured    bool
	configuredCfg Config
	idCounter     uint64
)

type contextKey string

const (
	requestIDKey     contextKey = "schemaflux_request_id"
	correlationIDKey contextKey = "schemaflux_correlation_id"
)

func DefaultConfig() Config {
	cfg := Config{
		Enabled:               true,
		RequestIDStrategy:     IDStrategyAuto,
		CorrelationIDStrategy: CorrelationStrategyInherit,
		RequestIDHeader:       "X-Request-ID",
		CorrelationIDHeader:   "X-Correlation-ID",
	}

	if raw := strings.TrimSpace(os.Getenv("SCHEMAFLUX_REQUEST_TRACKING")); raw != "" {
		cfg.Enabled = envEnabled(raw)
	}
	if raw := strings.TrimSpace(os.Getenv("SCHEMAFLUX_REQUEST_ID_STRATEGY")); raw != "" {
		cfg.RequestIDStrategy = IDStrategy(strings.ToLower(raw))
	}
	if raw := strings.TrimSpace(os.Getenv("SCHEMAFLUX_CORRELATION_ID_STRATEGY")); raw != "" {
		cfg.CorrelationIDStrategy = CorrelationStrategy(strings.ToLower(raw))
	}
	if raw := strings.TrimSpace(os.Getenv("SCHEMAFLUX_REQUEST_ID_HEADER")); raw != "" {
		cfg.RequestIDHeader = raw
	}
	if raw := strings.TrimSpace(os.Getenv("SCHEMAFLUX_CORRELATION_ID_HEADER")); raw != "" {
		cfg.CorrelationIDHeader = raw
	}

	return normalizeConfig(cfg)
}

func Configure(cfg Config) {
	configMu.Lock()
	defer configMu.Unlock()
	configuredCfg = normalizeConfig(cfg)
	configured = true
}

func GetConfig() Config {
	configMu.RLock()
	if configured {
		cfg := configuredCfg
		configMu.RUnlock()
		return cfg
	}
	configMu.RUnlock()
	return DefaultConfig()
}

func WithRequestID(ctx context.Context, requestID string) context.Context {
	if strings.TrimSpace(requestID) == "" {
		return ctx
	}
	return context.WithValue(ctx, requestIDKey, requestID)
}

func WithCorrelationID(ctx context.Context, correlationID string) context.Context {
	if strings.TrimSpace(correlationID) == "" {
		return ctx
	}
	return context.WithValue(ctx, correlationIDKey, correlationID)
}

func WithMetadata(ctx context.Context, metadata Metadata) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if metadata.RequestID != "" {
		ctx = context.WithValue(ctx, requestIDKey, metadata.RequestID)
	}
	if metadata.CorrelationID != "" {
		ctx = context.WithValue(ctx, correlationIDKey, metadata.CorrelationID)
	}
	return ctx
}

func FromContext(ctx context.Context) Metadata {
	if ctx == nil {
		return Metadata{}
	}
	metadata := Metadata{}
	if requestID, ok := ctx.Value(requestIDKey).(string); ok {
		metadata.RequestID = requestID
	}
	if correlationID, ok := ctx.Value(correlationIDKey).(string); ok {
		metadata.CorrelationID = correlationID
	}
	return metadata
}

func Resolve(ctx context.Context, explicitRequestID, explicitCorrelationID string) Metadata {
	cfg := GetConfig()
	contextMetadata := FromContext(ctx)

	requestID := strings.TrimSpace(explicitRequestID)
	if requestID == "" {
		requestID = strings.TrimSpace(contextMetadata.RequestID)
	}

	correlationID := strings.TrimSpace(explicitCorrelationID)
	if correlationID == "" {
		correlationID = strings.TrimSpace(contextMetadata.CorrelationID)
	}

	if cfg.Enabled && requestID == "" {
		requestID = generateRequestID(cfg.RequestIDStrategy, ctx)
	}

	if cfg.Enabled && correlationID == "" {
		correlationID = generateCorrelationID(cfg.CorrelationIDStrategy, requestID, ctx)
	}

	return Metadata{
		RequestID:     requestID,
		CorrelationID: correlationID,
	}
}

func Ensure(ctx context.Context, explicitRequestID, explicitCorrelationID string) (context.Context, Metadata) {
	if ctx == nil {
		ctx = context.Background()
	}
	metadata := Resolve(ctx, explicitRequestID, explicitCorrelationID)
	return WithMetadata(ctx, metadata), metadata
}

func Inject(ctx context.Context, carrier map[string]string) {
	if carrier == nil {
		return
	}
	cfg := GetConfig()
	metadata := FromContext(ctx)
	if metadata.RequestID != "" {
		carrier[cfg.RequestIDHeader] = metadata.RequestID
	}
	if metadata.CorrelationID != "" {
		carrier[cfg.CorrelationIDHeader] = metadata.CorrelationID
	}
}

func Extract(ctx context.Context, carrier map[string]string) context.Context {
	if carrier == nil {
		return ctx
	}
	cfg := GetConfig()
	requestID := strings.TrimSpace(carrier[cfg.RequestIDHeader])
	correlationID := strings.TrimSpace(carrier[cfg.CorrelationIDHeader])
	return WithMetadata(ctx, Metadata{RequestID: requestID, CorrelationID: correlationID})
}

func normalizeConfig(cfg Config) Config {
	if cfg.RequestIDStrategy == "" {
		cfg.RequestIDStrategy = IDStrategyAuto
	}
	switch cfg.RequestIDStrategy {
	case IDStrategyAuto, IDStrategyUUID, IDStrategyTimestamp, IDStrategyTrace, IDStrategyNone:
	default:
		cfg.RequestIDStrategy = IDStrategyAuto
	}

	if cfg.CorrelationIDStrategy == "" {
		cfg.CorrelationIDStrategy = CorrelationStrategyInherit
	}
	switch cfg.CorrelationIDStrategy {
	case CorrelationStrategyInherit, CorrelationStrategyRequest, CorrelationStrategyGenerate, CorrelationStrategyNone:
	default:
		cfg.CorrelationIDStrategy = CorrelationStrategyInherit
	}

	if strings.TrimSpace(cfg.RequestIDHeader) == "" {
		cfg.RequestIDHeader = "X-Request-ID"
	}
	if strings.TrimSpace(cfg.CorrelationIDHeader) == "" {
		cfg.CorrelationIDHeader = "X-Correlation-ID"
	}
	return cfg
}

func generateRequestID(strategy IDStrategy, ctx context.Context) string {
	switch strategy {
	case IDStrategyNone:
		return ""
	case IDStrategyTrace:
		if traceID := currentTraceID(ctx); traceID != "" {
			return traceID
		}
		return generateIdentifier("req")
	case IDStrategyTimestamp:
		return generateTimestampID("req")
	case IDStrategyUUID, IDStrategyAuto:
		return generateIdentifier("req")
	default:
		return generateIdentifier("req")
	}
}

func generateCorrelationID(strategy CorrelationStrategy, requestID string, ctx context.Context) string {
	switch strategy {
	case CorrelationStrategyNone:
		return ""
	case CorrelationStrategyGenerate:
		return generateIdentifier("corr")
	case CorrelationStrategyRequest:
		if requestID != "" {
			return requestID
		}
		return generateIdentifier("corr")
	case CorrelationStrategyInherit:
		metadata := FromContext(ctx)
		if metadata.CorrelationID != "" {
			return metadata.CorrelationID
		}
		if requestID != "" {
			return requestID
		}
		return generateIdentifier("corr")
	default:
		if requestID != "" {
			return requestID
		}
		return generateIdentifier("corr")
	}
}

func generateIdentifier(prefix string) string {
	var bytes [8]byte
	if _, err := crand.Read(bytes[:]); err == nil {
		return prefix + "_" + hex.EncodeToString(bytes[:])
	}
	return generateTimestampID(prefix)
}

func generateTimestampID(prefix string) string {
	counter := atomic.AddUint64(&idCounter, 1)
	return fmt.Sprintf("%s_%d_%d_%d", prefix, time.Now().UnixNano(), os.Getpid(), counter)
}

func currentTraceID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	span := oteltrace.SpanFromContext(ctx)
	if span == nil {
		return ""
	}
	spanContext := span.SpanContext()
	if !spanContext.IsValid() {
		return ""
	}
	return spanContext.TraceID().String()
}

func envEnabled(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
