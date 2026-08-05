package ops

import (
	"fmt"
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

// runeSlice returns the runes of text from start to end, both counted in
// characters. It reports an error rather than panicking or silently clamping,
// because the offsets it is given come from a model.
func runeSlice(text string, start, end int) (string, error) {
	total := utf8.RuneCountInString(text)

	switch {
	case start < 0 || end < 0:
		return "", fmt.Errorf("negative offset: [%d:%d]", start, end)
	case start > end:
		return "", fmt.Errorf("inverted range: [%d:%d]", start, end)
	case end > total:
		return "", fmt.Errorf("range [%d:%d] exceeds the text, which is %d characters", start, end, total)
	}

	if start == end {
		return "", nil
	}

	startByte, endByte := -1, len(text)
	count := 0
	for offset := range text {
		if count == start {
			startByte = offset
		}
		if count == end {
			endByte = offset
			break
		}
		count++
	}
	if startByte < 0 {
		startByte = len(text)
	}

	return text[startByte:endByte], nil
}

// runeByteOffsets converts a rune range into the byte offsets that slice it,
// so a caller building a string can splice around the range.
func runeByteOffsets(text string, start, end int) (int, int, error) {
	if _, err := runeSlice(text, start, end); err != nil {
		return 0, 0, err
	}

	startByte, endByte := len(text), len(text)
	count := 0
	for offset := range text {
		if count == start {
			startByte = offset
		}
		if count == end {
			endByte = offset
			break
		}
		count++
	}
	if start == 0 {
		startByte = 0
	}
	if end >= utf8.RuneCountInString(text) {
		endByte = len(text)
	}

	return startByte, endByte, nil
}
