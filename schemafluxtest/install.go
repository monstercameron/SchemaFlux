package schemafluxtest

import (
	schemaflux "github.com/monstercameron/schemaflux"
)

// TestingT is the part of *testing.T this package uses. It is an interface so
// schemafluxtest does not import testing into your binary.
type TestingT interface {
	Helper()
	Cleanup(func())
	Setenv(key, value string)
	Fatalf(format string, args ...any)
}

// Install points SchemaFlux at the provider for the duration of the test, and
// returns a function that restores the previous one. The restore also runs via
// t.Cleanup, so calling it is optional:
//
//	defer schemafluxtest.Install(t, provider)()
//
// It calls t.Setenv, which makes Go fail any test that also calls t.Parallel.
// That is deliberate. SchemaFlux resolves its provider through a package
// global, so two parallel tests with different providers would silently share
// one; failing loudly is better than a flake nobody can reproduce. Removing the
// global is TODOS.md TI-002.
func Install(t TestingT, provider *Provider) func() {
	t.Helper()

	if provider == nil {
		t.Fatalf("schemafluxtest: Install needs a provider; call New()")
		return func() {}
	}

	// A key is required before a client will build, and this one is never sent
	// anywhere: the provider below answers every call in-process.
	t.Setenv("SCHEMAFLUX_API_KEY", "schemafluxtest-not-a-real-key")

	previous := schemaflux.GetDefaultClient()

	// NewClient builds a client; it does not install one. Setting the provider
	// on it registers the package-level provider the operations read, but
	// GetDefaultClient would still return whatever was there before -- so the
	// client has to be installed explicitly.
	client := schemaflux.NewClient("schemafluxtest-not-a-real-key").WithProviderInstance(provider)
	schemaflux.SetDefaultClient(client)

	restore := func() {
		if previous != nil {
			schemaflux.SetDefaultClient(previous)
			return
		}
		schemaflux.SetDefaultClient(nil)
	}
	t.Cleanup(restore)

	return restore
}
