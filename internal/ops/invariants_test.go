package ops

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
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

// OP-402. MaxLength and TargetLength were requested in a prompt and never
// checked, so an operation documented as producing at most N of something
// produced whatever the model produced.
func TestWithinLength(t *testing.T) {
	cases := []struct {
		name      string
		text      string
		limit     int
		unit      LengthUnit
		tolerance float64
		wantErr   bool
	}{
		{"under the limit", "one two three", 5, LengthInWords, 0, false},
		{"exactly at the limit", "one two three", 3, LengthInWords, 0, false},
		{"over the limit", "one two three four five six", 3, LengthInWords, 0, true},
		{"over but inside the tolerance", "one two three four", 3, LengthInWords, 0.5, false},
		{"over the tolerance", "one two three four five", 3, LengthInWords, 0.2, true},
		{"no limit configured", "anything at all, really", 0, LengthInWords, 0, false},
		{"characters count runes", "Montréal", 8, LengthInCharacters, 0, false},
		{"characters would fail as bytes", "東京都", 3, LengthInCharacters, 0, false},
		{"three sentences requested, three given", "One. Two. Three.", 3, LengthInSentences, 0, false},
		{"three sentences requested, six given", "A. B. C. D. E. F.", 3, LengthInSentences, 0, true},
		{"paragraphs", "first\n\nsecond", 2, LengthInParagraphs, 0, false},
		{"too many paragraphs", "a\n\nb\n\nc\n\nd", 2, LengthInParagraphs, 0, true},
		{"empty text", "", 3, LengthInSentences, 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := WithinLength(tc.text, tc.limit, tc.unit, tc.tolerance)
			if tc.wantErr && err == nil {
				t.Errorf("WithinLength accepted %d %s against a limit of %d",
					MeasureLength(tc.text, tc.unit), tc.unit, tc.limit)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("WithinLength = %v, want nil", err)
			}
		})
	}
}

// The measurement itself, per unit. Counting is where a length check goes
// wrong quietly.
func TestMeasureLength(t *testing.T) {
	cases := []struct {
		text string
		unit LengthUnit
		want int
	}{
		{"one two three", LengthInWords, 3},
		{"  spaced   out  ", LengthInWords, 2},
		{"", LengthInWords, 0},
		{"One. Two! Three?", LengthInSentences, 3},
		{"No terminator", LengthInSentences, 1},
		{"Ends with two dots..", LengthInSentences, 1},
		{"", LengthInSentences, 0},
		{"a\n\nb\n\nc", LengthInParagraphs, 3},
		{"single", LengthInParagraphs, 1},
		{"Montréal", LengthInCharacters, 8},
		{"東京都", LengthInCharacters, 3},
		{"", LengthInCharacters, 0},
	}

	for _, tc := range cases {
		if got := MeasureLength(tc.text, tc.unit); got != tc.want {
			t.Errorf("MeasureLength(%q, %s) = %d, want %d", tc.text, tc.unit, got, tc.want)
		}
	}
}

