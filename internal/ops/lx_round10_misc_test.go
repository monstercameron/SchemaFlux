package ops

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/pricing"
)

// closingProvider closes its scheduler and then fails with an error of its
// own, which is what a provider failure a moment before a shutdown looks like.
type closingProvider struct {
	scheduler *Scheduler
	failure   error
}

func (p *closingProvider) Complete(ctx context.Context, _ llm.CompletionRequest) (llm.CompletionResponse, error) {
	// Close waits for what is in flight, which is this call, so it is started
	// and not waited on. It has closed the scheduler and cancelled this call's
	// context by the time the context is seen to be done.
	go p.scheduler.Close(context.Background())
	<-ctx.Done()
	return llm.CompletionResponse{}, p.failure
}
func (p *closingProvider) Name() string                               { return "openai" }
func (p *closingProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *closingProvider) RetryPolicy() (int, time.Duration)          { return 0, 0 }

// Review round 10. A provider's own failure is not a shutdown because the
// scheduler is closed by the time anyone looks. Any error at all was relabelled
// one, which told the caller the request had been stopped when it had failed.
func TestAProvidersOwnFailureIsNotCalledAShutdown(t *testing.T) {
	failures := []struct {
		name     string
		err      error
		shutdown bool
	}{
		{"an authentication failure", &types.OperationError{Kind: types.KindAuthentication, Message: "the key was refused"}, false},
		{"an invalid request", &types.OperationError{Kind: types.KindInvalidRequest, Message: "bad request"}, false},
		{"a rate limit", &types.OperationError{Kind: types.KindRateLimited, Message: "slow down"}, false},
		{"an unclassified failure", errors.New("the upstream broke"), false},
		{"a timeout of the provider's own", &types.OperationError{Kind: types.KindTimeout, Message: "too slow"}, false},
		// What Close does to work in flight: the context it runs under is
		// cancelled, and the work reports that.
		{"a cancellation", context.Canceled, true},
		{"a classified cancellation", &types.OperationError{Kind: types.KindCanceled, Cause: context.Canceled}, true},
	}
	for _, tc := range failures {
		t.Run(tc.name, func(t *testing.T) {
			scheduler := NewScheduler(SchedulerLimits{MaxConcurrent: 1})
			defer scheduler.Close(context.Background())
			ctx := WithExecConfig(context.Background(), &ExecConfig{Scheduler: scheduler})
			provider := &closingProvider{scheduler: scheduler, failure: tc.err}

			_, err := CallLLM(ctx, provider, "system", "user", lx126Options)
			if err == nil {
				t.Fatal("the call succeeded")
			}
			if got := errors.Is(err, types.ErrShutdown); got != tc.shutdown {
				t.Errorf("err = %v; a shutdown: %v, want %v", err, got, tc.shutdown)
			}
			if !tc.shutdown && !errors.Is(err, tc.err) {
				t.Errorf("err = %v, want the provider's own %v", err, tc.err)
			}
		})
	}
}

