package utils

import (
	"strings"
	"sync"
	"testing"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/telemetry"
)

func TestGenerateRequestID_Unique(t *testing.T) {
	const n = 200
	ids := make([]string, n)
	for i := range ids {
		ids[i] = GenerateRequestID()
	}

	seen := make(map[string]bool, n)
	for _, id := range ids {
		if id == "" {
			t.Fatal("GenerateRequestID() returned an empty string")
		}
		if seen[id] {
			t.Fatalf("GenerateRequestID() produced a duplicate: %q", id)
		}
		seen[id] = true
		// Shape check: "<unixnano>-<pid>-<counter>", three dash-separated
		// non-empty parts, not an assertion about specific values.
		parts := strings.Split(id, "-")
		if len(parts) != 3 {
			t.Fatalf("GenerateRequestID() = %q, want 3 dash-separated parts, got %d", id, len(parts))
		}
		for _, p := range parts {
			if p == "" {
				t.Fatalf("GenerateRequestID() = %q has an empty component", id)
			}
		}
	}
}

// TestGenerateRequestID_UniqueUnderConcurrency exercises the atomic counter
// that exists specifically to keep IDs unique "even when called in quick
// succession" (the function's own doc comment) -- a sequential-only test
// would never touch the reason atomic.AddUint64 is there instead of a plain
// increment.
func TestGenerateRequestID_UniqueUnderConcurrency(t *testing.T) {
	const goroutines = 50
	const perGoroutine = 20

	var mu sync.Mutex
	seen := make(map[string]bool, goroutines*perGoroutine)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				id := GenerateRequestID()
				mu.Lock()
				if seen[id] {
					mu.Unlock()
					t.Errorf("duplicate request ID under concurrency: %q", id)
					return
				}
				seen[id] = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
}

func TestMin(t *testing.T) {
	cases := []struct {
		name string
		a, b int
		want int
	}{
		{"a less than b", 1, 5, 1},
		{"b less than a", 5, 1, 1},
		{"equal", 3, 3, 3},
		{"both negative, a smaller", -5, -1, -5},
		{"both negative, b smaller", -1, -5, -5},
		{"a negative b positive", -1, 1, -1},
		{"a positive b negative", 1, -1, -1},
		{"zero and positive", 0, 4, 0},
		{"zero and negative", 0, -4, -4},
		{"large values", 1_000_000, 999_999, 999_999},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Min(tc.a, tc.b); got != tc.want {
				t.Errorf("Min(%d, %d) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestRecordMetricDelegatesToTelemetry(t *testing.T) {
	telemetry.ResetMetrics()
	t.Cleanup(telemetry.ResetMetrics)
	t.Setenv("SCHEMAFLUX_METRICS", "")

	original := config.IsMetricsEnabled()
	t.Cleanup(func() { config.SetMetricsEnabled(original) })
	config.SetMetricsEnabled(true)

	RecordMetric("internal_metric", 7, map[string]string{"source": "utils"})

	snapshot, ok := telemetry.GetMetricSnapshot("internal_metric", map[string]string{"source": "utils"})
	if !ok {
		t.Fatal("expected metric snapshot to be recorded")
	}
	if snapshot.Count != 1 || snapshot.Sum != 7 {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
}
