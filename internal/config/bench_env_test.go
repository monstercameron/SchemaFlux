package config

import (
	"os"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Measured allocations, per operation, median over the runs of the baseline
// of 2026-10-03: allocations and bytes of one operation, exact where the
// times are not. bench_baseline_summary.txt has the nanoseconds, the time each
// package was run, and the tree it ran against.
//
//	benchmark                                allocs/op       B/op
//	EnvironmentRead/unset                            2        272
//	EnvironmentRead/set                              3        260
//	EnvironmentRead/unset/parallel                   2        272
//	ConfigGetters/IsMetricsEnabled                   2        256
//	ConfigGetters/IsMetricsEnabled/parallel          2        256
//	ConfigGetters/GetDebugMode                       2        256
//	ConfigGetters/GetDebugMode/parallel              2        256
//	ConfigGetters/GetModel                           4        512
//	ConfigGetters/GetModel/parallel                  4        512
//	ConfigGetters/GetRepairAttempts                  2        272
//	ConfigGetters/GetRepairAttempts/parallel         2        272
//	ConfigGetters/GetTimeout                         2        256
//	ConfigGetters/GetTimeout/parallel                2        256
//	ConfigGetters/GetLLMMaxRetries                   2        272
//	ConfigGetters/GetLLMMaxRetries/parallel          2        272

// What the environment reads of one call cost, one function at a time.
//
// The configuration is read from the environment on every call, not once, so a
// call's cost includes however many reads it makes. On Windows os.Getenv is a
// system call (GetEnvironmentVariableW) that allocates its key and its buffer;
// on Linux it is a locked map lookup. These benchmarks measure the functions a
// call reaches, one each, with nothing set (the case of a caller who sets no
// SCHEMAFLUX_* variable), so a later change to any one of them can be told
// apart from the rest.
//
// BenchmarkEnvironmentRead is the unit all the others are made of: one
// os.Getenv of a variable that is not set. Divide a function's allocations by
// this one's to read off how many reads it makes.
//
// They are serial and parallel, so a lock around the read (config.mu) shows as
// a parallel figure that is not close to the serial one divided by the cores.

func BenchmarkEnvironmentRead(b *testing.B) {
	b.Run("unset", func(b *testing.B) {
		b.ReportAllocs()
		var sink string
		for b.Loop() {
			sink = os.Getenv("SCHEMAFLUX_BENCH_NOT_SET")
		}
		_ = sink
	})
	b.Run("set", func(b *testing.B) {
		b.Setenv("SCHEMAFLUX_BENCH_SET", "true")
		b.ReportAllocs()
		var sink string
		for b.Loop() {
			sink = os.Getenv("SCHEMAFLUX_BENCH_SET")
		}
		_ = sink
	})
	b.Run("unset/parallel", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			var sink string
			for pb.Next() {
				sink = os.Getenv("SCHEMAFLUX_BENCH_NOT_SET")
			}
			_ = sink
		})
	})
}

func BenchmarkConfigGetters(b *testing.B) {
	getters := []struct {
		name string
		call func()
	}{
		{"IsMetricsEnabled", func() { _ = IsMetricsEnabled() }},
		{"GetDebugMode", func() { _ = GetDebugMode() }},
		{"GetModel", func() { _ = GetModel(types.Fast, "openai") }},
		{"GetRepairAttempts", func() { _ = GetRepairAttempts() }},
		{"GetTimeout", func() { _ = GetTimeout() }},
		{"GetLLMMaxRetries", func() { _ = GetLLMMaxRetries() }},
	}
	for _, getter := range getters {
		b.Run(getter.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				getter.call()
			}
		})
		b.Run(getter.name+"/parallel", func(b *testing.B) {
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					getter.call()
				}
			})
		})
	}
}
