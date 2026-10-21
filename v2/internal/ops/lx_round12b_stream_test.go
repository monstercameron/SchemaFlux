package ops

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/requesttracking"
	"github.com/monstercameron/schemaflux/v2/internal/runtime/pricing"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Review round 12b, streams. One outcome, recorded once, is what every reader
// yields, what Err reports, and what the budget and the call record say.

// strm12AnyError stands for any error in a table of expected ones.
var strm12AnyError = errors.New("any error")

var strm12Usage = types.TokenUsage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500}

func strm12Good() llm.CompletionResponse {
	return llm.CompletionResponse{Content: "all of it", FinishReason: "stop", Model: "gpt-6-luna", Provider: "openai", Usage: strm12Usage}
}

var strm12Requests atomic.Int64

// strm12Options is lx126TextOptions with a request ID of its own, which is how a
// test finds the one entry its stream left on the process-wide ledger.
func strm12Options() (types.OpOptions, string) {
	id := fmt.Sprintf("strm12-%d", strm12Requests.Add(1))
	opts := lx126TextOptions
	opts.RequestID = id
	return opts, id
}

// strm12Gate holds a provider where a test wants it, and is opened when the
// test ends so that nothing stays blocked behind it.
type strm12Gate struct {
	ch   chan struct{}
	once sync.Once
}

func strm12NewGate(t *testing.T) *strm12Gate {
	t.Helper()
	g := &strm12Gate{ch: make(chan struct{})}
	t.Cleanup(g.open)
	return g
}

func (g *strm12Gate) open() { g.once.Do(func() { close(g.ch) }) }

// strm12Gated streams first, announces that it has, waits at the gate, and then
// streams rest. It ignores its context, as a provider is allowed to.
func strm12Gated(first []string, reached chan struct{}, gate *strm12Gate, rest ...llm.StreamChunk) *namedStreamProvider {
	return &namedStreamProvider{name: "openai", seq: func(yield func(llm.StreamChunk, error) bool) {
		for _, delta := range first {
			if !yield(llm.StreamChunk{Delta: delta}, nil) {
				return
			}
		}
		close(reached)
		<-gate.ch
		for _, chunk := range rest {
			if !yield(chunk, nil) {
				return
			}
		}
	}}
}

// strm12AwaitProducer waits until the stream's producer has returned, defers
// included, by reading what is left until the channel is closed. Only for a
// stream nobody is reading any more.
func strm12AwaitProducer(t *testing.T, s *TextStream) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		for range s.items {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream's producer never returned")
	}
}

// strm12ReadersWaiting counts the goroutines parked in a select inside a stream's
// next, waiting for its next item. A goroutine that is merely inside next has
// not yet got as far as waiting, and is not counted, which is the only way to
// know a reader is blocked rather than about to be.
func strm12ReadersWaiting() int {
	size := 1 << 20
	for {
		buf := make([]byte, size)
		if n := runtime.Stack(buf, true); n < len(buf) {
			waiting := 0
			for _, block := range strings.Split(string(buf[:n]), "\n\n") {
				header, _, _ := strings.Cut(block, "\n")
				if strings.Contains(header, "[select") && strings.Contains(block, "(*TextStream).next(") {
					waiting++
				}
			}
			return waiting
		}
		size *= 2
	}
}

func strm12WaitForReader(t *testing.T, before int) {
	t.Helper()
	waitFor(t, func() bool { return strm12ReadersWaiting() > before })
}

// strm12Deadline is a context whose deadline passes when the test says so.
type strm12Deadline struct {
	context.Context
	done chan struct{}
}

func strm12NewDeadline() *strm12Deadline {
	return &strm12Deadline{Context: context.Background(), done: make(chan struct{})}
}
func (c *strm12Deadline) Done() <-chan struct{} { return c.done }
func (c *strm12Deadline) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}
func (c *strm12Deadline) pass() { close(c.done) }

func strm12Literal(t *testing.T, buffer int) *TextStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &TextStream{ctx: ctx, cancel: cancel, items: make(chan textStreamItem, buffer)}
}

func strm12Cancelled() error {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return contextOver(ctx)
}

func strm12Collect(s *TextStream) (text string, last error) {
	for delta, err := range s.All() {
		text += delta
		last = err
	}
	return text, last
}

// A reader that takes the final item yields the stream's outcome. It yielded the
// item's own error, so with two readers one could be told a cancelled stream had
// finished cleanly: the other had left the loop, which cancelled it, and the
// producer's success arrived after.
func TestAReaderYieldsTheStreamsOutcomeAndNotItsItemsError(t *testing.T) {
	cancelled := strm12Cancelled()
	failure := errors.New("the provider failed")
	other := errors.New("another failure")
	answer := llm.CompletionResponse{Content: "the answer"}
	another := llm.CompletionResponse{Content: "another answer"}

	cases := []struct {
		name     string
		claimed  *textStreamItem
		item     textStreamItem
		wantErr  error
		wantText string
	}{
		{"nothing claimed, a result", nil, textStreamItem{final: true, resp: answer}, nil, "the answer"},
		{"nothing claimed, an error", nil, textStreamItem{final: true, err: failure}, failure, ""},
		{"a cancellation claimed, a result arrives", &textStreamItem{final: true, err: cancelled}, textStreamItem{final: true, resp: answer}, cancelled, ""},
		{"a cancellation claimed, an error arrives", &textStreamItem{final: true, err: cancelled}, textStreamItem{final: true, err: failure}, cancelled, ""},
		{"a failure claimed, a result arrives", &textStreamItem{final: true, err: failure}, textStreamItem{final: true, resp: answer}, failure, ""},
		{"a result claimed, an error arrives", &textStreamItem{final: true, resp: answer}, textStreamItem{final: true, err: failure}, nil, "the answer"},
		{"a result claimed, another result arrives", &textStreamItem{final: true, resp: answer}, textStreamItem{final: true, resp: another}, nil, "the answer"},
		{"a failure claimed, another failure arrives", &textStreamItem{final: true, err: failure}, textStreamItem{final: true, err: other}, failure, ""},
		{"a result claimed, the same result arrives", &textStreamItem{final: true, resp: answer}, textStreamItem{final: true, resp: answer}, nil, "the answer"},
		{"a cancellation claimed, an empty result arrives", &textStreamItem{final: true, err: cancelled}, textStreamItem{final: true}, cancelled, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := strm12Literal(t, 4)
			if tc.claimed != nil {
				claimed := *tc.claimed
				s.final = &claimed
			}
			s.items <- textStreamItem{delta: "a"}
			s.items <- tc.item
			close(s.items)

			text, last := strm12Collect(s)
			if text != "a" {
				t.Errorf("the range yielded %q, want the delta that was queued", text)
			}
			if tc.wantErr == nil && last != nil || tc.wantErr != nil && !errors.Is(last, tc.wantErr) {
				t.Errorf("the range ended with %v, want %v", last, tc.wantErr)
			}
			if got := s.Err(); got != last && !(tc.wantErr != nil && errors.Is(got, tc.wantErr)) {
				t.Errorf("Err = %v, but the range ended with %v", got, last)
			}
			got, err := s.Text()
			if tc.wantErr == nil && (err != nil || got != tc.wantText) {
				t.Errorf("Text = %q, %v; want %q", got, err, tc.wantText)
			}
			if tc.wantErr != nil && (!errors.Is(err, tc.wantErr) || got != "") {
				t.Errorf("Text = %q, %v; want %v", got, err, tc.wantErr)
			}
		})
	}
}

