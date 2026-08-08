package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// C-06: every collection operation marshalled the whole slice into one prompt
// with no size guard, while output tokens are capped at 1000–4000 by tier. Sort
// and Filter have to echo every item back, so a Sort over a few hundred modest
// objects could not physically return a complete result: the completion
// truncated, the JSON failed to parse, and the caller got a parse error that
// said nothing about the real cause.

type guardItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

func guardItems(n int) []guardItem {
	items := make([]guardItem, n)
	for i := range items {
		items[i] = guardItem{
			ID:          fmt.Sprintf("item-%05d", i),
			Name:        fmt.Sprintf("Item number %d", i),
			Description: "A description long enough to be realistic rather than a toy.",
		}
	}
	return items
}

// OP-101/OP-106: this used to be a test that Sort refused a batch too large
// to echo back, because the answer had to reproduce every item and the
// output budget could not hold five hundred of them. The answer is now a
// handful of ids, and above sortScoringThreshold the item count routes
// straight to independent per-item scoring (OP-106) instead of the
// whole-list path at all, so a batch this size is no longer refused for size
// -- it is scored, one small call per item, and sorted in Go. That is the
// ceiling OP-101 describes removing, made concrete: the same 500-item sort at
// the smallest tier that used to fail now succeeds.
func TestSortNoLongerRefusesALargeBatchForSize(t *testing.T) {
	items := guardItems(500)

	previous := customLLMCaller
	setLLMCaller(func(_ context.Context, system, _ string, _ types.OpOptions) (string, error) {
		// The scoring fallback's system prompt asks for a rank_score; every
		// item gets the same one, so what is being asserted is that the sort
		// completes and returns every item, not a particular order.
		if strings.Contains(system, "rank_score") {
			return `{"rank_score": 0.5}`, nil
		}
		return `{"ids": []}`, nil
	})
	defer func() { customLLMCaller = previous }()

	opts := NewSortOptions().WithCriteria("by name")
	// SortOptions embeds both CommonOptions and types.OpOptions, and
	// mergeEmbeddedOpOptions takes Mode and Intelligence from CommonOptions
	// unconditionally -- so writing to the other one is silently ignored. See
	// X-07.
	opts.CommonOptions.Intelligence = types.Quick // the 1000-token tier: the old ceiling

	sorted, err := Sort(items, opts)
	if err != nil {
		t.Fatalf("a 500-item sort must use scoring rather than being refused for size: %v", err)
	}
	if len(sorted) != len(items) {
		t.Fatalf("sorted %d items, want %d", len(sorted), len(items))
	}
	if err := SameMultiset(items, sorted); err != nil {
		t.Errorf("scoring must still return every item exactly once: %v", err)
	}
}

// Filter no longer refuses a large batch: it chunks. The merge for a filter is
// a concatenation, because each item's fate depends only on the criteria, so
// splitting is safe. Sort is not split, because interleaving two sorted runs
// needs knowledge the library does not have.
func TestFilterChunksRatherThanRefusing(t *testing.T) {
	// An id-only answer no longer scales with item content, so it takes many
	// more items than the old echo protocol did to exceed the output budget --
	// see filterChunkSize's comment. 600 items comfortably forces more than
	// one chunk at the quick tier; 120 no longer does.
	items := guardItems(600)

	// Every chunk keeps the id of its first item, whatever the chunk
	// boundaries are.
	previous := customLLMCaller
	setLLMCaller(func(_ context.Context, _, user string, _ types.OpOptions) (string, error) {
		var chunk []taggedItem[guardItem]
		start := strings.Index(user, "[")
		if start < 0 {
			return `{"ids":[]}`, nil
		}
		if err := json.Unmarshal([]byte(user[start:]), &chunk); err != nil {
			return `{"ids":[]}`, nil
		}
		if len(chunk) == 0 {
			return `{"ids":[]}`, nil
		}
		kept, err := json.Marshal(idListResponse{IDs: []string{chunk[0].ID}})
		if err != nil {
			return `{"ids":[]}`, nil
		}
		return string(kept), nil
	})
	defer func() { customLLMCaller = previous }()

	opts := NewFilterOptions().WithCriteria("anything")
	opts.CommonOptions.Intelligence = types.Quick

	kept, err := Filter(items, opts)
	if err != nil {
		t.Fatalf("Filter must chunk rather than refuse: %v", err)
	}
	if len(kept) == 0 {
		t.Fatal("chunking produced nothing")
	}
	// One item per chunk, so more than one result means it really did split.
	if len(kept) < 2 {
		t.Errorf("kept %d items; the collection should have been split into several chunks", len(kept))
	}
	// And every kept item came from the input, in input order.
	for i := 1; i < len(kept); i++ {
		if kept[i-1].ID >= kept[i].ID {
			t.Errorf("results are not in input order: %q then %q", kept[i-1].ID, kept[i].ID)
		}
	}
}

