package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/telemetry"
)

// A turn goes through the call path an operation's request does. Each test here
// is one thing that path does, shown on the turn's entry.

func oneTurn(text string) Turn {
	return Turn{Messages: []llm.Message{{Role: "user", Content: text}}}
}

func TestATurnThatIsOnlyToolCallsIsReturnedOnceWithItsCalls(t *testing.T) {
	calls := []llm.ToolCall{weatherCall("call_1"), {ID: "call_2", Name: "get_time", Arguments: json.RawMessage(`{}`)}}
	cases := []struct {
		name    string
		retries int
		calls   []llm.ToolCall
	}{
		{"one call, a provider that retries", 3, calls[:1]},
		{"two calls, a provider that retries", 3, calls},
		{"one call, a provider that does not retry", 0, calls[:1]},
		{"two calls, a provider that does not retry", 0, calls},
		{"one call, a provider that retries once", 1, calls[:1]},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &chatScript{retries: tc.retries, steps: []chatStep{{resp: llm.CompletionResponse{
				ToolCalls: tc.calls, FinishReason: "tool_calls",
				Usage: types.TokenUsage{PromptTokens: 40, CompletionTokens: 9, TotalTokens: 49},
			}}}}
			ctx := WithSharedExecConfig(context.Background(), &ExecConfig{Provider: provider})

			resp, err := CallTurn(ctx, oneTurn("How warm is Oslo?"), callOptions())
			if err != nil {
				t.Fatalf("CallTurn: %v", err)
			}
			if got := provider.count(); got != 1 {
				t.Errorf("%d requests were sent, want 1", got)
			}
			if resp.Content != "" || len(resp.ToolCalls) != len(tc.calls) {
				t.Fatalf("response = %+v, want no text and %d calls", resp, len(tc.calls))
			}
			for i, call := range resp.ToolCalls {
				if call.ID != tc.calls[i].ID || call.Name != tc.calls[i].Name || string(call.Arguments) != string(tc.calls[i].Arguments) {
					t.Errorf("call %d came back changed", i+1)
				}
			}
			if resp.Usage.TotalTokens != 49 || resp.FinishReason != "tool_calls" {
				t.Errorf("usage %+v, finish %q: what the provider said was lost", resp.Usage, resp.FinishReason)
			}
		})
	}
}

func TestATurnWithNeitherTextNorCallsIsAskedAgainAsAnOperationIs(t *testing.T) {
	cases := []struct {
		name     string
		retries  int
		steps    []chatStep
		wantSent int
		wantErr  bool
	}{
		{"empty then an answer", 2, []chatStep{{resp: llm.CompletionResponse{}}, {resp: llm.CompletionResponse{Content: "hi"}}}, 2, false},
		{"white space then calls", 2, []chatStep{{resp: llm.CompletionResponse{Content: " \n"}}, {resp: toolCallsOnly(weatherCall("c"))}}, 2, false},
		{"two empties then an answer", 2, []chatStep{{resp: llm.CompletionResponse{}}, {resp: llm.CompletionResponse{}}, {resp: llm.CompletionResponse{Content: "ok"}}}, 3, false},
		{"always empty, no retries", 0, []chatStep{{resp: llm.CompletionResponse{}}}, 1, true},
		{"always empty, one retry", 1, []chatStep{{resp: llm.CompletionResponse{}}}, 2, true},
		{"always empty, three retries", 3, []chatStep{{resp: llm.CompletionResponse{}}}, 4, true},
		{"always white space, two retries", 2, []chatStep{{resp: llm.CompletionResponse{Content: "\t"}}}, 3, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &chatScript{retries: tc.retries, steps: tc.steps}
			ctx := WithSharedExecConfig(context.Background(), &ExecConfig{Provider: provider})
			resp, err := CallTurn(ctx, oneTurn("hello"), callOptions())
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want an error: %v", err, tc.wantErr)
			}
			if got := provider.count(); got != tc.wantSent {
				t.Errorf("%d requests, want %d", got, tc.wantSent)
			}
			if err != nil {
				if !errors.Is(err, errEmptyCompletion) && types.KindOf(err) != types.KindMalformedOutput {
					t.Errorf("err = %v, want the empty-answer refusal", err)
				}
				if resp.Content != "" || len(resp.ToolCalls) != 0 || resp.Usage != (types.TokenUsage{}) {
					t.Errorf("a response %+v came back beside an error", resp)
				}
			}
		})
	}
}

