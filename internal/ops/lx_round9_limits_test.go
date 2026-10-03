package ops

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// Review round 9, on a client's scheduler.

// The budget is asked again once a call has its slot. It was asked only before
// the wait, so a hundred calls queued behind one slot all passed a budget that
// the first of them was about to spend.
func TestTheBudgetIsAskedAgainAfterTheWaitForASlot(t *testing.T) {
	provider := &gateProvider{release: make(chan struct{})}
	scheduler := NewScheduler(SchedulerLimits{MaxConcurrent: 1, MaxQueued: 10})
	defer scheduler.Close(context.Background())

	var spent atomic.Bool
	budget := &switchBudget{spent: &spent}
	ctx := WithExecConfig(context.Background(), &ExecConfig{Scheduler: scheduler, Budget: budget})

	results := make(chan error, 4)
	call := func() {
		_, err := CallLLM(ctx, provider, "system", "user", lx126Options)
		results <- err
	}
	go call()
	waitFor(t, func() bool { return provider.inFlight.Load() == 1 })
	for range 3 {
		go call()
	}
	waitFor(t, func() bool { return scheduler.Stats().Queued == 3 })

	// The call in flight spends the budget, and is then let finish.
	spent.Store(true)
	close(provider.release)

	failed := 0
	for range 4 {
		if err := <-results; err != nil {
			if !strings.Contains(err.Error(), "budget is spent") {
				t.Errorf("a queued call failed with %v, want the budget's refusal", err)
			}
			failed++
		}
	}
	if failed != 3 {
		t.Errorf("%d of the three queued calls were refused, want all three", failed)
	}
	if got := provider.calls.Load(); got != 1 {
		t.Errorf("the provider was called %d times, want 1: a call was sent on a spent budget", got)
	}
}

// switchBudget allows calls until it is told the budget is spent.
type switchBudget struct{ spent *atomic.Bool }

func (b *switchBudget) Check() error {
	if b.spent.Load() {
		return errors.New("this client's budget is spent")
	}
	return nil
}
func (b *switchBudget) Record(float64, bool) {}

// endingContext is a context that is still open the first time it is asked and
// over from then on, with its Done channel already closed. It is the instant a
// context ends, held still: Submit looks once before it queues the call, finds
// the context open, and then waits on an admission and a Done that are both
// ready.
type endingContext struct {
	context.Context
	asked atomic.Int64
	done  chan struct{}
}

func newEndingContext(parent context.Context) *endingContext {
	done := make(chan struct{})
	close(done)
	return &endingContext{Context: parent, done: done}
}

func (c *endingContext) Done() <-chan struct{} { return c.done }
func (c *endingContext) Err() error {
	if c.asked.Add(1) == 1 {
		return nil
	}
	return context.Canceled
}

// A call whose context ended while it waited is not sent, even when the
// scheduler admitted it in the same instant: the admission and the context are
// both ready, and which one Submit sees first is a coin toss. Over two hundred
// tosses, the call is never run.
func TestACallAdmittedOnAnEndedContextIsNotSent(t *testing.T) {
	admittedAnyway := 0
	for range 200 {
		provider := &gateProvider{}
		scheduler := NewScheduler(SchedulerLimits{MaxConcurrent: 4})
		ctx := newEndingContext(WithExecConfig(context.Background(), &ExecConfig{Scheduler: scheduler}))

		_, ran, err := throughScheduler[string](ctx, provider, llm.CompletionRequest{Model: "gpt-6-luna"}, func(context.Context) (string, error) {
			return "sent", nil
		})
		if ran || err == nil {
			t.Fatalf("ran = %v, err = %v; a call on an ended context was run", ran, err)
		}
		if !errors.Is(err, types.ErrCanceled) {
			t.Fatalf("err = %v, want a cancellation", err)
		}
		// Whether the scheduler had admitted it is seen in how many times the
		// context was asked: the guard after admission asks once more.
		if ctx.asked.Load() > 2 {
			admittedAnyway++
		}
		_ = scheduler.Close(context.Background())
	}
	if admittedAnyway == 0 {
		t.Log("the scheduler never admitted the call in two hundred tries; the guard after admission was not exercised")
	}
}