// Sentence counting. Each row says what a reader would count; the rows marked
// "knowingly wrong" pin what countSentences gets wrong, so that a change that
// fixes or worsens one is a decision and not an accident.
func TestCountSentences(t *testing.T) {
	cases := []struct {
		name string
		text string
		want int
	}{
		{"plain sentences", "One. Two! Three?", 3},
		{"no terminator", "No terminator", 1},
		{"empty", "", 0},
		{"only space", "  \n\t ", 0},
		{"a quotation that ends with a full stop", `"Yes."`, 1},
		{"a quotation inside a sentence", `He said "no." Then left.`, 2},
		{"a bracketed sentence", "(see above.)", 1},
		{"a bracket after the mark, then more text", "We tried (twice.) It failed.", 2},
		{"a decimal number", "The ratio is 3.14 in total.", 1},
		{"a price", "It costs $3.50. Fine.", 2},
		{"a version number", "Version 2.0 is out.", 1},
		{"a domain name", "See example.com for more. Thanks.", 2},
		{"a title before a name", "Dr. Smith went home.", 1},
		{"two titles", "Mr. and Mrs. Jones met Prof. Lee.", 1},
		{"a title, then a real end", "I met Mr. Jones. He left.", 2},
		{"an abbreviation followed by lower case", "Bring tools, e.g. hammers, saws.", 1},
		{"i.e. in the middle", "It is small, i.e. tiny, and light.", 1},
		{"an ellipsis then lower case", "Wait... what?", 1},
		{"an ellipsis then a capital", "Wait... What?", 2},
		{"a unicode ellipsis then lower case", "Well… maybe so.", 1},
		{"an ellipsis at the end", "Ends with two dots..", 1},
		{"a dialogue tag after a quoted exclamation", `"Stop!" she said.`, 1},
		{"a dialogue tag after a quoted question", `"Why?" he asked. "Because."`, 2},
		{"mixed marks", "Really?! Yes.", 2},
		{"repeated marks", "Hi!!! Ok.", 2},
		{"a closing quote then a capital", `She said "go." Then she went.`, 2},
		{"windows line ends", "First.\r\nSecond.\r\n", 2},
		{"a blank line between", "First.\n\nSecond.", 2},
		{"no space after the mark", "one.Two", 1},
		{"stray punctuation only", "...", 0},
		{"quotes only", `""`, 0},
		{"a sentence that ends in a bracket and a quote", `He wrote ("never.")`, 1},
		{"knowingly wrong: a numbered list counts each marker", "1. Apples 2. Pears", 3},
		{"knowingly wrong: initials count as sentences", "J. K. Rowling wrote it.", 3},
		{"knowingly wrong: a lower-case sentence start after a full stop", "It ended. and then it began.", 1},
		{"knowingly wrong: a number after an abbreviation not in the list", "Room No. 5 is open.", 2},
	}
	for _, tc := range cases {
		if got := countSentences(tc.text); got != tc.want {
			t.Errorf("%s: countSentences(%q) = %d, want %d", tc.name, tc.text, got, tc.want)
		}
	}
}

func TestMeasureLengthCountsAQuotedSentenceOnce(t *testing.T) {
	if got := MeasureLength(`"Yes."`, LengthInSentences); got != 1 {
		t.Errorf("MeasureLength = %d, want 1", got)
	}
	if err := WithinLength(`"Yes." (see above.)`, 2, LengthInSentences, 0); err != nil {
		t.Errorf("a two-sentence text was refused against a limit of two: %v", err)
	}
}

func TestMeasureLengthCountsParagraphsAcrossCRLF(t *testing.T) {
	cases := []struct {
		text string
		want int
	}{
		{"a\r\n\r\nb\r\n\r\nc", 3},
		{"a\n\nb", 2},
		{"a\r\n\r\n\r\n\r\nb", 2},
		{"single line", 1},
		{"", 0},
		{"\r\n\r\n", 0},
	}
	for _, tc := range cases {
		if got := MeasureLength(tc.text, LengthInParagraphs); got != tc.want {
			t.Errorf("MeasureLength(%q, paragraphs) = %d, want %d", tc.text, got, tc.want)
		}
	}
}

func TestWithinLengthRefusesWhatItCannotMeasure(t *testing.T) {
	nan := math.NaN()
	cases := []struct {
		name      string
		limit     int
		unit      LengthUnit
		tolerance float64
		wantErr   bool
	}{
		{"an unknown unit", 3, LengthUnit("sentence"), 0, true},
		{"an unknown unit on text that is under the limit", 100, LengthUnit("bogus"), 0, true},
		{"another unknown unit", 3, LengthUnit("word"), 0, true},
		{"an unknown unit with no limit set", 0, LengthUnit("word"), 0, false},
		{"the empty unit means characters", 100, "", 0, false},
		{"a NaN tolerance", 3, LengthInWords, nan, true},
		{"an infinite tolerance", 3, LengthInWords, math.Inf(1), true},
		{"a negative infinite tolerance", 3, LengthInWords, math.Inf(-1), true},
		{"a negative tolerance is read as none", 3, LengthInWords, -0.5, false},
		{"all four units are known", 100, LengthInParagraphs, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := WithinLength("one two three", tc.limit, tc.unit, tc.tolerance)
			if tc.wantErr {
				wantKind(t, err, types.KindInvariantViolation)
			} else if err != nil {
				t.Errorf("refused: %v", err)
			}
		})
	}
}

