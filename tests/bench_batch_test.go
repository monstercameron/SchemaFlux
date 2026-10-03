package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
)

// Measured allocations, per operation, median over the runs of the baseline
// of 2026-10-03: allocations and bytes of one operation, exact where the
// times are not. bench_baseline_summary.txt has the nanoseconds, the time each
// package was run, and the tree it ran against.
//
//	benchmark                       allocs/op       B/op
//	BatchPath/RunOpMany_chunks20         2398     250752  5 calls/op  0.2848 ms/item  1 peak-calls
//	BatchPath/RunOpMany_chunks10         3127     285978  10 calls/op  0.5883 ms/item  1 peak-calls
//	BatchPath/RunOpMany_oneChunk         1947     232033  2 calls/op  0.153 ms/item  1 peak-calls
//	BatchPath/RunOpMany_atomic           9672     977452  100 calls/op  1.355 ms/item  4 peak-calls
//	BatchPath/ExtractBatch_parallel     30310    6192055  100 calls/op  0.567 ms/item  10 peak-calls
//	BatchPath/ExtractBatch_merged        2138     239304  5 calls/op  0.2793 ms/item  1 peak-calls
//	BatchPath/Extract_loop              30228    6064312  100 calls/op  5.498 ms/item  1 peak-calls
//	BatchPathOverlap/chunks_of_50        1948     229372  2 calls/op  1 peak-calls  10 serial-ms
//	BatchPathOverlap/chunks_of_20        2396     250658  5 calls/op  1 peak-calls  25 serial-ms
//	BatchPathOverlap/chunks_of_10        3132     288560  10 calls/op  1 peak-calls  50 serial-ms
//	BatchPathOverlap/chunks_of_5         4396     352280  20 calls/op  1 peak-calls  100 serial-ms
//	BatchPathGoroutinePeak             299680   59915310  2 baseline-goroutines  1002 peak-goroutines

// Whether the batch paths overlap the calls they make.
//
// A hundred items through each batch path, with a provider that takes a fixed
// 5 ms to answer every call (benchBatchDelay) and answers correctly. The
// figures that tell whether calls overlap are:
//
//	ms/item      wall time of the whole batch divided by its items
//	peak-calls   the most provider calls that were in flight at once
//	calls/op     provider calls the batch made
//
// A path that runs its calls one after another has peak-calls of 1 and a wall
// time of about calls x 5 ms; one that overlaps them has more, and a wall time
// near the longest chain. The provider does no work but wait, so the machine's
// load barely moves these figures, and they are the ones to rely on here more
// than any nanosecond count elsewhere.
//
// What each benchmark runs (100 items in each):
//
//	BenchmarkBatchPath/RunOpMany_chunks20     RunOpMany forced to the batched shape, at most 20 items a call: 5 chunks
//	BenchmarkBatchPath/RunOpMany_chunks10     the same at 10 items a call: 10 chunks
//	BenchmarkBatchPath/RunOpMany_oneChunk     the same at 100 items a call: 1 chunk
//	BenchmarkBatchPath/RunOpMany_atomic       RunOpMany over an operation with no batch form: one call per item
//	BenchmarkBatchPath/ExtractBatch_parallel  ExtractBatch in its parallel mode, 10 at a time
//	BenchmarkBatchPath/ExtractBatch_merged    ExtractBatch in its merged mode, 20 items a call: 5 calls
//	BenchmarkBatchPath/Extract_loop           a loop of Extract calls, for the serial baseline
//
// The allocations are in the table at the top of this file and the
// nanoseconds in bench_baseline_summary.txt with the three figures above.
//
// The logging is off for these (benchSilenceLogging): the benchmark is about
// waiting, and a line per call to standard error would add a cost that depends
// on where standard error goes.

const (
	benchBatchDelay = 5 * time.Millisecond
	benchBatchItems = 100
)

// benchSlowProvider answers after a fixed delay, from the request it is
// given, and counts how many calls were in flight at once.
type benchSlowProvider struct {
	delay    time.Duration
	answer   func(system, user string) string
	calls    atomic.Int64
	inflight atomic.Int64
	peak     atomic.Int64
}

