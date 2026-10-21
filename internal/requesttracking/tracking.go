package requesttracking

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	mrand "math/rand/v2"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/config"
)

// IDStrategy names how a request ID is made when the caller supplied none.
type IDStrategy string

// CorrelationStrategy names how a correlation ID is made when the caller
// supplied none.
type CorrelationStrategy string

// Config says whether requests are tracked, how their IDs are made, and which
// header names carry them across a process boundary.
type Config struct {
	Enabled               bool
	RequestIDStrategy     IDStrategy
	CorrelationIDStrategy CorrelationStrategy
	RequestIDHeader       string
	CorrelationIDHeader   string
}

// Metadata is the request and correlation ID a context carries.
type Metadata struct {
	RequestID     string
	CorrelationID string
}

const (
	// IDStrategyAuto makes an ID of the form "req_" and sixteen hex digits.
	IDStrategyAuto IDStrategy = "auto"

	// IDStrategyUUID makes a random (version 4) UUID.
	IDStrategyUUID IDStrategy = "uuid"

	// IDStrategyTimestamp makes an ID from the clock, the process ID and a
	// counter.
	IDStrategyTimestamp IDStrategy = "timestamp"

	// IDStrategyTrace uses the trace ID of the span on the context, and falls
	// back to IDStrategyAuto when there is none.
	IDStrategyTrace IDStrategy = "trace"

	// IDStrategyNone makes no request ID.
	IDStrategyNone IDStrategy = "none"

	// CorrelationStrategyInherit keeps the correlation ID the context carries and
	// otherwise uses the request ID.
	CorrelationStrategyInherit CorrelationStrategy = "inherit"

	// CorrelationStrategyRequest uses the request ID.
	CorrelationStrategyRequest CorrelationStrategy = "request"

	// CorrelationStrategyGenerate makes a new ID.
	CorrelationStrategyGenerate CorrelationStrategy = "generate"

	// CorrelationStrategyNone makes no correlation ID.
	CorrelationStrategyNone CorrelationStrategy = "none"
)

// maxExtractedIDLength bounds an ID taken from a carrier. The carrier is a
// request header from another process, and the ID goes into logs and back out
// in Inject, so it is not trusted to be short or to hold only printable text.
const maxExtractedIDLength = 128

var (
	configMu      sync.RWMutex
	configured    bool
	configuredCfg Config

	// environmentCfg is the configuration the environment gave, read the first
	// time GetConfig needs it and kept until config.ReloadEnvironment, which
	// config.Init calls. It was read again on every call, five variables each
	// time, on the path of every provider request. environmentMu makes
	// goroutines that arrive together share one read, so a malformed value is
	// reported once.
	environmentMu  sync.Mutex
	environmentCfg atomic.Pointer[Config]

	idCounter atomic.Uint64
)

func init() {
	config.OnEnvironmentReload(forgetEnvironmentConfig)
}

// forgetEnvironmentConfig makes the next GetConfig that has no configuration of
// its own read the environment again. A configuration given to Configure is
// kept: it was made in code, and the environment does not replace it.
func forgetEnvironmentConfig() {
	environmentMu.Lock()
	defer environmentMu.Unlock()
	environmentCfg.Store(nil)
}

type contextKey string

const (
	requestIDKey     contextKey = "schemaflux_request_id"
	correlationIDKey contextKey = "schemaflux_correlation_id"
)

// DefaultConfig reads the configuration the environment describes:
// SCHEMAFLUX_REQUEST_TRACKING, SCHEMAFLUX_REQUEST_ID_STRATEGY,
// SCHEMAFLUX_CORRELATION_ID_STRATEGY, SCHEMAFLUX_REQUEST_ID_HEADER and
// SCHEMAFLUX_CORRELATION_ID_HEADER. A value that is not one the variable takes is
// reported once, naming the variable, and the default applies.
//
// It reads the environment each time it is called. GetConfig does not: it reads
// the environment once, and again after config.ReloadEnvironment. Calling
// Configure(DefaultConfig()) instead pins what the environment says now, and the
// environment is not read again.
func DefaultConfig() Config {
	cfg := Config{
		Enabled:               config.EnvBoolOr("SCHEMAFLUX_REQUEST_TRACKING", true),
		RequestIDStrategy:     IDStrategy(config.EnvChoice("SCHEMAFLUX_REQUEST_ID_STRATEGY", string(IDStrategyAuto), "auto", "uuid", "timestamp", "trace", "none")),
		CorrelationIDStrategy: CorrelationStrategy(config.EnvChoice("SCHEMAFLUX_CORRELATION_ID_STRATEGY", string(CorrelationStrategyInherit), "inherit", "request", "generate", "none")),
		RequestIDHeader:       "X-Request-ID",
		CorrelationIDHeader:   "X-Correlation-ID",
	}

	if raw := strings.TrimSpace(os.Getenv("SCHEMAFLUX_REQUEST_ID_HEADER")); raw != "" {
		cfg.RequestIDHeader = raw
	}
	if raw := strings.TrimSpace(os.Getenv("SCHEMAFLUX_CORRELATION_ID_HEADER")); raw != "" {
		cfg.CorrelationIDHeader = raw
	}

	return normalizeConfig(cfg)
}

