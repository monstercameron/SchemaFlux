package config

import (
	"testing"
	"time"
)

func TestGetTimeoutParsesValidDurationAndFallsBackOtherwise(t *testing.T) {
	setenvAndReload(t, "SCHEMAFLUX_TIMEOUT", "45s")
	if got := GetTimeout(); got != 45*time.Second {
		t.Errorf("GetTimeout() = %v, want 45s", got)
	}

	setenvAndReload(t, "SCHEMAFLUX_TIMEOUT", "not-a-duration")
	if got := GetTimeout(); got != 30*time.Second {
		t.Errorf("GetTimeout() = %v, want the 30s default for an unparseable value", got)
	}

	setenvAndReload(t, "SCHEMAFLUX_TIMEOUT", "")
	if got := GetTimeout(); got != 30*time.Second {
		t.Errorf("GetTimeout() = %v, want the 30s default when unset", got)
	}

	// A zero or negative duration parses and cannot be used: a deadline of zero
	// expires as it is set, so every call that carried none of its own failed.
	for _, raw := range []string{"0s", "0", "-1s", "-30m"} {
		setenvAndReload(t, "SCHEMAFLUX_TIMEOUT", raw)
		if got := GetTimeout(); got != 30*time.Second {
			t.Errorf("GetTimeout() = %v for SCHEMAFLUX_TIMEOUT=%q, want the 30s default", got, raw)
		}
	}
}

func TestGetLLMMaxRetriesParsesAndRefusesNegative(t *testing.T) {
	setenvAndReload(t, "SCHEMAFLUX_LLM_MAX_RETRIES", "5")
	if got := GetLLMMaxRetries(); got != 5 {
		t.Errorf("GetLLMMaxRetries() = %d, want 5", got)
	}

	// The refusal: a negative retry budget is nonsensical and must not be
	// honoured -- the default applies instead.
	setenvAndReload(t, "SCHEMAFLUX_LLM_MAX_RETRIES", "-1")
	if got := GetLLMMaxRetries(); got != 3 {
		t.Errorf("GetLLMMaxRetries() = %d, want the default 3 for a negative value", got)
	}

	setenvAndReload(t, "SCHEMAFLUX_LLM_MAX_RETRIES", "not-a-number")
	if got := GetLLMMaxRetries(); got != 3 {
		t.Errorf("GetLLMMaxRetries() = %d, want the default 3 for an unparseable value", got)
	}

	setenvAndReload(t, "SCHEMAFLUX_LLM_MAX_RETRIES", "0")
	if got := GetLLMMaxRetries(); got != 0 {
		t.Errorf("GetLLMMaxRetries() = %d, want 0 to be honoured (a real, valid choice)", got)
	}
}

func TestGetRepairAttemptsParsesAndRefusesNegative(t *testing.T) {
	setenvAndReload(t, "SCHEMAFLUX_REPAIR_ATTEMPTS", "2")
	if got := GetRepairAttempts(); got != 2 {
		t.Errorf("GetRepairAttempts() = %d, want 2", got)
	}

	setenvAndReload(t, "SCHEMAFLUX_REPAIR_ATTEMPTS", "-1")
	if got := GetRepairAttempts(); got != 1 {
		t.Errorf("GetRepairAttempts() = %d, want the default 1 for a negative value", got)
	}

	// Zero disables repair entirely, and the doc comment says this is
	// supported -- it must be honoured, not treated as "unset".
	setenvAndReload(t, "SCHEMAFLUX_REPAIR_ATTEMPTS", "0")
	if got := GetRepairAttempts(); got != 0 {
		t.Errorf("GetRepairAttempts() = %d, want 0 to disable repair", got)
	}

	setenvAndReload(t, "SCHEMAFLUX_REPAIR_ATTEMPTS", "")
	if got := GetRepairAttempts(); got != 1 {
		t.Errorf("GetRepairAttempts() = %d, want the default 1 when unset", got)
	}
}

func TestGetLLMRetryBackoffParsesAndRefusesNonPositive(t *testing.T) {
	setenvAndReload(t, "SCHEMAFLUX_LLM_RETRY_BACKOFF", "2s")
	if got := GetLLMRetryBackoff(); got != 2*time.Second {
		t.Errorf("GetLLMRetryBackoff() = %v, want 2s", got)
	}

	// The refusal: zero or negative backoff is not a legitimate value (an
	// immediate or backwards retry), so the default applies.
	setenvAndReload(t, "SCHEMAFLUX_LLM_RETRY_BACKOFF", "0s")
	if got := GetLLMRetryBackoff(); got != 500*time.Millisecond {
		t.Errorf("GetLLMRetryBackoff() = %v, want the 500ms default for a zero value", got)
	}

	setenvAndReload(t, "SCHEMAFLUX_LLM_RETRY_BACKOFF", "-5s")
	if got := GetLLMRetryBackoff(); got != 500*time.Millisecond {
		t.Errorf("GetLLMRetryBackoff() = %v, want the 500ms default for a negative value", got)
	}

	setenvAndReload(t, "SCHEMAFLUX_LLM_RETRY_BACKOFF", "garbage")
	if got := GetLLMRetryBackoff(); got != 500*time.Millisecond {
		t.Errorf("GetLLMRetryBackoff() = %v, want the 500ms default for an unparseable value", got)
	}
}

// GetDebugMode/SetDebugMode: the explicit setter wins once true, and the
// environment variable is the fallback when nobody has called the setter.
func TestDebugModeExplicitSetterAndEnvFallback(t *testing.T) {
	SetDebugMode(false)
	setenvAndReload(t, "SCHEMAFLUX_DEBUG", "")
	if GetDebugMode() {
		t.Fatal("GetDebugMode() = true with nothing configured")
	}

	setenvAndReload(t, "SCHEMAFLUX_DEBUG", "true")
	if !GetDebugMode() {
		t.Error("GetDebugMode() = false despite SCHEMAFLUX_DEBUG=true")
	}

	setenvAndReload(t, "SCHEMAFLUX_DEBUG", "")
	SetDebugMode(true)
	if !GetDebugMode() {
		t.Error("GetDebugMode() = false despite SetDebugMode(true)")
	}
	SetDebugMode(false) // restore for later tests in this package
}

func TestMetricsEnabledExplicitSetterAndEnvFallback(t *testing.T) {
	SetMetricsEnabled(false)
	setenvAndReload(t, "SCHEMAFLUX_METRICS", "")
	if IsMetricsEnabled() {
		t.Fatal("IsMetricsEnabled() = true with nothing configured")
	}

	setenvAndReload(t, "SCHEMAFLUX_METRICS", "true")
	if !IsMetricsEnabled() {
		t.Error("IsMetricsEnabled() = false despite SCHEMAFLUX_METRICS=true")
	}

	setenvAndReload(t, "SCHEMAFLUX_METRICS", "")
	SetMetricsEnabled(true)
	if !IsMetricsEnabled() {
		t.Error("IsMetricsEnabled() = false despite SetMetricsEnabled(true)")
	}
	SetMetricsEnabled(false) // restore for later tests in this package
}
