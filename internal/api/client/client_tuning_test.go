package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/config/configtest"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
)

// The settings of WithTimeout, WithRetries, WithRetryBackoff and WithDebug were
// stored on the client and read again only when a provider was next built, so
// NewClient("k").WithRetries(0) left the provider NewClient had already built at
// three retries. The tests here go through the client: a local server counts the
// attempts that reach it and the time they take, and the provider the client
// installed is asked what it will do.

type tuningTarget struct {
	Name string `json:"name"`
}

// tuningServer answers every request with a 500, counts them, and holds each one
// for hold (or until the test ends) before it answers.
func tuningServer(t *testing.T, hold time.Duration) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if hold > 0 {
			select {
			case <-time.After(hold):
			case <-release:
				return
			}
		}
		http.Error(w, `{"error":{"message":"unavailable"}}`, http.StatusInternalServerError)
	}))
	// Cleanups run last in, first out: held requests are let go before the
	// server waits for them.
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	return server, &hits
}

// tuningEnvironment points a client built with only a key at the server, with
// nothing else in the environment able to change the answer.
func tuningEnvironment(t *testing.T, baseURL string) {
	t.Helper()
	clearCredentialEnv(t)
	clearModelOverrides(t)
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
	t.Setenv("SCHEMAFLUX_OPENAI_BASE_URL", "")
	t.Setenv("OPENAI_BASE_URL", baseURL)
	t.Cleanup(func() { ops.SetDefaultProvider(nil) })
}

// extractThrough makes one call on the client's own context and reports how long
// it took and what it returned.
func extractThrough(client *Client) (time.Duration, error) {
	opts := ops.NewExtractOptions()
	opts.CommonOptions = opts.CommonOptions.WithContext(client.Context(context.Background()))
	start := time.Now()
	_, err := ops.ExtractResult[tuningTarget]("some input", opts)
	return time.Since(start), err
}

func TestWithRetriesChangesTheAttemptsTheProviderMakes(t *testing.T) {
	cases := []struct {
		name  string
		build func(url string) *Client
		want  int64
	}{
		{"retries off, after NewClient", func(string) *Client {
			return NewClient("k").WithRetryBackoff(time.Millisecond).WithRetries(0)
		}, 1},
		{"retries off, before the provider is chosen again", func(string) *Client {
			return NewClient("k").WithRetryBackoff(time.Millisecond).WithRetries(0).WithProvider("openai")
		}, 1},
		{"retries off, after the provider is chosen again", func(string) *Client {
			return NewClient("k").WithRetryBackoff(time.Millisecond).WithProvider("openai").WithRetries(0)
		}, 1},
		{"retries off, after a config with its own server", func(url string) *Client {
			return NewClient("k").WithRetryBackoff(time.Millisecond).
				WithProviderConfig("openai", llm.ProviderConfig{BaseURL: url}).WithRetries(0)
		}, 1},
		{"two retries, after NewClient", func(string) *Client {
			return NewClient("k").WithRetryBackoff(time.Millisecond).WithRetries(2)
		}, 3},
		{"two retries, before the provider is chosen again", func(string) *Client {
			return NewClient("k").WithRetryBackoff(time.Millisecond).WithRetries(2).WithProvider("openai")
		}, 3},
		{"the default budget", func(string) *Client {
			return NewClient("k").WithRetryBackoff(time.Millisecond)
		}, 4},
		{"retries lowered then raised", func(string) *Client {
			return NewClient("k").WithRetryBackoff(time.Millisecond).WithRetries(0).WithRetries(1)
		}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, hits := tuningServer(t, 0)
			tuningEnvironment(t, server.URL)

			_, err := extractThrough(tc.build(server.URL))
			if err == nil {
				t.Fatal("a server that only answers 500 produced a result")
			}
			if got := hits.Load(); got != tc.want {
				t.Errorf("the server saw %d request(s), want %d", got, tc.want)
			}
		})
	}
}

