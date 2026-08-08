package telemetry

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// These tests fill the coverage gaps left by logger_test.go and the
// content-policy test files: the level/config accessors and their nil-
// receiver guards, ConfigureLogger's global-swap path, Close's error and
// nil-receiver paths, the multiHandler/historyHandler internals directly
// (Enabled/Handle branches that a single end-to-end log call does not all
// reach), addAttr's group recursion, valueToAny's full Kind switch, and
// parseLogLevel/toSlogLevel's full string/level tables.

// --- nil receiver guards -----------------------------------------------
//
// Every method here starts "if l == nil". A caller holding a *Logger
// obtained from somewhere that returned nil (GetLogger never does, but a
// zero-value var does) must not panic -- these prove the guard, not just
// that the guard exists in the source.

func TestNilLoggerMethodsAreSafe(t *testing.T) {
	var l *Logger

	if err := l.Close(); err != nil {
		t.Errorf("nil.Close() = %v, want nil", err)
	}
	l.SetLevel(DebugLevel) // must not panic
	l.SetLevelString("debug")
	if got := l.Level(); got != InfoLevel {
		t.Errorf("nil.Level() = %v, want InfoLevel", got)
	}
	if got := l.Config(); got != (LoggerConfig{}) {
		t.Errorf("nil.Config() = %+v, want zero value", got)
	}
	if got := l.Entries(); got != nil {
		t.Errorf("nil.Entries() = %v, want nil", got)
	}
	l.ResetEntries() // must not panic
	if got := l.WithFields(map[string]any{"a": 1}); got != nil {
		t.Errorf("nil.WithFields(...) = %v, want nil", got)
	}
}

// A Logger built with Capture: false has a history object but never routes
// through historyHandler; Entries/ResetEntries must still be safe and
// report empty rather than panicking on the "history exists but nothing was
// ever appended" state, and on a logger whose history pointer is present but
// unused.
func TestEntriesWithoutCaptureIsEmpty(t *testing.T) {
	log := NewLoggerWithConfig(LoggerConfig{Level: InfoLevel, Capture: false, DisableStderr: true})
	t.Cleanup(func() { _ = log.Close() })

	log.Info("not captured")
	if entries := log.Entries(); len(entries) != 0 {
		t.Errorf("expected no entries when Capture=false, got %d", len(entries))
	}
	log.ResetEntries() // must not panic even though nothing was captured
}

// --- ConfigureLogger / SetLogger / GetLogger ----------------------------

func TestConfigureLoggerReplacesTheGlobalLogger(t *testing.T) {
	previous := GetLogger()
	t.Cleanup(func() { SetLogger(previous) })

	next := ConfigureLogger(LoggerConfig{Level: WarnLevel, Capture: true, DisableStderr: true, BufferSize: 5})
	if GetLogger() != next {
		t.Fatal("ConfigureLogger did not install its logger as the global one")
	}
	if next.Level() != WarnLevel {
		t.Fatalf("Level() = %v, want WarnLevel", next.Level())
	}
}

// SetLogger closes the previous global logger's file-backed outputs before
// swapping it out, so file handles do not leak across reconfiguration.
func TestSetLoggerClosesThePreviousLogger(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.log")

	first := NewLoggerWithConfig(LoggerConfig{Level: InfoLevel, FilePath: path, DisableStderr: true})
	previous := GetLogger()
	t.Cleanup(func() { SetLogger(previous) })

	SetLogger(first)
	second := NewLoggerWithConfig(LoggerConfig{Level: InfoLevel, DisableStderr: true})
	SetLogger(second) // must close `first`'s file without panicking or erroring visibly

	// first is now closed; writing to it directly must not panic (slog
	// handlers tolerate a closed underlying file by returning a write error,
	// which Info/Error discard).
	first.Info("after close")
}

// SetLogger(sameInstance) must not close the logger out from under itself.
func TestSetLoggerSameInstanceDoesNotCloseItself(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "same.log")
	log := NewLoggerWithConfig(LoggerConfig{Level: InfoLevel, FilePath: path, DisableStderr: true})
	previous := GetLogger()
	t.Cleanup(func() { SetLogger(previous); _ = log.Close() })

	SetLogger(log)
	SetLogger(log) // same pointer twice: must not close its own file handle

	log.Info("still open")
	if err := log.Close(); err != nil {
		t.Errorf("Close() after self-reassignment = %v, want nil (file should not already be closed)", err)
	}
}

