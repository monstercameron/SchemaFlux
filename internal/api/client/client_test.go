// Package schemaflux's own tests -- the ones that reach unexported identifiers
// and therefore have to live in this directory, because Go requires a package's
// tests to sit beside it.
//
// They were nine separate files at the repository root. Everything that only
// touches the public API is in ./tests/ instead, and merging what was left keeps
// the root to the four source files plus this one. The sections below are the
// original files, named where each begins, so a reader looking for
// "client_env_test.go" can still find where it went.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/config/configtest"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/requesttracking"
	"github.com/monstercameron/schemaflux/internal/telemetry"
	"github.com/monstercameron/schemaflux/internal/types"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- from client_concurrency_test.go ---

// NewClient("") used to attach the local mock provider, so a caller who built a
// client by hand with an empty key got "Mock response for: ..." from every
// operation with nothing to indicate it. The mock has to be asked for.
func TestNewClientWithoutAKeyHasNoProvider(t *testing.T) {
	client := NewClient("")
	if client == nil {
		t.Fatal("NewClient must still return a client")
	}
	if client.provider != nil {
		t.Errorf("an empty key must not select a provider, got %q", client.provider.Name())
	}
}

// And a key produces one.
func TestNewClientWithAKeyHasAProvider(t *testing.T) {
	client := NewClient("test-key")
	if client.provider == nil {
		t.Fatal("a key must produce a provider")
	}
	if client.provider.Name() == "local" {
		t.Error("a real key must not resolve to the mock provider")
	}
}

// The mock is still reachable, deliberately.
func TestWithMockProviderIsExplicit(t *testing.T) {
	client := NewClient("").WithMockProvider()
	if client.provider == nil {
		t.Fatal("WithMockProvider must attach a provider")
	}
	if client.provider.Name() != "local" {
		t.Errorf("provider = %q, want local", client.provider.Name())
	}
	if client.providerName != "local" {
		t.Errorf("providerName = %q, want local", client.providerName)
	}
}

// Chaining still works after WithMockProvider.
func TestWithMockProviderChains(t *testing.T) {
	client := NewClient("").WithMockProvider().WithRetries(0).WithDebug(false)
	if client.provider == nil || client.maxRetries != 0 {
		t.Errorf("chaining lost state: provider=%v retries=%d", client.provider, client.maxRetries)
	}
}

// Init returning an error widened the window in which defaultClient is nil, and
// the logger accessors read it without the mutex Init writes it under. Run them
// together under -race.
func TestConcurrentInitAndLoggerAccess(t *testing.T) {
	t.Setenv("SCHEMAFLUX_API_KEY", "concurrent-test-key")
	t.Cleanup(func() {
		mu.Lock()
		defaultClient = nil
		mu.Unlock()
	})

	var wg sync.WaitGroup
	const workers = 8
	const iterations = 50

	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				switch (id + i) % 5 {
				case 0:
					_ = Init("concurrent-test-key")
				case 1:
					_ = GetLogger()
				case 2:
					SetLogLevel(telemetry.InfoLevel)
				case 3:
					_ = ConfigureLogging(telemetry.LoggerConfig{})
				case 4:
					_ = GetDefaultClient()
				}
			}
		}(worker)
	}

	wg.Wait()
}

// GetLogger must work before any Init, without panicking on the nil client.
func TestGetLoggerBeforeInit(t *testing.T) {
	mu.Lock()
	previous := defaultClient
	defaultClient = nil
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		defaultClient = previous
		mu.Unlock()
	})

	if GetLogger() == nil {
		t.Fatal("GetLogger must return a logger before Init")
	}
	if GetDefaultClient() != nil {
		t.Error("GetDefaultClient must be nil before Init")
	}
	SetLogLevel(telemetry.InfoLevel)
	_ = ConfigureLogging(telemetry.LoggerConfig{})
}

// The no-provider error a caller sees when they discarded Init's error must
// name the way out.
func TestUninitialisedOperationErrorNamesTheWayOut(t *testing.T) {
	message := ops.ErrNoProvider.Error()

	for _, expected := range []string{
		"Init", "InitWithEnv",
		"SCHEMAFLUX_API_KEY", "SCHEMAFLUX_OPENAI_API_KEY", "OPENAI_API_KEY", "OPENAI",
	} {
		if !strings.Contains(message, expected) {
			t.Errorf("the message should name %q: %s", expected, message)
		}
	}
}

// The provider a client ends up with must match how it was configured, across
// every route into one. NewClient("") used to reach the mock through the last
// of these silently.
func TestClientProviderSelection(t *testing.T) {
	cases := []struct {
		name         string
		build        func() *Client
		wantProvider bool
		wantName     string
	}{
		{"no_key", func() *Client { return NewClient("") }, false, ""},
		{"with_key", func() *Client { return NewClient("k") }, true, "openai"},
		{"explicit_mock", func() *Client { return NewClient("").WithMockProvider() }, true, "local"},
		{"mock_then_failed_switch", func() *Client { return NewClient("").WithMockProvider().WithProvider("openai") }, true, "local"},
		{"real_then_mock", func() *Client { return NewClient("k").WithMockProvider() }, true, "local"},
		{"provider_by_name", func() *Client { return NewClient("k").WithProvider("local") }, true, "local"},
		{"whitespace_key_is_a_key", func() *Client { return NewClient(" ") }, true, "openai"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := tc.build()
			if client == nil {
				t.Fatal("NewClient must return a client")
			}
			if hasProvider := client.provider != nil; hasProvider != tc.wantProvider {
				t.Fatalf("provider present = %v, want %v", hasProvider, tc.wantProvider)
			}
			if tc.wantProvider && tc.wantName != "" && client.provider.Name() != tc.wantName {
				t.Errorf("provider = %q, want %q", client.provider.Name(), tc.wantName)
			}
			// The reported name and the running provider must agree. A failed
			// switch used to rename the client without replacing the provider.
			if tc.wantProvider && client.providerName != client.provider.Name() {
				t.Errorf("providerName = %q but the running provider is %q",
					client.providerName, client.provider.Name())
			}
		})
	}
}

// The builder methods have to be safe to call from several goroutines, because
// the package hands out one shared default client.
func TestClientBuilderIsConcurrencySafe(t *testing.T) {
	client := NewClient("k")
	// The workers leave debug on or off by whichever ran last. A client that
	// still holds the shared logger at debug would keep every later test's
	// WithDebug(false) from restoring a level.
	t.Cleanup(func() { client.WithDebug(false) })

	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				switch (id + i) % 4 {
				case 0:
					client.WithTimeout(time.Duration(i+1) * time.Second)
				case 1:
					client.WithRetries(i % 4)
				case 2:
					client.WithRetryBackoff(time.Duration(i+1) * time.Millisecond)
				case 3:
					client.WithDebug(i%2 == 0)
				}
			}
		}(worker)
	}
	wg.Wait()

	if client.maxRetries < 0 || client.timeout <= 0 {
		t.Errorf("the client ended in an inconsistent state: retries=%d timeout=%v", client.maxRetries, client.timeout)
	}
}

// WithRetries clamps a negative budget rather than storing it, because a
// negative attempt count elsewhere means "use the default".
func TestWithRetriesClampsNegatives(t *testing.T) {
	for _, given := range []int{-5, -1, 0, 1, 3} {
		client := NewClient("k").WithRetries(given)
		want := given
		if want < 0 {
			want = 0
		}
		if client.maxRetries != want {
			t.Errorf("WithRetries(%d) stored %d, want %d", given, client.maxRetries, want)
		}
	}
}

