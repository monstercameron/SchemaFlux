package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A gateway or proxy in front of a provider can answer a rejected request with
// a message that quotes the request back: the API key it was given, a fragment
// of the prompt, the schema it refused. The library's rule is that nothing it
// returns in an error's text, and nothing it logs, carries a credential, the
// caller's data or the provider's free text, so these tests point every
// provider that makes HTTP calls at a server that does exactly that and look
// for the three markers in every place an error's text can reach a log.

const (
	// The key does not match any shape types.RedactCredentials knows, so a leak
	// of it is only stopped by removing the configured key itself.
	disclosureKey          = "gw-key-7f3a91c2d41d8cd98f00b204e9800998"
	disclosureUserMarker   = "USER-PROMPT-MARKER-4c1e9b"
	disclosureSystemMarker = "SYSTEM-PROMPT-MARKER-82ad07"
)

// disclosureMessage is what a careless gateway writes as a vendor message.
const disclosureMessage = "Incorrect API key provided: " + disclosureKey +
	". The request was refused near \"" + disclosureUserMarker +
	"\" under the instructions \"" + disclosureSystemMarker + "\"."

// disclosureLeaks names each marker found in text.
func disclosureLeaks(text string) []string {
	var found []string
	for name, marker := range map[string]string{
		"the API key":       disclosureKey,
		"the user prompt":   disclosureUserMarker,
		"the system prompt": disclosureSystemMarker,
	} {
		if strings.Contains(text, marker) {
			found = append(found, name)
		}
	}
	return found
}

// disclosureBodies are the two shapes a vendor writes an error in: nested under
// "error", as OpenAI and Anthropic do, and at the top level, as Cerebras does.
func disclosureBodies(message string) map[string]string {
	quoted, _ := json.Marshal(message)
	return map[string]string{
		"nested": `{"type":"error","error":{"type":"invalid_request_error","code":"invalid_api_key","param":"response_format.json_schema.schema","message":` + string(quoted) + `}}`,
		"flat":   `{"type":"invalid_request_error","code":"invalid_api_key","param":"messages","message":` + string(quoted) + `}`,
	}
}

type disclosureTarget struct {
	name string
	// build makes the provider at the server's address, configured with the key.
	build func(t *testing.T, baseURL string) Provider
}

func disclosureTargets() []disclosureTarget {
	config := func(baseURL string) ProviderConfig {
		return ProviderConfig{APIKey: disclosureKey, BaseURL: baseURL}
	}
	must := func(t *testing.T, provider Provider, err error) Provider {
		t.Helper()
		if err != nil {
			t.Fatalf("building the provider: %v", err)
		}
		return provider
	}
	return []disclosureTarget{
		{"openai", func(t *testing.T, url string) Provider {
			provider, err := NewOpenAIProvider(config(url))
			return must(t, provider, err)
		}},
		{"anthropic", func(t *testing.T, url string) Provider {
			provider, err := NewAnthropicProvider(config(url))
			return must(t, provider, err)
		}},
		{"compatible", func(t *testing.T, url string) Provider {
			provider, err := NewOpenAICompatibleProvider("acme", config(url))
			return must(t, provider, err)
		}},
		{"openrouter", func(t *testing.T, url string) Provider {
			provider, err := NewOpenRouterProvider(config(url))
			return must(t, provider, err)
		}},
		{"cerebras", func(t *testing.T, url string) Provider {
			provider, err := NewCerebrasProvider(config(url))
			return must(t, provider, err)
		}},
		{"deepseek", func(t *testing.T, url string) Provider {
			provider, err := NewDeepSeekProvider(config(url))
			return must(t, provider, err)
		}},
		{"qwen", func(t *testing.T, url string) Provider {
			provider, err := NewQwenProvider(config(url))
			return must(t, provider, err)
		}},
		{"zai", func(t *testing.T, url string) Provider {
			provider, err := NewZAIProvider(config(url))
			return must(t, provider, err)
		}},
	}
}

func disclosureRequest() CompletionRequest {
	return CompletionRequest{
		Model:        "some-model",
		SystemPrompt: "Instructions: " + disclosureSystemMarker,
		UserPrompt:   "The record: " + disclosureUserMarker,
	}
}

