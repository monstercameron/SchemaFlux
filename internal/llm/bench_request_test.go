package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Measured allocations, per operation, median over the runs of the baseline
// of 2026-10-03: allocations and bytes of one operation, exact where the
// times are not. bench_baseline_summary.txt has the nanoseconds, the time each
// package was run, and the tree it ran against.
//
//	benchmark                                         allocs/op       B/op
//	StrictSchemaProblem/5_properties                        103       4862
//	StrictSchemaProblem/20_properties                       325      18651
//	StrictSchemaProblem/60_properties                       894      58948
//	BuildResponsesRequestBody/0_properties/120B              18       1800
//	BuildResponsesRequestBody/5_properties/120B              19       1816
//	BuildResponsesRequestBody/20_properties/120B             19       1816
//	BuildResponsesRequestBody/60_properties/120B             19       1816
//	BuildResponsesRequestBody/5_properties/100KB             19     198264
//	BuildResponsesRequest/0_properties/120B                  57       4592
//	BuildResponsesRequest/0_properties/120B/parallel         57       4623
//	BuildResponsesRequest/5_properties/120B                 202      11126
//	BuildResponsesRequest/5_properties/120B/parallel        202      11192
//	BuildResponsesRequest/20_properties/120B                499      27824
//	BuildResponsesRequest/20_properties/120B/parallel       499      28103
//	BuildResponsesRequest/60_properties/120B               1268      76122
//	BuildResponsesRequest/60_properties/120B/parallel      1271      78384
//	BuildResponsesRequest/5_properties/100KB                202     319964
//	BuildResponsesRequest/5_properties/100KB/parallel       203     353542
//	DecodeResponses/small_200B                               31       2168
//	DecodeResponses/large_100KB                              38     703851
//	OpenAIComplete/base_url/no_schema                       198      16824  0.000444 conns/op
//	OpenAIComplete/base_url/no_schema/parallel              200      20794  0.006143 conns/op
//	OpenAIComplete/base_url/schema_12                       277      20495  0.0005165 conns/op
//	OpenAIComplete/base_url/schema_12/parallel              278      25202  0.004518 conns/op
//	OpenAIComplete/rewritten/no_schema                      197      17088  0.0003894 conns/op
//	OpenAIComplete/rewritten/no_schema/parallel             199      21710  0.003252 conns/op
//	OpenAIComplete/rewritten/schema_12                      483      32436  0.0006953 conns/op
//	OpenAIComplete/rewritten/schema_12/parallel             486      37984  0.007799 conns/op
//	OpenAICompleteServerHoldsStreamOpen                     338      91572  1 conns/op
//	OpenAICompleteStream                                   2480     228146  1 conns/op

// What the OpenAI Responses provider costs around the network wait: building
// the request, and decoding the response.
//
// Everything here is local. The request builders are called directly; the
// round trips go to an httptest server on the loopback interface that answers
// at once, so what is measured is this library's work plus the cost of a
// loopback HTTP exchange, and nothing a network adds. No key is used.
//
// Two ways of reaching the server, because they exercise different code:
//
//   - base URL: the provider is configured with the server's URL. That is not
//     OpenAI's own endpoint, so the provider skips two checks it makes for
//     OpenAI's (the strict-schema rules, StrictSchemaProblem, and the effort
//     resolution). This is the simplest way to point a provider at a test
//     server, and it under-counts the work a real request does.
//
//   - rewritten: the provider keeps its default address, api.openai.com, and
//     the http.Client it is given sends the request to the local server
//     instead. The provider takes every OpenAI-endpoint path, including the
//     strict-schema check, and no packet leaves the machine.
//
// The benchmarks:
//
//	BenchmarkStrictSchemaProblem/<n properties>           the check alone
//	BenchmarkBuildResponsesRequestBody/<n>/<prompt>       the body map, no marshal
//	BenchmarkBuildResponsesRequest/<n>/<prompt>           the whole http.Request: check, body, marshal, headers
//	BenchmarkDecodeResponses/<size>                       the response's JSON decode and classification
//	BenchmarkOpenAIComplete/<url>/<schema>                a full Complete against the local server
//	BenchmarkOpenAICompleteServerHoldsStreamOpen          the same when the server keeps the stream open
//	BenchmarkOpenAICompleteStream                         a streamed call: 200 deltas, the cost of a frame, and whether the connection is kept
//
// The connection count of each Complete benchmark is reported as conns/op: a
// provider that reuses its connection reports about zero, one that opens a new
// one per call reports about one.