// Configure sets the configuration for the whole process, replacing the one the
// environment gave. A strategy it does not know becomes the default one.
func Configure(cfg Config) {
	configMu.Lock()
	defer configMu.Unlock()
	configuredCfg = normalizeConfig(cfg)
	configured = true
}

// GetConfig returns the configuration in force: the one given to Configure, or
// otherwise the one the environment described when it was last read. The
// environment is read the first time it is needed and again after
// config.ReloadEnvironment (so after config.Init).
func GetConfig() Config {
	configMu.RLock()
	if configured {
		cfg := configuredCfg
		configMu.RUnlock()
		return cfg
	}
	configMu.RUnlock()

	if held := environmentCfg.Load(); held != nil {
		return *held
	}
	environmentMu.Lock()
	defer environmentMu.Unlock()
	if held := environmentCfg.Load(); held != nil {
		return *held
	}
	cfg := DefaultConfig()
	environmentCfg.Store(&cfg)
	return cfg
}

// WithRequestID returns ctx carrying requestID. An empty or blank ID, and a nil
// ctx, are not an error: a blank ID leaves ctx as it was, and a nil ctx is taken
// as context.Background().
func WithRequestID(ctx context.Context, requestID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return ctx
	}
	return context.WithValue(ctx, requestIDKey, requestID)
}

// WithCorrelationID returns ctx carrying correlationID, with the same treatment
// of a blank ID and a nil ctx as WithRequestID.
func WithCorrelationID(ctx context.Context, correlationID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	correlationID = strings.TrimSpace(correlationID)
	if correlationID == "" {
		return ctx
	}
	return context.WithValue(ctx, correlationIDKey, correlationID)
}

// WithMetadata returns ctx carrying the IDs in metadata. An ID that is empty or
// blank is not stored, so it cannot hide as a value later. A nil ctx is taken as
// context.Background(), by WithRequestID.
func WithMetadata(ctx context.Context, metadata Metadata) context.Context {
	ctx = WithRequestID(ctx, metadata.RequestID)
	return WithCorrelationID(ctx, metadata.CorrelationID)
}

// FromContext reads the IDs ctx carries. A nil ctx carries none.
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

// Resolve decides the IDs for one request: an explicit ID wins, then one the
// context carries, then one made by the configured strategy. With tracking
// disabled it makes none, and returns only what was supplied.
func Resolve(ctx context.Context, explicitRequestID, explicitCorrelationID string) Metadata {
	resolved, _ := resolve(ctx, explicitRequestID, explicitCorrelationID)
	return resolved
}

// resolve is Resolve, and also returns what the context already carried. The
// configuration is read only when an ID has to be made: a request that already
// has both takes no part in it.
func resolve(ctx context.Context, explicitRequestID, explicitCorrelationID string) (resolved, carried Metadata) {
	carried = FromContext(ctx)

	requestID := strings.TrimSpace(explicitRequestID)
	if requestID == "" {
		requestID = strings.TrimSpace(carried.RequestID)
	}

	correlationID := strings.TrimSpace(explicitCorrelationID)
	if correlationID == "" {
		correlationID = strings.TrimSpace(carried.CorrelationID)
	}

	if requestID == "" || correlationID == "" {
		cfg := GetConfig()
		if cfg.Enabled && requestID == "" {
			requestID = generateRequestID(cfg.RequestIDStrategy, ctx)
		}
		if cfg.Enabled && correlationID == "" {
			correlationID = generateCorrelationID(cfg.CorrelationIDStrategy, requestID)
		}
	}

	return Metadata{
		RequestID:     requestID,
		CorrelationID: correlationID,
	}, carried
}