// A nil provider instance is ignored rather than clearing the one in place.
func TestWithProviderInstanceIgnoresNil(t *testing.T) {
	client := NewClient("").WithMockProvider()
	before := client.provider

	client.WithProviderInstance(nil)

	if client.provider != before {
		t.Error("a nil provider instance must not replace the configured one")
	}
}

// A builder chain that could not do what it was asked has to say so somewhere,
// because the methods return *Client and cannot return an error.
func TestFailedProviderSwitchIsReportedByErr(t *testing.T) {
	clearCredentialEnv(t)

	client := NewClient("").WithMockProvider()
	if err := client.Err(); err != nil {
		t.Fatalf("a working chain must report no error: %v", err)
	}

	client.WithProvider("openai")

	if client.Err() == nil {
		t.Fatal("a provider switch that could not be completed must be reported by Err")
	}
	if !strings.Contains(client.Err().Error(), "openai") {
		t.Errorf("the error should name the provider that failed: %v", client.Err())
	}
	if client.provider.Name() != "local" {
		t.Errorf("the previous provider must stay in use, got %q", client.provider.Name())
	}
}

// And a subsequent successful switch clears it.
func TestSuccessfulProviderSwitchClearsErr(t *testing.T) {
	clearCredentialEnv(t)

	client := NewClient("").WithProvider("openai")
	if client.Err() == nil {
		t.Fatal("expected the first switch to fail without a credential")
	}

	client.WithProvider("local")
	if err := client.Err(); err != nil {
		t.Errorf("a successful switch must clear the recorded error: %v", err)
	}
}

// --- from client_debug_test.go ---

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
// called from WithDebug on a client a caller may have built oddly. The level it
// reports is the default one, warn, which is what a logger nobody configured has.
func TestNilLoggerReportsTheDefaultLevel(t *testing.T) {
	var logger *telemetry.Logger
	if got := logger.Level(); got != telemetry.WarnLevel {
		t.Errorf("(*Logger)(nil).Level() = %v, want warn", got)
	}
}

// --- from client_env_test.go ---

// clearCredentialEnv removes every variable the key resolver consults so a test
// starts from a known-empty environment.
func clearCredentialEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"SCHEMAFLUX_API_KEY", "SCHEMAFLUX_OPENAI_API_KEY", "OPENAI_API_KEY", "OPENAI",
		"SCHEMAFLUX_PROVIDER", "SCHEMAFLUX_TIMEOUT", "SCHEMAFLUX_DEBUG",
	} {
		// Through configtest: the timeout and the debug switch are read once and
		// kept, so clearing them has to make the library read them again.
		configtest.Setenv(t, name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unset %s: %v", name, err)
		}
	}
	t.Cleanup(func() {
		mu.Lock()
		defaultClient = nil
		mu.Unlock()
	})
}

// A missing credential must be an error, not a silent fall-through to the mock
// provider, which returns "Mock response for: ..." from every operation.
func TestInitWithoutKeyIsAnError(t *testing.T) {
	clearCredentialEnv(t)

	err := Init("")
	if err == nil {
		t.Fatal("Init with no credential must return an error")
	}
	if !strings.Contains(err.Error(), "no API key") {
		t.Errorf("error should name the missing credential, got: %v", err)
	}
}

// The mock provider stays available when it is chosen deliberately.
func TestInitAllowsMockProviderWhenRequestedExplicitly(t *testing.T) {
	clearCredentialEnv(t)
	t.Setenv("SCHEMAFLUX_PROVIDER", "local")

	if err := Init(""); err != nil {
		t.Fatalf("explicitly requesting the local provider must succeed: %v", err)
	}
	if GetDefaultClient() == nil {
		t.Fatal("expected a client")
	}
}

// InitWithEnv must actually read the named file. Previously it accepted paths
// and ignored them, so a .env file never had any effect.
func TestInitWithEnvLoadsNamedFile(t *testing.T) {
	clearCredentialEnv(t)

	if err := InitWithEnv("../../../testdata/sample.env"); err != nil {
		t.Fatalf("InitWithEnv: %v", err)
	}
	if GetDefaultClient() == nil {
		t.Fatal("expected a client after loading the fixture")
	}
	// The fixture spells the key "OPENAI", a common .env convention. Rejecting
	// that spelling means a file plainly containing the key resolves to nothing.
	if got := os.Getenv("OPENAI"); got == "" {
		t.Error("the fixture's OPENAI value should be present in the environment")
	}
}

// A named path that does not exist is a configuration error worth reporting.
func TestInitWithEnvReportsMissingFile(t *testing.T) {
	clearCredentialEnv(t)

	err := InitWithEnv("../../../testdata/definitely-not-here.env")
	if err == nil {
		t.Fatal("a named .env path that does not exist must return an error")
	}
}

// An explicit export beats a .env value: the file supplies defaults, it does
// not override the operator.
func TestProcessEnvironmentWinsOverEnvFile(t *testing.T) {
	clearCredentialEnv(t)
	t.Setenv("OPENAI", "sk-from-the-process-environment")

	if err := InitWithEnv("../../../testdata/sample.env"); err != nil {
		t.Fatalf("InitWithEnv: %v", err)
	}
	if got := os.Getenv("OPENAI"); got != "sk-from-the-process-environment" {
		t.Errorf("the .env file overrode an explicit export: got %q", got)
	}
}

// The credential resolution chain must be honoured in order, and any one of the
// accepted spellings must work on its own. The key a provider sends is read from
// the Authorization header of a request, which is the only place the key it was
// really built with can be seen.
func TestCredentialResolutionOrder(t *testing.T) {
	cases := []struct {
		name string
		set  map[string]string
		want string
	}{
		{"schemaflux_generic", map[string]string{"SCHEMAFLUX_API_KEY": "k"}, "Bearer k"},
		{"schemaflux_openai", map[string]string{"SCHEMAFLUX_OPENAI_API_KEY": "k"}, "Bearer k"},
		{"openai_api_key", map[string]string{"OPENAI_API_KEY": "k"}, "Bearer k"},
		{"bare_openai", map[string]string{"OPENAI": "k"}, "Bearer k"},
		{"provider_specific_wins", map[string]string{
			"SCHEMAFLUX_OPENAI_API_KEY": "specific",
			"OPENAI":                    "generic",
		}, "Bearer specific"},
		{"provider_specific_beats_the_generic_variable", map[string]string{
			"OPENAI_API_KEY":     "specific",
			"SCHEMAFLUX_API_KEY": "generic",
		}, "Bearer specific"},
		{"order_among_provider_variables", map[string]string{
			"SCHEMAFLUX_OPENAI_API_KEY": "first",
			"OPENAI_API_KEY":            "second",
			"OPENAI":                    "third",
		}, "Bearer first"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, seen := authorizationServer(t)
			tuningEnvironment(t, server.URL)
			for name, value := range tc.set {
				t.Setenv(name, value)
			}
			if err := Init(""); err != nil {
				t.Fatalf("a resolvable credential must initialise: %v", err)
			}
			if GetDefaultClient() == nil {
				t.Fatal("expected a client")
			}
			if _, err := extractThrough(GetDefaultClient().WithRetries(0)); err == nil {
				t.Fatal("a server that answers 400 produced a result")
			}
			if got := seen(); len(got) != 1 || got[0] != tc.want {
				t.Errorf("the server saw Authorization %q, want exactly [%s]", got, tc.want)
			}
		})
	}
}

