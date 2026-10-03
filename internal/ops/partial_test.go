package ops

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/config/configtest"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/telemetry"
)

// Partial success and failure policies, and the per-item metrics computed on
// top of the same BatchResult.
//
// The fixture here is echoOp (op_kernel_seam_test.go) run over a slice of
// distinct strings: BuildPrompt sends the item itself as the user prompt
// (`"echo", input`), so a fake provider can decide per item, by prompt
// content, whether to fail -- there is no other way to target "item 3 of
// 10" specifically through the real call path.

// keyedProvider is a fake llm.Provider that fails for every UserPrompt in
// failing and otherwise echoes the prompt back as the completion body, with
// an optional priced Usage so a test can exercise the cost accounting
// without a live provider.
type keyedProvider struct {
	mu        sync.Mutex
	failing   map[string]bool
	failUntil map[string]int
	calls     map[string]int
	model     string
	priced    bool
	// failErr, when set, is the error a failing item returns instead of the
	// default untyped one, and onFail runs on every failure.
	failErr error
	onFail  func()
}

func newKeyedProvider(model string, priced bool, failing ...string) *keyedProvider {
	set := make(map[string]bool, len(failing))
	for _, f := range failing {
		set[f] = true
	}
	return &keyedProvider{failing: set, calls: map[string]int{}, model: model, priced: priced}
}

func (p *keyedProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.mu.Lock()
	p.calls[req.UserPrompt]++
	fail := p.failing[req.UserPrompt]
	if !fail && p.failUntil[req.UserPrompt] > 0 {
		// Transient failure: counts down toward success rather than failing
		// forever, for the retry-recovery tests.
		p.failUntil[req.UserPrompt]--
		fail = true
	}
	p.mu.Unlock()

	if fail {
		if p.onFail != nil {
			p.onFail()
		}
		if p.failErr != nil {
			return llm.CompletionResponse{}, p.failErr
		}
		return llm.CompletionResponse{}, fmt.Errorf("simulated provider failure for a keyed item")
	}

	usage := types.TokenUsage{}
	if p.priced {
		usage = types.TokenUsage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}
	}
	return llm.CompletionResponse{
		Content:      req.UserPrompt,
		Provider:     "keyed",
		Model:        p.model,
		FinishReason: "stop",
		Usage:        usage,
	}, nil
}

func (p *keyedProvider) Name() string                               { return "keyed" }
func (p *keyedProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *keyedProvider) RetryPolicy() (int, time.Duration)          { return 0, time.Millisecond }

func (p *keyedProvider) callCountFor(prompt string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[prompt]
}

// stopFailingAfter flips prompt from failing to succeeding, for the retry
// tests -- a transient failure that a retry pass should recover from.
func (p *keyedProvider) stopFailingAfter(prompt string, attemptsBeforeSuccess int) {
	// Wraps Complete's failure check with a counter, by replacing the
	// simple bool with a per-call countdown recorded in calls (already
	// counted there), checked lazily in Complete via failUntil.
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failUntil == nil {
		p.failUntil = map[string]int{}
	}
	p.failUntil[prompt] = attemptsBeforeSuccess
}

// causeText is the text of the innermost error in err's chain: what actually
// failed, which an engine keeps as the cause of an error whose own text is the
// library's.
func causeText(err error) string {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return err.Error()
		}
		err = next
	}
}

func indexedItems(n int) []string {
	items := make([]string, n)
	for i := range items {
		items[i] = fmt.Sprintf("item-%02d", i)
	}
	return items
}

// --- the five policies each behave differently on the same input.
//
// Every test in this block runs the identical input -- ten items, item 3
// ("item-03") fails every time it is attempted -- so the policy is the only
// variable, and the table of outcomes is the "each policy behaves
// differently from the others" bar the task sets.

func TestRunOpManyPartialCollectFailuresReportsEachItem(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
	items := indexedItems(10)
	provider := newKeyedProvider("fake-model", false, "item-03")
	ctx := WithProvider(context.Background(), provider)

	result, err := RunOpManyPartial(ctx, echoOp(), items, types.OpOptions{},
		PartialConfig{Policy: types.PolicyCollectFailures, Concurrency: 1})
	if err != nil {
		t.Fatalf("PolicyCollectFailures returned an error: %v", err)
	}
	if result.Summary.Succeeded != 9 || result.Summary.Failed != 1 || result.Summary.Cancelled != 0 {
		t.Fatalf("Summary = %+v, want 9 succeeded, 1 failed, 0 cancelled", result.Summary)
	}
	if result.Complete() {
		t.Fatal("Complete() = true with one item failed")
	}
	failures := result.Failures()
	if len(failures) != 1 || failures[0].Index != 3 {
		t.Fatalf("Failures() = %+v, want exactly index 3", failures)
	}
	if failures[0].Err == nil {
		t.Fatal("the failed item's Err is nil")
	}
	// Every other item ran (not cancelled) -- CollectFailures' whole point.
	for i, item := range result.Items {
		if i == 3 {
			continue
		}
		if item.Status != types.ItemSucceeded {
			t.Fatalf("item %d status = %s, want succeeded (CollectFailures runs every item)", i, item.Status)
		}
	}
}

func TestRunOpManyPartialFailFastCancelsRemaining(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
	items := indexedItems(10)
	provider := newKeyedProvider("fake-model", false, "item-03")
	ctx := WithProvider(context.Background(), provider)

	// Concurrency 1: dispatch is strictly sequential, so "item 3 fails" has
	// one deterministic consequence -- items 0-2 already ran and succeeded,
	// item 3 failed, items 4-9 were never dispatched.
	result, err := RunOpManyPartial(ctx, echoOp(), items, types.OpOptions{},
		PartialConfig{Policy: types.PolicyFailFast, Concurrency: 1})
	if err == nil {
		t.Fatal("PolicyFailFast returned no error with a terminal failure")
	}
	if result.Summary.Succeeded != 3 || result.Summary.Failed != 1 || result.Summary.Cancelled != 6 {
		t.Fatalf("Summary = %+v, want 3 succeeded, 1 failed, 6 cancelled", result.Summary)
	}
	for i := 0; i < 3; i++ {
		if result.Items[i].Status != types.ItemSucceeded {
			t.Fatalf("item %d status = %s, want succeeded", i, result.Items[i].Status)
		}
	}
	if result.Items[3].Status != types.ItemFailed {
		t.Fatalf("item 3 status = %s, want failed", result.Items[3].Status)
	}
	for i := 4; i < 10; i++ {
		if result.Items[i].Status != types.ItemCancelled {
			t.Fatalf("item %d status = %s, want cancelled (never started)", i, result.Items[i].Status)
		}
		if result.Items[i].Err == nil {
			t.Fatalf("cancelled item %d has no Err explaining why", i)
		}
	}
}

func TestRunOpManyPartialRequireAllRunsEveryItemButFails(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
	items := indexedItems(10)
	provider := newKeyedProvider("fake-model", false, "item-03")
	ctx := WithProvider(context.Background(), provider)

	result, err := RunOpManyPartial(ctx, echoOp(), items, types.OpOptions{},
		PartialConfig{Policy: types.PolicyRequireAll, Concurrency: 1})
	if err == nil {
		t.Fatal("PolicyRequireAll returned no error with one item failed")
	}
	// The distinguishing property versus FailFast: every item ran.
	if result.Summary.Succeeded != 9 || result.Summary.Failed != 1 || result.Summary.Cancelled != 0 {
		t.Fatalf("Summary = %+v, want 9 succeeded, 1 failed, 0 cancelled (RequireAll runs every item)", result.Summary)
	}
}

func TestRunOpManyPartialRetryFailedItemsRecoversTransientFailure(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
	items := indexedItems(5)
	provider := newKeyedProvider("fake-model", false) // nothing fails permanently
	provider.stopFailingAfter("item-02", 1)           // fails once, then succeeds
	ctx := WithProvider(context.Background(), provider)

	result, err := RunOpManyPartial(ctx, echoOp(), items, types.OpOptions{},
		PartialConfig{Policy: types.PolicyRetryFailedItems, Concurrency: 2, MaxRetries: 2})
	if err != nil {
		t.Fatalf("PolicyRetryFailedItems returned an error for a recoverable failure: %v", err)
	}
	if !result.Complete() {
		t.Fatalf("Complete() = false, want true once the retry pass recovered the item: %+v", result.Summary)
	}
	if result.Items[2].Status != types.ItemSucceeded {
		t.Fatalf("item 2 status = %s, want succeeded after retry", result.Items[2].Status)
	}
	// Meta.Attempts is read from CallLLM's own published records
	// (llm_helper.go, out of this task's scope to change), and CallLLM only
	// publishes a record for a call that returned a body -- a call that
	// exhausted its own internal retries without success publishes nothing.
	// So the failed first pass contributes 0 to Meta.Attempts and only the
	// recovering retry's successful call contributes 1; the provider's own
	// call count (checked below) is what actually proves two calls happened.
	if result.Items[2].Attempts < 1 {
		t.Fatalf("item 2 Attempts = %d, want at least 1 (the recovering call)", result.Items[2].Attempts)
	}
	if provider.callCountFor("item-02") < 2 {
		t.Fatalf("provider was called %d time(s) for item-02, want at least 2", provider.callCountFor("item-02"))
	}
}

