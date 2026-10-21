// Package codex is a provider that runs the Codex command line program, so that a
// program can use the library with no API key at all: it answers as whoever ran
// `codex login`.
//
// It is a package of its own because it starts a process, and a program that does
// not import it carries no code that does. It is not one of the names
// provider.New builds: a program opts in, either by building one,
//
//	p, err := codex.New(codex.Options{Model: "gpt-6.1-sol"})
//	if err != nil {
//		return err
//	}
//	client := schemaflux.Client{Provider: p}
//
// or by registering the name once at start-up, after which provider.New("codex",
// provider.Config{}) builds one, and so does a client told SCHEMAFLUX_PROVIDER=codex:
//
//	if err := codex.Register(); err != nil {
//		return err
//	}
//
// # What it authenticates as
//
// Whoever ran `codex login`. The provider never reads or forwards an API key, and
// it removes the key variables from the environment of the process it starts, so
// that the program cannot fall back to a key and bill an account nobody chose.
//
// # What it costs
//
// Nothing the library can state. The tokens are real and the program reports
// them, but they are drawn against a plan and not invoiced per token, so the cost
// of a call is unpriced, which is not zero. A client's Budget never refuses a
// call on it, and a Record's Cost is not Priced.
//
// # The model has to be named
//
// Which models a login may run is a property of the program's version and the
// subscription, not of this library, so the provider has no built-in model for a
// tier. Name one with Options.Model, which every tier then means, with the
// client's Model, or with the call's, or a call fails with
// schemaflux.ErrConfiguration before anything is sent.
//
// # What it cannot do
//
// A request is answered from its prompts and its schema. The program takes one
// task on its standard input, so the system prompt and the user message arrive as
// one text, with no role between them: the instructions of an operation and the
// caller's data are in one channel, where a chat API keeps them apart. A caller
// weighing prompt injection should know that this provider has one channel.
//
// What the program cannot do is refused and not ignored: a request that asks for
// web search fails with schemaflux.ErrUnsupported before the program runs, and
// so does one that carries tools or a conversation. The program has no setting
// for the sampling temperature, the output ceiling or a cache key, and the library
// sends the first two on every request, so a request that sets them is answered and
// they are not applied. A decision in a request is ignored, as every provider that
// writes text ignores it: the prompts are answered.
//
// The program's own account of a failure, which is its prose and can quote the
// task, is kept out of the error's text and of its log value; Detail returns it,
// with the shapes of credentials removed.
package codex

import (
	"strings"
	"time"
	"unicode"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/bind"
	oldcodex "github.com/monstercameron/schemaflux/v2/internal/runtime/codex"
	"github.com/monstercameron/schemaflux/v2/provider"
)

const (
	// providerName is what the provider calls itself and what Register adds.
	providerName = "codex"

	opNew = "codex.new"
)

// Options says how to run the program. Its zero value runs the executable named
// by SCHEMAFLUX_CODEX_BIN, or `codex` on the PATH, with the program's own
// reasoning effort, no model of its own and the library's default retries; each
// field says what its zero value means.
type Options struct {
	// Executable is the program to run. Empty means the variable
	// SCHEMAFLUX_CODEX_BIN, and then `codex` on the PATH. New resolves it, so that
	// a program that is not installed is reported when the provider is built and
	// not inside the first request.
	Executable string

	// Model is the model every tier means, and the model a request that names none
	// is run on. Empty means the provider has no model of its own: the client's
	// Model or the call's must name one.
	Model string

	// Effort is the reasoning effort the program is given, for a request that sets
	// none of its own. Empty leaves the program's own default. A value that is not
	// one of the library's efforts is an error from New.
	Effort schemaflux.Effort

	// Retries is how many times a request that failed is sent again. Zero means
	// the library's default, and a negative value means never.
	Retries int

	// RetryBackoff is the wait before the first of those repeats. Zero means the
	// library's default; a negative value is an error from New.
	RetryBackoff time.Duration
}

// New returns a provider that runs the Codex program, or an error of kind
// schemaflux.ErrConfiguration when it cannot: the program is not installed (the
// error says how to name it), Effort is not an effort of the library's,
// RetryBackoff is negative, or Model or Executable holds a control character or
// a Model begins with a dash, which the program would read as a flag. Nothing is
// run to find out.
//
// The provider is safe for concurrent use: each request starts a process of its
// own, in a working directory of its own that is removed afterwards.
func New(opts Options) (schemaflux.Provider, error) {
	return build(opts, [3]string{opts.Model, opts.Model, opts.Model})
}

