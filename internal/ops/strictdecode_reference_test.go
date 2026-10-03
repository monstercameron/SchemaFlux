package ops

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/monstercameron/schemaflux/internal/types"
)

// The exact decoder as it was before it ran on encoding/json/v2, kept as the
// measure of the one that replaced it. It decoded with encoding/json's Decoder,
// after a pass of its own over the bytes for repeated keys and limits, and read
// the numbers by decoding the answer and the decoded value into `any` once each.
//
// The tests that hold DecodeExact to it feed both the same bodies and ask that
// the new one never accept what this one refused, and that where both refuse
// they refuse with the same kind of error.

// referenceDecodeExact is DecodeExact as it was.
func referenceDecodeExact(input string, target any, limits DecodeLimits) error {
	limits = limits.withDefaults()

	body := extractJSON(input)

	if len(body) > limits.MaxBytes {
		return &types.OperationError{
			Kind: types.KindMalformedOutput,
			Message: fmt.Sprintf("the response is %d bytes, above the %d-byte limit",
				len(body), limits.MaxBytes),
		}
	}

	if err := referenceCheckStructure(body, limits); err != nil {
		return err
	}

	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()

	if err := decoder.Decode(target); err != nil {
		return &types.OperationError{
			Kind:    types.KindSchemaViolation,
			Cause:   err,
			Message: describeDecodeFailure(err),
		}
	}

	if second, ok := secondJSONValue(input, body); ok {
		return &types.OperationError{
			Kind: types.KindSchemaViolation,
			Message: fmt.Sprintf("the response carried more than one JSON value (a second %s follows the first); only the first would have been used",
				second),
		}
	}

	if _, err := decoder.Token(); err != io.EOF {
		return &types.OperationError{
			Kind:    types.KindSchemaViolation,
			Message: "the response has data after its first JSON value; only the first would have been used",
		}
	}

	return referenceCheckNumericFidelity(body, target)
}

// referenceStructureFrame is one open container while the token walk reads a
// document.
type referenceStructureFrame struct {
	object    bool
	keys      map[string]bool
	expectKey bool
	key       string
	count     int
}

func (f *referenceStructureFrame) segment() (string, bool) {
	if f.object {
		return f.key, !f.expectKey
	}
	return strconv.Itoa(f.count), true
}

func referenceFramePath(stack []*referenceStructureFrame, includeTop bool) string {
	var path []string
	for i, frame := range stack {
		if i == len(stack)-1 && !includeTop {
			break
		}
		if segment, ok := frame.segment(); ok {
			path = append(path, segment)
		}
	}
	return pointerOf(path)
}

// referenceCheckStructure is one pass over the bytes that clears an answer with
// nothing wrong in it, and the token walk for any other.
func referenceCheckStructure(body string, limits DecodeLimits) error {
	if referenceStructureWithinLimits(body, limits) {
		return nil
	}
	return referenceCheckStructureByTokens(body, limits)
}

const referenceMaxScannedObjectKeys = 256

type referenceScanFrame struct {
	object    bool
	expectKey bool
	count     int
	firstKey  int
}

// referenceStructureWithinLimits is the pass over the bytes. It answers false
// whenever it is not sure.
func referenceStructureWithinLimits(body string, limits DecodeLimits) bool {
	if !json.Valid([]byte(body)) {
		return false
	}

	var frameSpace [16]referenceScanFrame
	var keySpace [64]string
	frames := frameSpace[:0]
	keys := keySpace[:0]

	completed := func() bool {
		if len(frames) == 0 {
			return true
		}
		top := &frames[len(frames)-1]
		if top.object {
			top.expectKey = true
			return true
		}
		top.count++
		return top.count <= limits.MaxArrayItems
	}

	for i := 0; i < len(body); i++ {
		switch body[i] {
		case ' ', '\t', '\r', '\n', ',', ':':

		case '{', '[':
			if len(frames)+1 > limits.MaxDepth {
				return false
			}
			frame := referenceScanFrame{object: body[i] == '{', firstKey: len(keys)}
			frame.expectKey = frame.object
			frames = append(frames, frame)

		case '}', ']':
			if len(frames) == 0 {
				return false
			}
			keys = keys[:frames[len(frames)-1].firstKey]
			frames = frames[:len(frames)-1]
			if !completed() {
				return false
			}

		case '"':
			end, escaped := endOfScannedString(body, i)
			text := body[i+1 : end]
			i = end

			if len(frames) > 0 {
				if top := &frames[len(frames)-1]; top.object && top.expectKey {
					if escaped || !utf8.ValidString(text) {
						return false
					}
					for _, seen := range keys[top.firstKey:] {
						if seen == text {
							return false
						}
					}
					if len(keys)-top.firstKey >= referenceMaxScannedObjectKeys {
						return false
					}
					keys = append(keys, text)
					top.expectKey = false
					continue
				}
			}

			if len(text) > limits.MaxStringBytes/3 {
				if len(text) > limits.MaxStringBytes || !utf8.ValidString(text) {
					return false
				}
			}
			if !completed() {
				return false
			}

		default:
			for i+1 < len(body) && !isScannedDelimiter(body[i+1]) {
				i++
			}
			if !completed() {
				return false
			}
		}
	}
	return len(frames) == 0
}

