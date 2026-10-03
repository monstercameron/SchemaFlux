package ops

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// endAnswer is the text every scripted stream answers with. No end report may
// hold it.
const endAnswer = "THE-ANSWER-TEXT-9c41"

// endStreamProvider is a streaming provider whose stream is a script, so a test
// decides where it is blocked and what it yields.
type endStreamProvider struct {
	run   func(ctx context.Context, yield func(llm.StreamChunk, error) bool)
	calls atomic.Int32
}

func (p *endStreamProvider) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{}, errors.New("Complete must not be called by the streaming path")
}
func (p *endStreamProvider) Name() string                               { return "endstream" }
func (p *endStreamProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *endStreamProvider) RetryPolicy() (int, time.Duration)          { return 0, 0 }
func (p *endStreamProvider) CompleteStream(ctx context.Context, _ llm.CompletionRequest) iter.Seq2[llm.StreamChunk, error] {
	return func(yield func(llm.StreamChunk, error) bool) {
		p.calls.Add(1)
		p.run(ctx, yield)
	}
}

// endDone is a provider's last chunk, with the facts the end report carries.
func endDone(finish string) llm.StreamChunk {
	return llm.StreamChunk{Done: true, Response: llm.CompletionResponse{
		Content:      endAnswer,
		Provider:     "endstream",
		Model:        "end-model-served",
		FinishReason: finish,
		Usage:        types.TokenUsage{PromptTokens: 7, CompletionTokens: 5, TotalTokens: 12},
	}}
}

// endCompletes yields the deltas and then finishes cleanly.
func endCompletes(deltas ...string) func(context.Context, func(llm.StreamChunk, error) bool) {
	return func(_ context.Context, yield func(llm.StreamChunk, error) bool) {
		for _, delta := range deltas {
			if !yield(llm.StreamChunk{Delta: delta}, nil) {
				return
			}
		}
		yield(endDone("stop"), nil)
	}
}

// endWatcher is the callback a test installs. It records what each call saw,
// and holds the producer inside the callback until gate is closed when it has
// one.
type endWatcher struct {
	calls atomic.Int32
	gate  chan struct{}

	mu   sync.Mutex
	seen []endSeen
}

type endSeen struct {
	ended bool
	err   error
	info  streamEnd
}

func (w *endWatcher) fn(s *TextStream) {
	w.calls.Add(1)
	ended, err := s.Outcome()
	w.mu.Lock()
	w.seen = append(w.seen, endSeen{ended: ended, err: err, info: s.ending()})
	w.mu.Unlock()
	if w.gate != nil {
		<-w.gate
	}
}

func (w *endWatcher) only(t *testing.T) endSeen {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.seen) != 1 {
		t.Fatalf("the end callback saw %d streams end, want 1", len(w.seen))
	}
	return w.seen[0]
}

// openWithEnd opens a stream on provider whose end is reported to watcher.
func openWithEnd(ctx context.Context, provider llm.Provider, watcher *endWatcher) (*TextStream, error) {
	ctx = WithProvider(withStreamEnd(ctx, watcher.fn), provider)
	return streamLLM(ctx, "system", "user", types.OpOptions{Model: "end-model"})
}

// readAll ranges over the stream and returns the text and the last error.
func readAll(stream *TextStream) (string, error) {
	var text strings.Builder
	var last error
	for delta, err := range stream.All() {
		text.WriteString(delta)
		if err != nil {
			last = err
		}
	}
	return text.String(), last
}

// The callback has run, once, when the range loop that took the last item ends
// and when Text returns, and it ran before that item was handed over: the
// producer, held inside the callback, has not sent it, so the reader is still
// waiting for it.
func TestStreamEndRunsOnceBeforeTheLastItem(t *testing.T) {
	t.Run("range", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			watcher := &endWatcher{gate: make(chan struct{})}
			stream, err := openWithEnd(context.Background(), &endStreamProvider{run: endCompletes("one ", "two")}, watcher)
			if err != nil {
				t.Fatalf("streamLLM: %v", err)
			}
			var read atomic.Int32
			var done atomic.Bool
			go func() {
				defer done.Store(true)
				for range stream.All() {
					read.Add(1)
				}
				if got := watcher.calls.Load(); got != 1 {
					t.Errorf("the loop ended with %d callbacks run, want 1", got)
				}
			}()

			// Every goroutine is blocked: the producer inside the callback, and the
			// reader on an item that has not been sent.
			synctest.Wait()
			if watcher.calls.Load() != 1 {
				t.Fatalf("the callback was not running while the producer was held: %d calls", watcher.calls.Load())
			}
			if done.Load() {
				t.Fatal("the reader had its last item while the end callback was still running")
			}
			if got := read.Load(); got != 2 {
				t.Errorf("the reader took %d deltas before the end, want both", got)
			}
			if ended, err := stream.Outcome(); !ended || err != nil {
				t.Errorf("Outcome inside the callback = %v, %v, want an ended, clean stream", ended, err)
			}
			close(watcher.gate)
			synctest.Wait()
			if !done.Load() {
				t.Fatal("the reader did not finish once the callback returned")
			}
			if got := watcher.calls.Load(); got != 1 {
				t.Errorf("the callback ran %d times, want 1", got)
			}
		})
	})
	t.Run("Text", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			watcher := &endWatcher{}
			stream, err := openWithEnd(context.Background(), &endStreamProvider{run: endCompletes("one ", "two")}, watcher)
			if err != nil {
				t.Fatalf("streamLLM: %v", err)
			}
			text, err := stream.Text()
			if err != nil || text != endAnswer {
				t.Fatalf("Text = %q, %v", text, err)
			}
			if got := watcher.calls.Load(); got != 1 {
				t.Errorf("Text returned with %d callbacks run, want 1", got)
			}
			seen := watcher.only(t)
			if seen.info.Kind != streamEndCompleted || seen.err != nil || !seen.ended {
				t.Errorf("a completed stream reported %+v", seen)
			}
		})
	})
}

