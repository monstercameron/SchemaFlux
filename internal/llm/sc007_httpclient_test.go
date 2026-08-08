package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// SC-007. "Caller-owned HTTP. Every provider accepts an *http.Client or
// transport with documented ownership, because enterprise deployments need
// their own proxies, mTLS, and instrumentation, and tests need a transport
// that fails on dial."
//
// Before ProviderConfig.HTTPClient existed, every constructor in
// provider.go built its own `&http.Client{Timeout: config.Timeout}` with no
// injection point -- NewOpenAIProvider, newOpenAICompatibleProvider (used by
// OpenRouter, Cerebras, DeepSeek, Qwen, Z.ai), and AnthropicProvider.Complete
// (which rebuilt one on every single call, defeating connection reuse
// entirely). resolveHTTPClient (provider.go) is now the one place that
// decides which *http.Client a provider uses, and every constructor calls
// it.
//
// These tests prove the caller-supplied client is the one that actually
// executes the request -- by pointing it at a server that records whether
// it was hit and asserting through that server, not by reading back a
// struct field the production code could set without ever using it -- and
// that a caller who configures nothing gets a working client with no
// behaviour change.
func TestSC007CallerOwnedHTTPClient(t *testing.T) {
	t.Run("OpenAI: caller-supplied client is the one that executes the request", func(t *testing.T) {
		server, hit := recordingServer(t, openAIResponsesBody("caller client hit"))
		defer server.Close()

		var dialed atomic.Bool
		client := &http.Client{Transport: recordingTransport{dialed: &dialed}}

		provider, err := NewOpenAIProvider(ProviderConfig{
			APIKey:     "test-key",
			BaseURL:    server.URL,
			HTTPClient: client,
		})
		if err != nil {
			t.Fatalf("NewOpenAIProvider: %v", err)
		}

		resp, err := provider.Complete(context.Background(), CompletionRequest{UserPrompt: "hi"})
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if resp.Content != "caller client hit" {
			t.Errorf("Content = %q, want the server's response, proving the request went through the server at all", resp.Content)
		}
		if !hit.Load() {
			t.Error("the mock server was never hit")
		}
		if !dialed.Load() {
			t.Error("the caller-supplied *http.Client's Transport never saw the request -- some other client executed it instead")
		}
	})

	t.Run("OpenAI: a caller who supplies nothing gets a working client (unchanged default behaviour)", func(t *testing.T) {
		server, hit := recordingServer(t, openAIResponsesBody("default client hit"))
		defer server.Close()

		provider, err := NewOpenAIProvider(ProviderConfig{
			APIKey:  "test-key",
			BaseURL: server.URL,
			// HTTPClient deliberately left nil.
		})
		if err != nil {
			t.Fatalf("NewOpenAIProvider: %v", err)
		}

		resp, err := provider.Complete(context.Background(), CompletionRequest{UserPrompt: "hi"})
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if resp.Content != "default client hit" {
			t.Errorf("Content = %q, want the server's response", resp.Content)
		}
		if !hit.Load() {
			t.Error("the mock server was never hit -- the default client path is broken")
		}
	})

	t.Run("OpenAI-compatible (Cerebras): caller-supplied client executes the request", func(t *testing.T) {
		server, hit := recordingServer(t, chatCompletionsBody("cerebras via caller client"))
		defer server.Close()

		var dialed atomic.Bool
		client := &http.Client{Transport: recordingTransport{dialed: &dialed}}

		provider, err := NewOpenAICompatibleProvider("cerebras-test", ProviderConfig{
			APIKey:     "test-key",
			BaseURL:    server.URL,
			HTTPClient: client,
		})
		if err != nil {
			t.Fatalf("NewOpenAICompatibleProvider: %v", err)
		}

		_, err = provider.Complete(context.Background(), CompletionRequest{UserPrompt: "hi"})
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if !hit.Load() {
			t.Error("the mock server was never hit")
		}
		if !dialed.Load() {
			t.Error("the caller-supplied *http.Client's Transport never saw the request")
		}
	})

	t.Run("OpenAI-compatible: a caller who supplies nothing gets today's behaviour", func(t *testing.T) {
		server, hit := recordingServer(t, chatCompletionsBody("cerebras default client"))
		defer server.Close()

		provider, err := NewOpenAICompatibleProvider("cerebras-test", ProviderConfig{
			APIKey:  "test-key",
			BaseURL: server.URL,
		})
		if err != nil {
			t.Fatalf("NewOpenAICompatibleProvider: %v", err)
		}

		_, err = provider.Complete(context.Background(), CompletionRequest{UserPrompt: "hi"})
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if !hit.Load() {
			t.Error("the mock server was never hit -- the default client path is broken")
		}
	})

	t.Run("Anthropic: caller-supplied client executes the request", func(t *testing.T) {
		server, hit := recordingServer(t, anthropicBody("anthropic via caller client"))
		defer server.Close()

		var dialed atomic.Bool
		client := &http.Client{Transport: recordingTransport{dialed: &dialed}}

		provider, err := NewAnthropicProvider(ProviderConfig{
			APIKey:     "test-key",
			BaseURL:    server.URL,
			HTTPClient: client,
		})
		if err != nil {
			t.Fatalf("NewAnthropicProvider: %v", err)
		}

		_, err = provider.Complete(context.Background(), CompletionRequest{UserPrompt: "hi"})
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if !hit.Load() {
			t.Error("the mock server was never hit")
		}
		if !dialed.Load() {
			t.Error("the caller-supplied *http.Client's Transport never saw the request")
		}
	})

	t.Run("Anthropic: a caller who supplies nothing gets today's behaviour", func(t *testing.T) {
		server, hit := recordingServer(t, anthropicBody("anthropic default client"))
		defer server.Close()

		provider, err := NewAnthropicProvider(ProviderConfig{
			APIKey:  "test-key",
			BaseURL: server.URL,
		})
		if err != nil {
			t.Fatalf("NewAnthropicProvider: %v", err)
		}

		_, err = provider.Complete(context.Background(), CompletionRequest{UserPrompt: "hi"})
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if !hit.Load() {
			t.Error("the mock server was never hit -- the default client path is broken")
		}
	})

	t.Run("Anthropic reuses one client across calls instead of building one per call", func(t *testing.T) {
		server, _ := recordingServer(t, anthropicBody("reused client"))
		defer server.Close()

		provider, err := NewAnthropicProvider(ProviderConfig{APIKey: "test-key", BaseURL: server.URL})
		if err != nil {
			t.Fatalf("NewAnthropicProvider: %v", err)
		}
		if provider.httpClient == nil {
			t.Fatal("AnthropicProvider.httpClient is nil -- Complete would have to build one per call again")
		}
		first := provider.httpClient
		if _, err := provider.Complete(context.Background(), CompletionRequest{UserPrompt: "one"}); err != nil {
			t.Fatalf("Complete (1): %v", err)
		}
		if _, err := provider.Complete(context.Background(), CompletionRequest{UserPrompt: "two"}); err != nil {
			t.Fatalf("Complete (2): %v", err)
		}
		if provider.httpClient != first {
			t.Error("provider.httpClient changed identity across calls -- Complete is building a fresh client again")
		}
	})

	t.Run("a dial-failing transport reaches the caller as an error, not a silent fallback", func(t *testing.T) {
		errDial := errors.New("dial refused: sandboxed test transport")
		client := &http.Client{Transport: failingTransport{err: errDial}}

		provider, err := NewOpenAIProvider(ProviderConfig{
			APIKey:     "test-key",
			BaseURL:    "https://example.invalid",
			HTTPClient: client,
		})
		if err != nil {
			t.Fatalf("NewOpenAIProvider: %v", err)
		}

		_, err = provider.Complete(context.Background(), CompletionRequest{UserPrompt: "hi"})
		if err == nil {
			t.Fatal("Complete succeeded despite a transport that always fails to dial -- the caller-supplied client was not actually used")
		}
	})

	t.Run("ExtraHeaders still apply on top of a caller-supplied client", func(t *testing.T) {
		var sawHeader atomic.Bool
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Custom-Tenant") == "acme" {
				sawHeader.Store(true)
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(openAIResponsesBody("headers applied")))
		}))
		defer server.Close()

		var dialed atomic.Bool
		client := &http.Client{Transport: recordingTransport{dialed: &dialed}}

		provider, err := NewOpenAIProvider(ProviderConfig{
			APIKey:       "test-key",
			BaseURL:      server.URL,
			HTTPClient:   client,
			ExtraHeaders: map[string]string{"X-Custom-Tenant": "acme"},
		})
		if err != nil {
			t.Fatalf("NewOpenAIProvider: %v", err)
		}

		if _, err := provider.Complete(context.Background(), CompletionRequest{UserPrompt: "hi"}); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if !dialed.Load() {
			t.Error("the caller-supplied client's underlying Transport never ran -- ExtraHeaders wrapping discarded it instead of layering on top")
		}
		if !sawHeader.Load() {
			t.Error("ExtraHeaders was not applied when a caller-supplied HTTPClient was also set")
		}
	})

	t.Run("the caller's original *http.Client value is never mutated", func(t *testing.T) {
		server, _ := recordingServer(t, openAIResponsesBody("untouched"))
		defer server.Close()

		client := &http.Client{}
		originalTransport := client.Transport // nil

		_, err := NewOpenAIProvider(ProviderConfig{
			APIKey:       "test-key",
			BaseURL:      server.URL,
			HTTPClient:   client,
			ExtraHeaders: map[string]string{"X-Whatever": "1"},
		})
		if err != nil {
			t.Fatalf("NewOpenAIProvider: %v", err)
		}
		if client.Transport != originalTransport {
			t.Error("the caller's own *http.Client.Transport was mutated in place -- ExtraHeaders wrapping must operate on a copy, not the caller's original value")
		}
	})

	t.Run("EndpointPolicy nil enforces nothing (today's behaviour, SEC-004's documented default)", func(t *testing.T) {
		server, hit := recordingServer(t, openAIResponsesBody("no policy"))
		defer server.Close()

		provider, err := NewOpenAIProvider(ProviderConfig{
			APIKey:  "test-key",
			BaseURL: server.URL,
			// EndpointPolicy deliberately left nil.
		})
		if err != nil {
			t.Fatalf("NewOpenAIProvider: %v", err)
		}
		if _, err := provider.Complete(context.Background(), CompletionRequest{UserPrompt: "hi"}); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if !hit.Load() {
			t.Error("the mock server was never hit")
		}
	})

	t.Run("EndpointPolicy refuses a disallowed BaseURL at construction, before any request", func(t *testing.T) {
		server, hit := recordingServer(t, openAIResponsesBody("should never be reached"))
		defer server.Close()

		_, err := NewOpenAIProvider(ProviderConfig{
			APIKey:         "test-key",
			BaseURL:        server.URL,
			EndpointPolicy: &types.EndpointPolicy{}, // undeclared -- refuses everything
		})
		if err == nil {
			t.Fatal("NewOpenAIProvider succeeded despite an EndpointPolicy that allows nothing")
		}
		if !errors.Is(err, types.ErrEndpointNotAllowed) {
			t.Errorf("error = %v, want it to wrap types.ErrEndpointNotAllowed", err)
		}
		if hit.Load() {
			t.Error("the server was hit despite construction failing -- the policy check must run before any client is used")
		}
	})

	t.Run("EndpointPolicy permits an explicitly allowed BaseURL", func(t *testing.T) {
		server, hit := recordingServer(t, openAIResponsesBody("policy allowed"))
		defer server.Close()

		u, err := url.Parse(server.URL)
		if err != nil {
			t.Fatalf("parsing test server URL: %v", err)
		}

		provider, err := NewOpenAIProvider(ProviderConfig{
			APIKey:  "test-key",
			BaseURL: server.URL,
			EndpointPolicy: &types.EndpointPolicy{
				AllowedSchemes:        []string{u.Scheme},
				AllowedHosts:          []string{u.Hostname()},
				AllowPrivateAddresses: true, // httptest.NewServer binds to loopback
			},
		})
		if err != nil {
			t.Fatalf("NewOpenAIProvider: %v", err)
		}
		if _, err := provider.Complete(context.Background(), CompletionRequest{UserPrompt: "hi"}); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if !hit.Load() {
			t.Error("the mock server was never hit despite an explicitly permitting policy")
		}
	})
}

