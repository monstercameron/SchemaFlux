package requesttracking

import (
	"context"
	"strings"
	"testing"
)

// resetConfig restores the package's configured/default state after a test
// that calls Configure, so tests in this file (and tracking_test.go, which
// runs in the same package and does not itself reset) do not leak a
// configuration into whichever test runs next -- this suite is run with
// -shuffle=on -count=3, so an un-reset global is a self-inflicted flake
// waiting to happen.
func resetConfig(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		configMu.Lock()
		configured = false
		configuredCfg = Config{}
		configMu.Unlock()
	})
}

func TestDefaultConfig_Defaults(t *testing.T) {
	t.Setenv("SCHEMAFLUX_REQUEST_TRACKING", "")
	t.Setenv("SCHEMAFLUX_REQUEST_ID_STRATEGY", "")
	t.Setenv("SCHEMAFLUX_CORRELATION_ID_STRATEGY", "")
	t.Setenv("SCHEMAFLUX_REQUEST_ID_HEADER", "")
	t.Setenv("SCHEMAFLUX_CORRELATION_ID_HEADER", "")

	cfg := DefaultConfig()
	if !cfg.Enabled {
		t.Error("Enabled = false, want true by default")
	}
	if cfg.RequestIDStrategy != IDStrategyAuto {
		t.Errorf("RequestIDStrategy = %q, want %q", cfg.RequestIDStrategy, IDStrategyAuto)
	}
	if cfg.CorrelationIDStrategy != CorrelationStrategyInherit {
		t.Errorf("CorrelationIDStrategy = %q, want %q", cfg.CorrelationIDStrategy, CorrelationStrategyInherit)
	}
	if cfg.RequestIDHeader != "X-Request-ID" {
		t.Errorf("RequestIDHeader = %q, want X-Request-ID", cfg.RequestIDHeader)
	}
	if cfg.CorrelationIDHeader != "X-Correlation-ID" {
		t.Errorf("CorrelationIDHeader = %q, want X-Correlation-ID", cfg.CorrelationIDHeader)
	}
}

func TestDefaultConfig_ReadsEveryEnvVar(t *testing.T) {
	t.Setenv("SCHEMAFLUX_REQUEST_TRACKING", "false")
	t.Setenv("SCHEMAFLUX_REQUEST_ID_STRATEGY", "TIMESTAMP")
	t.Setenv("SCHEMAFLUX_CORRELATION_ID_STRATEGY", "GENERATE")
	t.Setenv("SCHEMAFLUX_REQUEST_ID_HEADER", "X-My-Request")
	t.Setenv("SCHEMAFLUX_CORRELATION_ID_HEADER", "X-My-Correlation")

	cfg := DefaultConfig()
	if cfg.Enabled {
		t.Error("Enabled = true, want false (SCHEMAFLUX_REQUEST_TRACKING=false)")
	}
	if cfg.RequestIDStrategy != IDStrategyTimestamp {
		t.Errorf("RequestIDStrategy = %q, want %q (lowercased from env)", cfg.RequestIDStrategy, IDStrategyTimestamp)
	}
	if cfg.CorrelationIDStrategy != CorrelationStrategyGenerate {
		t.Errorf("CorrelationIDStrategy = %q, want %q", cfg.CorrelationIDStrategy, CorrelationStrategyGenerate)
	}
	if cfg.RequestIDHeader != "X-My-Request" {
		t.Errorf("RequestIDHeader = %q", cfg.RequestIDHeader)
	}
	if cfg.CorrelationIDHeader != "X-My-Correlation" {
		t.Errorf("CorrelationIDHeader = %q", cfg.CorrelationIDHeader)
	}
}

func TestGetConfig_FallsBackToDefaultWhenUnconfigured(t *testing.T) {
	resetConfig(t)
	configMu.Lock()
	configured = false
	configMu.Unlock()
	t.Setenv("SCHEMAFLUX_REQUEST_TRACKING", "")

	cfg := GetConfig()
	if !cfg.Enabled {
		t.Error("GetConfig() before any Configure() call did not fall back to DefaultConfig()")
	}
}

