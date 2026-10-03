package logger

import (
	"testing"

	"github.com/monstercameron/schemaflux/internal/config/configtest"
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
	t.Setenv("SCHEMAFLUX_LOG_SOURCE", "true")
	t.Setenv("SCHEMAFLUX_LOG_DISABLE_STDERR", "true")
	t.Setenv("SCHEMAFLUX_LOG_DISABLE_CAPTURE", "")
	t.Setenv("SCHEMAFLUX_LOG_FILE", "")
	configtest.Setenv(t, "SCHEMAFLUX_DEBUG", "")

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
		t.Error("AddSource = false, want true (SCHEMAFLUX_LOG_SOURCE=true)")
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
	configtest.Setenv(t, "SCHEMAFLUX_DEBUG", "true")

	cfg := DefaultLoggerConfig()
	if cfg.Level != DebugLevel {
		t.Errorf("Level = %v, want DebugLevel when SCHEMAFLUX_DEBUG=true and no explicit log level", cfg.Level)
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
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s: internal/logger constant = %v, telemetry constant = %v", tc.name, tc.got, tc.want)
		}
	}
}
