package ops

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// Direct tests of the helpers the LX-020, LX-022 and LX-061 changes rest on.
// Each was first covered only through the operation that calls it.

func TestFoldRune(t *testing.T) {
	cases := []struct {
		name string
		a, b rune
		same bool
	}{
		{"a letter and its capital", 'a', 'A', true},
		{"two capitals", 'A', 'A', true},
		{"two different letters", 'a', 'b', false},
		{"a digit", '7', '7', true},
		{"two digits", '7', '8', false},
		{"punctuation", '-', '-', true},
		{"accented", 'é', 'É', true},
		{"an accent is not its base letter", 'é', 'e', false},
		{"greek sigma, final form", 'σ', 'ς', true},
		{"greek capital sigma", 'Σ', 'ς', true},
		{"the kelvin sign and k", 0x212a, 'k', true},
		{"the long s and S", 0x017f, 'S', true},
		{"cyrillic", 'и', 'И', true},
		{"a dotted capital I is not i", 0x0130, 'i', false},
		{"a sharp s is not s", 'ß', 's', false},
		{"cjk has no case", '東', '東', true},
		{"a fullwidth digit is not an ASCII one", '３', '3', false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := foldRune(tc.a) == foldRune(tc.b); got != tc.same {
				t.Errorf("foldRune(%q) == foldRune(%q) is %v, want %v", tc.a, tc.b, got, tc.same)
			}
		})
	}
	// Folding twice changes nothing.
	for r := rune(0); r < 0x3000; r++ {
		if once := foldRune(r); foldRune(once) != once {
			t.Fatalf("foldRune is not stable at %U", r)
		}
	}
}

func TestFoldText(t *testing.T) {
	cases := []struct {
		name, text string
		runes      string
		starts     []int32
	}{
		{"empty", "", "", []int32{0}},
		{"ascii", "ab", "AB", []int32{0, 1, 2}},
		{"capitals stay", "AB", "AB", []int32{0, 1, 2}},
		{"one space", "a b", "A B", []int32{0, 1, 2, 3}},
		{"a run of spaces is one", "a   b", "A B", []int32{0, 1, 4, 5}},
		{"mixed whitespace is one", "a \t\n b", "A B", []int32{0, 1, 5, 6}},
		{"a no-break space", "a\u00a0b", "A B", []int32{0, 1, 3, 4}},
		{"leading whitespace", "  a", " A", []int32{0, 2, 3}},
		{"trailing whitespace", "a  ", "A ", []int32{0, 1, 3}},
		{"a two-byte letter", "é!", "É!", []int32{0, 2, 3}},
		{"a three-byte letter", "東a", "東A", []int32{0, 3, 4}},
		{"an invalid byte is one character", "a\xffb", "A\ufffdB", []int32{0, 1, 2, 3}},
		{"only whitespace", " \n ", " ", []int32{0, 3}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := foldText(tc.text)
			if string(got.runes) != tc.runes {
				t.Errorf("runes = %q, want %q", string(got.runes), tc.runes)
			}
			if !reflect.DeepEqual(got.starts, tc.starts) {
				t.Errorf("starts = %v, want %v", got.starts, tc.starts)
			}
			if len(got.starts) != len(got.runes)+1 {
				t.Errorf("%d starts for %d characters, want one more", len(got.starts), len(got.runes))
			}
		})
	}
}

