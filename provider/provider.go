package provider

import (
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/config"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// A Config says how to build a provider. Its zero value builds the named
// provider from the environment with the defaults of a provider this package
// builds, and each field below says what its zero value means.
//
// A Config is read when the provider is built. Changing it afterwards, or a map
// or slice it holds, does not change the provider.
type Config struct {
	// APIKey is the credential the provider sends. Empty means the environment's
	// key for this provider (see the package comment); a key here wins over
	// every variable. Surrounding space is removed.
	APIKey string

	// BaseURL is the address the provider sends requests to, such as
	// "https://proxy.example/v1". Empty means the environment's address for this
	// provider and then the provider's own. It has to be an absolute http or
	// https URL with a host, and hold no credentials, query or fragment: any
	// other is a configuration error that names this field. OpenAICompatible takes
	// its address as an argument and ignores this field.
	BaseURL string

	// OrgID is the organisation a request is billed to, sent in the
	// OpenAI-Organization header. Empty means none. Only OpenAI reads it.
	OrgID string

	// Timeout bounds one HTTP call, from the request to the end of the response.
	// Zero means 30 seconds. A negative value is a configuration error. It is
	// not the deadline of an operation: that is the context's, and the client's
	// Timeout.
	Timeout time.Duration

	// Retries is how many times a request that failed in transit is sent again,
	// after the first attempt. Zero means 3. A negative value means none: the
	// request is sent once.
	Retries int

	// RetryBackoff is the wait before the first of those repeats, which the
	// provider lengthens for each. Zero means 1 second. A negative value is a
	// configuration error.
	RetryBackoff time.Duration

	// Headers are added to every request. Nil means none. The provider keeps a
	// copy.
	Headers map[string]string

	// Store asks the provider to keep the response on its side. False, the zero
	// value, asks it not to. Only OpenAI reads it.
	Store bool

	// ServiceTier is the provider's service tier for every request: "priority",
	// "flex", "default" or "auto". Empty means none is sent and the account's own
	// setting applies. Only OpenAI's own endpoint takes it: on another provider,
	// or on OpenAI behind another base URL, it is a configuration error.
	ServiceTier string

	// OmitTemperature leaves the temperature out of every request, whatever the
	// model. False, the zero value, lets the model table decide. Set it for an
	// endpoint that rejects the parameter under a model name the table does not
	// know, such as a deployment alias.
	OmitTemperature bool

	// HTTPClient is the client the provider sends through, for a proxy, a
	// client certificate or request instrumentation. Nil means one the library
	// builds. A client given here is used as it is and is never changed; Timeout
	// above is applied to each call's context.
	HTTPClient *http.Client

	// Endpoint, when not nil, is checked against the base URL before the
	// provider is built, and a base URL it does not allow is a configuration
	// error. Nil means the base URL is used as given.
	Endpoint *EndpointPolicy

	// Models are the models a call asks for, by tier, when nothing pins one. A
	// zero entry means the provider's built-in model for that tier.
	Models Models
}

// Models names the model a call asks for at each tier, the classes of
// schemaflux.Tier: Smart for the calls that need the most capable model, Fast
// for the middle, and Quick for the cheapest. An empty field means the
// provider's built-in model for that tier.
type Models struct {
	// Smart is the model for the most capable tier. Empty uses the provider default.
	Smart string
	// Fast is the model for the middle tier. Empty uses the provider default.
	Fast string
	// Quick is the model for the cheapest tier. Empty uses the provider default.
	Quick string
}

// An EndpointPolicy says which base URLs a provider may be built for. Its zero
// value allows none: a policy that names nothing as allowed has refused
// everything, so a program that sets one names the schemes and hosts it allows.
type EndpointPolicy struct {
	// Schemes are the URL schemes allowed, such as "https", compared without
	// regard to case. Empty allows none.
	Schemes []string

	// Hosts are the host names allowed, without port or path, compared exactly
	// and without regard to case. Empty allows none.
	Hosts []string

	// AllowPrivate lets a host through that is a loopback, link-local or private
	// address, which is otherwise refused even when it is in Hosts. False, the
	// zero value, refuses them.
	AllowPrivate bool
}

// A Factory builds a provider from a Config. Register adds one under a name.
//
// The Config a Factory receives is the one New was given with the defaults of a
// built provider filled in (Timeout, Retries and RetryBackoff), and with
// nothing read from the environment: a factory that wants a variable reads it
// itself. A Factory that returns a nil Provider with a nil error has failed, and
// New reports it as a configuration error.
type Factory func(cfg Config) (schemaflux.Provider, error)

const (
	opNew        = "provider.new"
	opCompatible = "provider.compatible"
	opRegister   = "provider.register"
)

// New builds the provider called name, which is one of the names Names returns:
// a built-in provider or one added with Register. Surrounding space and case in
// name do not matter.
//
// For a built-in provider with a zero Config, the key comes from the
// environment variables of the package comment, own variables first and then
// SCHEMAFLUX_API_KEY, and so does the base URL; a field of cfg that is set wins.
// No key anywhere is an error of kind schemaflux.ErrConfiguration naming the
// variables that were looked at. The providers "local" and "mock", which answer
// every request with a placeholder, are not offered: asking for one is a
// configuration error that names the Provider of the schemafluxtest package, the
// offline provider.
//
// The provider has 3 retries, a 1 second backoff and a 30 second HTTP timeout
// where cfg leaves them zero. A provider that cannot be built, an unknown name, a
// negative Timeout or RetryBackoff, a key with a control character, or a
// setting the provider refuses, or a base URL that is not an absolute http or
// https URL with a host, is an *Error of kind ErrConfiguration. An error never
// contains a key.
//
// The provider New returns satisfies the optional interfaces of this package
// that the provider can honour: for example Warmer for OpenAI, and Decider for
// "jev". Find one with a type assertion, or through the wrappers of a chain as
// the library does (see Unwrapper).
func New(name string, cfg Config) (schemaflux.Provider, error) {
	return newFrom(os.LookupEnv, name, cfg)
}

// newFrom is New reading its environment through lookup, so that a test gives it
// any environment it likes and runs in parallel with any other.
func newFrom(lookup func(string) (string, bool), name string, cfg Config) (schemaflux.Provider, error) {
	key := llm.NormalizeProviderName(name)
	if key == "" {
		return nil, configurationError(opNew, "", "the provider name is empty")
	}
	if bind.PlaceholderProvider(key) {
		return nil, configurationError(opNew, key, bind.PlaceholderAdvice(key))
	}
	if err := cfg.check(opNew, key); err != nil {
		return nil, err
	}

	if factory, ok := factoryFor(key); ok {
		return buildRegistered(key, factory, cfg)
	}
	if !slices.Contains(llm.ListProviders(), key) {
		return nil, configurationError(opNew, key, "no provider is called "+quote(key)+"; Names lists the ones there are")
	}

	if _, takesKey := variablesFor(key); takesKey {
		env := config.Read(lookup)
		resolved, err := withEnvironment(opNew, key, cfg, &env)
		if err != nil {
			return nil, err
		}
		cfg = resolved
	}

	built, err := llm.BuildProvider(key, internalConfig(cfg), cfg.Retries)
	if err != nil {
		return nil, buildFailure(opNew, key, err)
	}
	return adapt(built), nil
}

// OpenAICompatible builds a provider for a server that speaks the chat
// completions dialect, such as a proxy, a gateway or a model server you run. name
// is what the provider calls itself, which a client's data policy and a price
// table know it by; baseURL is the server's address up to its /chat/completions
// path, such as "http://localhost:8000/v1".
//
// Config.APIKey is required and is sent as a bearer token; a server that needs
// none takes any non-empty string. OpenAICompatible reads no environment
// variable, because a key from the environment belongs to the vendor it was
// issued by and not to an address the caller chose. Config.BaseURL is ignored.
// The defaults of New apply to the other fields.
//
// An empty name, an empty baseURL, no key or a setting the server's dialect
// refuses is an *Error of kind ErrConfiguration.
func OpenAICompatible(name, baseURL string, cfg Config) (schemaflux.Provider, error) {
	key := llm.NormalizeProviderName(name)
	if key == "" {
		return nil, configurationError(opCompatible, "", "the provider name is empty")
	}
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return nil, configurationError(opCompatible, key, "the base URL is empty")
	}
	if problem := checkBaseURL(baseURL); problem != "" {
		return nil, configurationError(opCompatible, key, "the baseURL argument "+problem)
	}
	cfg.BaseURL = ""
	if err := cfg.check(opCompatible, key); err != nil {
		return nil, err
	}
	cfg.BaseURL = baseURL
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	if cfg.APIKey == "" {
		return nil, configurationError(opCompatible, key, "Config.APIKey is empty; a server that needs no key takes any non-empty string")
	}

	internal := internalConfig(cfg.withDefaults())
	built, err := llm.NewOpenAICompatibleProvider(key, internal)
	if err != nil {
		return nil, buildFailure(opCompatible, key, err)
	}
	return adapt(built), nil
}

