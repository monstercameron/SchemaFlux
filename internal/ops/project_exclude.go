package ops

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"sort"
	"strings"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// excludedValue is one scalar removed from a payload, kept so the output can
// be checked for it.
type excludedValue struct {
	// text is the value as it reads in the document: a string trimmed, a
	// number's own digits (without a sign), a boolean as "true" or "false".
	text string

	// number is set when the value was a JSON number, so it can be compared
	// with the numbers of the output as numbers: the same value is written
	// 123456789 in one document and 1.23456789e+08 in another.
	number *big.Rat
}

// excludedValues holds every removed value, by the name of the field it was
// removed from. A field name that occurs in many records (a list of users, each
// with an "ssn") holds one entry per record: the scan has to look for all of
// them, not for whichever was removed last.
type excludedValues map[string][]excludedValue

// maxNumberLiteral bounds the numbers compared as numbers. A literal longer
// than this is still scanned as text.
const maxNumberLiteral = 400

// marshalWithoutExcluded serialises input with the named fields removed, and
// returns the values it removed so the output can be checked against them.
//
// Matching is on the JSON field name, case-insensitively, at every level of the
// document: a field named "ssn" nested inside an address is still an SSN.
//
// The document is copied token by token and not round-tripped through a decoded
// value. Decoding every number into a float64 changed an integer above 2^53 on
// its way to the model, so a call with Exclude sent different data from the
// same call without it, and writing a map back out reordered every struct's
// fields. Numbers are carried as the text they were written with, and the order
// of the fields is kept.
func marshalWithoutExcluded(input any, exclude []string) ([]byte, excludedValues, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, nil, err
	}
	if len(exclude) == 0 {
		return raw, nil, nil
	}

	excluded := make(map[string]struct{}, len(exclude))
	for _, name := range exclude {
		name = strings.TrimSpace(strings.ToLower(name))
		if name != "" {
			excluded[name] = struct{}{}
		}
	}
	if len(excluded) == 0 {
		return raw, nil, nil
	}

	copier := &excludingCopier{excluded: excluded, removed: excludedValues{}}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	var cleaned bytes.Buffer
	if err := copier.copyValue(decoder, &cleaned); err != nil {
		return nil, nil, err
	}
	return cleaned.Bytes(), copier.removed, nil
}

// excludingCopier copies a JSON document, leaving out every excluded key.
type excludingCopier struct {
	excluded map[string]struct{}
	removed  excludedValues
}

// copyValue copies the next value from decoder to out.
func (c *excludingCopier) copyValue(decoder *json.Decoder, out *bytes.Buffer) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}

	switch typed := token.(type) {
	case json.Delim:
		switch typed {
		case '{':
			return c.copyObject(decoder, out)
		case '[':
			return c.copyArray(decoder, out)
		}
		return types.Classified(types.KindConfiguration, errors.New("unexpected delimiter in the input document"))
	case string:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return err
		}
		out.Write(encoded)
	case json.Number:
		out.WriteString(typed.String())
	case bool:
		if typed {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case nil:
		out.WriteString("null")
	default:
		return types.Classified(types.KindConfiguration, errors.New("unexpected token in the input document"))
	}
	return nil
}

func (c *excludingCopier) copyObject(decoder *json.Decoder, out *bytes.Buffer) error {
	out.WriteByte('{')
	written := 0
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return types.Classified(types.KindConfiguration, errors.New("unexpected object key in the input document"))
		}

		if _, drop := c.excluded[strings.ToLower(key)]; drop {
			var value any
			if err := decoder.Decode(&value); err != nil {
				return err
			}
			c.record(key, value)
			continue
		}

		if written > 0 {
			out.WriteByte(',')
		}
		written++
		encodedKey, err := json.Marshal(key)
		if err != nil {
			return err
		}
		out.Write(encodedKey)
		out.WriteByte(':')
		if err := c.copyValue(decoder, out); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil { // the closing brace
		return err
	}
	out.WriteByte('}')
	return nil
}

func (c *excludingCopier) copyArray(decoder *json.Decoder, out *bytes.Buffer) error {
	out.WriteByte('[')
	written := 0
	for decoder.More() {
		if written > 0 {
			out.WriteByte(',')
		}
		written++
		if err := c.copyValue(decoder, out); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil { // the closing bracket
		return err
	}
	out.WriteByte(']')
	return nil
}

// record keeps a removed value for the output scan. Only scalars are kept: a
// removed object is gone wholesale, and matching its parts individually would
// produce false positives on common substrings.
func (c *excludingCopier) record(key string, value any) {
	if kept, ok := newExcludedValue(value); ok {
		c.removed[key] = append(c.removed[key], kept)
	}
}

// newExcludedValue describes a removed scalar, or reports that it is not one.
func newExcludedValue(value any) (excludedValue, bool) {
	switch typed := value.(type) {
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return excludedValue{}, false
		}
		return excludedValue{text: text}, true
	case bool:
		if typed {
			return excludedValue{text: "true"}, true
		}
		return excludedValue{text: "false"}, true
	case json.Number:
		return newExcludedNumber(typed.String())
	default:
		return excludedValue{}, false
	}
}

