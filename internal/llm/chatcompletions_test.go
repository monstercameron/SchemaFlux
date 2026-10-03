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
	"unicode/utf8"

	"github.com/monstercameron/schemaflux/internal/types"
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

// The schema the caller passed is never shared with the one that is sent, in the
// positions where a value that is a list stands.
func TestSanitisedSchemaSharesNoListWithTheCallers(t *testing.T) {
	in := map[string]any{
		"type":  "array",
		"items": []any{map[string]any{"type": "string", "format": "email"}},
	}
	out := chatDialectSchema(in)
	items, ok := out["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items = %#v", out["items"])
	}
	items[0] = "changed"
	if _, still := in["items"].([]any)[0].(map[string]any); !still {
		t.Error("editing the sanitised schema changed the caller's")
	}
}

// Stripping removes keywords, never a property whose name happens to be one. A
// field called "format" or "default" was deleted from `properties` while
// `required` still named it, so the vendor was sent a strict schema that
// required a property it did not declare.
func TestChatDialectSchemaKeepsPropertiesNamedLikeKeywords(t *testing.T) {
	for banned := range unsupportedInChatDialect {
		t.Run(banned, func(t *testing.T) {
			schema := map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{banned, "other"},
				"properties": map[string]any{
					banned: map[string]any{"type": "string", "format": "email", "minLength": float64(3)},
					"other": map[string]any{"type": "object", "additionalProperties": false,
						"required":   []any{banned},
						"properties": map[string]any{banned: map[string]any{"type": "number", "minimum": float64(0)}}},
				},
			}
			got := chatDialectSchema(schema)
			properties, _ := got["properties"].(map[string]any)
			if _, ok := properties[banned]; !ok {
				t.Fatalf("the property %q was dropped; required still names it: %#v", banned, got)
			}
			required, _ := got["required"].([]any)
			if len(required) != 2 {
				t.Fatalf("required = %#v, want it kept whole", required)
			}
			for _, name := range required {
				if _, ok := properties[name.(string)]; !ok {
					t.Errorf("required names %q, which is not a property of the sent schema", name)
				}
			}
			inner := properties[banned].(map[string]any)
			if _, stripped := inner["format"]; stripped {
				t.Errorf("the keyword format survived inside the property schema: %#v", inner)
			}
			nested := properties["other"].(map[string]any)["properties"].(map[string]any)
			if _, ok := nested[banned]; !ok {
				t.Errorf("the nested property %q was dropped: %#v", banned, nested)
			}
		})
	}
}