// callRequest is CallLLM without the request it builds: what it returns is the
// provider's whole answer.
func TestCallRequestReturnsTheWholeAnswer(t *testing.T) {
	cases := []struct {
		name string
		resp llm.CompletionResponse
	}{
		{"text", llm.CompletionResponse{Content: "hello", FinishReason: "stop"}},
		{"calls", toolCallsOnly(weatherCall("a"))},
		{"text and calls", llm.CompletionResponse{Content: "one moment", ToolCalls: []llm.ToolCall{weatherCall("a")}, FinishReason: "tool_calls"}},
		{"two calls", toolCallsOnly(weatherCall("a"), weatherCall("b"))},
		{"usage", llm.CompletionResponse{Content: "x", Usage: types.TokenUsage{PromptTokens: 7, CompletionTokens: 2, TotalTokens: 9, CachedTokens: 4}}},
		{"a served model", llm.CompletionResponse{Content: "x", Model: "served-model", Provider: "served-by"}},
		{"a service tier", llm.CompletionResponse{Content: "x", ServiceTier: "priority"}},
		{"a cached response", llm.CompletionResponse{Content: "x", Cached: true}},
		{"a hedged response", llm.CompletionResponse{Content: "x", Hedged: true}},
		{"timing", llm.CompletionResponse{Content: "x", Timing: types.Timing{Calls: 1}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &chatScript{steps: []chatStep{{resp: tc.resp}}}
			req := llm.CompletionRequest{
				Model: "chat-model", ResponseFormat: "text",
				Messages: []llm.Message{{Role: "user", Content: "hi"}},
			}
			got, err := callRequest(context.Background(), provider, req, callOptions())
			if err != nil {
				t.Fatalf("callRequest: %v", err)
			}
			want := tc.resp
			if want.Model == "" {
				want.Model = "chat-model"
			}
			if want.Provider == "" {
				want.Provider = "local"
			}
			if got.Content != want.Content || got.Model != want.Model || got.Provider != want.Provider ||
				got.FinishReason != want.FinishReason || got.Usage != want.Usage || got.Cached != want.Cached ||
				got.Hedged != want.Hedged || got.ServiceTier != want.ServiceTier || len(got.ToolCalls) != len(want.ToolCalls) {
				t.Errorf("callRequest returned %+v, the provider answered %+v", got, want)
			}
		})
	}
}

// The data policy of the client is held before anything is sent, as for an
// operation.
func TestATurnIsHeldToTheClientsDataPolicy(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		allowed  []string
		refused  bool
	}{
		{"a policy that names another provider", "local", []string{"anthropic"}, true},
		{"a policy that names nobody", "local", nil, true},
		{"a policy that names this provider", "local", []string{"local"}, false},
		{"the provider's name in other capitals", "local", []string{"LOCAL"}, false},
		{"two providers, the second is this one", "openai", []string{"anthropic", "openai"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			llmResetCapabilities(t)
			provider := &chatScript{name: tc.provider, steps: []chatStep{{resp: llm.CompletionResponse{Content: "hi"}}}}
			ctx := WithSharedExecConfig(context.Background(), &ExecConfig{
				Provider:           provider,
				DataPolicy:         types.DataPolicy{AllowedProviders: tc.allowed, AllowedRegions: []string{"eu"}, MaxRetentionDays: types.RetentionUnspecified},
				DataPolicyDeclared: true,
			})
			llm.RegisterCapabilities(llm.ProviderCapabilities{Provider: tc.provider, Model: "chat-model", Regions: []string{"eu"}})

			_, err := CallTurn(ctx, oneTurn("hi"), callOptions())
			if tc.refused {
				if !errors.Is(err, types.ErrPolicyViolation) {
					t.Fatalf("err = %v, want a policy refusal", err)
				}
				if provider.count() != 0 {
					t.Errorf("the provider was sent %d requests for a route the policy forbids", provider.count())
				}
				return
			}
			if err != nil || provider.count() != 1 {
				t.Fatalf("err = %v, %d requests: the policy allows the route", err, provider.count())
			}
		})
	}
}

