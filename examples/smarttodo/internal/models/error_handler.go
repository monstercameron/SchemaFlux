// error_handler.go - Centralized error handling and recovery
package models

import (
	"fmt"
	"runtime/debug"
	"strings"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
)

// ErrorSeverity defines the severity of errors
type ErrorSeverity int

const (
	ErrorInfo ErrorSeverity = iota
	ErrorWarning
	ErrorCritical
)

// AppError represents an application error with context
type AppError struct {
	Message   string
	Severity  ErrorSeverity
	Context   string
	Timestamp time.Time
	Stack     string
}

// Error implements the error interface
func (e AppError) Error() string {
	return fmt.Sprintf("[%s] %s: %s", e.Severity.String(), e.Context, e.Message)
}

// String returns the severity as a string
func (s ErrorSeverity) String() string {
	switch s {
	case ErrorInfo:
		return "INFO"
	case ErrorWarning:
		return "WARNING"
	case ErrorCritical:
		return "CRITICAL"
	default:
		return "UNKNOWN"
	}
}

// NewAppError creates a new application error with stack trace
func NewAppError(message string, severity ErrorSeverity, context string) AppError {
	return AppError{
		Message:   message,
		Severity:  severity,
		Context:   context,
		Timestamp: time.Now(),
		Stack:     string(debug.Stack()),
	}
}

// HandleError provides centralized error handling with recovery
func HandleError(err error, context string) error {
	if err == nil {
		return nil
	}

	// Log the error
	schemaflux.GetLogger().Error("Error in operation", "context", context, "error", err)

	// Check for specific error types and provide recovery strategies
	switch {
	case strings.Contains(err.Error(), "API key"):
		return NewAppError(
			"API key issue detected. Please update your key with Ctrl+K",
			ErrorWarning,
			context,
		)
	case strings.Contains(err.Error(), "rate limit"):
		return NewAppError(
			"Rate limit reached. Please wait a moment before trying again",
			ErrorWarning,
			context,
		)
	case strings.Contains(err.Error(), "network"):
		return NewAppError(
			"Network issue detected. Please check your connection",
			ErrorWarning,
			context,
		)
	case strings.Contains(err.Error(), "database"):
		return NewAppError(
			"Database error. Your data is safe but please restart if issues persist",
			ErrorCritical,
			context,
		)
	default:
		return NewAppError(
			err.Error(),
			ErrorInfo,
			context,
		)
	}
}

// RecoverFromPanic recovers from panics and converts them to errors
func RecoverFromPanic(context string) error {
	if r := recover(); r != nil {
		_ = fmt.Errorf("panic recovered: %v", r)
		schemaflux.GetLogger().Error("PANIC recovered", "context", context, "panic", r, "stack", string(debug.Stack()))
		return NewAppError(
			fmt.Sprintf("Unexpected error: %v", r),
			ErrorCritical,
			context,
		)
	}
	return nil
}

// RetryWithBackoff retries an operation with exponential backoff
func RetryWithBackoff(operation func() error, maxAttempts int, context string) error {
	var lastErr error
	backoff := time.Second

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := operation(); err == nil {
			return nil
		} else {
			lastErr = err
			if attempt < maxAttempts {
				schemaflux.GetLogger().Warn("Retry attempt failed", "attempt", attempt, "maxAttempts", maxAttempts, "context", context, "error", err, "backoff", backoff)
				time.Sleep(backoff)
				backoff *= 2
				if backoff > 30*time.Second {
					backoff = 30 * time.Second
				}
			}
		}
	}

	return HandleError(
		fmt.Errorf("operation failed after %d attempts: %w", maxAttempts, lastErr),
		context,
	)
}

// SafeExecute executes a function with panic recovery
func SafeExecute(fn func() error, context string) error {
	defer func() {
		if err := RecoverFromPanic(context); err != nil {
			schemaflux.GetLogger().Error("Recovered from panic in operation", "context", context, "error", err)
		}
	}()
	return fn()
}
