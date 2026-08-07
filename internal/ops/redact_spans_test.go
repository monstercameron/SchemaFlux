package ops

import (
	"strings"
	"testing"
)

const spanDoc = "Contact john@email.com or call 305-555-1234 about invoice INV-4417."

// OP-507. The offsets were bounds-checked and otherwise trusted, and a bounds
// check cannot tell a correct offset from a plausible wrong one. Spans are
// located by their text now, so a wrong offset costs nothing.
func TestSpansAreLocatedByTextNotByOffset(t *testing.T) {
	cases := []struct {
		name     string
		response string
		want     string
	}{
		{
			"correct offset",
			`{"spans":[{"text":"john@email.com","start":8,"category":"email"}]}`,
			"john@email.com",
		},
		{
			"offset off by five, in range",
			`{"spans":[{"text":"john@email.com","start":3,"category":"email"}]}`,
			"john@email.com",
		},
		{
			"offset past the value",
			`{"spans":[{"text":"john@email.com","start":40,"category":"email"}]}`,
			"john@email.com",
		},
		{
			"no offset at all",
			`{"spans":[{"text":"305-555-1234","category":"phone"}]}`,
			"305-555-1234",
		},
		{
			"offset out of range entirely",
			`{"spans":[{"text":"305-555-1234","start":9999,"category":"phone"}]}`,
			"305-555-1234",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spans, err := parseRedactResponse(tc.response, spanDoc)
			if err != nil {
				t.Fatalf("parseRedactResponse: %v", err)
			}
			if len(spans) != 1 {
				t.Fatalf("got %d spans, want 1", len(spans))
			}
			span := spans[0]
			if span.Original != tc.want {
				t.Errorf("Original = %q, want %q", span.Original, tc.want)
			}
			// The recomputed offsets have to slice the value back out of the
			// document, which is the property the old code assumed and did not
			// check.
			if got := spanDoc[span.Start:span.End]; got != tc.want {
				t.Errorf("the span slices %q out of the document, want %q", got, tc.want)
			}
		})
	}
}

// A span the model invented is dropped rather than masking something the model
// never found.
func TestHallucinatedSpansAreDropped(t *testing.T) {
	responses := []string{
		`{"spans":[{"text":"nobody@example.org","start":8,"category":"email"}]}`,
		`{"spans":[{"text":"","start":8,"category":"email"}]}`,
		`{"spans":[{"start":8,"end":22,"category":"email"}]}`, // offsets only: nothing to verify
	}

	for _, response := range responses {
		spans, err := parseRedactResponse(response, spanDoc)
		if err != nil {
			t.Fatalf("parseRedactResponse: %v", err)
		}
		if len(spans) != 0 {
			t.Errorf("response %s produced %d spans, want none", response, len(spans))
		}
	}
}

// A value appearing twice produces two distinct spans, not two copies of the
// first occurrence.
func TestRepeatedValuesMapToDistinctOccurrences(t *testing.T) {
	doc := "Write to ada@example.com. Yes, ada@example.com."
	response := `{"spans":[{"text":"ada@example.com","start":10,"category":"email"},{"text":"ada@example.com","start":31,"category":"email"}]}`

	spans, err := parseRedactResponse(response, doc)
	if err != nil {
		t.Fatalf("parseRedactResponse: %v", err)
	}
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want 2", len(spans))
	}
	if spans[0].Start == spans[1].Start {
		t.Errorf("both spans landed on the same occurrence at %d", spans[0].Start)
	}
	for _, span := range spans {
		if doc[span.Start:span.End] != "ada@example.com" {
			t.Errorf("span slices %q", doc[span.Start:span.End])
		}
	}
}

// Non-ASCII is where offsets and byte indices part company: the model counts
// characters, Go slices bytes, and the two agree only for ASCII. Locating by
// text sidesteps it, and this proves the resulting offsets are byte offsets
// that slice correctly.
func TestSpansSurviveNonASCIIText(t *testing.T) {
	doc := "Écrire à josé@exemple.fr — merci ☕"
	response := `{"spans":[{"text":"josé@exemple.fr","start":9,"category":"email"}]}`

	spans, err := parseRedactResponse(response, doc)
	if err != nil {
		t.Fatalf("parseRedactResponse: %v", err)
	}
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	if got := doc[spans[0].Start:spans[0].End]; got != "josé@exemple.fr" {
		t.Errorf("the span slices %q out of the document", got)
	}
}

