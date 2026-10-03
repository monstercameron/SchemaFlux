package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Round 13. The shared transport and the warming request.

// roundTripFunc is a transport that is a function: a stub of the kind a
// test installs as the process's default, and a type that cannot be compared.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// sliceHoldingTransport is a recorder that holds a slice, which also cannot be
// compared.
type sliceHoldingTransport struct{ seen []string }

func (r sliceHoldingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("not a real transport")
}

// interfaceHoldingTransport is a struct that the compiler allows to be compared and
// that panics when it is, because the interface it holds is a slice.
type interfaceHoldingTransport struct{ note any }

func (r interfaceHoldingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("not a real transport")
}

// pointerTransport is a recorder that is a pointer, so it compares.
type pointerTransport struct{ calls int }

func (r *pointerTransport) RoundTrip(*http.Request) (*http.Response, error) {
	r.calls++
	return nil, errors.New("not a real transport")
}

// installDefaultTransport installs a process default for a test and puts back
// what was there, and what the shared transport had made of it.
func installDefaultTransport(t *testing.T, replacement http.RoundTripper) {
	t.Helper()
	shared.Lock()
	previousFrom, previousTuned := shared.from, shared.tuned
	shared.from, shared.tuned = nil, nil
	shared.Unlock()
	previous := http.DefaultTransport
	http.DefaultTransport = replacement
	t.Cleanup(func() {
		http.DefaultTransport = previous
		shared.Lock()
		shared.from, shared.tuned = previousFrom, previousTuned
		shared.Unlock()
	})
}

// A provider built while a stub is the process's default must not take the
// process down. The comparison of the default with the one the copy was made
// from panicked when both held a function type, and a provider with
// ExtraHeaders asks for the transport twice.
func TestAProviderBuiltOnAnUncomparableDefaultTransportDoesNotPanic(t *testing.T) {
	stubs := []struct {
		name string
		stub http.RoundTripper
	}{
		{"a function", roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("stub") })},
		{"a struct with a slice", sliceHoldingTransport{seen: []string{"a"}}},
		{"a struct holding an interface that holds a slice", interfaceHoldingTransport{note: []int{1}}},
		{"a struct with a slice, empty", sliceHoldingTransport{}},
	}
	for _, tc := range stubs {
		t.Run(tc.name, func(t *testing.T) {
			installDefaultTransport(t, tc.stub)
			for range 3 {
				first := resolveHTTPClient(ProviderConfig{ExtraHeaders: map[string]string{"api-key": "k"}})
				second := resolveHTTPClient(ProviderConfig{})
				if first.Transport == nil || second.Transport == nil {
					t.Fatal("a provider was left with no transport")
				}
			}
			// Two providers of the same kind, built one after the other.
			for range 2 {
				if _, err := NewOpenAIProvider(ProviderConfig{APIKey: "k", ExtraHeaders: map[string]string{"api-key": "k"}}); err != nil {
					t.Fatalf("NewOpenAIProvider: %v", err)
				}
			}
			// The stub is what the provider sends through, as it is.
			if got := sharedTransport(); !sameTransportForTest(got, tc.stub) {
				t.Errorf("the shared transport is %T, want the stub the process installed", got)
			}
		})
	}
}

// sameTransportForTest compares by type, which is all an uncomparable
// stub allows.
func sameTransportForTest(a, b http.RoundTripper) bool {
	return transportTypeName(a) == transportTypeName(b)
}

func transportTypeName(rt http.RoundTripper) string {
	return func() string {
		switch rt.(type) {
		case roundTripFunc:
			return "func"
		case sliceHoldingTransport:
			return "slice"
		case interfaceHoldingTransport:
			return "iface"
		case *pointerTransport:
			return "ptr"
		case *http.Transport:
			return "transport"
		}
		return "other"
	}()
}

