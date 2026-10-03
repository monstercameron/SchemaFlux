package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// wireRecorder stands in for the network. It records every request body
// and answers with a canned success, so a test can use the provider's default
// endpoint -- OpenAI's own, which is where the endpoint-specific rules apply --
// without a request ever leaving the process.
type wireRecorder struct {
	bodies []map[string]any
	stream bool
}

func (rt *wireRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	raw, _ := io.ReadAll(req.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	rt.bodies = append(rt.bodies, body)

	payload := `{"model":"m","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"{}"}]}],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}`
	header := http.Header{"Content-Type": []string{"application/json"}}
	if rt.stream {
		payload = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + payload + "}\n\n"
		header = http.Header{"Content-Type": []string{"text/event-stream"}}
	}
	return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(bytes.NewBufferString(payload)), Request: req}, nil
}

// openAIProviderOn builds the real provider against a base URL, with the
// network replaced. An empty base URL is OpenAI's own endpoint.
func openAIProviderOn(t *testing.T, baseURL string) (*OpenAIProvider, *wireRecorder) {
	t.Helper()
	transport := &wireRecorder{}
	provider, err := NewOpenAIProvider(ProviderConfig{
		APIKey:     "k",
		BaseURL:    baseURL,
		Timeout:    5 * time.Second,
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}
	return provider, transport
}

// LX-001. On 2026-10-01 every operation returned HTTP 400 on gpt-6-luna:
// "Unsupported parameter: 'temperature' is not supported with this model."
// The gate exempted the gpt-5 prefix only and answered yes for everything
// else, so a new OpenAI family failed open.
//
// The first version of the fix decided by name alone, and review showed the
// name cannot carry it: a fine-tune, a gateway spelling or a deployment alias
// reaches OpenAI under a name with no gpt- prefix, while a local gpt-oss model
// has one. So a name the table knows gets its row anywhere, and an unknown name
// is decided by the endpoint.
func TestTemperatureIsNotSentToModelsThatRejectIt(t *testing.T) {
	const gateway = "http://127.0.0.1:9/v1"
	cases := []struct {
		model   string
		baseURL string
		sent    bool
	}{
		{"gpt-6-luna", "", false},
		{"gpt-6-sol", "", false},
		{"gpt-6-astra", "", false},
		{"gpt-6.1-sol", "", false},
		{"GPT-6-LUNA", "", false},
		{"  gpt-6-luna  ", "", false},
		{"gpt-5.6-luna", "", false},
		{"o3-mini", "", false},
		{"ft:gpt-6-luna:acme::abc", "", false},

		// Known to reject it, so not sent through a gateway either.
		{"gpt-6-luna", gateway, false},
		{"openai/gpt-6-luna", gateway, false},

		// Unknown on OpenAI's own endpoint: fail closed.
		{"gpt-7-nova", "", false},
		{"o5", "", false},
		{"codex-mini-latest", "", false},
		{"my-deployment", "", false},

		// Known to accept it.
		{"gpt-4o", "", true},
		{"gpt-4.1", "", true},
		{"gpt-3.5-turbo", "", true},
		{"ft:gpt-4o:acme::abc", "", true},

		// Unknown on someone else's server: sent, as it always was.
		{"gemma-4-31b", gateway, true},
		{"llama-3.3-70b", gateway, true},
		{"gpt-oss-120b", gateway, true},
	}
	for _, tc := range cases {
		t.Run(tc.model+"@"+tc.baseURL, func(t *testing.T) {
			provider, transport := openAIProviderOn(t, tc.baseURL)
			if _, err := provider.Complete(context.Background(), CompletionRequest{
				Model: tc.model, SystemPrompt: "s", UserPrompt: "u", Temperature: 0.3,
			}); err != nil {
				t.Fatalf("Complete: %v", err)
			}
			_, sent := transport.bodies[0]["temperature"]
			if sent != tc.sent {
				t.Fatalf("temperature sent = %v, want %v", sent, tc.sent)
			}
		})
	}
}

// An alias hides the model from the table: an Azure deployment or a proxy route
// for gpt-6-luna is an unknown name on a host that is not OpenAI's, so it would
// be sent the `temperature` it rejects. OmitTemperature is the caller's way to
// say so.
func TestOmitTemperatureLeavesItOffWhateverTheModel(t *testing.T) {
	const azure = "https://my-resource.openai.azure.com/openai/v1"
	for _, model := range []string{"my-gpt6-deployment", "gemma-4-31b", "gpt-4o", "gpt-6-luna"} {
		transport := &wireRecorder{}
		provider, err := NewOpenAIProvider(ProviderConfig{
			APIKey: "k", BaseURL: azure, Timeout: 5 * time.Second,
			HTTPClient: &http.Client{Transport: transport}, OmitTemperature: true,
		})
		if err != nil {
			t.Fatalf("NewOpenAIProvider: %v", err)
		}
		if _, err := provider.Complete(context.Background(), CompletionRequest{
			Model: model, SystemPrompt: "s", UserPrompt: "u", Temperature: 0.3,
		}); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if _, sent := transport.bodies[0]["temperature"]; sent {
			t.Errorf("%s: temperature was sent with OmitTemperature set", model)
		}
	}

	// Without the switch the alias is sent it, which is the case the switch
	// exists for.
	provider, transport := openAIProviderOn(t, azure)
	if _, err := provider.Complete(context.Background(), CompletionRequest{
		Model: "my-gpt6-deployment", SystemPrompt: "s", UserPrompt: "u", Temperature: 0.3,
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if _, sent := transport.bodies[0]["temperature"]; !sent {
		t.Error("an unknown name on another host should keep temperature by default")
	}
}

// LX-002. Extract returned HTTP 400 on the default model: "Invalid
// 'prompt_cache_key': string too long. Expected a string with maximum length
// 64." The key had no bound anywhere.
func TestBoundPromptCacheKey(t *testing.T) {
	long := "extract:v1:main.triage:v1:0123456789ab:json-schema-2020-12-strict:gpt-6-luna:deadbeefcafe:transform:json"
	cases := []struct {
		name     string
		key      string
		verbatim bool
	}{
		{"empty", "", true},
		{"short", "-:gpt-6-luna:abc123def456:strict:json", true},
		{"exactly 64", strings.Repeat("k", 64), true},
		{"65", strings.Repeat("k", 65), false},
		{"observed live, over 100 characters", long, false},
		{"the 88-character key the old test sent verbatim", "extract:v1:Person:v1:abc123:json-schema-2020-12-strict:gpt-5.6-luna:deadbeef:strict:json", false},
		{"very long", strings.Repeat("x", 4096), false},
		{"multi-byte under the limit in runes but over in bytes", strings.Repeat("é", 40), false},
		{"long pinned model name", "-:" + strings.Repeat("m", 70) + ":abc:strict:json", false},
		{"colons only", strings.Repeat(":", 100), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := boundPromptCacheKey(tc.key)
			if len(got) > maxPromptCacheKeyLength {
				t.Fatalf("bounded key is %d bytes, over the limit of %d", len(got), maxPromptCacheKeyLength)
			}
			if tc.verbatim && got != tc.key {
				t.Fatalf("a key within the limit must be sent unchanged, got %q", got)
			}
			if !tc.verbatim && got == tc.key {
				t.Fatalf("a key over the limit was sent unchanged")
			}
			if again := boundPromptCacheKey(tc.key); again != got {
				t.Fatalf("bounding is not stable: %q then %q", got, again)
			}
		})
	}

	// The key exists so that different prefixes get different keys. Two long
	// keys that differ only in their last segment must still differ once
	// bounded.
	if a, b := boundPromptCacheKey(long+":a"), boundPromptCacheKey(long+":b"); a == b {
		t.Fatalf("two different long keys collapsed to one bounded key")
	}
}

func TestOpenAIResponsesBoundsALongPromptCacheKeyOnTheWire(t *testing.T) {
	long := "extract:v1:main.triage:v1:0123456789ab:json-schema-2020-12-strict:gpt-6-luna:deadbeefcafe:transform:json"
	for _, stream := range []bool{false, true} {
		provider, transport := openAIProviderOn(t, "")
		transport.stream = stream
		req := CompletionRequest{Model: "gpt-6-luna", SystemPrompt: "s", UserPrompt: "u", PromptCacheKey: long}
		if stream {
			for range provider.CompleteStream(context.Background(), req) {
			}
		} else if _, err := provider.Complete(context.Background(), req); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if len(transport.bodies) != 1 {
			t.Fatalf("stream=%v: %d requests recorded, want 1", stream, len(transport.bodies))
		}
		got, _ := transport.bodies[0]["prompt_cache_key"].(string)
		if got == "" || len(got) > 64 {
			t.Fatalf("stream=%v: prompt_cache_key on the wire is %q (%d bytes), want 1 to 64 bytes", stream, got, len(got))
		}
	}
}

// LX-003 and LX-004. The rules were each seen as a 400 from the live API: an
// open object, a property missing from required, a name in required that is
// not a property, and a root that is not exactly an object.
func TestStrictSchemaProblem(t *testing.T) {
	closed := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
	}
	str := map[string]any{"type": "string"}
	var typedNil map[string]any

	cases := []struct {
		name   string
		schema map[string]any
		// want is a fragment of the expected message, or "" for a schema that
		// breaks none of the rules.
		want string
	}{
		{"closed, all required", closed(map[string]any{"a": str}, "a"), ""},
		{"required as []any", map[string]any{"type": "object", "properties": map[string]any{"a": str}, "required": []any{"a"}, "additionalProperties": false}, ""},
		{"array of closed objects inside an object", closed(map[string]any{"list": map[string]any{"type": "array", "items": closed(map[string]any{"a": str}, "a")}}, "list"), ""},
		{"nullable nested object", closed(map[string]any{"inner": map[string]any{"type": []string{"object", "null"}, "properties": map[string]any{"x": str}, "required": []string{"x"}, "additionalProperties": false}}, "inner"), ""},
		{"closed object with no properties", map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}, "additionalProperties": false}, ""},
		{"string format", closed(map[string]any{"t": map[string]any{"type": "string", "format": "date-time"}}, "t"), ""},

		{"the question_answer schema that failed live", map[string]any{"type": "object", "properties": map[string]any{"answer": map[string]any{"type": "boolean"}, "confidence": map[string]any{"type": "number"}}, "required": []string{"answer"}}, "$: an object must set additionalProperties to false"},
		{"a property is not required", closed(map[string]any{"a": str, "b": str}, "a"), `$: property "b" must be listed in required`},
		{"required names something that is not a property", closed(map[string]any{"a": str}, "a", "ghost"), `$: required names "ghost"`},
		{"additionalProperties true", map[string]any{"type": "object", "properties": map[string]any{"a": str}, "required": []string{"a"}, "additionalProperties": true}, "$: an object must set additionalProperties to false"},
		{"additionalProperties is a schema", map[string]any{"type": "object", "properties": map[string]any{"a": str}, "required": []string{"a"}, "additionalProperties": str}, "$: an object must set additionalProperties to false"},
		{"open object nested in a property", closed(map[string]any{"inner": map[string]any{"type": "object", "properties": map[string]any{"x": str}, "required": []string{"x"}}}, "inner"), "$.properties.inner: an object must set additionalProperties to false"},
		{"open object nested in array items", closed(map[string]any{"list": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"x": str}}}}, "list"), "$.properties.list.items: an object must set"},
		{"open object inside anyOf", closed(map[string]any{"u": map[string]any{"anyOf": []any{map[string]any{"type": "object", "properties": map[string]any{"x": str}}, map[string]any{"type": "null"}}}}, "u"), "$.properties.u.anyOf[0]: an object must set"},
		{"required missing entirely", map[string]any{"type": "object", "properties": map[string]any{"a": str}, "additionalProperties": false}, `$: property "a" must be listed in required`},

		// The root rule. Each of these was refused live with "schema must be a
		// JSON Schema of 'type: "object"'".
		{"array root", map[string]any{"type": "array", "items": str}, "$: the root must be exactly"},
		{"scalar root", str, "$: the root must be exactly"},
		{"union root", map[string]any{"anyOf": []any{closed(map[string]any{"a": str}, "a"), map[string]any{"type": "null"}}}, "$: the root must be exactly"},
		{"nullable object root", map[string]any{"type": []string{"object", "null"}, "properties": map[string]any{"a": str}, "required": []string{"a"}, "additionalProperties": false}, "$: the root must be exactly"},

		// What an inexpressible Go type turns into when nobody checks.
		{"a property with a nil schema", closed(map[string]any{"a": typedNil}, "a"), `$: property "a" has no schema`},
		{"a property with an empty schema", closed(map[string]any{"a": map[string]any{}}, "a"), `$: property "a" has no schema`},
		{"a union with a nil member", closed(map[string]any{"a": map[string]any{"anyOf": []any{typedNil, map[string]any{"type": "null"}}}}, "a"), "$.properties.a.anyOf[0]: a union member has no schema"},
		{"array items with no schema", closed(map[string]any{"a": map[string]any{"type": "array", "items": map[string]any{}}}, "a"), "$.properties.a: items has no schema"},
		{"empty schema", map[string]any{}, "$: the schema is empty"},
		{"nil schema", nil, "$: the schema is empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problem := StrictSchemaProblem(tc.schema)
			if tc.want == "" {
				if problem != "" {
					t.Fatalf("a schema that breaks no rule was refused: %s", problem)
				}
				return
			}
			if !strings.HasPrefix(problem, tc.want) {
				t.Fatalf("problem = %q, want it to start with %q", problem, tc.want)
			}
			if again := StrictSchemaProblem(tc.schema); again != problem {
				t.Fatalf("the message is not stable: %q then %q", problem, again)
			}
		})
	}
}

