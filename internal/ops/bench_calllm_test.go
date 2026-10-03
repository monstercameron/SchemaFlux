package ops

import (
	"context"
	"fmt"
	"io"
	"strings"
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
//	benchmark                   allocs/op       B/op
//	CallLLM/NoSchema/1KB/Info          85       9664
//	CallLLM/NoSchema/1KB/Warn          72       8362
//	CallLLM/NoSchema/100KB/Info        85       9590
//	CallLLM/NoSchema/100KB/Warn        72       8362
//	CallLLM/NoSchema/1MB/Info          85       9592
//	CallLLM/NoSchema/1MB/Warn          72       8362
//	CallLLM/Schema12/1KB/Info          85       9624
//	CallLLM/Schema12/1KB/Warn          72       8394
//	CallLLM/Schema12/100KB/Info        85       9624
//	CallLLM/Schema12/100KB/Warn        72       8394
//	CallLLM/Schema12/1MB/Info          85       9623
//	CallLLM/Schema12/1MB/Warn          72       8394
//	CallLLMParallel/Info               85       8570
//	CallLLMParallel/Warn               72       7338

// What CallLLM costs by itself, which every provider-backed operation pays.
//
// CallLLM is everything between an operation's prompts and the provider: the
// model and tier resolution, the data-policy and budget checks, the prompt
// reinforcement, the request identity, the prompt cache key (a hash of the
// system prompt), the observer hooks, the call, the pricing and metrics of the
// answer, and the line it logs. The older benchmarks in this package replace it
// with a stub; this one runs it against a provider that answers at once, so
// that its cost can be told apart from the operation around it.
//
// Sub-benchmarks: the request carries no schema, or a 12-property one; the
// user prompt is 1 KB, 100 KB or 1 MB (what a prompt-sized copy costs); and
// the logger is at Info (its default: one line is written and kept) or at Warn
// (nothing is). The logger writes to io.Discard in both, so that the figure is
// the cost of building and formatting the line, not of where it goes, and its
// history capture is on, as it is by default.
//
//	BenchmarkCallLLM/<schema>/<prompt>/<level>
//	BenchmarkCallLLMParallel/<level>           the same call on every core, no schema, 1 KB
//
// The allocations are in the table at the top of this file and the
// nanoseconds in bench_baseline_summary.txt, which also has what the Info and Warn rows differ by.

const benchSentence = "Why is my October invoice for order A-2207 $49 and not $39? "

type benchInstantProvider struct{ answer string }

func (p benchInstantProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{
		Content: p.answer, Model: req.Model, Provider: "openai", FinishReason: "stop",
		Usage: types.TokenUsage{PromptTokens: 120, CompletionTokens: 24, TotalTokens: 144},
	}, nil
}
func (benchInstantProvider) Name() string                               { return "openai" }
func (benchInstantProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (benchInstantProvider) RetryPolicy() (int, time.Duration)          { return 0, 0 }

// benchLoggerLevel puts a logger at the given level on the process, writing to
// io.Discard with history capture on, and puts the previous one back.
func benchLoggerLevel(b *testing.B, level telemetry.LogLevel) {
	b.Helper()
	previous := telemetry.GetLogger()
	cfg := telemetry.DefaultLoggerConfig()
	cfg.Level = level
	cfg.Output = io.Discard
	cfg.Capture = true
	telemetry.SetLogger(telemetry.NewLoggerWithConfig(cfg))
	b.Cleanup(func() { telemetry.SetLogger(previous) })
}

func benchCallOptions(schema map[string]any) types.OpOptions {
	opts := types.OpOptions{
		Context:        context.Background(),
		Mode:           types.TransformMode,
		Intelligence:   types.Fast,
		ResponseFormat: "json",
	}
	if schema != nil {
		opts.JSONSchema = schema
		opts.SchemaName = "result"
		opts.CacheIdentity = "extract/v1/benchNested/0123456789ab"
	}
	return opts
}

func BenchmarkCallLLM(b *testing.B) {
	provider := benchInstantProvider{answer: `{"order_id":"A-2207"}`}
	system := BuildExtractSystemPrompt(GenerateTypeSchema(benchBuildTypes[1].typ), types.TransformMode)
	schemas := []struct {
		name   string
		schema map[string]any
	}{
		{"NoSchema", nil},
		{"Schema12", GenerateJSONSchema(benchBuildTypes[1].typ)},
	}
	prompts := []struct {
		name string
		size int
	}{{"1KB", 1 << 10}, {"100KB", 100 << 10}, {"1MB", 1 << 20}}
	levels := []struct {
		name  string
		level telemetry.LogLevel
	}{{"Info", telemetry.InfoLevel}, {"Warn", telemetry.WarnLevel}}

	for _, s := range schemas {
		for _, p := range prompts {
			user := strings.Repeat(benchSentence, p.size/len(benchSentence)+1)[:p.size]
			for _, l := range levels {
				b.Run(fmt.Sprintf("%s/%s/%s", s.name, p.name, l.name), func(b *testing.B) {
					benchLoggerLevel(b, l.level)
					opts := benchCallOptions(s.schema)
					if _, err := CallLLM(context.Background(), provider, system, user, opts); err != nil {
						b.Fatalf("the benchmark's call fails before it is measured: %v", err)
					}
					b.SetBytes(int64(p.size))
					b.ReportAllocs()
					for b.Loop() {
						if _, err := CallLLM(context.Background(), provider, system, user, opts); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		}
	}
}

func BenchmarkCallLLMParallel(b *testing.B) {
	provider := benchInstantProvider{answer: `{"order_id":"A-2207"}`}
	system := BuildExtractSystemPrompt(GenerateTypeSchema(benchBuildTypes[0].typ), types.TransformMode)
	user := "Why is my October invoice for order A-2207 $49 and not $39?"
	for _, l := range []struct {
		name  string
		level telemetry.LogLevel
	}{{"Info", telemetry.InfoLevel}, {"Warn", telemetry.WarnLevel}} {
		b.Run(l.name, func(b *testing.B) {
			benchLoggerLevel(b, l.level)
			opts := benchCallOptions(nil)
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if _, err := CallLLM(context.Background(), provider, system, user, opts); err != nil {
						b.Error(err)
						return
					}
				}
			})
		})
	}
}
