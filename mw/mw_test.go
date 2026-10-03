package mw

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/telemetry"
	"github.com/monstercameron/schemaflux/internal/types"
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

// senderProvider is a fakeProvider that sends another model than the one a
// request names.
type senderProvider struct{ fakeProvider }

func (p *senderProvider) ModelSentFor(requested string) string { return "sent-" + requested }

// Every handler this package builds answers Name, EstimateCost and RetryPolicy
// as the provider it wraps does, forwards llm.ModelSender to it, and says which
// provider it wraps.
func TestEveryMiddlewareDelegatesToTheProviderItWraps(t *testing.T) {
	constructors := []struct {
		name string
		wrap func(Handler) Handler
		// retries is the retry count the chain reports: the wrapped provider's
		// own, 4, unless the chain holds a Retry, which makes the repeats itself
		// and says that whatever calls the chain is to make none.
		retries int
	}{
		{"Budget", Budget(10), 4},
		{"Bulkhead", Bulkhead(2), 4},
		{"Cache", Cache(), 4},
		{"CircuitBreaker", CircuitBreaker(ops.CircuitBreakerConfig{}), 4},
		{"Fallback", Fallback([]FallbackRoute{{Handler: &fakeProvider{name: "alternate"}}}), 4},
		{"Hedge", Hedge(time.Second), 4},
		{"RateLimit", RateLimit(10, time.Second), 4},
		{"RedactEgress", RedactEgress(), 4},
		{"Retry", Retry(), 0},
		{"Retry with attempts of its own", Retry(WithMaxAttempts(9)), 0},
		{"Chain of all of them", func(base Handler) Handler {
			return Chain(base, Cache(), Retry(), RateLimit(10, time.Second), Budget(10), Bulkhead(2), RedactEgress(), Hedge(time.Second))
		}, 0},
		{"Chain of all of them but Retry", func(base Handler) Handler {
			return Chain(base, Cache(), RateLimit(10, time.Second), Budget(10), Bulkhead(2), RedactEgress(), Hedge(time.Second))
		}, 4},
		{"Retry inside a Fallback", func(base Handler) Handler {
			return Chain(base, Fallback(nil), Retry())
		}, 0},
		{"Retry around a Fallback", func(base Handler) Handler {
			return Chain(base, Retry(), Fallback(nil))
		}, 0},
		{"Retry in a pair's generating provider", func(base Handler) Handler {
			return Decisions(Chain(base, Retry()), &fakeProvider{name: "decider"}, DecisionOptions{})
		}, 0},
		{"Chain with nothing", func(base Handler) Handler { return Chain(base) }, 4},
	}
	for _, tc := range constructors {
		t.Run(tc.name, func(t *testing.T) {
			base := &senderProvider{fakeProvider{name: "openai-fake", maxRetries: 4, backoff: 2 * time.Second}}
			wrapped := tc.wrap(base)

			if wrapped.Name() != "openai-fake" {
				t.Errorf("Name() = %q, want %q", wrapped.Name(), "openai-fake")
			}
			if got := wrapped.EstimateCost(llm.CompletionRequest{}); got != 0.01 {
				t.Errorf("EstimateCost() = %v, want 0.01", got)
			}
			retries, backoff := wrapped.RetryPolicy()
			if retries != tc.retries || backoff != 2*time.Second {
				t.Errorf("RetryPolicy() = (%d, %v), want (%d, 2s)", retries, backoff, tc.retries)
			}
			if got := llm.ModelSentBy(wrapped, "m"); got != "sent-m" {
				t.Errorf("the model sent for %q is %q, want the wrapped provider's %q", "m", got, "sent-m")
			}
		})
	}

	t.Run("a wrapped provider that sends the model it was asked for", func(t *testing.T) {
		wrapped := Cache()(&fakeProvider{name: "plain"})
		if got := llm.ModelSentBy(wrapped, "m"); got != "m" {
			t.Errorf("the model sent is %q, want the requested %q", got, "m")
		}
	})
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

// The one test in this package that does not substitute the sleep hook: every
// other rate-limit test replaces bucket.sleep, which means realSleep -- the
// production implementation every timing-based middleware in this package
// actually ships with -- was never run by any of them. It runs here as it
// ships, on timers, inside a synctest bubble, so the timers read a fake clock
// that advances when every goroutine is blocked: the wait is exactly as long as
// it was asked to be, and its context-cancellation path ends exactly when the
// context does, whatever else the machine is doing.
func TestRealSleepActuallyWaitsAndHonoursContext(t *testing.T) {
	t.Run("waits out the duration", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			start := time.Now()
			err := realSleep(context.Background(), 20*time.Millisecond)
			if err != nil {
				t.Fatalf("realSleep: %v", err)
			}
			if elapsed := time.Since(start); elapsed != 20*time.Millisecond {
				t.Errorf("realSleep returned after %v, want 20ms", elapsed)
			}
		})
	})

	t.Run("returns immediately for a non-positive duration", func(t *testing.T) {
		if err := realSleep(context.Background(), 0); err != nil {
			t.Errorf("realSleep(0) = %v, want nil", err)
		}
		if err := realSleep(context.Background(), -time.Second); err != nil {
			t.Errorf("realSleep(negative) = %v, want nil", err)
		}
	})

	t.Run("reports an already-cancelled context before waiting", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := realSleep(ctx, time.Hour); err == nil {
			t.Error("realSleep against an already-cancelled context must fail immediately")
		}
	})

	t.Run("is cancellable mid-wait", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()
			start := time.Now()
			err := realSleep(ctx, time.Hour)
			if err == nil {
				t.Fatal("realSleep against a 10ms-deadline context waiting an hour must fail")
			}
			if elapsed := time.Since(start); elapsed != 10*time.Millisecond {
				t.Errorf("realSleep returned after %v, want it to end with the context's 10ms; the wait is not actually cancellable", elapsed)
			}
		})
	})
}

