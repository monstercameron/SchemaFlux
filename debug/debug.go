// Package debug - Debugging utilities and diagnostics
package debug

import (
	"encoding/json"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"time"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/internal/utils"
)

// Debug enables debug mode for detailed operation tracking
func Debug(enabled bool) {
	config.SetDebugMode(enabled)
}

// GetDebugInfo returns current debug information
func GetDebugInfo() types.DebugInfo {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	return types.DebugInfo{
		RequestID: utils.GenerateRequestID(),
		MemoryUsage: types.MemoryStats{
			Allocated:      m.Alloc,
			TotalAllocated: m.TotalAlloc,
			System:         m.Sys,
			NumGC:          m.NumGC,
		},
		StackTrace: getStackTrace(2),
	}
}

// TraceOperation creates a trace for an operation
func TraceOperation(operation string, input any) *OperationTrace {
	return &OperationTrace{
		Operation: operation,
		RequestID: utils.GenerateRequestID(),
		StartTime: time.Now(),
		Input:     input,
	}
}

// OperationTrace tracks a single operation execution
type OperationTrace struct {
	Operation string
	RequestID string
	StartTime time.Time
	EndTime   time.Time
	Input     any
	Output    any
	Error     error
	LLMCalls  []types.LLMCallInfo
}

// Complete marks the operation as complete
func (t *OperationTrace) Complete(output any, err error) {
	t.EndTime = time.Now()
	t.Output = output
	t.Error = err

	if config.GetTraceEnabled() {
		t.log()
	}
}

// log writes the trace to the logger
func (t *OperationTrace) log() {
	duration := t.EndTime.Sub(t.StartTime)

	fields := []any{
		"requestID", t.RequestID,
		"operation", t.Operation,
		"duration", duration,
	}

	if t.Error != nil {
		logger.GetLogger().Error("Operation failed",
			append(fields, "error", t.Error)...,
		)
	} else {
		logger.GetLogger().Info("Operation completed",
			fields...,
		)
	}

	if config.GetDebugMode() {
		logger.GetLogger().Debug("Operation trace",
			"requestID", t.RequestID,
			"input", formatForLog(t.Input),
			"output", formatForLog(t.Output),
			"llmCalls", len(t.LLMCalls),
		)
	}
}

// ValidateInput performs input validation for operations
func ValidateInput(input any, operation string) error {
	if input == nil {
		return fmt.Errorf("%s: input cannot be nil", operation)
	}

	v := reflect.ValueOf(input)

	// Check for zero values
	if v.IsZero() {
		logger.GetLogger().Warn("Zero value input",
			"operation", operation,
			"type", v.Type().String(),
		)
	}

	// Validate string inputs
	if v.Kind() == reflect.String {
		s := v.String()
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("%s: input string cannot be empty", operation)
		}

		// Check for potentially malicious content
		if err := sanitizeString(s); err != nil {
			return fmt.Errorf("%s: input validation failed: %w", operation, err)
		}
	}

	// Validate slice inputs
	if v.Kind() == reflect.Slice {
		if v.Len() == 0 {
			return fmt.Errorf("%s: input slice cannot be empty", operation)
		}

		// Check maximum size
		const maxSliceSize = 10000
		if v.Len() > maxSliceSize {
			return fmt.Errorf("%s: input slice too large (%d > %d)", operation, v.Len(), maxSliceSize)
		}
	}

	return nil
}

// sanitizeString checks for potentially malicious content
func sanitizeString(s string) error {
	// Check for excessive length
	const maxStringLength = 100000
	if len(s) > maxStringLength {
		return fmt.Errorf("string too long (%d > %d)", len(s), maxStringLength)
	}

	// Check for control characters
	for _, r := range s {
		if r < 32 && r != '\n' && r != '\r' && r != '\t' {
			return fmt.Errorf("string contains control characters")
		}
	}

	// Check for potential injection patterns (basic)
	dangerousPatterns := []string{
		"<script",
		"javascript:",
		"data:text/html",
		"vbscript:",
		"onload=",
		"onerror=",
	}

	lower := strings.ToLower(s)
	for _, pattern := range dangerousPatterns {
		if strings.Contains(lower, pattern) {
			logger.GetLogger().Warn("Potentially dangerous pattern detected",
				"pattern", pattern,
				"input", s[:min(len(s), 50)],
			)
		}
	}

	return nil
}

