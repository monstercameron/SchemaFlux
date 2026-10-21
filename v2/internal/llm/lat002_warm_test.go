package llm

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// LAT-002. Connections.

// The first call of a process pays for a name lookup, a connection and a
// handshake, which on a call that otherwise takes a second is a quarter as
// much again. Warm pays for them ahead of the call, so the first decision a
// program makes is as quick as its second.
func TestWarmPaysForTheConnectionBeforeTheFirstCall(t *testing.T) {
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
		for _, warmed := range []bool{false, true} {
			name := tc.name + ", cold"
			if warmed {
				name = tc.name + ", warmed"
			}
			t.Run(name, func(t *testing.T) {
				var completions, connections atomic.Int32
				server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodPost {
						completions.Add(1)
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(tc.body))
				}))
				// The connections the server accepts are what shows whether the
				// call had to make one, whatever the clock can resolve.
				server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
					if state == http.StateNew {
						connections.Add(1)
					}
				}
				server.StartTLS()
				defer server.Close()
				provider, err := tc.build(ProviderConfig{APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client()})
				if err != nil {
					t.Fatalf("building the provider: %v", err)
				}

				if warmed {
					did, err := Warm(context.Background(), provider)
					if err != nil || !did {
						t.Fatalf("Warm = %v, %v", did, err)
					}
					if completions.Load() != 0 {
						t.Fatalf("warming made %d completion requests: it has to cost nothing", completions.Load())
					}
				}
				connectionsBefore := connections.Load()
				resp, err := provider.Complete(context.Background(), CompletionRequest{Model: tc.model, SystemPrompt: "system", UserPrompt: "user"})
				if err != nil {
					t.Fatalf("Complete: %v", err)
				}
				wantNew := int32(1)
				if warmed {
					wantNew = 0
				}
				if made := connections.Load() - connectionsBefore; made != wantNew {
					t.Errorf("the first call made %d connections, want %d", made, wantNew)
				}
				if resp.Timing.Reused != warmed {
					t.Errorf("the first call reused a connection = %v, want %v", resp.Timing.Reused, warmed)
				}
				// A connection that was made ahead costs the call nothing at all.
				// One that was not has to have been made, which the server's
				// count above shows; what the clock says it cost is not asked
				// for. The connect is to the loopback address, and on a clock as
				// coarse as Windows' a connect, and often a handshake, begins
				// and ends in one tick and measures as nothing. The timing is
				// only required not to be negative.
				timing := resp.Timing
				if warmed {
					if timing.Setup() != 0 {
						t.Errorf("the first call paid %v for a connection that was warmed", timing.Setup())
					}
				} else if timing.Handshake < 0 || timing.Connect < 0 || timing.Lookup < 0 {
					t.Errorf("negative setup time: lookup %v, connect %v, handshake %v", timing.Lookup, timing.Connect, timing.Handshake)
				}
			})
		}
	}
}

// What Warm does when there is nothing to warm, or it cannot.
func TestWarmSaysWhatItDid(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such page", http.StatusNotFound)
	}))
	build := func(url string, client *http.Client) Provider {
		provider, err := NewOpenAIProvider(ProviderConfig{APIKey: "test-key", BaseURL: url, HTTPClient: client})
		if err != nil {
			t.Fatalf("NewOpenAIProvider: %v", err)
		}
		return provider
	}
	local, _ := NewLocalProvider(ProviderConfig{})
	over, cancel := context.WithCancel(context.Background())
	cancel()

	gone := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	goneURL, goneClient := gone.URL, gone.Client()
	gone.Close()

	cases := []struct {
		name     string
		ctx      context.Context
		provider Provider
		warmed   bool
		wantErr  bool
	}{
		{"a server that answers 404: the connection is open, which is all that was wanted", context.Background(), build(server.URL, server.Client()), true, false},
		{"the same again: already warm", context.Background(), build(server.URL, server.Client()), true, false},
		{"a provider with nothing to connect to", context.Background(), local, false, false},
		{"no provider", context.Background(), nil, false, false},
		{"a server that is not there", context.Background(), build(goneURL, goneClient), false, true},
		{"a context that is over", over, build(server.URL, server.Client()), false, true},
		{"a provider wrapped by a fake that cannot warm", context.Background(), notAWarmer{}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			warmed, err := Warm(tc.ctx, tc.provider)
			if warmed != tc.warmed || tc.wantErr != (err != nil) {
				t.Errorf("Warm = %v, %v; want %v, error %v", warmed, err, tc.warmed, tc.wantErr)
			}
		})
	}
	server.Close()
}

type notAWarmer struct{}

