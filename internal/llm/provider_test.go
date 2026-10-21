package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/config/configtest"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

func TestProviders(t *testing.T) {
	t.Run("OpenAIProvider", func(t *testing.T) {
		// Create mock server
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Verify headers
			if r.Header.Get("Authorization") != "Bearer test-key" {
				t.Errorf("Expected Authorization header")
			}
			if !strings.Contains(r.URL.Path, "/responses") {
				t.Errorf("Expected path to contain /responses, got %s", r.URL.Path)
			}

			// Return mock response
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"id": "resp_123",
				"status": "completed", "output": [
					{
						"content": [
							{
								"type": "output_text",
								"text": "OpenAI response"
							}
						]
					}
				],
				"model": "gpt-5-2025-08-07",
				"usage": {
					"input_tokens": 10,
					"output_tokens": 20,
					"total_tokens": 30
				}
			}`))
		}))
		defer server.Close()

		provider, err := NewOpenAIProvider(ProviderConfig{
			APIKey:  "test-key",
			BaseURL: server.URL,
		})

		if err != nil {
			t.Fatalf("Failed to create OpenAI provider: %v", err)
		}

		if provider.Name() != "openai" {
			t.Errorf("Expected provider name 'openai', got %s", provider.Name())
		}

		// Test completion with mock server
		resp, err := provider.Complete(context.Background(), CompletionRequest{
			SystemPrompt: "You are a helpful assistant",
			UserPrompt:   "Hello",
		})

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if resp.Content != "OpenAI response" {
			t.Errorf("Expected content 'OpenAI response', got %s", resp.Content)
		}

		// Test cost estimation
		cost := provider.EstimateCost(CompletionRequest{
			SystemPrompt: "You are a helpful assistant",
			UserPrompt:   "Hello, how are you?",
			MaxTokens:    100,
		})

		if cost <= 0 {
			t.Error("Expected positive cost estimate")
		}
	})

	t.Run("OpenAIProvider JSON mode omits unsupported temperature and parses later message output", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("failed to read request body: %v", err)
				return
			}

			var req map[string]any
			if err := json.Unmarshal(body, &req); err != nil {
				t.Errorf("failed to unmarshal request: %v", err)
				return
			}

			if _, ok := req["temperature"]; ok {
				t.Errorf("temperature should be omitted for gpt-5 models: %s", string(body))
				return
			}

			textCfg, ok := req["text"].(map[string]any)
			if !ok {
				t.Errorf("expected text config in request body: %s", string(body))
				return
			}
			formatCfg, ok := textCfg["format"].(map[string]any)
			if !ok || formatCfg["type"] != "json_object" {
				t.Errorf("expected json_object response format: %s", string(body))
				return
			}
			input, ok := req["input"].(string)
			if !ok || !strings.Contains(strings.ToLower(input), "json") {
				t.Errorf("expected json hint in input prompt: %s", string(body))
				return
			}
			reasoningCfg, ok := req["reasoning"].(map[string]any)
			if !ok || reasoningCfg["effort"] != "minimal" {
				t.Errorf("expected model-specific reasoning effort: %s", string(body))
				return
			}
			if textCfg["verbosity"] != "low" {
				t.Errorf("expected low verbosity for gpt-5 JSON mode: %s", string(body))
				return
			}

			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"id": "resp_json",
				"status": "completed", "output": [
					{ "type": "reasoning", "summary": [] },
					{
						"type": "message",
						"content": [
							{ "type": "output_text", "text": "{\"ok\":true}" }
						]
					}
				],
				"model": "gpt-5-nano-2025-08-07",
				"usage": {
					"input_tokens": 10,
					"output_tokens": 20,
					"total_tokens": 30
				}
			}`))
		}))
		defer server.Close()

		provider, err := NewOpenAIProvider(ProviderConfig{
			APIKey:  "test-key",
			BaseURL: server.URL,
			Timeout: 5 * time.Second,
		})
		if err != nil {
			t.Fatalf("Failed to create OpenAI provider: %v", err)
		}

		resp, err := provider.Complete(context.Background(), CompletionRequest{
			Model:          "gpt-5-nano-2025-08-07",
			SystemPrompt:   "Return a JSON object",
			UserPrompt:     "Test",
			Temperature:    0.3,
			ResponseFormat: "json",
		})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if resp.Content != `{"ok":true}` {
			t.Fatalf("expected parsed JSON content, got %q", resp.Content)
		}
	})

	t.Run("AnthropicProvider", func(t *testing.T) {
		// Create mock server
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Verify headers
			if r.Header.Get("x-api-key") != "test-key" {
				t.Errorf("Expected x-api-key header")
			}
			if r.Header.Get("anthropic-version") != "2023-06-01" {
				t.Errorf("Expected anthropic-version header")
			}

			// Return mock response
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"id": "msg_123",
				"stop_reason": "end_turn", "content": [
					{
						"type": "text",
						"text": "Anthropic response"
					}
				],
				"model": "claude-3-5-sonnet-20240620",
				"usage": {
					"input_tokens": 10,
					"output_tokens": 20
				}
			}`))
		}))
		defer server.Close()

		provider, err := NewAnthropicProvider(ProviderConfig{
			APIKey:  "test-key",
			BaseURL: server.URL,
		})

		if err != nil {
			t.Fatalf("Failed to create Anthropic provider: %v", err)
		}

		if provider.Name() != "anthropic" {
			t.Errorf("Expected provider name 'anthropic', got %s", provider.Name())
		}

		// Test completion with mock server
		resp, err := provider.Complete(context.Background(), CompletionRequest{
			SystemPrompt: "Test",
			UserPrompt:   "Hello",
		})

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if resp.Provider != "anthropic" {
			t.Errorf("Expected provider 'anthropic' in response, got %s", resp.Provider)
		}

		if resp.Content != "Anthropic response" {
			t.Errorf("Expected content 'Anthropic response', got %s", resp.Content)
		}
	})

	t.Run("OpenRouterProvider", func(t *testing.T) {
		provider, err := NewOpenRouterProvider(ProviderConfig{
			APIKey: "test-key",
		})

		if err != nil {
			t.Fatalf("Failed to create OpenRouter provider: %v", err)
		}

		if provider.Name() != "openrouter" {
			t.Errorf("Expected provider name 'openrouter', got %s", provider.Name())
		}

		// Test cost estimation
		cost := provider.EstimateCost(CompletionRequest{
			SystemPrompt: "You are a helpful assistant",
			UserPrompt:   "Hello, how are you?",
			MaxTokens:    100,
		})

		if cost <= 0 {
			t.Error("Expected positive cost estimate")
		}
	})

	t.Run("CerebrasProvider", func(t *testing.T) {
		provider, err := NewCerebrasProvider(ProviderConfig{
			APIKey: "test-key",
		})

		if err != nil {
			t.Fatalf("Failed to create Cerebras provider: %v", err)
		}

		if provider.Name() != "cerebras" {
			t.Errorf("Expected provider name 'cerebras', got %s", provider.Name())
		}

		// Test cost estimation
		cost := provider.EstimateCost(CompletionRequest{
			SystemPrompt: "You are a helpful assistant",
			UserPrompt:   "Hello, how are you?",
			MaxTokens:    100,
		})

		if cost <= 0 {
			t.Error("Expected positive cost estimate")
		}
	})

	t.Run("LocalProvider", func(t *testing.T) {
		provider, err := NewLocalProvider(ProviderConfig{})

		if err != nil {
			t.Fatalf("Failed to create local provider: %v", err)
		}

		if provider.Name() != "local" {
			t.Errorf("Expected provider name 'local', got %s", provider.Name())
		}

		// Test cost (should be 0)
		cost := provider.EstimateCost(CompletionRequest{})
		if cost != 0 {
			t.Errorf("Expected 0 cost for local provider, got %f", cost)
		}

		// Test mock completion
		resp, err := provider.Complete(context.Background(), CompletionRequest{
			SystemPrompt:   "You are an extraction expert",
			UserPrompt:     "Extract data from text",
			ResponseFormat: "json",
		})

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if !strings.Contains(resp.Content, "{") {
			t.Error("Expected JSON response from local provider")
		}
	})

	t.Run("LocalProviderWithHandler", func(t *testing.T) {
		provider, _ := NewLocalProvider(ProviderConfig{})

		// Set custom handler
		provider.WithHandler(func(ctx context.Context, req CompletionRequest) (string, error) {
			return fmt.Sprintf("Custom response to: %s", req.UserPrompt), nil
		})

		resp, err := provider.Complete(context.Background(), CompletionRequest{
			UserPrompt: "test prompt",
		})

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if resp.Content != "Custom response to: test prompt" {
			t.Errorf("Expected custom response, got: %s", resp.Content)
		}
	})
}

func TestOpenAIProviderUsesSupportedReasoningEffortForGPT54(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("failed to read request body: %v", err)
			return
		}

		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("failed to unmarshal request: %v", err)
			return
		}

		reasoningCfg, ok := req["reasoning"].(map[string]any)
		if !ok || reasoningCfg["effort"] != "none" {
			t.Errorf("expected reasoning effort none for gpt-5.4: %s", string(body))
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id": "resp_json",
			"status": "completed",
			"output": [
				{
					"type": "message",
					"content": [
						{ "type": "output_text", "text": "{\"ok\":true}" }
					]
				}
			],
			"model": "gpt-5.4",
			"usage": {
				"input_tokens": 10,
				"output_tokens": 20,
				"total_tokens": 30
			}
		}`))
	}))
	defer server.Close()

	provider, err := NewOpenAIProvider(ProviderConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Failed to create OpenAI provider: %v", err)
	}

	_, err = provider.Complete(context.Background(), CompletionRequest{
		Model:          "gpt-5.4",
		SystemPrompt:   "Return a JSON object",
		UserPrompt:     "Test",
		ResponseFormat: "json",
	})
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
}

