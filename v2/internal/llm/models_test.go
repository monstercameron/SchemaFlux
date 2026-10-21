package llm

import "testing"

// The Jev API answers as "jev-1.13.0" when it is asked for "jev-latest". The
// version it resolved to ends in a dotted number, which is not a date and not a
// run of digits, so the answer from the model that was asked for was not read as
// that model and a client with a data policy refused it after it had been paid
// for. A dotted version is a tag for a model whose base name was asked for as
// "-latest" or bare, and for no other pair of names.
func TestSameModelRouteReadsADottedVersionAsATag(t *testing.T) {
	cases := []struct {
		name     string
		asked    string
		answered string
		want     bool
	}{
		// The same model.
		{"the alias answered by the version it resolved to", "jev-latest", "jev-1.13.0", true},
		{"in capitals", "jev-latest", "JEV-1.13.0", true},
		{"asked in capitals", "Jev-Latest", "jev-1.13.0", true},
		{"a version of two parts", "jev-latest", "jev-1.13", true},
		{"a version of four parts", "jev-latest", "jev-1.13.0.2", true},
		{"a later major version", "jev-latest", "jev-2.0.1", true},
		{"the bare name answered by a version", "jev", "jev-1.13.0", true},
		{"the same name", "jev-latest", "jev-latest", true},
		{"a gateway's alias answered by the vendor's version", "~typesafe/jev-latest", "typesafe/jev-1.13.0", true},
		{"a gateway's alias answered without the vendor", "~typesafe/jev-latest", "jev-1.13.0", true},
		{"a vendor's alias answered by its version", "typesafe/jev-latest", "typesafe/jev-1.13.0", true},
		{"another family that names its versions so", "claude-sonnet-latest", "claude-sonnet-4.5.1", true},
		{"a name with a hyphen before the version", "jev-fast-latest", "jev-fast-1.13.0", true},

		// Not the same model.
		{"a model that shares a prefix", "jev-latest", "jevx-1.13.0", false},
		{"a sibling that shares the family name", "jev-latest", "jev-mini-1.13.0", false},
		{"a version of a different family", "jev-latest", "gpt-1.13.0", false},
		{"a different vendor's version", "jev-latest", "other/jev-1.13.0", false},
		{"a gateway alias against another vendor's version", "~typesafe/jev-latest", "other/jev-1.13.0", false},
		{"gpt-6 against gpt-6-luna", "gpt-6", "gpt-6-luna", false},
		{"gpt-6 against a version of gpt", "gpt-6", "gpt-6.1.0", false},
		{"a pinned version against another version", "jev-1.13.0", "jev-1.14.0", false},
		{"a pinned version of two parts against a later patch", "gpt-5.4", "gpt-5.4.1", false},
		{"a version with a word after it", "jev-latest", "jev-1.13.0-preview", false},
		{"a version with a word in it", "jev-latest", "jev-1.13.beta", false},
		{"a single number is not a dotted version", "jev-latest", "jev-1", false},
		{"a dot with nothing after it", "jev-latest", "jev-1.", false},
		{"a dot with nothing before it", "jev-latest", "jev-.13", false},
		{"two dots in a row", "jev-latest", "jev-1..0", false},
		{"nothing before the version", "jev-latest", "-1.13.0", false},
		{"nothing answered", "jev-latest", "", false},
		{"nothing asked", "", "jev-1.13.0", false},
		{"a version without a hyphen", "jev-latest", "jev1.13.0", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SameModelRoute(tc.asked, tc.answered); got != tc.want {
				t.Errorf("SameModelRoute(%q, %q) = %v, want %v", tc.asked, tc.answered, got, tc.want)
			}
		})
	}
}

// What was a tag before is still one, and what was not is still not.
func TestSameModelRouteStillReadsDatesAndNumbersAsTags(t *testing.T) {
	cases := []struct {
		asked, answered string
		want            bool
	}{
		{"gpt-4o", "gpt-4o-2024-08-06", true},
		{"gpt-4o-2024-08-06", "gpt-4o", true},
		{"claude-3-5-sonnet-latest", "claude-3-5-sonnet-20241022", true},
		{"gemini-1.5-pro-latest", "gemini-1.5-pro-002", true},
		{"openai/gpt-4o", "gpt-4o-2024-08-06", true},
		{"gpt-4", "gpt-4-32k", false},
		{"gpt-4o", "gpt-4o-mini-2024-07-18", false},
		{"gpt-6-luna", "gpt-6-luna-mini", false},
		{"gpt-6", "gpt-6-luna", false},
		{"gpt-4o", "gpt-4o-mini", false},
	}
	for _, tc := range cases {
		if got := SameModelRoute(tc.asked, tc.answered); got != tc.want {
			t.Errorf("SameModelRoute(%q, %q) = %v, want %v", tc.asked, tc.answered, got, tc.want)
		}
	}
}

func TestWithoutDottedVersion(t *testing.T) {
	cases := []struct {
		name string
		in   string
		base string
		ok   bool
	}{
		{"three parts", "jev-1.13.0", "jev", true},
		{"two parts", "jev-1.13", "jev", true},
		{"four parts", "jev-1.13.0.2", "jev", true},
		{"a hyphen in the base", "claude-sonnet-4.5.1", "claude-sonnet", true},
		{"a vendor in front", "typesafe/jev-1.13.0", "typesafe/jev", true},
		{"a tilde in front", "~typesafe/jev-1.13.0", "~typesafe/jev", true},
		{"a leading zero", "jev-01.13.0", "jev", true},
		{"no version", "jev", "jev", false},
		{"one number", "jev-1", "jev-1", false},
		{"a trailing dot", "jev-1.", "jev-1.", false},
		{"a leading dot", "jev-.1", "jev-.1", false},
		{"an empty part", "jev-1..2", "jev-1..2", false},
		{"a letter in a part", "jev-1.x", "jev-1.x", false},
		{"a v in front", "jev-v1.13.0", "jev-v1.13.0", false},
		{"a word after the version", "jev-1.13.0-rc1", "jev-1.13.0-rc1", false},
		{"nothing before the hyphen", "-1.13.0", "-1.13.0", false},
		{"no hyphen", "jev1.13.0", "jev1.13.0", false},
		{"a version and nothing else", "1.13.0", "1.13.0", false},
		{"empty", "", "", false},
		{"only a hyphen", "-", "-", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base, ok := withoutDottedVersion(tc.in)
			if base != tc.base || ok != tc.ok {
				t.Errorf("withoutDottedVersion(%q) = %q, %v, want %q, %v", tc.in, base, ok, tc.base, tc.ok)
			}
		})
	}
}
