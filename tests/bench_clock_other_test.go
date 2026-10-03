//go:build !windows

package tests

import "time"

var benchClockOrigin = time.Now()

// benchClockNanos is a monotonic clock for timing one call. Outside Windows the
// standard clock is fine enough; see bench_clock_windows_test.go.
func benchClockNanos() int64 { return time.Since(benchClockOrigin).Nanoseconds() }
