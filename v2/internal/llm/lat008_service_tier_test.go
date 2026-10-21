package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// LAT-008. A provider's service tier trades price for speed, and is the
// caller's to choose: it is sent when they set one and left off when they do
// not, and the tier the provider says it served the answer on comes back with
// the answer, since a provider may serve a request on another tier than the
// one it was asked for.
//
// It is sent to OpenAI's own endpoint and nowhere else, so these go through a
// transport that answers in place of the server: a server of the test's own
// would be another address, which is a configuration the provider refuses.

// openAIStubClient answers a Responses API call in place of OpenAI and keeps the
// body it was sent.
func openAIStubClient(body *map[string]any) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(req.Body)
		*body = nil
		_ = json.Unmarshal(raw, body)
		answer := strings.Replace(responsesAnswerBody, `"model":"gpt-6-luna"`, `"model":"gpt-6-luna","service_tier":"default"`, 1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(answer)),
			Request:    req,
		}, nil
	})}
}

func TestTheServiceTierIsSentWhenItIsSet(t *testing.T) {
	cases := []struct {
		name    string
		tier    string
		sent    string // "" for not sent at all
		wantErr bool
	}{
		{"none set", "", "", false},
		{"priority", "priority", "priority", false},
		{"flex", "flex", "flex", false},
		{"default", "default", "default", false},
		{"auto", "auto", "auto", false},
		{"in capitals", "Priority", "priority", false},
		{"with space around it", "  priority ", "priority", false},
		{"only space", "   ", "", false},
		{"a tier nobody offers", "turbo", "", true},
		{"a number", "1", "", true},
		{"two words", "priority flex", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body map[string]any
			provider, err := NewOpenAIProvider(ProviderConfig{APIKey: "test-key", ServiceTier: tc.tier, HTTPClient: openAIStubClient(&body)})
			if tc.wantErr {
				if err == nil {
					t.Fatal("a provider was built with a tier nobody offers")
				}
				if !strings.Contains(err.Error(), "priority") {
					t.Errorf("the error does not say which tiers there are: %v", err)
				}
				if strings.Contains(err.Error(), tc.tier) {
					t.Errorf("the error repeats the tier that was set: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewOpenAIProvider: %v", err)
			}
			resp, err := provider.Complete(context.Background(), CompletionRequest{Model: "gpt-6-luna", SystemPrompt: "system", UserPrompt: "user"})
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			sent, present := body["service_tier"]
			if tc.sent == "" && present {
				t.Errorf("a tier was sent that nobody set: %v", sent)
			}
			if tc.sent != "" && sent != tc.sent {
				t.Errorf("the request carries tier %v, want %q", sent, tc.sent)
			}
			// What the provider served it on, which is not always what was
			// asked.
			if resp.ServiceTier != "default" {
				t.Errorf("the answer's tier is %q, want what the provider said, %q", resp.ServiceTier, "default")
			}
		})
	}
}