// What the readers are told, what Err reports, and what the call record and the
// budget say agree, however two readers and a leaving one interleave. Many
// rounds, because the window is narrow.
func TestReadersErrAndTheCallRecordAgreeAboutOneOutcome(t *testing.T) {
	good := strm12Good()
	for round := range 300 {
		budget := &recordingBudget{}
		ctx, records := withCallRecording(WithExecConfig(context.Background(), &ExecConfig{Budget: budget}))
		stream, err := streamWith(ctx, chunksStream(good, "all ", "of ", "it"), lx126TextOptions)
		if err != nil {
			t.Fatalf("the stream: %v", err)
		}

		var wg sync.WaitGroup
		var stayed error
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range stream.All() {
				break
			}
		}()
		go func() {
			defer wg.Done()
			_, stayed = strm12Collect(stream)
		}()
		wg.Wait()
		strm12AwaitProducer(t, stream)

		outcome := stream.Err()
		if (stayed == nil) != (outcome == nil) {
			t.Fatalf("round %d: the reader that stayed was told %v and Err says %v", round, stayed, outcome)
		}
		text, textErr := stream.Text()
		if (textErr == nil) != (outcome == nil) || (outcome == nil) != (text == "all of it") {
			t.Fatalf("round %d: Text = %q, %v and Err says %v", round, text, textErr, outcome)
		}
		recs := records.collect()
		if len(recs) > 1 {
			t.Fatalf("round %d: %d call records for one stream", round, len(recs))
		}
		switch {
		case outcome == nil:
			if len(recs) != 1 || recs[0].Custom["outcome"] != streamOutcomeCompleted {
				t.Fatalf("round %d: the stream finished and its record is %v", round, recs)
			}
		case len(recs) == 1 && recs[0].Custom["outcome"] != streamOutcomeStopped:
			t.Fatalf("round %d: the stream was %v and its record says %v", round, outcome, recs[0].Custom["outcome"])
		}
		if seen := budget.seen(); len(seen) != 1 {
			t.Fatalf("round %d: the budget was told %v", round, seen)
		}
	}
}

// The producer decides how the stream ended before it charges anything. A reader
// that left, or a Cancel, while the final chunk was in flight used to record a
// cancellation while the producer charged the budget and published a record for
// a stream that completed.
func TestAStreamThatWasStoppedAsItsAnswerArrivedIsRecordedAsStopped(t *testing.T) {
	good := strm12Good()
	interrupts := []struct {
		name string
		stop func(cancel context.CancelFunc, s *TextStream)
	}{
		{"the reader leaves", func(_ context.CancelFunc, s *TextStream) {
			for range s.All() {
				break // with the answer still in flight
			}
		}},
		{"Cancel", func(_ context.CancelFunc, s *TextStream) { s.Cancel() }},
		{"the caller's context ends and Err is asked", func(cancel context.CancelFunc, s *TextStream) {
			cancel()
			_ = s.Err()
		}},
		{"the caller's context ends and Ended is asked", func(cancel context.CancelFunc, s *TextStream) {
			cancel()
			_ = s.Ended()
		}},
	}
	for _, tc := range interrupts {
		t.Run(tc.name, func(t *testing.T) {
			budget := &recordingBudget{}
			base, cancel := context.WithCancel(WithExecConfig(context.Background(), &ExecConfig{Budget: budget}))
			defer cancel()
			ctx, records := withCallRecording(base)
			reached, gate := make(chan struct{}), strm12NewGate(t)
			provider := strm12Gated([]string{"a"}, reached, gate,
				llm.StreamChunk{Done: true, Response: good})
			stream, err := streamWith(ctx, provider, lx126TextOptions)
			if err != nil {
				t.Fatalf("the stream: %v", err)
			}
			<-reached
			tc.stop(cancel, stream)
			gate.open()
			strm12AwaitProducer(t, stream)

			if err := stream.Err(); !errors.Is(err, types.ErrCanceled) {
				t.Fatalf("Err = %v, want the cancellation", err)
			}
			if text, err := stream.Text(); !errors.Is(err, types.ErrCanceled) || text != "" {
				t.Errorf("Text = %q, %v; want the cancellation", text, err)
			}
			recs := records.collect()
			if len(recs) != 1 || recs[0].Custom["outcome"] != streamOutcomeStopped {
				t.Fatalf("the call record is %v; it was stopped", recs)
			}
			if seen := budget.seen(); len(seen) != 1 || !seen[0] {
				t.Errorf("the budget was told %v; the usage was reported, so one priced record", seen)
			}
		})
	}

	t.Run("the reader leaves after the stream finished", func(t *testing.T) {
		budget := &recordingBudget{}
		ctx, records := withCallRecording(WithExecConfig(context.Background(), &ExecConfig{Budget: budget}))
		stream, err := streamWith(ctx, chunksStream(good, "all ", "of ", "it"), lx126TextOptions)
		if err != nil {
			t.Fatalf("the stream: %v", err)
		}
		waitFor(t, stream.Ended)
		for range stream.All() {
			break
		}
		strm12AwaitProducer(t, stream)
		if text, err := stream.Text(); err != nil || text != "all of it" {
			t.Fatalf("Text = %q, %v; the stream had finished", text, err)
		}
		if recs := records.collect(); len(recs) != 1 || recs[0].Custom["outcome"] != streamOutcomeCompleted {
			t.Errorf("the call record is %v; the stream completed", recs)
		}
	})

	t.Run("Err said cancelled, so the answer that arrives later does not change it", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		reached, gate := make(chan struct{}), strm12NewGate(t)
		stream, err := streamWith(ctx, strm12Gated(nil, reached, gate, llm.StreamChunk{Done: true, Response: good}), lx126TextOptions)
		if err != nil {
			t.Fatalf("the stream: %v", err)
		}
		<-reached
		cancel()
		first := stream.Err()
		gate.open()
		strm12AwaitProducer(t, stream)
		if !errors.Is(first, types.ErrCanceled) || !errors.Is(stream.Err(), types.ErrCanceled) {
			t.Errorf("Err said %v and then %v", first, stream.Err())
		}
	})
}