func TestKindIsTerminalAndRetryableAgreeWithOperationError(t *testing.T) {
	retryable := map[types.ErrorKind]bool{
		types.KindRateLimited: true, types.KindProviderUnavailable: true,
		types.KindTimeout: true, types.KindCircuitOpen: true,
	}
	terminal := map[types.ErrorKind]bool{
		types.KindConfiguration: true, types.KindAuthentication: true, types.KindPermission: true,
		types.KindInvalidRequest: true, types.KindPolicyViolation: true, types.KindBudgetExceeded: true,
		types.KindUnsupportedCapability: true, types.KindCanceled: true, types.KindShutdown: true,
		types.KindRepairExhausted: true, types.KindReviewRequired: true,
	}
	for kind := types.KindUnknown; kind <= types.KindReviewRequired; kind++ {
		t.Run(kind.String(), func(t *testing.T) {
			if got := kindIsRetryable(kind); got != retryable[kind] {
				t.Errorf("kindIsRetryable = %v, want %v", got, retryable[kind])
			}
			if got := kindIsTerminal(kind); got != terminal[kind] {
				t.Errorf("kindIsTerminal = %v, want %v", got, terminal[kind])
			}
		})
	}
}

// toolRequestProbe is a provider that keeps every request it is asked to complete
// and answers with a fixed reply, after the failures it is told to return, so a
// test can compare what a middleware handed on with what it was given.
type toolRequestProbe struct {
	name     string
	reply    llm.CompletionResponse
	failures []error

	mu    sync.Mutex
	seen  []llm.CompletionRequest
	calls int
}

func (p *toolRequestProbe) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.mu.Lock()
	n := p.calls
	p.calls++
	p.seen = append(p.seen, req)
	p.mu.Unlock()
	if n < len(p.failures) && p.failures[n] != nil {
		return llm.CompletionResponse{}, p.failures[n]
	}
	return p.reply, nil
}

func (p *toolRequestProbe) Name() string                                   { return p.name }
func (p *toolRequestProbe) EstimateCost(req llm.CompletionRequest) float64 { return 0.001 }
func (p *toolRequestProbe) RetryPolicy() (int, time.Duration)              { return 0, 0 }

func (p *toolRequestProbe) requests() []llm.CompletionRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]llm.CompletionRequest(nil), p.seen...)
}

