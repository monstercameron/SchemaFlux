package ops

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/monstercameron/schemaflux/internal/types"
)

// Numeric fidelity.
//
// float64 is the wrong default for money, identifiers, and large integers, and
// it is the default a Go struct gets by accident. A nineteen-digit account
// number decoded into a float64 comes back rounded; a `float32` price loses
// cents; an `int8` status silently overflows -- and encoding/json reports none
// of the first two, because losing precision is what a float is for.
//
// The check is a round trip. Decode the answer, re-encode the decoded value,
// and compare the numbers literal for literal as exact rationals. A number that
// changed is a number the target type could not hold, and the caller is told
// which field rather than being handed a value that is quietly wrong.
//
// Rationals rather than floats, because comparing the thing under suspicion
// with itself proves nothing: 1284.50 and 1284.5 are the same rational and
// different strings, and 90071992547409910 and 90071992547409920 are different
// rationals and the same float64.
//
// What the round trip cannot see: float32. Go marshals a float32 as the
// shortest decimal that round-trips *as a float32*, so 1284.57 stored as
// 1284.5699462890625 is re-encoded as "1284.57" and the trip looks clean. The
// loss is real and this method cannot detect it, which is a reason to say so
// here rather than to imply coverage the check does not have. A float32 for
// money is a type choice to catch at preflight -- S-010 -- not a value to catch
// at decode.

// CheckNumericFidelity reports whether decoding lost numeric precision.
//
// It runs after a successful decode, on the strict path, because it can only
// compare an answer against what the target actually holds.
func CheckNumericFidelity(body string, decoded any) error {
	original, ok := decodeExactNumbers(extractJSON(body))
	if !ok {
		return nil
	}

	reEncoded, err := json.Marshal(decoded)
	if err != nil {
		return nil
	}
	roundTripped, ok := decodeExactNumbers(string(reEncoded))
	if !ok {
		return nil
	}

	if path, before, after, lost := findNumericLoss(original, roundTripped, nil); lost {
		return &types.OperationError{
			Kind: types.KindSchemaViolation,
			Message: fmt.Sprintf(
				"the value at %s cannot be held by its Go type: the response said %s and the decoded value is %s",
				pointerOf(path), before, after),
		}
	}

	return nil
}

// decodeExactNumbers decodes into `any` with numbers kept as their literals.
func decodeExactNumbers(body string) (any, bool) {
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()

	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	return value, true
}

// findNumericLoss walks two decoded documents in parallel and reports the first
// number that changed.
//
// Fields the target does not have are skipped rather than reported: a value the
// Go type drops entirely is S-008's business (an unrecognised property), not a
// precision question, and reporting it here would produce two errors for one
// problem.
func findNumericLoss(original, roundTripped any, path []string) ([]string, string, string, bool) {
	switch source := original.(type) {
	case map[string]any:
		target, ok := roundTripped.(map[string]any)
		if !ok {
			return nil, "", "", false
		}
		// Sorted, so the reported field does not depend on map iteration order.
		for _, key := range sortedKeys(source) {
			next, present := target[key]
			if !present {
				continue
			}
			if p, before, after, lost := findNumericLoss(source[key], next, append(path, key)); lost {
				return p, before, after, true
			}
		}

	case []any:
		target, ok := roundTripped.([]any)
		if !ok {
			return nil, "", "", false
		}
		for i := range source {
			if i >= len(target) {
				break
			}
			if p, before, after, lost := findNumericLoss(source[i], target[i], append(path, fmt.Sprint(i))); lost {
				return p, before, after, true
			}
		}

	case json.Number:
		target, ok := roundTripped.(json.Number)
		if !ok {
			// The value stopped being a number: a float64 that became a string,
			// or a field the target holds as text. Not a precision loss.
			return nil, "", "", false
		}
		if !sameExactValue(source.String(), target.String()) {
			return path, source.String(), target.String(), true
		}
	}

	return nil, "", "", false
}

// sameExactValue compares two JSON number literals as exact rationals.
func sameExactValue(before, after string) bool {
	first, firstOK := new(big.Rat).SetString(before)
	second, secondOK := new(big.Rat).SetString(after)
	if !firstOK || !secondOK {
		// A literal neither big.Rat nor the decoder can read is not something
		// to accuse the target type over.
		return true
	}
	return first.Cmp(second) == 0
}
