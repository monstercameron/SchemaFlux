package ops

import (
	"errors"
	"testing"
)

func TestSessionValidTranscriptSucceeds(t *testing.T) {
	s := NewSession()
	if err := s.AppendSystem("You are terse."); err != nil {
		t.Fatalf("AppendSystem: %v", err)
	}
	if err := s.AppendUser("What is 2+2?"); err != nil {
		t.Fatalf("AppendUser: %v", err)
	}
	if err := s.AppendAssistant("4"); err != nil {
		t.Fatalf("AppendAssistant: %v", err)
	}
	if err := s.AppendUser("And 3+3?"); err != nil {
		t.Fatalf("second AppendUser: %v", err)
	}
	if err := s.AppendAssistant("6"); err != nil {
		t.Fatalf("second AppendAssistant: %v", err)
	}
	if s.Len() != 5 {
		t.Fatalf("Len() = %d, want 5", s.Len())
	}
	msgs := s.Messages()
	if len(msgs) != 5 || msgs[0].Role != "system" || msgs[4].Content != "6" {
		t.Fatalf("Messages() = %#v", msgs)
	}
}

func TestSessionSystemMustBeFirstTurn(t *testing.T) {
	s := NewSession()
	if err := s.AppendUser("hi"); err != nil {
		t.Fatalf("AppendUser: %v", err)
	}
	err := s.AppendSystem("too late")
	if err == nil {
		t.Fatal("expected an error appending a system turn after the session started")
	}
	if !errors.Is(err, ErrSessionInvariant) {
		t.Errorf("error %v does not wrap ErrSessionInvariant", err)
	}
}

func TestSessionRejectsConsecutiveUserTurns(t *testing.T) {
	s := NewSession()
	if err := s.AppendUser("first"); err != nil {
		t.Fatalf("AppendUser: %v", err)
	}
	err := s.AppendUser("second")
	if err == nil {
		t.Fatal("expected an error for two consecutive user turns")
	}
	if !errors.Is(err, ErrSessionInvariant) {
		t.Errorf("error %v does not wrap ErrSessionInvariant", err)
	}
}

func TestSessionRejectsAssistantBeforeAnyUserTurn(t *testing.T) {
	s := NewSession()
	err := s.AppendAssistant("nothing to answer")
	if err == nil {
		t.Fatal("expected an error for an assistant turn with no prior user turn")
	}
	if !errors.Is(err, ErrSessionInvariant) {
		t.Errorf("error %v does not wrap ErrSessionInvariant", err)
	}
}

func TestSessionRejectsConsecutiveAssistantTurns(t *testing.T) {
	s := NewSession()
	if err := s.AppendUser("hi"); err != nil {
		t.Fatalf("AppendUser: %v", err)
	}
	if err := s.AppendAssistant("hello"); err != nil {
		t.Fatalf("AppendAssistant: %v", err)
	}
	err := s.AppendAssistant("hello again")
	if err == nil {
		t.Fatal("expected an error for two consecutive assistant turns")
	}
	if !errors.Is(err, ErrSessionInvariant) {
		t.Errorf("error %v does not wrap ErrSessionInvariant", err)
	}
}

func TestSessionToolResultRequiresPriorAssistantTurn(t *testing.T) {
	s := NewSession()
	if err := s.AppendUser("call a tool for me"); err != nil {
		t.Fatalf("AppendUser: %v", err)
	}
	err := s.AppendToolResult("call_1", `{"ok":true}`)
	if err == nil {
		t.Fatal("expected an error for a tool result with no assistant turn in front of it")
	}
	if !errors.Is(err, ErrSessionInvariant) {
		t.Errorf("error %v does not wrap ErrSessionInvariant", err)
	}
}

func TestSessionToolResultRequiresACallID(t *testing.T) {
	s := NewSession()
	if err := s.AppendUser("call a tool for me"); err != nil {
		t.Fatalf("AppendUser: %v", err)
	}
	if err := s.AppendAssistant("calling get_weather"); err != nil {
		t.Fatalf("AppendAssistant: %v", err)
	}
	err := s.AppendToolResult("", `{"ok":true}`)
	if err == nil {
		t.Fatal("expected an error for a tool result with no call ID")
	}
	if !errors.Is(err, ErrSessionInvariant) {
		t.Errorf("error %v does not wrap ErrSessionInvariant", err)
	}
}

func TestSessionToolResultThenAssistantSucceeds(t *testing.T) {
	s := NewSession()
	if err := s.AppendUser("what's the weather?"); err != nil {
		t.Fatalf("AppendUser: %v", err)
	}
	if err := s.AppendAssistant("calling get_weather"); err != nil {
		t.Fatalf("AppendAssistant: %v", err)
	}
	if err := s.AppendToolResult("call_1", `{"tempF":72}`); err != nil {
		t.Fatalf("AppendToolResult: %v", err)
	}
	if err := s.AppendAssistant("it's 72F"); err != nil {
		t.Fatalf("final AppendAssistant: %v", err)
	}
	msgs := s.Messages()
	if len(msgs) != 4 {
		t.Fatalf("Len() = %d, want 4", len(msgs))
	}
	if msgs[2].Role != "tool" || msgs[2].ToolCallID != "call_1" {
		t.Errorf("msgs[2] = %#v", msgs[2])
	}
}

// TestSessionMessagesReturnsACopy proves mutating the slice Messages()
// returns cannot reach back into the session's own recorded transcript --
// the invariants above are only meaningful if the history they validated
// cannot be silently rewritten after the fact.
func TestSessionMessagesReturnsACopy(t *testing.T) {
	s := NewSession()
	if err := s.AppendUser("original"); err != nil {
		t.Fatalf("AppendUser: %v", err)
	}
	msgs := s.Messages()
	msgs[0].Content = "tampered"

	again := s.Messages()
	if again[0].Content != "original" {
		t.Errorf("session transcript was mutated via a returned slice: got %q", again[0].Content)
	}
}
