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
//
// It reports an error only for a loss it found. A body that is not JSON, or a
// decoded value that cannot be written back as JSON, gives it nothing to
// compare, and it returns nil: no loss was found, which is not the same as the
// numbers having been checked. On the strict path the body has been decoded
// already, so neither case arises there.
func CheckNumericFidelity(body string, decoded any) error {
	return checkNumericFidelity(extractJSON(body), decoded)
}

// checkNumericFidelity is CheckNumericFidelity for a body that is already the
// JSON value and nothing around it.
//
// An answer that fits is the case that matters, and in it the decoded value
// writes back as the answer was written: the same members, each number as its
// own literal or one with the same value. numbersAgree sees that by reading the
// two texts side by side, which costs no allocation beyond the written text.
// Decoding both into `any` to compare them, which is what finding the number that
// changed takes, costs about forty allocations for each record and is kept for
// the answer that does not read back the same.
func checkNumericFidelity(body string, decoded any) error {
	reEncoded, err := json.Marshal(decoded)
	if err != nil {
		return nil
	}
	if numbersAgree(body, reEncoded) {
		return nil
	}

	original, ok := decodeExactNumbers(body)
	if !ok {
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

// numbersAgree reports whether two JSON texts have the same shape, the same
// object members, and at each place a number of the same value in both, and so
// whether findNumericLoss would find nothing in them. Members are matched by
// their keys, in whatever order each text has them: a model that is held to a
// schema often writes the members in the order of the schema's properties,
// which is not the order the type declares them in and so not the order a
// decoded value is written back in.
//
// It answers false whenever it is not sure, for a text it cannot follow, for keys
// that are not spelled the same, for a number in one where the other has
// something else, for objects nested past thirty-two levels or with more than
// sixty-four members, and the caller then decides by comparing the decoded texts.
// A true is therefore always right.
//
// Strings are not compared except where they are keys, since the check is about
// numbers; a string value that was written another way, with an escape the
// encoder does not use, is the same string.
func numbersAgree(original string, reEncoded []byte) bool {
	i, j := skipJSONSpace(original, 0), skipJSONSpace(reEncoded, 0)
	if i == len(original) || j == len(reEncoded) {
		return i == len(original) && j == len(reEncoded)
	}
	i, j, agrees := valuesAgree(original, i, reEncoded, j, 0)
	return agrees && skipJSONSpace(original, i) == len(original) && skipJSONSpace(reEncoded, j) == len(reEncoded)
}

// How deep numbersAgree follows, and how many members of one object it matches
// by key. Past either it leaves the texts to be compared by decoding them.
const (
	numbersAgreeDepth   = 32
	numbersAgreeMembers = 64
)

// memberSpan is where a member of an object begins: its key, and its value.
type memberSpan struct{ key, value int }

// valuesAgree reads the value that begins at original[i] and the one that begins
// at reEncoded[j], both at a place that is not white space, and reports where
// each ends and whether they agree.
func valuesAgree(original string, i int, reEncoded []byte, j, depth int) (endOriginal, endReEncoded int, agrees bool) {
	if i >= len(original) || j >= len(reEncoded) {
		return i, j, false
	}

	switch c := original[i]; {
	case c == '{':
		if reEncoded[j] != '{' || depth == numbersAgreeDepth {
			return i, j, false
		}
		var spans [numbersAgreeMembers]memberSpan
		reEncodedMembers, endReEncoded, ok := membersOf(reEncoded, j, spans[:0])
		if !ok {
			return i, j, false
		}

		// Each member of the original is looked for by its key among those of the
		// re-encoded text, and there are as many of one as of the other.
		members := 0
		p := skipJSONSpace(original, i+1)
		for p < len(original) && original[p] != '}' {
			if members == len(reEncodedMembers) || original[p] != '"' {
				return i, j, false
			}
			keyEnd, _ := endOfScannedString(original, p)
			if keyEnd == len(original) {
				return i, j, false
			}
			key := original[p : keyEnd+1]
			p = skipJSONSpace(original, keyEnd+1)
			if p >= len(original) || original[p] != ':' {
				return i, j, false
			}
			p = skipJSONSpace(original, p+1)

			found := false
			for _, span := range reEncodedMembers {
				keyEnd, _ := endOfScannedString(reEncoded, span.key)
				if key != string(reEncoded[span.key:keyEnd+1]) {
					continue
				}
				if _, _, agrees := valuesAgree(original, p, reEncoded, span.value, depth+1); !agrees {
					return i, j, false
				}
				found = true
				break
			}
			if !found {
				return i, j, false
			}

			members++
			end, ok := skipJSONValue(original, p)
			if !ok {
				return i, j, false
			}
			p = skipJSONSpace(original, end)
			if p < len(original) && original[p] == ',' {
				p = skipJSONSpace(original, p+1)
			}
		}
		if p >= len(original) || members != len(reEncodedMembers) {
			return i, j, false
		}
		return p + 1, endReEncoded, true

	case c == '[':
		if reEncoded[j] != '[' || depth == numbersAgreeDepth {
			return i, j, false
		}
		i, j = skipJSONSpace(original, i+1), skipJSONSpace(reEncoded, j+1)
		for i < len(original) && original[i] != ']' {
			if j >= len(reEncoded) || reEncoded[j] == ']' {
				return i, j, false
			}
			var agrees bool
			if i, j, agrees = valuesAgree(original, i, reEncoded, j, depth+1); !agrees {
				return i, j, false
			}
			i, j = skipJSONSpace(original, i), skipJSONSpace(reEncoded, j)
			if i < len(original) && original[i] == ',' {
				i = skipJSONSpace(original, i+1)
			}
			if j < len(reEncoded) && reEncoded[j] == ',' {
				j = skipJSONSpace(reEncoded, j+1)
			}
		}
		if i >= len(original) || j >= len(reEncoded) || reEncoded[j] != ']' {
			return i, j, false
		}
		return i + 1, j + 1, true

	case c == '"':
		if reEncoded[j] != '"' {
			return i, j, false
		}
		endOriginal, _ := endOfScannedString(original, i)
		endReEncoded, _ := endOfScannedString(reEncoded, j)
		if endOriginal == len(original) || endReEncoded == len(reEncoded) {
			return i, j, false
		}
		return endOriginal + 1, endReEncoded + 1, true

	default:
		endOriginal, endReEncoded := endOfScannedWord(original, i), endOfScannedWord(reEncoded, j)
		word, other := original[i:endOriginal], reEncoded[j:endReEncoded]
		switch {
		case c == '-' || ('0' <= c && c <= '9'):
			if len(other) == 0 || !(other[0] == '-' || ('0' <= other[0] && other[0] <= '9')) ||
				(word != string(other) && !sameExactValue(word, string(other))) {
				return i, j, false
			}
		case word == "true" || word == "false" || word == "null":
			if word != string(other) {
				return i, j, false
			}
		default:
			return i, j, false
		}
		return endOriginal, endReEncoded, true
	}
}

// membersOf lists where each member of the object that begins at text[at] begins,
// appending them to spans, and says where the object ends. It reports false for
// text it cannot follow and for an object with more members than spans has room
// for.
func membersOf(text []byte, at int, spans []memberSpan) (members []memberSpan, end int, ok bool) {
	p := skipJSONSpace(text, at+1)
	for p < len(text) && text[p] != '}' {
		if text[p] != '"' || len(spans) == cap(spans) {
			return nil, p, false
		}
		keyEnd, _ := endOfScannedString(text, p)
		if keyEnd == len(text) {
			return nil, p, false
		}
		span := memberSpan{key: p}
		p = skipJSONSpace(text, keyEnd+1)
		if p >= len(text) || text[p] != ':' {
			return nil, p, false
		}
		span.value = skipJSONSpace(text, p+1)
		spans = append(spans, span)

		valueEnd, ok := skipJSONValue(text, span.value)
		if !ok {
			return nil, p, false
		}
		p = skipJSONSpace(text, valueEnd)
		if p < len(text) && text[p] == ',' {
			p = skipJSONSpace(text, p+1)
		}
	}
	if p >= len(text) {
		return nil, p, false
	}
	return spans, p + 1, true
}

// skipJSONValue is where the value that begins at text[i] ends, and false when it
// does not end: it runs past the text, or what stands there is not a value.
func skipJSONValue[Text ~string | ~[]byte](text Text, i int) (int, bool) {
	depth := 0
	for i < len(text) {
		switch text[i] {
		case '"':
			end, _ := endOfScannedString(text, i)
			if end == len(text) {
				return i, false
			}
			i = end + 1
			if depth == 0 {
				return i, true
			}
		case '{', '[':
			depth++
			i++
		case '}', ']':
			depth--
			i++
			if depth <= 0 {
				return i, depth == 0
			}
		case ' ', '\t', '\r', '\n', ',', ':':
			if depth == 0 {
				return i, false
			}
			i++
		default:
			i = endOfScannedWord(text, i)
			if depth == 0 {
				return i, true
			}
		}
	}
	return i, false
}

// skipJSONSpace is the position of the first byte at or after i that is not
// white space.
func skipJSONSpace[Text ~string | ~[]byte](text Text, i int) int {
	for i < len(text) {
		switch text[i] {
		case ' ', '\t', '\r', '\n':
			i++
		default:
			return i
		}
	}
	return i
}

// endOfScannedWord is the position after the number or literal that begins at
// start: the first delimiter at or after it, or the end of the text.
func endOfScannedWord[Text ~string | ~[]byte](text Text, start int) int {
	end := start
	for end < len(text) && !isScannedDelimiter(text[end]) && text[end] != ':' {
		end++
	}
	return end
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

// sameExactValue compares two JSON number literals as exact rationals. The same
// text is the same number, which is what nearly every pair is, and is answered
// without parsing either.
func sameExactValue(before, after string) bool {
	if before == after {
		return true
	}
	first, firstOK := new(big.Rat).SetString(before)
	second, secondOK := new(big.Rat).SetString(after)
	if !firstOK || !secondOK {
		// A literal neither big.Rat nor the decoder can read is not something
		// to accuse the target type over.
		return true
	}
	return first.Cmp(second) == 0
}