func TestSharedTransportDirectly(t *testing.T) {
	transportWith := func(perHost, total int, idle time.Duration, http2 bool) *http.Transport {
		return &http.Transport{MaxIdleConnsPerHost: perHost, MaxIdleConns: total, IdleConnTimeout: idle, ForceAttemptHTTP2: http2}
	}
	cases := []struct {
		name        string
		def         http.RoundTripper
		perHost     int
		total       int
		idle        time.Duration
		http2       bool
		viaDefault  bool // the transport is the default itself, as it is
		checkFields bool
	}{
		{"Go's own settings: both counts raised, the timeout kept", transportWith(2, 100, 90*time.Second, true), sharedIdleConnsPerHost, sharedIdleConns, 90 * time.Second, true, false, true},
		{"a count per host of zero is Go's default of two, and is raised", transportWith(0, 100, 0, false), sharedIdleConnsPerHost, sharedIdleConns, 0, false, false, true},
		{"a count per host that is higher is kept", transportWith(300, 100, 0, false), 300, sharedIdleConns, 0, false, false, true},
		{"a count per host equal to ours is kept", transportWith(sharedIdleConnsPerHost, 100, 0, false), sharedIdleConnsPerHost, sharedIdleConns, 0, false, false, true},
		{"a total that is higher is kept", transportWith(2, 2000, 0, false), sharedIdleConnsPerHost, 2000, 0, false, false, true},
		{"a total of zero is no limit, and stays so", transportWith(2, 0, 0, false), sharedIdleConnsPerHost, 0, 0, false, false, true},
		{"a total that is lower is raised", transportWith(2, 10, 0, false), sharedIdleConnsPerHost, sharedIdleConns, 0, false, false, true},
		{"a short idle timeout set for a NAT is kept", transportWith(2, 100, 30*time.Second, false), sharedIdleConnsPerHost, sharedIdleConns, 30 * time.Second, false, false, true},
		{"a long idle timeout is kept", transportWith(2, 100, 10*time.Minute, false), sharedIdleConnsPerHost, sharedIdleConns, 10 * time.Minute, false, false, true},
		{"no idle timeout is kept as none", transportWith(2, 100, 0, false), sharedIdleConnsPerHost, sharedIdleConns, 0, false, false, true},
		{"HTTP/2 off stays off", transportWith(2, 100, 0, false), sharedIdleConnsPerHost, sharedIdleConns, 0, false, false, true},
		{"HTTP/2 on stays on", transportWith(2, 100, 0, true), sharedIdleConnsPerHost, sharedIdleConns, 0, true, false, true},
		{"a default that is not a transport is used as it is", &pointerTransport{}, 0, 0, 0, false, true, false},
		{"a default that is a function is used as it is", roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, nil }), 0, 0, 0, false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			installDefaultTransport(t, tc.def)
			var before http.Transport
			if original, ok := tc.def.(*http.Transport); ok {
				before.MaxIdleConnsPerHost, before.MaxIdleConns = original.MaxIdleConnsPerHost, original.MaxIdleConns
				before.IdleConnTimeout, before.ForceAttemptHTTP2 = original.IdleConnTimeout, original.ForceAttemptHTTP2
			}
			got := sharedTransport()
			if tc.viaDefault {
				if transportTypeName(got) != transportTypeName(tc.def) {
					t.Fatalf("the transport is %T, want the default's own %T", got, tc.def)
				}
				return
			}
			tuned, ok := got.(*http.Transport)
			if !ok {
				t.Fatalf("the shared transport is a %T", got)
			}
			if tuned == tc.def {
				t.Fatal("the process's default itself was handed out and would be tuned")
			}
			if tuned.MaxIdleConnsPerHost != tc.perHost || tuned.MaxIdleConns != tc.total ||
				tuned.IdleConnTimeout != tc.idle || tuned.ForceAttemptHTTP2 != tc.http2 {
				t.Errorf("per host %d, total %d, idle %v, http2 %v; want %d, %d, %v, %v",
					tuned.MaxIdleConnsPerHost, tuned.MaxIdleConns, tuned.IdleConnTimeout, tuned.ForceAttemptHTTP2,
					tc.perHost, tc.total, tc.idle, tc.http2)
			}
			// The process's own transport is not written to.
			original := tc.def.(*http.Transport)
			if original.MaxIdleConnsPerHost != before.MaxIdleConnsPerHost || original.MaxIdleConns != before.MaxIdleConns ||
				original.IdleConnTimeout != before.IdleConnTimeout || original.ForceAttemptHTTP2 != before.ForceAttemptHTTP2 {
				t.Error("the default was tuned in place")
			}
			// The same default, asked again, is the same copy.
			if again := sharedTransport(); again != got {
				t.Error("two providers on one default do not share a transport")
			}
		})
	}
}