// What the callback is told of a stream that was read to its end: how, and what
// the provider reported, and none of the answer.
func TestStreamEndReportsWhatTheProviderReported(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		watcher := &endWatcher{}
		stream, err := openWithEnd(context.Background(), &endStreamProvider{run: endCompletes("a", "b", "c")}, watcher)
		if err != nil {
			t.Fatalf("streamLLM: %v", err)
		}
		if text, err := readAll(stream); err != nil || text != "abc" {
			t.Fatalf("read %q, %v", text, err)
		}
		end := watcher.only(t).info
		want := streamEnd{
			Kind: streamEndCompleted, Reported: true, Provider: "endstream", Model: "end-model-served", FinishReason: "stop",
			Usage: types.TokenUsage{PromptTokens: 7, CompletionTokens: 5, TotalTokens: 12},
		}
		if end != want {
			t.Errorf("the end report is %+v, want %+v", end, want)
		}
		if strings.Contains(fmt.Sprintf("%+v", end), endAnswer) {
			t.Error("the end report holds the answer's text")
		}
		if got := stream.ending(); got != want {
			t.Errorf("ending after the end = %+v, want the report the callback saw", got)
		}
		if end.Kind.String() != "completed" {
			t.Errorf("Kind.String() = %q", end.Kind.String())
		}
	})
}

