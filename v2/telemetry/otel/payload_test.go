package otel

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// Nothing of the caller's, the model's or the provider's reaches a span. Five
// markers are planted, one in each place such text comes from, a real operation
// of the library is run through a scripted provider with the Observer
// installed, and every part of every exported span is searched for them.
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
	provider, exporter := recording(t)
	ctx, span := provider.Tracer("host").Start(context.Background(), "name "+markInput)
	_, event := provider.Tracer("host").Start(ctx, "event")
	event.AddEvent("e "+markAnswer, oteltrace.WithAttributes(attribute.String("k", markErrBody)))
	event.SetStatus(codes.Error, markAPIKey)
	event.End()
	span.End()
	if found := leaks(exporter.GetSpans()); len(found) != 4 {
		t.Errorf("four markers were planted and %d found: %v", len(found), found)
	}
}

type invoice struct {
	Number string `json:"number"`
	Vendor string `json:"vendor"`
}

// scripted is a provider that answers each call as its script says.
type scripted struct {
	retries int
	answer  func(call int) (schemaflux.Response, error)
	calls   atomic.Int64
}

func (p *scripted) Complete(_ context.Context, req schemaflux.Request) (schemaflux.Response, error) {
	resp, err := p.answer(int(p.calls.Add(1)) - 1)
	if err == nil {
		resp.Model, resp.FinishReason = req.Model, "stop"
		resp.Usage = schemaflux.Usage{Input: 11, Output: 7, Total: 18}
	}
	return resp, err
}

func (p *scripted) Name() string                      { return "local" }
func (p *scripted) RetryPolicy() (int, time.Duration) { return p.retries, time.Millisecond }

func TestNoPayloadReachesASpanFromARealOperation(t *testing.T) {
	good := `{"number":"` + markAnswer + `","vendor":"Acme"}`
	keyBody := `{"error":{"message":"Incorrect API key provided: ` + markAPIKey + `. ` + markErrBody + `","type":"invalid_request_error","code":"invalid_api_key"}}`
	cases := []struct {
		name      string
		retries   int
		answer    func(call int) (schemaflux.Response, error)
		wantError bool
		wantRetry bool
	}{
		{"an answer that is used", 0, func(int) (schemaflux.Response, error) { return schemaflux.Response{Content: good}, nil }, false, false},
		{"an answer that is refused every time", 0, func(int) (schemaflux.Response, error) {
			return schemaflux.Response{Content: "not JSON at all " + markAnswer}, nil
		}, true, false},
		{"a provider error that quotes the key", 0, func(int) (schemaflux.Response, error) {
			return schemaflux.Response{}, llm.NewAPIError("local", "m", 401, keyBody)
		}, true, false},
		{"a provider error whose body is not JSON", 0, func(int) (schemaflux.Response, error) {
			return schemaflux.Response{}, llm.NewAPIError("local", "m", 500, "upstream broke "+markErrBody+" key="+markAPIKey)
		}, true, false},
		{"a failure that is retried and then answers", 2, func(call int) (schemaflux.Response, error) {
			if call == 0 {
				return schemaflux.Response{}, llm.NewAPIError("local", "m", 500, `{"error":{"message":"`+markErrBody+` `+markAPIKey+`"}}`)
			}
			return schemaflux.Response{Content: good}, nil
		}, false, true},
		{"an error of no kind that quotes the input", 0, func(int) (schemaflux.Response, error) {
			return schemaflux.Response{}, fmt.Errorf("connection reset while sending %s %s", markInput, markErrBody)
		}, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider, exporter := installed(t)
			client := schemaflux.Client{Provider: &scripted{retries: tc.retries, answer: tc.answer}, Model: "test-model"}

			ctx, caller := provider.Tracer("host").Start(context.Background(), "caller")
			options := ops.NewExtractOptions()
			options.CommonOptions.Context = schemaflux.WithClient(ctx, client)
			options.CommonOptions.Steering = "prefer the header " + markSteering
			options.SchemaHints = map[string]string{"number": markSteering}
			_, err := ops.ExtractResult[invoice]("Invoice from Acme "+markInput, options)
			caller.End()
			spans := exporter.GetSpans()

			if (err != nil) != tc.wantError {
				t.Fatalf("the call returned %v, want an error: %v", err, tc.wantError)
			}
			var operations, requests, retries int
			for _, span := range spans {
				switch {
				case span.Name == "schemaflux.extract":
					operations++
				case strings.HasPrefix(span.Name, "chat"):
					requests++
					for _, event := range span.Events {
						if event.Name == "retry" {
							retries++
						}
					}
				}
			}
			if operations != 1 || requests == 0 {
				t.Fatalf("%d operation spans and %d request spans: the test is not looking at a trace of a call", operations, requests)
			}
			if tc.wantRetry && retries == 0 {
				t.Fatal("no retry event on a request that was retried")
			}
			for _, leak := range leaks(spans) {
				t.Errorf("leaked: %s", leak)
			}
		})
	}
}