func TestWithRequestID_EmptyIsANoop(t *testing.T) {
	ctx := context.Background()
	got := WithRequestID(ctx, "   ")
	if got != ctx {
		t.Error("WithRequestID with a blank id returned a different context")
	}
	if meta := FromContext(got); meta.RequestID != "" {
		t.Errorf("RequestID = %q, want empty", meta.RequestID)
	}
}

func TestWithRequestID_SetsTheValue(t *testing.T) {
	ctx := WithRequestID(context.Background(), "req-9")
	if got := FromContext(ctx).RequestID; got != "req-9" {
		t.Errorf("RequestID = %q, want req-9", got)
	}
}

func TestWithCorrelationID_EmptyIsANoop(t *testing.T) {
	ctx := context.Background()
	got := WithCorrelationID(ctx, "")
	if got != ctx {
		t.Error("WithCorrelationID with an empty id returned a different context")
	}
}

func TestWithCorrelationID_SetsTheValue(t *testing.T) {
	ctx := WithCorrelationID(context.Background(), "corr-9")
	if got := FromContext(ctx).CorrelationID; got != "corr-9" {
		t.Errorf("CorrelationID = %q, want corr-9", got)
	}
}

func TestWithMetadata_NilContextBecomesBackground(t *testing.T) {
	//lint:ignore SA1012 exercising WithMetadata's own nil-context handling
	ctx := WithMetadata(nil, Metadata{RequestID: "r", CorrelationID: "c"})
	if ctx == nil {
		t.Fatal("WithMetadata(nil, ...) returned nil")
	}
	got := FromContext(ctx)
	if got.RequestID != "r" || got.CorrelationID != "c" {
		t.Errorf("metadata = %+v", got)
	}
}

func TestWithMetadata_EmptyFieldsLeaveContextUnchanged(t *testing.T) {
	ctx := WithMetadata(context.Background(), Metadata{})
	if got := FromContext(ctx); got.RequestID != "" || got.CorrelationID != "" {
		t.Errorf("metadata = %+v, want zero value", got)
	}
}

func TestFromContext_NilContext(t *testing.T) {
	//lint:ignore SA1012 exercising FromContext's own nil-context handling
	if got := FromContext(nil); got != (Metadata{}) {
		t.Errorf("FromContext(nil) = %+v, want zero value", got)
	}
}

func TestEnsure_NilContextAndPopulatesMetadata(t *testing.T) {
	resetConfig(t)
	Configure(Config{Enabled: true, RequestIDStrategy: IDStrategyAuto, CorrelationIDStrategy: CorrelationStrategyGenerate})

	//lint:ignore SA1012 exercising Ensure's own nil-context handling
	ctx, meta := Ensure(nil, "", "")
	if ctx == nil {
		t.Fatal("Ensure(nil, ...) returned a nil context")
	}
	if meta.RequestID == "" {
		t.Error("Ensure did not generate a request id")
	}
	// The metadata Ensure returns must also be the metadata now reachable
	// from the context it returns -- that round trip is the whole point of
	// Ensure over calling Resolve and WithMetadata separately.
	if got := FromContext(ctx); got != meta {
		t.Errorf("FromContext(ctx) = %+v, want the same metadata Ensure returned (%+v)", got, meta)
	}
}

func TestEnsure_ExplicitIDsWin(t *testing.T) {
	resetConfig(t)
	Configure(Config{Enabled: true, RequestIDStrategy: IDStrategyAuto, CorrelationIDStrategy: CorrelationStrategyGenerate})

	ctx, meta := Ensure(context.Background(), "req-explicit", "corr-explicit")
	if meta.RequestID != "req-explicit" || meta.CorrelationID != "corr-explicit" {
		t.Errorf("meta = %+v", meta)
	}
	if got := FromContext(ctx); got.RequestID != "req-explicit" {
		t.Errorf("context metadata = %+v", got)
	}
}