// An explicit key argument beats the environment entirely, the provider's own
// variables included. The test used to assert only that a client existed, so it
// could not tell the argument from the environment.
func TestExplicitKeyArgumentWins(t *testing.T) {
	server, seen := authorizationServer(t)
	tuningEnvironment(t, server.URL)
	t.Setenv("SCHEMAFLUX_API_KEY", "from-env")
	t.Setenv("OPENAI_API_KEY", "from-the-provider-variable")

	if err := Init("from-argument"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if GetDefaultClient() == nil {
		t.Fatal("expected a client")
	}
	if _, err := extractThrough(GetDefaultClient().WithRetries(0)); err == nil {
		t.Fatal("a server that answers 400 produced a result")
	}
	if got := seen(); len(got) != 1 || got[0] != "Bearer from-argument" {
		t.Errorf("the server saw Authorization %q, want the argument's key", got)
	}
}

// Initialising twice must not leave a stale client behind.
func TestReinitialisationReplacesTheClient(t *testing.T) {
	clearCredentialEnv(t)
	t.Setenv("SCHEMAFLUX_API_KEY", "first")
	if err := Init(""); err != nil {
		t.Fatalf("Init: %v", err)
	}
	first := GetDefaultClient()

	if err := Init("second"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if GetDefaultClient() == first {
		t.Error("a second Init must replace the client")
	}
}

// Loading several .env files applies them in order without error.
func TestInitWithEnvAcceptsSeveralFiles(t *testing.T) {
	clearCredentialEnv(t)

	if err := InitWithEnv("../../../testdata/sample.env", "../../../testdata/sample.env"); err != nil {
		t.Fatalf("loading the same file twice must be harmless: %v", err)
	}
}

// A directory is not a .env file and must be reported rather than ignored.
func TestInitWithEnvRejectsADirectory(t *testing.T) {
	clearCredentialEnv(t)

	if err := InitWithEnv("testdata"); err == nil {
		t.Fatal("a directory must not be accepted as a .env file")
	}
}

// The timeout in the fixture must actually be applied, proving the file is read
// for more than the credential alone.
func TestInitWithEnvAppliesNonCredentialValues(t *testing.T) {
	clearCredentialEnv(t)

	if err := InitWithEnv("../../../testdata/sample.env"); err != nil {
		t.Fatalf("InitWithEnv: %v", err)
	}
	if got := os.Getenv("SCHEMAFLUX_TIMEOUT"); got != "12s" {
		t.Errorf("SCHEMAFLUX_TIMEOUT = %q, want the fixture value 12s", got)
	}
	// And it reached the client that InitWithEnv built, not only the environment.
	if got := GetDefaultClient().timeout; got != 12*time.Second {
		t.Errorf("the default client's timeout is %v, want the fixture's 12s", got)
	}
}

// --- from client_isolation_test.go ---

// clearModelOverrides removes every variable that names a model, so a test starts
// from an environment in which a provider's own mapping decides.
func clearModelOverrides(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"SCHEMAFLUX_MODEL", "SCHEMAFLUX_MODEL_SMART", "SCHEMAFLUX_MODEL_FAST", "SCHEMAFLUX_MODEL_QUICK",
	} {
		configtest.Setenv(t, name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unset %s: %v", name, err)
		}
	}
	t.Cleanup(func() {
		mu.Lock()
		defaultClient = nil
		mu.Unlock()
	})
}

// A provider with no model mapping used to be reported as configured. The first
// operation to run then failed with a message the caller read minutes or days
// after the configuration they needed to change.
func TestUnmappedProviderIsReportedAtConstruction(t *testing.T) {
	clearModelOverrides(t)

	for _, provider := range []string{"qwen", "zai"} {
		t.Run(provider, func(t *testing.T) {
			client := NewClient("test-key").WithProvider(provider)

			err := client.Err()
			if err == nil {
				t.Fatalf("WithProvider(%q) left Err() nil; the client cannot resolve a model", provider)
			}
			if !errors.Is(err, config.ErrNoModelMapping) {
				t.Errorf("errors.Is(err, ErrNoModelMapping) = false; err = %v", err)
			}
			if !strings.Contains(err.Error(), "SCHEMAFLUX_MODEL") {
				t.Errorf("the error must name the way out, got: %v", err)
			}
		})
	}
}

// Every provider the library ships a mapping for constructs clean.
func TestMappedProvidersConstructWithoutError(t *testing.T) {
	clearModelOverrides(t)

	for _, provider := range []string{"openai", "anthropic", "cerebras", "deepseek", "openrouter", "local"} {
		t.Run(provider, func(t *testing.T) {
			client := NewClient("test-key").WithProvider(provider)
			if err := client.Err(); err != nil {
				t.Fatalf("WithProvider(%q) reported %v, want nil", provider, err)
			}
		})
	}
}

// An override rescues the unmapped provider, because pointing the library at a
// private gateway is a supported configuration.
func TestModelOverrideClearsTheConstructionError(t *testing.T) {
	clearModelOverrides(t)
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "my-local-model")

	client := NewClient("test-key").WithProvider("qwen")
	if err := client.Err(); err != nil {
		t.Fatalf("Err() = %v, want nil with SCHEMAFLUX_MODEL set", err)
	}
}

// The other half of "an unknown provider/model pair returns an error": a name
// the provider factory has never heard of fails earlier, at construction, and
// the previous provider stays in use rather than the client running headless.
func TestUnknownProviderNameFailsAtConstruction(t *testing.T) {
	clearModelOverrides(t)

	client := NewClient("test-key")
	client.WithProvider("my-internal-gateway")

	err := client.Err()
	if err == nil {
		t.Fatal("an unknown provider name must be reported")
	}
	if !strings.Contains(err.Error(), "my-internal-gateway") {
		t.Errorf("the error must name the provider, got: %v", err)
	}

	client.mu.RLock()
	name := client.providerName
	client.mu.RUnlock()
	if name != "openai" {
		t.Errorf("providerName = %q; a failed switch must leave the previous provider in place", name)
	}
}

// The provider is still attached when the model is unresolvable: the caller may
// be about to set the variable, and dropping their provider would lose the rest
// of the configuration to a problem they can fix.
func TestUnmappedProviderIsStillAttached(t *testing.T) {
	clearModelOverrides(t)

	client := NewClient("test-key").WithProvider("qwen")
	if client.Err() == nil {
		t.Fatal("expected a configuration error")
	}

	client.mu.RLock()
	provider, name := client.provider, client.providerName
	client.mu.RUnlock()

	if provider == nil {
		t.Error("the provider was dropped; the caller loses the rest of their configuration")
	}
	if name != "qwen" {
		t.Errorf("providerName = %q, want qwen", name)
	}
}

// Switching away from a broken provider clears the error, so Err() reports the
// current state rather than a historical one.
func TestSwitchingToAMappedProviderClearsTheError(t *testing.T) {
	clearModelOverrides(t)

	client := NewClient("test-key").WithProvider("qwen")
	if client.Err() == nil {
		t.Fatal("expected a configuration error from the unmapped provider")
	}

	client.WithProvider("anthropic")
	if err := client.Err(); err != nil {
		t.Fatalf("Err() = %v after switching to a mapped provider, want nil", err)
	}
}

