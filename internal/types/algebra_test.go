package types

import "testing"

// AlgebraKind.String and RequiresExactCoverage are read by anything
// rendering a batch algebra into a log line or deciding whether the shared
// id protocol may validate a batch generically. Table-driven because every
// case rhymes: one kind in, one rendering out.
func TestAlgebraKindString(t *testing.T) {
	cases := []struct {
		kind AlgebraKind
		want string
	}{
		{AlgebraUnspecified, "unspecified"},
		{AlgebraIndependent, "independent"},
		{AlgebraSubset, "subset"},
		{AlgebraPermutation, "permutation"},
		{AlgebraPartition, "partition"},
		{AlgebraGraph, "graph"},
		{AlgebraHierarchical, "hierarchical"},
		{AlgebraSequential, "sequential"},
		{AlgebraKind(99), "unspecified"}, // an unrecognised value falls through, never invents a name
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.kind.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

// RequiresExactCoverage's one false case is Subset, whose contract
// explicitly permits fewer ids back than were offered -- every other kind,
// including an unrecognised value, defaults to requiring exact coverage
// rather than silently tolerating a dropped id.
func TestAlgebraKindRequiresExactCoverage(t *testing.T) {
	cases := []struct {
		kind AlgebraKind
		want bool
	}{
		{AlgebraSubset, false},
		{AlgebraUnspecified, true},
		{AlgebraIndependent, true},
		{AlgebraPermutation, true},
		{AlgebraPartition, true},
		{AlgebraGraph, true},
		{AlgebraHierarchical, true},
		{AlgebraSequential, true},
		{AlgebraKind(99), true},
	}
	for _, tc := range cases {
		t.Run(tc.kind.String(), func(t *testing.T) {
			if got := tc.kind.RequiresExactCoverage(); got != tc.want {
				t.Errorf("RequiresExactCoverage() = %v, want %v", got, tc.want)
			}
		})
	}
}
