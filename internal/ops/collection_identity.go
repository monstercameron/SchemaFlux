package ops

import (
	"encoding/json"
	"fmt"
)

// The collection operations ask the model to echo back whole objects. Nothing
// checked that what came back was what went in, so a model that invented a
// product, or — far more likely — echoed one with a subtly altered price, ID,
// or date, produced a result that looked authoritative and did not exist in the
// caller's data.
//
// The fix is identity rather than equality: match the model's answer against
// the input, and return the caller's original item. What the model sends back
// is a selection, not a value.

// canonicalise renders a value as JSON for comparison. Two values of the same
// type that differ only in formatting produce identical bytes, because they
// have both been through Unmarshal into T and back.
func canonicalise[T any](value T) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// buildIndex maps each item's canonical form to its position. Duplicates in the
// input keep their first position, which is the one a stable filter returns.
func buildIndex[T any](items []T) (map[string]int, error) {
	index := make(map[string]int, len(items))
	for i, item := range items {
		key, err := canonicalise(item)
		if err != nil {
			return nil, fmt.Errorf("canonicalising item %d: %w", i, err)
		}
		if _, seen := index[key]; !seen {
			index[key] = i
		}
	}
	return index, nil
}

// matchInputItem finds the caller's item corresponding to a model-returned one.
// It returns the index into items, or -1 when the model returned something that
// was not offered.
func matchInputItem[T any](returned T, index map[string]int) (int, error) {
	key, err := canonicalise(returned)
	if err != nil {
		return -1, fmt.Errorf("canonicalising the returned item: %w", err)
	}
	if position, found := index[key]; found {
		return position, nil
	}
	return -1, nil
}

// resolveSelection maps one model-returned item back to the caller's original.
// A selection that was not among the options is an error: the alternative is
// returning a record that does not exist in the caller's data.
func resolveSelection[T any](returned T, items []T) (T, int, error) {
	var zero T

	index, err := buildIndex(items)
	if err != nil {
		return zero, -1, err
	}

	position, err := matchInputItem(returned, index)
	if err != nil {
		return zero, -1, err
	}
	if position < 0 {
		// The error names the shape of the mismatch, never the payload: this
		// error is logged, and the payload is the caller's data.
		return zero, -1, fmt.Errorf(
			"the selection was not one of the %d options offered; the model returned an item that does not appear in the input",
			len(items))
	}

	return items[position], position, nil
}

// resolveSubset maps a model-returned slice back to the caller's originals,
// preserving input order. Every returned item must have been in the input, and
// no item may appear twice.
func resolveSubset[T any](returned []T, items []T) ([]T, error) {
	if len(returned) > len(items) {
		return nil, fmt.Errorf(
			"the filter returned %d items from an input of %d; a subset cannot be larger than the set",
			len(returned), len(items))
	}

	index, err := buildIndex(items)
	if err != nil {
		return nil, err
	}

	keep := make(map[int]struct{}, len(returned))
	for i, item := range returned {
		position, err := matchInputItem(item, index)
		if err != nil {
			return nil, err
		}
		if position < 0 {
			return nil, fmt.Errorf(
				"filtered item %d of %d was not in the input; the filter returned an item the caller never supplied",
				i+1, len(returned))
		}
		if _, duplicate := keep[position]; duplicate {
			return nil, fmt.Errorf(
				"filtered item %d of %d duplicates an item already kept; a filter selects, it does not repeat",
				i+1, len(returned))
		}
		keep[position] = struct{}{}
	}

	// Input order, caller's values.
	result := make([]T, 0, len(keep))
	for i, item := range items {
		if _, kept := keep[i]; kept {
			result = append(result, item)
		}
	}
	return result, nil
}
