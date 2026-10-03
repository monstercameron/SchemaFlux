package provider

import (
	"strings"
	"unicode"

	schemaflux "github.com/monstercameron/schemaflux/v2"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/config"
)

// sharedKeyVariable is the key of any provider whose own variables are not set.
const sharedKeyVariable = "SCHEMAFLUX_API_KEY"

// variablesFor returns the variables a built-in provider reads its key and base
// URL from, in the order they are tried, and whether it reads any. The key
// variables end with the shared one. "local" and a name added with Register read
// none. The table is the root's (bind.ProviderVariables), which a test there
// holds to what internal/config reads: this package keeps no copy of it.
func variablesFor(name string) (variables struct{ keys, baseURLs []string }, ok bool) {
	keys, baseURLs, ok := bind.ProviderVariables(name)
	if !ok {
		return variables, false
	}
	variables.keys = append(keys, sharedKeyVariable)
	variables.baseURLs = baseURLs
	return variables, true
}

// withEnvironment returns cfg with the key and the base URL that cfg leaves
// empty taken from env, for the built-in provider name. A key that is not in cfg
// or in env is an error that names the variables it looked in; so is a variable
// that is set to something that cannot be used and that would have been read.
// A field that cfg sets is never read from, and so is never refused because of a
// variable it did not use.
func withEnvironment(op, name string, cfg Config, env *config.Environment) (Config, error) {
	variables, _ := variablesFor(name)

	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	if cfg.APIKey == "" {
		if err := unusable(op, name, env, variables.keys); err != nil {
			return cfg, err
		}
		key := env.Keys[name]
		if key == "" {
			return cfg, configurationError(op, name,
				"no API key: set Config.APIKey, or one of "+strings.Join(variables.keys, ", "))
		}
		cfg.APIKey = key
	}

	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	if cfg.BaseURL == "" {
		if err := unusable(op, name, env, variables.baseURLs); err != nil {
			return cfg, err
		}
		cfg.BaseURL = env.BaseURLs[name]
	}
	return cfg, nil
}

// unusable is the configuration error for the first of names that env holds a
// value for that cannot be used. It names the variable and what it takes, and
// never the value.
func unusable(op, name string, env *config.Environment, names []string) error {
	for _, variable := range names {
		for _, problem := range env.Problems {
			if problem.Variable == variable {
				return configurationError(op, name, variable+" is not usable; it takes "+problem.Want)
			}
		}
	}
	return nil
}

// configurationError is an *Error of kind ErrConfiguration. message is this
// package's own words and holds no value read from the environment.
func configurationError(op, name, message string) error {
	return &schemaflux.Error{Kind: schemaflux.ErrConfiguration, Op: op, Provider: name, Message: message}
}

// hasControl reports whether s holds a control character, which cannot be sent
// in a header.
func hasControl(s string) bool {
	return strings.IndexFunc(s, unicode.IsControl) >= 0
}

// quote returns s in quotation marks, for a message that names a provider the
// caller wrote.
func quote(s string) string {
	return `"` + s + `"`
}
