package client

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/config/configtest"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/telemetry"
	"github.com/monstercameron/schemaflux/internal/types"
)

// Err reports the client as it is now. A provider with no model mapping stops
// being a problem when the caller names a model, in either order, and used to
// stay one: WithProviderConfig recorded the failure and nothing cleared it.
func TestErrIsNilOnceTheCallerHasDoneWhatTheErrorAsked(t *testing.T) {
	cases := []struct {
		name  string
		build func() *Client
		want  bool // whether Err is expected to be non-nil
	}{
		{"an unmapped provider", func() *Client { return NewClient("k").WithProvider("qwen") }, true},
		{"then a model", func() *Client { return NewClient("k").WithProvider("qwen").WithModel("my-model") }, false},
		{"a model first", func() *Client { return NewClient("k").WithModel("my-model").WithProvider("qwen") }, false},
		{"a model, then none", func() *Client { return NewClient("k").WithProvider("qwen").WithModel("m").WithModel("") }, true},
		{"a blank model", func() *Client { return NewClient("k").WithProvider("qwen").WithModel("   ") }, true},
		{"a mapped provider", func() *Client { return NewClient("k").WithProvider("anthropic") }, false},
		{"another unmapped one", func() *Client { return NewClient("k").WithProvider("zai") }, true},
		{"then a mapped one", func() *Client { return NewClient("k").WithProvider("zai").WithProvider("deepseek") }, false},
		{"then retuned", func() *Client { return NewClient("k").WithProvider("qwen").WithRetries(1) }, true},
		{"a model, then retuned", func() *Client { return NewClient("k").WithProvider("qwen").WithModel("m").WithRetries(1) }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearModelOverrides(t)
			err := tc.build().Err()
			if (err != nil) != tc.want {
				t.Fatalf("Err() = %v, want an error: %v", err, tc.want)
			}
			if err != nil && (!errors.Is(err, config.ErrNoModelMapping) || types.KindOf(err) != types.KindConfiguration) {
				t.Errorf("the error is not the classified no-model error: kind %v, %v", types.KindOf(err), err)
			}
		})
	}
}

// The environment is the other way out, and it is read when Err is asked, so a
// variable set after the provider was chosen counts.
func TestErrReadsTheModelEnvironmentAtTheTimeItIsAsked(t *testing.T) {
	clearModelOverrides(t)
	client := NewClient("k").WithProvider("qwen")
	if client.Err() == nil {
		t.Fatal("expected an error with no model")
	}
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "from-the-environment")
	if err := client.Err(); err != nil {
		t.Errorf("Err() = %v after SCHEMAFLUX_MODEL named a model", err)
	}
}

// A failed provider switch is still the previous provider's state until a switch
// succeeds.
func TestAFailedSwitchStaysReportedUntilASwitchSucceeds(t *testing.T) {
	clearCredentialEnv(t)
	clearModelOverrides(t)
	client := NewClient("").WithMockProvider().WithProvider("openai")
	if client.Err() == nil {
		t.Fatal("expected a failure without a key")
	}
	client.WithModel("m").WithRetries(2)
	if client.Err() == nil {
		t.Error("an unrelated change cleared the failure of the switch")
	}
	client.WithProviderConfig("openai", llm.ProviderConfig{APIKey: "k"})
	if err := client.Err(); err != nil {
		t.Errorf("Err() = %v after a successful switch", err)
	}
}

