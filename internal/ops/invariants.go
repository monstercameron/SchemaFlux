package ops

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The beginning of the shared invariant library A-009 describes. Each one is a
// deterministic check the model's answer has to survive, expressed once rather
// than approximated per operation.
//
// The pattern these replace is a count: `if len(result) != len(items)`. A count
// catches the model that dropped an item and misses the model that returned one
// item twice and dropped another, which is the more common failure and the one
// that silently corrupts a result rather than obviously breaking it.

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
		return fmt.Errorf("received %d items for %d input items", len(output), len(input))
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
		return fmt.Errorf("the result is not a permutation of the input: %d items are missing and %d were duplicated or invented",
			len(missing), len(invented))
	case len(missing) > 0:
		return fmt.Errorf("the result is not a permutation of the input: %d items are missing", len(missing))
	default:
		return fmt.Errorf("the result is not a permutation of the input: %d items were duplicated or invented", len(invented))
	}
}

// SubsetOf reports whether every value in output appears in input, with no
// duplicates. It is the contract a filter has.
func SubsetOf[T any](input, output []T) error {
	if len(output) > len(input) {
		return fmt.Errorf("the result has %d items for %d input items, so it is not a subset",
			len(output), len(input))
	}

	counts := make(map[string]int, len(input))
	for _, item := range input {
		counts[canonicalKey(item)]++
	}

	var foreign, duplicated int
	for _, item := range output {
		key := canonicalKey(item)
		switch {
		case counts[key] == 0:
			foreign++
		default:
			counts[key]--
		}
	}

	// A value returned more times than it appeared in the input shows up as
	// foreign on its second appearance, which is the right classification for a
	// subset: the extra copy was not in the input.
	if foreign == 0 && duplicated == 0 {
		return nil
	}
	return fmt.Errorf("the result is not a subset of the input: %d items were not offered", foreign)
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
	return fmt.Errorf("the groups do not partition the input: %s", strings.Join(problems, "; "))
}

// MemberOf reports whether the chosen value is one of the offered ones.
func MemberOf[T any](offered []T, chosen T) error {
	key := canonicalKey(chosen)
	for _, item := range offered {
		if canonicalKey(item) == key {
			return nil
		}
	}
	return fmt.Errorf("the selection was not one of the %d items offered", len(offered))
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
	// The answer is the model's, not the caller's data, and naming it is what
	// makes the error actionable -- a category that is nearly right reads very
	// differently from one that is nothing like the set.
	return "", fmt.Errorf("the model answered %q, which is not one of the %d allowed categories %v",
		answer, len(allowed), allowed)
}

// AtLeastConfidence reports whether a model-reported confidence meets the
// caller's floor.
//
// The number being checked is a model claim, not a measurement, and this does
// not make it one. What it does is make the option mean something: MinConfidence
// was interpolated into the prompt and never read back, so an operation with a
// default of 0.7 returned results the model itself scored at 0.2 and reported
// them as success.
func AtLeastConfidence(reported, minimum float64) error {
	if minimum <= 0 {
		return nil
	}
	if reported >= minimum {
		return nil
	}
	return fmt.Errorf("the model reported confidence %.2f, below the %.2f floor this operation was configured with", reported, minimum)
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
// count.
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

// WithinLength reports whether text is at most limit units long.
//
// MaxLength and TargetLength were requested in a prompt and never checked, so
// an operation documented as producing at most N of something produced whatever
// the model produced. The tolerance exists because "target" is a target: a
// model asked for three sentences that returns four has done the job, and
// failing the call would be a worse answer than the one it gave.
func WithinLength(text string, limit int, unit LengthUnit, tolerance float64) error {
	if limit <= 0 {
		return nil
	}
	if tolerance < 0 {
		tolerance = 0
	}

	measured := MeasureLength(text, unit)
	ceiling := int(float64(limit) * (1 + tolerance))
	if measured <= ceiling {
		return nil
	}

	return fmt.Errorf("the result is %d %s, above the %d requested (tolerance %.0f%%)",
		measured, unit, limit, tolerance*100)
}

// countSentences counts terminators rather than splitting, so trailing
// punctuation and a missing final period both behave.
func countSentences(text string) int {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0
	}

	count := 0
	inSentence := false
	for _, char := range trimmed {
		switch char {
		case '.', '!', '?':
			if inSentence {
				count++
				inSentence = false
			}
		case ' ', '\t', '\n', '\r':
			// whitespace does not start a sentence
		default:
			inSentence = true
		}
	}
	if inSentence {
		// Text that does not end in a terminator is still a sentence.
		count++
	}
	return count
}

func countParagraphs(text string) int {
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
	return fmt.Errorf("received %d items for a limit of %d", len(items), limit)
}

// ExcludesValues reports whether an answer contains any value the caller ruled
// out.
//
// The count is deliberate. Naming the offending values would put the caller's
// own records into an error string that every caller logs, which is F-034 and
// the payload rule in AGENTS.md: a redaction operation that reports "the
// output still contains 4111-1111-1111-1111" has leaked the thing it was asked
// to remove.
func ExcludesValues[T any](forbidden, output []T) error {
	if len(forbidden) == 0 || len(output) == 0 {
		return nil
	}

	banned := make(map[string]struct{}, len(forbidden))
	for _, value := range forbidden {
		banned[canonicalKey(value)] = struct{}{}
	}

	violations := 0
	for _, value := range output {
		if _, found := banned[canonicalKey(value)]; found {
			violations++
		}
	}
	if violations > 0 {
		return fmt.Errorf("the answer contains %d value(s) the caller excluded", violations)
	}
	return nil
}

// Satisfies runs a caller's own predicate as an invariant, so a domain rule is
// checked in the same place and on the same terms as the built-in ones.
//
// A-009's Revised line is what this is for: a rule the caller writes after the
// operation returns is paid for twice and cannot participate in the repair
// loop, because by then the answer has already been handed back. Naming the
// rule is required rather than optional -- an unnamed predicate produces
// "invariant failed", which tells the repair loop nothing to feed back and
// tells the caller nothing to fix.
//
// The value is never named in the error, for the same reason ExcludesValues
// counts rather than quotes.
func Satisfies[T any](value T, name string, predicate func(T) bool) error {
	if predicate == nil {
		// A nil predicate is a rule that cannot fail, which would silently
		// weaken the contract of every call that registered it.
		return fmt.Errorf("the invariant %q has no predicate to run", name)
	}
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("an invariant was registered with no name, so a failure could not be reported usefully")
	}
	if predicate(value) {
		return nil
	}
	return fmt.Errorf("the answer did not satisfy %q", name)
}

// OneOf is deliberately absent. A-009 lists it beside MemberOf, and they are
// the same check: "is this answer one of the values it was allowed to be".
// MemberOf covers values compared canonically and CategoryIn covers the
// case-folded string enum; a third spelling would be the fourth membership
// test AGENTS.md forbids, and the failure that causes is two call sites
// disagreeing about whether "Urgent" is "urgent".
