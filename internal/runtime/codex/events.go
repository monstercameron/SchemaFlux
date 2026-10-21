package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"math"
	"strings"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// The event stream `codex exec --json` writes to stdout: one JSON object per
// line. Two of its event types matter here.
//
//   - "turn.completed" carries the token usage and is the only thing that says
//     the turn actually finished. Its absence is a failure, not a success with
//     nothing measured -- see Complete, and AGENTS.md's "never fail open".
//   - "turn.failed" carries the CLI's own explanation. It is read for the fact
//     that it exists; what it says is vendor prose and is handled under the
//     rules ADR 0001 sets for exactly that (see errors.go).
//
// Every other event type is ignored rather than rejected. The CLI adds events
// between releases, and a parser that fails on an event it has not been taught
// about would break on a codex upgrade that changed nothing this library uses.

// completedEvent is the shape of the one event this library reads figures from.
//
// The counts are int64 on the wire and are narrowed deliberately below --
// types.TokenUsage counts in int, and a value that cannot be represented is
// dropped rather than wrapped into a small or negative number that would look
// like a measurement.
type completedEvent struct {
	Type  string `json:"type"`
	Usage struct {
		InputTokens       *int64 `json:"input_tokens"`
		CachedInputTokens *int64 `json:"cached_input_tokens"`
		OutputTokens      *int64 `json:"output_tokens"`
		ReasoningTokens   *int64 `json:"reasoning_output_tokens"`
	} `json:"usage"`
}

// failedEvent is read for its existence and its message.
type failedEvent struct {
	Type  string `json:"type"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// outcome is what one codex turn's event stream says happened.
type outcome struct {
	// completed reports that a "turn.completed" event arrived. Nothing else
	// makes a turn a success: a zero exit code with no completed event is a
	// CLI that stopped early, and treating it as an empty answer would be the
	// fail-open this library spends most of its task list removing.
	completed bool

	// usageReported distinguishes "the turn reported no counts" from "the turn
	// counted zero". types.TokenUsage has no field for the difference -- every
	// count is an int -- so the distinction lives here, and the one place it
	// changes behaviour is that nothing derives a total from counts nobody
	// reported. Zero is what an unmeasured turn reports downstream, and cost
	// is left unpriced rather than claimed to be nothing (see EstimateCost).
	usageReported bool

	usage types.TokenUsage

	// failure is the CLI's own explanation, kept out of any error string by
	// default. See errors.go.
	failure string
}

// scanEvents reads the JSONL stream.
//
// A line that is not JSON, or is JSON of a shape this does not recognise, is
// skipped. That is not leniency about malformed output -- the completed event
// is still required before the turn counts as a success -- it is tolerance of
// event types this library does not consume.
func scanEvents(stream []byte) outcome {
	var result outcome

	scanner := bufio.NewScanner(bytes.NewReader(stream))
	// The default 64KiB line limit is too small for an event stream that can
	// carry a long assistant message on one line. A truncated line would parse
	// as malformed and be skipped, so the completed event could be silently
	// lost and a good turn reported as a failure.
	scanner.Buffer(make([]byte, 0, 64*1024), maxEventLineBytes)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}

		var kind struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(line, &kind) != nil {
			continue
		}

		switch kind.Type {
		case "turn.completed":
			var event completedEvent
			if json.Unmarshal(line, &event) != nil {
				continue
			}
			result.completed = true
			usage, reported := usageFrom(event)
			if reported {
				result.usage = usage
				result.usageReported = true
			}
		case "turn.failed":
			var event failedEvent
			if json.Unmarshal(line, &event) != nil {
				continue
			}
			if result.failure == "" {
				result.failure = strings.TrimSpace(event.Error.Message)
			}
		}
	}

	return result
}

// usageFrom maps the CLI's counts onto this library's shape, reporting whether
// anything was actually measured.
//
// The pointers matter: a missing field and a reported zero are different facts,
// and only the second is a measurement. A total is derived only from counts
// that were reported -- summing two absent fields into a total of zero would
// manufacture the one figure nobody stated.
func usageFrom(event completedEvent) (types.TokenUsage, bool) {
	input, hasInput := narrow(event.Usage.InputTokens)
	output, hasOutput := narrow(event.Usage.OutputTokens)
	cached, hasCached := narrow(event.Usage.CachedInputTokens)
	reasoning, hasReasoning := narrow(event.Usage.ReasoningTokens)

	if !hasInput && !hasOutput && !hasCached && !hasReasoning {
		return types.TokenUsage{}, false
	}

	usage := types.TokenUsage{
		PromptTokens:     input,
		CompletionTokens: output,
		InputTokens:      input,
		OutputTokens:     output,
		CachedTokens:     cached,
		ReasoningTokens:  reasoning,
	}
	if hasInput || hasOutput {
		usage.TotalTokens = input + output
	}

	// CacheWriteTokens is left at zero and not inferred. The CLI does not
	// report it, and a cache write is billed differently from a read -- a
	// guess here would land in cost accounting as though it had been measured.

	return usage, true
}

// narrow converts a reported count to int, reporting whether the value is one
// this library can carry. A negative count is not a measurement, and a value
// past the int range would wrap into one that looks like it is.
func narrow(value *int64) (int, bool) {
	if value == nil || *value < 0 || *value > math.MaxInt32 {
		return 0, false
	}
	return int(*value), true
}
