package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// captureRequest stands a server up, runs one completion against it, and hands
// back the body the provider actually put on the wire. What the provider sends
// is the whole subject here: the defect being guarded against was invisible in
// the response.
func captureRequest(t *testing.T, req CompletionRequest, reply string, status int) map[string]any {
	t.Helper()

	var captured map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &captured)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	defer server.Close()

	provider, err := NewOpenAICompatibleProvider("testvendor", ProviderConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewOpenAICompatibleProvider: %v", err)
	}

	_, _ = provider.Complete(context.Background(), req)

	if captured == nil {
		t.Fatal("no request body was captured")
	}
	return captured
}

const okReply = `{"model":"gemma-4-31b","choices":[{"finish_reason":"stop","message":{"content":"{\"ok\":true}"}}],` +
	`"usage":{"prompt_tokens":11,"completion_tokens":5,"total_tokens":16}}`

// The defect: a typed operation's schema was dropped and the request went out
// asking only for "some JSON". Constrained decoding on OpenAI, an unconstrained
// guess everywhere else, and nothing at the call site to tell them apart.
func TestCompatibleProviderSendsTheStrictSchema(t *testing.T) {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"queue"},
		"properties": map[string]any{
			"queue": map[string]any{"type": "string"},
		},
	}

	body := captureRequest(t, CompletionRequest{
		Model:          "gemma-4-31b",
		SystemPrompt:   "classify",
		UserPrompt:     "a ticket",
		ResponseFormat: "json",
		JSONSchema:     schema,
		SchemaName:     "triage",
	}, okReply, http.StatusOK)

	format, ok := body["response_format"].(map[string]any)
	if !ok {
		t.Fatalf("no response_format was sent: %#v", body)
	}
	if format["type"] != "json_schema" {
		t.Fatalf("response_format type = %v, want json_schema -- the schema was dropped", format["type"])
	}

	wrapper, ok := format["json_schema"].(map[string]any)
	if !ok {
		t.Fatalf("no json_schema payload: %#v", format)
	}
	if wrapper["strict"] != true {
		t.Error("strict is not set; without it the schema is a suggestion, not a contract")
	}
	if wrapper["name"] != "triage" {
		t.Errorf("name = %v, want the caller's schema name", wrapper["name"])
	}

	sent, ok := wrapper["schema"].(map[string]any)
	if !ok {
		t.Fatalf("no schema in the payload: %#v", wrapper)
	}
	if sent["additionalProperties"] != false {
		t.Error("additionalProperties:false did not survive; the vendor requires it on every object")
	}
	if _, present := sent["properties"]; !present {
		t.Error("the schema's properties were lost")
	}
}

// A schema name is required by the API, so an unnamed schema must not produce a
// request that is rejected for a reason the caller did not cause.
func TestCompatibleProviderNamesAnUnnamedSchema(t *testing.T) {
	body := captureRequest(t, CompletionRequest{
		Model:          "gemma-4-31b",
		UserPrompt:     "x",
		ResponseFormat: "json",
		JSONSchema:     map[string]any{"type": "object"},
	}, okReply, http.StatusOK)

	wrapper := body["response_format"].(map[string]any)["json_schema"].(map[string]any)
	if name, _ := wrapper["name"].(string); strings.TrimSpace(name) == "" {
		t.Error("an unnamed schema was sent with no name, which the API rejects")
	}
}

// Without a schema there is nothing to enforce, and json_object is the honest
// request. Sending an empty json_schema instead would be a 400.
func TestCompatibleProviderFallsBackToJSONObject(t *testing.T) {
	body := captureRequest(t, CompletionRequest{
		Model:          "gemma-4-31b",
		UserPrompt:     "x",
		ResponseFormat: "json",
	}, okReply, http.StatusOK)

	format := body["response_format"].(map[string]any)
	if format["type"] != "json_object" {
		t.Errorf("type = %v, want json_object when there is no schema", format["type"])
	}
	if _, present := format["json_schema"]; present {
		t.Error("an empty json_schema was sent, which the API rejects")
	}
}

// A text request must not carry a response_format at all.
func TestCompatibleProviderOmitsResponseFormatForText(t *testing.T) {
	body := captureRequest(t, CompletionRequest{
		Model:          "gemma-4-31b",
		UserPrompt:     "x",
		ResponseFormat: "text",
	}, okReply, http.StatusOK)

	if _, present := body["response_format"]; present {
		t.Errorf("a text request carried a response_format: %#v", body["response_format"])
	}
}