// Err reports the outcome as soon as it is known, and Ended says whether there
// is one. Err was nil after Cancel, after the caller's context ended and while a
// detached stream read itself to its end, until something read the stream, and
// nil is what a stream that finished returns.
func TestErrAndEndedSayWhatHappenedToAStream(t *testing.T) {
	good := strm12Good()
	failure := errors.New("the provider failed")
	open := func(t *testing.T, provider llm.Provider) *TextStream {
		t.Helper()
		stream, err := streamWith(context.Background(), provider, lx126TextOptions)
		if err != nil {
			t.Fatalf("the stream: %v", err)
		}
		return stream
	}
	flood := func(t *testing.T) (*TextStream, *floodProvider) {
		t.Helper()
		provider := &floodProvider{started: make(chan struct{})}
		stream := open(t, provider)
		t.Cleanup(stream.Cancel)
		<-provider.started
		return stream, provider
	}

	cases := []struct {
		name      string
		run       func(t *testing.T) *TextStream
		wantErr   error // nil: no error
		wantEnded bool
	}{
		{"running", func(t *testing.T) *TextStream { s, _ := flood(t); return s }, nil, false},
		{"Cancel was called", func(t *testing.T) *TextStream { s, _ := flood(t); s.Cancel(); return s }, types.ErrCanceled, true},
		{"the caller's context was cancelled", func(t *testing.T) *TextStream {
			ctx, cancel := context.WithCancel(context.Background())
			provider := &floodProvider{started: make(chan struct{})}
			s, err := streamWith(ctx, provider, lx126TextOptions)
			if err != nil {
				t.Fatalf("the stream: %v", err)
			}
			<-provider.started
			cancel()
			return s
		}, types.ErrCanceled, true},
		{"the deadline passed", func(t *testing.T) *TextStream {
			ctx := strm12NewDeadline()
			provider := &floodProvider{started: make(chan struct{})}
			s, err := streamWith(ctx, provider, lx126TextOptions)
			if err != nil {
				t.Fatalf("the stream: %v", err)
			}
			t.Cleanup(s.Cancel)
			<-provider.started
			ctx.pass()
			// The stream's own context learns of it a moment later.
			waitFor(t, func() bool { return s.ctx.Err() != nil })
			return s
		}, types.ErrTimeout, true},
		{"the provider failed, and nobody has read", func(t *testing.T) *TextStream {
			s := open(t, &namedStreamProvider{name: "openai", seq: func(yield func(llm.StreamChunk, error) bool) {
				yield(llm.StreamChunk{}, failure)
			}})
			waitFor(t, s.Ended)
			return s
		}, failure, true},
		{"it finished, and nobody has read", func(t *testing.T) *TextStream {
			s := open(t, chunksStream(good, "all of it"))
			waitFor(t, s.Ended)
			return s
		}, nil, true},
		{"it finished and was read", func(t *testing.T) *TextStream {
			s := open(t, chunksStream(good, "all of it"))
			if _, err := s.Text(); err != nil {
				t.Fatalf("Text: %v", err)
			}
			return s
		}, nil, true},
		{"it finished, and then the caller's context ended", func(t *testing.T) *TextStream {
			ctx, cancel := context.WithCancel(context.Background())
			s, err := streamWith(ctx, chunksStream(good, "all of it"), lx126TextOptions)
			if err != nil {
				t.Fatalf("the stream: %v", err)
			}
			waitFor(t, s.Ended)
			cancel()
			return s
		}, nil, true},
		{"the reader left the loop", func(t *testing.T) *TextStream {
			s, _ := flood(t)
			for range s.All() {
				break
			}
			return s
		}, types.ErrCanceled, true},
		{"a detached reader left, and the provider is still working", func(t *testing.T) *TextStream {
			reached, gate := make(chan struct{}), strm12NewGate(t)
			s := open(t, strm12Gated([]string{"a"}, reached, gate, llm.StreamChunk{Done: true, Response: good}))
			s.Detach()
			for range s.All() {
				break
			}
			<-reached
			return s
		}, nil, false},
		{"a detached reader left and then Cancel was called", func(t *testing.T) *TextStream {
			reached, gate := make(chan struct{}), strm12NewGate(t)
			s := open(t, strm12Gated([]string{"a"}, reached, gate, llm.StreamChunk{Done: true, Response: good}))
			s.Detach()
			for range s.All() {
				break
			}
			<-reached
			s.Cancel()
			return s
		}, types.ErrCanceled, true},
		{"the scheduler closed on a stream nobody reads", func(t *testing.T) *TextStream {
			provider := &floodProvider{started: make(chan struct{})}
			scheduler := NewScheduler(SchedulerLimits{MaxConcurrent: 1})
			ctx, cancel := context.WithTimeout(WithExecConfig(context.Background(), &ExecConfig{Scheduler: scheduler}), time.Hour)
			t.Cleanup(cancel)
			s, err := streamWith(ctx, provider, lx126TextOptions)
			if err != nil {
				t.Fatalf("the stream: %v", err)
			}
			<-provider.started
			waitFor(t, func() bool { return len(s.items) == cap(s.items) })
			if err := scheduler.Close(context.Background()); err != nil {
				t.Fatalf("Close: %v", err)
			}
			waitFor(t, s.Ended)
			return s
		}, types.ErrShutdown, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.run(t)
			if got := s.Ended(); got != tc.wantEnded {
				t.Errorf("Ended = %v, want %v", got, tc.wantEnded)
			}
			err := s.Err()
			if tc.wantErr == nil && err != nil || tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Errorf("Err = %v, want %v", err, tc.wantErr)
			}
			// Asking does not change the answer.
			if again := s.Err(); (again == nil) != (err == nil) || s.Ended() != tc.wantEnded {
				t.Errorf("asked again: Err %v, Ended %v", again, s.Ended())
			}
		})
	}
}