// The package default provider is what an operation with no client on its
// context reaches, and it has to be the rebuilt one.
func TestRetuningARegisteredProviderUpdatesTheProcessDefaultItWasInstalledAs(t *testing.T) {
	server, hits := tuningServer(t, 0)
	tuningEnvironment(t, server.URL)

	NewClient("k").WithRetryBackoff(time.Millisecond).WithProvider("openai").WithRetries(0)

	if _, err := ops.ExtractResult[tuningTarget]("some input", ops.NewExtractOptions()); err == nil {
		t.Fatal("a server that only answers 500 produced a result")
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("a call on the process default made %d attempt(s), want 1: WithRetries(0) did not reach it", got)
	}
}

// Retuning one client must not touch another client's provider or the process
// default another client installed.
func TestRetuningOneClientLeavesAnotherClientsProviderAlone(t *testing.T) {
	server, _ := tuningServer(t, 0)
	tuningEnvironment(t, server.URL)

	other := NewClient("other-key")
	otherProvider := other.provider
	mine := NewClient("my-key")

	mine.WithRetries(0).WithTimeout(time.Second).WithRetryBackoff(time.Millisecond)

	if other.provider != otherProvider {
		t.Error("retuning one client replaced another client's provider")
	}
	if retries, _ := other.provider.RetryPolicy(); retries != 3 {
		t.Errorf("the other client's retry budget is %d, want its own 3", retries)
	}
	if retries, _ := mine.provider.RetryPolicy(); retries != 0 {
		t.Errorf("the retuned client's retry budget is %d, want 0", retries)
	}
}

func TestWithTimeoutBoundsARequestToTheProviderTheClientHas(t *testing.T) {
	cases := []struct {
		name  string
		build func() *Client
	}{
		{"after NewClient", func() *Client { return NewClient("k").WithRetries(0).WithTimeout(150 * time.Millisecond) }},
		{"before the provider is chosen again", func() *Client {
			return NewClient("k").WithRetries(0).WithTimeout(150 * time.Millisecond).WithProvider("openai")
		}},
		{"after the provider is chosen again", func() *Client {
			return NewClient("k").WithRetries(0).WithProvider("openai").WithTimeout(150 * time.Millisecond)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The server would take five seconds to answer: a request that is
			// still waiting when the test ends was not bounded by the client.
			server, hits := tuningServer(t, 5*time.Second)
			tuningEnvironment(t, server.URL)

			elapsed, err := extractThrough(tc.build())
			if err == nil {
				t.Fatal("a server that never answered produced a result")
			}
			if hits.Load() != 1 {
				t.Fatalf("the server saw %d request(s), want 1", hits.Load())
			}
			if elapsed > 2*time.Second {
				t.Errorf("the call took %v; WithTimeout(150ms) did not bound the provider's request", elapsed)
			}
		})
	}
}

func TestWithRetryBackoffReachesTheProviderThatWasAlreadyBuilt(t *testing.T) {
	server, _ := tuningServer(t, 0)
	tuningEnvironment(t, server.URL)

	client := NewClient("k").WithRetryBackoff(250 * time.Millisecond)
	if _, backoff := client.provider.RetryPolicy(); backoff != 250*time.Millisecond {
		t.Errorf("the provider reports a backoff of %v, want 250ms", backoff)
	}
	client.WithRetryBackoff(75 * time.Millisecond)
	if _, backoff := client.provider.RetryPolicy(); backoff != 75*time.Millisecond {
		t.Errorf("after a second change the provider reports %v, want 75ms", backoff)
	}
}

// ProviderConfig.Debug reaches a provider only through the factory that builds it,
// so a registered factory is what can see whether a rebuilt provider was built
// with the client's debug setting.
func TestWithDebugReachesTheProviderThatWasAlreadyBuilt(t *testing.T) {
	clearCredentialEnv(t)
	clearModelOverrides(t)
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
	t.Cleanup(func() { ops.SetDefaultProvider(nil) })

	var mu sync.Mutex
	var seen []bool
	const name = "debug-recording-factory"
	if err := llm.RegisterProviderFactory(name, func(config llm.ProviderConfig) (llm.Provider, error) {
		mu.Lock()
		seen = append(seen, config.Debug)
		mu.Unlock()
		return llm.NewLocalProvider(config)
	}); err != nil {
		t.Fatal(err)
	}

	client := NewClient("k").WithProvider(name)
	last := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return seen[len(seen)-1]
	}
	if last() {
		t.Fatal("a client that never turned debug on built its provider with Debug set")
	}
	client.WithDebug(true)
	t.Cleanup(func() { client.WithDebug(false) })
	if !last() {
		t.Error("WithDebug(true) did not rebuild the provider with Debug set")
	}
	client.WithDebug(false)
	if last() {
		t.Error("WithDebug(false) did not rebuild the provider with Debug cleared")
	}
}

