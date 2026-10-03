package ops

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// Reading a decision's answer.

// dec12ReadObject reads the model's response as the JSON object it was asked
// for, and reports whether any answer in it was given twice.
//
// Decoding into a map keeps the last of two entries under one key and says
// nothing, so a model that answered a question "true" and then "false" was read
// as having said no. The object is decoded into a map as it always was, which is
// what an answer costs when nothing is wrong, and the members of the text are
// counted beside it: more members than entries means a key was given twice, and
// only then are the keys read one at a time to see whether it was a key the
// decision reads, a question's or the confidence's. A repeat of anything else the
// object holds is not a contradiction of anything.
//
// A response that is JSON null is an object with nothing in it, as it is to the
// decoder, and is refused for what it does not answer.
func dec12ReadObject(response string) (entries map[string]json.RawMessage, repeated bool, err error) {
	cleaned := cleanJSON(response)
	if err := json.Unmarshal([]byte(cleaned), &entries); err != nil {
		return nil, false, err
	}
	if dec12CountMembers(cleaned) > len(entries) {
		repeated = dec12RepeatsADecisionKey(cleaned)
	}
	return entries, repeated, nil
}

// dec12CountMembers counts the members of the JSON object a text holds: the
// commas outside any string at its top level, and one more when there is a
// member at all. The text has been decoded already, so it is valid JSON, and
// this does not check that it is.
func dec12CountMembers(text string) int {
	depth, commas := 0, 0
	inString, escaped, member := false, false, false
	for i := 0; i < len(text); i++ {
		c := text[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case ' ', '\t', '\r', '\n':
		case '{', '[':
			if depth == 1 {
				member = true
			}
			depth++
		case '}', ']':
			depth--
		case ',':
			if depth == 1 {
				commas++
			}
		case '"':
			inString = true
			if depth == 1 {
				member = true
			}
		default:
			if depth == 1 {
				member = true
			}
		}
	}
	if !member {
		return 0
	}
	return commas + 1
}

// dec12RepeatsADecisionKey reads the keys of a JSON object one at a time and
// reports whether a key a decision reads is among them twice.
func dec12RepeatsADecisionKey(text string) bool {
	decoder := json.NewDecoder(strings.NewReader(text))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return false
		}
		key, _ := keyToken.(string)
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return false
		}
		if seen[key] && dec12DecisionKey(key) {
			return true
		}
		seen[key] = true
	}
	return false
}

// dec12DecisionKey reports whether a key is one a decision reads.
func dec12DecisionKey(key string) bool {
	return key == "confidence" || dec12QuestionLike(key)
}

// dec12QuestionLike reports whether a key is written as the key of a question:
// a "q" and then digits or signs.
func dec12QuestionLike(key string) bool {
	if len(key) < 2 || key[0] != 'q' {
		return false
	}
	for i := 1; i < len(key); i++ {
		if c := key[i]; (c < '0' || c > '9') && c != '+' && c != '-' {
			return false
		}
	}
	return true
}

// dec12AskedKey reports whether key is the key of one of n questions, written
// as theirs are: "q" and the position, with no sign and no leading zero.
func dec12AskedKey(key string, n int) bool {
	digits := key[1:]
	if len(digits) == 0 || len(digits) > 9 || digits[0] == '0' {
		return false
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return false
		}
	}
	position, _ := strconv.Atoi(digits)
	return position >= 1 && position <= n
}

// dec12WholeNumber reads a JSON number as the whole number it is, exactly. It
// takes 2, 2.0 and 2e0 as the same 2, and refuses a fraction, a string, a
// number that does not fit an int, and anything that is not a number.
//
// It does not go through a float: a number past 2^53 would be rounded to a
// neighbour, which could land on an option or a level that was not the one the
// model wrote, and a float outside the range of an int converts to a value that
// depends on the platform.
func dec12WholeNumber(raw []byte) (int, bool) {
	if number, plain := dec12PlainInteger(raw); plain {
		return number, true
	}

	text := bytes.TrimSpace(raw)
	i := 0
	negative := i < len(text) && text[i] == '-'
	if negative {
		i++
	}
	digitsFrom := func() []byte {
		start := i
		for i < len(text) && text[i] >= '0' && text[i] <= '9' {
			i++
		}
		return text[start:i]
	}

	whole := digitsFrom()
	if len(whole) == 0 {
		return 0, false
	}
	var fraction []byte
	if i < len(text) && text[i] == '.' {
		i++
		if fraction = digitsFrom(); len(fraction) == 0 {
			return 0, false
		}
	}
	exponent := 0
	if i < len(text) && (text[i] == 'e' || text[i] == 'E') {
		i++
		exponentNegative := false
		if i < len(text) && (text[i] == '+' || text[i] == '-') {
			exponentNegative = text[i] == '-'
			i++
		}
		written := digitsFrom()
		// An exponent of more than four digits is a number that is far outside
		// an int, or a zero with a strange way of being written.
		if len(written) == 0 || len(written) > 4 {
			return 0, false
		}
		exponent, _ = strconv.Atoi(string(written))
		if exponentNegative {
			exponent = -exponent
		}
	}
	if i != len(text) {
		return 0, false
	}

	digits := strings.TrimLeft(string(whole)+string(fraction), "0")
	if digits == "" {
		return 0, true
	}
	scale := exponent - len(fraction)
	for scale < 0 && strings.HasSuffix(digits, "0") {
		digits = digits[:len(digits)-1]
		scale++
	}
	// Digits left to the right of the point are a fraction.
	if scale < 0 || len(digits)+scale > 19 {
		return 0, false
	}
	digits += strings.Repeat("0", scale)
	if negative {
		digits = "-" + digits
	}
	value, err := strconv.ParseInt(digits, 10, strconv.IntSize)
	if err != nil {
		return 0, false
	}
	return int(value), true
}
