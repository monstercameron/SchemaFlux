package debug

import (
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/types"
)

// Test helper type
type Person struct {
	Name string
	Age  int
}

func TestDebug(t *testing.T) {
	// Save original state
	originalDebug := config.GetDebugMode()
	originalMetrics := config.IsMetricsEnabled()
	config.Init("test-api-key") // Ensure config is initialized

	// Defer restoration
	defer func() {
		config.SetDebugMode(originalDebug)
		config.SetMetricsEnabled(originalMetrics)
	}()

	// Test setting debug mode
	config.SetDebugMode(true)
	if !config.GetDebugMode() {
		t.Error("Expected debug mode to be enabled")
	}

	// Test disabling debug mode
	config.SetDebugMode(false)
	if config.GetDebugMode() {
		t.Error("Expected debug mode to be disabled")
	}

	// Test setting metrics
	config.SetMetricsEnabled(true)
	if !config.IsMetricsEnabled() {
		t.Error("Expected metrics to be enabled")
	}

	// Test disabling metrics
	config.SetMetricsEnabled(false)
	if config.IsMetricsEnabled() {
		t.Error("Expected metrics to be disabled")
	}
}

func TestGetDebugInfo(t *testing.T) {
	info := GetDebugInfo()

	if info.RequestID == "" {
		t.Error("Expected RequestID to be set")
	}

	if info.MemoryUsage.Allocated == 0 {
		t.Error("Expected memory usage to be tracked")
	}

	if len(info.StackTrace) == 0 {
		t.Error("Expected stack trace to be captured")
	}
}

func TestTraceOperation(t *testing.T) {
	trace := TraceOperation("TestOp", "test input")

	if trace.Operation != "TestOp" {
		t.Errorf("Expected operation 'TestOp', got %s", trace.Operation)
	}

	if trace.RequestID == "" {
		t.Error("Expected RequestID to be set")
	}

	if trace.Input != "test input" {
		t.Errorf("Expected input 'test input', got %v", trace.Input)
	}

	// Complete the trace
	trace.Complete("output", nil)

	if trace.Output != "output" {
		t.Errorf("Expected output 'output', got %v", trace.Output)
	}

	if trace.EndTime.IsZero() {
		t.Error("Expected EndTime to be set")
	}
}

func TestValidateInput(t *testing.T) {
	tests := []struct {
		name      string
		input     any
		operation string
		wantErr   bool
	}{
		{
			name:      "nil input",
			input:     nil,
			operation: "test",
			wantErr:   true,
		},
		{
			name:      "empty string",
			input:     "",
			operation: "test",
			wantErr:   true,
		},
		{
			name:      "valid string",
			input:     "valid input",
			operation: "test",
			wantErr:   false,
		},
		{
			name:      "empty slice",
			input:     []string{},
			operation: "test",
			wantErr:   true,
		},
		{
			name:      "valid slice",
			input:     []string{"item1", "item2"},
			operation: "test",
			wantErr:   false,
		},
		{
			name:      "very long string",
			input:     strings.Repeat("a", 100001),
			operation: "test",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateInput(tt.input, tt.operation)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateInput() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestDumpOperation(t *testing.T) {
	opts := types.OpOptions{
		Steering:     "test steering",
		Threshold:    0.8,
		Mode:         types.TransformMode,
		Intelligence: types.Smart,
	}

	dump := DumpOperation("TestOp", "input", "output", nil, opts)

	if !strings.Contains(dump, "TestOp") {
		t.Error("Expected dump to contain operation name")
	}

	if !strings.Contains(dump, "RequestID") {
		t.Error("Expected dump to contain request ID")
	}

	if !strings.Contains(dump, "memory_stats") {
		t.Error("Expected dump to contain memory stats")
	}
}

func TestBenchmarkOperation(t *testing.T) {
	result := BenchmarkOperation("TestBenchmark", func() error {
		// Simulate some work
		time.Sleep(10 * time.Millisecond)
		return nil
	})

	if result.Operation != "TestBenchmark" {
		t.Errorf("Expected operation 'TestBenchmark', got %s", result.Operation)
	}

	if result.Duration < 10*time.Millisecond {
		t.Errorf("Expected duration >= 10ms, got %v", result.Duration)
	}

	if result.Error != nil {
		t.Errorf("Expected no error, got %v", result.Error)
	}

	// Test string representation
	str := result.String()
	if !strings.Contains(str, "SUCCESS") {
		t.Error("Expected string to contain 'SUCCESS'")
	}
	if !strings.Contains(str, "TestBenchmark") {
		t.Error("Expected string to contain operation name")
	}
}

func TestGetMemoryStats(t *testing.T) {
	stats := getMemoryStats()

	if stats.Allocated == 0 {
		t.Error("Expected allocated memory to be > 0")
	}

	if stats.System == 0 {
		t.Error("Expected system memory to be > 0")
	}
}

func TestGetStackTrace(t *testing.T) {
	trace := getStackTrace(0)

	if len(trace) == 0 {
		t.Error("Expected stack trace to have entries")
	}

	// Check that trace contains this test function
	found := false
	for _, entry := range trace {
		if strings.Contains(entry, "TestGetStackTrace") {
			found = true
			break
		}
	}

	if !found {
		t.Error("Expected stack trace to contain TestGetStackTrace")
	}
}

func TestFormatForLog(t *testing.T) {
	tests := []struct {
		name   string
		input  any
		maxLen int
	}{
		{
			name:   "nil value",
			input:  nil,
			maxLen: 10,
		},
		{
			name:   "short string",
			input:  "test",
			maxLen: 10,
		},
		{
			name:   "long string",
			input:  strings.Repeat("a", 300),
			maxLen: 210, // Should be truncated
		},
		{
			name:   "struct",
			input:  Person{Name: "John", Age: 30},
			maxLen: 500,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatForLog(tt.input)
			if len(result) > tt.maxLen {
				t.Errorf("formatForLog() result too long: %d > %d", len(result), tt.maxLen)
			}
		})
	}
}