func TestFoldValue(t *testing.T) {
	cases := []struct{ name, value, want string }{
		{"empty", "", ""},
		{"only spaces", "   ", ""},
		{"a word", "Dana", "DANA"},
		{"two words", "Dana Kim", "DANA KIM"},
		{"spaces at the ends", "  Dana Kim  ", "DANA KIM"},
		{"a run inside", "Dana \t\n Kim", "DANA KIM"},
		{"a no-break space inside", "Dana\u00a0Kim", "DANA KIM"},
		{"a no-break space at the end", "Dana\u00a0", "DANA"},
		{"punctuation is kept", "a.b-c", "A.B-C"},
		{"accents are kept", "José", "JOSÉ"},
		{"digits are kept", "078-05", "078-05"},
		{"a zero-width space is not whitespace", "a\u200bb", "A\u200bB"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(foldValue(tc.value)); got != tc.want {
				t.Errorf("foldValue(%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

func TestFoldedTextFind(t *testing.T) {
	cases := []struct {
		name, text, value string
		want              [][2]int
	}{
		{"not there", "abc", "x", nil},
		{"an empty value", "abc", "", nil},
		{"a value longer than the text", "ab", "abc", nil},
		{"the whole text", "abc", "abc", [][2]int{{0, 3}}},
		{"at the start", "abcd", "ab", [][2]int{{0, 2}}},
		{"at the end", "abcd", "cd", [][2]int{{2, 4}}},
		{"twice", "abxab", "ab", [][2]int{{0, 2}, {3, 5}}},
		{"overlapping", "aaaa", "aa", [][2]int{{0, 2}, {1, 3}, {2, 4}}},
		{"overlapping with a period of two", "ababab", "abab", [][2]int{{0, 4}, {2, 6}}},
		{"a partial match and then a full one", "aab", "ab", [][2]int{{1, 3}}},
		{"a longer false start", "abcabcabd", "abcabd", [][2]int{{3, 9}}},
		{"across a run of whitespace", "a  \n b", "a b", [][2]int{{0, 6}}},
		{"ends before trailing whitespace", "ab   ", "ab", [][2]int{{0, 2}}},
		{"after a multi-byte letter", "éab", "ab", [][2]int{{2, 4}}},
		{"a multi-byte value", "xéy", "é", [][2]int{{1, 3}}},
		{"next to an invalid byte", "\xffab\xff", "ab", [][2]int{{1, 3}}},
		{"ignoring case", "xABx", "ab", [][2]int{{1, 3}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := foldText(tc.text).find(foldValue(tc.value))
			if len(got) != len(tc.want) || (len(got) > 0 && !reflect.DeepEqual(got, tc.want)) {
				t.Fatalf("find = %v, want %v", got, tc.want)
			}
			for _, at := range got {
				if at[0] < 0 || at[1] > len(tc.text) || at[0] >= at[1] {
					t.Fatalf("range %v is not inside a text of %d bytes", at, len(tc.text))
				}
			}
		})
	}
}

func TestShortRedactValue(t *testing.T) {
	cases := []struct {
		value string
		want  int
	}{
		{"a", shortWord}, {"Al", shortWord}, {"Kim", shortWord}, {"Kimi", notShort}, {"Müller", notShort},
		{"7", shortDigits}, {"34", shortDigits}, {"0420", shortDigits}, {"41111", notShort},
		{"B2", shortWord}, {"a1b", shortWord}, {"a1b2", notShort},
		{"田中", shortWord}, {"３４", shortDigits}, {"#1", shortWord}, {"...", shortWord},
	}
	for _, tc := range cases {
		if got := shortRedactValue(foldValue(tc.value)); got != tc.want {
			t.Errorf("shortRedactValue(%q) = %d, want %d", tc.value, got, tc.want)
		}
	}
}

func TestLeftInLongerWord(t *testing.T) {
	// The occurrence is the part of text between the two bars.
	cases := []struct {
		name, text string
		kind       int
		want       bool
	}{
		{"a word on its own", "|Al| rang", shortWord, false},
		{"at the start of a longer word", "|Al|so", shortWord, false},
		{"in the middle of a word", "c|al|led", shortWord, true},
		{"at the end of a word", "H|al|", shortWord, true},
		{"after punctuation", "(|Al|)", shortWord, false},
		{"after a digit", "9|Al|", shortWord, true},
		{"after an accent mark", "e\u0301|al|", shortWord, true},
		{"after a letter of an unspaced script", "東|Al|", shortWord, false},
		{"a value that starts with punctuation", "x|#1|", shortWord, false},
		{"digits on their own", "age |34|.", shortDigits, false},
		{"digits at the start of a number", "|34|00", shortDigits, true},
		{"digits at the end of a number", "13|34|", shortDigits, true},
		{"digits inside a number", "1|34|5", shortDigits, true},
		{"digits after a letter", "A|34| ", shortDigits, true},
		{"digits between punctuation", "-|34|-", shortDigits, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start := strings.Index(tc.text, "|")
			text := strings.Replace(tc.text, "|", "", 1)
			end := strings.Index(text, "|")
			text = strings.Replace(text, "|", "", 1)
			if got := leftInLongerWord(text, start, end, tc.kind); got != tc.want {
				t.Errorf("leftInLongerWord(%q, %d, %d) = %v, want %v", text, start, end, got, tc.want)
			}
		})
	}
}

func TestEmbeddedInWord(t *testing.T) {
	cases := []struct {
		name, text string
		want       bool
	}{
		{"on its own", "a |34| b", false},
		{"the whole text", "|34|", false},
		{"a letter before", "x|34| ", true},
		{"a letter after", " |34|x", true},
		{"a digit before", "1|34| ", true},
		{"a digit after", " |34|5", true},
		{"punctuation both sides", "(|34|)", false},
		{"a mark after", "|cafe|\u0301", true},
		{"cjk after", "|34|円", false},
		{"hangul before", "김|34|", false},
		{"a value ending in punctuation", "|a.|b", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start := strings.Index(tc.text, "|")
			text := strings.Replace(tc.text, "|", "", 1)
			end := strings.Index(text, "|")
			text = strings.Replace(text, "|", "", 1)
			if got := embeddedInWord(text, start, end); got != tc.want {
				t.Errorf("embeddedInWord(%q, %d, %d) = %v, want %v", text, start, end, got, tc.want)
			}
		})
	}
}

func TestCategoryAsAsked(t *testing.T) {
	cases := []struct {
		name, label string
		requested   []string
		want        string
	}{
		{"as requested", "email", []string{"email"}, "email"},
		{"the caller's capitals win", "email", []string{"Email"}, "Email"},
		{"the model's capitals lose", "EMAIL", []string{"email"}, "email"},
		{"padding on the label", "  email ", []string{"email"}, "email"},
		{"padding on the request", "email", []string{" email "}, "email"},
		{"a custom name", "employee id", []string{"Employee ID"}, "Employee ID"},
		{"a built-in name under all", "Phone", []string{"all"}, "phone"},
		{"a built-in name that was not requested", "SSN", []string{"email"}, "ssn"},
		{"the model's own label is kept", "Passport", []string{"all"}, "Passport"},
		{"an empty label", "", []string{"all"}, ""},
		{"a requested name wins over a built-in", "name", []string{"Name"}, "Name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := categoryAsAsked(tc.label, tc.requested); got != tc.want {
				t.Errorf("categoryAsAsked(%q, %v) = %q, want %q", tc.label, tc.requested, got, tc.want)
			}
		})
	}
}

func TestWaitForRetry(t *testing.T) {
	background := context.Background()
	cancelled, cancel := context.WithCancel(background)
	cancel()
	expired, release := context.WithDeadline(background, time.Now().Add(-time.Second))
	defer release()
	soon, stop := context.WithTimeout(background, 5*time.Millisecond)
	defer stop()

	cases := []struct {
		name  string
		ctx   context.Context
		delay time.Duration
		kind  types.ErrorKind
		is    error
	}{
		{"the delay passes", background, time.Millisecond, types.KindUnknown, nil},
		{"no delay", background, 0, types.KindUnknown, nil},
		{"a negative delay", background, -time.Second, types.KindUnknown, nil},
		{"already cancelled, long delay", cancelled, time.Hour, types.KindCanceled, context.Canceled},
		{"already expired, long delay", expired, time.Hour, types.KindTimeout, context.DeadlineExceeded},
		{"the deadline comes first", soon, time.Hour, types.KindTimeout, context.DeadlineExceeded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			started := time.Now()
			err := waitForRetry(tc.ctx, tc.delay)
			if time.Since(started) > 5*time.Second {
				t.Fatal("the wait did not end")
			}
			if tc.kind == types.KindUnknown {
				if err != nil {
					t.Fatalf("err = %v, want none", err)
				}
				return
			}
			if types.KindOf(err) != tc.kind || !errors.Is(err, tc.is) {
				t.Fatalf("err = %v (kind %v), want kind %v wrapping %v", err, types.KindOf(err), tc.kind, tc.is)
			}
		})
	}

	// A cancellation during the wait ends it, classified.
	during, end := context.WithCancel(background)
	go func() {
		time.Sleep(5 * time.Millisecond)
		end()
	}()
	if err := waitForRetry(during, time.Hour); types.KindOf(err) != types.KindCanceled {
		t.Errorf("cancelled during the wait: err = %v", err)
	}
}

