package types

import "testing"

// OperationID.String is what a plan, a cache key, and provenance render --
// the three shapes it must handle: both parts set, name missing, version
// missing.
func TestOperationIDString(t *testing.T) {
	cases := []struct {
		name string
		id   OperationID
		want string
	}{
		{"both set", OperationID{Name: "extract", Version: "v1"}, "extract@v1"},
		{"no name", OperationID{Version: "v1"}, "unnamed@v1"},
		{"no version", OperationID{Name: "extract"}, "extract@unversioned"},
		{"neither", OperationID{}, "unnamed@"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.id.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCategoryString(t *testing.T) {
	cases := []struct {
		c    Category
		want string
	}{
		{CategoryUnspecified, "unspecified"},
		{CategoryExtraction, "extraction"},
		{CategoryTransformation, "transformation"},
		{CategoryGeneration, "generation"},
		{CategorySelection, "selection"},
		{CategoryJudgment, "judgment"},
		{Category(99), "unspecified"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.c.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

// StabilityTier.String is what NewOp's build check (a stable operation with
// no declared batch class fails construction) reports against.
func TestStabilityTierString(t *testing.T) {
	cases := []struct {
		s    StabilityTier
		want string
	}{
		{StabilityUnspecified, "unspecified"},
		{StabilityExperimental, "experimental"},
		{StabilityStable, "stable"},
		{StabilityTier(99), "unspecified"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.s.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBatchClassString(t *testing.T) {
	cases := []struct {
		c    BatchClass
		want string
	}{
		{BatchUnspecified, "unspecified"},
		{BatchNone, "none"},
		{BatchItemwise, "itemwise"},
		{BatchAggregate, "aggregate"},
		{BatchClass(99), "unspecified"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.c.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}
