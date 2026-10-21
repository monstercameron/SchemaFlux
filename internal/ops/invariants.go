package ops

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// The beginning of the shared invariant library A-009 describes. Each one is a
// deterministic check the model's answer has to survive, expressed once rather
// than approximated per operation.
//
// The pattern these replace is a count: `if len(result) != len(items)`. A count
// catches the model that dropped an item and misses the model that returned one
// item twice and dropped another, which is the more common failure and the one
// that silently corrupts a result rather than obviously breaking it.
//
// Every refusal is an invariant violation, so a caller can branch on it with
// errors.Is(err, types.ErrInvariantViolation), and none of them carries a value
// of the caller's or the model's: counts and positions only.

// invariantFailure is the error every check here returns.
func invariantFailure(format string, args ...any) error {
	return types.Newf(types.KindInvariantViolation, "", format, args...)
}

// canonicalKey renders a value as a comparable string.
//
// encoding/json sorts map keys, so two equal maps produce equal bytes; struct
// field order is the declaration order, which is fixed for a given type. Values
// that cannot be marshalled fall back to their Go formatting, which is enough
// for a multiset comparison even though it is not a wire format.
func canonicalKey(value any) string {
	if encoded, err := json.Marshal(value); err == nil {
		return string(encoded)
	}
	return fmt.Sprintf("%#v", value)
}

// SameMultiset reports whether output contains exactly the same values as
// input, in any order.
//
// This is the contract a sort has: a permutation. Not "the same number of
// things" -- the same things.
func SameMultiset[T any](input, output []T) error {
	if len(input) != len(output) {
		return invariantFailure("received %d items for %d input items", len(output), len(input))
	}

	counts := make(map[string]int, len(input))
	for _, item := range input {
		counts[canonicalKey(item)]++
	}

	var invented []string
	for _, item := range output {
		key := canonicalKey(item)
		counts[key]--
		if counts[key] < 0 {
			invented = append(invented, key)
		}
	}

	var missing []string
	for key, remaining := range counts {
		for i := 0; i < remaining; i++ {
			missing = append(missing, key)
		}
	}

	if len(missing) == 0 && len(invented) == 0 {
		return nil
	}

	// The keys are the caller's own values, so the message reports how many
	// went wrong rather than which. F-034: an error that carries the payload
	// copies it wherever the error goes.
	switch {
	case len(missing) > 0 && len(invented) > 0:
		return invariantFailure("the result is not a permutation of the input: %d items are missing and %d were duplicated or invented",
			len(missing), len(invented))
	case len(missing) > 0:
		return invariantFailure("the result is not a permutation of the input: %d items are missing", len(missing))
	default:
		return invariantFailure("the result is not a permutation of the input: %d items were duplicated or invented", len(invented))
	}
}

// SubsetOf reports whether every value in output appears in input, with no
// duplicates. It is the contract a filter has.
func SubsetOf[T any](input, output []T) error {
	if len(output) > len(input) {
		return invariantFailure("the result has %d items for %d input items, so it is not a subset",
			len(output), len(input))
	}

	counts := make(map[string]int, len(input))
	for _, item := range input {
		counts[canonicalKey(item)]++
	}

	var foreign int
	for _, item := range output {
		key := canonicalKey(item)
		if counts[key] == 0 {
			foreign++
			continue
		}
		counts[key]--
	}

	// A value returned more times than it appeared in the input shows up as
	// foreign on its second appearance, which is the right classification for a
	// subset: the extra copy was not in the input.
	if foreign == 0 {
		return nil
	}
	return invariantFailure("the result is not a subset of the input: %d items were not offered", foreign)
}