func (notAWarmer) Complete(context.Context, CompletionRequest) (CompletionResponse, error) {
	return CompletionResponse{}, nil
}
func (notAWarmer) Name() string                           { return "fake" }
func (notAWarmer) EstimateCost(CompletionRequest) float64 { return 0 }
func (notAWarmer) RetryPolicy() (int, time.Duration)      { return 0, 0 }

// A program that asks many things at once keeps its connections. The
// transport a provider used when the caller supplied none was Go's default,
// which keeps two idle connections to a host: of forty calls made together
// over HTTP/1.1, thirty-eight closed their connections afterwards and the next
// forty opened thirty-eight new ones.
func TestManyCallsAtOnceKeepTheirConnections(t *testing.T) {
	const together = 40
	var opened atomic.Int32
	var firstArrivals atomic.Int32
	allFirstArrived := make(chan struct{})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Keep the first batch in flight until all forty have a connection.
		// A fixed sleep does not guarantee this on a busy or slow test host.
		if count := firstArrivals.Add(1); count <= together {
			if count == together {
				close(allFirstArrived)
			}
			select {
			case <-allFirstArrived:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responsesAnswerBody))
	}))
	// Connections the server was given, which is what the transport opened.
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			opened.Add(1)
		}
	}
	server.Start()
	defer server.Close()

	// No HTTPClient: the provider builds its own, which is what is under test.
	provider, err := NewOpenAIProvider(ProviderConfig{APIKey: "test-key", BaseURL: server.URL, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}
	batch := func() (reused int) {
		var wait sync.WaitGroup
		var count atomic.Int32
		for range together {
			wait.Add(1)
			go func() {
				defer wait.Done()
				resp, err := provider.Complete(context.Background(), CompletionRequest{Model: "gpt-6-luna", SystemPrompt: "system", UserPrompt: "user"})
				if err != nil {
					t.Errorf("Complete: %v", err)
					return
				}
				if resp.Timing.Reused {
					count.Add(1)
				}
			}()
		}
		wait.Wait()
		return int(count.Load())
	}

	batch()
	afterFirst := int(opened.Load())
	if afterFirst < together {
		t.Fatalf("the first batch opened only %d connections for %d calls at once", afterFirst, together)
	}
	// A connection goes back to the pool a moment after the call that used it
	// has returned, so a few of the second batch can still find none.
	const slack = 4
	reused := batch()
	if reused < together-slack {
		t.Errorf("%d of %d calls in the second batch reused a connection from the first", reused, together)
	}
	if more := int(opened.Load()) - afterFirst; more > slack {
		t.Errorf("the second batch opened %d more connections, after the first opened %d", more, afterFirst)
	}
	// Never more than the transport lets be idle to one host.
	if total := int(opened.Load()); total > sharedIdleConnsPerHost {
		t.Errorf("%d connections were opened for %d calls at once, over the transport's limit of %d idle to a host", total, together, sharedIdleConnsPerHost)
	}
}

// The transport is the caller's when they supply a client, and is left alone.
func TestACallersOwnClientIsNotGivenAnotherTransport(t *testing.T) {
	own := &http.Transport{MaxIdleConnsPerHost: 1}
	client := &http.Client{Transport: own}
	resolved := resolveHTTPClient(ProviderConfig{HTTPClient: client})
	if resolved != client || resolved.Transport != own {
		t.Error("the caller's client or its transport was replaced")
	}
	withHeaders := resolveHTTPClient(ProviderConfig{HTTPClient: client, ExtraHeaders: map[string]string{"X-A": "b"}})
	wrapped, ok := withHeaders.Transport.(*customTransport)
	if !ok || wrapped.transport != own {
		t.Error("the caller's transport is not the one under the headers")
	}
	if client.Transport != own {
		t.Error("the caller's client was written to")
	}

	// With none supplied, every provider shares one tuned transport.
	first := resolveHTTPClient(ProviderConfig{Timeout: time.Second})
	second := resolveHTTPClient(ProviderConfig{Timeout: 2 * time.Second})
	if first.Transport == nil || first.Transport != second.Transport {
		t.Error("two providers do not share a transport, so they do not share connections")
	}
	if first.Timeout != time.Second || second.Timeout != 2*time.Second {
		t.Error("the timeouts were not kept")
	}
	tuned, ok := first.Transport.(*http.Transport)
	if !ok || tuned.MaxIdleConnsPerHost < 64 {
		t.Errorf("the shared transport keeps %v idle connections to a host", tuned)
	}
	if tuned == http.DefaultTransport {
		t.Error("the shared transport is Go's default, which every other package in the process also sets and reads")
	}
}
