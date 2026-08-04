package telemetry

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	logger *Logger
	mu     sync.RWMutex
)

// LogLevel represents the severity of a log message.
type LogLevel int

const (
	// DebugLevel logs everything including debug information.
	DebugLevel LogLevel = iota
	// InfoLevel logs informational messages and above.
	InfoLevel
	// WarnLevel logs warnings and errors.
	WarnLevel
	// ErrorLevel logs only errors.
	ErrorLevel
	// FatalLevel logs fatal errors and exits.
	FatalLevel
)

// LogEntry is a captured log record for later review.
type LogEntry struct {
	Time       time.Time      `json:"time"`
	Level      string         `json:"level"`
	Message    string         `json:"message"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// LoggerConfig controls logger behavior and outputs.
type LoggerConfig struct {
	Level         LogLevel
	Format        string
	Output        io.Writer
	FilePath      string
	Capture       bool
	BufferSize    int
	AddSource     bool
	DisableStderr bool
}

// Logger wraps slog.Logger and tracks captured history for review.
type Logger struct {
	*slog.Logger
	level   *slog.LevelVar
	history *logHistory
	closers []io.Closer
	config  LoggerConfig
}

type logHistory struct {
	mu      sync.RWMutex
	entries []LogEntry
	maxSize int
}

type multiHandler struct {
	handlers []slog.Handler
}

type historyHandler struct {
	level   *slog.LevelVar
	history *logHistory
	attrs   []slog.Attr
	groups  []string
}

// DefaultLoggerConfig returns logger configuration from environment variables.
func DefaultLoggerConfig() LoggerConfig {
	level := parseLogLevel(os.Getenv("SCHEMAFLUX_LOG_LEVEL"))
	if level == InfoLevel && envEnabled("SCHEMAFLUX_DEBUG") {
		level = DebugLevel
	}

	format := strings.ToLower(strings.TrimSpace(os.Getenv("SCHEMAFLUX_LOG_FORMAT")))
	if format == "" {
		format = "text"
	}
	if format != "json" {
		format = "text"
	}

	bufferSize := 1000
	if raw := strings.TrimSpace(os.Getenv("SCHEMAFLUX_LOG_BUFFER")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			bufferSize = parsed
		}
	}

	return LoggerConfig{
		Level:         level,
		Format:        format,
		FilePath:      strings.TrimSpace(os.Getenv("SCHEMAFLUX_LOG_FILE")),
		Capture:       !envEnabled("SCHEMAFLUX_LOG_DISABLE_CAPTURE"),
		BufferSize:    bufferSize,
		AddSource:     envEnabled("SCHEMAFLUX_LOG_SOURCE"),
		DisableStderr: envEnabled("SCHEMAFLUX_LOG_DISABLE_STDERR"),
	}
}

// NewLogger creates a configured logger from environment defaults.
func NewLogger() *Logger {
	return NewLoggerWithConfig(DefaultLoggerConfig())
}

// NewLoggerWithConfig creates a new logger with the provided configuration.
func NewLoggerWithConfig(cfg LoggerConfig) *Logger {
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = 1000
	}
	if cfg.Format == "" {
		cfg.Format = "text"
	}

	levelVar := new(slog.LevelVar)
	levelVar.Set(toSlogLevel(cfg.Level))

	history := &logHistory{maxSize: cfg.BufferSize}
	handlerOpts := &slog.HandlerOptions{
		Level:     levelVar,
		AddSource: cfg.AddSource,
	}

	handlers := make([]slog.Handler, 0, 3)
	closers := make([]io.Closer, 0, 1)

	if !cfg.DisableStderr {
		writer := cfg.Output
		if writer == nil {
			writer = os.Stderr
		}
		handlers = append(handlers, newStructuredHandler(writer, cfg.Format, handlerOpts))
	}

	if cfg.FilePath != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.FilePath), 0o755); err == nil {
			file, err := os.OpenFile(cfg.FilePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
			if err == nil {
				handlers = append(handlers, newStructuredHandler(file, cfg.Format, handlerOpts))
				closers = append(closers, file)
			}
		}
	}

	if cfg.Capture {
		handlers = append(handlers, &historyHandler{
			level:   levelVar,
			history: history,
		})
	}

	if len(handlers) == 0 {
		handlers = append(handlers, slog.NewTextHandler(io.Discard, handlerOpts))
	}

	return &Logger{
		Logger:  slog.New(&multiHandler{handlers: handlers}),
		level:   levelVar,
		history: history,
		closers: closers,
		config:  cfg,
	}
}

// ConfigureLogger replaces the global logger with one built from cfg.
func ConfigureLogger(cfg LoggerConfig) *Logger {
	next := NewLoggerWithConfig(cfg)
	SetLogger(next)
	return next
}

// SetLogger replaces the global logger instance.
func SetLogger(next *Logger) {
	mu.Lock()
	defer mu.Unlock()

	if logger != nil && logger != next {
		_ = logger.Close()
	}
	logger = next
}

// GetLogger returns the global logger instance, creating one on-demand.
func GetLogger() *Logger {
	mu.RLock()
	if logger != nil {
		defer mu.RUnlock()
		return logger
	}
	mu.RUnlock()

	mu.Lock()
	defer mu.Unlock()
	if logger == nil {
		logger = NewLogger()
	}
	return logger
}

// Close flushes and closes any file-backed outputs.
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	var firstErr error
	for _, closer := range l.closers {
		if err := closer.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	l.closers = nil
	return firstErr
}

// SetLevel updates the logger level.
func (l *Logger) SetLevel(level LogLevel) {
	if l == nil || l.level == nil {
		return
	}
	l.level.Set(toSlogLevel(level))
}

// SetLevelString updates the logger level using a string value.
func (l *Logger) SetLevelString(level string) {
	l.SetLevel(parseLogLevel(level))
}

// Config returns the logger configuration.
func (l *Logger) Config() LoggerConfig {
	if l == nil {
		return LoggerConfig{}
	}
	return l.config
}

// Entries returns a copy of the captured log history.
func (l *Logger) Entries() []LogEntry {
	if l == nil || l.history == nil {
		return nil
	}
	return l.history.snapshot()
}

// ResetEntries clears captured log history.
func (l *Logger) ResetEntries() {
	if l == nil || l.history == nil {
		return
	}
	l.history.reset()
}

// WithFields returns a logger augmented with structured fields.
func (l *Logger) WithFields(fields map[string]any) *Logger {
	if l == nil {
		return nil
	}

	attrs := make([]any, 0, len(fields)*2)
	for key, value := range fields {
		attrs = append(attrs, key, value)
	}

	return &Logger{
		Logger:  l.Logger.With(attrs...),
		level:   l.level,
		history: l.history,
		closers: l.closers,
		config:  l.config,
	}
}

// Fatal logs an error message and exits the process.
func (l *Logger) Fatal(message string, args ...any) {
	if l == nil {
		return
	}
	l.Logger.Error(message, args...)
	os.Exit(1)
}

func newStructuredHandler(writer io.Writer, format string, opts *slog.HandlerOptions) slog.Handler {
	if format == "json" {
		return slog.NewJSONHandler(writer, opts)
	}
	return slog.NewTextHandler(writer, opts)
}

func (h *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h *multiHandler) Handle(ctx context.Context, record slog.Record) error {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, record.Level) {
			if err := handler.Handle(ctx, record.Clone()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		handlers[i] = handler.WithAttrs(attrs)
	}
	return &multiHandler{handlers: handlers}
}

func (h *multiHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		handlers[i] = handler.WithGroup(name)
	}
	return &multiHandler{handlers: handlers}
}

func (h *historyHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

func (h *historyHandler) Handle(_ context.Context, record slog.Record) error {
	entry := LogEntry{
		Time:       record.Time.UTC(),
		Level:      record.Level.String(),
		Message:    record.Message,
		Attributes: make(map[string]any),
	}

	for _, attr := range h.attrs {
		addAttr(entry.Attributes, h.groups, attr)
	}
	record.Attrs(func(attr slog.Attr) bool {
		addAttr(entry.Attributes, h.groups, attr)
		return true
	})

	if len(entry.Attributes) == 0 {
		entry.Attributes = nil
	}
	h.history.append(entry)
	return nil
}

func (h *historyHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := &historyHandler{
		level:   h.level,
		history: h.history,
		attrs:   append([]slog.Attr{}, h.attrs...),
		groups:  append([]string{}, h.groups...),
	}
	next.attrs = append(next.attrs, attrs...)
	return next
}

func (h *historyHandler) WithGroup(name string) slog.Handler {
	next := &historyHandler{
		level:   h.level,
		history: h.history,
		attrs:   append([]slog.Attr{}, h.attrs...),
		groups:  append([]string{}, h.groups...),
	}
	next.groups = append(next.groups, name)
	return next
}

func (h *logHistory) append(entry LogEntry) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.maxSize <= 0 {
		h.maxSize = 1000
	}
	if len(h.entries) >= h.maxSize {
		copy(h.entries, h.entries[1:])
		h.entries[len(h.entries)-1] = entry
		return
	}
	h.entries = append(h.entries, entry)
}

func (h *logHistory) snapshot() []LogEntry {
	h.mu.RLock()
	defer h.mu.RUnlock()

	entries := make([]LogEntry, len(h.entries))
	for i, entry := range h.entries {
		entries[i] = LogEntry{
			Time:    entry.Time,
			Level:   entry.Level,
			Message: entry.Message,
		}
		if len(entry.Attributes) > 0 {
			entries[i].Attributes = make(map[string]any, len(entry.Attributes))
			for key, value := range entry.Attributes {
				entries[i].Attributes[key] = value
			}
		}
	}
	return entries
}

func (h *logHistory) reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entries = nil
}

func addAttr(target map[string]any, groups []string, attr slog.Attr) {
	attr.Value = attr.Value.Resolve()
	if attr.Equal(slog.Attr{}) {
		return
	}

	if attr.Value.Kind() == slog.KindGroup {
		groupChain := groups
		if attr.Key != "" {
			groupChain = append(groupChain, attr.Key)
		}
		for _, child := range attr.Value.Group() {
			addAttr(target, groupChain, child)
		}
		return
	}

	key := attr.Key
	if len(groups) > 0 {
		key = strings.Join(append(append([]string{}, groups...), attr.Key), ".")
	}
	target[key] = valueToAny(attr.Value)
}

func valueToAny(value slog.Value) any {
	switch value.Kind() {
	case slog.KindString:
		return value.String()
	case slog.KindInt64:
		return value.Int64()
	case slog.KindUint64:
		return value.Uint64()
	case slog.KindFloat64:
		return value.Float64()
	case slog.KindBool:
		return value.Bool()
	case slog.KindDuration:
		return value.Duration()
	case slog.KindTime:
		return value.Time()
	case slog.KindAny:
		return value.Any()
	default:
		return value.String()
	}
}

func parseLogLevel(raw string) LogLevel {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return DebugLevel
	case "warn", "warning":
		return WarnLevel
	case "error":
		return ErrorLevel
	case "fatal":
		return FatalLevel
	default:
		return InfoLevel
	}
}

func toSlogLevel(level LogLevel) slog.Level {
	switch level {
	case DebugLevel:
		return slog.LevelDebug
	case WarnLevel:
		return slog.LevelWarn
	case ErrorLevel, FatalLevel:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func envEnabled(keys ...string) bool {
	for _, key := range keys {
		switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	return false
}