func TestRunOpManyPartialRetryFailedItemsStillFailsAfterExhaustingRetries(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
	items := indexedItems(5)
	provider := newKeyedProvider("fake-model", false, "item-02") // permanent failure
	ctx := WithProvider(context.Background(), provider)

	result, err := RunOpManyPartial(ctx, echoOp(), items, types.OpOptions{},
		PartialConfig{Policy: types.PolicyRetryFailedItems, Concurrency: 2, MaxRetries: 2})
	if err == nil {
		t.Fatal("PolicyRetryFailedItems returned no error though the item never recovered")
	}
	if result.Items[2].Status != types.ItemFailed {
		t.Fatalf("item 2 status = %s, want failed", result.Items[2].Status)
	}
	// One initial pass + 2 retries = 3 provider calls for this item.
	if got := provider.callCountFor("item-02"); got != 3 {
		t.Fatalf("provider was called %d time(s) for item-02, want 3 (1 initial + 2 retries)", got)
	}
	// Attempts matches the provider's own count, which is the ground truth
	// checked just above.
	//
	// This assertion used to require **zero**, with a comment explaining that
	// CallLLM published a call record only for a call that returned a body --
	// so an item that burned its whole retry budget reported as though the
	// provider had never been called. That was a real defect in the shared
	// envelope, not a limitation to encode: it made "how many attempts did
	// that take" answerable only for the requests that worked. `publishFailedCall`
	// fixes it (see failedattempts_test.go), and this now asserts the truth
	// instead of pinning the bug.
	if got := result.Items[2].Attempts; got != 3 {
		t.Fatalf("item 2 Attempts = %d, want 3 to match the provider's own call count", got)
	}
}

func TestRunOpManyPartialRetryThenCollectNeverErrors(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
	items := indexedItems(5)
	provider := newKeyedProvider("fake-model", false, "item-02") // permanent failure
	ctx := WithProvider(context.Background(), provider)

	result, err := RunOpManyPartial(ctx, echoOp(), items, types.OpOptions{},
		PartialConfig{Policy: types.PolicyRetryThenCollect, Concurrency: 2, MaxRetries: 2})
	if err != nil {
		t.Fatalf("PolicyRetryThenCollect returned an error: %v (its contract is to never fail the call)", err)
	}
	if result.Complete() {
		t.Fatal("Complete() = true though item 2 never recovered")
	}
	if result.Summary.Failed != 1 {
		t.Fatalf("Summary.Failed = %d, want 1", result.Summary.Failed)
	}
	// Same retry effort as PolicyRetryFailedItems -- the two differ only in
	// the return, not in what they attempted.
	if got := provider.callCountFor("item-02"); got != 3 {
		t.Fatalf("provider was called %d time(s) for item-02, want 3 (1 initial + 2 retries)", got)
	}
}

func TestRunOpManyPartialRetryPassSkipsAFailureRepeatingCannotCure(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantCalls int
	}{
		{"policy violation", types.Newf(types.KindPolicyViolation, "x", "refused"), 1},
		{"invalid request", types.Newf(types.KindInvalidRequest, "x", "malformed"), 1},
		{"budget exceeded", types.Newf(types.KindBudgetExceeded, "x", "spent"), 1},
		{"authentication", types.Newf(types.KindAuthentication, "x", "denied"), 1},
		{"provider unavailable", types.Newf(types.KindProviderUnavailable, "x", "down"), 3},
		{"rate limited", types.Newf(types.KindRateLimited, "x", "slow down"), 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
			provider := newKeyedProvider("fake-model", false, "item-00")
			provider.failErr = tc.err
			ctx := WithProvider(context.Background(), provider)

			result, err := RunOpManyPartial(ctx, echoOp(), indexedItems(2), types.OpOptions{},
				PartialConfig{Policy: types.PolicyRetryThenCollect, Concurrency: 1, MaxRetries: 2})
			if err != nil {
				t.Fatalf("PolicyRetryThenCollect returned an error: %v", err)
			}
			if got := provider.callCountFor("item-00"); got != tc.wantCalls {
				t.Errorf("the failing item was sent %d time(s), want %d", got, tc.wantCalls)
			}
			if got := provider.callCountFor("item-01"); got != 1 {
				t.Errorf("the item that succeeded was sent %d time(s), want 1", got)
			}
			if result.Items[0].Status != types.ItemFailed {
				t.Errorf("item 0 status = %s", result.Items[0].Status)
			}
		})
	}
}

func TestRunOpManyPartialRetryPassStopsWhenTheContextEnds(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	provider := newKeyedProvider("fake-model", false, "item-00", "item-01", "item-02")
	provider.onFail = cancel // the caller gives up while the first pass is failing
	ctx = WithProvider(ctx, provider)

	result, err := RunOpManyPartial(ctx, echoOp(), indexedItems(3), types.OpOptions{},
		PartialConfig{Policy: types.PolicyRetryThenCollect, Concurrency: 1, MaxRetries: 3})
	if err != nil {
		t.Fatalf("PolicyRetryThenCollect returned an error: %v", err)
	}
	total := 0
	for _, item := range indexedItems(3) {
		total += provider.callCountFor(item)
	}
	if total > 3 {
		t.Errorf("the provider was called %d times; a cancelled run must not start a retry round", total)
	}
	if result.Summary.Failed+result.Summary.Cancelled+result.Summary.Succeeded != 3 {
		t.Errorf("Summary = %+v", result.Summary)
	}
	// The call count above cannot tell a run that stopped from one that started a
	// round and had it refused: the item keeps the failure it had, and the one
	// attempt it made.
	first := result.Items[0]
	if first.Err == nil || !strings.Contains(causeText(first.Err), "simulated provider failure") {
		t.Errorf("item 0 lost the failure it had: Err = %v; a cancelled run must not start a retry round that overwrites it", first.Err)
	}
	if first.Attempts != 1 {
		t.Errorf("item 0 Attempts = %d, want 1", first.Attempts)
	}
}

// With several workers, a failure cancels what has not started and what is in
// flight, and every item ends in a state: none is left unresolved.
func TestRunOpManyPartialFailFastWithSeveralWorkersResolvesEveryItem(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
	for _, concurrency := range []int{2, 4, 8} {
		t.Run(fmt.Sprintf("concurrency %d", concurrency), func(t *testing.T) {
			items := indexedItems(40)
			provider := newKeyedProvider("fake-model", false, "item-05")
			provider.failErr = types.Newf(types.KindPolicyViolation, "x", "refused")
			ctx := WithProvider(context.Background(), provider)

			result, err := RunOpManyPartial(ctx, echoOp(), items, types.OpOptions{},
				PartialConfig{Policy: types.PolicyFailFast, Concurrency: concurrency})
			if err == nil {
				t.Fatal("PolicyFailFast returned no error though an item failed")
			}
			if len(result.Items) != len(items) {
				t.Fatalf("got %d results for %d items", len(result.Items), len(items))
			}
			if result.Items[5].Status != types.ItemFailed {
				t.Errorf("item 5 status = %s, want failed", result.Items[5].Status)
			}
			for i, item := range result.Items {
				switch item.Status {
				case types.ItemSucceeded, types.ItemFailed, types.ItemCancelled:
				default:
					t.Errorf("item %d was left %s", i, item.Status)
				}
			}
			if got := result.Summary.Succeeded + result.Summary.Failed + result.Summary.Cancelled; got != len(items) {
				t.Errorf("Summary = %+v accounts for %d of %d items", result.Summary, got, len(items))
			}
		})
	}
}

// A nil context is the caller's mistake, and reading it as Background would run
// the whole batch with no way to cancel it. It used to be read that way, and this
// test pinned it. It is refused as an invalid request, with nothing sent and no
// result beside the error, for every policy and whatever the items.
func TestRunOpManyPartialRefusesANilContext(t *testing.T) {
	policies := []types.BatchPolicy{types.PolicyUnspecified, types.PolicyFailFast, types.PolicyCollectFailures,
		types.PolicyRetryFailedItems, types.PolicyRetryThenCollect, types.PolicyRequireAll}
	for _, policy := range policies {
		for _, count := range []int{0, 2} {
			t.Run(fmt.Sprintf("%s over %d items", policy, count), func(t *testing.T) {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("RunOpManyPartial panicked on a nil context: %v", r)
					}
				}()
				provider := newKeyedProvider("fake-model", false)
				var noContext context.Context
				result, err := RunOpManyPartial(noContext, echoOp(), indexedItems(count), types.OpOptions{},
					PartialConfig{Policy: policy, Concurrency: 1, MaxRetries: 1})
				if types.KindOf(err) != types.KindInvalidRequest || !errors.Is(err, types.ErrInvalidRequest) {
					t.Fatalf("err = %v, want an invalid request", err)
				}
				requireNoBatchResult(t, result)
				if calls := provider.callCountFor("item-00"); calls != 0 {
					t.Errorf("%d requests were sent", calls)
				}
			})
		}
	}
}

// requireNoBatchResult fails unless result is the empty result a refusal returns
// beside its error.
func requireNoBatchResult[Out any](t *testing.T, result types.BatchResult[Out]) {
	t.Helper()
	if result.Items != nil || result.Summary != (types.BatchSummary{}) || len(result.Ledger.Entries) != 0 {
		t.Errorf("a result came back beside the error: %+v %+v", result.Summary, result.Items)
	}
}

