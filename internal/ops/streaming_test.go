package ops

import (
	"context"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// withStreamingProvider installs provider as the default and restores
// whatever was installed before, the same pattern provider_globals_test.go
// uses for the non-streaming hooks.
func withStreamingProvider(t *testing.T, provider llm.Provider) {
	t.Helper()
	originalCaller, originalProvider := currentHooks()
	setLLMCaller(nil)
	SetDefaultProvider(provider)
	t.Cleanup(func() {
		setLLMCaller(originalCaller)
		SetDefaultProvider(originalProvider)
	})
}

// fakeStreamingProvider wraps captureProvider (llm_helper_test.go) with a
// CompleteStream, so tests can drive TextStream without a real socket for
// anything that is not specifically about SSE framing -- that part is
// covered end to end against a real httptest SSE server further down.
type fakeStreamingProvider struct {
	*captureProvider
	seq func(yield func(llm.StreamChunk, error) bool)
}

func (f *fakeStreamingProvider) CompleteStream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamChunk, error] {
	return f.seq
}

// 1. A provider that cannot stream must say so through
// KindUnsupportedCapability, never a silent fall back to a buffered call
// dressed up as a stream.
func TestStreamSummarizeUnsupportedProviderReturnsCapabilityError(t *testing.T) {
	local, err := llm.NewLocalProvider(llm.ProviderConfig{})
	if err != nil {
		t.Fatalf("NewLocalProvider: %v", err)
	}
	withStreamingProvider(t, local)

	stream, err := StreamSummarize("some input", NewSummarizeOptions())
	if err == nil {
		t.Fatal("expected an error from a non-streaming provider")
	}
	if stream != nil {
		t.Fatal("expected a nil stream alongside the error")
	}
	if got := types.KindOf(err); got != types.KindUnsupportedCapability {
		t.Errorf("KindOf(err) = %v, want KindUnsupportedCapability", got)
	}
}

// 2. No provider at all reports ErrNoProvider, same as the buffered path.
func TestStreamSummarizeNoProviderReportsErrNoProvider(t *testing.T) {
	withStreamingProvider(t, nil)

	_, err := StreamSummarize("some input", NewSummarizeOptions())
	if !errors.Is(err, ErrNoProvider) {
		t.Errorf("err = %v, want ErrNoProvider", err)
	}
}

// 3. Invalid options are rejected before any provider is touched -- the
// streaming entry points validate exactly like their buffered siblings.
func TestStreamSummarizeValidatesOptionsBeforeStreaming(t *testing.T) {
	withStreamingProvider(t, &fakeStreamingProvider{captureProvider: &captureProvider{}, seq: func(func(llm.StreamChunk, error) bool) {
		t.Fatal("the provider must not be called when options fail validation")
	}})

	opts := NewSummarizeOptions()
	opts.LengthUnit = "not-a-real-unit"
	if _, err := StreamSummarize("input", opts); err == nil {
		t.Fatal("expected a validation error")
	}
}

