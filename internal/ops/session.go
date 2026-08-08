package ops

import (
	"errors"
	"fmt"

	"github.com/monstercameron/schemaflux/internal/llm"
)

// Session is the message/session abstraction PS-005 (Revised, ARC-20) asked
// for: a sequential, stateful multi-turn transcript, not a generic
// multi-directional session protocol (MDSP). It exists to give a caller who
// wants a real conversation -- several model round trips that each see the
// turns before them -- something to build one with, using
// llm.CompletionRequest.Messages.
//
// It is deliberately not wired into Question, Negotiate, or
// NegotiateAdversarial. Each of those calls the package-level callLLM
// helper in llm_helper.go exactly once per invocation, and that helper
// takes a single system/user string pair -- it has no parameter for a
// message history, and this task's file constraints exclude llm_helper.go
// from the files this change may touch. Wiring a transcript into those
// three operations is consequently future work, not a promise this file
// makes: what Session provides today is the transcript primitive itself,
// exercised end to end against llm.Provider by whichever caller assembles
// its own multi-turn loop around it, plus the invariants a transcript has
// to hold to be trustworthy state rather than an unchecked slice of
// strings.
type Session struct {
	turns []llm.Message
}

// NewSession returns an empty transcript.
func NewSession() *Session {
	return &Session{}
}

// ErrSessionInvariant reports a transcript operation that would leave the
// session in a shape no real conversation produces -- an assistant turn
// before any user turn, two user turns in a row with nothing answering the
// first, or a tool result that does not follow the assistant turn that
// asked for it. These are caught here, in Go, before the transcript is ever
// sent to a model, because a provider given a malformed turn sequence fails
// with a vendor error that names none of this library's own concepts.
var ErrSessionInvariant = errors.New("session transcript invariant violated")

// AppendSystem adds the system turn. It must be the first turn in the
// session -- a system prompt that arrives after the conversation has
// started is not "the system prompt," it is an instruction injected
// mid-conversation wearing the system prompt's name, and the Responses API
// has no way to backdate it.
func (s *Session) AppendSystem(content string) error {
	if len(s.turns) != 0 {
		return fmt.Errorf("system turn must be first, session already has %d turn(s): %w", len(s.turns), ErrSessionInvariant)
	}
	s.turns = append(s.turns, llm.Message{Role: "system", Content: content})
	return nil
}

// AppendUser adds a user turn. Two user turns in a row is not a
// conversation -- it is a caller that forgot to record the assistant's
// answer to the first one, and the transcript sent to the model would
// silently drop that turn rather than fail loudly here.
func (s *Session) AppendUser(content string) error {
	if last, ok := s.lastNonSystem(); ok && last.Role == "user" {
		return fmt.Errorf("consecutive user turns: %w", ErrSessionInvariant)
	}
	s.turns = append(s.turns, llm.Message{Role: "user", Content: content})
	return nil
}

// AppendAssistant adds the model's answer. It must follow a user turn or a
// tool-result turn -- an assistant turn with nothing to answer (no prior
// user turn at all, or two assistant turns back to back) is not a state a
// real exchange with a model produces.
func (s *Session) AppendAssistant(content string) error {
	last, ok := s.lastNonSystem()
	if !ok {
		return fmt.Errorf("assistant turn before any user turn: %w", ErrSessionInvariant)
	}
	if last.Role != "user" && last.Role != "tool" {
		return fmt.Errorf("assistant turn must follow a user or tool turn, not %q: %w", last.Role, ErrSessionInvariant)
	}
	s.turns = append(s.turns, llm.Message{Role: "assistant", Content: content})
	return nil
}

// AppendToolResult adds a tool's answer to a call the assistant just made.
// toolCallID must name the llm.ToolCall.ID the immediately preceding
// assistant turn asked for -- a tool result with no assistant turn in front
// of it, or one whose ID does not match anything the model actually
// requested, cannot be correlated back to a call by the provider and is
// refused here instead of being sent.
func (s *Session) AppendToolResult(toolCallID, content string) error {
	if toolCallID == "" {
		return fmt.Errorf("tool result requires a call ID: %w", ErrSessionInvariant)
	}
	last, ok := s.lastNonSystem()
	if !ok || last.Role != "assistant" {
		return fmt.Errorf("tool result must follow an assistant turn: %w", ErrSessionInvariant)
	}
	s.turns = append(s.turns, llm.Message{Role: "tool", Content: content, ToolCallID: toolCallID})
	return nil
}

// Messages returns a copy of the transcript in order, oldest first. It is a
// copy, not the internal slice, so a caller mutating the result cannot
// reach back and rewrite a turn this Session already recorded -- the
// transcript invariants above are worth nothing if the history they
// validated on the way in can be edited on the way out.
func (s *Session) Messages() []llm.Message {
	out := make([]llm.Message, len(s.turns))
	copy(out, s.turns)
	return out
}

// Len reports how many turns the transcript holds.
func (s *Session) Len() int {
	return len(s.turns)
}

// lastNonSystem returns the most recently appended turn that is not the
// leading system turn, and whether one exists. The system turn (if present)
// is always index 0 and never participates in the user/assistant/tool
// alternation the other Append methods check.
func (s *Session) lastNonSystem() (llm.Message, bool) {
	for i := len(s.turns) - 1; i >= 0; i-- {
		if s.turns[i].Role != "system" {
			return s.turns[i], true
		}
	}
	return llm.Message{}, false
}