// Init has an error return and a configuration failure to report; it used to
// return nil and leave the failure for the first operation.
func TestInitReportsAnUnresolvableModel(t *testing.T) {
	clearCredentialEnv(t)
	clearModelOverrides(t)
	t.Setenv("SCHEMAFLUX_API_KEY", "test-key")
	t.Setenv("SCHEMAFLUX_PROVIDER", "qwen")

	err := Init("")
	if err == nil {
		t.Fatal("Init must report a provider it cannot resolve a model for")
	}
	if !errors.Is(err, config.ErrNoModelMapping) {
		t.Errorf("errors.Is(err, ErrNoModelMapping) = false; err = %v", err)
	}
	if GetDefaultClient() == nil {
		t.Error("the client should still be installed; the caller can fix this with an env var")
	}
}

// The same Init with the variable set succeeds, which is the configuration the
// error above tells the caller to write.
func TestInitSucceedsWithAModelOverride(t *testing.T) {
	clearCredentialEnv(t)
	clearModelOverrides(t)
	t.Setenv("SCHEMAFLUX_API_KEY", "test-key")
	t.Setenv("SCHEMAFLUX_PROVIDER", "qwen")
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "qwen-max")

	if err := Init(""); err != nil {
		t.Fatalf("Init = %v, want nil once the model is named", err)
	}
}

// A mapped provider through Init reports nothing, which is the case that must
// not regress: the check is only useful if it is quiet when the configuration
// is fine.
func TestInitWithAMappedProviderIsQuiet(t *testing.T) {
	clearCredentialEnv(t)
	clearModelOverrides(t)
	t.Setenv("SCHEMAFLUX_API_KEY", "test-key")
	t.Setenv("SCHEMAFLUX_PROVIDER", "deepseek")

	if err := Init(""); err != nil {
		t.Fatalf("Init = %v, want nil for a mapped provider", err)
	}
}

// WithProviderInstance is the injection path tests and callers with their own
// transport use. It carries its own model decisions, so the mapping check does
// not apply to it — asserting that here keeps a later change from breaking
// schemafluxtest.Install.
func TestProviderInstanceIsNotSubjectToTheMappingCheck(t *testing.T) {
	clearModelOverrides(t)

	client := NewClient("test-key").WithProvider("anthropic")
	if err := client.Err(); err != nil {
		t.Fatalf("setup: %v", err)
	}

	client.WithProviderInstance(&stubProvider{name: "custom-gateway"})
	if err := client.Err(); err != nil {
		t.Fatalf("Err() = %v after WithProviderInstance, want nil", err)
	}
}

// --- from client_snapshot_test.go ---

// IN-004's Verify line: "two clients with different providers, budgets, and
// policies run concurrently and independently."
//
// A test with one client proves nothing here. The bug was never that a single
// client misbehaved — it was that there was only ever one set of process
// globals, so constructing a second client silently reconfigured the first.
// IN-001's mutexes made that safe and left it wrong: a mutex around a global
// passes `-race` and still fails this test.
//
// So every case below runs TWO clients, and most of them run both at once.

// namedProvider answers with its own name, so a test can tell which client's
// provider actually served a call rather than only that some call happened.
type namedProvider struct {
	name  string
	calls atomic.Int64
	cost  float64
}

func (p *namedProvider) Complete(_ context.Context, _ llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.calls.Add(1)
	return llm.CompletionResponse{
		Content:      `{"name":"` + p.name + `"}`,
		Provider:     p.name,
		Model:        p.name + "-model",
		FinishReason: "stop",
	}, nil
}

func (p *namedProvider) Name() string                               { return p.name }
func (p *namedProvider) EstimateCost(llm.CompletionRequest) float64 { return p.cost }
func (p *namedProvider) RetryPolicy() (int, time.Duration)          { return 0, time.Millisecond }

type isolationTarget struct {
	Name string `json:"name"`
}

func TestTwoClientsReachTheirOwnProvidersConcurrently(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")

	first := &namedProvider{name: "alpha"}
	second := &namedProvider{name: "beta"}

	clientA := NewClient("key-a").WithProviderInstance(first)
	clientB := NewClient("key-b").WithProviderInstance(second)

	// The provider is NOT passed to the operation. It has to be resolved from
	// the client's context, because that resolution is the entire thing under
	// test -- handing each call its provider explicitly would pass even with
	// the seam removed.
	extract := func(client *Client) {
		ctx := client.Context(context.Background())
		opts := ops.NewExtractOptions()
		opts.CommonOptions = opts.CommonOptions.WithContext(ctx)
		_, _ = ops.ExtractResult[isolationTarget]("some input", opts)
	}

	const each = 50
	var wg sync.WaitGroup
	for i := 0; i < each; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); extract(clientA) }()
		go func() { defer wg.Done(); extract(clientB) }()
	}
	wg.Wait()

	if got := first.calls.Load(); got != each {
		t.Errorf("client A's provider saw %d calls, want %d", got, each)
	}
	if got := second.calls.Load(); got != each {
		t.Errorf("client B's provider saw %d calls, want %d", got, each)
	}
}

// The half that was genuinely impossible before: two budgets. One client
// exhausting its allowance must not stop the other, which is exactly what a
// single process-wide budget did.
func TestOneClientsExhaustedBudgetDoesNotStopAnother(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")

	poor := NewClient("key-poor").WithBudget(0.0001)
	rich := NewClient("key-rich").WithBudget(1000)

	// Spend the poor client's allowance directly on its ledger, which is what
	// a real call would have done.
	poorCtx := poor.Context(context.Background())
	if budget := ops.ExecBudget(poorCtx); budget != nil {
		budget.Record(1.0, true)
	} else {
		t.Fatal("the poor client carried no budget into its context")
	}

	if err := ops.ExecBudget(poorCtx).Check(); err == nil {
		t.Error("the exhausted client was still permitted to call")
	}

	richCtx := rich.Context(context.Background())
	richBudget := ops.ExecBudget(richCtx)
	if richBudget == nil {
		t.Fatal("the rich client carried no budget into its context")
	}
	if err := richBudget.Check(); err != nil {
		t.Errorf("the second client was refused by the first client's exhausted budget: %v", err)
	}
}

func TestAClientWithNoBudgetFallsBackToTheProcessBudget(t *testing.T) {
	plain := NewClient("key-plain")

	ctx := plain.Context(context.Background())
	if budget := ops.ExecBudget(ctx); budget != nil {
		t.Error("a client that never asked for a budget carries one; the process-wide budget should apply unchanged")
	}
}

func TestAZeroCeilingClearsTheBudgetRatherThanPinningIt(t *testing.T) {
	client := NewClient("key").WithBudget(5)
	if ops.ExecBudget(client.Context(context.Background())) == nil {
		t.Fatal("precondition: a positive ceiling should install a ledger")
	}

	client.WithBudget(0)
	if ops.ExecBudget(client.Context(context.Background())) != nil {
		t.Error("a zero ceiling pinned the client to a budget it can never spend under; it should clear it")
	}
}

