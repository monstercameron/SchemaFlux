package telemetry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoggerCapturesStructuredEntries(t *testing.T) {
	log := NewLoggerWithConfig(LoggerConfig{
		Level:         DebugLevel,
		Format:        "json",
		Capture:       true,
		BufferSize:    10,
		DisableStderr: true,
	})
	t.Cleanup(func() { _ = log.Close() })

	log.WithFields(map[string]any{
		"requestID": "req-1",
		"provider":  "openai",
	}).Info("completed", "operation", "extract", "tokens", 42)

	entries := log.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(entries))
	}

	entry := entries[0]
	if entry.Message != "completed" {
		t.Fatalf("expected message %q, got %q", "completed", entry.Message)
	}
	if entry.Level != "INFO" {
		t.Fatalf("expected INFO level, got %q", entry.Level)
	}
	if entry.Attributes["requestID"] != "req-1" {
		t.Fatalf("expected requestID attr, got %#v", entry.Attributes)
	}
	if entry.Attributes["operation"] != "extract" {
		t.Fatalf("expected operation attr, got %#v", entry.Attributes)
	}
	if entry.Attributes["tokens"] != int64(42) {
		t.Fatalf("expected integer tokens attr, got %#v", entry.Attributes["tokens"])
	}
}

func TestLoggerRespectsBufferLimit(t *testing.T) {
	log := NewLoggerWithConfig(LoggerConfig{
		Level:         InfoLevel,
		Capture:       true,
		BufferSize:    2,
		DisableStderr: true,
	})
	t.Cleanup(func() { _ = log.Close() })

	log.Info("first")
	log.Info("second")
	log.Info("third")

	entries := log.Entries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Message != "second" || entries[1].Message != "third" {
		t.Fatalf("unexpected buffered messages: %#v", entries)
	}

	log.ResetEntries()
	if len(log.Entries()) != 0 {
		t.Fatal("expected entries to be cleared")
	}
}

func TestLoggerWritesToFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schemaflux.log")

	log := NewLoggerWithConfig(LoggerConfig{
		Level:         InfoLevel,
		Format:        "text",
		Capture:       false,
		FilePath:      path,
		DisableStderr: true,
	})
	log.Info("file message", "component", "test")
	if err := log.Close(); err != nil {
		t.Fatalf("failed to close logger: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}
	if !strings.Contains(string(data), "file message") {
		t.Fatalf("expected log file to contain message, got %q", string(data))
	}
}

func TestDefaultLoggerConfigReadsEnvironment(t *testing.T) {
	t.Setenv("SCHEMAFLUX_LOG_LEVEL", "debug")
	t.Setenv("SCHEMAFLUX_LOG_FORMAT", "json")
	t.Setenv("SCHEMAFLUX_LOG_BUFFER", "25")
	t.Setenv("SCHEMAFLUX_LOG_SOURCE", "true")
	t.Setenv("SCHEMAFLUX_LOG_DISABLE_STDERR", "1")

	cfg := DefaultLoggerConfig()
	if cfg.Level != DebugLevel {
		t.Fatalf("expected debug level, got %v", cfg.Level)
	}
	if cfg.Format != "json" {
		t.Fatalf("expected json format, got %q", cfg.Format)
	}
	if cfg.BufferSize != 25 {
		t.Fatalf("expected buffer size 25, got %d", cfg.BufferSize)
	}
	if !cfg.AddSource {
		t.Fatal("expected AddSource to be enabled")
	}
	if !cfg.DisableStderr {
		t.Fatal("expected stderr to be disabled")
	}
}