// A call the descriptor cannot be run for is refused before anything is sent,
// and the result that comes back beside the error is empty. The first two used
// to return a result with the policy resolved and the total filled in, beside
// an error nobody had classified.
func TestRunOpManyPartialRefusesAnUnusableCallBeforeSendingAnything(t *testing.T) {
	noPrompt := func(op *Op[string, string]) { op.BuildPrompt = nil }
	noDecode := func(op *Op[string, string]) { op.Contract.Decode = nil }
	cases := []struct {
		name   string
		break_ func(*Op[string, string])
		policy types.BatchPolicy
	}{
		{"no BuildPrompt, unspecified policy", noPrompt, types.PolicyUnspecified},
		{"no BuildPrompt, fail fast", noPrompt, types.PolicyFailFast},
		{"no BuildPrompt, collect failures", noPrompt, types.PolicyCollectFailures},
		{"no BuildPrompt, retry failed items", noPrompt, types.PolicyRetryFailedItems},
		{"no BuildPrompt, retry then collect", noPrompt, types.PolicyRetryThenCollect},
		{"no BuildPrompt, require all", noPrompt, types.PolicyRequireAll},
		{"no Decode, unspecified policy", noDecode, types.PolicyUnspecified},
		{"no Decode, fail fast", noDecode, types.PolicyFailFast},
		{"no Decode, collect failures", noDecode, types.PolicyCollectFailures},
		{"no Decode, require all", noDecode, types.PolicyRequireAll},
		{"a policy that does not exist", func(*Op[string, string]) {}, types.BatchPolicy(99)},
		{"another policy that does not exist", func(*Op[string, string]) {}, types.BatchPolicy(7)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := newKeyedProvider("fake-model", false)
			ctx := WithProvider(context.Background(), provider)
			op := echoOp()
			tc.break_(&op)

			result, err := RunOpManyPartial(ctx, op, indexedItems(3), types.OpOptions{}, PartialConfig{Policy: tc.policy})
			if types.KindOf(err) != types.KindConfiguration {
				t.Fatalf("err = %v, want a configuration error", err)
			}
			requireNoBatchResult(t, result)
			for _, item := range indexedItems(3) {
				if provider.callCountFor(item) != 0 {
					t.Errorf("a request was sent for %s", item)
				}
			}
		})
	}
}

// A floor the descriptor cannot reach is refused before anything is sent, under
// every policy. Each item used to refuse for itself, so a run that collects its
// failures came back with every item failed and no error.
func TestRunOpManyPartialRefusesAFloorItCannotReachBeforeSending(t *testing.T) {
	for _, policy := range []types.BatchPolicy{types.PolicyFailFast, types.PolicyCollectFailures, types.PolicyRetryFailedItems,
		types.PolicyRetryThenCollect, types.PolicyRequireAll, types.PolicyUnspecified} {
		t.Run(policy.String(), func(t *testing.T) {
			provider := newKeyedProvider("fake-model", false)
			ctx := WithContractPolicy(WithProvider(context.Background(), provider), types.DataPolicy{MinimumContract: types.ContractFullyGoverned})
			result, err := RunOpManyPartial(ctx, echoOp(), indexedItems(3), types.OpOptions{}, PartialConfig{Policy: policy})
			if types.KindOf(err) != types.KindPolicyViolation {
				t.Fatalf("err = %v, want a policy violation", err)
			}
			requireNoBatchResult(t, result)
			if calls := provider.callCountFor("item-00") + provider.callCountFor("item-01") + provider.callCountFor("item-02"); calls != 0 {
				t.Errorf("%d requests were sent", calls)
			}
		})
	}
}

// Every error that leaves the call is classified, and nothing a caller can read
// names a task or a protocol's old name: the summary's reason, the call's error,
// and each item's.
func TestRunOpManyPartialErrorsAreClassifiedAndCarryNoTicketOrJargon(t *testing.T) {
	// want is the kind the call's error has to carry, or KindUnknown for any kind
	// at all: a provider's plain error is classified by the call path, which
	// decides what.
	cases := []struct {
		name   string
		policy types.BatchPolicy
		fail   error
		over   bool
		want   types.ErrorKind
	}{
		{"fail fast, an error of no kind", types.PolicyFailFast, nil, false, types.KindUnknown},
		{"require all, an error of no kind", types.PolicyRequireAll, nil, false, types.KindUnknown},
		{"retry failed items, an error of no kind", types.PolicyRetryFailedItems, nil, false, types.KindUnknown},
		{"fail fast, a refused answer", types.PolicyFailFast, types.Newf(types.KindPolicyViolation, "x", "refused"), false, types.KindPolicyViolation},
		{"require all, a refused answer", types.PolicyRequireAll, types.Newf(types.KindPolicyViolation, "x", "refused"), false, types.KindPolicyViolation},
		{"retry failed items, a malformed answer", types.PolicyRetryFailedItems, types.Newf(types.KindMalformedOutput, "x", "bad"), false, types.KindMalformedOutput},
		{"fail fast, a context that was over", types.PolicyFailFast, nil, true, types.KindCanceled},
		{"require all, a context that was over", types.PolicyRequireAll, nil, true, types.KindCanceled},
		{"retry failed items, a context that was over", types.PolicyRetryFailedItems, nil, true, types.KindCanceled},
		{"unspecified, a context that was over", types.PolicyUnspecified, nil, true, types.KindCanceled},
		{"fail fast, a rate limit", types.PolicyFailFast, types.Newf(types.KindRateLimited, "x", "slow"), false, types.KindRateLimited},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
			provider := newKeyedProvider("fake-model", false, "item-01")
			provider.failErr = tc.fail
			base := context.Background()
			if tc.over {
				var cancel context.CancelFunc
				base, cancel = context.WithCancel(base)
				cancel()
			}
			result, err := RunOpManyPartial(WithProvider(base, provider), echoOp(), indexedItems(4), types.OpOptions{},
				PartialConfig{Policy: tc.policy, Concurrency: 1, MaxRetries: 1})
			if err == nil {
				t.Fatal("no error")
			}
			got := types.KindOf(err)
			switch {
			case tc.want == types.KindUnknown && got == types.KindUnknown:
				t.Errorf("the call's error is unclassified: %v", err)
			case tc.want != types.KindUnknown && got != tc.want:
				t.Errorf("kind = %v, want %v (%v)", got, tc.want, err)
			}
			if _, isOperationError := err.(*types.OperationError); !isOperationError {
				t.Errorf("the error is a %T and not an *OperationError: %v", err, err)
			}
			texts := []string{err.Error(), result.Summary.PolicyReason}
			for _, item := range result.Items {
				if item.Err != nil {
					texts = append(texts, item.Err.Error())
				}
			}
			for _, text := range texts {
				requireNoTicketOrJargon(t, text)
			}
		})
	}
}

// requireNoTicketOrJargon fails when text names a task, or a protocol by the name
// the code that implements it gave it.
func requireNoTicketOrJargon(t *testing.T, text string) {
	t.Helper()
	lowered := strings.ToLower(text)
	for _, word := range []string{"pl-0", "pl0", "mdsp", "ticket"} {
		if strings.Contains(lowered, word) {
			t.Errorf("%q names %q, which a caller cannot act on", text, word)
		}
	}
}

// A failed or cancelled item holds the zero value and not an answer left over
// from somewhere: its Value is read by callers who skipped the status.
func TestRunOpManyPartialFailedItemsHoldNoValue(t *testing.T) {
	for _, policy := range []types.BatchPolicy{types.PolicyUnspecified, types.PolicyFailFast, types.PolicyCollectFailures,
		types.PolicyRetryFailedItems, types.PolicyRetryThenCollect, types.PolicyRequireAll} {
		for _, workers := range []int{1, 3} {
			t.Run(fmt.Sprintf("%s with %d workers", policy, workers), func(t *testing.T) {
				configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
				provider := newKeyedProvider("fake-model", false, "item-02", "item-05")
				result, _ := RunOpManyPartial(WithProvider(context.Background(), provider), echoOp(), indexedItems(8), types.OpOptions{},
					PartialConfig{Policy: policy, Concurrency: workers, MaxRetries: 1})
				failed := 0
				for i, item := range result.Items {
					if item.Status == types.ItemSucceeded {
						if item.Value == "" {
							t.Errorf("item %d succeeded with no value", i)
						}
						continue
					}
					failed++
					if item.Value != "" {
						t.Errorf("item %d (%s) holds %q beside its failure", i, item.Status, item.Value)
					}
				}
				if failed == 0 {
					t.Error("no item failed, so nothing was checked")
				}
			})
		}
	}
}

// One call of an operation is one unit to an observer, with the requests it makes
// as its children: a tracing observer sees the run and not only its requests. Every
// item used to open a unit of its own, so a run of five was five operations.
func TestRunOpManyPartialIsOneOperationToAnObserver(t *testing.T) {
	for _, policy := range []types.BatchPolicy{types.PolicyFailFast, types.PolicyCollectFailures, types.PolicyRetryFailedItems,
		types.PolicyRetryThenCollect, types.PolicyRequireAll} {
		for _, failing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s failing=%v", policy, failing), func(t *testing.T) {
				configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
				collector := installCollector(t)
				provider := newKeyedProvider("fake-model", false)
				if failing {
					provider.failing["item-02"] = true
					provider.failErr = types.Newf(types.KindPolicyViolation, "x", "refused")
				}
				_, _ = RunOpManyPartial(WithProvider(context.Background(), provider), echoOp(), indexedItems(5), types.OpOptions{},
					PartialConfig{Policy: policy, Concurrency: 1, MaxRetries: 1})

				started, finished := collector.snapshot()
				operations := scopes(started, telemetry.ScopeOperation, eventScope)
				if len(operations) != 1 || operations[0].Operation != "callerEcho" {
					t.Fatalf("%d operation units started, want one named for the operation that covers the whole run", len(operations))
				}
				if ended := scopes(finished, telemetry.ScopeOperation, resultScope); len(ended) != 1 {
					t.Fatalf("%d operation units ended, want 1", len(ended))
				}
				if started[0].Scope != telemetry.ScopeOperation {
					t.Error("a request started before the operation did")
				}
				if requests := scopes(started, telemetry.ScopeRequest, eventScope); len(requests) < 3 {
					t.Errorf("only %d requests were seen inside the operation", len(requests))
				}
			})
		}
	}
}

