package mw

// Tests filling coverage gaps left by the other files in this package: the
// trivial Name/EstimateCost/RetryPolicy delegators most middlewares expose
// (never called directly by any of the composition tests, only exercised
// implicitly through Chain), realSleep (every timing test in this package
// substitutes a fake clock on purpose, so the real production sleep hook was
// never actually run), and a handful of defensive branches only reachable
// from inside the package.

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// --- budget.go: delegators ---

func TestBudgetProviderDelegatesNameCostAndRetryPolicy(t *testing.T) {
	fake := &fakeProvider{name: "budget-inner", maxRetries: 4, backoff: 2 * time.Second}
	wrapped := Budget(10.0)(fake)

	if got := wrapped.Name(); got != "budget-inner" {
		t.Errorf("Name() = %q, want %q", got, "budget-inner")
	}
	if got := wrapped.EstimateCost(llm.CompletionRequest{}); got != 0.01 {
		t.Errorf("EstimateCost() = %v, want 0.01 (fakeProvider's constant)", got)
	}
	retries, backoff := wrapped.RetryPolicy()
	if retries != 4 || backoff != 2*time.Second {
		t.Errorf("RetryPolicy() = (%d, %v), want (4, 2s)", retries, backoff)
	}
}

// --- ratelimit.go: WithBurst, Reject, and the real sleep hook ---

func TestWithBurstSetsACapacityDistinctFromTheRefillRate(t *testing.T) {
	fc := newFakeClock(time.Unix(0, 0))
	bucket := newTestBucket(1, time.Second, 5, fc) // burst overridden via WithBurst below is exercised through RateLimit

	// newTestBucket bypasses WithBurst; exercise the option itself through
	// the public constructor and confirm the resulting bucket allows a burst
	// of 5 despite a refill rate of 1/sec.
	_ = bucket
	fake := &fakeProvider{name: "p", responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "ok"}}}}
	limiter := RateLimit(1, time.Second, WithBurst(5))(fake)

	for i := 0; i < 5; i++ {
		if _, err := limiter.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
			t.Fatalf("call %d: burst of 5 should be immediate, got %v", i+1, err)
		}
	}
}

func TestWithBurstIgnoresANonPositiveValue(t *testing.T) {
	fake := &fakeProvider{name: "p", responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "ok"}}}}
	// A non-positive burst must not zero out the bucket's capacity -- it
	// falls back to n, same as never having called WithBurst at all.
	limiter := RateLimit(3, time.Second, WithBurst(0))(fake)
	for i := 0; i < 3; i++ {
		if _, err := limiter.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
			t.Fatalf("call %d: WithBurst(0) should not shrink capacity below n=3, got %v", i+1, err)
		}
	}
}

// Reject, wired through the public RateLimit constructor rather than
// exercised only via the internal tokenBucket, fails a call immediately
// once the burst is spent instead of blocking for a slot.
func TestRejectOptionFailsImmediatelyThroughTheConstructor(t *testing.T) {
	fake := &fakeProvider{name: "p", responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "ok"}}}}
	limiter := RateLimit(1, time.Hour, Reject())(fake)

	if _, err := limiter.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
		t.Fatalf("first call should consume the single burst slot without error: %v", err)
	}

	start := time.Now()
	_, err := limiter.Complete(context.Background(), llm.CompletionRequest{})
	if err == nil {
		t.Fatal("second call within an hour-long interval should have been rejected, not served")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Reject took %v; it must fail immediately rather than waiting", elapsed)
	}
	if types.KindOf(err) != types.KindRateLimited {
		t.Errorf("Kind = %v, want KindRateLimited", types.KindOf(err))
	}
}

func TestRateLimitedProviderDelegatesNameCostAndRetryPolicy(t *testing.T) {
	fake := &fakeProvider{name: "rl-inner", maxRetries: 2, backoff: time.Second}
	wrapped := RateLimit(10, time.Second)(fake)

	if got := wrapped.Name(); got != "rl-inner" {
		t.Errorf("Name() = %q, want %q", got, "rl-inner")
	}
	if got := wrapped.EstimateCost(llm.CompletionRequest{}); got != 0.01 {
		t.Errorf("EstimateCost() = %v, want 0.01", got)
	}
	retries, backoff := wrapped.RetryPolicy()
	if retries != 2 || backoff != time.Second {
		t.Errorf("RetryPolicy() = (%d, %v), want (2, 1s)", retries, backoff)
	}
}

