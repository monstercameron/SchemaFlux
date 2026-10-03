package codex

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The shell boundary.
//
// Everything above this file works on bytes; this is the only part that starts
// a process, and it is behind a function-typed seam (Provider.turn) so tests
// exercise the real request assembly, the real event parsing, and the real
// error classification without a codex binary existing.

const (
	// maxEventLineBytes bounds one JSONL line. An assistant message arrives on
	// a single line and can be long; the scanner's 64KiB default would treat a
	// longer one as malformed and skip it, which could silently lose the
	// completed event and turn a good answer into a reported failure.
	maxEventLineBytes = 8 << 20

	// maxReplyBytes bounds the answer read back from the CLI's output file.
	maxReplyBytes = 8 << 20

	// maxStderrBytes bounds what is kept from the child's stderr. It is
	// captured so the pipe cannot fill and block the child, and so it is
	// available behind ADR 0001's gate -- not so it can be printed.
	maxStderrBytes = 64 << 10

	// cancellationDrain is how long Wait may block after the context ends
	// before the child's pipes are force-closed.
	//
	// exec.CommandContext kills the process it started, but Wait keeps reading
	// until every inherited handle is closed -- and codex runs tools that
	// spawn their own children, which inherit stdout and stderr. Without this,
	// a cancelled call blocks until an orphaned grandchild exits, which defeats
	// the caller's deadline entirely: the whole point of cancelling is not to
	// wait.
	cancellationDrain = time.Second
)

// turnRequest is what one codex turn needs. It holds bytes, not files: where
// the schema and the answer live on disk is runTurn's business.
type turnRequest struct {
	model string

	// prompt goes to the child's stdin. It is never an argument -- see
	// errors.go for why that is load-bearing rather than stylistic.
	prompt []byte

	// schema is the JSON Schema the answer must match, or nil when the
	// request declared none.
	schema []byte

	// effort is the reasoning effort the caller asked for on this call, or ""
	// to use the one the provider was configured with.
	effort string
}

// turnTranscript is everything one turn produced.
type turnTranscript struct {
	// reply is the CLI's final message, read from the file -o names.
	reply []byte

	// events is the JSONL stream from stdout.
	events []byte

	// stderr is bounded and is never printed by an error's Error().
	stderr []byte

	// exitCode is the CLI's status, or -1 when it produced none.
	exitCode int
}

// turn is the seam. A nil turn on a Provider means the real shell-out.
type turn func(ctx context.Context, request turnRequest) (turnTranscript, error)

