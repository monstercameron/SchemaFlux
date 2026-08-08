package schemaflux_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
)

// capturingResponsesAPI stands up a fake OpenAI Responses endpoint like
// responsesAPI (provider_integration_test.go), but also hands back every
// request body it received, so a test can inspect the field the provider
// actually sent -- the recorded-request boundary the P-009 verify line asks
// for: public API -> ops (key computed) -> OpenAI provider (key sent) -> HTTP.
func capturingResponsesAPI(t *testing.T, body string) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var captured []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		captured = append(captured, decoded)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	schemaflux.NewClient("test-key").WithProviderConfig("openai", schemaflux.ProviderConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
	})
	return server, &captured
}

type cacheKeyPerson struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

type cacheKeyCompany struct {
	Legal string `json:"legal"`
}

// TestIntegrationPromptCacheKeyStableAcrossIdenticalCalls is the P-009 verify
// line's first half: two identical Extract calls must reach the provider
// carrying the same prompt_cache_key, so a repeat request can route to the
// server that already holds its stable prefix.
func TestIntegrationPromptCacheKeyStableAcrossIdenticalCalls(t *testing.T) {
	_, captured := capturingResponsesAPI(t, message(`{"name":"Ada Lovelace","age":36}`))

	for i := 0; i < 2; i++ {
		if _, err := schemaflux.Extract[cacheKeyPerson]("Ada Lovelace is 36.", schemaflux.NewExtractOptions()); err != nil {
			t.Fatalf("Extract call %d: %v", i, err)
		}
	}

	if len(*captured) != 2 {
		t.Fatalf("expected 2 recorded requests, got %d", len(*captured))
	}
	key1, _ := (*captured)[0]["prompt_cache_key"].(string)
	key2, _ := (*captured)[1]["prompt_cache_key"].(string)
	if key1 == "" {
		t.Fatal("expected a non-empty prompt_cache_key")
	}
	if key1 != key2 {
		t.Fatalf("expected identical calls to share a cache key, got %q vs %q", key1, key2)
	}
}

// TestIntegrationPromptCacheKeyDiffersAcrossSchemas is the P-009 verify line's
// second half: a different target type -- a different schema, a different
// operation-and-schema identity -- must reach the provider with a different
// key, or the second type's answer could be served from the first's prefix.
func TestIntegrationPromptCacheKeyDiffersAcrossSchemas(t *testing.T) {
	// Each type needs a server answering with a body that type can actually
	// decode into -- a schema violation would retry and fail before a request
	// worth comparing ever got recorded.
	_, capturedPerson := capturingResponsesAPI(t, message(`{"name":"Ada Lovelace","age":36}`))
	if _, err := schemaflux.Extract[cacheKeyPerson]("Ada Lovelace is 36.", schemaflux.NewExtractOptions()); err != nil {
		t.Fatalf("Extract person: %v", err)
	}

	_, capturedCompany := capturingResponsesAPI(t, message(`{"legal":"Ada Lovelace Analytical Engines Ltd"}`))
	if _, err := schemaflux.Extract[cacheKeyCompany]("Ada Lovelace's company.", schemaflux.NewExtractOptions()); err != nil {
		t.Fatalf("Extract company: %v", err)
	}

	if len(*capturedPerson) != 1 {
		t.Fatalf("expected 1 recorded person request, got %d", len(*capturedPerson))
	}
	if len(*capturedCompany) != 1 {
		t.Fatalf("expected 1 recorded company request, got %d", len(*capturedCompany))
	}
	key1, _ := (*capturedPerson)[0]["prompt_cache_key"].(string)
	key2, _ := (*capturedCompany)[0]["prompt_cache_key"].(string)
	if key1 == "" || key2 == "" {
		t.Fatalf("expected non-empty cache keys, got %q and %q", key1, key2)
	}
	if key1 == key2 {
		t.Fatalf("expected different schemas to mint different cache keys, both were %q", key1)
	}
}

// TestIntegrationPromptCacheKeyIgnoresSteering is the Revised note's addendum:
// steering is volatile and lives outside the stable prefix, so two calls
// differing only in steering must share a cache key. If they did not, every
// call would mint a fresh key, because steering is the one piece of the
// request a caller is expected to vary from call to call.
func TestIntegrationPromptCacheKeyIgnoresSteering(t *testing.T) {
	_, captured := capturingResponsesAPI(t, message(`{"name":"Ada Lovelace","age":36}`))

	if _, err := schemaflux.Extract[cacheKeyPerson]("Ada Lovelace is 36.", schemaflux.NewExtractOptions()); err != nil {
		t.Fatalf("Extract without steering: %v", err)
	}
	if _, err := schemaflux.Extract[cacheKeyPerson]("Ada Lovelace is 36.",
		schemaflux.NewExtractOptions().WithSteering("Be extra precise about the age.")); err != nil {
		t.Fatalf("Extract with steering: %v", err)
	}

	if len(*captured) != 2 {
		t.Fatalf("expected 2 recorded requests, got %d", len(*captured))
	}
	key1, _ := (*captured)[0]["prompt_cache_key"].(string)
	key2, _ := (*captured)[1]["prompt_cache_key"].(string)
	if key1 == "" {
		t.Fatal("expected a non-empty prompt_cache_key")
	}
	if key1 != key2 {
		t.Fatalf("expected a steering-only difference to share a cache key, got %q vs %q", key1, key2)
	}
}