func llmResetCapabilities(t *testing.T) {
	t.Helper()
	llm.ResetCapabilityRegistryForTest()
	t.Cleanup(llm.ResetCapabilityRegistryForTest)
}

// turnLedger is a client's budget that counts what is asked of it and can refuse.
type turnLedger struct {
	mu      sync.Mutex
	refuse  error
	checks  int
	records []bool
}

func (l *turnLedger) Check() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.checks++
	return l.refuse
}

func (l *turnLedger) Record(_ float64, priced bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, priced)
}

func (l *turnLedger) seen() (checks int, records []bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.checks, append([]bool(nil), l.records...)
}

func TestATurnIsHeldToTheClientsBudget(t *testing.T) {
	t.Run("an exhausted budget refuses the turn before it is sent", func(t *testing.T) {
		ledger := &turnLedger{refuse: types.Classified(types.KindBudgetExceeded, errors.New("the budget is spent"))}
		provider := &chatScript{retries: 2, steps: []chatStep{{resp: llm.CompletionResponse{Content: "hi"}}}}
		ctx := WithSharedExecConfig(context.Background(), &ExecConfig{Provider: provider, Budget: ledger})

		_, err := CallTurn(ctx, oneTurn("hi"), callOptions())
		if !errors.Is(err, types.ErrBudgetExceeded) {
			t.Fatalf("err = %v, want a budget refusal", err)
		}
		if provider.count() != 0 {
			t.Errorf("%d requests were sent after the budget was spent", provider.count())
		}
		if _, records := ledger.seen(); len(records) != 0 {
			t.Errorf("a refused turn was charged: %v", records)
		}
	})

	t.Run("a turn that is answered is charged to the client", func(t *testing.T) {
		ledger := &turnLedger{}
		provider := &chatScript{steps: []chatStep{{resp: llm.CompletionResponse{
			Content: "hi", Usage: types.TokenUsage{PromptTokens: 5, CompletionTokens: 1, TotalTokens: 6},
		}}}}
		ctx := WithSharedExecConfig(context.Background(), &ExecConfig{Provider: provider, Budget: ledger})

		if _, err := CallTurn(ctx, oneTurn("hi"), callOptions()); err != nil {
			t.Fatal(err)
		}
		checks, records := ledger.seen()
		if checks != 1 || len(records) != 1 {
			t.Errorf("the ledger was checked %d times and charged %d times, want 1 and 1", checks, len(records))
		}
	})

	t.Run("a budget that runs out between two attempts stops the retry", func(t *testing.T) {
		ledger := &budgetThatRunsOut{after: 1}
		provider := &chatScript{retries: 3, steps: []chatStep{{err: errors.New("rate limit exceeded: status 429")}}}
		ctx := WithSharedExecConfig(context.Background(), &ExecConfig{Provider: provider, Budget: ledger})

		_, err := CallTurn(ctx, oneTurn("hi"), callOptions())
		if !errors.Is(err, types.ErrBudgetExceeded) {
			t.Fatalf("err = %v, want a budget refusal", err)
		}
		if got := provider.count(); got != 1 {
			t.Errorf("%d requests were sent, want the one that came before the budget ran out", got)
		}
	})
}

// budgetThatRunsOut allows the first checks and refuses every one after.
type budgetThatRunsOut struct {
	after int
	seen  atomic.Int64
}

func (b *budgetThatRunsOut) Check() error {
	if int(b.seen.Add(1)) > b.after {
		return types.Classified(types.KindBudgetExceeded, errors.New("the budget is spent"))
	}
	return nil
}
func (b *budgetThatRunsOut) Record(float64, bool) {}

