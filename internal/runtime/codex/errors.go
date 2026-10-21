package codex

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// What a failed codex turn is allowed to say.
//
// ADR 0001 withholds a provider's message and its raw body from an error's text
// alike: a provider's own words can quote the request, and a gateway's can quote
// the key. The CLI's turn.failed message is the message, and its stderr, a
// console stream, is the raw-body analogue, so both are withheld by default and
// both are reachable through the same deliberate act the rule defines --
// Detail(), or SCHEMAFLUX_ERROR_DETAIL=1, which opens both and not one of them.
// The mechanism, the environment variable and the "withheld (N bytes)" wording
// are the ones apierror.go uses, so a caller debugging a codex failure reaches
// for exactly what they reach for with any other provider. What is printed
// through either has credential shapes redacted.
//
// The prompt itself cannot reach either stream by a route this package
// controls: it is written to the child's stdin, never to argv, so a CLI that
// echoes its own arguments echoes flags, a model name, and two temporary file
// paths. TestTheChildNeverReceivesThePromptOnArgv pins that.

// Error is a codex turn that failed. It carries the CLI's own account of the
// failure without printing it.
//
// It is the Cause of the *types.ErrorKind-classified error the provider
// returns, so errors.As reaches it when a caller wants the detail and nothing
// reaches it when they only log the error.
type Error struct {
	// Kind is the classification the provider assigned, decided from the shape
	// of the failure rather than from the text below. See classifyTurn.
	Kind types.ErrorKind

	// Model is the model the turn named. This library chose it or the caller
	// pinned it; it is not the caller's data.
	Model string

	// ExitCode is the CLI's exit status, or -1 when it never ran or was
	// killed before reporting one.
	ExitCode int

	// Failure is the CLI's turn.failed message. Withheld from Error().
	Failure string

	// Stderr is the CLI's diagnostic stream, already bounded by the runner.
	// Withheld from Error().
	Stderr string
}

// Error describes the failure without quoting the CLI.
//
// It reports that detail exists and how to see it, rather than staying silent
// about it -- saying nothing would read as "the CLI explained nothing", which
// is a different and more confusing situation. apierror.go phrases the same
// choice the same way.
func (e *Error) Error() string {
	var builder strings.Builder
	builder.WriteString(e.header())

	withheld := len(strings.TrimSpace(e.Failure)) + len(strings.TrimSpace(e.Stderr))
	if withheld == 0 {
		return builder.String()
	}

	if llm.ErrorDetailEnabled() {
		var parts []string
		if message := strings.TrimSpace(e.Failure); message != "" {
			parts = append(parts, "turn.failed: "+types.RedactCredentials(message))
		}
		if stderr := strings.TrimSpace(e.Stderr); stderr != "" {
			parts = append(parts, "stderr: "+types.RedactCredentials(stderr))
		}
		if len(parts) > 0 {
			fmt.Fprintf(&builder, ": %s", strings.Join(parts, "; "))
		}
		return builder.String()
	}

	fmt.Fprintf(&builder, "; the CLI's own output is withheld (%d bytes), set %s=1 to include it",
		withheld, llm.ErrorDetailEnvVar)
	return builder.String()
}

// Detail returns the failure with the CLI's message and diagnostic stream
// included. It is the escape hatch for somebody debugging against data they
// own, and calling it is the deliberate act ADR 0001 asks for.
func (e *Error) Detail() string {
	var builder strings.Builder
	builder.WriteString(e.header())

	if message := strings.TrimSpace(e.Failure); message != "" {
		fmt.Fprintf(&builder, "\nturn.failed: %s", types.RedactCredentials(message))
	}
	if stderr := strings.TrimSpace(e.Stderr); stderr != "" {
		fmt.Fprintf(&builder, "\nstderr: %s", types.RedactCredentials(stderr))
	}
	return builder.String()
}

// String, GoString and LogValue make an Error held by value, in a struct or in a
// log group print as Error does. A value has no Error method, since that is on the
// pointer, so fmt and slog would otherwise print Failure and Stderr.
func (e Error) String() string { return (&e).Error() }

