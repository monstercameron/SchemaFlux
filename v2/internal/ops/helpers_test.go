package ops

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/config"
	"github.com/monstercameron/schemaflux/v2/internal/logger"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

type Person struct {
	Name string
	Age  int
}

// mockLLMResponse answers with a body shaped like Person, the target these
// tests extract into. It used to return `{"mock": "response"}`, which shares no
// field with any target -- so every test using it was asserting that an answer
// about something else parses successfully, which is the fail-open behaviour
// ParseJSONStrict now rejects.
func mockLLMResponse(ctx context.Context, system, user string, opts types.OpOptions) (string, error) {
	return `{"Name": "Mock Person", "Age": 30}`, nil
}

func setupMockClient() {
	setLLMCaller(mockLLMResponse)
}

// swapLLMCaller installs caller as the package's LLM caller for one test and
// puts back what was there when the test ends. Both the install and the restore
// go through providerMu: assigning customLLMCaller directly is a write the lock
// does not see, and the restore that does not also put back defaultProvider
// leaves the next test with whichever one this test replaced.
func swapLLMCaller(t *testing.T, caller LLMCaller) {
	t.Helper()
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(caller)
	t.Cleanup(func() {
		providerMu.Lock()
		defer providerMu.Unlock()
		customLLMCaller, defaultProvider = previousCaller, previousProvider
	})
}

// swapLLMResponse answers every call with response, for one test.
func swapLLMResponse(t *testing.T, response string) {
	t.Helper()
	swapLLMCaller(t, func(_ context.Context, _, _ string, _ types.OpOptions) (string, error) {
		return response, nil
	})
}

// swapNoProvider leaves the package with no LLM caller and no default provider
// for one test, which is the state of a process that never called Init.
func swapNoProvider(t *testing.T) {
	t.Helper()
	swapLLMCaller(t, nil)
	providerMu.Lock()
	defaultProvider = nil
	providerMu.Unlock()
}

// captureLogs sends the log, at the most talkative level and with debug mode
// on, to the logger's own history for the length of a test, and returns a
// function that renders every entry written so far: the level, the message and
// the attributes, one entry to a line.
//
// It reads the history and does not hand the logger a writer, because a logger
// with its stderr output disabled ignores the writer it was given. A helper
// that did both captured nothing, and every test that asserted the log did not
// carry a payload passed whatever was logged.
func captureLogs(t *testing.T) func() string {
	t.Helper()
	previous := logger.GetLogger()
	log := logger.ConfigureLogger(logger.LoggerConfig{Level: logger.DebugLevel, Format: "text", DisableStderr: true, Capture: true, BufferSize: 10000})
	config.SetDebugMode(true)
	t.Cleanup(func() {
		config.SetDebugMode(false)
		logger.SetLogger(previous)
	})
	return func() string {
		var rendered strings.Builder
		for _, entry := range log.Entries() {
			fmt.Fprintf(&rendered, "%s %s %v\n", entry.Level, entry.Message, entry.Attributes)
		}
		return rendered.String()
	}
}

// The helper that every "the log does not carry the payload" test is built on
// has to see what the library logs. This logs a known marker, as a message and
// as an attribute, at each level, and requires the helper to return it.
func TestCaptureLogsCapturesWhatTheLibraryLogs(t *testing.T) {
	const marker = "LOG-CAPTURE-MARKER-7f3a91"
	helpers := map[string]func(*testing.T) func() string{
		"captureLogs": captureLogs,
	}
	levels := map[string]func(message string, args ...any){
		"debug": func(message string, args ...any) { logger.GetLogger().Debug(message, args...) },
		"info":  func(message string, args ...any) { logger.GetLogger().Info(message, args...) },
		"warn":  func(message string, args ...any) { logger.GetLogger().Warn(message, args...) },
		"error": func(message string, args ...any) { logger.GetLogger().Error(message, args...) },
	}
	for helperName, helper := range helpers {
		for levelName, write := range levels {
			t.Run(helperName+" sees "+levelName+" attribute", func(t *testing.T) {
				logs := helper(t)
				write("a line with an attribute", "value", marker)
				if !strings.Contains(logs(), marker) {
					t.Errorf("the attribute logged at %s was not captured: %q", levelName, logs())
				}
			})
			t.Run(helperName+" sees "+levelName+" message", func(t *testing.T) {
				logs := helper(t)
				write(marker)
				if !strings.Contains(logs(), marker) {
					t.Errorf("the message logged at %s was not captured: %q", levelName, logs())
				}
			})
		}
		t.Run(helperName+" keeps every line, in order", func(t *testing.T) {
			logs := helper(t)
			logger.GetLogger().Info("first line", "value", "one")
			logger.GetLogger().Info("second line", "value", "two")
			text := logs()
			first, second := strings.Index(text, "first line"), strings.Index(text, "second line")
			if first < 0 || second < first {
				t.Errorf("the lines are missing or out of order: %q", text)
			}
		})
		t.Run(helperName+" sees nothing before the test logs", func(t *testing.T) {
			if text := helper(t)(); strings.Contains(text, marker) {
				t.Errorf("the capture starts with %q", text)
			}
		})
	}

	// Code that logs only in debug mode, which is where the callers' payload
	// would be logged, is captured only if the helper turns the mode on for the
	// test, and it does not leave it on afterwards.
	t.Run("captureLogs turns debug mode on for the test", func(t *testing.T) {
		captureLogs(t)
		if !config.GetDebugMode() {
			t.Error("debug mode is off during a test that captures the log")
		}
	})
	if config.GetDebugMode() {
		t.Error("captureLogs left debug mode on after its test")
	}
}

// replaceLLMCaller installs caller as the package's LLM caller and returns the
// function that puts back what was there, for a helper that hands the restore
// to its caller. The install and the restore both go through providerMu, as
// swapLLMCaller's do, and both hooks are put back.
func replaceLLMCaller(caller LLMCaller) (restore func()) {
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(caller)
	return func() {
		providerMu.Lock()
		defer providerMu.Unlock()
		customLLMCaller, defaultProvider = previousCaller, previousProvider
	}
}
