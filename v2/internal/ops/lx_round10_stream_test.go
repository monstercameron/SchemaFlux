package ops

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

func chunksStream(final llm.CompletionResponse, deltas ...string) *namedStreamProvider {
	return &namedStreamProvider{name: "openai", seq: func(yield func(llm.StreamChunk, error) bool) {
		for _, delta := range deltas {
			if !yield(llm.StreamChunk{Delta: delta}, nil) {
				return
			}
		}
		yield(llm.StreamChunk{Done: true, Response: final}, nil)
	}}
}

// Review round 10, the blocker. A stream that has been read to its end keeps
// its outcome, and reading it again gives that outcome. The fix before this one
// taught a stream to say it had been stopped when its items ran out with no
// final one, and a second read of a finished stream is exactly that: ranging
// over All to the end and then calling Text, which is the use All's own comment
// describes, returned "the stream ended before it finished".
func TestAStreamKeepsItsOutcomeHoweverOftenItIsRead(t *testing.T) {
	good := llm.CompletionResponse{Content: "all of it", FinishReason: "stop", Model: "gpt-6-luna", Provider: "openai"}
	truncated := good
	truncated.FinishReason = "length"

	readAll := func(stream *TextStream) (string, error) {
		var text string
		var last error
		for delta, err := range stream.All() {
			text += delta
			last = err
		}
		return text, last
	}

	t.Run("read with All, then Text", func(t *testing.T) {
		stream, err := streamWith(context.Background(), chunksStream(good, "all ", "of ", "it"), lx126TextOptions)
		if err != nil {
			t.Fatalf("the stream: %v", err)
		}
		if text, err := readAll(stream); err != nil || text != "all of it" {
			t.Fatalf("the first read: %q, %v", text, err)
		}
		for range 3 {
			text, err := stream.Text()
			if err != nil || text != "all of it" {
				t.Fatalf("Text after the stream was read: %q, %v", text, err)
			}
			if stream.Err() != nil {
				t.Fatalf("Err after a clean stream: %v", stream.Err())
			}
		}
		// Ranging again yields nothing and no error: there is nothing left.
		if text, err := readAll(stream); err != nil || text != "" {
			t.Errorf("a second range gave %q, %v", text, err)
		}
	})

	t.Run("Text twice", func(t *testing.T) {
		stream, err := streamWith(context.Background(), chunksStream(good, "all of it"), lx126TextOptions)
		if err != nil {
			t.Fatalf("the stream: %v", err)
		}
		for range 3 {
			if text, err := stream.Text(); err != nil || text != "all of it" {
				t.Fatalf("Text: %q, %v", text, err)
			}
		}
	})

	t.Run("a truncated stream keeps its own error", func(t *testing.T) {
		stream, err := streamWith(context.Background(), chunksStream(truncated, "all of"), lx126TextOptions)
		if err != nil {
			t.Fatalf("the stream: %v", err)
		}
		for range 3 {
			text, err := stream.Text()
			if !errors.Is(err, types.ErrOutputTruncated) || text != "" {
				t.Fatalf("Text = %q, %v; want the truncation every time", text, err)
			}
			if !errors.Is(stream.Err(), types.ErrOutputTruncated) {
				t.Fatalf("Err = %v, want the truncation", stream.Err())
			}
		}
	})

	t.Run("a stopped stream keeps its own error", func(t *testing.T) {
		provider := &floodProvider{started: make(chan struct{})}
		stream, err := streamWith(context.Background(), provider, lx126TextOptions)
		if err != nil {
			t.Fatalf("the stream: %v", err)
		}
		<-provider.started
		stream.Cancel()
		for range 3 {
			if _, err := stream.Text(); !errors.Is(err, types.ErrCanceled) {
				t.Fatalf("Text: %v, want the cancellation every time", err)
			}
		}
	})

	t.Run("detached, left early, then Text", func(t *testing.T) {
		stream, err := streamWith(context.Background(), chunksStream(good, "all ", "of ", "it"), lx126TextOptions)
		if err != nil {
			t.Fatalf("the stream: %v", err)
		}
		stream.Detach()
		for range stream.All() {
			break
		}
		// The work was left running, and the drain reads it to its end on the
		// caller's behalf. This waits until it has, so that the read below
		// cannot take the final item itself: the outcome has to have been
		// kept by the drain.
		deadline := time.Now().Add(2 * time.Second)
		for {
			if ended, _ := stream.outcome(); ended {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the drain of a detached stream never recorded its outcome")
			}
			time.Sleep(time.Millisecond)
		}
		if text, err := stream.Text(); err != nil || text != "all of it" {
			t.Fatalf("Text after a detached stream finished: %q, %v", text, err)
		}
	})

	t.Run("left early without detaching, then Text", func(t *testing.T) {
		provider := &floodProvider{started: make(chan struct{})}
		stream, err := streamWith(context.Background(), provider, lx126TextOptions)
		if err != nil {
			t.Fatalf("the stream: %v", err)
		}
		for range stream.All() {
			break
		}
		// Leaving the loop stopped the work. What is left is a stream that was
		// cancelled, and it says so.
		if text, err := stream.Text(); !errors.Is(err, types.ErrCanceled) || text != "" {
			t.Fatalf("Text = %q, %v; want the cancellation", text, err)
		}
	})

	// One reader can take the final item off the channel while the other
	// finds the channel closed, before the first has recorded anything. Run
	// many times, because that window is narrow: it was seen once in a full
	// run of the suite, with both readers told the stream had been stopped.
	t.Run("two readers at once", func(t *testing.T) {
		for round := range 300 {
			stream, err := streamWith(context.Background(), chunksStream(good, "all ", "of ", "it"), lx126TextOptions)
			if err != nil {
				t.Fatalf("the stream: %v", err)
			}
			type read struct {
				text string
				err  error
			}
			results := make(chan read, 3)
			for range 3 {
				go func() {
					text, err := stream.Text()
					results <- read{text, err}
				}()
			}
			for range 3 {
				if got := <-results; got.err != nil || got.text != "all of it" {
					t.Fatalf("round %d: a reader of a clean stream got %q, %v", round, got.text, got.err)
				}
			}
			if text, err := stream.Text(); err != nil || text != "all of it" {
				t.Fatalf("round %d: Text afterwards: %q, %v", round, text, err)
			}
		}
	})
}