// CoversExactlyOnce reports whether the index groups partition the input: every
// index used once, none out of range, none repeated.
//
// It is the contract a clustering has. A cluster operation that reports
// overlapping groups, or leaves items in no group at all, is not a partition,
// and a caller iterating the clusters silently processes an item twice or not
// at all.
func CoversExactlyOnce(total int, groups [][]int) error {
	seen := make([]int, total)

	var outOfRange []int
	for _, group := range groups {
		for _, index := range group {
			if index < 0 || index >= total {
				outOfRange = append(outOfRange, index)
				continue
			}
			seen[index]++
		}
	}

	var missing, repeated []int
	for index, count := range seen {
		switch {
		case count == 0:
			missing = append(missing, index)
		case count > 1:
			repeated = append(repeated, index)
		}
	}

	if len(outOfRange) == 0 && len(missing) == 0 && len(repeated) == 0 {
		return nil
	}

	var problems []string
	if len(outOfRange) > 0 {
		sort.Ints(outOfRange)
		problems = append(problems, fmt.Sprintf("%d out of range (%v)", len(outOfRange), outOfRange))
	}
	if len(missing) > 0 {
		problems = append(problems, fmt.Sprintf("%d items in no group (%v)", len(missing), missing))
	}
	if len(repeated) > 0 {
		problems = append(problems, fmt.Sprintf("%d items in more than one group (%v)", len(repeated), repeated))
	}

	// Indices, not values: a position is not the caller's data.
	return invariantFailure("the groups do not partition the input: %s", strings.Join(problems, "; "))
}

// MemberOf reports whether the chosen value is one of the offered ones.
func MemberOf[T any](offered []T, chosen T) error {
	key := canonicalKey(chosen)
	for _, item := range offered {
		if canonicalKey(item) == key {
			return nil
		}
	}
	return invariantFailure("the selection was not one of the %d items offered", len(offered))
}

// CategoryIn resolves the model's answer to one of the allowed categories,
// folding case, and reports an error when it is not one of them.
//
// Classify was the only operation in the library that checked the model's
// answer against the set it was offered. It should have been the template, not
// the exception -- returning the canonical spelling is what makes it reusable,
// because a caller comparing against their own constants needs "urgent" and
// "Urgent" to be the same answer.
func CategoryIn(allowed []string, answer string) (string, error) {
	for _, candidate := range allowed {
		if strings.EqualFold(answer, candidate) {
			return candidate, nil
		}
	}
	// Neither the answer nor the labels are named. A classifier that answers
	// with the text it was given as its category would put that text, and the
	// caller's labels, into an error every caller logs.
	return "", invariantFailure("the model's answer (%d characters) is not one of the %d allowed categories",
		len([]rune(answer)), len(allowed))
}

// AtLeastConfidence reports whether a model-reported confidence meets the
// caller's floor.
//
// The number being checked is a model claim, not a measurement, and this does
// not make it one. What it does is make the option mean something: MinConfidence
// was interpolated into the prompt and never read back, so an operation with a
// default of 0.7 returned results the model itself scored at 0.2 and reported
// them as success.
//
// A confidence is a number from 0 to 1. A reported value outside that range
// (85 for 85 percent, 7.0 for seven out of ten) is refused rather than read as
// meeting every floor, and so is a floor above 1, which nothing could meet.
func AtLeastConfidence(reported, minimum float64) error {
	if minimum <= 0 {
		return nil
	}
	if math.IsNaN(minimum) || minimum > 1 {
		return invariantFailure("the confidence floor this operation was configured with is not a number from 0 to 1")
	}
	if math.IsNaN(reported) || reported < 0 || reported > 1 {
		return invariantFailure("the model reported a confidence outside the range 0 to 1")
	}
	if reported >= minimum {
		return nil
	}
	return invariantFailure("the model reported confidence %.2f, below the %.2f floor this operation was configured with", reported, minimum)
}

// LengthUnit names what a length is counted in. The unit is part of the
// contract: "200" means nothing without it, and a summary asked for 3 sentences
// that comes back as 3 paragraphs is not the length that was requested.
type LengthUnit string

const (
	LengthInCharacters LengthUnit = "characters"
	LengthInWords      LengthUnit = "words"
	LengthInSentences  LengthUnit = "sentences"
	LengthInParagraphs LengthUnit = "paragraphs"
)

