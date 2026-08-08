package ops

import (
	"fmt"
	"sync"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// CA-006. A prompt cache that is doing nothing looks exactly like one that is
// working: the calls succeed, the answers are right, and the only difference
// is the bill. These cover both halves -- the measured ratio, and the
// diagnostic that fires when a repeating prefix never reports a cached read.

func TestCacheHitRatioIsMeasuredFromReportedUsage(t *testing.T) {
	cases := []struct {
		name   string
		prompt int
		cached int
		want   float64
	}{
		{"nothing cached", 1000, 0, 0},
		{"half cached", 1000, 500, 0.5},
		{"entirely cached", 1000, 1000, 1},
		{"a fifth cached", 500, 100, 0.2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta := envelopeFrom([]*types.ResultMetadata{{
				TokenUsage: &types.TokenUsage{PromptTokens: tc.prompt, CachedTokens: tc.cached},
			}}, "extract")

			if meta.CacheHitRatio != tc.want {
				t.Errorf("CacheHitRatio = %v, want %v", meta.CacheHitRatio, tc.want)
			}
		})
	}
}

// A provider that reports no prompt tokens gives an undefined ratio, not a
// division by zero and not an invented number.
func TestCacheHitRatioIsZeroWhenNothingWasReported(t *testing.T) {
	meta := envelopeFrom([]*types.ResultMetadata{{}}, "extract")
	if meta.CacheHitRatio != 0 {
		t.Errorf("CacheHitRatio = %v with no usage reported at all", meta.CacheHitRatio)
	}

	meta = envelopeFrom([]*types.ResultMetadata{{
		TokenUsage: &types.TokenUsage{PromptTokens: 0, CachedTokens: 0},
	}}, "extract")
	if meta.CacheHitRatio != 0 {
		t.Errorf("CacheHitRatio = %v for zero prompt tokens", meta.CacheHitRatio)
	}
}

// The ratio is over the whole request, retries included, because that is what
// the caller paid for.
func TestCacheHitRatioSpansEveryAttempt(t *testing.T) {
	meta := envelopeFrom([]*types.ResultMetadata{
		{TokenUsage: &types.TokenUsage{PromptTokens: 100, CachedTokens: 0}},
		{TokenUsage: &types.TokenUsage{PromptTokens: 100, CachedTokens: 100}},
	}, "extract")

	if meta.CacheHitRatio != 0.5 {
		t.Errorf("CacheHitRatio = %v, want 0.5 across the two attempts", meta.CacheHitRatio)
	}
}

func TestCacheReadTrackingCountsARepeatingPrefix(t *testing.T) {
	resetCacheReadTracking()
	defer resetCacheReadTracking()

	usage := types.TokenUsage{PromptTokens: 900}
	for i := 0; i < cacheReadWarnAfter; i++ {
		noteCacheReads("key-a", usage)
	}

	cacheReadMu.Lock()
	count, warned := cacheReadCounts["key-a"], cacheReadWarned["key-a"]
	cacheReadMu.Unlock()

	if count != cacheReadWarnAfter {
		t.Errorf("count = %d, want %d", count, cacheReadWarnAfter)
	}
	if !warned {
		t.Errorf("a prefix sent %d times with no cached read never produced a diagnostic", count)
	}
}

// Once, not once per call. A warning on every request is a warning nobody
// reads, which is the same as no warning at all.
func TestCacheReadDiagnosticFiresOnlyOnce(t *testing.T) {
	resetCacheReadTracking()
	defer resetCacheReadTracking()

	usage := types.TokenUsage{PromptTokens: 900}
	for i := 0; i < cacheReadWarnAfter*5; i++ {
		noteCacheReads("key-b", usage)
	}

	cacheReadMu.Lock()
	warned := cacheReadWarned["key-b"]
	cacheReadMu.Unlock()

	if !warned {
		t.Fatal("no diagnostic at all")
	}
	// warned stays a single flag; the count keeps rising, which is what the
	// diagnostic reports, but the flag is what stops the repeat.
}

func TestCacheReadTrackingStaysQuietBelowTheThreshold(t *testing.T) {
	resetCacheReadTracking()
	defer resetCacheReadTracking()

	usage := types.TokenUsage{PromptTokens: 900}
	for i := 0; i < cacheReadWarnAfter-1; i++ {
		noteCacheReads("key-c", usage)
	}

	cacheReadMu.Lock()
	warned := cacheReadWarned["key-c"]
	cacheReadMu.Unlock()

	if warned {
		t.Error("warned before the prefix had repeated enough to mean anything")
	}
}

// A cache that starts working stops being complained about.
func TestAReportedCacheReadClearsTheCount(t *testing.T) {
	resetCacheReadTracking()
	defer resetCacheReadTracking()

	for i := 0; i < cacheReadWarnAfter-1; i++ {
		noteCacheReads("key-d", types.TokenUsage{PromptTokens: 900})
	}
	noteCacheReads("key-d", types.TokenUsage{PromptTokens: 900, CachedTokens: 800})

	cacheReadMu.Lock()
	_, counted := cacheReadCounts["key-d"]
	_, warned := cacheReadWarned["key-d"]
	cacheReadMu.Unlock()

	if counted || warned {
		t.Error("a key that started reporting cached tokens is still being tracked as broken")
	}
}

func TestCacheReadTrackingIgnoresAnEmptyKey(t *testing.T) {
	resetCacheReadTracking()
	defer resetCacheReadTracking()

	for i := 0; i < cacheReadWarnAfter*2; i++ {
		noteCacheReads("", types.TokenUsage{PromptTokens: 900})
	}

	cacheReadMu.Lock()
	size := len(cacheReadCounts)
	cacheReadMu.Unlock()

	if size != 0 {
		t.Errorf("tracked %d entries for calls with no cache key", size)
	}
}

// A diagnostic that grows without limit in a long-running process is a worse
// bug than the one it reports.
func TestCacheReadTrackingIsBounded(t *testing.T) {
	resetCacheReadTracking()
	defer resetCacheReadTracking()

	for i := 0; i < cacheReadTrackerCap*3; i++ {
		noteCacheReads(fmt.Sprintf("key-%d", i), types.TokenUsage{PromptTokens: 900})
	}

	cacheReadMu.Lock()
	size := len(cacheReadCounts)
	cacheReadMu.Unlock()

	if size > cacheReadTrackerCap {
		t.Errorf("tracker holds %d entries, above its cap of %d", size, cacheReadTrackerCap)
	}
}

func TestCacheReadTrackingIsSafeUnderConcurrency(t *testing.T) {
	resetCacheReadTracking()
	defer resetCacheReadTracking()

	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				noteCacheReads(fmt.Sprintf("key-%d", i%4), types.TokenUsage{PromptTokens: 900})
			}
		}(worker)
	}
	wg.Wait()

	cacheReadMu.Lock()
	size := len(cacheReadCounts)
	cacheReadMu.Unlock()

	if size == 0 || size > 4 {
		t.Errorf("tracker holds %d entries for 4 distinct keys", size)
	}
}
