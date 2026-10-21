package requesttracking

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/config"
)

func recordEnvProblems(t *testing.T) *[]config.EnvProblem {
	t.Helper()
	var problems []config.EnvProblem
	t.Cleanup(config.SetEnvProblemReporter(func(problem config.EnvProblem) { problems = append(problems, problem) }))
	return &problems
}

// GetConfig reads the environment once. A variable changed afterwards is not
// seen until the program asks for it: config.ReloadEnvironment, or
// Configure(DefaultConfig()), which pins what the environment says at that moment.
func TestGetConfigReadsTheEnvironmentOnce(t *testing.T) {
	resetConfig(t)
	t.Setenv("SCHEMAFLUX_REQUEST_TRACKING", "true")
	t.Setenv("SCHEMAFLUX_REQUEST_ID_HEADER", "X-First")
	if got := GetConfig().RequestIDHeader; got != "X-First" {
		t.Fatalf("RequestIDHeader = %q, want X-First", got)
	}

	t.Setenv("SCHEMAFLUX_REQUEST_ID_HEADER", "X-Second")
	t.Setenv("SCHEMAFLUX_REQUEST_TRACKING", "false")
	for i := 0; i < 5; i++ {
		got := GetConfig()
		if got.RequestIDHeader != "X-First" || !got.Enabled {
			t.Fatalf("call %d: GetConfig re-read the environment: %+v", i, got)
		}
	}

	Configure(DefaultConfig())
	if got := GetConfig(); got.RequestIDHeader != "X-Second" || got.Enabled {
		t.Errorf("after Configure(DefaultConfig()) the new environment was not taken: %+v", got)
	}
}

func TestGetConfigReadsTheEnvironmentOnceUnderConcurrency(t *testing.T) {
	resetConfig(t)
	t.Setenv("SCHEMAFLUX_REQUEST_ID_HEADER", "X-Concurrent")
	done := make(chan Config, 16)
	for i := 0; i < 16; i++ {
		go func() { done <- GetConfig() }()
	}
	for i := 0; i < 16; i++ {
		if got := <-done; got.RequestIDHeader != "X-Concurrent" {
			t.Fatalf("a concurrent first read saw %q", got.RequestIDHeader)
		}
	}
}

func TestAMalformedEnvironmentValueIsReportedOnceAndTheDefaultApplies(t *testing.T) {
	cases := []struct {
		variable string
		value    string
		check    func(Config) bool
	}{
		{"SCHEMAFLUX_REQUEST_TRACKING", "enabled", func(c Config) bool { return c.Enabled }},
		{"SCHEMAFLUX_REQUEST_TRACKING", "on", func(c Config) bool { return c.Enabled }},
		{"SCHEMAFLUX_REQUEST_TRACKING", "yes", func(c Config) bool { return c.Enabled }},
		{"SCHEMAFLUX_REQUEST_TRACKING", "tRuE", func(c Config) bool { return c.Enabled }},
		{"SCHEMAFLUX_REQUEST_ID_STRATEGY", "uuiid", func(c Config) bool { return c.RequestIDStrategy == IDStrategyAuto }},
		{"SCHEMAFLUX_REQUEST_ID_STRATEGY", "random", func(c Config) bool { return c.RequestIDStrategy == IDStrategyAuto }},
		{"SCHEMAFLUX_CORRELATION_ID_STRATEGY", "same", func(c Config) bool { return c.CorrelationIDStrategy == CorrelationStrategyInherit }},
		{"SCHEMAFLUX_CORRELATION_ID_STRATEGY", "uuid", func(c Config) bool { return c.CorrelationIDStrategy == CorrelationStrategyInherit }},
	}
	for _, tc := range cases {
		t.Run(tc.variable+"="+tc.value, func(t *testing.T) {
			problems := recordEnvProblems(t)
			t.Setenv(tc.variable, tc.value)

			cfg := DefaultConfig()
			DefaultConfig()

			if !tc.check(cfg) {
				t.Errorf("a malformed value changed the configuration: %+v", cfg)
			}
			if len(*problems) != 1 {
				t.Fatalf("the problem was reported %d times, want once: %+v", len(*problems), *problems)
			}
			if (*problems)[0].Variable != tc.variable {
				t.Errorf("the report names %q, want %q", (*problems)[0].Variable, tc.variable)
			}
			if len(tc.value) > 3 && strings.Contains((*problems)[0].Want, tc.value) {
				t.Errorf("the report repeats the value: %q", (*problems)[0].Want)
			}
		})
	}
}