// --- Fixtures.

// benchStrictSchema is a strict-valid object schema with n properties of four
// kinds, the shape the library sends for a target type of that many fields.
func benchStrictSchema(n int) map[string]any {
	kinds := []string{"string", "integer", "number", "boolean"}
	properties := make(map[string]any, n)
	required := make([]string, 0, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("field_%02d", i)
		properties[name] = map[string]any{"type": kinds[i%len(kinds)]}
		required = append(required, name)
	}
	return map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
}

const benchSystemPrompt = `You are a data extraction expert. Extract structured data from the input and return it as JSON.
Target schema:
  order_id: string (required)
  product: string (required)
  amount: string (required)

Rules:
- Extract all relevant information that maps to the schema
- Return ONLY valid JSON, no explanations or markdown
- Infer missing fields intelligently when possible
- Use reasonable defaults for missing data
- Be flexible with type conversions
- Preserve as much information as possible`

func benchResponsesRequest(properties, promptBytes int) CompletionRequest {
	req := CompletionRequest{
		Model:          "gpt-5.4-mini",
		SystemPrompt:   benchSystemPrompt,
		UserPrompt:     "Why is my October invoice for order A-2207 $49 and not $39?",
		Temperature:    0,
		MaxTokens:      4096,
		ResponseFormat: "json",
		SchemaName:     "result",
		PromptCacheKey: "extract/v1/gpt-5.4-mini/0123456789ab/0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	if properties > 0 {
		req.JSONSchema = benchStrictSchema(properties)
	}
	if promptBytes > 0 {
		req.UserPrompt = strings.Repeat("Why is my October invoice for order A-2207 $49 and not $39? ", promptBytes/64)
	}
	return req
}

// benchResponsesBody is a Responses API answer carrying text.
func benchResponsesBody(text string) []byte {
	encoded, _ := json.Marshal(text)
	return []byte(`{"id":"resp_0123","status":"completed","model":"gpt-5.4-mini","service_tier":"default",` +
		`"output":[{"type":"reasoning","content":[]},{"type":"message","content":[{"type":"output_text","text":` + string(encoded) + `}]}],` +
		`"usage":{"input_tokens":120,"output_tokens":24,"total_tokens":144,` +
		`"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}`)
}

// benchAnswerText is the text of an answer: a three-field record, or (size
// above zero) a list of records of about that many bytes.
func benchAnswerText(size int) string {
	if size <= 0 {
		return `{"order_id":"A-2207","product":"Team plan","amount":"$49"}`
	}
	record := `{"id":"rec-00001","name":"Dana Whitfield","email":"dana@example.com","note":"A short note about this record.","amount":12.25,"count":3,"active":true}`
	count := size/len(record) + 1
	return `{"records":[` + strings.TrimSuffix(strings.Repeat(record+",", count), ",") + `]}`
}

// rewriteTransport sends every request to target, whatever address it names:
// the way a provider that believes it is talking to api.openai.com reaches a
// server on the loopback interface.
type rewriteTransport struct {
	target string
	base   http.RoundTripper
}

func (r rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = "http"
	clone.URL.Host = r.target
	clone.Host = r.target
	return r.base.RoundTrip(clone)
}

// benchResponsesServer answers every request with body at once, and counts the
// connections it accepts.
func benchResponsesServer(b *testing.B, body []byte, handler http.HandlerFunc) (*httptest.Server, *atomic.Int64) {
	b.Helper()
	var connections atomic.Int64
	if handler == nil {
		handler = func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		}
	}
	server := httptest.NewUnstartedServer(handler)
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	b.Cleanup(server.Close)
	return server, &connections
}

