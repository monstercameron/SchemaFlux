package logger

import (
	telemetry "github.com/monstercameron/schemaflux/internal/telemetry"
)

// The logger types and levels are those of internal/telemetry, which this
// package re-exports for the operations that log through it.
type (
	// Logger is telemetry.Logger.
	Logger = telemetry.Logger

	// LogEntry is telemetry.LogEntry.
	LogEntry = telemetry.LogEntry

	// LoggerConfig is telemetry.LoggerConfig.
	LoggerConfig = telemetry.LoggerConfig

	// LogLevel is telemetry.LogLevel.
	LogLevel = telemetry.LogLevel
)

// The log levels, from telemetry.
const (
	DebugLevel = telemetry.DebugLevel
	InfoLevel  = telemetry.InfoLevel
	WarnLevel  = telemetry.WarnLevel
	ErrorLevel = telemetry.ErrorLevel
)

// GetLogger returns the global structured logger instance.
func GetLogger() *Logger {
	return telemetry.GetLogger()
}

// SetLogger replaces the global logger instance.
func SetLogger(l *Logger) {
	telemetry.SetLogger(l)
}

// ConfigureLogger replaces the global logger using the supplied config.
func ConfigureLogger(cfg LoggerConfig) *Logger {
	return telemetry.ConfigureLogger(cfg)
}

// DefaultLoggerConfig returns logger configuration from the environment.
func DefaultLoggerConfig() LoggerConfig {
	return telemetry.DefaultLoggerConfig()
}
