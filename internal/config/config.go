package config

import (
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

var (
	debugMode      bool
	traceEnabled   bool
	metricsEnabled bool
	apiKey         string
	mu             sync.RWMutex
)

// Init initializes the configuration with an API key
func Init(key string) {
	mu.Lock()
	defer mu.Unlock()

	apiKey = key
	if apiKey == "" {
		apiKey = os.Getenv("SCHEMAFLUX_API_KEY")
	}
}

// GetAPIKey returns the configured API key
func GetAPIKey() string {
	mu.RLock()
	defer mu.RUnlock()
	if apiKey != "" {
		return apiKey
	}
	return os.Getenv("SCHEMAFLUX_API_KEY")
}

// GetTimeout returns the default timeout for operations
func GetTimeout() time.Duration {
	if timeoutStr := os.Getenv("SCHEMAFLUX_TIMEOUT"); timeoutStr != "" {
		if duration, err := time.ParseDuration(timeoutStr); err == nil {
			return duration
		}
	}
	return 30 * time.Second
}

// GetLLMMaxRetries returns the retry budget for LLM calls.
func GetLLMMaxRetries() int {
	if raw := os.Getenv("SCHEMAFLUX_LLM_MAX_RETRIES"); raw != "" {
		if retries, err := strconv.Atoi(raw); err == nil && retries >= 0 {
			return retries
		}
	}
	return 3
}

// GetLLMRetryBackoff returns the base backoff for LLM retries.
func GetLLMRetryBackoff() time.Duration {
	if raw := os.Getenv("SCHEMAFLUX_LLM_RETRY_BACKOFF"); raw != "" {
		if delay, err := time.ParseDuration(raw); err == nil && delay > 0 {
			return delay
		}
	}
	return 500 * time.Millisecond
}

// GetDebugMode returns whether debug mode is enabled
func GetDebugMode() bool {
	mu.RLock()
	defer mu.RUnlock()
	if debugMode {
		return true
	}
	return os.Getenv("SCHEMAFLUX_DEBUG") == "true"
}

// SetDebugMode sets the debug mode
func SetDebugMode(enabled bool) {
	mu.Lock()
	defer mu.Unlock()
	debugMode = enabled
}

// GetTraceEnabled returns whether tracing is enabled
func GetTraceEnabled() bool {
	mu.RLock()
	defer mu.RUnlock()
	if traceEnabled {
		return true
	}
	return envEnabled("SCHEMAFLUX_TRACE") || envEnabled("SCHEMAFLUX_ENABLE_TRACING")
}

// SetTraceEnabled sets tracing mode
func SetTraceEnabled(enabled bool) {
	mu.Lock()
	defer mu.Unlock()
	traceEnabled = enabled
}

// IsMetricsEnabled returns whether metrics collection is enabled
func IsMetricsEnabled() bool {
	mu.RLock()
	defer mu.RUnlock()
	if metricsEnabled {
		return true
	}
	return os.Getenv("SCHEMAFLUX_METRICS") == "true"
}

// SetMetricsEnabled sets whether metrics collection is enabled
func SetMetricsEnabled(enabled bool) {
	mu.Lock()
	defer mu.Unlock()
	metricsEnabled = enabled
}

func envEnabled(keys ...string) bool {
	for _, key := range keys {
		switch os.Getenv(key) {
		case "true", "1":
			return true
		}
	}
	return false
}

// GetModel returns the appropriate model based on intelligence level
func GetModel(intelligence types.Speed, provider string) string {
	// Check for global model override via environment variable
	if envModel := os.Getenv("SCHEMAFLUX_MODEL"); envModel != "" {
		return envModel
	}

	// Check for intelligence-level specific overrides
	var envLevelModel string
	switch intelligence {
	case types.Smart:
		envLevelModel = os.Getenv("SCHEMAFLUX_MODEL_SMART")
	case types.Fast:
		envLevelModel = os.Getenv("SCHEMAFLUX_MODEL_FAST")
	case types.Quick:
		envLevelModel = os.Getenv("SCHEMAFLUX_MODEL_QUICK")
	}

	if envLevelModel != "" {
		return envLevelModel
	}

	// Check global provider
	if provider == "openrouter" {
		switch intelligence {
		case types.Smart:
			return "openai/gpt-4o"
		case types.Fast:
			return "openai/gpt-4o-mini"
		case types.Quick:
			return "openai/gpt-4o-mini"
		default:
			return "openai/gpt-4o-mini"
		}
	}

	if provider == "cerebras" {
		switch intelligence {
		case types.Smart:
			return "llama-3.3-70b"
		case types.Fast:
			return "llama3.1-8b"
		case types.Quick:
			return "llama3.1-8b"
		default:
			return "llama3.1-8b"
		}
	}

	if provider == "anthropic" {
		switch intelligence {
		case types.Smart:
			return "claude-3-5-sonnet-20240620"
		case types.Fast:
			return "claude-3-haiku-20240307"
		case types.Quick:
			return "claude-3-haiku-20240307"
		default:
			return "claude-3-haiku-20240307"
		}
	}

	switch intelligence {
	case types.Smart:
		return "gpt-5.4"
	case types.Fast:
		return "gpt-5-mini"
	case types.Quick:
		return "gpt-5-nano"
	default:
		return "gpt-5-mini"
	}
}

// GetMaxTokens returns the maximum token limit based on intelligence level
func GetMaxTokens(intelligence types.Speed) int {
	switch intelligence {
	case types.Smart:
		return 4000
	case types.Fast:
		return 2000
	case types.Quick:
		return 1000
	default:
		return 2000
	}
}

// GetTemperature returns the appropriate temperature setting based on mode
func GetTemperature(mode types.Mode) float32 {
	switch mode {
	case types.Strict:
		return 0.1 // Very deterministic
	case types.TransformMode:
		return 0.3 // Balanced
	case types.Creative:
		return 0.7 // More creative/varied
	default:
		return 0.3
	}
}
