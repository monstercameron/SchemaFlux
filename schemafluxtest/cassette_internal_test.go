package schemafluxtest

import "testing"

// Cassette files replay by the value of their number, then by name where two
// names hold the same number, so that the order of a replay never depends on
// the order the directory happened to be read in.
func TestCassetteFileLessOrdersByNumberThenByName(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"a smaller number first", "1.json", "2.json", true},
		{"a larger number second", "2.json", "1.json", false},
		{"numbers by value, not by text", "9.json", "10.json", true},
		{"a larger number by value against its text", "10.json", "9.json", false},
		{"padding does not change the value", "002.json", "10.json", true},
		{"the same number, ordered by name", "01.json", "1.json", true},
		{"the same number, the other way round", "1.json", "01.json", false},
		{"the same number with more padding", "001.json", "01.json", true},
		{"a name is not less than itself", "1.json", "1.json", false},
		{"a number before a name", "1.json", "a.json", true},
		{"a name after a number", "a.json", "1.json", false},
		{"names that are not numbers go by string order", "a.json", "b.json", true},
		{"names that are not numbers, the other way round", "b.json", "a.json", false},
		{"a name that is not a cassette number", "1a.json", "2.json", false},
		{"no extension is not a number", "1", "2.json", false},
		{"a number too large for any integer", "1.json", "99999999999999999999999.json", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cassetteFileLess(tc.a, tc.b); got != tc.want {
				t.Errorf("cassetteFileLess(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