// Keywords are stripped wherever a schema appears, and only there. Names under
// $defs, and the values of enum and const, are data of the schema and not
// schemas.
func TestChatDialectSchemaStripsKeywordsOnlyAtSchemaPositions(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"$defs": map[string]any{
			"format": map[string]any{"type": "string", "pattern": "^x$"},
		},
		"properties": map[string]any{
			"kind":  map[string]any{"type": "string", "enum": []any{"format", "default", "pattern"}},
			"fixed": map[string]any{"const": map[string]any{"format": "csv", "maxItems": float64(3)}},
			"either": map[string]any{"anyOf": []any{
				map[string]any{"type": "string", "format": "date"},
				map[string]any{"type": "array", "items": map[string]any{"type": "string", "maxLength": float64(4)}, "maxItems": float64(2)},
			}},
			"list": map[string]any{"type": "array", "items": map[string]any{"type": "object",
				"properties": map[string]any{"when": map[string]any{"type": "string", "format": "date-time"}}}},
		},
	}
	got := chatDialectSchema(schema)

	defs := got["$defs"].(map[string]any)
	if _, ok := defs["format"]; !ok {
		t.Error("a definition named format was dropped")
	}
	if _, stripped := defs["format"].(map[string]any)["pattern"]; stripped {
		t.Error("pattern survived inside a definition")
	}

	properties := got["properties"].(map[string]any)
	if enum := properties["kind"].(map[string]any)["enum"].([]any); len(enum) != 3 {
		t.Errorf("enum values were altered: %#v", enum)
	}
	if constant := properties["fixed"].(map[string]any)["const"].(map[string]any); len(constant) != 2 {
		t.Errorf("a const value was altered: %#v", constant)
	}
	members := properties["either"].(map[string]any)["anyOf"].([]any)
	if _, stripped := members[0].(map[string]any)["format"]; stripped {
		t.Error("format survived inside an anyOf member")
	}
	array := members[1].(map[string]any)
	if _, stripped := array["maxItems"]; stripped {
		t.Error("maxItems survived inside an anyOf member")
	}
	if _, stripped := array["items"].(map[string]any)["maxLength"]; stripped {
		t.Error("maxLength survived inside items")
	}
	when := properties["list"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)["when"].(map[string]any)
	if _, stripped := when["format"]; stripped {
		t.Error("format survived two levels down")
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
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !strings.Contains(apiErr.Message, "nesting depth") || !strings.Contains(apiErr.Detail(), "nesting depth") {
		t.Errorf("the vendor's reason was swallowed: %v", err)
	}
	if strings.Contains(err.Error(), "nesting depth") {
		t.Errorf("the vendor's reason is in the error's text: %v", err)
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
	} else {
		var apiErr *APIError
		if !errors.As(err, &apiErr) || !strings.Contains(apiErr.Message, "rate limit") || apiErr.Type != "too_many_requests" {
			t.Errorf("the reason was lost: %v", err)
		}
		if strings.Contains(err.Error(), "rate limit exceeded") {
			t.Errorf("the vendor's message is in the error's text: %v", err)
		}
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
		{"empty_content", `{"model":"m","choices":[{"finish_reason":"stop","message":{"content":""}}]}`},
		{"whitespace_content", `{"model":"m","choices":[{"finish_reason":"stop","message":{"content":"   \n"}}]}`},
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

// A finish reason that is missing, or that this library does not know, is not
// read as a normal end. "" was read as "stop", so a body with no reason at all
// was returned as a complete answer; "tool_calls" for a request that offered no
// tools is an answer to something else.
func TestCompatibleProviderRefusesAnUnknownFinishReason(t *testing.T) {
	for _, given := range []string{"", "tool_calls", "function_call", "STOP", "banana", "error", "null"} {
		t.Run("finish_"+given, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, `{"model":"m","choices":[{"finish_reason":"`+given+`","message":{"content":"a plausible answer"}}]}`)
			}))
			defer server.Close()

			provider, _ := NewOpenAICompatibleProvider("testvendor", ProviderConfig{
				APIKey: "k", BaseURL: server.URL, Timeout: 5 * time.Second,
			})
			resp, err := provider.Complete(context.Background(), CompletionRequest{Model: "m", UserPrompt: "x"})
			if err == nil {
				t.Fatalf("an answer with finish reason %q was returned: %+v", given, resp)
			}
			if !isEmptyResponse(resp) {
				t.Errorf("a result came back beside the error: %+v", resp)
			}
			if got := Classify(err); got != types.KindMalformedOutput {
				t.Errorf("Classify = %v, want %v", got, types.KindMalformedOutput)
			}
			if given == "banana" && strings.Contains(err.Error(), given) {
				t.Errorf("the vendor's string is in the error: %v", err)
			}
		})
	}
}

// An answer stopped before any text is not an empty answer: it says why, and the
// same request would be stopped the same way.
func TestCompatibleProviderClassifiesAnAnswerStoppedBeforeAnyText(t *testing.T) {
	cases := []struct {
		reason string
		kind   types.ErrorKind
	}{
		{"length", types.KindOutputTruncated},
		{"content_filter", types.KindPolicyViolation},
	}
	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, `{"model":"m","choices":[{"finish_reason":"`+tc.reason+`","message":{"content":""}}]}`)
			}))
			defer server.Close()
			provider, _ := NewOpenAICompatibleProvider("testvendor", ProviderConfig{
				APIKey: "k", BaseURL: server.URL, Timeout: 5 * time.Second,
			})
			_, err := provider.Complete(context.Background(), CompletionRequest{Model: "m", UserPrompt: "x"})
			if got := Classify(err); got != tc.kind {
				t.Errorf("Classify = %v, want %v (%v)", got, tc.kind, err)
			}
			if retryable := (&types.OperationError{Kind: Classify(err)}).Retryable(); retryable {
				t.Error("an answer that is stopped the same way each time must not be retried")
			}
		})
	}
}

