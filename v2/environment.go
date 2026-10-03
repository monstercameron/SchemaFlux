package schemaflux

import (
	"context"
	"iter"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// This file is the one place the root asks internal/config for the
// environment and builds the provider the environment names. It reads
// internal/config and internal/llm and nothing under v2: the root imports no
// subpackage of its own, so the environment's provider is not built by the
// provider package.
//
// The environment is read when a client needs it, and the provider built from it
// is built once and kept. Only a success is kept: a client that could not build
// its provider (no key is set yet, the program loads its variables after the
// library is linked) reads the environment again on its next call, so a program
// that sets what was missing is not left with the first failure for the life of
// the process. A program that changes its environment after a success and wants
// the change to count gives its Client a Provider.

// environmentCell is an environment and the provider built from it. The process
// has one, whose lookup is the process environment. The tests of this package
// make their own with a lookup they control, which is how a test sets an
// environment without setting a variable of the process.
type environmentCell struct {
	// lookup is where the variables are read. Nil means the process
	// environment, through config.Process.
	lookup func(name string) (string, bool)

	// mu orders the reads and the build, so that calls that arrive together build
	// one provider and see one outcome. Nothing is waited for under it: reading
	// the variables and building a provider send nothing.
	mu sync.Mutex

	// provider is the provider built, and env the variables it was built from. Both
	// are set together by the first build that succeeds and never changed after.
	provider llm.Provider
	env      *config.Environment

	// builds counts the builds that were tried, which a test reads.
	builds atomic.Int32
}

// processEnvironment is the environment of the process.
var processEnvironment environmentCell

// environmentOverride, when set, is the environment every client of this
// package reads in place of the process's. Only the tests of this package set
// it, and only from a test that is not parallel.
var environmentOverride atomic.Pointer[environmentCell]

// currentEnvironment is the environment a client reads now.
func currentEnvironment() *environmentCell {
	if cell := environmentOverride.Load(); cell != nil {
		return cell
	}
	return &processEnvironment
}

// read parses the variables now.
func (e *environmentCell) read() *config.Environment {
	if e.lookup == nil {
		return config.Process()
	}
	read := config.Read(e.lookup)
	return &read
}

// environment returns the variables the provider was built from, once there is
// one, and the variables as they are now until then.
func (e *environmentCell) environment() *config.Environment {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.env != nil {
		return e.env
	}
	return e.read()
}

// resolve returns the provider the environment names. A provider that was built
// is the same one after. When none can be built it returns the configuration
// error that says why, and the next call reads the environment and tries again.
func (e *environmentCell) resolve() (llm.Provider, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.provider != nil {
		return e.provider, nil
	}
	e.builds.Add(1)
	env := e.read()
	built, err := buildEnvironmentProvider(env)
	if err != nil {
		return nil, err
	}
	e.provider, e.env = built, env
	return built, nil
}

// timeout returns the deadline the environment gives a call whose context has
// none: SCHEMAFLUX_TIMEOUT when it is set, and zero when it is not. A malformed
// SCHEMAFLUX_TIMEOUT is the configuration error naming it.
func (e *environmentCell) timeout() (time.Duration, error) {
	env := e.environment()
	if err := env.Problem(timeoutVariable); err != nil {
		return 0, err
	}
	return env.Timeout, nil
}

const timeoutVariable = "SCHEMAFLUX_TIMEOUT"

// providerVariables are the variables the key and the base URL of each provider
// are taken from, in the order internal/config tries them: the library's own
// form first, then the vendor's. The provider package reads this table through
// bind.ProviderVariables and does not keep one, and a test holds it to what
// config.Read does, in the order it does it, so the two cannot drift.
var providerVariables = map[string]struct{ keys, baseURLs []string }{
	"openai":         {[]string{"SCHEMAFLUX_OPENAI_API_KEY", "OPENAI_API_KEY"}, []string{"SCHEMAFLUX_OPENAI_BASE_URL", "OPENAI_BASE_URL"}},
	"anthropic":      {[]string{"SCHEMAFLUX_ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY"}, []string{"SCHEMAFLUX_ANTHROPIC_BASE_URL", "ANTHROPIC_BASE_URL"}},
	"openrouter":     {[]string{"SCHEMAFLUX_OPENROUTER_API_KEY", "OPENROUTER_API_KEY"}, []string{"SCHEMAFLUX_OPENROUTER_BASE_URL", "OPENROUTER_BASE_URL"}},
	"cerebras":       {[]string{"SCHEMAFLUX_CEREBRAS_API_KEY", "CEREBRAS_API_KEY"}, []string{"SCHEMAFLUX_CEREBRAS_BASE_URL", "CEREBRAS_BASE_URL"}},
	"deepseek":       {[]string{"SCHEMAFLUX_DEEPSEEK_API_KEY", "DEEPSEEK_API_KEY"}, []string{"SCHEMAFLUX_DEEPSEEK_BASE_URL", "DEEPSEEK_BASE_URL"}},
	"qwen":           {[]string{"SCHEMAFLUX_QWEN_API_KEY", "QWEN_API_KEY", "DASHSCOPE_API_KEY"}, []string{"SCHEMAFLUX_QWEN_BASE_URL", "QWEN_BASE_URL", "DASHSCOPE_BASE_URL"}},
	"zai":            {[]string{"SCHEMAFLUX_ZAI_API_KEY", "ZAI_API_KEY", "GLM_API_KEY"}, []string{"SCHEMAFLUX_ZAI_BASE_URL", "ZAI_BASE_URL", "GLM_BASE_URL"}},
	"jev":            {[]string{"SCHEMAFLUX_TYPESAFE_API_KEY", "TYPESAFE_API_KEY"}, []string{"SCHEMAFLUX_TYPESAFE_BASE_URL", "TYPESAFE_BASE_URL"}},
	"jev-openrouter": {[]string{"SCHEMAFLUX_OPENROUTER_API_KEY", "OPENROUTER_API_KEY"}, []string{"SCHEMAFLUX_OPENROUTER_BASE_URL", "OPENROUTER_BASE_URL"}},
	"jev-litellm":    {[]string{"SCHEMAFLUX_LITELLM_API_KEY", "LITELLM_API_KEY"}, []string{"SCHEMAFLUX_LITELLM_BASE_URL", "LITELLM_BASE_URL"}},
}

// variablesOf is the row of providerVariables for a built-in provider, as the
// copy bind.ProviderVariables hands to the provider package.
func variablesOf(name string) (keys, baseURLs []string, ok bool) {
	row, ok := providerVariables[name]
	if !ok {
		return nil, nil, false
	}
	return slices.Clone(row.keys), slices.Clone(row.baseURLs), true
}

// sharedKeyVariable is the key of any provider whose own variables are not set.
const sharedKeyVariable = "SCHEMAFLUX_API_KEY"

// modelsOf is the models the environment's provider means by each tier:
// SCHEMAFLUX_MODEL for all three when it is set, and otherwise the three
// per-tier variables, a tier with none being the provider's own.
func modelsOf(env *config.Environment) [3]string {
	if env.Model != "" {
		return [3]string{env.Model, env.Model, env.Model}
	}
	return env.Models
}

// buildEnvironmentProvider builds the provider env names, with the key and the
// base URL env holds for it and with the defaults of a provider that is built
// (llm.BuildProvider). A variable of the provider's that cannot be used, and a
// provider with no key, are configuration errors that name the variables and
// never their values. A provider that needs no key (one registered with
// provider.RegisterKeyless) is built without one. The two providers that answer
// with a placeholder, "local" and "mock", are refused.
func buildEnvironmentProvider(env *config.Environment) (llm.Provider, error) {
	name := env.Provider
	if bind.PlaceholderProvider(name) {
		return nil, types.Newf(types.KindConfiguration, "client", "%s", bind.PlaceholderAdvice(name))
	}
	variables, known := providerVariables[name]
	names := append(append([]string(nil), variables.keys...), variables.baseURLs...)
	names = append(names, sharedKeyVariable)
	if err := env.Problem(names...); err != nil {
		return nil, err
	}

	settings := llm.ProviderConfig{BaseURL: env.BaseURLs[name], Models: modelsOf(env)}
	if !bind.Keyless(name) {
		settings.APIKey = env.Keys[name]
		if settings.APIKey == "" {
			return nil, noKeyError(name, variables.keys, known)
		}
	}
	provider, err := llm.BuildProvider(name, settings, 0)
	if err != nil {
		// A name no provider has, and a setting the provider refuses, are what
		// the environment says and not a fault of the library's.
		if types.KindOf(err) == types.KindUnknown {
			err = types.Classified(types.KindConfiguration, err)
		}
		return nil, err
	}
	return provider, nil
}

// noKeyError is the configuration error of an environment that names a provider
// and holds no key for it. It lists the variables the key was looked for in.
func noKeyError(provider string, keys []string, known bool) error {
	// The variable suggested first is the vendor's own, which is the one a
	// program that already calls the vendor has set; the library's form, the
	// other spellings and the shared key follow, in the order they are tried.
	var native string
	for _, key := range keys {
		if !strings.HasPrefix(key, "SCHEMAFLUX_") {
			native = key
			break
		}
	}
	var also []string
	for _, key := range keys {
		if key != native {
			also = append(also, key)
		}
	}
	also = append(also, sharedKeyVariable)
	if native == "" {
		native, also = also[0], also[1:]
	}
	message := "no provider: no key for \"" + provider + "\" is set; set " + native
	if len(also) > 0 {
		message += " (or " + strings.Join(also, ", ") + ")"
	}
	message += ", or give the client a Provider"
	return types.Newf(types.KindConfiguration, "client", "%s", message)
}

// environmentProvider is the provider of a client whose Provider is nil and
// whose environment has not been read yet: it reads and builds on its first
// use, so that a program may load a file of variables into its own environment
// after the library is linked and before it makes a call. It is what the
// default client holds until it is replaced. The environment it reads is the
// current one (currentEnvironment), which is the process's. Every method of it that the call
// path asks resolves the provider first and then answers as that provider does.
type environmentProvider struct {
	// checkTimeout is whether the client names no Timeout of its own, so that
	// a SCHEMAFLUX_TIMEOUT that cannot be used is its failure too.
	checkTimeout bool
}

// resolved is the provider the environment names, or the configuration error
// that says why there is none.
func (p *environmentProvider) resolved() (llm.Provider, error) {
	built, err := currentEnvironment().resolve()
	if err != nil {
		return nil, err
	}
	if p.checkTimeout {
		if _, err := currentEnvironment().timeout(); err != nil {
			return nil, err
		}
	}
	return built, nil
}

// Complete sends the request to the environment's provider, and fails with the
// configuration error before sending anything when there is none.
func (p *environmentProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	built, err := p.resolved()
	if err != nil {
		return llm.CompletionResponse{}, err
	}
	return built.Complete(ctx, req)
}

// Name is the name of the provider the environment selects.
func (p *environmentProvider) Name() string { return currentEnvironment().environment().Provider }

// EstimateCost is zero: the call path no longer asks for one.
func (p *environmentProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }

// RetryPolicy is the provider's own, and "not configured" while there is none.
func (p *environmentProvider) RetryPolicy() (int, time.Duration) {
	if built, err := p.resolved(); err == nil {
		return built.RetryPolicy()
	}
	return -1, 0
}

// Unwrap is the provider the environment names, or nil when there is none. It
// is how the call path looks through this one to the optional interfaces of the
// provider underneath: warming, the model sent for a request, decision limits.
func (p *environmentProvider) Unwrap() llm.Provider {
	built, err := p.resolved()
	if err != nil {
		return nil
	}
	return built
}

// ModelFor is the model the provider means by a tier. While there is no
// provider it is the library's built-in model for the provider's name, so that
// a call on a client with no key reaches the first thing that says why it
// cannot be made, and not a complaint about a model.
func (p *environmentProvider) ModelFor(tier types.Speed) string {
	if built, err := p.resolved(); err == nil {
		model, _ := llm.ModelFor(built, tier)
		return model
	}
	return config.BuiltInModel(p.Name(), tier)
}

// ModelSentFor is the model the provider sends for a requested one.
func (p *environmentProvider) ModelSentFor(requested string) string {
	if built, err := p.resolved(); err == nil {
		return llm.ModelSentBy(built, requested)
	}
	return requested
}

// CompleteStream streams through the provider the environment names. It yields
// the configuration error when there is none and the unsupported-capability
// error when the provider cannot stream.
func (p *environmentProvider) CompleteStream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamChunk, error] {
	built, err := p.resolved()
	if err != nil {
		return func(yield func(llm.StreamChunk, error) bool) { yield(llm.StreamChunk{}, err) }
	}
	if streaming, ok := built.(llm.StreamingProvider); ok {
		return streaming.CompleteStream(ctx, req)
	}
	name := built.Name()
	return func(yield func(llm.StreamChunk, error) bool) {
		yield(llm.StreamChunk{}, types.Newf(types.KindUnsupportedCapability, "llm.stream", "%s cannot stream", name))
	}
}