// The zero Client works: its methods do not panic on a missing logger.
func TestTheZeroClientIsUsable(t *testing.T) {
	t.Cleanup(func() { ops.SetDefaultProvider(nil) })
	clearCredentialEnv(t)

	cases := map[string]func(c *Client){
		"WithModel":                 func(c *Client) { c.WithModel("m") },
		"WithEffort":                func(c *Client) { c.WithEffort(types.EffortLow) },
		"WithBudget":                func(c *Client) { c.WithBudget(5) },
		"WithProviderInstance":      func(c *Client) { c.WithProviderInstance(&stubProvider{name: "x"}) },
		"WithProviderInstance(nil)": func(c *Client) { c.WithProviderInstance(nil) },
		"WithMockProvider":          func(c *Client) { c.WithMockProvider() },
		"WithProvider":              func(c *Client) { c.WithProvider("local") },
		"a failing WithProvider":    func(c *Client) { c.WithProvider("no-such-provider") },
		"WithProviderConfig":        func(c *Client) { c.WithProviderConfig("openai", llm.ProviderConfig{APIKey: "k"}) },
		"WithDebug on":              func(c *Client) { c.WithDebug(true); c.WithDebug(false) },
		"WithTimeout":               func(c *Client) { c.WithTimeout(time.Second) },
		"WithRetries":               func(c *Client) { c.WithRetries(1) },
		"WithRetryBackoff":          func(c *Client) { c.WithRetryBackoff(time.Second) },
		"Err":                       func(c *Client) { _ = c.Err() },
		"Spent":                     func(c *Client) { _, _ = c.Spent() },
		"Context":                   func(c *Client) { _ = c.Context(context.Background()) },
		"Warm":                      func(c *Client) { _, _ = c.Warm(context.Background()) },
	}
	for name, use := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("%s panicked on the zero Client: %v", name, recovered)
				}
			}()
			use(&Client{})
		})
	}

	// A zero client given a provider builds it with the defaults a constructed
	// client has, and not with a zero timeout, which means none.
	client := (&Client{}).WithProviderConfig("openai", llm.ProviderConfig{APIKey: "k"})
	if retries, backoff := client.provider.RetryPolicy(); retries != 3 || backoff != time.Second {
		t.Errorf("a zero client's provider has retries %d and backoff %v, want 3 and 1s", retries, backoff)
	}
	built := client.providerConfig("openai", llm.ProviderConfig{})
	if built.Timeout != 30*time.Second {
		t.Errorf("a zero client's provider timeout is %v, want 30s", built.Timeout)
	}
	if retries := (&Client{}).WithRetries(0).providerConfig("openai", llm.ProviderConfig{}).MaxRetries; retries != 0 {
		t.Errorf("WithRetries(0) on a zero client gave %d retries", retries)
	}
}

// WithDebug raises the shared logger while any client has debug on, and puts the
// level back when the last one turns it off, in whatever order they do it.
func TestWithDebugLeavesTheSharedLoggerWhereItWasInAnyOrder(t *testing.T) {
	orders := map[string][]string{
		"on a, on b, off a, off b": {"a+", "b+", "a-", "b-"},
		"on a, on b, off b, off a": {"a+", "b+", "b-", "a-"},
		"on a, off a, on b, off b": {"a+", "a-", "b+", "b-"},
		"on twice, off twice":      {"a+", "a+", "a-", "a-"},
		"off before on":            {"a-", "a+", "a-"},
		"b on, a off, b off":       {"b+", "a-", "b-"},
		"interleaved":              {"a+", "b+", "a-", "a+", "b-", "a-"},
	}
	for name, order := range orders {
		t.Run(name, func(t *testing.T) {
			logger := telemetry.GetLogger()
			original := logger.Level()
			t.Cleanup(func() { logger.SetLevel(original) })
			logger.SetLevel(telemetry.WarnLevel)

			clients := map[string]*Client{"a": NewClient("a"), "b": NewClient("b")}
			held := map[string]bool{}
			for _, step := range order {
				client := clients[step[:1]]
				on := step[1] == '+'
				client.WithDebug(on)
				held[step[:1]] = on

				anyHeld := held["a"] || held["b"]
				if got := logger.Level(); anyHeld && got != telemetry.DebugLevel {
					t.Fatalf("after %q the level is %v with debug held, want debug", step, got)
				}
				if got := logger.Level(); !anyHeld && got != telemetry.WarnLevel {
					t.Fatalf("after %q the level is %v with debug released by everyone, want the original warn", step, got)
				}
			}
		})
	}
}

