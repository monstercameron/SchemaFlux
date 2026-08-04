package logger

import (
	"github.com/monstercameron/schemaflux/internal/config"
	telemetry "github.com/monstercameron/schemaflux/internal/telemetry"
)

type (
	Logger       = telemetry.Logger
	LogEntry     = telemetry.LogEntry
	LoggerConfig = telemetry.LoggerConfig
	LogLevel     = telemetry.LogLevel
)

const (
	DebugLevel = telemetry.DebugLevel
	InfoLevel  = telemetry.InfoLevel
	WarnLevel  = telemetry.WarnLevel
	ErrorLevel = telemetry.ErrorLevel
	FatalLevel = telemetry.FatalLevel
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

// GetDebugMode returns whether debug mode is enabled.
func GetDebugMode() bool {
	return config.GetDebugMode()
}

// IsMetricsEnabled returns whether metrics collection is enabled.
func IsMetricsEnabled() bool {
	return config.IsMetricsEnabled()
}