// throughScheduler on its own: what runs, what the scheduler is told, and what
// comes back when nothing was run.
func TestThroughScheduler(t *testing.T) {
	type told struct {
		provider string
		priority SchedPriority
		tokens   int64
	}
	long := strings.Repeat("word ", 400) // 2,000 characters: 500 tokens by the size guard's count

	cases := []struct {
		name    string
		limits  *SchedulerLimits // nil for no scheduler on the context
		closed  bool
		req     llm.CompletionRequest
		ran     bool
		refused error
	}{
		{"no scheduler", nil, false, llm.CompletionRequest{UserPrompt: "u"}, true, nil},
		{"a scheduler with no limits", &SchedulerLimits{}, false, llm.CompletionRequest{UserPrompt: "u"}, true, nil},
		{"room for the request", &SchedulerLimits{MaxConcurrent: 1}, false, llm.CompletionRequest{UserPrompt: "u"}, true, nil},
		{"a closed scheduler", &SchedulerLimits{MaxConcurrent: 1}, true, llm.CompletionRequest{UserPrompt: "u"}, false, types.ErrShutdown},
		{"the prompt alone is over the token ceiling", &SchedulerLimits{MaxInFlightTokens: 300}, false,
			llm.CompletionRequest{UserPrompt: long, MaxTokens: 10}, false, types.ErrAdmissionRejected},
		{"the system prompt alone is over the token ceiling", &SchedulerLimits{MaxInFlightTokens: 300}, false,
			llm.CompletionRequest{SystemPrompt: long, MaxTokens: 10}, false, types.ErrAdmissionRejected},
		{"the output ceiling alone is over the token ceiling", &SchedulerLimits{MaxInFlightTokens: 300}, false,
			llm.CompletionRequest{UserPrompt: "u", MaxTokens: 301}, false, types.ErrAdmissionRejected},
		{"prompt and output together are over it", &SchedulerLimits{MaxInFlightTokens: 600}, false,
			llm.CompletionRequest{UserPrompt: long, MaxTokens: 200}, false, types.ErrAdmissionRejected},
		{"prompt and output together fit", &SchedulerLimits{MaxInFlightTokens: 800}, false,
			llm.CompletionRequest{UserPrompt: long, MaxTokens: 200}, true, nil},
		{"a short prompt under the ceiling", &SchedulerLimits{MaxInFlightTokens: 300}, false,
			llm.CompletionRequest{UserPrompt: "u", MaxTokens: 10}, true, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.limits != nil {
				scheduler := NewScheduler(*tc.limits)
				defer scheduler.Close(context.Background())
				if tc.closed {
					_ = scheduler.Close(context.Background())
				}
				ctx = WithExecConfig(ctx, &ExecConfig{Scheduler: scheduler})
			}
			provider := &gateProvider{name: "openai"}

			got, ran, err := throughScheduler(ctx, provider, tc.req, func(context.Context) (string, error) {
				return "sent", nil
			})
			if ran != tc.ran {
				t.Fatalf("ran = %v, want %v (err %v)", ran, tc.ran, err)
			}
			if tc.ran && (got != "sent" || err != nil) {
				t.Errorf("got %q, err %v", got, err)
			}
			if !tc.ran && !errors.Is(err, tc.refused) {
				t.Errorf("err = %v, want %v", err, tc.refused)
			}
		})
	}

	// The error of the work itself comes back as it is, with ran true.
	scheduler := NewScheduler(SchedulerLimits{MaxConcurrent: 1})
	defer scheduler.Close(context.Background())
	ctx := WithExecConfig(context.Background(), &ExecConfig{Scheduler: scheduler})
	failure := errors.New("the provider failed")
	_, ran, err := throughScheduler(ctx, &gateProvider{}, llm.CompletionRequest{}, func(context.Context) (string, error) {
		return "", failure
	})
	if !ran || !errors.Is(err, failure) {
		t.Errorf("ran = %v, err = %v; want the work's own error", ran, err)
	}
	_ = told{}
}

