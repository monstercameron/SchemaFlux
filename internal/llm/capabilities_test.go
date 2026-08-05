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

// P-011. These pin what the live API was measured to accept, per model ID. The
// stakes are higher than a normal default: these parameters are not negotiated,
// so an unaccepted one fails the WHOLE request with a 400. A wrong guess here
// is not a degraded call, it is no call at all.
//
// Evidence: .audit/live/capabilities.py and .audit/live/bench3.py.

func TestCapabilityFlagsPerModelID(t *testing.T) {
	cases := []struct {
		model       string
		temperature bool
		reasoning   bool
		effort      string
	}{
		// Measured: the whole 5.6 family rejects temperature, including zero,
		// and `effort: none` takes luna and sol from 4/4 correct to 0/4. The
		// effort value is still named so that enabling the block later
		// produces a valid request rather than a 400.
		{"gpt-5.6-luna", false, false, "low"},
		{"gpt-5.6-sol", false, false, "low"},
		{"gpt-5.6-terra", false, false, "low"},
		{"GPT-5.6-LUNA", false, false, "low"},
		{"  gpt-5.6-terra  ", false, false, "low"},

		{"gpt-5.4", false, true, "none"},
		{"gpt-5", false, true, "minimal"},
		{"gpt-5-mini", false, true, "minimal"},
		{"gpt-5-nano", false, true, "minimal"},

		{"gpt-4o", true, false, ""},
		{"gpt-4", true, false, ""},
		{"gemma-4-31b", true, false, ""},
		{"", true, false, ""},
	}

	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			if got := supportsTemperature(tc.model); got != tc.temperature {
				t.Errorf("supportsTemperature = %v, want %v", got, tc.temperature)
			}
			if got := supportsReasoningControls(tc.model); got != tc.reasoning {
				t.Errorf("supportsReasoningControls = %v, want %v", got, tc.reasoning)
			}
			if got := reasoningEffort(tc.model); got != tc.effort {
				t.Errorf("reasoningEffort = %q, want %q", got, tc.effort)
			}
		})
	}
}

// The landmine this closes: reasoningEffort returned "minimal" for anything
// that was not gpt-5.4, and the 5.6 family rejects "minimal" outright. The two
// functions were one flag away from failing every request, with
// supportsReasoningControls returning false as the only thing hiding it.
func TestReasoningEffortIsNeverAValueTheModelRejects(t *testing.T) {
	// The values the live API named as accepted for the 5.6 family.
	acceptedBy56 := map[string]bool{
		"none": true, "low": true, "medium": true,
		"high": true, "xhigh": true, "max": true,
	}

	for _, model := range []string{"gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra"} {
		effort := reasoningEffort(model)
		if effort == "" {
			continue // omitted entirely, which is always valid
		}
		if !acceptedBy56[effort] {
			t.Errorf("reasoningEffort(%q) = %q, which the model rejects -- "+
				"enabling the block would 400 every request", model, effort)
		}
	}
}

// An unknown family must omit the block rather than guess at an enum.
func TestUnknownModelsOmitTheReasoningBlock(t *testing.T) {
	for _, model := range []string{"gpt-6", "claude-3-5-sonnet", "gemma-4-31b", "mystery"} {
		if effort := reasoningEffort(model); effort != "" {
			t.Errorf("reasoningEffort(%q) = %q; an unknown family must omit the block, "+
				"because a wrong enum fails the whole request", model, effort)
		}
	}
}

// captureResponsesRequest runs one OpenAI Responses call against a stand-in and
// returns the body that was sent.
func captureResponsesRequest(t *testing.T, config ProviderConfig, req CompletionRequest) map[string]any {
	t.Helper()

	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &captured)
		_, _ = io.WriteString(w, `{"model":"gpt-5.6-luna","status":"completed",
			"output":[{"type":"message","content":[{"type":"output_text","text":"{}"}]}],
			"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}`)
	}))
	defer server.Close()

	config.BaseURL = server.URL
	if config.APIKey == "" {
		config.APIKey = "k"
	}
	if config.Timeout == 0 {
		config.Timeout = 5 * time.Second
	}

	provider, err := NewOpenAIProvider(config)
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}
	if _, err := provider.Complete(context.Background(), req); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if captured == nil {
		t.Fatal("no request body captured")
	}
	return captured
}

