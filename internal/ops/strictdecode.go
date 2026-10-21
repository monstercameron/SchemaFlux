package ops

import (
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/monstercameron/schemaflux/v2/internal/types"
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

// strictDecodeOptions make encoding/json/v2 read as encoding/json has always
// read, with the three refusals strict mode adds: a member repeated in an
// object, a member the target does not have, and anything after the value.
//
// The base is v1's own defaults, so that nothing else changes for a caller:
// names are matched ignoring case, a time.Duration is a number of nanoseconds,
// invalid UTF-8 is read as U+FFFD, and a failure is reported as the error types
// of encoding/json. v2's defaults differ in each of those and would refuse
// answers this library has always accepted.
//
// A number held by a value of type any is kept as its literal (readAnyExact),
// which is what Decoder.UseNumber did before.
var strictDecodeOptions = jsonv2.JoinOptions(
	json.DefaultOptionsV1(),
	jsontext.AllowDuplicateNames(false),
	jsonv2.RejectUnknownMembers(true),
	jsonv2.WithUnmarshalers(jsonv2.UnmarshalFromFunc(readAnyExact)),
)

// DecodeExact parses a response under the strict contract.
//
// It reports the smallest failing JSON pointer it can, because "the response
// did not fit" is not actionable and "/items/3/price is a string" is.
//
// The decoder does most of the work: it refuses a repeated key at any depth,
// a member the target lacks, and text after the value. What it has no limit
// for is checked first, in one pass over the bytes (structureWithinLimits): how
// deep the answer nests, how long a list is and how long a string is. When
// either finds something wrong, checkStructure walks the answer to say what
// came first and where, because the first thing wrong in the text is the thing
// to report.
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

	cleared := structureWithinLimits(body, limits)
	var decodeErr error
	if cleared {
		decodeErr = jsonv2.Unmarshal([]byte(body), target, strictDecodeOptions)
	}
	if !cleared || decodeErr != nil {
		// A fault in the text comes before a fault in the fit: what the decoder
		// reports is the first it meets, and a type that does not fit is no use
		// to a caller whose answer is not well-formed.
		if err := checkStructure(body, limits); err != nil {
			return err
		}
		if cleared {
			return decodeFailure(body, target, decodeErr)
		}
		if decodeErr = jsonv2.Unmarshal([]byte(body), target, strictDecodeOptions); decodeErr != nil {
			return decodeFailure(body, target, decodeErr)
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

	// Numbers last, because it can only compare an answer against what the
	// target actually holds. S-009.
	return checkNumericFidelity(body, target)
}

// decodeFailure is the error for a body the decoder did not accept and whose
// text checkStructure found nothing wrong with.
//
// The decoder's own error is classified and not attached: it can quote a member
// name or a number the model wrote, and nothing the model wrote belongs in an
// error. What is left of it is its type, which says what kind of misfit it was.
func decodeFailure(body string, target any, err error) error {
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) && repeatsAMemberByCase(body, target) {
		return &types.OperationError{
			Kind: types.KindSchemaViolation,
			Message: "the response gives one member of the target more than once, under names that differ only in the case of their letters; " +
				"only the last would have been used",
		}
	}
	return &types.OperationError{
		Kind:    types.KindSchemaViolation,
		Message: describeDecodeFailure(err),
	}
}

// repeatsAMemberByCase reports whether the body is refused only for naming one
// member of the target twice. Names are matched ignoring case, except for a
// member tagged case:strict, which takes only its own spelling, so "Name" and
// "name" are two keys to checkStructure and, for a member that is not strict,
// one member to the decoder, which refuses the object as it refuses a key
// written twice. The case rule of each member is the decoder's to apply, which
// is why the test is to read the body again into a new value with repeated names
// allowed and not to compare keys here: if that succeeds, repetition was the
// whole of the fault. Two keys that differ in case and reach a strict member are
// not a repetition, and the second is refused as a member the target lacks.
func repeatsAMemberByCase(body string, target any) bool {
	pointer := reflect.ValueOf(target)
	if pointer.Kind() != reflect.Pointer || pointer.IsNil() {
		return false
	}
	fresh := reflect.New(pointer.Type().Elem())
	options := jsonv2.JoinOptions(strictDecodeOptions, jsontext.AllowDuplicateNames(true))
	return jsonv2.Unmarshal([]byte(body), fresh.Interface(), options) == nil
}

// readAnyExact reads a JSON value into an `any` as encoding/json reads it for a
// Decoder that was told to use numbers: an object is a map[string]any, an
// array a []any, and a number is a json.Number holding the text the model wrote
// and not a float64 that has already forgotten some of it.
func readAnyExact(decoder *jsontext.Decoder, value *any) error {
	token, err := decoder.ReadToken()
	if err != nil {
		return err
	}
	switch token.Kind() {
	case 'n':
		*value = nil
	case 't':
		*value = true
	case 'f':
		*value = false
	case '"':
		*value = token.String()
	case '0':
		*value = json.Number(token.String())
	case '{':
		object := map[string]any{}
		for decoder.PeekKind() != '}' {
			name, err := decoder.ReadToken()
			if err != nil {
				return err
			}
			key := name.String()
			var member any
			if err := readAnyExact(decoder, &member); err != nil {
				return err
			}
			object[key] = member
		}
		if _, err := decoder.ReadToken(); err != nil {
			return err
		}
		*value = object
	case '[':
		list := []any{}
		for decoder.PeekKind() != ']' {
			var element any
			if err := readAnyExact(decoder, &element); err != nil {
				return err
			}
			list = append(list, element)
		}
		if _, err := decoder.ReadToken(); err != nil {
			return err
		}
		*value = list
	}
	return nil
}

// scanFrame is one open container of structureWithinLimits.
type scanFrame struct {
	// object is true for '{' and false for '['.
	object bool
	// expectKey is true when the next string of an object is a key.
	expectKey bool
	// count is the number of array elements completed so far.
	count int
}

// structureWithinLimits reports whether the body nests no deeper than the limit,
// holds no list with more items than the limit and no string value longer than
// the limit, in one pass over the bytes that allocates nothing. It does not read
// a body for whether it is JSON, which is the decoder's business, and it
// answers false for a body it cannot follow to the end; checkStructure then
// says what is wrong.
//
// A string is measured by the length of the text it decodes to, as it was when
// the answer was walked by tokens. That is never more than the bytes between the
// quotes when they are valid UTF-8, and up to three times as much when they are
// not, since each bad byte is read as the three bytes of U+FFFD. So a string
// that is no more than a third of the limit is cleared on its length, and one
// that is longer is decoded to be measured.
func structureWithinLimits(body string, limits DecodeLimits) bool {
	var frameSpace [16]scanFrame
	frames := frameSpace[:0]

	// completed records that a value has just ended in the innermost
	// container: an object goes back to expecting a key, an array counts the
	// element.
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
			frames = append(frames, scanFrame{object: body[i] == '{', expectKey: body[i] == '{'})

		case '}', ']':
			if len(frames) == 0 {
				return false
			}
			frames = frames[:len(frames)-1]
			if !completed() {
				return false
			}

		case '"':
			end, _ := endOfScannedString(body, i)
			text := body[i+1 : end]
			i = end

			if len(frames) > 0 {
				if top := &frames[len(frames)-1]; top.object && top.expectKey {
					top.expectKey = false
					continue
				}
			}

			// A string value. A key has no limit.
			if len(text) > limits.MaxStringBytes/3 && decodedStringLength(text) > limits.MaxStringBytes {
				return false
			}
			if !completed() {
				return false
			}

		default:
			// A number, true, false or null runs to the next delimiter.
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

// endOfScannedString is the position of the quote that closes the string that
// opens at start, and whether the string holds an escape. It reads text held as
// a string or as bytes.
func endOfScannedString[Text ~string | ~[]byte](body Text, start int) (end int, escaped bool) {
	for i := start + 1; i < len(body); i++ {
		switch body[i] {
		case '\\':
			escaped = true
			i++
		case '"':
			return i, escaped
		}
	}
	return len(body), escaped
}

func isScannedDelimiter(c byte) bool {
	switch c {
	case ',', ']', '}', ' ', '\t', '\r', '\n':
		return true
	}
	return false
}

// decodedStringLength is the length in bytes of the text a JSON string decodes
// to, given what lies between its quotes. The decoder reads an escape as the
// character it stands for, a surrogate pair as one character of four bytes, a
// surrogate that has no partner as U+FFFD, and each byte that is not valid
// UTF-8 as U+FFFD, which is three bytes. For text that is not a JSON string it
// gives the length of a reading, not an error: the decoder is the judge of that.
func decodedStringLength(text string) int {
	length := 0
	for i := 0; i < len(text); {
		c := text[i]
		switch {
		case c == '\\' && i+1 < len(text):
			if text[i+1] != 'u' {
				length++
				i += 2
				continue
			}
			first, ok := hexRune(text, i+2)
			if !ok {
				length++
				i += 2
				continue
			}
			i += 6
			if first >= 0xD800 && first < 0xDC00 && i+1 < len(text) && text[i] == '\\' && text[i+1] == 'u' {
				if second, ok := hexRune(text, i+2); ok && second >= 0xDC00 && second < 0xE000 {
					length += 4
					i += 6
					continue
				}
			}
			if first >= 0xD800 && first < 0xE000 {
				first = utf8.RuneError
			}
			length += utf8.RuneLen(first)
		case c < utf8.RuneSelf:
			length++
			i++
		default:
			r, size := utf8.DecodeRuneInString(text[i:])
			if r == utf8.RuneError && size == 1 {
				length += len(string(utf8.RuneError))
			} else {
				length += size
			}
			i += size
		}
	}
	return length
}

// hexRune reads the four hexadecimal digits that follow \u at text[at:].
func hexRune(text string, at int) (rune, bool) {
	if at+4 > len(text) {
		return 0, false
	}
	var r rune
	for _, c := range []byte(text[at : at+4]) {
		switch {
		case '0' <= c && c <= '9':
			r = r<<4 | rune(c-'0')
		case 'a' <= c && c <= 'f':
			r = r<<4 | rune(c-'a'+10)
		case 'A' <= c && c <= 'F':
			r = r<<4 | rune(c-'A'+10)
		default:
			return 0, false
		}
	}
	return r, true
}

// walkFrame is one open container while checkStructure reads a document.
type walkFrame struct {
	// object is true for '{' and false for '['.
	object bool
	// expectKey is true when the next string of an object is a key.
	expectKey bool
	// count is the number of array elements completed so far.
	count int
}

// checkStructure reads the answer token by token and reports the first thing
// wrong with it as text: that it is not JSON, that an object repeats a key, or
// that it is past a limit (nesting, the items of a list, the length of a string).
//
// The decoder refuses the first two on its own and checkStructure asks it, so
// that the three are found in the order the text has them. Which came first
// matters because the kinds differ: a repeated key is a schema violation that
// repair can name, and text that is not JSON is malformed output.
//
// Every value that ends, a scalar or a closing delimiter alike, is a completed
// value of the container below it. Treating a closing delimiter as something
// that only pops the stack made the parent lose its place: the key after a
// nested object was read as a value and the value after it as a key, so two
// equal strings that followed a nested container looked like a duplicate key
// and a real duplicate key went unseen.
func checkStructure(body string, limits DecodeLimits) error {
	decoder := jsontext.NewDecoder(strings.NewReader(body),
		jsontext.AllowDuplicateNames(false), jsontext.AllowInvalidUTF8(true))

	var stack []walkFrame

	// completed records that a value has just ended in the innermost
	// container: an object goes back to expecting a key, an array counts the
	// element. The decoder's pointer is, at that point, the pointer to the value
	// that ended, so an array is its parent.
	completed := func() error {
		if len(stack) == 0 {
			return nil
		}
		top := &stack[len(stack)-1]
		if top.object {
			top.expectKey = true
			return nil
		}
		top.count++
		if top.count > limits.MaxArrayItems {
			return &types.OperationError{
				Kind: types.KindMalformedOutput,
				Message: fmt.Sprintf("an array at %s has more than %d items",
					pointerText(decoder.StackPointer().Parent()), limits.MaxArrayItems),
			}
		}
		return nil
	}

	for {
		token, err := decoder.ReadToken()
		if err == io.EOF || (errors.Is(err, io.ErrUnexpectedEOF) && endsBetweenTokens(body[decoder.InputOffset():])) {
			// The text ends, and an end between two tokens is not a fault of
			// its structure: it is an answer cut off, which the decoder reports
			// when it reads it for the type, as a schema violation. An end in the
			// middle of a token is text that is not JSON.
			return nil
		}
		if err != nil {
			return structureFault(err)
		}

		switch token.Kind() {
		case '{', '[':
			if len(stack)+1 > limits.MaxDepth {
				return &types.OperationError{
					Kind: types.KindMalformedOutput,
					Message: fmt.Sprintf("the response nests deeper than %d levels at %s",
						limits.MaxDepth, pointerText(decoder.StackPointer())),
				}
			}
			stack = append(stack, walkFrame{object: token.Kind() == '{', expectKey: token.Kind() == '{'})
			continue

		case '}', ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}

		case '"':
			if len(stack) > 0 {
				if top := &stack[len(stack)-1]; top.object && top.expectKey {
					top.expectKey = false
					continue
				}
			}
			if length := len(token.String()); length > limits.MaxStringBytes {
				return &types.OperationError{
					Kind: types.KindMalformedOutput,
					Message: fmt.Sprintf("a string value at %s is %d bytes, above the %d-byte limit",
						pointerText(decoder.StackPointer()), length, limits.MaxStringBytes),
				}
			}
		}

		if err := completed(); err != nil {
			return err
		}
	}
}

// endsBetweenTokens reports whether what is left of the text after the last
// token read holds no part of another: nothing, or only the white space, commas
// and colons that stand between tokens.
func endsBetweenTokens(rest string) bool {
	return strings.Trim(rest, " \t\r\n,:") == ""
}

// structureFault classifies what the token reader reported. A repeated key is
// the one fault in the text that is a schema violation, and the reader says so
// with a sentinel and the pointer to the key; anything else is text that is not
// JSON. The error itself is dropped: for a repeated key it quotes the key and
// its place, which are rebuilt here from the pointer in the library's words.
func structureFault(err error) error {
	var syntactic *jsontext.SyntacticError
	if errors.Is(err, jsontext.ErrDuplicateName) && errors.As(err, &syntactic) {
		return &types.OperationError{
			Kind: types.KindSchemaViolation,
			Message: fmt.Sprintf("the response repeats the key %q at %s; only the last would have been used",
				syntactic.JSONPointer.LastToken(), pointerText(syntactic.JSONPointer.Parent())),
		}
	}
	return &types.OperationError{
		Kind:    types.KindMalformedOutput,
		Message: "the response is not well-formed JSON",
	}
}

// pointerText renders a JSON pointer for a message, the root as "the top level".
func pointerText(pointer jsontext.Pointer) string {
	if pointer == "" {
		return "the top level"
	}
	return string(pointer)
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
		//
		// Naming Strict() is the point of the second clause. Rejecting an
		// unrecognised property is opt-in -- nothing else in this library does
		// it -- but the error arrives looking like a decoding fact rather than
		// a consequence of a mode the caller chose, and `Strict` reads as "be
		// careful" rather than as "reject anything I did not name".
		//
		// The expensive version is a batch: a model that answers the question
		// AND volunteers an extra key fails the whole call, so one unrequested
		// field discards every good answer beside it.
		return "the response carried " + strings.TrimPrefix(err.Error(), "json: ") +
			"; Strict() is what rejects it, so drop Strict if this operation's contract permits a field it did not ask for"
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