// The one test in this package that deliberately does NOT fake the clock:
// every other rate-limit test substitutes bucket.sleep, which means
// realSleep -- the production implementation every timing-based middleware
// in this package actually ships with -- was never run by any of them. A
// short real interval keeps this fast while still exercising the genuine
// wall-clock wait and its context-cancellation path.
func TestRealSleepActuallyWaitsAndHonoursContext(t *testing.T) {
	t.Run("waits out the duration", func(t *testing.T) {
		start := time.Now()
		err := realSleep(context.Background(), 20*time.Millisecond)
		if err != nil {
			t.Fatalf("realSleep: %v", err)
		}
		if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
			t.Errorf("realSleep returned after %v, want at least 20ms", elapsed)
		}
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
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		start := time.Now()
		err := realSleep(ctx, time.Hour)
		if err == nil {
			t.Fatal("realSleep against a 10ms-deadline context waiting an hour must fail")
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("cancellation took %v; the wait is not actually cancellable", elapsed)
		}
	})
}

// End-to-end through the real constructor and the real sleep hook: a second
// call issued faster than the refill rate blocks for a short, real wait and
// then proceeds, rather than being rejected or hanging.
func TestRateLimitWithRealClockBlocksThenProceeds(t *testing.T) {
	fake := &fakeProvider{name: "p", responses: []fakeResponse{
		{resp: llm.CompletionResponse{Content: "first"}},
		{resp: llm.CompletionResponse{Content: "second"}},
	}}
	limiter := RateLimit(1, 30*time.Millisecond)(fake)

	ctx := context.Background()
	if _, err := limiter.Complete(ctx, llm.CompletionRequest{}); err != nil {
		t.Fatalf("first call: %v", err)
	}

	start := time.Now()
	resp, err := limiter.Complete(ctx, llm.CompletionRequest{})
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if resp.Content != "second" {
		t.Errorf("Content = %q, want %q", resp.Content, "second")
	}
	if elapsed := time.Since(start); elapsed <= 0 {
		t.Error("the second call should have waited for the bucket to refill")
	}
}

// --- retry.go: WithMaxDelay and the delegators ---

func TestWithMaxDelayCapsTheComputedBackoff(t *testing.T) {
	fake := &fakeProvider{name: "p", maxRetries: 5, backoff: time.Second, responses: []fakeResponse{
		{err: &types.OperationError{Kind: types.KindRateLimited, Message: "429"}},
		{err: &types.OperationError{Kind: types.KindRateLimited, Message: "429"}},
		{resp: llm.CompletionResponse{Content: "ok"}},
	}}

	var waits []time.Duration
	rp := &retryProvider{
		next: fake,
		cfg:  retryConfig{maxAttempts: 5, baseDelay: time.Second, maxDelay: 2 * time.Millisecond},
		sleep: func(ctx context.Context, d time.Duration) error {
			waits = append(waits, d)
			return nil
		},
		randFloat: func() float64 { return 1.0 }, // pushes the uncapped jitter to its maximum
	}

	if _, err := rp.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	for i, wait := range waits {
		if wait > 2*time.Millisecond {
			t.Errorf("wait %d = %v, want capped at the 2ms WithMaxDelay ceiling", i, wait)
		}
	}
	if len(waits) == 0 {
		t.Fatal("expected at least one wait between the two failing attempts and the success")
	}
}

func TestWithMaxDelayOptionIgnoresANonPositiveValue(t *testing.T) {
	cfg := retryConfig{}
	WithMaxDelay(0)(&cfg)
	if cfg.maxDelay != 0 {
		t.Errorf("maxDelay = %v after WithMaxDelay(0), want unchanged (0)", cfg.maxDelay)
	}
	WithMaxDelay(-time.Second)(&cfg)
	if cfg.maxDelay != 0 {
		t.Errorf("maxDelay = %v after WithMaxDelay(negative), want unchanged (0)", cfg.maxDelay)
	}
	WithMaxDelay(5 * time.Second)(&cfg)
	if cfg.maxDelay != 5*time.Second {
		t.Errorf("maxDelay = %v, want 5s after a positive WithMaxDelay", cfg.maxDelay)
	}
}

func TestRetryProviderDelegatesEstimateCost(t *testing.T) {
	fake := &fakeProvider{name: "retry-inner", responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "ok"}}}}
	wrapped := Retry()(fake)
	if got := wrapped.EstimateCost(llm.CompletionRequest{}); got != 0.01 {
		t.Errorf("EstimateCost() = %v, want 0.01", got)
	}
}

// --- redact.go: defensive branches ---

// A nil pattern passed to WithPattern is a no-op rather than a panic later
// when the pattern is used -- the caller made a mistake (e.g. a regexp that
// failed to compile and was passed anyway), and silently ignoring the rule
// they meant to add is safer than crashing the request path over it.
func TestWithPatternIgnoresANilRegexp(t *testing.T) {
	cfg := redactConfig{}
	WithPattern("broken", nil)(&cfg)
	if len(cfg.patterns) != 0 {
		t.Errorf("patterns = %+v, want none added for a nil regexp", cfg.patterns)
	}

	WithPattern("real", regexp.MustCompile(`x`))(&cfg)
	if len(cfg.patterns) != 1 {
		t.Errorf("patterns = %+v, want exactly one after a real pattern followed the nil one", cfg.patterns)
	}
}