// send, finish, whyStopped and stopCause, each on its own.
func TestStreamSendAndFinish(t *testing.T) {
	newStream := func(buffer int) (*TextStream, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		return &TextStream{ctx: ctx, cancel: cancel, items: make(chan textStreamItem, buffer)}, cancel
	}
	open := context.Background()
	ended, end := context.WithCancel(context.Background())
	end()
	failure := errors.New("the provider failed")

	t.Run("send: room in the buffer", func(t *testing.T) {
		s, cancel := newStream(1)
		defer cancel()
		if !s.send(open, textStreamItem{delta: "a"}) {
			t.Error("an item was not sent with room in the buffer")
		}
	})
	t.Run("send: room in the buffer, on a stream that is stopped", func(t *testing.T) {
		s, cancel := newStream(1)
		cancel()
		if !s.send(ended, textStreamItem{delta: "a"}) {
			t.Error("room in the buffer was not taken because the stream is stopped")
		}
	})
	// A send that does not watch one of its contexts never returns when only
	// that one is over, and a blocked call would hold the whole run until the
	// test binary's own timeout: so the call is bounded, and a send that does not
	// return is a failure of its own.
	sendReturns := func(t *testing.T, s *TextStream, run context.Context) (sent bool) {
		t.Helper()
		result := make(chan bool, 1)
		go func() { result <- s.send(run, textStreamItem{delta: "a"}) }()
		select {
		case sent = <-result:
			return sent
		case <-time.After(5 * time.Second):
			t.Fatal("send did not return although a context it watches is over")
			return false
		}
	}
	// finish delivers through send, so it is bounded the same way.
	finishReturns := func(t *testing.T, s *TextStream, run context.Context, item textStreamItem) {
		t.Helper()
		returned := make(chan struct{})
		go func() {
			s.finish(run, item)
			close(returned)
		}()
		select {
		case <-returned:
		case <-time.After(5 * time.Second):
			t.Fatal("finish did not return although a context it watches is over")
		}
	}
	t.Run("send: no room and the stream's context over", func(t *testing.T) {
		s, cancel := newStream(0)
		cancel()
		if sendReturns(t, s, open) {
			t.Error("an item was sent to nobody")
		}
	})
	t.Run("send: no room and the run's context over", func(t *testing.T) {
		s, cancel := newStream(0)
		defer cancel()
		if sendReturns(t, s, ended) {
			t.Error("an item was sent to nobody")
		}
	})
	t.Run("send: no room, and a reader arrives", func(t *testing.T) {
		s, cancel := newStream(0)
		defer cancel()
		go func() { <-s.items }()
		if !s.send(open, textStreamItem{delta: "a"}) {
			t.Error("an item was not sent to a waiting reader")
		}
	})
	t.Run("finish: an error that cannot be delivered is kept", func(t *testing.T) {
		s, cancel := newStream(0)
		defer cancel()
		finishReturns(t, s, ended, textStreamItem{final: true, err: failure})
		if !errors.Is(s.whyStopped(), failure) {
			t.Errorf("whyStopped = %v, want the error that could not be delivered", s.whyStopped())
		}
	})
	t.Run("finish: the first error kept is the one that stays", func(t *testing.T) {
		s, cancel := newStream(0)
		defer cancel()
		finishReturns(t, s, ended, textStreamItem{final: true, err: failure})
		finishReturns(t, s, ended, textStreamItem{final: true, err: errors.New("a later one")})
		if !errors.Is(s.whyStopped(), failure) {
			t.Errorf("whyStopped = %v, want the first", s.whyStopped())
		}
	})
	// These two asserted that s.stopped was nil after finish, and finish never
	// writes it, so they passed whatever finish did. What finish is for is the
	// record a reader that finds the channel closed takes the outcome from, so
	// that is what they assert now: a delivered error is the outcome and
	// delivering it again does not replace it, and a result that nobody could
	// take is still the outcome, and not an error.
	t.Run("finish: a delivered error is the outcome", func(t *testing.T) {
		s, cancel := newStream(1)
		defer cancel()
		s.finish(open, textStreamItem{final: true, err: failure})
		if len(s.items) != 1 {
			t.Fatalf("%d items are waiting, want the delivered one", len(s.items))
		}
		close(s.items)
		if got := s.endWithoutFinal(); !errors.Is(got, failure) {
			t.Errorf("the outcome is %v, want the error that was delivered", got)
		}
	})
	t.Run("finish: a result that cannot be delivered is still the outcome", func(t *testing.T) {
		s, cancel := newStream(0)
		defer cancel()
		finishReturns(t, s, ended, textStreamItem{final: true, resp: llm.CompletionResponse{Content: "x"}})
		if len(s.items) != 0 {
			t.Fatalf("%d items are waiting; there was no room", len(s.items))
		}
		close(s.items)
		if got := s.endWithoutFinal(); got != nil {
			t.Errorf("an undelivered result left the error %v", got)
		}
		if s.finalText != "x" {
			t.Errorf("text = %q, want the result that could not be delivered", s.finalText)
		}
	})
	t.Run("whyStopped: the stream's own context", func(t *testing.T) {
		s, cancel := newStream(0)
		cancel()
		if !errors.Is(s.whyStopped(), types.ErrCanceled) {
			t.Errorf("whyStopped = %v, want a cancellation", s.whyStopped())
		}
	})
	t.Run("whyStopped: nothing known", func(t *testing.T) {
		s, cancel := newStream(0)
		defer cancel()
		if s.whyStopped() == nil {
			t.Error("a stream that ended with no final item and no reason reads as finished")
		}
	})

	closed := NewScheduler(SchedulerLimits{})
	_ = closed.Close(context.Background())
	live := NewScheduler(SchedulerLimits{})
	defer live.Close(context.Background())
	withScheduler := func(parent context.Context, scheduler *Scheduler) context.Context {
		return WithExecConfig(parent, &ExecConfig{Scheduler: scheduler})
	}
	cancellation := context.Canceled

	causes := []struct {
		name    string
		run     context.Context
		stopped bool // the stream's own context is over
		err     error
		want    error
	}{
		{"nothing wrong", open, false, nil, nil},
		{"the stream's context over", open, true, cancellation, types.ErrCanceled},
		{"the stream's context over, with a provider error", open, true, failure, types.ErrCanceled},
		{"a provider error, nothing closed", open, false, failure, failure},
		{"a provider error with a live scheduler", withScheduler(open, live), false, failure, failure},
		{"a provider error after the scheduler closed, the run not cancelled", withScheduler(open, closed), false, failure, failure},
		{"a cancellation from the scheduler closing", withScheduler(ended, closed), false, cancellation, types.ErrShutdown},
		{"the run cancelled with a live scheduler", withScheduler(ended, live), false, cancellation, cancellation},
		{"the run cancelled with no scheduler", ended, false, cancellation, cancellation},
		// A provider's own failure is not a shutdown because the scheduler
		// happens to be closed by the time it is looked at.
		{"a provider's own error as the scheduler closes", withScheduler(ended, closed), false, failure, failure},
		// Round 11: both reasons at once. The stream's own context ending is
		// the caller's doing and is what is reported; with the two checks the
		// other way round this said "shutdown" and no case here noticed.
		{"the stream's context over as the scheduler closes", withScheduler(ended, closed), true, cancellation, types.ErrCanceled},
		{"the stream's context over as the scheduler closes, with a provider error", withScheduler(ended, closed), true, failure, types.ErrCanceled},
	}
	for _, tc := range causes {
		t.Run("stopCause: "+tc.name, func(t *testing.T) {
			s, cancel := newStream(0)
			defer cancel()
			if tc.stopped {
				cancel()
			}
			got := stopCause(tc.run, s, tc.err)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("stopCause = %v, want nothing", got)
				}
				return
			}
			if !errors.Is(got, tc.want) {
				t.Fatalf("stopCause = %v, want %v", got, tc.want)
			}
			if tc.want != types.ErrShutdown && errors.Is(got, types.ErrShutdown) {
				t.Errorf("reported as a shutdown, and it was not one: %v", got)
			}
		})
	}
}