func TestALevelSetWhileDebugIsOnIsNotOverwrittenWhenDebugEnds(t *testing.T) {
	logger := telemetry.GetLogger()
	original := logger.Level()
	t.Cleanup(func() { logger.SetLevel(original) })
	logger.SetLevel(telemetry.InfoLevel)

	client := NewClient("k").WithDebug(true)
	logger.SetLevel(telemetry.ErrorLevel)
	client.WithDebug(false)
	if got := logger.Level(); got != telemetry.ErrorLevel {
		t.Errorf("level = %v; the level somebody set while debug was on was overwritten", got)
	}
}

func TestWithDebugIsSafeUnderConcurrentUse(t *testing.T) {
	logger := telemetry.GetLogger()
	original := logger.Level()
	t.Cleanup(func() { logger.SetLevel(original) })
	logger.SetLevel(telemetry.InfoLevel)

	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := NewClient("k")
			for i := 0; i < 50; i++ {
				client.WithDebug(true)
				client.WithDebug(false)
			}
		}()
	}
	wg.Wait()
	if got := logger.Level(); got != telemetry.InfoLevel {
		t.Errorf("level = %v after every client turned debug off, want info", got)
	}
}

// A caller who edits the endpoint policy they passed after the call must not
// change the policy a live client enforces, including the one a later retune
// builds the provider with.
func TestTheEndpointPolicyTheClientHoldsIsACopy(t *testing.T) {
	server, _ := tuningServer(t, 0)
	tuningEnvironment(t, server.URL)
	policy := &types.EndpointPolicy{
		AllowedSchemes:        []string{"http"},
		AllowedHosts:          []string{"127.0.0.1"},
		AllowPrivateAddresses: true,
	}
	client := NewClient("k").WithProviderConfig("openai", llm.ProviderConfig{BaseURL: server.URL, EndpointPolicy: policy})
	if err := client.Err(); err != nil {
		t.Fatalf("the policy refused the local server: %v", err)
	}

	policy.AllowedHosts[0] = "elsewhere.example"
	policy.AllowedSchemes = []string{"ftp"}

	client.WithRetries(0)
	if err := client.Err(); err != nil {
		t.Errorf("a change to the caller's policy reached the client: %v", err)
	}
}

func TestSettingsAreOnlyHeldWhileTheyAreValid(t *testing.T) {
	tuningEnvironment(t, "http://127.0.0.1:1")
	client := NewClient("k").WithRetryBackoff(30 * time.Millisecond)
	client.WithRetryBackoff(0)
	client.WithRetryBackoff(-time.Second)
	if _, backoff := client.provider.RetryPolicy(); backoff != 30*time.Millisecond {
		t.Errorf("backoff = %v after refused values, want the 30ms kept", backoff)
	}
	if err := client.Err(); err != nil {
		t.Errorf("Err() = %v; a refused backoff is documented as ignored", err)
	}
}

// A timeout the environment names that cannot be used is refused where the
// program is configured, naming the variable and not its value. It used to be
// accepted, and every call that carried no deadline of its own then failed at
// once.
func TestInitRefusesAnUnusableTimeoutFromTheEnvironment(t *testing.T) {
	for _, raw := range []string{"0s", "0", "-1s", "-5m", "0ms", "soon", "sk-looks-like-a-key"} {
		t.Run(raw, func(t *testing.T) {
			clearCredentialEnv(t)
			t.Setenv("SCHEMAFLUX_API_KEY", "k")
			configtest.Setenv(t, "SCHEMAFLUX_TIMEOUT", raw)
			before := GetDefaultClient()

			err := Init("")
			if err == nil {
				t.Fatalf("Init accepted SCHEMAFLUX_TIMEOUT=%q", raw)
			}
			if !strings.Contains(err.Error(), "SCHEMAFLUX_TIMEOUT") {
				t.Errorf("the error does not name the variable: %v", err)
			}
			if len(raw) > 3 && strings.Contains(err.Error(), raw) {
				t.Errorf("the error repeats the value: %v", err)
			}
			if types.KindOf(err) != types.KindConfiguration {
				t.Errorf("the error is not a configuration error: %v", err)
			}
			if GetDefaultClient() != before {
				t.Error("a refused Init replaced the default client")
			}
		})
	}
}

