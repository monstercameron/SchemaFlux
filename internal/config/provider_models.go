package config

import (
	"sort"
	"strings"

	"github.com/monstercameron/schemaflux/internal/types"
)

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
}

// normaliseProviderName folds the spellings a caller might use into the one the
// map is keyed by.
func normaliseProviderName(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

// isOpenAIProvider reports whether a provider name means OpenAI itself, which
// is the only one entitled to the gpt-5.6 tier defaults.
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
