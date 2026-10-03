package ops

import (
	"context"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/telemetry"
	"github.com/monstercameron/schemaflux/internal/types"
)

// Where the lines of the call path itself go: the request's start and finish,
// its retries, its refusals. A line written where a call's context is in reach
// goes to the logger of the snapshot the call pinned; with no snapshot it goes
// to the process logger, as it always did; and a snapshot whose logger discards
// writes nothing anywhere.

// loggedCall is one call of CallLLM that writes lines, and the lines it writes
// when its logger sees every level.
type loggedCall struct {
	name   string
	script []func() (llm.CompletionResponse, error)
	// retries is the provider's own retry policy.
	retries int
	// budget is a client budget that refuses, for the call refused before it
	// is sent; it only exists on a snapshot.
	refused bool
	// lines are what the call writes, one "LEVEL message" to a line.
	lines []string
}

// loggedAnswer is an answer from a model with a price, so that the call writes
// no note about a model nobody priced: that note is the pricing package's, and
// is written once per model for the whole process.
func loggedAnswer(content string) func() (llm.CompletionResponse, error) {
	return func() (llm.CompletionResponse, error) {
		return llm.CompletionResponse{
			Content: content, Model: "gpt-5.6-luna", Provider: "openai", FinishReason: "stop",
			Usage: types.TokenUsage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110},
		}, nil
	}
}

func loggedCalls() []loggedCall {
	return []loggedCall{
		{
			name:   "an answer",
			script: []func() (llm.CompletionResponse, error){loggedAnswer("ok")},
			lines:  []string{"DEBUG LLM request started", "DEBUG LLM request completed"},
		},
		{
			name:    "a failure that is retried and then an answer",
			script:  []func() (llm.CompletionResponse, error){failingWith(llm.NewAPIError("acme", "m", 500, "boom")), loggedAnswer("ok")},
			retries: 1,
			lines:   []string{"DEBUG LLM request started", "WARN LLM request retry scheduled", "DEBUG LLM request completed"},
		},
		{
			name:   "a failure that is not retried",
			script: []func() (llm.CompletionResponse, error){failingWith(llm.NewAPIError("acme", "m", 401, "no"))},
			lines:  []string{"DEBUG LLM request started", "ERROR LLM request failed"},
		},
		{
			name:    "retries that run out",
			script:  []func() (llm.CompletionResponse, error){failingWith(llm.NewAPIError("acme", "m", 503, "down"))},
			retries: 2,
			lines: []string{
				"DEBUG LLM request started", "WARN LLM request retry scheduled", "WARN LLM request retry scheduled", "ERROR LLM request failed",
			},
		},
		{
			name:    "a request the client's budget refuses",
			script:  []func() (llm.CompletionResponse, error){loggedAnswer("ok")},
			refused: true,
			lines:   []string{"ERROR Refusing the request: budget exhausted"},
		},
	}
}

// run makes the call under ctx.
func (c loggedCall) run(ctx context.Context) {
	provider := &scriptedAnswers{callerSuppliedProvider: callerSuppliedProvider{name: "acme"}, script: c.script, retries: c.retries}
	_, _ = CallLLM(ctx, provider, "system", "user", types.OpOptions{Model: "asked"})
}

// snapshot is the snapshot the call runs under, with the logger it is given.
func (c loggedCall) snapshot(log *logger.Logger) *ExecConfig {
	cfg := &ExecConfig{Logger: log}
	if c.refused {
		cfg.Budget = refusingLedger{}
	}
	return cfg
}

// freshCacheReadTracking empties the count of how often each prompt prefix has
// been sent, for the length of a test: the note about a prefix that is never
// read from the provider's cache is due after the third call with the same key,
// and the calls of the tests before this one would make it due in this one.
func freshCacheReadTracking(t *testing.T) {
	t.Helper()
	resetCacheReadTracking()
	t.Cleanup(resetCacheReadTracking)
}

