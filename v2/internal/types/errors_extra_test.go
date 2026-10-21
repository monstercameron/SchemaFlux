package types

import (
	"errors"
	"strings"
	"testing"
)

// The remaining operation-specific error types payloadprivacy_test.go did
// not enumerate: same rule (describe, never reproduce), same Unwrap
// contract. Table-driven because every case is the identical shape.
func TestRemainingErrorTypesDoNotPrintThePayloadAndUnwrap(t *testing.T) {
	cause := errors.New("underlying failure")

	cases := []struct {
		name string
		err  interface {
			error
			Unwrap() error
		}
		wantSubstr string
	}{
		{"score", ScoreError{InputShape: DescribeValue(payloadMarker), Reason: "no basis to score", Err: cause}, "scoring failed"},
		{"compare", CompareError{AShape: DescribeValue(payloadMarker), BShape: "string, 5 bytes", Reason: "incomparable types", Err: cause}, "comparison failed"},
		{"choose", ChooseError{OptionCount: 4, Reason: "no option matched", Err: cause}, "selection failed"},
		{"filter", FilterError{ItemCount: 10, Reason: "predicate errored", Err: cause}, "filtering failed"},
		{"sort", SortError{ItemCount: 5, Reason: "not a permutation", Err: cause}, "sorting failed"},
		{"rewrite", RewriteError{InputShape: DescribeValue(payloadMarker), Reason: "refused", Err: cause}, "rewrite failed"},
		{"translate", TranslateError{InputShape: DescribeValue(payloadMarker), Reason: "unsupported language", Err: cause}, "translation failed"},
		{"expand", ExpandError{InputShape: DescribeValue(payloadMarker), Reason: "no expansion", Err: cause}, "expansion failed"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			message := tc.err.Error()
			if !strings.Contains(message, tc.wantSubstr) {
				t.Errorf("Error() = %q, want it to contain %q", message, tc.wantSubstr)
			}
			if strings.Contains(message, payloadMarker) {
				t.Errorf("the caller's value reached the error string: %s", message)
			}
			if !errors.Is(tc.err, cause) {
				t.Errorf("%T does not unwrap to its cause", tc.err)
			}
		})
	}
}

// describeText classifies by shape alone; every branch, exercised directly
// rather than only through the cases DescribeValue's own test happened to
// pick.
func TestDescribeTextClassifiesByShapeAlone(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"empty", "", "empty"},
		{"whitespace only", "   \n\t  ", "empty"},
		{"fenced block", "```go\nfmt.Println(1)\n```", "fenced block"},
		{"json object", `{"a": 1}`, "json object"},
		{"json array", `[1, 2, 3]`, "json array"},
		{"markup", "<html><body/></html>", "markup"},
		{"plain text", "just some ordinary prose", "text"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := describeText(tc.text); got != tc.want {
				t.Errorf("describeText(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

// DescribeValue's non-string, non-collection branch: a plain scalar or
// struct value reports its Go type only, never a rendering of its content.
func TestDescribeValueReportsTypeForScalarsAndStructs(t *testing.T) {
	type invoice struct{ Total float64 }

	if got := DescribeValue(3.14); got != "float64" {
		t.Errorf("DescribeValue(3.14) = %q, want \"float64\"", got)
	}
	if got := DescribeValue(invoice{Total: 42}); got != "types.invoice" {
		t.Errorf("DescribeValue(struct) = %q, want the type name", got)
	}
	if got := DescribeValue([]int{1, 2, 3}); got != "[]int of 3" {
		t.Errorf("DescribeValue(slice) = %q, want \"[]int of 3\"", got)
	}
	if got := DescribeValue(map[string]int{"a": 1, "b": 2}); got != "map[string]int of 2" {
		t.Errorf("DescribeValue(map) = %q, want \"map[string]int of 2\"", got)
	}
}

// DescribeValue's []byte branch is a separate case from string, and must
// follow the identical "describe, never reproduce" rule.
func TestDescribeValueHandlesByteSlicesLikeStrings(t *testing.T) {
	described := DescribeValue([]byte(payloadMarker))
	if strings.Contains(described, payloadMarker) {
		t.Errorf("DescribeValue([]byte) reproduced the value: %q", described)
	}
	if !strings.Contains(described, "bytes") {
		t.Errorf("DescribeValue([]byte) = %q, want it to report a byte count", described)
	}
}

// digestOf degrades to a fixed marker rather than panicking when its input
// cannot be marshalled to JSON -- a channel is the simplest such value.
func TestDigestOfDegradesGracefullyOnUnmarshalableInput(t *testing.T) {
	ch := make(chan int)
	if got := digestOf(ch); got != "undigestable" {
		t.Errorf("digestOf(chan) = %q, want \"undigestable\"", got)
	}
}