// A call already running keeps the configuration it started with. This is the
// property the word "snapshot" is doing work for: not that the struct lacks
// setters, but that reconfiguring a client cannot reach into a call in flight.
func TestReconfiguringAClientDoesNotChangeACallAlreadyInFlight(t *testing.T) {
	first := &namedProvider{name: "alpha"}
	second := &namedProvider{name: "beta"}

	client := NewClient("key").WithProviderInstance(first)
	inFlight := client.Context(context.Background())

	// Reconfigure after the context was taken.
	client.WithProviderInstance(second)

	if got := ops.ExecProvider(inFlight); got != llm.Provider(first) {
		t.Errorf("the in-flight context now resolves to %v; reconfiguration reached a call already started", got)
	}

	// And a context taken after the change sees the change.
	if got := ops.ExecProvider(client.Context(context.Background())); got != llm.Provider(second) {
		t.Error("a context taken after reconfiguration still resolves to the old provider")
	}
}

func TestTwoClientsCarryTheirOwnDataPolicies(t *testing.T) {
	strict := NewClient("key-strict").WithDataPolicy(types.DataPolicy{AllowedProviders: []string{"alpha"}})
	open := NewClient("key-open").WithDataPolicy(types.DataPolicy{AllowedProviders: []string{"beta"}})

	strictPolicy, ok := ops.ExecDataPolicy(strict.Context(context.Background()))
	if !ok {
		t.Fatal("the strict client carried no policy")
	}
	openPolicy, ok := ops.ExecDataPolicy(open.Context(context.Background()))
	if !ok {
		t.Fatal("the open client carried no policy")
	}

	if len(strictPolicy.AllowedProviders) != 1 || strictPolicy.AllowedProviders[0] != "alpha" {
		t.Errorf("strict client's policy = %v", strictPolicy.AllowedProviders)
	}
	if len(openPolicy.AllowedProviders) != 1 || openPolicy.AllowedProviders[0] != "beta" {
		t.Errorf("open client's policy = %v", openPolicy.AllowedProviders)
	}
}

// An unconfigured process and a client that deliberately allows everything must
// stay distinguishable, or "no policy" and "an empty policy" become the same
// thing and one of them will eventually be enforced as the other.
func TestNoClientIsDistinguishableFromAnEmptyPolicy(t *testing.T) {
	if _, ok := ops.ExecDataPolicy(context.Background()); ok {
		t.Error("a bare context reported carrying a policy")
	}

	empty := NewClient("key").WithDataPolicy(types.DataPolicy{})
	if _, ok := ops.ExecDataPolicy(empty.Context(context.Background())); !ok {
		t.Error("a client that declared an empty policy is indistinguishable from no client at all")
	}
}

// The ledger's own honesty: an unpriced call must not read as a free one.
func TestAnUnpricedCallMakesTheSpentFigureAFloor(t *testing.T) {
	client := NewClient("key").WithBudget(10)
	budget := ops.ExecBudget(client.Context(context.Background()))

	budget.Record(1.50, true)
	if spent, complete := client.Spent(); spent != 1.50 || !complete {
		t.Errorf("Spent = (%v, %v), want (1.50, true)", spent, complete)
	}

	budget.Record(0, false)
	spent, complete := client.Spent()
	if complete {
		t.Error("the total is still reported as complete after an unpriced call")
	}
	if spent != 1.50 {
		t.Errorf("spend = %v; an unpriced call changed the figure rather than its completeness", spent)
	}
}

func TestAnExhaustedLedgerRefusesWithATypedError(t *testing.T) {
	budget := ops.NewClientBudget(1.0)
	budget.Record(1.0, true)

	err := budget.Check()
	if err == nil {
		t.Fatal("a ledger at its ceiling permitted a call")
	}
	if !errors.Is(err, ops.ErrClientBudgetExhausted) {
		t.Errorf("err = %v, want ErrClientBudgetExhausted", err)
	}
}

// Concurrency on one ledger: the recorded total must be exact, not
// approximately right.
func TestALedgerUnderConcurrentRecordsTotalsExactly(t *testing.T) {
	budget := ops.NewClientBudget(1e9)

	const goroutines, each = 20, 100
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				budget.Record(0.01, true)
			}
		}()
	}
	wg.Wait()

	spent, complete := budget.Spent()
	if !complete {
		t.Error("a ledger that saw only priced calls reported an incomplete total")
	}
	want := 0.01 * goroutines * each
	if diff := spent - want; diff > 1e-6 || diff < -1e-6 {
		t.Errorf("spent = %v, want %v", spent, want)
	}
}

// End to end: an exhausted client budget refuses a real call, and the other
// client's identical call still goes through. This is the case the unit tests
// above cannot make -- they check the ledger, this checks that the call path
// consults it.
func TestAnExhaustedClientBudgetRefusesARealCallWhileAnotherClientProceeds(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")

	poorProvider := &namedProvider{name: "alpha"}
	richProvider := &namedProvider{name: "beta"}

	poor := NewClient("key-poor").WithProviderInstance(poorProvider).WithBudget(1.0)
	rich := NewClient("key-rich").WithProviderInstance(richProvider).WithBudget(1000)

	// Exhaust the poor client's ledger.
	ops.ExecBudget(poor.Context(context.Background())).Record(1.0, true)

	call := func(client *Client) error {
		ctx := client.Context(context.Background())
		opts := ops.NewExtractOptions()
		opts.CommonOptions = opts.CommonOptions.WithContext(ctx)
		_, err := ops.ExtractResult[isolationTarget]("some input", opts)
		return err
	}

	if err := call(poor); err == nil {
		t.Error("the exhausted client's call succeeded; the call path never consulted its budget")
	}
	if got := poorProvider.calls.Load(); got != 0 {
		t.Errorf("the exhausted client contacted its provider %d time(s); the budget is checked before the request, not after", got)
	}

	if err := call(rich); err != nil {
		t.Errorf("the second client was refused: %v", err)
	}
	if got := richProvider.calls.Load(); got == 0 {
		t.Error("the funded client never reached its provider")
	}
}

// --- from client_test.go ---

type stubProvider struct {
	name string
}

func (provider *stubProvider) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{Provider: provider.name}, nil
}

func (provider *stubProvider) Name() string {
	return provider.name
}

func (provider *stubProvider) EstimateCost(llm.CompletionRequest) float64 {
	return 0
}

func (provider *stubProvider) RetryPolicy() (int, time.Duration) {
	return 0, 0
}

func TestWithProviderUsesVendorSpecificEnv(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "deepseek-env-key")

	client := NewClient("")
	client.WithProvider("deepseek")

	if client.provider == nil {
		t.Fatal("expected provider to be configured")
	}
	if client.provider.Name() != "deepseek" {
		t.Fatalf("expected deepseek provider, got %s", client.provider.Name())
	}
}

func TestWithProviderConfigUsesRegisteredFactory(t *testing.T) {
	const providerName = "custom-factory"

	err := llm.RegisterProviderFactory(providerName, func(config llm.ProviderConfig) (llm.Provider, error) {
		return &stubProvider{name: providerName}, nil
	})
	if err != nil {
		t.Fatalf("failed to register provider factory: %v", err)
	}

	client := NewClient("")
	client.WithProviderConfig(providerName, llm.ProviderConfig{})

	if client.provider == nil {
		t.Fatal("expected provider to be configured")
	}
	if client.provider.Name() != providerName {
		t.Fatalf("expected %s provider, got %s", providerName, client.provider.Name())
	}
}

func TestWithProviderInstance(t *testing.T) {
	client := NewClient("")
	client.WithProviderInstance(&stubProvider{name: "instance-provider"})

	if client.provider == nil {
		t.Fatal("expected provider instance to be set")
	}
	if client.provider.Name() != "instance-provider" {
		t.Fatalf("expected instance-provider, got %s", client.provider.Name())
	}
}

