package llm

import (
	"strings"

	"github.com/monstercameron/schemaflux/internal/types"
)

// LAT-008, round 13. A service tier is OpenAI's: the Responses API takes it, and
// nothing else this library talks to does. A tier set on a provider that will
// never send it is a configuration the caller meant to have an effect and that
// has none, so it is refused when the provider is built and not ignored. It was
// ignored: a typo in a tier on Anthropic or on a chat-completions provider was
// never caught, and neither was a tier set for an OpenAI-shaped server at
// another address, which was sent a parameter it may not know.

// serviceTierNotSent is the error for a service tier configured on a provider
// that never sends one. It names the tiers there are and not the one that was
// set, which is not this library's to repeat.
func serviceTierNotSent(provider string, config ProviderConfig, reason string) error {
	if strings.TrimSpace(config.ServiceTier) == "" {
		return nil
	}
	return types.Newf(types.KindConfiguration, "llm.configure",
		"%s: a service tier is set, and %s. A tier is sent only to OpenAI's own Responses API, where it takes %s",
		provider, reason, strings.Join(serviceTiers, ", "))
}

// refuseServiceTier refuses a tier on a provider that has no such parameter.
func refuseServiceTier(provider string, config ProviderConfig) error {
	return serviceTierNotSent(provider, config, "this provider has no such parameter")
}

// requireOpenAIEndpointForTier refuses a tier on an OpenAI provider whose base URL is
// not OpenAI's. A server that speaks the Responses API at another address, a
// gateway or an Azure deployment, is not known to take the parameter.
func requireOpenAIEndpointForTier(config ProviderConfig) error {
	if isOpenAIEndpoint(config.BaseURL) {
		return nil
	}
	return serviceTierNotSent("openai", config, "the base URL is not OpenAI's own endpoint")
}
