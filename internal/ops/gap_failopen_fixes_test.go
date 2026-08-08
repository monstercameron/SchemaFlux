package ops

import (
	"strings"
	"testing"
)

// Three operations that reported success while producing something wrong.
//
// All three are the same class, and it is the one this library exists to
// refuse: an option or a contract stated in the prompt that Go never verified.
// The prompt asks the model to return a conformed value, to keep the sequence
// the same length, to leave non-gap items alone — and the model is free to
// ignore any of it. A request is not a guarantee, and the Go side is where the
// difference gets enforced.

type fillItem struct {
	Value int `json:"value"`
}

// Conform's answer IS the conformed value. A response without one used to
// return a nil error and T's zero value, because the unmarshal was guarded by
// `if len(parsed.Conformed) > 0` and any other recognised field satisfied the
// decoder. A caller writing `if err == nil { use(result.Conformed) }` got an
// empty struct and no signal.
func TestConformRefusesAResponseCarryingNoConformedValue(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"conformed absent entirely", `{"adjustments":[],"violations":[],"compliance":0.9}`},
		{"conformed explicitly null", `{"conformed":null,"compliance":0.9}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withResponse(t, tc.body)

			result, err := Conform(conformAddress{Street: "1 main st"}, "USPS")
			if err == nil {
				t.Fatalf("Conform returned success with no conformed value; result = %+v", result.Conformed)
			}
			if !strings.Contains(err.Error(), "conformed") {
				t.Errorf("the error does not say what was missing: %v", err)
			}
		})
	}
}

// Interpolate fills gaps in a sequence. A sequence that changed length is not
// that sequence, and a caller indexing the result against their own input would
// read the wrong element or run off the end.
func TestInterpolateRefusesAnAnswerOfADifferentLength(t *testing.T) {
	items := []fillItem{{Value: 10}, {}, {Value: 30}}

	for _, tc := range []struct {
		name string
		body string
	}{
		{"too short", `{"complete":[{"value":10}],"filled":[],"gap_count":1}`},
		{"too long", `{"complete":[{"value":10},{"value":20},{"value":30},{"value":40}],"filled":[],"gap_count":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withResponse(t, tc.body)

			result, err := Interpolate(items, NewInterpolateOptions())
			if err == nil {
				t.Fatalf("Interpolate accepted a %d-item answer for a %d-item input: %+v",
					len(result.Complete), len(items), result.Complete)
			}
			if !strings.Contains(err.Error(), "length") {
				t.Errorf("the error does not say the length changed: %v", err)
			}
		})
	}
}

// The more dangerous half: a model that rewrites a value it was only asked to
// preserve produces a result that looks entirely well-formed. Index 0 is not a
// declared gap here, and coming back changed is a contract violation.
func TestInterpolateRefusesAChangeToAnItemThatWasNotAGap(t *testing.T) {
	items := []fillItem{{Value: 10}, {}, {Value: 30}}

	opts := NewInterpolateOptions()
	opts.GapIndices = []int{1}

	withResponse(t, `{"complete":[{"value":999},{"value":20},{"value":30}],"filled":[{"index":1}],"gap_count":1}`)

	result, err := Interpolate(items, opts)
	if err == nil {
		t.Fatalf("Interpolate accepted a change to index 0, which was never declared a gap: %+v", result.Complete)
	}
	if !strings.Contains(err.Error(), "not declared a gap") {
		t.Errorf("the error does not name the violated contract: %v", err)
	}
}

// And the honest limit of that check: with gaps INFERRED rather than declared,
// there is no reference for "unchanged" — the operation is deciding which items
// were gaps in the first place — so the invariant does not apply and a valid
// answer must still be accepted.
func TestInterpolateStillAcceptsAValidAnswerWhenGapsWereNotDeclared(t *testing.T) {
	items := []fillItem{{Value: 10}, {}, {Value: 30}}

	withResponse(t, `{"complete":[{"value":10},{"value":20},{"value":30}],"filled":[{"index":1}],"gap_count":1}`)

	result, err := Interpolate(items, NewInterpolateOptions())
	if err != nil {
		t.Fatalf("Interpolate refused a well-formed answer: %v", err)
	}
	if len(result.Complete) != 3 || result.Complete[1].Value != 20 {
		t.Errorf("result = %+v, want the gap at index 1 filled with 20", result.Complete)
	}
}
