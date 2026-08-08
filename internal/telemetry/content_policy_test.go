package telemetry

import (
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// SEC-002. Unit coverage for the Content gate itself: at each
// ContentLoggingLevel, and for levels this package was never taught about,
// Content.LogValue() must return exactly what the level promises -- no more.
//
// withContentPolicy saves and restores the package-level contentPolicy
// around a subtest, since SetContentLoggingPolicy is process-wide state (the
// same reason schemafluxtest.Install forbids t.Parallel -- these tests
// cannot run in parallel with each other either).
func withContentPolicy(t *testing.T, level types.ContentLoggingLevel, fn func()) {
	t.Helper()
	previous := ContentLoggingPolicy()
	SetContentLoggingPolicy(level)
	t.Cleanup(func() { SetContentLoggingPolicy(previous) })
	fn()
}

const marker = "MARKER-do-not-log-me-4f8c1e"

func TestContentLogValueByPolicyLevel(t *testing.T) {
	cases := []struct {
		name           string
		level          types.ContentLoggingLevel
		wantContains   string // substring the resolved value MUST contain
		mustNotContain string // substring the resolved value MUST NOT contain ("" skips)
	}{
		{
			name:           "LogNoContent hides the marker entirely",
			level:          types.LogNoContent,
			wantContains:   contentSuppressedMarker,
			mustNotContain: marker,
		},
		{
			name:           "LogMetadataOnly reports a shape, not the value",
			level:          types.LogMetadataOnly,
			wantContains:   "bytes",
			mustNotContain: marker,
		},
		{
			name:           "LogRedactedContent passes plain text through unredacted (it is not credential-shaped)",
			level:          types.LogRedactedContent,
			wantContains:   marker,
			mustNotContain: "",
		},
		{
			name:           "LogFullContent passes the value through verbatim",
			level:          types.LogFullContent,
			wantContains:   marker,
			mustNotContain: "",
		},
		{
			name:           "an out-of-range level refuses like LogNoContent, not like LogFullContent",
			level:          types.ContentLoggingLevel(200),
			wantContains:   contentSuppressedMarker,
			mustNotContain: marker,
		},
		{
			name:           "an out-of-range level one above LogFullContent still refuses",
			level:          types.ContentLoggingLevel(uint8(types.LogFullContent) + 1),
			wantContains:   contentSuppressedMarker,
			mustNotContain: marker,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withContentPolicy(t, tc.level, func() {
				got := Content(marker).LogValue().String()
				if tc.wantContains != "" && !strings.Contains(got, tc.wantContains) {
					t.Errorf("LogValue() = %q, want it to contain %q", got, tc.wantContains)
				}
				if tc.mustNotContain != "" && strings.Contains(got, tc.mustNotContain) {
					t.Errorf("LogValue() = %q, must not contain %q at level %s", got, tc.mustNotContain, tc.level)
				}
			})
		})
	}
}

// The zero types.ContentLoggingLevel is LogNoContent -- a process that never
// calls SetContentLoggingPolicy must still suppress content, matching
// DataPolicy's "nobody configured it" contract in internal/types/policy.go.
func TestContentDefaultsToNoContentUnconfigured(t *testing.T) {
	// Reset to the true zero value rather than whatever a prior test left --
	// this test's whole point is what happens when nobody ever called
	// SetContentLoggingPolicy.
	withContentPolicy(t, types.ContentLoggingLevel(0), func() {
		if got := ContentLoggingPolicy(); got != types.LogNoContent {
			t.Fatalf("zero value = %s, want LogNoContent", got)
		}
		got := Content(marker).LogValue().String()
		if strings.Contains(got, marker) {
			t.Fatalf("LogValue() = %q leaked the marker under the zero-value policy", got)
		}
	})
}

// Driving a Content value through a real Logger (not just LogValue()
// directly) at the strictest level, across every severity Debug raises,
// proves "Debug changes verbosity, not content policy" -- SEC-002's own
// phrase -- rather than just asserting it in a comment.
func TestLoggerNeverCapturesContentAtStrictestLevelRegardlessOfVerbosity(t *testing.T) {
	withContentPolicy(t, types.LogNoContent, func() {
		log := NewLoggerWithConfig(LoggerConfig{
			Level:         DebugLevel, // maximum verbosity
			Format:        "json",
			Capture:       true,
			BufferSize:    50,
			DisableStderr: true,
		})
		t.Cleanup(func() { _ = log.Close() })

		log.Debug("debug line", "prompt", Content(marker))
		log.Info("info line", "prompt", Content(marker))
		log.Warn("warn line", "prompt", Content(marker))
		log.Error("error line", "prompt", Content(marker))

		entries := log.Entries()
		if len(entries) != 4 {
			t.Fatalf("expected 4 entries, got %d", len(entries))
		}
		for _, entry := range entries {
			for key, value := range entry.Attributes {
				if s, ok := value.(string); ok && strings.Contains(s, marker) {
					t.Fatalf("entry %+v attribute %q leaked the marker", entry, key)
				}
			}
			if strings.Contains(entry.Message, marker) {
				t.Fatalf("entry message %q leaked the marker", entry.Message)
			}
		}
	})
}

// At LogFullContent -- the level a call can only reach because a
// tenant-level policy already permitted it, per Tighten's TRU-11 contract --
// the same Content value DOES reach the log. This is the control case
// proving the gate is actually gating something, not merely always-off.
func TestLoggerCapturesContentAtFullLevel(t *testing.T) {
	withContentPolicy(t, types.LogFullContent, func() {
		log := NewLoggerWithConfig(LoggerConfig{
			Level:         InfoLevel,
			Format:        "json",
			Capture:       true,
			BufferSize:    10,
			DisableStderr: true,
		})
		t.Cleanup(func() { _ = log.Close() })

		log.Info("full content line", "prompt", Content(marker))

		entries := log.Entries()
		if len(entries) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(entries))
		}
		got, _ := entries[0].Attributes["prompt"].(string)
		if got != marker {
			t.Fatalf("prompt attribute = %q, want the literal marker at LogFullContent", got)
		}
	})
}

func TestRedactLogContentScrubsCredentialShapesOnly(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"openai key", "here is sk-proj-abcdefghijklmnopqrstuvwx", "here is [redacted]"},
		{"anthropic key", "key sk-ant-abcdefghijklmnopqrstuvwx", "key [redacted]"},
		{"bearer header", `Authorization: Bearer abc123def456`, "[redacted]"},
		{"email", "contact jane.doe@example.com please", "contact [redacted] please"},
		{"plain text is untouched", "the invoice total is 42 dollars", "the invoice total is 42 dollars"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactLogContent(tc.input)
			if got != tc.want {
				t.Errorf("redactLogContent(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// ContentLoggingPolicy/SetContentLoggingPolicy must be safe for concurrent
// use -- multiple in-flight calls with different tenant policies are exactly
// the situation DataPolicy.Tighten exists for.
func TestContentLoggingPolicyConcurrentAccess(t *testing.T) {
	var wg sync.WaitGroup
	levels := []types.ContentLoggingLevel{
		types.LogNoContent, types.LogMetadataOnly, types.LogRedactedContent, types.LogFullContent,
	}
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			SetContentLoggingPolicy(levels[i%len(levels)])
		}(i)
		go func() {
			defer wg.Done()
			_ = ContentLoggingPolicy()
			_ = Content(marker).LogValue()
		}()
	}
	wg.Wait()
	// No assertion beyond "the race detector and this loop did not crash" --
	// -race is what actually verifies this; the loop above is the workload.
	SetContentLoggingPolicy(types.LogNoContent)
}

var _ slog.LogValuer = Content("")
