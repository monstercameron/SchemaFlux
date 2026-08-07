package ops

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

type batchRecord struct {
	Name string `json:"name"`
}

// installNormalizeCaller swaps in a scripted caller and restores the previous
// one, so these cases never reach a provider.
func installNormalizeCaller(t *testing.T, caller LLMCaller) {
	t.Helper()
	previous, previousProvider := currentHooks()
	setLLMCaller(caller)
	t.Cleanup(func() {
		setLLMCaller(previous)
		SetDefaultProvider(previousProvider)
	})
}

// OP-304. NormalizeBatch was a serial loop: one blocking provider call per
// item, so two hundred records took two hundred round trips end to end.
func TestNormalizeBatchRunsConcurrently(t *testing.T) {
	const items = 24
	const concurrency = 6

	var inFlight, peak int32
	var mu sync.Mutex
	release := make(chan struct{})

	installNormalizeCaller(t, func(context.Context, string, string, types.OpOptions) (string, error) {
		current := atomic.AddInt32(&inFlight, 1)
		mu.Lock()
		if current > peak {
			peak = current
		}
		mu.Unlock()

		<-release // hold every worker until the test lets go
		atomic.AddInt32(&inFlight, -1)
		return `{"normalized":{"name":"ada"},"changes":[],"total_changes":0}`, nil
	})

	input := make([]batchRecord, items)
	for i := range input {
		input[i] = batchRecord{Name: fmt.Sprintf("record-%02d", i)}
	}

	done := make(chan error, 1)
	go func() {
		_, err := NormalizeBatch(input, NewNormalizeOptions().WithConcurrency(concurrency))
		done <- err
	}()

	// Wait until the workers are all parked, then let them go.
	for atomic.LoadInt32(&inFlight) < int32(concurrency) {
	}
	close(release)

	if err := <-done; err != nil {
		t.Fatalf("NormalizeBatch: %v", err)
	}

	mu.Lock()
	observed := peak
	mu.Unlock()

	if observed < 2 {
		t.Errorf("peak concurrency was %d; the batch ran serially", observed)
	}
	if observed > concurrency {
		t.Errorf("peak concurrency was %d, above the configured %d", observed, concurrency)
	}
}

// Results stay in input order regardless of which item finishes first.
func TestNormalizeBatchPreservesInputOrder(t *testing.T) {
	var counter int32

	installNormalizeCaller(t, func(_ context.Context, _, userPrompt string, _ types.OpOptions) (string, error) {
		// Later items answer first, which is what a real provider does under
		// concurrency and what an ordered result has to survive.
		n := atomic.AddInt32(&counter, 1)
		_ = n
		for _, name := range []string{"alpha", "bravo", "charlie", "delta"} {
			if containsName(userPrompt, name) {
				return fmt.Sprintf(`{"normalized":{"name":%q},"changes":[],"total_changes":0}`, name+"-done"), nil
			}
		}
		return `{"normalized":{"name":"unknown"},"changes":[],"total_changes":0}`, nil
	})

	input := []batchRecord{{Name: "alpha"}, {Name: "bravo"}, {Name: "charlie"}, {Name: "delta"}}
	results, err := NormalizeBatch(input, NewNormalizeOptions().WithConcurrency(4))
	if err != nil {
		t.Fatalf("NormalizeBatch: %v", err)
	}
	if len(results) != len(input) {
		t.Fatalf("got %d results, want %d", len(results), len(input))
	}
	for i, want := range []string{"alpha-done", "bravo-done", "charlie-done", "delta-done"} {
		if results[i].Normalized.Name != want {
			t.Errorf("result %d = %q, want %q -- order was lost", i, results[i].Normalized.Name, want)
		}
	}
}

func containsName(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

// A failure names the item's index, because a caller who gets an error needs to
// know which of their records is missing -- and it is the lowest-indexed
// failure, so the error does not depend on which worker finished first.
func TestNormalizeBatchReportsTheFailingIndex(t *testing.T) {
	installNormalizeCaller(t, func(_ context.Context, _, userPrompt string, _ types.OpOptions) (string, error) {
		if containsName(userPrompt, "bad") {
			return "", fmt.Errorf("this one failed")
		}
		return `{"normalized":{"name":"ok"},"changes":[],"total_changes":0}`, nil
	})

	input := []batchRecord{{Name: "fine"}, {Name: "bad"}, {Name: "fine"}}
	_, err := NormalizeBatch(input, NewNormalizeOptions())
	if err == nil {
		t.Fatal("a failing item must fail the batch")
	}
	if !containsName(err.Error(), "item 1") {
		t.Errorf("the error does not name the failing index: %v", err)
	}
}

// An empty batch is not an error and makes no calls.
func TestNormalizeBatchOnAnEmptySlice(t *testing.T) {
	var calls int32
	installNormalizeCaller(t, func(context.Context, string, string, types.OpOptions) (string, error) {
		atomic.AddInt32(&calls, 1)
		return `{}`, nil
	})

	results, err := NormalizeBatch([]batchRecord{}, NewNormalizeOptions())
	if err != nil {
		t.Fatalf("NormalizeBatch on an empty slice = %v", err)
	}
	if len(results) != 0 {
		t.Errorf("got %d results", len(results))
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Errorf("an empty batch made %d provider calls", calls)
	}
}
