package ops

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/monstercameron/schemaflux/internal/types"
)

// ParseJSON parses a JSON string into a target struct, handling common LLM output issues
// like markdown code blocks and whitespace.
func ParseJSON(input string, target any) error {
	// Clean up the input
	cleaned := cleanJSON(input)

	// Try to unmarshal
	err := json.Unmarshal([]byte(cleaned), target)
	if err != nil {
		// Deliberately no payload in the error. This error is logged by every
		// caller, and the payload is the caller's data -- often the very record
		// being extracted or redacted -- so embedding it copies user content
		// into every log aggregator the operator runs. Report the shape instead:
		// enough to diagnose, nothing to leak.
		// Classified, so a caller can branch on the kind instead of matching
		// this sentence -- which is the whole point of A-007, and which this
		// message was previously the only route to.
		return &types.OperationError{
			Kind:  types.KindMalformedOutput,
			Cause: err,
			Message: fmt.Sprintf("the response could not be parsed as JSON (%d bytes, shape: %s)",
				len(cleaned), describeShape(cleaned)),
		}
	}

	return nil
}

// jsonNames are the names of the members encoding/json reads into a struct, each
// with the rule by which a key is matched to it.
type jsonNames map[string]jsonCaseRule

// matches reports whether encoding/json would put the value of a key into one of
// the members: a key that is a member's name as it is written is, and otherwise
// a key that differs from a name only in case is, except for a member tagged
// case:strict, which only its own spelling matches.
func (names jsonNames) matches(key string) bool {
	if _, written := names[key]; written {
		return true
	}
	for name, rule := range names {
		if rule != jsonCaseStrict && strings.EqualFold(name, key) {
			return true
		}
	}
	return false
}

// jsonKeyNames returns the names of the members encoding/json reads into a
// struct (jsonMembers), as they are written, each with its rule of case. It
// returns nil for anything that is not a struct.
func jsonKeyNames(targetType reflect.Type) jsonNames {
	for targetType != nil && (targetType.Kind() == reflect.Ptr || targetType.Kind() == reflect.Interface) {
		if targetType.Kind() == reflect.Interface {
			return nil
		}
		targetType = targetType.Elem()
	}
	if targetType == nil || targetType.Kind() != reflect.Struct {
		return nil
	}

	names := jsonNames{}
	for _, member := range jsonMembers(targetType) {
		if answerMemberIsReadable(member) {
			names[member.name] = parseJSONTagOptions(member.field.Tag.Get("json")).caseRule
		}
	}
	return names
}

// describeShape classifies a response without reproducing any of it. A prefix
// was the obvious choice and the wrong one: the first 24 characters of a JSON
// body are usually the first field and its value, so a payload beginning
// {"ssn": "123-45-6789" leaks the value it was meant to summarise. The
// classification below distinguishes the cases an operator actually needs to
// tell apart -- an error page, prose, a truncated object -- and carries no
// caller data at all.
func describeShape(s string) string {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return "empty"
	case strings.HasPrefix(s, "```"):
		return "fenced block"
	case strings.HasPrefix(s, "{"):
		return "json object"
	case strings.HasPrefix(s, "["):
		return "json array"
	case strings.HasPrefix(s, "<"):
		return "markup, likely an error page"
	default:
		return "prose"
	}
}

// cleanJSON is the one extraction path. It used to strip a leading fence and
// nothing else; see extractJSON in jsonextract.go for what it does now and why.
func cleanJSON(input string) string {
	return extractJSON(input)
}

// promptJSON writes a value as JSON for a prompt.
//
// It differs from json.Marshal in one way: "<", ">" and "&" are left as they
// are. encoding/json escapes them so that JSON can be pasted into HTML, which a
// prompt is not, and the escape cost both ways: "R&D" reached the model as six
// characters of escape sequence, several tokens where one would do, and the
// model was shown something other than what the caller wrote.
func promptJSON(value any) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	// Encode ends its output with a newline.
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}