// MeasureLength counts text in the given unit. Characters means runes, not
// bytes: an accented character is one character to everyone except a byte
// count. A unit that is not one of the four (and the empty unit) is measured in
// characters; WithinLength is the function that refuses an unknown unit.
func MeasureLength(text string, unit LengthUnit) int {
	switch unit {
	case LengthInWords:
		return len(strings.Fields(text))
	case LengthInSentences:
		return countSentences(text)
	case LengthInParagraphs:
		return countParagraphs(text)
	default:
		return len([]rune(text))
	}
}

// knownLengthUnit reports whether a unit is one MeasureLength counts in. The
// empty unit is accepted and means characters.
func knownLengthUnit(unit LengthUnit) bool {
	switch unit {
	case "", LengthInCharacters, LengthInWords, LengthInSentences, LengthInParagraphs:
		return true
	}
	return false
}

// WithinLength reports whether text is at most limit units long.
//
// MaxLength and TargetLength were requested in a prompt and never checked, so
// an operation documented as producing at most N of something produced whatever
// the model produced. The tolerance exists because "target" is a target: a
// model asked for three sentences that returns four has done the job, and
// failing the call would be a worse answer than the one it gave.
//
// A unit that is not one of the four (or empty, which means characters) is
// refused: measuring a limit of three sentences in characters because "sentence"
// was misspelled would pass nearly any text. A tolerance that is not a finite
// number is refused too, because converting one to an int gives a different
// answer on different platforms.
func WithinLength(text string, limit int, unit LengthUnit, tolerance float64) error {
	if limit <= 0 {
		return nil
	}
	if !knownLengthUnit(unit) {
		return invariantFailure("the length unit is not one of characters, words, sentences or paragraphs")
	}
	if math.IsNaN(tolerance) || math.IsInf(tolerance, 0) {
		return invariantFailure("the length tolerance is not a finite number")
	}
	if tolerance < 0 {
		tolerance = 0
	}

	measured := MeasureLength(text, unit)
	ceiling := int(math.Floor(float64(limit) * (1 + tolerance)))
	if measured <= ceiling {
		return nil
	}

	return invariantFailure("the result is %d %s, above the %d requested (tolerance %.0f%%)",
		measured, unit, limit, tolerance*100)
}

// sentenceAbbreviations are the words whose full stop does not end a sentence
// when a capitalised word follows ("Dr. Smith"). Without a capital after it,
// the stop is not read as an end whatever the word is.
var sentenceAbbreviations = map[string]bool{
	"mr": true, "mrs": true, "ms": true, "dr": true, "prof": true, "sr": true,
	"jr": true, "st": true, "mt": true, "vs": true, "inc": true, "ltd": true,
	"corp": true, "gen": true, "col": true, "lt": true, "sgt": true, "capt": true,
	"gov": true, "rep": true, "sen": true, "rev": true, "e.g": true, "i.e": true,
}

func isSentenceTerminator(r rune) bool {
	return r == '.' || r == '!' || r == '?' || r == '…'
}

// isSentenceCloser reports whether a rune can close a quotation or a bracket
// after a sentence's last mark and belongs to that sentence.
func isSentenceCloser(r rune) bool {
	switch r {
	case '"', '\'', ')', ']', '}', '”', '’', '»', '›':
		return true
	}
	return false
}

// isSentenceOpener reports whether a rune only opens a quotation or a bracket,
// which does not make a sentence of what is inside it by itself.
func isSentenceOpener(r rune) bool {
	switch r {
	case '(', '[', '{', '“', '‘', '«', '‹':
		return true
	}
	return false
}

// precedingWord is the run of non-space runes that ends just before index end,
// lower-cased, with leading punctuation of the opening kind dropped.
func precedingWord(runes []rune, end int) string {
	start := end
	for start > 0 && !unicode.IsSpace(runes[start-1]) {
		start--
	}
	for start < end && (isSentenceOpener(runes[start]) || isSentenceCloser(runes[start])) {
		start++
	}
	return strings.ToLower(string(runes[start:end]))
}