func TestInitAppliesAValidTimeoutFromTheEnvironment(t *testing.T) {
	for raw, want := range map[string]time.Duration{"45s": 45 * time.Second, "2m": 2 * time.Minute, "150ms": 150 * time.Millisecond, "": 30 * time.Second} {
		t.Run(raw, func(t *testing.T) {
			clearCredentialEnv(t)
			t.Setenv("SCHEMAFLUX_API_KEY", "k")
			configtest.Setenv(t, "SCHEMAFLUX_TIMEOUT", raw)
			if err := Init(""); err != nil {
				t.Fatalf("Init: %v", err)
			}
			if got := GetDefaultClient().timeout; got != want {
				t.Errorf("the default client's timeout is %v, want %v", got, want)
			}
		})
	}
}

// SCHEMAFLUX_DEBUG has the one reading everywhere, config.EnvBool's: Init does
// not accept a spelling the operations' debug branch does not, and "1" is one
// both accept.
func TestInitReadsSchemafluxDebugLikeEveryOtherReader(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{"true", true}, {"false", false}, {"", false}, {"1", true}, {"0", false}, {"T", true},
		{"TRUE", true}, {"f", false}, {"yes", false}, {"on", false}, {"tRuE", false},
	} {
		t.Run("["+tc.raw+"]", func(t *testing.T) {
			clearCredentialEnv(t)
			var problems []config.EnvProblem
			t.Cleanup(config.SetEnvProblemReporter(func(problem config.EnvProblem) { problems = append(problems, problem) }))
			logger := telemetry.GetLogger()
			original := logger.Level()
			t.Cleanup(func() { logger.SetLevel(original) })
			logger.SetLevel(telemetry.InfoLevel)
			t.Setenv("SCHEMAFLUX_API_KEY", "k")
			configtest.Setenv(t, "SCHEMAFLUX_DEBUG", tc.raw)

			if err := Init(""); err != nil {
				t.Fatalf("Init: %v", err)
			}
			if got := GetDefaultClient().debugMode; got != tc.want {
				t.Errorf("debugMode = %v with SCHEMAFLUX_DEBUG=%q, want %v", got, tc.raw, tc.want)
			}
			if got := config.GetDebugMode(); got != tc.want {
				t.Errorf("config.GetDebugMode() = %v, but Init read %v: they must agree", got, tc.want)
			}
			if (logger.Level() == telemetry.DebugLevel) != tc.want {
				t.Errorf("the logger level is %v with debug %v", logger.Level(), tc.want)
			}
			if tc.raw == "yes" || tc.raw == "on" || tc.raw == "tRuE" {
				if len(problems) == 0 || problems[0].Variable != "SCHEMAFLUX_DEBUG" {
					t.Errorf("the malformed value was not reported by name: %+v", problems)
				}
			}
			GetDefaultClient().WithDebug(false)
		})
	}
}

// The library keeps what it read from the environment (config.ReloadEnvironment),
// so a program that used it once, then set a variable and initialised it, got the
// value it had read before. Init is the call that says "configure from the
// environment now", and it has to read the environment again.
func TestInitSeesAVariableSetAfterTheLibraryWasFirstUsed(t *testing.T) {
	clearCredentialEnv(t)
	logger := telemetry.GetLogger()
	original := logger.Level()
	t.Cleanup(func() { logger.SetLevel(original) })
	t.Cleanup(config.ReloadEnvironment)
	t.Setenv("SCHEMAFLUX_API_KEY", "k")

	config.ReloadEnvironment()
	if config.GetDebugMode() {
		t.Fatal("debug mode is on before anything set it")
	}

	// Set directly and not through configtest, which would reload: the point is
	// that Init is what makes the library read it.
	t.Setenv("SCHEMAFLUX_DEBUG", "true")
	if err := Init(""); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { GetDefaultClient().WithDebug(false) })
	if !GetDefaultClient().debugMode {
		t.Error("Init kept the debug setting read before SCHEMAFLUX_DEBUG was set")
	}
	if !config.GetDebugMode() {
		t.Error("config.GetDebugMode() still reports the value read before Init")
	}
}

