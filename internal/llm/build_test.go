package llm

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

const buildTestKey = "sk-build-test-key-must-not-appear"

// builtSettings is what a built provider will do: how many times it sends a
// failed request again, how long it waits, and how long its HTTP client lets one
// call take. The timeout lives in the client of a provider that talks HTTP and in
// the configuration of the one that does not.
func builtSettings(t *testing.T, provider Provider) (retries int, backoff, timeout time.Duration) {
	t.Helper()
	retries, backoff = provider.RetryPolicy()
	var client *http.Client
	switch built := provider.(type) {
	case *OpenAIProvider:
		client = built.httpClient
	case *AnthropicProvider:
		client = built.httpClient
	case *OpenAICompatibleProvider:
		client = built.httpClient
	case *OpenRouterProvider:
		client = built.httpClient
	case *CerebrasProvider:
		client = built.httpClient
	case *DeepSeekProvider:
		client = built.httpClient
	case *QwenProvider:
		client = built.httpClient
	case *ZAIProvider:
		client = built.httpClient
	case *LocalProvider:
		return retries, backoff, built.config.Timeout
	default:
		t.Fatalf("a %T is not a built-in provider", provider)
	}
	return retries, backoff, client.Timeout
}

func TestBuildProviderDefaults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		provider    string
		config      ProviderConfig
		retries     int
		wantRetries int
		wantBackoff time.Duration
		wantTimeout time.Duration
	}{
		{name: "nothing set gives the three defaults", provider: "openai",
			wantRetries: 3, wantBackoff: time.Second, wantTimeout: 30 * time.Second},
		{name: "retries of one", provider: "openai", retries: 1,
			wantRetries: 1, wantBackoff: time.Second, wantTimeout: 30 * time.Second},
		{name: "retries of five", provider: "openai", retries: 5,
			wantRetries: 5, wantBackoff: time.Second, wantTimeout: 30 * time.Second},
		{name: "a negative number of retries is none", provider: "openai", retries: -1,
			wantRetries: 0, wantBackoff: time.Second, wantTimeout: 30 * time.Second},
		{name: "any negative number of retries is none", provider: "openai", retries: -100,
			wantRetries: 0, wantBackoff: time.Second, wantTimeout: 30 * time.Second},
		{name: "the configuration's own retries are not read", provider: "openai",
			config:  ProviderConfig{MaxRetries: 9},
			retries: 0, wantRetries: 3, wantBackoff: time.Second, wantTimeout: 30 * time.Second},
		{name: "the configuration's own retries are not read for none", provider: "openai",
			config:  ProviderConfig{MaxRetries: 9},
			retries: -1, wantRetries: 0, wantBackoff: time.Second, wantTimeout: 30 * time.Second},
		{name: "the argument beats the configuration's retries", provider: "openai",
			config:  ProviderConfig{MaxRetries: 9},
			retries: 2, wantRetries: 2, wantBackoff: time.Second, wantTimeout: 30 * time.Second},
		{name: "a timeout of the configuration's own", provider: "openai",
			config:      ProviderConfig{Timeout: 5 * time.Second},
			wantRetries: 3, wantBackoff: time.Second, wantTimeout: 5 * time.Second},
		{name: "a negative timeout is not replaced: net/http reads it as no bound", provider: "openai",
			config:      ProviderConfig{Timeout: -time.Second},
			wantRetries: 3, wantBackoff: time.Second, wantTimeout: -time.Second},
		{name: "a backoff of the configuration's own", provider: "openai",
			config:      ProviderConfig{RetryBackoff: 2 * time.Second},
			wantRetries: 3, wantBackoff: 2 * time.Second, wantTimeout: 30 * time.Second},
		{name: "a negative backoff is unset, as CallLLM reads it", provider: "openai",
			config:      ProviderConfig{RetryBackoff: -time.Second},
			wantRetries: 3, wantBackoff: time.Second, wantTimeout: 30 * time.Second},
		{name: "all three set", provider: "openai",
			config:  ProviderConfig{Timeout: 7 * time.Second, RetryBackoff: 250 * time.Millisecond},
			retries: 4, wantRetries: 4, wantBackoff: 250 * time.Millisecond, wantTimeout: 7 * time.Second},
		{name: "all three set, none for retries", provider: "openai",
			config:  ProviderConfig{Timeout: time.Minute, RetryBackoff: time.Millisecond},
			retries: -1, wantRetries: 0, wantBackoff: time.Millisecond, wantTimeout: time.Minute},
		{name: "anthropic", provider: "anthropic",
			wantRetries: 3, wantBackoff: time.Second, wantTimeout: 30 * time.Second},
		{name: "openrouter", provider: "openrouter",
			wantRetries: 3, wantBackoff: time.Second, wantTimeout: 30 * time.Second},
		{name: "cerebras", provider: "cerebras", retries: 2,
			wantRetries: 2, wantBackoff: time.Second, wantTimeout: 30 * time.Second},
		{name: "deepseek", provider: "deepseek",
			wantRetries: 3, wantBackoff: time.Second, wantTimeout: 30 * time.Second},
		{name: "qwen", provider: "qwen", retries: -1,
			wantRetries: 0, wantBackoff: time.Second, wantTimeout: 30 * time.Second},
		{name: "zai", provider: "zai",
			wantRetries: 3, wantBackoff: time.Second, wantTimeout: 30 * time.Second},
		{name: "the local provider", provider: "local",
			wantRetries: 3, wantBackoff: time.Second, wantTimeout: 30 * time.Second},
		{name: "the mock provider is the local one", provider: "mock", retries: 1,
			wantRetries: 1, wantBackoff: time.Second, wantTimeout: 30 * time.Second},
		{name: "a name in capitals and space", provider: "  OpenAI ",
			wantRetries: 3, wantBackoff: time.Second, wantTimeout: 30 * time.Second},
		{name: "an openai-compatible base URL given in the configuration", provider: "openrouter",
			config:      ProviderConfig{BaseURL: "https://gateway.example.test/v1"},
			wantRetries: 3, wantBackoff: time.Second, wantTimeout: 30 * time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			config := test.config
			config.APIKey = buildTestKey

			provider, err := BuildProvider(test.provider, config, test.retries)
			if err != nil {
				t.Fatalf("BuildProvider: %v", err)
			}
			retries, backoff, timeout := builtSettings(t, provider)
			if retries != test.wantRetries || backoff != test.wantBackoff || timeout != test.wantTimeout {
				t.Fatalf("retries %d, backoff %v, timeout %v; want %d, %v, %v",
					retries, backoff, timeout, test.wantRetries, test.wantBackoff, test.wantTimeout)
			}
		})
	}
}

