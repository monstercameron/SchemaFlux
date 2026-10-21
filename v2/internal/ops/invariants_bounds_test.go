package ops

import (
	"strings"
	"testing"
)

// The rest of A-009's shared library: a ceiling, an exclusion, and the seam a
// caller's own rule runs through. Each has a pass case, a fail case, and a case
// pinning what the error is allowed to say — because the repair loop feeds
// these messages back to a model and every caller logs them.

func TestAtMostAcceptsCollectionsInsideTheCeiling(t *testing.T) {
	cases := []struct {
		name  string
		items []string
		limit int
	}{
		{"under the limit", []string{"a", "b"}, 3},
		{"exactly at the limit", []string{"a", "b", "c"}, 3},
		{"empty", nil, 3},
		{"limit of zero means unset", []string{"a", "b", "c"}, 0},
		{"negative limit means unset", []string{"a"}, -1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := AtMost(tc.items, tc.limit); err != nil {
				t.Errorf("AtMost(%d items, limit %d) = %v", len(tc.items), tc.limit, err)
			}
		})
	}
}

func TestAtMostRejectsAnOverrun(t *testing.T) {
	err := AtMost([]string{"a", "b", "c", "d"}, 2)
	if err == nil {
		t.Fatal("four items passed a limit of two")
	}
	// Both numbers, because "too many items" does not tell a repair loop how
	// many to drop.
	if !strings.Contains(err.Error(), "4") || !strings.Contains(err.Error(), "2") {
		t.Errorf("error names neither the count nor the limit: %v", err)
	}
}

// A ceiling that silently truncates turns a model that ignored the instruction
// into a result that looks obedient, which is the defect this replaces.
func TestAtMostDoesNotQuoteTheItems(t *testing.T) {
	err := AtMost([]string{"patient-record-alpha", "patient-record-beta", "patient-record-gamma"}, 1)
	if err == nil {
		t.Fatal("expected an overrun")
	}
	if strings.Contains(err.Error(), "patient-record") {
		t.Errorf("the caller's records are in the error string: %v", err)
	}
}
