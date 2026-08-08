package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// RC-005: load and chaos coverage for RunOpManyRecover (recover.go), PL-009's
// progressive MDSP-to-atomic cascade -- "large schemas and near-limit
// chunks, partial MDSP failures forcing atomic fallback" from the task's own
// list. recover_test.go already pins the cascade's mechanics one failure
// mode at a time (a single omission, a single duplicate, a single invented
// id) against five-item fixtures; these tests scale the same fixture
// (recoverWidgetOp/recoverCodec/newRecoverFakeProvider, all from
// recover_test.go, same package) up to hundreds of items across many
// near-the-configured-limit chunks, with the coverage failures and transport
// failures happening at random rather than at one scripted position, so the
// isolate-pass and atomic-fallback machinery is exercised across many more
// distinct chunk states than a handful of scripted cases can reach.

// randomPartialCoverageBatch answers an MDSP call with full coverage most of
// the time, and otherwise deliberately breaks the id contract one of the
// three ways runOneMDSPPass (recover.go) has to detect: an omission, a
// duplicate, or an invented id. It never fails every item in one chunk
// outright, so the cascade is stressed without ever depending on the atomic
// fallback stage itself being reliable to reach completion -- that
// dependency is exercised separately, in
// TestLoadRunOpManyRecoverTransportFailureBurstsDuringMDSPPassesFallBackCleanly
// below.
func randomPartialCoverageBatch(rng *rand.Rand, mu *sync.Mutex) recoverBatchHandler {
	return func(tagged []taggedItem[recoverWidget]) (string, error) {
		mu.Lock()
		r := rng.Float64()
		skipIdx := -1
		dupIdx := -1
		invent := false
		switch {
		case r < 0.15 && len(tagged) > 1:
			skipIdx = rng.Intn(len(tagged))
		case r < 0.25:
			dupIdx = rng.Intn(len(tagged))
		case r < 0.30:
			invent = true
		}
		mu.Unlock()

		var out []idBatchItem
		for i, t := range tagged {
			if i == skipIdx {
				continue
			}
			out = append(out, idBatchItem{ID: t.ID, Output: json.RawMessage(fmt.Sprintf(`{"length":%d}`, len(t.Item.Name)))})
			if i == dupIdx {
				out = append(out, idBatchItem{ID: t.ID, Output: json.RawMessage(`{"length":-1}`)})
			}
		}
		if invent {
			out = append(out, idBatchItem{ID: "i-999999", Output: json.RawMessage(`{"length":999}`)})
		}
		body, err := json.Marshal(idBatchResponse{Items: out})
		if err != nil {
			return "", err
		}
		return string(body), nil
	}
}

// TestLoadRunOpManyRecoverLargeStreamNearLimitChunksWithRandomPartialMDSPFailures
// runs 300 items through the cascade at a chunk size well under
// defaultMaxItemsPerCall but still large enough that most calls approach it
// (20 items/call, 15 top-level chunks), with roughly 30% of every MDSP call
// -- top-level or isolate -- corrupting its own coverage at random. Every
// item must still resolve, through whichever of MDSP, an isolate pass, or
// atomic fallback it took, with the correct value: a cascade that resolves
// the right COUNT of items by accident (an atomic-fallback answer landing on
// the wrong index, say) would still pass a summary-only check, which is why
// every item's value is checked individually.
func TestLoadRunOpManyRecoverLargeStreamNearLimitChunksWithRandomPartialMDSPFailures(t *testing.T) {
	before := chaosSettledGoroutines()

	const n = 300
	items := recoverWidgets(n)
	provider := newRecoverFakeProvider("fake-model")

	rng := rand.New(rand.NewSource(1))
	var handlerMu sync.Mutex
	provider.batchDefault = randomPartialCoverageBatch(rng, &handlerMu)

	ctx := recoverCtx(t, provider)

	var result types.BatchResult[recoverSummary]
	var err error
	runWithDeadline(t, 30*time.Second, func() {
		result, err = RunOpManyRecover(ctx, recoverWidgetOp(), items, recoverCodec(), types.OpOptions{},
			RecoverConfig{MaxItemsPerCall: 20, StartItemsPerCall: 20, Concurrency: 16})
	})
	if err != nil {
		t.Fatalf("RunOpManyRecover: %v", err)
	}
	if !result.Complete() {
		t.Fatalf("Complete() = false, want true (atomic fallback never fails in this fixture): Summary=%+v", result.Summary)
	}
	if result.Summary.Total != n || result.Summary.Succeeded != n {
		t.Fatalf("Summary = %+v, want Total=%d Succeeded=%d", result.Summary, n, n)
	}

	var mdspCount, atomicCount int
	for i, item := range result.Items {
		if item.Index != i {
			t.Fatalf("item %d: Index = %d, want %d", i, item.Index, i)
		}
		if item.Value.Length != len(items[i].Name) {
			t.Fatalf("item %d: Value.Length = %d, want %d (its own name's length, not another item's)", i, item.Value.Length, len(items[i].Name))
		}
		switch item.Mode {
		case ModeMDSP:
			mdspCount++
		case types.ModeAtomicFallback:
			atomicCount++
		default:
			t.Fatalf("item %d: Mode = %q, unrecognised", i, item.Mode)
		}
	}
	if mdspCount == 0 {
		t.Fatal("nothing resolved via MDSP -- this run did not exercise the compliant path at all")
	}
	if atomicCount == 0 {
		t.Fatal("nothing fell back to atomic -- the 30% corruption rate never actually forced a fallback, so this proves nothing about it")
	}
	t.Logf("resolved %d/%d via MDSP, %d/%d via atomic fallback", mdspCount, n, atomicCount, n)

	after := chaosSettledGoroutines()
	if after > before+5 {
		t.Errorf("goroutines went from %d to %d over a %d-item recovery cascade", before, after, n)
	}
}