// A request's tools, tool choice and messages, and a response's tool calls, go
// through every middleware as they came: the same request to the provider on
// every attempt, and the same calls back, with no content beside them.
func TestMiddlewaresPassToolsMessagesAndToolCallsThrough(t *testing.T) {
	down := llm.NewAPIError("primary", "m", 503, "")
	noSleep := func(context.Context, time.Duration) error { return nil }

	cases := []struct {
		name     string
		failures []error // the primary probe's, before it answers
		build    func(primary, alternate *toolRequestProbe) Handler
		attempts int // requests the primary probe is expected to see
		failover int // requests the alternate is expected to see
	}{
		{"retry, one attempt", nil, func(p, _ *toolRequestProbe) Handler {
			return Retry(WithMaxAttempts(3), WithBaseDelay(time.Millisecond))(p)
		}, 1, 0},
		{"retry, after two failures", []error{down, down}, func(p, _ *toolRequestProbe) Handler {
			retried := Retry(WithMaxAttempts(3), WithBaseDelay(time.Millisecond))(p)
			retried.(*retryProvider).sleep = noSleep
			return retried
		}, 3, 0},
		{"rate limit", nil, func(p, _ *toolRequestProbe) Handler { return RateLimit(10, time.Second)(p) }, 1, 0},
		{"rate limit that rejects", nil, func(p, _ *toolRequestProbe) Handler { return RateLimit(10, time.Second, Reject())(p) }, 1, 0},
		{"bulkhead", nil, func(p, _ *toolRequestProbe) Handler { return Bulkhead(2)(p) }, 1, 0},
		{"bulkhead that rejects when full", nil, func(p, _ *toolRequestProbe) Handler { return Bulkhead(2, RejectWhenFull())(p) }, 1, 0},
		{"circuit breaker", nil, func(p, _ *toolRequestProbe) Handler {
			return CircuitBreaker(ops.CircuitBreakerConfig{FailureThreshold: 5})(p)
		}, 1, 0},
		{"circuit breaker keyed by model", nil, func(p, _ *toolRequestProbe) Handler {
			return CircuitBreaker(ops.CircuitBreakerConfig{FailureThreshold: 5}, KeyByModel())(p)
		}, 1, 0},
		{"budget", nil, func(p, _ *toolRequestProbe) Handler { return Budget(1)(p) }, 1, 0},
		{"budget with no cost function", nil, func(p, _ *toolRequestProbe) Handler { return Budget(1, WithBudgetCostFunc(nil))(p) }, 1, 0},
		{"fallback, the primary answers", nil, func(p, alt *toolRequestProbe) Handler {
			return Fallback([]FallbackRoute{{Handler: alt}})(p)
		}, 1, 0},
		{"fallback, the primary fails", []error{down}, func(p, alt *toolRequestProbe) Handler {
			return Fallback([]FallbackRoute{{Handler: alt}})(p)
		}, 1, 1},
		{"fallback, the primary cannot take tools", []error{unsupportedErr("primary")}, func(p, alt *toolRequestProbe) Handler {
			return Fallback([]FallbackRoute{{Handler: alt}})(p)
		}, 1, 1},
		{"hedge that does not need its second copy", nil, func(p, _ *toolRequestProbe) Handler { return Hedge(time.Hour)(p) }, 1, 0},
		{"hedge with the delay off", nil, func(p, _ *toolRequestProbe) Handler { return Hedge(0)(p) }, 1, 0},
		{"cache, a miss", nil, func(p, _ *toolRequestProbe) Handler { return Cache()(p) }, 1, 0},
		{"redaction with nothing to redact", nil, func(p, _ *toolRequestProbe) Handler { return RedactEgress()(p) }, 1, 0},
		{"the pair, the request goes to the generator", nil, func(p, _ *toolRequestProbe) Handler {
			return Decisions(p, &decisionProbe{name: "jev", reply: probeReply()}, DecisionOptions{})
		}, 1, 0},
		{"a chain of all of them", nil, func(p, _ *toolRequestProbe) Handler {
			return Chain(p, RedactEgress(), Cache(), Retry(WithMaxAttempts(2)), Budget(1), RateLimit(10, time.Second), Bulkhead(2),
				CircuitBreaker(ops.CircuitBreakerConfig{FailureThreshold: 5}), Hedge(time.Hour))
		}, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reply := llm.CompletionResponse{
				Provider: "primary", Model: "m", FinishReason: "tool_calls",
				ToolCalls: []llm.ToolCall{
					{ID: "call_2", Name: "get_weather", Arguments: []byte(`{"city":"Oslo"}`)},
					{ID: "call_3", Name: "get_time", Arguments: []byte(`{}`)},
				},
				Usage: types.TokenUsage{PromptTokens: 30, CompletionTokens: 4, TotalTokens: 34},
			}
			primary := &toolRequestProbe{name: "primary", failures: tc.failures, reply: reply}
			alternate := &toolRequestProbe{name: "alt", reply: reply}
			req := toolsRequest()
			req.ToolChoice = "auto"
			want := toolsRequest()
			want.ToolChoice = "auto"

			resp, err := tc.build(primary, alternate).Complete(context.Background(), req)
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if resp.Content != "" || resp.FinishReason != "tool_calls" || !reflect.DeepEqual(resp.ToolCalls, reply.ToolCalls) {
				t.Errorf("the reply came back changed: %+v", resp)
			}
			for name, probe := range map[string]*toolRequestProbe{"primary": primary, "alternate": alternate} {
				expected := tc.attempts
				if probe == alternate {
					expected = tc.failover
				}
				seen := probe.requests()
				if len(seen) != expected {
					t.Fatalf("the %s saw %d requests, want %d", name, len(seen), expected)
				}
				for i, got := range seen {
					if !reflect.DeepEqual(got.Tools, want.Tools) || !reflect.DeepEqual(got.Messages, want.Messages) || got.ToolChoice != want.ToolChoice {
						t.Errorf("the %s was sent something else on attempt %d:\n got tools %+v messages %+v choice %q\nwant tools %+v messages %+v choice %q",
							name, i+1, got.Tools, got.Messages, got.ToolChoice, want.Tools, want.Messages, want.ToolChoice)
					}
				}
			}
			if !reflect.DeepEqual(req, want) {
				t.Errorf("the caller's request was changed: %+v", req)
			}
		})
	}
}

