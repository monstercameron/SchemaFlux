package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// sseFrame renders one SSE "data:" frame from a JSON-able value. Real SSE
// framing -- a "data:" line per event, a blank line to terminate it -- is
// what these tests exercise, per TODOS.md ST-001's instruction to prefer an
// httptest.Server emitting real frames over a hand-mocked reader: the
// framing itself is where a streaming implementation usually goes wrong.
func sseFrame(t *testing.T, v any) string {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("sseFrame: marshal: %v", err)
	}
	return "data: " + string(body) + "\n\n"
}

// sseServer stands up a fake Responses API endpoint. write is called once
// per request with a flusher, so a test can interleave writes with pauses,
// or observe cancellation via r.Context().Done().
func sseServer(t *testing.T, write func(w http.ResponseWriter, r *http.Request, flusher http.Flusher)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			t.Errorf("expected the Responses API path, got %s", r.URL.Path)
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseRecorder must support flushing for a real SSE test")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		write(w, r, flusher)
	}))
	t.Cleanup(server.Close)
	return server
}

func completedFrame(text, status, incompleteReason string, inputTokens, outputTokens int) map[string]any {
	resp := map[string]any{
		"id":     "resp_stream",
		"status": status,
		"model":  "gpt-5.6-luna",
		"output": []map[string]any{
			{"type": "message", "content": []map[string]any{{"type": "output_text", "text": text}}},
		},
		"usage": map[string]any{
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
			"total_tokens":  inputTokens + outputTokens,
		},
	}
	if status == "incomplete" {
		resp["incomplete_details"] = map[string]any{"reason": incompleteReason}
	}
	eventType := "response.completed"
	if status == "incomplete" {
		eventType = "response.incomplete"
	}
	return map[string]any{"type": eventType, "response": resp}
}

func deltaFrame(delta string) map[string]any {
	return map[string]any{"type": "response.output_text.delta", "delta": delta}
}

// collectStream drains a stream into its deltas and final outcome.
func collectStream(seq func(func(StreamChunk, error) bool)) (deltas []string, done StreamChunk, err error) {
	for chunk, chunkErr := range seq {
		if chunkErr != nil {
			err = chunkErr
			return
		}
		if chunk.Done {
			done = chunk
			return
		}
		deltas = append(deltas, chunk.Delta)
	}
	return
}

// 1. Deltas arrive, then a Done chunk carrying the accumulated content --
// the ordinary happy path a caller builds a UI against.
func TestOpenAIStreamYieldsDeltasThenDone(t *testing.T) {
	server := sseServer(t, func(w http.ResponseWriter, r *http.Request, f http.Flusher) {
		for _, frame := range []map[string]any{
			deltaFrame("Hel"), deltaFrame("lo, "), deltaFrame("world"),
			completedFrame("Hello, world", "completed", "", 10, 3),
		} {
			_, _ = w.Write([]byte(sseFrame(t, frame)))
			f.Flush()
		}
	})
	provider := newTestOpenAIProvider(t, server.URL)

	deltas, done, err := collectStream(provider.CompleteStream(context.Background(), CompletionRequest{Model: "gpt-5.6-luna"}))
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if got := strings.Join(deltas, ""); got != "Hello, world" {
		t.Fatalf("deltas joined = %q, want %q", got, "Hello, world")
	}
	if !done.Done {
		t.Fatal("expected a terminal Done chunk")
	}
	if done.Response.Content != "Hello, world" {
		t.Errorf("Done.Response.Content = %q, want %q", done.Response.Content, "Hello, world")
	}
	if done.Response.FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want stop", done.Response.FinishReason)
	}
	if done.Response.Usage.PromptTokens != 10 || done.Response.Usage.CompletionTokens != 3 {
		t.Errorf("usage not carried through: %+v", done.Response.Usage)
	}
}

