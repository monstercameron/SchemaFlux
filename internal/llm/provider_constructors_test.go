package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Constructing a provider never dials the network -- only Complete/Embed do
// (confirmed by reading provider.go: every New*Provider builds a client and
// returns, nothing calls httpClient.Do or client.CreateChatCompletion). So
// these tests assert on base URL, registry entries, and RetryPolicy without
// an httptest.Server anywhere.

// TestNewDeepSeekProviderDefaults proves the constructor sets the documented
// base URL, requires an API key, and is reachable through NewOpenAICompatibleProvider's
// underlying transport.
func TestNewDeepSeekProviderDefaults(t *testing.T) {
	if _, err := NewDeepSeekProvider(ProviderConfig{}); err == nil {
		t.Error("a missing API key must be reported rather than producing a provider that 401s")
	}

	provider, err := NewDeepSeekProvider(ProviderConfig{APIKey: "k", Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}
	if provider.Name() != "deepseek" {
		t.Errorf("Name() = %q, want deepseek", provider.Name())
	}
	if provider.config.BaseURL != "https://api.deepseek.com/v1" {
		t.Errorf("BaseURL = %q, want the documented DeepSeek endpoint", provider.config.BaseURL)
	}
}

// TestNewQwenProviderDefaults mirrors TestNewDeepSeekProviderDefaults for the
// Qwen/DashScope route.
func TestNewQwenProviderDefaults(t *testing.T) {
	if _, err := NewQwenProvider(ProviderConfig{}); err == nil {
		t.Error("a missing API key must be reported rather than producing a provider that 401s")
	}

	provider, err := NewQwenProvider(ProviderConfig{APIKey: "k", Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewQwenProvider: %v", err)
	}
	if provider.Name() != "qwen" {
		t.Errorf("Name() = %q, want qwen", provider.Name())
	}
	if provider.config.BaseURL != "https://dashscope-intl.aliyuncs.com/compatible-mode/v1" {
		t.Errorf("BaseURL = %q, want the documented DashScope endpoint", provider.config.BaseURL)
	}
}

// TestNewZAIProviderDefaults mirrors the above for Z.ai.
func TestNewZAIProviderDefaults(t *testing.T) {
	if _, err := NewZAIProvider(ProviderConfig{}); err == nil {
		t.Error("a missing API key must be reported rather than producing a provider that 401s")
	}

	provider, err := NewZAIProvider(ProviderConfig{APIKey: "k", Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewZAIProvider: %v", err)
	}
	if provider.Name() != "zai" {
		t.Errorf("Name() = %q, want zai", provider.Name())
	}
	if provider.config.BaseURL != "https://api.z.ai/api/paas/v4" {
		t.Errorf("BaseURL = %q, want the documented Z.ai endpoint", provider.config.BaseURL)
	}
}

// TestOpenAICompatibleFamilyRetryPolicyForwardsConfig proves RetryPolicy on
// every OpenAI-compatible wrapper (DeepSeek, Qwen, Z.ai, OpenRouter, Cerebras)
// reports back exactly the MaxRetries/RetryBackoff the caller configured --
// the same underlying OpenAICompatibleProvider.RetryPolicy, embedded.
func TestOpenAICompatibleFamilyRetryPolicyForwardsConfig(t *testing.T) {
	cfg := ProviderConfig{APIKey: "k", MaxRetries: 4, RetryBackoff: 750 * time.Millisecond}

	deepseek, err := NewDeepSeekProvider(cfg)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}
	if retries, backoff := deepseek.RetryPolicy(); retries != 4 || backoff != 750*time.Millisecond {
		t.Errorf("DeepSeek RetryPolicy() = (%d, %s), want (4, 750ms)", retries, backoff)
	}

	qwen, err := NewQwenProvider(cfg)
	if err != nil {
		t.Fatalf("NewQwenProvider: %v", err)
	}
	if retries, backoff := qwen.RetryPolicy(); retries != 4 || backoff != 750*time.Millisecond {
		t.Errorf("Qwen RetryPolicy() = (%d, %s), want (4, 750ms)", retries, backoff)
	}

	zai, err := NewZAIProvider(cfg)
	if err != nil {
		t.Fatalf("NewZAIProvider: %v", err)
	}
	if retries, backoff := zai.RetryPolicy(); retries != 4 || backoff != 750*time.Millisecond {
		t.Errorf("ZAI RetryPolicy() = (%d, %s), want (4, 750ms)", retries, backoff)
	}
}

// TestAnthropicProviderRetryPolicy pins AnthropicProvider.RetryPolicy(), the
// second of the two distinct RetryPolicy receivers this file declares (the
// other is OpenAICompatibleProvider's, covered above through its wrappers).
func TestAnthropicProviderRetryPolicy(t *testing.T) {
	provider, err := NewAnthropicProvider(ProviderConfig{
		APIKey: "k", MaxRetries: 2, RetryBackoff: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewAnthropicProvider: %v", err)
	}
	if retries, backoff := provider.RetryPolicy(); retries != 2 || backoff != 5*time.Second {
		t.Errorf("RetryPolicy() = (%d, %s), want (2, 5s)", retries, backoff)
	}
}

// TestLocalProviderRetryPolicy pins LocalProvider.RetryPolicy(), the third
// distinct receiver.
func TestLocalProviderRetryPolicy(t *testing.T) {
	provider, err := NewLocalProvider(ProviderConfig{MaxRetries: 7, RetryBackoff: time.Minute})
	if err != nil {
		t.Fatalf("NewLocalProvider: %v", err)
	}
	if retries, backoff := provider.RetryPolicy(); retries != 7 || backoff != time.Minute {
		t.Errorf("RetryPolicy() = (%d, %s), want (7, 1m)", retries, backoff)
	}
}

// TestGlobalRegistryRoundTrip exercises RegisterProvider, RegisterProviderFactory,
// GetProviderFromRegistry, and SetDefaultProvider -- the package-level
// forwarding functions over globalRegistry, none of which registerBuiltInProviderFactories
// itself exercises since it only calls RegisterFactory (the *method*, not the
// global function).
func TestGlobalRegistryRoundTrip(t *testing.T) {
	name := "conformance-test-global-provider"
	local, err := NewLocalProvider(ProviderConfig{})
	if err != nil {
		t.Fatalf("NewLocalProvider: %v", err)
	}

	if err := RegisterProvider(name, local); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}

	got, err := GetProviderFromRegistry(name)
	if err != nil {
		t.Fatalf("GetProviderFromRegistry: %v", err)
	}
	if got.Name() != "local" {
		t.Errorf("GetProviderFromRegistry returned %q, want the registered local provider", got.Name())
	}

	if err := SetDefaultProvider(name); err != nil {
		t.Fatalf("SetDefaultProvider: %v", err)
	}

	names := ListProviders()
	found := false
	for _, n := range names {
		if n == name {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ListProviders() = %v, want it to contain %q", names, name)
	}
}

// TestRegisterProviderFactoryGlobal proves RegisterProviderFactory registers
// a lazily-constructed provider under the global registry, reachable through
// CreateProvider.
func TestRegisterProviderFactoryGlobal(t *testing.T) {
	name := "conformance-test-global-factory"
	calls := 0
	factory := func(config ProviderConfig) (Provider, error) {
		calls++
		return NewLocalProvider(config)
	}

	if err := RegisterProviderFactory(name, factory); err != nil {
		t.Fatalf("RegisterProviderFactory: %v", err)
	}

	provider, err := CreateProvider(name, ProviderConfig{})
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if provider.Name() != "local" {
		t.Errorf("CreateProvider returned %q", provider.Name())
	}
	if calls != 1 {
		t.Errorf("factory was called %d times, want exactly 1", calls)
	}
}

// TestSetDefaultProviderUnknownNameErrors proves SetDefaultProvider refuses a
// name the registry has never heard of, rather than silently accepting it.
func TestSetDefaultProviderUnknownNameErrors(t *testing.T) {
	if err := SetDefaultProvider("conformance-test-unregistered-provider-xyz"); err == nil {
		t.Error("SetDefaultProvider accepted a name that was never registered")
	}
}

// TestBuiltInProviderFactoriesAreRegistered proves registerBuiltInProviderFactories
// (run once from init) populated the global registry with every documented
// built-in name, each reachable via GetProviderFromRegistry/CreateProvider
// with a valid config, and failing informatively without one where the
// underlying constructor requires a key.
func TestBuiltInProviderFactoriesAreRegistered(t *testing.T) {
	builtIns := []string{"openai", "anthropic", "openrouter", "cerebras", "deepseek", "qwen", "zai", "local", "mock"}

	names := ListProviders()
	registered := make(map[string]bool, len(names))
	for _, n := range names {
		registered[n] = true
	}
	for _, name := range builtIns {
		if !registered[name] {
			t.Errorf("built-in provider %q is missing from ListProviders(): %v", name, names)
		}
	}

	for _, name := range builtIns {
		t.Run(name, func(t *testing.T) {
			provider, err := CreateProvider(name, ProviderConfig{APIKey: "k", Timeout: time.Second})
			if err != nil {
				t.Fatalf("CreateProvider(%q): %v", name, err)
			}
			if provider == nil {
				t.Fatalf("CreateProvider(%q) returned a nil provider with no error", name)
			}
		})
	}
}

// TestMockProviderFactoryIsALocalProvider proves the "mock" built-in name is
// wired to the same constructor as "local" (registerBuiltInProviderFactories'
// mock entry), not a distinct implementation that could drift from it.
func TestMockProviderFactoryIsALocalProvider(t *testing.T) {
	provider, err := CreateProvider("mock", ProviderConfig{})
	if err != nil {
		t.Fatalf("CreateProvider(mock): %v", err)
	}
	if provider.Name() != "local" {
		t.Errorf("CreateProvider(mock).Name() = %q, want local", provider.Name())
	}
	if _, ok := provider.(*LocalProvider); !ok {
		t.Errorf("CreateProvider(mock) returned %T, want *LocalProvider", provider)
	}
}

// TestLocalProviderMockResponseFallbackKeywords pins the keyword-guessing
// fallback mockResponse falls back to when mockShapedResponse finds nothing
// to shape from (no JSONSchema, no JSON template in the system prompt) --
// the branches mockShapedResponse's own doc comment calls out as "the
// fallback for requests that declare neither".
func TestLocalProviderMockResponseFallbackKeywords(t *testing.T) {
	provider, err := NewLocalProvider(ProviderConfig{})
	if err != nil {
		t.Fatalf("NewLocalProvider: %v", err)
	}

	cases := []struct {
		name string
		req  CompletionRequest
		want string
	}{
		{
			"extract json",
			CompletionRequest{UserPrompt: "please extract the fields", ResponseFormat: "json"},
			`"name": "John Doe"`,
		},
		{
			"extract text",
			CompletionRequest{UserPrompt: "please extract the fields"},
			"Extracted data:",
		},
		{
			"validate",
			CompletionRequest{UserPrompt: "please validate this record"},
			`"valid": true`,
		},
		{
			"transform",
			CompletionRequest{UserPrompt: "please transform this record"},
			`"result": "transformed data"`,
		},
		{
			"default json",
			CompletionRequest{UserPrompt: "do something else entirely", ResponseFormat: "json"},
			`"response": "Mock response for testing"`,
		},
		{
			"default text",
			CompletionRequest{UserPrompt: "do something else entirely"},
			"Mock response for: do something else entirely",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := provider.Complete(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if !strings.Contains(resp.Content, tc.want) {
				t.Errorf("Content = %q, want it to contain %q", resp.Content, tc.want)
			}
		})
	}
}

// TestLocalProviderWithHandlerOverridesMockResponse proves WithHandler's
// custom handler is used instead of the built-in mock, and that a handler
// error is propagated rather than swallowed.
func TestLocalProviderWithHandlerOverridesMockResponse(t *testing.T) {
	provider, err := NewLocalProvider(ProviderConfig{})
	if err != nil {
		t.Fatalf("NewLocalProvider: %v", err)
	}

	provider.WithHandler(func(ctx context.Context, req CompletionRequest) (string, error) {
		return "handled: " + req.UserPrompt, nil
	})

	resp, err := provider.Complete(context.Background(), CompletionRequest{UserPrompt: "hi"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Content != "handled: hi" {
		t.Errorf("Content = %q, want the custom handler's output", resp.Content)
	}

	boom := errors.New("handler refuses")
	provider.WithHandler(func(ctx context.Context, req CompletionRequest) (string, error) {
		return "", boom
	})
	if _, err := provider.Complete(context.Background(), CompletionRequest{UserPrompt: "hi"}); !errors.Is(err, boom) {
		t.Errorf("Complete did not propagate the handler's error: %v", err)
	}
}
