package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/pricing"
)

// responsesAPI stands up a fake OpenAI Responses endpoint and points the
// default client at it. This is the integration seam for the provider fixes:
// public API -> ops -> OpenAI provider -> HTTP -> parser.
func responsesAPI(t *testing.T, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			t.Errorf("expected the Responses API, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	schemaflux.NewClient("test-key").WithProviderConfig("openai", schemaflux.ProviderConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
	})
	return server
}

// message builds a Responses body carrying a reasoning item before the message
// item, which is the shape a reasoning model returns.
func message(answer string) string {
	payload, _ := json.Marshal(answer)
	return `{
	  "id":"resp","status":"completed","model":"gpt-5.6-luna",
	  "output":[
	    {"type":"reasoning","content":[{"type":"reasoning_text","text":"Let me work through this step by step."}]},
	    {"type":"message","content":[{"type":"output_text","text":` + string(payload) + `}]}
	  ],
	  "usage":{"input_tokens":50,"output_tokens":20,"total_tokens":70,
	           "input_tokens_details":{"cached_tokens":32},
	           "output_tokens_details":{"reasoning_tokens":12}}
	}`
}

type person struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

// The end-to-end proof of P-001: a reasoning model's output must reach Extract
// as parseable JSON. Before the fix the reasoning text was concatenated onto
// the payload and every extraction failed.
func TestIntegrationExtractSurvivesReasoningItems(t *testing.T) {
	responsesAPI(t, message(`{"name":"Ada Lovelace","age":36}`))

	result, err := schemaflux.Extract[person]("Ada Lovelace is 36.", schemaflux.NewExtractOptions())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if result.Name != "Ada Lovelace" || result.Age != 36 {
		t.Fatalf("got %+v", result)
	}
}

// The same path through the fluent builder.
func TestIntegrationFluentExtractSurvivesReasoningItems(t *testing.T) {
	responsesAPI(t, message(`{"name":"Grace Hopper","age":45}`))

	result, err := schemaflux.Extracting[person]("Grace Hopper is 45.").Run()
	if err != nil {
		t.Fatalf("Extracting: %v", err)
	}
	if result.Name != "Grace Hopper" {
		t.Fatalf("got %+v", result)
	}
}

// Reasoning-only responses, truncation, and transport failures must all be
// errors at the public boundary rather than empty successes.
func TestIntegrationProviderFailureModes(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"reasoning_only", `{"id":"r","status":"completed","model":"gpt-5.6-luna",
		  "output":[{"type":"reasoning","content":[{"type":"reasoning_text","text":"thinking"}]}],
		  "usage":{"input_tokens":1,"output_tokens":0,"total_tokens":1}}`},
		{"no_output_items", `{"id":"r","status":"completed","model":"gpt-5.6-luna","output":[],
		  "usage":{"input_tokens":1,"output_tokens":0,"total_tokens":1}}`},
		{"truncated_json_payload", message(`{"name":"Ada`)},
		{"empty_message_text", `{"id":"r","status":"completed","model":"gpt-5.6-luna",
		  "output":[{"type":"message","content":[{"type":"output_text","text":""}]}],
		  "usage":{"input_tokens":1,"output_tokens":0,"total_tokens":1}}`},
		{"prose_not_json", message("I am unable to extract that.")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			responsesAPI(t, tc.body)
			if _, err := schemaflux.Extract[person]("input", schemaflux.NewExtractOptions()); err == nil {
				t.Fatal("expected an error at the public boundary")
			}
		})
	}
}

// Cost accounting end to end: reasoning and cached tokens parsed by the
// provider must reach the cost record, and an unpriced model must be reported
// as unpriced rather than free.
func TestIntegrationUsageAndCostReachTheCostRecord(t *testing.T) {
	responsesAPI(t, message(`{"name":"Ada","age":36}`))

	const requestID = "integration-usage-1"
	opts := schemaflux.NewExtractOptions()
	// Set it on CommonOptions, not the embedded OpOptions: ExtractOptions
	// embeds both, and toOpOptions() returns only the CommonOptions half, so
	// anything set on the OpOptions side is silently dropped (TODOS.md F-032).
	opts.CommonOptions.RequestID = requestID

	if _, err := schemaflux.Extract[person]("Ada is 36.", opts); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	record, ok := pricing.GetRequestCost(requestID)
	if !ok {
		t.Fatal("no cost record was written for the request")
	}
	if record.TokenUsage.CachedTokens != 32 {
		t.Errorf("CachedTokens = %d, want 32", record.TokenUsage.CachedTokens)
	}
	if record.TokenUsage.ReasoningTokens != 12 {
		t.Errorf("ReasoningTokens = %d, want 12", record.TokenUsage.ReasoningTokens)
	}
	// gpt-5.6-luna has no published rates yet, so the record must say so
	// rather than reporting a confident $0.00.
	if record.Cost.Priced {
		t.Error("gpt-5.6-luna has no rates; the record must report Priced=false")
	}
}

// Example_extractFromReasoningModel is a runnable example for an LLM-backed
// operation against the Responses API shape a reasoning model produces. It runs
// under go test with no credential and no spend.
func Example_extractFromReasoningModel() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
		  "id":"resp","status":"completed","model":"gpt-5.6-luna",
		  "output":[
		    {"type":"reasoning","content":[{"type":"reasoning_text","text":"internal reasoning"}]},
		    {"type":"message","content":[{"type":"output_text","text":"{\"name\":\"Ada Lovelace\",\"age\":36}"}]}
		  ],
		  "usage":{"input_tokens":50,"output_tokens":20,"total_tokens":70}}`))
	}))
	defer server.Close()

	schemaflux.NewClient("example-key").WithProviderConfig("openai", schemaflux.ProviderConfig{
		APIKey:  "example-key",
		BaseURL: server.URL,
	})

	type Person struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}

	result, err := schemaflux.Extract[Person]("Ada Lovelace is 36.", schemaflux.NewExtractOptions())
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Printf("%s, %d\n", result.Name, result.Age)

	// Output:
	// Ada Lovelace, 36
}

// The end-to-end proof of F-032: a RequestID set on the embedded OpOptions --
// not the CommonOptions side -- must reach the provider and produce a cost
// record. Before the fix the field was dropped in toOpOptions() and no record
// was written, which is how the bug was found.
func TestIntegrationEmbeddedOpOptionsReachTheProvider(t *testing.T) {
	responsesAPI(t, message(`{"name":"Ada","age":36}`))

	const requestID = "embedded-side-request-id"
	opts := schemaflux.NewExtractOptions()
	opts.OpOptions.RequestID = requestID

	if _, err := schemaflux.Extract[person]("Ada is 36.", opts); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if _, ok := pricing.GetRequestCost(requestID); !ok {
		t.Fatal("no cost record for the embedded-side request ID; the option was discarded")
	}
}

// A context set on the embedded side must be able to cancel the call. A dropped
// context is a cancellation that never arrives.
func TestIntegrationEmbeddedContextCancels(t *testing.T) {
	responsesAPI(t, message(`{"name":"Ada","age":36}`))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	opts := schemaflux.NewExtractOptions()
	opts.OpOptions.Context = ctx

	if _, err := schemaflux.Extract[person]("Ada is 36.", opts); err == nil {
		t.Fatal("a cancelled context on the embedded side must abort the call")
	}
}