// A 200 carrying an error object is the same failure as one that arrived as a
// status, and is typed as one. It was an untyped error with the vendor's whole
// message in it, which Classify read as unknown and the retry loop resent.
func TestCompatibleProviderTypesAnErrorObjectInA200(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		kind      types.ErrorKind
		status    int
		retryable bool
	}{
		{"numeric 429 code", `{"error":{"message":"rate limit hit","code":429}}`, types.KindRateLimited, 429, true},
		{"numeric 503 code", `{"error":{"message":"upstream is down","code":503}}`, types.KindProviderUnavailable, 503, true},
		{"numeric 400 code", `{"error":{"message":"bad input","code":400}}`, types.KindInvalidRequest, 400, false},
		{"numeric 401 code", `{"error":{"message":"bad key","code":401}}`, types.KindAuthentication, 401, false},
		{"string rate limit code", `{"error":{"message":"slow","code":"rate_limit_exceeded"}}`, types.KindRateLimited, 429, true},
		{"unrecognised code", `{"error":{"message":"odd","code":"something_else"}}`, types.KindProviderUnavailable, 502, true},
		{"no code", `{"error":{"message":"odd"}}`, types.KindProviderUnavailable, 502, true},
		{"budget in the text", `{"error":{"message":"max_tokens must exceed thinking.budget_tokens","code":400}}`, types.KindInvalidRequest, 400, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "4")
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			provider, _ := NewOpenAICompatibleProvider("testvendor", ProviderConfig{
				APIKey: "k", BaseURL: server.URL, Timeout: 5 * time.Second,
			})
			resp, err := provider.Complete(context.Background(), CompletionRequest{Model: "m", UserPrompt: "x"})
			if err == nil || !isEmptyResponse(resp) {
				t.Fatalf("got %+v, %v; want an empty result and an error", resp, err)
			}
			if got := Classify(err); got != tc.kind {
				t.Errorf("Classify = %v, want %v (%v)", got, tc.kind, err)
			}
			if status, ok := StatusCodeFrom(err); !ok || status != tc.status {
				t.Errorf("StatusCodeFrom = %d, %v; want %d", status, ok, tc.status)
			}
			if retryable := (&types.OperationError{Kind: Classify(err)}).Retryable(); retryable != tc.retryable {
				t.Errorf("retryable = %v, want %v", retryable, tc.retryable)
			}
		})
	}
}