// The 5.6 family rejects temperature, so it must not reach the wire even when
// the caller sets one.
func TestTemperatureIsWithheldFromModelsThatRejectIt(t *testing.T) {
	body := captureResponsesRequest(t, ProviderConfig{}, CompletionRequest{
		Model: "gpt-5.6-luna", UserPrompt: "x", Temperature: 0.7,
	})
	if _, present := body["temperature"]; present {
		t.Error("temperature reached a model that rejects it; the request would 400")
	}

	body = captureResponsesRequest(t, ProviderConfig{}, CompletionRequest{
		Model: "gpt-4o", UserPrompt: "x", Temperature: 0.7,
	})
	if body["temperature"] != 0.7 {
		t.Errorf("temperature = %v, want 0.7 for a model that accepts it", body["temperature"])
	}
}

// And the reasoning block must not reach the 5.6 family at all, because
// `effort: none` was measured to destroy accuracy on luna and sol.
func TestReasoningBlockIsWithheldFromThe56Family(t *testing.T) {
	body := captureResponsesRequest(t, ProviderConfig{}, CompletionRequest{
		Model: "gpt-5.6-luna", UserPrompt: "x",
	})
	if _, present := body["reasoning"]; present {
		t.Errorf("a reasoning block reached gpt-5.6-luna: %#v", body["reasoning"])
	}
}

// P-008. The Responses API retains responses server-side unless told
// otherwise. For a library whose job is running arbitrary user records through
// a model, that is a surprising thing to leave on: the caller opted into an
// extraction, not into retention.
func TestStoreIsFalseByDefault(t *testing.T) {
	body := captureResponsesRequest(t, ProviderConfig{}, CompletionRequest{
		Model: "gpt-5.6-luna", UserPrompt: "an invoice with a customer name on it",
	})

	store, present := body["store"]
	if !present {
		t.Fatal("no store field was sent, so the server default (retain) applies")
	}
	if store != false {
		t.Errorf("store = %v, want false", store)
	}
}

// A caller who wants retention can have it, explicitly.
func TestStoreCanBeTurnedOn(t *testing.T) {
	body := captureResponsesRequest(t, ProviderConfig{Store: true}, CompletionRequest{
		Model: "gpt-5.6-luna", UserPrompt: "x",
	})
	if body["store"] != true {
		t.Errorf("store = %v, want true when the caller asked for it", body["store"])
	}
}

// The whole envelope, so a change to one field cannot silently drop another.
func TestResponsesRequestEnvelope(t *testing.T) {
	body := captureResponsesRequest(t, ProviderConfig{}, CompletionRequest{
		Model:          "gpt-5.6-luna",
		SystemPrompt:   "you extract invoices",
		UserPrompt:     "Invoice from Acme",
		MaxTokens:      512,
		ResponseFormat: "json",
		JSONSchema: map[string]any{
			"type": "object", "additionalProperties": false,
			"required":   []any{"vendor"},
			"properties": map[string]any{"vendor": map[string]any{"type": "string"}},
		},
		SchemaName: "invoice",
	})

	if body["model"] != "gpt-5.6-luna" {
		t.Errorf("model = %v", body["model"])
	}
	if body["instructions"] != "you extract invoices" {
		t.Errorf("instructions = %v", body["instructions"])
	}
	if body["max_output_tokens"] != float64(512) {
		t.Errorf("max_output_tokens = %v", body["max_output_tokens"])
	}

	text, ok := body["text"].(map[string]any)
	if !ok {
		t.Fatalf("no text config: %#v", body)
	}
	format, ok := text["format"].(map[string]any)
	if !ok {
		t.Fatalf("no format: %#v", text)
	}
	if format["type"] != "json_schema" || format["strict"] != true || format["name"] != "invoice" {
		t.Errorf("format = %#v", format)
	}
}

// json_object is rejected unless the input mentions JSON -- measured, not
// assumed: "Response input messages must contain the word 'json' in some form".
// A schema-less JSON request must therefore carry the word.
func TestSchemalessJSONRequestsMentionJSON(t *testing.T) {
	body := captureResponsesRequest(t, ProviderConfig{}, CompletionRequest{
		Model:          "gpt-5.6-luna",
		UserPrompt:     "Extract the vendor from this invoice.",
		ResponseFormat: "json",
	})

	input, _ := body["input"].(string)
	if !strings.Contains(strings.ToLower(input), "json") {
		t.Errorf("a json_object request went out without the word json in the input, "+
			"which the API rejects: %q", input)
	}

	// A prompt that already says it must not be padded twice.
	body = captureResponsesRequest(t, ProviderConfig{}, CompletionRequest{
		Model:          "gpt-5.6-luna",
		UserPrompt:     "Return JSON describing the vendor.",
		ResponseFormat: "json",
	})
	input, _ = body["input"].(string)
	if input != "Return JSON describing the vendor." {
		t.Errorf("a prompt that already mentions JSON was modified: %q", input)
	}
}
