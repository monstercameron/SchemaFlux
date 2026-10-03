// Package wirecontract checks, without a network call, that the request body
// each operation sends is one the live OpenAI Responses API accepts.
//
// LX-008. On 2026-10-01 three request shapes turned out to be rejected with
// HTTP 400 by the live API: `temperature` on a model that does not take it, a
// `prompt_cache_key` over 64 characters, and a response schema sent as strict
// that was not strict-valid. Nothing in the gate could have caught any of
// them: the unit tests asserted on hand-written requests, two of them pinned
// the rejected shape as correct, and the only thing that sent real operation
// bodies anywhere was a command that needs a paid key.
//
// This runs every case the smoke command runs through the exported API and the
// real OpenAI provider, pointed at a local stand-in, and applies the three
// constraints to every body that arrives.
package wirecontract

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/config/configtest"
	"github.com/monstercameron/schemaflux/internal/smokecases"
)

// recorder stands in for the network. It is installed as the provider's HTTP
// transport, so the provider is configured exactly as it is in production --
// the default base URL, which is OpenAI's own endpoint and the one the
// endpoint-specific rules apply to -- and no request leaves the process.
//
// It answers every request with an empty JSON object, which most operations go
// on to reject as malformed. That is fine: the test is about what was sent, not
// about what came back.
type recorder struct {
	mu     sync.Mutex
	bodies []map[string]any
}