// Register makes the provider selectable as "codex", through provider.New and
// through a client that reads SCHEMAFLUX_PROVIDER, so that a program can choose
// it from configuration. It is registered as one that needs no API key
// (provider.RegisterKeyless), so SCHEMAFLUX_PROVIDER=codex works with no key
// variable set. It is a function and not an init, so that a program that
// imports the package for its types gains no provider it did not ask for.
//
// A provider built by name has the program's defaults, with these settings of
// provider.Config applied: Retries and RetryBackoff, and Models, whose Smart,
// Fast and Quick are the models of the three tiers. Every setting of the Config
// that the program cannot honour is an error from provider.New and not ignored:
// BaseURL, OrgID, Headers, Store, ServiceTier, HTTPClient and Endpoint. An APIKey
// is the one it ignores without an error, because a client that builds its
// provider from the environment is handed whatever key the variables hold, and
// the provider never sends one. Timeout is not applied either: no HTTP call is
// made, and the call's context bounds a turn.
//
// Register returns an error of kind schemaflux.ErrConfiguration if the name is
// taken, which it is after the first successful call. There is no way to take a
// name back.
func Register() error {
	return provider.RegisterKeyless(providerName, func(cfg provider.Config) (schemaflux.Provider, error) {
		if setting := unsupportedSetting(cfg); setting != "" {
			return nil, &schemaflux.Error{
				Kind: schemaflux.ErrConfiguration, Op: opNew, Provider: providerName,
				Message: "Config." + setting + " is set, and the Codex program cannot use it: it takes no such setting",
			}
		}
		return build(Options{Retries: cfg.Retries, RetryBackoff: cfg.RetryBackoff},
			[3]string{cfg.Models.Smart, cfg.Models.Fast, cfg.Models.Quick})
	})
}

// unsupportedSetting names the first field of cfg that is set and that the
// program has no use for, or returns "".
func unsupportedSetting(cfg provider.Config) string {
	switch {
	case cfg.BaseURL != "":
		return "BaseURL"
	case cfg.OrgID != "":
		return "OrgID"
	case len(cfg.Headers) > 0:
		return "Headers"
	case cfg.Store:
		return "Store"
	case cfg.ServiceTier != "":
		return "ServiceTier"
	case cfg.HTTPClient != nil:
		return "HTTPClient"
	case cfg.Endpoint != nil:
		return "Endpoint"
	}
	return ""
}

// build checks opts, builds the provider over the codex package and gives it the
// models of its tiers (smart, fast and quick, in that order).
func build(opts Options, models [3]string) (schemaflux.Provider, error) {
	if err := check(opts); err != nil {
		return nil, err
	}
	// The convention of the codex package is the old one, a positive count, zero
	// for none and a negative number for "no opinion". The convention of this
	// library is the other way about, as provider.Config has it.
	retries := opts.Retries
	switch {
	case retries == 0:
		retries = -1
	case retries < 0:
		retries = 0
	}
	built, err := oldcodex.New(oldcodex.Options{
		Executable:      opts.Executable,
		Model:           opts.Model,
		ReasoningEffort: string(opts.Effort),
		MaxRetries:      retries,
		RetryBackoff:    opts.RetryBackoff,
	})
	if err != nil {
		return nil, bind.ConvertError(opNew, err)
	}
	adapted, ok := bind.FromInternal(built).(schemaflux.Provider)
	if !ok {
		return nil, &schemaflux.Error{Kind: schemaflux.ErrInternal, Op: opNew, Provider: providerName, Message: "the root package registered no adapter for a built-in provider"}
	}
	if models == [3]string{} {
		return adapted, nil
	}
	return &tiered{Provider: adapted, models: models}, nil
}

// check refuses the settings no provider could be built from.
func check(opts Options) error {
	refuse := func(format string) error {
		return &schemaflux.Error{Kind: schemaflux.ErrConfiguration, Op: opNew, Provider: providerName, Message: format}
	}
	switch {
	case opts.Effort != "" && !validEffort(opts.Effort):
		return refuse("Options.Effort is not one of none, minimal, low, medium, high, xhigh and max")
	case opts.RetryBackoff < 0:
		return refuse("Options.RetryBackoff is negative; zero means the library's default")
	case hasControl(opts.Executable):
		return refuse("Options.Executable holds a control character")
	case hasControl(opts.Model):
		return refuse("Options.Model holds a control character")
	case strings.HasPrefix(strings.TrimSpace(opts.Model), "-"):
		return refuse("Options.Model begins with a dash, which the program would read as a flag")
	}
	return nil
}

func validEffort(e schemaflux.Effort) bool {
	switch e {
	case schemaflux.EffortNone, schemaflux.EffortMinimal, schemaflux.EffortLow, schemaflux.EffortMedium,
		schemaflux.EffortHigh, schemaflux.EffortXHigh, schemaflux.EffortMax:
		return true
	}
	return false
}

func hasControl(s string) bool {
	return strings.IndexFunc(s, unicode.IsControl) >= 0
}

// tiered is the provider with the models of its tiers: it adds ModelFor to the
// provider it wraps and leaves everything else to it, which the library finds
// through Unwrap (provider.Unwrapper).
type tiered struct {
	schemaflux.Provider
	models [3]string
}

// ModelFor is the model the provider means by tier, or the empty string, which
// is no model of its own, for a tier the models do not name.
func (t *tiered) ModelFor(tier schemaflux.Tier) string {
	switch tier {
	case schemaflux.TierSmart:
		return t.models[0]
	case schemaflux.TierFast:
		return t.models[1]
	case schemaflux.TierQuick:
		return t.models[2]
	}
	return ""
}

// Unwrap is the provider under this one.
func (t *tiered) Unwrap() schemaflux.Provider { return t.Provider }
