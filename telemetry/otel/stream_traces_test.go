package otel_test

import (
	"context"
	"fmt"
	"iter"
	"runtime"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
	fluxotel "github.com/monstercameron/schemaflux/telemetry/otel"
)

// A streamed answer is one request span: started when the stream's request is
// made, and ended once, however the stream ends. The streaming operations used
// to emit nothing at all.

// streamScript describes what a scripted stream does.
type streamScript struct {
	deltas       int
	failAfter    int    // yield an error instead of this delta; -1 never
	holdAfter    int    // after this delta, wait for the context to end; -1 never
	finishReason string // of the final chunk; "stop" when empty
	// pauseAfterFirst is how long the provider waits after its first delta,
	// without looking at its context: a provider that does not notice it was
	// cancelled and answers anyway.
	pauseAfterFirst time.Duration
}

func (script streamScript) stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamChunk, error] {
	return func(yield func(llm.StreamChunk, error) bool) {
		var text strings.Builder
		for index := 0; index < script.deltas; index++ {
			if index == script.failAfter {
				yield(llm.StreamChunk{}, llm.NewAPIError("local", req.Model, 500, `{"error":{"message":"broke midway"}}`))
				return
			}
			delta := fmt.Sprintf("piece %d ", index)
			text.WriteString(delta)
			if !yield(llm.StreamChunk{Delta: delta}, nil) {
				return
			}
			if index == 0 && script.pauseAfterFirst > 0 {
				time.Sleep(script.pauseAfterFirst)
			}
			if index == script.holdAfter {
				<-ctx.Done()
				yield(llm.StreamChunk{}, ctx.Err())
				return
			}
		}
		reason := script.finishReason
		if reason == "" {
			reason = "stop"
		}
		yield(llm.StreamChunk{Done: true, Response: llm.CompletionResponse{
			Content: text.String(), Model: req.Model, Provider: "local", FinishReason: reason,
			Usage: types.TokenUsage{PromptTokens: 3, CompletionTokens: 4, TotalTokens: 7},
		}}, nil)
	}
}

func summarizeOptions(ctx context.Context) ops.SummarizeOptions {
	options := ops.NewSummarizeOptions()
	options.CommonOptions.Context = ctx
	return options
}