// A throttle the chat dialect reports inside a 200 keeps the wait the server
// asked for, as one it reports with a status does.
func TestCompatibleProviderKeepsTheRetryAfterOfAThrottleInA200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "4")
		_, _ = io.WriteString(w, `{"error":{"message":"slow down","code":429}}`)
	}))
	defer server.Close()
	provider, err := NewOpenAICompatibleProvider("testvendor", ProviderConfig{APIKey: "k", BaseURL: server.URL, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Complete(context.Background(), CompletionRequest{Model: "m", UserPrompt: "x"})
	if err == nil {
		t.Fatal("a throttle in a 200 produced a result")
	}
	wait, isRateLimit := RetryAfterFrom(err)
	if !isRateLimit || wait != 4*time.Second {
		t.Errorf("RetryAfterFrom = %v, %v; want 4s, true", wait, isRateLimit)
	}
}

// The buffered answer of the chat dialect is read under the same ceiling as the
// Responses one: a body past it is refused, not decoded.
func TestCompatibleProviderBodyIsBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"`))
		chunk := []byte(strings.Repeat("a", 1<<20))
		for written := 0; written <= maxResponseBodyBytes; written += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
		_, _ = w.Write([]byte(`"}}]}`))
	}))
	defer server.Close()
	provider, err := NewOpenAICompatibleProvider("testvendor", ProviderConfig{APIKey: "k", BaseURL: server.URL, Timeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := provider.Complete(context.Background(), CompletionRequest{Model: "m", UserPrompt: "x"})
	if err == nil {
		t.Fatalf("a body past the ceiling was accepted: %d bytes of content", len(resp.Content))
	}
	if got := Classify(err); got != types.KindMalformedOutput {
		t.Errorf("Classify = %v, want %v (%v)", got, types.KindMalformedOutput, err)
	}
}

func TestCompatibleProviderBoundsTheMessageOfAnErrorObjectInA200(t *testing.T) {
	long := strings.Repeat("é", maxMessageLength*3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"error":{"message":"`+long+`","code":500}}`)
	}))
	defer server.Close()
	provider, _ := NewOpenAICompatibleProvider("testvendor", ProviderConfig{
		APIKey: "k", BaseURL: server.URL, Timeout: 5 * time.Second,
	})
	_, err := provider.Complete(context.Background(), CompletionRequest{Model: "m", UserPrompt: "x"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Count(err.Error(), "é") > maxMessageLength || !utf8.ValidString(err.Error()) {
		t.Errorf("the vendor's message was not bounded on a rune boundary: %d runes", strings.Count(err.Error(), "é"))
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

// chatBodyOf sends one request to a chat-dialect server answering reply, and
// returns the raw body the provider wrote, what it returned, and how many
// requests the server saw.
func chatBodyOf(t *testing.T, req CompletionRequest, reply string) (body string, resp CompletionResponse, err error, requests int) {
	t.Helper()
	var seen int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		seen++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, reply)
	}))
	defer server.Close()
	provider, perr := NewOpenAICompatibleProvider("testvendor", ProviderConfig{APIKey: "k", BaseURL: server.URL, Timeout: 10 * time.Second})
	if perr != nil {
		t.Fatal(perr)
	}
	resp, err = provider.Complete(context.Background(), req)
	return body, resp, err, seen
}

// A request that mentions no tool and no message is written to the wire exactly
// as it was before the chat dialect carried either.
func TestChatDialectRequestWithNoToolsAndNoMessagesIsWrittenAsItAlwaysWas(t *testing.T) {
	schema := map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"a": map[string]any{"type": "string"}},
		"required":             []any{"a"},
		"additionalProperties": false,
	}
	cases := []struct {
		name string
		req  CompletionRequest
		want string
	}{
		{"user prompt only", CompletionRequest{Model: "m", UserPrompt: "u"}, `{"model":"m","messages":[{"role":"system","content":""},{"role":"user","content":"u"}]}`},
		{"system and user", CompletionRequest{Model: "m", SystemPrompt: "s", UserPrompt: "u"}, `{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"u"}]}`},
		{"json object", CompletionRequest{Model: "m", SystemPrompt: "s", UserPrompt: "u", ResponseFormat: "json"}, `{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"u"}],"response_format":{"type":"json_object"}}`},
		{"a strict schema", CompletionRequest{Model: "m", SystemPrompt: "s", UserPrompt: "u", ResponseFormat: "json", JSONSchema: schema, SchemaName: "answer"}, `{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"u"}],"response_format":{"type":"json_schema","json_schema":{"name":"answer","strict":true,"schema":{"additionalProperties":false,"properties":{"a":{"type":"string"}},"required":["a"],"type":"object"}}}}`},
		{"temperature and a ceiling", CompletionRequest{Model: "m", UserPrompt: "u", Temperature: 0.4, MaxTokens: 256}, `{"model":"m","messages":[{"role":"system","content":""},{"role":"user","content":"u"}],"temperature":0.4,"max_tokens":256}`},
		{"an empty tools list and an empty transcript", CompletionRequest{Model: "m", UserPrompt: "u", Tools: []Tool{}, Messages: []Message{}}, `{"model":"m","messages":[{"role":"system","content":""},{"role":"user","content":"u"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _, err, _ := chatBodyOf(t, tc.req, okReply)
			if err != nil {
				t.Fatal(err)
			}
			if body != tc.want {
				t.Errorf("body\n got %s\nwant %s", body, tc.want)
			}
		})
	}
}

// messagesOnTheWire is the "messages" a chat request carried, as JSON with the
// keys sorted.
func messagesOnTheWire(t *testing.T, req CompletionRequest) (string, error) {
	t.Helper()
	body, _, err, _ := chatBodyOf(t, req, okReply)
	if err != nil {
		return "", err
	}
	var decoded struct {
		Messages json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("body %q: %v", body, err)
	}
	var generic any
	_ = json.Unmarshal(decoded.Messages, &generic)
	sorted, _ := json.Marshal(generic)
	return string(sorted), nil
}