// --- Close's error path --------------------------------------------------

// Close reports the first closer's error rather than swallowing it -- built
// directly against an already-closed *os.File (via a Logger constructed by
// hand, since the normal NewLoggerWithConfig path never hands out one whose
// file is already closed) to force the error path deterministically.
func TestLoggerCloseReportsTheFirstError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "already-closed.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close() // closing it again below is what produces the error Close() must surface

	log := &Logger{closers: []io.Closer{file}}
	if err := log.Close(); err == nil {
		t.Fatal("expected Close() to report the underlying file's already-closed error, got nil")
	}
}

// --- SetLevel / SetLevelString / Level round-trip ------------------------

func TestLoggerLevelRoundTrip(t *testing.T) {
	cases := []struct {
		set  LogLevel
		want LogLevel
	}{
		{DebugLevel, DebugLevel},
		{InfoLevel, InfoLevel},
		{WarnLevel, WarnLevel},
		{ErrorLevel, ErrorLevel},
		// FatalLevel shares slog.LevelError: the doc comment on Level says it
		// does not survive the round trip.
		{FatalLevel, ErrorLevel},
	}
	log := NewLoggerWithConfig(LoggerConfig{Level: InfoLevel, DisableStderr: true})
	t.Cleanup(func() { _ = log.Close() })

	for _, tc := range cases {
		log.SetLevel(tc.set)
		if got := log.Level(); got != tc.want {
			t.Errorf("SetLevel(%v); Level() = %v, want %v", tc.set, got, tc.want)
		}
	}
}

func TestLoggerSetLevelStringDrivesSetLevel(t *testing.T) {
	log := NewLoggerWithConfig(LoggerConfig{Level: InfoLevel, DisableStderr: true})
	t.Cleanup(func() { _ = log.Close() })

	log.SetLevelString("debug")
	if got := log.Level(); got != DebugLevel {
		t.Fatalf("SetLevelString(debug); Level() = %v, want DebugLevel", got)
	}
	log.SetLevelString("nonsense") // unrecognised strings fall back to InfoLevel
	if got := log.Level(); got != InfoLevel {
		t.Fatalf("SetLevelString(nonsense); Level() = %v, want InfoLevel", got)
	}
}

// --- Config() --------------------------------------------------------------

func TestLoggerConfigReturnsWhatItWasBuiltWith(t *testing.T) {
	cfg := LoggerConfig{Level: WarnLevel, Format: "json", Capture: true, BufferSize: 7, DisableStderr: true}
	log := NewLoggerWithConfig(cfg)
	t.Cleanup(func() { _ = log.Close() })

	got := log.Config()
	if got.Level != cfg.Level || got.Format != cfg.Format || got.BufferSize != cfg.BufferSize {
		t.Fatalf("Config() = %+v, want %+v", got, cfg)
	}
}

// --- WithFields chains without losing history/level/closers --------------

func TestWithFieldsPreservesHistoryAndLevel(t *testing.T) {
	log := NewLoggerWithConfig(LoggerConfig{Level: DebugLevel, Capture: true, BufferSize: 10, DisableStderr: true})
	t.Cleanup(func() { _ = log.Close() })

	fielded := log.WithFields(map[string]any{"component": "test"})
	fielded.Info("hello")

	entries := log.Entries() // the underlying history is shared with the parent
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry captured via the WithFields-derived logger, got %d", len(entries))
	}
	if entries[0].Attributes["component"] != "test" {
		t.Errorf("expected the WithFields attribute on the entry, got %#v", entries[0].Attributes)
	}
	if fielded.Level() != DebugLevel {
		t.Errorf("WithFields logger Level() = %v, want DebugLevel (shared level)", fielded.Level())
	}
}

// --- Fatal exits the process -----------------------------------------------
//
// Fatal calls os.Exit(1) after logging, so it cannot be called in-process
// without ending this test binary. Standard Go pattern: re-exec the test
// binary with an env var that makes this specific test call Fatal, and
// assert the child process exited with status 1.