func TestSimilarAspectAnswers(t *testing.T) {
	named := []string{"meaning", "tone"}
	cases := []struct {
		name     string
		raw      string
		want     []string // "name=score"
		wantKind types.ErrorKind
	}{
		{"absent", ``, nil, types.KindUnknown},
		{"null", `null`, nil, types.KindUnknown},
		{"an empty object", `{}`, nil, types.KindUnknown},
		{"an empty list", `[]`, nil, types.KindUnknown},
		{"keyed by position", `{"a1":{"score":0.9},"a2":{"score":0.4}}`, []string{"meaning=0.9", "tone=0.4"}, types.KindUnknown},
		{"position keys out of order", `{"a2":{"score":0.4},"a1":{"score":0.9}}`, []string{"meaning=0.9", "tone=0.4"}, types.KindUnknown},
		{"keyed by name", `{"tone":{"score":0.4}}`, []string{"tone=0.4"}, types.KindUnknown},
		{"a key past the named aspects is kept as a name", `{"a3":{"score":0.4}}`, []string{"a3=0.4"}, types.KindUnknown},
		{"a list", `[{"aspect":"tone","score":0.4},{"aspect":"meaning","score":0.9}]`, []string{"tone=0.4", "meaning=0.9"}, types.KindUnknown},
		{"an entry with no score is passed on for the check", `{"a1":{"reason":"x"}}`, []string{"meaning=nil"}, types.KindUnknown},
		{"a string", `"tone is the same"`, nil, types.KindSchemaViolation},
		{"a number", `7`, nil, types.KindSchemaViolation},
		{"an object of numbers", `{"a1":0.9}`, nil, types.KindSchemaViolation},
		{"a list of strings", `["tone"]`, nil, types.KindSchemaViolation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := similarAspectAnswers(json.RawMessage(tc.raw), named)
			if types.KindOf(err) != tc.wantKind {
				t.Fatalf("err = %v (kind %v), want kind %v", err, types.KindOf(err), tc.wantKind)
			}
			var shown []string
			for _, answer := range got {
				score := "nil"
				if answer.Score != nil {
					score = strconv.FormatFloat(*answer.Score, 'f', -1, 64)
				}
				shown = append(shown, answer.Aspect+"="+score)
			}
			if !reflect.DeepEqual(shown, tc.want) {
				t.Fatalf("answers = %v, want %v", shown, tc.want)
			}
		})
	}
}

