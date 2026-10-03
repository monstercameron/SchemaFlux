//go:build windows

package tests

import (
	"syscall"
	"unsafe"
)

// benchClockNanos is a monotonic clock fine enough to time one call.
//
// Go's own clock on Windows reads the system's interrupt time, which advances
// once per timer tick (about a millisecond on this machine): a call that takes
// 60 microseconds is measured as zero or as a whole tick. That is harmless to a
// benchmark, which divides a long loop by its count, and useless to the first
// call of a process, which is one sample. QueryPerformanceCounter is the
// hardware counter and resolves tens of nanoseconds.
var (
	benchQPC       = syscall.NewLazyDLL("kernel32.dll").NewProc("QueryPerformanceCounter")
	benchQPF       = syscall.NewLazyDLL("kernel32.dll").NewProc("QueryPerformanceFrequency")
	benchQPFHertz  int64
	benchQPFLoaded bool
)

func benchClockNanos() int64 {
	if !benchQPFLoaded {
		var frequency int64
		_, _, _ = benchQPF.Call(uintptr(unsafe.Pointer(&frequency)))
		benchQPFHertz, benchQPFLoaded = frequency, true
	}
	var counter int64
	_, _, _ = benchQPC.Call(uintptr(unsafe.Pointer(&counter)))
	// Split so that counter * 1e9 cannot overflow after weeks of uptime.
	return (counter/benchQPFHertz)*1_000_000_000 + (counter%benchQPFHertz)*1_000_000_000/benchQPFHertz
}
