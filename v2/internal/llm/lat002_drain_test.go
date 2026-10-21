package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Round 13. A body is read to its end before it is closed, or the connection is
// not kept.

// chunkedServer answers with a body of no stated length, in two parts: the
// whole of the JSON, flushed, and after a pause the end of the response. A
// decoder that stops at the closing brace has not seen the end.
func chunkedServer(t *testing.T, body string, pause time.Duration) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
		w.(http.Flusher).Flush()
		time.Sleep(pause)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestACallLeavesItsConnectionForTheNext(t *testing.T) {
	anthropicBody := `{"id":"msg_1","model":"claude-x","content":[{"type":"text","text":"an answer"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
	chatBody := `{"model":"m","choices":[{"message":{"content":"an answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	previous := drainTimeout
	drainTimeout = 10 * time.Second
	t.Cleanup(func() { drainTimeout = previous })

	cases := []struct {
		name  string
		body  string
		build func(ProviderConfig) (Provider, error)
		model string
	}{
		{"openai", responsesAnswerBody, func(c ProviderConfig) (Provider, error) { return NewOpenAIProvider(c) }, "gpt-6-luna"},
		{"anthropic", anthropicBody, func(c ProviderConfig) (Provider, error) { return NewAnthropicProvider(c) }, "claude-x"},
		{"openrouter", chatBody, func(c ProviderConfig) (Provider, error) { return NewOpenRouterProvider(c) }, "m"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := chunkedServer(t, tc.body, 30*time.Millisecond)
			provider, err := tc.build(ProviderConfig{APIKey: "k", BaseURL: server.URL, Timeout: 20 * time.Second})
			if err != nil {
				t.Fatalf("building the provider: %v", err)
			}
			request := CompletionRequest{Model: tc.model, SystemPrompt: "system", UserPrompt: "user"}
			for i := range 3 {
				resp, err := provider.Complete(context.Background(), request)
				if err != nil {
					t.Fatalf("call %d: %v", i+1, err)
				}
				if i > 0 && !resp.Timing.Reused {
					t.Errorf("call %d opened a new connection: the one before it was closed with its body not read to the end", i+1)
				}
			}
		})
	}
}

// What drains, and how much.
func TestLat13CloseBody(t *testing.T) {
	previous := drainTimeout
	t.Cleanup(func() { drainTimeout = previous })

	t.Run("a body that is read to its end is closed", func(t *testing.T) {
		body := &observedBody{reader: strings.NewReader("tail")}
		drainAndClose(body)
		if closed, read := body.state(); !closed || read != 4 {
			t.Errorf("closed %v after reading %d bytes", closed, read)
		}
	})
	t.Run("an empty body", func(t *testing.T) {
		body := &observedBody{reader: strings.NewReader("")}
		drainAndClose(body)
		if closed, _ := body.state(); !closed {
			t.Error("not closed")
		}
	})
	t.Run("a long body is read only so far", func(t *testing.T) {
		body := &observedBody{reader: strings.NewReader(strings.Repeat("x", 1<<20))}
		drainAndClose(body)
		if closed, read := body.state(); !closed || read > drainLimitBytes {
			t.Errorf("closed %v after reading %d bytes, more than the %d bound", closed, read, drainLimitBytes)
		}
	})
	t.Run("a body that never ends is closed at the time bound", func(t *testing.T) {
		drainTimeout = 50 * time.Millisecond
		release := make(chan struct{})
		body := &observedBody{reader: &blockedBody{release: release}, release: release}
		started := time.Now()
		drainAndClose(body)
		if took := time.Since(started); took > 5*time.Second {
			t.Errorf("took %v for a body that never ends", took)
		}
		if closed, _ := body.state(); !closed {
			t.Error("not closed")
		}
	})
	t.Run("a body that fails to read is closed", func(t *testing.T) {
		body := &observedBody{reader: &failingBody{}}
		drainAndClose(body)
		if closed, _ := body.state(); !closed {
			t.Error("not closed")
		}
	})
	t.Run("a body closed twice is no harm", func(t *testing.T) {
		body := &observedBody{reader: strings.NewReader("x")}
		drainAndClose(body)
		drainAndClose(body)
	})
}

// observedBody is a response body that notes what was read and whether it was
// closed. Closing it lets a read that is blocked on release go.
type observedBody struct {
	reader  io.Reader
	release chan struct{}

	mu     sync.Mutex
	read   int
	closed bool
}

func (b *observedBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	b.mu.Lock()
	b.read += n
	b.mu.Unlock()
	return n, err
}

func (b *observedBody) state() (closed bool, read int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed, b.read
}

func (b *observedBody) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed && b.release != nil {
		close(b.release)
	}
	b.closed = true
	return nil
}

// blockedBody reads nothing until it is released.
type blockedBody struct{ release chan struct{} }

func (b *blockedBody) Read([]byte) (int, error) {
	<-b.release
	return 0, io.ErrClosedPipe
}

type failingBody struct{}

func (*failingBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