// A boolean is read as config.EnvBool reads it, so the spellings strconv.ParseBool
// accepts turn tracking on and off without a report.
func TestRequestTrackingReadsEveryBooleanSpelling(t *testing.T) {
	for value, want := range map[string]bool{
		"1": true, "t": true, "T": true, "TRUE": true, "true": true, "True": true, " true ": true,
		"0": false, "f": false, "F": false, "FALSE": false, "false": false, "False": false,
	} {
		t.Run("["+value+"]", func(t *testing.T) {
			problems := recordEnvProblems(t)
			t.Setenv("SCHEMAFLUX_REQUEST_TRACKING", value)
			if got := DefaultConfig().Enabled; got != want {
				t.Errorf("Enabled = %v for SCHEMAFLUX_REQUEST_TRACKING=%q, want %v", got, value, want)
			}
			if len(*problems) != 0 {
				t.Errorf("a well-formed value was reported: %+v", *problems)
			}
		})
	}
}

func TestAWellFormedEnvironmentIsNotReported(t *testing.T) {
	problems := recordEnvProblems(t)
	t.Setenv("SCHEMAFLUX_REQUEST_TRACKING", "false")
	t.Setenv("SCHEMAFLUX_REQUEST_ID_STRATEGY", "UUID")
	t.Setenv("SCHEMAFLUX_CORRELATION_ID_STRATEGY", " request ")
	DefaultConfig()
	if len(*problems) != 0 {
		t.Errorf("a well-formed environment was reported: %+v", *problems)
	}
}

func TestWithRequestIDAndWithCorrelationIDAcceptANilContext(t *testing.T) {
	//lint:ignore SA1012 exercising the nil-context handling
	ctx := WithRequestID(nil, "req-1")
	if got := FromContext(ctx).RequestID; got != "req-1" {
		t.Errorf("RequestID = %q, want req-1", got)
	}
	//lint:ignore SA1012 exercising the nil-context handling
	ctx = WithCorrelationID(nil, "corr-1")
	if got := FromContext(ctx).CorrelationID; got != "corr-1" {
		t.Errorf("CorrelationID = %q, want corr-1", got)
	}
	//lint:ignore SA1012 exercising the nil-context handling
	if WithRequestID(nil, "  ") == nil || WithCorrelationID(nil, "") == nil {
		t.Error("a blank ID with a nil context returned a nil context")
	}
	//lint:ignore SA1012 exercising the nil-context handling
	ctx = WithMetadata(nil, Metadata{RequestID: "req-2", CorrelationID: "corr-2"})
	if got := FromContext(ctx); got.RequestID != "req-2" || got.CorrelationID != "corr-2" {
		t.Errorf("WithMetadata on a nil context = %+v, want both IDs", got)
	}
	//lint:ignore SA1012 exercising the nil-context handling
	if WithMetadata(nil, Metadata{}) == nil {
		t.Error("blank metadata with a nil context returned a nil context")
	}
}

// Ensure takes a nil context for the background one, so the trace source is asked
// about a context as it is for any other request, and the context it returns
// carries what was resolved.
func TestEnsureTreatsANilContextAsTheBackgroundContext(t *testing.T) {
	resetConfig(t)
	Configure(Config{Enabled: true, RequestIDStrategy: IDStrategyTrace, CorrelationIDStrategy: CorrelationStrategyNone})
	restore := SetTraceIDSource(func(context.Context) string { return "trace-abc123" })
	defer restore()

	//lint:ignore SA1012 exercising the nil-context handling
	ctx, meta := Ensure(nil, "", "")
	if ctx == nil {
		t.Fatal("Ensure returned a nil context")
	}
	if meta.RequestID != "trace-abc123" {
		t.Errorf("RequestID = %q, want the trace source's ID: the nil context was not replaced before the strategy ran", meta.RequestID)
	}
	if got := FromContext(ctx).RequestID; got != "trace-abc123" {
		t.Errorf("the returned context carries %q, want trace-abc123", got)
	}
}