func TestRequestTrackingHelpers(t *testing.T) {
	original := GetRequestTrackingConfig()
	t.Cleanup(func() { ConfigureRequestTracking(original) })

	ConfigureRequestTracking(requesttracking.Config{
		Enabled:               true,
		RequestIDStrategy:     requesttracking.IDStrategyUUID,
		CorrelationIDStrategy: requesttracking.CorrelationStrategyGenerate,
		RequestIDHeader:       "X-Test-Request-ID",
		CorrelationIDHeader:   "X-Test-Correlation-ID",
	})

	cfg := GetRequestTrackingConfig()
	if cfg.RequestIDHeader != "X-Test-Request-ID" {
		t.Fatalf("unexpected request tracking config: %#v", cfg)
	}

	ctx := WithRequestID(context.Background(), "req-1")
	ctx = WithCorrelationID(ctx, "corr-1")
	metadata := RequestTrackingFromContext(ctx)
	if metadata.RequestID != "req-1" || metadata.CorrelationID != "corr-1" {
		t.Fatalf("unexpected tracking metadata: %#v", metadata)
	}

	carrier := map[string]string{}
	InjectRequestTracking(ctx, carrier)
	if carrier["X-Test-Request-ID"] != "req-1" || carrier["X-Test-Correlation-ID"] != "corr-1" {
		t.Fatalf("unexpected tracking carrier: %#v", carrier)
	}

	extracted := RequestTrackingFromContext(ExtractRequestTracking(context.Background(), carrier))
	if extracted.RequestID != "req-1" || extracted.CorrelationID != "corr-1" {
		t.Fatalf("unexpected extracted metadata: %#v", extracted)
	}

	client := NewClient("").WithRequestTracking(requesttracking.Config{
		Enabled:               false,
		RequestIDStrategy:     requesttracking.IDStrategyNone,
		CorrelationIDStrategy: requesttracking.CorrelationStrategyNone,
	})
	if client == nil {
		t.Fatal("expected client")
	}
	if GetRequestTrackingConfig().Enabled {
		t.Fatal("expected request tracking to be disabled")
	}
}

// --- from fluent_test.go ---

func TestNewCommonOptionsDefaults(t *testing.T) {
	opts := ops.NewCommonOptions()

	if opts.Mode != types.TransformMode {
		t.Fatalf("expected default mode %v, got %v", types.TransformMode, opts.Mode)
	}
	if opts.Intelligence != types.Fast {
		t.Fatalf("expected default intelligence %v, got %v", types.Fast, opts.Intelligence)
	}
}

func TestCollectionOptionTypesExposeCommonBuilders(t *testing.T) {
	ctx := context.Background()

	chooseOpts := ops.NewChooseOptions().
		WithMode(types.Strict).
		WithIntelligence(types.Smart).
		WithSteering("best fit").
		WithThreshold(0.8).
		WithContext(ctx).
		WithRequestID("choose-1")
	if chooseOpts.CommonOptions.Mode != types.Strict || chooseOpts.CommonOptions.Intelligence != types.Smart || chooseOpts.CommonOptions.Steering != "best fit" || chooseOpts.CommonOptions.Threshold != 0.8 || chooseOpts.CommonOptions.Context != ctx || chooseOpts.CommonOptions.RequestID != "choose-1" {
		t.Fatalf("choose options lost common builder state: %#v", chooseOpts)
	}

	filterOpts := ops.NewFilterOptions().
		WithMode(types.Strict).
		WithIntelligence(types.Quick).
		WithSteering("keep only compliant").
		WithThreshold(0.7).
		WithContext(ctx).
		WithRequestID("filter-1")
	if filterOpts.CommonOptions.Mode != types.Strict || filterOpts.CommonOptions.Intelligence != types.Quick || filterOpts.CommonOptions.Steering != "keep only compliant" || filterOpts.CommonOptions.Threshold != 0.7 || filterOpts.CommonOptions.Context != ctx || filterOpts.CommonOptions.RequestID != "filter-1" {
		t.Fatalf("filter options lost common builder state: %#v", filterOpts)
	}

	sortOpts := ops.NewSortOptions().
		WithMode(types.Strict).
		WithIntelligence(types.Fast).
		WithSteering("latest first").
		WithThreshold(0.6).
		WithContext(ctx).
		WithRequestID("sort-1")
	if sortOpts.CommonOptions.Mode != types.Strict || sortOpts.CommonOptions.Intelligence != types.Fast || sortOpts.CommonOptions.Steering != "latest first" || sortOpts.CommonOptions.Threshold != 0.6 || sortOpts.CommonOptions.Context != ctx || sortOpts.CommonOptions.RequestID != "sort-1" {
		t.Fatalf("sort options lost common builder state: %#v", sortOpts)
	}
}

func nonZeroProviderConfig() llm.ProviderConfig {
	return llm.ProviderConfig{
		APIKey:          "sk-override",
		BaseURL:         "https://override.example.com/v1",
		OrgID:           "org-override",
		Models:          [3]string{"smart-override", "fast-override", "quick-override"},
		Timeout:         11 * time.Second,
		MaxRetries:      7,
		RetryBackoff:    3 * time.Second,
		Debug:           true,
		ExtraHeaders:    map[string]string{"X-Override": "yes"},
		Store:           true,
		OmitTemperature: true,
		ServiceTier:     "priority",
		HTTPClient:      &http.Client{Timeout: 13 * time.Second},
		EndpointPolicy:  &types.EndpointPolicy{},
	}
}

func TestEveryProviderConfigFieldIsPopulatedInTheFixture(t *testing.T) {
	value := reflect.ValueOf(nonZeroProviderConfig())
	structType := value.Type()

	for i := 0; i < value.NumField(); i++ {
		if value.Field(i).IsZero() {
			t.Errorf("nonZeroProviderConfig leaves %s at its zero value, so no override test covers it",
				structType.Field(i).Name)
		}
	}
}

// The regression itself: everything a caller set on the override has to reach
// the config the provider is actually built from.
func TestProviderConfigOverrideCarriesEveryField(t *testing.T) {
	client := NewClient("sk-test")
	override := nonZeroProviderConfig()

	got := client.providerConfig("openai", override)

	inValue := reflect.ValueOf(override)
	outValue := reflect.ValueOf(got)
	structType := inValue.Type()

	for i := 0; i < inValue.NumField(); i++ {
		name := structType.Field(i).Name

		if outValue.Field(i).IsZero() {
			t.Errorf("providerConfig dropped %s entirely", name)
			continue
		}
		// Pointers are carried, not copied -- identity is the right assertion
		// for those, and DeepEqual on a *types.EndpointPolicy with an empty
		// allowlist would pass against a *different* empty policy, which is
		// exactly the substitution this is meant to catch.
		switch inValue.Field(i).Kind() {
		case reflect.Pointer:
			if inValue.Field(i).Pointer() != outValue.Field(i).Pointer() {
				t.Errorf("providerConfig replaced %s with a different value", name)
			}
		default:
			if !reflect.DeepEqual(inValue.Field(i).Interface(), outValue.Field(i).Interface()) {
				t.Errorf("providerConfig changed %s: %v -> %v",
					name, inValue.Field(i).Interface(), outValue.Field(i).Interface())
			}
		}
	}
}

