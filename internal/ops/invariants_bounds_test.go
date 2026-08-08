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

func TestExcludesValuesAcceptsACleanAnswer(t *testing.T) {
	cases := []struct {
		name      string
		forbidden []string
		output    []string
	}{
		{"nothing forbidden", nil, []string{"a", "b"}},
		{"empty output", []string{"a"}, nil},
		{"disjoint", []string{"x", "y"}, []string{"a", "b"}},
		{"both empty", nil, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ExcludesValues(tc.forbidden, tc.output); err != nil {
				t.Errorf("ExcludesValues = %v, want nil", err)
			}
		})
	}
}

func TestExcludesValuesRejectsAForbiddenValue(t *testing.T) {
	err := ExcludesValues([]string{"secret"}, []string{"fine", "secret", "also fine"})
	if err == nil {
		t.Fatal("a forbidden value passed")
	}
	if !strings.Contains(err.Error(), "1") {
		t.Errorf("error does not report how many: %v", err)
	}
}

func TestExcludesValuesCountsEveryViolation(t *testing.T) {
	err := ExcludesValues([]string{"x", "y"}, []string{"x", "ok", "y", "x"})
	if err == nil {
		t.Fatal("three forbidden values passed")
	}
	if !strings.Contains(err.Error(), "3") {
		t.Errorf("want a count of 3, got: %v", err)
	}
}

// The reason this counts rather than quotes: a redaction operation reporting
// "the output still contains 4111-1111-1111-1111" has leaked the exact thing it
// was asked to remove, into a string every caller logs.
func TestExcludesValuesNeverNamesTheValue(t *testing.T) {
	err := ExcludesValues([]string{"4111-1111-1111-1111"}, []string{"4111-1111-1111-1111"})
	if err == nil {
		t.Fatal("expected a violation")
	}
	if strings.Contains(err.Error(), "4111") {
		t.Errorf("the excluded value leaked into the error: %v", err)
	}
}

// Comparison is canonical, not by Go equality, so a struct echoed back with the
// same fields is the same value however it was rebuilt.
func TestExcludesValuesComparesStructsByValue(t *testing.T) {
	type record struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}

	err := ExcludesValues(
		[]record{{ID: 1, Name: "banned"}},
		[]record{{ID: 2, Name: "fine"}, {ID: 1, Name: "banned"}},
	)
	if err == nil {
		t.Fatal("a banned record passed because it was a different Go value")
	}
}

func TestSatisfiesRunsTheCallersRule(t *testing.T) {
	passing := func(n int) bool { return n > 0 }

	if err := Satisfies(5, "must be positive", passing); err != nil {
		t.Errorf("a satisfied rule reported %v", err)
	}
	err := Satisfies(-1, "must be positive", passing)
	if err == nil {
		t.Fatal("a violated rule passed")
	}
	if !strings.Contains(err.Error(), "must be positive") {
		t.Errorf("the error does not name the rule, so a repair has nothing to act on: %v", err)
	}
}

// A nil predicate is a rule that cannot fail. Accepting one silently weakens
// the contract of every call that registered it, so it is an error rather than
// a no-op.
func TestSatisfiesRejectsARuleThatCannotRun(t *testing.T) {
	if err := Satisfies(5, "some rule", nil); err == nil {
		t.Error("a nil predicate was treated as a passing rule")
	}
}

func TestSatisfiesRequiresAName(t *testing.T) {
	always := func(int) bool { return true }

	for _, name := range []string{"", "   ", "\t"} {
		if err := Satisfies(5, name, always); err == nil {
			t.Errorf("an invariant named %q was accepted; a failure could not be reported usefully", name)
		}
	}
}

func TestSatisfiesDoesNotQuoteTheValue(t *testing.T) {
	err := Satisfies("ssn 123-45-6789", "must be redacted", func(string) bool { return false })
	if err == nil {
		t.Fatal("expected a violation")
	}
	if strings.Contains(err.Error(), "123-45-6789") {
		t.Errorf("the checked value leaked into the error: %v", err)
	}
}