// Ensure resolves the IDs for a request and returns a context that carries them.
//
// A context that already carries exactly these IDs is returned as it is. The
// operations resolve a call's IDs once, on the way in, and the provider call
// beneath them asks again with the IDs it was handed, so most calls reach here
// with nothing to add and used to wrap the context twice more regardless.
func Ensure(ctx context.Context, explicitRequestID, explicitCorrelationID string) (context.Context, Metadata) {
	if ctx == nil {
		ctx = context.Background()
	}
	metadata, carried := resolve(ctx, explicitRequestID, explicitCorrelationID)
	if carried == metadata {
		return ctx, metadata
	}
	return WithMetadata(ctx, metadata), metadata
}

// Inject writes the IDs ctx carries into carrier under the configured header
// names. A nil carrier is left alone.
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

// Extract reads the IDs from carrier into ctx. The carrier comes from another
// process, so an ID is kept only if it is at most 128 bytes of printable ASCII
// without spaces; anything else is dropped as if the header were absent. Header
// names are matched without regard to case, because a carrier built from an
// http.Header holds "X-Request-Id" and the configured name is "X-Request-ID".
func Extract(ctx context.Context, carrier map[string]string) context.Context {
	if carrier == nil {
		return ctx
	}
	cfg := GetConfig()
	requestID := carrierID(carrier, cfg.RequestIDHeader)
	correlationID := carrierID(carrier, cfg.CorrelationIDHeader)
	return WithMetadata(ctx, Metadata{RequestID: requestID, CorrelationID: correlationID})
}

// carrierID looks up header in carrier, exact name first and then without regard
// to case, and returns the value if it is an acceptable ID and "" otherwise.
func carrierID(carrier map[string]string, header string) string {
	value, found := carrier[header]
	if !found {
		for name, candidate := range carrier {
			if strings.EqualFold(name, header) {
				value, found = candidate, true
				break
			}
		}
	}
	if !found {
		return ""
	}
	value = strings.TrimSpace(value)
	if len(value) > maxExtractedIDLength {
		return ""
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 0x21 || value[i] > 0x7e {
			return ""
		}
	}
	return value
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
	case IDStrategyUUID:
		return generateUUID()
	default:
		return generateIdentifier("req")
	}
}

// generateCorrelationID makes the correlation ID for a request that was
// supplied none. An inherited one has already been taken from the context by
// Resolve, so the inherit strategy has nothing of its own to add: it falls back
// to the request ID, as the request strategy does.
func generateCorrelationID(strategy CorrelationStrategy, requestID string) string {
	switch strategy {
	case CorrelationStrategyNone:
		return ""
	case CorrelationStrategyGenerate:
		return generateIdentifier("corr")
	default:
		if requestID != "" {
			return requestID
		}
		return generateIdentifier("corr")
	}
}

// identifierStride is odd, so multiplying a counter by it modulo 2^64 gives a
// different number for every counter value, and it is large and uneven so that
// consecutive identifiers do not look consecutive.
const identifierStride uint64 = 0x9E3779B97F4A7C15

var (
	// identifierBase is the process's starting point, 0 until it has been chosen.
	// seedIdentifierBase never returns 0, so 0 can mean "not chosen".
	identifierBase    atomic.Uint64
	identifierCounter atomic.Uint64
)

// seedIdentifierBase chooses the starting point of a process's identifiers. It
// is what keeps two processes from emitting the same sequence: with every process
// starting from one point, all of them would make the same identifiers in the
// same order. It never returns 0, which identifierBase uses for "not chosen yet".
func seedIdentifierBase() uint64 {
	for {
		if seed := mrand.Uint64(); seed != 0 {
			return seed
		}
	}
}

// identifierStart returns the process's starting point, choosing it the first
// time. Goroutines that arrive together may each choose one; the first stored is
// the one every one of them uses.
func identifierStart() uint64 {
	if base := identifierBase.Load(); base != 0 {
		return base
	}
	identifierBase.CompareAndSwap(0, seedIdentifierBase())
	return identifierBase.Load()
}

// identifierAt is the identifier for the counter'th call of a process whose
// starting point is base: a prefix and the sixteen hex digits of base plus
// counter times identifierStride, modulo 2^64.
func identifierAt(prefix string, base, counter uint64) string {
	value := base + counter*identifierStride

	var buffer [32]byte
	encoded := append(buffer[:0], prefix...)
	encoded = append(encoded, '_')
	for shift := 60; shift >= 0; shift -= 4 {
		encoded = append(encoded, "0123456789abcdef"[(value>>uint(shift))&0xf])
	}
	return string(encoded)
}

