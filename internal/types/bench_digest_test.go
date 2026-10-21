package types

import (
	"strings"
	"testing"
)

// Measured allocations, per operation, median over the runs of the baseline
// of 2026-10-03: allocations and bytes of one operation, exact where the
// times are not. bench_baseline_summary.txt has the nanoseconds, the time each
// package was run, and the tree it ran against.
//
//	benchmark         allocs/op       B/op
//	DigestValue/1KB           4       1168
//	DigestValue/200KB         4     204944
//	DigestValue/1MB           4    1048722
//	NewResultID               1         32

// What the provenance work after an answer costs, by itself.
//
// ExtractResult (and every operation's envelope form) identifies the call's
// input by a content digest and its result by a random id. The digest is a
// SHA-256 over the whole input, copied to bytes first, so it is the one step
// after the answer whose cost grows with the size of the input.
//
//	BenchmarkDigestValue/<size>    DigestValue of a string of 1 KB, 200 KB, 1 MB
//	BenchmarkNewResultID           the id
//
// Whether SHA-256 runs on the processor's own instructions depends on the
// build: a program built for amd64 and run under emulation on an arm64
// machine has no access to them, and hashes in software, several times slower.
// The figures in bench_baseline_summary.txt say what this machine's build did.
//
// The allocations are in the table at the top of this file.

func BenchmarkDigestValue(b *testing.B) {
	for _, size := range []struct {
		name  string
		bytes int
	}{{"1KB", 1 << 10}, {"200KB", 200 << 10}, {"1MB", 1 << 20}} {
		text := strings.Repeat("Why is my October invoice for order A-2207 $49 and not $39? ", size.bytes/60+1)[:size.bytes]
		b.Run(size.name, func(b *testing.B) {
			b.SetBytes(int64(size.bytes))
			b.ReportAllocs()
			for b.Loop() {
				_ = DigestValue(text)
			}
		})
	}
}

func BenchmarkNewResultID(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = NewResultID()
	}
}
