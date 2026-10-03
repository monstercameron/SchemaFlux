package ops

import (
	"strings"
	"unicode/utf8"
)

// Go strings are bytes, and every length and offset in this package was written
// as though they were characters. A cut that lands mid-rune produces invalid
// UTF-8, so "Montréal" truncated to 7 "characters" came back with a replacement
// character where the é should be — and MaxLength is documented in characters.
//
// These helpers do the same jobs on runes. They are deliberately boring: the
// bug was never in the arithmetic, it was in the unit.

// truncateRunes returns the first limit runes of text. A limit at or beyond the
// length returns text unchanged, and a negative limit returns nothing.
func truncateRunes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if utf8.RuneCountInString(text) <= limit {
		return text
	}

	count := 0
	for offset := range text {
		if count == limit {
			return text[:offset]
		}
		count++
	}
	return text
}

// runeLen is the character count, which is what every "length" in this package
// was documented to mean.
func runeLen(text string) int {
	return utf8.RuneCountInString(text)
}

// truncateAtWord trims text to at most limit runes, preferring to cut at the
// last space at or after keep runes so a completion does not end mid-word.
func truncateAtWord(text string, limit, keep int) string {
	truncated := truncateRunes(text, limit)
	if truncated == text {
		return text
	}

	lastSpace := strings.LastIndex(truncated, " ")
	if lastSpace < 0 {
		return truncated
	}
	// The byte offset has to translate back to a rune count before it can be
	// compared with keep, which is in runes.
	if runeLen(truncated[:lastSpace]) > keep {
		return truncated[:lastSpace]
	}
	return truncated
}
