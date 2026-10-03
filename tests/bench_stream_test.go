package tests

import (
	"context"
	"fmt"
	"iter"
	"runtime"
	"strings"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
)

// Measured allocations, per operation, median over the runs of the baseline
// of 2026-10-03: allocations and bytes of one operation, exact where the
// times are not. bench_baseline_summary.txt has the nanoseconds, the time each
// package was run, and the tree it ran against.
//
//	benchmark                         allocs/op       B/op
//	CallPathStreamSummarize/plain           134      56016  0.1342 allocs/delta  0 leaked-goroutines
//	CallPathStreamSummarize/scheduled       150      57122  0.1503 allocs/delta  0 leaked-goroutines

// What a stream costs per piece of text delivered, and whether it leaves a
// goroutine behind.
//
// A streaming provider here yields 1000 small deltas and then its terminal
// chunk, with no network and no delay. StreamSummarize is drained to its end
// on each iteration. The figures:
//
//	allocs/op        allocations for the whole stream (1000 deltas)
//	allocs/delta     the same divided by the deltas: what each piece costs
//	leaked-goroutines  goroutines alive after the benchmark that were not alive before it
//
// This measures the library's own stream machinery (the goroutine, the channel
// send per delta, the accumulation of the answer) and not any provider's
// framing of the wire; the provider's own SSE decoding is in internal/llm.
//
// The "scheduled" variant puts a scheduler on the client, which adds a second
// goroutine per stream that waits to be admitted.
//
// The allocations are in the table at the top of this file and the
// nanoseconds in bench_baseline_summary.txt.

const benchStreamDeltas = 1000

type benchStreamingProvider struct{ deltas []string }

func (p *benchStreamingProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{Content: strings.Join(p.deltas, ""), Model: req.Model, Provider: "openai", FinishReason: "stop"}, nil
}
func (*benchStreamingProvider) Name() string                               { return "openai" }
func (*benchStreamingProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (*benchStreamingProvider) RetryPolicy() (int, time.Duration)          { return 0, 0 }
func (p *benchStreamingProvider) CompleteStream(_ context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamChunk, error] {
	return func(yield func(llm.StreamChunk, error) bool) {
		var full strings.Builder
		for _, delta := range p.deltas {
			full.WriteString(delta)
			if !yield(llm.StreamChunk{Delta: delta}, nil) {
				return
			}
		}
		yield(llm.StreamChunk{Done: true, Response: llm.CompletionResponse{
			Content: full.String(), Model: req.Model, Provider: "openai", FinishReason: "stop",
			Usage: schemaflux.TokenUsage{PromptTokens: 120, CompletionTokens: 1000, TotalTokens: 1120},
		}}, nil)
	}
}

func BenchmarkCallPathStreamSummarize(b *testing.B) {
	deltas := make([]string, benchStreamDeltas)
	for i := range deltas {
		deltas[i] = fmt.Sprintf("word%d ", i)
	}
	provider := &benchStreamingProvider{deltas: deltas}

	for _, scheduled := range []bool{false, true} {
		name := "plain"
		if scheduled {
			name = "scheduled"
		}
		b.Run(name, func(b *testing.B) {
			benchSilenceLogging(b)
			client := schemaflux.NewClient("").WithProviderInstance(provider)
			if scheduled {
				scheduler := ops.NewScheduler(ops.SchedulerLimits{MaxConcurrent: 1 << 20})
				b.Cleanup(func() { _ = scheduler.Close(context.Background()) })
				client = client.WithScheduler(scheduler)
			}
			opts := schemaflux.NewSummarizeOptions()
			opts.CommonOptions = opts.CommonOptions.WithContext(client.Context(context.Background()))

			drain := func() error {
				stream, err := schemaflux.StreamSummarize(benchText, opts)
				if err != nil {
					return err
				}
				count := 0
				for _, err := range stream.All() {
					if err != nil {
						return err
					}
					count++
				}
				if err := stream.Err(); err != nil {
					return err
				}
				if count != benchStreamDeltas {
					return fmt.Errorf("the stream delivered %d deltas, want %d", count, benchStreamDeltas)
				}
				return nil
			}
			if err := drain(); err != nil {
				b.Fatalf("the benchmark's stream fails before it is measured: %v", err)
			}

			time.Sleep(20 * time.Millisecond)
			goroutinesBefore := runtime.NumGoroutine()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			b.ReportAllocs()
			for b.Loop() {
				if err := drain(); err != nil {
					b.Fatal(err)
				}
			}
			runtime.ReadMemStats(&after)
			b.ReportMetric(float64(after.Mallocs-before.Mallocs)/float64(b.N)/benchStreamDeltas, "allocs/delta")
			time.Sleep(50 * time.Millisecond)
			b.ReportMetric(float64(runtime.NumGoroutine()-goroutinesBefore), "leaked-goroutines")
		})
	}
}