// countSentences counts the sentences in a text.
//
// A run of full stops, question marks and exclamation marks ends a sentence
// when what follows it, after any closing quotes and brackets, is a space or the
// end of the text. The closing quotes and brackets belong to the sentence that
// they close, so "Yes." is one sentence and not two, and so is (see above.).
// Text that does not end in a mark is still a sentence.
//
// A mark is not an end when:
//   - it sits inside a token, which is how "3.14", "example.com" and "e.g" look;
//   - it is a full stop or an ellipsis, or is followed by a closing quote, and the
//     next word starts in lower case ("Wait... what?", "\"Stop!\" she said.");
//   - it is a single full stop after a title or another common abbreviation,
//     and the next word is capitalised ("Dr. Smith").
//
// What it knowingly gets wrong: a numbered list ("1. Apples 2. Pears" is three
// sentences), initials ("J. K. Rowling" is three), an abbreviation that is not
// in the list, an abbreviation such as "etc." that ends a sentence and one that
// does not look the same, a sentence that begins in lower case after a "!" or
// "?" with no quote between them, and a sentence whose first word is lower case
// after a full stop. Text without a space after the mark ("one.Two") is one
// sentence.
func countSentences(text string) int {
	runes := []rune(strings.TrimSpace(text))

	count := 0
	inSentence := false
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if unicode.IsSpace(r) || isSentenceCloser(r) || isSentenceOpener(r) {
			continue
		}
		if !isSentenceTerminator(r) {
			inSentence = true
			continue
		}

		// A run of marks, then the closing quotes and brackets after it.
		markEnd := i
		for markEnd < len(runes) && isSentenceTerminator(runes[markEnd]) {
			markEnd++
		}
		if !inSentence {
			i = markEnd - 1
			continue
		}
		closeEnd := markEnd
		for closeEnd < len(runes) && isSentenceCloser(runes[closeEnd]) {
			closeEnd++
		}

		if closeEnd < len(runes) && !unicode.IsSpace(runes[closeEnd]) {
			// Inside a token: the mark does not end anything.
			i = markEnd - 1
			continue
		}

		next, hasNext := rune(0), false
		for j := closeEnd; j < len(runes); j++ {
			if !unicode.IsSpace(runes[j]) {
				next, hasNext = runes[j], true
				break
			}
		}
		if hasNext {
			endsWithStop := runes[markEnd-1] == '.' || runes[markEnd-1] == '…'
			singleStop := markEnd-i == 1 && runes[i] == '.'
			continues := false
			switch {
			case unicode.IsLower(next) && (endsWithStop || closeEnd > markEnd):
				continues = true
			case unicode.IsUpper(next) && singleStop && closeEnd == markEnd && sentenceAbbreviations[precedingWord(runes, i)]:
				continues = true
			}
			if continues {
				i = closeEnd - 1
				continue
			}
		}

		count++
		inSentence = false
		i = closeEnd - 1
	}
	if inSentence {
		// Text that does not end in a terminator is still a sentence.
		count++
	}
	return count
}

// countParagraphs counts the blocks of text separated by a blank line, whether
// the line ends are "\n" or "\r\n".
func countParagraphs(text string) int {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	count := 0
	for _, block := range strings.Split(strings.TrimSpace(text), "\n\n") {
		if strings.TrimSpace(block) != "" {
			count++
		}
	}
	return count
}

// AtMost reports whether a collection stayed inside a caller's ceiling.
//
// The operations that take a "return at most N" option interpolated it into the
// prompt and never checked it, so a caller asking for three suggestions and
// getting seven had no way to find out except by counting -- and the ones that
// did count usually truncated silently, which turns a model that ignored the
// instruction into a result that looks obedient.
func AtMost[T any](items []T, limit int) error {
	if limit <= 0 || len(items) <= limit {
		return nil
	}
	return invariantFailure("received %d items for a limit of %d", len(items), limit)
}