func TestBuildProviderDefaultsAreThoseOfTheClient(t *testing.T) {
	t.Parallel()

	// The numbers (*Client).providerConfig in internal/api/client applies today.
	if DefaultHTTPTimeout != 30*time.Second || DefaultRetries != 3 || DefaultRetryBackoff != time.Second {
		t.Fatalf("defaults are %v, %d, %v", DefaultHTTPTimeout, DefaultRetries, DefaultRetryBackoff)
	}
}

func TestBuildProviderLeavesTheCallersClientAlone(t *testing.T) {
	t.Parallel()

	own := &http.Client{Timeout: 3 * time.Second}
	provider, err := BuildProvider("openai", ProviderConfig{APIKey: buildTestKey, HTTPClient: own}, 0)
	if err != nil {
		t.Fatalf("BuildProvider: %v", err)
	}
	if own.Timeout != 3*time.Second {
		t.Fatalf("the caller's client now has a timeout of %v", own.Timeout)
	}
	if built := provider.(*OpenAIProvider); built.httpClient != own {
		t.Fatal("the provider does not use the caller's client")
	}
	if got := provider.(*OpenAIProvider).config.Timeout; got != DefaultHTTPTimeout {
		// It bounds each call's context instead (callContext), since the
		// caller's client is used as it is.
		t.Fatalf("the configuration's timeout is %v, want %v", got, DefaultHTTPTimeout)
	}
}