// benchProvider builds an OpenAI provider that reaches server one of the two
// ways above.
func benchProviderFor(b *testing.B, server *httptest.Server, rewritten bool) *OpenAIProvider {
	b.Helper()
	config := ProviderConfig{APIKey: "bench-key", Timeout: 10 * time.Second}
	if rewritten {
		transport := &http.Transport{MaxIdleConnsPerHost: 64}
		b.Cleanup(transport.CloseIdleConnections)
		config.HTTPClient = &http.Client{Transport: rewriteTransport{
			target: strings.TrimPrefix(server.URL, "http://"),
			base:   transport,
		}}
	} else {
		config.BaseURL = server.URL
	}
	provider, err := NewOpenAIProvider(config)
	if err != nil {
		b.Fatalf("NewOpenAIProvider: %v", err)
	}
	return provider
}

// --- The request.

func BenchmarkStrictSchemaProblem(b *testing.B) {
	for _, n := range []int{5, 20, 60} {
		schema := benchStrictSchema(n)
		if problem := StrictSchemaProblem(schema); problem != "" {
			b.Fatalf("the benchmark's schema is not strict-valid: %s", problem)
		}
		b.Run(fmt.Sprintf("%d_properties", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = StrictSchemaProblem(schema)
			}
		})
	}
}

func BenchmarkBuildResponsesRequestBody(b *testing.B) {
	provider := &OpenAIProvider{config: ProviderConfig{APIKey: "bench-key"}}
	for _, shape := range []struct {
		name       string
		properties int
		prompt     int
	}{
		{"0_properties/120B", 0, 0},
		{"5_properties/120B", 5, 0},
		{"20_properties/120B", 20, 0},
		{"60_properties/120B", 60, 0},
		{"5_properties/100KB", 5, 100 << 10},
	} {
		req := benchResponsesRequest(shape.properties, shape.prompt)
		b.Run(shape.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = provider.buildResponsesRequestBody(req, false)
			}
		})
	}
}

// BenchmarkBuildResponsesRequest is the whole outgoing request: the strict
// schema check, the body, its marshalling, the headers. It is built at the
// provider's default address, so every OpenAI-only check runs.
func BenchmarkBuildResponsesRequest(b *testing.B) {
	provider := &OpenAIProvider{config: ProviderConfig{APIKey: "bench-key"}}
	ctx := context.Background()
	for _, shape := range []struct {
		name       string
		properties int
		prompt     int
	}{
		{"0_properties/120B", 0, 0},
		{"5_properties/120B", 5, 0},
		{"20_properties/120B", 20, 0},
		{"60_properties/120B", 60, 0},
		{"5_properties/100KB", 5, 100 << 10},
	} {
		req := benchResponsesRequest(shape.properties, shape.prompt)
		b.Run(shape.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := provider.newResponsesHTTPRequest(ctx, req, false); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(shape.name+"/parallel", func(b *testing.B) {
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if _, err := provider.newResponsesHTTPRequest(ctx, req, false); err != nil {
						b.Error(err)
						return
					}
				}
			})
		})
	}
}

// --- The response.