// InitWithEnv loads a file that sets the variables and then initialises, which
// is the case most likely to follow earlier use of the library in the process.
func TestInitWithEnvSeesTheVariablesOfTheFileItLoads(t *testing.T) {
	clearCredentialEnv(t)
	logger := telemetry.GetLogger()
	original := logger.Level()
	t.Cleanup(func() { logger.SetLevel(original) })
	t.Cleanup(config.ReloadEnvironment)

	config.ReloadEnvironment()
	if config.GetDebugMode() {
		t.Fatal("debug mode is on before anything set it")
	}

	path := filepath.Join(t.TempDir(), "test.env")
	if err := os.WriteFile(path, []byte("SCHEMAFLUX_API_KEY=k\nSCHEMAFLUX_DEBUG=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// godotenv sets these in the process; put back what was there.
	t.Setenv("SCHEMAFLUX_API_KEY", "")
	os.Unsetenv("SCHEMAFLUX_API_KEY")
	if err := InitWithEnv(path); err != nil {
		t.Fatalf("InitWithEnv: %v", err)
	}
	t.Cleanup(func() { GetDefaultClient().WithDebug(false) })
	if !GetDefaultClient().debugMode {
		t.Error("InitWithEnv kept the debug setting read before the file was loaded")
	}
}

// Init replaces the default client, and a client nobody can reach cannot turn
// debug off. The one it replaces gives up its hold on the shared logger, so
// initialising again with debug off puts the level back.
func TestInitReleasesTheDebugHoldOfTheClientItReplaces(t *testing.T) {
	clearCredentialEnv(t)
	logger := telemetry.GetLogger()
	original := logger.Level()
	t.Cleanup(func() { logger.SetLevel(original) })
	logger.SetLevel(telemetry.WarnLevel)
	t.Setenv("SCHEMAFLUX_API_KEY", "k")

	configtest.Setenv(t, "SCHEMAFLUX_DEBUG", "true")
	for i := 0; i < 3; i++ {
		if err := Init(""); err != nil {
			t.Fatalf("Init: %v", err)
		}
		if logger.Level() != telemetry.DebugLevel {
			t.Fatalf("Init with SCHEMAFLUX_DEBUG=true left the level at %v", logger.Level())
		}
	}

	configtest.Setenv(t, "SCHEMAFLUX_DEBUG", "false")
	if err := Init(""); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if got := logger.Level(); got != telemetry.WarnLevel {
		t.Errorf("after the last Init turned debug off the level is %v, want the original warn: a replaced client kept its hold", got)
	}
}

// NewClient has a provider to build, and a cost variable that is not a price makes
// that fail. The failure is kept and reported by Err, and a provider handed in
// afterwards clears it.
func TestNewClientKeepsAProviderBuildFailureAndAnInstanceClearsIt(t *testing.T) {
	clearCredentialEnv(t)
	clearModelOverrides(t)
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
	t.Cleanup(func() { ops.SetDefaultProvider(nil) })
	t.Setenv("SCHEMAFLUX_COST_INPUT_FAKE_MODEL", "NaN")

	client := NewClient("k")
	if err := client.Err(); err == nil || types.KindOf(err) != types.KindConfiguration {
		t.Fatalf("Err() = %v, want a configuration error for a provider that could not be built", err)
	}

	local, err := llm.NewLocalProvider(llm.ProviderConfig{})
	if err != nil {
		t.Fatal(err)
	}
	client.WithProviderInstance(local)
	if err := client.Err(); err != nil {
		t.Errorf("Err() = %v after a working provider was installed, want nil", err)
	}
}

// The provider name is kept in its one spelling.
func TestWithProviderConfigKeepsTheNormalisedProviderName(t *testing.T) {
	clearCredentialEnv(t)
	clearModelOverrides(t)
	t.Cleanup(func() { ops.SetDefaultProvider(nil) })

	client := (&Client{}).WithProviderConfig("  OpenAI ", llm.ProviderConfig{APIKey: "k"})
	if client.providerName != "openai" {
		t.Errorf("providerName = %q, want %q", client.providerName, "openai")
	}
	if client.spec == nil || client.spec.name != "openai" {
		t.Errorf("the remembered request names %+v, want openai", client.spec)
	}
}

// SCHEMAFLUX_PROVIDER names the mock in whatever spelling a person types.
func TestInitReadsTheMockProviderNameWithoutRegardToCaseOrSpace(t *testing.T) {
	clearCredentialEnv(t)
	clearModelOverrides(t)
	t.Setenv("SCHEMAFLUX_PROVIDER", "  Local ")
	t.Cleanup(func() { ops.SetDefaultProvider(nil) })

	if err := Init(""); err != nil {
		t.Errorf("Init with SCHEMAFLUX_PROVIDER=\"  Local \" and no key = %v, want the mock chosen", err)
	}
}

func TestResolveProviderAPIKeyOrder(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		explicit string
		env      map[string]string
		want     string
	}{
		{"an explicit key and nothing else", "openai", "arg", nil, "arg"},
		{"an explicit key beats every openai variable", "openai", "arg", map[string]string{"SCHEMAFLUX_OPENAI_API_KEY": "a", "OPENAI_API_KEY": "b", "OPENAI": "c", "SCHEMAFLUX_API_KEY": "d"}, "arg"},
		{"an explicit key beats the generic variable for another provider", "anthropic", "arg", map[string]string{"SCHEMAFLUX_API_KEY": "d", "ANTHROPIC_API_KEY": "e"}, "arg"},
		{"the provider's first variable", "openai", "", map[string]string{"SCHEMAFLUX_OPENAI_API_KEY": "a", "OPENAI_API_KEY": "b", "OPENAI": "c", "SCHEMAFLUX_API_KEY": "d"}, "a"},
		{"the provider's second variable", "openai", "", map[string]string{"OPENAI_API_KEY": "b", "OPENAI": "c", "SCHEMAFLUX_API_KEY": "d"}, "b"},
		{"the bare variable beats the generic one", "openai", "", map[string]string{"OPENAI": "c", "SCHEMAFLUX_API_KEY": "d"}, "c"},
		{"the generic variable last", "openai", "", map[string]string{"SCHEMAFLUX_API_KEY": "d"}, "d"},
		{"another provider's variable is not this provider's", "anthropic", "", map[string]string{"OPENAI_API_KEY": "b"}, ""},
		{"a provider with only the generic variable", "my-gateway", "", map[string]string{"SCHEMAFLUX_API_KEY": "d"}, "d"},
		{"qwen takes the dashscope name", "qwen", "", map[string]string{"DASHSCOPE_API_KEY": "q"}, "q"},
		{"zai takes the glm name", "zai", "", map[string]string{"GLM_API_KEY": "z"}, "z"},
		{"a provider named with padding and capitals still finds its own variable", "  OpenAI ", "", map[string]string{"OPENAI_API_KEY": "b", "SCHEMAFLUX_API_KEY": "d"}, "b"},
		{"an upper-case provider name finds its own variable", "ANTHROPIC", "", map[string]string{"ANTHROPIC_API_KEY": "e", "SCHEMAFLUX_API_KEY": "d"}, "e"},
		{"nothing at all", "openai", "", nil, ""},
		{"no explicit key falls back to the environment", "openai", "", map[string]string{"OPENAI_API_KEY": "b"}, "b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearCredentialEnv(t)
			for _, name := range []string{"ANTHROPIC_API_KEY", "SCHEMAFLUX_ANTHROPIC_API_KEY", "QWEN_API_KEY", "DASHSCOPE_API_KEY", "ZAI_API_KEY", "GLM_API_KEY"} {
				t.Setenv(name, "")
			}
			for name, value := range tc.env {
				t.Setenv(name, value)
			}
			if got := resolveProviderAPIKey(tc.provider, tc.explicit); got != tc.want {
				t.Errorf("resolveProviderAPIKey(%q, %q) = %q, want %q", tc.provider, tc.explicit, got, tc.want)
			}
		})
	}
}

