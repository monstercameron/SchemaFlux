package llm

import (
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// TestNewOpenAIProviderRequiresAPIKey pins the missing-key branch directly
// (by message), which no existing test asserted on for OpenAI specifically.
func TestNewOpenAIProviderRequiresAPIKey(t *testing.T) {
	_, err := NewOpenAIProvider(ProviderConfig{})
	if err == nil || !strings.Contains(err.Error(), "OpenAI API key is required") {
		t.Errorf("NewOpenAIProvider(no key) = %v, want the documented message", err)
	}
}

// TestNewAnthropicProviderRequiresAPIKey mirrors the above for Anthropic.
func TestNewAnthropicProviderRequiresAPIKey(t *testing.T) {
	_, err := NewAnthropicProvider(ProviderConfig{})
	if err == nil || !strings.Contains(err.Error(), "anthropic API key is required") {
		t.Errorf("NewAnthropicProvider(no key) = %v, want the documented message", err)
	}
}

// TestNewAnthropicProviderDefaultBaseURL proves an unset BaseURL falls back
// to the documented Anthropic endpoint.
func TestNewAnthropicProviderDefaultBaseURL(t *testing.T) {
	provider, err := NewAnthropicProvider(ProviderConfig{APIKey: "k"})
	if err != nil {
		t.Fatalf("NewAnthropicProvider: %v", err)
	}
	if provider.baseURL != "https://api.anthropic.com" {
		t.Errorf("baseURL = %q, want the documented default", provider.baseURL)
	}
}

// TestNewAnthropicProviderEndpointPolicy proves the endpoint policy check
// runs before construction, the same guarantee SC-007's tests pin for
// OpenAI.
func TestNewAnthropicProviderEndpointPolicy(t *testing.T) {
	_, err := NewAnthropicProvider(ProviderConfig{
		APIKey:         "k",
		BaseURL:        "https://example.invalid",
		EndpointPolicy: &types.EndpointPolicy{}, // refuses everything
	})
	if err == nil {
		t.Fatal("NewAnthropicProvider succeeded despite an EndpointPolicy that allows nothing")
	}
}

// TestNewOpenRouterProviderRequiresAPIKey pins OpenRouter's own key check,
// which runs before newOpenAICompatibleProvider is ever called.
func TestNewOpenRouterProviderRequiresAPIKey(t *testing.T) {
	_, err := NewOpenRouterProvider(ProviderConfig{})
	if err == nil || !strings.Contains(err.Error(), "OpenRouter API key is required") {
		t.Errorf("NewOpenRouterProvider(no key) = %v, want the documented message", err)
	}
}

// TestNewOpenRouterProviderDefaults proves the documented base URL and that
// a failure from the shared newOpenAICompatibleProvider path (an
// EndpointPolicy refusal) propagates back out of the OpenRouter-specific
// wrapper.
func TestNewOpenRouterProviderDefaults(t *testing.T) {
	provider, err := NewOpenRouterProvider(ProviderConfig{APIKey: "k"})
	if err != nil {
		t.Fatalf("NewOpenRouterProvider: %v", err)
	}
	if provider.config.BaseURL != "https://openrouter.ai/api/v1" {
		t.Errorf("BaseURL = %q, want the documented OpenRouter endpoint", provider.config.BaseURL)
	}

	_, err = NewOpenRouterProvider(ProviderConfig{
		APIKey:         "k",
		BaseURL:        "https://example.invalid", // a policy has nothing to judge against an empty BaseURL
		EndpointPolicy: &types.EndpointPolicy{},   // refuses everything
	})
	if err == nil {
		t.Error("NewOpenRouterProvider succeeded despite an EndpointPolicy that allows nothing")
	}
}

// TestNewOpenAICompatibleProviderRequiresAPIKeyAtSendTime proves the public
// NewOpenAICompatibleProvider constructor checks BaseURL but delegates the
// API-key check down to newOpenAIClient inside the shared helper -- a
// missing key still fails construction, just via a different message path
// than the vendor-specific wrappers (DeepSeek/Qwen/Z.ai/Cerebras) which check
// it themselves first.
func TestNewOpenAICompatibleProviderRequiresAPIKeyAtSendTime(t *testing.T) {
	_, err := NewOpenAICompatibleProvider("testvendor", ProviderConfig{BaseURL: "https://example.invalid"})
	if err == nil {
		t.Fatal("NewOpenAICompatibleProvider succeeded with no API key")
	}
	if !strings.Contains(err.Error(), "testvendor") || !strings.Contains(err.Error(), "API key is required") {
		t.Errorf("error = %v, want it to name the vendor and say a key is required", err)
	}
}

// TestNewOpenAICompatibleProviderEndpointPolicy proves the shared endpoint
// policy check applies to a directly-constructed compatible provider too,
// not only to the vendor-specific wrappers.
func TestNewOpenAICompatibleProviderEndpointPolicy(t *testing.T) {
	_, err := NewOpenAICompatibleProvider("testvendor", ProviderConfig{
		APIKey:         "k",
		BaseURL:        "https://example.invalid",
		EndpointPolicy: &types.EndpointPolicy{}, // refuses everything
	})
	if err == nil {
		t.Fatal("NewOpenAICompatibleProvider succeeded despite an EndpointPolicy that allows nothing")
	}
}
