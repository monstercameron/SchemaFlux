package ops

import (
	"strings"
	"testing"
)

// OP-202. Classify's membership check was the only one of its kind in the
// library; it is a shared invariant now, and returning the canonical spelling
// is what makes it reusable.
func TestCategoryIn(t *testing.T) {
	allowed := []string{"billing", "technical", "account"}

	cases := []struct {
		name    string
		answer  string
		want    string
		wantErr bool
	}{
		{"exact", "billing", "billing", false},
		{"different case", "BILLING", "billing", false},
		{"mixed case", "Technical", "technical", false},
		{"a category not offered", "sales", "", true},
		{"nearly right", "billings", "", true},
		{"empty answer", "", "", true},
		{"a sentence instead of a category", "I think this is billing", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CategoryIn(allowed, tc.answer)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("CategoryIn(%q) = %q, want an error", tc.answer, got)
				}
				// The answer is the model's, and naming it is what makes the
				// error actionable: "billings" reads very differently from
				// something unrelated.
				if tc.answer != "" && !strings.Contains(err.Error(), tc.answer) {
					t.Errorf("the error does not name what the model said: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("CategoryIn(%q) = %v", tc.answer, err)
			}
			if got != tc.want {
				t.Errorf("CategoryIn(%q) = %q, want the canonical %q", tc.answer, got, tc.want)
			}
		})
	}

	if _, err := CategoryIn(nil, "anything"); err == nil {
		t.Error("CategoryIn accepted an answer with no categories offered")
	}
}

// OP-201. The floor was interpolated into the prompt and never read back.
func TestAtLeastConfidence(t *testing.T) {
	cases := []struct {
		name     string
		reported float64
		minimum  float64
		wantErr  bool
	}{
		{"above the floor", 0.9, 0.7, false},
		{"exactly at the floor", 0.7, 0.7, false},
		{"below the floor", 0.2, 0.7, true},
		{"just below", 0.699, 0.7, true},
		{"no floor configured", 0.1, 0, false},
		{"a negative floor is no floor", 0.1, -1, false},
		{"zero confidence against a floor", 0, 0.5, true},
		{"zero confidence with no floor", 0, 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := AtLeastConfidence(tc.reported, tc.minimum)
			if tc.wantErr && err == nil {
				t.Error("AtLeastConfidence accepted a result below the floor")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("AtLeastConfidence = %v, want nil", err)
			}
			if err != nil && !strings.Contains(err.Error(), "floor") {
				t.Errorf("the error should say what was violated: %v", err)
			}
		})
	}
}
