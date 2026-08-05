package ops

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Every length and offset in this package was written as though a Go string
// were characters. It is bytes. A cut landing mid-rune produces invalid UTF-8,
// and MaxLength, ShowFirst, and ShowLast are all documented in characters.
//
// The test inputs are deliberately ordinary text rather than exotica: an
// accented place name, a currency symbol, a name in a non-Latin script. This is
// the text a real caller has.

const (
	accented    = "Montréal, Québec"         // 2-byte runes
	currency    = "Total: €1.284,50 or £900" // 3-byte runes
	japanese    = "東京都渋谷区"                   // 3-byte runes throughout
	emoji       = "shipped 📦 today"          // 4-byte rune
	mixedScript = "Café 東京 €5 📦"
)

func TestTruncateRunesNeverProducesInvalidUTF8(t *testing.T) {
	inputs := []string{accented, currency, japanese, emoji, mixedScript, "plain ascii text"}

	for _, input := range inputs {
		t.Run(input[:8], func(t *testing.T) {
			// Every cut point, including the ones that would land mid-rune if
			// the slice were byte-indexed.
			for limit := 0; limit <= utf8.RuneCountInString(input)+2; limit++ {
				got := truncateRunes(input, limit)

				if !utf8.ValidString(got) {
					t.Fatalf("limit %d produced invalid UTF-8: %q", limit, got)
				}
				if strings.ContainsRune(got, utf8.RuneError) && !strings.ContainsRune(input, utf8.RuneError) {
					t.Fatalf("limit %d introduced a replacement character: %q", limit, got)
				}
				if want := smaller(limit, utf8.RuneCountInString(input)); utf8.RuneCountInString(got) != want {
					t.Errorf("limit %d produced %d runes, want %d", limit, utf8.RuneCountInString(got), want)
				}
				if !strings.HasPrefix(input, got) {
					t.Errorf("limit %d produced %q, which is not a prefix of the input", limit, got)
				}
			}
		})
	}
}

func smaller(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// The boundary cases, stated explicitly.
func TestTruncateRunesEdges(t *testing.T) {
	cases := []struct {
		name  string
		input string
		limit int
		want  string
	}{
		{"zero", accented, 0, ""},
		{"negative", accented, -3, ""},
		{"one", accented, 1, "M"},
		{"stops_before_a_two_byte_rune", "Montréal", 5, "Montr"},
		{"includes_a_two_byte_rune", "Montréal", 6, "Montré"},
		{"whole_string", japanese, 6, japanese},
		{"beyond_the_end", japanese, 99, japanese},
		{"empty_input", "", 5, ""},
		{"emoji_boundary", "ab📦cd", 2, "ab"},
		{"includes_the_emoji", "ab📦cd", 3, "ab📦"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := truncateRunes(tc.input, tc.limit); got != tc.want {
				t.Errorf("truncateRunes(%q, %d) = %q, want %q", tc.input, tc.limit, got, tc.want)
			}
		})
	}
}

// runeLen is the count the documentation means.
func TestRuneLen(t *testing.T) {
	cases := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"abc", 3},
		{"Montréal", 8},
		{japanese, 6},
		{"ab📦cd", 5},
		{"€", 1},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			if got := runeLen(tc.input); got != tc.want {
				t.Errorf("runeLen(%q) = %d, want %d (byte length is %d)", tc.input, got, tc.want, len(tc.input))
			}
		})
	}
}

// runeSlice reports a bad range rather than panicking or clamping, because the
// offsets it is given come from a model.
func TestRuneSlice(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		start   int
		end     int
		want    string
		wantErr bool
	}{
		{"whole", "Montréal", 0, 8, "Montréal", false},
		{"across_a_multibyte_rune", "Montréal", 4, 7, "réa", false},
		{"single_multibyte_rune", "Montréal", 5, 6, "é", false},
		{"empty_range", "Montréal", 3, 3, "", false},
		{"japanese_middle", japanese, 1, 4, "京都渋", false},
		{"emoji", "ab📦cd", 2, 3, "📦", false},
		{"negative_start", "abc", -1, 2, "", true},
		{"negative_end", "abc", 0, -1, "", true},
		{"inverted", "abc", 2, 1, "", true},
		{"past_the_end", "Montréal", 0, 99, "", true},
		{"past_the_end_by_one", "Montréal", 0, 9, "", true},
		{"byte_length_would_have_passed", "Montréal", 0, 9, "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runeSlice(tc.text, tc.start, tc.end)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("runeSlice(%q, %d, %d) = %q, want an error", tc.text, tc.start, tc.end, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("runeSlice: %v", err)
			}
			if got != tc.want {
				t.Errorf("runeSlice(%q, %d, %d) = %q, want %q", tc.text, tc.start, tc.end, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("produced invalid UTF-8: %q", got)
			}
		})
	}
}

