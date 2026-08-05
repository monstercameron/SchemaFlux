package schemaflux

import (
	"strings"
	"sync"
	"testing"

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
