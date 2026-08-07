package schemaflux

import (
	"testing"

	"github.com/monstercameron/schemaflux/internal/telemetry"
)

// WithDebug(false) used to do nothing to the logger, so a caller who turned
// debug on for one operation and off again kept debug logging -- and every
// prompt-adjacent field in those records -- for the rest of the process.
func TestWithDebugFalseRestoresThePriorLevel(t *testing.T) {
	client := NewClient("test-key")
	logger := client.logger

	cases := []telemetry.LogLevel{
		telemetry.InfoLevel,
		telemetry.WarnLevel,
		telemetry.ErrorLevel,
	}

	for _, start := range cases {
		logger.SetLevel(start)

		client.WithDebug(true)
		if got := logger.Level(); got != telemetry.DebugLevel {
			t.Fatalf("WithDebug(true) left the level at %v, want debug", got)
		}

		client.WithDebug(false)
		if got := logger.Level(); got != start {
			t.Errorf("WithDebug(false) left the level at %v, want the prior %v", got, start)
		}
	}
}

// Two enables must not make the remembered level "debug", or the restore is a
// no-op and the option is one-way again.
func TestRepeatedDebugEnablesRememberOnlyTheOriginalLevel(t *testing.T) {
	client := NewClient("test-key")
	client.logger.SetLevel(telemetry.WarnLevel)

	client.WithDebug(true)
	client.WithDebug(true)
	client.WithDebug(false)

	if got := client.logger.Level(); got != telemetry.WarnLevel {
		t.Errorf("level = %v, want warn", got)
	}
}

// Disabling debug that was never enabled leaves the level alone rather than
// resetting it to something the caller did not ask for.
func TestDisablingDebugThatWasNeverEnabledChangesNothing(t *testing.T) {
	client := NewClient("test-key")
	client.logger.SetLevel(telemetry.ErrorLevel)

	client.WithDebug(false)

	if got := client.logger.Level(); got != telemetry.ErrorLevel {
		t.Errorf("level = %v, want the untouched error level", got)
	}
}

// The flag itself still tracks the argument, because providerConfig reads it.
func TestDebugModeFlagTracksTheArgument(t *testing.T) {
	client := NewClient("test-key")

	client.WithDebug(true)
	if !client.debugMode {
		t.Error("debugMode = false after WithDebug(true)")
	}
	client.WithDebug(false)
	if client.debugMode {
		t.Error("debugMode = true after WithDebug(false)")
	}
}

// Level round-trips every level the library sets. Fatal shares slog's error
// level and is documented as not surviving; nothing here sets it.
func TestLoggerLevelRoundTrips(t *testing.T) {
	logger := telemetry.GetLogger()
	original := logger.Level()
	t.Cleanup(func() { logger.SetLevel(original) })

	for _, level := range []telemetry.LogLevel{
		telemetry.DebugLevel,
		telemetry.InfoLevel,
		telemetry.WarnLevel,
		telemetry.ErrorLevel,
	} {
		logger.SetLevel(level)
		if got := logger.Level(); got != level {
			t.Errorf("SetLevel(%v) then Level() = %v", level, got)
		}
	}
}

// A nil logger reports a usable level rather than panicking, because Level is
// called from WithDebug on a client a caller may have built oddly.
func TestNilLoggerReportsInfo(t *testing.T) {
	var logger *telemetry.Logger
	if got := logger.Level(); got != telemetry.InfoLevel {
		t.Errorf("(*Logger)(nil).Level() = %v, want info", got)
	}
}