// GoString is what %#v prints.
func (e Error) GoString() string { return (&e).Error() }

// LogValue is what slog logs for an Error that is not logged as an error.
func (e Error) LogValue() slog.Value { return slog.StringValue((&e).Error()) }

// MarshalJSON writes the model, the exit status and the lengths of what was
// withheld, and the CLI's words only while the gate is open. Without it
// encoding/json would write Failure and Stderr, which is what a caller who
// marshals the classified error, whose Cause this is, would get.
func (e Error) MarshalJSON() ([]byte, error) {
	view := struct {
		Kind         string `json:"kind"`
		Model        string `json:"model,omitempty"`
		ExitCode     int    `json:"exit_code"`
		FailureBytes int    `json:"failure_bytes,omitempty"`
		StderrBytes  int    `json:"stderr_bytes,omitempty"`
		Failure      string `json:"failure,omitempty"`
		Stderr       string `json:"stderr,omitempty"`
	}{
		Kind:         e.Kind.String(),
		Model:        e.Model,
		ExitCode:     e.ExitCode,
		FailureBytes: len(strings.TrimSpace(e.Failure)),
		StderrBytes:  len(strings.TrimSpace(e.Stderr)),
	}
	if llm.ErrorDetailEnabled() {
		view.Failure = types.RedactCredentials(strings.TrimSpace(e.Failure))
		view.Stderr = types.RedactCredentials(strings.TrimSpace(e.Stderr))
	}
	return json.Marshal(view)
}

// header is the part of the text that is the library's own: the model and the
// exit status.
func (e *Error) header() string {
	var builder strings.Builder
	builder.WriteString("codex CLI turn failed")
	if e.Model != "" {
		fmt.Fprintf(&builder, " (model %s)", e.Model)
	}
	if e.ExitCode >= 0 {
		fmt.Fprintf(&builder, ", exit status %d", e.ExitCode)
	}
	return builder.String()
}

// classified wraps a codex failure in the shared taxonomy.
//
// The Message is always set, and always to a string this library wrote, which
// is what keeps types.OperationError.Error() from falling through to printing
// the Cause -- see its implementation, where the cause is appended only when
// there is no message. So the classified error prints a kind and a sentence,
// and the detail stays on the value.
func classified(kind types.ErrorKind, model, message string, cause *Error) *types.OperationError {
	if cause != nil {
		cause.Kind = kind
		if cause.Model == "" {
			cause.Model = model
		}
	}

	operationErr := &types.OperationError{
		Kind:     kind,
		Provider: providerName,
		Model:    model,
		Message:  message,
	}
	if cause != nil {
		operationErr.Cause = cause
	}
	return operationErr
}

// classifyTurn decides what a failed turn was, structurally.
//
// It deliberately does not read the CLI's message to decide. Matching a
// vendor's prose to pick a retry disposition is the defect P-007 removed from
// this library's retry classifier -- a 500 whose body happened to contain
// "invalid_request_error" was classified permanent and lost every retry it was
// entitled to -- and the CLI's text is exactly that kind of prose. What is
// structural is whether a completed event arrived, whether the process ran at
// all, and whether the caller's context ended.
//
// The consequence is that a permanently-doomed turn (an unusable schema, say)
// is retried under the caller's budget rather than failed fast. That is the
// trade AGENTS.md already states for KindUnknown: the attempt budget bounds
// what being wrong costs, and failing fast on a guess costs a caller retries
// they were entitled to.
func classifyTurn(result outcome, exitCode int) (types.ErrorKind, string) {
	switch {
	case exitCode < 0:
		// The process never produced a status: it could not be started, or it
		// was killed. Not the caller's request's fault.
		return types.KindProviderUnavailable, "the codex CLI did not run to completion"
	case result.failure != "":
		return types.KindProviderUnavailable, "the codex CLI reported a failed turn"
	case !result.completed:
		return types.KindProviderUnavailable, "the codex CLI ended before it completed a turn"
	default:
		return types.KindProviderUnavailable, "the codex turn produced no usable answer"
	}
}
