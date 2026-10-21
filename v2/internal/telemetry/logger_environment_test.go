package telemetry

import (
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/config"
	"github.com/monstercameron/schemaflux/v2/internal/config/configtest"
)

func clearLoggerEnvironment(t *testing.T) *[]config.EnvProblem {
	t.Helper()
	for _, name := range []string{
		"SCHEMAFLUX_LOG_LEVEL", "SCHEMAFLUX_LOG_FORMAT", "SCHEMAFLUX_LOG_BUFFER", "SCHEMAFLUX_LOG_FILE",
		"SCHEMAFLUX_LOG_DISABLE_CAPTURE", "SCHEMAFLUX_LOG_SOURCE", "SCHEMAFLUX_LOG_DISABLE_STDERR", "SCHEMAFLUX_DEBUG",
	} {
		// Through configtest: SCHEMAFLUX_DEBUG is among them, and the library
		// reads it once and keeps it.
		configtest.Setenv(t, name, "")
	}
	var problems []config.EnvProblem
	t.Cleanup(config.SetEnvProblemReporter(func(problem config.EnvProblem) { problems = append(problems, problem) }))
	return &problems
}

// SCHEMAFLUX_DEBUG means one thing to every reader of it, config.EnvBool's: the
// logger accepted yes and on while the operations' debug branch read only
// "true", and now both read what strconv.ParseBool reads.
func TestTheLoggerReadsSchemafluxDebugLikeTheOperationsDo(t *testing.T) {
	cases := []struct {
		raw         string
		want        LogLevel
		wantProblem bool
	}{
		{"true", DebugLevel, false},
		{"false", WarnLevel, false},
		{"", WarnLevel, false},
		{"1", DebugLevel, false},
		{"0", WarnLevel, false},
		{"t", DebugLevel, false},
		{"T", DebugLevel, false},
		{"f", WarnLevel, false},
		{"TRUE", DebugLevel, false},
		{"True", DebugLevel, false},
		{"FALSE", WarnLevel, false},
		{" true ", DebugLevel, false},
		{"yes", WarnLevel, true},
		{"on", WarnLevel, true},
		{"tRuE", WarnLevel, true},
		{"enabled", WarnLevel, true},
	}
	for _, tc := range cases {
		t.Run("["+tc.raw+"]", func(t *testing.T) {
			problems := clearLoggerEnvironment(t)
			configtest.Setenv(t, "SCHEMAFLUX_DEBUG", tc.raw)

			if got := DefaultLoggerConfig().Level; got != tc.want {
				t.Errorf("Level = %v with SCHEMAFLUX_DEBUG=%q, want %v", got, tc.raw, tc.want)
			}
			if got := len(*problems) == 1; got != tc.wantProblem {
				t.Errorf("reported %d problem(s), want a report: %v", len(*problems), tc.wantProblem)
			}
		})
	}
}

func TestAMalformedLoggerSettingIsReportedByName(t *testing.T) {
	cases := []struct {
		variable string
		value    string
		check    func(LoggerConfig) bool
	}{
		{"SCHEMAFLUX_LOG_LEVEL", "verbose", func(c LoggerConfig) bool { return c.Level == WarnLevel }},
		{"SCHEMAFLUX_LOG_FORMAT", "yaml", func(c LoggerConfig) bool { return c.Format == "text" }},
		{"SCHEMAFLUX_LOG_BUFFER", "lots", func(c LoggerConfig) bool { return c.BufferSize == 1000 }},
		{"SCHEMAFLUX_LOG_BUFFER", "0", func(c LoggerConfig) bool { return c.BufferSize == 1000 }},
		{"SCHEMAFLUX_LOG_BUFFER", "-5", func(c LoggerConfig) bool { return c.BufferSize == 1000 }},
		{"SCHEMAFLUX_LOG_SOURCE", "on", func(c LoggerConfig) bool { return !c.AddSource }},
		{"SCHEMAFLUX_LOG_SOURCE", "enabled", func(c LoggerConfig) bool { return !c.AddSource }},
		{"SCHEMAFLUX_LOG_DISABLE_STDERR", "yes", func(c LoggerConfig) bool { return !c.DisableStderr }},
		{"SCHEMAFLUX_LOG_DISABLE_CAPTURE", "on", func(c LoggerConfig) bool { return c.Capture }},
	}
	for _, tc := range cases {
		t.Run(tc.variable+"="+tc.value, func(t *testing.T) {
			problems := clearLoggerEnvironment(t)
			t.Setenv(tc.variable, tc.value)

			cfg := DefaultLoggerConfig()
			if !tc.check(cfg) {
				t.Errorf("a malformed value was not given the default: %+v", cfg)
			}
			if len(*problems) != 1 || (*problems)[0].Variable != tc.variable {
				t.Errorf("the problem was not reported once by name: %+v", *problems)
			}
		})
	}
}

func TestAWellFormedLoggerEnvironmentIsAcceptedWithoutAReport(t *testing.T) {
	problems := clearLoggerEnvironment(t)
	t.Setenv("SCHEMAFLUX_LOG_LEVEL", "WARN")
	t.Setenv("SCHEMAFLUX_LOG_FORMAT", "JSON")
	t.Setenv("SCHEMAFLUX_LOG_BUFFER", "25")
	t.Setenv("SCHEMAFLUX_LOG_SOURCE", "true")
	t.Setenv("SCHEMAFLUX_LOG_DISABLE_STDERR", "true")
	t.Setenv("SCHEMAFLUX_LOG_DISABLE_CAPTURE", "false")

	cfg := DefaultLoggerConfig()
	if cfg.Level != WarnLevel || cfg.Format != "json" || cfg.BufferSize != 25 || !cfg.AddSource || !cfg.DisableStderr || !cfg.Capture {
		t.Errorf("config = %+v", cfg)
	}
	if len(*problems) != 0 {
		t.Errorf("a well-formed environment was reported: %+v", *problems)
	}
}