// A client given no settings builds its provider with the defaults a constructed
// client has, and a client given settings builds it with those: the zero timeout
// and the zero backoff, which would mean no timeout and a retry storm, are never
// what reaches the provider.
func TestProviderConfigFromTheClientsSettings(t *testing.T) {
	cases := []struct {
		name        string
		client      *Client
		wantTimeout time.Duration
		wantRetries int
		wantBackoff time.Duration
	}{
		{"a zero client", &Client{}, 30 * time.Second, 3, time.Second},
		{"a constructed client", newClient("k"), 30 * time.Second, 3, time.Second},
		{"a zero client with retries off", (&Client{}).WithRetries(0), 30 * time.Second, 0, time.Second},
		{"a zero client with two retries", (&Client{}).WithRetries(2), 30 * time.Second, 2, time.Second},
		{"a zero client with a timeout", (&Client{}).WithTimeout(5 * time.Second), 5 * time.Second, 3, time.Second},
		{"a zero client with a backoff", (&Client{}).WithRetryBackoff(40 * time.Millisecond), 30 * time.Second, 3, 40 * time.Millisecond},
		{"every setting", (&Client{}).WithTimeout(7 * time.Second).WithRetries(1).WithRetryBackoff(9 * time.Millisecond), 7 * time.Second, 1, 9 * time.Millisecond},
		{"a negative retry count", (&Client{}).WithRetries(-4), 30 * time.Second, 0, time.Second},
		{"a refused timeout leaves the default", (&Client{}).WithTimeout(0), 30 * time.Second, 3, time.Second},
		{"a refused backoff leaves the default", (&Client{}).WithRetryBackoff(-time.Second), 30 * time.Second, 3, time.Second},
		{"settings given twice, the last wins", newClient("k").WithRetries(5).WithRetries(2).WithTimeout(time.Second).WithTimeout(2 * time.Second), 2 * time.Second, 2, time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.client.providerConfig("openai", llm.ProviderConfig{})
			if got.Timeout != tc.wantTimeout || got.MaxRetries != tc.wantRetries || got.RetryBackoff != tc.wantBackoff {
				t.Errorf("timeout %v, retries %d, backoff %v; want %v, %d, %v",
					got.Timeout, got.MaxRetries, got.RetryBackoff, tc.wantTimeout, tc.wantRetries, tc.wantBackoff)
			}
		})
	}
}