func TestStoppedByShutdown(t *testing.T) {
	closed := NewScheduler(SchedulerLimits{})
	_ = closed.Close(context.Background())
	live := NewScheduler(SchedulerLimits{})
	defer live.Close(context.Background())
	failure := errors.New("the provider failed")

	cases := []struct {
		name      string
		scheduler *Scheduler
		cancelled bool
		err       error
		want      bool
	}{
		{"closed, cancelled, a cancellation", closed, true, context.Canceled, true},
		{"closed, cancelled, a classified cancellation", closed, true, &types.OperationError{Kind: types.KindCanceled}, true},
		{"closed, cancelled, a wrapped cancellation", closed, true, errors.Join(errors.New("x"), context.Canceled), true},
		{"closed, cancelled, the provider's own error", closed, true, failure, false},
		{"closed, cancelled, a deadline", closed, true, context.DeadlineExceeded, false},
		{"closed, not cancelled", closed, false, context.Canceled, false},
		{"live, cancelled", live, true, context.Canceled, false},
		{"no scheduler", nil, true, context.Canceled, false},
		{"no error", closed, true, nil, false},
		{"live, not cancelled, the provider's own error", live, false, failure, false},
	}
	for _, tc := range cases {
		if got := stoppedByShutdown(tc.scheduler, tc.cancelled, tc.err); got != tc.want {
			t.Errorf("%s: stoppedByShutdown = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestSchedulerClosed(t *testing.T) {
	scheduler := NewScheduler(SchedulerLimits{MaxConcurrent: 1})
	if scheduler.Closed() {
		t.Error("a new scheduler reports itself closed")
	}
	_, err := Submit(context.Background(), scheduler, SubmitRequest{}, func(context.Context) (struct{}, error) { return struct{}{}, nil })
	if err != nil || scheduler.Closed() {
		t.Errorf("after one request: err %v, closed %v", err, scheduler.Closed())
	}
	if err := scheduler.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for range 3 {
		if !scheduler.Closed() {
			t.Error("a closed scheduler reports itself open")
		}
		// Closing again changes nothing.
		_ = scheduler.Close(context.Background())
	}
}

// enqueue on its own: what it refuses and why, and that a refusal leaves
// nothing behind.
func TestSchedulerEnqueue(t *testing.T) {
	hold := func(t *testing.T, scheduler *Scheduler, req SubmitRequest) {
		t.Helper()
		waiter, err := scheduler.enqueue(req)
		if err != nil {
			t.Fatalf("the setup request was refused: %v", err)
		}
		select {
		case <-waiter.admitted:
		default:
		}
	}
	cases := []struct {
		name    string
		limits  SchedulerLimits
		before  []SubmitRequest // requests already admitted or queued
		req     SubmitRequest
		refused error
		queued  int // how many are waiting afterwards
	}{
		{"an idle scheduler", SchedulerLimits{MaxConcurrent: 1, MaxQueued: 1}, nil, SubmitRequest{}, nil, 0},
		{"no limits at all", SchedulerLimits{}, []SubmitRequest{{}, {}, {}}, SubmitRequest{}, nil, 0},
		{"a slot taken, room in the queue", SchedulerLimits{MaxConcurrent: 1, MaxQueued: 1}, []SubmitRequest{{}}, SubmitRequest{}, nil, 1},
		{"a slot taken, the queue full", SchedulerLimits{MaxConcurrent: 1, MaxQueued: 1}, []SubmitRequest{{}, {}}, SubmitRequest{}, types.ErrAdmissionRejected, 1},
		{"a slot taken, no bound on the queue", SchedulerLimits{MaxConcurrent: 1}, []SubmitRequest{{}, {}, {}}, SubmitRequest{}, nil, 3},
		{"a deadline already past", SchedulerLimits{MaxConcurrent: 1}, nil, SubmitRequest{Deadline: time.Now().Add(-time.Minute)}, types.ErrAdmissionRejected, 0},
		{"tokens the ceiling cannot hold", SchedulerLimits{MaxInFlightTokens: 10}, nil, SubmitRequest{EstimatedTokens: 11}, types.ErrAdmissionRejected, 0},
		{"a cost the ceiling cannot hold", SchedulerLimits{MaxInFlightCost: 1}, nil, SubmitRequest{EstimatedCost: 1.5}, types.ErrAdmissionRejected, 0},
		{"a negative estimate", SchedulerLimits{}, nil, SubmitRequest{EstimatedTokens: -5}, types.ErrInvalidRequest, 0},
		{"another provider's slot is free", SchedulerLimits{MaxConcurrent: 4, PerProviderConcurrency: 1, MaxQueued: 1},
			[]SubmitRequest{{Provider: "a"}, {Provider: "a"}}, SubmitRequest{Provider: "b"}, nil, 1},
		{"the same provider's slot is taken and the queue is full", SchedulerLimits{MaxConcurrent: 4, PerProviderConcurrency: 1, MaxQueued: 1},
			[]SubmitRequest{{Provider: "a"}, {Provider: "a"}}, SubmitRequest{Provider: "a"}, types.ErrAdmissionRejected, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scheduler := NewScheduler(tc.limits)
			defer scheduler.Close(context.Background())
			for _, req := range tc.before {
				hold(t, scheduler, req)
			}

			waiter, err := scheduler.enqueue(tc.req)
			if tc.refused != nil {
				if !errors.Is(err, tc.refused) || waiter != nil {
					t.Fatalf("err = %v, waiter %v; want %v and no waiter", err, waiter != nil, tc.refused)
				}
			} else if err != nil {
				t.Fatalf("enqueue: %v", err)
			}
			if got := scheduler.Stats().Queued; got != tc.queued {
				t.Errorf("%d waiting afterwards, want %d", got, tc.queued)
			}
		})
	}

	// A closed scheduler refuses with a shutdown.
	closed := NewScheduler(SchedulerLimits{})
	_ = closed.Close(context.Background())
	if _, err := closed.enqueue(SubmitRequest{}); !errors.Is(err, types.ErrShutdown) {
		t.Errorf("err = %v, want a shutdown", err)
	}
}

// A tenant first seen in a request that was refused is not kept in the
// rotation. Every refused newcomer stayed in it for the scheduler's life.
func TestARefusedNewcomerIsNotKeptInTheRotation(t *testing.T) {
	scheduler := NewScheduler(SchedulerLimits{MaxConcurrent: 1, MaxQueued: 1})
	defer scheduler.Close(context.Background())
	for range 2 {
		if _, err := scheduler.enqueue(SubmitRequest{Tenant: "resident"}); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	tenants := func() []string {
		scheduler.mu.Lock()
		defer scheduler.mu.Unlock()
		return append([]string(nil), scheduler.tenantOrder...)
	}
	// The rotation is two things, the order and the queues it indexes, and a
	// refused newcomer has to leave both. This looked at the order alone, and
	// passed with the queues left behind: one map for every tenant name ever
	// refused, for the scheduler's life.
	queues := func() int {
		scheduler.mu.Lock()
		defer scheduler.mu.Unlock()
		return len(scheduler.queues)
	}
	before := tenants()
	queuesBefore := queues()

	for i := range 50 {
		_, err := scheduler.enqueue(SubmitRequest{Tenant: "newcomer-" + string(rune('a'+i%26))})
		if !errors.Is(err, types.ErrAdmissionRejected) {
			t.Fatalf("request %d: err = %v, want a refusal", i, err)
		}
	}
	if after := tenants(); !reflect.DeepEqual(after, before) {
		t.Errorf("the rotation holds %v after fifty refused newcomers, it held %v", after, before)
	}
	if after := queues(); after != queuesBefore {
		t.Errorf("the scheduler holds queues for %d tenants after fifty refused newcomers, it held %d", after, queuesBefore)
	}

	// A resident that is refused stays, since it has work here.
	if _, err := scheduler.enqueue(SubmitRequest{Tenant: "resident"}); !errors.Is(err, types.ErrAdmissionRejected) {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if after := tenants(); !reflect.DeepEqual(after, before) {
		t.Errorf("a refused resident changed the rotation: %v", after)
	}
}

// estimatedCost against the price table itself, with no case that compares the
// function with its own answer.
func TestEstimatedCostIsThePriceTablesFigure(t *testing.T) {
	price := func(model, provider string, prompt, output int) float64 {
		cost := pricing.CalculateCost(&types.TokenUsage{PromptTokens: prompt, CompletionTokens: output, TotalTokens: prompt + output}, model, provider)
		if cost == nil || !cost.Priced {
			return 0
		}
		return cost.TotalCost
	}
	cases := []struct {
		provider, model string
		prompt, output  int
	}{
		{"openai", "gpt-6-luna", 0, 0},
		{"openai", "gpt-6-luna", 1000, 0},
		{"openai", "gpt-6-luna", 0, 500},
		{"openai", "gpt-6-luna", 1000, 500},
		{"openai", "gpt-6-luna", 250000, 16000},
		{"openai", "a-model-with-no-price", 1000, 500},
		{"nobody", "gpt-6-luna", 1000, 500},
		{"", "gpt-6-luna", 1000, 500},
		{"openai", "", 1000, 500},
		{"local", "llama", 1000, 500},
		{"codex", "gpt-6-luna", 1000, 500},
	}
	for _, tc := range cases {
		want := price(tc.model, tc.provider, tc.prompt, tc.output)
		got := estimatedCost(tc.provider, tc.model, tc.prompt, tc.output)
		if diff := got - want; diff > 1e-12 || diff < -1e-12 {
			t.Errorf("estimatedCost(%q, %q, %d, %d) = %v, the price table says %v", tc.provider, tc.model, tc.prompt, tc.output, got, want)
		}
		if got < 0 || got != got {
			t.Errorf("estimatedCost(%q, %q) = %v, which a ceiling cannot be compared with", tc.provider, tc.model, got)
		}
	}
}

func TestAuditPolicyKey(t *testing.T) {
	cases := map[int]string{0: "p1", 1: "p2", 8: "p9", 9: "p10", 10: "p11", 98: "p99", 99: "p100", 100: "p101", 998: "p999", 1000: "p1001"}
	for index, want := range cases {
		if got := auditPolicyKey(index); got != want {
			t.Errorf("auditPolicyKey(%d) = %q, want %q", index, got, want)
		}
	}
}

func TestAuditFindingSchema(t *testing.T) {
	cases := [][]string{
		{"security"},
		{"security", "compliance"},
		auditCategories,
		{"privacy", "security", "compliance", "quality", "consistency", "completeness"},
		{"a category with spaces"},
		{"Ünïcödé"},
		{"a", "b", "c", "d", "e", "f", "g", "h"},
		{"x"},
		{"quality", "privacy"},
		{"1", "2", "3"},
	}
	for _, categories := range cases {
		schema := auditFindingSchema(categories)
		if problem := llm.StrictSchemaProblem(strictEnvelope(map[string]any{"findings": listOf(schema)}, "findings")); problem != "" {
			t.Fatalf("%v: the schema breaks a strict-mode rule: %s", categories, problem)
		}
		properties := schema["properties"].(map[string]any)
		enum := properties["category"].(map[string]any)["enum"].([]any)
		if len(enum) != len(categories) {
			t.Errorf("%v: %d categories in the schema", categories, len(enum))
		}
		for i, category := range categories {
			if enum[i] != category {
				t.Errorf("%v: category %d is %v", categories, i, enum[i])
			}
		}
		for _, field := range []string{"category", "severity", "field", "issue", "evidence", "recommendation"} {
			if _, declared := properties[field]; !declared {
				t.Errorf("%v: the schema has no %q", categories, field)
			}
		}
	}
}

func TestAuditEnumValues(t *testing.T) {
	policies := func(n int) []string { return make([]string, n) }
	cases := []struct {
		name       string
		policies   int
		categories []string
		want       int
	}{
		{"nothing", 0, nil, 5},
		{"one policy", 1, nil, 10},
		{"ten policies", 10, nil, 55},
		{"ninety-nine policies", 99, nil, 500},
		{"one hundred policies", 100, nil, 505},
		{"one category of the caller's", 0, []string{"privacy"}, 1},
		{"a policy and a new category", 1, []string{"privacy"}, 7},
		{"a policy and a category that is one of the five", 1, []string{"quality"}, 6},
		{"two policies and two new categories", 2, []string{"privacy", "fraud"}, 16},
		{"many categories and no policy", 0, []string{"a", "b", "c", "d"}, 4},
	}
	for _, tc := range cases {
		if got := auditEnumValues(policies(tc.policies), tc.categories); got != tc.want {
			t.Errorf("%s: %d enum values, want %d", tc.name, got, tc.want)
		}
	}
}

// RunOpBatchResult states the level its operation declares and is held to the
// floor before anything is sent, like the other Result forms.
func TestRunOpBatchResultIsHeldToTheContractFloor(t *testing.T) {
	op := echoOp(func(string) error { return nil })
	// Round 12: what a batch establishes is what the batch path checks, which
	// for an operation with no id-protocol merge is that the provider
	// answered. It was read off the contract.
	declared := batchContractLevel(op)

	floors := []types.ContractLevel{
		types.ContractPromptOnly, types.ContractJSONWellFormed, types.ContractSchemaConstrained,
		types.ContractSchemaAndInvariantChecked, types.ContractEvidenceChecked, types.ContractFullyGoverned,
	}
	for _, floor := range floors {
		t.Run("a floor of "+floor.String(), func(t *testing.T) {
			provider := &captureProvider{resp: llm.CompletionResponse{Content: `{}`, FinishReason: "stop"}}
			ctx := WithContractPolicy(WithProvider(context.Background(), provider), types.DataPolicy{MinimumContract: floor})

			result, err := RunOpBatchResult(ctx, op, []string{"a", "b"}, types.OpOptions{Model: "gpt-6-luna"})
			if declared < floor {
				if !errors.Is(err, types.ErrPolicyViolation) || !errors.Is(err, ErrContractDegraded) {
					t.Fatalf("err = %v, want the floor's refusal", err)
				}
				if provider.attempts != 0 {
					t.Errorf("the provider was called %d times for a batch that could never meet the floor", provider.attempts)
				}
				return
			}
			// Whether the batch itself succeeds is not this test's subject:
			// the floor did not stop it, and the envelope states the level.
			if errors.Is(err, ErrContractDegraded) {
				t.Fatalf("refused under a floor it meets: %v", err)
			}
			if result.Meta.RequestedContract != declared {
				t.Errorf("the envelope says requested %s, the operation declares %s", result.Meta.RequestedContract, declared)
			}
		})
	}
}
