package requesttracking

import (
	"context"
	"testing"
)

// Measured allocations, per operation, median over the runs of the baseline
// of 2026-10-03: allocations and bytes of one operation, exact where the
// times are not. bench_baseline_summary.txt has the nanoseconds, the time each
// package was run, and the tree it ran against.
//
//	benchmark                                   allocs/op       B/op
//	DefaultConfig                                      10       1392
//	GetConfig/unconfigured                              0          0
//	GetConfig/configured                                0          0
//	GetConfig/configured/parallel                       0          0
//	GenerateRequestID/auto                              1         24
//	GenerateRequestID/timestamp                         5         88
//	GenerateRequestID/auto/parallel                     1         24
//	Resolve/no_ids                                      1         24
//	Resolve/both_ids_on_the_context                     0          0
//	Resolve/configured,_no_ids                          1         24
//	Resolve/configured,_both_ids_on_the_context         0          0
//	Ensure/no_ids                                       5        152
//	Ensure/both_ids_on_the_context                      4        128
//	Ensure/no_ids/parallel                              5        152

// What request tracking costs a call, one piece at a time.
//
// One Extract resolves its request identity more than once (while validating
// its options, while converting them, and again inside the provider call), and
// each resolution reads the package's configuration. When nothing has called
// Configure, that configuration is built from five environment variables every
// time. These benchmarks take the pieces apart:
//
//   - DefaultConfig: the five environment reads, by themselves.
//   - GetConfig, unconfigured and configured: what a resolution pays to get the
//     configuration, with and without the environment.
//   - GenerateRequestID: the cryptographic read, the hex encoding and the
//     concatenation that make an id.
//   - Resolve and Ensure, with no ids on the context (an id is generated), with
//     both ids already on it (nothing to generate; does it still read the
//     environment?), and with tracking configured.
//

// benchUnconfigured puts the package into the state of a program that never
// called Configure, and restores what it found.
func benchUnconfigured(b *testing.B) {
	b.Helper()
	configMu.Lock()
	wasConfigured, was := configured, configuredCfg
	configured = false
	configMu.Unlock()
	b.Cleanup(func() {
		configMu.Lock()
		configured, configuredCfg = wasConfigured, was
		configMu.Unlock()
	})
}

// benchConfigured is the state after Configure(DefaultConfig()).
func benchConfigured(b *testing.B) {
	b.Helper()
	benchUnconfigured(b)
	Configure(DefaultConfig())
}

func BenchmarkDefaultConfig(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = DefaultConfig()
	}
}

func BenchmarkGetConfig(b *testing.B) {
	b.Run("unconfigured", func(b *testing.B) {
		benchUnconfigured(b)
		b.ReportAllocs()
		for b.Loop() {
			_ = GetConfig()
		}
	})
	b.Run("configured", func(b *testing.B) {
		benchConfigured(b)
		b.ReportAllocs()
		for b.Loop() {
			_ = GetConfig()
		}
	})
	b.Run("configured/parallel", func(b *testing.B) {
		benchConfigured(b)
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_ = GetConfig()
			}
		})
	})
}

func BenchmarkGenerateRequestID(b *testing.B) {
	ctx := context.Background()
	for _, strategy := range []IDStrategy{IDStrategyAuto, IDStrategyTimestamp} {
		b.Run(string(strategy), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = generateRequestID(strategy, ctx)
			}
		})
	}
	b.Run("auto/parallel", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_ = generateRequestID(IDStrategyAuto, ctx)
			}
		})
	})
}

func BenchmarkResolve(b *testing.B) {
	bare := context.Background()
	identified := WithMetadata(bare, Metadata{RequestID: "req_0123456789abcdef", CorrelationID: "req_0123456789abcdef"})

	b.Run("no ids", func(b *testing.B) {
		benchUnconfigured(b)
		b.ReportAllocs()
		for b.Loop() {
			_ = Resolve(bare, "", "")
		}
	})
	b.Run("both ids on the context", func(b *testing.B) {
		benchUnconfigured(b)
		b.ReportAllocs()
		for b.Loop() {
			_ = Resolve(identified, "", "")
		}
	})
	b.Run("configured, no ids", func(b *testing.B) {
		benchConfigured(b)
		b.ReportAllocs()
		for b.Loop() {
			_ = Resolve(bare, "", "")
		}
	})
	b.Run("configured, both ids on the context", func(b *testing.B) {
		benchConfigured(b)
		b.ReportAllocs()
		for b.Loop() {
			_ = Resolve(identified, "", "")
		}
	})
}

func BenchmarkEnsure(b *testing.B) {
	bare := context.Background()
	identified := WithMetadata(bare, Metadata{RequestID: "req_0123456789abcdef", CorrelationID: "req_0123456789abcdef"})

	b.Run("no ids", func(b *testing.B) {
		benchUnconfigured(b)
		b.ReportAllocs()
		for b.Loop() {
			_, _ = Ensure(bare, "", "")
		}
	})
	b.Run("both ids on the context", func(b *testing.B) {
		benchUnconfigured(b)
		b.ReportAllocs()
		for b.Loop() {
			_, _ = Ensure(identified, "", "")
		}
	})
	b.Run("no ids/parallel", func(b *testing.B) {
		benchUnconfigured(b)
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_, _ = Ensure(bare, "", "")
			}
		})
	})
}