// newExcludedNumber describes a removed number by its digits and its value.
func newExcludedNumber(literal string) (excludedValue, bool) {
	if literal == "" {
		return excludedValue{}, false
	}
	value := excludedValue{text: strings.TrimPrefix(literal, "-")}
	if len(literal) > maxNumberLiteral {
		return value, true
	}
	if rat, ok := new(big.Rat).SetString(literal); ok {
		value.number = rat
		value.text = numberDigits(rat, literal)
	}
	return value, true
}

// numberDigits is how a number reads when written out in full, without a sign:
// an integer in its digits, anything else as the literal it was given unless
// that used an exponent.
func numberDigits(rat *big.Rat, literal string) string {
	if rat.IsInt() {
		return new(big.Int).Abs(rat.Num()).String()
	}
	if !strings.ContainsAny(literal, "eE") {
		return strings.TrimPrefix(literal, "-")
	}
	text := new(big.Rat).Abs(rat).FloatString(12)
	text = strings.TrimRight(text, "0")
	return strings.TrimSuffix(text, ".")
}

// findLeakedValues reports the name of the first excluded field one of whose
// removed values appears in the projected output, or "" if none does. Every
// value removed under a name is checked, not one per name.
//
// A removed string is looked for as a substring of the output's JSON, in any
// case. A removed number is compared with the output's numbers as a number, so
// it is found however the output writes it, and it is also looked for as digits
// inside the output's strings.
//
// Short values are skipped: a removed boolean, or a two-character country code,
// would match by coincidence and turn every projection into an error.
func findLeakedValues(projected any, removed excludedValues) string {
	if len(removed) == 0 {
		return ""
	}

	rendered, err := json.Marshal(projected)
	if err != nil {
		return ""
	}
	haystack := strings.ToLower(string(rendered))

	var numbers []*big.Rat
	var texts []string
	numbersDecoded := false
	collectOutput := func() {
		if numbersDecoded {
			return
		}
		numbersDecoded = true
		decoder := json.NewDecoder(bytes.NewReader(rendered))
		decoder.UseNumber()
		var document any
		if err := decoder.Decode(&document); err != nil && err != io.EOF {
			return
		}
		numbers, texts = collectScalars(document, numbers, texts)
	}

	// Sorted, so the reported field is the same one on every run.
	fields := make([]string, 0, len(removed))
	for field := range removed {
		fields = append(fields, field)
	}
	sort.Strings(fields)

	for _, field := range fields {
		for _, value := range removed[field] {
			if len(value.text) < minimumLeakLength {
				continue
			}
			if value.number == nil {
				if strings.Contains(haystack, strings.ToLower(value.text)) {
					return field
				}
				continue
			}

			collectOutput()
			for _, number := range numbers {
				if number.Cmp(value.number) == 0 || number.Cmp(new(big.Rat).Neg(value.number)) == 0 {
					return field
				}
			}
			for _, text := range texts {
				if strings.Contains(text, value.text) {
					return field
				}
			}
		}
	}
	return ""
}

// collectScalars gathers the numbers and the strings of a decoded document, at
// every level.
func collectScalars(node any, numbers []*big.Rat, texts []string) ([]*big.Rat, []string) {
	switch typed := node.(type) {
	case map[string]any:
		for _, value := range typed {
			numbers, texts = collectScalars(value, numbers, texts)
		}
	case []any:
		for _, value := range typed {
			numbers, texts = collectScalars(value, numbers, texts)
		}
	case string:
		texts = append(texts, typed)
	case json.Number:
		if len(typed) <= maxNumberLiteral {
			if rat, ok := new(big.Rat).SetString(typed.String()); ok {
				numbers = append(numbers, rat)
			}
		}
	}
	return numbers, texts
}

// minimumLeakLength is the shortest excluded value worth scanning for. Below
// it, a match says more about coincidence than about a leak.
const minimumLeakLength = 6

// sortedStrings returns a copy in a stable order, so the prompt bytes do not
// depend on the caller's slice order.
func sortedStrings(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