// TestLoadRunOpManyRecoverTransportFailureBurstsDuringMDSPPassesFallBackCleanly
// simulates a provider whose first several batch calls fail outright with a
// rate-limit-shaped transport error (the kind classify.go maps to
// types.KindRateLimited, not a coverage problem at all) before it starts
// answering normally -- proving a burst of transport failures early in the
// cascade forces the affected chunks through isolation and atomic fallback
// exactly as a coverage failure would, and that later chunks, once the
// burst has passed, still resolve directly via MDSP rather than every
// later chunk being needlessly downgraded too.
func TestLoadRunOpManyRecoverTransportFailureBurstsDuringMDSPPassesFallBackCleanly(t *testing.T) {
	const n = 150
	items := recoverWidgets(n)
	provider := newRecoverFakeProvider("fake-model")

	var remaining int32 = 5 // the first 5 batch calls (top-level + isolate) fail
	provider.batchDefault = func(tagged []taggedItem[recoverWidget]) (string, error) {
		if atomic.AddInt32(&remaining, -1) >= 0 {
			return "", &llm.RateLimitError{
				APIError:   &llm.APIError{Provider: "recover-fake", StatusCode: 429},
				RetryAfter: time.Millisecond,
			}
		}
		return fullSuccessBatch(tagged)
	}

	ctx := recoverCtx(t, provider)

	var result types.BatchResult[recoverSummary]
	var err error
	runWithDeadline(t, 20*time.Second, func() {
		result, err = RunOpManyRecover(ctx, recoverWidgetOp(), items, recoverCodec(), types.OpOptions{},
			RecoverConfig{MaxItemsPerCall: 15, StartItemsPerCall: 15, Concurrency: 8})
	})
	if err != nil {
		t.Fatalf("RunOpManyRecover: %v", err)
	}
	if !result.Complete() {
		t.Fatalf("Complete() = false, want true: Summary=%+v", result.Summary)
	}

	var mdspCount, atomicCount int
	for i, item := range result.Items {
		if item.Value.Length != len(items[i].Name) {
			t.Fatalf("item %d: Value.Length = %d, want %d", i, item.Value.Length, len(items[i].Name))
		}
		switch item.Mode {
		case ModeMDSP:
			mdspCount++
		case types.ModeAtomicFallback:
			atomicCount++
		}
	}
	if atomicCount == 0 {
		t.Fatal("nothing fell back to atomic despite the initial transport-failure burst")
	}
	if mdspCount == 0 {
		t.Fatal("nothing resolved via plain MDSP after the burst passed -- later chunks should not be dragged down by earlier ones")
	}
}

// TestLoadRunOpManyRecoverManyConcurrentCascadesDoNotCrossContaminate runs
// several independent RunOpManyRecover cascades concurrently, each against
// its own provider and its own items, and proves each one's result reflects
// only its own run: the cascade's internal state (records, AdaptiveChunkState,
// the ledger) is per-call, and this is the check that a shared or
// accidentally-package-level piece of that state would fail under real
// concurrency, the same way TestLoadRunOpManyPartialManyConcurrentBatchesDoNotCrossContaminate
// checks it for RunOpManyPartial.
func TestLoadRunOpManyRecoverManyConcurrentCascadesDoNotCrossContaminate(t *testing.T) {
	// A single model resolution for every goroutine: config.GetModel keys off
	// the environment and the provider's own Name(), neither of which a
	// per-goroutine t.Setenv could vary safely across concurrent cascades, and
	// nothing about this test needs them to differ -- the contamination this
	// test is checking for is between cascades' own item slices and results,
	// not between distinct models.
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")

	const cascades = 16
	const itemsPerCascade = 40

	var wg sync.WaitGroup
	var mismatches int64

	runWithDeadline(t, 20*time.Second, func() {
		for c := 0; c < cascades; c++ {
			wg.Add(1)
			go func(c int) {
				defer wg.Done()

				items := recoverWidgets(itemsPerCascade)
				provider := newRecoverFakeProvider("fake-model")
				rng := rand.New(rand.NewSource(int64(c) + 1))
				var handlerMu sync.Mutex
				provider.batchDefault = randomPartialCoverageBatch(rng, &handlerMu)

				ctx := WithProvider(context.Background(), provider)

				result, err := RunOpManyRecover(ctx, recoverWidgetOp(), items, recoverCodec(), types.OpOptions{},
					RecoverConfig{MaxItemsPerCall: 10, StartItemsPerCall: 10, Concurrency: 4})
				if err != nil {
					t.Errorf("cascade %d: RunOpManyRecover: %v", c, err)
					return
				}
				if !result.Complete() {
					t.Errorf("cascade %d: Complete() = false: %+v", c, result.Summary)
					return
				}
				for i, item := range result.Items {
					if item.Value.Length != len(items[i].Name) {
						atomic.AddInt64(&mismatches, 1)
						t.Errorf("cascade %d item %d: Value.Length = %d, want %d", c, i, item.Value.Length, len(items[i].Name))
					}
				}
			}(c)
		}
		wg.Wait()
	})

	if mismatches != 0 {
		t.Fatalf("%d item(s) across %d concurrent cascades showed cross-cascade contamination", mismatches, cascades)
	}
}