// runTurn runs the codex CLI once.
func (p *Provider) runTurn(ctx context.Context, request turnRequest) (turnTranscript, error) {
	transcript := turnTranscript{exitCode: -1}

	// A fresh empty directory as the child's working root. Combined with
	// --skip-git-repo-check and --ignore-rules it keeps the CLI from picking
	// up whatever repository the host process happens to be sitting in and
	// treating it as context for the answer.
	workspace, err := os.MkdirTemp("", "schemaflux-codex-")
	if err != nil {
		return transcript, fmt.Errorf("codex: creating a working directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(workspace) }()

	replyPath := filepath.Join(workspace, "reply.txt")

	arguments, err := p.arguments(request, workspace, replyPath)
	if err != nil {
		return transcript, err
	}

	command := exec.CommandContext(ctx, p.options.Executable, arguments...)
	command.Dir = workspace
	command.Env = environmentWithoutAPIKeys(os.Environ())
	command.Stdin = bytes.NewReader(request.prompt)

	var stdout bytes.Buffer
	stderr := &boundedBuffer{limit: maxStderrBytes}
	command.Stdout = &stdout
	command.Stderr = stderr
	command.WaitDelay = cancellationDrain

	runErr := command.Run()

	transcript.events = stdout.Bytes()
	transcript.stderr = stderr.Bytes()
	if command.ProcessState != nil {
		transcript.exitCode = command.ProcessState.ExitCode()
	}

	// The reply file is read whatever the exit status: a CLI that wrote an
	// answer and then failed on the way out still wrote an answer, and
	// discarding it because of the status would throw away work the caller
	// already paid for.
	if reply, readErr := readBounded(replyPath, maxReplyBytes); readErr == nil {
		transcript.reply = reply
	}

	if runErr != nil && transcript.exitCode < 0 {
		// No status at all: the binary could not be started, or the process
		// was killed. Distinguishable from a non-zero exit, and classified
		// differently.
		return transcript, runErr
	}
	return transcript, nil
}

// arguments builds the child's argv.
//
// The prompt is not here. The trailing "-" tells codex exec to read the task
// from stdin, which is what keeps the caller's data out of the process
// argument list -- where it would be readable by any process listing on the
// machine and could be echoed back by a CLI that prints its own invocation.
func (p *Provider) arguments(request turnRequest, workspace, replyPath string) ([]string, error) {
	arguments := []string{
		"exec",
		// No session files: this is one stateless completion, and a library
		// that litters a user's codex history with them is a library nobody
		// can reason about the disk usage of.
		"--ephemeral",
		// The CLI's own config and rules files describe how a person wants
		// their interactive agent to behave. They are not this library's
		// instructions, and letting them change a typed operation's answer
		// would make the same call return different things on two machines.
		"--ignore-user-config",
		"--ignore-rules",
		// This library asks the model for an answer, not for work. read-only
		// is the most restrictive sandbox the CLI offers.
		"--sandbox", "read-only",
		"--skip-git-repo-check",
		"--json",
	}

	if request.model != "" {
		arguments = append(arguments, "--model", request.model)
	}
	effort := strings.TrimSpace(request.effort)
	if effort == "" {
		effort = strings.TrimSpace(p.options.ReasoningEffort)
	}
	if effort != "" {
		arguments = append(arguments, "-c", fmt.Sprintf("model_reasoning_effort=%q", effort))
	}

	if len(request.schema) > 0 {
		schemaPath := filepath.Join(workspace, "output.schema.json")
		if err := os.WriteFile(schemaPath, request.schema, 0o600); err != nil {
			return nil, fmt.Errorf("codex: writing the output schema: %w", err)
		}
		arguments = append(arguments, "--output-schema", schemaPath)
	}

	arguments = append(arguments, "-o", replyPath, "-")
	return arguments, nil
}

// blockedEnvironment names the variables stripped from the child.
//
// The point of this transport is that it authenticates as the person who ran
// `codex login`. A child that inherited an API key could quietly authenticate
// as that key instead -- billing an account the caller did not choose, under
// an identity they did not intend, with nothing in the result saying which of
// the two answered. Removing the variables makes the fallback impossible
// rather than merely unlikely.
//
// Everything else is inherited, which CODEX_HOME depends on: it is where the
// login this transport exists to use actually lives.
var blockedEnvironment = map[string]bool{
	"OPENAI_API_KEY": true,
	"OPENAI_KEY":     true,
	"CODEX_API_KEY":  true,
}

// environmentWithoutAPIKeys returns environment with the key variables removed.
// The name is compared case-insensitively: Windows environment blocks are
// case-insensitive, so a variable spelled OpenAI_Key is the same variable.
func environmentWithoutAPIKeys(environment []string) []string {
	kept := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		if !found {
			// Not a NAME=VALUE pair. Passed through rather than dropped: it is
			// not something this function is in a position to interpret.
			kept = append(kept, entry)
			continue
		}
		if blockedEnvironment[strings.ToUpper(strings.TrimSpace(name))] {
			continue
		}
		kept = append(kept, entry)
	}
	return kept
}

// readBounded reads at most limit bytes from path.
func readBounded(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	var buffer bytes.Buffer
	if _, err := buffer.ReadFrom(io.LimitReader(file, limit)); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// boundedBuffer keeps the first limit bytes written to it and counts the rest
// away. It is what the child's stderr is pointed at: an unbounded buffer would
// let a chatty or looping CLI grow the host's memory without limit, and
// dropping the stream entirely would block the child once the pipe filled.
//
// It is written to by the exec package from its own goroutine, so it locks.
type boundedBuffer struct {
	mu     sync.Mutex
	limit  int
	buffer bytes.Buffer
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if remaining := b.limit - b.buffer.Len(); remaining > 0 {
		if len(p) <= remaining {
			b.buffer.Write(p)
		} else {
			b.buffer.Write(p[:remaining])
		}
	}
	// The full length is reported written either way. Reporting a short write
	// would make the exec package treat a deliberate bound as an I/O failure.
	return len(p), nil
}

func (b *boundedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buffer.Bytes()...)
}