// The built-in providers are registered from a map, and the first one visited
// became the registry's default, so CreateProvider("") built a different vendor
// from the caller's configuration on each start of the process, and the local mock
// on two starts in nine. A built-in is never the default; the default is what a
// caller chose, and with none chosen an empty name is a configuration error.
func TestBuiltInProvidersAreNeverTheDefault(t *testing.T) {
	for i := 0; i < 100; i++ {
		registry := NewProviderRegistry()
		registerBuiltInProviderFactories(registry)
		if registry.defaultProvider != "" {
			t.Fatalf("registration %d made %q the default", i, registry.defaultProvider)
		}
		provider, err := registry.Create("", ProviderConfig{APIKey: "k"})
		if err == nil {
			t.Fatalf("registration %d: an empty name built a %s provider", i, provider.Name())
		}
		if provider != nil {
			t.Fatalf("a provider came back beside the error: %v", provider)
		}
	}
}

func TestAnEmptyProviderNameWithNoDefaultIsAConfigurationError(t *testing.T) {
	registry := NewProviderRegistry()
	registerBuiltInProviderFactories(registry)

	for name, create := range map[string]func() (Provider, error){
		"Create":                   func() (Provider, error) { return registry.Create("", ProviderConfig{APIKey: "k"}) },
		"Create with spaces":       func() (Provider, error) { return registry.Create("   ", ProviderConfig{APIKey: "k"}) },
		"Get":                      func() (Provider, error) { return registry.Get("") },
		"Create with a nil config": func() (Provider, error) { return registry.Create("", ProviderConfig{}) },
	} {
		t.Run(name, func(t *testing.T) {
			provider, err := create()
			if err == nil || provider != nil {
				t.Fatalf("got %v, %v; want no provider and an error", provider, err)
			}
			if got := Classify(err); got != types.KindConfiguration {
				t.Errorf("Classify = %v, want %v", got, types.KindConfiguration)
			}
			// Classify falls back to the text of an untyped error, so the kind
			// has to be asked of the error itself as well.
			if got := types.KindOf(err); got != types.KindConfiguration {
				t.Errorf("types.KindOf = %v, want %v", got, types.KindConfiguration)
			}
			if !strings.Contains(err.Error(), "SetDefaultProvider") {
				t.Errorf("the error does not say what to configure: %v", err)
			}
		})
	}
}

