package ops

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/monstercameron/schemaflux/internal/types"
)

// Exact decoding.
//
// encoding/json is permissive by design: it ignores properties it does not
// recognise, accepts a duplicate key by taking the last one, and stops at the
// end of the first value without minding what follows. Those are reasonable
// defaults for reading a config file written by a person. They are the wrong
// defaults for reading an answer from a model, where an unrecognised property
// is the most interesting thing in the response -- it is the model producing a
// field nobody asked for, which is what overproduction looks like from the
// outside, and F-035 could only catch the case where *no* expected field was
// present.
//
// Strict mode applies all of this. Nothing else does, because rejecting an
// extra field is exactly wrong for an operation whose contract permits one.

// DecodeLimits bounds what a decoder will accept before it gives up.
//
// These exist because the input is hostile by default: a response is bytes from
// a remote service, and a deeply nested or enormous one costs memory and time
// before any of it turns out to be useless. The numbers are generous for real
// answers and small enough to stop a pathological one.
type DecodeLimits struct {
	// MaxBytes is the largest response body accepted. Zero means the default.
	MaxBytes int

	// MaxDepth is the deepest nesting accepted.
	MaxDepth int

	// MaxArrayItems bounds any single array.
	MaxArrayItems int

	// MaxStringBytes bounds any single string value.
	MaxStringBytes int
}

// DefaultDecodeLimits are what strict mode uses when a caller says nothing.
func DefaultDecodeLimits() DecodeLimits {
	return DecodeLimits{
		MaxBytes:       8 << 20, // 8 MiB: far above any real structured answer
		MaxDepth:       64,
		MaxArrayItems:  100_000,
		MaxStringBytes: 4 << 20,
	}
}

func (l DecodeLimits) withDefaults() DecodeLimits {
	defaults := DefaultDecodeLimits()
	if l.MaxBytes <= 0 {
		l.MaxBytes = defaults.MaxBytes
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = defaults.MaxDepth
	}
	if l.MaxArrayItems <= 0 {
		l.MaxArrayItems = defaults.MaxArrayItems
	}
	if l.MaxStringBytes <= 0 {
		l.MaxStringBytes = defaults.MaxStringBytes
	}
	return l
}

// DecodeExact parses a response under the strict contract.
//
// It reports the smallest failing JSON pointer it can, because "the response
// did not fit" is not actionable and "/items/3/price is a string" is.
func DecodeExact(input string, target any, limits DecodeLimits) error {
	limits = limits.withDefaults()

	body := extractJSON(input)

	if len(body) > limits.MaxBytes {
		return &types.OperationError{
			Kind: types.KindMalformedOutput,
			Message: fmt.Sprintf("the response is %d bytes, above the %d-byte limit",
				len(body), limits.MaxBytes),
		}
	}

	// Structural checks first, on the raw bytes: encoding/json resolves
	// duplicates and stops at the first value, so by the time it has decoded,
	// the evidence of both is gone.
	if err := checkStructure(body, limits); err != nil {
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

	// Trailing data, checked against the *original* response rather than the
	// extracted body.
	//
	// `{"a":1} {"a":2}` is a model that answered twice, and taking the first
	// silently is the same class of mistake as taking the last of a duplicate
	// key. The extractor pulls out the first balanced value by design -- that is
	// how it finds a payload amid prose -- so by the time the decoder runs, the
	// second value is already gone and the decoder's own trailing check has
	// nothing to see.
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
			Message: "the response carried more than one JSON value; only the first would have been used",
		}
	}

	// Numbers last, because it can only compare an answer against what the
	// target actually holds. S-009.
	return CheckNumericFidelity(body, target)
}

