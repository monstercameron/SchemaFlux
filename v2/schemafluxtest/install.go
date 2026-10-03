package schemafluxtest

import (
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// installMarker is a variable Install sets for the length of a test, and the
// library never reads. Setting a variable is what makes Go refuse a test that
// has called t.Parallel: the default client is the process's, and two tests that
// install one at once would answer each other's calls.
const installMarker = "SCHEMAFLUX_TEST_INSTALLED"

// Install makes p the provider of the default client for the length of the
// test t, and puts the default back when the test ends. It is for code that
// calls on the default client, which has no context to carry a fake through
// (code that takes its context from its caller needs no Install: it is given
// schemaflux.WithClient(ctx, schemaflux.Client{Provider: p})).
//
// What it installs is the whole of a default client with that provider and
// nothing else set: the model, the limits and the policy of whatever was the
// default are not kept for the test, and are back when it ends. A fake is a
// provider in code, so no variable of the environment changes what it answers.
//
// Install changes the process, so it cannot be used in a parallel test: it sets
// a variable with t.Setenv, which Go refuses once t.Parallel has been called,
// and the test panics saying so. Two Installs in one test are undone in the
// reverse order, and the last one wins until it is.
//
//	func TestHandlerSummarises(t *testing.T) {
//		var model schemafluxtest.Provider
//		model.Reply("Short.")
//		schemafluxtest.Install(t, &model)
//
//		summary := handler.Summarise(t.Context(), longText) // calls schemaflux.Summarize(ctx, ...)
//		...
//	}
func Install(t testing.TB, p schemaflux.Provider) {
	t.Helper()
	if p == nil {
		t.Fatal("schemafluxtest.Install: the provider is nil")
	}
	t.Setenv(installMarker, "1")
	previous := schemaflux.Default()
	schemaflux.SetDefault(schemaflux.Client{Provider: p})
	t.Cleanup(func() { schemaflux.SetDefault(previous) })
}
