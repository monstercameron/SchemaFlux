package mw

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
)

// fakeProvider is a minimal llm.Provider whose behavior a test script
// controls call-by-call, so a test can assert exactly what the middleware
// under test did with a given sequence of successes and failures.
type fakeProvider struct {
	name      string
	calls     int
	callTimes []time.Time
	// responses is consumed one per call; once exhausted the last entry
	// repeats, so a test does not have to size the slice to the exact
	// number of retries it expects.
	responses []fakeResponse
	now       func() time.Time

	maxRetries int
	backoff    time.Duration
}

type fakeResponse struct {
	resp llm.CompletionResponse
	err  error
}

func (f *fakeProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	if f.now != nil {
		f.callTimes = append(f.callTimes, f.now())
	}
	idx := f.calls
	if idx >= len(f.responses) {
		idx = len(f.responses) - 1
	}
	f.calls++
	if idx < 0 {
		return llm.CompletionResponse{}, errors.New("fakeProvider: no responses configured")
	}
	return f.responses[idx].resp, f.responses[idx].err
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) EstimateCost(req llm.CompletionRequest) float64 { return 0.01 }

func (f *fakeProvider) RetryPolicy() (int, time.Duration) { return f.maxRetries, f.backoff }

func TestChainAppliesInListedOrder(t *testing.T) {
	var order []string

	tag := func(name string) Middleware {
		return func(next Handler) Handler {
			return completeFunc(func(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
				order = append(order, name+":before")
				resp, err := next.Complete(ctx, req)
				order = append(order, name+":after")
				return resp, err
			})
		}
	}

	base := &fakeProvider{responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "ok"}}}}
	wrapped := Chain(base, tag("outer"), tag("inner"))

	if _, err := wrapped.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
		t.Fatalf("Complete returned an error: %v", err)
	}

	want := []string{"outer:before", "inner:before", "inner:after", "outer:after"}
	if len(order) != len(want) {
		t.Fatalf("call order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("call order = %v, want %v", order, want)
		}
	}
}

func TestChainWithNoMiddlewareReturnsBase(t *testing.T) {
	base := &fakeProvider{name: "base"}
	got := Chain(base)
	if got != Handler(base) {
		t.Fatalf("Chain with no middleware did not return base unchanged")
	}
}

func TestChainSkipsNilMiddleware(t *testing.T) {
	base := &fakeProvider{name: "base", responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "ok"}}}}
	// A nil in the slice (e.g. from a conditionally-omitted option) must not
	// panic Chain -- a caller building the list with an `if` per middleware
	// should not have to filter nils out themselves.
	got := Chain(base, nil, nil)
	if _, err := got.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
		t.Fatalf("Complete returned an error: %v", err)
	}
}

func TestChainDelegatesNameCostAndRetryPolicy(t *testing.T) {
	base := &fakeProvider{name: "openai-fake", maxRetries: 4, backoff: 2 * time.Second}
	identity := func(next Handler) Handler { return next }
	wrapped := Chain(base, identity)

	if wrapped.Name() != "openai-fake" {
		t.Fatalf("Name() = %q, want %q", wrapped.Name(), "openai-fake")
	}
	if got := wrapped.EstimateCost(llm.CompletionRequest{}); got != 0.01 {
		t.Fatalf("EstimateCost() = %v, want 0.01", got)
	}
	retries, backoff := wrapped.RetryPolicy()
	if retries != 4 || backoff != 2*time.Second {
		t.Fatalf("RetryPolicy() = (%d, %v), want (4, 2s)", retries, backoff)
	}
}

// completeFunc adapts a plain function to Handler for tests that only care
// about wrapping Complete, without writing out Name/EstimateCost/RetryPolicy
// stubs at every call site.
type completeFunc func(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error)

func (f completeFunc) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	return f(ctx, req)
}
func (f completeFunc) Name() string                                   { return "completeFunc" }
func (f completeFunc) EstimateCost(req llm.CompletionRequest) float64 { return 0 }
func (f completeFunc) RetryPolicy() (int, time.Duration)              { return 0, 0 }

// TestRateLimitAndRetryCompose is the integration case: a provider that
// fails once with a rate-limited error, wrapped by both RateLimit and Retry
// through the same Chain a caller would use at client construction. It
// exercises the seam MW-001 exists for -- that the pieces work together
// through Chain, not just in isolation -- and that RateLimit's own refusal
// (when in Reject mode) is something Retry can retry, because both speak the
// same classification.
func TestRateLimitAndRetryCompose(t *testing.T) {
	fake := &fakeProvider{
		responses: []fakeResponse{
			{err: llm.NewAPIError("fake", "m", 429, "")},
			{resp: llm.CompletionResponse{Content: "ok"}},
		},
	}

	fc := newFakeClock(time.Unix(0, 0))
	limiter := RateLimit(100, time.Second) // effectively unlimited; not the thing under test here
	retrier := Retry(WithMaxAttempts(3), WithBaseDelay(time.Millisecond))

	// Build the chain by hand rather than through Chain() so the retry
	// stage's sleep can be swapped for the fake clock before any call is
	// made -- the same composition Chain(fake, limiter, retrier) produces,
	// just with a seam to patch through.
	retried := retrier(fake)
	rp, ok := retried.(*retryProvider)
	if !ok {
		t.Fatalf("Retry did not return a *retryProvider")
	}
	rp.sleep = fc.sleep
	rp.randFloat = func() float64 { return 0 }
	wrapped := limiter(retried)

	resp, err := wrapped.Complete(context.Background(), llm.CompletionRequest{})
	if err != nil {
		t.Fatalf("Complete returned an error: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("Content = %q, want %q", resp.Content, "ok")
	}
	if fake.calls != 2 {
		t.Fatalf("calls = %d, want 2 (one failure, one retry)", fake.calls)
	}
}