// The vendors reject these keywords outright rather than ignoring them, so a
// `time.Time` field -- which the generator annotates with format: date-time --
// would take an otherwise valid extraction down with a 400.
func TestCompatibleProviderStripsUnsupportedSchemaKeywords(t *testing.T) {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"when":  map[string]any{"type": "string", "format": "date-time"},
			"code":  map[string]any{"type": "string", "pattern": "^[A-Z]+$", "minLength": float64(2)},
			"tags":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": float64(5)},
			"score": map[string]any{"type": "number", "minimum": float64(0), "maximum": float64(1)},
			"nested": map[string]any{
				"type":       "object",
				"properties": map[string]any{"deep": map[string]any{"type": "string", "format": "email"}},
			},
		},
	}

	body := captureRequest(t, CompletionRequest{
		Model:          "gemma-4-31b",
		UserPrompt:     "x",
		ResponseFormat: "json",
		JSONSchema:     schema,
		SchemaName:     "s",
	}, okReply, http.StatusOK)

	rendered, err := json.Marshal(body["response_format"])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, banned := range []string{"format", "pattern", "minLength", "maxItems", "minimum", "maximum"} {
		if strings.Contains(string(rendered), `"`+banned+`"`) {
			t.Errorf("%q reached the wire; the vendor rejects the whole request for it", banned)
		}
	}

	// The types themselves must survive -- stripping annotations is a downgrade
	// only if it takes the contract with it.
	if !strings.Contains(string(rendered), `"deep"`) {
		t.Error("a nested property was lost while stripping keywords")
	}
	if !strings.Contains(string(rendered), `"items"`) {
		t.Error("the array's item type was lost while stripping keywords")
	}
}

// The caller's schema is reused across providers. Sanitising in place would
// mean one Cerebras call quietly strips annotations from the next OpenAI one.
func TestSanitisingDoesNotMutateTheCallersSchema(t *testing.T) {
	original := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"when": map[string]any{"type": "string", "format": "date-time"},
		},
	}

	sanitised := chatDialectSchema(original)
	if sanitised == nil {
		t.Fatal("chatDialectSchema returned nothing")
	}

	when := original["properties"].(map[string]any)["when"].(map[string]any)
	if when["format"] != "date-time" {
		t.Error("the caller's own schema was edited")
	}

	sanitisedWhen := sanitised["properties"].(map[string]any)["when"].(map[string]any)
	if _, present := sanitisedWhen["format"]; present {
		t.Error("format survived sanitisation")
	}
}

func TestChatDialectSchemaDegenerateInputs(t *testing.T) {
	if got := chatDialectSchema(nil); got != nil {
		t.Errorf("nil schema = %#v, want nil", got)
	}
	if got := chatDialectSchema(map[string]any{}); got != nil {
		t.Errorf("empty schema = %#v, want nil so the caller falls back to json_object", got)
	}
}

// A rejected schema is reported in the body and nowhere else. A bare status
// code sends the caller guessing at which of their fields the vendor disliked.
func TestCompatibleProviderSurfacesTheErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"schema exceeds maximum nesting depth"}}`)
	}))
	defer server.Close()

	provider, err := NewOpenAICompatibleProvider("testvendor", ProviderConfig{
		APIKey: "k", BaseURL: server.URL, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewOpenAICompatibleProvider: %v", err)
	}

	_, err = provider.Complete(context.Background(), CompletionRequest{Model: "m", UserPrompt: "x"})
	if err == nil {
		t.Fatal("a 400 must be an error")
	}
	if !strings.Contains(err.Error(), "nesting depth") {
		t.Errorf("the vendor's reason was swallowed: %v", err)
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("the status code is not reported: %v", err)
	}
	if !strings.Contains(err.Error(), "testvendor") {
		t.Errorf("the error does not say which provider failed: %v", err)
	}
}

// Some compatible vendors answer 200 with an error object in the body. Treating
// that as a success hands the caller an empty result with no error.
func TestCompatibleProviderRejectsAnErrorBodyOnA200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"error":{"message":"rate limit exceeded","type":"too_many_requests"}}`)
	}))
	defer server.Close()

	provider, _ := NewOpenAICompatibleProvider("testvendor", ProviderConfig{
		APIKey: "k", BaseURL: server.URL, Timeout: 5 * time.Second,
	})

	if _, err := provider.Complete(context.Background(), CompletionRequest{Model: "m", UserPrompt: "x"}); err == nil {
		t.Fatal("an error body on a 200 must not be reported as success")
	} else if !strings.Contains(err.Error(), "rate limit") {
		t.Errorf("the reason was lost: %v", err)
	}
}