// The specific control that was silently disabled, asserted on its own so a
// failure names the security consequence rather than a field name.
func TestEndpointPolicyReachesTheProviderConfig(t *testing.T) {
	policy := &types.EndpointPolicy{AllowedSchemes: []string{"https"}, AllowedHosts: []string{"api.example.com"}}

	got := NewClient("sk-test").providerConfig("openai", llm.ProviderConfig{EndpointPolicy: policy})

	if got.EndpointPolicy == nil {
		t.Fatal("a caller-supplied EndpointPolicy never reached the provider config; SEC-004's check would run against nil and enforce nothing")
	}
	if got.EndpointPolicy != policy {
		t.Error("the EndpointPolicy that reached the provider config is not the one the caller supplied")
	}
}

// Not passing one must stay unenforced -- the field is opt-in, and a default
// that started refusing endpoints would break every existing caller.
func TestNoEndpointPolicyStaysUnenforced(t *testing.T) {
	got := NewClient("sk-test").providerConfig("openai", llm.ProviderConfig{})

	if got.EndpointPolicy != nil {
		t.Error("a policy appeared without the caller asking for one; EndpointPolicy is opt-in")
	}
}

// An override that mentions nothing must not erase what the client resolved
// for itself -- the other half of a merge, and the half a copy list tends to
// get right only by accident.
func TestAnEmptyOverrideKeepsTheClientsOwnSettings(t *testing.T) {
	client := NewClient("sk-test").WithTimeout(29 * time.Second).WithRetries(5).WithRetryBackoff(2 * time.Second)

	got := client.providerConfig("openai", llm.ProviderConfig{})

	if got.Timeout != 29*time.Second {
		t.Errorf("Timeout = %v; an empty override erased the client's own setting", got.Timeout)
	}
	if got.MaxRetries != 5 {
		t.Errorf("MaxRetries = %d; an empty override erased the client's own setting", got.MaxRetries)
	}
	if got.RetryBackoff != 2*time.Second {
		t.Errorf("RetryBackoff = %v; an empty override erased the client's own setting", got.RetryBackoff)
	}

	// The provider the client holds says the same: that is what a call uses.
	if retries, backoff := client.provider.RetryPolicy(); retries != 5 || backoff != 2*time.Second {
		t.Errorf("the installed provider retries %d times with a backoff of %v, want 5 and 2s", retries, backoff)
	}
}

// ExtraHeaders is copied rather than aliased, so a caller mutating their own
// map after the call does not retroactively change what a provider sends.
func TestExtraHeadersAreCopiedNotAliased(t *testing.T) {
	headers := map[string]string{"X-Tenant": "acme"}

	got := NewClient("sk-test").providerConfig("openai", llm.ProviderConfig{ExtraHeaders: headers})
	headers["X-Tenant"] = "other"

	if got.ExtraHeaders["X-Tenant"] != "acme" {
		t.Error("mutating the caller's header map changed the config after the fact; the map was aliased, not copied")
	}
}

// --- the decision providers ---

// The tables that map a provider's name to its variables name the decision
// providers too. They had no rows, so WithProvider("jev") read only
// SCHEMAFLUX_API_KEY and never TypeSafe's own variable.
func TestProviderVariableTablesNameTheDecisionProviders(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		keys     []string
		urls     []string
	}{
		{"jev", "jev", []string{"SCHEMAFLUX_TYPESAFE_API_KEY", "TYPESAFE_API_KEY"}, []string{"SCHEMAFLUX_TYPESAFE_BASE_URL", "TYPESAFE_BASE_URL"}},
		{"jev in capitals", "JEV", []string{"SCHEMAFLUX_TYPESAFE_API_KEY", "TYPESAFE_API_KEY"}, []string{"SCHEMAFLUX_TYPESAFE_BASE_URL", "TYPESAFE_BASE_URL"}},
		{"jev with space", "  jev ", []string{"SCHEMAFLUX_TYPESAFE_API_KEY", "TYPESAFE_API_KEY"}, []string{"SCHEMAFLUX_TYPESAFE_BASE_URL", "TYPESAFE_BASE_URL"}},
		{"jev-openrouter", "jev-openrouter", []string{"SCHEMAFLUX_OPENROUTER_API_KEY", "OPENROUTER_API_KEY"}, []string{"SCHEMAFLUX_OPENROUTER_BASE_URL", "OPENROUTER_BASE_URL"}},
		{"jev-litellm", "jev-litellm", []string{"SCHEMAFLUX_LITELLM_API_KEY", "LITELLM_API_KEY"}, []string{"SCHEMAFLUX_LITELLM_BASE_URL", "LITELLM_BASE_URL"}},
		{"jev-litellm in capitals", "Jev-LiteLLM", []string{"SCHEMAFLUX_LITELLM_API_KEY", "LITELLM_API_KEY"}, []string{"SCHEMAFLUX_LITELLM_BASE_URL", "LITELLM_BASE_URL"}},
		// What was there is where it was.
		{"openai", "openai", []string{"SCHEMAFLUX_OPENAI_API_KEY", "OPENAI_API_KEY", "OPENAI"}, []string{"SCHEMAFLUX_OPENAI_BASE_URL", "OPENAI_BASE_URL"}},
		{"openrouter", "openrouter", []string{"SCHEMAFLUX_OPENROUTER_API_KEY", "OPENROUTER_API_KEY"}, []string{"SCHEMAFLUX_OPENROUTER_BASE_URL", "OPENROUTER_BASE_URL"}},
		{"cerebras", "cerebras", []string{"SCHEMAFLUX_CEREBRAS_API_KEY", "CEREBRAS_API_KEY", "CEREBRAS"}, []string{"SCHEMAFLUX_CEREBRAS_BASE_URL", "CEREBRAS_BASE_URL"}},
		{"qwen", "qwen", []string{"SCHEMAFLUX_QWEN_API_KEY", "QWEN_API_KEY", "DASHSCOPE_API_KEY"}, []string{"SCHEMAFLUX_QWEN_BASE_URL", "QWEN_BASE_URL", "DASHSCOPE_BASE_URL"}},
		// A name with no row of its own has the shared key and no base URL.
		{"another name", "my-gateway", []string{"SCHEMAFLUX_API_KEY"}, nil},
		{"the name of a decision provider that is not one", "jev-other", []string{"SCHEMAFLUX_API_KEY"}, nil},
		{"no name", "", []string{"SCHEMAFLUX_API_KEY"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := providerAPIKeyEnvVars(tc.provider); !reflect.DeepEqual(got, tc.keys) {
				t.Errorf("providerAPIKeyEnvVars(%q) = %v, want %v", tc.provider, got, tc.keys)
			}
			if got := providerBaseURLEnvVars(tc.provider); !reflect.DeepEqual(got, tc.urls) {
				t.Errorf("providerBaseURLEnvVars(%q) = %v, want %v", tc.provider, got, tc.urls)
			}
		})
	}

	t.Run("a caller cannot change the table by editing what it was handed", func(t *testing.T) {
		keys := providerAPIKeyEnvVars("jev")
		keys[0] = "EDITED"
		urls := providerBaseURLEnvVars("jev")
		urls[0] = "EDITED"
		if got := providerAPIKeyEnvVars("jev")[0]; got != "SCHEMAFLUX_TYPESAFE_API_KEY" {
			t.Errorf("the key table now starts with %q", got)
		}
		if got := providerBaseURLEnvVars("jev")[0]; got != "SCHEMAFLUX_TYPESAFE_BASE_URL" {
			t.Errorf("the base URL table now starts with %q", got)
		}
		if llm.SystemOneTypeSafe.KeyVariables[0] != "SCHEMAFLUX_TYPESAFE_API_KEY" || llm.SystemOneTypeSafe.BaseURLVariables[0] != "SCHEMAFLUX_TYPESAFE_BASE_URL" {
			t.Error("the preset itself was edited through the table")
		}
	})
}