// A context that carries a blank correlation ID resolves to no ID of its own,
// and the correlation strategy then applies; the blank value must not come back
// as the correlation ID.
func TestABlankCorrelationIDOnTheContextIsNotInherited(t *testing.T) {
	resetConfig(t)
	Configure(Config{Enabled: true, RequestIDStrategy: IDStrategyAuto, CorrelationIDStrategy: CorrelationStrategyInherit})

	ctx := context.WithValue(context.Background(), correlationIDKey, "   ")
	meta := Resolve(ctx, "req-1", "")
	if strings.TrimSpace(meta.CorrelationID) == "" {
		t.Fatalf("CorrelationID = %q, a blank inherited from the context", meta.CorrelationID)
	}
	if meta.CorrelationID != "req-1" {
		t.Errorf("CorrelationID = %q, want the request ID the inherit strategy falls back to", meta.CorrelationID)
	}
}

// An explicit correlation ID that is only space is no ID: Resolve makes the one
// the strategy gives, and does not hand the blank back.
func TestResolveTreatsABlankExplicitCorrelationIDAsNone(t *testing.T) {
	resetConfig(t)
	Configure(Config{Enabled: true, RequestIDStrategy: IDStrategyAuto, CorrelationIDStrategy: CorrelationStrategyInherit})

	meta := Resolve(context.Background(), "req-1", " \t ")
	if strings.TrimSpace(meta.CorrelationID) == "" {
		t.Fatalf("CorrelationID = %q, a blank explicit ID returned as the ID", meta.CorrelationID)
	}
	if meta.CorrelationID != "req-1" {
		t.Errorf("CorrelationID = %q, want the request ID the inherit strategy falls back to", meta.CorrelationID)
	}
}

// Inject into a nil carrier writes nothing and does not panic, even when the
// context carries IDs.
func TestInjectIntoANilCarrierDoesNotPanic(t *testing.T) {
	resetConfig(t)
	ctx := WithMetadata(context.Background(), Metadata{RequestID: "req-1", CorrelationID: "corr-1"})
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Inject panicked on a nil carrier: %v", r)
		}
	}()
	Inject(ctx, nil)
}

func TestWithMetadataDoesNotStoreABlankID(t *testing.T) {
	ctx := WithMetadata(context.Background(), Metadata{RequestID: "  ", CorrelationID: " \t"})
	if got := FromContext(ctx); got != (Metadata{}) {
		t.Errorf("metadata = %+v, want none", got)
	}
	ctx = WithMetadata(context.Background(), Metadata{RequestID: "  req  ", CorrelationID: "corr"})
	if got := FromContext(ctx); got.RequestID != "req" || got.CorrelationID != "corr" {
		t.Errorf("metadata = %+v, want the trimmed IDs", got)
	}
}

func TestBoundedIDAppliesTheSharedCallerAndCarrierRule(t *testing.T) {
	cases := []struct{ name, id, want string }{
		{"plain", "req-123", "req-123"},
		{"edge printable bytes", "!~", "!~"},
		{"exact limit", strings.Repeat("a", 128), strings.Repeat("a", 128)},
		{"over limit", strings.Repeat("a", 129), ""},
		{"surrounding space", "  req-123\n", "req-123"},
		{"empty", "", ""},
		{"blank", " \t ", ""},
		{"internal space", "req 123", ""},
		{"internal tab", "req\t123", ""},
		{"newline injection", "req\nX-Injected: yes", ""},
		{"carriage return injection", "req\rX-Injected: yes", ""},
		{"NUL", "req\x00", ""},
		{"DEL", "req\x7f", ""},
		{"non-ASCII", "req-é", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := BoundedID(tc.id); got != tc.want {
				t.Errorf("BoundedID(%q) = %q, want %q", tc.id, got, tc.want)
			}
		})
	}
}

// An ID from a carrier is another process's text. It is kept only when it is
// short and printable.
func TestExtractDropsAnIDThatIsNotSafeToLogOrForward(t *testing.T) {
	resetConfig(t)
	Configure(Config{Enabled: true, RequestIDHeader: "X-Request-ID", CorrelationIDHeader: "X-Correlation-ID"})

	cases := []struct {
		name string
		id   string
		keep bool
	}{
		{"a plain id", "req-123", true},
		{"a uuid", "123e4567-e89b-12d3-a456-426614174000", true},
		{"an id of exactly the limit", strings.Repeat("a", maxExtractedIDLength), true},
		{"an id one over the limit", strings.Repeat("a", maxExtractedIDLength+1), false},
		{"a very long id", strings.Repeat("a", 100000), false},
		{"an id with a newline in it", "req\nX-Injected: yes", false},
		{"an id with a carriage return", "req\rabc", false},
		{"an id with a space inside", "req 1", false},
		{"an id with a tab inside", "req\t1", false},
		{"an id with a control character", "req\x00", false},
		{"an id with a non-ASCII character", "req-é", false},
		{"an id with a DEL", "req\x7f", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FromContext(Extract(context.Background(), map[string]string{
				"X-Request-ID": tc.id, "X-Correlation-ID": tc.id,
			}))
			want := Metadata{}
			if tc.keep {
				want = Metadata{RequestID: tc.id, CorrelationID: tc.id}
			}
			if got != want {
				t.Errorf("Extract kept %+v, want %+v", got, want)
			}
		})
	}
}