// An answer that is not an answer must be an error rather than an empty string
// that parses as a zero value further up.
func TestCompatibleProviderRejectsEmptyAnswers(t *testing.T) {
	cases := []struct {
		name  string
		reply string
	}{
		{"no_choices", `{"model":"m","choices":[]}`},
		{"absent_choices", `{"model":"m"}`},
		{"empty_content", `{"model":"m","choices":[{"message":{"content":""}}]}`},
		{"whitespace_content", `{"model":"m","choices":[{"message":{"content":"   \n"}}]}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, tc.reply)
			}))
			defer server.Close()

			provider, _ := NewOpenAICompatibleProvider("testvendor", ProviderConfig{
				APIKey: "k", BaseURL: server.URL, Timeout: 5 * time.Second,
			})

			if _, err := provider.Complete(context.Background(), CompletionRequest{Model: "m", UserPrompt: "x"}); err == nil {
				t.Fatal("an empty answer must be an error")
			}
		})
	}
}

// Unparseable bodies -- a proxy's HTML error page is the common one.
func TestCompatibleProviderReportsUndecodableBodies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "<html>502 Bad Gateway</html>")
	}))
	defer server.Close()

	provider, _ := NewOpenAICompatibleProvider("testvendor", ProviderConfig{
		APIKey: "k", BaseURL: server.URL, Timeout: 5 * time.Second,
	})

	if _, err := provider.Complete(context.Background(), CompletionRequest{Model: "m", UserPrompt: "x"}); err == nil {
		t.Fatal("an undecodable body must be an error")
	}
}

// Usage has to arrive intact or the cost accounting silently reports zero.
func TestCompatibleProviderReportsUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"model":"gemma-4-31b","choices":[{"finish_reason":"stop","message":{"content":"hi"}}],
			"usage":{"prompt_tokens":120,"completion_tokens":34,"total_tokens":154,
			"prompt_tokens_details":{"cached_tokens":80},
			"completion_tokens_details":{"reasoning_tokens":12}}}`)
	}))
	defer server.Close()

	provider, _ := NewOpenAICompatibleProvider("cerebras", ProviderConfig{
		APIKey: "k", BaseURL: server.URL, Timeout: 5 * time.Second,
	})

	resp, err := provider.Complete(context.Background(), CompletionRequest{Model: "gemma-4-31b", UserPrompt: "x"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if resp.Usage.PromptTokens != 120 || resp.Usage.CompletionTokens != 34 || resp.Usage.TotalTokens != 154 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if resp.Usage.CachedTokens != 80 {
		t.Errorf("CachedTokens = %d, want 80 -- the cache discount is invisible without it", resp.Usage.CachedTokens)
	}
	if resp.Usage.ReasoningTokens != 12 {
		t.Errorf("ReasoningTokens = %d, want 12", resp.Usage.ReasoningTokens)
	}
	if resp.Provider != "cerebras" || resp.Model != "gemma-4-31b" {
		t.Errorf("provider/model = %q/%q", resp.Provider, resp.Model)
	}
}

// A truncated answer must be distinguishable from a complete one, or a caller
// parses half a JSON document and reports the parse failure as the model's
// fault rather than the token budget's.
func TestCompatibleProviderPreservesFinishReason(t *testing.T) {
	cases := map[string]string{
		"length":         "length",
		"stop":           "stop",
		"content_filter": "content_filter",
		"":               "stop", // absent means it finished
	}

	for given, want := range cases {
		t.Run("finish_"+given, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, `{"model":"m","choices":[{"finish_reason":"`+given+`","message":{"content":"partial"}}]}`)
			}))
			defer server.Close()

			provider, _ := NewOpenAICompatibleProvider("testvendor", ProviderConfig{
				APIKey: "k", BaseURL: server.URL, Timeout: 5 * time.Second,
			})

			resp, err := provider.Complete(context.Background(), CompletionRequest{Model: "m", UserPrompt: "x"})
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if resp.FinishReason != want {
				t.Errorf("FinishReason = %q, want %q", resp.FinishReason, want)
			}
		})
	}
}

// The credential and the model have to reach the wire, and the endpoint has to
// be the chat completions one.
func TestCompatibleProviderRequestEnvelope(t *testing.T) {
	var (
		gotPath string
		gotAuth string
		gotType string
		gotXtra string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotType = r.Header.Get("Content-Type")
		gotXtra = r.Header.Get("X-Trace")
		_, _ = io.WriteString(w, okReply)
	}))
	defer server.Close()

	provider, _ := NewOpenAICompatibleProvider("testvendor", ProviderConfig{
		APIKey:       "sk-secret",
		BaseURL:      server.URL + "/v1/",
		Timeout:      5 * time.Second,
		ExtraHeaders: map[string]string{"X-Trace": "abc"},
	})

	if _, err := provider.Complete(context.Background(), CompletionRequest{Model: "m", UserPrompt: "x"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %q, want /v1/chat/completions (a trailing slash on the base URL must not double up)", gotPath)
	}
	if gotAuth != "Bearer sk-secret" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotType != "application/json" {
		t.Errorf("Content-Type = %q", gotType)
	}
	if gotXtra != "abc" {
		t.Errorf("X-Trace = %q; ExtraHeaders did not reach the wire", gotXtra)
	}
}