// Every way a stream ends runs the callback once, with the stream's own
// outcome, which is what its readers are told.
func TestStreamEndRunsOnceForEveryWayAStreamEnds(t *testing.T) {
	providerErr := types.Newf(types.KindProviderUnavailable, "endstream", "the provider fell over")

	t.Run("read to the end", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			watcher := &endWatcher{}
			stream, _ := openWithEnd(context.Background(), &endStreamProvider{run: endCompletes("x")}, watcher)
			if _, err := readAll(stream); err != nil {
				t.Fatalf("read: %v", err)
			}
			synctest.Wait()
			if got := watcher.only(t).info.Kind; got != streamEndCompleted {
				t.Errorf("kind = %v", got)
			}
		})
	})

	t.Run("an error from the provider before the first chunk", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			watcher := &endWatcher{}
			provider := &endStreamProvider{run: func(_ context.Context, yield func(llm.StreamChunk, error) bool) {
				yield(llm.StreamChunk{}, providerErr)
			}}
			stream, _ := openWithEnd(context.Background(), provider, watcher)
			text, err := readAll(stream)
			if text != "" || !errors.Is(err, providerErr) {
				t.Fatalf("read %q, %v", text, err)
			}
			seen := watcher.only(t)
			if seen.info.Kind != streamEndFailed || !errors.Is(seen.err, providerErr) || seen.info.Reported {
				t.Errorf("a stream that failed at once reported %+v", seen)
			}
			if types.KindOf(seen.err) != types.KindProviderUnavailable {
				t.Errorf("the report's error has kind %v", types.KindOf(seen.err))
			}
			if _, got := stream.Outcome(); got != err {
				t.Errorf("Outcome says %v, the reader was told %v", got, err)
			}
		})
	})

	t.Run("an error from the provider midway", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			watcher := &endWatcher{}
			provider := &endStreamProvider{run: func(_ context.Context, yield func(llm.StreamChunk, error) bool) {
				yield(llm.StreamChunk{Delta: "half "}, nil)
				yield(llm.StreamChunk{Delta: "of it "}, nil)
				yield(llm.StreamChunk{}, providerErr)
			}}
			stream, _ := openWithEnd(context.Background(), provider, watcher)
			text, err := readAll(stream)
			if text != "half of it " || !errors.Is(err, providerErr) {
				t.Fatalf("read %q, %v", text, err)
			}
			seen := watcher.only(t)
			if seen.info.Kind != streamEndFailed || seen.err != err {
				t.Errorf("a stream that failed midway reported %+v, the reader got %v", seen, err)
			}
		})
	})

	t.Run("an answer that is refused", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			watcher := &endWatcher{}
			provider := &endStreamProvider{run: func(_ context.Context, yield func(llm.StreamChunk, error) bool) {
				yield(llm.StreamChunk{Delta: "cut off"}, nil)
				yield(endDone("length"), nil)
			}}
			stream, _ := openWithEnd(context.Background(), provider, watcher)
			_, err := readAll(stream)
			if types.KindOf(err) != types.KindOutputTruncated {
				t.Fatalf("the reader got %v (kind %v), want a truncation", err, types.KindOf(err))
			}
			seen := watcher.only(t)
			if seen.info.Kind != streamEndFailed || types.KindOf(seen.err) != types.KindOutputTruncated {
				t.Errorf("a truncated stream reported %+v", seen)
			}
			if !seen.info.Reported || seen.info.FinishReason != "length" || seen.info.Usage.TotalTokens != 12 {
				t.Errorf("the provider's report of a refused answer was lost: %+v", seen.info)
			}
		})
	})

	t.Run("the reader stops early and closes", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			watcher := &endWatcher{}
			provider := &endStreamProvider{run: func(ctx context.Context, yield func(llm.StreamChunk, error) bool) {
				if !yield(llm.StreamChunk{Delta: "first"}, nil) {
					return
				}
				<-ctx.Done()
				yield(llm.StreamChunk{}, ctx.Err())
			}}
			stream, _ := openWithEnd(context.Background(), provider, watcher)
			for delta, err := range stream.All() {
				if err != nil || delta != "first" {
					t.Fatalf("took %q, %v", delta, err)
				}
				break
			}
			synctest.Wait()
			seen := watcher.only(t)
			if seen.info.Kind != streamEndClosed || !errors.Is(seen.err, context.Canceled) {
				t.Errorf("a stream the reader left reported %+v", seen)
			}
			if got := stream.Err(); got != seen.err {
				t.Errorf("Err = %v, the callback was told %v", got, seen.err)
			}
		})
	})

	t.Run("the reader stops early and never closes, and the context ends", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			watcher := &endWatcher{}
			deltas := make([]string, 4*textStreamBufferSize)
			for i := range deltas {
				deltas[i] = "x"
			}
			ctx, cancel := context.WithCancel(context.Background())
			stream, _ := openWithEnd(ctx, &endStreamProvider{run: endCompletes(deltas...)}, watcher)
			// The buffer fills and the producer blocks sending to a reader that
			// never comes.
			synctest.Wait()
			if got := watcher.calls.Load(); got != 0 {
				t.Fatalf("the callback ran %d times for a stream still being produced", got)
			}
			cancel()
			synctest.Wait()
			seen := watcher.only(t)
			if seen.info.Kind != streamEndContextEnded || !errors.Is(seen.err, context.Canceled) {
				t.Errorf("a stream whose context ended reported %+v", seen)
			}
			if types.KindOf(seen.err) != types.KindCanceled {
				t.Errorf("the error has kind %v, want canceled", types.KindOf(seen.err))
			}
			_ = stream
		})
	})

	t.Run("the context passes its deadline", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			watcher := &endWatcher{}
			provider := &endStreamProvider{run: func(ctx context.Context, yield func(llm.StreamChunk, error) bool) {
				<-ctx.Done()
				yield(llm.StreamChunk{}, ctx.Err())
			}}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			stream, _ := openWithEnd(ctx, provider, watcher)
			_, err := readAll(stream)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("the reader got %v", err)
			}
			// The reader is released by the context ending, which is before the
			// producer has returned from the provider: the callback runs when it does.
			synctest.Wait()
			seen := watcher.only(t)
			if seen.info.Kind != streamEndContextEnded || types.KindOf(seen.err) != types.KindTimeout {
				t.Errorf("a stream past its deadline reported %+v (kind %v)", seen, types.KindOf(seen.err))
			}
		})
	})

	t.Run("the reader panics out of the loop", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			watcher := &endWatcher{}
			provider := &endStreamProvider{run: func(ctx context.Context, yield func(llm.StreamChunk, error) bool) {
				if !yield(llm.StreamChunk{Delta: "first"}, nil) {
					return
				}
				<-ctx.Done()
				yield(llm.StreamChunk{}, ctx.Err())
			}}
			stream, _ := openWithEnd(context.Background(), provider, watcher)
			func() {
				defer func() {
					if recovered := recover(); recovered != "the reader's own failure" {
						t.Errorf("recovered %v", recovered)
					}
				}()
				for range stream.All() {
					panic("the reader's own failure")
				}
			}()
			synctest.Wait()
			seen := watcher.only(t)
			if seen.info.Kind != streamEndClosed {
				t.Errorf("a stream whose reader panicked reported %+v", seen)
			}
		})
	})

	t.Run("two readers", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			watcher := &endWatcher{}
			stream, _ := openWithEnd(context.Background(), &endStreamProvider{run: endCompletes("1", "2", "3", "4", "5", "6")}, watcher)
			var wait sync.WaitGroup
			var gotErr [2]error
			var text [2]string
			for i := range gotErr {
				wait.Go(func() { text[i], gotErr[i] = readAll(stream) })
			}
			wait.Wait()
			if got := watcher.calls.Load(); got != 1 {
				t.Fatalf("two readers ran the callback %d times, want 1", got)
			}
			if joined := len(text[0]) + len(text[1]); joined != 6 {
				t.Errorf("the readers shared %d of 6 deltas", joined)
			}
			if gotErr[0] != nil || gotErr[1] != nil {
				t.Errorf("readers were told %v and %v of a stream that completed", gotErr[0], gotErr[1])
			}
			if got := watcher.only(t).info.Kind; got != streamEndCompleted {
				t.Errorf("kind = %v", got)
			}
		})
	})

	t.Run("Cancel", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			watcher := &endWatcher{}
			provider := &endStreamProvider{run: func(ctx context.Context, yield func(llm.StreamChunk, error) bool) {
				<-ctx.Done()
				yield(llm.StreamChunk{}, ctx.Err())
			}}
			stream, _ := openWithEnd(context.Background(), provider, watcher)
			synctest.Wait()
			stream.Cancel()
			synctest.Wait()
			if got := watcher.only(t).info.Kind; got != streamEndClosed {
				t.Errorf("a cancelled stream reported %v", got)
			}
		})
	})

	t.Run("a detached reader leaves and the stream finishes", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			watcher := &endWatcher{}
			release := make(chan struct{})
			provider := &endStreamProvider{run: func(ctx context.Context, yield func(llm.StreamChunk, error) bool) {
				if !yield(llm.StreamChunk{Delta: "first "}, nil) {
					return
				}
				<-release
				yield(llm.StreamChunk{Delta: "second"}, nil)
				yield(endDone("stop"), nil)
			}}
			stream, _ := openWithEnd(context.Background(), provider, watcher)
			stream.Detach()
			for range stream.All() {
				break
			}
			synctest.Wait()
			if got := watcher.calls.Load(); got != 0 {
				t.Fatalf("the callback ran for a stream still being produced: %d", got)
			}
			close(release)
			synctest.Wait()
			if got := watcher.only(t).info.Kind; got != streamEndCompleted {
				t.Errorf("a detached stream that finished reported %v", got)
			}
		})
	})

	t.Run("the stream is not opened under a scheduler that admits it later than the end", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			scheduler := NewScheduler(SchedulerLimits{MaxConcurrent: 1})
			defer scheduler.Close(context.Background())
			watcher := &endWatcher{}
			ctx := WithSharedExecConfig(context.Background(), &ExecConfig{Scheduler: scheduler})
			stream, err := openWithEnd(ctx, &endStreamProvider{run: endCompletes("a", "b")}, watcher)
			if err != nil {
				t.Fatalf("streamLLM: %v", err)
			}
			if text, err := readAll(stream); err != nil || text != "ab" {
				t.Fatalf("read %q, %v", text, err)
			}
			waitForIdle(t, scheduler)
			if got := watcher.calls.Load(); got != 1 {
				t.Errorf("the callback ran %d times under a scheduler", got)
			}
		})
	})
}