// A chunk that fails fails the call. A filter that quietly returned the items
// from the chunks that happened to succeed would be exactly the fail-open
// behaviour this library exists to remove.
func TestFilterFailsWhenAChunkFails(t *testing.T) {
	items := guardItems(120)

	restore := stubLLM("I'm sorry, I can't help with that.")
	defer restore()

	opts := NewFilterOptions().WithCriteria("anything")
	opts.CommonOptions.Intelligence = types.Quick

	kept, err := Filter(items, opts)
	if err == nil {
		t.Fatalf("a failing chunk must fail the call; got %d items", len(kept))
	}
	if kept != nil {
		t.Error("a failed filter must return nothing, not a partial result")
	}
}

// A batch that fits is not refused, at any tier.
func TestCollectionsAcceptBatchesThatFit(t *testing.T) {
	items := guardItems(3)

	for _, tier := range []struct {
		name  string
		speed types.Speed
	}{
		{"quick", types.Quick},
		{"fast", types.Fast},
		{"smart", types.Smart},
	} {
		t.Run(tier.name, func(t *testing.T) {
			// The answer is the assigned ids, in any order -- identity order
			// here, since the test is about the size guard, not the sort.
			restore := stubLLM(mustEncode(t, idListResponse{IDs: itemIDs(len(items))}))
			defer restore()

			opts := NewSortOptions().WithCriteria("by name")
			opts.CommonOptions.Intelligence = tier.speed

			if _, err := Sort(items, opts); err != nil {
				t.Errorf("a three-item sort must not be refused at the %s tier: %v", tier.name, err)
			}
		})
	}
}

// A larger tier accepts a batch a smaller one refuses, which is what makes the
// error's advice actionable.
func TestARicherTierAcceptsMore(t *testing.T) {
	items := guardItems(60)
	payload := mustEncode(t, items)

	quick := checkEchoBudget("Sort", payload, len(items), types.Quick, 1.0)
	smart := checkEchoBudget("Sort", payload, len(items), types.Smart, 1.0)

	if quick == nil {
		t.Skip("60 items fit even the quick tier; the guard is not exercised here")
	}
	if smart != nil {
		t.Errorf("the smart tier refuses what quick refuses: %v", smart)
	}
}

// The guard itself.
func TestCheckEchoBudget(t *testing.T) {
	cases := []struct {
		name       string
		payload    string
		itemCount  int
		speed      types.Speed
		echoFactor float64
		wantErr    bool
	}{
		{"empty_collection", "", 0, types.Quick, 1.0, false},
		{"tiny_payload", `[{"a":1}]`, 1, types.Quick, 1.0, false},
		{"huge_payload_full_echo", strings.Repeat("x", 40000), 100, types.Quick, 1.0, true},
		{"huge_payload_no_echo", strings.Repeat("x", 40000), 100, types.Quick, 0.0, false},
		{"huge_payload_smart_tier", strings.Repeat("x", 12000), 100, types.Smart, 1.0, false},
		{"huge_payload_quick_tier", strings.Repeat("x", 12000), 100, types.Quick, 1.0, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkEchoBudget("Test", tc.payload, tc.itemCount, tc.speed, tc.echoFactor)
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Errorf("err = %v, want error = %v", err, tc.wantErr)
			}
		})
	}
}

// The estimate has to be monotonic in size, or the guard is arbitrary.
func TestEstimateTokensGrowsWithSize(t *testing.T) {
	previous := 0
	for _, size := range []int{0, 1, 10, 100, 1000, 10000} {
		got := estimateTokens(strings.Repeat("x", size))
		if got < previous {
			t.Errorf("a %d-character payload estimated %d tokens, fewer than the previous %d",
				size, got, previous)
		}
		previous = got
	}
}

func mustEncode(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}