// eventually waits for the exporter to hold a request span. A stream that is
// stopped rather than finished ends its span when its producer notices, which
// is a moment after the reader does.
func eventually(t *testing.T, exporter *tracetest.InMemoryExporter) tracetest.SpanStubs {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if made := requests(exporter.GetSpans()); len(made) > 0 || time.Now().After(deadline) {
			return made
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestAStreamIsOneRequestSpanEndedOnceHoweverItEnds(t *testing.T) {
	cases := []struct {
		name   string
		script streamScript
		// read consumes the stream. It may cancel the context it was opened on.
		read       func(t *testing.T, stream *ops.TextStream, cancel context.CancelFunc)
		derive     func(ctx context.Context) (context.Context, context.CancelFunc)
		wantStatus codes.Code
		wantKind   string
		answered   bool
		// atOnce means the span has ended by the time the reader returns.
		atOnce bool
	}{
		{
			name:   "read to the end",
			script: streamScript{deltas: 5, failAfter: -1, holdAfter: -1},
			read: func(t *testing.T, stream *ops.TextStream, _ context.CancelFunc) {
				text, err := stream.Text()
				if err != nil || !strings.HasPrefix(text, "piece 0") {
					t.Fatalf("Text = %q, %v", text, err)
				}
			},
			wantStatus: codes.Unset, answered: true, atOnce: true,
		},
		{
			name:   "ranged over to the end",
			script: streamScript{deltas: 3, failAfter: -1, holdAfter: -1},
			read: func(t *testing.T, stream *ops.TextStream, _ context.CancelFunc) {
				for _, err := range stream.All() {
					if err != nil {
						t.Fatalf("stream: %v", err)
					}
				}
			},
			wantStatus: codes.Unset, answered: true, atOnce: true,
		},
		{
			name:   "fails midway",
			script: streamScript{deltas: 5, failAfter: 2, holdAfter: -1},
			read: func(t *testing.T, stream *ops.TextStream, _ context.CancelFunc) {
				if _, err := stream.Text(); err == nil {
					t.Fatal("a stream that broke midway reported no error")
				}
			},
			wantStatus: codes.Error, wantKind: "provider unavailable", atOnce: true,
		},
		{
			name:   "fails before anything arrives",
			script: streamScript{deltas: 5, failAfter: 0, holdAfter: -1},
			read: func(_ *testing.T, stream *ops.TextStream, _ context.CancelFunc) {
				_, _ = stream.Text()
			},
			wantStatus: codes.Error, wantKind: "provider unavailable", atOnce: true,
		},
		{
			name:   "the reader leaves the loop",
			script: streamScript{deltas: 5, failAfter: -1, holdAfter: 0},
			read: func(_ *testing.T, stream *ops.TextStream, _ context.CancelFunc) {
				for range stream.All() {
					break
				}
			},
			wantStatus: codes.Error, wantKind: "canceled",
		},
		{
			// The stream ends with no final item of its own: the reader's
			// leaving decided the outcome, and the answer arrived afterwards.
			name:   "the reader leaves and the provider answers anyway",
			script: streamScript{deltas: 3, failAfter: -1, holdAfter: -1, pauseAfterFirst: 60 * time.Millisecond},
			read: func(_ *testing.T, stream *ops.TextStream, _ context.CancelFunc) {
				for range stream.All() {
					break
				}
			},
			wantStatus: codes.Error, wantKind: "canceled", answered: true,
		},
		{
			name:   "closed with Cancel",
			script: streamScript{deltas: 5, failAfter: -1, holdAfter: 0},
			read: func(_ *testing.T, stream *ops.TextStream, _ context.CancelFunc) {
				stream.Cancel()
			},
			wantStatus: codes.Error, wantKind: "canceled",
		},
		{
			name:   "never read, and its context is cancelled",
			script: streamScript{deltas: 5, failAfter: -1, holdAfter: 0},
			read: func(_ *testing.T, _ *ops.TextStream, cancel context.CancelFunc) {
				cancel()
			},
			wantStatus: codes.Error, wantKind: "canceled",
		},
		{
			name:   "its deadline passes",
			script: streamScript{deltas: 5, failAfter: -1, holdAfter: 0},
			derive: func(ctx context.Context) (context.Context, context.CancelFunc) {
				return context.WithTimeout(ctx, 40*time.Millisecond)
			},
			read: func(_ *testing.T, stream *ops.TextStream, _ context.CancelFunc) {
				_, _ = stream.Text()
			},
			wantStatus: codes.Error, wantKind: "timeout",
		},
		{
			name:   "the answer is cut off",
			script: streamScript{deltas: 2, failAfter: -1, holdAfter: -1, finishReason: "length"},
			read: func(t *testing.T, stream *ops.TextStream, _ context.CancelFunc) {
				if _, err := stream.Text(); types.KindOf(err) != types.KindOutputTruncated {
					t.Fatalf("Text returned %v, want a truncation", err)
				}
			},
			wantStatus: codes.Error, wantKind: "output truncated", answered: true, atOnce: true,
		},
		{
			name:   "an empty answer",
			script: streamScript{deltas: 0, failAfter: -1, holdAfter: -1},
			read: func(_ *testing.T, stream *ops.TextStream, _ context.CancelFunc) {
				_, _ = stream.Text()
			},
			wantStatus: codes.Error, wantKind: "malformed output", answered: true, atOnce: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exporter := tracetest.NewInMemoryExporter()
			tally := &tallyProvider{TracerProvider: sdktrace.NewTracerProvider(
				sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSyncer(exporter))}
			restore, err := fluxotel.Install(tally)
			if err != nil {
				t.Fatal(err)
			}
			defer restore()
			useProvider(t, &scriptedProvider{stream: tc.script.stream})

			callerCtx, caller := tally.Tracer("host").Start(context.Background(), "caller")
			derive := tc.derive
			if derive == nil {
				derive = context.WithCancel
			}
			ctx, cancel := derive(callerCtx)
			defer cancel()

			stream, err := ops.StreamSummarize("A long text about invoices.", summarizeOptions(ctx))
			if err != nil {
				t.Fatalf("StreamSummarize: %v", err)
			}
			tc.read(t, stream, cancel)

			if tc.atOnce {
				if made := requests(exporter.GetSpans()); len(made) != 1 {
					t.Fatalf("%d request spans had ended when the reader saw the stream end, want 1", len(made))
				}
			}
			made := eventually(t, exporter)
			if len(made) != 1 {
				t.Fatalf("%d request spans exported for one stream, want 1", len(made))
			}
			// Left a moment for a second end, which must not come.
			time.Sleep(20 * time.Millisecond)
			if started, ended, twice := tally.started.Load()-1, tally.ended.Load(), tally.endedTwice.Load(); started != 1 || ended != 1 || twice != 0 {
				t.Errorf("the library started %d spans, ended %d and ended %d a second time; want 1, 1 and 0", started, ended, twice)
			}

			span := made[0]
			if span.Name != "chat local-mock" || span.Parent.SpanID() != caller.SpanContext().SpanID() {
				t.Errorf("span %q under %s, want \"chat local-mock\" under the caller", span.Name, span.Parent.SpanID())
			}
			if span.Status.Code != tc.wantStatus || span.Status.Description != tc.wantKind {
				t.Errorf("status = %v %q, want %v %q", span.Status.Code, span.Status.Description, tc.wantStatus, tc.wantKind)
			}
			if stream, ok := attributeOf(span, "schemaflux.stream"); !ok || !stream.AsBool() {
				t.Error("the span is not marked schemaflux.stream")
			}
			if kind, ok := attributeOf(span, "error.type"); ok != (tc.wantKind != "") || kind.AsString() != tc.wantKind {
				t.Errorf("error.type = %q (set %v), want %q", kind.AsString(), ok, tc.wantKind)
			}
			if attempts, _ := attributeOf(span, "schemaflux.attempts"); attempts.AsInt64() != 1 {
				t.Errorf("schemaflux.attempts = %d; a stream is one attempt", attempts.AsInt64())
			}

			// What the provider reported with its last chunk is on the span
			// when that chunk arrived, and absent when it did not.
			_, usage := attributeOf(span, "gen_ai.usage.input_tokens")
			_, reason := attributeOf(span, "gen_ai.response.finish_reasons")
			_, model := attributeOf(span, "gen_ai.response.model")
			if usage != tc.answered || reason != tc.answered || model != tc.answered {
				t.Errorf("usage set %v, finish reason set %v, response model set %v; want all %v", usage, reason, model, tc.answered)
			}
			if _, priced := attributeOf(span, "schemaflux.cost.priced"); priced != (tc.answered && tc.wantKind == "") {
				t.Errorf("cost set = %v, want it only for an answer that was delivered", priced)
			}
			caller.End()
		})
	}
}

func TestAStreamASamplerDroppedIsWrittenNothingAndStillEnded(t *testing.T) {
	tally := &tallyProvider{TracerProvider: sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample()))}
	restore, err := fluxotel.Install(tally)
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	useProvider(t, &scriptedProvider{stream: streamScript{deltas: 3, failAfter: -1, holdAfter: -1}.stream})

	for range 5 {
		stream, err := ops.StreamSummarize("A long text.", summarizeOptions(context.Background()))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := stream.Text(); err != nil {
			t.Fatal(err)
		}
	}
	if started, ended := tally.started.Load(), tally.ended.Load(); started != 5 || ended != 5 {
		t.Errorf("%d spans started and %d ended, want 5 and 5", started, ended)
	}
	if writes := tally.onNonRecording.Load(); writes != 0 {
		t.Errorf("%d writes to spans the sampler dropped", writes)
	}
}

