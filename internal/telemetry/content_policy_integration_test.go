package telemetry_test

// SEC-002 integration coverage: drives a real, exported operation
// (schemaflux.Extract) through schemafluxtest's in-process fake provider with
// a marker string planted in the input, at DebugLevel (maximum verbosity),
// and then walks every captured log entry looking for the marker. It never
// appears -- confirming, empirically rather than only by reading the call
// sites, that none of the logger.Debug/Info/Warn/Error call sites this task can
// reach (client.go, internal/ops/complete.go, internal/ops/redact_llm.go,
// internal/ops/llm_helper.go) puts prompt or response content into a log
// line today.
//
// This is a separate (_test) package, not `package telemetry`, because it
// needs schemaflux (the root package) and schemafluxtest, both of which
// import internal/telemetry transitively (via internal/logger) -- importing
// them from an internal test file in package telemetry would be a real
// import cycle; importing them from an external test package is not, since
// telemetry_test is not part of telemetry's own compiled package.

import (
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/telemetry"
	"github.com/monstercameron/schemaflux/schemafluxtest"
)

type ticketMarker struct {
	Body string `json:"body"`
}

type extractedMarker struct {
	Summary string `json:"summary"`
}

// TestRealOperationNeverLogsMarker is the "driving a real
// operation with a marker string in its input" case the task's verification
// bar names explicitly.
func TestRealOperationNeverLogsMarker(t *testing.T) {
	const inputMarker = "REAL-OP-MARKER-b91a7d3c-do-not-log"

	log := telemetry.NewLoggerWithConfig(telemetry.LoggerConfig{
		Level:         telemetry.DebugLevel, // maximum verbosity
		Format:        "json",
		Capture:       true,
		BufferSize:    200,
		DisableStderr: true,
	})
	t.Cleanup(func() { _ = log.Close() })
	previousLogger := telemetry.GetLogger()
	telemetry.SetLogger(log)
	t.Cleanup(func() { telemetry.SetLogger(previousLogger) })

	provider := schemafluxtest.New().Reply(`{"summary":"a summary that does not echo the input"}`)
	defer schemafluxtest.Install(t, provider)()

	input := ticketMarker{Body: "please handle this: " + inputMarker}
	result, err := schemaflux.Extract[extractedMarker](input, schemaflux.NewExtractOptions())
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}
	if strings.Contains(result.Summary, inputMarker) {
		t.Fatalf("test setup issue: the scripted reply should not itself contain the marker")
	}

	entries := log.Entries()
	if len(entries) == 0 {
		t.Fatalf("expected at least one captured log entry driving a real Extract call, got 0")
	}

	for _, entry := range entries {
		if strings.Contains(entry.Message, inputMarker) {
			t.Fatalf("log message %q leaked the input marker", entry.Message)
		}
		for key, value := range entry.Attributes {
			if s, ok := value.(string); ok && strings.Contains(s, inputMarker) {
				t.Fatalf("log entry attribute %q = %q leaked the input marker", key, s)
			}
		}
	}
}