// DumpOperation creates a detailed dump of an operation for debugging
func DumpOperation(op string, input any, output any, err error, opts types.OpOptions) string {
	dump := OperationDump{
		Operation:   op,
		RequestID:   utils.GenerateRequestID(),
		Timestamp:   time.Now(),
		Input:       input,
		Output:      output,
		Error:       err,
		Options:     opts,
		MemoryStats: getMemoryStats(),
		Goroutines:  runtime.NumGoroutine(),
		StackTrace:  getStackTrace(2),
	}

	data, _ := json.MarshalIndent(dump, "", "  ")
	return string(data)
}

// OperationDump contains complete operation information
type OperationDump struct {
	Operation   string            `json:"operation"`
	RequestID   string            `json:"request_id"`
	Timestamp   time.Time         `json:"timestamp"`
	Input       any               `json:"input"`
	Output      any               `json:"output"`
	Error       error             `json:"error,omitempty"`
	Options     types.OpOptions   `json:"options"`
	MemoryStats types.MemoryStats `json:"memory_stats"`
	Goroutines  int               `json:"goroutines"`
	StackTrace  []string          `json:"stack_trace"`
}

// getMemoryStats returns current memory statistics
func getMemoryStats() types.MemoryStats {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return types.MemoryStats{
		Allocated:      m.Alloc,
		TotalAllocated: m.TotalAlloc,
		System:         m.Sys,
		NumGC:          m.NumGC,
	}
}

// getStackTrace returns the current stack trace
func getStackTrace(skip int) []string {
	var trace []string
	for i := skip; i < 20; i++ {
		pc, file, line, ok := runtime.Caller(i)
		if !ok {
			break
		}

		fn := runtime.FuncForPC(pc)
		if fn == nil {
			continue
		}

		// Skip runtime functions
		name := fn.Name()
		if strings.HasPrefix(name, "runtime.") {
			continue
		}

		// Format: function_name (file:line)
		trace = append(trace, fmt.Sprintf("%s (%s:%d)", name, file, line))
	}
	return trace
}

// formatForLog formats a value for logging (truncates if too long)
func formatForLog(v any) string {
	if v == nil {
		return "nil"
	}

	// Handle special types
	switch val := v.(type) {
	case string:
		if len(val) > 200 {
			return val[:200] + "..."
		}
		return val
	case []byte:
		if len(val) > 200 {
			return string(val[:200]) + "..."
		}
		return string(val)
	case error:
		return val.Error()
	}

	// Try JSON for complex types
	if data, err := json.Marshal(v); err == nil {
		if len(data) > 500 {
			return string(data[:500]) + "..."
		}
		return string(data)
	}

	// Fallback to Sprint
	s := fmt.Sprint(v)
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

// BenchmarkOperation measures the performance of an operation
func BenchmarkOperation(name string, fn func() error) BenchmarkResult {
	result := BenchmarkResult{
		Operation: name,
		StartTime: time.Now(),
	}

	// Memory before
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)

	// Run operation
	err := fn()

	// Memory after
	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)

	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(result.StartTime)
	result.Error = err
	result.MemoryUsed = memAfter.Alloc - memBefore.Alloc
	result.GCRuns = memAfter.NumGC - memBefore.NumGC

	return result
}

// BenchmarkResult contains performance metrics for an operation
type BenchmarkResult struct {
	Operation  string        `json:"operation"`
	StartTime  time.Time     `json:"start_time"`
	EndTime    time.Time     `json:"end_time"`
	Duration   time.Duration `json:"duration"`
	Error      error         `json:"error,omitempty"`
	MemoryUsed uint64        `json:"memory_used"`
	GCRuns     uint32        `json:"gc_runs"`
}

// String returns a formatted string representation of the benchmark result
func (b BenchmarkResult) String() string {
	status := "SUCCESS"
	if b.Error != nil {
		status = "FAILED"
	}

	return fmt.Sprintf(
		"[%s] %s - Duration: %v, Memory: %d bytes, GC: %d runs",
		status,
		b.Operation,
		b.Duration,
		b.MemoryUsed,
		b.GCRuns,
	)
}
