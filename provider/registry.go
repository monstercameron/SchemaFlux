package provider

import (
	"errors"
	"reflect"
	"slices"
	"sync"

	schemaflux "github.com/monstercameron/schemaflux/v2"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// registry holds the factories added with Register. It is guarded by one mutex,
// held across the whole of a registration, so that two registrations of one name
// cannot both succeed and a lookup never sees a name that is half added.
var registry struct {
	sync.Mutex
	factories map[string]Factory
}

// Register adds a provider under name, so that New(name, cfg) builds it with f.
// Surrounding space and case in name do not matter: "Acme " and "acme" are one
// name.
//
// A name that is already taken is an error of kind schemaflux.ErrConfiguration,
// whether the name belongs to a built-in provider or to an earlier call of
// Register: a program that wants to replace "openai" builds a provider of its own
// and puts it in Client.Provider, and a program whose two parts register one name
// learns it at start-up and not by one of them silently winning. An empty name
// and a nil f are errors of the same kind.
//
// A provider that needs no API key (one that runs a program on the same machine,
// say) is added with RegisterKeyless instead, so that a client whose environment
// selects it is not refused for want of a key.
//
// The name is also added to the internal registry that a client resolves
// SCHEMAFLUX_PROVIDER through, so that a client that builds its provider from the
// environment can be told to use it.
//
// Register is safe for concurrent use. There is no way to remove a name. It is
// for start-up: a name added later is found by every New that follows it.
func Register(name string, f Factory) error { return register(name, f, false) }

// RegisterKeyless is Register for a provider that needs no API key, such as one
// that runs a program on the same machine. A client whose environment selects it
// (SCHEMAFLUX_PROVIDER) builds it with none, where a provider added with Register
// is refused for want of a key. A provider that does need one, and checks
// Config.APIKey itself, is added with Register.
func RegisterKeyless(name string, f Factory) error { return register(name, f, true) }

// register adds a provider under name, and records that it needs no key when
// keyless is set.
func register(name string, f Factory, keyless bool) error {
	key := llm.NormalizeProviderName(name)
	if key == "" {
		return configurationError(opRegister, "", "the provider name is empty")
	}
	if f == nil {
		return configurationError(opRegister, key, "the factory is nil")
	}

	registry.Lock()
	defer registry.Unlock()
	if _, taken := registry.factories[key]; taken || slices.Contains(llm.ListProviders(), key) {
		return configurationError(opRegister, key, "the name "+quote(key)+" is already a provider; Names lists the taken names")
	}
	if err := llm.RegisterProviderFactory(key, bridge(key, f)); err != nil {
		return buildFailure(opRegister, key, err)
	}
	if registry.factories == nil {
		registry.factories = make(map[string]Factory)
	}
	registry.factories[key] = f
	if keyless {
		bind.DeclareKeyless(key)
	}
	return nil
}

// factoryFor returns the factory registered under the normalised name key.
func factoryFor(key string) (Factory, bool) {
	registry.Lock()
	defer registry.Unlock()
	f, ok := registry.factories[key]
	return f, ok
}

// buildRegistered builds a provider with a registered factory, giving it cfg with
// the defaults of a built provider. The factory is the program's own code: a
// panic in it is not recovered.
func buildRegistered(key string, f Factory, cfg Config) (schemaflux.Provider, error) {
	built, err := f(cfg.withDefaults())
	if err != nil {
		return nil, factoryFailure(opNew, key, err)
	}
	if isNil(built) {
		return nil, configurationError(opNew, key, "the factory returned no provider and no error")
	}
	return built, nil
}

// isNil reports whether p is nil or holds a nil pointer, map, slice, function or
// channel: a typed nil is not a provider, and its first method call would panic
// in the middle of a call.
func isNil(p schemaflux.Provider) bool {
	if p == nil {
		return true
	}
	switch value := reflect.ValueOf(p); value.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.Interface:
		return value.IsNil()
	}
	return false
}

// maxFactoryReason is the longest a factory's account of why it failed may be in
// the error New returns, in bytes.
const maxFactoryReason = 200

// factoryFailure is the error for a factory that failed. A kind the factory gave
// its error is honoured, and a plain error is a configuration error: a provider
// that cannot be built has sent nothing. The factory's own words are the
// message: the factory is the program's code, and its refusal is its account of
// what is wrong with the configuration it was given. They are cut to a bounded
// length of printable text, and the error itself stays in the chain.
func factoryFailure(op, name string, err error) error {
	kind := schemaflux.ErrConfiguration
	if known, ok := bind.KindOfError(err); ok {
		kind = schemaflux.Kind(known)
	}
	message := err.Error()
	var own *schemaflux.Error
	if errors.As(err, &own) && own.Message != "" {
		message = own.Message
	}
	return &schemaflux.Error{Kind: kind, Op: op, Provider: name, Message: types.Bound(message, maxFactoryReason), Err: err}
}

// bridge is the internal factory for f, which is what puts a registered name in
// the internal registry. The internal configuration it is given has the defaults
// of a built provider already applied by llm.BuildProvider; a count of retries
// of zero in it means none.
func bridge(key string, f Factory) llm.ProviderFactory {
	return func(internal llm.ProviderConfig) (llm.Provider, error) {
		built, err := f(configFromInternal(internal))
		if err != nil {
			return nil, bind.ToProvider(factoryFailure(opNew, key, err), key, "")
		}
		if isNil(built) {
			return nil, types.Newf(types.KindConfiguration, "llm.configure", "the factory of %s returned no provider and no error", key)
		}
		return bind.ToInternal(built), nil
	}
}

// configFromInternal is the Config for an internal configuration: the inverse of
// internalConfig, for a factory the internal registry calls.
func configFromInternal(c llm.ProviderConfig) Config {
	cfg := Config{
		APIKey:          c.APIKey,
		BaseURL:         c.BaseURL,
		OrgID:           c.OrgID,
		Timeout:         c.Timeout,
		Retries:         c.MaxRetries,
		RetryBackoff:    c.RetryBackoff,
		Store:           c.Store,
		ServiceTier:     c.ServiceTier,
		OmitTemperature: c.OmitTemperature,
		HTTPClient:      c.HTTPClient,
		Models:          Models{Smart: c.Models[0], Fast: c.Models[1], Quick: c.Models[2]},
	}
	if cfg.Retries == 0 {
		cfg.Retries = -1
	}
	if len(c.ExtraHeaders) > 0 {
		cfg.Headers = make(map[string]string, len(c.ExtraHeaders))
		for name, value := range c.ExtraHeaders {
			cfg.Headers[name] = value
		}
	}
	if c.EndpointPolicy != nil {
		cfg.Endpoint = &EndpointPolicy{
			Schemes:      slices.Clone(c.EndpointPolicy.AllowedSchemes),
			Hosts:        slices.Clone(c.EndpointPolicy.AllowedHosts),
			AllowPrivate: c.EndpointPolicy.AllowPrivateAddresses,
		}
	}
	return cfg
}
