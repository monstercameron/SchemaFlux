package ops

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// RC-005: load and chaos coverage for RunOpManyPartial (partial.go) -- large
// item streams, a provider that is both slow and bursts 429s, and
// cancellation mid-stream -- driven through the real retry classifier
// (llm.Classify / isRetryableLLMError, llm_helper.go) rather than a second
// opinion invented for this test file. partial_test.go already proves each
// policy's contract with a small, deterministic fixture (ten items, one
// permanently failing); these tests scale that up and add the two axes the
// task asks for that partial_test.go's keyedProvider cannot express: latency
// and rate-limit bursts with a real Retry-After.
//
// loadProvider's delays are small (single-digit milliseconds) so these stay
// fast enough for the ordinary suite; the point is exercising the mechanism
// under real concurrency and real (if brief) waits, not measuring a
// throughput number -- RC-005's own text is explicit that the outcome
// metrics are cost and latency per valid item, not calls per second, and
// this file invents neither.

// loadProvider is a fake llm.Provider keyed by UserPrompt (indexedItems'
// items are already distinct strings, so the prompt is the item), with a
// per-call delay for simulating a slow provider and a per-key countdown of
// 429s for simulating a rate-limit burst that eventually clears -- both
// axes RC-005 names and neither of which schemafluxtest.Provider (a
// different package's fake, over a different Provider interface -- see the
// doc comment on why it is not reused here) or partial_test.go's
// keyedProvider currently expose.
type loadProvider struct {
	mu sync.Mutex

	delay time.Duration

	rateLimitBurst map[string]int  // remaining 429s before this key succeeds
	permanentFail  map[string]bool // never succeeds
	calls          map[string]int
	model          string
}

func newLoadProvider(model string) *loadProvider {
	return &loadProvider{
		rateLimitBurst: map[string]int{},
		permanentFail:  map[string]bool{},
		calls:          map[string]int{},
		model:          model,
	}
}

func (p *loadProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.mu.Lock()
	prompt := req.UserPrompt
	p.calls[prompt]++
	delay := p.delay
	p.mu.Unlock()

	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return llm.CompletionResponse{}, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return llm.CompletionResponse{}, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.permanentFail[prompt] {
		return llm.CompletionResponse{}, &types.OperationError{Kind: types.KindInvalidRequest, Op: "load", Message: "simulated permanent rejection"}
	}
	if p.rateLimitBurst[prompt] > 0 {
		p.rateLimitBurst[prompt]--
		return llm.CompletionResponse{}, &llm.RateLimitError{
			APIError:   &llm.APIError{Provider: "load", Model: p.model, StatusCode: 429},
			RetryAfter: 2 * time.Millisecond,
		}
	}

	return llm.CompletionResponse{
		Content:      prompt,
		Provider:     "load",
		Model:        p.model,
		FinishReason: "stop",
	}, nil
}

func (p *loadProvider) Name() string                               { return "load" }
func (p *loadProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }

// RetryPolicy enables CallLLM's own internal retry loop (llm_helper.go), so a
// 429 burst that clears within the configured attempts is recovered WITHOUT
// PartialConfig.MaxRetries ever being consulted -- the two retry mechanisms
// this test deliberately exercises separately (see the two tests below).
func (p *loadProvider) RetryPolicy() (int, time.Duration) { return 4, time.Millisecond }

func (p *loadProvider) callCountFor(prompt string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[prompt]
}

// TestLoadRunOpManyPartialLargeStreamSlowProviderWithRateLimitBursts drives
// 600 items through a provider that adds latency to every call and answers
// roughly one in eight items with a 429 burst that clears after two retries,
// plus a permanently-rejecting minority -- proving the retry classifier
// recovers the transient failures through CallLLM's own retry loop (not
// PartialConfig's item-level retry pass, which never even needs to run here)
// and that the permanent failures are still reported individually rather
// than one provider hiccup poisoning the whole batch.
func TestLoadRunOpManyPartialLargeStreamSlowProviderWithRateLimitBursts(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
	before := chaosSettledGoroutines()

	const n = 600
	items := indexedItems(n)
	provider := newLoadProvider("fake-model")
	provider.delay = 2 * time.Millisecond

	var rateLimited, permanent int
	for i, item := range items {
		switch {
		case i%13 == 0:
			provider.permanentFail[item] = true
			permanent++
		case i%8 == 0:
			provider.rateLimitBurst[item] = 2
			rateLimited++
		}
	}

	ctx := WithProvider(context.Background(), provider)

	var result types.BatchResult[string]
	var err error
	runWithDeadline(t, 30*time.Second, func() {
		result, err = RunOpManyPartial(ctx, echoOp(), items, types.OpOptions{},
			PartialConfig{Policy: types.PolicyCollectFailures, Concurrency: 24})
	})
	if err != nil {
		t.Fatalf("PolicyCollectFailures returned an error: %v", err)
	}

	if result.Summary.Total != n {
		t.Fatalf("Summary.Total = %d, want %d", result.Summary.Total, n)
	}
	if result.Summary.Succeeded+result.Summary.Failed != n {
		t.Fatalf("succeeded(%d)+failed(%d) = %d, want %d -- some item vanished",
			result.Summary.Succeeded, result.Summary.Failed, result.Summary.Succeeded+result.Summary.Failed, n)
	}
	if result.Summary.Failed != permanent {
		t.Fatalf("Summary.Failed = %d, want %d (only the permanently-rejecting items)", result.Summary.Failed, permanent)
	}
	if result.Summary.Succeeded != n-permanent {
		t.Fatalf("Summary.Succeeded = %d, want %d (everything except the permanent rejections, including every rate-limited item that recovered)",
			result.Summary.Succeeded, n-permanent)
	}

	// Every rate-limited item recovered without PartialConfig's own retry
	// pass running at all -- proof the recovery happened inside CallLLM's
	// retry loop, driven by the real classifier, not by this test's outer
	// policy.
	for i, item := range items {
		if i%13 == 0 {
			continue // permanently failing; not part of this check
		}
		if i%8 == 0 {
			if got := provider.callCountFor(item); got < 3 {
				t.Errorf("item %q: provider called %d time(s), want at least 3 (2 rate-limited + 1 recovering)", item, got)
			}
		}
	}

	// Every failed item carries a usable, classified error.
	for _, f := range result.Failures() {
		if f.Err == nil {
			t.Fatalf("failed item %d has a nil Err", f.Index)
		}
	}

	after := chaosSettledGoroutines()
	if after > before+5 {
		t.Errorf("goroutines went from %d to %d over a %d-item partial run", before, after, n)
	}
}