// A client that does not own the process default provider must not replace it
// when it retunes its own provider.
func TestRetuningAClientThatDoesNotOwnTheDefaultLeavesTheDefaultAlone(t *testing.T) {
	server, hits := tuningServer(t, 0)
	tuningEnvironment(t, server.URL)

	NewClient("owner-key").WithRetryBackoff(time.Millisecond).WithProvider("openai") // installs the default, 3 retries
	NewClient("other-key").WithRetries(0)                                            // owns nothing

	if _, err := ops.ExtractResult[tuningTarget]("some input", ops.NewExtractOptions()); err == nil {
		t.Fatal("a server that only answers 500 produced a result")
	}
	if got := hits.Load(); got != 4 {
		t.Errorf("the process default made %d attempt(s), want its owner's 4: another client's WithRetries(0) replaced it", got)
	}
}

// A provider the caller built is configured by its owner. A setting the client
// cannot apply to it is not dropped quietly: Err says so until the client is
// given a provider it can configure.
func TestSettingsThatCannotReachAProviderInstanceAreReported(t *testing.T) {
	cases := []struct {
		name  string
		build func(provider llm.Provider) *Client
		named string
	}{
		{"retries before the instance", func(p llm.Provider) *Client { return NewClient("").WithRetries(0).WithProviderInstance(p) }, "WithRetries"},
		{"retries after the instance", func(p llm.Provider) *Client { return NewClient("").WithProviderInstance(p).WithRetries(0) }, "WithRetries"},
		{"a timeout after the instance", func(p llm.Provider) *Client {
			return NewClient("").WithProviderInstance(p).WithTimeout(time.Second)
		}, "WithTimeout"},
		{"a backoff before the instance", func(p llm.Provider) *Client {
			return NewClient("").WithRetryBackoff(time.Second).WithProviderInstance(p)
		}, "WithRetryBackoff"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(func() { ops.SetDefaultProvider(nil) })
			provider := &stubProvider{name: "instance"}
			client := tc.build(provider)

			err := client.Err()
			if err == nil {
				t.Fatal("a setting the instance cannot take was dropped without a report")
			}
			if !strings.Contains(err.Error(), tc.named) {
				t.Errorf("the report does not name %s: %v", tc.named, err)
			}
			if types.KindOf(err) != types.KindConfiguration || !errors.Is(err, types.ErrConfiguration) {
				t.Errorf("the report is not classified as a configuration error: %v", err)
			}
			if client.provider != llm.Provider(provider) {
				t.Error("the client replaced the instance it was given")
			}

			// Choosing a provider the client builds takes the settings.
			configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
			client.WithProviderConfig("openai", llm.ProviderConfig{APIKey: "k"})
			if err := client.Err(); err != nil {
				t.Errorf("Err is still set after the client was given a provider it can configure: %v", err)
			}
		})
	}
}

func TestAnInstanceWithNoSettingsIsNotReported(t *testing.T) {
	t.Cleanup(func() { ops.SetDefaultProvider(nil) })
	client := NewClient("").WithProviderInstance(&stubProvider{name: "instance"}).WithDebug(false)
	if err := client.Err(); err != nil {
		t.Errorf("Err = %v for an instance and no tuning", err)
	}
}

func TestANonPositiveTimeoutIsRefusedAndTheOldOneKept(t *testing.T) {
	for _, bad := range []time.Duration{0, -time.Second, -1} {
		configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
		client := NewClient("k").WithTimeout(7 * time.Second)
		client.WithTimeout(bad)

		if client.timeout != 7*time.Second {
			t.Errorf("WithTimeout(%v) replaced the timeout with %v", bad, client.timeout)
		}
		err := client.Err()
		if err == nil || !strings.Contains(err.Error(), "WithTimeout") {
			t.Errorf("WithTimeout(%v) was refused without a report naming it: %v", bad, err)
		}
		client.WithTimeout(9 * time.Second)
		if err := client.Err(); err != nil {
			t.Errorf("a valid timeout after a refused one left Err set: %v", err)
		}
	}
}