func (r *recorder) take() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.bodies
	r.bodies = nil
	return out
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	raw, _ := io.ReadAll(req.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	r.mu.Lock()
	r.bodies = append(r.bodies, body)
	r.mu.Unlock()

	const answer = `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"{}"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(answer)),
		Request:    req,
	}, nil
}

func startRecorder(t *testing.T) *recorder {
	t.Helper()
	// A base URL left in the environment would point the provider somewhere
	// else and change which rules apply.
	t.Setenv("SCHEMAFLUX_OPENAI_BASE_URL", "")
	t.Setenv("OPENAI_BASE_URL", "")

	rec := &recorder{}
	schemaflux.NewClient("test-key").WithProviderConfig("openai", schemaflux.ProviderConfig{
		APIKey:     "test-key",
		HTTPClient: &http.Client{Transport: rec},
	})
	return rec
}

// extraCases are operations and option combinations the smoke list does not
// reach but that build their own request shape.
func extraCases() []smokecases.Case {
	type answer struct {
		Eligible bool   `json:"eligible"`
		Reason   string `json:"reason"`
	}
	return []smokecases.Case{
		{Name: "Deduplicate", Run: func() error {
			_, err := schemaflux.Deduplicate([]string{"The app crashed.", "The app crashed during checkout.", "Invoices are monthly."}, 0.8)
			return err
		}},
		{Name: "Is", Run: func() error {
			_, err := schemaflux.Is(context.Background(), "Please refund my order.", "Is the customer asking for a refund?")
			return err
		}},
		{Name: "IsResult", Run: func() error {
			_, err := schemaflux.IsResult(context.Background(), "Please refund my order.", "Is the customer asking for a refund?")
			return err
		}},
		{Name: "Which", Run: func() error {
			_, err := schemaflux.Which(context.Background(), "Please refund my order.", "Which team should handle this?", []string{"billing", "technical", "shipping"})
			return err
		}},
		{Name: "WhichOrNone", Run: func() error {
			_, _, err := schemaflux.WhichOrNone(context.Background(), "Please refund my order.", "Which open ticket is this about?", []string{"Late parcel", "Double charge"})
			return err
		}},
		{Name: "Rate", Run: func() error {
			_, err := schemaflux.Rate(context.Background(), "Please refund my order.", "How urgent is this?", 1, 5)
			return err
		}},
		{Name: "Assess", Run: func() error {
			type triage struct {
				Refund  bool   `ask:"Is the customer asking for a refund?"`
				Team    string `ask:"Which team should handle this?" oneof:"billing,technical,shipping"`
				Urgency int    `ask:"How urgent is this?" scale:"1-5"`
			}
			_, err := schemaflux.Assess[triage](context.Background(), "Please refund my order.")
			return err
		}},
		{Name: "AssessResult", Run: func() error {
			type triage struct {
				Refund bool `ask:"Is the customer asking for a refund?"`
			}
			_, err := schemaflux.AssessResult[triage](context.Background(), "Please refund my order.")
			return err
		}},
		{Name: "Match", Run: func() error {
			_, err := schemaflux.Match(context.Background(), "Please refund my order.",
				schemaflux.When("the customer asks for a refund", func() {}),
				schemaflux.Otherwise(func() {}))
			return err
		}},
		{Name: "Question, every optional field", Run: func() error {
			opts := schemaflux.NewQuestionOptions("Is this signup eligible?")
			opts.IncludeConfidence, opts.IncludeReasoning, opts.IncludeEvidence = true, true, true
			_, err := schemaflux.Question[map[string]any, answer](map[string]any{"age": 24}, opts)
			return err
		}},
		{Name: "Question, string answer", Run: func() error {
			_, err := schemaflux.Question[string, string]("The invoice total is $40.", schemaflux.NewQuestionOptions("What is the total?"))
			return err
		}},
		{Name: "Predict, struct prediction", Run: func() error {
			type forecast struct {
				Revenue float64 `json:"revenue"`
				Units   int     `json:"units"`
			}
			opts := schemaflux.NewPredictOptions()
			opts.Horizon = "next quarter"
			_, err := schemaflux.Predict[forecast]([]map[string]any{{"q": "Q1", "revenue": 10.0}, {"q": "Q2", "revenue": 12.0}}, opts)
			return err
		}},
		{Name: "Compress, string", Run: func() error {
			_, err := schemaflux.Compress("A long note about the launch timeline and its owners.", schemaflux.NewCompressOptions())
			return err
		}},
		{Name: "Decompose, string content", Run: func() error {
			_, err := schemaflux.Decompose("Design the page, integrate billing, migrate customers.", schemaflux.NewDecomposeOptions())
			return err
		}},
		{Name: "Extract, long type name", Run: func() error {
			type anExtractionTargetWithAnUnusuallyLongTypeNameForTheCacheKey struct {
				Name string `json:"name"`
			}
			_, err := schemaflux.Extract[anExtractionTargetWithAnUnusuallyLongTypeNameForTheCacheKey]("Jordan Lee is a PM.", schemaflux.NewExtractOptions())
			return err
		}},
	}
}

// modelsUnderTest are the model names the contract is checked under: the
// library default, the family that failed live, and an OpenAI-named family the
// library has never heard of. want is the model name every request must carry,
// so an override that silently failed to apply cannot leave the test checking
// the default four times.
var modelsUnderTest = []struct{ label, model, want string }{
	{"default model", "", "gpt-6-luna"},
	{"gpt-6-luna", "gpt-6-luna", "gpt-6-luna"},
	{"gpt-6.1-sol", "gpt-6.1-sol", "gpt-6.1-sol"},
	{"an unknown gpt family", "gpt-9-example", "gpt-9-example"},
}

// sendsNothing names the cases that are allowed to make no request, because the
// operation decides the answer in Go. Every other case must reach the provider
// at least once: a case that quietly sends nothing is a case this test is not
// checking, and before this list existed up to half of them could do that.
var sendsNothing = map[string]bool{}

func TestEveryOperationSendsABodyTheLiveAPIAccepts(t *testing.T) {
	cases := append(smokecases.All(), extraCases()...)

	for _, m := range modelsUnderTest {
		t.Run(m.label, func(t *testing.T) {
			configtest.Setenv(t, "SCHEMAFLUX_MODEL", m.model)
			// A tier override left in the environment would beat the default
			// this subtest means to check.
			for _, tier := range []string{"SCHEMAFLUX_MODEL_SMART", "SCHEMAFLUX_MODEL_FAST", "SCHEMAFLUX_MODEL_QUICK"} {
				configtest.Setenv(t, tier, "")
			}
			// One attempt and no repair round is enough to see the first body,
			// and keeps a run of seventy cases to a few seconds. It also means
			// only the first request of an operation that makes several is
			// seen here; that is a stated limit of this test.
			configtest.Setenv(t, "SCHEMAFLUX_REPAIR_ATTEMPTS", "0")
			rec := startRecorder(t)

			for _, tc := range cases {
				rec.take()
				// Most cases fail on the stand-in's empty answer, which is
				// expected. The error matters only when nothing was sent: then
				// it is the reason, such as a schema refused before the
				// network.
				runErr := tc.Run()
				bodies := rec.take()

				if len(bodies) == 0 {
					if !sendsNothing[tc.Name] {
						t.Errorf("%s: no request reached the provider, so nothing about it was checked (the call returned: %v)", tc.Name, runErr)
					}
					continue
				}
				if sendsNothing[tc.Name] {
					t.Errorf("%s is listed as sending nothing but made %d request(s); remove it from sendsNothing", tc.Name, len(bodies))
				}

				for i, body := range bodies {
					if got := body["model"]; got != m.want {
						t.Errorf("%s, request %d: model = %v, want %q", tc.Name, i+1, got, m.want)
					}
					for _, problem := range bodyProblems(body) {
						t.Errorf("%s, request %d: %s", tc.Name, i+1, problem)
					}
				}
			}
		})
	}
}

// bodyProblems applies the constraints learned live to one request body.
//
// The schema rules are restated here on purpose and do not call the library's
// own validator. The provider uses that validator to decide what to refuse, so
// a rule it does not know would pass both it and a test built on it.
func bodyProblems(body map[string]any) []string {
	var problems []string

	if _, has := body["temperature"]; has {
		problems = append(problems, fmt.Sprintf("sends temperature to %v, which rejects it", body["model"]))
	}

	if key, ok := body["prompt_cache_key"].(string); ok && len(key) > 64 {
		problems = append(problems, fmt.Sprintf("prompt_cache_key is %d bytes; the API limit is 64", len(key)))
	}

	// A reasoning effort is not negotiated either: the API fails the request
	// on a word it does not know. These are the words it named when it
	// rejected one, on 2026-10-01. None of these cases sets an effort of its
	// own, so any that is on the wire is an operation's preference, and a
	// preference is sent only to the model it was measured on. A caller's own
	// value would be passed to an unknown model as given (LX-105).
	if reasoning, ok := body["reasoning"].(map[string]any); ok {
		effort, _ := reasoning["effort"].(string)
		model, _ := body["model"].(string)
		accepted := map[string]bool{"none": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true}
		switch {
		case !strings.HasPrefix(model, "gpt-6"):
			problems = append(problems, fmt.Sprintf("sends reasoning effort %q to %v, whose accepted efforts are not known", effort, model))
		case !accepted[effort]:
			problems = append(problems, fmt.Sprintf("sends reasoning effort %q, which %v rejects", effort, model))
		}
	}

	if text, ok := body["text"].(map[string]any); ok {
		if format, ok := text["format"].(map[string]any); ok && format["type"] == "json_schema" {
			schema, _ := format["schema"].(map[string]any)
			if schema["type"] != "object" {
				problems = append(problems, fmt.Sprintf("schema %v has root type %v; the API requires an object", format["name"], schema["type"]))
			}
			for _, problem := range openObjects(schema, "$") {
				problems = append(problems, fmt.Sprintf("schema %v is sent as strict: %s", format["name"], problem))
			}
		}
	}
	return problems
}

// openObjects walks a decoded JSON schema and reports every object that is not
// closed, every property missing from required, every required name that is not
// a property, and every place a schema should be and is not.
func openObjects(node any, path string) []string {
	var problems []string
	schema, ok := node.(map[string]any)
	if !ok || len(schema) == 0 {
		return []string{path + " is not a schema"}
	}

	isObject := schema["type"] == "object"
	if list, ok := schema["type"].([]any); ok {
		for _, name := range list {
			if name == "object" {
				isObject = true
			}
		}
	}
	properties, hasProperties := schema["properties"].(map[string]any)
	if isObject || hasProperties {
		if schema["additionalProperties"] != false {
			problems = append(problems, path+" does not set additionalProperties to false")
		}
		required := map[string]bool{}
		list, _ := schema["required"].([]any)
		for _, name := range list {
			required[fmt.Sprint(name)] = true
		}
		for name := range properties {
			if !required[name] {
				problems = append(problems, path+" does not list "+name+" in required")
			}
		}
		for name := range required {
			if _, declared := properties[name]; !declared {
				problems = append(problems, path+" requires "+name+", which is not a property")
			}
		}
	}

	for name, child := range properties {
		problems = append(problems, openObjects(child, path+"."+name)...)
	}
	if items, ok := schema["items"]; ok {
		problems = append(problems, openObjects(items, path+"[]")...)
	}
	for _, keyword := range []string{"anyOf", "oneOf", "allOf"} {
		if members, ok := schema[keyword].([]any); ok {
			for i, member := range members {
				problems = append(problems, openObjects(member, fmt.Sprintf("%s.%s[%d]", path, keyword, i))...)
			}
		}
	}
	return problems
}

// The helper is only as good as its own judgement of a body, so it is checked
// against bodies whose verdict is known.
func TestBodyProblems(t *testing.T) {
	strict := func(schema map[string]any) map[string]any {
		return map[string]any{"format": map[string]any{"type": "json_schema", "name": "x", "strict": true, "schema": schema}}
	}
	closed := map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}, "required": []any{"a"}, "additionalProperties": false}
	open := map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}, "required": []any{"a"}}

	array := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	ghost := map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}, "required": []any{"a", "ghost"}, "additionalProperties": false}
	nullProperty := map[string]any{"type": "object", "properties": map[string]any{"a": nil}, "required": []any{"a"}, "additionalProperties": false}
	nested := map[string]any{"type": "object", "properties": map[string]any{"inner": open}, "required": []any{"inner"}, "additionalProperties": false}

	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"clean", map[string]any{"model": "gpt-6-luna"}, 0},
		{"temperature", map[string]any{"model": "gpt-6-luna", "temperature": 0.3}, 1},
		{"temperature zero is still the key", map[string]any{"temperature": 0.0}, 1},
		{"key at the limit", map[string]any{"prompt_cache_key": strings.Repeat("k", 64)}, 0},
		{"key over the limit", map[string]any{"prompt_cache_key": strings.Repeat("k", 65)}, 1},
		{"an effort gpt-6 takes", map[string]any{"model": "gpt-6-luna", "reasoning": map[string]any{"effort": "none"}}, 0},
		{"another effort gpt-6 takes", map[string]any{"model": "gpt-6.1-sol", "reasoning": map[string]any{"effort": "xhigh"}}, 0},
		{"an effort gpt-6 rejects", map[string]any{"model": "gpt-6-luna", "reasoning": map[string]any{"effort": "minimal"}}, 1},
		{"an empty effort", map[string]any{"model": "gpt-6-luna", "reasoning": map[string]any{}}, 1},
		{"an effort sent to a model nothing is known about", map[string]any{"model": "a-new-model", "reasoning": map[string]any{"effort": "low"}}, 1},
		{"strict-valid schema", map[string]any{"text": strict(closed)}, 0},
		{"open schema", map[string]any{"text": strict(open)}, 1},
		{"open schema nested in a property", map[string]any{"text": strict(nested)}, 1},
		{"array root", map[string]any{"text": strict(array)}, 1},
		{"required names a property that does not exist", map[string]any{"text": strict(ghost)}, 1},
		{"a property with no schema", map[string]any{"text": strict(nullProperty)}, 1},
		{"json_object carries no schema", map[string]any{"text": map[string]any{"format": map[string]any{"type": "json_object"}}}, 0},
		{"all three at once", map[string]any{"temperature": 0.1, "prompt_cache_key": strings.Repeat("k", 100), "text": strict(open)}, 3},
		{"text block without a format", map[string]any{"text": map[string]any{"verbosity": "low"}}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := bodyProblems(tc.body); len(got) != tc.want {
				t.Fatalf("found %d problem(s) %v, want %d", len(got), got, tc.want)
			}
		})
	}
}
