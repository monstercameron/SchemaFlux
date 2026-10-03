package otel_test

import (
	"context"
	"iter"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
	fluxotel "github.com/monstercameron/schemaflux/telemetry/otel"
)

// The tests in this file run real operations of the library through a scripted
// provider with the Observer installed over the SDK's in-memory exporter, and
// read what was exported. Nothing dials anything.

// scriptedProvider answers each call with whatever answer returns for it. It
// streams through stream when that is set.
type scriptedProvider struct {
	name    string
	retries int
	backoff time.Duration
	answer  func(ctx context.Context, call int, req llm.CompletionRequest) (llm.CompletionResponse, error)
	stream  func(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamChunk, error]
	calls   atomic.Int64
}

func (p *scriptedProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	call := int(p.calls.Add(1)) - 1
	return p.answer(ctx, call, req)
}

func (p *scriptedProvider) Name() string {
	if p.name == "" {
		return "local"
	}
	return p.name
}

func (p *scriptedProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }

func (p *scriptedProvider) RetryPolicy() (int, time.Duration) { return p.retries, p.backoff }

func (p *scriptedProvider) CompleteStream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamChunk, error] {
	return p.stream(ctx, req)
}

// answering returns a script that answers every call with body.
func answering(body string) func(context.Context, int, llm.CompletionRequest) (llm.CompletionResponse, error) {
	return func(_ context.Context, _ int, req llm.CompletionRequest) (llm.CompletionResponse, error) {
		return llm.CompletionResponse{
			Content:      body,
			Model:        req.Model,
			Provider:     "local",
			FinishReason: "stop",
			Usage:        types.TokenUsage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18},
		}, nil
	}
}

// useProvider makes p the provider operations run under for one test.
func useProvider(t *testing.T, p llm.Provider) {
	t.Helper()
	ops.SetDefaultProvider(p)
	t.Cleanup(func() { ops.SetDefaultProvider(nil) })
}

// invoice is the target the extraction tests decode into.
type invoice struct {
	Number string `json:"number"`
	Vendor string `json:"vendor"`
}

const invoiceAnswer = `{"number":"INV-1","vendor":"Acme"}`

// tallyProvider wraps a TracerProvider and counts what the library does to the
// spans it starts on it: how many were started, how many were ended and how
// often, and how many attribute writes reached a span that was not recording.
type tallyProvider struct {
	oteltrace.TracerProvider
	started, ended, endedTwice atomic.Int64
	// onNonRecording counts attribute and event writes that reached a span
	// that was not recording.
	onNonRecording atomic.Int64
}

type tallyTracer struct {
	oteltrace.Tracer
	tally *tallyProvider
}

type tallySpan struct {
	oteltrace.Span
	tally *tallyProvider
	ends  atomic.Int64
}

func (p *tallyProvider) Tracer(name string, opts ...oteltrace.TracerOption) oteltrace.Tracer {
	return &tallyTracer{Tracer: p.TracerProvider.Tracer(name, opts...), tally: p}
}

func (t *tallyTracer) Start(ctx context.Context, name string, opts ...oteltrace.SpanStartOption) (context.Context, oteltrace.Span) {
	ctx, span := t.Tracer.Start(ctx, name, opts...)
	t.tally.started.Add(1)
	wrapped := &tallySpan{Span: span, tally: t.tally}
	return oteltrace.ContextWithSpan(ctx, wrapped), wrapped
}

func (s *tallySpan) End(opts ...oteltrace.SpanEndOption) {
	if s.ends.Add(1) == 1 {
		s.tally.ended.Add(1)
	} else {
		s.tally.endedTwice.Add(1)
	}
	s.Span.End(opts...)
}

func (s *tallySpan) SetAttributes(attributes ...attribute.KeyValue) {
	if !s.Span.IsRecording() {
		s.tally.onNonRecording.Add(1)
	}
	s.Span.SetAttributes(attributes...)
}

func (s *tallySpan) AddEvent(name string, opts ...oteltrace.EventOption) {
	if !s.Span.IsRecording() {
		s.tally.onNonRecording.Add(1)
	}
	s.Span.AddEvent(name, opts...)
}

// discardingExporter accepts every span and keeps none.
type discardingExporter struct{}

func (discardingExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error { return nil }
func (discardingExporter) Shutdown(context.Context) error                             { return nil }

// A span started while one observer is installed is ended, whatever is
// installed by the time the call returns. Fifty goroutines make calls while the
// observer is installed and removed forty times; a call that began under an
// install and finished after its removal used to leave its span open for ever.
func TestEverySpanStartedIsEndedWhileObserversComeAndGo(t *testing.T) {
	provider := &scriptedProvider{answer: func(ctx context.Context, call int, req llm.CompletionRequest) (llm.CompletionResponse, error) {
		time.Sleep(200 * time.Microsecond)
		return answering(invoiceAnswer)(ctx, call, req)
	}}
	useProvider(t, provider)

	tally := &tallyProvider{TracerProvider: sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSyncer(discardingExporter{}))}

	var callers sync.WaitGroup
	stop := make(chan struct{})
	for range 50 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = ops.Extract[invoice]("Invoice", ops.NewExtractOptions())
			}
		}()
	}
	for range 40 {
		restore, err := fluxotel.Install(tally)
		if err != nil {
			t.Fatalf("Install: %v", err)
		}
		time.Sleep(4 * time.Millisecond)
		restore()
		time.Sleep(2 * time.Millisecond)
	}
	close(stop)
	callers.Wait()

	started, ended := tally.started.Load(), tally.ended.Load()
	if started == 0 {
		t.Fatal("no span was started, so the test observed nothing")
	}
	if started != ended {
		t.Errorf("%d spans were started and %d were ended: %d were never ended", started, ended, started-ended)
	}
	if twice := tally.endedTwice.Load(); twice != 0 {
		t.Errorf("%d spans were ended more than once", twice)
	}
}

// inMemory builds a TracerProvider that samples everything into an in-memory
// exporter, and shuts it down when the test ends.
func inMemory(t *testing.T, sampler sdktrace.Sampler) (*sdktrace.TracerProvider, *tracetest.InMemoryExporter) {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sampler), sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	return provider, exporter
}
