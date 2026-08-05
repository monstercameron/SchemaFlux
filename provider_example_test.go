package schemaflux_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
)

type expenseClaim struct {
	Claimant string  `json:"claimant"`
	Amount   float64 `json:"amount"`
	Category string  `json:"category"`
}

// Example_secondaryProvider shows selecting Cerebras instead of the default.
// The only thing that changes at the call site is the client; the operations,
// the types, and the guarantees are the same.
//
// It runs against a local stand-in so the example needs no credential and
// spends nothing. In an application the BaseURL is omitted -- the provider
// knows its own endpoint -- and the key comes from CEREBRAS_API_KEY.
func Example_secondaryProvider() {
	// A stand-in for api.cerebras.ai, so this example runs under `go test`.
	cerebras := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"model":"gemma-4-31b","choices":[{"finish_reason":"stop",
			"message":{"content":"{\"claimant\":\"R. Okonkwo\",\"amount\":12000,\"category\":\"office snacks\"}"}}],
			"usage":{"prompt_tokens":90,"completion_tokens":28,"total_tokens":118}}`)
	}))
	defer cerebras.Close()

	client := schemaflux.NewClient("cerebras-key").
		WithProviderConfig("cerebras", schemaflux.ProviderConfig{
			BaseURL: cerebras.URL,
			Timeout: 30 * time.Second,
		})
	if err := client.Err(); err != nil {
		fmt.Println("configuration failed:", err)
		return
	}
	schemaflux.SetDefaultClient(client)

	claim, err := schemaflux.Extract[expenseClaim](
		"R. Okonkwo submitted $12,000 for office snacks.",
		schemaflux.NewExtractOptions())
	if err != nil {
		fmt.Println("extraction failed:", err)
		return
	}

	fmt.Println("claimant:", claim.Claimant)
	fmt.Println("amount:", claim.Amount)
	fmt.Println("category:", claim.Category)

	// Output:
	// claimant: R. Okonkwo
	// amount: 12000
	// category: office snacks
}

// Example_secondaryProviderEnforcesTheSchema shows the part that is easy to
// assume and was not true until recently: the schema a typed operation
// generates reaches the secondary provider as a strict contract, not as a
// request to please emit some JSON.
//
// The stand-in asserts on what arrived rather than on what came back, because
// from the result alone a caller cannot tell the two apart -- which is exactly
// why the downgrade went unnoticed.
func Example_secondaryProviderEnforcesTheSchema() {
	var sentFormat string

	cerebras := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sent := string(body)
		switch {
		case strings.Contains(sent, `"type":"json_schema"`) && strings.Contains(sent, `"strict":true`):
			sentFormat = "a strict schema"
		case strings.Contains(sent, `"json_object"`):
			sentFormat = "a request for some JSON"
		default:
			sentFormat = "nothing"
		}
		_, _ = io.WriteString(w, `{"model":"gemma-4-31b","choices":[{"finish_reason":"stop",
			"message":{"content":"{\"claimant\":\"R. Okonkwo\",\"amount\":12000,\"category\":\"snacks\"}"}}]}`)
	}))
	defer cerebras.Close()

	client := schemaflux.NewClient("cerebras-key").
		WithProviderConfig("cerebras", schemaflux.ProviderConfig{
			BaseURL: cerebras.URL,
			Timeout: 30 * time.Second,
		})
	schemaflux.SetDefaultClient(client)

	if _, err := schemaflux.Extract[expenseClaim](
		"R. Okonkwo submitted $12,000 for snacks.",
		schemaflux.NewExtractOptions()); err != nil {
		fmt.Println("extraction failed:", err)
		return
	}

	fmt.Println("what reached the provider:", sentFormat)

	// Output:
	// what reached the provider: a strict schema
}

// Example_rateLimitsAreWaitedOut shows the resilience behaviour that matters on
// a shared or free-tier endpoint. A provider that limits per minute answers a
// 429 with the wait it wants; the library uses that number instead of its own
// backoff, which tops out at five seconds and so could never clear the window.
func Example_rateLimitsAreWaitedOut() {
	var attempts int

	cerebras := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			// A real endpoint says 53 here; one second keeps the example quick.
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"message":"Requests per minute limit exceeded"}`)
			return
		}
		_, _ = io.WriteString(w, `{"model":"gemma-4-31b","choices":[{"finish_reason":"stop",
			"message":{"content":"{\"claimant\":\"R. Okonkwo\",\"amount\":12000,\"category\":\"snacks\"}"}}]}`)
	}))
	defer cerebras.Close()

	client := schemaflux.NewClient("cerebras-key").
		WithProviderConfig("cerebras", schemaflux.ProviderConfig{
			BaseURL: cerebras.URL,
			Timeout: 30 * time.Second,
		})
	schemaflux.SetDefaultClient(client)

	start := time.Now()
	claim, err := schemaflux.Extract[expenseClaim](
		"R. Okonkwo submitted $12,000 for snacks.",
		schemaflux.NewExtractOptions())
	elapsed := time.Since(start)

	fmt.Println("error is nil:", err == nil)
	fmt.Println("claimant:", claim.Claimant)
	fmt.Println("attempts:", attempts)
	fmt.Println("waited out the window:", elapsed >= time.Second)

	// Output:
	// error is nil: true
	// claimant: R. Okonkwo
	// attempts: 2
	// waited out the window: true
}
