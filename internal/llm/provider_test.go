package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
				"output": [
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
				t.Fatalf("failed to read request body: %v", err)
			}

			var req map[string]any
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatalf("failed to unmarshal request: %v", err)
			}

			if _, ok := req["temperature"]; ok {
				t.Fatalf("temperature should be omitted for gpt-5 models: %s", string(body))
			}

			textCfg, ok := req["text"].(map[string]any)
			if !ok {
				t.Fatalf("expected text config in request body: %s", string(body))
			}
			formatCfg, ok := textCfg["format"].(map[string]any)
			if !ok || formatCfg["type"] != "json_object" {
				t.Fatalf("expected json_object response format: %s", string(body))
			}
			input, ok := req["input"].(string)
			if !ok || !strings.Contains(strings.ToLower(input), "json") {
				t.Fatalf("expected json hint in input prompt: %s", string(body))
			}
			reasoningCfg, ok := req["reasoning"].(map[string]any)
			if !ok || reasoningCfg["effort"] != "minimal" {
				t.Fatalf("expected model-specific reasoning effort: %s", string(body))
			}
			if textCfg["verbosity"] != "low" {
				t.Fatalf("expected low verbosity for gpt-5 JSON mode: %s", string(body))
			}

			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"id": "resp_json",
				"output": [
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
				"content": [
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
			t.Fatalf("failed to read request body: %v", err)
		}

		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("failed to unmarshal request: %v", err)
		}

		reasoningCfg, ok := req["reasoning"].(map[string]any)
		if !ok || reasoningCfg["effort"] != "none" {
			t.Fatalf("expected reasoning effort none for gpt-5.4: %s", string(body))
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

	t.Run("OpenAICost", func(t *testing.T) {
		provider, _ := NewOpenAIProvider(ProviderConfig{APIKey: "test"})
		cost := provider.EstimateCost(req)

		// Should be roughly (500 * 0.03 + 500 * 0.06) / 1000
		// = (15 + 30) / 1000 = 0.045
		if cost < 0.01 || cost > 0.1 {
			t.Errorf("Unexpected cost estimate: %f", cost)
		}
	})

	t.Run("AnthropicCost", func(t *testing.T) {
		provider, _ := NewAnthropicProvider(ProviderConfig{APIKey: "test"})
		cost := provider.EstimateCost(req)

		// Should be lower than OpenAI due to different pricing
		if cost < 0.001 || cost > 0.05 {
			t.Errorf("Unexpected cost estimate: %f", cost)
		}
	})

	t.Run("OpenRouterCost", func(t *testing.T) {
		provider, _ := NewOpenRouterProvider(ProviderConfig{APIKey: "test"})
		cost := provider.EstimateCost(req)

		if cost <= 0 {
			t.Errorf("Expected positive cost estimate, got %f", cost)
		}
	})

	t.Run("CerebrasCost", func(t *testing.T) {
		provider, _ := NewCerebrasProvider(ProviderConfig{APIKey: "test"})
		cost := provider.EstimateCost(req)

		if cost <= 0 {
			t.Errorf("Expected positive cost estimate, got %f", cost)
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

		if cost < 0.14 || cost > 0.16 {
			t.Errorf("Expected cost ~0.15, got %f", cost)
		}
	})

	t.Run("LevelCostOverride", func(t *testing.T) {
		// Map a model to a level
		t.Setenv("SCHEMAFLUX_MODEL_SMART", "level-model")
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

		if cost < 0.074 || cost > 0.076 {
			t.Errorf("Expected cost ~0.075, got %f", cost)
		}
	})
}

func TestProviderTimeout(t *testing.T) {
	t.Run("TimeoutHandling", func(t *testing.T) {
		provider, _ := NewLocalProvider(ProviderConfig{})

		// Set handler that takes too long
		provider.WithHandler(func(ctx context.Context, req CompletionRequest) (string, error) {
			select {
			case <-time.After(1 * time.Second):
				return "too late", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		})

		// Create context with short timeout
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		_, err := provider.Complete(ctx, CompletionRequest{
			UserPrompt: "test",
		})

		if err == nil {
			t.Error("Expected timeout error")
		}

		if !strings.Contains(err.Error(), "context") {
			t.Errorf("Expected context error, got: %v", err)
		}
	})
}

func TestOpenAICompatibleProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer compat-key" {
			t.Fatalf("expected bearer auth header, got %q", got)
		}
		if got := r.Header.Get("X-Test-Header"); got != "enabled" {
			t.Fatalf("expected custom header, got %q", got)
		}
		if !strings.Contains(r.URL.Path, "/chat/completions") {
			t.Fatalf("expected chat completions path, got %s", r.URL.Path)
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