func TestRunOpManyPartialEmptyItemsReturnsEmptyCompleteResult(t *testing.T) {
	result, err := RunOpManyPartial[string, string](context.Background(), echoOp(), nil, types.OpOptions{}, PartialConfig{})
	if err != nil {
		t.Fatalf("empty input returned an error: %v", err)
	}
	if result.Summary.Total != 0 || len(result.Items) != 0 {
		t.Fatalf("result = %+v, want an empty, total-zero result", result)
	}
	if !result.Complete() {
		t.Fatal("an empty batch should read as Complete")
	}
}

// A policy left unspecified is one policy whatever the number of items. It used
// to resolve to fail-fast up to twenty items and to retry-then-collect above, so a
// program that got an error for one failed item at 20 items got a nil error and a
// shorter result at 21.
func TestRunOpManyPartialUnspecifiedPolicyIsCollectAtEveryLength(t *testing.T) {
	for _, n := range []int{1, 2, 5, 19, 20, 21, 22, 40} {
		t.Run(fmt.Sprintf("%d items", n), func(t *testing.T) {
			configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
			items := indexedItems(n)
			provider := newKeyedProvider("fake-model", false, "item-00")
			ctx := WithProvider(context.Background(), provider)

			result, err := RunOpManyPartial(ctx, echoOp(), items, types.OpOptions{}, PartialConfig{Concurrency: 1})
			if err != nil {
				t.Fatalf("an unspecified policy returned an error at %d items: %v", n, err)
			}
			if result.Summary.Policy != types.PolicyCollectFailures {
				t.Fatalf("Summary.Policy = %s, want collect_failures at %d items", result.Summary.Policy, n)
			}
			if result.Summary.PolicyReason == "" {
				t.Fatal("PolicyReason is empty for a policy that was resolved and not specified")
			}
			if result.Summary.Failed != 1 || result.Summary.Cancelled != 0 || result.Summary.Succeeded != n-1 {
				t.Fatalf("Summary = %+v, want 1 failed and %d succeeded at %d items, nothing cancelled", result.Summary, n-1, n)
			}
			if got := provider.callCountFor("item-00"); got != 1 {
				t.Fatalf("the failing item was sent %d times, want 1: collecting does not retry", got)
			}
		})
	}
}

// --- per-item metrics, computed from the same BatchResult.

func TestBatchMetricsValidRatioAndCostPerAcceptedItem(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "gpt-4")
	items := indexedItems(4)
	provider := newKeyedProvider("gpt-4", true, "item-01") // priced model, one permanent failure
	ctx := WithProvider(context.Background(), provider)

	result, err := RunOpManyPartial(ctx, echoOp(), items, types.OpOptions{},
		PartialConfig{Policy: types.PolicyCollectFailures, Concurrency: 1})
	if err != nil {
		t.Fatalf("RunOpManyPartial: %v", err)
	}

	metrics := result.Metrics()
	if metrics.Total != 4 || metrics.Succeeded != 3 || metrics.Failed != 1 {
		t.Fatalf("metrics = %+v, want Total 4, Succeeded 3, Failed 1", metrics)
	}
	if metrics.ValidItemRatio != 0.75 {
		t.Fatalf("ValidItemRatio = %v, want 0.75 (3/4)", metrics.ValidItemRatio)
	}
	if !metrics.CostPriced {
		t.Fatal("CostPriced = false against gpt-4, a model with a rate card entry")
	}
	if metrics.AcceptedCost <= 0 {
		t.Fatalf("AcceptedCost = %v, want > 0 across 3 succeeded, priced items", metrics.AcceptedCost)
	}
	if metrics.FailedCost != 0 {
		// item-01 fails on the transport call itself (provider.Complete
		// returns an error before any Usage/CostInfo exists), so nothing
		// was priced for it -- FailedCost is 0, not a fabricated figure.
		t.Fatalf("FailedCost = %v, want 0 (the failure never produced a priced response)", metrics.FailedCost)
	}
	wantPerItem := metrics.AcceptedCost / 3
	if metrics.CostPerAcceptedItem != wantPerItem {
		t.Fatalf("CostPerAcceptedItem = %v, want %v (AcceptedCost / Succeeded)", metrics.CostPerAcceptedItem, wantPerItem)
	}
}

func TestBatchMetricsUnpricedModelReportsUnpriced(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "definitely-not-a-real-model-xyz")
	items := indexedItems(3)
	provider := newKeyedProvider("definitely-not-a-real-model-xyz", false)
	ctx := WithProvider(context.Background(), provider)

	result, err := RunOpManyPartial(ctx, echoOp(), items, types.OpOptions{},
		PartialConfig{Policy: types.PolicyCollectFailures, Concurrency: 1})
	if err != nil {
		t.Fatalf("RunOpManyPartial: %v", err)
	}
	metrics := result.Metrics()
	if metrics.CostPriced {
		t.Fatal("CostPriced = true for a model with no rate-card entry")
	}
	if metrics.CostPerAcceptedItem != 0 {
		t.Fatalf("CostPerAcceptedItem = %v on an unpriced batch, want 0 (unset, not free)", metrics.CostPerAcceptedItem)
	}
}

// --- BatchResult.Values() forces the completeness check, the
// concrete case the task's Verify line names: "a caller cannot mistake a
// partial for a complete one."

func TestBatchResultValuesReportsIncompleteRatherThanPadding(t *testing.T) {
	r := types.BatchResult[string]{
		Items: []types.ItemResult[string]{
			{Index: 0, Status: types.ItemSucceeded, Value: "a"},
			{Index: 1, Status: types.ItemFailed, Err: errors.New("boom")},
			{Index: 2, Status: types.ItemSucceeded, Value: "c"},
		},
		Summary: types.BatchSummary{Total: 3, Succeeded: 2, Failed: 1},
	}

	values, complete := r.Values()
	if complete {
		t.Fatal("Values() reported complete with a failed item present")
	}
	// The honestly-short slice: two values, not three, and never a zero
	// value standing in for the failed one.
	if len(values) != 2 || values[0] != "a" || values[1] != "c" {
		t.Fatalf("Values() = %v, want [a c] (the two succeeded values, in order, no padding)", values)
	}
}

func TestBatchResultValuesReportsCompleteWhenEverySucceeded(t *testing.T) {
	r := types.BatchResult[int]{
		Items: []types.ItemResult[int]{
			{Index: 0, Status: types.ItemSucceeded, Value: 10},
			{Index: 1, Status: types.ItemSucceeded, Value: 20},
		},
		Summary: types.BatchSummary{Total: 2, Succeeded: 2},
	}
	values, complete := r.Values()
	if !complete {
		t.Fatal("Values() reported incomplete though every item succeeded")
	}
	if len(values) != 2 || values[0] != 10 || values[1] != 20 {
		t.Fatalf("Values() = %v, want [10 20]", values)
	}
}

func TestBatchResultFailuresListsOnlyNonSucceeded(t *testing.T) {
	r := types.BatchResult[string]{
		Items: []types.ItemResult[string]{
			{Index: 0, Status: types.ItemSucceeded, Value: "ok"},
			{Index: 1, Status: types.ItemFailed, Err: errors.New("bad")},
			{Index: 2, Status: types.ItemCancelled, Err: context.Canceled},
		},
		Summary: types.BatchSummary{Total: 3, Succeeded: 1, Failed: 1, Cancelled: 1},
	}
	failures := r.Failures()
	if len(failures) != 2 {
		t.Fatalf("Failures() returned %d item(s), want 2", len(failures))
	}
	if failures[0].Index != 1 || failures[1].Index != 2 {
		t.Fatalf("Failures() = %+v, want indices [1 2] in input order", failures)
	}
}

// --- addCost carries Quality/PricingSource forward, not just
// Priced/TotalCost, so a retried item's summed CostInfo never ends up
// Priced == true with an unrecognized zero-value Quality -- a state a
// caller's Cost.Quality.Known() check would misread.

func TestAddCostCarriesQualityAndPricingSourceFromThePricedSide(t *testing.T) {
	prior := types.CostInfo{Priced: true, Currency: "USD", TotalCost: 0.25, Quality: types.PricingExact, PricingSource: "gpt-4"}
	retry := types.CostInfo{Priced: true, Currency: "USD", TotalCost: 0.25, Quality: types.PricingExact, PricingSource: "gpt-4"}

	sum := addCost(prior, retry)

	if sum.TotalCost != 0.5 {
		t.Fatalf("TotalCost = %v, want 0.5", sum.TotalCost)
	}
	if !sum.Priced {
		t.Fatal("Priced = false summing two priced CostInfo values")
	}
	if sum.Quality != types.PricingExact {
		t.Fatalf("Quality = %q, want PricingExact -- summing two priced attempts must not reset it to the zero value", sum.Quality)
	}
	if sum.PricingSource != "gpt-4" {
		t.Fatalf("PricingSource = %q, want %q", sum.PricingSource, "gpt-4")
	}
}

func TestAddCostOnePricedSideCarriesThatSidesQualityForward(t *testing.T) {
	// The shape a retry pass actually produces: a first attempt that never
	// returned a priced usage record (a call that errored before any body
	// came back -- outcomeFrom's own doc comment) summed with a recovering
	// attempt that did.
	unpriced := types.CostInfo{}
	priced := types.CostInfo{Priced: true, Currency: "USD", TotalCost: 0.20, Quality: types.PricingEstimated, PricingSource: "fake-model"}

	sum := addCost(unpriced, priced)
	if !sum.Priced || sum.TotalCost != 0.20 {
		t.Fatalf("sum = %+v, want Priced true and TotalCost 0.20", sum)
	}
	if sum.Quality != types.PricingEstimated {
		t.Fatalf("Quality = %q, want PricingEstimated, carried from the priced side", sum.Quality)
	}
	if sum.PricingSource != "fake-model" {
		t.Fatalf("PricingSource = %q, want %q", sum.PricingSource, "fake-model")
	}

	// Order must not matter: the priced side's Quality wins whichever
	// argument position it is in.
	sum2 := addCost(priced, unpriced)
	if sum2.Quality != types.PricingEstimated || sum2.PricingSource != "fake-model" {
		t.Fatalf("addCost(priced, unpriced) = %+v, want the same priced-side Quality/PricingSource regardless of argument order", sum2)
	}
}