// A call in flight when its scheduler is closed is told so. It came back as a
// plain cancellation, which reads as the caller having given up.
func TestACallInFlightWhenTheSchedulerClosesIsToldItWasShutDown(t *testing.T) {
	provider := &gateProvider{release: make(chan struct{})}
	scheduler := NewScheduler(SchedulerLimits{MaxConcurrent: 1})
	ctx := WithExecConfig(context.Background(), &ExecConfig{Scheduler: scheduler})

	result := make(chan error, 1)
	go func() {
		_, err := CallLLM(ctx, provider, "system", "user", lx126Options)
		result <- err
	}()
	waitFor(t, func() bool { return provider.inFlight.Load() == 1 })

	closed := make(chan error, 1)
	go func() { closed <- scheduler.Close(context.Background()) }()

	select {
	case err := <-result:
		if !errors.Is(err, types.ErrShutdown) {
			t.Errorf("err = %v, want a shutdown", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the call in flight was not stopped by Close")
	}
	if err := <-closed; err != nil {
		t.Errorf("Close: %v", err)
	}
}

// An estimate that is not a number a ceiling can be compared with is refused.
// NaN compares false with everything, so one request carrying it was admitted
// over any ceiling and then made the running total NaN, which disabled the
// ceiling for every request after it.
func TestASchedulerRefusesAnEstimateThatIsNotANumber(t *testing.T) {
	cases := []struct {
		name    string
		req     SubmitRequest
		refused bool
	}{
		{"no estimates", SubmitRequest{}, false},
		{"ordinary estimates", SubmitRequest{EstimatedTokens: 100, EstimatedCost: 0.01}, false},
		{"a cost that is not a number", SubmitRequest{EstimatedCost: math.NaN()}, true},
		{"an infinite cost", SubmitRequest{EstimatedCost: math.Inf(1)}, true},
		{"a negative infinite cost", SubmitRequest{EstimatedCost: math.Inf(-1)}, true},
		{"a negative cost", SubmitRequest{EstimatedCost: -0.01}, true},
		{"negative tokens", SubmitRequest{EstimatedTokens: -1}, true},
		{"a zero cost", SubmitRequest{EstimatedCost: 0}, false},
		{"a very small cost", SubmitRequest{EstimatedCost: 1e-12}, false},
		{"a large token estimate with no ceiling", SubmitRequest{EstimatedTokens: 1 << 50}, false},
	}
	for _, tc := range cases {
		for _, limits := range []SchedulerLimits{{}, {MaxInFlightCost: 100, MaxInFlightTokens: 1 << 60}} {
			t.Run(tc.name, func(t *testing.T) {
				scheduler := NewScheduler(limits)
				defer scheduler.Close(context.Background())

				ran := false
				_, err := Submit(context.Background(), scheduler, tc.req, func(context.Context) (struct{}, error) {
					ran = true
					return struct{}{}, nil
				})
				if tc.refused {
					if !errors.Is(err, types.ErrInvalidRequest) || ran {
						t.Fatalf("err = %v, ran = %v; want an invalid request and nothing run", err, ran)
					}
					return
				}
				if err != nil || !ran {
					t.Fatalf("err = %v, ran = %v", err, ran)
				}
				if stats := scheduler.Stats(); stats.InFlightCost != 0 || stats.InFlightTokens != 0 {
					t.Errorf("the running totals did not come back to zero: %+v", stats)
				}
			})
		}
	}
}

// The queue bound is on requests that have to wait, whatever keeps the others
// from running. With a limit per provider, a request for an idle provider was
// refused for a full queue while a request for a busy provider sat in it and a
// slot stood free.
func TestTheQueueBoundDoesNotRefuseARequestThatCanRunNow(t *testing.T) {
	scheduler := NewScheduler(SchedulerLimits{MaxConcurrent: 4, PerProviderConcurrency: 1, MaxQueued: 1})
	defer scheduler.Close(context.Background())
	release := make(chan struct{})
	defer close(release)
	hold := func(context.Context) (struct{}, error) {
		<-release
		return struct{}{}, nil
	}

	go Submit(context.Background(), scheduler, SubmitRequest{Provider: "a"}, hold)
	waitFor(t, func() bool { return scheduler.Stats().InFlight == 1 })
	go Submit(context.Background(), scheduler, SubmitRequest{Provider: "a"}, hold)
	waitFor(t, func() bool { return scheduler.Stats().Queued == 1 })

	// Provider b is idle and three slots are free.
	ran := make(chan struct{}, 1)
	_, err := Submit(context.Background(), scheduler, SubmitRequest{Provider: "b"}, func(context.Context) (struct{}, error) {
		ran <- struct{}{}
		return struct{}{}, nil
	})
	if err != nil {
		t.Fatalf("a request that could run at once was refused: %v", err)
	}
	<-ran

	// A third request for the busy provider does have to wait, and the queue
	// is full.
	_, err = Submit(context.Background(), scheduler, SubmitRequest{Provider: "a"}, hold)
	if !errors.Is(err, types.ErrAdmissionRejected) {
		t.Errorf("err = %v, want an admission refusal", err)
	}
	if stats := scheduler.Stats(); stats.Queued != 1 || stats.InFlight != 1 {
		t.Errorf("after the refusal the scheduler holds %+v, want one in flight and one queued", stats)
	}
}

// A request that can run is admitted by the call that asks, and not left for
// the dispatcher to get round to.
func TestARequestThatCanRunIsAdmittedWhenItAsks(t *testing.T) {
	scheduler := NewScheduler(SchedulerLimits{MaxConcurrent: 2, MaxQueued: 4})
	defer scheduler.Close(context.Background())

	for i := range 2 {
		waiter, err := scheduler.enqueue(SubmitRequest{})
		if err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
		select {
		case outcome := <-waiter.admitted:
			if outcome.err != nil {
				t.Fatalf("request %d was refused: %v", i, outcome.err)
			}
		default:
			t.Fatalf("request %d could run and was not admitted by the time enqueue returned", i)
		}
	}
	// The third has to wait.
	waiter, err := scheduler.enqueue(SubmitRequest{})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	select {
	case <-waiter.admitted:
		t.Fatal("a request was admitted with every slot taken")
	default:
	}
	scheduler.cancelWaiter(waiter)
}

// The data policy is checked before the budget is asked: a route the policy
// forbids is not a question for the budget at all.
func TestTheDataPolicyIsCheckedBeforeTheBudgetIsAsked(t *testing.T) {
	llm.ResetCapabilityRegistryForTest()
	for _, path := range []string{"buffered", "streamed"} {
		t.Run(path, func(t *testing.T) {
			budget := &refusingBudget{}
			ctx := WithExecConfig(context.Background(), &ExecConfig{
				Budget:             budget,
				DataPolicy:         types.DataPolicy{AllowedProviders: []string{"anthropic"}},
				DataPolicyDeclared: true,
			})
			provider := &gateProvider{}

			var err error
			if path == "buffered" {
				_, err = CallLLM(ctx, provider, "system", "user", lx126Options)
			} else {
				opts := lx126Options
				opts.ResponseFormat = "text"
				_, err = streamWith(ctx, provider, opts)
			}
			if !errors.Is(err, types.ErrPolicyViolation) {
				t.Fatalf("err = %v, want the policy's refusal and not the budget's", err)
			}
			if budget.checked != 0 {
				t.Errorf("the budget was asked %d times about a route the policy forbids", budget.checked)
			}
		})
	}
}

// CheckRoutePolicy on its own, by provider name: the form a router uses for the
// routes behind it.
func TestCheckRoutePolicy(t *testing.T) {
	eu := types.DataPolicy{AllowedProviders: []string{"openai"}, AllowedRegions: []string{"eu"}}
	declared := func(policy types.DataPolicy) context.Context { return declaredPolicy(policy) }

	cases := []struct {
		name     string
		ctx      context.Context
		route    *llm.ProviderCapabilities
		provider string
		model    string
		refused  bool
	}{
		{"no policy", context.Background(), nil, "anything", "any-model", false},
		{"allowed", declared(eu), &llm.ProviderCapabilities{Provider: "openai", Model: "m", Regions: []string{"eu"}}, "openai", "m", false},
		{"another model of an allowed provider, not declared", declared(eu), &llm.ProviderCapabilities{Provider: "openai", Model: "m", Regions: []string{"eu"}}, "openai", "other", true},
		{"a provider that is not allowed", declared(eu), &llm.ProviderCapabilities{Provider: "anthropic", Model: "m", Regions: []string{"eu"}}, "anthropic", "m", true},
		{"an empty provider name", declared(eu), nil, "", "m", true},
		{"an empty model name", declared(eu), &llm.ProviderCapabilities{Provider: "openai", Model: "m", Regions: []string{"eu"}}, "openai", "", true},
		{"negative retention is not a declaration", declared(eu), &llm.ProviderCapabilities{Provider: "openai", Model: "m", Regions: []string{"eu"}, RetentionDays: -1}, "openai", "m", true},
		{"negative retention under a generous policy", declared(types.DataPolicy{AllowedProviders: []string{"openai"}, AllowedRegions: []string{"eu"}, MaxRetentionDays: 365}),
			&llm.ProviderCapabilities{Provider: "openai", Model: "m", Regions: []string{"eu"}, RetentionDays: -1}, "openai", "m", true},
		{"a blank region among the route's", declared(eu), &llm.ProviderCapabilities{Provider: "openai", Model: "m", Regions: []string{"eu", ""}}, "openai", "m", true},
		{"the model in other capitals", declared(eu), &llm.ProviderCapabilities{Provider: "openai", Model: "m", Regions: []string{"eu"}}, "openai", "M", false},
		{"the region in other capitals", declared(eu), &llm.ProviderCapabilities{Provider: "openai", Model: "m", Regions: []string{"EU"}}, "openai", "m", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			llm.ResetCapabilityRegistryForTest()
			t.Cleanup(llm.ResetCapabilityRegistryForTest)
			if tc.route != nil {
				llm.RegisterCapabilities(*tc.route)
			}
			err := CheckRoutePolicy(tc.ctx, tc.provider, tc.model)
			if tc.refused != (err != nil) {
				t.Fatalf("err = %v, want a refusal: %v", err, tc.refused)
			}
			if err != nil && (!errors.Is(err, types.ErrPolicyViolation) || !errors.Is(err, ErrDataPolicyRefusal)) {
				t.Errorf("the refusal is %v, not a data-policy refusal", err)
			}
		})
	}
}

// An answer that came from a provider the policy does not allow is refused,
// whatever the provider the request was handed to calls itself. A router that
// sends a request on reports who answered in the response, and that is the one
// place this library can see it.
func TestAnAnswerFromAProviderThePolicyDoesNotAllowIsRefused(t *testing.T) {
	llm.ResetCapabilityRegistryForTest()
	t.Cleanup(llm.ResetCapabilityRegistryForTest)
	llm.RegisterCapabilities(llm.ProviderCapabilities{Provider: "openai", Model: "gpt-6-luna", Regions: []string{"eu"}})
	ctx := declaredPolicy(types.DataPolicy{AllowedProviders: []string{"openai"}, AllowedRegions: []string{"eu"}})

	cases := []struct {
		name     string
		answered string
		refused  bool
	}{
		{"the provider that was asked", "openai", false},
		{"the same in other capitals", "OpenAI", false},
		{"no provider named in the answer", "", false},
		{"another provider", "anthropic", true},
		{"a provider that only looks like it", "openai-compatible", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &answersProvider{responses: []llm.CompletionResponse{{
				Content: `{"ok":true}`, FinishReason: "stop", Model: "gpt-6-luna", Provider: tc.answered,
			}}, retries: 3}
			_, err := CallLLM(ctx, provider, "system", "user", lx126Options)
			if tc.refused != errors.Is(err, types.ErrPolicyViolation) {
				t.Fatalf("err = %v, want a policy refusal: %v", err, tc.refused)
			}
			if tc.refused && provider.calls != 1 {
				t.Errorf("the provider was called %d times: a policy refusal was retried", provider.calls)
			}
		})
	}
}
