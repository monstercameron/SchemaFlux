package tests

import (
	"context"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
)

// This file covers Init, InitWithEnv, GetLogger, SetLogLevel, and the
// request-tracking wrappers: ConfigureRequestTracking,
// GetRequestTrackingConfig, WithRequestID, WithCorrelationID,
// WithRequestTrackingMetadata, RequestTrackingFromContext,
// InjectRequestTracking, and ExtractRequestTracking.

// restoreDefaultClient saves and restores the package-level default client,
// since Init/InitWithEnv mutate global state the same way schemafluxtest does.
func restoreDefaultClient(t *testing.T) {
	t.Helper()
	previous := schemaflux.GetDefaultClient()
	t.Cleanup(func() { schemaflux.SetDefaultClient(previous) })
}

// Init with an explicit key builds and installs a client -- no network call,
// only local construction, so it makes no request the AGENTS.md "never spend
// money" rule would forbid.
func TestInitInstallsAClientForAnExplicitKey(t *testing.T) {
	restoreDefaultClient(t)
	t.Setenv("SCHEMAFLUX_PROVIDER", "openai")

	if err := schemaflux.Init("a-non-real-test-key"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if schemaflux.GetDefaultClient() == nil {
		t.Error("GetDefaultClient() is nil after Init succeeded")
	}
}

// With no key anywhere reachable and a provider that is not the deliberate
// mock, Init must refuse rather than silently falling back to something that
// looks configured.
func TestInitWithNoKeyIsAnError(t *testing.T) {
	restoreDefaultClient(t)
	t.Setenv("SCHEMAFLUX_API_KEY", "")
	t.Setenv("SCHEMAFLUX_PROVIDER", "openai")
	t.Setenv("OPENAI_API_KEY", "")

	if err := schemaflux.Init(""); err == nil {
		t.Fatal("expected an error when no key is available for a real provider")
	}
}

// SCHEMAFLUX_PROVIDER=local is the deliberate opt-in to the mock, and Init
// must honor it even with no key at all.
func TestInitWithLocalProviderNeedsNoKey(t *testing.T) {
	restoreDefaultClient(t)
	t.Setenv("SCHEMAFLUX_API_KEY", "")
	t.Setenv("SCHEMAFLUX_PROVIDER", "local")

	if err := schemaflux.Init(""); err != nil {
		t.Fatalf("Init with the deliberate mock provider: %v", err)
	}
}

// InitWithEnv with a path that does not exist must report the load failure
// rather than silently continuing as if nothing was asked for.
func TestInitWithEnvMissingNamedFileIsAnError(t *testing.T) {
	restoreDefaultClient(t)

	err := schemaflux.InitWithEnv("this/path/does/not/exist.env")
	if err == nil {
		t.Fatal("expected an error for a named .env file that does not exist")
	}
}

func TestGetLoggerReturnsANonNilLogger(t *testing.T) {
	if schemaflux.GetLogger() == nil {
		t.Fatal("GetLogger() returned nil")
	}
}

// SetLogLevel must actually reach the shared logger's level, not merely
// compile.
func TestSetLogLevelChangesTheLoggerLevel(t *testing.T) {
	original := schemaflux.GetLogger()
	t.Cleanup(func() { schemaflux.SetLogLevel(schemaflux.LogInfo) })

	schemaflux.SetLogLevel(schemaflux.LogError)
	if original.Level() != schemaflux.LogError {
		t.Errorf("logger level = %v, want LogError after SetLogLevel", original.Level())
	}
}

// --- Request tracking --------------------------------------------------------

func TestConfigureRequestTrackingIsVisibleThroughGetRequestTrackingConfig(t *testing.T) {
	original := schemaflux.GetRequestTrackingConfig()
	t.Cleanup(func() { schemaflux.ConfigureRequestTracking(original) })

	schemaflux.ConfigureRequestTracking(schemaflux.RequestTrackingConfig{
		Enabled:               true,
		RequestIDStrategy:     schemaflux.RequestIDTrace,
		CorrelationIDStrategy: schemaflux.CorrelationGenerate,
		RequestIDHeader:       "X-My-Request-ID",
		CorrelationIDHeader:   "X-My-Correlation-ID",
	})

	got := schemaflux.GetRequestTrackingConfig()
	if got.RequestIDHeader != "X-My-Request-ID" || got.CorrelationIDHeader != "X-My-Correlation-ID" {
		t.Errorf("got = %+v, want the configured headers reflected back", got)
	}
	if got.RequestIDStrategy != schemaflux.RequestIDTrace {
		t.Errorf("RequestIDStrategy = %v, want %v", got.RequestIDStrategy, schemaflux.RequestIDTrace)
	}
}

func TestWithRequestIDAndCorrelationIDRoundTripThroughContext(t *testing.T) {
	ctx := schemaflux.WithRequestID(context.Background(), "req-123")
	ctx = schemaflux.WithCorrelationID(ctx, "corr-456")

	meta := schemaflux.RequestTrackingFromContext(ctx)
	if meta.RequestID != "req-123" || meta.CorrelationID != "corr-456" {
		t.Errorf("meta = %+v, want the ids set above", meta)
	}
}

// An empty id must not overwrite a context that already carries one -- both
// With* wrappers are documented as forwarding to requesttracking's own
// "empty means no opinion" rule.
func TestWithRequestIDEmptyDoesNotClearAnExistingID(t *testing.T) {
	ctx := schemaflux.WithRequestID(context.Background(), "req-123")
	ctx = schemaflux.WithRequestID(ctx, "")

	meta := schemaflux.RequestTrackingFromContext(ctx)
	if meta.RequestID != "req-123" {
		t.Errorf("RequestID = %q, want the original id preserved", meta.RequestID)
	}
}

func TestWithRequestTrackingMetadataSetsBothIDs(t *testing.T) {
	ctx := schemaflux.WithRequestTrackingMetadata(context.Background(), schemaflux.RequestTrackingMetadata{
		RequestID:     "req-789",
		CorrelationID: "corr-abc",
	})

	meta := schemaflux.RequestTrackingFromContext(ctx)
	if meta.RequestID != "req-789" || meta.CorrelationID != "corr-abc" {
		t.Errorf("meta = %+v, want both ids from the metadata struct", meta)
	}
}

// InjectRequestTracking writes the context's ids into a carrier map, and
// ExtractRequestTracking reads them back into a fresh context -- proving both
// wrappers reach the SAME headers the configured RequestIDHeader/
// CorrelationIDHeader name.
func TestInjectAndExtractRequestTrackingRoundTrip(t *testing.T) {
	original := schemaflux.GetRequestTrackingConfig()
	t.Cleanup(func() { schemaflux.ConfigureRequestTracking(original) })
	schemaflux.ConfigureRequestTracking(schemaflux.RequestTrackingConfig{
		Enabled:             true,
		RequestIDHeader:     "X-Test-Request-ID",
		CorrelationIDHeader: "X-Test-Correlation-ID",
	})

	ctx := schemaflux.WithRequestID(context.Background(), "req-999")
	ctx = schemaflux.WithCorrelationID(ctx, "corr-999")

	carrier := map[string]string{}
	schemaflux.InjectRequestTracking(ctx, carrier)

	if carrier["X-Test-Request-ID"] != "req-999" || carrier["X-Test-Correlation-ID"] != "corr-999" {
		t.Fatalf("carrier = %v, want the configured headers populated", carrier)
	}

	restored := schemaflux.ExtractRequestTracking(context.Background(), carrier)
	meta := schemaflux.RequestTrackingFromContext(restored)
	if meta.RequestID != "req-999" || meta.CorrelationID != "corr-999" {
		t.Errorf("round-tripped meta = %+v, want the original ids back", meta)
	}
}

// A carrier with nothing set must not fabricate ids on Extract.
func TestExtractRequestTrackingWithEmptyCarrierAddsNothing(t *testing.T) {
	ctx := schemaflux.ExtractRequestTracking(context.Background(), map[string]string{})
	meta := schemaflux.RequestTrackingFromContext(ctx)
	if meta.RequestID != "" || meta.CorrelationID != "" {
		t.Errorf("meta = %+v, want no ids from an empty carrier", meta)
	}
}
