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

// GetRepairAttempts returns how many times an operation may show the model its
// own mistake and ask again, after the first attempt.
//
// Repair is not retry: a retry sends the same request and hopes, a repair sends
// the same task plus what was wrong. One is enough for the common case -- prose
// where JSON was asked for, or a missing field -- without turning one failed
// call into an unbounded spend. Set SCHEMAFLUX_REPAIR_ATTEMPTS=0 to disable it.
func GetRepairAttempts() int {
	if raw := os.Getenv("SCHEMAFLUX_REPAIR_ATTEMPTS"); raw != "" {
		if attempts, err := strconv.Atoi(raw); err == nil && attempts >= 0 {
			return attempts
		}
	}
	return 1
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

	if models, known := providerModels[normaliseProviderName(provider)]; known {
		if model := models[intelligence]; model != "" {
			return model
		}
		return models[types.Fast]
	}

	// An unmapped provider gets nothing rather than an OpenAI model ID. Sending
	// "gpt-5.6-luna" to api.deepseek.com produces a 400 that reads as the
	// caller's fault; the README's own `WithProvider("deepseek")` example did
	// exactly that. The empty string is fail-closed: CallLLM turns it into an
	// error naming the variable to set.
	if !isOpenAIProvider(provider) {
		return ""
	}

	switch intelligence {
	case types.Smart:
		return ModelDefaultSmart
	case types.Fast:
		return ModelDefaultFast
	case types.Quick:
		return ModelDefaultQuick
	default:
		return ModelDefaultFast
	}
}

// OpenAI tier defaults, measured against the live API rather than assumed.
//
// The benchmark is .audit/live/bench.py (typed extraction) and bench2.py (a
// proration with a distractor figure), four runs each across gpt-5.6-luna,
// -sol, and -terra. What it found:
//
//	                 typed extraction        proration
//	terra            959ms   4/4 correct     2050ms  4/4 correct
//	sol             1594ms   4/4 correct     3925ms  4/4 correct
//	luna            1680ms   4/4 correct     2094ms  4/4 correct
//
// On these tasks the three models are indistinguishable in accuracy and differ
// only in latency. That supports exactly one assignment: Quick takes terra,
// which is the fastest and gave up nothing to be so.
//
// It does not support a Smart/Fast split between luna and sol. Sol was the
// slowest on the harder task without being more accurate, and assuming slower
// means smarter is the kind of guess this library is trying to stop making.
//
// P-017 asked for a task set that WOULD discriminate, and .audit/live/bench4.py
// is that attempt: recall with planted near-misses, instructions embedded in the
// data, and arithmetic whose greedy reading is wrong — three families chosen
// because each fails differently. Run 2026-08-08:
//
//	                     base set        hard set (two runs)
//	terra            18/18  1109ms    15/15, 15/15   1327ms / 1536ms
//	sol              18/18  1627ms    15/15, 15/15   1889ms / 1967ms
//	luna             18/18  1302ms    14/15, 15/15   1699ms / 1549ms
//
// Luna's single miss did not reproduce. Its 95% interval [0.702, 0.988] overlaps
// the others' [0.796, 1.000], so the difference is sampling noise, not a
// finding — and separating a 0.933 from a 1.000 at this confidence would take
// roughly 115 samples per model, which is 2000 calls to decide a tier.
//
// So the answer to P-017 is its own second option: these models should NOT be
// split on quality. Four benchmarks at three difficulty levels have failed to
// separate them, including on instruction-following under injection, where all
// three refused every embedded order. Smart and Fast both stay on luna; the only
// property that has ever been measurably different is latency, and Quick already
// takes the fastest model. A caller who wants a specific model sets it with
// SCHEMAFLUX_MODEL_*, or per call with the Model pin (TC-005).
const (
	ModelDefaultSmart = "gpt-5.6-luna"
	ModelDefaultFast  = "gpt-5.6-luna"
	ModelDefaultQuick = "gpt-5.6-terra"
)

// GetMaxTokens returns the default output-token ceiling for an intelligence
// tier.
//
// This used to be the only ceiling a caller could get -- tier and ceiling were
// the same knob, so a caller who wanted a long answer from a fast model, or a
// short one from a smart model, had no way to ask (I-09). It is now only the
// default: ops.CallLLM uses this when types.OpOptions.MaxOutputTokens is unset
// (zero) and the caller's value otherwise, so every existing call that never
// touched the option keeps exactly the ceiling it had.
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