// disclosureServer answers every request with status and body.
func disclosureServer(t *testing.T, status int, header http.Header, body string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		for name, values := range header {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server.URL
}

// disclosureRenderings is every text the error can be turned into by a caller
// who logs it, or formats it, or marshals it, labelled for the failure message.
func disclosureRenderings(t *testing.T, err error) map[string]string {
	t.Helper()
	renderings := map[string]string{
		"Error()": err.Error(),
		"%v":      fmt.Sprintf("%v", err),
		"%+v":     fmt.Sprintf("%+v", err),
		"%s":      fmt.Sprintf("%s", err),
		"%#v":     fmt.Sprintf("%#v", err),
		"%q":      fmt.Sprintf("%q", err),
		"wrapped": fmt.Errorf("extract: %w", err).Error(),
	}

	// Every error in the chain, each of which a caller may log on its own.
	position := 0
	for link := err; link != nil; link = errors.Unwrap(link) {
		renderings[fmt.Sprintf("chain %d Error()", position)] = link.Error()
		renderings[fmt.Sprintf("chain %d %%+v", position)] = fmt.Sprintf("%+v", link)
		position++
	}

	// The same error as a struct value, which has no Error method and so is
	// printed by reflection unless the type says how.
	var apiErr *APIError
	var rateLimit *RateLimitError
	if errors.As(err, &rateLimit) {
		renderings["rate limit value %v"] = fmt.Sprintf("%v", *rateLimit)
		renderings["rate limit value %+v"] = fmt.Sprintf("%+v", *rateLimit)
		renderings["rate limit value %#v"] = fmt.Sprintf("%#v", *rateLimit)
	}
	if errors.As(err, &apiErr) {
		renderings["value %v"] = fmt.Sprintf("%v", *apiErr)
		renderings["value %+v"] = fmt.Sprintf("%+v", *apiErr)
		renderings["value %#v"] = fmt.Sprintf("%#v", *apiErr)
	}

	// What a caller who marshals the error, or a struct holding it, writes.
	for label, value := range map[string]any{
		"json error":            err,
		"json holder":           struct{ Err error }{err},
		"json holder of values": struct{ Err any }{apiErrValue(apiErr)},
	} {
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatalf("%s: json.Marshal: %v", label, marshalErr)
		}
		renderings[label] = string(encoded)
	}

	// What slog writes for it, under both built-in handlers, whether the error
	// is passed as an error, as a struct value, or inside a group.
	handlers := map[string]func(io.Writer) slog.Handler{
		"slog text": func(w io.Writer) slog.Handler {
			return slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug})
		},
		"slog json": func(w io.Writer) slog.Handler {
			return slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug})
		},
	}
	for handlerName, newHandler := range handlers {
		var out bytes.Buffer
		logger := slog.New(newHandler(&out))
		logger.Error("call failed", "error", err)
		logger.Error("call failed", slog.Any("error", err))
		logger.Error("call failed", slog.Group("call", slog.Any("error", err)))
		logger.Error("call failed", "holder", struct{ Err error }{err})
		if apiErr != nil {
			logger.Error("call failed", "value", *apiErr)
		}
		if rateLimit != nil {
			logger.Error("call failed", "value", *rateLimit)
		}
		renderings[handlerName] = out.String()
	}
	return renderings
}

func apiErrValue(err *APIError) any {
	if err == nil {
		return nil
	}
	return *err
}

// requireNoDisclosure fails for every rendering of err that carries a marker.
func requireNoDisclosure(t *testing.T, err error) {
	t.Helper()
	for label, text := range disclosureRenderings(t, err) {
		if leaks := disclosureLeaks(text); len(leaks) > 0 {
			t.Errorf("%s carries %s:\n%s", label, strings.Join(leaks, " and "), text)
		}
	}
}

// requireFieldsHoldNoKey fails when the configured key is held by the error's
// own fields. Withholding it from the text is not enough: a caller who reads
// Message or Body, or a debugger that dumps the struct, would still have it.
func requireFieldsHoldNoKey(t *testing.T, err error) {
	t.Helper()
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("the error is not an *APIError: %T", err)
	}
	for field, value := range map[string]string{
		"Message": apiErr.Message, "Body": apiErr.Body,
		"Type": apiErr.Type, "Code": apiErr.Code, "Param": apiErr.Param,
	} {
		if strings.Contains(value, disclosureKey) {
			t.Errorf("APIError.%s holds the configured key: %q", field, value)
		}
	}
}

