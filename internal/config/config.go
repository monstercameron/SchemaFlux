package config

import (
	"os"
	"sync/atomic"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

var (
	// The two switches the setters turn on. They are read on every call, so
	// they are atomics: a read behind a lock is a shared cache line every call
	// on every core touches.
	debugMode      atomic.Bool
	metricsEnabled atomic.Bool
)

// GetTimeout returns the default timeout for operations: SCHEMAFLUX_TIMEOUT when
// it names a positive duration, and 30 seconds otherwise. A value that is not a
// positive duration is reported once, naming the variable, and the default
// applies; TimeoutFromEnv is the form that returns the refusal as an error.
//
// The variable is read once, when this is first called (ReloadEnvironment).
func GetTimeout() time.Duration {
	return timeoutEnvironment.get(func() time.Duration {
		timeout, err := TimeoutFromEnv()
		if err != nil {
			reportEnvProblem("SCHEMAFLUX_TIMEOUT", os.Getenv("SCHEMAFLUX_TIMEOUT"), "a positive duration such as 30s")
			return defaultTimeout
		}
		return timeout
	})
}

// GetLLMMaxRetries returns the retry budget for LLM calls. The variable is
// read once (ReloadEnvironment).
func GetLLMMaxRetries() int {
	return retriesEnvironment.get(func() int { return EnvInt("SCHEMAFLUX_LLM_MAX_RETRIES", 3, 0) })
}

// GetRepairAttempts returns how many times an operation may show the model its
// own mistake and ask again, after the first attempt.
//
// Repair is not retry: a retry sends the same request and hopes, a repair sends
// the same task plus what was wrong. One is enough for the common case -- prose
// where JSON was asked for, or a missing field -- without turning one failed
// call into an unbounded spend. Set SCHEMAFLUX_REPAIR_ATTEMPTS=0 to disable it.
func GetRepairAttempts() int {
	return repairEnvironment.get(func() int { return EnvInt("SCHEMAFLUX_REPAIR_ATTEMPTS", 1, 0) })
}

// GetLLMRetryBackoff returns the base backoff for LLM retries. The variable is
// read once (ReloadEnvironment).
func GetLLMRetryBackoff() time.Duration {
	return backoffEnvironment.get(func() time.Duration {
		return EnvPositiveDuration("SCHEMAFLUX_LLM_RETRY_BACKOFF", 500*time.Millisecond)
	})
}

// GetDebugMode returns whether debug mode is enabled, by SetDebugMode or by
// SCHEMAFLUX_DEBUG=true. The variable is read once (ReloadEnvironment).
func GetDebugMode() bool {
	if debugMode.Load() {
		return true
	}
	return debugEnvironment.get(func() bool { return EnvBool("SCHEMAFLUX_DEBUG") })
}

// SetDebugMode sets the debug mode
func SetDebugMode(enabled bool) {
	debugMode.Store(enabled)
}

// IsMetricsEnabled returns whether metrics collection is enabled, by
// SetMetricsEnabled or by SCHEMAFLUX_METRICS=true. It is asked on every call,
// so with nothing set it is two atomic loads and no system call; the variable
// is read once (ReloadEnvironment).
func IsMetricsEnabled() bool {
	if metricsEnabled.Load() {
		return true
	}
	return metricsEnvironment.get(func() bool { return EnvBool("SCHEMAFLUX_METRICS") })
}

// SetMetricsEnabled sets whether metrics collection is enabled
func SetMetricsEnabled(enabled bool) {
	metricsEnabled.Store(enabled)
}

// GetModel returns the appropriate model based on intelligence level. The
// SCHEMAFLUX_MODEL variables are read once (ReloadEnvironment).
func GetModel(intelligence types.Speed, provider string) string {
	overrides := modelEnvironment.get(func() modelOverrides {
		return modelOverrides{
			all:   os.Getenv("SCHEMAFLUX_MODEL"),
			smart: os.Getenv("SCHEMAFLUX_MODEL_SMART"),
			fast:  os.Getenv("SCHEMAFLUX_MODEL_FAST"),
			quick: os.Getenv("SCHEMAFLUX_MODEL_QUICK"),
		}
	})

	// Check for global model override via environment variable
	if overrides.all != "" {
		return overrides.all
	}

	// Check for intelligence-level specific overrides
	var envLevelModel string
	switch intelligence {
	case types.Smart:
		envLevelModel = overrides.smart
	case types.Fast:
		envLevelModel = overrides.fast
	case types.Quick:
		envLevelModel = overrides.quick
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

// The OpenAI tier defaults. All three tiers name one model, gpt-6-luna: no
// comparison the library has run separates the models of a family on accuracy,
// and a Smart/Fast split between them would assume that slower means smarter.
// A caller who wants a specific model names it with SCHEMAFLUX_MODEL_*, with
// Client.WithModel, or with the Model pin on a call. The benchmark scripts that
// were run to reach this are under .audit/live/.
const (
	ModelDefaultSmart = "gpt-6-luna"
	ModelDefaultFast  = "gpt-6-luna"
	ModelDefaultQuick = "gpt-6-luna"
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
