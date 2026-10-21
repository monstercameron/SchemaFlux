package ops

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Review round 11, streams.

// A detached stream that its reader has left is read to its end by the stream
// itself. A second range over it used to read the same channel beside that, so
// the two shared the text between them: a caller joining what the second range
// yielded got "d1d3", with no error to say a part was missing. A range over a
// stream that is being read to its end waits for the end and yields no text:
// the whole of it is what Text returns.
func TestADetachedStreamIsNotReadTwiceAtOnce(t *testing.T) {
	good := llm.CompletionResponse{Content: "d1d2d3d4d5", FinishReason: "stop", Model: "gpt-6-luna", Provider: "openai"}
	for round := range 300 {
		stream, err := streamWith(context.Background(), chunksStream(good, "d1", "d2", "d3", "d4", "d5"), lx126TextOptions)
		if err != nil {
			t.Fatalf("the stream: %v", err)
		}
		stream.Detach()
		first := ""
		for delta := range stream.All() {
			first += delta
			break
		}

		again := ""
		var last error
		for delta, err := range stream.All() {
			again += delta
			last = err
		}
		if again != "" || last != nil {
			t.Fatalf("round %d: after %q, a second range yielded %q, %v; want nothing more", round, first, again, last)
		}
		if text, err := stream.Text(); err != nil || text != "d1d2d3d4d5" {
			t.Fatalf("round %d: Text = %q, %v; want the whole answer", round, text, err)
		}
		if stream.Err() != nil {
			t.Fatalf("round %d: Err = %v", round, stream.Err())
		}
	}

	// A detached stream that fails says so to the range that waited for it.
	truncated := good
	truncated.FinishReason = "length"
	stream, err := streamWith(context.Background(), chunksStream(truncated, "d1", "d2"), lx126TextOptions)
	if err != nil {
		t.Fatalf("the stream: %v", err)
	}
	stream.Detach()
	for range stream.All() {
		break
	}
	var last error
	text := ""
	for delta, err := range stream.All() {
		text += delta
		last = err
	}
	if !errors.Is(last, types.ErrOutputTruncated) || text != "" {
		t.Errorf("a second range of a truncated detached stream gave %q, %v", text, last)
	}
}

// Err says what happened to a stream as soon as it is known. A reader that
// left the loop early cancelled the stream, and Err went on returning nil,
// which is what a stream that finished returns, until something read it again.
func TestErrSaysAStreamItsReaderLeftWasCancelled(t *testing.T) {
	t.Run("left early", func(t *testing.T) {
		provider := &floodProvider{started: make(chan struct{})}
		stream, err := streamWith(context.Background(), provider, lx126TextOptions)
		if err != nil {
			t.Fatalf("the stream: %v", err)
		}
		if stream.Err() != nil {
			t.Fatalf("Err before the stream was read: %v", stream.Err())
		}
		for range stream.All() {
			break
		}
		if !errors.Is(stream.Err(), types.ErrCanceled) {
			t.Fatalf("Err = %v straight after the reader left; want the cancellation", stream.Err())
		}
		// And it is the outcome from then on.
		if text, err := stream.Text(); !errors.Is(err, types.ErrCanceled) || text != "" {
			t.Errorf("Text = %q, %v", text, err)
		}
		if text := ""; true {
			for delta, err := range stream.All() {
				text += delta
				if !errors.Is(err, types.ErrCanceled) {
					t.Errorf("a later range yielded %q, %v", delta, err)
				}
			}
			if text != "" {
				t.Errorf("a later range yielded text: %q", text)
			}
		}
	})

	t.Run("cancelled outright", func(t *testing.T) {
		provider := &floodProvider{started: make(chan struct{})}
		stream, err := streamWith(context.Background(), provider, lx126TextOptions)
		if err != nil {
			t.Fatalf("the stream: %v", err)
		}
		<-provider.started
		stream.Cancel()
		if _, err := stream.Text(); !errors.Is(err, types.ErrCanceled) {
			t.Fatalf("Text: %v", err)
		}
		if !errors.Is(stream.Err(), types.ErrCanceled) {
			t.Errorf("Err = %v", stream.Err())
		}
	})

	// Leaving a stream that had already finished is not a cancellation of
	// anything. Whichever way the timing falls, it is never "no text, and no
	// error".
	t.Run("left early at the very end", func(t *testing.T) {
		good := llm.CompletionResponse{Content: "all of it", FinishReason: "stop", Model: "gpt-6-luna", Provider: "openai"}
		for round := range 200 {
			stream, err := streamWith(context.Background(), chunksStream(good, "all of it"), lx126TextOptions)
			if err != nil {
				t.Fatalf("the stream: %v", err)
			}
			for range stream.All() {
				break
			}
			text, err := stream.Text()
			switch {
			case err == nil && text == "all of it":
			case errors.Is(err, types.ErrCanceled) && text == "":
			default:
				t.Fatalf("round %d: Text = %q, %v", round, text, err)
			}
			if (err == nil) != (stream.Err() == nil) {
				t.Fatalf("round %d: Text says %v and Err says %v", round, err, stream.Err())
			}
		}
	})
}

