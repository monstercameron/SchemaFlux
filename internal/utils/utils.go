package utils

import (
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/monstercameron/schemaflux/telemetry"
)

var requestIDCounter uint64

// GenerateRequestID generates a unique request ID for tracing
func GenerateRequestID() string {
	// Use atomic counter to ensure uniqueness even when called in quick succession
	counter := atomic.AddUint64(&requestIDCounter, 1)
	return fmt.Sprintf("%d-%d-%d", time.Now().UnixNano(), os.Getpid(), counter)
}

// Min returns the minimum of two integers
func Min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// RecordMetric records a metric through the shared telemetry registry.
func RecordMetric(name string, value int64, tags map[string]string) {
	telemetry.RecordMetric(name, value, tags)
}