// checkStructure walks the tokens looking for what the decoder would silently
// resolve: duplicate keys, and nesting or sizes beyond the limits.
func checkStructure(body string, limits DecodeLimits) error {
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()

	// path tracks where we are, so a failure can name a pointer rather than an
	// offset nobody can map back to the response.
	var path []string
	// keysAtDepth[i] holds the object keys seen at nesting level i.
	keysAtDepth := map[int]map[string]bool{}
	// countAtDepth counts array elements at each level.
	countAtDepth := map[int]int{}
	// expectingKey[i] is true when the next token at level i is an object key.
	expectingKey := map[int]bool{}

	depth := 0

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
				depth++
				if depth > limits.MaxDepth {
					return &types.OperationError{
						Kind: types.KindMalformedOutput,
						Message: fmt.Sprintf("the response nests deeper than %d levels at %s",
							limits.MaxDepth, pointerOf(path)),
					}
				}
				keysAtDepth[depth] = map[string]bool{}
				countAtDepth[depth] = 0
				expectingKey[depth] = typed == '{'
			case '}', ']':
				delete(keysAtDepth, depth)
				delete(countAtDepth, depth)
				delete(expectingKey, depth)
				depth--
				if len(path) > 0 {
					path = path[:len(path)-1]
				}
			}
			continue

		case string:
			if expectingKey[depth] {
				if keysAtDepth[depth][typed] {
					return &types.OperationError{
						Kind: types.KindSchemaViolation,
						Message: fmt.Sprintf("the response repeats the key %q at %s; only the last would have been used",
							typed, pointerOf(path)),
					}
				}
				keysAtDepth[depth][typed] = true
				path = append(path, typed)
				expectingKey[depth] = false
				continue
			}
			if len(typed) > limits.MaxStringBytes {
				return &types.OperationError{
					Kind: types.KindMalformedOutput,
					Message: fmt.Sprintf("a string value at %s is %d bytes, above the %d-byte limit",
						pointerOf(path), len(typed), limits.MaxStringBytes),
				}
			}
		}

		// A value completed. In an object the next token is a key again; in an
		// array it is another element.
		if depth > 0 {
			if _, isObject := keysAtDepth[depth]; isObject && !expectingKey[depth] {
				expectingKey[depth] = true
				if len(path) > 0 {
					path = path[:len(path)-1]
				}
			} else {
				countAtDepth[depth]++
				if countAtDepth[depth] > limits.MaxArrayItems {
					return &types.OperationError{
						Kind: types.KindMalformedOutput,
						Message: fmt.Sprintf("an array at %s has more than %d items",
							pointerOf(path), limits.MaxArrayItems),
					}
				}
			}
		}
	}

	return nil
}

// pointerOf renders a JSON pointer for the path, which is the standard way to
// name a location inside a document and the one a caller can act on.
func pointerOf(path []string) string {
	if len(path) == 0 {
		return "the top level"
	}
	var builder strings.Builder
	for _, segment := range path {
		builder.WriteByte('/')
		builder.WriteString(strings.NewReplacer("~", "~0", "/", "~1").Replace(segment))
	}
	return builder.String()
}

// describeDecodeFailure turns a decoder error into a message that names the
// field rather than reproducing the value.
func describeDecodeFailure(err error) string {
	var unknown *json.UnmarshalTypeError
	switch {
	case errors.As(err, &unknown):
		if unknown.Field != "" {
			return fmt.Sprintf("the field /%s is a %s, which does not fit the target type",
				strings.ReplaceAll(unknown.Field, ".", "/"), unknown.Value)
		}
		return fmt.Sprintf("a %s value does not fit the target type", unknown.Value)
	case strings.Contains(err.Error(), "unknown field"):
		// The decoder names the field and nothing else, so this is safe to
		// pass through: it is a key, not a value.
		return "the response carried " + strings.TrimPrefix(err.Error(), "json: ")
	default:
		return "the response does not fit the target type"
	}
}

// secondJSONValue reports whether another complete JSON value follows the one
// that was extracted, and what shape it is.
//
// Prose after a JSON answer is normal and is not this: a model saying "let me
// know if you need anything else" is packaging. A whole second object is an
// answer nobody will read.
func secondJSONValue(original, extracted string) (string, bool) {
	trimmed := strings.TrimSpace(original)
	at := strings.Index(trimmed, extracted)
	if at < 0 {
		return "", false
	}

	rest := trimmed[at+len(extracted):]
	// A fenced answer's closing fence is packaging, not a value.
	rest = strings.TrimSpace(strings.ReplaceAll(rest, "```", " "))
	if rest == "" {
		return "", false
	}

	next, ok := balancedValue(rest)
	if !ok || strings.TrimSpace(next) == "" {
		return "", false
	}
	if !json.Valid([]byte(next)) {
		return "", false
	}

	if strings.HasPrefix(strings.TrimSpace(next), "[") {
		return "array", true
	}
	return "object", true
}
