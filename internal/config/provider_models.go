package config

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/monstercameron/schemaflux/internal/types"
)

// ErrNoModelMapping reports that a provider has no model for at least one tier
// and no environment override supplying one.
//
// It exists so the failure can be recognised rather than string-matched:
// GetModel returns the empty string in that case, and the empty string had to be
// turned back into a sentence at every call site that cared.
var ErrNoModelMapping = errors.New("provider has no model mapping")

// noModelAdvice is the way out of a provider with no model, in the order a
// caller is likely to reach for them. The environment variables are kept in the
// words callers and tests already match on.
const noModelAdvice = "name one with Client.WithModel (and make the call on Client.Context), " +
	"or with Model on the call's options, or set SCHEMAFLUX_MODEL, or SCHEMAFLUX_MODEL_SMART / _FAST / _QUICK"

// NoDefaultModelError reports that a call named no model and the library has
// none to use for provider.
//
// A provider whose name is not in the model table is not a provider the library
// can guess for: a custom one injected with WithProviderInstance, a gateway, or
// a built-in one such as qwen with no tier mapping. The error says what to do,
// which the inline message in CallLLM said only in terms of the environment, so
// a caller using a client had to find Client.WithModel by reading source.
//
// It is classified: types.KindOf reports a configuration error, errors.Is
// matches both types.ErrConfiguration and ErrNoModelMapping, and the message
// still contains "no default model for provider", the phrase the text fallback
// of the error classifier and the existing tests match on.
func NoDefaultModelError(provider string) error {
	return &types.OperationError{
		Kind: types.KindConfiguration,
		Op:   "model resolution",
		Message: fmt.Sprintf("no default model for provider %q; %s. Providers with a built-in mapping: %s",
			provider, noModelAdvice, strings.Join(KnownProviders(), ", ")),
		Cause: ErrNoModelMapping,
	}
}

// providerModels maps a provider to the model it should use at each tier.
//
// Before this existed, GetModel special-cased three providers and let every
// other one fall through to the OpenAI defaults. So `WithProvider("deepseek")`
// — the README's own example — sent `model: "gpt-5.4"` to api.deepseek.com and
// got a 400 that reads as the caller's mistake. Adding a provider now means
// adding a row here, and a provider with no row gets no model rather than
// somebody else's.
var providerModels = map[string]map[types.Speed]string{
	"openrouter": {
		types.Smart: "openai/gpt-4o",
		types.Fast:  "openai/gpt-4o-mini",
		types.Quick: "openai/gpt-4o-mini",
	},
	// Cerebras runs one model across all three tiers on purpose. The tiers
	// exist to trade accuracy against latency, and Cerebras' pitch is that
	// gemma-4-31b is already the fast option -- there is no cheaper sibling
	// whose accuracy loss buys anything here. Mapping Quick to a smaller model
	// would be inventing a trade-off that the benchmark has not shown.
	"cerebras": {
		types.Smart: "gemma-4-31b",
		types.Fast:  "gemma-4-31b",
		types.Quick: "gemma-4-31b",
	},
	"anthropic": {
		types.Smart: "claude-3-5-sonnet-20240620",
		types.Fast:  "claude-3-haiku-20240307",
		types.Quick: "claude-3-haiku-20240307",
	},
	"deepseek": {
		types.Smart: "deepseek-reasoner",
		types.Fast:  "deepseek-chat",
		types.Quick: "deepseek-chat",
	},
	"groq": {
		types.Smart: "llama-3.3-70b-versatile",
		types.Fast:  "llama-3.1-8b-instant",
		types.Quick: "llama-3.1-8b-instant",
	},
	"mistral": {
		types.Smart: "mistral-large-latest",
		types.Fast:  "mistral-small-latest",
		types.Quick: "mistral-small-latest",
	},
	"local": {
		types.Smart: "local-mock",
		types.Fast:  "local-mock",
		types.Quick: "local-mock",
	},

	// The decision providers (internal/llm, systemone.go). Each answers decisions
	// with one model, so the three tiers are the model the preset sends: a client
	// whose only provider is one of these has a model without being told one. This
	// package cannot read the presets, since internal/llm imports it, so the names
	// are repeated here and a test in internal/api/client holds them to the
	// presets'. Through OpenRouter the alias is "~typesafe/jev-latest", which
	// OpenRouter resolves to the newest version.
	"jev": {
		types.Smart: "jev-latest",
		types.Fast:  "jev-latest",
		types.Quick: "jev-latest",
	},
	"jev-openrouter": {
		types.Smart: "~typesafe/jev-latest",
		types.Fast:  "~typesafe/jev-latest",
		types.Quick: "~typesafe/jev-latest",
	},
	"jev-litellm": {
		types.Smart: "jev-latest",
		types.Fast:  "jev-latest",
		types.Quick: "jev-latest",
	},
}

// normaliseProviderName folds the spellings a caller might use into the one the
// map is keyed by.
func normaliseProviderName(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

// isOpenAIProvider reports whether a provider name means OpenAI itself, which
// is the only one entitled to the OpenAI tier defaults in config.go.
func isOpenAIProvider(provider string) bool {
	switch normaliseProviderName(provider) {
	case "", "openai":
		return true
	default:
		return false
	}
}

// KnownProviders returns the providers with a model mapping, sorted. OpenAI is
// included even though its models come from the tier constants rather than the
// map.
func KnownProviders() []string {
	names := make([]string, 0, len(providerModels)+1)
	names = append(names, "openai")
	for name := range providerModels {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// HasModelMapping reports whether GetModel can resolve a model for a provider
// without the caller setting SCHEMAFLUX_MODEL.
func HasModelMapping(provider string) bool {
	if isOpenAIProvider(provider) {
		return true
	}
	_, known := providerModels[normaliseProviderName(provider)]
	return known
}

// ValidateModelResolution reports whether every tier resolves to a model for
// this provider, counting environment overrides.
//
// GetModel already fails closed, but it fails at the first call — after the
// client is built, after the caller believes they are configured, and inside
// whatever operation happened to run first. The same check at construction says
// so while the caller is still looking at their configuration.
//
// All three tiers must resolve, not just one. A caller who sets only
// SCHEMAFLUX_MODEL_SMART has a working Smart path and two broken ones, and the
// point of checking early is to find that before an operation defaulting to Fast
// discovers it in production.
func ValidateModelResolution(provider string) error {
	var missing []string
	for _, tier := range []types.Speed{types.Smart, types.Fast, types.Quick} {
		if GetModel(tier, provider) == "" {
			missing = append(missing, tierName(tier))
		}
	}
	if len(missing) == 0 {
		return nil
	}

	// The same classified error NoDefaultModelError gives at call time, so the
	// construction-time and call-time reports of one fault have one identity:
	// types.KindOf reports a configuration error, and errors.Is matches both
	// types.ErrConfiguration and ErrNoModelMapping.
	return &types.OperationError{
		Kind: types.KindConfiguration,
		Op:   "model resolution",
		Message: fmt.Sprintf("provider %q resolves no model for %s; %s. Providers with a built-in mapping: %s",
			provider, strings.Join(missing, ", "), noModelAdvice, strings.Join(KnownProviders(), ", ")),
		Cause: ErrNoModelMapping,
	}
}

func tierName(tier types.Speed) string {
	switch tier {
	case types.Smart:
		return "Smart"
	case types.Fast:
		return "Fast"
	case types.Quick:
		return "Quick"
	default:
		return "unknown tier"
	}
}