// A call that returns an error opened no stream, and nothing is told of an end
// that was never begun: a context that is over before the request is made, a
// provider that cannot stream, a model that cannot be resolved.
func TestStreamEndIsNotRunForACallThatOpenedNoStream(t *testing.T) {
	t.Run("the context is over before the request", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			watcher := &endWatcher{}
			provider := &endStreamProvider{run: endCompletes("x")}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			stream, err := openWithEnd(ctx, provider, watcher)
			if stream != nil || types.KindOf(err) != types.KindCanceled {
				t.Fatalf("streamLLM = %v, %v, want no stream and a cancellation", stream, err)
			}
			synctest.Wait()
			if watcher.calls.Load() != 0 || provider.calls.Load() != 0 {
				t.Errorf("callback ran %d times and the provider was called %d times", watcher.calls.Load(), provider.calls.Load())
			}
		})
	})

	t.Run("the provider cannot stream", func(t *testing.T) {
		watcher := &endWatcher{}
		ctx := WithProvider(withStreamEnd(context.Background(), watcher.fn), &namedProvider{name: "no-stream"})
		stream, err := streamLLM(ctx, "system", "user", types.OpOptions{Model: "end-model"})
		if stream != nil || types.KindOf(err) != types.KindUnsupportedCapability {
			t.Fatalf("streamLLM = %v, %v", stream, err)
		}
		if watcher.calls.Load() != 0 {
			t.Error("the callback ran for a call that opened no stream")
		}
	})

	t.Run("no provider", func(t *testing.T) {
		swapNoProvider(t)
		watcher := &endWatcher{}
		stream, err := streamLLM(withStreamEnd(context.Background(), watcher.fn), "system", "user", types.OpOptions{Model: "end-model"})
		if stream != nil || !errors.Is(err, ErrNoProvider) || watcher.calls.Load() != 0 {
			t.Errorf("streamLLM = %v, %v, callbacks %d", stream, err, watcher.calls.Load())
		}
	})

	t.Run("the scheduler is closed", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			scheduler := NewScheduler(SchedulerLimits{MaxConcurrent: 1})
			scheduler.Close(context.Background())
			watcher := &endWatcher{}
			provider := &endStreamProvider{run: endCompletes("x")}
			ctx := WithSharedExecConfig(context.Background(), &ExecConfig{Scheduler: scheduler})
			stream, err := openWithEnd(ctx, provider, watcher)
			if stream != nil || err == nil {
				t.Fatalf("streamLLM = %v, %v, want a refusal", stream, err)
			}
			synctest.Wait()
			if watcher.calls.Load() != 0 || provider.calls.Load() != 0 {
				t.Errorf("callback ran %d times and the provider was called %d times", watcher.calls.Load(), provider.calls.Load())
			}
		})
	})
}

// A context that ends right after the stream is opened, before its producer has
// reached the provider, ends the stream once.
func TestStreamEndRunsForAContextThatEndsBeforeTheProviderIsReached(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		watcher := &endWatcher{}
		provider := &endStreamProvider{run: func(ctx context.Context, yield func(llm.StreamChunk, error) bool) {
			if err := ctx.Err(); err != nil {
				yield(llm.StreamChunk{}, err)
				return
			}
			endCompletes("never")(ctx, yield)
		}}
		ctx, cancel := context.WithCancel(context.Background())
		stream, err := openWithEnd(ctx, provider, watcher)
		if err != nil {
			t.Fatalf("streamLLM: %v", err)
		}
		cancel() // before the producer's goroutine has run
		_, readErr := readAll(stream)
		if readErr == nil {
			t.Fatal("a cancelled stream read to a clean end")
		}
		synctest.Wait()
		seen := watcher.only(t)
		if seen.info.Kind != streamEndContextEnded {
			t.Errorf("reported %+v", seen)
		}
	})
}

