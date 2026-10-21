//go:build !js && !wasip1

package schemaflux

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/requesttracking"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// callThatWarns runs one internal operation whose first attempt fails with a
// retryable error, so that the call path logs at warn that it is retrying. It
// reports how many requests the provider received; two means the retry
// happened and the warning was due. The call runs on a client with the Logger
// given, or with none.
func callThatWarns(t *testing.T, lines ...*slog.Logger) (requests int) {
	t.Helper()
	provider := &retryingProvider{
		scriptedProvider: scriptedProvider{name: "custom", complete: func(context.Context, Request) (Response, error) {
			requests++
			if requests == 1 {
				return Response{}, &Error{Kind: ErrUnavailable, Message: "down"}
			}
			return Response{Content: `{"answer":true}`}, nil
		}},
		retries: 2,
		backoff: time.Millisecond,
	}
	client := Client{Provider: provider}
	if len(lines) > 0 {
		client.Logger = lines[0]
	}
	ctx := WithClient(context.Background(), client)
	// The answer is not what matters here, only that the first attempt failed
	// and was retried.
	_, _ = ops.Is(ctx, "a subject", "Is this a question?", types.OpOptions{Model: "test-model"})
	return requests
}

// capturedStderr runs f with os.Stderr pointing at a pipe and returns what was
// written to it.
func capturedStderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Skipf("no pipe on this platform: %v", err)
	}
	saved := os.Stderr
	os.Stderr = w
	done := make(chan string)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()
	func() {
		defer func() {
			os.Stderr = saved
			w.Close()
		}()
		f()
	}()
	out := <-done
	r.Close()
	return out
}

func TestWithTheRootLinkedACallThatWarnsWritesNothingToStandardError(t *testing.T) {
	// Not parallel: it replaces os.Stderr and the process logger, and puts both
	// back.
	t.Run("as the root's init left the logger", func(t *testing.T) {
		var requests int
		got := capturedStderr(t, func() { requests = callThatWarns(t) })
		if requests < 2 {
			t.Fatalf("the provider received %d requests: the call did not retry, so nothing was due to be logged", requests)
		}
		if got != "" {
			t.Errorf("the library wrote to standard error: %q", got)
		}
	})
	t.Run("the same call does write when the client has a logger that writes to standard error", func(t *testing.T) {
		// The control: it shows the call above would have been heard. Without
		// it, the first case passes for a call that never logs at all.
		var requests int
		got := capturedStderr(t, func() {
			requests = callThatWarns(t, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
		})
		if requests < 2 {
			t.Fatalf("the provider received %d requests", requests)
		}
		if !strings.Contains(got, "retry") {
			t.Errorf("the warning did not reach standard error, so the first case proves nothing: %q", got)
		}
	})
}

// The child process of TestAProgramLinkingTheRootIgnoresTheEnvironment runs the
// same test binary with childModeVariable set; the test then checks one thing
// and returns, and the parent reads its exit status and what it wrote.
const childModeVariable = "SCHEMAFLUX_BIND_TEST_CHILD"

// pinnedTracking is what the root's init pins: the defaults.
var pinnedTracking = requesttracking.Config{
	Enabled:               true,
	RequestIDStrategy:     requesttracking.IDStrategyAuto,
	CorrelationIDStrategy: requesttracking.CorrelationStrategyInherit,
	RequestIDHeader:       "X-Request-ID",
	CorrelationIDHeader:   "X-Correlation-ID",
}

func TestAProgramLinkingTheRootIgnoresTheEnvironment(t *testing.T) {
	if mode := os.Getenv(childModeVariable); mode != "" {
		runEnvironmentChild(t, mode)
		return
	}
	if testing.Short() {
		t.Skip("runs the test binary again as a child process")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("no path to the test binary: %v", err)
	}
	scenarios := []struct {
		name string
		mode string
		env  []string
	}{
		{
			name: "five valid settings that are not the defaults",
			mode: "different",
			env: []string{
				"SCHEMAFLUX_REQUEST_TRACKING=false",
				"SCHEMAFLUX_REQUEST_ID_STRATEGY=uuid",
				"SCHEMAFLUX_CORRELATION_ID_STRATEGY=generate",
				"SCHEMAFLUX_REQUEST_ID_HEADER=X-Other-Request",
				"SCHEMAFLUX_CORRELATION_ID_HEADER=X-Other-Correlation",
			},
		},
		{
			name: "five malformed settings",
			mode: "malformed",
			env: []string{
				"SCHEMAFLUX_REQUEST_TRACKING=perhaps",
				"SCHEMAFLUX_REQUEST_ID_STRATEGY=../../etc/passwd",
				"SCHEMAFLUX_CORRELATION_ID_STRATEGY=%s%s%s%n",
				"SCHEMAFLUX_REQUEST_ID_HEADER=bad header\r\nX-Injected: 1",
				"SCHEMAFLUX_CORRELATION_ID_HEADER=" + strings.Repeat("h", 20000),
			},
		},
		{
			name: "settings that name a strategy that needs a tracer nobody installed",
			mode: "trace",
			env: []string{
				"SCHEMAFLUX_REQUEST_ID_STRATEGY=trace",
				"SCHEMAFLUX_CORRELATION_ID_STRATEGY=request",
			},
		},
		{
			name: "a call that warns, with the log level asking for everything",
			mode: "logging",
			env: []string{
				"SCHEMAFLUX_LOG_LEVEL=debug",
				"SCHEMAFLUX_DEBUG=true",
			},
		},
	}
	for _, s := range scenarios {
		t.Run(s.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, exe, "-test.run=^TestAProgramLinkingTheRootIgnoresTheEnvironment$", "-test.count=1")
			cmd.Env = append(withoutLibraryVariables(os.Environ()), childModeVariable+"="+s.mode)
			cmd.Env = append(cmd.Env, s.env...)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("the child failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
			}
			if stderr.Len() != 0 {
				t.Errorf("the child wrote to standard error: %q", stderr.String())
			}
		})
	}
}

// withoutLibraryVariables is env without any SCHEMAFLUX_ variable, so that a
// child starts from a known environment whatever the parent's was.
func withoutLibraryVariables(env []string) []string {
	kept := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(strings.ToUpper(kv), "SCHEMAFLUX_") {
			kept = append(kept, kv)
		}
	}
	return kept
}

// runEnvironmentChild is what the child process does for one scenario.
func runEnvironmentChild(t *testing.T, mode string) {
	t.Helper()
	if mode == "logging" {
		if requests := callThatWarns(t); requests < 2 {
			t.Fatalf("the provider received %d requests: the call did not retry", requests)
		}
		return
	}
	if got := requesttracking.GetConfig(); got != pinnedTracking {
		t.Errorf("request tracking is configured as %+v, want the defaults %+v", got, pinnedTracking)
	}
	_, ids := requesttracking.Ensure(context.Background(), "", "")
	if !strings.HasPrefix(ids.RequestID, "req_") || len(ids.RequestID) != len("req_")+16 {
		t.Errorf("request id %q is not the auto form", ids.RequestID)
	}
	if ids.CorrelationID != ids.RequestID {
		t.Errorf("correlation id %q is not the request id %q: the strategy is not inherit", ids.CorrelationID, ids.RequestID)
	}
	if mode == "different" {
		// The control: the environment of this child does say something else,
		// and what the call path read is the pinned defaults all the same.
		if env := requesttracking.DefaultConfig(); env == pinnedTracking {
			t.Errorf("the environment of the child gives the defaults, so the check above proves nothing: %+v", env)
		}
	}
}