// The chat dialect takes a tool's answer as a message with the role "tool" and
// the id of the call it answers, after the system prompt and the turns before it.
func TestChatDialectWritesTheTranscriptAsMessages(t *testing.T) {
	cases := []struct {
		name     string
		system   string
		messages []Message
		want     string
	}{
		{"a result", "s", []Message{{Role: "user", Content: "hi"}, {Role: "tool", Content: "7C", ToolCallID: "call_1"}},
			`[{"content":"s","role":"system"},{"content":"hi","role":"user"},{"content":"7C","role":"tool","tool_call_id":"call_1"}]`},
		{"two results in order", "s", []Message{{Role: "tool", Content: "a", ToolCallID: "call_2"}, {Role: "tool", Content: "b", ToolCallID: "call_1"}},
			`[{"content":"s","role":"system"},{"content":"a","role":"tool","tool_call_id":"call_2"},{"content":"b","role":"tool","tool_call_id":"call_1"}]`},
		{"an empty result is still content", "s", []Message{{Role: "tool", Content: "", ToolCallID: "call_1"}},
			`[{"content":"s","role":"system"},{"content":"","role":"tool","tool_call_id":"call_1"}]`},
		{"plain turns", "s", []Message{{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"}, {Role: "user", Content: "c"}},
			`[{"content":"s","role":"system"},{"content":"a","role":"user"},{"content":"b","role":"assistant"},{"content":"c","role":"user"}]`},
		{"a system turn in the transcript", "s", []Message{{Role: "system", Content: "more"}, {Role: "user", Content: "a"}},
			`[{"content":"s","role":"system"},{"content":"more","role":"system"},{"content":"a","role":"user"}]`},
		{"no system prompt keeps its empty system message", "", []Message{{Role: "user", Content: "a"}},
			`[{"content":"","role":"system"},{"content":"a","role":"user"}]`},
		{"a developer turn", "s", []Message{{Role: "developer", Content: "d"}, {Role: "user", Content: "a"}},
			`[{"content":"s","role":"system"},{"content":"d","role":"developer"},{"content":"a","role":"user"}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := messagesOnTheWire(t, CompletionRequest{Model: "m", SystemPrompt: tc.system, Messages: tc.messages})
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("messages\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

// toolsOnTheWire is the "tools" and "tool_choice" a chat request carried.
func toolsOnTheWire(t *testing.T, req CompletionRequest) (tools, choice string, err error) {
	t.Helper()
	body, _, err, _ := chatBodyOf(t, req, okReply)
	if err != nil {
		return "", "", err
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("body %q: %v", body, err)
	}
	encodedTools, _ := json.Marshal(decoded["tools"])
	encodedChoice, _ := json.Marshal(decoded["tool_choice"])
	return string(encodedTools), string(encodedChoice), nil
}

// A tool is declared as the chat dialect defines it: a function with its name,
// its description and its parameters, flagged strict when the schema is one
// strict mode takes. The keywords the compatible vendors reject are taken out of
// the schema, as they are from a response schema.
func TestChatDialectDeclaresTools(t *testing.T) {
	closed := map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"city": map[string]any{"type": "string"}},
		"required":             []any{"city"},
		"additionalProperties": false,
	}
	cases := []struct {
		name  string
		tools []Tool
		want  string
	}{
		{"a closed schema", []Tool{{Name: "get_weather", Description: "d", Parameters: closed}},
			`[{"function":{"description":"d","name":"get_weather","parameters":{"additionalProperties":false,"properties":{"city":{"type":"string"}},"required":["city"],"type":"object"},"strict":true},"type":"function"}]`},
		{"no parameters", []Tool{{Name: "ping"}},
			`[{"function":{"name":"ping","parameters":{"additionalProperties":false,"properties":{},"required":[],"type":"object"},"strict":true},"type":"function"}]`},
		{"an optional property is not strict", []Tool{{Name: "t", Description: "d", Parameters: map[string]any{
			"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}, "required": []any{}}}},
			`[{"function":{"description":"d","name":"t","parameters":{"properties":{"a":{"type":"string"}},"required":[],"type":"object"}},"type":"function"}]`},
		{"keywords the vendors reject are taken out", []Tool{{Name: "t", Parameters: map[string]any{
			"type": "object", "required": []any{"a"}, "additionalProperties": false,
			"properties": map[string]any{"a": map[string]any{"type": "string", "format": "email", "minLength": 3}}}}},
			`[{"function":{"name":"t","parameters":{"additionalProperties":false,"properties":{"a":{"type":"string"}},"required":["a"],"type":"object"},"strict":true},"type":"function"}]`},
		{"two tools in the order offered", []Tool{{Name: "b"}, {Name: "a"}},
			`[{"function":{"name":"b","parameters":{"additionalProperties":false,"properties":{},"required":[],"type":"object"},"strict":true},"type":"function"},{"function":{"name":"a","parameters":{"additionalProperties":false,"properties":{},"required":[],"type":"object"},"strict":true},"type":"function"}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tools, choice, err := toolsOnTheWire(t, CompletionRequest{Model: "m", UserPrompt: "u", Tools: tc.tools})
			if err != nil {
				t.Fatal(err)
			}
			if tools != tc.want {
				t.Errorf("tools\n got %s\nwant %s", tools, tc.want)
			}
			if choice != "null" {
				t.Errorf("tool_choice = %s, want none sent", choice)
			}
		})
	}

	t.Run("the callers schema is not edited", func(t *testing.T) {
		schema := map[string]any{"type": "object", "required": []any{"a"}, "additionalProperties": false,
			"properties": map[string]any{"a": map[string]any{"type": "string", "format": "email"}}}
		if _, _, err := toolsOnTheWire(t, CompletionRequest{Model: "m", UserPrompt: "u", Tools: []Tool{{Name: "t", Parameters: schema}}}); err != nil {
			t.Fatal(err)
		}
		if schema["properties"].(map[string]any)["a"].(map[string]any)["format"] != "email" {
			t.Error("the keyword was taken out of the caller's own schema")
		}
	})
}

func TestChatDialectSendsTheToolChoice(t *testing.T) {
	tools := []Tool{{Name: "get_weather"}, {Name: "get_time"}}
	cases := []struct {
		name   string
		choice string
		want   string
	}{
		{"none sent", "", "null"},
		{"auto", "auto", `"auto"`},
		{"none", "none", `"none"`},
		{"required", "required", `"required"`},
		{"a named tool", "get_time", `{"function":{"name":"get_time"},"type":"function"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, choice, err := toolsOnTheWire(t, CompletionRequest{Model: "m", UserPrompt: "u", Tools: tools, ToolChoice: tc.choice})
			if err != nil {
				t.Fatal(err)
			}
			if choice != tc.want {
				t.Errorf("tool_choice = %s, want %s", choice, tc.want)
			}
		})
	}
}

// What cannot be written is refused before anything is sent.
func TestChatDialectRefusesWhatItCannotWriteBeforeSending(t *testing.T) {
	const secret = "CALLER-SECRET-7731"
	tool := Tool{Name: "get_weather"}
	cases := []struct {
		name string
		req  CompletionRequest
		want types.ErrorKind
	}{
		{"web search", CompletionRequest{Model: "m", UserPrompt: "u", WebSearch: true}, types.KindUnsupportedCapability},
		{"a tool choice that names no tool", CompletionRequest{Model: "m", UserPrompt: "u", Tools: []Tool{tool}, ToolChoice: "get_wether"}, types.KindInvalidRequest},
		{"a tool choice with no tools", CompletionRequest{Model: "m", UserPrompt: "u", ToolChoice: "get_weather"}, types.KindInvalidRequest},
		{"a tool with no name", CompletionRequest{Model: "m", UserPrompt: "u", Tools: []Tool{{Description: secret}}}, types.KindInvalidRequest},
		{"two tools of one name", CompletionRequest{Model: "m", UserPrompt: "u", Tools: []Tool{tool, tool}}, types.KindInvalidRequest},
		{"a role nobody defined", CompletionRequest{Model: "m", Messages: []Message{{Role: "narrator", Content: secret}}}, types.KindInvalidRequest},
		{"no role", CompletionRequest{Model: "m", Messages: []Message{{Content: secret}}}, types.KindInvalidRequest},
		{"a tool result with no call id", CompletionRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}, {Role: "tool", Content: secret}}}, types.KindInvalidRequest},
		{"a function role", CompletionRequest{Model: "m", Messages: []Message{{Role: "function", Content: secret, ToolCallID: "c1"}}}, types.KindInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, resp, err, requests := chatBodyOf(t, tc.req, okReply)
			if err == nil {
				t.Fatalf("answered: %+v", resp)
			}
			if kind := Classify(err); kind != tc.want {
				t.Errorf("kind = %v, want %v (%v)", kind, tc.want, err)
			}
			if requests != 0 {
				t.Error("the request was sent")
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("the error carries the caller's text: %v", err)
			}
			if !isEmptyResponse(resp) {
				t.Error("a result came back beside the error")
			}
		})
	}
}

// chatReply is a chat completion with the given message and finish reason.
func chatReply(t *testing.T, finish string, content any, calls ...map[string]any) string {
	t.Helper()
	message := map[string]any{"role": "assistant", "content": content}
	if len(calls) > 0 {
		list := make([]any, len(calls))
		for i, call := range calls {
			list[i] = call
		}
		message["tool_calls"] = list
	}
	encoded, err := json.Marshal(map[string]any{
		"model":   "m",
		"choices": []any{map[string]any{"finish_reason": finish, "message": message}},
		"usage":   map[string]any{"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func chatCall(id, name, arguments string) map[string]any {
	call := map[string]any{"type": "function", "function": map[string]any{"name": name, "arguments": arguments}}
	if id != "" {
		call["id"] = id
	}
	return call
}

// A reply that is only tool calls is an answer: Content empty, the calls in
// order with their ids, names and argument text, and the finish reason as the
// vendor gave it. A reply with calls that cannot be used is refused as a
// malformed output, naming nothing the model wrote.
func TestChatDialectReplyToolCallsAreReadOrRefused(t *testing.T) {
	const marker = "MODEL-WROTE-THIS-8854"
	offered := []Tool{{Name: "get_weather"}, {Name: "get_time"}}

	cases := []struct {
		name       string
		reply      string
		tools      []Tool
		wantCalls  []ToolCall
		wantText   string
		wantFinish string
		wantKind   types.ErrorKind
		wantIs     error
	}{
		{name: "only one call", tools: offered, wantFinish: "tool_calls",
			reply:     chatReply(t, "tool_calls", nil, chatCall("call_1", "get_weather", `{"city":"Oslo"}`)),
			wantCalls: []ToolCall{{ID: "call_1", Name: "get_weather", Arguments: json.RawMessage(`{"city":"Oslo"}`)}}},
		{name: "an empty string of text beside the calls", tools: offered, wantFinish: "tool_calls",
			reply:     chatReply(t, "tool_calls", "", chatCall("call_1", "get_time", `{}`)),
			wantCalls: []ToolCall{{ID: "call_1", Name: "get_time", Arguments: json.RawMessage(`{}`)}}},
		{name: "two calls in the order the model made them", tools: offered, wantFinish: "tool_calls",
			reply: chatReply(t, "tool_calls", nil, chatCall("call_b", "get_time", `{}`), chatCall("call_a", "get_weather", `{"x":1}`)),
			wantCalls: []ToolCall{
				{ID: "call_b", Name: "get_time", Arguments: json.RawMessage(`{}`)},
				{ID: "call_a", Name: "get_weather", Arguments: json.RawMessage(`{"x":1}`)}}},
		{name: "text beside a call", tools: offered, wantFinish: "tool_calls", wantText: "One moment.",
			reply:     chatReply(t, "tool_calls", "One moment.", chatCall("call_1", "get_time", `{}`)),
			wantCalls: []ToolCall{{ID: "call_1", Name: "get_time", Arguments: json.RawMessage(`{}`)}}},
		{name: "calls under a finish reason of stop", tools: offered, wantFinish: "stop",
			reply:     chatReply(t, "stop", nil, chatCall("call_1", "get_time", `{}`)),
			wantCalls: []ToolCall{{ID: "call_1", Name: "get_time", Arguments: json.RawMessage(`{}`)}}},
		{name: "arguments written as an object", tools: offered, wantFinish: "tool_calls",
			reply:     strings.Replace(chatReply(t, "tool_calls", nil, chatCall("call_1", "get_time", "OBJECT")), `"OBJECT"`, `{"a":1}`, 1),
			wantCalls: []ToolCall{{ID: "call_1", Name: "get_time", Arguments: json.RawMessage(`{"a":1}`)}}},

		{name: "no id", tools: offered, wantKind: types.KindMalformedOutput, reply: chatReply(t, "tool_calls", nil, chatCall("", "get_time", `{}`))},
		{name: "no name", tools: offered, wantKind: types.KindMalformedOutput, wantIs: ErrUnrequestedTool, reply: chatReply(t, "tool_calls", nil, chatCall("c1", "", `{}`))},
		{name: "an id used twice", tools: offered, wantKind: types.KindMalformedOutput,
			reply: chatReply(t, "tool_calls", nil, chatCall("c1", "get_time", `{}`), chatCall("c1", "get_weather", `{}`))},
		{name: "arguments that are an array", tools: offered, wantKind: types.KindMalformedOutput, reply: chatReply(t, "tool_calls", nil, chatCall("c1", "get_time", `[1]`))},
		{name: "arguments that are null", tools: offered, wantKind: types.KindMalformedOutput, reply: chatReply(t, "tool_calls", nil, chatCall("c1", "get_time", `null`))},
		{name: "arguments that are empty", tools: offered, wantKind: types.KindMalformedOutput, reply: chatReply(t, "tool_calls", nil, chatCall("c1", "get_time", ``))},
		{name: "arguments cut short", tools: offered, wantKind: types.KindMalformedOutput, reply: chatReply(t, "tool_calls", nil, chatCall("c1", "get_time", `{"a":"`+marker))},
		{name: "arguments with text after the object", tools: offered, wantKind: types.KindMalformedOutput, reply: chatReply(t, "tool_calls", nil, chatCall("c1", "get_time", `{} `+marker))},
		{name: "a tool nobody offered", tools: offered, wantKind: types.KindMalformedOutput, wantIs: ErrUnrequestedTool,
			reply: chatReply(t, "tool_calls", nil, chatCall("c1", marker, `{}`))},
		{name: "a call when no tools were offered", tools: nil, wantKind: types.KindMalformedOutput, wantIs: ErrUnrequestedTool,
			reply: chatReply(t, "tool_calls", nil, chatCall("c1", marker, `{}`))},
		{name: "calls cut off at the ceiling", tools: offered, wantKind: types.KindOutputTruncated,
			reply: chatReply(t, "length", nil, chatCall("c1", "get_time", `{"a":`))},
		{name: "calls withheld by a content filter", tools: offered, wantKind: types.KindPolicyViolation,
			reply: chatReply(t, "content_filter", nil, chatCall("c1", "get_time", `{}`))},
		{name: "a finish reason of tool_calls and no calls", tools: offered, wantKind: types.KindMalformedOutput,
			reply: chatReply(t, "tool_calls", "text")},
		{name: "a finish reason of tool_calls when no tools were offered", tools: nil, wantKind: types.KindMalformedOutput,
			reply: chatReply(t, "tool_calls", "text")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, resp, err, _ := chatBodyOf(t, CompletionRequest{Model: "m", UserPrompt: "u", Tools: tc.tools}, tc.reply)
			if tc.wantKind != types.KindUnknown {
				if err == nil {
					t.Fatalf("answered %+v, want a refusal", resp)
				}
				if kind := Classify(err); kind != tc.wantKind {
					t.Errorf("kind = %v, want %v (%v)", kind, tc.wantKind, err)
				}
				if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
					t.Errorf("%v does not wrap %v", err, tc.wantIs)
				}
				if strings.Contains(err.Error(), marker) {
					t.Errorf("the error repeats what the model wrote: %v", err)
				}
				if !isEmptyResponse(resp) {
					t.Error("a result came back beside the error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(resp.ToolCalls) != len(tc.wantCalls) {
				t.Fatalf("%d calls, want %d", len(resp.ToolCalls), len(tc.wantCalls))
			}
			for i, want := range tc.wantCalls {
				call := resp.ToolCalls[i]
				if call.ID != want.ID || call.Name != want.Name || string(call.Arguments) != string(want.Arguments) {
					t.Errorf("call %d = %+v, want %+v", i, call, want)
				}
			}
			if resp.Content != tc.wantText {
				t.Errorf("Content = %q, want %q", resp.Content, tc.wantText)
			}
			if resp.FinishReason != tc.wantFinish {
				t.Errorf("FinishReason = %q, want %q", resp.FinishReason, tc.wantFinish)
			}
		})
	}
}