// Sampling controls are sent only when set, so a provider's own default is not
// overwritten with a zero the caller never asked for.
func TestCompatibleProviderOmitsUnsetSamplingControls(t *testing.T) {
	body := captureRequest(t, CompletionRequest{Model: "m", UserPrompt: "x"}, okReply, http.StatusOK)

	if _, present := body["temperature"]; present {
		t.Error("an unset temperature was sent as zero, which pins the model to greedy decoding")
	}
	if _, present := body["max_tokens"]; present {
		t.Error("an unset max_tokens was sent")
	}

	body = captureRequest(t, CompletionRequest{
		Model: "m", UserPrompt: "x", Temperature: 0.4, MaxTokens: 256,
	}, okReply, http.StatusOK)

	if body["temperature"] != 0.4 {
		t.Errorf("temperature = %v, want 0.4", body["temperature"])
	}
	if body["max_tokens"] != float64(256) {
		t.Errorf("max_tokens = %v, want 256", body["max_tokens"])
	}
}

// Both prompts have to arrive, in the right roles.
func TestCompatibleProviderSendsBothPrompts(t *testing.T) {
	body := captureRequest(t, CompletionRequest{
		Model:        "m",
		SystemPrompt: "you are a classifier",
		UserPrompt:   "the ticket text",
	}, okReply, http.StatusOK)

	messages, ok := body["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("messages = %#v, want a system and a user message", body["messages"])
	}

	system := messages[0].(map[string]any)
	user := messages[1].(map[string]any)

	if system["role"] != "system" || system["content"] != "you are a classifier" {
		t.Errorf("system message = %#v", system)
	}
	if user["role"] != "user" || user["content"] != "the ticket text" {
		t.Errorf("user message = %#v", user)
	}
}

// A cancelled caller must not wait out the provider's timeout.
func TestCompatibleProviderHonoursCancellation(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer func() {
		close(release)
		server.Close()
	}()

	provider, _ := NewOpenAICompatibleProvider("testvendor", ProviderConfig{
		APIKey: "k", BaseURL: server.URL, Timeout: 30 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := provider.Complete(ctx, CompletionRequest{Model: "m", UserPrompt: "x"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a cancelled request must fail")
	}
	if elapsed > 5*time.Second {
		t.Errorf("cancellation took %v; the context is not reaching the transport", elapsed)
	}
}

// A provider with no base URL cannot be posted to, and saying so beats posting
// to a path that resolves to the process's own host.
func TestCompatibleProviderRequiresABaseURL(t *testing.T) {
	if _, err := NewOpenAICompatibleProvider("testvendor", ProviderConfig{APIKey: "k"}); err == nil {
		t.Fatal("a missing base URL must be reported at construction")
	}

	_, err := completeViaChatCompletions(context.Background(), http.DefaultClient, "testvendor",
		ProviderConfig{APIKey: "k"}, CompletionRequest{Model: "m", UserPrompt: "x"})
	if err == nil {
		t.Fatal("a missing base URL must be reported at send time too")
	}
	if !strings.Contains(err.Error(), "base URL") {
		t.Errorf("the error does not name the cause: %v", err)
	}
}

// Cerebras is reachable through the registry under its own name, with the
// documented endpoint, so `WithProvider("cerebras")` needs no base URL.
func TestCerebrasProviderDefaults(t *testing.T) {
	provider, err := NewCerebrasProvider(ProviderConfig{APIKey: "k", Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewCerebrasProvider: %v", err)
	}
	if provider.Name() != "cerebras" {
		t.Errorf("Name = %q", provider.Name())
	}
	if provider.config.BaseURL != "https://api.cerebras.ai/v1" {
		t.Errorf("BaseURL = %q, want the documented endpoint", provider.config.BaseURL)
	}

	if _, err := NewCerebrasProvider(ProviderConfig{}); err == nil {
		t.Error("a missing API key must be reported rather than producing a provider that 401s")
	}

	created, err := CreateProvider("cerebras", ProviderConfig{APIKey: "k", Timeout: time.Second})
	if err != nil {
		t.Fatalf("the registry does not know cerebras: %v", err)
	}
	if created.Name() != "cerebras" {
		t.Errorf("registry produced %q", created.Name())
	}
}