// A wait with no way out blocked for ever on a provider that ignores its
// context: a range over a detached stream waited on the drain, and the drain
// waited on the channel. Cancel could not release either.
func TestCancelReleasesAReaderOfAProviderThatNeverReturns(t *testing.T) {
	good := strm12Good()
	begin := func(t *testing.T, ctx context.Context) *TextStream {
		t.Helper()
		reached, gate := make(chan struct{}), strm12NewGate(t)
		stream, err := streamWith(ctx, strm12Gated([]string{"a"}, reached, gate, llm.StreamChunk{Done: true, Response: good}), lx126TextOptions)
		if err != nil {
			t.Fatalf("the stream: %v", err)
		}
		stream.Detach()
		for range stream.All() {
			break
		}
		<-reached
		return stream
	}
	within := func(t *testing.T, what string, run func()) {
		t.Helper()
		done := make(chan struct{})
		go func() { defer close(done); run() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s was not released", what)
		}
	}

	t.Run("a detached stream's Text, released by Cancel", func(t *testing.T) {
		stream := begin(t, context.Background())
		var text string
		var err error
		released := make(chan struct{})
		go func() { text, err = stream.Text(); close(released) }()
		stream.Cancel()
		within(t, "Text", func() { <-released })
		if !errors.Is(err, types.ErrCanceled) || text != "" {
			t.Errorf("Text = %q, %v", text, err)
		}
	})
	t.Run("a detached stream's second range, released by Cancel", func(t *testing.T) {
		stream := begin(t, context.Background())
		var last error
		released := make(chan struct{})
		go func() { _, last = strm12Collect(stream); close(released) }()
		stream.Cancel()
		within(t, "the range", func() { <-released })
		if !errors.Is(last, types.ErrCanceled) {
			t.Errorf("the range ended with %v", last)
		}
	})
	t.Run("the drain ends too", func(t *testing.T) {
		stream := begin(t, context.Background())
		stream.Cancel()
		done := stream.draining()
		if done == nil {
			t.Fatal("the detached reader left and nothing drains the stream")
		}
		within(t, "the drain", func() { <-done })
	})
	t.Run("a stream that was never detached, released by Cancel", func(t *testing.T) {
		reached, gate := make(chan struct{}), strm12NewGate(t)
		stream, err := streamWith(context.Background(), strm12Gated(nil, reached, gate, llm.StreamChunk{Done: true, Response: good}), lx126TextOptions)
		if err != nil {
			t.Fatalf("the stream: %v", err)
		}
		<-reached
		var text string
		var textErr error
		released := make(chan struct{})
		before := strm12ReadersWaiting()
		go func() { text, textErr = stream.Text(); close(released) }()
		strm12WaitForReader(t, before)
		stream.Cancel()
		within(t, "Text", func() { <-released })
		if !errors.Is(textErr, types.ErrCanceled) || text != "" {
			t.Errorf("Text = %q, %v", text, textErr)
		}
	})
	t.Run("released by the caller's context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		stream := begin(t, ctx)
		released := make(chan error, 1)
		go func() { _, err := stream.Text(); released <- err }()
		cancel()
		select {
		case err := <-released:
			if !errors.Is(err, types.ErrCanceled) {
				t.Errorf("Text = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Text was not released by the context")
		}
	})
	t.Run("released by the deadline", func(t *testing.T) {
		ctx := strm12NewDeadline()
		stream := begin(t, ctx)
		released := make(chan error, 1)
		go func() { _, err := stream.Text(); released <- err }()
		ctx.pass()
		select {
		case err := <-released:
			if !errors.Is(err, types.ErrTimeout) {
				t.Errorf("Text = %v, want a timeout", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Text was not released by the deadline")
		}
	})
	t.Run("several readers are all released", func(t *testing.T) {
		stream := begin(t, context.Background())
		const readers = 5
		results := make(chan error, readers)
		for range readers {
			go func() { _, err := stream.Text(); results <- err }()
		}
		stream.Cancel()
		for range readers {
			select {
			case err := <-results:
				if !errors.Is(err, types.ErrCanceled) {
					t.Errorf("a reader was told %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("a reader was not released")
			}
		}
	})
}

// A second range already in its loop when a detached reader leaves stops reading
// beside the drain. The fix before this one covered only a range that started
// after the drain did; one already waiting for an item took the pieces the drain
// was reading, and ended with part of the text and no error to say so.
func TestASecondRangeAlreadyWaitingStandsAsideWhenADetachedReaderLeaves(t *testing.T) {
	good := llm.CompletionResponse{Content: "d1d2d3", FinishReason: "stop", Model: "gpt-6-luna", Provider: "openai"}
	for round := range 40 {
		reached, gate := make(chan struct{}), strm12NewGate(t)
		provider := strm12Gated([]string{"d1"}, reached, gate,
			llm.StreamChunk{Delta: "d2"}, llm.StreamChunk{Delta: "d3"}, llm.StreamChunk{Done: true, Response: good})
		stream, err := streamWith(context.Background(), provider, lx126TextOptions)
		if err != nil {
			t.Fatalf("the stream: %v", err)
		}
		stream.Detach()

		type read struct {
			text string
			err  error
		}
		second := make(chan read, 1)
		for delta := range stream.All() {
			if delta != "d1" {
				t.Fatalf("round %d: the first piece was %q", round, delta)
			}
			before := strm12ReadersWaiting()
			go func() {
				text, err := strm12Collect(stream)
				second <- read{text, err}
			}()
			strm12WaitForReader(t, before)
			break // the detached reader leaves while the second one is waiting
		}
		gate.open()

		select {
		case got := <-second:
			if got.text != "" || got.err != nil {
				t.Fatalf("round %d: the second range took %q, %v beside the drain", round, got.text, got.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("round %d: the second range never ended", round)
		}
		if text, err := stream.Text(); err != nil || text != "d1d2d3" {
			t.Fatalf("round %d: Text = %q, %v", round, text, err)
		}
		if stream.Err() != nil {
			t.Fatalf("round %d: Err = %v", round, stream.Err())
		}
	}
}

// Cancel while a detached stream is being read to its end stops it, and what the
// stream says afterwards is the cancellation.
func TestCancelDuringADrainStopsTheStream(t *testing.T) {
	good := strm12Good()
	for _, finishes := range []bool{false, true} {
		t.Run(fmt.Sprintf("the provider goes on to answer: %v", finishes), func(t *testing.T) {
			budget := &recordingBudget{}
			ctx, records := withCallRecording(WithExecConfig(context.Background(), &ExecConfig{Budget: budget}))
			reached, gate := make(chan struct{}), strm12NewGate(t)
			var rest []llm.StreamChunk
			if finishes {
				rest = append(rest, llm.StreamChunk{Done: true, Response: good})
			}
			stream, err := streamWith(ctx, strm12Gated([]string{"a"}, reached, gate, rest...), lx126TextOptions)
			if err != nil {
				t.Fatalf("the stream: %v", err)
			}
			stream.Detach()
			for range stream.All() {
				break
			}
			<-reached
			stream.Cancel()
			gate.open()
			if text, err := stream.Text(); !errors.Is(err, types.ErrCanceled) || text != "" {
				t.Fatalf("Text = %q, %v", text, err)
			}
			strm12AwaitProducer(t, stream)
			if !errors.Is(stream.Err(), types.ErrCanceled) {
				t.Errorf("Err = %v", stream.Err())
			}
			recs := records.collect()
			if finishes && (len(recs) != 1 || recs[0].Custom["outcome"] != streamOutcomeStopped) {
				t.Errorf("the call record is %v", recs)
			}
			// A stream that never answered still sent its request: its record
			// says so, with the usage not reported.
			if !finishes && (len(recs) != 1 || recs[0].Custom["outcome"] != streamOutcomeStopped || recs[0].TokenUsage != nil) {
				t.Errorf("a stream that never answered published %v, want one that stopped with no usage", recs)
			}
		})
	}
}

// runStream, through a stream, for every way a stream ends: what each ledger and
// the call record are told.
func TestAStreamsEndingIsRecordedOnEveryLedger(t *testing.T) {
	good := strm12Good()
	truncated := good
	truncated.FinishReason = "length"
	truncatedNoUsage := truncated
	truncatedNoUsage.Usage = types.TokenUsage{}
	cached := good
	cached.Cached = true
	elsewhere := good
	elsewhere.Provider = "anthropic"
	elsewhereNoUsage := elsewhere
	elsewhereNoUsage.Usage = types.TokenUsage{}
	failure := errors.New("the connection dropped")

	one := func(chunks ...llm.StreamChunk) func(yield func(llm.StreamChunk, error) bool) {
		return func(yield func(llm.StreamChunk, error) bool) {
			for _, chunk := range chunks {
				if !yield(chunk, nil) {
					return
				}
			}
		}
	}
	cases := []struct {
		name       string
		seq        func(yield func(llm.StreamChunk, error) bool)
		policy     bool
		wantErr    error
		wantRecord string // the outcome the call record carries
		wantBudget []bool
		wantLedger string // "priced", "refused", "unknown" or ""
	}{
		{"a clean stream", one(llm.StreamChunk{Delta: "all of it"}, llm.StreamChunk{Done: true, Response: good}), false, nil, streamOutcomeCompleted, []bool{true}, "priced"},
		{"a clean stream whose last chunk carries text", one(llm.StreamChunk{Done: true, Delta: "all of it", Response: good}), false, nil, streamOutcomeCompleted, []bool{true}, "priced"},
		{"a truncated stream", one(llm.StreamChunk{Delta: "all"}, llm.StreamChunk{Done: true, Response: truncated}), false, types.ErrOutputTruncated, streamOutcomeFailed, []bool{true}, "refused"},
		{"a truncated stream that reports no usage", one(llm.StreamChunk{Delta: "all"}, llm.StreamChunk{Done: true, Response: truncatedNoUsage}), false, types.ErrOutputTruncated, streamOutcomeFailed, []bool{false}, "unknown"},
		{"a stream served from a cache", one(llm.StreamChunk{Delta: "all of it"}, llm.StreamChunk{Done: true, Response: cached}), false, nil, streamOutcomeCompleted, nil, ""},
		{"a refused stream", one(llm.StreamChunk{Delta: "all of it"}, llm.StreamChunk{Done: true, Response: elsewhere}), true, types.ErrPolicyViolation, streamOutcomeRefused, []bool{true}, "refused"},
		{"a refused stream that reports no usage", one(llm.StreamChunk{Delta: "all of it"}, llm.StreamChunk{Done: true, Response: elsewhereNoUsage}), true, types.ErrPolicyViolation, streamOutcomeRefused, []bool{false}, ""},
		{"a refused stream whose last chunk carries text", one(llm.StreamChunk{Done: true, Delta: "all of it", Response: elsewhere}), true, types.ErrPolicyViolation, streamOutcomeRefused, []bool{true}, "refused"},
		{"a stream that broke after some text", func(yield func(llm.StreamChunk, error) bool) {
			if yield(llm.StreamChunk{Delta: "all"}, nil) {
				yield(llm.StreamChunk{}, failure)
			}
		}, false, failure, streamOutcomeFailed, []bool{false}, "unknown"},
		{"a stream that broke before anything arrived", func(yield func(llm.StreamChunk, error) bool) {
			yield(llm.StreamChunk{}, failure)
		}, false, failure, streamOutcomeFailed, nil, ""},
		{"a stream that ended with text and no result", one(llm.StreamChunk{Delta: "all"}), false, strm12AnyError, streamOutcomeFailed, []bool{false}, "unknown"},
		{"a stream that ended with nothing", one(), false, strm12AnyError, streamOutcomeFailed, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.policy {
				llm.ResetCapabilityRegistryForTest()
				t.Cleanup(llm.ResetCapabilityRegistryForTest)
				llm.RegisterCapabilities(llm.ProviderCapabilities{Provider: "openai", Model: "gpt-6-luna", Regions: []string{"eu"}})
			}
			budget := &recordingBudget{}
			config := &ExecConfig{Budget: budget}
			if tc.policy {
				config.DataPolicy = types.DataPolicy{AllowedProviders: []string{"openai"}, AllowedRegions: []string{"eu"}}
				config.DataPolicyDeclared = true
			}
			ctx, records := withCallRecording(WithExecConfig(context.Background(), config))
			opts, id := strm12Options()
			stream, err := streamWith(ctx, &namedStreamProvider{name: "openai", seq: tc.seq}, opts)
			if err != nil {
				t.Fatalf("the stream: %v", err)
			}
			_, readErr := stream.Text()
			strm12AwaitProducer(t, stream)

			switch {
			case tc.wantErr == nil && readErr != nil:
				t.Fatalf("Text: %v", readErr)
			case tc.wantErr == strm12AnyError && readErr == nil:
				t.Fatal("Text returned no error for a stream that never produced a result")
			case tc.wantErr != nil && tc.wantErr != strm12AnyError && !errors.Is(readErr, tc.wantErr):
				t.Fatalf("Text: %v, want %v", readErr, tc.wantErr)
			}
			recs := records.collect()
			// Every stream here sent its request, so each has one record, whatever
			// its ending.
			if len(recs) != 1 || recs[0].Custom["outcome"] != tc.wantRecord {
				t.Errorf("the call record is %v, want one that says %q", recs, tc.wantRecord)
			}
			if got := budget.seen(); fmt.Sprint(got) != fmt.Sprint(tc.wantBudget) {
				t.Errorf("the budget was told %v, want %v", got, tc.wantBudget)
			}
			entry, found := pricing.GetRequestCost(id)
			switch tc.wantLedger {
			case "":
				if found {
					t.Errorf("the process ledger has an entry: %+v", entry)
				}
			case "priced":
				if !found || !entry.Cost.Priced || entry.Tags["outcome"] != streamOutcomeCompleted {
					t.Errorf("the process ledger entry is %+v, %v", entry, found)
				}
			case "refused":
				if !found || !entry.Cost.Priced || entry.Tags["refused"] != "true" {
					t.Errorf("the process ledger entry is %+v, %v", entry, found)
				}
			case "unknown":
				if !found || entry.Cost.Priced || entry.Tags["spend"] != "unknown" {
					t.Errorf("the process ledger entry is %+v, %v", entry, found)
				}
			}
		})
	}
}

// A Done chunk that also carries text, arriving as the stream is stopped, loses
// its text and keeps its usage. Its send failed and the stream returned, so the
// figure that was in hand was recorded as spend of unknown size.
func TestAFinalChunkThatCannotBeSentStillReportsItsUsage(t *testing.T) {
	good := strm12Good()
	budget := &recordingBudget{}
	base, cancel := context.WithCancel(WithExecConfig(context.Background(), &ExecConfig{Budget: budget}))
	defer cancel()
	ctx, records := withCallRecording(base)

	filled, gate := make(chan struct{}), strm12NewGate(t)
	provider := &namedStreamProvider{name: "openai", seq: func(yield func(llm.StreamChunk, error) bool) {
		for range textStreamBufferSize {
			if !yield(llm.StreamChunk{Delta: "x"}, nil) {
				return
			}
		}
		close(filled)
		<-gate.ch
		yield(llm.StreamChunk{Done: true, Delta: "tail", Response: good}, nil)
	}}
	opts, id := strm12Options()
	stream, err := streamWith(ctx, provider, opts)
	if err != nil {
		t.Fatalf("the stream: %v", err)
	}
	<-filled
	waitFor(t, func() bool { return len(stream.items) == cap(stream.items) })
	cancel()
	gate.open()
	// Nothing reads the stream until the producer has tried to send: a reader
	// would make room and the send would succeed. The producer's last act before
	// it returns is the ledger entry.
	waitFor(t, func() bool { _, found := pricing.GetRequestCost(id); return found })
	defer strm12AwaitProducer(t, stream)

	if seen := budget.seen(); len(seen) != 1 || !seen[0] {
		t.Errorf("the budget was told %v; the usage was in the last chunk", seen)
	}
	if recs := records.collect(); len(recs) != 1 || recs[0].Custom["outcome"] != streamOutcomeStopped {
		t.Errorf("the call record is %v", recs)
	}
	if entry, found := pricing.GetRequestCost(id); !found || !entry.Cost.Priced {
		t.Errorf("the process ledger entry is %+v, %v", entry, found)
	}
	if !errors.Is(stream.Err(), types.ErrCanceled) {
		t.Errorf("Err = %v", stream.Err())
	}
}

// claim, next and awaitDrain, each on its own.
func TestStreamClaimNextAndAwaitDrain(t *testing.T) {
	cancelled := strm12Cancelled()
	failure := errors.New("the provider failed")
	answer := llm.CompletionResponse{Content: "the answer"}

	claims := []struct {
		name  string
		items []textStreamItem
		stand []bool
		err   error
		text  string
	}{
		{"a result", []textStreamItem{{final: true, resp: answer}}, []bool{true}, nil, "the answer"},
		{"an error", []textStreamItem{{final: true, err: failure}}, []bool{true}, failure, ""},
		{"a result, then an error", []textStreamItem{{final: true, resp: answer}, {final: true, err: failure}}, []bool{true, false}, nil, "the answer"},
		{"an error, then a result", []textStreamItem{{final: true, err: failure}, {final: true, resp: answer}}, []bool{true, false}, failure, ""},
		{"a cancellation, then a result", []textStreamItem{{final: true, err: cancelled}, {final: true, resp: answer}}, []bool{true, false}, cancelled, ""},
		{"a result, then a cancellation", []textStreamItem{{final: true, resp: answer}, {final: true, err: cancelled}}, []bool{true, false}, nil, "the answer"},
		{"an empty result", []textStreamItem{{final: true}}, []bool{true}, nil, ""},
		{"three claims", []textStreamItem{{final: true, err: failure}, {final: true, err: cancelled}, {final: true, resp: answer}}, []bool{true, false, false}, failure, ""},
		{"the same claim twice", []textStreamItem{{final: true, err: failure}, {final: true, err: failure}}, []bool{true, false}, failure, ""},
		{"an error beside a response", []textStreamItem{{final: true, err: failure, resp: answer}}, []bool{true}, failure, ""},
	}
	for _, tc := range claims {
		t.Run("claim: "+tc.name, func(t *testing.T) {
			s := strm12Literal(t, 1)
			for i, item := range tc.items {
				if got := s.claim(item); got != tc.stand[i] {
					t.Errorf("claim %d stood: %v, want %v", i, got, tc.stand[i])
				}
			}
			// What stands is what a reader that finds nothing in its hands is told.
			close(s.items)
			err := s.endWithoutFinal()
			if tc.err == nil && err != nil || tc.err != nil && !errors.Is(err, tc.err) {
				t.Errorf("the outcome is %v, want %v", err, tc.err)
			}
			if s.finalText != tc.text {
				t.Errorf("text = %q, want %q", s.finalText, tc.text)
			}
		})
	}

	nexts := []struct {
		name   string
		queued []textStreamItem
		closed bool // the producer closed the channel
		over   bool // the stream's context is over
		begun  bool // the drain has started
		watch  bool
		want   readState
		delta  string
	}{
		{"an item is queued", []textStreamItem{{delta: "a"}}, false, false, false, true, readTook, "a"},
		{"nothing queued and the channel closed", nil, true, false, false, true, readClosed, ""},
		{"an item is queued and the channel closed", []textStreamItem{{delta: "a"}}, true, false, false, true, readTook, "a"},
		{"nothing queued and the stream stopped", nil, false, true, false, true, readStopped, ""},
		{"an item is queued and the stream stopped", []textStreamItem{{delta: "a"}}, false, true, false, true, readTook, "a"},
		{"the channel closed and the stream stopped", nil, true, true, false, true, readClosed, ""},
		{"the drain has begun and an item is queued", []textStreamItem{{delta: "a"}}, false, false, true, true, readDraining, ""},
		{"the drain has begun, read by the drain itself", []textStreamItem{{delta: "a"}}, false, false, true, false, readTook, "a"},
		{"the drain has begun and the stream stopped", nil, false, true, true, true, readDraining, ""},
		{"the drain has begun, the stream stopped, read by the drain", nil, false, true, true, false, readStopped, ""},
		{"the drain has begun and the channel closed, read by the drain", nil, true, false, true, false, readClosed, ""},
		{"a final item is queued", []textStreamItem{{final: true, resp: answer}}, false, false, false, true, readTook, ""},
	}
	for _, tc := range nexts {
		t.Run("next: "+tc.name, func(t *testing.T) {
			s := strm12Literal(t, 4)
			for _, item := range tc.queued {
				s.items <- item
			}
			if tc.closed {
				close(s.items)
			}
			if tc.over {
				s.cancel()
			}
			if tc.begun {
				close(s.drainSignal())
			}
			item, state := s.next(tc.watch)
			if state != tc.want || item.delta != tc.delta {
				t.Errorf("next = %q, %d; want %q, %d", item.delta, state, tc.delta, tc.want)
			}
		})
	}

	awaits := []struct {
		name      string
		finished  bool // the drain has finished and kept its outcome
		over      bool
		claimed   *textStreamItem
		wantErr   error
		wantText  string
		wantStays bool // the outcome is the one that was claimed
	}{
		{"the drain finished with a result", true, false, &textStreamItem{final: true, resp: answer}, nil, "the answer", true},
		{"the drain finished with an error", true, false, &textStreamItem{final: true, err: failure}, failure, "", true},
		{"the stream was stopped, nothing claimed", false, true, nil, types.ErrCanceled, "", false},
		{"the stream was stopped, a result claimed", false, true, &textStreamItem{final: true, resp: answer}, nil, "the answer", true},
		{"the stream was stopped, an error claimed", false, true, &textStreamItem{final: true, err: failure}, failure, "", true},
		{"the drain finished and the stream was stopped, a result claimed", true, true, &textStreamItem{final: true, resp: answer}, nil, "the answer", true},
		{"the drain finished and the stream was stopped, an error claimed", true, true, &textStreamItem{final: true, err: failure}, failure, "", true},
		{"the drain finished with nothing claimed", true, false, nil, nil, "", false},
		{"the stream was stopped, a cancellation claimed", false, true, &textStreamItem{final: true, err: cancelled}, types.ErrCanceled, "", true},
		{"the drain finished, a cancellation claimed", true, false, &textStreamItem{final: true, err: cancelled}, types.ErrCanceled, "", true},
	}
	for _, tc := range awaits {
		t.Run("awaitDrain: "+tc.name, func(t *testing.T) {
			s := strm12Literal(t, 1)
			if tc.claimed != nil {
				claimed := *tc.claimed
				s.final = &claimed
			}
			done := make(chan struct{})
			if tc.finished {
				close(done)
			}
			if tc.over {
				s.cancel()
			}
			got := make(chan error, 1)
			go func() { got <- s.awaitDrain(done) }()
			var err error
			select {
			case err = <-got:
			case <-time.After(5 * time.Second):
				t.Fatal("awaitDrain was not released")
			}
			switch {
			case tc.wantErr == nil && tc.claimed == nil && tc.finished:
				if err == nil {
					t.Error("a drain that finished with nothing recorded reads as a clean finish")
				}
			case tc.wantErr == nil && err != nil, tc.wantErr != nil && !errors.Is(err, tc.wantErr):
				t.Errorf("awaitDrain = %v, want %v", err, tc.wantErr)
			}
			if tc.wantText != "" && s.finalText != tc.wantText {
				t.Errorf("text = %q, want %q", s.finalText, tc.wantText)
			}
			if ended, _ := s.outcome(); !ended {
				t.Error("the outcome was not recorded")
			}
		})
	}
}

// stoppedWhileSending, streamCallRecord, recordStoppedStreamAnswer and
// recordUnknownStreamSpend, each on its own.
func TestStreamAccountingHelpers(t *testing.T) {
	over := func() context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}
	timedOut := func() context.Context {
		ctx := strm12NewDeadline()
		ctx.pass()
		return ctx
	}
	stream := func(t *testing.T, ctx context.Context) *TextStream {
		s := strm12Literal(t, 0)
		s.ctx = ctx
		return s
	}

	whys := []struct {
		name string
		run  context.Context
		own  context.Context
		want error
	}{
		{"the run is over", over(), context.Background(), types.ErrCanceled},
		{"the stream is over", context.Background(), over(), types.ErrCanceled},
		{"both are over", over(), over(), types.ErrCanceled},
		{"the run timed out", timedOut(), context.Background(), types.ErrTimeout},
		{"the stream timed out", context.Background(), timedOut(), types.ErrTimeout},
		{"the run timed out and the stream was cancelled", timedOut(), over(), types.ErrTimeout},
		{"the run was cancelled and the stream timed out", over(), timedOut(), types.ErrCanceled},
		{"neither is over", context.Background(), context.Background(), types.ErrProviderUnavailable},
		{"both timed out", timedOut(), timedOut(), types.ErrTimeout},
		{"an open run and a stream that timed out", context.Background(), timedOut(), types.ErrTimeout},
	}
	for _, tc := range whys {
		t.Run("stoppedWhileSending: "+tc.name, func(t *testing.T) {
			got := stoppedWhileSending(tc.run, stream(t, tc.own))
			if got == nil || !errors.Is(got, tc.want) {
				t.Errorf("stoppedWhileSending = %v, want %v", got, tc.want)
			}
		})
	}

	provider := &namedStreamProvider{name: "openai"}
	cached := strm12Good()
	cached.Cached = true
	noModel := strm12Good()
	noModel.Model, noModel.Provider = "", ""
	records := []struct {
		name    string
		resp    llm.CompletionResponse
		outcome string
		model   string
		wantMod string
		wantPro string
	}{
		{"completed", strm12Good(), streamOutcomeCompleted, "gpt-6-luna", "gpt-6-luna", "openai"},
		{"refused", strm12Good(), streamOutcomeRefused, "gpt-6-luna", "gpt-6-luna", "openai"},
		{"stopped", strm12Good(), streamOutcomeStopped, "gpt-6-luna", "gpt-6-luna", "openai"},
		{"the response names no model or provider", noModel, streamOutcomeCompleted, "gpt-6-luna", "gpt-6-luna", "openai"},
		{"the response names another model", llm.CompletionResponse{Model: "gpt-6-sol", Provider: "openai", Usage: strm12Usage}, streamOutcomeStopped, "gpt-6-luna", "gpt-6-sol", "openai"},
		{"the response names another provider", llm.CompletionResponse{Model: "gpt-6-luna", Provider: "anthropic", Usage: strm12Usage}, streamOutcomeRefused, "gpt-6-luna", "gpt-6-luna", "anthropic"},
		{"cached", cached, streamOutcomeCompleted, "gpt-6-luna", "gpt-6-luna", "openai"},
		{"no usage", llm.CompletionResponse{Model: "gpt-6-luna"}, streamOutcomeStopped, "gpt-6-luna", "gpt-6-luna", "openai"},
		{"empty", llm.CompletionResponse{}, streamOutcomeCompleted, "gpt-6-luna", "gpt-6-luna", "openai"},
		{"empty and refused", llm.CompletionResponse{}, streamOutcomeRefused, "gpt-6-luna", "gpt-6-luna", "openai"},
	}
	for _, tc := range records {
		t.Run("streamCallRecord: "+tc.name, func(t *testing.T) {
			tracking := requesttracking.Metadata{RequestID: "r", CorrelationID: "c"}
			meta := streamCallRecord(context.Background(), tracking, provider, tc.model, "text", types.OpOptions{SchemaID: "s"}, tc.resp, time.Now(), tc.outcome)
			if meta.Model != tc.wantMod || meta.Provider != tc.wantPro {
				t.Errorf("the record names %s/%s, want %s/%s", meta.Provider, meta.Model, tc.wantPro, tc.wantMod)
			}
			if meta.Custom["outcome"] != tc.outcome || meta.Custom["streamed"] != true || meta.Custom["schema_id"] != "s" {
				t.Errorf("Custom = %v", meta.Custom)
			}
			if meta.RequestID != "r" || meta.CorrelationID != "c" || meta.CostInfo == nil {
				t.Errorf("the record is incomplete: %+v", meta)
			}
			if meta.RequestedModel != tc.model || meta.ObservedModel != tc.resp.Model {
				t.Errorf("the record asked for %q and was answered by %q, want %q and %q", meta.RequestedModel, meta.ObservedModel, tc.model, tc.resp.Model)
			}
			// A response that reports no usage, and was not served from a cache,
			// has not used nothing: it has not said, and the record says that.
			if reported := tc.resp.Cached || reportsUsage(tc.resp.Usage); reported != (meta.TokenUsage != nil) {
				t.Errorf("usage reported = %v, the record carries %v", reported, meta.TokenUsage)
			} else if !reported && (meta.CostInfo.Priced || meta.CostInfo.Quality != types.PricingUnknown) {
				t.Errorf("the cost of unreported usage is %+v, want one marked unknown", meta.CostInfo)
			}
		})
	}

	stops := []struct {
		name       string
		resp       llm.CompletionResponse
		outcome    string
		budget     bool
		wantBudget []bool
		wantLedger bool
	}{
		{"refused with usage", strm12Good(), streamOutcomeRefused, true, []bool{true}, true},
		{"stopped with usage", strm12Good(), streamOutcomeStopped, true, []bool{true}, true},
		{"refused with no usage", llm.CompletionResponse{Model: "gpt-6-luna", Provider: "openai"}, streamOutcomeRefused, true, []bool{false}, false},
		{"stopped with no usage", llm.CompletionResponse{Model: "gpt-6-luna", Provider: "openai"}, streamOutcomeStopped, true, []bool{false}, false},
		{"refused from a cache", cached, streamOutcomeRefused, true, nil, false},
		{"stopped from a cache", cached, streamOutcomeStopped, true, nil, false},
		{"refused with no client budget", strm12Good(), streamOutcomeRefused, false, nil, true},
		{"stopped with no client budget", strm12Good(), streamOutcomeStopped, false, nil, true},
		{"no usage and no client budget", llm.CompletionResponse{Model: "gpt-6-luna"}, streamOutcomeStopped, false, nil, false},
		{"a cache and no client budget", cached, streamOutcomeRefused, false, nil, false},
	}
	for _, tc := range stops {
		t.Run("recordStoppedStreamAnswer: "+tc.name, func(t *testing.T) {
			budget := &recordingBudget{}
			base := context.Background()
			if tc.budget {
				base = WithExecConfig(base, &ExecConfig{Budget: budget})
			}
			ctx, collected := withCallRecording(base)
			opts, id := strm12Options()
			tracking := requesttracking.Metadata{RequestID: id}
			recordStoppedStreamAnswer(ctx, tracking, provider, "gpt-6-luna", "text", opts, tc.resp, time.Now(), tc.outcome)

			recs := collected.collect()
			if len(recs) != 1 || recs[0].Custom["outcome"] != tc.outcome {
				t.Fatalf("the call record is %v", recs)
			}
			if _, refused := recs[0].Custom["refused"]; refused != (tc.outcome == streamOutcomeRefused) {
				t.Errorf("the refused mark is %v on a %s record", refused, tc.outcome)
			}
			if got := budget.seen(); fmt.Sprint(got) != fmt.Sprint(tc.wantBudget) {
				t.Errorf("the budget was told %v, want %v", got, tc.wantBudget)
			}
			if _, found := pricing.GetRequestCost(id); found != tc.wantLedger {
				t.Errorf("the process ledger has an entry: %v, want %v", found, tc.wantLedger)
			}
		})
	}

	unknowns := []struct {
		name     string
		budget   bool
		provider string
		model    string
	}{
		{"a client with a budget", true, "openai", "gpt-6-luna"},
		{"no client budget", false, "openai", "gpt-6-luna"},
		{"another provider", true, "anthropic", "claude-x"},
		{"another provider and no budget", false, "anthropic", "claude-x"},
		{"no model", true, "openai", ""},
		{"no model and no budget", false, "openai", ""},
		{"a local provider", false, "local", "m"},
		{"a local provider with a budget", true, "local", "m"},
		{"a long model name", true, "openai", strings.Repeat("m", 200)},
		{"a long model name and no budget", false, "openai", strings.Repeat("m", 200)},
	}
	for _, tc := range unknowns {
		t.Run("recordUnknownStreamSpend: "+tc.name, func(t *testing.T) {
			budget := &recordingBudget{}
			ctx := context.Background()
			if tc.budget {
				ctx = WithExecConfig(ctx, &ExecConfig{Budget: budget})
			}
			_, id := strm12Options()
			recordUnknownStreamSpend(ctx, requesttracking.Metadata{RequestID: id}, &namedStreamProvider{name: tc.provider}, tc.model)

			want := []bool(nil)
			if tc.budget {
				want = []bool{false}
			}
			if got := budget.seen(); fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("the budget was told %v, want %v", got, want)
			}
			entry, found := pricing.GetRequestCost(id)
			if !found || entry.Cost.Priced || entry.Cost.TotalCost != 0 || entry.Tags["spend"] != "unknown" {
				t.Errorf("the process ledger entry is %+v, %v; want one with no price and no amount", entry, found)
			}
			if entry.Provider != tc.provider || entry.Model != tc.model {
				t.Errorf("the entry names %s/%s", entry.Provider, entry.Model)
			}
		})
	}
}

// Text, Err and Ended are consistent with each other whichever way a stream is
// read, and a stream that nobody reads and that finished still gives its whole
// text afterwards.
func TestTextAfterAnUnreadStreamFinished(t *testing.T) {
	good := strm12Good()
	stream, err := streamWith(context.Background(), chunksStream(good, "all ", "of ", "it"), lx126TextOptions)
	if err != nil {
		t.Fatalf("the stream: %v", err)
	}
	waitFor(t, stream.Ended)
	if stream.Err() != nil {
		t.Fatalf("Err = %v", stream.Err())
	}
	for range 3 {
		if text, err := stream.Text(); err != nil || text != "all of it" {
			t.Fatalf("Text = %q, %v", text, err)
		}
	}
}