// A schema OpenAI would reject is refused before anything is sent, as an
// invalid-request error, on both the buffered and the streaming path. The
// refusal applies to OpenAI's own endpoint only: on any other server the same
// schema is sent as it always was, because that server's rules are not known.
func TestOpenAIRefusesANonStrictSchemaBeforeSending(t *testing.T) {
	open := map[string]any{
		"type":       "object",
		"properties": map[string]any{"answer": map[string]any{"type": "boolean"}, "confidence": map[string]any{"type": "number"}},
		"required":   []string{"answer"},
	}
	closed := map[string]any{"type": "object", "properties": map[string]any{"answer": map[string]any{"type": "boolean"}}, "required": []string{"answer"}, "additionalProperties": false}
	arrayRoot := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}

	cases := []struct {
		name    string
		baseURL string
		schema  map[string]any
		format  string
		stream  bool
		refused bool
	}{
		{"open schema, OpenAI", "", open, "json", false, true},
		{"open schema, OpenAI, streaming", "", open, "json", true, true},
		{"array root, OpenAI", "", arrayRoot, "json", false, true},
		{"array root, OpenAI, streaming", "", arrayRoot, "json", true, true},
		{"open schema, OpenAI by explicit URL", "https://api.openai.com/v1", open, "json", false, true},
		{"closed schema, OpenAI", "", closed, "json", false, false},
		{"closed schema, OpenAI, streaming", "", closed, "json", true, false},
		{"open schema, another server", "http://127.0.0.1:9/v1", open, "json", false, false},
		{"array root, another server", "http://127.0.0.1:9/v1", arrayRoot, "json", false, false},
		{"no schema at all", "", nil, "json", false, false},
		{"schema ignored when the format is not json", "", open, "", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider, transport := openAIProviderOn(t, tc.baseURL)
			transport.stream = tc.stream
			req := CompletionRequest{
				Model: "gpt-6-luna", SystemPrompt: "s", UserPrompt: "u",
				ResponseFormat: tc.format, SchemaName: "question_answer", JSONSchema: tc.schema,
			}

			var err error
			if tc.stream {
				for _, streamErr := range provider.CompleteStream(context.Background(), req) {
					if streamErr != nil {
						err = streamErr
					}
				}
			} else {
				_, err = provider.Complete(context.Background(), req)
			}

			if !tc.refused {
				if err != nil {
					t.Fatalf("the request was refused: %v", err)
				}
				if len(transport.bodies) != 1 {
					t.Fatalf("%d requests were sent, want 1", len(transport.bodies))
				}
				return
			}

			if err == nil {
				t.Fatal("a schema the live API rejects was sent")
			}
			if len(transport.bodies) != 0 {
				t.Fatalf("%d request(s) were sent; the refusal must happen first", len(transport.bodies))
			}
			if kind := Classify(err); kind != types.KindInvalidRequest {
				t.Fatalf("error kind = %v, want KindInvalidRequest so it is not retried", kind)
			}
			var opErr *types.OperationError
			if !errors.As(err, &opErr) {
				t.Fatalf("error is %T, want a classified *types.OperationError", err)
			}
			if opErr.Retryable() {
				t.Fatal("the refusal is marked retryable; the same schema would be refused again")
			}
			if !strings.Contains(err.Error(), "question_answer") {
				t.Fatalf("the error should name the schema, got %q", err.Error())
			}
		})
	}
}

// OmitTemperature is documented as every request, whatever the model. The
// Anthropic provider did not read it, so the switch did nothing there.
func TestOmitTemperatureIsHonouredByTheAnthropicProvider(t *testing.T) {
	for _, omit := range []bool{false, true} {
		server, captured := captureAnthropicRequest(t)
		provider, err := NewAnthropicProvider(ProviderConfig{APIKey: "test-key", BaseURL: server.URL, OmitTemperature: omit})
		if err != nil {
			t.Fatalf("NewAnthropicProvider: %v", err)
		}
		if _, err := provider.Complete(context.Background(), CompletionRequest{
			SystemPrompt: "be terse", UserPrompt: "hello", Temperature: 0.3,
		}); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if _, sent := (*captured)["temperature"]; sent == omit {
			t.Errorf("OmitTemperature=%v: temperature sent = %v", omit, sent)
		}
	}
}