// The scheduler of the client bounds turns as it bounds operations: turns that
// run together take its slots one at a time, and a turn it does not admit is
// never sent.
func TestATurnTakesASlotInTheClientsScheduler(t *testing.T) {
	t.Run("two turns share one slot", func(t *testing.T) {
		provider := &gateProvider{hold: 20 * time.Millisecond}
		ctx, _ := scheduledContext(t, SchedulerLimits{MaxConcurrent: 1, MaxQueued: 10})
		ctx = WithSharedExecConfig(ctx, &ExecConfig{Provider: provider, Scheduler: ExecScheduler(ctx)})

		var wg sync.WaitGroup
		errs := make(chan error, 6)
		for range 6 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := CallTurn(ctx, oneTurn("hi"), callOptions())
				errs <- err
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Errorf("a turn failed: %v", err)
			}
		}
		if got := provider.peak.Load(); got != 1 {
			t.Errorf("%d turns were with the provider at once, the scheduler's limit is 1", got)
		}
		if got := provider.calls.Load(); got != 6 {
			t.Errorf("the provider was sent %d requests, want 6", got)
		}
	})

	t.Run("a turn the scheduler does not admit is not sent", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			provider := &gateProvider{release: make(chan struct{})}
			scheduled, _ := scheduledContext(t, SchedulerLimits{MaxConcurrent: 1, MaxQueued: 1})
			ctx := WithSharedExecConfig(scheduled, &ExecConfig{Provider: provider, Scheduler: ExecScheduler(scheduled)})

			results := make(chan error, 2)
			call := func() {
				_, err := CallTurn(ctx, oneTurn("hi"), callOptions())
				results <- err
			}
			go call()
			waitFor(t, func() bool { return provider.inFlight.Load() == 1 })
			go call()
			waitFor(t, func() bool { return provider.inFlight.Load() == 1 && ExecScheduler(ctx).Stats().Queued == 1 })

			_, err := CallTurn(ctx, oneTurn("hi"), callOptions())
			if !errors.Is(err, types.ErrAdmissionRejected) {
				t.Errorf("err = %v, want an admission refusal", err)
			}
			if got := provider.calls.Load(); got != 1 {
				t.Errorf("the provider was sent %d requests, want 1", got)
			}
			close(provider.release)
			for range 2 {
				if err := <-results; err != nil {
					t.Errorf("a turn that was admitted failed: %v", err)
				}
			}
		})
	})
}

// The client's Timeout is the deadline of a turn whose context has none, and a
// deadline the caller set is left alone.
func TestATurnRunsUnderTheClientsDeadline(t *testing.T) {
	cases := []struct {
		name    string
		timeout time.Duration
		caller  time.Duration
		bound   bool
	}{
		{"the client's timeout", 5 * time.Minute, 0, true},
		{"the caller's own deadline", 0, 4 * time.Minute, true},
		{"neither", 0, 0, false},
		{"both, and the caller's is the nearer", 9 * time.Minute, 2 * time.Minute, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &deadlineProvider{}
			ctx := WithSharedExecConfig(context.Background(), &ExecConfig{Provider: provider, Timeout: tc.timeout})
			if tc.caller > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.caller)
				defer cancel()
			}
			_, _ = CallTurn(ctx, oneTurn("hi"), callOptions())
			requests, bounded, left := provider.first()
			if requests != 1 {
				t.Fatalf("%d requests", requests)
			}
			if bounded != tc.bound {
				t.Fatalf("the request's context is bounded: %v, want %v", bounded, tc.bound)
			}
			if tc.bound {
				want := tc.timeout
				if tc.caller > 0 && (want == 0 || tc.caller < want) {
					want = tc.caller
				}
				if left > want || left < want-time.Minute {
					t.Errorf("%v was left to the request, want about %v", left, want)
				}
			}
		})
	}

	t.Run("a provider that never answers is a timeout", func(t *testing.T) {
		// It answers after three seconds, so that a turn with no deadline ends
		// the test with an answer and not by hanging it.
		provider := &gateProvider{hold: 3 * time.Second}
		ctx := WithSharedExecConfig(context.Background(), &ExecConfig{Provider: provider, Timeout: 100 * time.Millisecond})
		started := time.Now()
		_, err := CallTurn(ctx, oneTurn("hi"), callOptions())
		if kind := types.KindOf(err); kind != types.KindTimeout {
			t.Fatalf("kind = %v (%v), want a timeout", kind, err)
		}
		if elapsed := time.Since(started); elapsed > 2*time.Second {
			t.Errorf("the turn took %v against a deadline of 100ms", elapsed)
		}
	})
}

