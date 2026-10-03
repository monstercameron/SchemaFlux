package pricing

import (
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// Measured allocations, per operation, median over the runs of the baseline
// of 2026-10-03: allocations and bytes of one operation, exact where the
// times are not. bench_baseline_summary.txt has the nanoseconds, the time each
// package was run, and the tree it ran against.
//
//	benchmark                          allocs/op       B/op
//	CalculateCost/priced                       1        160
//	CalculateCost/priced/parallel              1        160
//	CalculateCost/unpriced                     2        192
//	CalculateCost/unpriced/parallel            2        192
//	TrackCost/empty_history/serial            14        512
//	TrackCost/empty_history/parallel          14        512
//	TrackCost/full_history/serial             14        512
//	TrackCost/full_history/parallel           14        512
//	PriceAndTrack/serial                      15        672
//	PriceAndTrack/parallel                    15        672
//	CheckBudget/not_enforcing                  0          0
//	CheckBudget/not_enforcing/parallel         0          0

// What the cost ledger costs every call, serially and under contention.
//
// Every provider call prices its usage (CalculateCost) and records the result
// in a process-wide ledger (TrackCost) whether or not anyone set a budget. The
// ledger is one mutex over a history ring, a record and four running totals,
// so the serial figure is the cost of a call and the parallel figure shows
// whether calls on different cores wait for one another: if the lock is not a
// bottleneck, the parallel ns/op is about the serial ns/op divided by the
// number of cores; if it is, it is about the same as the serial one.
//
// "full history" runs after the ring of cost records has been filled to its
// limit, which is the steady state of a program that has made more calls than
// the limit; the first benchmarks run on an empty ring and so measure growth.

// pricedModel and unpricedModel: one with a rate card, one without.
const (
	benchPricedModel   = "gpt-5.4"
	benchUnpricedModel = "an-unlisted-model-name"
)

func benchUsage() *types.TokenUsage {
	return &types.TokenUsage{PromptTokens: 1200, CompletionTokens: 240, TotalTokens: 1440}
}

// benchMetadata is a call's record as CallLLM builds it, including the two
// entries of its Custom map.
func benchMetadata(model string, cost *types.CostInfo) *types.ResultMetadata {
	usage := benchUsage()
	now := time.Now()
	return &types.ResultMetadata{
		RequestID:     "req_0123456789abcdef",
		CorrelationID: "req_0123456789abcdef",
		StartTime:     now.Add(-400 * time.Millisecond),
		EndTime:       now,
		Duration:      400 * time.Millisecond,
		Model:         model,
		Provider:      "openai",
		Operation:     "extract@v1",
		TokenUsage:    usage,
		CostInfo:      cost,
		Custom: map[string]any{
			"response_format": "json",
			"schema_id":       "extract/benchInvoice/ab12cd34",
		},
	}
}

func benchFreshLedger(b *testing.B) {
	b.Helper()
	ResetCostTracking()
	b.Cleanup(ResetCostTracking)
}

// benchFullLedger fills the history ring to its limit.
func benchFullLedger(b *testing.B) {
	b.Helper()
	benchFreshLedger(b)
	cost := CalculateCost(benchUsage(), benchPricedModel, "openai")
	metadata := benchMetadata(benchPricedModel, cost)
	for i := 0; i < DefaultCostHistoryLimit+100; i++ {
		TrackCost(cost, metadata)
	}
}

func BenchmarkCalculateCost(b *testing.B) {
	usage := benchUsage()
	for _, tc := range []struct{ name, model string }{
		{"priced", benchPricedModel},
		{"unpriced", benchUnpricedModel},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = CalculateCost(usage, tc.model, "openai")
			}
		})
		b.Run(tc.name+"/parallel", func(b *testing.B) {
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					_ = CalculateCost(usage, tc.model, "openai")
				}
			})
		})
	}
}

func benchTrackCost(b *testing.B) {
	cost := CalculateCost(benchUsage(), benchPricedModel, "openai")
	metadata := benchMetadata(benchPricedModel, cost)
	b.Run("serial", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			TrackCost(cost, metadata)
		}
	})
	b.Run("parallel", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				TrackCost(cost, metadata)
			}
		})
	})
}

func BenchmarkTrackCost(b *testing.B) {
	b.Run("empty history", func(b *testing.B) {
		benchFreshLedger(b)
		benchTrackCost(b)
	})
	b.Run("full history", func(b *testing.B) {
		benchFullLedger(b)
		benchTrackCost(b)
	})
}

// BenchmarkPriceAndTrack is what one provider call pays, the two together, as
// CallLLM does.
func BenchmarkPriceAndTrack(b *testing.B) {
	benchFullLedger(b)
	usage := benchUsage()
	// The metadata is built once: CallLLM builds its own as part of its own
	// cost, which these benchmarks do not measure.
	metadata := benchMetadata(benchPricedModel, nil)
	b.Run("serial", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			TrackCost(CalculateCost(usage, benchPricedModel, "openai"), metadata)
		}
	})
	b.Run("parallel", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				TrackCost(CalculateCost(usage, benchPricedModel, "openai"), metadata)
			}
		})
	})
}

// BenchmarkCheckBudget is the check every call makes before it is sent, when
// no budget is enforced.
func BenchmarkCheckBudget(b *testing.B) {
	benchFreshLedger(b)
	b.Run("not enforcing", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = CheckBudget()
		}
	})
	b.Run("not enforcing/parallel", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_ = CheckBudget()
			}
		})
	})
}
