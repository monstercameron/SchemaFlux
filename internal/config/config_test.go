package config

import "testing"

func TestGetTraceEnabledHonorsBothEnvNames(t *testing.T) {
	t.Setenv("SCHEMAFLUX_TRACE", "")
	t.Setenv("SCHEMAFLUX_ENABLE_TRACING", "")
	SetTraceEnabled(false)

	if GetTraceEnabled() {
		t.Fatal("expected tracing disabled when env vars are unset")
	}

	t.Setenv("SCHEMAFLUX_TRACE", "true")
	if !GetTraceEnabled() {
		t.Fatal("expected SCHEMAFLUX_TRACE=true to enable tracing")
	}

	t.Setenv("SCHEMAFLUX_TRACE", "")
	t.Setenv("SCHEMAFLUX_ENABLE_TRACING", "1")
	if !GetTraceEnabled() {
		t.Fatal("expected SCHEMAFLUX_ENABLE_TRACING=1 to enable tracing")
	}
}