// A panic recovered in code of the caller's (a cost function, a store, a callback,
// a redaction function) is written to the logger of the client the call was made
// with and not to the process's, and the line holds the type of what was thrown
// and not its value, which can hold the text the code was handed.
func TestARecoveredPanicIsLoggedToTheClientsLoggerWithItsTypeOnly(t *testing.T) {
	const secret = "the text the code was handed"
	thrower := func() { panic(secret) }
	cases := []struct {
		name string
		run  func(ctx context.Context)
		line string
	}{
		{"a cost function", func(ctx context.Context) {
			Budget(10, WithBudgetCostFunc(func(*types.TokenUsage, string, string) *types.CostInfo { thrower(); return nil }))(
				&budgetFakeProvider{resp: llm.CompletionResponse{Content: "ok", Usage: types.TokenUsage{PromptTokens: 1}}}).Complete(ctx, llm.CompletionRequest{})
		}, "A budget cost function panicked"},
		{"a cost function asked for the estimate", func(ctx context.Context) {
			Budget(10, WithBudgetRequestEstimate(), WithBudgetCostFunc(func(*types.TokenUsage, string, string) *types.CostInfo { thrower(); return nil }))(
				&budgetFakeProvider{resp: llm.CompletionResponse{Content: "ok"}}).Complete(ctx, llm.CompletionRequest{})
		}, "A budget cost function panicked"},
		{"a store that panics reading", func(ctx context.Context) {
			Cache(WithCacheStore(&panickingStore{onGet: true}))(&fakeProvider{responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "ok"}}}}).Complete(ctx, llm.CompletionRequest{Model: "m"})
		}, "A cache store panicked reading"},
		{"a store that panics writing", func(ctx context.Context) {
			Cache(WithCacheStore(&panickingStore{onSet: true}))(&fakeProvider{responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "ok"}}}}).Complete(ctx, llm.CompletionRequest{Model: "m"})
		}, "A cache store panicked writing"},
		{"a cache callback", func(ctx context.Context) {
			Cache(WithCacheStats(func(CacheEvent) { thrower() }))(&fakeProvider{responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "ok"}}}}).Complete(ctx, llm.CompletionRequest{Model: "m"})
		}, "A cache stats callback panicked"},
		{"a redaction function", func(ctx context.Context) {
			RedactEgress(WithFunc(func(string) string { thrower(); return "" }))(&fakeProvider{}).Complete(ctx, llm.CompletionRequest{UserPrompt: "x"})
		}, "A redaction function panicked"},
		{"a hedged request", func(ctx context.Context) {
			Hedge(time.Hour)(completeFunc(func(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
				thrower()
				return llm.CompletionResponse{}, nil
			})).Complete(ctx, llm.CompletionRequest{})
		}, "A hedged request panicked"},
		{"a hedge observer", func(ctx context.Context) {
			Hedge(time.Hour, WithHedgeObserver(func(HedgeEvent) { thrower() }))(&fakeProvider{responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "ok"}}}}).Complete(ctx, llm.CompletionRequest{})
		}, "A hedge observer panicked"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var process, client bytes.Buffer
			previous := logger.GetLogger()
			logger.SetLogger(telemetry.FromSlog(slog.New(slog.NewTextHandler(&process, nil))))
			t.Cleanup(func() { logger.SetLogger(previous) })
			clients := telemetry.FromSlog(slog.New(slog.NewTextHandler(&client, nil)))
			tc.run(ops.WithExecConfig(context.Background(), &ops.ExecConfig{Logger: clients}))

			if !strings.Contains(client.String(), tc.line) || !strings.Contains(client.String(), "thrown=string") {
				t.Errorf("the client's logger holds %q, want %q and the type", client.String(), tc.line)
			}
			if process.Len() != 0 {
				t.Errorf("the process logger was written to: %q", process.String())
			}
			if strings.Contains(client.String(), secret) || strings.Contains(client.String(), "goroutine") {
				t.Errorf("the line holds more than the type: %q", client.String())
			}
		})
	}
}