func TestExtractFindsAHeaderWhateverItsCase(t *testing.T) {
	resetConfig(t)
	Configure(Config{Enabled: true, RequestIDHeader: "X-Request-ID", CorrelationIDHeader: "X-Correlation-ID"})
	for _, name := range []string{"X-Request-ID", "X-Request-Id", "x-request-id", "X-REQUEST-ID"} {
		got := FromContext(Extract(context.Background(), map[string]string{name: "req-1"}))
		if got.RequestID != "req-1" {
			t.Errorf("header %q was not read: %+v", name, got)
		}
	}
}

// The five variables are read when the configuration is first needed and kept,
// and config.ReloadEnvironment makes the next use read them again. Before, only
// Configure(DefaultConfig()) did, and that pins the configuration for good, so a
// .env loaded after the first request was ignored.
func TestReloadingTheEnvironmentTakesTheTrackingSettingsFromIt(t *testing.T) {
	variables := []struct {
		name          string
		first, second string
		read          func(Config) string
	}{
		{"SCHEMAFLUX_REQUEST_TRACKING", "true", "false", func(c Config) string { return strconv.FormatBool(c.Enabled) }},
		{"SCHEMAFLUX_REQUEST_ID_STRATEGY", "auto", "uuid", func(c Config) string { return string(c.RequestIDStrategy) }},
		{"SCHEMAFLUX_CORRELATION_ID_STRATEGY", "inherit", "generate", func(c Config) string { return string(c.CorrelationIDStrategy) }},
		{"SCHEMAFLUX_REQUEST_ID_HEADER", "X-First-Request", "X-Second-Request", func(c Config) string { return c.RequestIDHeader }},
		{"SCHEMAFLUX_CORRELATION_ID_HEADER", "X-First-Correlation", "X-Second-Correlation", func(c Config) string { return c.CorrelationIDHeader }},
	}
	reloads := map[string]func(){
		"config.ReloadEnvironment": config.ReloadEnvironment,
	}
	for _, variable := range variables {
		for reloadName, reload := range reloads {
			t.Run(variable.name+" by "+reloadName, func(t *testing.T) {
				resetConfig(t)
				t.Setenv(variable.name, variable.first)
				if got := variable.read(GetConfig()); got != variable.first {
					t.Fatalf("first read = %q, want %q", got, variable.first)
				}

				t.Setenv(variable.name, variable.second)
				if got := variable.read(GetConfig()); got != variable.first {
					t.Fatalf("read after the variable changed, before any reload = %q, want the %q kept", got, variable.first)
				}

				reload()
				if got := variable.read(GetConfig()); got != variable.second {
					t.Errorf("read after %s = %q, want %q", reloadName, got, variable.second)
				}
			})
		}
	}
}

// A configuration made in code is not the environment's to replace: it wins over
// the variables, and a reload of them leaves it alone.
func TestAConfigureMadeInCodeWinsOverTheEnvironmentAndSurvivesAReload(t *testing.T) {
	resetConfig(t)
	t.Setenv("SCHEMAFLUX_REQUEST_ID_STRATEGY", "uuid")
	t.Setenv("SCHEMAFLUX_REQUEST_ID_HEADER", "X-From-Environment")
	Configure(Config{Enabled: true, RequestIDStrategy: IDStrategyTimestamp, RequestIDHeader: "X-From-Code"})

	check := func(when string) {
		t.Helper()
		got := GetConfig()
		if got.RequestIDStrategy != IDStrategyTimestamp || got.RequestIDHeader != "X-From-Code" {
			t.Errorf("%s: the configuration is %+v, want the one made in code", when, got)
		}
	}
	check("before a reload")
	config.ReloadEnvironment()
	check("after config.ReloadEnvironment")
	t.Setenv("SCHEMAFLUX_REQUEST_ID_HEADER", "X-Changed")
	config.ReloadEnvironment()
	check("after the environment changed and was reloaded")
}
