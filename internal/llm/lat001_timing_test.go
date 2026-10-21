package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// LAT-001. A call says where its time went.

const responsesAnswerBody = `{"id":"resp_1","status": "completed", "output":[{"content":[{"type":"output_text","text":"an answer"}]}],"model":"gpt-6-luna","usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30}}`

// slowServer answers after thinking for a known time, and then takes a known
// time to send the body.
func slowServer(t *testing.T, think, send time.Duration, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(think)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		half := len(body) / 2
		_, _ = w.Write([]byte(body[:half]))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(send)
		_, _ = w.Write([]byte(body[half:]))
	}))
	t.Cleanup(server.Close)
	return server
}

// Against a server whose thinking time and sending time are known, the wait
// for the first byte is the thinking time and the read is the sending time;
// the first call pays for a connection and a handshake and the second does
// not; and the phases never add up to more than the whole.
func TestACallSaysWhereItsTimeWent(t *testing.T) {
	const think, send = 120 * time.Millisecond, 80 * time.Millisecond
	server := slowServer(t, think, send, responsesAnswerBody)
	provider, err := NewOpenAIProvider(ProviderConfig{APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}
	request := CompletionRequest{Model: "gpt-6-luna", SystemPrompt: "system", UserPrompt: "user"}

	first, err := provider.Complete(context.Background(), request)
	if err != nil {
		t.Fatalf("the first call: %v", err)
	}
	second, err := provider.Complete(context.Background(), request)
	if err != nil {
		t.Fatalf("the second call: %v", err)
	}

	for name, timing := range map[string]types.Timing{"first": first.Timing, "second": second.Timing} {
		if !timing.Measured() {
			t.Fatalf("the %s call was not timed: %+v", name, timing)
		}
		// The server's delays are floors: the clock cannot say the wait was
		// shorter than the server slept. The margins above them are wide,
		// since a loaded machine is slow and that is not what is being tested.
		if timing.Wait < think-10*time.Millisecond || timing.Wait > think+2*time.Second {
			t.Errorf("the %s call waited %v for a server that thought for %v", name, timing.Wait, think)
		}
		if timing.Read < send-10*time.Millisecond || timing.Read > send+2*time.Second {
			t.Errorf("the %s call read for %v from a server that took %v to send", name, timing.Read, send)
		}
		phases := timing.Lookup + timing.Connect + timing.Handshake + timing.Send + timing.Wait + timing.Read
		if phases > timing.Network {
			t.Errorf("the %s call's phases add up to %v, more than the whole call's %v", name, phases, timing.Network)
		}
		if timing.Network < think+send-20*time.Millisecond {
			t.Errorf("the %s call took %v in all, less than the server's %v", name, timing.Network, think+send)
		}
		if timing.Queued != 0 {
			t.Errorf("the %s call reports %v queued, and a provider has no queue", name, timing.Queued)
		}
	}

	if first.Timing.Reused {
		t.Error("the first call says it reused a connection")
	}
	// The handshake is what shows a connection was set up. The connect itself
	// is to the loopback address, and on a clock as coarse as Windows' it can
	// begin and end in one tick and measure as nothing, which failed this
	// test about one run in twenty.
	if first.Timing.Handshake <= 0 || first.Timing.Connect < 0 {
		t.Errorf("the first call paid nothing for its connection: connect %v, handshake %v", first.Timing.Connect, first.Timing.Handshake)
	}
	if first.Timing.Setup() != first.Timing.Lookup+first.Timing.Connect+first.Timing.Handshake {
		t.Error("Setup is not the lookup, the connect and the handshake")
	}
	if !second.Timing.Reused {
		t.Error("the second call did not reuse the first one's connection")
	}
	if second.Timing.Setup() != 0 {
		t.Errorf("the second call paid %v for a connection it reused", second.Timing.Setup())
	}
}

// The chat-completions providers and Anthropic time their calls too.
func TestEveryBufferedProviderTimesItsCalls(t *testing.T) {
	const think = 60 * time.Millisecond
	chatBody := `{"model":"m","choices":[{"message":{"content":"an answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	anthropicBody := `{"id":"msg_1","model":"claude-x","content":[{"type":"text","text":"an answer"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`

	cases := []struct {
		name  string
		body  string
		build func(config ProviderConfig) (Provider, error)
		model string
	}{
		{"openai", responsesAnswerBody, func(c ProviderConfig) (Provider, error) { return NewOpenAIProvider(c) }, "gpt-6-luna"},
		{"anthropic", anthropicBody, func(c ProviderConfig) (Provider, error) { return NewAnthropicProvider(c) }, "claude-x"},
		{"openrouter", chatBody, func(c ProviderConfig) (Provider, error) { return NewOpenRouterProvider(c) }, "m"},
		{"cerebras", chatBody, func(c ProviderConfig) (Provider, error) { return NewCerebrasProvider(c) }, "m"},
		{"deepseek", chatBody, func(c ProviderConfig) (Provider, error) { return NewDeepSeekProvider(c) }, "m"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := slowServer(t, think, 0, tc.body)
			provider, err := tc.build(ProviderConfig{APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client()})
			if err != nil {
				t.Fatalf("building the provider: %v", err)
			}
			resp, err := provider.Complete(context.Background(), CompletionRequest{Model: tc.model, SystemPrompt: "system", UserPrompt: "user"})
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if !resp.Timing.Measured() || resp.Timing.Wait < think-10*time.Millisecond || resp.Timing.Network < resp.Timing.Wait {
				t.Errorf("timing %+v from a server that thought for %v", resp.Timing, think)
			}
		})
	}
}

// A trace the caller put on the context is still called: the clock adds its
// own and does not take the caller's place.
func TestTheCallClockKeepsTheCallersOwnTrace(t *testing.T) {
	server := slowServer(t, 0, 0, responsesAnswerBody)
	provider, err := NewOpenAIProvider(ProviderConfig{APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}
	gotConnection, gotFirstByte := false, false
	ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
		GotConn:              func(httptrace.GotConnInfo) { gotConnection = true },
		GotFirstResponseByte: func() { gotFirstByte = true },
	})
	resp, err := provider.Complete(ctx, CompletionRequest{Model: "gpt-6-luna", SystemPrompt: "system", UserPrompt: "user"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !gotConnection || !gotFirstByte {
		t.Errorf("the caller's own trace was not called: connection %v, first byte %v", gotConnection, gotFirstByte)
	}
	if !resp.Timing.Measured() {
		t.Error("the call was not timed beside the caller's trace")
	}
}

func TestCallClockTiming(t *testing.T) {
	at := func(base time.Time, ms int) time.Time { return base.Add(time.Duration(ms) * time.Millisecond) }
	base := time.Now().Add(-time.Second)
	cases := []struct {
		name  string
		clock *callClock
		want  types.Timing // the phases; Network is checked on its own
	}{
		{"a new connection", &callClock{start: base,
			lookupStart: at(base, 0), lookupDone: at(base, 10),
			connectStart: at(base, 10), connectDone: at(base, 30),
			handshakeStart: at(base, 30), handshakeDone: at(base, 70),
			gotConn: at(base, 70), wrote: at(base, 72), firstByte: at(base, 272)},
			types.Timing{Lookup: 10 * time.Millisecond, Connect: 20 * time.Millisecond, Handshake: 40 * time.Millisecond, Send: 2 * time.Millisecond, Wait: 200 * time.Millisecond}},
		{"a reused connection", &callClock{start: base, reused: true, gotConn: at(base, 1), wrote: at(base, 2), firstByte: at(base, 102)},
			types.Timing{Reused: true, Send: time.Millisecond, Wait: 100 * time.Millisecond}},
		{"no lookup, an address was given", &callClock{start: base,
			connectStart: at(base, 0), connectDone: at(base, 5), gotConn: at(base, 5), wrote: at(base, 6), firstByte: at(base, 56)},
			types.Timing{Connect: 5 * time.Millisecond, Send: time.Millisecond, Wait: 50 * time.Millisecond}},
		{"no handshake, plain HTTP", &callClock{start: base,
			connectStart: at(base, 0), connectDone: at(base, 5), gotConn: at(base, 5), wrote: at(base, 5), firstByte: at(base, 25)},
			types.Timing{Connect: 5 * time.Millisecond, Wait: 20 * time.Millisecond}},
		{"nothing fired", &callClock{start: base}, types.Timing{}},
		{"the request was never written", &callClock{start: base, gotConn: at(base, 3)}, types.Timing{}},
		{"no first byte", &callClock{start: base, gotConn: at(base, 3), wrote: at(base, 4)}, types.Timing{Send: time.Millisecond}},
		{"a lookup that never finished", &callClock{start: base, lookupStart: at(base, 0)}, types.Timing{}},
		{"events out of order are not a negative time", &callClock{start: base, gotConn: at(base, 9), wrote: at(base, 4), firstByte: at(base, 2)}, types.Timing{}},
		{"a handshake with no connect, a tunnel", &callClock{start: base, handshakeStart: at(base, 1), handshakeDone: at(base, 31), gotConn: at(base, 31), wrote: at(base, 32), firstByte: at(base, 40)},
			types.Timing{Handshake: 30 * time.Millisecond, Send: time.Millisecond, Wait: 8 * time.Millisecond}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.clock.timing()
			if got.Network < time.Second {
				t.Errorf("Network = %v for a call that started a second ago", got.Network)
			}
			if got.Read < 0 {
				t.Errorf("Read = %v", got.Read)
			}
			// Read runs to "now", so it is checked for being there and not for
			// its length.
			if (got.Read > 0) != !tc.clock.firstByte.IsZero() && !tc.clock.firstByte.After(time.Now()) {
				t.Errorf("Read = %v with a first byte at %v", got.Read, tc.clock.firstByte)
			}
			if got.Calls != 1 {
				t.Errorf("Calls = %d, want 1: one clock times one call", got.Calls)
			}
			got.Calls, got.Network, got.Read = 0, 0, 0
			if got != tc.want {
				t.Errorf("timing = %+v, want %+v", got, tc.want)
			}
		})
	}
}