func TestSharedTransportFollowsADefaultThatIsReplaced(t *testing.T) {
	first := &http.Transport{MaxIdleConnsPerHost: 2, MaxIdleConns: 100, IdleConnTimeout: 30 * time.Second}
	installDefaultTransport(t, first)
	built := sharedTransport()
	if built.(*http.Transport).IdleConnTimeout != 30*time.Second {
		t.Fatal("the copy does not carry the default's settings")
	}

	// Replaced: the next provider is built on the new one.
	second := &http.Transport{MaxIdleConnsPerHost: 2, MaxIdleConns: 100, IdleConnTimeout: 5 * time.Second}
	http.DefaultTransport = second
	rebuilt := sharedTransport()
	if rebuilt == built || rebuilt.(*http.Transport).IdleConnTimeout != 5*time.Second {
		t.Error("a default that was replaced is not followed by a provider built after")
	}

	// Replaced by a stub that is a pointer: used as it is, and then again.
	stub := &pointerTransport{}
	http.DefaultTransport = stub
	if got := sharedTransport(); got != http.RoundTripper(stub) {
		t.Errorf("the shared transport is %T, want the stub", got)
	}
	if got := sharedTransport(); got != http.RoundTripper(stub) {
		t.Errorf("the second ask returned %T, want the stub", got)
	}

	// And back to a real transport.
	http.DefaultTransport = first
	if back := sharedTransport(); back == built || back.(*http.Transport).MaxIdleConnsPerHost != sharedIdleConnsPerHost {
		t.Error("the transport was not rebuilt on the default that came back")
	}
}

// A default that is changed in place keeps its pointer, and the copy is not
// made again. This is the documented limit, pinned so that it is a decision.
func TestSharedTransportDoesNotFollowADefaultChangedInPlace(t *testing.T) {
	own := &http.Transport{MaxIdleConnsPerHost: 2, MaxIdleConns: 100}
	installDefaultTransport(t, own)
	built := sharedTransport().(*http.Transport)
	own.IdleConnTimeout = 7 * time.Second
	if after := sharedTransport().(*http.Transport); after != built || after.IdleConnTimeout != 0 {
		t.Errorf("a default changed in place was followed: idle timeout %v", after.IdleConnTimeout)
	}
}

func TestSharedTransportWithNoDefaultDoesNotPanic(t *testing.T) {
	installDefaultTransport(t, nil)
	if got := sharedTransport(); got != nil {
		t.Errorf("the shared transport is %T for no default", got)
	}
}