// A request that failed in transit is sent again, as the client or the provider
// says, and one that failed for good is not.
func TestATurnIsSentAgainWhenTheProviderFailedInTransit(t *testing.T) {
	transit := errors.New("rate limit exceeded: status 429")
	cases := []struct {
		name     string
		provider int
		client   int
		steps    []chatStep
		wantSent int
		wantErr  bool
	}{
		{"the provider's two retries", 2, 0, []chatStep{{err: transit}, {err: transit}, {resp: llm.CompletionResponse{Content: "ok"}}}, 3, false},
		{"the client's two over a provider with none", 0, 2, []chatStep{{err: transit}, {err: transit}, {resp: llm.CompletionResponse{Content: "ok"}}}, 3, false},
		{"the client's never over the provider's two", 2, -1, []chatStep{{err: transit}, {resp: llm.CompletionResponse{Content: "ok"}}}, 1, true},
		{"retries used up", 1, 0, []chatStep{{err: transit}}, 2, true},
		{"a failure that is for good", 3, 0, []chatStep{{err: types.Newf(types.KindInvalidRequest, "llm", "rejected")}}, 1, true},
		{"calls only after a failure", 2, 0, []chatStep{{err: transit}, {resp: toolCallsOnly(weatherCall("c"))}}, 2, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &chatScript{retries: tc.provider, steps: tc.steps}
			ctx := WithSharedExecConfig(context.Background(), &ExecConfig{Provider: provider, Retries: tc.client})
			_, err := CallTurn(ctx, oneTurn("hi"), callOptions())
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want an error: %v", err, tc.wantErr)
			}
			if got := provider.count(); got != tc.wantSent {
				t.Errorf("%d requests, want %d", got, tc.wantSent)
			}
		})
	}
}

// spanObserver gives every unit it sees start an id and records the id of the
// unit it started inside, the way an observer that opens spans does.
type spanObserver struct {
	mu     sync.Mutex
	next   int
	starts []spanStart
	finish []telemetry.OperationResult
}

type spanStart struct {
	scope     telemetry.Scope
	operation string
	id        int
	parent    int
}

type spanContextKey struct{}

func (o *spanObserver) OperationStarted(ctx context.Context, event telemetry.OperationEvent) context.Context {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.next++
	parent, _ := ctx.Value(spanContextKey{}).(int)
	o.starts = append(o.starts, spanStart{scope: event.Scope, operation: event.Operation, id: o.next, parent: parent})
	return context.WithValue(ctx, spanContextKey{}, o.next)
}

func (o *spanObserver) OperationFinished(_ context.Context, result telemetry.OperationResult) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.finish = append(o.finish, result)
}