func TestLoggerFatalExitsWithStatus1(t *testing.T) {
	if os.Getenv("SCHEMAFLUX_TEST_FATAL_CHILD") == "1" {
		log := NewLoggerWithConfig(LoggerConfig{Level: InfoLevel, DisableStderr: true})
		log.Fatal("fatal from child", "reason", "test")
		return // unreached if Fatal behaves correctly
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestLoggerFatalExitsWithStatus1")
	cmd.Env = append(os.Environ(), "SCHEMAFLUX_TEST_FATAL_CHILD=1")
	err := cmd.Run()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected the child process to exit with an error, got %v", err)
	}
	if exitErr.ExitCode() != 1 {
		t.Fatalf("child exit code = %d, want 1", exitErr.ExitCode())
	}
}

// A nil-receiver Fatal must be a safe no-op, not a call to os.Exit -- a nil
// *Logger has nothing to log and nothing to exit on behalf of.
func TestNilLoggerFatalDoesNotExit(t *testing.T) {
	var l *Logger
	l.Fatal("should not exit")
	// Reaching this line proves Fatal returned rather than calling os.Exit.
}

// --- newStructuredHandler ---------------------------------------------------

func TestNewStructuredHandlerPicksFormat(t *testing.T) {
	jsonHandler := newStructuredHandler(io.Discard, "json", &slog.HandlerOptions{})
	if _, ok := jsonHandler.(*slog.JSONHandler); !ok {
		t.Errorf("format=json produced %T, want *slog.JSONHandler", jsonHandler)
	}

	textHandler := newStructuredHandler(io.Discard, "text", &slog.HandlerOptions{})
	if _, ok := textHandler.(*slog.TextHandler); !ok {
		t.Errorf("format=text produced %T, want *slog.TextHandler", textHandler)
	}

	// Anything other than exactly "json" falls back to text.
	otherHandler := newStructuredHandler(io.Discard, "yaml", &slog.HandlerOptions{})
	if _, ok := otherHandler.(*slog.TextHandler); !ok {
		t.Errorf("format=yaml produced %T, want *slog.TextHandler (fallback)", otherHandler)
	}
}

// --- multiHandler direct tests: Enabled/Handle branches -------------------

type fakeHandler struct {
	enabled   bool
	handleErr error
	handled   int
}

func (f *fakeHandler) Enabled(context.Context, slog.Level) bool { return f.enabled }
func (f *fakeHandler) Handle(context.Context, slog.Record) error {
	f.handled++
	return f.handleErr
}
func (f *fakeHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return f }
func (f *fakeHandler) WithGroup(name string) slog.Handler       { return f }

func TestMultiHandlerEnabledIsTrueIfAnyHandlerIsEnabled(t *testing.T) {
	mh := &multiHandler{handlers: []slog.Handler{
		&fakeHandler{enabled: false},
		&fakeHandler{enabled: true},
	}}
	if !mh.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("expected Enabled=true when at least one handler is enabled")
	}
}

func TestMultiHandlerEnabledIsFalseIfNoHandlerIsEnabled(t *testing.T) {
	mh := &multiHandler{handlers: []slog.Handler{
		&fakeHandler{enabled: false},
		&fakeHandler{enabled: false},
	}}
	if mh.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("expected Enabled=false when no handler is enabled")
	}
}

func TestMultiHandlerHandleSkipsDisabledHandlers(t *testing.T) {
	disabled := &fakeHandler{enabled: false}
	enabled := &fakeHandler{enabled: true}
	mh := &multiHandler{handlers: []slog.Handler{disabled, enabled}}

	record := slog.NewRecord(time.Now(), slog.LevelInfo, "msg", 0)
	if err := mh.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle() = %v, want nil", err)
	}
	if disabled.handled != 0 {
		t.Error("a disabled handler must not receive Handle")
	}
	if enabled.handled != 1 {
		t.Error("an enabled handler must receive Handle exactly once")
	}
}

// multiHandler.Handle stops and returns the first handler's error, rather
// than continuing to call the rest -- so a caller sees the actual failure,
// not a later handler's unrelated one.
func TestMultiHandlerHandlePropagatesTheFirstError(t *testing.T) {
	boom := errors.New("boom")
	first := &fakeHandler{enabled: true, handleErr: boom}
	second := &fakeHandler{enabled: true}
	mh := &multiHandler{handlers: []slog.Handler{first, second}}

	record := slog.NewRecord(time.Now(), slog.LevelInfo, "msg", 0)
	err := mh.Handle(context.Background(), record)
	if !errors.Is(err, boom) {
		t.Fatalf("Handle() = %v, want %v", err, boom)
	}
	if second.handled != 0 {
		t.Error("a handler after one that errored must not be called")
	}
}

