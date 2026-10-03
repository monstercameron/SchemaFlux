package telemetry

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/monstercameron/schemaflux/internal/config"
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

// DefaultLevel is the level of a logger nobody configured: warn, so that a call
// that goes well writes nothing.
const DefaultLevel = WarnLevel

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

// logHistory keeps the newest maxSize entries in a ring: once it is full, an
// append overwrites the oldest entry in place. It used to shift the whole slice
// down by one on every append at capacity, which is a 64 KB copy under the lock
// every log call has to take.
type logHistory struct {
	mu      sync.RWMutex
	entries []LogEntry
	start   int // index of the oldest entry once the ring is full
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
//
// The variables are SCHEMAFLUX_LOG_LEVEL (debug, info, warn, warning, error or
// fatal), SCHEMAFLUX_LOG_FORMAT (text or json), SCHEMAFLUX_LOG_BUFFER (a whole
// number of entries, at least 1), SCHEMAFLUX_LOG_FILE, the booleans
// SCHEMAFLUX_LOG_DISABLE_CAPTURE, SCHEMAFLUX_LOG_SOURCE and
// SCHEMAFLUX_LOG_DISABLE_STDERR, and SCHEMAFLUX_DEBUG=true, which raises a level
// of info, or of none stated, to debug. A value one of them cannot take is
// reported once, naming the variable, and the default applies.
//
// The default level is warn. A library that prints a line to standard error for
// every call it makes is not one a program embeds without noticing, and the
// lines about a call that went well (that an operation started, that a request
// completed) are written at debug. Set SCHEMAFLUX_LOG_LEVEL=debug to see them,
// or SCHEMAFLUX_LOG_LEVEL=info for the lines that remain at that level.
func DefaultLoggerConfig() LoggerConfig {
	named := config.EnvChoice("SCHEMAFLUX_LOG_LEVEL", "", "debug", "info", "warn", "warning", "error", "fatal")
	level := DefaultLevel
	if named != "" {
		level = parseLogLevel(named)
	}
	if (named == "" || level == InfoLevel) && config.EnvBool("SCHEMAFLUX_DEBUG") {
		level = DebugLevel
	}

	return LoggerConfig{
		Level:         level,
		Format:        config.EnvChoice("SCHEMAFLUX_LOG_FORMAT", "text", "text", "json"),
		FilePath:      strings.TrimSpace(os.Getenv("SCHEMAFLUX_LOG_FILE")),
		Capture:       !config.EnvBool("SCHEMAFLUX_LOG_DISABLE_CAPTURE"),
		BufferSize:    config.EnvInt("SCHEMAFLUX_LOG_BUFFER", 1000, 1),
		AddSource:     config.EnvBool("SCHEMAFLUX_LOG_SOURCE"),
		DisableStderr: config.EnvBool("SCHEMAFLUX_LOG_DISABLE_STDERR"),
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

	// A log file that cannot be opened is reported, once, through the handlers
	// that did start, and the logger runs without it. It used to be dropped with
	// no word anywhere, so the log a deployment believed it was keeping did not
	// exist.
	var fileErr error
	if cfg.FilePath != "" {
		fileErr = os.MkdirAll(filepath.Dir(cfg.FilePath), 0o755)
		if fileErr == nil {
			var file *os.File
			file, fileErr = os.OpenFile(cfg.FilePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
			if fileErr == nil {
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

	built := &Logger{
		Logger:  slog.New(&multiHandler{handlers: handlers}),
		level:   levelVar,
		history: history,
		closers: closers,
		config:  cfg,
	}
	if fileErr != nil {
		built.Warn("the log file could not be opened; logging to it is off", "error", fileErr)
	}
	return built
}

// FromSlog returns a Logger that writes through l and keeps no history of its
// own: Entries is empty, SetLevel changes nothing (the level is l's handler's
// to decide), and Close closes nothing, because the outputs are the caller's.
// It is how a program that already has a *slog.Logger hands it to a client.
//
// A nil l is read as no logger at all and returns Discard(), so the result is
// never nil and never panics on the first line written.
func FromSlog(l *slog.Logger) *Logger {
	if l == nil {
		return Discard()
	}
	return &Logger{Logger: l}
}

// Discard returns a Logger that is enabled at no level and writes nowhere.
// Each call returns a Logger of its own.
func Discard() *Logger {
	return &Logger{Logger: slog.New(slog.DiscardHandler)}
}

// ConfigureLogger replaces the global logger with one built from cfg.
func ConfigureLogger(cfg LoggerConfig) *Logger {
	next := NewLoggerWithConfig(cfg)
	SetLogger(next)
	return next
}

// SetLogger replaces the global logger instance, and closes the file outputs of
// the one it replaces. Anything else still holding the replaced logger, such as a
// client built before the swap, keeps a logger whose file is closed: its records
// still reach its other outputs and its history, and the file write fails and is
// dropped. Replace the logger before building what keeps it.
func SetLogger(next *Logger) {
	mu.Lock()
	defer mu.Unlock()

	if logger != nil && logger != next {
		_ = logger.Close()
	}
	logger = next
	published.Store(next)
}

// published is the logger GetLogger hands out, for a read that takes no lock.
// Every call asks for the logger several times, and the read lock was a cache
// line every core touched each time. It is written under mu, with logger.
var published atomic.Pointer[Logger]

// GetLogger returns the global logger instance, creating one on-demand.
func GetLogger() *Logger {
	if held := published.Load(); held != nil {
		return held
	}

	mu.Lock()
	defer mu.Unlock()
	if logger == nil {
		logger = NewLogger()
		published.Store(logger)
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

// Level reports the current logger level.
//
// It exists so a caller who raises verbosity can put it back. Client.WithDebug
// used to switch the logger to debug and had no way to undo it, so
// WithDebug(false) left every subsequent request logging at debug -- the
// opposite of what it says.
//
// FatalLevel does not survive the round trip: it shares slog.LevelError, so a
// logger set to fatal reports error. Nothing in this library sets fatal.
func (l *Logger) Level() LogLevel {
	if l == nil {
		return DefaultLevel
	}
	if l.level == nil {
		if l.Logger == nil {
			return DefaultLevel
		}
		return probedLevel(l.Logger)
	}
	switch l.level.Level() {
	case slog.LevelDebug:
		return DebugLevel
	case slog.LevelWarn:
		return WarnLevel
	case slog.LevelError:
		return ErrorLevel
	default:
		return InfoLevel
	}
}

// probedLevel reports the lowest level a logger built by FromSlog or Discard
// writes at, which is the only way to learn it: the *slog.Logger it was made
// from keeps its level inside its handler. A logger enabled at none of the
// four reports FatalLevel, the one above them.
func probedLevel(l *slog.Logger) LogLevel {
	ctx := context.Background()
	switch {
	case l.Enabled(ctx, slog.LevelDebug):
		return DebugLevel
	case l.Enabled(ctx, slog.LevelInfo):
		return InfoLevel
	case l.Enabled(ctx, slog.LevelWarn):
		return WarnLevel
	case l.Enabled(ctx, slog.LevelError):
		return ErrorLevel
	default:
		return FatalLevel
	}
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

	// In key order: ranging over the map put the attributes of every line in a
	// different order from one run to the next.
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	attrs := make([]any, 0, len(fields)*2)
	for _, key := range keys {
		attrs = append(attrs, key, fields[key])
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

// Handle gives the record to every enabled handler, and returns the errors of
// those that failed joined together. One output failing, a closed pipe or a full
// disk, must not stop the others: the history handler comes after the file and
// the stderr handlers, and stopping at the first error left it without every
// record from then on while the process kept logging.
func (h *multiHandler) Handle(ctx context.Context, record slog.Record) error {
	var failures []error
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, record.Level) {
			if err := handler.Handle(ctx, record.Clone()); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
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
		h.entries[h.start] = entry
		h.start = (h.start + 1) % len(h.entries)
		return
	}
	h.entries = append(h.entries, entry)
}

func (h *logHistory) snapshot() []LogEntry {
	h.mu.RLock()
	defer h.mu.RUnlock()

	entries := make([]LogEntry, len(h.entries))
	for i := range h.entries {
		entry := h.entries[(h.start+i)%len(h.entries)]
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
	h.start = 0
}

func addAttr(target map[string]any, groups []string, attr slog.Attr) {
	attr.Value = attr.Value.Resolve()
	if attr.Equal(slog.Attr{}) {
		return
	}

	if attr.Value.Kind() == slog.KindGroup {
		// A copy: appending to groups itself writes into spare capacity of the
		// slice the handler shares with every concurrent Handle call.
		groupChain := append([]string(nil), groups...)
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