// OP-506. JumbleSmart was documented as preserving vowel and consonant
// structure and implemented as a call to jumbleBasic, which preserves neither.
func TestJumbleSmartPreservesTheStructureItPromises(t *testing.T) {
	opts := NewRedactOptions().WithJumbleMode(JumbleSmart).WithJumbleSeed(42)

	inputs := []string{
		"Alexander Hamilton",
		"ada@example.com",
		"305-555-1234",
		"Suite 400, 12 High Street",
	}

	for _, input := range inputs {
		got := jumbleString(input, opts)

		if len([]rune(got)) != len([]rune(input)) {
			t.Errorf("jumbleSmart(%q) = %q; length is part of the structure", input, got)
			continue
		}

		for i, original := range []rune(input) {
			replacement := []rune(got)[i]
			switch {
			case original >= '0' && original <= '9':
				if replacement < '0' || replacement > '9' {
					t.Errorf("%q: digit at %d became %q", input, i, string(replacement))
				}
			case isVowel(original):
				if !isVowel(replacement) {
					t.Errorf("%q: vowel at %d became %q", input, i, string(replacement))
				}
			case isASCIILetter(original):
				if !isASCIILetter(replacement) || isVowel(replacement) {
					t.Errorf("%q: consonant at %d became %q", input, i, string(replacement))
				}
			default:
				if replacement != original {
					t.Errorf("%q: separator at %d became %q", input, i, string(replacement))
				}
			}
		}
	}
}

// Case is structure too, and a caller looking at jumbled output should not be
// able to tell that a name was a name by its capitalisation being lost.
func TestJumbleSmartPreservesCase(t *testing.T) {
	opts := NewRedactOptions().WithJumbleMode(JumbleSmart).WithJumbleSeed(7)
	got := jumbleString("Ada Lovelace", opts)

	for i, original := range []rune("Ada Lovelace") {
		replacement := []rune(got)[i]
		if isASCIILetter(original) {
			upper := original >= 'A' && original <= 'Z'
			replacementUpper := replacement >= 'A' && replacement <= 'Z'
			if upper != replacementUpper {
				t.Errorf("case at %d changed: %q -> %q", i, string(original), string(replacement))
			}
		}
	}
}

// Substitution, not permutation. A shuffle keeps the exact multiset of
// characters, so the original is a rearrangement away and the character counts
// are a fingerprint; drawing fresh characters keeps only the shape.
func TestJumbleSmartIsNotAPermutation(t *testing.T) {
	opts := NewRedactOptions().WithJumbleMode(JumbleSmart).WithJumbleSeed(99)
	input := "Alexander Hamilton"
	got := jumbleString(input, opts)

	if sameRuneMultiset(input, got) {
		t.Errorf("jumbleSmart(%q) = %q, which is a rearrangement of the input", input, got)
	}
	if got == input {
		t.Error("the value came back unchanged")
	}
}

func sameRuneMultiset(a, b string) bool {
	counts := map[rune]int{}
	for _, r := range a {
		counts[r]++
	}
	for _, r := range b {
		counts[r]--
	}
	for _, remaining := range counts {
		if remaining != 0 {
			return false
		}
	}
	return true
}

// OP-504. The options argument was `...interface{}` with a default branch that
// silently substituted the defaults, so passing the wrong struct configured a
// redaction pass with none of the caller's settings. The signature is typed
// now, which makes that a compile error -- this asserts the behaviour that
// remains: no options means the documented defaults, and options given are the
// ones used.
func TestRedactOptionsResolution(t *testing.T) {
	defaults := resolveRedactOptions()
	if defaults.Strategy != RedactMask {
		t.Errorf("default Strategy = %q, want mask", defaults.Strategy)
	}
	if len(defaults.Categories) == 0 {
		t.Error("the defaults have no categories, so validation would refuse them")
	}

	custom := NewRedactOptions().WithStrategy(RedactRemove).WithMaskChar('#')
	resolved := resolveRedactOptions(custom)
	if resolved.Strategy != RedactRemove || resolved.MaskChar != '#' {
		t.Errorf("resolved = %+v, want the caller's own options", resolved)
	}
}

// The masked output still contains none of the original, which is the point of
// all of the above.
func TestRedactedTextKeepsNoneOfTheValue(t *testing.T) {
	spans, err := parseRedactResponse(
		`{"spans":[{"text":"john@email.com","start":8,"category":"email"},{"text":"305-555-1234","start":31,"category":"phone"}]}`,
		spanDoc)
	if err != nil {
		t.Fatalf("parseRedactResponse: %v", err)
	}

	redacted, err := applyRedactions(spanDoc, spans, NewRedactLLMOptions())
	if err != nil {
		t.Fatalf("applyRedactions: %v", err)
	}
	for _, secret := range []string{"john@email.com", "305-555-1234"} {
		if strings.Contains(redacted, secret) {
			t.Errorf("the redacted text still contains %q: %s", secret, redacted)
		}
	}
	if !strings.Contains(redacted, "INV-4417") {
		t.Errorf("the redaction removed something it was not asked to: %s", redacted)
	}
}