// Naming a provider still works, the local mock included: it is reached when the
// caller asks for it by name.
func TestBuiltInProvidersAreReachedByName(t *testing.T) {
	registry := NewProviderRegistry()
	registerBuiltInProviderFactories(registry)
	for _, name := range []string{"openai", "anthropic", "openrouter", "cerebras", "deepseek", "qwen", "zai", "local", "mock", " OpenAI "} {
		t.Run(name, func(t *testing.T) {
			provider, err := registry.Create(name, ProviderConfig{APIKey: "k", BaseURL: "https://example.invalid", Timeout: time.Second})
			if err != nil || provider == nil {
				t.Fatalf("Create(%q) = %v, %v", name, provider, err)
			}
		})
	}
}

// A default the caller chose is used, whichever it is.
func TestADefaultTheCallerChoseIsUsed(t *testing.T) {
	registry := NewProviderRegistry()
	registerBuiltInProviderFactories(registry)
	if err := registry.SetDefault("anthropic"); err != nil {
		t.Fatalf("SetDefault: %v", err)
	}
	provider, err := registry.Create("", ProviderConfig{APIKey: "k"})
	if err != nil || provider.Name() != "anthropic" {
		t.Fatalf("Create(\"\") = %v, %v; want the anthropic provider", provider, err)
	}

	// A provider the caller registers is a choice, so the first one is the default.
	other := NewProviderRegistry()
	registerBuiltInProviderFactories(other)
	if err := other.RegisterFactory("mine", func(ProviderConfig) (Provider, error) { return NewLocalProvider(ProviderConfig{}) }); err != nil {
		t.Fatal(err)
	}
	if other.defaultProvider != "mine" {
		t.Errorf("default = %q, want the first provider the caller registered", other.defaultProvider)
	}
}