// 2. A stream yields the same final text as the buffered call for the same
// underlying response -- the core claim ST-001 makes about streaming.
func TestOpenAIStreamMatchesBufferedContentForSameResponse(t *testing.T) {
	const text = `{"name":"Ada","age":36}`

	bufferedBody := `{
	  "id": "resp_x", "status": "completed", "model": "gpt-5.6-luna",
	  "output": [{"type": "message", "content": [{"type": "output_text", "text": ` + jsonString(t, text) + `}]}],
	  "usage": {"input_tokens": 20, "output_tokens": 8, "total_tokens": 28}
	}`
	bufferedProvider := newTestOpenAIProvider(t, responsesServer(t, bufferedBody).URL)
	bufferedResp, err := bufferedProvider.Complete(context.Background(), CompletionRequest{Model: "gpt-5.6-luna"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	streamServer := sseServer(t, func(w http.ResponseWriter, r *http.Request, f http.Flusher) {
		for _, frame := range []map[string]any{
			deltaFrame(text),
			completedFrame(text, "completed", "", 20, 8),
		} {
			_, _ = w.Write([]byte(sseFrame(t, frame)))
			f.Flush()
		}
	})
	streamProvider := newTestOpenAIProvider(t, streamServer.URL)
	_, done, err := collectStream(streamProvider.CompleteStream(context.Background(), CompletionRequest{Model: "gpt-5.6-luna"}))
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}

	if done.Response.Content != bufferedResp.Content {
		t.Errorf("streamed content = %q, buffered = %q", done.Response.Content, bufferedResp.Content)
	}
	if done.Response.FinishReason != bufferedResp.FinishReason {
		t.Errorf("streamed finish reason = %q, buffered = %q", done.Response.FinishReason, bufferedResp.FinishReason)
	}
	if done.Response.Usage != bufferedResp.Usage {
		t.Errorf("streamed usage = %+v, buffered = %+v", done.Response.Usage, bufferedResp.Usage)
	}
}

func jsonString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("jsonString: %v", err)
	}
	return string(b)
}