// The callback is told once however the stream is touched after it ends.
func TestStreamEndIsNotRunAgainByWhatHappensAfterTheEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		watcher := &endWatcher{}
		stream, _ := openWithEnd(context.Background(), &endStreamProvider{run: endCompletes("a")}, watcher)
		if _, err := readAll(stream); err != nil {
			t.Fatalf("read: %v", err)
		}
		stream.Cancel()
		stream.Cancel()
		stream.Detach()
		_, _ = readAll(stream)
		_, _ = stream.Text()
		_ = stream.Err()
		stream.ended()
		stream.ended()
		synctest.Wait()
		if got := watcher.calls.Load(); got != 1 {
			t.Errorf("the callback ran %d times, want 1", got)
		}
		// A reader leaving a stream that had ended did not end it: the report is
		// the completion it was.
		if got := stream.ending().Kind; got != streamEndCompleted {
			t.Errorf("the report after a late Cancel is %v, want completed", got)
		}
	})
}

// The callback that is nearest the stream is the one it is told by.
func TestStreamEndNearestCallbackWins(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		outer, inner := &endWatcher{}, &endWatcher{}
		ctx := WithProvider(withStreamEnd(withStreamEnd(context.Background(), outer.fn), inner.fn), &endStreamProvider{run: endCompletes("a")})
		stream, err := streamLLM(ctx, "system", "user", types.OpOptions{Model: "end-model"})
		if err != nil {
			t.Fatalf("streamLLM: %v", err)
		}
		_, _ = readAll(stream)
		if outer.calls.Load() != 0 || inner.calls.Load() != 1 {
			t.Errorf("outer ran %d times and inner %d, want 0 and 1", outer.calls.Load(), inner.calls.Load())
		}
	})
}

// A callback that panics does not take the process down or cost the reader its
// last item, and what it panicked with is not logged.
func TestStreamEndSurvivesACallbackThatPanics(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		logs := captureLogs(t)
		var calls atomic.Int32
		ctx := WithProvider(withStreamEnd(context.Background(), func(*TextStream) {
			calls.Add(1)
			panic("PANIC-VALUE-WITH-A-PAYLOAD-77")
		}), &endStreamProvider{run: endCompletes("a", "b")})
		stream, err := streamLLM(ctx, "system", "user", types.OpOptions{Model: "end-model"})
		if err != nil {
			t.Fatalf("streamLLM: %v", err)
		}
		text, err := stream.Text()
		if err != nil || text != endAnswer {
			t.Fatalf("Text = %q, %v", text, err)
		}
		if calls.Load() != 1 {
			t.Errorf("the callback ran %d times", calls.Load())
		}
		rendered := logs()
		if !strings.Contains(rendered, "end callback panicked") || !strings.Contains(rendered, "string") {
			t.Errorf("the panic was not logged by type: %q", rendered)
		}
		if strings.Contains(rendered, "PANIC-VALUE-WITH-A-PAYLOAD-77") {
			t.Error("the log carries what the callback panicked with")
		}
	})
}

// The callback is told of the same stream, whichever of the producer's two exits
// ended it, and Outcome inside it agrees with every reader.
func TestStreamEndOutcomeInsideTheCallbackAgreesWithTheReaders(t *testing.T) {
	cases := []struct {
		name string
		run  func(ctx context.Context, yield func(llm.StreamChunk, error) bool)
		want streamEndKind
	}{
		{"completes", endCompletes("a"), streamEndCompleted},
		{"fails", func(_ context.Context, yield func(llm.StreamChunk, error) bool) {
			yield(llm.StreamChunk{}, errors.New("plain failure"))
		}, streamEndFailed},
		{"ends with no last chunk", func(_ context.Context, yield func(llm.StreamChunk, error) bool) {
			yield(llm.StreamChunk{Delta: "a"}, nil)
		}, streamEndFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				watcher := &endWatcher{}
				stream, _ := openWithEnd(context.Background(), &endStreamProvider{run: tc.run}, watcher)
				_, readErr := readAll(stream)
				seen := watcher.only(t)
				if !seen.ended || seen.info.Kind != tc.want {
					t.Errorf("reported %+v, want kind %v", seen, tc.want)
				}
				if (seen.err == nil) != (readErr == nil) {
					t.Errorf("the callback was told %v and the reader %v", seen.err, readErr)
				}
				if seen.err != nil && seen.err.Error() != readErr.Error() {
					t.Errorf("the callback was told %q and the reader %q", seen.err, readErr)
				}
			})
		})
	}
}

