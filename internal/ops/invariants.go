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