// The refusal says which of three things was wrong: a floor that is not a
// number from 0 to 1, a report outside that range, or a report under the floor.
// A report of -0.1 or NaN is also under every floor, so the answer is refused
// either way, and it is the message that tells the caller the model's number was
// not a confidence at all.
func TestAtLeastConfidenceRefusesWhatIsNotAConfidence(t *testing.T) {
	const badFloor = "not a number from 0 to 1"
	const badReport = "outside the range 0 to 1"
	const belowFloor = "below the"
	cases := []struct {
		name              string
		reported, minimum float64
		wantErr           bool
		wantText          string
	}{
		{"above the floor", 0.9, 0.7, false, ""},
		{"exactly at the floor", 0.7, 0.7, false, ""},
		{"below the floor", 0.2, 0.7, true, belowFloor},
		{"no floor set", 0.2, 0, false, ""},
		{"no floor set and a nonsense report", 85, 0, false, ""},
		{"a percentage", 85, 0.7, true, badReport},
		{"seven out of ten", 7.0, 0.7, true, badReport},
		{"a negative report", -0.1, 0.5, true, badReport},
		{"a NaN report", math.NaN(), 0.5, true, badReport},
		{"an infinite report", math.Inf(1), 0.5, true, badReport},
		{"a negative infinite report", math.Inf(-1), 0.5, true, badReport},
		{"a floor nothing could meet", 1, 1.5, true, badFloor},
		{"a floor nothing could meet beside an ordinary report", 0.5, 1.5, true, badFloor},
		{"an infinite floor", 0.9, math.Inf(1), true, badFloor},
		{"a NaN floor", 0.9, math.NaN(), true, badFloor},
		{"a report of exactly 1", 1, 1, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := AtLeastConfidence(tc.reported, tc.minimum)
			if tc.wantErr {
				wantKind(t, err, types.KindInvariantViolation)
				if !strings.Contains(err.Error(), tc.wantText) {
					t.Errorf("the refusal reads %q, want it to say %q", err, tc.wantText)
				}
			} else if err != nil {
				t.Errorf("refused: %v", err)
			}
		})
	}
}

// A classifier that answers with the input itself as its category puts that
// text into an error every caller logs.
func TestCategoryInDoesNotQuoteTheAnswerOrTheLabels(t *testing.T) {
	labels := []string{"billing", "refund-secret-label", "shipping"}
	answer := "my card number is 4111 1111 1111 1111"
	got, err := CategoryIn(labels, answer)
	wantKind(t, err, types.KindInvariantViolation, "4111", "billing", "refund-secret-label", "shipping")
	if got != "" {
		t.Errorf("a category came back beside the error: %q", got)
	}
}

func TestCategoryInFoldsCaseAndReturnsTheCallersSpelling(t *testing.T) {
	cases := []struct {
		answer string
		want   string
		ok     bool
	}{
		{"urgent", "Urgent", true},
		{"URGENT", "Urgent", true},
		{"Urgent", "Urgent", true},
		{"low", "low", true},
		{"urgent!", "", false},
		{" urgent", "", false},
		{"", "", false},
		{"medium", "", false},
	}
	for _, tc := range cases {
		got, err := CategoryIn([]string{"Urgent", "low"}, tc.answer)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("CategoryIn(%q) = %q, %v; want %q, ok=%v", tc.answer, got, err, tc.want, tc.ok)
		}
	}
}

func TestEveryInvariantRefusalIsClassifiedAndCarriesNoValue(t *testing.T) {
	secret := "ssn-123-45-6789"
	items := []string{secret, "b"}
	other := []string{"c", "d"}
	refusals := map[string]error{
		"SameMultiset":      SameMultiset(items, other),
		"SameMultiset size": SameMultiset(items, []string{"x"}),
		"SubsetOf":          SubsetOf(items, []string{secret + "-other"}),
		"SubsetOf size":     SubsetOf([]string{"a"}, items),
		"CoversExactlyOnce": CoversExactlyOnce(2, [][]int{{0, 0}, {5}}),
		"MemberOf":          MemberOf(items, "zzz"),
		"AtMost":            AtMost(items, 1),
	}
	for name, err := range refusals {
		if err == nil {
			t.Errorf("%s: no refusal", name)
			continue
		}
		wantKind(t, err, types.KindInvariantViolation, secret)
		if !errors.Is(err, types.ErrInvariantViolation) {
			t.Errorf("%s: errors.Is(err, ErrInvariantViolation) is false", name)
		}
	}
}