// recordingServer returns an httptest server that always answers 200 with
// body, and an atomic flag that flips true the first time it is hit.
func recordingServer(t *testing.T, body string) (*httptest.Server, *atomic.Bool) {
	t.Helper()
	var hit atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Store(true)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(body))
	}))
	return server, &hit
}

// recordingTransport wraps http.DefaultTransport and flips dialed to true
// the moment it is asked to round-trip a request -- proof that THIS
// transport, not some other client built internally, is what carried the
// call.
type recordingTransport struct {
	dialed *atomic.Bool
}

func (t recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.dialed.Store(true)
	return http.DefaultTransport.RoundTrip(req)
}

// failingTransport always fails, simulating a transport that refuses to
// dial (a proxy misconfiguration, a test double, mTLS rejection).
type failingTransport struct {
	err error
}

func (t failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, t.err
}

func openAIResponsesBody(text string) string {
	return `{"id":"resp_1","output":[{"type":"message","content":[{"type":"output_text","text":"` + text + `"}]}],"model":"gpt-test","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
}

func chatCompletionsBody(text string) string {
	return `{"id":"cmpl_1","choices":[{"message":{"role":"assistant","content":"` + text + `"},"finish_reason":"stop"}],"model":"test-model","usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
}

func anthropicBody(text string) string {
	return `{"id":"msg_1","content":[{"type":"text","text":"` + text + `"}],"stop_reason":"end_turn","model":"claude-test","usage":{"input_tokens":1,"output_tokens":1}}`
}
