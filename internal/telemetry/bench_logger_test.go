package telemetry

import (
	"io"
	"path/filepath"
	"testing"
	"time"
)

// Measured allocations, per operation, median over the runs of the baseline
// of 2026-10-03: allocations and bytes of one operation, exact where the
// times are not. bench_baseline_summary.txt has the nanoseconds, the time each
// package was run, and the tree it ran against.
//
//	benchmark                                 allocs/op       B/op
//	LoggerInfoLine/discard/capture/serial            14       1226
//	LoggerInfoLine/discard/capture/parallel          14       1227
//	LoggerInfoLine/discard/nocapture/serial           1        160
//	LoggerInfoLine/discard/nocapture/parallel         1        160
//	LoggerInfoLine/file/capture/serial               14       1227
//	LoggerInfoLine/file/capture/parallel             14       1229
//	LoggerInfoLine/file/nocapture/serial              1        160
//	LoggerInfoLine/file/nocapture/parallel            1        160
//	LoggerLinesOfOneExtract/discard/serial           24       1987
//	LoggerLinesOfOneExtract/discard/parallel         24       1987
//	LoggerLinesOfOneExtract/file/serial              24       1991
//	LoggerLinesOfOneExtract/file/parallel            24       1994
//	LoggerFilteredDebugLine/serial                    0          0
//	LoggerFilteredDebugLine/parallel                  0          0

// What the logger costs at its default level, for the lines one call writes.
//
// Every logger here is built from DefaultLoggerConfig, the configuration a
// caller who never touches logging gets (history capture on, a buffer of 1000),
// with the level put at Info, which is no longer the default (a default logger
// is at warn and writes nothing for a call that goes well) but is what a caller
// who asks for the lines gets, and the destination changed: io.Discard, to take the
// cost of formatting apart from the cost of a write, or a real file in a
// temporary directory, to put the write back. Standard error is never used, so
// these benchmarks are quiet and the figures do not depend on what is on the
// other end of the harness's pipe.
//
// The history ring is filled to its limit before the timer starts, because a
// full ring is the steady state of a long-running program and is the state in
// which the audit suspects a 64 KB copy per line.

// benchLoggerAt returns a logger with the default configuration and the given
// destination and history capture.
func benchLoggerAt(b *testing.B, sink string, capture bool) *Logger {
	b.Helper()
	cfg := DefaultLoggerConfig()
	cfg.Level = InfoLevel
	cfg.Capture = capture
	switch sink {
	case "discard":
		cfg.Output = io.Discard
	case "file":
		cfg.DisableStderr = true
		cfg.FilePath = filepath.Join(b.TempDir(), "schemaflux.log")
	default:
		b.Fatalf("unknown sink %q", sink)
	}
	logger := NewLoggerWithConfig(cfg)
	b.Cleanup(func() { _ = logger.Close() })
	if capture {
		// A full ring: every later line shifts the entries down by one.
		for i := 0; i < cfg.BufferSize+10; i++ {
			logger.Info("prefill", "n", i)
		}
	}
	return logger
}

// logLLMRequestCompleted writes the line CallLLM writes after every provider
// call: nine attributes of the kinds a real call has.
func logLLMRequestCompleted(logger *Logger) {
	logger.Info("LLM request completed",
		"requestID", "req_1a2b3c4d5e6f7a8b",
		"correlationID", "req_1a2b3c4d5e6f7a8b",
		"provider", "openai",
		"model", "gpt-5.4-mini",
		"responseFormat", "json",
		"duration_ms", int64(412),
		"tokens_total", 144,
		"cost_usd", 0.000123,
		"finishReason", "stop",
	)
}

// logOneExtract writes the three lines one successful Extract writes at the
// default level: the start line, the provider line, the completion line.
func logOneExtract(logger *Logger) {
	logger.Info("Extract operation started",
		"requestID", "req_1a2b3c4d5e6f7a8b",
		"targetType", "tests.benchInvoice",
		"mode", "transform",
		"intelligence", "fast",
	)
	logLLMRequestCompleted(logger)
	logger.Info("Extract operation completed",
		"requestID", "req_1a2b3c4d5e6f7a8b",
		"duration", 412*time.Millisecond,
		"attempts", 1,
		"repaired", false,
	)
}

func runSerialAndParallel(b *testing.B, write func()) {
	b.Run("serial", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			write()
		}
	})
	b.Run("parallel", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				write()
			}
		})
	})
}

// BenchmarkLoggerInfoLine is one nine-attribute Info line, with and without
// history capture, to a discarded writer and to a file.
func BenchmarkLoggerInfoLine(b *testing.B) {
	for _, sink := range []string{"discard", "file"} {
		for _, capture := range []bool{true, false} {
			name := sink + "/nocapture"
			if capture {
				name = sink + "/capture"
			}
			b.Run(name, func(b *testing.B) {
				logger := benchLoggerAt(b, sink, capture)
				runSerialAndParallel(b, func() { logLLMRequestCompleted(logger) })
			})
		}
	}
}

// BenchmarkLoggerLinesOfOneExtract is the three lines one Extract writes, at
// the default configuration.
func BenchmarkLoggerLinesOfOneExtract(b *testing.B) {
	for _, sink := range []string{"discard", "file"} {
		b.Run(sink, func(b *testing.B) {
			logger := benchLoggerAt(b, sink, true)
			runSerialAndParallel(b, func() { logOneExtract(logger) })
		})
	}
}

// BenchmarkLoggerFilteredDebugLine is a Debug line at the default (Info)
// level, which the logger drops. The arguments are evaluated and boxed before
// the level is looked at, which is what the line costs a caller who never asked
// for Debug.
func BenchmarkLoggerFilteredDebugLine(b *testing.B) {
	logger := benchLoggerAt(b, "discard", true)
	mode, tier := "transform", "fast"
	write := func() {
		logger.Debug("LLM request started",
			"requestID", "req_1a2b3c4d5e6f7a8b",
			"correlationID", "req_1a2b3c4d5e6f7a8b",
			"provider", "openai",
			"model", "gpt-5.4-mini",
			"responseFormat", "json",
			"maxTokens", 4096,
			"mode", mode,
			"intelligence", tier,
		)
	}
	runSerialAndParallel(b, write)
}
