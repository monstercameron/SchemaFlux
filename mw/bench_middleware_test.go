package mw

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// Measured allocations, per operation, median over the runs of the baseline
// of 2026-10-03: allocations and bytes of one operation, exact where the
// times are not. bench_baseline_summary.txt has the nanoseconds, the time each
// package was run, and the tree it ran against.
//
//	benchmark                      allocs/op       B/op
//	RedactEgress/1KB/clean                60      22162
//	RedactEgress/1KB/with_secret          60      24788
//	RedactEgress/100KB/clean              73    1997526
//	RedactEgress/100KB/with_secret        78    1993716
//	CacheKey/0_properties                 40       3864
//	CacheKey/12_properties               115       8300
//	CacheKey/60_properties               355      24376
//	CacheHit/serial                      115       8299
//	CacheHit/parallel                    115       8308
//	CacheMissAndStore                    122       9184
//	HedgeNoFire/serial                     9       1384
//	HedgeNoFire/parallel                   9       1384

// What the opt-in middleware costs a call that passes through it.
//
// Nothing in NewClient wires a middleware, so none of this lands on a caller
// who did not ask for it; for one who did, each of these runs before the
// request is sent. The provider underneath answers at once, so the figures are
// the middleware's own.
//
// The benchmarks:
//
//	BenchmarkRedactEgress/<prompt>/<secret>          redaction of a 1 KB or 100 KB prompt, with and without a credential in it
//	BenchmarkCacheKey/<schema>                       the key a cache computes for a request
//	BenchmarkCacheHit/serial, parallel               a hit, which is the case a cache exists for
//	BenchmarkCacheMissAndStore                       a miss: key, provider call, store
//	BenchmarkHedgeNoFire                             a hedge whose delay is never reached: the goroutine it starts anyway
//
// The cache's key is computed before the call is looked up, so the key figure
// is part of the hit figure; the hit figure includes the provider wrapper's
// own lock and map work as well.

type benchAnswerProvider struct{ calls atomic.Int64 }

func (p *benchAnswerProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.calls.Add(1)
	return llm.CompletionResponse{
		Content: `{"order_id":"A-2207"}`, Model: req.Model, Provider: "openai", FinishReason: "stop",
		Usage: types.TokenUsage{PromptTokens: 120, CompletionTokens: 24, TotalTokens: 144},
	}, nil
}
func (*benchAnswerProvider) Name() string                               { return "openai" }
func (*benchAnswerProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (*benchAnswerProvider) RetryPolicy() (int, time.Duration)          { return 0, 0 }

func benchSchema(properties int) map[string]any {
	props := make(map[string]any, properties)
	required := make([]string, 0, properties)
	for i := 0; i < properties; i++ {
		name := fmt.Sprintf("field_%02d", i)
		props[name] = map[string]any{"type": "string"}
		required = append(required, name)
	}
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

func benchPromptOf(size int, withSecret bool) string {
	text := strings.Repeat("Why is my October invoice for order A-2207 $49 and not $39? ", size/64+1)
	if withSecret {
		text += " My key is sk-abcdefghijklmnopqrstuvwxyz0123456789 and my mail is dana@example.com."
	}
	return text
}

func benchRequest(prompt string, schemaProperties int) llm.CompletionRequest {
	req := llm.CompletionRequest{
		Model:          "gpt-5.4-mini",
		SystemPrompt:   "You are a data extraction expert. Return ONLY valid JSON.",
		UserPrompt:     prompt,
		ResponseFormat: "json",
		SchemaName:     "result",
		MaxTokens:      4096,
		PromptCacheKey: "extract/v1/gpt-5.4-mini/0123456789ab",
	}
	if schemaProperties > 0 {
		req.JSONSchema = benchSchema(schemaProperties)
	}
	return req
}

func BenchmarkRedactEgress(b *testing.B) {
	for _, size := range []int{1 << 10, 100 << 10} {
		for _, withSecret := range []bool{false, true} {
			secret := "clean"
			if withSecret {
				secret = "with_secret"
			}
			req := benchRequest(benchPromptOf(size, withSecret), 0)
			handler := Chain(&benchAnswerProvider{}, RedactEgress())
			b.Run(fmt.Sprintf("%dKB/%s", size>>10, secret), func(b *testing.B) {
				b.SetBytes(int64(len(req.UserPrompt)))
				b.ReportAllocs()
				for b.Loop() {
					if _, err := handler.Complete(context.Background(), req); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func BenchmarkCacheKey(b *testing.B) {
	for _, properties := range []int{0, 12, 60} {
		req := benchRequest(benchPromptOf(0, false), properties)
		b.Run(fmt.Sprintf("%d_properties", properties), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = cacheKeyFor(req, "openai", CachePartition{})
			}
		})
	}
}

func BenchmarkCacheHit(b *testing.B) {
	provider := &benchAnswerProvider{}
	handler := Chain(provider, Cache())
	req := benchRequest(benchPromptOf(0, false), 12)
	if _, err := handler.Complete(context.Background(), req); err != nil {
		b.Fatal(err)
	}
	b.Run("serial", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := handler.Complete(context.Background(), req); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("parallel", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if _, err := handler.Complete(context.Background(), req); err != nil {
					b.Error(err)
					return
				}
			}
		})
	})
	if provider.calls.Load() != 1 {
		b.Fatalf("the cache let %d calls through to the provider, want 1", provider.calls.Load())
	}
}

// BenchmarkCacheMissAndStore sends a different request each time, so every
// call misses and stores.
func BenchmarkCacheMissAndStore(b *testing.B) {
	handler := Chain(&benchAnswerProvider{}, Cache())
	req := benchRequest(benchPromptOf(0, false), 12)
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		i++
		req.UserPrompt = fmt.Sprintf("request number %d", i)
		if _, err := handler.Complete(context.Background(), req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkHedgeNoFire(b *testing.B) {
	handler := Chain(&benchAnswerProvider{}, Hedge(time.Hour))
	req := benchRequest(benchPromptOf(0, false), 0)
	b.Run("serial", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := handler.Complete(context.Background(), req); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("parallel", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if _, err := handler.Complete(context.Background(), req); err != nil {
					b.Error(err)
					return
				}
			}
		})
	})
}
