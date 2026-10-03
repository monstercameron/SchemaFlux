package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

const (
	// testKey is the key every test provider is configured with. No error, log
	// line or encoding of one may contain it.
	testKey = "sk-test-0123456789abcdefghij"

	// testMarker stands for what a provider writes in an error body: free text
	// that Error must not print and Detail may.
	testMarker = "vendor-marker-5e1d9c"
)

// the answers each dialect's success path reads.
const (
	responsesOK = `{"id":"resp_1","status":"completed","model":"gpt-test","output":[{"type":"message","content":[` +
		`{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30}}`
	chatOK = `{"model":"chat-test","choices":[{"finish_reason":"stop","message":{"content":"hello"}}],` +
		`"usage":{"prompt_tokens":4,"completion_tokens":5,"total_tokens":9}}`
	anthropicOK = `{"id":"msg_1","model":"claude-test","stop_reason":"end_turn","content":[{"type":"text","text":"hello"}],` +
		`"usage":{"input_tokens":3,"output_tokens":4}}`
)

// lookupOf is an environment of the pairs given as name, value, name, value.
func lookupOf(pairs ...string) func(string) (string, bool) {
	env := make(map[string]string, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		env[pairs[i]] = pairs[i+1]
	}
	return func(name string) (string, bool) {
		value, ok := env[name]
		return value, ok
	}
}

// seen is one request that reached a test server.
type seen struct {
	method, path string
	header       http.Header
	body         string
}

// server is a loopback server that records what reaches it.
type server struct {
	*httptest.Server

	mu       sync.Mutex
	requests []seen
}

// serve starts a loopback server that answers every request with handler and
// stops it when the test ends.
func serve(t testing.TB, handler http.HandlerFunc) *server {
	t.Helper()
	s := &server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, seen{r.Method, r.URL.Path, r.Header.Clone(), string(body)})
		s.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

// replying is a handler that answers with a status and a body, and the headers
// given as name, value, name, value.
func replying(status int, body string, headers ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i+1 < len(headers); i += 2 {
			w.Header().Set(headers[i], headers[i+1])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

// failingWith is the body a provider's error carries: free text holding the
// marker and the key, which is what a gateway that echoes a request does.
func failingWith(code string) string {
	return `{"error":{"message":"rejected ` + testMarker + ` ` + testKey + `","type":"invalid_request_error","code":"` + code + `"}}`
}

func (s *server) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func (s *server) last(t testing.TB) seen {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		t.Fatal("no request reached the server")
	}
	return s.requests[len(s.requests)-1]
}

// ask sends one request to p.
func ask(p schemaflux.Provider) (schemaflux.Response, error) {
	return p.Complete(context.Background(), schemaflux.Request{Model: "m", System: "s", User: "hi"})
}

// mustNew builds a provider and fails the test when it cannot.
func mustNew(t testing.TB, name string, cfg Config) schemaflux.Provider {
	t.Helper()
	p, err := newFrom(lookupOf(), name, cfg)
	if err != nil {
		t.Fatalf("building %s: %v", name, err)
	}
	return p
}

// errorOf returns err as the root's error, and fails the test when it is not one.
func errorOf(t testing.TB, err error) *schemaflux.Error {
	t.Helper()
	var e *schemaflux.Error
	if !errors.As(err, &e) {
		t.Fatalf("got %T (%v), want a *schemaflux.Error", err, err)
	}
	return e
}

// roundTripFunc is an http.RoundTripper that is a function.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// deadlineProbe is an *http.Client whose transport records the deadline of the
// context of the request it is given and then fails it, so that what a provider
// sets as its timeout can be read without waiting for it.
type deadlineProbe struct {
	client   *http.Client
	deadline atomic.Pointer[time.Time]
}

func newDeadlineProbe() *deadlineProbe {
	probe := &deadlineProbe{}
	probe.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if deadline, ok := r.Context().Deadline(); ok {
			probe.deadline.Store(&deadline)
		}
		return nil, errors.New("probe stops here")
	})}
	return probe
}

// remaining is how long the last request's context had left when it arrived,
// and false when it had no deadline.
func (p *deadlineProbe) remaining() (time.Duration, bool) {
	deadline := p.deadline.Load()
	if deadline == nil {
		return 0, false
	}
	return time.Until(*deadline), true
}

// wrapper is a provider that wraps another and does not say so: it hides what
// the provider under it can do.
type wrapper struct{ schemaflux.Provider }

// transparent is a wrapper that implements Unwrapper.
type transparent struct{ schemaflux.Provider }

func (t transparent) Unwrap() schemaflux.Provider { return t.Provider }

// looping is a wrapper that unwraps to itself.
type looping struct{ schemaflux.Provider }

func (l *looping) Unwrap() schemaflux.Provider { return l }

// scripted is a Provider that answers with a function and has a name.
type scripted struct {
	name     string
	complete func(ctx context.Context, req schemaflux.Request) (schemaflux.Response, error)
}

func (s *scripted) Name() string { return s.name }

func (s *scripted) Complete(ctx context.Context, req schemaflux.Request) (schemaflux.Response, error) {
	if s.complete == nil {
		return schemaflux.Response{Content: "scripted", Model: req.Model}, nil
	}
	return s.complete(ctx, req)
}
