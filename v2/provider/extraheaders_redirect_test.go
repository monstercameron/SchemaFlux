package provider_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/provider"
)

func TestExportedOpenAIProviderDoesNotForwardConfiguredHeadersAcrossRedirect(t *testing.T) {
	var received string
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Header.Get("X-API-Key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	defer destination.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	p, err := provider.New("openai", provider.Config{
		APIKey:  "test-api-key",
		BaseURL: source.URL,
		Headers: map[string]string{"X-API-Key": "tenant-secret"},
	})
	if err != nil {
		t.Fatalf("provider.New: %v", err)
	}
	if _, err := p.Complete(context.Background(), schemaflux.Request{Model: "gpt-6-luna", User: "hello"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if received != "" {
		t.Fatalf("redirect destination received X-API-Key %q", received)
	}
}

func TestExportedOpenAICompatibleProviderDoesNotForwardConfiguredHeadersAcrossRedirect(t *testing.T) {
	var received string
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Header.Get("X-Tenant")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"test-model","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer destination.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/chat/completions", http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	p, err := provider.OpenAICompatible("proxy", source.URL, provider.Config{
		APIKey:  "test-api-key",
		Headers: map[string]string{"X-Tenant": "tenant-secret"},
	})
	if err != nil {
		t.Fatalf("provider.OpenAICompatible: %v", err)
	}
	if _, err := p.Complete(context.Background(), schemaflux.Request{Model: "test-model", User: "hello"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if received != "" {
		t.Fatalf("redirect destination received X-Tenant %q", received)
	}
}

func Example_openAIProviderExtraHeadersRedirect() {
	var leaked bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("X-API-Key") != ""
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	p, err := provider.New("openai", provider.Config{
		APIKey: "test-api-key", BaseURL: source.URL,
		Headers: map[string]string{"X-API-Key": "tenant-secret"},
	})
	if err != nil {
		fmt.Println("provider configuration failed")
		return
	}
	_, err = p.Complete(context.Background(), schemaflux.Request{Model: "gpt-6-luna", User: "hello"})
	fmt.Println("request succeeded:", err == nil, "cross-origin header forwarded:", leaked)
	// Output: request succeeded: true cross-origin header forwarded: false
}
