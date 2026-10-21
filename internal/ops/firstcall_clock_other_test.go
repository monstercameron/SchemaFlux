//go:build !windows

package ops

import "time"

var firstCallClockOrigin = time.Now()

// firstCallClockNanos is a monotonic clock for timing one stage. Outside Windows
// the standard clock is fine enough.
func firstCallClockNanos() int64 { return time.Since(firstCallClockOrigin).Nanoseconds() }