// Names returns the names New accepts, in order: the built-in providers and
// every name added with Register.
func Names() []string {
	var names []string
	for _, name := range llm.ListProviders() {
		if !bind.PlaceholderProvider(name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// DeclareRoute declares where one provider and model run and how long they keep
// what they are sent, which is what a client's data policy asks of a route.
// regions is every region the route may be served from: a route that may be
// served from two declares both and is allowed only by a policy that allows
// both. An empty list declares that the route runs nowhere a policy can place,
// which is not the same as not declaring it. retentionDays is the longest the
// route keeps content, and zero says it keeps none; a route whose retention is
// not known should not be declared.
//
// A later declaration of the same route, with provider and model compared
// without regard to case or surrounding space, replaces the earlier one. The
// slice is copied. DeclareRoute is for start-up, before requests are served, and
// changes process state that has no undo.
func DeclareRoute(provider, model string, regions []string, retentionDays int) {
	llm.DeclareRoute(provider, model, regions, retentionDays)
}

// check refuses what no provider can be built from: a negative duration and a
// key that cannot be sent as a header.
func (c Config) check(op, name string) error {
	if c.Timeout < 0 {
		return configurationError(op, name, "Config.Timeout is negative; zero means the default of 30 seconds")
	}
	if c.RetryBackoff < 0 {
		return configurationError(op, name, "Config.RetryBackoff is negative; zero means the default of 1 second")
	}
	if hasControl(strings.TrimSpace(c.APIKey)) {
		return configurationError(op, name, "Config.APIKey holds a control character")
	}
	if base := strings.TrimSpace(c.BaseURL); base != "" {
		if problem := checkBaseURL(base); problem != "" {
			return configurationError(op, name, "Config.BaseURL "+problem)
		}
	}
	return nil
}

// checkBaseURL returns what is wrong with a base URL, as the end of a sentence
// that names the field, or the empty string when it is one a request can be sent
// to: an absolute URL with the http or https scheme and a host, that holds no
// credentials, query or fragment. The answer never repeats the URL, which can
// hold a key.
func checkBaseURL(raw string) string {
	parsed, err := url.Parse(raw)
	switch {
	case err != nil:
		return "is not a URL"
	case parsed.Scheme != "http" && parsed.Scheme != "https":
		return "is not an absolute http or https URL"
	case parsed.Hostname() == "":
		return "has no host"
	case parsed.User != nil:
		return "holds credentials, which belong in Config.APIKey"
	case parsed.RawQuery != "" || parsed.ForceQuery:
		return "holds a query, which a base URL cannot"
	case parsed.Fragment != "" || strings.Contains(raw, "#"):
		return "holds a fragment, which a base URL cannot"
	case parsed.Opaque != "":
		return "is not an absolute http or https URL"
	}
	return ""
}

// withDefaults returns c with the settings a built provider has when its
// Config leaves them zero. A negative Retries stays negative, which is none.
func (c Config) withDefaults() Config {
	if c.Timeout == 0 {
		c.Timeout = llm.DefaultHTTPTimeout
	}
	if c.RetryBackoff == 0 {
		c.RetryBackoff = llm.DefaultRetryBackoff
	}
	if c.Retries == 0 {
		c.Retries = llm.DefaultRetries
	}
	return c
}

// internalConfig is the internal configuration for c. The retry count is not in
// it: llm.BuildProvider takes it beside the configuration so that one number
// says "unset", and OpenAICompatible, which does not go through it, takes it
// from the defaults of withDefaults.
func internalConfig(c Config) llm.ProviderConfig {
	internal := llm.ProviderConfig{
		APIKey:          strings.TrimSpace(c.APIKey),
		BaseURL:         strings.TrimSpace(c.BaseURL),
		OrgID:           c.OrgID,
		Timeout:         c.Timeout,
		RetryBackoff:    c.RetryBackoff,
		Store:           c.Store,
		ServiceTier:     c.ServiceTier,
		OmitTemperature: c.OmitTemperature,
		HTTPClient:      c.HTTPClient,
		Models:          [3]string{c.Models.Smart, c.Models.Fast, c.Models.Quick},
	}
	switch {
	case c.Retries < 0:
		internal.MaxRetries = 0
	default:
		internal.MaxRetries = c.Retries
	}
	if len(c.Headers) > 0 {
		internal.ExtraHeaders = make(map[string]string, len(c.Headers))
		for name, value := range c.Headers {
			internal.ExtraHeaders[name] = value
		}
	}
	if c.Endpoint != nil {
		internal.EndpointPolicy = &types.EndpointPolicy{
			AllowedSchemes:        slices.Clone(c.Endpoint.Schemes),
			AllowedHosts:          slices.Clone(c.Endpoint.Hosts),
			AllowPrivateAddresses: c.Endpoint.AllowPrivate,
		}
	}
	return internal
}