func TestInject_NilCarrierIsANoop(t *testing.T) {
	// Must not panic.
	Inject(context.Background(), nil)
}

func TestExtract_NilCarrierReturnsContextUnchanged(t *testing.T) {
	ctx := context.Background()
	if got := Extract(ctx, nil); got != ctx {
		t.Error("Extract(ctx, nil) returned a different context")
	}
}

// TestExtract_MalformedCarrier drives the malformed-carrier cases the task
// calls out explicitly: headers with surrounding whitespace, headers using
// the wrong case or a header name the config was not set to read, and an
// entirely empty carrier. Extract must produce empty metadata rather than
// panicking or picking up a value from the wrong key.
func TestExtract_MalformedCarrier(t *testing.T) {
	resetConfig(t)
	Configure(Config{
		Enabled:             true,
		RequestIDHeader:     "X-Request-ID",
		CorrelationIDHeader: "X-Correlation-ID",
	})

	cases := []struct {
		name    string
		carrier map[string]string
		want    Metadata
	}{
		{"empty carrier", map[string]string{}, Metadata{}},
		{
			"whitespace-only values are trimmed to empty",
			map[string]string{"X-Request-ID": "   ", "X-Correlation-ID": "\t"},
			Metadata{},
		},
		{
			"wrong case header name is not read (map keys are exact)",
			map[string]string{"x-request-id": "req-1"},
			Metadata{},
		},
		{
			"unrelated keys are ignored",
			map[string]string{"Content-Type": "application/json"},
			Metadata{},
		},
		{
			"values are trimmed",
			map[string]string{"X-Request-ID": "  req-2  ", "X-Correlation-ID": "  corr-2  "},
			Metadata{RequestID: "req-2", CorrelationID: "corr-2"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FromContext(Extract(context.Background(), tc.carrier))
			if got != tc.want {
				t.Errorf("Extract(%v) metadata = %+v, want %+v", tc.carrier, got, tc.want)
			}
		})
	}
}

// TestInjectExtractRoundTrip_AcrossACarrierMap is the round trip the task
// calls out: Inject onto one map, Extract from it into a fresh context,
// and confirm both IDs survive the trip through the plain map[string]string
// a real transport (headers, a queue message) would use.
func TestInjectExtractRoundTrip_AcrossACarrierMap(t *testing.T) {
	resetConfig(t)
	Configure(Config{
		Enabled:             true,
		RequestIDHeader:     "X-Request-ID",
		CorrelationIDHeader: "X-Correlation-ID",
	})

	src := WithMetadata(context.Background(), Metadata{RequestID: "req-rt", CorrelationID: "corr-rt"})
	carrier := map[string]string{}
	Inject(src, carrier)

	if len(carrier) != 2 {
		t.Fatalf("carrier = %v, want 2 entries", carrier)
	}

	dst := Extract(context.Background(), carrier)
	got := FromContext(dst)
	if got.RequestID != "req-rt" || got.CorrelationID != "corr-rt" {
		t.Errorf("round-tripped metadata = %+v", got)
	}
}

// Inject with only a request ID set must not write a correlation header at
// all -- an empty header value would round-trip back as "", indistinguishable
// from Extract's own empty-carrier case, and a caller checking
// `carrier["X-Correlation-ID"] == ""` cannot tell "not sent" from "sent
// empty" unless the key is simply absent.
func TestInject_OmitsUnsetFields(t *testing.T) {
	resetConfig(t)
	Configure(Config{Enabled: true, RequestIDHeader: "X-Request-ID", CorrelationIDHeader: "X-Correlation-ID"})

	ctx := WithRequestID(context.Background(), "req-only")
	carrier := map[string]string{}
	Inject(ctx, carrier)

	if _, ok := carrier["X-Correlation-ID"]; ok {
		t.Error("Inject wrote a correlation header for metadata that had none")
	}
	if carrier["X-Request-ID"] != "req-only" {
		t.Errorf("X-Request-ID = %q", carrier["X-Request-ID"])
	}
}

