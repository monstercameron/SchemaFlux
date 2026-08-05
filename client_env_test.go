package schemaflux

import (
	"os"
	"strings"
	"testing"
)

// clearCredentialEnv removes every variable the key resolver consults so a test
// starts from a known-empty environment.
func clearCredentialEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"SCHEMAFLUX_API_KEY", "SCHEMAFLUX_OPENAI_API_KEY", "OPENAI_API_KEY", "OPENAI",
		"SCHEMAFLUX_PROVIDER", "SCHEMAFLUX_TIMEOUT", "SCHEMAFLUX_DEBUG",
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

	if err := InitWithEnv("testdata/sample.env"); err != nil {
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

	err := InitWithEnv("testdata/definitely-not-here.env")
	if err == nil {
		t.Fatal("a named .env path that does not exist must return an error")
	}
}

// An explicit export beats a .env value: the file supplies defaults, it does
// not override the operator.
func TestProcessEnvironmentWinsOverEnvFile(t *testing.T) {
	clearCredentialEnv(t)
	t.Setenv("OPENAI", "sk-from-the-process-environment")

	if err := InitWithEnv("testdata/sample.env"); err != nil {
		t.Fatalf("InitWithEnv: %v", err)
	}
	if got := os.Getenv("OPENAI"); got != "sk-from-the-process-environment" {
		t.Errorf("the .env file overrode an explicit export: got %q", got)
	}
}
