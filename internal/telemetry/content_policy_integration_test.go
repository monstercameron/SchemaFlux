package telemetry_test

// SEC-002 integration coverage: drives a real, exported operation
// (schemaflux.Extract) through schemafluxtest's in-process fake provider with
// a marker string planted in the input, at DebugLevel (maximum verbosity),
// and then walks every captured log entry looking for the marker. It never
// appears -- confirming, empirically rather than only by reading the call
// sites, the audit recorded in internal/telemetry/logger.go's SEC-002
// comment: none of the logger.Debug/Info/Warn/Error call sites this task can
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
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/schemafluxtest"
)

type ticketMarker struct {
	Body string `json:"body"`
}

type extractedMarker struct {
	Summary string `json:"summary"`
}

// TestRealOperationNeverLogsMarkerAtStrictestPolicy is the "driving a real
// operation with a marker string in its input" case the task's verification
// bar names explicitly.
func TestRealOperationNeverLogsMarkerAtStrictestPolicy(t *testing.T) {
	const inputMarker = "REAL-OP-MARKER-b91a7d3c-do-not-log"

	previousPolicy := telemetry.ContentLoggingPolicy()
	telemetry.SetContentLoggingPolicy(types.LogNoContent) // the production default (also the zero value)
	t.Cleanup(func() { telemetry.SetContentLoggingPolicy(previousPolicy) })

	log := telemetry.NewLoggerWithConfig(telemetry.LoggerConfig{
		Level:         telemetry.DebugLevel, // maximum verbosity: "Debug changes verbosity, not content policy"
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

// TestRealOperationMarkerAcrossAllContentLevels repeats the drive at every
// ContentLoggingLevel. Since no call site in this operation's path uses
// telemetry.Content today (the SEC-002 comment's audit), the marker must
// never appear regardless of level -- this is the level-independence half of
// the property: the Content gate only affects values explicitly wrapped in
// it, so the ambient "nothing logs content" behavior this codebase already
// has does not regress as the policy knob moves.
func TestRealOperationMarkerAcrossAllContentLevels(t *testing.T) {
	levels := []types.ContentLoggingLevel{
		types.LogNoContent,
		types.LogMetadataOnly,
		types.LogRedactedContent,
		types.LogFullContent,
	}

	for _, level := range levels {
		level := level
		t.Run(level.String(), func(t *testing.T) {
			const inputMarker = "LEVEL-SWEEP-MARKER-2c6e9f"

			previousPolicy := telemetry.ContentLoggingPolicy()
			telemetry.SetContentLoggingPolicy(level)
			t.Cleanup(func() { telemetry.SetContentLoggingPolicy(previousPolicy) })

			log := telemetry.NewLoggerWithConfig(telemetry.LoggerConfig{
				Level:         telemetry.DebugLevel,
				Format:        "json",
				Capture:       true,
				BufferSize:    200,
				DisableStderr: true,
			})
			t.Cleanup(func() { _ = log.Close() })
			previousLogger := telemetry.GetLogger()
			telemetry.SetLogger(log)
			t.Cleanup(func() { telemetry.SetLogger(previousLogger) })

			provider := schemafluxtest.New().Reply(`{"summary":"ok"}`)
			defer schemafluxtest.Install(t, provider)()

			input := ticketMarker{Body: inputMarker}
			if _, err := schemaflux.Extract[extractedMarker](input, schemaflux.NewExtractOptions()); err != nil {
				t.Fatalf("Extract failed: %v", err)
			}

			for _, entry := range log.Entries() {
				if strings.Contains(entry.Message, inputMarker) {
					t.Fatalf("level %s: log message leaked the marker: %q", level, entry.Message)
				}
				for key, value := range entry.Attributes {
					if s, ok := value.(string); ok && strings.Contains(s, inputMarker) {
						t.Fatalf("level %s: attribute %q leaked the marker: %q", level, key, s)
					}
				}
			}
		})
	}
}