func TestProviderRegistry(t *testing.T) {
	t.Run("RegisterAndGet", func(t *testing.T) {
		registry := NewProviderRegistry()

		// Create and register providers
		openai, _ := NewOpenAIProvider(ProviderConfig{APIKey: "test"})
		local, _ := NewLocalProvider(ProviderConfig{})

		err := registry.Register("openai", openai)
		if err != nil {
			t.Fatalf("Failed to register OpenAI provider: %v", err)
		}

		err = registry.Register("local", local)
		if err != nil {
			t.Fatalf("Failed to register local provider: %v", err)
		}

		// Get provider
		provider, err := registry.Get("openai")
		if err != nil {
			t.Fatalf("Failed to get OpenAI provider: %v", err)
		}

		if provider.Name() != "openai" {
			t.Errorf("Expected OpenAI provider, got %s", provider.Name())
		}

		// Get default (should be first registered)
		provider, err = registry.Get("")
		if err != nil {
			t.Fatalf("Failed to get default provider: %v", err)
		}

		if provider.Name() != "openai" {
			t.Errorf("Expected default to be OpenAI, got %s", provider.Name())
		}
	})

	t.Run("SetDefault", func(t *testing.T) {
		registry := NewProviderRegistry()

		openai, _ := NewOpenAIProvider(ProviderConfig{APIKey: "test"})
		local, _ := NewLocalProvider(ProviderConfig{})

		registry.Register("openai", openai)
		registry.Register("local", local)

		// Change default
		err := registry.SetDefault("local")
		if err != nil {
			t.Fatalf("Failed to set default: %v", err)
		}

		provider, _ := registry.Get("")
		if provider.Name() != "local" {
			t.Errorf("Expected default to be local, got %s", provider.Name())
		}
	})

	t.Run("ListProviders", func(t *testing.T) {
		registry := NewProviderRegistry()

		openai, _ := NewOpenAIProvider(ProviderConfig{APIKey: "test"})
		local, _ := NewLocalProvider(ProviderConfig{})

		registry.Register("openai", openai)
		registry.Register("local", local)

		providers := registry.List()
		if len(providers) != 2 {
			t.Errorf("Expected 2 providers, got %d", len(providers))
		}

		// Check both providers are in the list
		hasOpenAI := false
		hasLocal := false
		for _, name := range providers {
			if name == "openai" {
				hasOpenAI = true
			}
			if name == "local" {
				hasLocal = true
			}
		}

		if !hasOpenAI || !hasLocal {
			t.Error("Missing expected providers in list")
		}
	})
}

func TestProviderCostEstimation(t *testing.T) {
	req := CompletionRequest{
		SystemPrompt: strings.Repeat("a", 1000), // ~250 tokens
		UserPrompt:   strings.Repeat("b", 1000), // ~250 tokens
		MaxTokens:    500,
	}

	// The figures are the table's rates applied to 500 prompt tokens (2000
	// characters over four) and a 500 token ceiling, and are asserted exactly: a
	// range wide enough to hold a price table that is wrong by a factor of two
	// checks nothing.
	t.Run("OpenAICost", func(t *testing.T) {
		provider, _ := NewOpenAIProvider(ProviderConfig{APIKey: "test"})
		// 500 * $30/1M + 500 * $60/1M
		if got, want := provider.EstimateCost(req), 0.045; math.Abs(got-want) > 1e-9 {
			t.Errorf("cost = %v, want %v", got, want)
		}
	})

	t.Run("AnthropicCost", func(t *testing.T) {
		provider, _ := NewAnthropicProvider(ProviderConfig{APIKey: "test"})
		// 500 * $15/1M + 500 * $75/1M
		if got, want := provider.EstimateCost(req), 0.045; math.Abs(got-want) > 1e-9 {
			t.Errorf("cost = %v, want %v", got, want)
		}
	})

	t.Run("OpenRouterCost", func(t *testing.T) {
		provider, _ := NewOpenRouterProvider(ProviderConfig{APIKey: "test"})
		// 500 * $1/1M + 500 * $2/1M
		if got, want := provider.EstimateCost(req), 0.0015; math.Abs(got-want) > 1e-9 {
			t.Errorf("cost = %v, want %v", got, want)
		}
	})

	t.Run("CerebrasCost", func(t *testing.T) {
		provider, _ := NewCerebrasProvider(ProviderConfig{APIKey: "test"})
		// 500 * $0.10/1M + 500 * $0.10/1M
		if got, want := provider.EstimateCost(req), 0.0001; math.Abs(got-want) > 1e-9 {
			t.Errorf("cost = %v, want %v", got, want)
		}
	})

	t.Run("LocalCost", func(t *testing.T) {
		provider, _ := NewLocalProvider(ProviderConfig{})
		cost := provider.EstimateCost(req)

		if cost != 0 {
			t.Errorf("Expected 0 cost for local provider, got %f", cost)
		}
	})

	t.Run("CostOverride", func(t *testing.T) {
		// Override cost for a specific model
		t.Setenv("SCHEMAFLUX_COST_INPUT_TEST_MODEL", "100.0")  // $100/1M
		t.Setenv("SCHEMAFLUX_COST_OUTPUT_TEST_MODEL", "200.0") // $200/1M

		provider, _ := NewOpenAIProvider(ProviderConfig{APIKey: "test"})

		req := CompletionRequest{
			Model:        "test-model",
			SystemPrompt: strings.Repeat("a", 1000), // ~250 tokens
			UserPrompt:   strings.Repeat("b", 1000), // ~250 tokens
			MaxTokens:    500,
		}

		cost := provider.EstimateCost(req)

		// Expected cost:
		// Input: 500 tokens * ($100 / 1M) = 0.05
		// Output: 500 tokens * ($200 / 1M) = 0.10
		// Total: 0.15

		if math.Abs(cost-0.15) > 1e-9 {
			t.Errorf("Expected cost 0.15, got %v", cost)
		}
	})

	t.Run("LevelCostOverride", func(t *testing.T) {
		// Map a model to a level
		configtest.Setenv(t, "SCHEMAFLUX_MODEL_SMART", "level-model")
		// Set cost for that level
		t.Setenv("SCHEMAFLUX_COST_INPUT_SMART", "50.0")   // $50/1M
		t.Setenv("SCHEMAFLUX_COST_OUTPUT_SMART", "100.0") // $100/1M

		provider, _ := NewOpenAIProvider(ProviderConfig{APIKey: "test"})

		req := CompletionRequest{
			Model:        "level-model", // Must match the model mapped to the level
			SystemPrompt: strings.Repeat("a", 1000),
			UserPrompt:   strings.Repeat("b", 1000),
			MaxTokens:    500,
		}

		cost := provider.EstimateCost(req)

		// Expected cost:
		// Input: 500 tokens * ($50 / 1M) = 0.025
		// Output: 500 tokens * ($100 / 1M) = 0.05
		// Total: 0.075

		if math.Abs(cost-0.075) > 1e-9 {
			t.Errorf("Expected cost 0.075, got %v", cost)
		}
	})
}