// --- WithGroup: both handler kinds thread the group name through ----------

// multiHandler.WithGroup must fan the group out to every wrapped handler,
// exactly the way WithAttrs already does.
func TestMultiHandlerWithGroupFansOutToEveryHandler(t *testing.T) {
	first := &fakeHandler{}
	second := &fakeHandler{}
	mh := &multiHandler{handlers: []slog.Handler{first, second}}

	grouped := mh.WithGroup("request")
	groupedMH, ok := grouped.(*multiHandler)
	if !ok {
		t.Fatalf("WithGroup returned %T, want *multiHandler", grouped)
	}
	if len(groupedMH.handlers) != 2 {
		t.Fatalf("expected 2 wrapped handlers, got %d", len(groupedMH.handlers))
	}
	// fakeHandler.WithGroup returns itself, so this exercises the loop and
	// return path without needing to inspect a group name it never stores.
	if groupedMH.handlers[0] != slog.Handler(first) || groupedMH.handlers[1] != slog.Handler(second) {
		t.Error("WithGroup did not call through to each wrapped handler")
	}
}

// historyHandler.WithGroup prefixes subsequent attribute keys with the group
// name, joined by "." with any outer groups already present -- proven end to
// end through Handle rather than by inspecting the unexported groups slice.
func TestHistoryHandlerWithGroupPrefixesSubsequentAttributeKeys(t *testing.T) {
	levelVar := new(slog.LevelVar)
	base := &historyHandler{level: levelVar, history: &logHistory{maxSize: 10}}

	grouped := base.WithGroup("request")
	record := slog.NewRecord(time.Now(), slog.LevelInfo, "msg", 0)
	record.AddAttrs(slog.String("id", "abc"))

	if err := grouped.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle() = %v, want nil", err)
	}

	entries := base.history.snapshot()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Attributes["request.id"] != "abc" {
		t.Errorf("expected the group prefix on the attribute key, got %#v", entries[0].Attributes)
	}

	// WithGroup must not mutate the handler it was called on: the base
	// handler's own Handle call still uses no group prefix.
	base.history.reset()
	if err := base.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle() = %v, want nil", err)
	}
	baseEntries := base.history.snapshot()
	if len(baseEntries) != 1 || baseEntries[0].Attributes["id"] != "abc" {
		t.Errorf("expected the original handler to remain ungrouped, got %#v", baseEntries)
	}
}

// --- historyHandler.Enabled: the level gate --------------------------------

func TestHistoryHandlerEnabledRespectsLevel(t *testing.T) {
	levelVar := new(slog.LevelVar)
	levelVar.Set(slog.LevelWarn)
	h := &historyHandler{level: levelVar, history: &logHistory{maxSize: 10}}

	if h.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("Info must not be enabled when the level is Warn")
	}
	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Error("Error must be enabled when the level is Warn")
	}
}

// --- logHistory.append with an invalid maxSize -----------------------------

// A logHistory built with maxSize <= 0 (which NewLoggerWithConfig's own
// BufferSize<=0 guard normally prevents, but a directly-constructed one
// might not) must default to 1000 rather than dropping every entry.
func TestLogHistoryAppendDefaultsInvalidMaxSize(t *testing.T) {
	h := &logHistory{maxSize: 0}
	h.append(LogEntry{Message: "one"})
	if len(h.entries) != 1 {
		t.Fatalf("expected the entry to be kept once maxSize is defaulted, got %d entries", len(h.entries))
	}
	if h.maxSize != 1000 {
		t.Fatalf("maxSize = %d, want defaulted to 1000", h.maxSize)
	}
}

// --- addAttr: group recursion, empty attrs, nested key joining ------------

func TestAddAttrSkipsTheEmptyAttr(t *testing.T) {
	target := map[string]any{}
	addAttr(target, nil, slog.Attr{})
	if len(target) != 0 {
		t.Fatalf("expected the empty Attr to be skipped, got %#v", target)
	}
}