// 3. A mid-stream failure is an error, never a truncated success -- the
// partial deltas already yielded are real, but there is no Done chunk.
func TestOpenAIStreamMidStreamErrorEventIsAnError(t *testing.T) {
	server := sseServer(t, func(w http.ResponseWriter, r *http.Request, f http.Flusher) {
		_, _ = w.Write([]byte(sseFrame(t, deltaFrame("partial answer"))))
		f.Flush()
		_, _ = w.Write([]byte(sseFrame(t, map[string]any{
			"type":  "response.failed",
			"error": map[string]any{"message": "the model provider had an internal error", "type": "server_error"},
		})))
		f.Flush()
	})
	provider := newTestOpenAIProvider(t, server.URL)

	deltas, done, err := collectStream(provider.CompleteStream(context.Background(), CompletionRequest{Model: "gpt-5.6-luna"}))
	if err == nil {
		t.Fatal("a mid-stream failure must surface as an error")
	}
	if done.Done {
		t.Fatal("a mid-stream failure must never yield a Done chunk")
	}
	if got := strings.Join(deltas, ""); got != "partial answer" {
		t.Errorf("deltas observed before the failure = %q, want %q", got, "partial answer")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
}

// 4. A connection that closes with no terminal event at all -- neither a
// completed response nor a declared error -- must not be read as success.
func TestOpenAIStreamSilentDisconnectIsAnError(t *testing.T) {
	server := sseServer(t, func(w http.ResponseWriter, r *http.Request, f http.Flusher) {
		_, _ = w.Write([]byte(sseFrame(t, deltaFrame("here is some"))))
		f.Flush()
		// The handler returns without ever sending response.completed,
		// response.incomplete, or an error event -- the connection just
		// stops, like a proxy timeout or a killed worker.
	})
	provider := newTestOpenAIProvider(t, server.URL)

	deltas, done, err := collectStream(provider.CompleteStream(context.Background(), CompletionRequest{Model: "gpt-5.6-luna"}))
	if err == nil {
		t.Fatal("a stream with no terminal event must be an error")
	}
	if done.Done {
		t.Fatal("must never synthesize a Done chunk from a silently dropped connection")
	}
	if !errors.Is(err, ErrStreamIncomplete) {
		t.Errorf("err = %v, want it to wrap ErrStreamIncomplete", err)
	}
	if got := strings.Join(deltas, ""); got != "here is some" {
		t.Errorf("deltas observed before the disconnect = %q", got)
	}
}

// 5. The classification of a streamed failure matches the buffered one for
// the same cause -- both paths hit the same status-code branch before any
// SSE framing is even parsed.
func TestClassifyStreamedFailureMatchesBuffered(t *testing.T) {
	body := `{"error":{"message":"rate limited","type":"rate_limit_error"}}`

	bufferedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(bufferedServer.Close)
	bufferedProvider := newTestOpenAIProvider(t, bufferedServer.URL)
	_, bufferedErr := bufferedProvider.Complete(context.Background(), CompletionRequest{Model: "gpt-5.6-luna"})
	if bufferedErr == nil {
		t.Fatal("buffered call must fail on 429")
	}

	streamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(streamServer.Close)
	streamProvider := newTestOpenAIProvider(t, streamServer.URL)
	_, _, streamErr := collectStream(streamProvider.CompleteStream(context.Background(), CompletionRequest{Model: "gpt-5.6-luna"}))
	if streamErr == nil {
		t.Fatal("streamed call must fail on 429")
	}

	if Classify(bufferedErr) != Classify(streamErr) {
		t.Errorf("Classify(buffered) = %v, Classify(streamed) = %v, want equal", Classify(bufferedErr), Classify(streamErr))
	}
	if Classify(streamErr) != types.KindRateLimited {
		t.Errorf("Classify(streamed) = %v, want KindRateLimited", Classify(streamErr))
	}
}

// 6. A truncated streamed response classifies exactly like a truncated
// buffered one -- ClassifyCompletion is the one place that decides, and it
// is fed the same shape by both paths.
func TestOpenAIStreamTruncationClassifiesLikeBuffered(t *testing.T) {
	server := sseServer(t, func(w http.ResponseWriter, r *http.Request, f http.Flusher) {
		_, _ = w.Write([]byte(sseFrame(t, deltaFrame(`{"name":"Jo`))))
		f.Flush()
		// The incomplete reason is left blank so finishReason() falls back
		// to "incomplete" -- a value classify.go's ClassifyCompletion
		// recognizes. The Responses API's real reason text
		// ("max_output_tokens") is not one of the phrases that switch
		// matches; that gap belongs to classify.go, which is out of this
		// change's reach, and is reported as a limitation rather than
		// worked around here.
		_, _ = w.Write([]byte(sseFrame(t, completedFrame(`{"name":"Jo`, "incomplete", "", 10, 4))))
		f.Flush()
	})
	provider := newTestOpenAIProvider(t, server.URL)

	_, done, err := collectStream(provider.CompleteStream(context.Background(), CompletionRequest{Model: "gpt-5.6-luna"}))
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if done.Response.FinishReason != "incomplete" {
		t.Fatalf("FinishReason = %q, want incomplete", done.Response.FinishReason)
	}
	if got := ClassifyCompletion(done.Response); got != types.KindOutputTruncated {
		t.Errorf("ClassifyCompletion(streamed truncation) = %v, want KindOutputTruncated", got)
	}
}

// 7. Stopping iteration (a `break`) stops the request: the server observes
// the client disconnect instead of being read to completion.
func TestOpenAIStreamBreakCancelsTheRequest(t *testing.T) {
	serverSawCancel := make(chan struct{})
	unblockServer := make(chan struct{})

	server := sseServer(t, func(w http.ResponseWriter, r *http.Request, f http.Flusher) {
		_, _ = w.Write([]byte(sseFrame(t, deltaFrame("first"))))
		f.Flush()
		select {
		case <-r.Context().Done():
			close(serverSawCancel)
		case <-unblockServer:
			// Test failed to observe cancellation in time; unblock so the
			// handler does not hang the test process.
		}
	})
	t.Cleanup(func() { close(unblockServer) })
	provider := newTestOpenAIProvider(t, server.URL)

	seen := 0
	for chunk, err := range provider.CompleteStream(context.Background(), CompletionRequest{Model: "gpt-5.6-luna"}) {
		if err != nil {
			t.Fatalf("unexpected error before break: %v", err)
		}
		seen++
		if chunk.Delta == "first" {
			break
		}
	}
	if seen != 1 {
		t.Fatalf("expected exactly one chunk observed before break, got %d", seen)
	}

	select {
	case <-serverSawCancel:
	case <-time.After(5 * time.Second):
		t.Fatal("server never observed the client disconnect after break")
	}
}

// 8. A non-streaming provider is a Provider that does not implement
// StreamingProvider, and the call site (internal/ops, tested separately)
// must detect that with a type assertion rather than guessing.
func TestNonStreamingProviderIsNotAStreamingProvider(t *testing.T) {
	local, err := NewLocalProvider(ProviderConfig{})
	if err != nil {
		t.Fatalf("NewLocalProvider: %v", err)
	}
	if _, ok := any(local).(StreamingProvider); ok {
		t.Fatal("LocalProvider must not satisfy StreamingProvider -- it has no CompleteStream")
	}

	anthropic, err := NewAnthropicProvider(ProviderConfig{APIKey: "k"})
	if err != nil {
		t.Fatalf("NewAnthropicProvider: %v", err)
	}
	if _, ok := any(anthropic).(StreamingProvider); ok {
		t.Fatal("AnthropicProvider must not satisfy StreamingProvider -- ST-001 only implements the Responses API")
	}

	openaiProvider, err := NewOpenAIProvider(ProviderConfig{APIKey: "k"})
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}
	if _, ok := any(openaiProvider).(StreamingProvider); !ok {
		t.Fatal("OpenAIProvider must satisfy StreamingProvider")
	}
}