func TestNormalizeConfig_InvalidStrategiesFallBackToDefaults(t *testing.T) {
	cfg := normalizeConfig(Config{
		RequestIDStrategy:     IDStrategy("not-a-real-strategy"),
		CorrelationIDStrategy: CorrelationStrategy("not-a-real-strategy"),
	})
	if cfg.RequestIDStrategy != IDStrategyAuto {
		t.Errorf("RequestIDStrategy = %q, want fallback to %q", cfg.RequestIDStrategy, IDStrategyAuto)
	}
	if cfg.CorrelationIDStrategy != CorrelationStrategyInherit {
		t.Errorf("CorrelationIDStrategy = %q, want fallback to %q", cfg.CorrelationIDStrategy, CorrelationStrategyInherit)
	}
	if cfg.RequestIDHeader != "X-Request-ID" || cfg.CorrelationIDHeader != "X-Correlation-ID" {
		t.Errorf("headers = %q / %q, want the defaults", cfg.RequestIDHeader, cfg.CorrelationIDHeader)
	}
}

func TestNormalizeConfig_BlankHeadersFallBackToDefaults(t *testing.T) {
	cfg := normalizeConfig(Config{RequestIDHeader: "   ", CorrelationIDHeader: ""})
	if cfg.RequestIDHeader != "X-Request-ID" {
		t.Errorf("RequestIDHeader = %q", cfg.RequestIDHeader)
	}
	if cfg.CorrelationIDHeader != "X-Correlation-ID" {
		t.Errorf("CorrelationIDHeader = %q", cfg.CorrelationIDHeader)
	}
}

// TestGenerateRequestID_EveryStrategy drives generateRequestID directly (via
// Resolve, its only caller) for every IDStrategy value, including the
// IDStrategyTrace path with and without an installed trace source -- the
// refusal this package documents on TraceIDSource: "The default returns ”,
// so a correlation id falls back to the request id exactly as it does when
// no span exists."
func TestGenerateRequestID_EveryStrategy(t *testing.T) {
	resetConfig(t)

	t.Run("none produces no id even when enabled", func(t *testing.T) {
		Configure(Config{Enabled: true, RequestIDStrategy: IDStrategyNone, CorrelationIDStrategy: CorrelationStrategyNone})
		meta := Resolve(context.Background(), "", "")
		if meta.RequestID != "" {
			t.Errorf("RequestID = %q, want empty for IDStrategyNone", meta.RequestID)
		}
	})

	t.Run("timestamp produces a nonempty id", func(t *testing.T) {
		Configure(Config{Enabled: true, RequestIDStrategy: IDStrategyTimestamp, CorrelationIDStrategy: CorrelationStrategyNone})
		meta := Resolve(context.Background(), "", "")
		if meta.RequestID == "" || !strings.HasPrefix(meta.RequestID, "req_") {
			t.Errorf("RequestID = %q, want a req_-prefixed timestamp id", meta.RequestID)
		}
	})

	t.Run("uuid produces a nonempty id", func(t *testing.T) {
		Configure(Config{Enabled: true, RequestIDStrategy: IDStrategyUUID, CorrelationIDStrategy: CorrelationStrategyNone})
		meta := Resolve(context.Background(), "", "")
		if meta.RequestID == "" || !strings.HasPrefix(meta.RequestID, "req_") {
			t.Errorf("RequestID = %q, want a req_-prefixed id", meta.RequestID)
		}
	})

	t.Run("auto produces a nonempty id", func(t *testing.T) {
		Configure(Config{Enabled: true, RequestIDStrategy: IDStrategyAuto, CorrelationIDStrategy: CorrelationStrategyNone})
		meta := Resolve(context.Background(), "", "")
		if meta.RequestID == "" {
			t.Error("RequestID is empty for IDStrategyAuto")
		}
	})

	t.Run("trace falls back to a generated id with no trace source installed", func(t *testing.T) {
		Configure(Config{Enabled: true, RequestIDStrategy: IDStrategyTrace, CorrelationIDStrategy: CorrelationStrategyNone})
		meta := Resolve(context.Background(), "", "")
		if meta.RequestID == "" {
			t.Error("RequestID is empty for IDStrategyTrace with no source installed")
		}
		if strings.HasPrefix(meta.RequestID, "trace-") {
			t.Error("RequestID looks like it came from a trace source that was never installed")
		}
	})

	t.Run("trace prefers the installed trace source", func(t *testing.T) {
		restore := SetTraceIDSource(func(context.Context) string { return "trace-abc123" })
		defer restore()

		Configure(Config{Enabled: true, RequestIDStrategy: IDStrategyTrace, CorrelationIDStrategy: CorrelationStrategyNone})
		meta := Resolve(context.Background(), "", "")
		if meta.RequestID != "trace-abc123" {
			t.Errorf("RequestID = %q, want the installed trace source's value", meta.RequestID)
		}
	})

	t.Run("trace source returning empty falls back to a generated id", func(t *testing.T) {
		restore := SetTraceIDSource(func(context.Context) string { return "" })
		defer restore()

		Configure(Config{Enabled: true, RequestIDStrategy: IDStrategyTrace, CorrelationIDStrategy: CorrelationStrategyNone})
		meta := Resolve(context.Background(), "", "")
		if meta.RequestID == "" {
			t.Error("RequestID is empty when the trace source itself returned empty")
		}
	})
}