// The level of a logger nobody configured is warn. A program that makes a call
// that goes well should not find a line about it on standard error.
func TestTheDefaultLevelIsWarn(t *testing.T) {
	problems := clearLoggerEnvironment(t)
	if DefaultLevel != WarnLevel {
		t.Fatalf("DefaultLevel = %v, want warn", DefaultLevel)
	}
	if got := DefaultLoggerConfig().Level; got != WarnLevel {
		t.Errorf("Level = %v with nothing set, want warn", got)
	}
	if got := NewLogger().Level(); got != WarnLevel {
		t.Errorf("a logger built from the environment is at %v, want warn", got)
	}
	if len(*problems) != 0 {
		t.Errorf("an empty environment was reported: %+v", *problems)
	}
}

func TestANamedLevelIsTakenAsItIs(t *testing.T) {
	cases := []struct {
		raw  string
		want LogLevel
	}{
		{"debug", DebugLevel},
		{"info", InfoLevel},
		{"INFO", InfoLevel},
		{"warn", WarnLevel},
		{"warning", WarnLevel},
		{"error", ErrorLevel},
		{"fatal", FatalLevel},
		{" Debug ", DebugLevel},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			clearLoggerEnvironment(t)
			t.Setenv("SCHEMAFLUX_LOG_LEVEL", tc.raw)
			if got := DefaultLoggerConfig().Level; got != tc.want {
				t.Errorf("Level = %v for SCHEMAFLUX_LOG_LEVEL=%q, want %v", got, tc.raw, tc.want)
			}
		})
	}
}

// SCHEMAFLUX_DEBUG raises a level of info, or one nobody named, to debug, and
// does not override a level somebody did name above info.
func TestSchemafluxDebugRaisesTheDefaultAndInfoButNotAHigherNamedLevel(t *testing.T) {
	cases := []struct {
		level string
		want  LogLevel
	}{
		{"", DebugLevel},
		{"info", DebugLevel},
		{"warn", WarnLevel},
		{"error", ErrorLevel},
		{"debug", DebugLevel},
	}
	for _, tc := range cases {
		t.Run("level["+tc.level+"]", func(t *testing.T) {
			clearLoggerEnvironment(t)
			t.Setenv("SCHEMAFLUX_LOG_LEVEL", tc.level)
			configtest.Setenv(t, "SCHEMAFLUX_DEBUG", "true")
			if got := DefaultLoggerConfig().Level; got != tc.want {
				t.Errorf("Level = %v with SCHEMAFLUX_DEBUG=true and SCHEMAFLUX_LOG_LEVEL=%q, want %v", got, tc.level, tc.want)
			}
		})
	}
}

// What a default logger writes for a call that goes well: nothing. A caller who
// sets the level to debug gets every line.
func TestADefaultLoggerWritesNothingForLinesBelowWarn(t *testing.T) {
	clearLoggerEnvironment(t)
	var out strings.Builder
	cfg := DefaultLoggerConfig()
	cfg.Output = &out
	log := NewLoggerWithConfig(cfg)

	log.Debug("a debug line")
	log.Info("an info line")
	if out.Len() != 0 || len(log.Entries()) != 0 {
		t.Fatalf("a default logger wrote %q and kept %d entries for debug and info lines", out.String(), len(log.Entries()))
	}
	log.Warn("a warn line")
	if !strings.Contains(out.String(), "a warn line") || len(log.Entries()) != 1 {
		t.Errorf("a default logger did not write a warning: %q", out.String())
	}

	log.SetLevel(DebugLevel)
	log.Debug("now a debug line")
	log.Info("now an info line")
	for _, want := range []string{"now a debug line", "now an info line"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("after the level was set to debug, %q was not written", want)
		}
	}
}

func TestGetLoggerHandsOutTheLoggerSetLoggerInstalled(t *testing.T) {
	previous := GetLogger()
	t.Cleanup(func() { SetLogger(previous) })

	first := NewLoggerWithConfig(LoggerConfig{Level: ErrorLevel, DisableStderr: true})
	second := NewLoggerWithConfig(LoggerConfig{Level: WarnLevel, DisableStderr: true})
	SetLogger(first)
	if GetLogger() != first {
		t.Fatal("GetLogger did not return the logger just installed")
	}
	SetLogger(second)
	if GetLogger() != second {
		t.Fatal("GetLogger returned a replaced logger")
	}
	SetLogger(nil)
	made := GetLogger()
	if made == nil || made == first || made == second {
		t.Fatal("after SetLogger(nil), GetLogger did not make a logger of its own")
	}
	if GetLogger() != made {
		t.Error("GetLogger made a second logger instead of keeping the first")
	}
}

func TestGetLoggerAsksForNoLockAndAllocatesNothing(t *testing.T) {
	_ = GetLogger()
	if got := testing.AllocsPerRun(200, func() { _ = GetLogger() }); got != 0 {
		t.Errorf("GetLogger allocates %v times, want 0", got)
	}
}

func TestConcurrentFirstUseOfGetLoggerMakesOneLogger(t *testing.T) {
	previous := GetLogger()
	t.Cleanup(func() { SetLogger(previous) })
	SetLogger(nil)

	const goroutines = 50
	results := make(chan *Logger, goroutines)
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		go func() { <-start; results <- GetLogger() }()
	}
	close(start)
	first := <-results
	for i := 1; i < goroutines; i++ {
		if got := <-results; got != first {
			t.Fatal("two goroutines were handed different loggers")
		}
	}
}