func TestAddCostBothUnpricedReturnsZeroValueNotAFabricatedPricedResult(t *testing.T) {
	sum := addCost(types.CostInfo{}, types.CostInfo{})
	if sum.Priced {
		t.Fatal("Priced = true summing two unpriced CostInfo values")
	}
	if sum.Quality != "" {
		t.Fatalf("Quality = %q, want the zero value for a wholly unpriced sum", sum.Quality)
	}
}

func TestBatchPolicyStringNamesEveryPolicy(t *testing.T) {
	cases := map[types.BatchPolicy]string{
		types.PolicyUnspecified:      "unspecified",
		types.PolicyFailFast:         "fail_fast",
		types.PolicyCollectFailures:  "collect_failures",
		types.PolicyRetryFailedItems: "retry_failed_items",
		types.PolicyRetryThenCollect: "retry_then_collect",
		types.PolicyRequireAll:       "require_all",
	}
	for policy, want := range cases {
		if got := policy.String(); got != want {
			t.Fatalf("%v.String() = %q, want %q", policy, got, want)
		}
	}
}

// --- Items run on a bounded pool, the bound is the scheduler's when there is
// one, and a call that panics is an error for its own item.

func partialItems(n int) []string {
	items := make([]string, n)
	for i := range items {
		items[i] = fmt.Sprintf("widget-%03d", i)
	}
	return items
}

func TestRunOpManyPartialDefaultBoundComesFromTheClientsScheduler(t *testing.T) {
	cases := []struct {
		name     string
		ctx      func(*testing.T, *gatedProvider) context.Context
		workers  int
		wantPeak int
	}{
		{"no scheduler", func(_ *testing.T, p *gatedProvider) context.Context { return gatedContext(p) }, 0, DefaultConcurrency},
		{"an explicit number wins", func(_ *testing.T, p *gatedProvider) context.Context { return gatedContext(p) }, 2, 2},
		{"a scheduler of one", func(t *testing.T, p *gatedProvider) context.Context {
			ctx, _ := schedulerContext(t, p, SchedulerLimits{MaxConcurrent: 1})
			return ctx
		}, 0, 1},
		{"a scheduler of seven", func(t *testing.T, p *gatedProvider) context.Context {
			ctx, _ := schedulerContext(t, p, SchedulerLimits{MaxConcurrent: 7})
			return ctx
		}, 0, 7},
	}
	for _, policy := range []types.BatchPolicy{types.PolicyCollectFailures, types.PolicyFailFast, types.PolicyRetryThenCollect} {
		for _, tc := range cases {
			t.Run(policy.String()+"/"+tc.name, func(t *testing.T) {
				provider := newGatedProvider()
				ctx := tc.ctx(t, provider)
				type returned struct {
					result types.BatchResult[string]
					err    error
				}
				done := make(chan returned, 1)
				go func() {
					result, err := RunOpManyPartial(ctx, atomicOnlyOp(), partialItems(30), types.OpOptions{}, PartialConfig{Policy: policy, Concurrency: tc.workers})
					done <- returned{result, err}
				}()
				provider.awaitArrivals(t, tc.wantPeak)
				settle()
				if inFlight, _, _, _ := provider.snapshot(); inFlight != tc.wantPeak {
					t.Fatalf("%d calls in flight, want %d", inFlight, tc.wantPeak)
				}
				provider.open()
				got := <-done
				if got.err != nil || !got.result.Complete() {
					t.Fatalf("result %+v, error %v", got.result.Summary, got.err)
				}
				for i, item := range got.result.Items {
					if want := fmt.Sprintf(`{"length":%d}`, i); item.Value != want {
						t.Fatalf("item %d = %q, want %q", i, item.Value, want)
					}
				}
				if _, peak, _, _ := provider.snapshot(); peak != tc.wantPeak {
					t.Errorf("peak = %d, want %d", peak, tc.wantPeak)
				}
			})
		}
	}
}

// A call that panics, or ends its goroutine, fails its own item under every
// policy; the other items finish and nothing is left running.
func TestRunOpManyPartialReportsAnItemWhoseCallPanics(t *testing.T) {
	for _, policy := range []types.BatchPolicy{types.PolicyCollectFailures, types.PolicyRequireAll, types.PolicyFailFast, types.PolicyRetryThenCollect} {
		for _, fail := range []struct {
			name string
			do   func()
		}{{"panic", func() { panic("a customer's name") }}, {"goexit", runtime.Goexit}} {
			t.Run(policy.String()+"/"+fail.name, func(t *testing.T) {
				before := chaosSettledGoroutines()
				provider := newGatedProvider()
				provider.hold = false
				provider.before = func(call *gatedCall) error {
					if widgetNumbers(call.request.UserPrompt)[0] == 3 {
						fail.do()
					}
					return nil
				}
				// One worker, so under fail-fast the items after the failed one
				// are the ones that were never started.
				result, _ := RunOpManyPartial(gatedContext(provider), atomicOnlyOp(), partialItems(8), types.OpOptions{}, PartialConfig{Policy: policy, Concurrency: 1})

				item := result.Items[3]
				if item.Status != types.ItemFailed || types.KindOf(item.Err) != types.KindInvariantViolation {
					t.Fatalf("item 3 = %v, %v; want a failure of kind invariant violation", item.Status, item.Err)
				}
				if strings.Contains(item.Err.Error(), "customer") {
					t.Errorf("the panic's value is in the error: %v", item.Err)
				}
				for i, other := range result.Items {
					switch {
					case i < 3 && other.Status != types.ItemSucceeded:
						t.Errorf("item %d = %v before the failure", i, other.Status)
					case i > 3 && policy == types.PolicyFailFast && other.Status != types.ItemCancelled:
						t.Errorf("item %d = %v, want cancelled after a fail-fast failure", i, other.Status)
					case i > 3 && policy != types.PolicyFailFast && other.Status != types.ItemSucceeded:
						t.Errorf("item %d = %v, want it to finish", i, other.Status)
					}
				}
				if after := chaosSettledGoroutines(); after > before {
					t.Errorf("%d goroutines before, %d after", before, after)
				}
			})
		}
	}
}

// A retry that panics leaves the item failed, with the panic as its error.
func TestRunOpManyPartialRetryThatPanicsFailsOnlyThatItem(t *testing.T) {
	provider := newGatedProvider()
	provider.hold = false
	var mu sync.Mutex
	attempts := map[int]int{}
	provider.before = func(call *gatedCall) error {
		n := widgetNumbers(call.request.UserPrompt)[0]
		mu.Lock()
		attempts[n]++
		attempt := attempts[n]
		mu.Unlock()
		if n == 2 {
			if attempt == 1 {
				return types.Newf(types.KindProviderUnavailable, "gated", "down")
			}
			panic("again")
		}
		return nil
	}
	result, err := RunOpManyPartial(gatedContext(provider), atomicOnlyOp(), partialItems(5), types.OpOptions{}, PartialConfig{Policy: types.PolicyRetryThenCollect, Concurrency: 2})
	if err != nil {
		t.Fatalf("RunOpManyPartial: %v", err)
	}
	if got := result.Items[2]; got.Status != types.ItemFailed || types.KindOf(got.Err) != types.KindInvariantViolation {
		t.Errorf("item 2 = %v, %v; want the retry's panic as its failure", got.Status, got.Err)
	}
	if result.Summary.Succeeded != 4 || result.Summary.Failed != 1 {
		t.Errorf("summary %+v", result.Summary)
	}
}

func TestRunOpManyPartialLeavesNoGoroutineBehindOver200Runs(t *testing.T) {
	before := chaosSettledGoroutines()
	policies := []types.BatchPolicy{types.PolicyCollectFailures, types.PolicyFailFast, types.PolicyRetryFailedItems, types.PolicyRetryThenCollect}
	for run := 0; run < 200; run++ {
		provider := newGatedProvider()
		provider.hold = false
		if run%2 == 1 {
			provider.before = func(call *gatedCall) error {
				if widgetNumbers(call.request.UserPrompt)[0] == 4 {
					panic("boom")
				}
				return nil
			}
		}
		result, _ := RunOpManyPartial(gatedContext(provider), atomicOnlyOp(), partialItems(8), types.OpOptions{}, PartialConfig{Policy: policies[run%4], Concurrency: 3})
		if len(result.Items) != 8 {
			t.Fatalf("run %d: %d items", run, len(result.Items))
		}
	}
	if after := chaosSettledGoroutines(); after > before {
		t.Errorf("%d goroutines before 200 runs, %d after", before, after)
	}
}

