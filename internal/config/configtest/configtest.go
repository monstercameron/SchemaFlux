// Package configtest is for tests that change a SCHEMAFLUX_* variable.
//
// The library reads those variables once and keeps what it read
// (config.ReloadEnvironment), so a test that sets one has to make the library
// read again, and has to do the same when the variable is put back. t.Setenv
// does the first half of each.
package configtest

import (
	"testing"

	"github.com/monstercameron/schemaflux/internal/config"
)

// Setenv sets an environment variable for the length of the test, as t.Setenv
// does, and makes the library read its settings again after setting it and after
// the test has put it back.
func Setenv(t testing.TB, name, value string) {
	t.Helper()
	// Registered first, so that it runs last: after the variable is restored.
	t.Cleanup(config.ReloadEnvironment)
	t.Setenv(name, value)
	config.ReloadEnvironment()
}

// Reload makes the library read its settings again now and when the test ends,
// for a test that changes the environment some other way.
func Reload(t testing.TB) {
	t.Helper()
	t.Cleanup(config.ReloadEnvironment)
	config.ReloadEnvironment()
}