func TestLat13SameTransport(t *testing.T) {
	ptr := &pointerTransport{}
	real := &http.Transport{}
	fn := roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, nil })
	cases := []struct {
		name string
		a, b http.RoundTripper
		want bool
	}{
		{"the same pointer", ptr, ptr, true},
		{"two pointers", ptr, &pointerTransport{}, false},
		{"the same real transport", real, real, true},
		{"two real transports", real, &http.Transport{}, false},
		{"a pointer and a real transport", ptr, real, false},
		{"nothing and nothing", nil, nil, true},
		{"nothing and something", nil, real, false},
		{"something and nothing", real, nil, false},
		{"a function and itself, which cannot be compared", fn, fn, false},
		{"a function and a pointer", fn, ptr, false},
		{"a slice-holding struct and itself", sliceHoldingTransport{seen: []string{"a"}}, sliceHoldingTransport{seen: []string{"a"}}, false},
		{"an interface holding a slice and itself", interfaceHoldingTransport{note: []int{1}}, interfaceHoldingTransport{note: []int{1}}, false},
		{"an interface holding a number, which compares", interfaceHoldingTransport{note: 1}, interfaceHoldingTransport{note: 1}, true},
	}
	for _, tc := range cases {
		if got := sameTransport(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// What a warming request carries.
func TestWarmCarriesNothingTheCallerConfigured(t *testing.T) {
	chatBody := `{"model":"m","choices":[{"message":{"content":"an answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	anthropicBody := `{"id":"msg_1","model":"claude-x","content":[{"type":"text","text":"an answer"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
	cases := []struct {
		name  string
		body  string
		model string
	}{
		{"openai", responsesAnswerBody, "gpt-6-luna"},
		{"anthropic", anthropicBody, "claude-x"},
		{"openrouter", chatBody, "m"},
		{"cerebras", chatBody, "m"},
		{"deepseek", chatBody, "m"},
		{"qwen", chatBody, "m"},
		{"zai", chatBody, "m"},
	}
	secrets := []string{"Authorization", "X-Api-Key", "Api-Key", "X-Azure-Key", "Openai-Organization", "Anthropic-Version", "Cookie"}
	for _, tc := range cases {
		for _, ownClient := range []bool{true, false} {
			name := tc.name + ", the library's own client"
			if ownClient {
				name = tc.name + ", the caller's client"
			}
			t.Run(name, func(t *testing.T) {
				var mu sync.Mutex
				var warm []http.Header
				var posts atomic.Int32
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodHead {
						mu.Lock()
						warm = append(warm, r.Header.Clone())
						mu.Unlock()
						return
					}
					posts.Add(1)
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(tc.body))
				})
				config := ProviderConfig{
					APIKey: "sk-secret-key", OrgID: "org-secret",
					ExtraHeaders: map[string]string{"api-key": "azure-secret", "X-Azure-Key": "another", "X-Custom": "custom"},
				}
				var server *httptest.Server
				if ownClient {
					server = httptest.NewTLSServer(handler)
					config.HTTPClient = server.Client()
				} else {
					server = httptest.NewServer(handler)
					config.Timeout = 10 * time.Second
				}
				defer server.Close()
				config.BaseURL = server.URL
				provider, err := CreateProvider(tc.name, config)
				if err != nil {
					t.Fatalf("CreateProvider: %v", err)
				}

				if warmed, err := Warm(context.Background(), provider); err != nil || !warmed {
					t.Fatalf("Warm = %v, %v", warmed, err)
				}
				mu.Lock()
				seen := append([]http.Header(nil), warm...)
				mu.Unlock()
				if len(seen) != 1 {
					t.Fatalf("%d warming requests reached the server", len(seen))
				}
				for _, header := range append(secrets, "X-Custom") {
					if value := seen[0].Get(header); value != "" {
						t.Errorf("the warming request carries %s", header)
					}
				}
				if posts.Load() != 0 {
					t.Errorf("warming sent %d completions", posts.Load())
				}

				// The connection that was opened is the one the call uses, with
				// the headers it is configured with.
				resp, err := provider.Complete(context.Background(), CompletionRequest{Model: tc.model, SystemPrompt: "system", UserPrompt: "user"})
				if err != nil {
					t.Fatalf("Complete: %v", err)
				}
				if !resp.Timing.Reused || resp.Timing.Setup() != 0 {
					t.Errorf("the call after Warm paid %v for its connection (reused %v)", resp.Timing.Setup(), resp.Timing.Reused)
				}
			})
		}
	}
}

// A call still carries what it is configured with: the warming request leaves
// the provider's client as it was.
func TestWarmingDoesNotTakeTheHeadersOffTheCalls(t *testing.T) {
	var got http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			got = r.Header.Clone()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responsesAnswerBody))
	}))
	defer server.Close()
	provider, err := NewOpenAIProvider(ProviderConfig{APIKey: "k", BaseURL: server.URL, ExtraHeaders: map[string]string{"api-key": "azure-secret"}})
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}
	if err := provider.Warm(context.Background()); err != nil {
		t.Fatalf("Warm: %v", err)
	}
	if _, err := provider.Complete(context.Background(), CompletionRequest{Model: "gpt-6-luna", UserPrompt: "u"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got.Get("Api-Key") != "azure-secret" || got.Get("Authorization") != "Bearer k" {
		t.Errorf("the call lost its headers after a warm-up: %v", got)
	}
}

// A redirect is not followed: it is an answer, and the connection is open.
func TestWarmDoesNotFollowARedirect(t *testing.T) {
	for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var elsewhere atomic.Int32
			other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { elsewhere.Add(1) }))
			defer other.Close()
			var asked atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				asked.Add(1)
				http.Redirect(w, r, other.URL+"/there", status)
			}))
			defer server.Close()
			provider, err := NewOpenAIProvider(ProviderConfig{APIKey: "k", BaseURL: server.URL, ExtraHeaders: map[string]string{"api-key": "azure-secret"}})
			if err != nil {
				t.Fatalf("NewOpenAIProvider: %v", err)
			}
			if warmed, err := Warm(context.Background(), provider); err != nil || !warmed {
				t.Fatalf("Warm = %v, %v: a redirect is as good an answer as any", warmed, err)
			}
			if asked.Load() != 1 || elsewhere.Load() != 0 {
				t.Errorf("the server was asked %d times and the host it redirects to %d", asked.Load(), elsewhere.Load())
			}
		})
	}
}