// Under fail-fast, a call that was cancelled in flight because another item
// failed is a cancelled item, as an item that never started is. Nothing about its
// own input was found wrong, and the summary's count of failures is the count of
// items that failed. The error returned is the failure that stopped the run.
func TestRunOpManyPartialFailFastCountsCallsCancelledInFlightAsCancelled(t *testing.T) {
	provider := newGatedProvider()
	provider.before = func(call *gatedCall) error {
		return types.Newf(types.KindProviderUnavailable, "gated", "the provider is down")
	}
	type returned struct {
		result types.BatchResult[string]
		err    error
	}
	done := make(chan returned, 1)
	go func() {
		result, err := RunOpManyPartial(gatedContext(provider), atomicOnlyOp(), partialItems(6), types.OpOptions{},
			PartialConfig{Policy: types.PolicyFailFast, Concurrency: 2})
		done <- returned{result, err}
	}()

	// Items 0 and 1 are in flight. Item 1 is let go, and fails; item 0 is still
	// held when the run is cancelled.
	for _, call := range provider.awaitArrivals(t, 2) {
		if widgetNumbers(call.request.UserPrompt)[0] == 1 {
			close(call.release)
		}
	}
	var got returned
	select {
	case got = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("RunOpManyPartial had not returned after 10 seconds")
	}

	if kind := types.KindOf(got.err); kind != types.KindProviderUnavailable {
		t.Errorf("error kind = %v, want the failure that stopped the run (%v): %v", kind, types.KindProviderUnavailable, got.err)
	}
	if s := got.result.Summary; s.Succeeded != 0 || s.Failed != 1 || s.Cancelled != 5 {
		t.Fatalf("summary = %+v, want 0 succeeded, 1 failed, 5 cancelled", s)
	}
	if status := got.result.Items[1].Status; status != types.ItemFailed {
		t.Errorf("item 1 = %v, want failed: it is the failure", status)
	}
	held := got.result.Items[0]
	if held.Status != types.ItemCancelled {
		t.Errorf("item 0 = %v, want cancelled: it was stopped in flight by item 1", held.Status)
	}
	if !errors.Is(held.Err, context.Canceled) {
		t.Errorf("item 0's error = %v, want the cancellation kept beside the status", held.Err)
	}
	for i := 2; i < 6; i++ {
		if status := got.result.Items[i].Status; status != types.ItemCancelled {
			t.Errorf("item %d = %v, want cancelled: it never started", i, status)
		}
	}
}

// A retry round cut short by the caller's context leaves each item with the
// failure it had. The cancellation that ends a retry is not what was wrong with
// the item, and an item whose retry never started is not given one.
func TestRunOpManyPartialRetryPassKeepsTheEarlierFailureWhenTheContextEndsInARound(t *testing.T) {
	provider := newGatedProvider()
	provider.hold = false
	inRetry := make(chan struct{})
	proceed := make(chan struct{})
	var mu sync.Mutex
	attempts := map[int]int{}
	provider.before = func(call *gatedCall) error {
		n := widgetNumbers(call.request.UserPrompt)[0]
		if n != 1 && n != 2 {
			return nil
		}
		mu.Lock()
		attempts[n]++
		attempt := attempts[n]
		mu.Unlock()
		if attempt == 1 {
			return types.Newf(types.KindProviderUnavailable, "gated", "the first pass failed")
		}
		// Item 1's retry: held until the test has cancelled the caller's context.
		close(inRetry)
		<-proceed
		return call.ctx.Err()
	}
	ctx, cancel := context.WithCancel(gatedContext(provider))
	defer cancel()

	type returned struct {
		result types.BatchResult[string]
		err    error
	}
	done := make(chan returned, 1)
	go func() {
		result, err := RunOpManyPartial(ctx, atomicOnlyOp(), partialItems(4), types.OpOptions{},
			PartialConfig{Policy: types.PolicyRetryThenCollect, Concurrency: 1})
		done <- returned{result, err}
	}()
	select {
	case <-inRetry:
	case <-time.After(10 * time.Second):
		t.Fatal("the retry of item 1 never started")
	}
	cancel()
	close(proceed)
	var got returned
	select {
	case got = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("RunOpManyPartial had not returned after 10 seconds")
	}

	if got.err != nil {
		t.Fatalf("RunOpManyPartial: %v", got.err)
	}
	if s := got.result.Summary; s.Succeeded != 2 || s.Failed != 2 || s.Cancelled != 0 {
		t.Fatalf("summary = %+v, want 2 succeeded, 2 failed, 0 cancelled", s)
	}
	for _, i := range []int{1, 2} {
		item := got.result.Items[i]
		if item.Status != types.ItemFailed {
			t.Errorf("item %d = %v, want failed", i, item.Status)
		}
		if kind := types.KindOf(item.Err); kind != types.KindProviderUnavailable || !strings.Contains(causeText(item.Err), "the first pass failed") {
			t.Errorf("item %d lost the failure it had to the cancellation that ended its retry: %v", i, item.Err)
		}
	}
	if attempts := got.result.Items[2].Attempts; attempts != 1 {
		t.Errorf("item 2 = %d attempts, want the 1 of the first pass: its retry never started", attempts)
	}
}

// Whichever order the calls answer in, every item is reported in its own place
// with its own answer, under every policy.
func TestRunOpManyPartialReportsEveryItemInItsPlaceForEveryCompletionOrder(t *testing.T) {
	policies := []types.BatchPolicy{
		types.PolicyCollectFailures, types.PolicyRequireAll, types.PolicyFailFast,
		types.PolicyRetryFailedItems, types.PolicyRetryThenCollect,
	}
	const items = 4
	for _, policy := range policies {
		for _, order := range permute([]int{0, 1, 2, 3}) {
			t.Run(fmt.Sprintf("%s/%v", policy, order), func(t *testing.T) {
				provider := newGatedProvider()
				type returned struct {
					result types.BatchResult[string]
					err    error
				}
				done := make(chan returned, 1)
				go func() {
					result, err := RunOpManyPartial(gatedContext(provider), atomicOnlyOp(), partialItems(items), types.OpOptions{},
						PartialConfig{Policy: policy, Concurrency: items})
					done <- returned{result, err}
				}()

				byItem := make(map[int]*gatedCall)
				for _, call := range provider.awaitArrivals(t, items) {
					byItem[widgetNumbers(call.request.UserPrompt)[0]] = call
				}
				for _, item := range order {
					close(byItem[item].release)
				}
				var got returned
				select {
				case got = <-done:
				case <-time.After(10 * time.Second):
					t.Fatal("RunOpManyPartial had not returned after 10 seconds")
				}

				if got.err != nil || !got.result.Complete() {
					t.Fatalf("summary %+v, error %v", got.result.Summary, got.err)
				}
				if len(got.result.Items) != items {
					t.Fatalf("%d items reported, want %d", len(got.result.Items), items)
				}
				for i, item := range got.result.Items {
					if want := fmt.Sprintf(`{"length":%d}`, i); item.Index != i || item.Status != types.ItemSucceeded || item.Value != want {
						t.Errorf("item %d = index %d, %v, %q; want its own answer %q", i, item.Index, item.Status, item.Value, want)
					}
				}
				if _, _, calls, _ := provider.snapshot(); calls != items {
					t.Errorf("%d calls, want one per item", calls)
				}
			})
		}
	}
}

// A run that collects its failures names the first item that failed, by
// position, as the error's cause, so the call's kind is the item's and not
// "unknown".
func TestFirstItemFailureIsTheFirstFailedItemByPosition(t *testing.T) {
	errA := types.Newf(types.KindRateLimited, "a", "first")
	errB := types.Newf(types.KindTimeout, "b", "second")
	ok := types.ItemResult[string]{Status: types.ItemSucceeded}
	failed := func(err error) types.ItemResult[string] {
		return types.ItemResult[string]{Status: types.ItemFailed, Err: err}
	}
	cases := []struct {
		name  string
		items []types.ItemResult[string]
		want  error
	}{
		{"no items", nil, nil},
		{"an empty slice", []types.ItemResult[string]{}, nil},
		{"every item succeeded", []types.ItemResult[string]{ok, ok, ok}, nil},
		{"the first item failed", []types.ItemResult[string]{failed(errA), ok}, errA},
		{"the last item failed", []types.ItemResult[string]{ok, ok, failed(errB)}, errB},
		{"two failed and the first by position wins", []types.ItemResult[string]{ok, failed(errA), failed(errB)}, errA},
		{"a cancelled item is not a failure", []types.ItemResult[string]{{Status: types.ItemCancelled, Err: errB}, ok}, nil},
		{"a failed item with no error is skipped", []types.ItemResult[string]{{Status: types.ItemFailed}, failed(errB)}, errB},
		{"an unspecified item is not a failure", []types.ItemResult[string]{{Status: types.ItemUnspecified, Err: errA}}, nil},
		{"a cancelled item before a failed one", []types.ItemResult[string]{{Status: types.ItemCancelled}, failed(errA)}, errA},
		{"a succeeded item that carries an error is not a failure", []types.ItemResult[string]{{Status: types.ItemSucceeded, Err: errA}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstItemFailure(tc.items); got != tc.want {
				t.Errorf("firstItemFailure = %v, want %v", got, tc.want)
			}
		})
	}
}

// Under PolicyRequireAll and PolicyRetryFailedItems the call's error is
// classified as the first failed item's kind and keeps it as the cause, as the
// fail-fast error does.
func TestRunOpManyPartialErrorForCollectedFailuresCarriesTheFirstFailedItemsKind(t *testing.T) {
	for _, policy := range []types.BatchPolicy{types.PolicyRequireAll, types.PolicyRetryFailedItems} {
		t.Run(policy.String(), func(t *testing.T) {
			configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
			provider := newKeyedProvider("fake-model", false, "item-03")
			ctx := WithProvider(context.Background(), provider)

			result, err := RunOpManyPartial(ctx, echoOp(), indexedItems(6), types.OpOptions{},
				PartialConfig{Policy: policy, Concurrency: 1, MaxRetries: 1})
			if err == nil {
				t.Fatal("no error with one item failed")
			}
			itemKind := types.KindOf(result.Items[3].Err)
			if itemKind == types.KindUnknown {
				t.Fatalf("the failed item's own error is unclassified: %v", result.Items[3].Err)
			}
			if got := types.KindOf(err); got != itemKind {
				t.Errorf("the call's kind is %v, the failed item's is %v", got, itemKind)
			}
			if !errors.Is(err, result.Items[3].Err) {
				t.Error("the call's error does not carry the failed item's error as its cause")
			}
		})
	}
}