// 9. The request sent on the wire for a streamed call is the buffered
// request plus "stream": true -- nothing else about the request changes
// depending on whether the caller asked to stream it.
func TestOpenAIStreamRequestMatchesBufferedRequest(t *testing.T) {
	var streamedBody, bufferedBody map[string]any
	var mu sync.Mutex

	capture := func(dst *map[string]any) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			body := map[string]any{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			*dst = body
			mu.Unlock()
			if body["stream"] == true {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(sseFrame(t, completedFrame("ok", "completed", "", 1, 1))))
				return
			}
			_, _ = w.Write([]byte(`{"id":"r","status":"completed","model":"m","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
		}
	}

	bufferedServer := httptest.NewServer(capture(&bufferedBody))
	t.Cleanup(bufferedServer.Close)
	streamServer := httptest.NewServer(capture(&streamedBody))
	t.Cleanup(streamServer.Close)

	req := CompletionRequest{
		Model:          "gpt-5.6-luna",
		SystemPrompt:   "sys",
		UserPrompt:     "user",
		MaxTokens:      500,
		PromptCacheKey: "cache-key-1",
	}

	bufferedProvider := newTestOpenAIProvider(t, bufferedServer.URL)
	if _, err := bufferedProvider.Complete(context.Background(), req); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	streamProvider := newTestOpenAIProvider(t, streamServer.URL)
	for range streamProvider.CompleteStream(context.Background(), req) {
	}

	mu.Lock()
	defer mu.Unlock()
	if streamedBody["stream"] != true {
		t.Error(`streamed request must set "stream": true`)
	}
	delete(streamedBody, "stream")
	if bufferedBody["stream"] != nil {
		t.Error(`buffered request must not set "stream" at all`)
	}
	delete(bufferedBody, "stream")

	streamedJSON, _ := json.Marshal(streamedBody)
	bufferedJSON, _ := json.Marshal(bufferedBody)
	if !bytes.Equal(streamedJSON, bufferedJSON) {
		t.Errorf("request bodies differ beyond \"stream\":\nstreamed: %s\nbuffered: %s", streamedJSON, bufferedJSON)
	}
}

// 10. A malformed SSE payload is reported, not silently skipped -- the
// framing itself is what streaming implementations most often get wrong.
func TestOpenAIStreamMalformedEventIsAnError(t *testing.T) {
	server := sseServer(t, func(w http.ResponseWriter, r *http.Request, f http.Flusher) {
		_, _ = w.Write([]byte("data: {not valid json\n\n"))
		f.Flush()
	})
	provider := newTestOpenAIProvider(t, server.URL)

	_, _, err := collectStream(provider.CompleteStream(context.Background(), CompletionRequest{Model: "gpt-5.6-luna"}))
	if err == nil {
		t.Fatal("a malformed SSE event must be reported as an error")
	}
}

// 11. Canceling the context before the request completes surfaces a
// canceled/timeout classification, exactly like the buffered path.
func TestOpenAIStreamContextCancellationClassifiesAsCanceled(t *testing.T) {
	release := make(chan struct{})
	server := sseServer(t, func(w http.ResponseWriter, r *http.Request, f http.Flusher) {
		_, _ = w.Write([]byte(sseFrame(t, deltaFrame("partial"))))
		f.Flush()
		<-release
	})
	t.Cleanup(func() { close(release) })
	provider := newTestOpenAIProvider(t, server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var gotErr error
	var chunkCount int32
	for chunk, err := range provider.CompleteStream(ctx, CompletionRequest{Model: "gpt-5.6-luna"}) {
		if err != nil {
			gotErr = err
			break
		}
		atomic.AddInt32(&chunkCount, 1)
		if chunk.Delta == "partial" {
			cancel()
		}
	}
	if gotErr == nil {
		t.Fatal("expected an error after the context was canceled mid-stream")
	}
	if got := Classify(gotErr); got != types.KindCanceled && got != types.KindTimeout {
		t.Errorf("Classify(canceled stream error) = %v, want KindCanceled or KindTimeout", got)
	}
}