// SetTraceIDSource(nil) restores the default (always "") rather than storing
// a nil function pointer that would panic when called -- see the function's
// own doc comment.
func TestSetTraceIDSource_NilRestoresDefault(t *testing.T) {
	restore := SetTraceIDSource(func(context.Context) string { return "installed" })
	restoreToNil := SetTraceIDSource(nil)
	defer restoreToNil()
	defer restore()

	resetConfig(t)
	Configure(Config{Enabled: true, RequestIDStrategy: IDStrategyTrace, CorrelationIDStrategy: CorrelationStrategyNone})
	meta := Resolve(context.Background(), "", "")
	if meta.RequestID == "installed" {
		t.Error("SetTraceIDSource(nil) left the previous source installed")
	}
}

func TestSetTraceIDSource_RestoreReturnsThePreviousSource(t *testing.T) {
	restoreFirst := SetTraceIDSource(func(context.Context) string { return "first" })
	restoreSecond := SetTraceIDSource(func(context.Context) string { return "second" })

	resetConfig(t)
	Configure(Config{Enabled: true, RequestIDStrategy: IDStrategyTrace, CorrelationIDStrategy: CorrelationStrategyNone})

	meta := Resolve(context.Background(), "", "")
	if meta.RequestID != "second" {
		t.Fatalf("RequestID = %q, want second", meta.RequestID)
	}

	restoreSecond()
	meta = Resolve(context.Background(), "", "")
	if meta.RequestID != "first" {
		t.Fatalf("RequestID = %q, want first after restoring", meta.RequestID)
	}
	restoreFirst()
}