// BenchmarkDecodeResponses is what Complete does with the bytes of an answer
// once they are in hand: the streaming JSON decode, and the classification
// that turns the decoded response into a CompletionResponse.
func BenchmarkDecodeResponses(b *testing.B) {
	for _, shape := range []struct {
		name string
		size int
	}{{"small_200B", 0}, {"large_100KB", 100 << 10}} {
		body := benchResponsesBody(benchAnswerText(shape.size))
		b.Run(shape.name, func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			for b.Loop() {
				var response responsesAPIResponse
				if err := json.NewDecoder(bytes.NewReader(body)).Decode(&response); err != nil {
					b.Fatal(err)
				}
				if _, err := classifyResponsesResult(&response, "openai", "gpt-5.4-mini", nil, ProviderConfig{}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// --- The round trip.

func BenchmarkOpenAIComplete(b *testing.B) {
	for _, rewritten := range []bool{false, true} {
		url := "base_url"
		if rewritten {
			url = "rewritten"
		}
		for _, schema := range []struct {
			name       string
			properties int
		}{{"no_schema", 0}, {"schema_12", 12}} {
			req := benchResponsesRequest(schema.properties, 0)
			body := benchResponsesBody(benchAnswerText(0))

			b.Run(url+"/"+schema.name, func(b *testing.B) {
				server, connections := benchResponsesServer(b, body, nil)
				provider := benchProviderFor(b, server, rewritten)
				b.ReportAllocs()
				for b.Loop() {
					if _, err := provider.Complete(context.Background(), req); err != nil {
						b.Fatal(err)
					}
				}
				b.ReportMetric(float64(connections.Load())/float64(b.N), "conns/op")
			})
			b.Run(url+"/"+schema.name+"/parallel", func(b *testing.B) {
				server, connections := benchResponsesServer(b, body, nil)
				provider := benchProviderFor(b, server, rewritten)
				b.ReportAllocs()
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						if _, err := provider.Complete(context.Background(), req); err != nil {
							b.Error(err)
							return
						}
					}
				})
				b.ReportMetric(float64(connections.Load())/float64(b.N), "conns/op")
			})
		}
	}
}

// benchStreamHold is how long the server below keeps a response open after
// it has written the whole answer.
const benchStreamHold = 300 * time.Millisecond

// BenchmarkOpenAICompleteServerHoldsStreamOpen is a server that writes the
// answer, flushes it, and keeps the response open for benchStreamHold longer.
// The answer is complete as soon as its object is decoded, so what the call
// still waits for shows as time in this figure beyond the loopback exchange:
// about the hold if Complete reads the body to its end before it decodes, about
// the drain's own limit (100 ms) if it stops at the end of the object and
// drains the rest with a bound. The figure is the time of one call, not a
// throughput.
func BenchmarkOpenAICompleteServerHoldsStreamOpen(b *testing.B) {
	body := benchResponsesBody(benchAnswerText(0))
	handler := func(w http.ResponseWriter, r *http.Request) {
		// The request is read to its end first, as a real server does; the
		// server notices that the client has gone only after that.
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
		case <-time.After(benchStreamHold):
		}
	}
	server, connections := benchResponsesServer(b, nil, handler)
	provider := benchProviderFor(b, server, false)
	req := benchResponsesRequest(0, 0)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := provider.Complete(context.Background(), req); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(connections.Load())/float64(b.N), "conns/op")
}

// BenchmarkOpenAICompleteStream is a streamed call against an HTTP/1.1 server on
// loopback that sends 200 text deltas and the terminal event, then ends the
// response. allocs/op over 200 deltas is the cost of the frames; conns/op is
// reported because a stream whose body is closed before it is read to its end
// does not return its connection to the pool, so the next call opens another:
// about zero means the connection is kept, about one means it is not.
func BenchmarkOpenAICompleteStream(b *testing.B) {
	const deltas = 200
	var events strings.Builder
	for i := 0; i < deltas; i++ {
		fmt.Fprintf(&events, `event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"word%d "}

`, i)
	}
	completed := benchResponsesBody(benchAnswerText(0))
	fmt.Fprintf(&events, `event: response.completed
data: {"type":"response.completed","response":%s}

`, completed)
	body := []byte(events.String())

	handler := func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(body)
	}
	server, connections := benchResponsesServer(b, nil, handler)
	provider := benchProviderFor(b, server, false)
	req := benchResponsesRequest(0, 0)

	drain := func() error {
		count := 0
		for chunk, err := range provider.CompleteStream(context.Background(), req) {
			if err != nil {
				return err
			}
			if chunk.Delta != "" {
				count++
			}
		}
		if count != deltas {
			return fmt.Errorf("the stream delivered %d deltas, want %d", count, deltas)
		}
		return nil
	}
	if err := drain(); err != nil {
		b.Fatalf("the benchmark's stream fails before it is measured: %v", err)
	}
	b.ReportAllocs()
	connections.Store(0)
	for b.Loop() {
		if err := drain(); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(connections.Load())/float64(b.N), "conns/op")
}
