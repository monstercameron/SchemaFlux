package tests

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/ops"
)

// Measured allocations, per operation, median over the runs of the baseline
// of 2026-10-03: allocations and bytes of one operation, exact where the
// times are not. bench_baseline_summary.txt has the nanoseconds, the time each
// package was run, and the tree it ran against.
//
//	benchmark                              allocs/op       B/op
//	CallPathExtractGoroutines/quiet/g=1          295      60281
//	CallPathExtractGoroutines/quiet/g=8          295      60299
//	CallPathExtractGoroutines/quiet/g=64         295      60294
//	CallPathExtractGoroutines/default/g=1        317      62340
//	CallPathExtractGoroutines/default/g=8        318      62354
//	CallPathExtractGoroutines/default/g=64       319      62416
//	CallPathIsGoroutines/quiet/g=1                90       7570
//	CallPathIsGoroutines/quiet/g=8                90       7573
//	CallPathIsGoroutines/quiet/g=64               90       7574
//	CallPathIsGoroutines/default/g=1             103       8816
//	CallPathIsGoroutines/default/g=8             103       8810
//	CallPathIsGoroutines/default/g=64            103       8814
//	CallPathExtractResultGoroutines/g=1          322      61281
//	CallPathExtractResultGoroutines/g=8          322      61292
//	CallPathExtractResultGoroutines/g=64         322      61271
//	CallPathScheduledExtract/g=1                 307      61410
//	CallPathScheduledExtract/g=8                 307      61432
//	CallPathScheduledExtract/g=64                308      61428

// Whether calls on different goroutines wait for one another.
//
// The library keeps state that every call in the process touches: the cost
// ledger (one mutex over a history ring and four running totals), the logger
// (a mutex around the writer, and a second around the in-memory history), the
// cache-read tracker, and a number of read locks on configuration. A call that
// holds none of them for long costs the same on 64 goroutines as on 1; one
// that does costs more per call as goroutines are added, because the wall time
// per call stops falling when the lock is the bottleneck.
//
// Each benchmark here makes b.N calls spread over exactly g goroutines (1, 8
// and 64), through the stub provider that answers at once, and reports the wall
// time per call (ns/op) as the figure. Compare across g, and with the serial
// benchmark of the same call in bench_callpath_test.go: with the cores this
// machine has, a lock-free call's ns/op at g=8 would be about an eighth of its
// ns/op at g=1.
//
//	BenchmarkCallPathExtractGoroutines/<logging>/g=<n>     Extract of the three-field struct
//	BenchmarkCallPathIsGoroutines/<logging>/g=<n>          Is, the lightest real call
//	BenchmarkCallPathExtractResultGoroutines/g=<n>         the envelope form, quiet
//
// <logging> is quiet (the logger is off), or default (level Info, history
// capture on, with standard error as the library has it; the harness is
// expected to discard standard error, which makes this the cost of formatting,
// of the in-memory history and of the write to the discard device). The
// difference between the two at the same g is what the logger costs under
// contention; the pricing ledger is in both.
//
// To see how it scales with the machine, run these with -cpu 1,2,4,8,18.
//
// The allocations are in the table at the top of this file and the
// nanoseconds in bench_baseline_summary.txt.

// onGoroutines runs call b.N times spread over g goroutines and returns when
// all are done. The timer runs for the whole of it, so ns/op is wall time per
// call.
func onGoroutines(b *testing.B, g int, call func() error) {
	b.Helper()
	var next atomic.Int64
	total := int64(b.N)
	var wg sync.WaitGroup
	b.ResetTimer()
	for i := 0; i < g; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for next.Add(1) <= total {
				if err := call(); err != nil {
					b.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

var benchGoroutineCounts = []int{1, 8, 64}

func benchLoggingModes() []struct {
	name  string
	apply func(testing.TB)
} {
	return []struct {
		name  string
		apply func(testing.TB)
	}{
		{"quiet", benchSilenceLogging},
		{"default", benchDefaultLogging},
	}
}

func BenchmarkCallPathExtractGoroutines(b *testing.B) {
	ctx := benchContext(`{"order_id":"A-2207","product":"Team plan","amount":"$49"}`)
	opts := schemaflux.NewExtractOptions()
	opts.CommonOptions = opts.CommonOptions.WithContext(ctx)
	call := func() error {
		_, err := schemaflux.Extract[benchInvoice](benchText, opts)
		return err
	}
	for _, mode := range benchLoggingModes() {
		for _, g := range benchGoroutineCounts {
			b.Run(fmt.Sprintf("%s/g=%d", mode.name, g), func(b *testing.B) {
				mode.apply(b)
				b.ReportAllocs()
				onGoroutines(b, g, call)
			})
		}
	}
}

func BenchmarkCallPathIsGoroutines(b *testing.B) {
	ctx := benchContext(`{"q1":false}`)
	call := func() error {
		_, err := schemaflux.Is(ctx, benchText, "Is this message spam?")
		return err
	}
	for _, mode := range benchLoggingModes() {
		for _, g := range benchGoroutineCounts {
			b.Run(fmt.Sprintf("%s/g=%d", mode.name, g), func(b *testing.B) {
				mode.apply(b)
				b.ReportAllocs()
				onGoroutines(b, g, call)
			})
		}
	}
}

func BenchmarkCallPathExtractResultGoroutines(b *testing.B) {
	ctx := benchContext(`{"order_id":"A-2207","product":"Team plan","amount":"$49"}`)
	opts := schemaflux.NewExtractOptions()
	opts.CommonOptions = opts.CommonOptions.WithContext(ctx)
	call := func() error {
		_, err := schemaflux.ExtractResult[benchInvoice](benchText, opts)
		return err
	}
	for _, g := range benchGoroutineCounts {
		b.Run(fmt.Sprintf("g=%d", g), func(b *testing.B) {
			benchSilenceLogging(b)
			b.ReportAllocs()
			onGoroutines(b, g, call)
		})
	}
}

// BenchmarkCallPathScheduledExtract is Extract through a client whose
// scheduler admits everything at once (MaxConcurrent far above the number of
// goroutines), so what is measured is the scheduler's own bookkeeping per
// attempt and the cost of its one shared lock, on 1, 8 and 64 goroutines.
func BenchmarkCallPathScheduledExtract(b *testing.B) {
	scheduler := ops.NewScheduler(ops.SchedulerLimits{MaxConcurrent: 1 << 20})
	b.Cleanup(func() { _ = scheduler.Close(context.Background()) })
	client := schemaflux.NewClient("").
		WithProviderInstance(benchProvider{`{"order_id":"A-2207","product":"Team plan","amount":"$49"}`}).
		WithScheduler(scheduler)
	ctx := client.Context(context.Background())
	opts := schemaflux.NewExtractOptions()
	opts.CommonOptions = opts.CommonOptions.WithContext(ctx)
	call := func() error {
		_, err := schemaflux.Extract[benchInvoice](benchText, opts)
		return err
	}
	for _, g := range benchGoroutineCounts {
		b.Run(fmt.Sprintf("g=%d", g), func(b *testing.B) {
			benchSilenceLogging(b)
			b.ReportAllocs()
			onGoroutines(b, g, call)
		})
	}
}