// The environment reader and the model table are in internal/config, which the
// presets' package imports and so cannot read them. This holds the two to each
// other: a preset whose variables or model change fails here until the rows do.
func TestEnvironmentReaderAndModelTableAgreeWithThePresets(t *testing.T) {
	for _, preset := range []llm.SystemOnePreset{llm.SystemOneTypeSafe, llm.SystemOneOpenRouter, llm.SystemOneLiteLLM} {
		t.Run(preset.Name, func(t *testing.T) {
			if got := providerAPIKeyEnvVars(preset.Name); !reflect.DeepEqual(got, preset.KeyVariables) {
				t.Errorf("the client's key variables are %v, the preset's are %v", got, preset.KeyVariables)
			}
			if got := providerBaseURLEnvVars(preset.Name); !reflect.DeepEqual(got, preset.BaseURLVariables) {
				t.Errorf("the client's base URL variables are %v, the preset's are %v", got, preset.BaseURLVariables)
			}

			// Each variable, set on its own, is read as this provider's key or URL, and
			// the first of a row wins.
			for i, name := range preset.KeyVariables {
				env := config.Read(func(variable string) (string, bool) { return "key-from-" + variable, variable == name })
				if got := env.Keys[preset.Name]; got != "key-from-"+name {
					t.Errorf("with only %s set (variable %d of the row) the reader's key is %q", name, i+1, got)
				}
			}
			for _, name := range preset.BaseURLVariables {
				env := config.Read(func(variable string) (string, bool) { return "https://" + variable + ".example.test", variable == name })
				if got := env.BaseURLs[preset.Name]; got != "https://"+name+".example.test" {
					t.Errorf("with only %s set the reader's base URL is %q", name, got)
				}
			}
			all := config.Read(func(variable string) (string, bool) { return "key-from-" + variable, true })
			if got := all.Keys[preset.Name]; got != "key-from-"+preset.KeyVariables[0] {
				t.Errorf("with every variable set the reader's key is %q, want the first of the preset's", got)
			}
			shared := config.Read(func(variable string) (string, bool) { return "shared", variable == "SCHEMAFLUX_API_KEY" })
			if got := shared.Keys[preset.Name]; got != "shared" {
				t.Errorf("with only the shared key set the reader's key is %q", got)
			}

			for _, tier := range []types.Speed{types.Smart, types.Fast, types.Quick} {
				if got := config.BuiltInModel(preset.Name, tier); got != preset.Model {
					t.Errorf("the model table names %q at tier %v, the preset sends %q", got, tier, preset.Model)
				}
			}
			if !config.HasModelMapping(preset.Name) {
				t.Error("the model table has no row")
			}
		})
	}
}

// decisionEndpoint stands in for a System One endpoint on loopback and records
// how it was reached.
type decisionEndpoint struct {
	*httptest.Server
	mu     sync.Mutex
	paths  []string
	auth   []string
	models []string
}

func newDecisionEndpoint(t *testing.T) *decisionEndpoint {
	t.Helper()
	endpoint := &decisionEndpoint{}
	endpoint.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(raw, &body)
		endpoint.mu.Lock()
		endpoint.paths = append(endpoint.paths, r.URL.Path)
		endpoint.auth = append(endpoint.auth, r.Header.Get("Authorization"))
		endpoint.models = append(endpoint.models, body.Model)
		endpoint.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"answers":{"q1":{"type":"noul","noul":0.97}},"model":"jev-1.13.0","usage":{"input_tokens":120,"output_tokens":5}}`)
	}))
	t.Cleanup(endpoint.Close)
	return endpoint
}

// A decision provider used alone needs no WithModel, and reads its own variables
// before the shared one.
func TestADecisionProviderUsedAloneResolvesItsKeyItsURLAndItsModel(t *testing.T) {
	cases := []struct {
		provider  string
		keyVar    string
		urlVar    string
		wantPath  string
		wantModel string
	}{
		{"jev", "TYPESAFE_API_KEY", "TYPESAFE_BASE_URL", "/v1/systemone", "jev-latest"},
		{"jev", "SCHEMAFLUX_TYPESAFE_API_KEY", "SCHEMAFLUX_TYPESAFE_BASE_URL", "/v1/systemone", "jev-latest"},
		{"jev-openrouter", "OPENROUTER_API_KEY", "OPENROUTER_BASE_URL", "/v1/systemone", "~typesafe/jev-latest"},
		{"jev-litellm", "LITELLM_API_KEY", "LITELLM_BASE_URL", "/typesafe/v1/systemone", "jev-latest"},
	}
	for _, tc := range cases {
		t.Run(tc.provider+" from "+tc.keyVar, func(t *testing.T) {
			clearModelOverrides(t)
			endpoint := newDecisionEndpoint(t)
			configtest.Setenv(t, "SCHEMAFLUX_API_KEY", "shared-key-that-must-not-win")
			configtest.Setenv(t, tc.keyVar, "the-providers-own-key")
			configtest.Setenv(t, tc.urlVar, endpoint.URL)

			client := NewClient("").WithProvider(tc.provider)
			if err := client.Err(); err != nil {
				t.Fatalf("Err() = %v: a decision provider has a model of its own", err)
			}
			ctx := client.Context(context.Background())
			got, err := ops.Is(ctx, "Please refund my order.", "Is the customer asking for a refund?")
			if err != nil || !got {
				t.Fatalf("Is = %v, %v, want true from the decider", got, err)
			}

			endpoint.mu.Lock()
			defer endpoint.mu.Unlock()
			if len(endpoint.paths) != 1 {
				t.Fatalf("%d requests reached the endpoint, want one", len(endpoint.paths))
			}
			if endpoint.paths[0] != tc.wantPath || endpoint.models[0] != tc.wantModel {
				t.Errorf("the request went to %s asking for %q, want %s and %q", endpoint.paths[0], endpoint.models[0], tc.wantPath, tc.wantModel)
			}
			if endpoint.auth[0] != "Bearer the-providers-own-key" {
				t.Errorf("the request carried a key that is not the provider's own")
			}
		})
	}

	t.Run("the shared key serves when the provider's own is not set", func(t *testing.T) {
		clearModelOverrides(t)
		endpoint := newDecisionEndpoint(t)
		configtest.Setenv(t, "SCHEMAFLUX_API_KEY", "the-shared-key")
		for _, name := range []string{"SCHEMAFLUX_TYPESAFE_API_KEY", "TYPESAFE_API_KEY"} {
			configtest.Setenv(t, name, "")
		}
		configtest.Setenv(t, "TYPESAFE_BASE_URL", endpoint.URL)

		client := NewClient("").WithProvider("jev")
		if _, err := ops.Is(client.Context(context.Background()), "Please refund my order.", "Is the customer asking for a refund?"); err != nil {
			t.Fatalf("Is: %v", err)
		}
		endpoint.mu.Lock()
		defer endpoint.mu.Unlock()
		if len(endpoint.auth) != 1 || endpoint.auth[0] != "Bearer the-shared-key" {
			t.Errorf("the request did not carry the shared key")
		}
	})
}