func TestEveryProviderKeepsTheKeyAndTheRequestOutOfAnErrorsText(t *testing.T) {
	statuses := []int{
		http.StatusUnauthorized, http.StatusBadRequest,
		http.StatusTooManyRequests, http.StatusInternalServerError,
	}
	for _, target := range disclosureTargets() {
		for _, status := range statuses {
			for shape, body := range disclosureBodies(disclosureMessage) {
				t.Run(fmt.Sprintf("%s/%d/%s", target.name, status, shape), func(t *testing.T) {
					url := disclosureServer(t, status, http.Header{"Retry-After": {"7"}}, body)
					provider := target.build(t, url)

					_, err := provider.Complete(context.Background(), disclosureRequest())
					if err == nil {
						t.Fatal("the call succeeded")
					}
					if got, ok := StatusCodeFrom(err); !ok || got != status {
						t.Fatalf("StatusCodeFrom = %d, %v; want %d", got, ok, status)
					}
					requireFieldsHoldNoKey(t, err)
					requireNoDisclosure(t, err)

					// The text still says what failed, in the library's own words.
					text := err.Error()
					for _, want := range []string{
						fmt.Sprintf("status %d", status), "invalid_request_error", "invalid_api_key", "withheld",
					} {
						if !strings.Contains(text, want) {
							t.Errorf("the error does not say %q: %s", want, text)
						}
					}
				})
			}
		}
	}
}

func TestTheOpenGateAddsTheVendorsWordsButNeverTheKey(t *testing.T) {
	t.Setenv(errorDetailEnvVar, "1")
	for _, target := range disclosureTargets() {
		for shape, body := range disclosureBodies(disclosureMessage) {
			t.Run(target.name+"/"+shape, func(t *testing.T) {
				url := disclosureServer(t, http.StatusUnauthorized, nil, body)
				_, err := target.build(t, url).Complete(context.Background(), disclosureRequest())
				if err == nil {
					t.Fatal("the call succeeded")
				}
				requireFieldsHoldNoKey(t, err)
				for label, text := range disclosureRenderings(t, err) {
					if strings.Contains(text, disclosureKey) {
						t.Errorf("%s carries the key with the gate open:\n%s", label, text)
					}
				}
				if !strings.Contains(err.Error(), disclosureUserMarker) {
					t.Errorf("the open gate did not print the vendor's message: %s", err)
				}
			})
		}
	}
}

func TestDetailReturnsTheMessageAndTheBodyWithoutTheKey(t *testing.T) {
	for _, target := range disclosureTargets() {
		t.Run(target.name, func(t *testing.T) {
			url := disclosureServer(t, http.StatusBadRequest, nil, disclosureBodies(disclosureMessage)["nested"])
			_, err := target.build(t, url).Complete(context.Background(), disclosureRequest())
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("not an *APIError: %v", err)
			}
			detail := apiErr.Detail()
			if strings.Contains(detail, disclosureKey) {
				t.Errorf("Detail() carries the key:\n%s", detail)
			}
			if !strings.Contains(detail, "message: ") || !strings.Contains(detail, "body: ") {
				t.Errorf("Detail() does not return both the message and the body:\n%s", detail)
			}
			if !strings.Contains(apiErr.Message, disclosureUserMarker) || !strings.Contains(apiErr.Body, disclosureSystemMarker) {
				t.Errorf("the message and the body were not kept for a caller who asks:\nMessage: %q\nBody: %q", apiErr.Message, apiErr.Body)
			}
		})
	}
}