// TestLoadRunOpManyPartialCancellationStormMidStream cancels the caller's
// context partway through a large, deliberately slow-provider run and proves
// every item still lands in the returned BatchResult -- either succeeded
// (it got through before the cancellation) or failed with a classified
// cancellation, never silently missing -- and that the run returns promptly
// rather than waiting out every in-flight item's own timeout.
func TestLoadRunOpManyPartialCancellationStormMidStream(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
	before := chaosSettledGoroutines()

	const n = 400
	items := indexedItems(n)
	provider := newLoadProvider("fake-model")
	provider.delay = 8 * time.Millisecond // slow enough that a short deadline cuts it off mid-stream

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	pctx := WithProvider(ctx, provider)

	var result types.BatchResult[string]
	var err error
	runWithDeadline(t, 10*time.Second, func() {
		// PolicyCollectFailures: every item is dispatched regardless of
		// another item's outcome, so a mid-stream cancellation's effect is
		// visible on whichever items were still in flight or not yet
		// started, without PolicyFailFast's own cancellation racing it.
		result, err = RunOpManyPartial(pctx, echoOp(), items, types.OpOptions{},
			PartialConfig{Policy: types.PolicyCollectFailures, Concurrency: 32})
	})
	if err != nil {
		t.Fatalf("PolicyCollectFailures returned an error: %v", err)
	}

	if result.Summary.Total != n {
		t.Fatalf("Summary.Total = %d, want %d", result.Summary.Total, n)
	}
	if result.Summary.Succeeded+result.Summary.Failed != n {
		t.Fatalf("succeeded(%d)+failed(%d) = %d, want %d -- some item vanished under cancellation",
			result.Summary.Succeeded, result.Summary.Failed, result.Summary.Succeeded+result.Summary.Failed, n)
	}
	if result.Summary.Failed == 0 {
		t.Fatal("nothing failed -- the deadline never actually interrupted the stream, so this proves nothing")
	}
	for _, f := range result.Failures() {
		if f.Err == nil {
			t.Fatalf("a cancelled item's Err is nil at index %d", f.Index)
		}
	}

	after := chaosSettledGoroutines()
	if after > before+5 {
		t.Errorf("goroutines went from %d to %d after a cancelled %d-item partial run", before, after, n)
	}
}

// TestLoadRunOpManyPartialManyConcurrentBatchesDoNotCrossContaminate runs
// several independent RunOpManyPartial calls concurrently -- the item-level
// analogue of the Scheduler's "mixed tenants" requirement, since
// RunOpManyPartial itself has no tenant concept and this is what "many
// unrelated callers sharing a process" looks like at this layer -- and
// proves each call's own results reflect only its own items: no result
// slice is corrupted, truncated, or mixed with another call's, which a
// shared closure capturing the wrong loop variable or slice would produce
// under real concurrency but not in a single-caller test.
func TestLoadRunOpManyPartialManyConcurrentBatchesDoNotCrossContaminate(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")

	const batches = 24
	const itemsPerBatch = 40

	var wg sync.WaitGroup
	var mismatches int64

	runWithDeadline(t, 20*time.Second, func() {
		for b := 0; b < batches; b++ {
			wg.Add(1)
			go func(b int) {
				defer wg.Done()

				items := make([]string, itemsPerBatch)
				for i := range items {
					items[i] = fmt.Sprintf("batch-%02d-item-%03d", b, i)
				}
				provider := newLoadProvider("fake-model")
				// Every third item in every batch fails permanently, so a
				// contaminated result (another batch's failure pattern
				// leaking in) is detectable by position, not just by count.
				for i, item := range items {
					if i%3 == 0 {
						provider.permanentFail[item] = true
					}
				}
				ctx := WithProvider(context.Background(), provider)

				result, err := RunOpManyPartial(ctx, echoOp(), items, types.OpOptions{},
					PartialConfig{Policy: types.PolicyCollectFailures, Concurrency: 8})
				if err != nil {
					t.Errorf("batch %d: unexpected error: %v", b, err)
					return
				}
				for i, item := range result.Items {
					wantFailed := i%3 == 0
					gotFailed := item.Status == types.ItemFailed
					if wantFailed != gotFailed {
						atomic.AddInt64(&mismatches, 1)
						t.Errorf("batch %d item %d (index %d): status = %s, want failed=%v",
							b, i, item.Index, item.Status, wantFailed)
					}
					if !wantFailed && item.Value != items[i] {
						atomic.AddInt64(&mismatches, 1)
						t.Errorf("batch %d item %d: value = %q, want %q (its own input echoed back)", b, i, item.Value, items[i])
					}
				}
			}(b)
		}
		wg.Wait()
	})

	if mismatches != 0 {
		t.Fatalf("%d item(s) across %d concurrent batches showed cross-batch contamination", mismatches, batches)
	}
}
