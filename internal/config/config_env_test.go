package config

import (
	"testing"
	"time"
)

// Init/GetAPIKey: an explicit key wins over the environment, and an empty
// explicit key falls back to SCHEMAFLUX_API_KEY -- both directions of the
// precedence Init documents.
func TestInitAndGetAPIKeyPrecedence(t *testing.T) {
	t.Setenv("SCHEMAFLUX_API_KEY", "env-key")

	Init("explicit-key")
	if got := GetAPIKey(); got != "explicit-key" {
		t.Errorf("GetAPIKey() = %q, want the explicit key", got)
	}

	Init("")
	if got := GetAPIKey(); got != "env-key" {
		t.Errorf("GetAPIKey() = %q, want the env fallback when Init is given an empty key", got)
	}
}

// GetAPIKey also falls back to the environment directly (without Init ever
// being called with a key) whenever the package-level key is empty.
func TestGetAPIKeyFallsBackToEnvWithoutInit(t *testing.T) {
	Init("") // clears any key a previous test left behind
	t.Setenv("SCHEMAFLUX_API_KEY", "from-env-directly")
	if got := GetAPIKey(); got != "from-env-directly" {
		t.Errorf("GetAPIKey() = %q, want from-env-directly", got)
	}
}

func TestGetTimeoutParsesValidDurationAndFallsBackOtherwise(t *testing.T) {
	t.Setenv("SCHEMAFLUX_TIMEOUT", "45s")
	if got := GetTimeout(); got != 45*time.Second {
		t.Errorf("GetTimeout() = %v, want 45s", got)
	}

	t.Setenv("SCHEMAFLUX_TIMEOUT", "not-a-duration")
	if got := GetTimeout(); got != 30*time.Second {
		t.Errorf("GetTimeout() = %v, want the 30s default for an unparseable value", got)
	}

	t.Setenv("SCHEMAFLUX_TIMEOUT", "")
	if got := GetTimeout(); got != 30*time.Second {
		t.Errorf("GetTimeout() = %v, want the 30s default when unset", got)
	}
}

func TestGetLLMMaxRetriesParsesAndRefusesNegative(t *testing.T) {
	t.Setenv("SCHEMAFLUX_LLM_MAX_RETRIES", "5")
	if got := GetLLMMaxRetries(); got != 5 {
		t.Errorf("GetLLMMaxRetries() = %d, want 5", got)
	}

	// The refusal: a negative retry budget is nonsensical and must not be
	// honoured -- the default applies instead.
	t.Setenv("SCHEMAFLUX_LLM_MAX_RETRIES", "-1")
	if got := GetLLMMaxRetries(); got != 3 {
		t.Errorf("GetLLMMaxRetries() = %d, want the default 3 for a negative value", got)
	}

	t.Setenv("SCHEMAFLUX_LLM_MAX_RETRIES", "not-a-number")
	if got := GetLLMMaxRetries(); got != 3 {
		t.Errorf("GetLLMMaxRetries() = %d, want the default 3 for an unparseable value", got)
	}

	t.Setenv("SCHEMAFLUX_LLM_MAX_RETRIES", "0")
	if got := GetLLMMaxRetries(); got != 0 {
		t.Errorf("GetLLMMaxRetries() = %d, want 0 to be honoured (a real, valid choice)", got)
	}
}

func TestGetRepairAttemptsParsesAndRefusesNegative(t *testing.T) {
	t.Setenv("SCHEMAFLUX_REPAIR_ATTEMPTS", "2")
	if got := GetRepairAttempts(); got != 2 {
		t.Errorf("GetRepairAttempts() = %d, want 2", got)
	}

	t.Setenv("SCHEMAFLUX_REPAIR_ATTEMPTS", "-1")
	if got := GetRepairAttempts(); got != 1 {
		t.Errorf("GetRepairAttempts() = %d, want the default 1 for a negative value", got)
	}

	// Zero disables repair entirely, and the doc comment says this is
	// supported -- it must be honoured, not treated as "unset".
	t.Setenv("SCHEMAFLUX_REPAIR_ATTEMPTS", "0")
	if got := GetRepairAttempts(); got != 0 {
		t.Errorf("GetRepairAttempts() = %d, want 0 to disable repair", got)
	}

	t.Setenv("SCHEMAFLUX_REPAIR_ATTEMPTS", "")
	if got := GetRepairAttempts(); got != 1 {
		t.Errorf("GetRepairAttempts() = %d, want the default 1 when unset", got)
	}
}

func TestGetLLMRetryBackoffParsesAndRefusesNonPositive(t *testing.T) {
	t.Setenv("SCHEMAFLUX_LLM_RETRY_BACKOFF", "2s")
	if got := GetLLMRetryBackoff(); got != 2*time.Second {
		t.Errorf("GetLLMRetryBackoff() = %v, want 2s", got)
	}

	// The refusal: zero or negative backoff is not a legitimate value (an
	// immediate or backwards retry), so the default applies.
	t.Setenv("SCHEMAFLUX_LLM_RETRY_BACKOFF", "0s")
	if got := GetLLMRetryBackoff(); got != 500*time.Millisecond {
		t.Errorf("GetLLMRetryBackoff() = %v, want the 500ms default for a zero value", got)
	}

	t.Setenv("SCHEMAFLUX_LLM_RETRY_BACKOFF", "-5s")
	if got := GetLLMRetryBackoff(); got != 500*time.Millisecond {
		t.Errorf("GetLLMRetryBackoff() = %v, want the 500ms default for a negative value", got)
	}

	t.Setenv("SCHEMAFLUX_LLM_RETRY_BACKOFF", "garbage")
	if got := GetLLMRetryBackoff(); got != 500*time.Millisecond {
		t.Errorf("GetLLMRetryBackoff() = %v, want the 500ms default for an unparseable value", got)
	}
}

// GetDebugMode/SetDebugMode: the explicit setter wins once true, and the
// environment variable is the fallback when nobody has called the setter.
func TestDebugModeExplicitSetterAndEnvFallback(t *testing.T) {
	SetDebugMode(false)
	t.Setenv("SCHEMAFLUX_DEBUG", "")
	if GetDebugMode() {
		t.Fatal("GetDebugMode() = true with nothing configured")
	}

	t.Setenv("SCHEMAFLUX_DEBUG", "true")
	if !GetDebugMode() {
		t.Error("GetDebugMode() = false despite SCHEMAFLUX_DEBUG=true")
	}

	t.Setenv("SCHEMAFLUX_DEBUG", "")
	SetDebugMode(true)
	if !GetDebugMode() {
		t.Error("GetDebugMode() = false despite SetDebugMode(true)")
	}
	SetDebugMode(false) // restore for later tests in this package
}

func TestMetricsEnabledExplicitSetterAndEnvFallback(t *testing.T) {
	SetMetricsEnabled(false)
	t.Setenv("SCHEMAFLUX_METRICS", "")
	if IsMetricsEnabled() {
		t.Fatal("IsMetricsEnabled() = true with nothing configured")
	}

	t.Setenv("SCHEMAFLUX_METRICS", "true")
	if !IsMetricsEnabled() {
		t.Error("IsMetricsEnabled() = false despite SCHEMAFLUX_METRICS=true")
	}

	t.Setenv("SCHEMAFLUX_METRICS", "")
	SetMetricsEnabled(true)
	if !IsMetricsEnabled() {
		t.Error("IsMetricsEnabled() = false despite SetMetricsEnabled(true)")
	}
	SetMetricsEnabled(false) // restore for later tests in this package
}