// A stream opened while the Observer is installed ends its span after the
// Observer has been removed.
func TestAStreamOpenAcrossARestoreStillEndsItsSpan(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSyncer(exporter))
	restore, err := fluxotel.Install(provider)
	if err != nil {
		t.Fatal(err)
	}
	useProvider(t, &scriptedProvider{stream: streamScript{deltas: 5, failAfter: -1, holdAfter: 0}.stream})

	stream, err := ops.StreamSummarize("A long text.", summarizeOptions(context.Background()))
	if err != nil {
		restore()
		t.Fatal(err)
	}
	for range stream.All() {
		break
	}
	restore()
	stream.Cancel()

	if made := eventually(t, exporter); len(made) != 1 || made[0].Status.Description != "canceled" {
		t.Errorf("the stream's span after the restore: %d exported", len(made))
	}
}

// A reader that panics out of the range loop leaves a stream as surely as one
// that breaks out of it, and has to stop it the same way. The producer was left
// blocked on a full buffer, holding a goroutine and the provider's connection,
// and the stream's span stayed open until the stream's own timeout.
func TestAReaderThatPanicsOutOfTheLoopStopsTheStreamAndEndsItsSpan(t *testing.T) {
	for _, tc := range []struct {
		name   string
		deltas int
	}{
		// More deltas than the stream buffers, so a producer nobody reads blocks.
		{"a long stream", 200},
		{"a stream longer than its buffer by one", 34},
		{"a short stream", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider, exporter := traced(t)
			useProvider(t, &scriptedProvider{stream: streamScript{deltas: tc.deltas, failAfter: -1, holdAfter: -1}.stream})
			goroutinesBefore := runtime.NumGoroutine()

			ctx, caller := provider.Tracer("host").Start(context.Background(), "caller")
			stream, err := ops.StreamSummarize("A long text.", summarizeOptions(ctx))
			if err != nil {
				t.Fatal(err)
			}
			func() {
				defer func() {
					if recovered := recover(); recovered != "the reader's bug" {
						t.Errorf("the reader's panic was lost or replaced: %v", recovered)
					}
				}()
				for range stream.All() {
					panic("the reader's bug")
				}
			}()
			caller.End()

			// The span ends within moments of the reader's panic, not at the
			// stream's timeout, which is seconds away.
			made := eventually(t, exporter)
			if len(made) != 1 {
				t.Fatalf("%d request spans exported after the reader panicked, want 1 (the stream was left running)", len(made))
			}
			if tc.deltas > 3 && (made[0].Status.Code != codes.Error || made[0].Status.Description != "canceled") {
				t.Errorf("the request span has status %v %q, want Error \"canceled\": the reader left before the stream finished",
					made[0].Status.Code, made[0].Status.Description)
			}

			deadline := time.Now().Add(5 * time.Second)
			for runtime.NumGoroutine() > goroutinesBefore && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			if after := runtime.NumGoroutine(); after > goroutinesBefore {
				t.Errorf("%d goroutines before and %d after: the stream's producer was left running", goroutinesBefore, after)
			}
		})
	}
}