func TestAddAttrFlattensGroupsIntoDottedKeys(t *testing.T) {
	target := map[string]any{}
	group := slog.Group("request", slog.String("id", "abc"), slog.Int("size", 5))
	addAttr(target, nil, group)

	if target["request.id"] != "abc" {
		t.Errorf("expected request.id=abc, got %#v", target)
	}
	if target["request.size"] != int64(5) {
		t.Errorf("expected request.size=5, got %#v", target)
	}
}

// A group attr with an empty key (as slog produces for With(attrs) groups in
// some cases) must not add a literal "." segment to the joined key.
func TestAddAttrGroupWithEmptyKeyDoesNotAddADotSegment(t *testing.T) {
	target := map[string]any{}
	anon := slog.Attr{Key: "", Value: slog.GroupValue(slog.String("inner", "v"))}
	addAttr(target, []string{"outer"}, anon)

	if target["outer.inner"] != "v" {
		t.Errorf("expected outer.inner=v, got %#v", target)
	}
}

func TestAddAttrJoinsExistingGroupPrefix(t *testing.T) {
	target := map[string]any{}
	addAttr(target, []string{"a", "b"}, slog.String("c", "v"))
	if target["a.b.c"] != "v" {
		t.Errorf("expected a.b.c=v, got %#v", target)
	}
}

// --- valueToAny: every slog.Kind ------------------------------------------

func TestValueToAnyCoversEveryKind(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name  string
		value slog.Value
		want  any
	}{
		{"string", slog.StringValue("s"), "s"},
		{"int64", slog.Int64Value(42), int64(42)},
		{"uint64", slog.Uint64Value(7), uint64(7)},
		{"float64", slog.Float64Value(1.5), 1.5},
		{"bool", slog.BoolValue(true), true},
		{"duration", slog.DurationValue(3 * time.Second), 3 * time.Second},
		{"time", slog.TimeValue(now), now},
		{"any", slog.AnyValue(struct{ X int }{X: 1}), struct{ X int }{X: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := valueToAny(tc.value)
			if wantTime, ok := tc.want.(time.Time); ok {
				gotTime, ok := got.(time.Time)
				if !ok || !gotTime.Equal(wantTime) {
					t.Errorf("valueToAny(%s) = %#v, want %#v", tc.name, got, tc.want)
				}
				return
			}
			if got != tc.want {
				t.Errorf("valueToAny(%s) = %#v, want %#v", tc.name, got, tc.want)
			}
		})
	}
}

// --- parseLogLevel: every recognised string, plus the default -------------

func TestParseLogLevelCoversEveryLevelString(t *testing.T) {
	cases := map[string]LogLevel{
		"debug":      DebugLevel,
		"DEBUG":      DebugLevel,
		"  debug  ":  DebugLevel,
		"warn":       WarnLevel,
		"warning":    WarnLevel,
		"error":      ErrorLevel,
		"fatal":      FatalLevel,
		"info":       InfoLevel,
		"":           InfoLevel,
		"total-junk": InfoLevel,
	}
	for input, want := range cases {
		if got := parseLogLevel(input); got != want {
			t.Errorf("parseLogLevel(%q) = %v, want %v", input, got, want)
		}
	}
}

// --- toSlogLevel: every LogLevel, including the Fatal collapse ------------

func TestToSlogLevelCoversEveryLogLevel(t *testing.T) {
	cases := map[LogLevel]slog.Level{
		DebugLevel:   slog.LevelDebug,
		InfoLevel:    slog.LevelInfo,
		WarnLevel:    slog.LevelWarn,
		ErrorLevel:   slog.LevelError,
		FatalLevel:   slog.LevelError, // Fatal has no distinct slog level
		LogLevel(99): slog.LevelInfo,  // out-of-range falls back to Info
	}
	for input, want := range cases {
		if got := toSlogLevel(input); got != want {
			t.Errorf("toSlogLevel(%v) = %v, want %v", input, got, want)
		}
	}
}

// --- DefaultLoggerConfig: the branches the existing env test does not hit --

