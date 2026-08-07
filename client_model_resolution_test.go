package schemaflux

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/config"
)

// clearModelOverrides drops the model environment variables so these cases see
// the built-in mapping. It also clears the credential variables, because
// WithProvider builds a real provider and a stray key changes which branch it
// takes.
func clearModelOverrides(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"SCHEMAFLUX_MODEL", "SCHEMAFLUX_MODEL_SMART", "SCHEMAFLUX_MODEL_FAST", "SCHEMAFLUX_MODEL_QUICK",
	} {
		t.Setenv(name, "")
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
	t.Setenv("SCHEMAFLUX_MODEL", "my-local-model")

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
	t.Setenv("SCHEMAFLUX_MODEL", "qwen-max")

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
