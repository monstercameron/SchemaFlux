package codex_test

import (
	"errors"
	"fmt"
	"slices"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/provider"
	"github.com/monstercameron/schemaflux/v2/provider/codex"
)

// New resolves the program when the provider is built, so that one that is not
// installed is a configuration error at start-up and not a failure inside the
// first request.
func ExampleNew() {
	_, err := codex.New(codex.Options{Executable: "codex-is-not-installed-here", Model: "gpt-6.1-sol"})
	fmt.Println(errors.Is(err, schemaflux.ErrConfiguration))

	_, err = codex.New(codex.Options{Executable: "codex-is-not-installed-here", Effort: "turbo"})
	fmt.Println(errors.Is(err, schemaflux.ErrConfiguration))
	// Output:
	// true
	// true
}

// Register adds the name "codex" to the providers provider.New builds. It is
// called once, at start-up; a second call, or a name that is taken, is an error
// of kind ErrConfiguration.
func ExampleRegister() {
	if err := codex.Register(); err != nil && !errors.Is(err, schemaflux.ErrConfiguration) {
		fmt.Println(err)
	}
	fmt.Println(slices.Contains(provider.Names(), "codex"))
	// Output: true
}