// generateIdentifier makes a prefix and sixteen hex digits.
//
// The sixteen digits are a 64-bit number: a starting point chosen at random
// once per process, plus the process's counter times identifierStride, modulo
// 2^64. Within one process no two identifiers are equal until 2^64 have been
// made, which is a guarantee and not a probability. Two processes that each
// make n identifiers can share one only if their starting points differ by one
// of about 2n values out of 2^64: for a billion identifiers each, about one
// chance in nine billion. Independent random 64-bit identifiers would collide
// between the same two processes with a chance of about n squared in 2^64, which
// for a billion each is five in a hundred.
//
// An identifier needs to be unique, not secret, so it does not read the system's
// random source for each one: that was 0.5 microseconds and an allocation a call,
// and the first read of it by a process took a millisecond. The starting point
// comes from math/rand/v2, whose generator the runtime seeds from the system's
// random source before the program starts, so the first identifier costs the same
// as the thousandth. IDStrategyUUID, which a caller chose for the quality of its
// randomness, still reads crypto/rand for every one.
func generateIdentifier(prefix string) string {
	return identifierAt(prefix, identifierStart(), identifierCounter.Add(1))
}

// generateUUID makes a random (version 4) UUID in its 8-4-4-4-12 form.
func generateUUID() string {
	var bytes [16]byte
	_, _ = crand.Read(bytes[:])
	bytes[6] = bytes[6]&0x0f | 0x40
	bytes[8] = bytes[8]&0x3f | 0x80
	encoded := hex.EncodeToString(bytes[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}

func generateTimestampID(prefix string) string {
	counter := idCounter.Add(1)
	var buffer [80]byte
	encoded := append(buffer[:0], prefix...)
	encoded = append(encoded, '_')
	encoded = strconv.AppendInt(encoded, time.Now().UnixNano(), 10)
	encoded = append(encoded, '_')
	encoded = strconv.AppendInt(encoded, int64(os.Getpid()), 10)
	encoded = append(encoded, '_')
	encoded = strconv.AppendUint(encoded, counter, 10)
	return string(encoded)
}

// TraceIDSource reports the trace ID of the span already on the context, or
// "" when there is none.
//
// It is a hook rather than a direct OpenTelemetry call because this package is
// core, and core does not import a telemetry vendor (OB-001, ARC-17). Reading
// the ambient trace ID was the last thing in the whole of internal/ that
// imported OpenTelemetry, which meant every consumer of this library compiled
// against it whether or not they used it.
//
// The default returns "", so a correlation ID falls back to the request ID
// exactly as it does when no span exists. telemetry/otel installs the real one.
type TraceIDSource func(context.Context) string

var traceIDSource atomic.Pointer[TraceIDSource]

// traceIDInstall is one call to SetTraceIDSource, chained to the one before
// it. A removal marks its own install and the head moves back past every
// install that is marked, so a removal that runs late, or twice, cannot bring
// back a source somebody has already taken out. A nil source is the default.
type traceIDInstall struct {
	source   *TraceIDSource
	previous *traceIDInstall
	undone   bool
}

var (
	traceIDMu sync.Mutex
	// traceIDHead is the newest install nobody has removed; nil means the
	// default.
	traceIDHead *traceIDInstall
)

// publishTraceIDSource makes the source the atomic holds match traceIDHead.
// The caller holds traceIDMu.
func publishTraceIDSource() {
	if traceIDHead == nil {
		traceIDSource.Store(nil)
		return
	}
	traceIDSource.Store(traceIDHead.source)
}

// SetTraceIDSource installs the source and returns a function removing this
// install. The source in effect is the newest install that has not been
// removed, whatever order the removals run in, and calling a removal again
// does nothing. Passing nil installs the default, which answers "", rather
// than storing a nil to be called later.
func SetTraceIDSource(source TraceIDSource) (restore func()) {
	traceIDMu.Lock()
	defer traceIDMu.Unlock()

	install := &traceIDInstall{previous: traceIDHead}
	if source != nil {
		install.source = &source
	}
	traceIDHead = install
	publishTraceIDSource()

	return func() {
		traceIDMu.Lock()
		defer traceIDMu.Unlock()

		install.undone = true
		for traceIDHead != nil && traceIDHead.undone {
			traceIDHead = traceIDHead.previous
		}
		publishTraceIDSource()
	}
}

func currentTraceID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	source := traceIDSource.Load()
	if source == nil {
		return ""
	}
	return (*source)(ctx)
}