// A stream opened with no callback behaves as it did: nothing is read from the
// context but one lookup, nothing is allocated for the end, and the report is
// empty.
func TestStreamWithNoEndCallbackIsUnchanged(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := WithProvider(context.Background(), &endStreamProvider{run: endCompletes("a", "b")})
		stream, err := streamLLM(ctx, "system", "user", types.OpOptions{Model: "end-model"})
		if err != nil {
			t.Fatalf("streamLLM: %v", err)
		}
		if stream.onEnd != nil {
			t.Error("a stream opened with no callback has one")
		}
		if text, err := stream.Text(); err != nil || text != endAnswer {
			t.Fatalf("Text = %q, %v", text, err)
		}
		if got := stream.ending(); got != (streamEnd{}) {
			t.Errorf("a stream with no callback has an end report: %+v", got)
		}
		if ended, err := stream.Outcome(); !ended || err != nil {
			t.Errorf("Outcome = %v, %v", ended, err)
		}
	})
	bare := context.Background()
	if allocs := testing.AllocsPerRun(100, func() { _ = streamEndFrom(bare) }); allocs != 0 {
		t.Errorf("reading an absent callback allocates %v", allocs)
	}
	var s TextStream
	if allocs := testing.AllocsPerRun(100, func() { s.ended() }); allocs != 0 {
		t.Errorf("ended on a stream with no callback allocates %v", allocs)
	}
	if got := withStreamEnd(bare, nil); got != bare {
		t.Error("a nil callback changed the context")
	}
}

// Outcome is Ended and Err as one reading, for a stream nobody has read.
func TestOutcomeReportsAStreamNobodyHasRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		provider := &endStreamProvider{run: func(ctx context.Context, yield func(llm.StreamChunk, error) bool) {
			<-release
			endCompletes("a")(ctx, yield)
		}}
		ctx := WithProvider(context.Background(), provider)
		cancelCtx, cancel := context.WithCancel(ctx)
		stream, err := streamLLM(cancelCtx, "system", "user", types.OpOptions{Model: "end-model"})
		if err != nil {
			t.Fatalf("streamLLM: %v", err)
		}
		synctest.Wait()
		if ended, err := stream.Outcome(); ended || err != nil {
			t.Errorf("a stream in flight reported %v, %v", ended, err)
		}
		cancel()
		ended, err := stream.Outcome()
		if !ended || !errors.Is(err, context.Canceled) {
			t.Errorf("a stream whose context ended reported %v, %v", ended, err)
		}
		if stream.Ended() != ended || stream.Err() != err {
			t.Error("Outcome disagrees with Ended and Err")
		}
		close(release)
		synctest.Wait()
		stream.Cancel()
	})
}

// unfinishedStreamCase is one way a stream that sent its request ends, other
// than by handing its reader the provider's answer.
type unfinishedStreamCase struct {
	name string
	// run is the provider's stream.
	run func(ctx context.Context, yield func(llm.StreamChunk, error) bool)
	// timeout, when set, is the deadline of the caller's context.
	timeout time.Duration
	// act is what the test does once the stream is open, with the provider
	// wherever run leaves it. Nil reads the stream to its end.
	act func(stream *TextStream, cancel context.CancelFunc)

	wantKind     types.ErrorKind // KindUnknown for a stream that completed
	wantOutcome  string          // under the "outcome" key of the call record
	wantAnswered bool            // the provider's last chunk arrived
	wantUsage    bool            // and it reported what was used
	wantFinish   string
	wantElapsed  time.Duration // zero when the test does not pin it
}

const (
	// unfinishedModel is the model every stream below asks for. It has a price,
	// so a stream that reports its usage has an exact cost.
	unfinishedModel = "gpt-6-luna"
	// unfinishedServedBy is who the provider names as having answered, which is
	// not the name the provider goes by: a stream that was answered says the first
	// and one that was not says the second.
	unfinishedServedBy = "endstream-served"
	unfinishedAskedOf  = "endstream"
	unfinishedTotal    = 1500
)

// unfinishedAnswer is a provider's last chunk.
func unfinishedAnswer(content, finish string, reportsUsage bool) llm.StreamChunk {
	resp := llm.CompletionResponse{Content: content, Provider: unfinishedServedBy, Model: unfinishedModel, FinishReason: finish}
	if reportsUsage {
		resp.Usage = types.TokenUsage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: unfinishedTotal}
	}
	return llm.StreamChunk{Done: true, Response: resp}
}

// unfinishedBlocksAfter yields the deltas and then waits for its context, as a
// provider that is still generating does.
func unfinishedBlocksAfter(deltas ...string) func(context.Context, func(llm.StreamChunk, error) bool) {
	return func(ctx context.Context, yield func(llm.StreamChunk, error) bool) {
		for _, delta := range deltas {
			if !yield(llm.StreamChunk{Delta: delta}, nil) {
				return
			}
		}
		<-ctx.Done()
		yield(llm.StreamChunk{}, ctx.Err())
	}
}