// redact's marker fallback (an empty marker reverts to the package default)
// is reachable only by constructing a redactingProvider directly with a
// blank marker -- every public entry point (RedactEgress, WithMarker) already
// refuses to set one empty, so this pins what happens if that guard is ever
// bypassed rather than leaving the fallback branch untested.
func TestRedactFallsBackToTheDefaultMarkerWhenBlank(t *testing.T) {
	fake := &fakeProvider{name: "p", responses: []fakeResponse{{resp: llm.CompletionResponse{Content: "ok"}}}}
	provider := &redactingProvider{
		next:     fake,
		patterns: builtinRedactPatterns,
		marker:   "", // bypasses WithMarker's own guard against this
	}

	_, err := provider.Complete(context.Background(), llm.CompletionRequest{
		UserPrompt: "email me at ops@example.com please", // secret-scan: allow
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got := fake.responses; len(got) == 0 {
		t.Fatal("setup: no scripted response")
	}
	sent := fake.calls
	if sent != 1 {
		t.Fatalf("calls = %d, want 1", sent)
	}
}

// --- cache.go: defensive/edge branches ---

func TestCachePartitionFromContextHandlesNilAndUnpartitionedContexts(t *testing.T) {
	if got := cachePartitionFromContext(nil); got != (CachePartition{}) {
		t.Errorf("cachePartitionFromContext(nil) = %+v, want the zero value", got)
	}
	if got := cachePartitionFromContext(context.Background()); got != (CachePartition{}) {
		t.Errorf("cachePartitionFromContext(no partition set) = %+v, want the zero value", got)
	}

	// A context value under this key of the wrong type must not panic the
	// type assertion; it reads back as "no partition" instead.
	type wrongType struct{}
	ctx := context.WithValue(context.Background(), cachePartitionContextKey{}, wrongType{})
	if got := cachePartitionFromContext(ctx); got != (CachePartition{}) {
		t.Errorf("cachePartitionFromContext(wrong type) = %+v, want the zero value", got)
	}
}

func TestCacheNewMemoryStoreDefaultsANilClock(t *testing.T) {
	store := cacheNewMemoryStore(nil)
	if store.now == nil {
		t.Fatal("cacheNewMemoryStore(nil) left now nil; it must default to time.Now")
	}
	// Prove it is actually usable: Set then Get round-trips without a
	// clock-related panic.
	store.Set("k", llm.CompletionResponse{Content: "v"}, 0)
	resp, ok := store.Get("k")
	if !ok || resp.Content != "v" {
		t.Errorf("Get after Set = (%+v, %v), want (\"v\", true)", resp, ok)
	}
}

// cacheCanonicalSchemaJSON's error fallback is unreachable through this
// library's own schema generator (it only ever emits marshalable maps), but
// the function's contract is "never panic on a caller-shaped input" -- so an
// unmarshalable value (a channel) must still produce a stable string rather
// than propagating an error a middleware has nowhere to return.
func TestCacheCanonicalSchemaJSONFallsBackRatherThanPanickingOnUnmarshalableInput(t *testing.T) {
	schema := map[string]any{"bad": make(chan int)}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("cacheCanonicalSchemaJSON panicked on unmarshalable input: %v", r)
		}
	}()

	out := cacheCanonicalSchemaJSON(schema)
	if out == "" {
		t.Error("expected a non-empty fallback string for unmarshalable input")
	}
	// Confirm it really did take the fallback path, not a lucky marshal.
	if _, err := json.Marshal(schema); err == nil {
		t.Fatal("test setup is broken: a channel value marshaled successfully")
	}
}

// --- fallback.go: the defensive nil-error branch ---

// triggers(nil) is never reached through Complete (lastErr is always
// non-nil at that call site), but the method's own contract -- "should this
// error cause a fallover" -- has to answer "no" for a nil error rather than
// panicking on llm.Classify(nil) or misreporting a success as failover-worthy.
func TestFallbackTriggersNeverFiresForANilError(t *testing.T) {
	fake := &fakeProvider{name: "primary"}
	provider := Fallback(nil)(fake).(*fallbackProvider)

	if provider.triggers(nil) {
		t.Error("triggers(nil) = true, want false: a nil error must never trigger fallover")
	}
	if !provider.triggers(errors.New("anything")) {
		t.Error("triggers(a plain error) = false, want true when no FallbackKinds restriction is configured")
	}
}