// A tier on a provider that will never send it is a configuration error, and
// not silence.
func TestAServiceTierOnAProviderThatNeverSendsOneIsRefused(t *testing.T) {
	const tier = "turbo-secret-tier"
	cases := []struct {
		name  string
		tier  string
		build func(ProviderConfig) (Provider, error)
		want  bool
	}{
		{"anthropic", "priority", func(c ProviderConfig) (Provider, error) { return NewAnthropicProvider(c) }, true},
		{"anthropic, a tier nobody offers", tier, func(c ProviderConfig) (Provider, error) { return NewAnthropicProvider(c) }, true},
		{"anthropic, only space", "  ", func(c ProviderConfig) (Provider, error) { return NewAnthropicProvider(c) }, false},
		{"anthropic, none", "", func(c ProviderConfig) (Provider, error) { return NewAnthropicProvider(c) }, false},
		{"openrouter", "flex", func(c ProviderConfig) (Provider, error) { return NewOpenRouterProvider(c) }, true},
		{"cerebras", "flex", func(c ProviderConfig) (Provider, error) { return NewCerebrasProvider(c) }, true},
		{"deepseek", "auto", func(c ProviderConfig) (Provider, error) { return NewDeepSeekProvider(c) }, true},
		{"qwen", "default", func(c ProviderConfig) (Provider, error) { return NewQwenProvider(c) }, true},
		{"zai", tier, func(c ProviderConfig) (Provider, error) { return NewZAIProvider(c) }, true},
		{"a generic chat-completions provider", "priority", func(c ProviderConfig) (Provider, error) {
			c.BaseURL = "https://gateway.example/v1"
			return NewOpenAICompatibleProvider("gateway", c)
		}, true},
		{"a generic chat-completions provider, none", "", func(c ProviderConfig) (Provider, error) {
			c.BaseURL = "https://gateway.example/v1"
			return NewOpenAICompatibleProvider("gateway", c)
		}, false},
		{"openai at a custom base URL", "priority", func(c ProviderConfig) (Provider, error) {
			c.BaseURL = "https://proxy.example/v1"
			return NewOpenAIProvider(c)
		}, true},
		{"openai at a custom base URL, a tier nobody offers", tier, func(c ProviderConfig) (Provider, error) {
			c.BaseURL = "https://proxy.example/v1"
			return NewOpenAIProvider(c)
		}, true},
		{"openai at a custom base URL, none", "", func(c ProviderConfig) (Provider, error) {
			c.BaseURL = "https://proxy.example/v1"
			return NewOpenAIProvider(c)
		}, false},
		{"openai at its own endpoint", "priority", func(c ProviderConfig) (Provider, error) {
			c.BaseURL = ""
			return NewOpenAIProvider(c)
		}, false},
		{"openai at its own endpoint, spelled out", "priority", func(c ProviderConfig) (Provider, error) {
			c.BaseURL = "https://api.openai.com/v1"
			return NewOpenAIProvider(c)
		}, false},
		{"openai at a regional endpoint", "priority", func(c ProviderConfig) (Provider, error) {
			c.BaseURL = "https://eu.api.openai.com/v1"
			return NewOpenAIProvider(c)
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider, err := tc.build(ProviderConfig{APIKey: "test-key", BaseURL: "https://api.example.invalid/v1", ServiceTier: tc.tier})
			if !tc.want {
				if err != nil {
					t.Fatalf("a provider that sets no tier, or sends it, was refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("a provider was built with a tier it will never send: %#v", provider)
			}
			if got := types.KindOf(err); got != types.KindConfiguration {
				t.Errorf("the error is of kind %v, want a configuration error: %v", got, err)
			}
			if !strings.Contains(err.Error(), "priority") || !strings.Contains(err.Error(), "flex") {
				t.Errorf("the error does not say which tiers there are: %v", err)
			}
			if strings.Contains(err.Error(), tc.tier) && tc.tier == tier {
				t.Errorf("the error repeats the tier that was set: %v", err)
			}
		})
	}
}

// The request is built without the tier for an endpoint that is not OpenAI's,
// whatever the constructor was given: a provider put together another way must
// not send it either.
func TestTheResponsesRequestCarriesNoTierToAnotherEndpoint(t *testing.T) {
	cases := []struct {
		baseURL string
		sent    bool
	}{
		{"", true},
		{"https://api.openai.com/v1", true},
		{"https://eu.api.openai.com/v1", true},
		{"https://proxy.example/v1", false},
		{"http://127.0.0.1:8080/v1", false},
		{"https://api.openai.com.evil.example/v1", false},
		{"https://example.com/api.openai.com", false},
	}
	for _, tc := range cases {
		provider := &OpenAIProvider{config: ProviderConfig{BaseURL: tc.baseURL, ServiceTier: "priority"}}
		body := provider.buildResponsesRequestBody(CompletionRequest{Model: "gpt-6-luna", UserPrompt: "u"}, false)
		if _, sent := body["service_tier"]; sent != tc.sent {
			t.Errorf("base URL %q: the tier is sent = %v, want %v", tc.baseURL, sent, tc.sent)
		}
	}
}