// unfinishedStreamCases is every way a stream that sent its request can end
// without completing, and the one that completes, whose record is published by
// the path that completes it and not by the end of the stream.
func unfinishedStreamCases() []unfinishedStreamCase {
	fellOver := types.Newf(types.KindProviderUnavailable, "endstream", "the provider fell over")
	flood := make([]string, 4*textStreamBufferSize)
	for i := range flood {
		flood[i] = "x"
	}
	return []unfinishedStreamCase{
		{
			name: "completed",
			run: func(_ context.Context, yield func(llm.StreamChunk, error) bool) {
				yield(llm.StreamChunk{Delta: "a"}, nil)
				yield(unfinishedAnswer("a", "stop", true), nil)
			},
			wantOutcome: streamOutcomeCompleted, wantAnswered: true, wantUsage: true, wantFinish: "stop",
		},
		{
			name: "failed before the first chunk",
			run: func(_ context.Context, yield func(llm.StreamChunk, error) bool) {
				yield(llm.StreamChunk{}, fellOver)
			},
			wantKind: types.KindProviderUnavailable, wantOutcome: streamOutcomeFailed,
		},
		{
			name: "failed midway",
			run: func(_ context.Context, yield func(llm.StreamChunk, error) bool) {
				yield(llm.StreamChunk{Delta: "half "}, nil)
				yield(llm.StreamChunk{}, fellOver)
			},
			wantKind: types.KindProviderUnavailable, wantOutcome: streamOutcomeFailed,
		},
		{
			name: "ended by the provider with no last chunk",
			run: func(_ context.Context, yield func(llm.StreamChunk, error) bool) {
				yield(llm.StreamChunk{Delta: "half "}, nil)
			},
			wantKind: types.KindProviderUnavailable, wantOutcome: streamOutcomeFailed,
		},
		{
			name: "truncated, with its usage reported",
			run: func(_ context.Context, yield func(llm.StreamChunk, error) bool) {
				yield(llm.StreamChunk{Delta: "cut off"}, nil)
				yield(unfinishedAnswer("cut off", "length", true), nil)
			},
			wantKind: types.KindOutputTruncated, wantOutcome: streamOutcomeFailed,
			wantAnswered: true, wantUsage: true, wantFinish: "length",
		},
		{
			name: "truncated, with no usage reported",
			run: func(_ context.Context, yield func(llm.StreamChunk, error) bool) {
				yield(llm.StreamChunk{Delta: "cut off"}, nil)
				yield(unfinishedAnswer("cut off", "length", false), nil)
			},
			wantKind: types.KindOutputTruncated, wantOutcome: streamOutcomeFailed,
			wantAnswered: true, wantFinish: "length",
		},
		{
			name: "refused as empty",
			run: func(_ context.Context, yield func(llm.StreamChunk, error) bool) {
				yield(unfinishedAnswer("", "stop", true), nil)
			},
			wantKind: types.KindMalformedOutput, wantOutcome: streamOutcomeFailed,
			wantAnswered: true, wantUsage: true, wantFinish: "stop",
		},
		{
			name: "left by its reader",
			run:  unfinishedBlocksAfter("first"),
			act: func(stream *TextStream, _ context.CancelFunc) {
				for range stream.All() {
					break
				}
			},
			wantKind: types.KindCanceled, wantOutcome: streamOutcomeStopped,
		},
		{
			name: "cancelled",
			run:  unfinishedBlocksAfter("first"),
			act: func(stream *TextStream, _ context.CancelFunc) {
				synctest.Wait()
				stream.Cancel()
			},
			wantKind: types.KindCanceled, wantOutcome: streamOutcomeStopped,
		},
		{
			name: "abandoned until its context ends",
			run: func(_ context.Context, yield func(llm.StreamChunk, error) bool) {
				for _, delta := range flood {
					if !yield(llm.StreamChunk{Delta: delta}, nil) {
						return
					}
				}
			},
			act: func(_ *TextStream, cancel context.CancelFunc) {
				// The buffer is full and the producer is blocked sending to a
				// reader that never comes.
				synctest.Wait()
				cancel()
			},
			wantKind: types.KindCanceled, wantOutcome: streamOutcomeStopped,
		},
		{
			name:    "its context reaches a deadline",
			run:     unfinishedBlocksAfter(),
			timeout: time.Second,
			act: func(stream *TextStream, _ context.CancelFunc) {
				for range stream.All() {
				}
			},
			wantKind: types.KindTimeout, wantOutcome: streamOutcomeStopped, wantElapsed: time.Second,
		},
	}
}

// openUnfinished opens a stream on a provider that runs c.run, under a context
// that carries what c asks of it, and does what c does to it. It returns once
// every goroutine of the bubble has run as far as it can.
func openUnfinished(t *testing.T, c unfinishedStreamCase, open func(ctx context.Context, provider llm.Provider) (*TextStream, error)) *TextStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	if c.timeout > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), c.timeout)
	}
	t.Cleanup(cancel)
	stream, err := open(ctx, &endStreamProvider{run: c.run})
	if err != nil {
		t.Fatalf("the stream was refused: %v", err)
	}
	if c.act != nil {
		c.act(stream, cancel)
	} else {
		_, _ = readAll(stream)
	}
	synctest.Wait()
	return stream
}

