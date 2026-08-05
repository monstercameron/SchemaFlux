package schemaflux

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/telemetry"
)

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