func TestBuildProviderKeepsTheRestOfTheConfiguration(t *testing.T) {
	t.Parallel()

	provider, err := BuildProvider("openrouter", ProviderConfig{
		APIKey:       buildTestKey,
		BaseURL:      "https://gateway.example.test/v1/",
		ExtraHeaders: map[string]string{"X-Team": "a"},
		Debug:        true,
		OrgID:        "org",
	}, 0)
	if err != nil {
		t.Fatalf("BuildProvider: %v", err)
	}
	config := provider.(*OpenRouterProvider).config
	if config.APIKey != buildTestKey || config.BaseURL != "https://gateway.example.test/v1" ||
		config.ExtraHeaders["X-Team"] != "a" || !config.Debug || config.OrgID != "org" {
		t.Fatalf("the configuration was not carried: %+v", config)
	}
}

func TestBuildProviderRefusalsCarryNoKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider string
		config   ProviderConfig
	}{
		{"a provider that does not exist", "no-such-provider", ProviderConfig{APIKey: buildTestKey}},
		{"a service tier that does not exist", "openai", ProviderConfig{APIKey: buildTestKey, ServiceTier: "ludicrous"}},
		{"a service tier on a provider with none", "anthropic", ProviderConfig{APIKey: buildTestKey, ServiceTier: "priority"}},
		{"no key", "openai", ProviderConfig{BaseURL: "https://gateway.example.test/v1"}},
		{"an endpoint the policy refuses", "openai", ProviderConfig{
			APIKey: buildTestKey, BaseURL: "http://gateway.example.test/v1",
			EndpointPolicy: &types.EndpointPolicy{AllowedSchemes: []string{"https"}},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			provider, err := BuildProvider(test.provider, test.config, 0)
			if err == nil {
				t.Fatalf("BuildProvider built %T", provider)
			}
			if provider != nil {
				t.Fatalf("a provider came back beside the error: %T", provider)
			}
			if strings.Contains(err.Error(), buildTestKey) || strings.Contains(err.Error(), "gateway.example.test") {
				t.Fatalf("the error holds a value: %v", err)
			}
		})
	}
}

// The constructors apply none of the defaults: a provider made by CreateProvider
// from an empty configuration sends a failed request once and has no HTTP timeout.
// The old root builds its own with (*Client).providerConfig and depends on it.
func TestCreateProviderStillAppliesNone(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"openai", "anthropic", "openrouter", "cerebras", "deepseek", "qwen", "zai", "local", "mock"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			provider, err := CreateProvider(name, ProviderConfig{APIKey: buildTestKey})
			if err != nil {
				t.Fatalf("CreateProvider: %v", err)
			}
			retries, backoff, timeout := builtSettings(t, provider)
			if retries != 0 || backoff != 0 || timeout != 0 {
				t.Fatalf("CreateProvider applied retries %d, backoff %v, timeout %v", retries, backoff, timeout)
			}
		})
	}

	t.Run("and keeps the configuration's own", func(t *testing.T) {
		t.Parallel()
		provider, err := CreateProvider("openai", ProviderConfig{
			APIKey: buildTestKey, MaxRetries: 7, RetryBackoff: 3 * time.Second, Timeout: 11 * time.Second,
		})
		if err != nil {
			t.Fatalf("CreateProvider: %v", err)
		}
		retries, backoff, timeout := builtSettings(t, provider)
		if retries != 7 || backoff != 3*time.Second || timeout != 11*time.Second {
			t.Fatalf("got %d, %v, %v", retries, backoff, timeout)
		}
	})
}