// A turn made under a record is one operation to the observers, and its request
// is a child of it. An operation's own request is seen the same way.
func TestATurnIsOneOperationWithItsRequestAsAChild(t *testing.T) {
	observer := &spanObserver{}
	t.Cleanup(telemetry.AddObserver(observer))

	provider := &chatScript{steps: []chatStep{{resp: toolCallsOnly(weatherCall("call_1"))}}}
	ctx := WithSharedExecConfig(context.Background(), &ExecConfig{Provider: provider})

	result, err := Recorded(ctx, "chat.send", types.ContractPromptOnly, func(ctx context.Context) (llm.CompletionResponse, error) {
		return CallTurn(ctx, oneTurn("How warm is Oslo?"), callOptions())
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Value.ToolCalls) != 1 {
		t.Fatalf("the turn answered %+v", result.Value)
	}

	observer.mu.Lock()
	starts, finished := append([]spanStart(nil), observer.starts...), append([]telemetry.OperationResult(nil), observer.finish...)
	observer.mu.Unlock()

	if len(starts) != 2 || len(finished) != 2 {
		t.Fatalf("%d starts and %d finishes, want an operation and its request: %+v", len(starts), len(finished), starts)
	}
	operation, request := starts[0], starts[1]
	if operation.scope != telemetry.ScopeOperation || operation.operation != "chat.send" || operation.parent != 0 {
		t.Errorf("the first unit is %+v, want the operation chat.send with no parent", operation)
	}
	if request.scope != telemetry.ScopeRequest || request.parent != operation.id || request.operation != "chat.send" {
		t.Errorf("the second unit is %+v, want a request inside operation %d", request, operation.id)
	}
	for _, f := range finished {
		if f.ErrorKind != "" {
			t.Errorf("a unit finished with the kind %q", f.ErrorKind)
		}
	}
	for _, f := range finished {
		if f.Scope == telemetry.ScopeRequest && f.Attempts != 1 {
			t.Errorf("the request finished with %d attempts, want 1", f.Attempts)
		}
	}
}

func TestAFailedTurnIsReportedToTheObserversWithItsKind(t *testing.T) {
	observer := &spanObserver{}
	t.Cleanup(telemetry.AddObserver(observer))

	provider := &chatScript{steps: []chatStep{{err: types.Newf(types.KindInvalidRequest, "llm", "rejected")}}}
	ctx := WithSharedExecConfig(context.Background(), &ExecConfig{Provider: provider})
	_, err := Recorded(ctx, "chat.send", types.ContractPromptOnly, func(ctx context.Context) (llm.CompletionResponse, error) {
		return CallTurn(ctx, oneTurn("hi"), callOptions())
	})
	if err == nil {
		t.Fatal("the turn succeeded")
	}
	observer.mu.Lock()
	finished := append([]telemetry.OperationResult(nil), observer.finish...)
	observer.mu.Unlock()
	if len(finished) != 2 {
		t.Fatalf("%d finishes, want 2", len(finished))
	}
	for _, f := range finished {
		if f.ErrorKind == "" {
			t.Errorf("a %v unit of a failed turn finished with no kind", f.Scope)
		}
		if strings.Contains(fmt.Sprint(f), "rejected") {
			t.Errorf("a finish event carries the provider's text: %+v", f)
		}
	}
}

// The record of a call holds what the turn used and where it went.
func TestATurnLeavesTheRecordOfACall(t *testing.T) {
	provider := &chatScript{retries: 1, steps: []chatStep{
		{err: errors.New("rate limit exceeded: status 429")},
		{resp: llm.CompletionResponse{
			ToolCalls: []llm.ToolCall{weatherCall("call_1")}, FinishReason: "tool_calls",
			Model: "served-model", Provider: "served-by", ServiceTier: "priority",
			Usage: types.TokenUsage{PromptTokens: 120, CompletionTokens: 30, TotalTokens: 150, CachedTokens: 64},
		}},
	}}
	ctx := WithSharedExecConfig(context.Background(), &ExecConfig{Provider: provider})

	result, err := Recorded(ctx, "chat.send", types.ContractPromptOnly, func(ctx context.Context) (llm.CompletionResponse, error) {
		return CallTurn(ctx, oneTurn("How warm is Oslo?"), callOptions())
	})
	if err != nil {
		t.Fatal(err)
	}
	meta := result.Meta
	if meta.Operation != "chat.send" || meta.Attempts != 2 || meta.Retries != 1 {
		t.Errorf("operation %q, %d attempts, %d retries; want chat.send, 2 and 1", meta.Operation, meta.Attempts, meta.Retries)
	}
	if meta.Usage.PromptTokens != 120 || meta.Usage.CompletionTokens != 30 || meta.Usage.CachedTokens != 64 {
		t.Errorf("usage = %+v: what the provider reported was lost", meta.Usage)
	}
	if meta.Model != "served-model" || meta.Provider != "served-by" || meta.ServiceTier != "priority" {
		t.Errorf("model %q, provider %q, tier %q: who answered was lost", meta.Model, meta.Provider, meta.ServiceTier)
	}
	if meta.RequestID == "" {
		t.Error("the record carries no request id")
	}
	if meta.RequestedContract != types.ContractPromptOnly || meta.DeliveredContract != types.ContractPromptOnly {
		t.Errorf("contract %v requested, %v delivered, want prompt only for both", meta.RequestedContract, meta.DeliveredContract)
	}
	if meta.Elapsed < 0 {
		t.Error("the record carries a negative elapsed time")
	}
}
