package ops

import (
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

// A batch that cannot be returned is refused, with an error that says why.
func TestSortRefusesABatchItCannotReturn(t *testing.T) {
	restore := stubLLM(`[]`)
	defer restore()

	opts := NewSortOptions().WithCriteria("by name")
	// SortOptions embeds both CommonOptions and types.OpOptions, and
	// mergeEmbeddedOpOptions takes Mode and Intelligence from CommonOptions
	// unconditionally -- so writing to the other one is silently ignored. See
	// X-07.
	opts.CommonOptions.Intelligence = types.Quick // the 1000-token tier

	_, err := Sort(guardItems(500), opts)
	if err == nil {
		t.Fatal("a batch that cannot fit the output budget must be refused")
	}

	for _, phrase := range []string{"Sort", "500", "token", "quick"} {
		if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(phrase)) {
			t.Errorf("the error does not mention %q: %v", phrase, err)
		}
	}
	// It has to say what would work, not only that this does not.
	if !strings.Contains(err.Error(), "would fit") {
		t.Errorf("the error does not say how many items would fit: %v", err)
	}
}

func TestFilterRefusesABatchItCannotReturn(t *testing.T) {
	restore := stubLLM(`[]`)
	defer restore()

	opts := NewFilterOptions().WithCriteria("anything")
	opts.CommonOptions.Intelligence = types.Quick

	if _, err := Filter(guardItems(500), opts); err == nil {
		t.Fatal("a filter that cannot echo its worst case must be refused")
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
			restore := stubLLM(mustEncode(t, items))
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