// A server that takes the connection and never answers does not hold the
// caller for ever.
func TestWarmIsBoundedWhenTheServerNeverAnswers(t *testing.T) {
	previous := warmTimeout
	warmTimeout = 150 * time.Millisecond
	t.Cleanup(func() { warmTimeout = previous })

	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer server.Close()
	defer close(release)

	provider, err := NewOpenAIProvider(ProviderConfig{APIKey: "k", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}
	started := time.Now()
	warmed, err := Warm(context.Background(), provider)
	if err == nil || warmed {
		t.Fatalf("Warm = %v, %v for a server that never answers", warmed, err)
	}
	if took := time.Since(started); took > 3*time.Second {
		t.Errorf("Warm took %v against a bound of %v", took, warmTimeout)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("the error does not say it ran out of time: %v", err)
	}
}

// A context with a deadline of the caller's is the bound, and is not replaced
// by the library's.
func TestWarmKeepsTheCallersOwnDeadline(t *testing.T) {
	previous := warmTimeout
	warmTimeout = time.Hour
	t.Cleanup(func() { warmTimeout = previous })
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer server.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := warmConnection(ctx, server.Client(), "p", server.URL); err == nil {
		t.Fatal("no error for a server that never answered")
	}
	if took := time.Since(started); took > 3*time.Second {
		t.Errorf("took %v, and the caller's deadline was 100ms", took)
	}
}

func TestWarmConnectionDirectly(t *testing.T) {
	statuses := map[string]int{"/ok": 200, "/missing": 404, "/denied": 401, "/error": 500, "/unavailable": 503, "/redirect": 302}
	var heads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			heads.Add(1)
		}
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/ok", http.StatusFound)
			return
		}
		w.WriteHeader(statuses[r.URL.Path])
	}))
	defer server.Close()
	over, cancel := context.WithCancel(context.Background())
	cancel()
	gone := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	goneURL := gone.URL
	gone.Close()

	cases := []struct {
		name     string
		ctx      context.Context
		endpoint string
		wantErr  bool
	}{
		{"an answer of 200", context.Background(), server.URL + "/ok", false},
		{"an answer of 404", context.Background(), server.URL + "/missing", false},
		{"an answer of 401", context.Background(), server.URL + "/denied", false},
		{"an answer of 500", context.Background(), server.URL + "/error", false},
		{"an answer of 503", context.Background(), server.URL + "/unavailable", false},
		{"a redirect, which is an answer", context.Background(), server.URL + "/redirect", false},
		{"no endpoint", context.Background(), "", true},
		{"an endpoint of space", context.Background(), "   ", true},
		{"an endpoint that is not a URL", context.Background(), "http://[::1", true},
		{"a context that is over", over, server.URL + "/ok", true},
		{"a server that is gone", context.Background(), goneURL, true},
		{"an endpoint with space around it", context.Background(), "  " + server.URL + "/ok  ", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := warmConnection(tc.ctx, server.Client(), "p", tc.endpoint)
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, want error %v", err, tc.wantErr)
			}
		})
	}
}

// wrappingProvider is a provider that wraps another and says so.
type wrappingProvider struct {
	notAWarmer
	inner Provider
}

func (w wrappingProvider) Unwrap() Provider { return w.inner }

type selfWrappingProvider struct{ notAWarmer }

func (l *selfWrappingProvider) Unwrap() Provider { return l }

type warmingProvider struct {
	notAWarmer
	err   error
	calls atomic.Int32
}

func (w *warmingProvider) Warm(context.Context) error { w.calls.Add(1); return w.err }

func TestWarmLooksThroughWrappers(t *testing.T) {
	bottom := &warmingProvider{}
	broken := &warmingProvider{err: errors.New("no route")}
	cases := []struct {
		name     string
		provider Provider
		warmed   bool
		wantErr  bool
		target   *warmingProvider
	}{
		{"a provider that warms", bottom, true, false, bottom},
		{"one wrapper over it", wrappingProvider{inner: bottom}, true, false, bottom},
		{"three wrappers over it", wrappingProvider{inner: wrappingProvider{inner: wrappingProvider{inner: bottom}}}, true, false, bottom},
		{"a wrapper over a provider that cannot warm", wrappingProvider{inner: notAWarmer{}}, false, false, nil},
		{"a wrapper over nothing", wrappingProvider{}, false, false, nil},
		{"a failure at the bottom is the failure", wrappingProvider{inner: broken}, false, true, broken},
		{"a wrapper that is its own inner", &selfWrappingProvider{}, false, false, nil},
		{"nothing", nil, false, false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.target != nil {
				tc.target.calls.Store(0)
			}
			warmed, err := Warm(context.Background(), tc.provider)
			if warmed != tc.warmed || (err != nil) != tc.wantErr {
				t.Errorf("Warm = %v, %v; want %v, error %v", warmed, err, tc.warmed, tc.wantErr)
			}
			if tc.target != nil && tc.target.calls.Load() != 1 {
				t.Errorf("the provider at the bottom was warmed %d times", tc.target.calls.Load())
			}
		})
	}
}
