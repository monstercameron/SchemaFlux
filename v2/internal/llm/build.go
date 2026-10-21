package llm

import "time"

// The settings a provider that is built has when its configuration leaves them
// unset. They are the numbers the old root's client applies when it builds a
// provider (defaultClientTimeout, defaultClientRetries and defaultClientBackoff
// in internal/api/client), moved to where a provider is built so that a provider
// built from the environment or by name has them too.
const (
	// DefaultHTTPTimeout bounds one call of a built provider.
	DefaultHTTPTimeout = 30 * time.Second

	// DefaultRetries is how many times a built provider sends a request that
	// failed in transit again.
	DefaultRetries = 3

	// DefaultRetryBackoff is the wait before the first of those.
	DefaultRetryBackoff = time.Second
)

// BuildProvider is CreateProvider for a caller whose configuration says "unset"
// with zero. A zero Timeout is DefaultHTTPTimeout and a zero RetryBackoff is
// DefaultRetryBackoff. retries is how many times a request that failed in transit
// is sent again: zero means DefaultRetries and a negative number means none.
// config.MaxRetries is not read, so that one number says it.
//
// A negative Timeout is kept, which net/http reads as no bound. A negative
// RetryBackoff is unset, as CallLLM reads it, and gets the default. CreateProvider
// and the constructors apply none of this: a provider they build from an empty
// configuration sends a request once and has no HTTP timeout.
//
// An error comes back beside a nil Provider. The constructors registered for the
// built-in names return a typed nil pointer on failure, which CreateProvider
// passes on as a Provider that is not nil.
func BuildProvider(name string, config ProviderConfig, retries int) (Provider, error) {
	if config.Timeout == 0 {
		config.Timeout = DefaultHTTPTimeout
	}
	if config.RetryBackoff <= 0 {
		config.RetryBackoff = DefaultRetryBackoff
	}
	switch {
	case retries == 0:
		config.MaxRetries = DefaultRetries
	case retries < 0:
		config.MaxRetries = 0
	default:
		config.MaxRetries = retries
	}

	provider, err := CreateProvider(name, config)
	if err != nil {
		return nil, err
	}
	return provider, nil
}
