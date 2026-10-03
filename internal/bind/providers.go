package bind

import (
	"strings"
	"sync"
)

// The two names under which the library ships a provider that answers every
// request with a placeholder and not with a model's answer. They exist for the
// old root's demonstrations. Version 2 has no provider that fails open, so the
// root and the provider package refuse both, and name the provider to use
// instead.
var placeholderProviders = []string{"local", "mock"}

// PlaceholderProvider reports whether name is the name of a built-in provider
// that answers with a placeholder, which version 2 does not offer: a program
// that wants an offline provider uses the schemafluxtest package's, which fails
// a request nobody scripted. Space and case in name do not matter.
func PlaceholderProvider(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, placeholder := range placeholderProviders {
		if name == placeholder {
			return true
		}
	}
	return false
}

// PlaceholderAdvice is what an error says to a program that asked for a
// placeholder provider: which one it asked for, and the one to use.
func PlaceholderAdvice(name string) string {
	return "the provider " + `"` + strings.ToLower(strings.TrimSpace(name)) + `"` +
		" answers every request with a placeholder and not with a model's answer, and version 2 does not offer it; " +
		"use the Provider of the schemafluxtest package, which fails a request nobody scripted"
}

// keyless is the providers added at run time that said they need no key.
var keyless sync.Map

// DeclareKeyless records that the registered provider called name needs no API
// key, so that a client whose environment selects it (SCHEMAFLUX_PROVIDER) does
// not refuse to build it for want of one. Space and case in name do not matter.
// It is called by the provider package's registration of such a provider, and
// declaring a name again changes nothing.
func DeclareKeyless(name string) {
	keyless.Store(strings.ToLower(strings.TrimSpace(name)), struct{}{})
}

// Keyless reports whether the registered provider called name declared that it
// needs no API key.
func Keyless(name string) bool {
	_, ok := keyless.Load(strings.ToLower(strings.TrimSpace(name)))
	return ok
}

// ProviderVariables returns the variables the key and the base URL of the
// built-in provider called name are read from, in the order they are tried, and
// whether it has any. The shared key variable is not in the list. It returns
// nothing before the root has registered.
func ProviderVariables(name string) (keys, baseURLs []string, ok bool) {
	if h := registered.Load(); h != nil && h.Variables != nil {
		return h.Variables(name)
	}
	return nil, nil, false
}
