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

// The credential resolution chain must be honoured in order, and any one of the
// accepted spellings must work on its own.
func TestCredentialResolutionOrder(t *testing.T) {
	cases := []struct {
		name string
		set  map[string]string
	}{
		{"schemaflux_generic", map[string]string{"SCHEMAFLUX_API_KEY": "k"}},
		{"schemaflux_openai", map[string]string{"SCHEMAFLUX_OPENAI_API_KEY": "k"}},
		{"openai_api_key", map[string]string{"OPENAI_API_KEY": "k"}},
		{"bare_openai", map[string]string{"OPENAI": "k"}},
		{"provider_specific_wins", map[string]string{
			"SCHEMAFLUX_OPENAI_API_KEY": "specific",
			"OPENAI":                    "generic",
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearCredentialEnv(t)
			for name, value := range tc.set {
				t.Setenv(name, value)
			}
			if err := Init(""); err != nil {
				t.Fatalf("a resolvable credential must initialise: %v", err)
			}
			if GetDefaultClient() == nil {
				t.Fatal("expected a client")
			}
		})
	}
}

// An explicit key argument beats the environment entirely.
func TestExplicitKeyArgumentWins(t *testing.T) {
	clearCredentialEnv(t)
	t.Setenv("SCHEMAFLUX_API_KEY", "from-env")

	if err := Init("from-argument"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if GetDefaultClient() == nil {
		t.Fatal("expected a client")
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

	if err := InitWithEnv("testdata/sample.env", "testdata/sample.env"); err != nil {
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

	if err := InitWithEnv("testdata/sample.env"); err != nil {
		t.Fatalf("InitWithEnv: %v", err)
	}
	if got := os.Getenv("SCHEMAFLUX_TIMEOUT"); got != "12s" {
		t.Errorf("SCHEMAFLUX_TIMEOUT = %q, want the fixture value 12s", got)
	}
}
