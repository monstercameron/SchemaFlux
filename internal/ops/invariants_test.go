package ops

import (
	"strings"
	"testing"
)

type invItem struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Price string `json:"price"`
}

func invItems(n int) []invItem {
	items := make([]invItem, n)
	for i := range items {
		items[i] = invItem{ID: i + 1, Name: "item", Price: "1.00"}
	}
	return items
}

// The failure a count check cannot see: same length, wrong contents.
func TestSameMultisetCatchesWhatACountMisses(t *testing.T) {
	input := invItems(4)

	cases := []struct {
		name    string
		output  []invItem
		wantErr bool
		expect  string
	}{
		{"identical", input, false, ""},
		{"reordered", []invItem{input[3], input[0], input[2], input[1]}, false, ""},
		{
			"one duplicated and one dropped -- the same length",
			[]invItem{input[0], input[0], input[2], input[3]},
			true, "missing",
		},
		{
			"an item edited in place",
			[]invItem{input[0], input[1], input[2], {ID: 4, Name: "item", Price: "9.99"}},
			true, "missing",
		},
		{"short", input[:3], true, "3 items for 4"},
		{"long", append(append([]invItem{}, input...), input[0]), true, "5 items for 4"},
		{"empty against empty", []invItem{}, true, "0 items for 4"},
		{
			"entirely invented",
			[]invItem{{ID: 9}, {ID: 10}, {ID: 11}, {ID: 12}},
			true, "missing",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := SameMultiset(input, tc.output)
			if tc.wantErr && err == nil {
				t.Fatal("SameMultiset accepted a result that is not a permutation")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("SameMultiset = %v, want nil", err)
			}
			if err != nil && tc.expect != "" && !strings.Contains(err.Error(), tc.expect) {
				t.Errorf("error %q does not mention %q", err.Error(), tc.expect)
			}
		})
	}
}

// The error describes how much went wrong, never what. These are the caller's
// own records.
func TestInvariantErrorsCarryNoValues(t *testing.T) {
	input := []invItem{{ID: 1, Name: "SSN-123-45-6789"}}
	output := []invItem{{ID: 2, Name: "different"}}

	err := SameMultiset(input, output)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "SSN-123-45-6789") {
		t.Errorf("the error carries the payload: %v", err)
	}
}

// An empty input and an empty output agree, which matters because a filter that
// legitimately matched nothing must not be an error.
func TestEmptyInputsAgree(t *testing.T) {
	if err := SameMultiset([]invItem{}, []invItem{}); err != nil {
		t.Errorf("SameMultiset on two empty slices = %v", err)
	}
	if err := SubsetOf([]invItem{}, []invItem{}); err != nil {
		t.Errorf("SubsetOf on two empty slices = %v", err)
	}
}

func TestSubsetOf(t *testing.T) {
	input := invItems(5)

	cases := []struct {
		name    string
		output  []invItem
		wantErr bool
	}{
		{"empty subset", []invItem{}, false},
		{"proper subset", []invItem{input[0], input[3]}, false},
		{"the whole set", input, false},
		{"reordered subset", []invItem{input[4], input[1]}, false},
		{"one item not offered", []invItem{input[0], {ID: 99}}, true},
		{"an offered item, edited", []invItem{{ID: 1, Name: "item", Price: "9.99"}}, true},
		{"a duplicate of an offered item", []invItem{input[0], input[0]}, true},
		{"longer than the input", append(append([]invItem{}, input...), input[0]), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := SubsetOf(input, tc.output)
			if tc.wantErr && err == nil {
				t.Error("SubsetOf accepted a result that is not a subset")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("SubsetOf = %v, want nil", err)
			}
		})
	}
}

func TestCoversExactlyOnce(t *testing.T) {
	cases := []struct {
		name    string
		total   int
		groups  [][]int
		wantErr string
	}{
		{"a clean partition", 5, [][]int{{0, 1}, {2}, {3, 4}}, ""},
		{"one group holding everything", 3, [][]int{{0, 1, 2}}, ""},
		{"empty groups are harmless", 3, [][]int{{0, 1, 2}, {}}, ""},
		{"an item in no group", 4, [][]int{{0, 1}, {2}}, "in no group"},
		{"an item in two groups", 3, [][]int{{0, 1}, {1, 2}}, "more than one group"},
		{"an index past the end", 3, [][]int{{0, 1, 2, 7}}, "out of range"},
		{"a negative index", 3, [][]int{{0, 1, 2, -1}}, "out of range"},
		{"nothing grouped at all", 2, [][]int{}, "in no group"},
		{"several problems at once", 4, [][]int{{0, 0}, {9}}, "out of range"},
		{"zero items and no groups", 0, [][]int{}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CoversExactlyOnce(tc.total, tc.groups)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("CoversExactlyOnce = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("CoversExactlyOnce accepted groups that do not partition %d items", tc.total)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not mention %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestMemberOf(t *testing.T) {
	offered := invItems(3)

	if err := MemberOf(offered, offered[1]); err != nil {
		t.Errorf("MemberOf on an offered item = %v", err)
	}
	if err := MemberOf(offered, invItem{ID: 99}); err == nil {
		t.Error("MemberOf accepted an item that was never offered")
	}
	// The failure that matters is not an invented item but an echoed one with a
	// changed field.
	if err := MemberOf(offered, invItem{ID: 2, Name: "item", Price: "9.99"}); err == nil {
		t.Error("MemberOf accepted an offered item whose price had been changed")
	}
	if err := MemberOf([]invItem{}, invItem{ID: 1}); err == nil {
		t.Error("MemberOf accepted a selection from an empty set")
	}
}

// Map-valued items compare by content, not by iteration order, because
// encoding/json sorts keys.
func TestCanonicalKeyIsStableForMaps(t *testing.T) {
	a := map[string]int{"z": 1, "a": 2, "m": 3}
	b := map[string]int{"a": 2, "m": 3, "z": 1}

	if canonicalKey(a) != canonicalKey(b) {
		t.Errorf("two equal maps produced different keys:\n  %s\n  %s", canonicalKey(a), canonicalKey(b))
	}
	if err := SameMultiset([]map[string]int{a}, []map[string]int{b}); err != nil {
		t.Errorf("SameMultiset over equal maps = %v", err)
	}
}
