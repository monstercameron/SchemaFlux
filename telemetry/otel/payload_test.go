package otel_test

import (
	"context"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
)

// Nothing of the caller's, the model's or the provider's reaches a span. Five
// markers are planted, one in each place such text comes from, and every part
// of every exported span is searched for them.
const (
	markInput    = "MARK_INPUT_ONE"    // in the text the caller passes in
	markSteering = "MARK_STEER_TWO"    // in the caller's steering
	markAnswer   = "MARK_ANSWER_THREE" // in what the model answers
	markErrBody  = "MARK_ERRBODY_FOUR" // in the body of a provider's error
	markAPIKey   = "MARK_APIKEY_FIVE"  // the API key itself
)

var payloadMarkers = []string{markInput, markSteering, markAnswer, markErrBody, markAPIKey}

// leaks lists every place in spans where a marker appears: a span's name, its
// status description, an attribute's key or value, an event's name or
// attributes, a link's attributes, the resource and the instrumentation scope.
func leaks(spans tracetest.SpanStubs) []string {
	var found []string
	check := func(where, text string) {
		for _, marker := range payloadMarkers {
			if strings.Contains(text, marker) {
				found = append(found, marker+" in "+where)
			}
		}
	}
	for _, span := range spans {
		check("the name of "+span.Name, span.Name)
		check("the status description of "+span.Name, span.Status.Description)
		for _, kv := range span.Attributes {
			check("attribute key "+string(kv.Key), string(kv.Key))
			check("attribute "+string(kv.Key)+" of "+span.Name, kv.Value.Emit())
		}
		for _, event := range span.Events {
			check("event name on "+span.Name, event.Name)
			for _, kv := range event.Attributes {
				check("event attribute "+string(kv.Key)+" on "+span.Name, string(kv.Key)+kv.Value.Emit())
			}
		}
		for _, link := range span.Links {
			for _, kv := range link.Attributes {
				check("link attribute "+string(kv.Key)+" on "+span.Name, string(kv.Key)+kv.Value.Emit())
			}
		}
		if span.Resource != nil {
			for _, kv := range span.Resource.Attributes() {
				check("resource attribute "+string(kv.Key), string(kv.Key)+kv.Value.Emit())
			}
		}
		check("the instrumentation scope", span.InstrumentationScope.Name+span.InstrumentationScope.Version+span.InstrumentationScope.SchemaURL)
	}
	return found
}

// The scan itself has to be able to find a marker, or a clean result means
// nothing.
func TestTheLeakScanFindsAMarkerWhereverItIsPut(t *testing.T) {
	provider, exporter := traced(t)
	_, span := provider.Tracer("host").Start(context.Background(), "name "+markInput)
	span.End()
	if found := leaks(exporter.GetSpans()); len(found) != 1 {
		t.Errorf("a marker in a span name was found %d times, want once", len(found))
	}
}

