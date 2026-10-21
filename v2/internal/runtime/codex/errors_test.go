package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

const (
	errorsFailureMarker = "FAILURE-MARKER-5d0c1e"
	errorsStderrMarker  = "STDERR-MARKER-91ab34"
	errorsKeyShape      = "sk-proj-abcdefghijklmnopqrstuvwxyz0123456789" // secret-scan: allow
)

func errorsSample() *Error {
	return &Error{
		Kind:     types.KindProviderUnavailable,
		Model:    "gpt-5.6-sol",
		ExitCode: 1,
		Failure:  "refused near " + errorsFailureMarker + " with " + errorsKeyShape,
		Stderr:   "invoked with " + errorsStderrMarker,
	}
}

// Everything a caller can turn the error into, with the gate closed, and then
// open: one gate, and it opens both the CLI's message and its stderr.
func TestTheGateOpensTheCLIsMessageAndItsStderrTogether(t *testing.T) {
	cases := []struct {
		name string
		gate string
		open bool
	}{
		{"unset", "", false},
		{"zero", "0", false},
		{"false", "false", false},
		{"one", "1", true},
		{"true", "true", true},
		{"upper case true", "TRUE", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(llm.ErrorDetailEnvVar, tc.gate)
			failure := errorsSample()
			text := failure.Error()
			hasFailure, hasStderr := strings.Contains(text, errorsFailureMarker), strings.Contains(text, errorsStderrMarker)
			if hasFailure != tc.open || hasStderr != tc.open {
				t.Errorf("open = %v, but the text carries failure %v and stderr %v: %s", tc.open, hasFailure, hasStderr, text)
			}
			if strings.Contains(text, errorsKeyShape) {
				t.Errorf("a credential shape reached the text: %s", text)
			}
			if !tc.open && !strings.Contains(text, "withheld (") {
				t.Errorf("the closed text does not say what it withheld: %s", text)
			}
			detail := failure.Detail()
			if !strings.Contains(detail, errorsFailureMarker) || !strings.Contains(detail, errorsStderrMarker) || strings.Contains(detail, errorsKeyShape) {
				t.Errorf("Detail() = %q", detail)
			}
		})
	}
}

func TestAFailureHeldInAnyWayPrintsAsSafeText(t *testing.T) {
	t.Setenv(llm.ErrorDetailEnvVar, "")
	failure := errorsSample()
	classified := classified(types.KindProviderUnavailable, "gpt-5.6-sol", "the codex CLI reported a failed turn", failure)

	renderings := map[string]string{
		"%v":                 fmt.Sprintf("%v", failure),
		"%+v":                fmt.Sprintf("%+v", failure),
		"%#v":                fmt.Sprintf("%#v", failure),
		"value %v":           fmt.Sprintf("%v", *failure),
		"value %+v":          fmt.Sprintf("%+v", *failure),
		"value %#v":          fmt.Sprintf("%#v", *failure),
		"classified %v":      fmt.Sprintf("%v", classified),
		"classified %+v":     fmt.Sprintf("%+v", classified),
		"classified cause":   fmt.Sprintf("%+v", errors.Unwrap(classified)),
		"classified Error()": classified.Error(),
	}
	for label, value := range map[string]any{
		"json error":      failure,
		"json value":      *failure,
		"json classified": classified,
		"json holder":     struct{ Err error }{failure},
	} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		renderings[label] = string(encoded)
	}
	for name, handler := range map[string]func(*bytes.Buffer) slog.Handler{
		"slog text": func(out *bytes.Buffer) slog.Handler { return slog.NewTextHandler(out, nil) },
		"slog json": func(out *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(out, nil) },
	} {
		var out bytes.Buffer
		logger := slog.New(handler(&out))
		logger.Error("turn", "error", failure)
		logger.Error("turn", "value", *failure)
		logger.Error("turn", "classified", classified)
		renderings[name] = out.String()
	}

	for label, text := range renderings {
		for _, marker := range []string{errorsFailureMarker, errorsStderrMarker, errorsKeyShape} {
			if strings.Contains(text, marker) {
				t.Errorf("%s carries %s: %s", label, marker, text)
			}
		}
	}
}

func TestTheJSONOfAFailureNamesLengthsAndOpensWithTheGate(t *testing.T) {
	failure := &Error{Kind: types.KindProviderUnavailable, Model: "m", ExitCode: 2, Failure: "abc", Stderr: "defgh"}

	t.Setenv(llm.ErrorDetailEnvVar, "")
	closed, _ := json.Marshal(failure)
	if want := `{"kind":"provider unavailable","model":"m","exit_code":2,"failure_bytes":3,"stderr_bytes":5}`; string(closed) != want {
		t.Errorf("closed:\n got %s\nwant %s", closed, want)
	}

	t.Setenv(llm.ErrorDetailEnvVar, "1")
	open, _ := json.Marshal(failure)
	if !strings.Contains(string(open), `"failure":"abc"`) || !strings.Contains(string(open), `"stderr":"defgh"`) {
		t.Errorf("open: %s", open)
	}
}

func TestTheTextOfAFailureIsExact(t *testing.T) {
	cases := []struct {
		name   string
		err    *Error
		closed string
		open   string
	}{
		{"nothing to withhold", &Error{Model: "m", ExitCode: 1}, "codex CLI turn failed (model m), exit status 1", "codex CLI turn failed (model m), exit status 1"},
		{"no exit status", &Error{ExitCode: -1}, "codex CLI turn failed", "codex CLI turn failed"},
		{"a message", &Error{ExitCode: 1, Failure: "boom"},
			"codex CLI turn failed, exit status 1; the CLI's own output is withheld (4 bytes), set SCHEMAFLUX_ERROR_DETAIL=1 to include it",
			"codex CLI turn failed, exit status 1: turn.failed: boom"},
		{"stderr only", &Error{ExitCode: 1, Stderr: "oops"},
			"codex CLI turn failed, exit status 1; the CLI's own output is withheld (4 bytes), set SCHEMAFLUX_ERROR_DETAIL=1 to include it",
			"codex CLI turn failed, exit status 1: stderr: oops"},
		{"both", &Error{Model: "m", ExitCode: 3, Failure: "boom", Stderr: "oops"},
			"codex CLI turn failed (model m), exit status 3; the CLI's own output is withheld (8 bytes), set SCHEMAFLUX_ERROR_DETAIL=1 to include it",
			"codex CLI turn failed (model m), exit status 3: turn.failed: boom; stderr: oops"},
		{"white space only", &Error{ExitCode: 1, Failure: "  \n", Stderr: "\t"}, "codex CLI turn failed, exit status 1", "codex CLI turn failed, exit status 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(llm.ErrorDetailEnvVar, "")
			if got := tc.err.Error(); got != tc.closed {
				t.Errorf("closed:\n got %s\nwant %s", got, tc.closed)
			}
			t.Setenv(llm.ErrorDetailEnvVar, "1")
			if got := tc.err.Error(); got != tc.open {
				t.Errorf("open:\n got %s\nwant %s", got, tc.open)
			}
		})
	}
}