// BatchResult.Total is what every request of the run cost, retries included, and
// it agrees with the provider's own count: nothing of the kind was reported, and
// the spend of a call that failed was lost with the item.
func TestRunOpManyPartialTotalIsWhatEveryRequestCost(t *testing.T) {
	cases := []struct {
		name     string
		policy   types.BatchPolicy
		items    int
		failing  []string
		recovers map[string]int // item -> failures before it answers
		workers  int
		failure  error
	}{
		{"collect, nothing fails", types.PolicyCollectFailures, 6, nil, nil, 1, nil},
		{"collect, one item fails", types.PolicyCollectFailures, 6, []string{"item-02"}, nil, 1, nil},
		{"fail fast, one item fails", types.PolicyFailFast, 6, []string{"item-02"}, nil, 1, nil},
		{"require all, two items fail", types.PolicyRequireAll, 6, []string{"item-01", "item-04"}, nil, 2, nil},
		{"retry failed items, a failure that goes away", types.PolicyRetryFailedItems, 6, nil, map[string]int{"item-03": 1}, 1, types.Newf(types.KindProviderUnavailable, "x", "down")},
		{"retry then collect, a failure that stays", types.PolicyRetryThenCollect, 6, []string{"item-03"}, nil, 1, types.Newf(types.KindProviderUnavailable, "x", "down")},
		{"unspecified, a small run", types.PolicyUnspecified, 5, nil, nil, 1, nil},
		{"unspecified, a long run", types.PolicyUnspecified, 25, []string{"item-07"}, nil, 4, types.Newf(types.KindRateLimited, "x", "slow")},
		{"every item fails", types.PolicyCollectFailures, 4, []string{"item-00", "item-01", "item-02", "item-03"}, nil, 2, nil},
		{"one item", types.PolicyCollectFailures, 1, nil, nil, 1, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configtest.Setenv(t, "SCHEMAFLUX_MODEL", "gpt-4")
			provider := newKeyedProvider("gpt-4", true, tc.failing...)
			provider.failErr = tc.failure
			for item, failures := range tc.recovers {
				provider.stopFailingAfter(item, failures)
			}
			began := time.Now()
			result, _ := RunOpManyPartial(WithProvider(context.Background(), provider), echoOp(), indexedItems(tc.items), types.OpOptions{},
				PartialConfig{Policy: tc.policy, Concurrency: tc.workers, MaxRetries: 2})

			asked := 0
			provider.mu.Lock()
			for _, n := range provider.calls {
				asked += n
			}
			provider.mu.Unlock()

			total := result.Total
			if total.Operation != echoOp().ID.String() || total.Strategy != types.ShapeAtomic.String() {
				t.Errorf("operation %q, strategy %q", total.Operation, total.Strategy)
			}
			if total.Attempts != asked {
				t.Errorf("Total.Attempts = %d, the provider was asked %d times", total.Attempts, asked)
			}
			if want := 150 * result.Summary.Succeeded; total.Usage.TotalTokens != want {
				t.Errorf("Total.Usage.TotalTokens = %d, want %d: a response for each item that succeeded", total.Usage.TotalTokens, want)
			}
			if total.Elapsed < 0 || total.Elapsed > time.Since(began) {
				t.Errorf("Total.Elapsed = %v, want a time within the call's own", total.Elapsed)
			}
			if total.RequestedContract != types.ContractJSONWellFormed {
				t.Errorf("RequestedContract = %s", total.RequestedContract)
			}
			if result.Summary.Succeeded > 0 {
				if total.DeliveredContract != types.ContractJSONWellFormed {
					t.Errorf("DeliveredContract = %s", total.DeliveredContract)
				}
				if !total.Cost.Priced || total.Cost.TotalCost <= 0 {
					t.Errorf("Total.Cost = %+v, want a priced total", total.Cost)
				}
			}
			var attributed float64
			for _, item := range result.Items {
				attributed += item.Cost.TotalCost
			}
			if attributed > total.Cost.TotalCost*(1+1e-9) {
				t.Errorf("the items' costs add up to %v, more than the %v the run cost", attributed, total.Cost.TotalCost)
			}
		})
	}
}

// Total.Elapsed is the time of the whole call, and not a sum over requests that
// ran together: three requests of 40ms each, run at once, took about 40ms.
func TestRunOpManyPartialAndRecoverTotalElapsedIsTheWholeCallOnTheClock(t *testing.T) {
	const delay = 40 * time.Millisecond
	t.Run("partial", func(t *testing.T) {
		configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
		provider := newGatedProvider()
		provider.hold = false
		provider.before = func(*gatedCall) error { time.Sleep(delay); return nil }
		began := time.Now()
		result, err := RunOpManyPartial(gatedContext(provider), atomicOnlyOp(), partialItems(3), types.OpOptions{}, PartialConfig{Policy: types.PolicyCollectFailures, Concurrency: 3})
		if err != nil {
			t.Fatalf("RunOpManyPartial: %v", err)
		}
		// The bound is the call's own time on the clock, which a loaded machine
		// stretches and a sum of the requests' durations would exceed by more.
		if wall := time.Since(began); result.Total.Elapsed < delay || result.Total.Elapsed > wall {
			t.Errorf("Total.Elapsed = %v, want at least %v and at most the %v the call took", result.Total.Elapsed, delay, wall)
		}
	})
	t.Run("recover", func(t *testing.T) {
		provider := newRecoverFakeProvider("fake-model")
		provider.batchHook = func(context.Context, int) error { time.Sleep(delay); return nil }
		result, err := RunOpManyRecover(recoverCtx(t, provider), recoverWidgetOp(), recoverWidgets(3), recoverCodec(), types.OpOptions{}, RecoverConfig{})
		if err != nil {
			t.Fatalf("RunOpManyRecover: %v", err)
		}
		if result.Total.Elapsed < delay {
			t.Errorf("Total.Elapsed = %v, want at least the %v the request took", result.Total.Elapsed, delay)
		}
	})
}

// --- A context that is over, and what it does to the items.

// A context that is already over runs nothing, under every policy: not an item, and
// not a function of the caller's. It used to return a nil error with every item
// failed, and BuildPrompt called for each of them.
func TestRunOpManyPartialRefusesAContextThatIsAlreadyOver(t *testing.T) {
	for _, policy := range []types.BatchPolicy{
		types.PolicyUnspecified, types.PolicyFailFast, types.PolicyCollectFailures,
		types.PolicyRetryFailedItems, types.PolicyRetryThenCollect, types.PolicyRequireAll,
	} {
		t.Run(policy.String(), func(t *testing.T) {
			configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
			provider := newKeyedProvider("fake-model", false)
			ctx, cancel := context.WithCancel(WithProvider(context.Background(), provider))
			cancel()
			built := 0
			op := echoOp()
			inner := op.BuildPrompt
			op.BuildPrompt = func(in string, opt types.OpOptions) (string, string) { built++; return inner(in, opt) }

			result, err := RunOpManyPartial(ctx, op, indexedItems(4), types.OpOptions{}, PartialConfig{Policy: policy})
			if types.KindOf(err) != types.KindCanceled || !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v (%v), want a cancellation that matches context.Canceled", err, types.KindOf(err))
			}
			if len(result.Items) != 0 || result.Summary.Total != 0 {
				t.Errorf("a result came back beside the error: %+v", result.Summary)
			}
			if built != 0 || provider.callCountFor("item-00") != 0 {
				t.Errorf("BuildPrompt ran %d times and a request was sent", built)
			}
		})
	}
}

// A context that ends during the run cancels the items it cut off, under every
// policy and not under one: they were not found wrong, and Failed counts only what
// went wrong with an item. The item whose own failure came first is still failed.
func TestRunOpManyPartialMarksTheItemsACancelledContextCutOffAsCancelledUnderEveryPolicy(t *testing.T) {
	for _, policy := range []types.BatchPolicy{
		types.PolicyCollectFailures, types.PolicyRetryFailedItems, types.PolicyRetryThenCollect, types.PolicyRequireAll,
	} {
		t.Run(policy.String(), func(t *testing.T) {
			configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			provider := newKeyedProvider("fake-model", false, "item-02")
			provider.onFail = cancel // the caller gives up once item 2 has failed
			result, err := RunOpManyPartial(WithProvider(ctx, provider), echoOp(), indexedItems(6), types.OpOptions{},
				PartialConfig{Policy: policy, Concurrency: 1})
			if policy == types.PolicyCollectFailures || policy == types.PolicyRetryThenCollect {
				if err != nil {
					t.Fatalf("RunOpManyPartial: %v", err)
				}
			}
			if s := result.Summary; s.Succeeded != 2 || s.Failed != 1 || s.Cancelled != 3 {
				t.Fatalf("Summary = %+v, want 2 succeeded, the one item that failed on its own failed, and the 3 after the cancellation cancelled", s)
			}
			for i := 3; i < 6; i++ {
				item := result.Items[i]
				if item.Status != types.ItemCancelled || types.KindOf(item.Err) != types.KindCanceled || !errors.Is(item.Err, context.Canceled) {
					t.Errorf("item %d = %v, %v; want a cancelled item whose error matches context.Canceled", i, item.Status, item.Err)
				}
			}
			if result.Complete() {
				t.Error("a cut-short run reads as complete")
			}
		})
	}
}