// settle, endWithoutFinal and drain, each on its own.
func TestStreamOutcomeIsRecordedOnce(t *testing.T) {
	failure := errors.New("the provider failed")
	later := errors.New("a later failure")
	answer := llm.CompletionResponse{Content: "the answer"}
	other := llm.CompletionResponse{Content: "another answer"}
	newStream := func() *TextStream {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		return &TextStream{ctx: ctx, cancel: cancel, items: make(chan textStreamItem, 8)}
	}
	text := func(s *TextStream) string {
		s.resultMu.Lock()
		defer s.resultMu.Unlock()
		return s.finalText
	}

	settles := []struct {
		name     string
		items    []textStreamItem
		wantErr  error
		wantText string
	}{
		{"a result", []textStreamItem{{final: true, resp: answer}}, nil, "the answer"},
		{"an error", []textStreamItem{{final: true, err: failure}}, failure, ""},
		{"a result, then an error", []textStreamItem{{final: true, resp: answer}, {final: true, err: failure}}, nil, "the answer"},
		{"an error, then a result", []textStreamItem{{final: true, err: failure}, {final: true, resp: answer}}, failure, ""},
		{"two results", []textStreamItem{{final: true, resp: answer}, {final: true, resp: other}}, nil, "the answer"},
		{"two errors", []textStreamItem{{final: true, err: failure}, {final: true, err: later}}, failure, ""},
		{"an empty result", []textStreamItem{{final: true}}, nil, ""},
		{"an error beside a response", []textStreamItem{{final: true, err: failure, resp: answer}}, failure, ""},
		{"a result three times", []textStreamItem{{final: true, resp: answer}, {final: true, resp: other}, {final: true, err: later}}, nil, "the answer"},
		{"an error three times", []textStreamItem{{final: true, err: failure}, {final: true, err: later}, {final: true, resp: other}}, failure, ""},
	}
	for _, tc := range settles {
		t.Run("settle: "+tc.name, func(t *testing.T) {
			s := newStream()
			for _, item := range tc.items {
				s.settle(item)
			}
			ended, err := s.outcome()
			if !ended || !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) || text(s) != tc.wantText {
				t.Errorf("ended %v, err %v, text %q; want %v and %q", ended, err, text(s), tc.wantErr, tc.wantText)
			}
			if s.Err() != err {
				t.Errorf("Err = %v, and the outcome is %v", s.Err(), err)
			}
		})
	}

	ends := []struct {
		name     string
		prepare  func(s *TextStream)
		wantErr  error // nil for success
		anyErr   bool  // an error of this library's own, whatever it is
		wantText string
	}{
		{"the producer recorded a result", func(s *TextStream) { s.final = &textStreamItem{final: true, resp: answer} }, nil, false, "the answer"},
		{"the producer recorded an error", func(s *TextStream) { s.final = &textStreamItem{final: true, err: failure} }, failure, false, ""},
		{"nothing recorded, and the stream's context is over", func(s *TextStream) { s.cancel() }, types.ErrCanceled, false, ""},
		{"nothing recorded, and nothing known", func(s *TextStream) {}, nil, true, ""},
		{"the producer left a reason", func(s *TextStream) { s.stopped = failure }, failure, false, ""},
		{"a reason, and the context over", func(s *TextStream) { s.stopped = failure; s.cancel() }, failure, false, ""},
		{"a result recorded, and the context over", func(s *TextStream) {
			s.final = &textStreamItem{final: true, resp: answer}
			s.cancel()
		}, nil, false, "the answer"},
		{"already settled with a result", func(s *TextStream) {
			s.settle(textStreamItem{final: true, resp: answer})
			s.stopped = failure
		}, nil, false, "the answer"},
		{"already settled with an error", func(s *TextStream) {
			s.settle(textStreamItem{final: true, err: failure})
			s.final = &textStreamItem{final: true, resp: answer}
		}, failure, false, ""},
		{"a result recorded beside a reason", func(s *TextStream) {
			s.final = &textStreamItem{final: true, resp: answer}
			s.stopped = failure
		}, nil, false, "the answer"},
	}
	for _, tc := range ends {
		for _, by := range []string{"endWithoutFinal", "drain"} {
			t.Run(by+": "+tc.name, func(t *testing.T) {
				s := newStream()
				tc.prepare(s)
				var got error
				if by == "drain" {
					close(s.items)
					s.drain()
					got = s.Err()
				} else {
					got = s.endWithoutFinal()
				}
				switch {
				case tc.anyErr:
					if got == nil {
						t.Error("a stream that ended with no final item and no reason reads as finished")
					}
				case tc.wantErr == nil:
					if got != nil {
						t.Errorf("err = %v, want none", got)
					}
				case !errors.Is(got, tc.wantErr):
					t.Errorf("err = %v, want %v", got, tc.wantErr)
				}
				if text(s) != tc.wantText {
					t.Errorf("text = %q, want %q", text(s), tc.wantText)
				}
				// And it is the outcome from then on.
				if again := s.endWithoutFinal(); again != got {
					t.Errorf("asked again, the outcome is %v, and it was %v", again, got)
				}
				if ended, _ := s.outcome(); !ended {
					t.Error("the outcome was not recorded")
				}
			})
		}
	}

	// drain reads what is left and keeps the final item it finds.
	drains := []struct {
		name     string
		items    []textStreamItem
		wantErr  error
		wantText string
	}{
		{"deltas and a result", []textStreamItem{{delta: "a"}, {delta: "b"}, {final: true, resp: answer}}, nil, "the answer"},
		{"deltas and an error", []textStreamItem{{delta: "a"}, {final: true, err: failure}}, failure, ""},
		{"a result alone", []textStreamItem{{final: true, resp: answer}}, nil, "the answer"},
		{"an error alone", []textStreamItem{{final: true, err: failure}}, failure, ""},
	}
	for _, tc := range drains {
		t.Run("drain: "+tc.name, func(t *testing.T) {
			s := newStream()
			for _, item := range tc.items {
				s.items <- item
			}
			close(s.items)
			s.drain()
			if !errors.Is(s.Err(), tc.wantErr) || (tc.wantErr == nil && s.Err() != nil) || text(s) != tc.wantText {
				t.Errorf("err %v, text %q", s.Err(), text(s))
			}
		})
	}

	// A range that arrives while the stream is being read to its end waits for
	// it, and is not left waiting if the end has already come.
	// One round passed about half the time with the wait removed, because the
	// range and the drain share the channel and either can take the delta. It
	// is repeated until that cannot pass by luck.
	t.Run("a range waits for a drain that has started", func(t *testing.T) {
		for round := range 300 {
			s := newStream()
			s.startDrain()
			done := make(chan string, 1)
			go func() {
				got := ""
				for delta := range s.All() {
					got += delta
				}
				done <- got
			}()
			s.items <- textStreamItem{delta: "a"}
			s.items <- textStreamItem{final: true, resp: answer}
			close(s.items)
			select {
			case got := <-done:
				if got != "" {
					t.Fatalf("round %d: the range yielded %q beside the drain", round, got)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("round %d: the range never returned", round)
			}
			if got, err := s.Text(); err != nil || got != "the answer" {
				t.Fatalf("round %d: Text = %q, %v", round, got, err)
			}
		}
	})
	t.Run("starting a drain twice starts one", func(t *testing.T) {
		s := newStream()
		s.startDrain()
		first := s.draining()
		s.startDrain()
		if first == nil || s.draining() != first {
			t.Error("a second drain was started beside the first")
		}
		close(s.items)
		<-first
	})
}
