package ops

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TI-009. The shared UTF-8 corpus.
//
// Every truncation, slicing, masking, and length calculation in this library is
// a place where a byte index can land mid-character, and each one was tested
// against whatever string its author happened to pick. This is the fixture they
// all use, so a new operation inherits the cases rather than rediscovering them.
//
// The categories are chosen for what breaks, not for coverage of Unicode:
//
//   - accented Latin: two bytes per character, one rune, one grapheme -- the
//     cheapest way to break a byte index
//   - CJK: three bytes per character, and no spaces, so word-based splitting
//     has nothing to split on
//   - emoji: four bytes, and the ZWJ sequence is several runes that display as
//     one, so even a rune count disagrees with what a reader sees
//   - combining marks: the same visible character as one rune or two, so two
//     strings that look identical have different lengths
//   - RTL: the visual order is not the byte order, so "the first three
//     characters" is not the leftmost three
var utf8Corpus = []struct {
	Name  string
	Text  string
	Runes int
}{
	{"ascii", "Hello, world", 12},
	{"accented latin", "Montréal café", 13},
	{"currency", "Total: 45€ or £38", 17},
	{"cjk", "東京都渋谷区", 6},
	{"japanese sentence", "これはテストです", 8},
	{"emoji", "ship it 🚀", 9},
	{"emoji zwj", "family 👨‍👩‍👧", 12},
	// Decomposed on purpose: "e" followed by U+0301, so seven visible
	// characters are nine runes. A rune count and a reader disagree here,
	// which is the case that catches code assuming they are the same.
	{"combining marks", "égalité", 9},
	{"rtl arabic", "مرحبا بالعالم", 13},
	{"rtl hebrew", "שלום עולם", 9},
	{"mixed", "café 東京 🚀 שלום", 14},
}

// The corpus has to be what it claims to be, or every test using it is
// asserting against a typo.
func TestUTF8CorpusIsWellFormed(t *testing.T) {
	for _, tc := range utf8Corpus {
		t.Run(tc.Name, func(t *testing.T) {
			if !utf8.ValidString(tc.Text) {
				t.Fatal("the corpus entry is not valid UTF-8")
			}
			if got := len([]rune(tc.Text)); got != tc.Runes {
				t.Errorf("rune count = %d, want %d", got, tc.Runes)
			}
			if tc.Name != "ascii" && len(tc.Text) == len([]rune(tc.Text)) {
				t.Error("this entry is all ASCII, so it exercises nothing")
			}
		})
	}
}

// Truncation at every cut point, over the whole corpus. A byte-indexed
// implementation fails this on the first non-ASCII entry.
func TestTruncationOverTheCorpusNeverSplitsACharacter(t *testing.T) {
	for _, tc := range utf8Corpus {
		t.Run(tc.Name, func(t *testing.T) {
			for cut := 0; cut <= tc.Runes+2; cut++ {
				got := truncateRunes(tc.Text, cut)

				if !utf8.ValidString(got) {
					t.Fatalf("truncating to %d produced invalid UTF-8: %q", cut, got)
				}
				if strings.ContainsRune(got, utf8.RuneError) && !strings.ContainsRune(tc.Text, utf8.RuneError) {
					t.Fatalf("truncating to %d produced a replacement character: %q", cut, got)
				}
				if want := min(cut, tc.Runes); len([]rune(got)) != want {
					t.Errorf("truncating to %d gave %d runes, want %d", cut, len([]rune(got)), want)
				}
				if !strings.HasPrefix(tc.Text, got) {
					t.Errorf("truncating to %d gave %q, which is not a prefix of the input", cut, got)
				}
			}
		})
	}
}

// Masking reveals characters, not bytes, so ShowFirst(2) on Japanese shows two
// Japanese characters rather than two thirds of one.
func TestMaskingOverTheCorpusCountsCharacters(t *testing.T) {
	opts := NewRedactLLMOptions().WithShowFirst(2).WithShowLast(1).WithMinMask(3)

	for _, tc := range utf8Corpus {
		if tc.Runes < 4 {
			continue
		}
		t.Run(tc.Name, func(t *testing.T) {
			masked := maskText(tc.Text, opts)

			if !utf8.ValidString(masked) {
				t.Fatalf("masking produced invalid UTF-8: %q", masked)
			}

			runes := []rune(tc.Text)
			if prefix := string(runes[:2]); !strings.HasPrefix(masked, prefix) {
				t.Errorf("masked = %q, want it to start with the first two characters %q", masked, prefix)
			}
			if suffix := string(runes[len(runes)-1:]); !strings.HasSuffix(masked, suffix) {
				t.Errorf("masked = %q, want it to end with the last character %q", masked, suffix)
			}
		})
	}
}

// Length measurement in characters means runes everywhere, which is what makes
// a MaxLength or a CompressionRatio mean the same thing in every alphabet.
func TestLengthMeasurementOverTheCorpus(t *testing.T) {
	for _, tc := range utf8Corpus {
		t.Run(tc.Name, func(t *testing.T) {
			if got := MeasureLength(tc.Text, LengthInCharacters); got != tc.Runes {
				t.Errorf("MeasureLength(characters) = %d, want %d", got, tc.Runes)
			}
			if got := MeasureLength(tc.Text, LengthInCharacters); got == len(tc.Text) && tc.Name != "ascii" {
				t.Error("the measurement is counting bytes")
			}
		})
	}
}

// Redaction spans located by text survive the corpus: the located offsets slice
// the value back out, whatever alphabet surrounds it.
func TestSpanLocationOverTheCorpus(t *testing.T) {
	for _, tc := range utf8Corpus {
		t.Run(tc.Name, func(t *testing.T) {
			doc := tc.Text + " contact ada@example.com now"

			spans, err := parseRedactResponse(
				`{"spans":[{"text":"ada@example.com","start":5,"category":"email"}]}`, doc)
			if err != nil {
				t.Fatalf("parseRedactResponse: %v", err)
			}
			if len(spans) != 1 {
				t.Fatalf("got %d spans, want 1", len(spans))
			}
			if got := doc[spans[0].Start:spans[0].End]; got != "ada@example.com" {
				t.Errorf("the span slices %q out of a document beginning with %s text", got, tc.Name)
			}
		})
	}
}
