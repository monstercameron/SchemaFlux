package logger

import (
	"bytes"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/config"
	telemetry "github.com/monstercameron/schemaflux/internal/telemetry"
)

// withRestoredGlobalLogger snapshots the package-global logger telemetry
// tracks and restores it after the test, so tests that call SetLogger /
// ConfigureLogger do not leak state into other tests running in the same
// process (this suite is run with -shuffle=on -count=3, so ordering and
// repetition both have to be safe).
func withRestoredGlobalLogger(t *testing.T) {
	t.Helper()
	prev := GetLogger()
	t.Cleanup(func() {
		SetLogger(prev)
	})
}

func TestGetLogger_NeverNil(t *testing.T) {
	withRestoredGlobalLogger(t)

	l := GetLogger()
	if l == nil {
		t.Fatal("GetLogger() returned nil; the package promises an on-demand default instance")
	}
}

func TestSetLogger_RoundTrip(t *testing.T) {
	withRestoredGlobalLogger(t)

	custom := ConfigureLogger(LoggerConfig{Level: WarnLevel, DisableStderr: true, Capture: true})
	SetLogger(custom)

	got := GetLogger()
	if got != custom {
		t.Fatalf("GetLogger() after SetLogger() = %p, want %p", got, custom)
	}
	if got.Level() != WarnLevel {
		t.Fatalf("Level() = %v, want WarnLevel", got.Level())
	}
}

func TestConfigureLogger_ReplacesGlobalAndReturnsIt(t *testing.T) {
	withRestoredGlobalLogger(t)

	cfg := LoggerConfig{Level: ErrorLevel, DisableStderr: true, Capture: true}
	created := ConfigureLogger(cfg)
	if created == nil {
		t.Fatal("ConfigureLogger returned nil")
	}
	if GetLogger() != created {
		t.Fatal("ConfigureLogger did not install its logger as the package-global instance")
	}
	if created.Level() != ErrorLevel {
		t.Fatalf("created logger Level() = %v, want ErrorLevel", created.Level())
	}
}

func TestDefaultLoggerConfig_ReadsEnvironment(t *testing.T) {
	t.Setenv("SCHEMAFLUX_LOG_LEVEL", "warn")
	t.Setenv("SCHEMAFLUX_LOG_FORMAT", "json")
	t.Setenv("SCHEMAFLUX_LOG_BUFFER", "42")
	t.Setenv("SCHEMAFLUX_LOG_SOURCE", "1")
	t.Setenv("SCHEMAFLUX_LOG_DISABLE_STDERR", "true")
	t.Setenv("SCHEMAFLUX_LOG_DISABLE_CAPTURE", "")
	t.Setenv("SCHEMAFLUX_LOG_FILE", "")
	t.Setenv("SCHEMAFLUX_DEBUG", "")

	cfg := DefaultLoggerConfig()

	if cfg.Level != WarnLevel {
		t.Errorf("Level = %v, want WarnLevel", cfg.Level)
	}
	if cfg.Format != "json" {
		t.Errorf("Format = %q, want %q", cfg.Format, "json")
	}
	if cfg.BufferSize != 42 {
		t.Errorf("BufferSize = %d, want 42", cfg.BufferSize)
	}
	if !cfg.AddSource {
		t.Error("AddSource = false, want true (SCHEMAFLUX_LOG_SOURCE=1)")
	}
	if !cfg.DisableStderr {
		t.Error("DisableStderr = false, want true")
	}
	if !cfg.Capture {
		t.Error("Capture = false, want true (default, no disable var set)")
	}
}

func TestDefaultLoggerConfig_DebugEnvPromotesLevel(t *testing.T) {
	t.Setenv("SCHEMAFLUX_LOG_LEVEL", "")
	t.Setenv("SCHEMAFLUX_DEBUG", "true")

	cfg := DefaultLoggerConfig()
	if cfg.Level != DebugLevel {
		t.Errorf("Level = %v, want DebugLevel when SCHEMAFLUX_DEBUG=true and no explicit log level", cfg.Level)
	}
}