func TestNoPayloadReachesASpanThroughARealProvider(t *testing.T) {
	// The library's own OpenAI provider against a server on the loopback
	// address, with the marker as its API key, so the key travels the path a
	// real one does.
	var mode atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		answer := func(text string) string {
			return `{"id":"resp_1","status":"completed","model":"gpt-served","output":[{"type":"message","content":[{"type":"output_text","text":` +
				text + `}]}],"usage":{"input_tokens":11,"output_tokens":7,"total_tokens":18}}`
		}
		switch mode.Load() {
		case 0: // an answer that is used
			_, _ = w.Write([]byte(answer(`"{\"number\":\"` + markAnswer + `\",\"vendor\":\"Acme\"}"`)))
		case 1: // an answer that is refused, every time
			_, _ = w.Write([]byte(answer(`"not JSON at all ` + markAnswer + `"`)))
		case 2: // the key is refused, and the error quotes it
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided: ` + markAPIKey + `. ` + markErrBody +
				`","type":"invalid_request_error","code":"invalid_api_key"}}`))
		case 3: // a failure whose body is not JSON
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`upstream broke ` + markErrBody + ` key=` + markAPIKey))
		}
	}))
	defer server.Close()

	openai, err := llm.NewOpenAIProvider(llm.ProviderConfig{APIKey: markAPIKey, BaseURL: server.URL, Timeout: 5 * time.Second, MaxRetries: 0})
	if err != nil {
		t.Fatal(err)
	}
	useProvider(t, openai)
	provider, exporter := traced(t)

	cases := []struct {
		name      string
		mode      int32
		wantError bool
		wantKind  types.ErrorKind
	}{
		{"a success", 0, false, types.KindUnknown},
		{"a refusal", 1, true, types.KindRepairExhausted},
		{"a provider error that quotes the key", 2, true, types.KindAuthentication},
		{"a provider error with a body that is not JSON", 3, true, types.KindProviderUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mode.Store(tc.mode)
			var callErr error
			spans := underCaller(provider, exporter, func(ctx context.Context) {
				options := extractOptions(ctx)
				options.CommonOptions.Steering = "prefer the header " + markSteering
				options.SchemaHints = map[string]string{"number": markSteering}
				_, callErr = ops.ExtractResult[invoice]("Invoice from Acme "+markInput, options)
			})

			if (callErr != nil) != tc.wantError || (tc.wantError && llm.Classify(callErr) != tc.wantKind) {
				t.Fatalf("the call returned %v (kind %q), want an error of kind %q: %v", callErr, llm.Classify(callErr), tc.wantKind, tc.wantError)
			}
			if len(requests(spans)) == 0 || len(named(spans, "schemaflux.extract")) != 1 {
				t.Fatalf("the trace is not the one under test:\n%s", describe(spans))
			}
			for _, leak := range leaks(spans) {
				t.Errorf("leaked: %s", leak)
			}
		})
	}
}

func TestNoPayloadReachesASpanFromAStream(t *testing.T) {
	provider, exporter := traced(t)
	script := &scriptedProvider{}
	useProvider(t, script)

	final := llm.StreamChunk{Done: true, Response: llm.CompletionResponse{
		Content: "summary " + markAnswer, Model: "local-mock", Provider: "local", FinishReason: "stop",
		Usage: types.TokenUsage{PromptTokens: 3, CompletionTokens: 4, TotalTokens: 7},
	}}
	streams := map[string]func(yield func(llm.StreamChunk, error) bool){
		"a stream read to its end": func(yield func(llm.StreamChunk, error) bool) {
			if yield(llm.StreamChunk{Delta: "summary " + markAnswer}, nil) {
				yield(final, nil)
			}
		},
		"a stream that fails with the provider's text": func(yield func(llm.StreamChunk, error) bool) {
			if yield(llm.StreamChunk{Delta: "summary " + markAnswer}, nil) {
				yield(llm.StreamChunk{}, llm.NewAPIError("local", "m", 500, `{"error":{"message":"`+markErrBody+` key `+markAPIKey+`"}}`))
			}
		},
		"a stream that fails with an error of no kind": func(yield func(llm.StreamChunk, error) bool) {
			yield(llm.StreamChunk{}, errors.New("connection reset while sending "+markInput+" "+markErrBody))
		},
	}
	for name, produce := range streams {
		t.Run(name, func(t *testing.T) {
			script.stream = func(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamChunk, error] { return produce }
			spans := underCaller(provider, exporter, func(ctx context.Context) {
				options := summarizeOptions(ctx)
				options.CommonOptions.Steering = "keep it short " + markSteering
				stream, err := ops.StreamSummarize("A text about "+markInput, options)
				if err != nil {
					t.Fatalf("StreamSummarize: %v", err)
				}
				_, _ = stream.Text()
			})

			if len(requests(spans)) != 1 {
				t.Fatalf("the trace is not the one under test:\n%s", describe(spans))
			}
			for _, leak := range leaks(spans) {
				t.Errorf("leaked: %s", leak)
			}
		})
	}
}