// What a client remembers of a provider config is a copy of what the caller can
// still change: the header map and the endpoint policy with its lists.
func TestCloneProviderConfigCopiesWhatTheCallerCanStillChange(t *testing.T) {
	cases := []struct {
		name   string
		config func() llm.ProviderConfig
	}{
		{"nothing", func() llm.ProviderConfig { return llm.ProviderConfig{} }},
		{"headers only", func() llm.ProviderConfig {
			return llm.ProviderConfig{ExtraHeaders: map[string]string{"X-A": "1"}}
		}},
		{"many headers", func() llm.ProviderConfig {
			return llm.ProviderConfig{ExtraHeaders: map[string]string{"X-A": "1", "X-B": "2", "X-C": "3"}}
		}},
		{"an empty header map", func() llm.ProviderConfig { return llm.ProviderConfig{ExtraHeaders: map[string]string{}} }},
		{"a policy with lists", func() llm.ProviderConfig {
			return llm.ProviderConfig{EndpointPolicy: &types.EndpointPolicy{AllowedSchemes: []string{"https"}, AllowedHosts: []string{"a.example", "b.example"}}}
		}},
		{"a policy with no lists", func() llm.ProviderConfig { return llm.ProviderConfig{EndpointPolicy: &types.EndpointPolicy{}} }},
		{"a policy that allows private addresses", func() llm.ProviderConfig {
			return llm.ProviderConfig{EndpointPolicy: &types.EndpointPolicy{AllowedSchemes: []string{"http"}, AllowedHosts: []string{"127.0.0.1"}, AllowPrivateAddresses: true}}
		}},
		{"headers and a policy", func() llm.ProviderConfig {
			return llm.ProviderConfig{ExtraHeaders: map[string]string{"X-A": "1"}, EndpointPolicy: &types.EndpointPolicy{AllowedSchemes: []string{"https"}, AllowedHosts: []string{"a.example"}}}
		}},
		{"scalars", func() llm.ProviderConfig {
			return llm.ProviderConfig{APIKey: "k", BaseURL: "https://x.example", OrgID: "o", Timeout: time.Second, MaxRetries: 2, Store: true}
		}},
		{"a callers http client", func() llm.ProviderConfig { return llm.ProviderConfig{HTTPClient: &http.Client{Timeout: time.Second}} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			original := tc.config()
			pristine := tc.config()
			copied := cloneProviderConfig(original)

			if copied.APIKey != original.APIKey || copied.BaseURL != original.BaseURL || copied.Timeout != original.Timeout ||
				copied.MaxRetries != original.MaxRetries || copied.Store != original.Store || copied.HTTPClient != original.HTTPClient {
				t.Errorf("a scalar or the caller's client changed: %+v -> %+v", original, copied)
			}
			if len(pristine.ExtraHeaders) > 0 && !reflect.DeepEqual(copied.ExtraHeaders, pristine.ExtraHeaders) {
				t.Errorf("the headers changed in the copy: %v -> %v", pristine.ExtraHeaders, copied.ExtraHeaders)
			}
			if pristine.EndpointPolicy != nil {
				if copied.EndpointPolicy == original.EndpointPolicy {
					t.Error("the endpoint policy is the caller's, not a copy")
				}
				if copied.EndpointPolicy.AllowPrivateAddresses != pristine.EndpointPolicy.AllowPrivateAddresses ||
					!reflect.DeepEqual(append([]string(nil), copied.EndpointPolicy.AllowedHosts...), append([]string(nil), pristine.EndpointPolicy.AllowedHosts...)) ||
					!reflect.DeepEqual(append([]string(nil), copied.EndpointPolicy.AllowedSchemes...), append([]string(nil), pristine.EndpointPolicy.AllowedSchemes...)) {
					t.Errorf("the policy changed in the copy: %+v -> %+v", *pristine.EndpointPolicy, *copied.EndpointPolicy)
				}
			}

			// Now the caller edits what they passed.
			if original.EndpointPolicy != nil {
				for i := range original.EndpointPolicy.AllowedHosts {
					original.EndpointPolicy.AllowedHosts[i] = "changed.example"
				}
				for i := range original.EndpointPolicy.AllowedSchemes {
					original.EndpointPolicy.AllowedSchemes[i] = "changed"
				}
				if !reflect.DeepEqual(append([]string(nil), copied.EndpointPolicy.AllowedHosts...), append([]string(nil), pristine.EndpointPolicy.AllowedHosts...)) ||
					!reflect.DeepEqual(append([]string(nil), copied.EndpointPolicy.AllowedSchemes...), append([]string(nil), pristine.EndpointPolicy.AllowedSchemes...)) {
					t.Errorf("editing the caller's lists reached the copy: %+v", *copied.EndpointPolicy)
				}
			}
			for name := range original.ExtraHeaders {
				original.ExtraHeaders[name] = "changed"
			}
			if len(pristine.ExtraHeaders) > 0 && !reflect.DeepEqual(copied.ExtraHeaders, pristine.ExtraHeaders) {
				t.Errorf("editing the caller's headers reached the copy: %v", copied.ExtraHeaders)
			}
		})
	}
}