func TestCheckedAspects(t *testing.T) {
	score := func(f float64) *float64 { return &f }
	cases := []struct {
		name     string
		answers  []aspectAnswer
		want     int
		wantKind types.ErrorKind
	}{
		{"none", nil, 0, types.KindUnknown},
		{"one", []aspectAnswer{{Aspect: "tone", Score: score(0.5)}}, 1, types.KindUnknown},
		{"zero is a score", []aspectAnswer{{Aspect: "tone", Score: score(0)}}, 1, types.KindUnknown},
		{"one is a score", []aspectAnswer{{Aspect: "tone", Score: score(1)}}, 1, types.KindUnknown},
		{"two", []aspectAnswer{{Aspect: "a", Score: score(0.1)}, {Aspect: "b", Score: score(0.9), Reason: "r"}}, 2, types.KindUnknown},
		{"no name", []aspectAnswer{{Score: score(0.5)}}, 0, types.KindSchemaViolation},
		{"a blank name", []aspectAnswer{{Aspect: "  ", Score: score(0.5)}}, 0, types.KindSchemaViolation},
		{"no score", []aspectAnswer{{Aspect: "tone"}}, 0, types.KindSchemaViolation},
		{"above the scale", []aspectAnswer{{Aspect: "tone", Score: score(1.01)}}, 0, types.KindInvariantViolation},
		{"below the scale", []aspectAnswer{{Aspect: "tone", Score: score(-0.01)}}, 0, types.KindInvariantViolation},
		{"a bad one after a good one", []aspectAnswer{{Aspect: "a", Score: score(0.5)}, {Aspect: "b"}}, 0, types.KindSchemaViolation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := checkedAspects(tc.answers)
			if types.KindOf(err) != tc.wantKind || len(got) != tc.want {
				t.Fatalf("%d aspects, err = %v; want %d and kind %v", len(got), err, tc.want, tc.wantKind)
			}
		})
	}
}