// 4. A stream yields the same final text as the buffered call for the same
// underlying response, exercised end to end against a real httptest SSE
// server per ST-001's instruction to prefer real framing over a hand-mocked
// reader.
func TestStreamSummarizeTextMatchesBufferedSummarizeForSameResponse(t *testing.T) {
	const answer = "This is the summarized answer."

	bufferedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"r1","status":"completed","model":"gpt-5.6-luna",
		  "output":[{"type":"message","content":[{"type":"output_text","text":"` + answer + `"}]}],
		  "usage":{"input_tokens":5,"output_tokens":5,"total_tokens":10}}`))
	}))
	t.Cleanup(bufferedServer.Close)
	bufferedProvider, err := llm.NewOpenAIProvider(llm.ProviderConfig{APIKey: "k", BaseURL: bufferedServer.URL, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}
	withStreamingProvider(t, bufferedProvider)
	bufferedResult, err := Summarize("input text", NewSummarizeOptions())
	if err != nil {
		t.Fatalf("Summarize (buffered): %v", err)
	}

	streamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		words := strings.Fields(answer)
		for i, word := range words {
			delta := word
			if i < len(words)-1 {
				delta += " "
			}
			_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"` + delta + `"}` + "\n\n"))
			flusher.Flush()
		}
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"r2","status":"completed","model":"gpt-5.6-luna",` +
			`"output":[{"type":"message","content":[{"type":"output_text","text":"` + answer + `"}]}],` +
			`"usage":{"input_tokens":5,"output_tokens":5,"total_tokens":10}}}` + "\n\n"))
		flusher.Flush()
	}))
	t.Cleanup(streamServer.Close)
	streamProvider, err := llm.NewOpenAIProvider(llm.ProviderConfig{APIKey: "k", BaseURL: streamServer.URL, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}
	withStreamingProvider(t, streamProvider)
	stream, err := StreamSummarize("input text", NewSummarizeOptions())
	if err != nil {
		t.Fatalf("StreamSummarize: %v", err)
	}
	streamedText, err := stream.Text()
	if err != nil {
		t.Fatalf("stream.Text(): %v", err)
	}

	if streamedText != bufferedResult {
		t.Errorf("streamed text = %q, buffered = %q", streamedText, bufferedResult)
	}
}

// 5. Deltas arrive, in order, before the stream reports its outcome.
func TestTextStreamDeltasArriveInOrderBeforeResult(t *testing.T) {
	provider := &fakeStreamingProvider{
		captureProvider: &captureProvider{},
		seq: func(yield func(llm.StreamChunk, error) bool) {
			for _, d := range []string{"one ", "two ", "three"} {
				if !yield(llm.StreamChunk{Delta: d}, nil) {
					return
				}
			}
			yield(llm.StreamChunk{Done: true, Response: llm.CompletionResponse{
				Content: "one two three", FinishReason: "stop",
			}}, nil)
		},
	}
	withStreamingProvider(t, provider)

	stream, err := StreamSummarize("input", NewSummarizeOptions())
	if err != nil {
		t.Fatalf("StreamSummarize: %v", err)
	}

	var got []string
	for delta, err := range stream.All() {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got = append(got, delta)
	}
	want := []string{"one ", "two ", "three"}
	if len(got) != len(want) {
		t.Fatalf("got %v deltas, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("delta[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if stream.Err() != nil {
		t.Errorf("Err() = %v, want nil", stream.Err())
	}
}

// 6. A mid-stream failure is an error, never a truncated success: no Done
// item is ever produced, and the deltas seen before the failure are real
// but the stream's outcome is unambiguously an error.
func TestTextStreamMidStreamFailureIsNeverPresentedAsSuccess(t *testing.T) {
	wantErr := errors.New("the connection dropped")
	provider := &fakeStreamingProvider{
		captureProvider: &captureProvider{},
		seq: func(yield func(llm.StreamChunk, error) bool) {
			if !yield(llm.StreamChunk{Delta: "partial "}, nil) {
				return
			}
			yield(llm.StreamChunk{}, wantErr)
		},
	}
	withStreamingProvider(t, provider)

	stream, err := StreamSummarize("input", NewSummarizeOptions())
	if err != nil {
		t.Fatalf("StreamSummarize: %v", err)
	}

	var deltas []string
	var rangeErr error
	for delta, err := range stream.All() {
		if err != nil {
			rangeErr = err
			continue
		}
		deltas = append(deltas, delta)
	}
	if rangeErr == nil {
		t.Fatal("expected the range loop to observe an error")
	}
	if !errors.Is(rangeErr, wantErr) {
		t.Errorf("range error = %v, want it to wrap %v", rangeErr, wantErr)
	}
	if got := strings.Join(deltas, ""); got != "partial " {
		t.Errorf("deltas before the failure = %q, want %q", got, "partial ")
	}
	if _, err := stream.Text(); err == nil {
		t.Fatal("Text() must report the failure, not the partial deltas as success")
	}
}

// 7. A truncated terminal response is classified exactly like the buffered
// path classifies it -- llm.ClassifyCompletion is the one function that
// decides, called from both.
func TestTextStreamTruncatedResponseClassifiesAsOutputTruncated(t *testing.T) {
	provider := &fakeStreamingProvider{
		captureProvider: &captureProvider{},
		seq: func(yield func(llm.StreamChunk, error) bool) {
			yield(llm.StreamChunk{Delta: "cut off"}, nil)
			yield(llm.StreamChunk{Done: true, Response: llm.CompletionResponse{
				Content: "cut off", FinishReason: "length", Provider: "fake", Model: "m",
			}}, nil)
		},
	}
	withStreamingProvider(t, provider)

	stream, err := StreamSummarize("input", NewSummarizeOptions())
	if err != nil {
		t.Fatalf("StreamSummarize: %v", err)
	}
	_, err = stream.Text()
	if err == nil {
		t.Fatal("a truncated stream must report an error, not the cut-off text as success")
	}
	if got := types.KindOf(err); got != types.KindOutputTruncated {
		t.Errorf("KindOf(err) = %v, want KindOutputTruncated", got)
	}

	// Same cause, same classification, via the buffered path's own
	// classifier -- there is exactly one function deciding this, not two
	// independent opinions that happen to agree today.
	bufferedKind := llm.ClassifyCompletion(llm.CompletionResponse{Content: "cut off", FinishReason: "length"})
	if bufferedKind != types.KindOutputTruncated {
		t.Fatalf("test setup: buffered classifier did not agree, got %v", bufferedKind)
	}
}

// 8. Stopping iteration (a break, no Detach) cancels the remaining work: the
// stream's context is canceled, which is what the producer goroutine
// observes to stop pulling more chunks.
func TestTextStreamBreakCancelsWork(t *testing.T) {
	block := make(chan struct{})
	// Released at test end so the fake's goroutine does not leak past this
	// test; the test itself never waits on it closing.
	t.Cleanup(func() { close(block) })

	provider := &fakeStreamingProvider{
		captureProvider: &captureProvider{},
		seq: func(yield func(llm.StreamChunk, error) bool) {
			if !yield(llm.StreamChunk{Delta: "first"}, nil) {
				return
			}
			// Simulates a provider still blocked reading the rest of the
			// answer, the way a real socket read would be. What actually
			// stops this goroutine is cancellation reaching it, which for a
			// real network read happens through the context; this fake
			// cannot observe ctx (it ignores it, matching the minimal
			// StreamingProvider contract), so the assertion below checks
			// stream.ctx directly rather than this goroutine unblocking.
			<-block
		},
	}
	withStreamingProvider(t, provider)

	stream, err := StreamSummarize("input", NewSummarizeOptions())
	if err != nil {
		t.Fatalf("StreamSummarize: %v", err)
	}

	for delta, err := range stream.All() {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if delta == "first" {
			break
		}
	}

	select {
	case <-stream.ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("breaking out of All without Detach must cancel the stream's context")
	}
}

// 9. Detach lets the work continue after the caller stops reading, and the
// producer drains without blocking (proving the drain goroutine All starts
// on Detach actually runs).
func TestTextStreamDetachLetsWorkFinish(t *testing.T) {
	finished := make(chan struct{})
	provider := &fakeStreamingProvider{
		captureProvider: &captureProvider{},
		seq: func(yield func(llm.StreamChunk, error) bool) {
			defer close(finished)
			for _, d := range []string{"a", "b", "c", "d", "e"} {
				if !yield(llm.StreamChunk{Delta: d}, nil) {
					return
				}
			}
			yield(llm.StreamChunk{Done: true, Response: llm.CompletionResponse{Content: "abcde", FinishReason: "stop"}}, nil)
		},
	}
	withStreamingProvider(t, provider)

	stream, err := StreamSummarize("input", NewSummarizeOptions())
	if err != nil {
		t.Fatalf("StreamSummarize: %v", err)
	}

	stream.Detach()
	for delta, err := range stream.All() {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if delta == "a" {
			break
		}
	}

	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("a detached stream must finish producing even after the caller stopped reading")
	}
}

// 10. The caller-facing iterator owns a BOUNDED buffer: a fast producer
// racing ahead of a slow consumer does not pull unboundedly ahead into
// memory. The producer's pull count is observed directly rather than
// inferred from timing.
func TestTextStreamBufferIsBounded(t *testing.T) {
	const totalChunks = textStreamBufferSize*3 + 7
	var produced atomic.Int64

	provider := &fakeStreamingProvider{
		captureProvider: &captureProvider{},
		seq: func(yield func(llm.StreamChunk, error) bool) {
			for i := 0; i < totalChunks; i++ {
				produced.Add(1)
				if !yield(llm.StreamChunk{Delta: "x"}, nil) {
					return
				}
			}
			yield(llm.StreamChunk{Done: true, Response: llm.CompletionResponse{
				Content: strings.Repeat("x", totalChunks), FinishReason: "stop",
			}}, nil)
		},
	}
	withStreamingProvider(t, provider)

	stream, err := StreamSummarize("input", NewSummarizeOptions())
	if err != nil {
		t.Fatalf("StreamSummarize: %v", err)
	}

	// Nobody reads stream.items yet. Give the producer goroutine time to run
	// ahead until it blocks on a full channel, then poll until the count
	// stabilizes -- it must stabilize well short of totalChunks.
	var last, stable int64
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		current := produced.Load()
		if current == last {
			stable++
			if stable >= 3 {
				break
			}
		} else {
			stable = 0
		}
		last = current
	}

	if last >= totalChunks {
		t.Fatalf("producer ran to completion (%d/%d chunks) with nobody consuming -- the buffer is not bounded",
			last, totalChunks)
	}
	// The bound is the channel capacity plus the one item the producer may
	// be holding in-flight while blocked on the next send, plus a little
	// slack for scheduling.
	if max := int64(textStreamBufferSize) + 4; last > max {
		t.Fatalf("producer ran %d chunks ahead of the (unread) buffer, want at most %d", last, max)
	}

	// Drain fully so the goroutine this test started does not leak past it.
	for range stream.All() {
	}
	if got := produced.Load(); got != totalChunks {
		t.Errorf("after draining, produced = %d, want %d", got, totalChunks)
	}
}

// 11. The classification of a streamed failure matches the buffered one for
// the same cause, exercised through the full ops entry points (streamLLM
// and CallLLM) rather than only at the llm package's classifier.
func TestStreamLLMFailureClassifiesLikeCallLLM(t *testing.T) {
	body := `{"error":{"message":"upstream exploded","type":"server_error"}}`

	bufferedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(bufferedServer.Close)
	bufferedProvider, err := llm.NewOpenAIProvider(llm.ProviderConfig{APIKey: "k", BaseURL: bufferedServer.URL, Timeout: 5 * time.Second, MaxRetries: 0})
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}
	withStreamingProvider(t, bufferedProvider)
	_, bufferedErr := Summarize("input", NewSummarizeOptions())
	if bufferedErr == nil {
		t.Fatal("buffered Summarize must fail on a 500")
	}

	streamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(streamServer.Close)
	streamProvider, err := llm.NewOpenAIProvider(llm.ProviderConfig{APIKey: "k", BaseURL: streamServer.URL, Timeout: 5 * time.Second, MaxRetries: 0})
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}
	withStreamingProvider(t, streamProvider)
	stream, err := StreamSummarize("input", NewSummarizeOptions())
	if err != nil {
		t.Fatalf("StreamSummarize: %v", err)
	}
	_, streamErr := stream.Text()
	if streamErr == nil {
		t.Fatal("streamed Summarize must fail on a 500")
	}

	if got, want := llm.Classify(streamErr), llm.Classify(bufferedErr); got != want {
		t.Errorf("Classify(streamed) = %v, Classify(buffered) = %v, want equal", got, want)
	}
	if llm.Classify(streamErr) != types.KindProviderUnavailable {
		t.Errorf("Classify(streamed) = %v, want KindProviderUnavailable", llm.Classify(streamErr))
	}
}

// 12. Every one of the four text operations exposes a streaming entry point,
// each reachable, validating, and producing a Done outcome.
func TestAllFourTextOperationsStream(t *testing.T) {
	newProvider := func(content string) *fakeStreamingProvider {
		return &fakeStreamingProvider{
			captureProvider: &captureProvider{},
			seq: func(yield func(llm.StreamChunk, error) bool) {
				if !yield(llm.StreamChunk{Delta: content}, nil) {
					return
				}
				yield(llm.StreamChunk{Done: true, Response: llm.CompletionResponse{Content: content, FinishReason: "stop"}}, nil)
			},
		}
	}

	t.Run("summarize", func(t *testing.T) {
		withStreamingProvider(t, newProvider("summary"))
		stream, err := StreamSummarize("input", NewSummarizeOptions())
		if err != nil {
			t.Fatalf("StreamSummarize: %v", err)
		}
		if text, err := stream.Text(); err != nil || text != "summary" {
			t.Errorf("Text() = %q, %v, want %q, nil", text, err, "summary")
		}
	})

	t.Run("rewrite", func(t *testing.T) {
		withStreamingProvider(t, newProvider("rewritten"))
		stream, err := StreamRewrite("input", NewRewriteOptions())
		if err != nil {
			t.Fatalf("StreamRewrite: %v", err)
		}
		if text, err := stream.Text(); err != nil || text != "rewritten" {
			t.Errorf("Text() = %q, %v, want %q, nil", text, err, "rewritten")
		}
	})

	t.Run("translate", func(t *testing.T) {
		withStreamingProvider(t, newProvider("traduit"))
		opts := NewTranslateOptions()
		opts.TargetLanguage = "French"
		stream, err := StreamTranslate("input", opts)
		if err != nil {
			t.Fatalf("StreamTranslate: %v", err)
		}
		if text, err := stream.Text(); err != nil || text != "traduit" {
			t.Errorf("Text() = %q, %v, want %q, nil", text, err, "traduit")
		}
	})

	t.Run("expand", func(t *testing.T) {
		withStreamingProvider(t, newProvider("expanded content"))
		stream, err := StreamExpand("input", NewExpandOptions())
		if err != nil {
			t.Fatalf("StreamExpand: %v", err)
		}
		if text, err := stream.Text(); err != nil || text != "expanded content" {
			t.Errorf("Text() = %q, %v, want %q, nil", text, err, "expanded content")
		}
	})
}