// The estimate is the prompt at four characters to a token plus the request's
// ceiling, each at the provider's rate; a request that names no ceiling is
// estimated at 500 tokens of answer.
func TestEstimateCostUsesTheRequestsOwnCeiling(t *testing.T) {
	for _, name := range []string{"SCHEMAFLUX_MODEL_SMART", "SCHEMAFLUX_MODEL_FAST", "SCHEMAFLUX_MODEL_QUICK"} {
		configtest.Setenv(t, name, "")
	}
	provider, err := NewOpenAIProvider(ProviderConfig{APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	prompt := strings.Repeat("a", 400) // 100 tokens at $30 per million
	cases := []struct {
		name string
		max  int
		want float64
	}{
		{"no ceiling named: 500 tokens at $60 per million", 0, 100*30.0/1e6 + 500*60.0/1e6},
		{"a ceiling of 1000", 1000, 100*30.0/1e6 + 1000*60.0/1e6},
		{"a ceiling of 10", 10, 100*30.0/1e6 + 10*60.0/1e6},
	}
	for _, tc := range cases {
		got := provider.EstimateCost(CompletionRequest{Model: "gpt-5.6-luna", UserPrompt: prompt, MaxTokens: tc.max})
		if math.Abs(got-tc.want) > 1e-12 {
			t.Errorf("%s: EstimateCost = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A price from the environment that is not a finite number of zero or more is
// refused when a provider is built, in an error that names the variable and not
// its value. NaN and infinity made every estimate NaN or infinite, which a budget
// guard compares false against its limit; a negative price made a call spend the
// budget down.
func TestCostEnvironmentThatIsNotAPriceIsRefusedWhenAProviderIsBuilt(t *testing.T) {
	const variable = "SCHEMAFLUX_COST_INPUT_MY_MODEL"
	values := []string{"NaN", "nan", "Inf", "+Inf", "-Inf", "infinity", "-1", "-0.0001", "1e999", "-1e999", "abc", "12abc", "$5", "1,5", "0x", "--1"}
	builders := map[string]func() error{
		"openai":     func() error { _, err := NewOpenAIProvider(ProviderConfig{APIKey: "k"}); return err },
		"anthropic":  func() error { _, err := NewAnthropicProvider(ProviderConfig{APIKey: "k"}); return err },
		"openrouter": func() error { _, err := NewOpenRouterProvider(ProviderConfig{APIKey: "k"}); return err },
		"cerebras":   func() error { _, err := NewCerebrasProvider(ProviderConfig{APIKey: "k"}); return err },
		"compatible": func() error {
			_, err := NewOpenAICompatibleProvider("x", ProviderConfig{APIKey: "k", BaseURL: "https://example.invalid"})
			return err
		},
	}
	for _, value := range values {
		for name, build := range builders {
			t.Run(name+"/"+value, func(t *testing.T) {
				t.Setenv(variable, value)
				err := build()
				if err == nil {
					t.Fatalf("a provider was built with %s=%q", variable, value)
				}
				if got := Classify(err); got != types.KindConfiguration {
					t.Errorf("Classify = %v, want %v", got, types.KindConfiguration)
				}
				if !strings.Contains(err.Error(), variable) {
					t.Errorf("the error does not name the variable: %v", err)
				}
				if len(value) > 3 && strings.Contains(err.Error(), value) {
					t.Errorf("the error carries the value: %v", err)
				}
			})
		}
	}
}

func TestOutputAndLevelCostVariablesAreCheckedToo(t *testing.T) {
	for _, variable := range []string{"SCHEMAFLUX_COST_OUTPUT_MY_MODEL", "SCHEMAFLUX_COST_INPUT_SMART", "SCHEMAFLUX_COST_OUTPUT_FAST", "SCHEMAFLUX_COST_INPUT_"} {
		t.Run(variable, func(t *testing.T) {
			t.Setenv(variable, "NaN")
			_, err := NewOpenAIProvider(ProviderConfig{APIKey: "k"})
			if err == nil || !strings.Contains(err.Error(), variable) {
				t.Fatalf("err = %v, want it to name %s", err, variable)
			}
		})
	}
}

func TestCostEnvironmentThatIsAPriceIsAccepted(t *testing.T) {
	for _, value := range []string{"0", "0.0", "1", "30", "30.5", "1e2", " 5 ", "0.000001", ""} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("SCHEMAFLUX_COST_INPUT_MY_MODEL", value)
			if _, err := NewOpenAIProvider(ProviderConfig{APIKey: "k"}); err != nil {
				t.Fatalf("a price of %q was refused: %v", value, err)
			}
		})
	}
}

// An estimate is always a finite number of zero or more, even if the variable
// went bad after the provider was built.
func TestEstimateIsFiniteWhenTheEnvironmentWentBadAfterConstruction(t *testing.T) {
	provider, err := NewOpenAIProvider(ProviderConfig{APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	req := CompletionRequest{Model: "my-model", UserPrompt: strings.Repeat("a", 400), MaxTokens: 100}
	good := provider.EstimateCost(req)
	for _, value := range []string{"NaN", "Inf", "-5", "junk"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("SCHEMAFLUX_COST_INPUT_MY_MODEL", value)
			t.Setenv("SCHEMAFLUX_COST_OUTPUT_MY_MODEL", value)
			got := provider.EstimateCost(req)
			if math.IsNaN(got) || math.IsInf(got, 0) || got < 0 {
				t.Fatalf("estimate = %v", got)
			}
			if got != good {
				t.Errorf("estimate = %v, want the table's %v: a bad variable is not used", got, good)
			}
		})
	}
}

func TestPriceFromEnv(t *testing.T) {
	cases := []struct {
		value   string
		want    float64
		set     bool
		wantErr bool
	}{
		{"", 0, false, false},
		{"   ", 0, false, false},
		{"30", 30e-6, true, false},
		{"0", 0, true, false},
		{"0.5", 0.5e-6, true, false},
		{" 7 ", 7e-6, true, false},
		{"1e3", 1e-3, true, false},
		{"NaN", 0, false, true},
		{"Inf", 0, false, true},
		{"-Inf", 0, false, true},
		{"-1", 0, false, true},
		{"1e999", 0, false, true},
		{"abc", 0, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("SCHEMAFLUX_TEST_PRICE", tc.value)
			got, set, err := priceFromEnv("SCHEMAFLUX_TEST_PRICE")
			if (err != nil) != tc.wantErr || set != tc.set || math.Abs(got-tc.want) > 1e-15 {
				t.Errorf("priceFromEnv(%q) = %v, %v, %v; want %v, %v, err %v", tc.value, got, set, err, tc.want, tc.set, tc.wantErr)
			}
		})
	}
}

// A server that never answers is a timeout, whichever provider is asked and
// whichever way the bound is set: by the call's context, by ProviderConfig.Timeout
// with the client the library builds, or by ProviderConfig.Timeout with a client of
// the caller's own. That last one was dropped, since a caller's client is used as
// it is, and a call under a context with no deadline could wait for ever.
func TestProviderTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(release)
		server.Close()
	})

	type build func(t *testing.T, config ProviderConfig) Provider
	providers := map[string]build{
		"openai": func(t *testing.T, config ProviderConfig) Provider {
			config.APIKey, config.BaseURL = "k", server.URL
			p, err := NewOpenAIProvider(config)
			if err != nil {
				t.Fatal(err)
			}
			return p
		},
		"anthropic": func(t *testing.T, config ProviderConfig) Provider {
			config.APIKey, config.BaseURL = "k", server.URL
			p, err := NewAnthropicProvider(config)
			if err != nil {
				t.Fatal(err)
			}
			return p
		},
		"chat dialect": func(t *testing.T, config ProviderConfig) Provider {
			config.APIKey, config.BaseURL = "k", server.URL
			p, err := NewOpenAICompatibleProvider("testvendor", config)
			if err != nil {
				t.Fatal(err)
			}
			return p
		},
	}

	setups := []struct {
		name    string
		config  ProviderConfig
		timeout time.Duration // the deadline on the call's context, 0 for none
	}{
		{"the context's deadline", ProviderConfig{}, 80 * time.Millisecond},
		{"the configured timeout with the library's client", ProviderConfig{Timeout: 80 * time.Millisecond}, 0},
		{"the configured timeout with a client of the caller's own", ProviderConfig{Timeout: 80 * time.Millisecond, HTTPClient: &http.Client{}}, 0},
		{"the configured timeout with a client that has a longer one", ProviderConfig{Timeout: 80 * time.Millisecond, HTTPClient: &http.Client{Timeout: time.Minute}}, 0},
		{"the client's timeout when it is the shorter", ProviderConfig{Timeout: time.Minute, HTTPClient: &http.Client{Timeout: 80 * time.Millisecond}}, 0},
	}
	for name, newProvider := range providers {
		for _, setup := range setups {
			t.Run(name+"/"+setup.name, func(t *testing.T) {
				provider := newProvider(t, setup.config)
				ctx := context.Background()
				if setup.timeout > 0 {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, setup.timeout)
					defer cancel()
				}
				start := time.Now()
				resp, err := provider.Complete(ctx, CompletionRequest{Model: "m", UserPrompt: "x"})
				elapsed := time.Since(start)
				if err == nil {
					t.Fatalf("a call to a server that never answers succeeded: %+v", resp)
				}
				if elapsed > 10*time.Second {
					t.Errorf("the call waited %v; the bound did not apply", elapsed)
				}
				if got := Classify(err); got != types.KindTimeout {
					t.Errorf("Classify = %v, want %v (%v)", got, types.KindTimeout, err)
				}
				if !isEmptyResponse(resp) {
					t.Errorf("a result came back beside the error")
				}
			})
		}
	}
}