func TestAspectsAsked(t *testing.T) {
	cases := []struct {
		name     string
		reported []AspectMatch
		named    []string
		want     []string
		wantErr  bool
	}{
		{"none named: everything is returned", []AspectMatch{{Aspect: "x", Score: 0.5}}, nil, []string{"x"}, false},
		{"none named, none reported", nil, nil, nil, false},
		{"in the caller's order", []AspectMatch{{Aspect: "tone", Score: 0.4}, {Aspect: "meaning", Score: 0.9}}, []string{"meaning", "tone"}, []string{"meaning", "tone"}, false},
		{"in the caller's spelling", []AspectMatch{{Aspect: " user intent ", Score: 0.4}}, []string{"User Intent"}, []string{"User Intent"}, false},
		{"one that was not asked for is left out", []AspectMatch{{Aspect: "tone", Score: 0.4}, {Aspect: "length", Score: 0.9}}, []string{"tone"}, []string{"tone"}, false},
		{"twice with one score", []AspectMatch{{Aspect: "tone", Score: 0.4}, {Aspect: "Tone", Score: 0.4}}, []string{"tone"}, []string{"tone"}, false},
		{"twice with two scores", []AspectMatch{{Aspect: "tone", Score: 0.4}, {Aspect: "tone", Score: 0.5}}, []string{"tone"}, nil, true},
		{"one is missing", []AspectMatch{{Aspect: "tone", Score: 0.4}}, []string{"tone", "meaning"}, nil, true},
		{"all are missing", nil, []string{"tone"}, nil, true},
		{"a different one was reported", []AspectMatch{{Aspect: "length", Score: 0.4}}, []string{"tone"}, nil, true},
		{"a rephrased name does not count", []AspectMatch{{Aspect: "intent of the user", Score: 0.4}}, []string{"user intent"}, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := aspectsAsked(tc.reported, tc.named)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want an error: %v", err, tc.wantErr)
			}
			if tc.wantErr && types.KindOf(err) != types.KindInvariantViolation {
				t.Errorf("kind = %v, want an invariant violation", types.KindOf(err))
			}
			if names := aspectNames(got); !reflect.DeepEqual(names, tc.want) {
				t.Fatalf("aspects = %v, want %v", names, tc.want)
			}
		})
	}
}

// A retry is never sent after the context ends. One run of this passes about
// half the time without the check before each retry, because the wait can
// return on its timer when the timer and the context are both ready; a hundred
// runs do not.
func TestNoRetryIsSentAfterTheContextEndsEveryTime(t *testing.T) {
	for run := 0; run < 100; run++ {
		ctx, cancel := context.WithCancel(context.Background())
		provider := &cancelOnFirstFailure{cancel: cancel}

		_, err := CallLLM(ctx, provider, "system", "user", types.OpOptions{})
		cancel()

		if provider.attempts != 1 {
			t.Fatalf("run %d: the provider was called %d times, want 1", run, provider.attempts)
		}
		if types.KindOf(err) != types.KindCanceled {
			t.Fatalf("run %d: err = %v, want a classified cancellation", run, err)
		}
	}
}