// authorizationServer records the Authorization header of every request it gets,
// which is the one place the key a provider was really built with can be read.
func authorizationServer(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		http.Error(w, `{"error":{"message":"unavailable"}}`, http.StatusBadRequest)
	}))
	t.Cleanup(server.Close)
	return server, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

// The key a provider sends is the one the caller gave, whatever the environment
// holds, and the environment supplies a key only to a client that was given none.
func TestAnExplicitKeyBeatsEveryEnvironmentVariable(t *testing.T) {
	envKeys := map[string]string{
		"SCHEMAFLUX_OPENAI_API_KEY": "env-specific",
		"OPENAI_API_KEY":            "env-openai",
		"OPENAI":                    "env-bare",
		"SCHEMAFLUX_API_KEY":        "env-generic",
	}
	cases := []struct {
		name  string
		build func() *Client
		want  string
	}{
		{"NewClient with a key", func() *Client { return NewClient("explicit") }, "Bearer explicit"},
		{"NewClient with a key, provider chosen again", func() *Client {
			return NewClient("explicit").WithProvider("openai")
		}, "Bearer explicit"},
		{"NewClient with a key, then retuned", func() *Client {
			return NewClient("explicit").WithRetries(0).WithTimeout(time.Second)
		}, "Bearer explicit"},
		{"a key in the provider config beats the client's", func() *Client {
			return NewClient("explicit").WithProviderConfig("openai", llm.ProviderConfig{APIKey: "from-config"})
		}, "Bearer from-config"},
		{"no key: the provider's own variable first", func() *Client {
			return NewClient("").WithProvider("openai")
		}, "Bearer env-specific"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, seen := authorizationServer(t)
			tuningEnvironment(t, server.URL)
			for name, value := range envKeys {
				t.Setenv(name, value)
			}

			client := tc.build()
			if _, err := extractThrough(client.WithRetries(0)); err == nil {
				t.Fatal("a server that answers 400 produced a result")
			}
			got := seen()
			if len(got) != 1 || got[0] != tc.want {
				t.Errorf("the server saw Authorization %q, want exactly [%s]", got, tc.want)
			}
		})
	}
}

// Init follows the same rule: the key it is given beats the environment, and
// with none the environment's keys apply in their documented order.
func TestInitsExplicitKeyBeatsTheEnvironment(t *testing.T) {
	cases := []struct {
		name string
		key  string
		env  map[string]string
		want string
	}{
		{"an argument beats the provider variable", "arg", map[string]string{"OPENAI_API_KEY": "env-openai", "SCHEMAFLUX_API_KEY": "env-generic"}, "Bearer arg"},
		{"an argument beats the generic variable", "arg", map[string]string{"SCHEMAFLUX_API_KEY": "env-generic"}, "Bearer arg"},
		{"an argument beats the bare variable", "arg", map[string]string{"OPENAI": "env-bare"}, "Bearer arg"},
		{"no argument: the provider variable beats the generic one", "", map[string]string{"OPENAI_API_KEY": "env-openai", "SCHEMAFLUX_API_KEY": "env-generic"}, "Bearer env-openai"},
		{"no argument: the generic variable alone", "", map[string]string{"SCHEMAFLUX_API_KEY": "env-generic"}, "Bearer env-generic"},
		{"no argument: the bare variable alone", "", map[string]string{"OPENAI": "env-bare"}, "Bearer env-bare"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, seen := authorizationServer(t)
			tuningEnvironment(t, server.URL)
			for name, value := range tc.env {
				t.Setenv(name, value)
			}

			if err := Init(tc.key); err != nil {
				t.Fatalf("Init: %v", err)
			}
			if _, err := extractThrough(GetDefaultClient().WithRetries(0)); err == nil {
				t.Fatal("a server that answers 400 produced a result")
			}
			got := seen()
			if len(got) != 1 || got[0] != tc.want {
				t.Errorf("the server saw Authorization %q, want exactly [%s]", got, tc.want)
			}
		})
	}
}
