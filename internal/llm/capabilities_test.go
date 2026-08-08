package llm

import (
	"context"
	"encoding/json"
	"errors"
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

// CP-001. The capability registry and negotiation function this file adds:
// a route this codebase has never declared anything about must refuse a
// requirement, not silently pass one that names nothing, and a known route
// must be checked against exactly what it declares.

func TestCapabilitiesForUnknownRouteIsUnknown(t *testing.T) {
	ResetCapabilityRegistryForTest()
	t.Cleanup(ResetCapabilityRegistryForTest)

	caps, known := CapabilitiesFor("nobody-heard-of-this", "mystery-model")
	if known {
		t.Fatalf("known = true for a route nothing registered, caps = %+v", caps)
	}
}

func TestCapabilitiesForExactMatch(t *testing.T) {
	ResetCapabilityRegistryForTest()
	t.Cleanup(ResetCapabilityRegistryForTest)

	RegisterCapabilities(ProviderCapabilities{
		Provider: "openai", Model: "gpt-4o",
		Supports: map[Capability]bool{CapNativeJSONSchema: true, CapStreaming: true},
	})

	caps, known := CapabilitiesFor("openai", "gpt-4o")
	if !known {
		t.Fatal("known = false for a registered exact route")
	}
	if !caps.Has(CapNativeJSONSchema) || !caps.Has(CapStreaming) {
		t.Fatalf("caps = %+v, want native json schema and streaming", caps)
	}
	if caps.Has(CapToolCalling) {
		t.Fatal("caps.Has(CapToolCalling) = true, was never registered")
	}
}

func TestCapabilitiesForFamilyPrefixMatch(t *testing.T) {
	ResetCapabilityRegistryForTest()
	t.Cleanup(ResetCapabilityRegistryForTest)

	RegisterCapabilityFamily("openai", "gpt-5.6-", ProviderCapabilities{
		Supports: map[Capability]bool{CapNativeJSONSchema: true},
	})

	for _, model := range []string{"gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra"} {
		caps, known := CapabilitiesFor("openai", model)
		if !known {
			t.Fatalf("known = false for family member %q", model)
		}
		if !caps.Has(CapNativeJSONSchema) {
			t.Fatalf("model %q did not inherit the family capability", model)
		}
		if caps.Model != model {
			t.Fatalf("caps.Model = %q, want %q (family lookup must stamp the actual model back on)", caps.Model, model)
		}
	}

	if _, known := CapabilitiesFor("openai", "gpt-4o"); known {
		t.Fatal("gpt-4o matched the gpt-5.6- family prefix, it should not have")
	}
}

func TestCapabilitiesForLongestFamilyPrefixWins(t *testing.T) {
	ResetCapabilityRegistryForTest()
	t.Cleanup(ResetCapabilityRegistryForTest)

	RegisterCapabilityFamily("openai", "gpt-5", ProviderCapabilities{
		Supports: map[Capability]bool{CapToolCalling: true},
	})
	RegisterCapabilityFamily("openai", "gpt-5.6-", ProviderCapabilities{
		Supports: map[Capability]bool{CapNativeJSONSchema: true},
	})

	caps, known := CapabilitiesFor("openai", "gpt-5.6-luna")
	if !known {
		t.Fatal("known = false")
	}
	if caps.Has(CapToolCalling) {
		t.Fatal("the shorter, less specific family prefix won instead of the longer one")
	}
	if !caps.Has(CapNativeJSONSchema) {
		t.Fatal("the longer, more specific family prefix did not win")
	}
}

func TestNegotiateRefusesUnknownRoute(t *testing.T) {
	caps := ProviderCapabilities{}
	err := Negotiate(caps, false, Requirement{Capabilities: []Capability{CapNativeJSONSchema}})
	if err == nil {
		t.Fatal("Negotiate returned nil for an unknown route")
	}
	if !errors.Is(err, ErrCapabilityUnknown) {
		t.Fatalf("err = %v, want it to wrap ErrCapabilityUnknown", err)
	}
}

func TestNegotiateRefusesMissingCapability(t *testing.T) {
	caps := ProviderCapabilities{
		Provider: "p", Model: "m",
		Supports: map[Capability]bool{CapJSONMode: true},
	}
	err := Negotiate(caps, true, Requirement{Capabilities: []Capability{CapNativeJSONSchema}})
	if err == nil {
		t.Fatal("Negotiate returned nil for a route lacking the required capability")
	}
	if !errors.Is(err, ErrCapabilityUnmet) {
		t.Fatalf("err = %v, want it to wrap ErrCapabilityUnmet", err)
	}
}

func TestNegotiateAcceptsSatisfiedRequirement(t *testing.T) {
	caps := ProviderCapabilities{
		Provider: "p", Model: "m",
		Supports:              map[Capability]bool{CapNativeJSONSchema: true, CapStreaming: true},
		UsageReportingQuality: UsageExact,
		ContextWindow:         128000,
		MaxOutputTokens:       4096,
	}
	req := Requirement{
		Capabilities:       []Capability{CapNativeJSONSchema, CapStreaming},
		MinUsageQuality:    UsageEstimated,
		MinContextWindow:   32000,
		MinMaxOutputTokens: 1024,
	}
	if err := Negotiate(caps, true, req); err != nil {
		t.Fatalf("Negotiate refused a fully-satisfied requirement: %v", err)
	}
	if !Meets(caps, req) {
		t.Fatal("Meets = false for the same requirement Negotiate accepted")
	}
}

func TestNegotiateRefusesBelowUsageQualityFloor(t *testing.T) {
	caps := ProviderCapabilities{
		Provider: "p", Model: "m",
		UsageReportingQuality: UsageEstimated,
	}
	err := Negotiate(caps, true, Requirement{MinUsageQuality: UsageExact})
	if err == nil {
		t.Fatal("Negotiate accepted estimated usage against a required exact floor")
	}
}

// UsageUnknown (the zero value) never satisfies a declared minimum -- a
// route that never said anything about its usage reporting is not the same
// as one that was measured and found estimated.
func TestUnknownUsageQualityNeverMeetsADeclaredFloor(t *testing.T) {
	caps := ProviderCapabilities{Provider: "p", Model: "m"}
	if err := Negotiate(caps, true, Requirement{MinUsageQuality: UsageEstimated}); err == nil {
		t.Fatal("an undeclared usage quality satisfied a declared minimum")
	}
}

func TestNegotiateRefusesUnmeasuredContextWindow(t *testing.T) {
	caps := ProviderCapabilities{Provider: "p", Model: "m", Supports: map[Capability]bool{}}
	err := Negotiate(caps, true, Requirement{MinContextWindow: 8000})
	if err == nil {
		t.Fatal("Negotiate accepted a requirement against an unmeasured (zero) context window")
	}
}

func TestNegotiateRefusesInsufficientContextWindow(t *testing.T) {
	caps := ProviderCapabilities{Provider: "p", Model: "m", ContextWindow: 4000}
	if err := Negotiate(caps, true, Requirement{MinContextWindow: 8000}); err == nil {
		t.Fatal("Negotiate accepted a context window below the requirement")
	}
}

func TestNegotiateRefusesMissingSchemaKeyword(t *testing.T) {
	caps := ProviderCapabilities{
		Provider: "p", Model: "m",
		Supports:       map[Capability]bool{CapNativeJSONSchema: true},
		SchemaKeywords: []string{"required", "type"},
	}
	req := Requirement{
		Capabilities:   []Capability{CapNativeJSONSchema},
		SchemaKeywords: []string{"required", "additionalProperties"},
	}
	err := Negotiate(caps, true, req)
	if err == nil {
		t.Fatal("Negotiate accepted a route silently missing a required schema keyword")
	}
	if !strings.Contains(err.Error(), "additionalProperties") {
		t.Fatalf("error does not name the missing keyword: %v", err)
	}
}

// A zero Requirement names nothing, so it is satisfied by any KNOWN route --
// but still refuses an unknown one, because "no route was ever checked" is
// not the same fact as "this route meets an empty bar."
func TestZeroRequirementStillRequiresAKnownRoute(t *testing.T) {
	if err := Negotiate(ProviderCapabilities{}, false, Requirement{}); err == nil {
		t.Fatal("a zero Requirement was satisfied against an unknown route")
	}
	caps := ProviderCapabilities{Provider: "p", Model: "m"}
	if err := Negotiate(caps, true, Requirement{}); err != nil {
		t.Fatalf("a zero Requirement was refused against a known route: %v", err)
	}
}

func TestHasDeclaredDistinguishesUndeclaredFromExplicitFalse(t *testing.T) {
	caps := ProviderCapabilities{
		Provider: "p", Model: "m",
		Supports: map[Capability]bool{CapToolCalling: false},
	}
	if !caps.HasDeclared(CapToolCalling) {
		t.Fatal("HasDeclared = false for an explicitly declared (false) capability")
	}
	if caps.HasDeclared(CapSeed) {
		t.Fatal("HasDeclared = true for a capability never mentioned")
	}
	if caps.Has(CapToolCalling) {
		t.Fatal("Has = true for a capability explicitly declared false")
	}
}