func TestGetDebugMode(t *testing.T) {
	config.SetDebugMode(false)
	t.Cleanup(func() { config.SetDebugMode(false) })

	if GetDebugMode() {
		t.Fatal("GetDebugMode() = true before SetDebugMode(true)")
	}
	config.SetDebugMode(true)
	if !GetDebugMode() {
		t.Fatal("GetDebugMode() = false after SetDebugMode(true)")
	}
}

func TestIsMetricsEnabled(t *testing.T) {
	config.SetMetricsEnabled(false)
	t.Cleanup(func() { config.SetMetricsEnabled(false) })

	if IsMetricsEnabled() {
		t.Fatal("IsMetricsEnabled() = true before SetMetricsEnabled(true)")
	}
	config.SetMetricsEnabled(true)
	if !IsMetricsEnabled() {
		t.Fatal("IsMetricsEnabled() = false after SetMetricsEnabled(true)")
	}
}

// TestContentLoggingDefaultOff_PayloadNeverReachesLogLine is the refusal
// test AGENTS.md calls load-bearing: "No request or response body in an
// error string or a log line. Ever." A process that never calls
// telemetry.SetContentLoggingPolicy must still never leak a caller payload
// wrapped in telemetry.Content, because the zero value of the policy is
// types.LogNoContent -- the strictest setting, not an unconfigured pass-
// through. This exercises internal/logger's own Logger alias end-to-end: a
// logger built through this package's ConfigureLogger, logging a
// Content-wrapped secret, must never produce that secret in the captured
// history or in the underlying writer.
func TestContentLoggingDefaultOff_PayloadNeverReachesLogLine(t *testing.T) {
	withRestoredGlobalLogger(t)

	// telemetry.ContentLoggingPolicy is process-global state (like the
	// logger itself); make sure a previous test's SetContentLoggingPolicy
	// call cannot make this test pass by accident, and put it back after.
	prevPolicy := telemetry.ContentLoggingPolicy()
	telemetry.SetContentLoggingPolicy(0) // types.LogNoContent, the zero value
	t.Cleanup(func() { telemetry.SetContentLoggingPolicy(prevPolicy) })

	var buf bytes.Buffer
	l := ConfigureLogger(LoggerConfig{
		Level:         DebugLevel,
		Output:        &buf,
		DisableStderr: false,
		Capture:       true,
	})

	const secretPayload = "sk-live-super-secret-customer-invoice-body-do-not-log-this"
	l.Info("processing request", "prompt", telemetry.Content(secretPayload))

	written := buf.String()
	if strings.Contains(written, secretPayload) {
		t.Fatalf("caller payload leaked into the log writer with content logging at its default (off) policy: %q", written)
	}

	entries := l.Entries()
	for _, entry := range entries {
		for key, value := range entry.Attributes {
			if s, ok := value.(string); ok && strings.Contains(s, secretPayload) {
				t.Fatalf("caller payload leaked into captured log history under attribute %q: %q", key, s)
			}
		}
	}
}

func TestLogLevelConstants_MapToTelemetry(t *testing.T) {
	// Pins that internal/logger's re-exported level constants are exactly
	// telemetry's, not an independently-numbered copy that could silently
	// drift out of sync (e.g. if telemetry ever reorders its iota block).
	cases := []struct {
		name string
		got  LogLevel
		want telemetry.LogLevel
	}{
		{"Debug", DebugLevel, telemetry.DebugLevel},
		{"Info", InfoLevel, telemetry.InfoLevel},
		{"Warn", WarnLevel, telemetry.WarnLevel},
		{"Error", ErrorLevel, telemetry.ErrorLevel},
		{"Fatal", FatalLevel, telemetry.FatalLevel},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s: internal/logger constant = %v, telemetry constant = %v", tc.name, tc.got, tc.want)
		}
	}
}
