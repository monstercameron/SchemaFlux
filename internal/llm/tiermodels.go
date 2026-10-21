package llm

import (
	"github.com/monstercameron/schemaflux/v2/internal/config"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// TierModels is implemented by a provider that knows which model a tier means
// for it. The built-in providers do: a tier names a model of the provider's own,
// and the model table that used to be consulted by provider name, from anywhere,
// is now asked of the provider the call is going to.
type TierModels interface {
	// ModelFor returns the model for a tier, or "" when it names none.
	ModelFor(tier types.Speed) string
}

// ModelFor asks provider, and then what it wraps, for the model of a tier. It
// reports false when none of them names one, which is a provider with no model
// table of its own (a custom one, or a built-in one such as qwen with no mapping)
// and not an error: the caller falls back to the process-wide table, or fails
// with the error that says what to configure.
//
// A wrapper is looked through as Warm looks through one (Unwrap() Provider), so
// a provider behind middleware is asked. A wrapper that answers with the empty
// string is looked past, not taken for an answer.
func ModelFor(provider Provider, tier types.Speed) (string, bool) {
	// A bound, so that a wrapper that returns itself cannot hold this for ever.
	for depth := 0; provider != nil && depth < 64; depth++ {
		if answerer, ok := provider.(TierModels); ok {
			if model := answerer.ModelFor(tier); model != "" {
				return model, true
			}
		}
		wrapper, ok := provider.(providerUnwrapper)
		if !ok {
			return "", false
		}
		provider = wrapper.Unwrap()
	}
	return "", false
}

// tierModel is the model a built-in provider with this name and this setting
// answers for tier: the setting's entry for the tier when it has one, and
// otherwise the built-in model for the provider (config.BuiltInModel). A tier
// that is not one of the three has no entry in the setting and gets the
// built-in answer, as config.GetModel gives one.
func tierModel(provider string, setting [3]string, tier types.Speed) string {
	if tier >= types.Smart && tier <= types.Quick {
		if model := setting[tier-types.Smart]; model != "" {
			return model
		}
	}
	return config.BuiltInModel(provider, tier)
}

// modelSetting is the models a provider was configured with, smart, fast and
// quick, where an empty entry means the built-in one.
func (c ProviderConfig) modelSetting() [3]string { return c.Models }

// ModelFor returns the model OpenAI answers for a tier.
func (provider *OpenAIProvider) ModelFor(tier types.Speed) string {
	return tierModel("openai", provider.config.modelSetting(), tier)
}

// ModelFor returns the model Anthropic answers for a tier.
func (provider *AnthropicProvider) ModelFor(tier types.Speed) string {
	return tierModel("anthropic", provider.config.modelSetting(), tier)
}

// ModelFor returns the model an OpenAI-compatible provider answers for a tier,
// by the name it was built under. OpenRouter, Cerebras, DeepSeek, Qwen and Z.ai
// hold one of these and answer through it.
func (provider *OpenAICompatibleProvider) ModelFor(tier types.Speed) string {
	return tierModel(provider.name, provider.config.modelSetting(), tier)
}

// ModelFor returns the model the local provider answers for a tier.
func (provider *LocalProvider) ModelFor(tier types.Speed) string {
	return tierModel("local", provider.config.modelSetting(), tier)
}