func (p *benchSlowProvider) Complete(ctx context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	p.calls.Add(1)
	now := p.inflight.Add(1)
	defer p.inflight.Add(-1)
	for {
		peak := p.peak.Load()
		if now <= peak || p.peak.CompareAndSwap(peak, now) {
			break
		}
	}
	timer := time.NewTimer(p.delay)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		return schemaflux.CompletionResponse{}, ctx.Err()
	}
	return schemaflux.CompletionResponse{
		Content: p.answer(req.SystemPrompt, req.UserPrompt), Model: req.Model, Provider: "openai", FinishReason: "stop",
		Usage: schemaflux.TokenUsage{PromptTokens: 120, CompletionTokens: 24, TotalTokens: 144},
	}, nil
}
func (*benchSlowProvider) Name() string                                      { return "openai" }
func (*benchSlowProvider) EstimateCost(schemaflux.CompletionRequest) float64 { return 0 }
func (*benchSlowProvider) RetryPolicy() (int, time.Duration)                 { return 0, 0 }

func (p *benchSlowProvider) reset() {
	p.calls.Store(0)
	p.inflight.Store(0)
	p.peak.Store(0)
}

// benchWidget and benchWidgetLength are the item and the answer of the
// operation the batch benchmarks run.
type benchWidget struct {
	Name string `json:"name"`
}

type benchWidgetLength struct {
	Length int `json:"length"`
}

func benchWidgets(n int) []benchWidget {
	items := make([]benchWidget, n)
	for i := range items {
		items[i] = benchWidget{Name: fmt.Sprintf("widget-%03d", i)}
	}
	return items
}

// benchWidgetOp is an operation with a batch form: items are tagged with ids,
// and the answer names the ids.
func benchWidgetOp(class types.BatchClass) ops.Op[benchWidget, benchWidgetLength] {
	decode := func(body string) (benchWidgetLength, error) {
		var out benchWidgetLength
		err := json.Unmarshal([]byte(body), &out)
		return out, err
	}
	op := ops.Op[benchWidget, benchWidgetLength]{
		ID:        types.OperationID{Name: "widgetLength", Version: "v1"},
		Semantics: types.Semantics{Category: types.CategoryTransformation, Stability: types.StabilityExperimental},
		Contract:  ops.OutputContract[benchWidgetLength]{Decode: decode},
		BuildPrompt: func(w benchWidget, _ types.OpOptions) (string, string) {
			return "Say how long the widget's name is.", w.Name
		},
	}
	if class == types.BatchNone {
		op.Batch = ops.BatchAlgebra[benchWidget, benchWidgetLength]{Class: types.BatchNone}
		return op
	}
	op.Batch = ops.NewIDBatchAlgebra[benchWidget, benchWidgetLength](types.BatchItemwise, types.AlgebraIndependent,
		"Say how long each widget's name is.",
		ops.BatchItemCodec[benchWidget, benchWidgetLength]{
			DecodeItem: func(raw json.RawMessage) (benchWidgetLength, error) { return decode(string(raw)) },
		})
	return op
}

const benchBatchPrefix = "Process these items:\n"