// A stream that sent its request records it however it ends: one request, the
// provider and model that were asked, and what the provider said it used when
// its last chunk carried that, and otherwise usage marked as not reported. It
// was recorded only when the provider's last chunk arrived and was taken, so a
// stream that failed, was cut off or was left had no request in its Meta.
func TestAStreamThatDidNotCompleteRecordsTheRequestItSent(t *testing.T) {
	for _, c := range unfinishedStreamCases() {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				var records *callRecords
				stream := openUnfinished(t, c, func(ctx context.Context, provider llm.Provider) (*TextStream, error) {
					ctx, records = withCallRecording(WithProvider(ctx, provider))
					return streamLLM(ctx, "system", "user", types.OpOptions{Model: unfinishedModel, RequestID: "unfinished-request"})
				})

				if got := types.KindOf(stream.Err()); got != c.wantKind {
					t.Errorf("the stream ended with kind %v, want %v", got, c.wantKind)
				}
				recs := records.collect()
				if len(recs) != 1 {
					t.Fatalf("%d call records for one stream, want 1: %+v", len(recs), recs)
				}
				rec := recs[0]
				wantProvider := unfinishedAskedOf
				if c.wantAnswered {
					wantProvider = unfinishedServedBy
				}
				if rec.UsageOnly || rec.RetryCount != 0 {
					t.Errorf("the record is %+v, want the one request the stream sent", rec)
				}
				if rec.Provider != wantProvider || rec.Model != unfinishedModel || rec.RequestedModel != unfinishedModel {
					t.Errorf("provider %q model %q requested %q, want %q %q %q", rec.Provider, rec.Model, rec.RequestedModel, wantProvider, unfinishedModel, unfinishedModel)
				}
				if rec.RequestID != "unfinished-request" || rec.FinishReason != c.wantFinish {
					t.Errorf("request id %q finish %q, want %q and %q", rec.RequestID, rec.FinishReason, "unfinished-request", c.wantFinish)
				}
				if rec.Custom["outcome"] != c.wantOutcome || rec.Custom["streamed"] != true {
					t.Errorf("the record says %v, want a streamed one that %q", rec.Custom, c.wantOutcome)
				}
				if !rec.StartTime.Equal(start) || rec.EndTime.Before(rec.StartTime) || rec.Duration < 0 {
					t.Errorf("start %v end %v duration %v for a stream begun at %v", rec.StartTime, rec.EndTime, rec.Duration, start)
				}
				if c.wantElapsed > 0 && rec.Duration != c.wantElapsed {
					t.Errorf("Duration = %v, want the %v the stream took", rec.Duration, c.wantElapsed)
				}
				if c.wantUsage {
					if rec.TokenUsage == nil || rec.TokenUsage.TotalTokens != unfinishedTotal || rec.CostInfo == nil ||
						!rec.CostInfo.Priced || rec.CostInfo.Quality != types.PricingExact {
						t.Errorf("usage %+v cost %+v, want the provider's report priced exactly", rec.TokenUsage, rec.CostInfo)
					}
				} else if rec.TokenUsage != nil || rec.CostInfo == nil || rec.CostInfo.Priced || rec.CostInfo.Quality != types.PricingUnknown {
					t.Errorf("usage %+v cost %+v, want no usage and a cost marked unknown, not a measured zero", rec.TokenUsage, rec.CostInfo)
				}

				meta := records.meta("streamsummarize")
				if meta.Attempts != 1 || meta.Provider != wantProvider || meta.Model != unfinishedModel {
					t.Errorf("Meta: %d attempts, provider %q, model %q", meta.Attempts, meta.Provider, meta.Model)
				}
				wantQuality := types.PricingUnknown
				if c.wantUsage {
					wantQuality = types.PricingExact
				}
				if meta.Cost.Quality != wantQuality || meta.Cost.Priced != c.wantUsage {
					t.Errorf("Meta's cost is %q priced %v, want %q priced %v", meta.Cost.Quality, meta.Cost.Priced, wantQuality, c.wantUsage)
				}
			})
		})
	}
}

// A stream that was refused before anything was sent records no request: its
// refusal is the call's, and nothing was spent.
func TestAStreamRefusedBeforeAnythingWasSentRecordsNoRequest(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	cases := []struct {
		name     string
		ctx      context.Context
		provider llm.Provider
		want     types.ErrorKind
	}{
		{"the context is already over", cancelled, &endStreamProvider{run: endCompletes("x")}, types.KindCanceled},
		{"the provider cannot stream", context.Background(), &namedProvider{name: "no-stream"}, types.KindUnsupportedCapability},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, records := withCallRecording(WithProvider(c.ctx, c.provider))
				stream, err := streamLLM(ctx, "system", "user", types.OpOptions{Model: unfinishedModel})
				if stream != nil || types.KindOf(err) != c.want {
					t.Fatalf("streamLLM = %v, %v, want a refusal of kind %v", stream, err, c.want)
				}
				synctest.Wait()
				if recs := records.collect(); len(recs) != 0 {
					t.Errorf("a stream that sent nothing recorded %+v", recs)
				}
				if meta := records.meta("streamsummarize"); meta.Attempts != 0 || meta.Provider != "" {
					t.Errorf("Meta counts %d attempts and provider %q for a stream that sent nothing", meta.Attempts, meta.Provider)
				}
			})
		})
	}

	t.Run("the scheduler is closed", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			scheduler := NewScheduler(SchedulerLimits{MaxConcurrent: 1})
			scheduler.Close(context.Background())
			provider := &endStreamProvider{run: endCompletes("x")}
			ctx, records := withCallRecording(WithProvider(WithSharedExecConfig(context.Background(), &ExecConfig{Scheduler: scheduler}), provider))
			if stream, err := streamLLM(ctx, "system", "user", types.OpOptions{Model: unfinishedModel}); stream != nil || err == nil {
				t.Fatalf("streamLLM = %v, %v, want a refusal", stream, err)
			}
			synctest.Wait()
			if recs := records.collect(); len(recs) != 0 || provider.calls.Load() != 0 {
				t.Errorf("a stream the scheduler refused recorded %+v and called the provider %d times", recs, provider.calls.Load())
			}
		})
	})
}