// truncateAtWord keeps the prefix and cuts on a space where it can.
func TestTruncateAtWord(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		limit int
		keep  int
		want  string
	}{
		{"fits", "one two", 20, 3, "one two"},
		{"cuts_at_space", "one two three", 9, 3, "one two"},
		{"no_space_to_cut_at", "onetwothree", 5, 2, "onetw"},
		{"space_before_keep_is_not_used", "ab cdefghij", 8, 5, "ab cdefg"},
		{"multibyte", "Montréal Québec", 12, 4, "Montréal"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateAtWord(tc.text, tc.limit, tc.keep)
			if got != tc.want {
				t.Errorf("truncateAtWord(%q, %d, %d) = %q, want %q", tc.text, tc.limit, tc.keep, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("produced invalid UTF-8: %q", got)
			}
		})
	}
}

// maskText reveals characters, not bytes: showing "the first four characters"
// of a name beginning with a multi-byte rune used to cut it in half.
func TestMaskTextRevealsCharactersNotBytes(t *testing.T) {
	opts := RedactLLMOptions{MaskChar: '*', ShowFirst: 3, ShowLast: 2, MinMask: 1}

	cases := []struct {
		name string
		text string
	}{
		{"accented", "Montréal"},
		{"japanese", japanese},
		{"emoji", "ab📦cdef"},
		{"currency", "€1.284,50"},
		{"ascii", "1234567890"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			masked := maskText(tc.text, opts)

			if !utf8.ValidString(masked) {
				t.Fatalf("maskText produced invalid UTF-8: %q", masked)
			}
			if strings.ContainsRune(masked, utf8.RuneError) {
				t.Fatalf("maskText produced a replacement character: %q", masked)
			}

			runes := []rune(tc.text)
			if len(runes) > opts.ShowFirst+opts.ShowLast {
				if !strings.HasPrefix(masked, string(runes[:opts.ShowFirst])) {
					t.Errorf("masked %q does not begin with the first %d characters of %q",
						masked, opts.ShowFirst, tc.text)
				}
				if !strings.HasSuffix(masked, string(runes[len(runes)-opts.ShowLast:])) {
					t.Errorf("masked %q does not end with the last %d characters of %q",
						masked, opts.ShowLast, tc.text)
				}
			}
		})
	}
}

// applyRedactions splices on character offsets and refuses a span the text
// cannot support, because applying a bad offset redacts the wrong characters
// and leaves the sensitive ones in place — which looks done.
func TestApplyRedactionsHonoursCharacterOffsets(t *testing.T) {
	opts := RedactLLMOptions{MaskChar: '*', MinMask: 3}

	t.Run("multibyte_text", func(t *testing.T) {
		text := "Nom: Montréal, ID: 12345"
		// "Montréal" is runes 5..13.
		got, err := applyRedactions(text, []RedactSpan{{Start: 5, End: 13, Category: "name"}}, opts)
		if err != nil {
			t.Fatalf("applyRedactions: %v", err)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("invalid UTF-8: %q", got)
		}
		if strings.Contains(got, "Montréal") {
			t.Errorf("the span was not redacted: %q", got)
		}
		if !strings.Contains(got, "Nom: ") || !strings.Contains(got, "12345") {
			t.Errorf("text outside the span was disturbed: %q", got)
		}
	})

	t.Run("bad_spans_are_refused", func(t *testing.T) {
		text := "Montréal"
		for _, span := range []RedactSpan{
			{Start: -1, End: 3},
			{Start: 3, End: 1},
			{Start: 0, End: 99},
			{Start: 0, End: 9}, // one past the rune count, inside the byte count
		} {
			if _, err := applyRedactions(text, []RedactSpan{span}, opts); err == nil {
				t.Errorf("span %+v must be refused", span)
			}
		}
	})

	t.Run("overlapping_spans_are_refused", func(t *testing.T) {
		text := "abcdefghij"
		_, err := applyRedactions(text, []RedactSpan{{Start: 0, End: 5}, {Start: 3, End: 8}}, opts)
		if err == nil {
			t.Error("overlapping spans must be refused")
		}
	})

	t.Run("no_spans_returns_the_text", func(t *testing.T) {
		got, err := applyRedactions(japanese, nil, opts)
		if err != nil || got != japanese {
			t.Errorf("got %q, err %v", got, err)
		}
	})

	t.Run("spans_out_of_order_are_handled", func(t *testing.T) {
		text := "abcdefghij"
		got, err := applyRedactions(text, []RedactSpan{{Start: 6, End: 8}, {Start: 0, End: 2}}, opts)
		if err != nil {
			t.Fatalf("applyRedactions: %v", err)
		}
		if strings.Contains(got, "ab") || strings.Contains(got, "gh") {
			t.Errorf("a span was not applied: %q", got)
		}
		if !strings.Contains(got, "cdef") {
			t.Errorf("text between the spans was disturbed: %q", got)
		}
	})
}
