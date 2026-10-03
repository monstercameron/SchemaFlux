package client

import (
	"context"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/telemetry"
	"github.com/monstercameron/schemaflux/internal/types"
)

// Measured allocations, per operation, median over the runs of the baseline
// of 2026-10-03: allocations and bytes of one operation, exact where the
// times are not. bench_baseline_summary.txt has the nanoseconds, the time each
// package was run, and the tree it ran against.
//
//	benchmark                    allocs/op       B/op
//	ClientContext/plain                  3        272
//	ClientContext/configured             5        336
//	ClientContext/plain/parallel         3        272

// What the documented way of using a client costs every operation.
//
// A caller attaches the client to each operation's context with
// client.Context(ctx), so that call is made once per operation. It takes the
// client's read lock, copies its configuration into a snapshot (so that a call
// already running keeps the configuration it started with), and derives two
// context values from the caller's.
//
// The benchmarks:
//
//	BenchmarkClientContext/plain             a client with a provider and nothing else
//	BenchmarkClientContext/configured        the same with a data policy, a model and an effort set
//	BenchmarkClientContext/plain/parallel    the read lock under contention
//
// The logger is not involved: building a client writes one line (a warning when
// it has no key), which is why the client here is built once, outside the loop.

type benchInstantProvider struct{}

func (benchInstantProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{Content: "{}", Model: req.Model, Provider: "openai", FinishReason: "stop"}, nil
}
func (benchInstantProvider) Name() string                               { return "openai" }
func (benchInstantProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (benchInstantProvider) RetryPolicy() (int, time.Duration)          { return 0, 0 }

func BenchmarkClientContext(b *testing.B) {
	// The client constructor logs a line when it has no key; this keeps it off
	// the benchmark's output without changing what the benchmark measures,
	// which does not log.
	previous := telemetry.GetLogger()
	b.Cleanup(func() { telemetry.SetLogger(previous) })
	telemetry.ConfigureLogger(telemetry.LoggerConfig{Level: telemetry.ErrorLevel, DisableStderr: true})

	plain := NewClient("").WithProviderInstance(benchInstantProvider{})
	configured := NewClient("").WithProviderInstance(benchInstantProvider{}).
		WithModel("gpt-5.4-mini").
		WithDataPolicy(types.DataPolicy{AllowedProviders: []string{"openai", "anthropic"}, AllowedRegions: []string{"us", "eu"}})

	base := context.Background()
	b.Run("plain", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = plain.Context(base)
		}
	})
	b.Run("configured", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = configured.Context(base)
		}
	})
	b.Run("plain/parallel", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_ = plain.Context(base)
			}
		})
	})
}