// benchBatchAnswer reads whichever of the three prompts it was given and
// answers it correctly: the id protocol, the merged-extract prompt, or one
// item on its own.
func benchBatchAnswer(_, user string) string {
	switch {
	case strings.HasPrefix(user, benchBatchPrefix):
		var tagged []struct {
			ID   string      `json:"id"`
			Item benchWidget `json:"item"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(user, benchBatchPrefix)), &tagged); err != nil {
			return `{"items":[]}`
		}
		var out strings.Builder
		out.WriteString(`{"items":[`)
		for i, item := range tagged {
			if i > 0 {
				out.WriteString(",")
			}
			fmt.Fprintf(&out, `{"id":%q,"output":{"length":%d}}`, item.ID, len(item.Item.Name))
		}
		out.WriteString(`]}`)
		return out.String()
	case strings.HasPrefix(user, "Extract structured data for each"):
		count := len(benchMergedItem.FindAllString(user, -1))
		var out strings.Builder
		out.WriteString("[")
		for i := 0; i < count; i++ {
			if i > 0 {
				out.WriteString(",")
			}
			fmt.Fprintf(&out, `{"index":%d,"data":{"order_id":"A-2207","product":"Team plan","amount":"$49"}}`, i)
		}
		out.WriteString("]")
		return out.String()
	default:
		return `{"length":10,"order_id":"A-2207","product":"Team plan","amount":"$49"}`
	}
}

var benchMergedItem = regexp.MustCompile(`(?m)^Item \d+:`)

func benchSlowContext(p *benchSlowProvider) context.Context {
	return schemaflux.NewClient("").WithProviderInstance(p).Context(context.Background())
}

func BenchmarkBatchPath(b *testing.B) {
	benchSilenceLogging(b)
	items := benchWidgets(benchBatchItems)
	inputs := make([]interface{}, benchBatchItems)
	for i := range inputs {
		inputs[i] = benchText
	}

	type path struct {
		name  string
		calls func(p *benchSlowProvider, ctx context.Context) error
	}
	manyOver := func(class types.BatchClass, perCall int, force types.ExecutionShape) func(*benchSlowProvider, context.Context) error {
		op := benchWidgetOp(class)
		return func(_ *benchSlowProvider, ctx context.Context) error {
			results, plan, err := ops.RunOpMany(ctx, op, items, types.PlanRequest{Force: force, MaxItemsPerCall: perCall})
			if err != nil {
				return err
			}
			if len(results) != len(items) || results[7].Value.Length != len(items[7].Name) {
				return fmt.Errorf("RunOpMany returned %d results, shape %s; item 7 = %+v", len(results), plan.Shape, results[7].Value)
			}
			return nil
		}
	}
	paths := []path{
		{"RunOpMany_chunks20", manyOver(types.BatchItemwise, 20, types.ShapeMDSP)},
		{"RunOpMany_chunks10", manyOver(types.BatchItemwise, 10, types.ShapeMDSP)},
		{"RunOpMany_oneChunk", manyOver(types.BatchItemwise, 100, types.ShapeMDSP)},
		{"RunOpMany_atomic", manyOver(types.BatchNone, 0, types.ShapeAtomic)},
		{"ExtractBatch_parallel", func(p *benchSlowProvider, ctx context.Context) error {
			batch := ops.NewBatchProcessor(p).WithMode(ops.ParallelMode).WithConcurrency(10)
			result := ops.ExtractBatch[benchInvoice](batch, inputs, types.OpOptions{Context: ctx})
			if result.Metadata.Failed != 0 {
				return fmt.Errorf("%d of %d inputs failed: %v", result.Metadata.Failed, result.Metadata.TotalItems, result.Errors[0])
			}
			return nil
		}},
		{"ExtractBatch_merged", func(p *benchSlowProvider, ctx context.Context) error {
			batch := ops.NewBatchProcessor(p).WithMode(ops.MergedMode).WithBatchSize(20)
			result := ops.ExtractBatch[benchInvoice](batch, inputs, types.OpOptions{Context: ctx})
			if result.Metadata.Failed != 0 {
				return fmt.Errorf("%d of %d inputs failed: %v", result.Metadata.Failed, result.Metadata.TotalItems, result.Errors[0])
			}
			return nil
		}},
		{"Extract_loop", func(p *benchSlowProvider, ctx context.Context) error {
			opts := schemaflux.NewExtractOptions()
			opts.CommonOptions = opts.CommonOptions.WithContext(ctx)
			for range inputs {
				if _, err := schemaflux.Extract[benchInvoice](benchText, opts); err != nil {
					return err
				}
			}
			return nil
		}},
	}

	for _, tc := range paths {
		b.Run(tc.name, func(b *testing.B) {
			provider := &benchSlowProvider{delay: benchBatchDelay, answer: benchBatchAnswer}
			ctx := benchSlowContext(provider)
			if err := tc.calls(provider, ctx); err != nil {
				b.Fatalf("the benchmark's batch fails before it is measured: %v", err)
			}
			provider.reset()
			b.ReportAllocs()
			start := time.Now()
			for b.Loop() {
				if err := tc.calls(provider, ctx); err != nil {
					b.Fatal(err)
				}
			}
			elapsed := time.Since(start)
			b.ReportMetric(float64(elapsed.Microseconds())/1000/float64(b.N)/benchBatchItems, "ms/item")
			b.ReportMetric(float64(provider.peak.Load()), "peak-calls")
			b.ReportMetric(float64(provider.calls.Load())/float64(b.N), "calls/op")
		})
	}
}

// BenchmarkBatchPathOverlap is RunOpMany at four chunk sizes. serial-ms is the
// wall time a path that ran its chunks one after another would take
// (chunks x the provider's delay); ns/op below it says the chunks overlapped,
// and ns/op near it says they did not. The number of chunks is the only thing
// that changes between the runs.
func BenchmarkBatchPathOverlap(b *testing.B) {
	benchSilenceLogging(b)
	items := benchWidgets(benchBatchItems)
	op := benchWidgetOp(types.BatchItemwise)
	for _, perCall := range []int{50, 20, 10, 5} {
		b.Run("chunks_of_"+strconv.Itoa(perCall), func(b *testing.B) {
			provider := &benchSlowProvider{delay: benchBatchDelay, answer: benchBatchAnswer}
			ctx := benchSlowContext(provider)
			b.ReportAllocs()
			for b.Loop() {
				if _, _, err := ops.RunOpMany(ctx, op, items, types.PlanRequest{Force: types.ShapeMDSP, MaxItemsPerCall: perCall}); err != nil {
					b.Fatal(err)
				}
			}
			chunks := float64(benchBatchItems / perCall)
			b.ReportMetric(float64(provider.peak.Load()), "peak-calls")
			b.ReportMetric(float64(provider.calls.Load())/float64(b.N), "calls/op")
			b.ReportMetric(chunks*float64(benchBatchDelay.Milliseconds()), "serial-ms")
		})
	}
}

// BenchmarkBatchPathGoroutinePeak is ExtractBatch's parallel mode over 1000
// inputs, ten at a time. The mode starts one goroutine per input at once and
// parks all but ten on a semaphore, so the peak number of goroutines is about
// the number of inputs, not about the concurrency asked for. peak-goroutines is
// the most that were alive while the batch ran, sampled by the provider at each
// call; baseline-goroutines is how many were alive before it started.
func BenchmarkBatchPathGoroutinePeak(b *testing.B) {
	benchSilenceLogging(b)
	inputs := make([]interface{}, 1000)
	for i := range inputs {
		inputs[i] = benchText
	}
	var peak atomic.Int64
	provider := &benchSlowProvider{delay: time.Millisecond, answer: func(_, _ string) string {
		for n := int64(runtime.NumGoroutine()); ; {
			current := peak.Load()
			if n <= current || peak.CompareAndSwap(current, n) {
				break
			}
		}
		return `{"order_id":"A-2207","product":"Team plan","amount":"$49"}`
	}}
	ctx := benchSlowContext(provider)
	baseline := runtime.NumGoroutine()
	b.ReportAllocs()
	for b.Loop() {
		batch := ops.NewBatchProcessor(provider).WithMode(ops.ParallelMode).WithConcurrency(10)
		result := ops.ExtractBatch[benchInvoice](batch, inputs, types.OpOptions{Context: ctx})
		if result.Metadata.Failed != 0 {
			b.Fatalf("%d of %d inputs failed: %v", result.Metadata.Failed, result.Metadata.TotalItems, result.Errors[0])
		}
	}
	b.ReportMetric(float64(peak.Load()), "peak-goroutines")
	b.ReportMetric(float64(baseline), "baseline-goroutines")
}