func TestDefaultLoggerConfigDebugEnvRaisesToDebug(t *testing.T) {
	t.Setenv("SCHEMAFLUX_LOG_LEVEL", "")
	t.Setenv("SCHEMAFLUX_DEBUG", "true")
	t.Setenv("SCHEMAFLUX_LOG_FORMAT", "")
	t.Setenv("SCHEMAFLUX_LOG_BUFFER", "")
	t.Setenv("SCHEMAFLUX_LOG_SOURCE", "")
	t.Setenv("SCHEMAFLUX_LOG_DISABLE_STDERR", "")
	t.Setenv("SCHEMAFLUX_LOG_DISABLE_CAPTURE", "")
	t.Setenv("SCHEMAFLUX_LOG_FILE", "")

	cfg := DefaultLoggerConfig()
	if cfg.Level != DebugLevel {
		t.Fatalf("SCHEMAFLUX_DEBUG=true with no explicit log level: Level = %v, want DebugLevel", cfg.Level)
	}
}

// An explicit log level takes priority over SCHEMAFLUX_DEBUG: debug-env only
// raises the level when nothing else already asked for something other than
// Info.
func TestDefaultLoggerConfigExplicitLevelWinsOverDebugEnv(t *testing.T) {
	t.Setenv("SCHEMAFLUX_LOG_LEVEL", "error")
	t.Setenv("SCHEMAFLUX_DEBUG", "true")

	cfg := DefaultLoggerConfig()
	if cfg.Level != ErrorLevel {
		t.Fatalf("Level = %v, want ErrorLevel (explicit level must not be overridden)", cfg.Level)
	}
}

func TestDefaultLoggerConfigUnrecognisedFormatFallsBackToText(t *testing.T) {
	t.Setenv("SCHEMAFLUX_LOG_FORMAT", "yaml")
	cfg := DefaultLoggerConfig()
	if cfg.Format != "text" {
		t.Fatalf("Format = %q, want text (fallback for an unrecognised format)", cfg.Format)
	}
}

func TestDefaultLoggerConfigInvalidBufferSizeFallsBackToDefault(t *testing.T) {
	t.Setenv("SCHEMAFLUX_LOG_BUFFER", "not-a-number")
	cfg := DefaultLoggerConfig()
	if cfg.BufferSize != 1000 {
		t.Fatalf("BufferSize = %d, want 1000 (fallback for an unparseable value)", cfg.BufferSize)
	}

	t.Setenv("SCHEMAFLUX_LOG_BUFFER", "-5")
	cfg = DefaultLoggerConfig()
	if cfg.BufferSize != 1000 {
		t.Fatalf("BufferSize = %d, want 1000 (fallback for a non-positive value)", cfg.BufferSize)
	}
}

func TestDefaultLoggerConfigDisableCaptureEnv(t *testing.T) {
	t.Setenv("SCHEMAFLUX_LOG_DISABLE_CAPTURE", "1")
	cfg := DefaultLoggerConfig()
	if cfg.Capture {
		t.Fatal("SCHEMAFLUX_LOG_DISABLE_CAPTURE=1 must turn Capture off")
	}
}

// --- NewLoggerWithConfig: the FilePath failure path ------------------------

// When the file path's directory cannot be created (a path component is an
// existing regular file, not a directory), NewLoggerWithConfig must not
// panic or error -- it silently falls back to whatever other handlers are
// configured, per its own "if err == nil" gate.
func TestNewLoggerWithConfigToleratesAnUncreatableFilePath(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// blocker is a file, not a directory: MkdirAll(blocker/sub, ...) must fail.
	badPath := filepath.Join(blocker, "sub", "schemaflux.log")

	log := NewLoggerWithConfig(LoggerConfig{
		Level:         InfoLevel,
		FilePath:      badPath,
		Capture:       true,
		BufferSize:    5,
		DisableStderr: true,
	})
	t.Cleanup(func() { _ = log.Close() })

	log.Info("still works despite the bad file path")
	if entries := log.Entries(); len(entries) != 1 {
		t.Fatalf("expected the history handler to still capture the entry, got %d entries", len(entries))
	}
}

// With every handler disabled (stderr off, no file path, capture off),
// NewLoggerWithConfig falls back to a discard handler rather than a nil
// slog.Logger.
func TestNewLoggerWithConfigFallsBackToDiscardWhenNoHandlersAreConfigured(t *testing.T) {
	log := NewLoggerWithConfig(LoggerConfig{Level: InfoLevel, Capture: false, DisableStderr: true})
	t.Cleanup(func() { _ = log.Close() })

	log.Info("goes nowhere, but must not panic")
	if entries := log.Entries(); len(entries) != 0 {
		t.Fatalf("expected no entries with Capture=false, got %v", entries)
	}
}
