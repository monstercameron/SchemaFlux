package ops

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// PL-008: partial success and failure policies, and PL-013's per-item
// metrics computed on top of the same BatchResult.
//
// The fixture here is echoOp (op_kernel_seam_test.go) run over a slice of
// distinct strings: BuildPrompt sends the item itself as the user prompt
// (`"echo", input`), so a fake provider can decide per item, by prompt
// content, whether to fail -- there is no other way to target "item 3 of
// 10" specifically through the real call path.

// keyedProvider is a fake llm.Provider that fails for every UserPrompt in
// failing and otherwise echoes the prompt back as the completion body, with
// an optional priced Usage so a test can exercise PL-013's cost accounting
// without a live provider.
type keyedProvider struct {
	mu        sync.Mutex
	failing   map[string]bool
	failUntil map[string]int
	calls     map[string]int
	model     string
	priced    bool
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

func indexedItems(n int) []string {
	items := make([]string, n)
	for i := range items {
		items[i] = fmt.Sprintf("item-%02d", i)
	}
	return items
}

// --- PL-008: the five policies each behave differently on the same input.
//
// Every test in this block runs the identical input -- ten items, item 3
// ("item-03") fails every time it is attempted -- so the policy is the only
// variable, and the table of outcomes is the "each policy behaves
// differently from the others" bar the task sets.

func TestRunOpManyPartialCollectFailuresReportsEachItem(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
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
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
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
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
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
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
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
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
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
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
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

// --- PL-008: PolicyUnspecified resolves through DefaultPolicyFor and the
// resolution is recorded, never left silently unresolved.

func TestRunOpManyPartialUnspecifiedPolicyResolvesForSmallBatch(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
	items := indexedItems(5)
	provider := newKeyedProvider("fake-model", false)
	ctx := WithProvider(context.Background(), provider)

	result, err := RunOpManyPartial(ctx, echoOp(), items, types.OpOptions{}, PartialConfig{})
	if err != nil {
		t.Fatalf("RunOpManyPartial: %v", err)
	}
	if result.Summary.Policy != types.PolicyFailFast {
		t.Fatalf("Summary.Policy = %s, want fail_fast for a 5-item batch under the %d threshold",
			result.Summary.Policy, types.LongBatchThreshold)
	}
	if result.Summary.PolicyReason == "" {
		t.Fatal("PolicyReason is empty for a resolved (not caller-specified) policy")
	}
}

func TestRunOpManyPartialUnspecifiedPolicyResolvesForLongBatch(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
	items := indexedItems(types.LongBatchThreshold + 5)
	provider := newKeyedProvider("fake-model", false)
	ctx := WithProvider(context.Background(), provider)

	result, err := RunOpManyPartial(ctx, echoOp(), items, types.OpOptions{}, PartialConfig{})
	if err != nil {
		t.Fatalf("RunOpManyPartial: %v", err)
	}
	if result.Summary.Policy != types.PolicyRetryThenCollect {
		t.Fatalf("Summary.Policy = %s, want retry_then_collect for a batch above the %d threshold",
			result.Summary.Policy, types.LongBatchThreshold)
	}
}

// --- PL-013: per-item metrics, computed from the same BatchResult.

func TestBatchMetricsValidRatioAndCostPerAcceptedItem(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "gpt-4")
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
	t.Setenv("SCHEMAFLUX_MODEL", "definitely-not-a-real-model-xyz")
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

// --- PL-008: BatchResult.Values() forces the completeness check, the
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

// --- PL-008: DefaultPolicyFor's threshold, pinned directly.

func TestDefaultPolicyForThreshold(t *testing.T) {
	cases := []struct {
		itemCount int
		want      types.BatchPolicy
	}{
		{1, types.PolicyFailFast},
		{types.LongBatchThreshold, types.PolicyFailFast},
		{types.LongBatchThreshold + 1, types.PolicyRetryThenCollect},
		{500, types.PolicyRetryThenCollect},
	}
	for _, tc := range cases {
		policy, reason := types.DefaultPolicyFor(tc.itemCount)
		if policy != tc.want {
			t.Fatalf("DefaultPolicyFor(%d) = %s, want %s", tc.itemCount, policy, tc.want)
		}
		if reason == "" {
			t.Fatalf("DefaultPolicyFor(%d) gave no reason", tc.itemCount)
		}
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