// A failure the vendor reports inside a 200 answer, or inside a stream, has no
// status line and is built on a different path from a non-200 one.
func TestAFailureReportedInsideAnAnswerKeepsTheKeyOutToo(t *testing.T) {
	quoted, _ := json.Marshal(disclosureMessage)
	message := string(quoted)

	t.Run("chat completions answer with an error object", func(t *testing.T) {
		body := `{"error":{"type":"invalid_request_error","code":"invalid_api_key","message":` + message + `}}`
		provider, err := NewOpenAICompatibleProvider("acme", ProviderConfig{APIKey: disclosureKey, BaseURL: disclosureServer(t, http.StatusOK, nil, body)})
		if err != nil {
			t.Fatal(err)
		}
		_, callErr := provider.Complete(context.Background(), disclosureRequest())
		if callErr == nil {
			t.Fatal("the call succeeded")
		}
		requireFieldsHoldNoKey(t, callErr)
		requireNoDisclosure(t, callErr)
	})

	t.Run("chat completions throttle inside a 200", func(t *testing.T) {
		body := `{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":` + message + `}}`
		provider, err := NewOpenAICompatibleProvider("acme", ProviderConfig{APIKey: disclosureKey, BaseURL: disclosureServer(t, http.StatusOK, http.Header{"Retry-After": {"3"}}, body)})
		if err != nil {
			t.Fatal(err)
		}
		_, callErr := provider.Complete(context.Background(), disclosureRequest())
		var rateLimit *RateLimitError
		if !errors.As(callErr, &rateLimit) {
			t.Fatalf("not a *RateLimitError: %v", callErr)
		}
		requireFieldsHoldNoKey(t, callErr)
		requireNoDisclosure(t, callErr)
	})

	t.Run("responses answer with status failed", func(t *testing.T) {
		body := `{"id":"resp_1","status":"failed","error":{"type":"invalid_request_error","code":"invalid_api_key","message":` + message + `}}`
		provider, err := NewOpenAIProvider(ProviderConfig{APIKey: disclosureKey, BaseURL: disclosureServer(t, http.StatusOK, nil, body)})
		if err != nil {
			t.Fatal(err)
		}
		_, callErr := provider.Complete(context.Background(), disclosureRequest())
		if callErr == nil {
			t.Fatal("the call succeeded")
		}
		requireFieldsHoldNoKey(t, callErr)
		requireNoDisclosure(t, callErr)
	})

	streams := map[string]string{
		"an error event": `{"type":"error","code":"invalid_api_key","message":` + message + `}`,
		"a failed event": `{"type":"response.failed","response":{"id":"r","status":"failed","error":{"type":"invalid_request_error","code":"invalid_api_key","message":` + message + `}}}`,
	}
	for name, event := range streams {
		t.Run("stream with "+name, func(t *testing.T) {
			body := "data: " + event + "\n\n"
			provider, err := NewOpenAIProvider(ProviderConfig{APIKey: disclosureKey, BaseURL: disclosureServer(t, http.StatusOK, nil, body)})
			if err != nil {
				t.Fatal(err)
			}
			var streamErr error
			for _, err := range provider.CompleteStream(context.Background(), disclosureRequest()) {
				if err != nil {
					streamErr = err
					break
				}
			}
			if streamErr == nil {
				t.Fatal("the stream ended without an error")
			}
			requireFieldsHoldNoKey(t, streamErr)
			requireNoDisclosure(t, streamErr)
		})
	}

	t.Run("stream rejected before it starts", func(t *testing.T) {
		url := disclosureServer(t, http.StatusUnauthorized, nil, disclosureBodies(disclosureMessage)["nested"])
		provider, err := NewOpenAIProvider(ProviderConfig{APIKey: disclosureKey, BaseURL: url})
		if err != nil {
			t.Fatal(err)
		}
		var streamErr error
		for _, err := range provider.CompleteStream(context.Background(), disclosureRequest()) {
			streamErr = err
			break
		}
		if streamErr == nil {
			t.Fatal("the stream ended without an error")
		}
		requireFieldsHoldNoKey(t, streamErr)
		requireNoDisclosure(t, streamErr)
	})

	t.Run("embeddings", func(t *testing.T) {
		url := disclosureServer(t, http.StatusUnauthorized, nil, disclosureBodies(disclosureMessage)["nested"])
		provider, err := NewOpenAIProvider(ProviderConfig{APIKey: disclosureKey, BaseURL: url})
		if err != nil {
			t.Fatal(err)
		}
		_, embedErr := provider.Embed(context.Background(), EmbeddingRequest{Input: []string{"The record: " + disclosureUserMarker}})
		if embedErr == nil {
			t.Fatal("the call succeeded")
		}
		requireFieldsHoldNoKey(t, embedErr)
		requireNoDisclosure(t, embedErr)
	})
}

// A body that is not a structured error, a proxy's HTML page, has no message to
// withhold and is withheld whole.
func TestABodyThatIsNotAStructuredErrorIsWithheldWhole(t *testing.T) {
	page := "<html><body>Bad gateway for " + disclosureKey + " near " + disclosureUserMarker + " " + disclosureSystemMarker + "</body></html>"
	for _, target := range disclosureTargets() {
		t.Run(target.name, func(t *testing.T) {
			url := disclosureServer(t, http.StatusBadGateway, nil, page)
			_, err := target.build(t, url).Complete(context.Background(), disclosureRequest())
			if err == nil {
				t.Fatal("the call succeeded")
			}
			requireFieldsHoldNoKey(t, err)
			requireNoDisclosure(t, err)
			if !strings.Contains(err.Error(), "body withheld") {
				t.Errorf("the error does not say the body was withheld: %s", err)
			}
		})
	}
}