// The configured timeout bounds a streamed call as well, and the embeddings call.
func TestProviderTimeoutWithACallersClientBoundsStreamsAndEmbeddings(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(release)
		server.Close()
	})
	provider, err := NewOpenAIProvider(ProviderConfig{
		APIKey: "k", BaseURL: server.URL, Timeout: 80 * time.Millisecond, HTTPClient: &http.Client{},
	})
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	_, _, streamErr := collectStream(provider.CompleteStream(context.Background(), CompletionRequest{Model: "m"}))
	if got := Classify(streamErr); got != types.KindTimeout {
		t.Errorf("stream: Classify = %v, want %v (%v)", got, types.KindTimeout, streamErr)
	}
	_, embedErr := provider.Embed(context.Background(), EmbeddingRequest{Input: []string{"x"}})
	if got := Classify(embedErr); got != types.KindTimeout {
		t.Errorf("embed: Classify = %v, want %v (%v)", got, types.KindTimeout, embedErr)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("the calls waited %v; the bound did not apply", elapsed)
	}
}

func TestCallContext(t *testing.T) {
	deadlineOf := func(ctx context.Context) (time.Duration, bool) {
		d, ok := ctx.Deadline()
		return time.Until(d), ok
	}
	cases := []struct {
		name         string
		config       ProviderConfig
		parent       time.Duration // 0 for no deadline
		wantDeadline bool
		wantAtMost   time.Duration
	}{
		{"no client of the caller's: the library's client carries it", ProviderConfig{Timeout: time.Minute}, 0, false, 0},
		{"a caller's client and a timeout", ProviderConfig{Timeout: time.Minute, HTTPClient: &http.Client{}}, 0, true, time.Minute},
		{"a caller's client and no timeout", ProviderConfig{HTTPClient: &http.Client{}}, 0, false, 0},
		{"a caller's client and a negative timeout", ProviderConfig{Timeout: -time.Second, HTTPClient: &http.Client{}}, 0, false, 0},
		{"the parent's earlier deadline wins", ProviderConfig{Timeout: time.Hour, HTTPClient: &http.Client{}}, time.Minute, true, time.Minute},
		{"the configured earlier deadline wins", ProviderConfig{Timeout: time.Minute, HTTPClient: &http.Client{}}, time.Hour, true, time.Minute},
		{"nothing configured", ProviderConfig{}, 0, false, 0},
		{"the parent's deadline alone is untouched", ProviderConfig{}, time.Minute, true, time.Minute},
		{"a client's own timeout does not stop the configured one", ProviderConfig{Timeout: time.Minute, HTTPClient: &http.Client{Timeout: time.Hour}}, 0, true, time.Minute},
		{"a tiny timeout", ProviderConfig{Timeout: time.Nanosecond, HTTPClient: &http.Client{}}, 0, true, time.Nanosecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parent := context.Background()
			if tc.parent > 0 {
				var cancel context.CancelFunc
				parent, cancel = context.WithTimeout(parent, tc.parent)
				defer cancel()
			}
			ctx, cancel := callContext(parent, tc.config)
			defer cancel()
			remaining, has := deadlineOf(ctx)
			if has != tc.wantDeadline {
				t.Fatalf("has deadline = %v, want %v", has, tc.wantDeadline)
			}
			if has && remaining > tc.wantAtMost {
				t.Errorf("deadline in %v, want at most %v", remaining, tc.wantAtMost)
			}
		})
	}
}

func TestOpenAICompatibleProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer compat-key" {
			t.Errorf("expected bearer auth header, got %q", got)
			return
		}
		if got := r.Header.Get("X-Test-Header"); got != "enabled" {
			t.Errorf("expected custom header, got %q", got)
			return
		}
		if !strings.Contains(r.URL.Path, "/chat/completions") {
			t.Errorf("expected chat completions path, got %s", r.URL.Path)
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id":"chatcmpl_123",
			"model":"deepseek-chat",
			"choices":[{"index":0,"message":{"role":"assistant","content":"compatible response"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":12,"completion_tokens":8,"total_tokens":20}
		}`))
	}))
	defer server.Close()

	provider, err := NewOpenAICompatibleProvider("deepseek", ProviderConfig{
		APIKey:  "compat-key",
		BaseURL: server.URL + "/v1",
		ExtraHeaders: map[string]string{
			"X-Test-Header": "enabled",
		},
	})
	if err != nil {
		t.Fatalf("failed to create compatible provider: %v", err)
	}

	resp, err := provider.Complete(context.Background(), CompletionRequest{
		Model:          "deepseek-chat",
		SystemPrompt:   "Be terse",
		UserPrompt:     "Hello",
		ResponseFormat: "json",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if provider.Name() != "deepseek" {
		t.Fatalf("expected provider name deepseek, got %s", provider.Name())
	}
	if resp.Content != "compatible response" {
		t.Fatalf("expected compatible response, got %q", resp.Content)
	}
}

func TestProviderRegistryFactories(t *testing.T) {
	registry := NewProviderRegistry()

	err := registry.RegisterFactory("custom", func(config ProviderConfig) (Provider, error) {
		provider, _ := NewLocalProvider(config)
		return provider, nil
	})
	if err != nil {
		t.Fatalf("failed to register factory: %v", err)
	}

	provider, err := registry.Create("custom", ProviderConfig{MaxRetries: 2})
	if err != nil {
		t.Fatalf("failed to create provider from factory: %v", err)
	}
	if provider.Name() != "local" {
		t.Fatalf("expected local provider from factory, got %s", provider.Name())
	}

	names := registry.List()
	if len(names) != 1 || names[0] != "custom" {
		t.Fatalf("expected custom in provider list, got %v", names)
	}

	if err := registry.SetDefault("custom"); err != nil {
		t.Fatalf("failed to set default factory provider: %v", err)
	}
}

// isEmptyResponse reports whether resp is the zero CompletionResponse, which is
// what every provider returns beside an error. A CompletionResponse holds a
// slice, so it cannot be compared with ==.
func isEmptyResponse(resp CompletionResponse) bool {
	return reflect.DeepEqual(resp, CompletionResponse{})
}

func TestVendorFactoriesRegistered(t *testing.T) {
	expected := []string{"anthropic", "cerebras", "deepseek", "local", "mock", "openai", "openrouter", "qwen", "zai"}
	names := ListProviders()

	for _, want := range expected {
		found := false
		for _, got := range names {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected built-in provider %s to be registered, got %v", want, names)
		}
	}
}

// The name of a provider is trimmed and lower-cased, and that one rule is
// shared with the client package, which used to carry a copy of its own.
func TestNormalizeProviderName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"already normal", "openai", "openai"},
		{"upper case", "OPENAI", "openai"},
		{"mixed case", "OpenAI", "openai"},
		{"leading space", " openai", "openai"},
		{"trailing space", "openai ", "openai"},
		{"surrounding tabs and newlines", "\topenai\n", "openai"},
		{"empty", "", ""},
		{"only space", "   ", ""},
		{"an inner space is kept", "open ai", "open ai"},
		{"a hyphen is kept", "Z-AI", "z-ai"},
		{"a digit is kept", "Qwen3", "qwen3"},
		{"non-ASCII letters fold", "\u00c9COLE", "\u00e9cole"},
		{"a non-breaking space is trimmed", "\u00a0local\u00a0", "local"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeProviderName(tc.in); got != tc.want {
				t.Errorf("NormalizeProviderName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