// lockedBuffer is where a logger writes when a test reads what reached the
// process's own output.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// processLogging replaces the process logger for the length of a test with one
// that keeps its history and writes to an output the test can read, and returns
// both. Nothing is filtered: a line another part of the library writes to the
// process logger while a call runs is a line the test sees.
func processLogging(t *testing.T) (lines func() []string, written *lockedBuffer) {
	t.Helper()
	previous := logger.GetLogger()
	written = &lockedBuffer{}
	process := logger.ConfigureLogger(logger.LoggerConfig{
		Level: logger.DebugLevel, Format: "text", Output: written, Capture: true, BufferSize: 10000,
	})
	config.SetDebugMode(true)
	t.Cleanup(func() {
		config.SetDebugMode(false)
		logger.SetLogger(previous)
	})
	return func() []string { return loggedMessagesOf(process) }, written
}

// standardError is what is written to os.Stderr directly, by anything, between
// the call and the function it returns, which reads it. It catches a line that
// bypasses every logger.
func standardError(t *testing.T) func() string {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stderr
	os.Stderr = file
	restore := sync.OnceFunc(func() { os.Stderr = saved })
	t.Cleanup(func() {
		restore()
		_ = file.Close()
	})
	return func() string {
		restore()
		data, err := os.ReadFile(file.Name())
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
}

// With no client anywhere the call path writes to the process logger, the lines
// it always wrote, and nothing else is changed by there being a second way for
// a line to go somewhere.
func TestOldRootLoggingUnchanged(t *testing.T) {
	for _, call := range loggedCalls() {
		if call.refused {
			// A refusal by a client's budget has no client here.
			continue
		}
		t.Run(call.name, func(t *testing.T) {
			withDefaultExec(t, nil)
			swapNoProvider(t)
			freshCacheReadTracking(t)
			lines, _ := processLogging(t)

			call.run(context.Background())

			if got := withoutCallPathLines(lines()); !slices.Equal(got, call.lines) {
				t.Errorf("the process logger received\n  %q\nwant\n  %q", got, call.lines)
			}
		})
	}
}

// A call's lines go to the logger of the snapshot pinned for the call, and none
// of them to the process logger.
func TestLinesGoToTheSnapshotsLogger(t *testing.T) {
	for _, call := range loggedCalls() {
		t.Run(call.name, func(t *testing.T) {
			withDefaultExec(t, nil)
			swapNoProvider(t)
			freshCacheReadTracking(t)
			process, _ := processLogging(t)
			client := clientCaptureLogger()
			ctx := WithExecConfig(context.Background(), call.snapshot(client))

			call.run(ctx)

			if got := withoutCallPathLines(loggedMessagesOf(client)); !slices.Equal(got, call.lines) {
				t.Errorf("the snapshot's logger received\n  %q\nwant\n  %q", got, call.lines)
			}
			if got := process(); len(got) != 0 {
				t.Errorf("the process logger received lines meant for the snapshot's: %q", got)
			}
		})
	}
	t.Run("the snapshot pinned for the call, not a later default", func(t *testing.T) {
		withDefaultExec(t, nil)
		swapNoProvider(t)
		freshCacheReadTracking(t)
		process, _ := processLogging(t)
		pinned, later := clientCaptureLogger(), clientCaptureLogger()
		ctx, _ := PinExec(WithExecConfig(context.Background(), &ExecConfig{Logger: pinned}))
		SetDefaultExec(&ExecConfig{Logger: later})
		t.Cleanup(func() { SetDefaultExec(nil) })

		loggedCalls()[0].run(ctx)

		if len(pinned.Entries()) == 0 || len(later.Entries()) != 0 || len(process()) != 0 {
			t.Errorf("pinned %d lines, a later default %d, the process %d; want the pinned snapshot's alone",
				len(pinned.Entries()), len(later.Entries()), len(process()))
		}
	})
	t.Run("the default snapshot's logger when the context carries none", func(t *testing.T) {
		swapNoProvider(t)
		freshCacheReadTracking(t)
		process, _ := processLogging(t)
		client := clientCaptureLogger()
		withDefaultExec(t, &ExecConfig{Logger: client})

		loggedCalls()[0].run(context.Background())

		if len(client.Entries()) == 0 || len(process()) != 0 {
			t.Errorf("the default's logger received %d lines and the process logger %d", len(client.Entries()), len(process()))
		}
	})
}

// A snapshot whose logger discards writes nothing: not to the process logger,
// not to the process's output, not to standard error.
func TestNoLoggerNoLines(t *testing.T) {
	for _, call := range loggedCalls() {
		t.Run(call.name, func(t *testing.T) {
			withDefaultExec(t, nil)
			swapNoProvider(t)
			freshCacheReadTracking(t)
			process, written := processLogging(t)
			stderr := standardError(t)
			ctx := WithExecConfig(context.Background(), call.snapshot(telemetry.Discard()))

			call.run(ctx)

			if got := process(); len(got) != 0 {
				t.Errorf("the process logger received lines although the client discards: %q", got)
			}
			if got := written.String(); got != "" {
				t.Errorf("the process logger's output received %q", got)
			}
			if got := stderr(); got != "" {
				t.Errorf("standard error received %q", got)
			}
		})
	}
	t.Run("the same calls under no client do write, so the capture is not blind", func(t *testing.T) {
		withDefaultExec(t, nil)
		swapNoProvider(t)
		freshCacheReadTracking(t)
		process, written := processLogging(t)

		loggedCalls()[0].run(context.Background())

		if len(process()) == 0 || written.String() == "" {
			t.Errorf("a call under no client wrote %d captured lines and %d bytes of output", len(process()), len(written.String()))
		}
	})
}

// The note that a prompt prefix which keeps repeating is never read from the
// provider's cache is the call's own line: it goes where the call's other lines
// go, and under a client that discards it is not written at all.
func TestTheCacheReadNoteFollowsTheCallsLogger(t *testing.T) {
	const note = "WARN prompt cache is reporting no cached tokens for a prefix that keeps repeating"
	repeat := func(ctx context.Context) {
		call := loggedCalls()[0]
		for range cacheReadWarnAfter {
			call.run(ctx)
		}
	}
	t.Run("a snapshot's logger receives it and the process logger does not", func(t *testing.T) {
		withDefaultExec(t, nil)
		swapNoProvider(t)
		freshCacheReadTracking(t)
		process, _ := processLogging(t)
		client := clientCaptureLogger()

		repeat(WithExecConfig(context.Background(), &ExecConfig{Logger: client}))

		if !slices.Contains(loggedMessagesOf(client), note) {
			t.Errorf("the snapshot's logger did not receive the note: %q", loggedMessagesOf(client))
		}
		if got := process(); len(got) != 0 {
			t.Errorf("the process logger received %q", got)
		}
	})
	t.Run("a snapshot that discards writes nothing", func(t *testing.T) {
		withDefaultExec(t, nil)
		swapNoProvider(t)
		freshCacheReadTracking(t)
		process, written := processLogging(t)
		stderr := standardError(t)

		repeat(WithExecConfig(context.Background(), &ExecConfig{Logger: telemetry.Discard()}))

		if got := process(); len(got) != 0 || written.String() != "" || stderr() != "" {
			t.Errorf("the process logger received %q, its output %q", got, written.String())
		}
	})
	t.Run("with no client it goes to the process logger", func(t *testing.T) {
		withDefaultExec(t, nil)
		swapNoProvider(t)
		freshCacheReadTracking(t)
		process, _ := processLogging(t)

		repeat(context.Background())

		if !slices.Contains(process(), note) {
			t.Errorf("the process logger did not receive the note: %q", process())
		}
	})
}
