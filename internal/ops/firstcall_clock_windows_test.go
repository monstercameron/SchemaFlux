//go:build windows

package ops

import (
	"syscall"
	"unsafe"
)

// firstCallClockNanos is a monotonic clock fine enough to time one call. Go's
// own clock on Windows advances once per timer tick, about a millisecond, which
// measures a 60-microsecond stage as zero or as a whole tick;
// QueryPerformanceCounter is the hardware counter and resolves tens of
// nanoseconds.
var (
	firstCallQPC       = syscall.NewLazyDLL("kernel32.dll").NewProc("QueryPerformanceCounter")
	firstCallQPF       = syscall.NewLazyDLL("kernel32.dll").NewProc("QueryPerformanceFrequency")
	firstCallQPFHertz  int64
	firstCallQPFLoaded bool
)

func firstCallClockNanos() int64 {
	if !firstCallQPFLoaded {
		var frequency int64
		_, _, _ = firstCallQPF.Call(uintptr(unsafe.Pointer(&frequency)))
		firstCallQPFHertz, firstCallQPFLoaded = frequency, true
	}
	var counter int64
	_, _, _ = firstCallQPC.Call(uintptr(unsafe.Pointer(&counter)))
	return (counter/firstCallQPFHertz)*1_000_000_000 + (counter%firstCallQPFHertz)*1_000_000_000/firstCallQPFHertz
}