// TestGenerateCorrelationID_EveryStrategy covers generateCorrelationID's
// branches beyond what TestResolveGeneratesCorrelationFromRequestByDefault
// (tracking_test.go) already exercises for CorrelationStrategyRequest with a
// nonempty request id.
func TestGenerateCorrelationID_EveryStrategy(t *testing.T) {
	resetConfig(t)

	t.Run("none produces no correlation id", func(t *testing.T) {
		Configure(Config{Enabled: true, RequestIDStrategy: IDStrategyNone, CorrelationIDStrategy: CorrelationStrategyNone})
		meta := Resolve(context.Background(), "", "")
		if meta.CorrelationID != "" {
			t.Errorf("CorrelationID = %q, want empty", meta.CorrelationID)
		}
	})

	t.Run("generate always mints a fresh id", func(t *testing.T) {
		Configure(Config{Enabled: true, RequestIDStrategy: IDStrategyNone, CorrelationIDStrategy: CorrelationStrategyGenerate})
		meta := Resolve(context.Background(), "", "")
		if meta.CorrelationID == "" || !strings.HasPrefix(meta.CorrelationID, "corr_") {
			t.Errorf("CorrelationID = %q, want a corr_-prefixed id", meta.CorrelationID)
		}
	})

	t.Run("request falls back to generated when there is no request id", func(t *testing.T) {
		Configure(Config{Enabled: true, RequestIDStrategy: IDStrategyNone, CorrelationIDStrategy: CorrelationStrategyRequest})
		meta := Resolve(context.Background(), "", "")
		if meta.CorrelationID == "" {
			t.Error("CorrelationID is empty for CorrelationStrategyRequest with no request id available")
		}
	})

	t.Run("inherit prefers the context's own correlation id over the request id", func(t *testing.T) {
		Configure(Config{Enabled: true, RequestIDStrategy: IDStrategyAuto, CorrelationIDStrategy: CorrelationStrategyInherit})
		ctx := WithCorrelationID(context.Background(), "corr-inherited")
		meta := Resolve(ctx, "req-explicit", "")
		if meta.CorrelationID != "corr-inherited" {
			t.Errorf("CorrelationID = %q, want corr-inherited", meta.CorrelationID)
		}
	})

	t.Run("inherit falls back to the request id with nothing in context", func(t *testing.T) {
		Configure(Config{Enabled: true, RequestIDStrategy: IDStrategyAuto, CorrelationIDStrategy: CorrelationStrategyInherit})
		meta := Resolve(context.Background(), "req-only", "")
		if meta.CorrelationID != "req-only" {
			t.Errorf("CorrelationID = %q, want req-only", meta.CorrelationID)
		}
	})

	t.Run("inherit falls back to generating when nothing else is available", func(t *testing.T) {
		Configure(Config{Enabled: true, RequestIDStrategy: IDStrategyNone, CorrelationIDStrategy: CorrelationStrategyInherit})
		meta := Resolve(context.Background(), "", "")
		if meta.CorrelationID == "" {
			t.Error("CorrelationID is empty with no context value, no request id, and IDStrategyNone")
		}
	})

	t.Run("an unrecognised strategy behaves like inherit's request-id fallback", func(t *testing.T) {
		Configure(Config{Enabled: true, RequestIDStrategy: IDStrategyAuto, CorrelationIDStrategy: CorrelationStrategy("bogus")})
		meta := Resolve(context.Background(), "req-fallback", "")
		if meta.CorrelationID != "req-fallback" {
			t.Errorf("CorrelationID = %q, want req-fallback (default branch falls back to the request id)", meta.CorrelationID)
		}
	})
}

// generateIdentifier's crand.Read path is exercised by every "produces a
// nonempty id" case above; this pins its shape (a prefix, an underscore, and
// hex bytes) directly, and generateTimestampID's shape via the timestamp
// strategy.
func TestGenerateIdentifier_Shape(t *testing.T) {
	id := generateIdentifier("req")
	if !strings.HasPrefix(id, "req_") {
		t.Errorf("generateIdentifier(%q) = %q, want a req_ prefix", "req", id)
	}
	if len(id) <= len("req_") {
		t.Errorf("generateIdentifier(%q) = %q, want more than just the prefix", "req", id)
	}
}

func TestGenerateTimestampID_UniqueAcrossCalls(t *testing.T) {
	first := generateTimestampID("t")
	second := generateTimestampID("t")
	if first == second {
		t.Errorf("generateTimestampID produced the same id twice: %q", first)
	}
	if !strings.HasPrefix(first, "t_") || !strings.HasPrefix(second, "t_") {
		t.Errorf("ids = %q, %q, want a t_ prefix", first, second)
	}
}

func TestEnvEnabled(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{"yes", true},
		{"on", true},
		{"  true  ", true},
		{"0", false},
		{"false", false},
		{"", false},
		{"maybe", false},
	}
	for _, tc := range cases {
		if got := envEnabled(tc.raw); got != tc.want {
			t.Errorf("envEnabled(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}