// Under fail-fast the items that were never started carry a classified
// cancellation that names the failure that stopped the run as its cause, where they
// carried a bare context error that said the caller cancelled when the caller had not.
func TestRunOpManyPartialFailFastItemsNeverStartedCarryTheFailureThatStoppedTheRun(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
	provider := newKeyedProvider("fake-model", false, "item-01")
	provider.failErr = &llm.RateLimitError{APIError: &llm.APIError{Provider: "keyed", StatusCode: 429}, RetryAfter: time.Millisecond}
	ctx := WithProvider(context.Background(), provider)

	result, err := RunOpManyPartial(ctx, echoOp(), indexedItems(5), types.OpOptions{}, PartialConfig{Policy: types.PolicyFailFast, Concurrency: 1})
	if types.KindOf(err) != types.KindRateLimited {
		t.Fatalf("err = %v (%v), want the failure that stopped the run", err, types.KindOf(err))
	}
	for i := 2; i < 5; i++ {
		item := result.Items[i]
		if item.Status != types.ItemCancelled {
			t.Fatalf("item %d: %v", i, item.Status)
		}
		var classified *types.OperationError
		if !errors.As(item.Err, &classified) || types.KindOf(item.Err) != types.KindCanceled || !errors.Is(item.Err, types.ErrCanceled) {
			t.Errorf("item %d's error is %T %v (%v), want a classified cancellation", i, item.Err, item.Err, types.KindOf(item.Err))
		}
		if errors.Is(item.Err, context.Canceled) {
			t.Errorf("item %d's error says the caller's context was cancelled, and it was not", i)
		}
		if !errors.Is(item.Err, provider.failErr) {
			t.Errorf("item %d's error does not carry the failure that stopped the run as its cause: %v", i, item.Err)
		}
		if strings.Contains(item.Err.Error(), "context canceled") {
			t.Errorf("item %d's text is a bare context error: %v", i, item.Err)
		}
	}
}

// The retry rounds are capped whatever the caller asks for, and the cap is the
// documented number: one item that is rate limited for ever makes at most
// 1+MaxRetriesLimit requests.
func TestRunOpManyPartialCapsTheRetryRounds(t *testing.T) {
	for _, asked := range []int{0, 1, 3, MaxRetriesLimit, MaxRetriesLimit + 1, 1000} {
		t.Run(fmt.Sprintf("MaxRetries %d", asked), func(t *testing.T) {
			configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
			provider := newKeyedProvider("fake-model", false, "item-00")
			provider.failErr = &llm.RateLimitError{APIError: &llm.APIError{Provider: "keyed", StatusCode: 429}, RetryAfter: time.Millisecond}
			result, err := RunOpManyPartial(WithProvider(context.Background(), provider), echoOp(), indexedItems(1), types.OpOptions{},
				PartialConfig{Policy: types.PolicyRetryThenCollect, MaxRetries: asked})
			if err != nil || result.Summary.Failed != 1 {
				t.Fatalf("err %v, %+v", err, result.Summary)
			}
			want := 1 + DefaultMaxRetries
			switch {
			case asked > MaxRetriesLimit:
				want = 1 + MaxRetriesLimit
			case asked > 0:
				want = 1 + asked
			}
			if got := provider.callCountFor("item-00"); got != want {
				t.Errorf("%d requests for one item with MaxRetries %d, want %d", got, asked, want)
			}
		})
	}
}

// --- The text of an item's error is the library's.

// Nothing a vendor said, and nothing the caller's own Decode or invariant reported,
// is in an item's error text, in the summary or in the run's error: only the
// operation, the kind and the item's place. The caller's own error is the cause,
// reached with errors.Is.
func TestRunOpManyPartialErrorTextsCarryNoVendorOrCallerText(t *testing.T) {
	const vendor, callerText, item = "CANARY-VENDOR", "CANARY-CALLER", "CANARY-ITEM"
	callerErr := errors.New(callerText + " cannot read " + item)

	build := map[string]func() (Op[string, string], llm.Provider){
		"the provider fails": func() (Op[string, string], llm.Provider) {
			p := newKeyedProvider("fake-model", false, item)
			p.failErr = errors.New("upstream said " + vendor + " quota for " + item)
			return echoOp(), p
		},
		"the caller's Decode fails": func() (Op[string, string], llm.Provider) {
			op := echoOp()
			op.Contract.Decode = func(string) (string, error) { return "", callerErr }
			return op, newKeyedProvider("fake-model", false)
		},
		"the caller's invariant fails": func() (Op[string, string], llm.Provider) {
			return echoOp(func(string) error { return callerErr }), newKeyedProvider("fake-model", false)
		},
		"the caller's Decode panics": func() (Op[string, string], llm.Provider) {
			op := echoOp()
			op.Contract.Decode = func(string) (string, error) { panic(callerText + item) }
			return op, newKeyedProvider("fake-model", false)
		},
	}
	for name, setup := range build {
		for _, policy := range []types.BatchPolicy{types.PolicyCollectFailures, types.PolicyFailFast, types.PolicyRequireAll, types.PolicyRetryFailedItems} {
			t.Run(name+"/"+policy.String(), func(t *testing.T) {
				configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
				op, provider := setup()
				result, runErr := RunOpManyPartial(WithProvider(context.Background(), provider), op, []string{item, item}, types.OpOptions{},
					PartialConfig{Policy: policy, Concurrency: 1})

				texts := []string{result.String(), result.Summary.PolicyReason, result.Ledger.Explain()}
				if runErr != nil {
					texts = append(texts, runErr.Error())
				}
				for _, entry := range result.Items {
					if entry.Err != nil {
						texts = append(texts, entry.Err.Error())
					}
				}
				for _, text := range texts {
					for _, canary := range []string{"CANARY", "Your previous answer", "Return a corrected answer", "upstream said"} {
						if strings.Contains(text, canary) {
							t.Errorf("%q carries %q", text, canary)
						}
					}
				}
				if len(result.Items) != 2 || result.Items[0].Err == nil {
					t.Fatalf("no item failed: %+v", result.Summary)
				}
				if strings.HasPrefix(name, "the caller's Decode f") || strings.HasPrefix(name, "the caller's invariant") {
					if !errors.Is(result.Items[0].Err, callerErr) {
						t.Errorf("the caller's own error is not in the chain of %v", result.Items[0].Err)
					}
				}
				if got := result.Items[0].Err.Error(); !strings.HasPrefix(got, "callerEcho: ") || !strings.Contains(got, "item 1 of 2") {
					t.Errorf("%q does not name the operation and the item's place", got)
				}
			})
		}
	}
}

// --- What a request cost is not lost when the caller's function panics on its answer.

func TestRunOpManyPartialTotalIncludesTheRequestOfAnItemWhoseDecodePanicked(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "gpt-4")
	provider := newKeyedProvider("gpt-4", true)
	op := echoOp()
	inner := op.Contract.Decode
	op.Contract.Decode = func(body string) (string, error) {
		if body == "item-01" {
			panic("the caller's decoder broke")
		}
		return inner(body)
	}
	result, err := RunOpManyPartial(WithProvider(context.Background(), provider), op, indexedItems(3), types.OpOptions{},
		PartialConfig{Policy: types.PolicyCollectFailures, Concurrency: 1})
	if err != nil || result.Summary.Failed != 1 {
		t.Fatalf("err %v, %+v", err, result.Summary)
	}
	calls := 0
	for _, n := range provider.calls {
		calls += n
	}
	if calls != 3 || result.Total.Attempts != 3 || result.Total.Usage.TotalTokens != 3*150 {
		t.Errorf("provider calls %d, Total.Attempts %d, Total tokens %d, want the 3 requests it billed: a panic must not take its request's cost with it", calls, result.Total.Attempts, result.Total.Usage.TotalTokens)
	}
	failed := result.Items[1]
	if failed.Attempts != 1 || !failed.Cost.Priced || failed.Cost.TotalCost <= 0 {
		t.Errorf("the item whose decoder panicked: attempts %d, cost %+v, want the request it paid for", failed.Attempts, failed.Cost)
	}
	if types.NarrowingOf(failed.Err) != types.NarrowPanic || strings.Contains(failed.Err.Error(), "broke") {
		t.Errorf("the item's error: %v", failed.Err)
	}
}

// --- A client's budget, and the overshoot the documentation promises.

type requestCapBudget struct {
	limit int32
	n     atomic.Int32
}

func (b *requestCapBudget) Check() error {
	if b.n.Load() >= b.limit {
		return &types.OperationError{Kind: types.KindBudgetExceeded, Op: "budget", Message: "spent"}
	}
	return nil
}
func (b *requestCapBudget) Record(float64, bool) { b.n.Add(1) }

// A budget that records each request is overshot by at most the concurrency less one
// requests that had already started when it ran out, and by none with a bound of one.
func TestRunOpManyPartialABudgetIsOvershotByAtMostTheConcurrencyLessOne(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
	for _, workers := range []int{1, 2, 4, 16} {
		t.Run(fmt.Sprintf("%d workers", workers), func(t *testing.T) {
			budget := &requestCapBudget{limit: 2}
			provider := newKeyedProvider("fake-model", true)
			ctx := WithExecConfig(context.Background(), &ExecConfig{Provider: provider, Budget: budget})
			result, err := RunOpManyPartial(ctx, echoOp(), indexedItems(100), types.OpOptions{}, PartialConfig{Policy: types.PolicyCollectFailures, Concurrency: workers})
			if err != nil {
				t.Fatalf("RunOpManyPartial: %v", err)
			}
			calls := 0
			for _, n := range provider.calls {
				calls += n
			}
			if limit := 2 + workers - 1; calls < 2 || calls > limit {
				t.Errorf("%d requests with a budget of 2 and %d workers, want between 2 and %d", calls, workers, limit)
			}
			if workers == 1 && calls != 2 {
				t.Errorf("%d requests with a bound of one, want exactly the budget's 2", calls)
			}
			if result.Summary.Succeeded != calls || result.Summary.Failed != 100-calls {
				t.Errorf("Summary = %+v for %d requests", result.Summary, calls)
			}
			for _, item := range result.Failures() {
				if types.KindOf(item.Err) != types.KindBudgetExceeded {
					t.Fatalf("an item over the budget failed with %v, want the budget", types.KindOf(item.Err))
				}
			}
		})
	}
}