// referenceCheckStructureByTokens walks the tokens of encoding/json's Decoder
// looking for a repeated key and for nesting or sizes past the limits.
func referenceCheckStructureByTokens(body string, limits DecodeLimits) error {
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()

	var stack []*referenceStructureFrame

	completed := func() error {
		if len(stack) == 0 {
			return nil
		}
		top := stack[len(stack)-1]
		if top.object {
			top.expectKey = true
			top.key = ""
			return nil
		}
		top.count++
		if top.count > limits.MaxArrayItems {
			return &types.OperationError{
				Kind: types.KindMalformedOutput,
				Message: fmt.Sprintf("an array at %s has more than %d items",
					referenceFramePath(stack, false), limits.MaxArrayItems),
			}
		}
		return nil
	}

	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return &types.OperationError{
				Kind:    types.KindMalformedOutput,
				Cause:   err,
				Message: "the response is not well-formed JSON",
			}
		}

		switch typed := token.(type) {
		case json.Delim:
			switch typed {
			case '{', '[':
				if len(stack)+1 > limits.MaxDepth {
					return &types.OperationError{
						Kind: types.KindMalformedOutput,
						Message: fmt.Sprintf("the response nests deeper than %d levels at %s",
							limits.MaxDepth, referenceFramePath(stack, true)),
					}
				}
				frame := &referenceStructureFrame{object: typed == '{'}
				if frame.object {
					frame.keys = map[string]bool{}
					frame.expectKey = true
				}
				stack = append(stack, frame)
			case '}', ']':
				if len(stack) > 0 {
					stack = stack[:len(stack)-1]
				}
				if err := completed(); err != nil {
					return err
				}
			}
			continue

		case string:
			if len(stack) > 0 {
				if top := stack[len(stack)-1]; top.object && top.expectKey {
					if top.keys[typed] {
						return &types.OperationError{
							Kind: types.KindSchemaViolation,
							Message: fmt.Sprintf("the response repeats the key %q at %s; only the last would have been used",
								typed, referenceFramePath(stack, false)),
						}
					}
					top.keys[typed] = true
					top.key = typed
					top.expectKey = false
					continue
				}
			}
			if len(typed) > limits.MaxStringBytes {
				return &types.OperationError{
					Kind: types.KindMalformedOutput,
					Message: fmt.Sprintf("a string value at %s is %d bytes, above the %d-byte limit",
						referenceFramePath(stack, true), len(typed), limits.MaxStringBytes),
				}
			}
		}

		if err := completed(); err != nil {
			return err
		}
	}

	return nil
}

// referenceCheckNumericFidelity decodes the answer and the re-encoded decoded
// value into `any` with numbers kept as literals and compares them.
func referenceCheckNumericFidelity(body string, decoded any) error {
	original, ok := referenceDecodeExactNumbers(extractJSON(body))
	if !ok {
		return nil
	}

	reEncoded, err := json.Marshal(decoded)
	if err != nil {
		return nil
	}
	roundTripped, ok := referenceDecodeExactNumbers(string(reEncoded))
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

func referenceDecodeExactNumbers(body string) (any, bool) {
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()

	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	return value, true
}
